package notifications

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
)

// Sender delivers an email to bare addresses. A nil Sender means email is off.
type Sender interface {
	Send(ctx context.Context, to []string, msg Message) error
}

// Engine runs the rules and delivers their notifications to the channels.
//
// A notification is one message, added once. A problem adds one when it starts, such as
// service.down, one when its severity first goes up, service.down.escalated, and one when it
// clears, service.down.resolved, which goes only to the channels that got one of the others.
// Each notification becomes one delivery per channel that gets it, tried and retried on its own,
// so one failing channel never delays or repeats another. Every change of state is one
// transaction, and a try is recorded only while its delivery is still pending, so a delivery
// that finishes after its problem resolved, or its channel was turned off, changes nothing.
type Engine struct {
	store  *Store // whose channels can send
	sender Sender
	log    *slog.Logger

	// FailingAfter is how long a channel's deliveries fail before channel.failing fires: an hour.
	FailingAfter time.Duration

	silent bool // the "nobody gets notifications" line was logged and nothing has changed since

	mu   sync.Mutex
	busy map[int64]bool // the channels being delivered to now
}

// deliverEvery is how often the engine tries the deliveries that are due, between rule runs.
const deliverEvery = 15 * time.Second

// NewEngine returns an engine whose email channels send through sender, nil when email is off.
// hub names the hub in what it sends.
func NewEngine(s *Store, sender Sender, hub Hub, log *slog.Logger) *Engine {
	return &Engine{store: s.sending(sender, hub), sender: sender, log: log, FailingAfter: time.Hour, busy: map[int64]bool{}}
}

// Run evaluates the rules now and then every minute, and delivers what is due every 15 seconds,
// until ctx ends. load returns the machines' current state. Each channel is delivered to on its
// own goroutine, so a slow one holds up neither the rules nor the others.
func (e *Engine) Run(ctx context.Context, load func(context.Context) ([]Machine, error)) {
	rules := time.NewTicker(time.Minute)
	defer rules.Stop()
	deliveries := time.NewTicker(deliverEvery)
	defer deliveries.Stop()
	for {
		ms, err := load(ctx)
		if err == nil {
			err = e.evaluate(ctx, time.Now(), ms)
		}
		if err != nil && ctx.Err() == nil {
			e.log.Error("run notification rules", "error", err)
		}
		for again := true; again; {
			e.deliver(ctx, time.Now, false)
			select {
			case <-ctx.Done():
				return
			case <-rules.C:
				again = false
			case <-deliveries.C:
			}
		}
	}
}

// Tick adds the notifications the rules call for as of now, and delivers what is due, waiting
// for every channel. Without an enabled channel, notifications are still recorded and stay
// pending, and the first tick that finds none says so in the log. It returns database errors; a
// failed delivery is logged and retried on a later tick.
func (e *Engine) Tick(ctx context.Context, now time.Time, ms []Machine) error {
	if err := e.evaluate(ctx, now, ms); err != nil {
		return err
	}
	e.deliver(ctx, func() time.Time { return now }, true)
	return nil
}

// usable lists the channels that can get notifications now: enabled, of a type this hub knows,
// and for email, with email on.
func (e *Engine) usable(ctx context.Context) ([]stored, error) {
	cs, err := e.store.loadChannels(ctx, e.store.db.Read, false, `WHERE enabled = 1`)
	out := cs[:0]
	for _, c := range cs {
		if e.store.adapters[c.Type] != nil && (c.Type != TypeEmail || e.sender != nil) {
			out = append(out, c)
		}
	}
	return out, err
}

// gets says whether channel c gets notification n: its events take n's event, and n is at least
// as severe as its min_severity. A channel.failing notification goes to every channel but the
// failing one, whatever its min_severity. A test goes to every channel it is sent to.
func gets(c stored, n Notification) bool {
	switch {
	case n.Event == EventTest:
		return true
	case !matches(c.Events, n.Event):
		return false
	case channels.Base(n.Event) == EventChannelFailing:
		return n.Channel == nil || *n.Channel != c.Name
	}
	return severityRank(c.MinSeverity) <= severityRank(n.Severity)
}

