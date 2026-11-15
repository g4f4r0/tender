package notifications

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads notifications and keeps the channels and their deliveries.
type Store struct {
	db       *db.DB
	key      []byte                      // seals the channels' secrets; nil where nothing reads or writes them
	adapters map[string]channels.Adapter // the channel types; they send only once sending set them up
	hub      Hub
}

// NewStore returns a store on d. key is TENDER_ENCRYPTION_KEY, decoded: it seals the secrets in
// channel configs. Without it, channels without secrets still work.
func NewStore(d *db.DB, key []byte) *Store { return &Store{db: d, key: key, adapters: adapters(nil, Hub{})} }

// sending returns a copy of the store whose channels send email through sender, nil when email
// is off, and name hub in what they send.
func (s *Store) sending(sender Sender, hub Hub) *Store {
	c := *s
	c.adapters, c.hub = adapters(sender, hub), hub
	return &c
}

// The machine's name comes from the machines table, which the schema already ties this domain to.
const selectNotifications = `
SELECT n.id, m.name, n.service, n.type, n.severity, n.status, n.content, n.channel, n.created_at, n.updated_at
FROM notifications n LEFT JOIN machines m ON m.id = n.machine_id`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Notification, error) {
	var n Notification
	var created, updated int64
	err := row.Scan(&n.ID, &n.Machine, &n.Service, &n.Type, &n.Severity, &n.Status, &n.Content, &n.Channel, &created, &updated)
	n.CreatedAt, n.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return n, err
}

// List returns notifications newest first, optionally of one machine (machineID 0 means every
// machine), status and severity ("" means any). It fetches one row more than the page limit;
// server.NewList cuts it.
func (s *Store) List(ctx context.Context, machineID int64, status, severity string, p server.Page) ([]Notification, error) {
	switch {
	case status != "" && status != StatusPending && status != StatusSent && status != StatusResolved:
		return nil, server.Invalid("status must be pending, sent or resolved.")
	case severity != "" && severity != SeverityInfo && severity != SeverityWarning && severity != SeverityCritical:
		return nil, server.Invalid("severity must be info, warning or critical.")
	}
	rows, err := s.db.Read.QueryContext(ctx, selectNotifications+`
		WHERE (? = 0 OR n.machine_id = ?) AND (? = '' OR n.status = ?) AND (? = '' OR n.severity = ?)
		  AND (n.created_at, n.id) < (?, ?)
		ORDER BY n.created_at DESC, n.id DESC LIMIT ?`,
		machineID, machineID, status, status, severity, severity, p.Before.CreatedAt, p.Before.ID, p.Size())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Notification
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.withDeliveries(ctx, out)
}

// Get returns one of a machine's notifications by the hub's id.
func (s *Store) Get(ctx context.Context, machineID, id int64) (Notification, error) {
	n, err := scan(s.db.Read.QueryRowContext(ctx, selectNotifications+` WHERE n.machine_id = ? AND n.id = ?`, machineID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, server.NotFound("Notification %d doesn't exist on this machine.", id)
	}
	if err != nil {
		return n, err
	}
	ns := []Notification{n}
	err = s.withDeliveries(ctx, ns)
	return ns[0], err
}

// key names a rule's notification: the type, the machine's name and, for a service alert, the
// service's name, or for a channel alert, the channel's name.
type key struct{ typ, machine, service, channel string }

func keyOf(n Notification) key {
	k := key{typ: n.Type}
	for _, f := range []struct {
		from *string
		to   *string
	}{{n.Machine, &k.machine}, {n.Service, &k.service}, {n.Channel, &k.channel}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	return k
}

// about names what a notification is about, for the log.
func (k key) about() string {
	if k.channel != "" {
		return "channel " + k.channel
	}
	return k.machine
}

// open returns the unresolved notifications of the rules, by key.
func (s *Store) open(ctx context.Context) (map[key]Notification, error) {
	rows, err := s.db.Read.QueryContext(ctx, selectNotifications+` WHERE n.status != 'resolved' AND (m.id IS NOT NULL OR n.channel IS NOT NULL)`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[key]Notification{}
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		if _, ok := typeText[n.Type]; ok {
			out[keyOf(n)] = n
		}
	}
	return out, rows.Err()
}

func sortedByID(m map[key]Notification) []Notification {
	out := make([]Notification, 0, len(m))
	for _, n := range m {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b Notification) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// insert opens a pending notification about a machine (machineID) or a channel (channel, with
// machineID 0). Its updated_at equals its created_at until it first goes out.
func (s *Store) insert(ctx context.Context, machineID int64, channel string, f finding, now time.Time) error {
	var service, ch *string
	if f.service != "" {
		service = &f.service
	}
	if channel != "" {
		ch = &channel
	}
	var id any // NULL for a channel's alert
	if machineID != 0 {
		id = machineID
	}
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO notifications (machine_id, service, channel, type, severity, status, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, service, ch, f.typ, f.severity, StatusPending, f.content, now.Unix(), now.Unix())
	return err
}

// changeSeverity stores a new severity and content for an open notification. A rise sets a sent
// notification back to pending, which owes an "escalated" email, unless its one escalated email
// already went out. A drop owes nothing: it also settles a pending escalation, since the email
// that went out was at the lower severity.
func (s *Store) changeSeverity(ctx context.Context, n Notification, f finding, now time.Time) error {
	status, updated := n.Status, now
	switch {
	case neverEmailed(n):
		updated = n.CreatedAt // stays "opened"
	case severityRank(f.severity) > severityRank(n.Severity):
		status = StatusPending
	case n.Status == StatusPending:
		status = StatusSent
	}
	_, err := s.db.Write.ExecContext(ctx,
		`UPDATE notifications SET severity = ?, status = CASE WHEN ? = 'pending' AND status = 'sent' AND escalated = 1 THEN status ELSE ? END,
		 content = ?, updated_at = ? WHERE id = ?`,
		f.severity, status, status, f.content, updated.Unix(), n.ID)
	return err
}

// escalated marks that a notification's escalated email went out.
func (s *Store) escalated(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notifications SET status = 'sent', escalated = 1, updated_at = ? WHERE id = ?`, now.Unix(), id)
	return err
}

func (s *Store) setStatus(ctx context.Context, id int64, status string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notifications SET status = ?, updated_at = ? WHERE id = ?`, status, now.Unix(), id)
	return err
}
