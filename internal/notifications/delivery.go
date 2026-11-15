package notifications

import (
	"context"
	"database/sql"
	"errors"
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

// keepDeliveries is how long the hub keeps the deliveries of a test, or of a problem that
// resolved. The notifications themselves stay.
const keepDeliveries = 30 * 24 * time.Hour

// testRecordTimeout bounds recording how a test went, after its request may have ended.
const testRecordTimeout = 10 * time.Second

// testLost is how long a test's delivery may stay pending before the hub counts it as lost: the
// hub stopped while it ran.
const testLost = time.Minute

// backoff is the wait after the attempts-th failure in a row.
func backoff(attempts int) time.Duration {
	if attempts < 1 {
		return 0
	}
	return retryAfter[min(attempts, len(retryAfter))-1]
}

// sameProblem matches notifications o and n of one problem: the same base event, machine,
// service and channel. The notifications_problem index serves it.
const sameProblem = `o.base = n.base AND coalesce(o.machine_id, 0) = coalesce(n.machine_id, 0)
	AND coalesce(o.service, '') = coalesce(n.service, '') AND coalesce(o.channel, '') = coalesce(n.channel, '')`

const deliveryColumns = `d.id, d.notification_id, c.name, d.status, d.attempts, d.error, d.created_at, d.updated_at`

func scanDelivery(row scanner) (Delivery, error) {
	var d Delivery
	var created, updated int64
	err := row.Scan(&d.ID, &d.NotificationID, &d.Channel, &d.Status, &d.Attempts, &d.Error, &created, &updated)
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

func placeholders(n int) string { return "?" + strings.Repeat(", ?", n-1) }

// withDeliveries fills in the deliveries of each notification in ns.
func (s *Store) withDeliveries(ctx context.Context, ns []Notification) error {
	if len(ns) == 0 {
		return nil
	}
	ids := make([]any, len(ns))
	at := map[int64]int{}
	for i, n := range ns {
		ids[i] = n.ID
		at[n.ID] = i
		ns[i].Deliveries = []Delivery{}
	}
	ds, err := s.deliveries(ctx, `WHERE d.notification_id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return err
	}
	for _, d := range ds {
		i := at[d.NotificationID]
		ns[i].Deliveries = append(ns[i].Deliveries, d)
	}
	return nil
}

// queue adds a pending delivery of notification id to each channel in channelIDs, and returns
// the deliveries' ids.
func queue(ctx context.Context, tx *sql.Tx, id int64, channelIDs []int64, now time.Time) ([]int64, error) {
	out := make([]int64, len(channelIDs))
	for i, c := range channelIDs {
		if err := tx.QueryRowContext(ctx, `INSERT INTO notification_deliveries
			(notification_id, channel_id, status, attempts, created_at, updated_at) VALUES (?, ?, 'pending', 0, ?, ?) RETURNING id`,
			id, c, now.Unix(), now.Unix()).Scan(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// refresh sets a notification's status from its deliveries: sent once one is delivered, for
// good, pending while one is still tried, and failed once every one failed. One with no deliveries stays
// pending while it is the latest of an open problem, for a channel added later, and is failed
// after: nobody got it.
func refresh(ctx context.Context, tx *sql.Tx, id int64, now time.Time) error {
	var delivered, waiting, total int
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(status = 'delivered'), 0), coalesce(sum(status = 'pending'), 0), count(*)
		FROM notification_deliveries WHERE notification_id = ?`, id).Scan(&delivered, &waiting, &total); err != nil {
		return err
	}
	status := StatusFailed
	switch {
	case delivered > 0:
		status = StatusSent
	case waiting > 0:
		status = StatusPending
	case total == 0:
		var open bool
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM notifications n WHERE n.id = ? AND `+latestOpen, id).Scan(&open); err != nil {
			return err
		}
		if open {
			status = StatusPending
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE notifications SET status = ?, sent_at = CASE WHEN ? = 'sent' THEN coalesce(sent_at, ?) END, updated_at = ?
		WHERE id = ? AND status NOT IN (?, 'sent')`, status, status, now.Unix(), now.Unix(), id, status)
	return err
}

// problem lists the notifications of n's problem since it last resolved, n included, oldest first.
func problem(ctx context.Context, tx *sql.Tx, n Notification) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.id FROM notifications n JOIN notifications o ON `+sameProblem+`
		WHERE n.id = ? AND o.id <= n.id AND o.id > coalesce((SELECT max(o.id) FROM notifications o
		  WHERE `+sameProblem+` AND o.id < n.id AND o.event LIKE '%.resolved'), 0)
		ORDER BY o.id`, n.ID)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
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

func anys(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// deliveredTo lists the channels that got one of the notifications ids.
func deliveredTo(ctx context.Context, tx *sql.Tx, ids []int64) (map[int64]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT channel_id FROM notification_deliveries
		WHERE status = 'delivered' AND notification_id IN (`+placeholders(len(ids))+`)`, anys(ids)...)
	if err != nil {
		return nil, err
	}
	got, err := scanIDs(rows)
	out := map[int64]bool{}
	for _, id := range got {
		out[id] = true
	}
	return out, err
}