// evaluate runs the rules as of now and adds the notifications they call for.
func (e *Engine) evaluate(ctx context.Context, now time.Time, ms []Machine) error {
	usable, err := e.usable(ctx)
	if err != nil {
		return err
	}
	if len(usable) == 0 {
		if !e.silent {
			e.log.Info("nobody gets notifications: there is no enabled channel, or only email ones while email is off, so notifications are only recorded. " +
				"Add a channel with \"tender channels add\" to get them")
			e.silent = true
		}
	} else {
		e.silent = false
	}
	if err := e.store.prune(ctx, now); err != nil {
		return err
	}
	open, err := e.store.open(ctx)
	if err != nil {
		return err
	}
	holds := map[key]bool{}
	changed := map[key]bool{}
	silent := map[string]bool{}
	byName := map[string]Machine{}
	found := func(k key, machineID int64, f finding) error {
		holds[k] = true
		n, ok := open[k]
		switch {
		case !ok:
			changed[k] = true
			n, err := e.store.raise(ctx, machineID, k, f, usable, now)
			if err == nil {
				e.log.Info("notification added", "id", n.ID, "event", n.Event, "about", k.about(), "service", f.service, "container", f.container, "severity", f.severity)
			}
			return err
		case severityRank(f.severity) > severityRank(n.Severity):
			changed[k] = true
			n, err := e.store.follow(ctx, n, k.base+"."+channels.PhaseEscalated, f.severity, f.content, usable, now)
			if err == nil {
				e.log.Info("notification added", "id", n.ID, "event", n.Event, "about", k.about(), "severity", f.severity)
			}
			return err
		}
		return nil
	}
	for _, m := range ms {
		byName[m.Name] = m
		for _, f := range evaluate(m, now) {
			silent[m.Name] = silent[m.Name] || f.event == EventMachineSilent
			if err := found(key{f.event, m.Name, f.service, f.container, ""}, m.ID, f); err != nil {
				return err
			}
		}
	}
	failing, err := e.store.failing(ctx)
	if err != nil {
		return err
	}
	for name, fc := range failing {
		if now.Sub(fc.since) < e.FailingAfter {
			continue
		}
		f := finding{event: EventChannelFailing, severity: SeverityWarning, content: fc.describe(now)}
		if err := found(key{base: EventChannelFailing, channel: name}, 0, f); err != nil {
			return err
		}
	}

	for _, n := range sortedByID(open) {
		k := keyOf(n)
		switch {
		case changed[k]:
		case !holds[k] && !byName[k.machine].keeps(n, silent[k.machine], now):
			r, err := e.store.resolve(ctx, n, usable, now)
			if err != nil {
				return err
			}
			e.log.Info("notification added", "id", r.ID, "event", r.Event, "about", k.about())
		case n.Status == StatusPending:
			if err := e.store.late(ctx, n, usable, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// targets lists the channels in usable that get n.
func targets(usable []stored, n Notification) []int64 {
	var ids []int64
	for _, c := range usable {
		if gets(c, n) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// raise adds the notification a new problem starts with, queued to every channel that gets it.
func (s *Store) raise(ctx context.Context, machineID int64, k key, f finding, usable []stored, now time.Time) (Notification, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Notification{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := add(ctx, tx, machineID, k.service, k.container, k.channel, f.event, f.severity, f.content, now)
	if err != nil {
		return n, err
	}
	if _, err := queue(ctx, tx, n.ID, targets(usable, n), now); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// follow adds a notification of event to prev's problem, queued to every channel that gets it.
func (s *Store) follow(ctx context.Context, prev Notification, event, severity, message string, usable []stored, now time.Time) (Notification, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Notification{}, err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := next(ctx, tx, prev, event, severity, message, now)
	if err != nil {
		return n, err
	}
	if _, err := queue(ctx, tx, n.ID, targets(usable, n), now); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// next adds a pending notification of event about what prev is about.
func next(ctx context.Context, tx *sql.Tx, prev Notification, event, severity, message string, now time.Time) (Notification, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO notifications (machine_id, service, container, channel, event, severity, message, status, created_at, updated_at)
		SELECT machine_id, service, container, channel, ?, ?, ?, 'pending', ?, ? FROM notifications WHERE id = ? RETURNING id`,
		event, severity, message, now.Unix(), now.Unix(), prev.ID).Scan(&id)
	if err != nil {
		return Notification{}, err
	}
	return scan(tx.QueryRowContext(ctx, selectNotifications+` WHERE n.id = ?`, id))
}

// resolve adds the .resolved notification of the open problem whose latest notification is
// latest, in one transaction. It goes to the channels that got one of the problem's
// notifications, and still get its event; what the problem still owed is moot.
func (s *Store) resolve(ctx context.Context, latest Notification, usable []stored, now time.Time) (Notification, error) {
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Notification{}, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, err := problem(ctx, tx, latest)
	if err != nil {
		return Notification{}, err
	}
	got, err := deliveredTo(ctx, tx, ids)
	if err != nil {
		return Notification{}, err
	}
	if err := supersede(ctx, tx, ids, "The problem resolved before this was delivered.", now); err != nil {
		return Notification{}, err
	}
	base := channels.Base(latest.Event)
	r, err := next(ctx, tx, latest, base+"."+channels.PhaseResolved, latest.Severity, "Resolved: "+latest.Message, now)
	if err != nil {
		return r, err
	}
	var to []int64
	for _, c := range usable {
		if got[c.ID] && matches(c.Events, base) {
			to = append(to, c.ID)
		}
	}
	if _, err := queue(ctx, tx, r.ID, to, now); err != nil {
		return r, err
	}
	for _, id := range append(ids, r.ID) {
		if err := refresh(ctx, tx, id, now); err != nil {
			return r, err
		}
	}
	return r, tx.Commit()
}

// late queues the latest notification of an open problem, which went to no channel, to the
// channels that get it now: ones added since.
func (s *Store) late(ctx context.Context, n Notification, usable []stored, now time.Time) error {
	to := targets(usable, n)
	if len(to) == 0 {
		return nil
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries WHERE notification_id = ?`, n.ID).Scan(&count); err != nil || count > 0 {
		return err
	}
	if _, err := queue(ctx, tx, n.ID, to, now); err != nil {
		return err
	}
	return tx.Commit()
}

// deliver tries the due deliveries of every channel that has some, one goroutine per channel,
// skipping a channel that is still busy from the last pass. With wait, it returns once all are
// done. clock gives the time of each try.
func (e *Engine) deliver(ctx context.Context, clock func() time.Time, wait bool) {
	ids, err := e.store.channelsOwed(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.log.Error("list the deliveries owed", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		e.mu.Lock()
		busy := e.busy[id]
		e.busy[id] = true
		e.mu.Unlock()
		if busy {
			continue
		}
		wg.Go(func() {
			defer func() {
				e.mu.Lock()
				delete(e.busy, id)
				e.mu.Unlock()
			}()
			if err := e.deliverTo(ctx, id, clock); err != nil && ctx.Err() == nil {
				e.log.Error("deliver notifications", "channel_id", id, "error", err)
			}
		})
	}
	if wait {
		wg.Wait()
	}
}

// deliverTo tries one channel's due deliveries, oldest first. One that waits for its next try
// holds up the later ones, so a channel gets its notifications in order.
func (e *Engine) deliverTo(ctx context.Context, channelID int64, clock func() time.Time) error {
	cs, err := e.store.loadChannels(ctx, e.store.db.Read, true, `WHERE id = ? AND enabled = 1`, channelID)
	if err != nil || len(cs) == 0 {
		return err
	}
	c := cs[0]
	a := e.store.adapters[c.Type]
	if a == nil {
		return nil // a type from a newer Tender: it waits for that version
	}
	ds, err := e.store.pendingOf(ctx, channelID)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if clock().Before(d.updatedAt.Add(backoff(d.attempts))) {
			return nil
		}
		sendErr := a.Send(ctx, c.config, event(d.n, e.store.hub.URL))
		if ctx.Err() != nil {
			return nil // the hub is stopping: this try doesn't count
		}
		now := clock()
		t := try{delivery: d.id, notification: d.n.ID, channel: c.ID, status: DeliveryDelivered, attempts: d.attempts + 1, err: sendErr}
		switch {
		case sendErr == nil:
		case channels.IsPermanent(sendErr), now.Sub(d.createdAt) >= giveUpAfter:
			t.status = DeliveryFailed
		default:
			t.status = DeliveryPending
		}
		recorded, err := e.store.attempted(ctx, t, now)
		if err != nil {
			return err
		}
		attrs := []any{"id", d.n.ID, "event", d.n.Event, "channel", c.Name, "attempt", t.attempts}
		switch {
		case !recorded:
			e.log.Info("notification delivery ended while it ran: its problem resolved or its channel was turned off", attrs...)
		case t.status == DeliveryDelivered:
			e.log.Info("notification delivered", attrs...)
		case t.status == DeliveryFailed:
			e.log.Warn("notification delivery failed for good", append(attrs, "error", sendErr)...)
		default:
			e.log.Warn("notification delivery failed; it is tried again", append(attrs, "next_in", backoff(t.attempts).String(), "error", sendErr)...)
			return nil
		}
	}
	return nil
}
