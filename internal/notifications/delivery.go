package notifications

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// The waits before each next try of a delivery that failed: 1, 2, 5, 10 and 30 minutes, then
// every 30 minutes, until giveUpAfter.
var retryAfter = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// giveUpAfter is how long a delivery is tried before it fails for good.
const giveUpAfter = 24 * time.Hour

// testKeep is how long the hub keeps the deliveries of tests.
const testKeep = 30 * 24 * time.Hour

// backoff is the wait after the attempts-th failure in a row.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		return 0
	}
	return retryAfter[min(attempts, len(retryAfter))-1]
}

const deliveryColumns = `d.id, d.notification_id, c.name, d.event, d.status, d.attempts, d.error, d.created_at, d.updated_at`

func scanDelivery(row scanner) (Delivery, error) {
	var d Delivery
	var created, updated int64
	err := row.Scan(&d.ID, &d.NotificationID, &d.Channel, &d.Event, &d.Status, &d.Attempts, &d.Error, &created, &updated)
	d.CreatedAt, d.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return d, err
}

func (s *Store) deliveries(ctx context.Context, where string, args ...any) ([]Delivery, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT `+deliveryColumns+`
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id `+where+` ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// withDeliveries fills in the deliveries of each notification in ns.
func (s *Store) withDeliveries(ctx context.Context, ns []Notification) error {
	if len(ns) == 0 {
		return nil
	}
	ids := make([]any, len(ns))
	for i, n := range ns {
		ids[i] = n.ID
		ns[i].Deliveries = []Delivery{}
	}
	ds, err := s.deliveries(ctx, `WHERE d.notification_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, ids...)
	if err != nil {
		return err
	}
	at := map[int64]int{}
	for i, n := range ns {
		at[n.ID] = i
	}
	for _, d := range ds {
		i := at[*d.NotificationID]
		ns[i].Deliveries = append(ns[i].Deliveries, d)
	}
	return nil
}

// queue adds a pending delivery of event to each channel in ids.
func (s *Store) queue(ctx context.Context, notificationID int64, ids []int64, event string, now time.Time) error {
	for _, id := range ids {
		if _, err := s.db.Write.ExecContext(ctx, `INSERT INTO notification_deliveries
			(notification_id, channel_id, event, status, attempts, created_at, updated_at) VALUES (?, ?, ?, 'pending', 0, ?, ?)`,
			notificationID, id, event, now.Unix(), now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

// hasDeliveries says whether the notification already has deliveries of event.
func (s *Store) hasDeliveries(ctx context.Context, notificationID int64, event string) (bool, error) {
	var n int
	err := s.db.Write.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries WHERE notification_id = ? AND event = ?`,
		notificationID, event).Scan(&n)
	return n > 0, err
}

// deliveredTo lists the channels that got some event of the notification.
func (s *Store) deliveredTo(ctx context.Context, notificationID int64) (map[int64]bool, error) {
	rows, err := s.db.Write.QueryContext(ctx, `SELECT DISTINCT channel_id FROM notification_deliveries WHERE notification_id = ? AND status = 'delivered'`, notificationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// supersede fails the notification's pending deliveries of event, or of every event when event is
// empty: something newer replaces them.
func (s *Store) supersede(ctx context.Context, notificationID int64, event, why string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'failed', error = ?, updated_at = ?
		WHERE notification_id = ? AND status = 'pending' AND (? = '' OR event = ?)`, why, now.Unix(), notificationID, event, event)
	return err
}

// pending is one delivery the hub still owes a channel.
type pending struct {
	id             int64
	notificationID *int64
	event          string
	attempts       int
	createdAt      time.Time
	updatedAt      time.Time
}

func (s *Store) pendingOf(ctx context.Context, channelID int64) ([]pending, error) {
	rows, err := s.db.Write.QueryContext(ctx, `SELECT id, notification_id, event, attempts, created_at, updated_at
		FROM notification_deliveries WHERE channel_id = ? AND status = 'pending' ORDER BY id`, channelID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pending
	for rows.Next() {
		var p pending
		var created, updated int64
		if err := rows.Scan(&p.id, &p.notificationID, &p.event, &p.attempts, &created, &updated); err != nil {
			return nil, err
		}
		p.createdAt, p.updatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

// channelsOwed lists the enabled channels that have pending deliveries.
func (s *Store) channelsOwed(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Write.QueryContext(ctx, `SELECT DISTINCT d.channel_id FROM notification_deliveries d
		JOIN notification_channels c ON c.id = d.channel_id WHERE d.status = 'pending' AND c.enabled = 1 ORDER BY d.channel_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// attempted records how a try went: delivered, failed, or pending for another try.
func (s *Store) attempted(ctx context.Context, id int64, status string, attempts int, why *string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notification_deliveries SET status = ?, attempts = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, attempts, why, now.Unix(), id)
	return err
}

// settle marks a pending notification sent once its owed event went out: it was delivered to a
// channel, or every channel's delivery of it ended. An escalation that went out is noted, so a
// severity that flaps doesn't send another.
func (s *Store) settle(ctx context.Context, id int64, now time.Time) error {
	n, err := scan(s.db.Write.QueryRowContext(ctx, selectNotifications+` WHERE n.id = ?`, id))
	if err != nil || n.Status != StatusPending {
		return err
	}
	event := owed(n)
	var total, delivered, waiting int
	err = s.db.Write.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(status = 'delivered'), 0), coalesce(sum(status = 'pending'), 0)
		FROM notification_deliveries WHERE notification_id = ? AND event = ?`, id, event).Scan(&total, &delivered, &waiting)
	switch {
	case err != nil:
		return err
	case total == 0 || (delivered == 0 && waiting > 0):
		return nil
	case event == channels.EventEscalated:
		return s.escalated(ctx, id, now)
	}
	return s.setStatus(ctx, id, StatusSent, now)
}

// owed is the event a pending notification owes: opened until it first went out, then escalated.
func owed(n Notification) string {
	if n.UpdatedAt.After(n.CreatedAt) {
		return channels.EventEscalated
	}
	return channels.EventOpened
}

// failingChannel is a channel whose deliveries have failed since a time, with the last error.
type failingChannel struct {
	name, typ string
	since     time.Time
	lastError string
}

// failing lists the enabled channels with deliveries that failed since the channel's last
// delivered one, by channel name, with the first such delivery's time.
func (s *Store) failing(ctx context.Context) (map[string]failingChannel, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT c.name, c.type, d.created_at, d.error
		FROM notification_deliveries d JOIN notification_channels c ON c.id = d.channel_id
		WHERE c.enabled = 1 AND d.status != 'delivered' AND d.attempts > 0 AND d.error IS NOT NULL
		  AND d.id > coalesce((SELECT max(o.id) FROM notification_deliveries o WHERE o.channel_id = d.channel_id AND o.status = 'delivered'), 0)
		ORDER BY d.updated_at, d.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]failingChannel{}
	for rows.Next() {
		var f failingChannel
		var created int64
		if err := rows.Scan(&f.name, &f.typ, &created, &f.lastError); err != nil {
			return nil, err
		}
		f.since = time.Unix(created, 0).UTC()
		if old, ok := out[f.name]; ok && old.since.Before(f.since) {
			f.since = old.since
		}
		out[f.name] = f
	}
	return out, rows.Err()
}

// pruneTests deletes the deliveries of tests older than testKeep.
func (s *Store) pruneTests(ctx context.Context, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE notification_id IS NULL AND created_at < ?`,
		now.Add(-testKeep).Unix())
	return err
}

// event is the contract's event about n.
func event(n Notification, kind, hubURL string) channels.Event {
	ev := channels.Event{Version: channels.Version, Event: kind, Hub: hubURL, Alert: channels.Alert{
		ID: n.ID, Type: n.Type, Severity: n.Severity, Machine: n.Machine, Service: n.Service, Message: n.Content, CreatedAt: n.CreatedAt,
	}}
	if n.Status == StatusResolved {
		at := n.UpdatedAt
		ev.Alert.ResolvedAt = &at
	}
	return ev
}

// SendTest sends the test event to one channel, or to every enabled channel when name is empty,
// once each and all at once, and returns how each went. Each try is kept as a delivery, so a
// test that gets through also resolves the channel's channel_failing alert.
func (s *Store) SendTest(ctx context.Context, name string) (server.List[Delivery], error) {
	var targets []stored
	if name != "" {
		c, err := s.channel(ctx, s.db.Read, true, name)
		if err != nil {
			return server.List[Delivery]{}, err
		}
		targets = []stored{c}
	} else {
		cs, err := s.loadChannels(ctx, s.db.Read, true, `WHERE enabled = 1`)
		if err != nil {
			return server.List[Delivery]{}, err
		}
		targets = cs
	}
	if len(targets) == 0 {
		return server.List[Delivery]{}, server.Invalid("There are no enabled channels to test. Add one first, such as an email channel to ops@example.com.")
	}
	now := time.Now()
	ev := channels.Event{Version: channels.Version, Event: channels.EventTest, Hub: s.hub.URL, Alert: channels.Alert{
		Type: channels.EventTest, Severity: SeverityInfo, CreatedAt: now.UTC().Truncate(time.Second),
		Message: "This is a test from the Tender hub at " + s.hub.URL + ". If you can read it, alerts reach this channel.",
	}}
	ids := make([]any, len(targets))
	for i, c := range targets {
		err := s.db.Write.QueryRowContext(ctx, `INSERT INTO notification_deliveries (channel_id, event, status, attempts, created_at, updated_at)
			VALUES (?, 'test', 'pending', 0, ?, ?) RETURNING id`, c.ID, now.Unix(), now.Unix()).Scan(&ids[i])
		if err != nil {
			return server.List[Delivery]{}, err
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, c := range targets {
		wg.Go(func() {
			status, why := DeliveryDelivered, (*string)(nil)
			if err := s.adapters[c.Type].Send(ctx, c.config, ev); err != nil {
				status, why = DeliveryFailed, ptrTo(err.Error())
			}
			errs[i] = s.attempted(ctx, ids[i].(int64), status, 1, why, time.Now())
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return server.List[Delivery]{}, err
		}
	}
	ds, err := s.deliveries(ctx, `WHERE d.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, ids...)
	return server.List[Delivery]{Data: ds}, err
}

func ptrTo[T any](v T) *T { return &v }

// describe says what a failing channel's alert says.
func (f failingChannel) describe(now time.Time) string {
	last := f.lastError
	if r := []rune(last); len(r) > 300 {
		last = string(r[:297]) + "..."
	}
	return fmt.Sprintf("The %s channel %s has failed to deliver alerts since %s (%s ago). Its last error: %s",
		f.typ, f.name, formatTime(f.since), formatDuration(now.Sub(f.since)), last)
}