// supersede fails the pending deliveries of the notifications ids: something newer replaces them.
func supersede(ctx context.Context, tx *sql.Tx, ids []int64, why string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'failed', error = ?, updated_at = ?
		WHERE status = 'pending' AND notification_id IN (`+placeholders(len(ids))+`)`, append([]any{why, now.Unix()}, anys(ids)...)...)
	return err
}

// pending is one delivery the hub still owes a channel, with its notification.
type pending struct {
	id        int64
	attempts  int
	createdAt time.Time
	updatedAt time.Time
	n         Notification
}

// pendingOf lists a channel's pending deliveries, oldest first, but for tests, which SendTest
// finishes itself.
func (s *Store) pendingOf(ctx context.Context, channelID int64) ([]pending, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT d.id, d.attempts, d.created_at, d.updated_at,
		n.id, m.name, n.service, n.channel, n.event, n.severity, n.message, n.status, n.sent_at, n.created_at, n.updated_at
		FROM notification_deliveries d JOIN notifications n ON n.id = d.notification_id LEFT JOIN machines m ON m.id = n.machine_id
		WHERE d.channel_id = ? AND d.status = 'pending' AND n.event != ? ORDER BY d.id`, channelID, EventTest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pending
	for rows.Next() {
		var p pending
		var created, updated int64
		n, err := scan(scanFunc(func(dest ...any) error {
			return rows.Scan(append([]any{&p.id, &p.attempts, &created, &updated}, dest...)...)
		}))
		if err != nil {
			return nil, err
		}
		p.createdAt, p.updatedAt, p.n = time.Unix(created, 0), time.Unix(updated, 0), n
		out = append(out, p)
	}
	return out, rows.Err()
}

type scanFunc func(dest ...any) error

func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

// channelsOwed lists the enabled channels that have pending deliveries, but for tests.
func (s *Store) channelsOwed(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT DISTINCT d.channel_id FROM notification_deliveries d
		JOIN notification_channels c ON c.id = d.channel_id JOIN notifications n ON n.id = d.notification_id
		WHERE d.status = 'pending' AND c.enabled = 1 AND n.event != ? ORDER BY d.channel_id`, EventTest)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// try is how one try to deliver went.
type try struct {
	delivery, notification, channel int64
	status                          string // delivered, failed, or pending for another try
	attempts                        int
	err                             error // nil when delivered
}

// attempted records a try, in one transaction with the notification's status and the channel's
// record of failing. It changes nothing when the delivery is no longer pending, because the
// problem resolved or the channel was turned off while it ran, and says so. A try that couldn't
// be made, because email is off, doesn't count as the channel failing.
func (s *Store) attempted(ctx context.Context, t try, now time.Time) (bool, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var why *string
	if t.err != nil {
		why = ptrTo(t.err.Error())
	}
	res, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = ?, attempts = ?, error = ?, updated_at = ?
		WHERE id = ? AND status = 'pending'`, t.status, t.attempts, why, now.Unix(), t.delivery)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if err := refresh(ctx, tx, t.notification, now); err != nil {
		return false, err
	}
	switch {
	case t.err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET failing_since = NULL, last_error = NULL WHERE id = ?`, t.channel)
	case !errors.Is(t.err, errEmailOff):
		_, err = tx.ExecContext(ctx, `UPDATE notification_channels SET failing_since = coalesce(failing_since, ?), last_error = ? WHERE id = ?`,
			now.Unix(), *why, t.channel)
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// failingChannel is an enabled channel whose real tries have failed since a time, with the last error.
type failingChannel struct {
	name, typ string
	since     time.Time
	lastError string
}

// failing lists the enabled channels whose every real try has failed since their last success,
// or since they were turned on, by name.
func (s *Store) failing(ctx context.Context) (map[string]failingChannel, error) {
	rows, err := s.db.Read.QueryContext(ctx, `SELECT name, type, failing_since, coalesce(last_error, '')
		FROM notification_channels WHERE enabled = 1 AND failing_since IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]failingChannel{}
	for rows.Next() {
		var f failingChannel
		var since int64
		if err := rows.Scan(&f.name, &f.typ, &since, &f.lastError); err != nil {
			return nil, err
		}
		f.since = time.Unix(since, 0).UTC()
		out[f.name] = f
	}
	return out, rows.Err()
}

