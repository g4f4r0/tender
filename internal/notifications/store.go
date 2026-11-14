package notifications

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads notifications.
type Store struct{ db *db.DB }

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

// The machine's name comes from the machines table, which the schema already ties this domain to.
const selectNotifications = `
SELECT n.id, m.name, n.type, n.severity, n.status, n.content, n.created_at, n.updated_at
FROM notifications n LEFT JOIN machines m ON m.id = n.machine_id`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Notification, error) {
	var n Notification
	var created, updated int64
	err := row.Scan(&n.ID, &n.Machine, &n.Type, &n.Severity, &n.Status, &n.Content, &created, &updated)
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
	return out, rows.Err()
}

// Get returns one of a machine's notifications by the hub's id.
func (s *Store) Get(ctx context.Context, machineID, id int64) (Notification, error) {
	n, err := scan(s.db.Read.QueryRowContext(ctx, selectNotifications+` WHERE n.machine_id = ? AND n.id = ?`, machineID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, server.NotFound("Notification %d doesn't exist on this machine.", id)
	}
	return n, err
}

// key names a rule's notification: the type and the machine's name.
type key struct{ typ, machine string }

// open returns the unresolved notifications of the rules, by type and machine.
func (s *Store) open(ctx context.Context) (map[key]Notification, error) {
	rows, err := s.db.Read.QueryContext(ctx, selectNotifications+` WHERE n.status != 'resolved' AND m.id IS NOT NULL`)
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
			out[key{n.Type, *n.Machine}] = n
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

// insert opens a pending notification. Its updated_at equals its created_at until it is emailed.
func (s *Store) insert(ctx context.Context, machineID int64, f finding, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO notifications (machine_id, type, severity, status, content, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		machineID, f.typ, f.severity, StatusPending, f.content, now.Unix(), now.Unix())
	return err
}

// changeSeverity stores a new severity and content for an open notification. A rise sets a sent
// notification back to pending, which owes an "escalated" email. A drop owes nothing: it also
// settles a pending escalation, since the email that went out was at the lower severity.
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
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notifications SET severity = ?, status = ?, content = ?, updated_at = ? WHERE id = ?`,
		f.severity, status, f.content, updated.Unix(), n.ID)
	return err
}

func (s *Store) setStatus(ctx context.Context, id int64, status string, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `UPDATE notifications SET status = ?, updated_at = ? WHERE id = ?`, status, now.Unix(), id)
	return err
}
