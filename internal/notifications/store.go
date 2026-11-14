package notifications

import (
	"context"
	"database/sql"
	"errors"
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
