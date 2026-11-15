package notifications

import (
	"context"
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
// A notification owes its channels an event when it opens, the first time its severity goes up,
// and when it resolves. Each event becomes one delivery per channel, tried and retried on its
// own, so one failing channel never delays or repeats another. The notification's status says
// where it stands:
//  1. pending: its next event (opened or escalated) hasn't gone out yet.
//  2. sent: it went out, to at least one channel, or every channel's delivery of it ended.
//  3. resolved: the condition cleared. The resolved event goes to every channel that got the
//     alert, from then on, however long that takes.
//
// "Opened" or "escalated" is not stored. A pending row whose updated_at is after its created_at
// went out before, so it owes "escalated". The engine keeps updated_at equal to created_at on a
// row that has never gone out, even when the severity of that row changes, so it still owes
// "opened".
type Engine struct {
	store  *Store // whose channels can send
	sender Sender
	log    *slog.Logger

	// FailingAfter is how long a channel's deliveries fail before channel_failing opens: an hour.
	FailingAfter time.Duration

	silent bool // the "nobody gets alerts" line was logged and nothing has changed since

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

// Tick opens, updates and resolves notifications for ms as of now, queues the events they owe,
// and delivers what is due, waiting for every channel. Without an enabled channel, notifications
// are still recorded and stay pending, and the first tick that finds none says so in the log. It
// returns database errors; a failed delivery is logged and retried on a later tick.
func (e *Engine) Tick(ctx context.Context, now time.Time, ms []Machine) error {
	if err := e.evaluate(ctx, now, ms); err != nil {
		return err
	}
	e.deliver(ctx, func() time.Time { return now }, true)
	return nil
}

// usable lists the channels that can get alerts now: enabled, of a type this hub knows, and for
// email, with email on.
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

// gets says whether channel c gets notification n: one at least as severe as its min_severity.
// A channel_failing alert goes to every channel but the failing one.
func gets(c stored, n Notification) bool {
	if n.Type == TypeChannelFailing {
		return n.Channel == nil || *n.Channel != c.Name
	}
	return severityRank(c.MinSeverity) <= severityRank(n.Severity)
}

// evaluate runs the rules as of now and queues the events the notifications owe.
func (e *Engine) evaluate(ctx context.Context, now time.Time, ms []Machine) error {
	usable, err := e.usable(ctx)
	if err != nil {
		return err
	}
	if len(usable) == 0 {
		if !e.silent {
			e.log.Info("nobody gets alerts: there is no enabled channel, or only email ones while email is off, so alerts are only recorded. " +
				"Add a channel with \"tender channels add\" to get them")
			e.silent = true
		}
	} else {
		e.silent = false
	}
	if err := e.store.pruneTests(ctx, now); err != nil {
		return err
	}
	open, err := e.store.open(ctx)
	if err != nil {
		return err
	}
	holds := map[key]bool{}
	silent := map[string]bool{}
	byName := map[string]Machine{}
	found := func(k key, machineID int64, f finding) error {
		holds[k] = true
		switch n, ok := open[k]; {
		case !ok:
			if err := e.store.insert(ctx, machineID, k.channel, f, now); err != nil {
				return err
			}
			e.log.Info("notification opened", "type", f.typ, "about", k.about(), "service", f.service, "severity", f.severity)
		case severityRank(f.severity) != severityRank(n.Severity):
			if severityRank(f.severity) < severityRank(n.Severity) && n.Status == StatusPending && !neverEmailed(n) {
				// The escalation it owed is moot: it is back where its last event left it.
				if err := e.store.supersede(ctx, n.ID, channels.EventEscalated, "The alert went back to "+f.severity+" before this was delivered.", now); err != nil {
					return err
				}
			}
			if err := e.store.changeSeverity(ctx, n, f, now); err != nil {
				return err
			}
			e.log.Info("notification severity changed", "id", n.ID, "type", f.typ, "about", k.about(), "from", n.Severity, "to", f.severity)
		}
		return nil
	}
	for _, m := range ms {
		byName[m.Name] = m
		for _, f := range evaluate(m, now) {
			silent[m.Name] = silent[m.Name] || f.typ == TypeMachineSilent
			if err := found(key{f.typ, m.Name, f.service, ""}, m.ID, f); err != nil {
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
		f := finding{typ: TypeChannelFailing, severity: SeverityWarning, content: fc.describe(now)}
		if err := found(key{typ: TypeChannelFailing, channel: name}, 0, f); err != nil {
			return err
		}
	}

	rows, err := e.store.open(ctx)
	if err != nil {
		return err
	}
	for _, n := range sortedByID(rows) {
		k := keyOf(n)
		kept := byName[k.machine].keeps(n, silent[k.machine], now)
		switch {
		case !holds[k] && !kept:
			err = e.resolve(ctx, now, n, usable)
		case n.Status == StatusPending:
			err = e.queueOwed(ctx, now, n, usable)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// queueOwed queues the event a pending notification owes to every channel that gets it, once.
// With no such channel it stays pending, for a channel added later.
func (e *Engine) queueOwed(ctx context.Context, now time.Time, n Notification, usable []stored) error {
	ev := owed(n)
	queued, err := e.store.hasDeliveries(ctx, n.ID, ev)
	if err != nil {
		return err
	}
	if !queued {
		var ids []int64
		for _, c := range usable {
			if gets(c, n) {
				ids = append(ids, c.ID)
			}
		}
		if err := e.store.queue(ctx, n.ID, ids, ev, now); err != nil {
			return err
		}
	}
	return e.store.settle(ctx, n.ID, now)
}

// resolve closes a notification whose condition cleared, and queues the resolved event for every
// channel that got an event of it. What it still owed the others is moot.
func (e *Engine) resolve(ctx context.Context, now time.Time, n Notification, usable []stored) error {
	got, err := e.store.deliveredTo(ctx, n.ID)
	if err != nil {
		return err
	}
	if err := e.store.supersede(ctx, n.ID, "", "The alert resolved before this was delivered.", now); err != nil {
		return err
	}
	if err := e.store.setStatus(ctx, n.ID, StatusResolved, now); err != nil {
		return err
	}
	var ids []int64
	for _, c := range usable {
		if got[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	if err := e.store.queue(ctx, n.ID, ids, channels.EventResolved, now); err != nil {
		return err
	}
	e.log.Info("notification resolved", "id", n.ID, "type", n.Type, "about", keyOf(n).about())
	return nil
}

// neverEmailed reports whether n never went out to any channel yet.
func neverEmailed(n Notification) bool {
	return n.Status == StatusPending && !n.UpdatedAt.After(n.CreatedAt)
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

// deliverTo tries one channel's due deliveries, oldest first. A delivery waits while an older
// one of the same alert to this channel is still pending, so a channel gets an alert's events
// in order.
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
	waiting := map[int64]bool{} // alerts with an older delivery still pending
	for _, d := range ds {
		if d.notificationID == nil {
			continue // a test, which SendTest finishes itself
		}
		id := *d.notificationID
		if waiting[id] || clock().Before(d.updatedAt.Add(backoff(d.attempts))) {
			waiting[id] = true
			continue
		}
		n, err := scan(e.store.db.Read.QueryRowContext(ctx, selectNotifications+` WHERE n.id = ?`, id))
		if err != nil {
			return err
		}
		sendErr := a.Send(ctx, c.config, event(n, d.event, e.store.hub.URL))
		if ctx.Err() != nil {
			return nil // the hub is stopping: this try doesn't count
		}
		now := clock()
		status, attempts, why := DeliveryDelivered, d.attempts+1, (*string)(nil)
		switch {
		case sendErr == nil:
			e.log.Info("notification delivered", "id", id, "type", n.Type, "event", d.event, "channel", c.Name, "attempt", attempts)
		case channels.IsPermanent(sendErr), now.Sub(d.createdAt) >= giveUpAfter:
			status, why = DeliveryFailed, ptrTo(sendErr.Error())
			e.log.Warn("notification delivery failed for good", "id", id, "type", n.Type, "event", d.event, "channel", c.Name,
				"attempt", attempts, "error", sendErr)
		default:
			status, why = DeliveryPending, ptrTo(sendErr.Error())
			waiting[id] = true
			e.log.Warn("notification delivery failed; it is tried again", "id", id, "type", n.Type, "event", d.event, "channel", c.Name,
				"attempt", attempts, "next_in", backoff(attempts).String(), "error", sendErr)
		}
		if err := e.store.attempted(ctx, d.id, status, attempts, why, now); err != nil {
			return err
		}
		if err := e.store.settle(ctx, id, now); err != nil {
			return err
		}
	}
	return nil
}
