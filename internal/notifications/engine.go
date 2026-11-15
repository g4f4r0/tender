package notifications

import (
	"context"
	"log/slog"
	"time"
)

// Sender delivers an email to bare addresses. A nil Sender means email is off.
type Sender interface {
	Send(ctx context.Context, to []string, msg Message) error
}

// Engine runs the rules and delivers their notifications.
//
// A notification is emailed once when it opens and once when it resolves, and again when its
// severity goes up. The status column is the only delivery state:
//  1. pending: the next email (opened or escalated) isn't sent yet.
//  2. sent: the last email went out.
//  3. resolved: the condition cleared, and the resolved email went out if one was owed.
//
// "Opened" or "escalated" is not stored. A pending row whose updated_at is after its created_at
// was emailed before, so its email is "escalated". The engine keeps updated_at equal to
// created_at on a row it has never emailed, even when the severity of that row changes, so it
// still gets an "opened" email.
type Engine struct {
	store  *Store
	sender Sender
	hub    string
	log    *slog.Logger

	failures map[attempt]failure // in memory: a restart starts the backoff over
	silent   bool                // the "nobody gets email" line was logged and nothing has changed since
}

type attempt struct {
	id   int64
	kind string
}

type failure struct {
	count int
	next  time.Time
}

// NewEngine returns an engine that emails through sender, or not at all when sender is nil.
// hub is the hub's host name, shown in every email.
func NewEngine(s *Store, sender Sender, hub string, log *slog.Logger) *Engine {
	return &Engine{store: s, sender: sender, hub: hub, log: log, failures: map[attempt]failure{}}
}

// Run evaluates the rules now and then every minute until ctx ends. load returns the machines' current state.
func (e *Engine) Run(ctx context.Context, load func(context.Context) ([]Machine, error)) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		ms, err := load(ctx)
		if err == nil {
			err = e.Tick(ctx, time.Now(), ms)
		}
		if err != nil && ctx.Err() == nil {
			e.log.Error("run notification rules", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick opens, updates and resolves notifications for ms as of now, then delivers what's owed to
// every recipient. Without email or recipients, notifications are still recorded and stay pending,
// and the first tick that finds nobody to email says so in the log. It returns database errors; a
// failed email is logged and retried on a later tick.
func (e *Engine) Tick(ctx context.Context, now time.Time, ms []Machine) error {
	to, err := e.store.emails(ctx)
	if err != nil {
		return err
	}
	if e.sender == nil || len(to) == 0 {
		to = nil
		if !e.silent {
			e.log.Info("nobody gets alert emails: email is off or there are no recipients, so alerts are only recorded. " +
				"Set TENDER_SMTP_HOST and TENDER_SMTP_FROM and add a recipient to get them by email")
			e.silent = true
		}
	} else {
		e.silent = false
	}
	open, err := e.store.open(ctx)
	if err != nil {
		return err
	}
	holds := map[key]bool{}
	silent := map[string]bool{}
	byName := map[string]Machine{}
	for _, m := range ms {
		byName[m.Name] = m
		for _, f := range evaluate(m, now) {
			k := key{f.typ, m.Name, f.service}
			holds[k] = true
			silent[m.Name] = silent[m.Name] || f.typ == TypeMachineSilent
			switch n, ok := open[k]; {
			case !ok:
				if err := e.store.insert(ctx, m.ID, f, now); err != nil {
					return err
				}
				e.log.Info("notification opened", "type", f.typ, "machine", m.Name, "service", f.service, "severity", f.severity)
			case severityRank(f.severity) != severityRank(n.Severity):
				if err := e.store.changeSeverity(ctx, n, f, now); err != nil {
					return err
				}
				e.log.Info("notification severity changed", "id", n.ID, "type", f.typ, "machine", m.Name, "from", n.Severity, "to", f.severity)
			}
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
			err = e.resolve(ctx, now, n, to)
		case n.Status == StatusPending && to != nil:
			err = e.sendPending(ctx, now, n, to)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sendPending emails an open notification that is pending.
func (e *Engine) sendPending(ctx context.Context, now time.Time, n Notification, to []string) error {
	kind := kindOpened
	if n.UpdatedAt.After(n.CreatedAt) {
		kind = kindEscalated
	}
	if !e.send(ctx, now, n, kind, to) {
		return nil
	}
	return e.store.setStatus(ctx, n.ID, StatusSent, now)
}

// resolve closes a notification whose condition cleared. It emails first when an email was owed
// and there is someone to send it to.
func (e *Engine) resolve(ctx context.Context, now time.Time, n Notification, to []string) error {
	if to != nil && !neverEmailed(n) && !e.send(ctx, now, n, kindResolved, to) {
		return nil
	}
	if err := e.store.setStatus(ctx, n.ID, StatusResolved, now); err != nil {
		return err
	}
	delete(e.failures, attempt{n.ID, kindOpened})
	delete(e.failures, attempt{n.ID, kindEscalated})
	delete(e.failures, attempt{n.ID, kindResolved})
	e.log.Info("notification resolved", "id", n.ID, "type", n.Type, "machine", *n.Machine)
	return nil
}

// neverEmailed reports whether no email went out for n yet.
func neverEmailed(n Notification) bool {
	return n.Status == StatusPending && !n.UpdatedAt.After(n.CreatedAt)
}

// send emails one notification and reports whether it went out. A failure is logged and the
// next try waits: none on the next tick, then 2, 4 and 8 minutes, then 10 minutes at most.
func (e *Engine) send(ctx context.Context, now time.Time, n Notification, kind string, to []string) bool {
	a := attempt{n.ID, kind}
	f := e.failures[a]
	if now.Before(f.next) {
		return false
	}
	text := typeText[n.Type]
	title := text.title
	if n.Service != nil {
		title = *n.Service + " " + title
	}
	data := emailData{
		Machine: *n.Machine, Title: title, Content: n.Content, Severity: n.Severity,
		Since: formatTime(n.CreatedAt), Duration: formatDuration(now.Sub(n.CreatedAt)), ResolvedAt: formatTime(now),
		Tool: text.tool, Hub: e.hub, Color: "#b00020",
	}
	if n.Severity == SeverityWarning {
		data.Color = "#8a5a00"
	}
	msg, err := render(kind, data)
	if err == nil {
		err = e.sender.Send(ctx, to, msg)
	}
	if err != nil {
		f.count++
		f.next = now.Add(backoff(f.count))
		e.failures[a] = f
		e.log.Warn("email failed", "id", n.ID, "type", n.Type, "machine", *n.Machine, "email", kind, "attempt", f.count, "error", err)
		return false
	}
	delete(e.failures, a)
	e.log.Info("email sent", "id", n.ID, "type", n.Type, "machine", *n.Machine, "email", kind)
	return true
}

// backoff is the wait after the count-th failure in a row.
func backoff(count int) time.Duration {
	if count < 2 {
		return 0
	}
	if count >= 5 {
		return 10 * time.Minute
	}
	return time.Duration(1<<(count-1)) * time.Minute
}