// prune fails the deliveries of tests the hub lost track of, and deletes the deliveries of
// tests and of resolved problems older than keepDeliveries.
func (s *Store) prune(ctx context.Context, now time.Time) error {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT d.notification_id FROM notification_deliveries d JOIN notifications n ON n.id = d.notification_id
		WHERE d.status = 'pending' AND n.event = ? AND d.created_at < ?`, EventTest, now.Add(-testLost).Unix())
	if err != nil {
		return err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if err := supersede(ctx, tx, ids, "The hub stopped before the test finished.", now); err != nil {
			return err
		}
		for _, id := range ids {
			if err := refresh(ctx, tx, id, now); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE status != 'pending' AND notification_id IN (
		SELECT n.id FROM notifications n WHERE n.created_at < ? AND (n.event = ? OR EXISTS (
		  SELECT 1 FROM notifications o WHERE `+sameProblem+` AND o.id >= n.id AND o.event LIKE '%.resolved')))`,
		now.Add(-keepDeliveries).Unix(), EventTest); err != nil {
		return err
	}
	return tx.Commit()
}

// event is the contract's event about n.
func event(n Notification, hubURL string) channels.Event {
	return channels.Event{Version: channels.Version, Event: n.Event, Hub: hubURL, Notification: channels.Notification{
		ID: n.ID, Event: n.Event, Severity: n.Severity, Machine: n.Machine, Service: n.Service, Channel: n.Channel,
		Message: n.Message, CreatedAt: n.CreatedAt,
	}}
}

// SendTest sends a test notification to one channel, or to every enabled channel when name is
// empty, once each and all at once, and returns how each went. It is one notification, whose
// event is notification.test, with one delivery per channel. A test that gets through also
// resolves the channel's channel.failing problem. How each went is recorded even when the
// request ends first.
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
	ids := make([]int64, len(targets))
	for i, c := range targets {
		ids[i] = c.ID
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return server.List[Delivery]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := add(ctx, tx, 0, "", "", EventTest, SeverityInfo,
		"This is a test from the Tender hub at "+s.hub.URL+". If you can read it, notifications reach this channel.", now)
	if err != nil {
		return server.List[Delivery]{}, err
	}
	dids, err := queue(ctx, tx, n.ID, ids, now)
	if err != nil {
		return server.List[Delivery]{}, err
	}
	if err := tx.Commit(); err != nil {
		return server.List[Delivery]{}, err
	}
	ev := event(n, s.hub.URL)
	var wg sync.WaitGroup
	errs := make([]error, len(targets))
	for i, c := range targets {
		wg.Go(func() {
			t := try{delivery: dids[i], notification: n.ID, channel: c.ID, status: DeliveryDelivered, attempts: 1}
			if t.err = s.adapters[c.Type].Send(ctx, c.config, ev); t.err != nil {
				t.status = DeliveryFailed
			}
			rec, cancel := context.WithTimeout(context.Background(), testRecordTimeout)
			defer cancel()
			_, errs[i] = s.attempted(rec, t, time.Now())
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return server.List[Delivery]{}, err
	}
	ds, err := s.deliveries(ctx, `WHERE d.id IN (`+placeholders(len(dids))+`)`, anys(dids)...)
	return server.List[Delivery]{Data: ds}, err
}

func ptrTo[T any](v T) *T { return &v }

// describe says what a failing channel's notification says.
func (f failingChannel) describe(now time.Time) string {
	last := f.lastError
	if r := []rune(last); len(r) > 300 {
		last = string(r[:297]) + "..."
	}
	return fmt.Sprintf("The %s channel %s has failed to deliver notifications since %s (%s ago). Its last error: %s",
		f.typ, f.name, formatTime(f.since), formatDuration(now.Sub(f.since)), last)
}
