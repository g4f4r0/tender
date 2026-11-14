package machines

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads and writes machines and their usage.
type Store struct{ db *db.DB }

// NewStore returns a store on d.
func NewStore(d *db.DB) *Store { return &Store{db: d} }

// newToken returns a random token and its SHA-256, the only form the database keeps.
func newToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

// Create adds a machine and returns its token. The token is shown once and can't be read again.
func (s *Store) Create(ctx context.Context, name string) (string, error) {
	if !server.ValidName(name) {
		return "", server.Invalid("Machine names are lowercase letters, digits and hyphens, up to 63 characters.")
	}
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO machines (name, token_hash, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		name, hash, now, now)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n == 0 {
		return "", server.Conflict("Machine %q already exists.", name)
	}
	return token, nil
}

// RotateToken replaces a machine's token and returns the new one. The old token stops working.
func (s *Store) RotateToken(ctx context.Context, name string) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	res, err := s.db.Write.ExecContext(ctx,
		`UPDATE machines SET token_hash = ?, updated_at = ? WHERE name = ?`, hash, time.Now().Unix(), name)
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n == 0 {
		return "", server.NotFound("Machine %q doesn't exist.", name)
	}
	return token, nil
}

// Authenticate returns the machine that owns token. It compares every stored hash in constant time.
func (s *Store) Authenticate(ctx context.Context, token string) (int64, string, error) {
	sum := sha256.Sum256([]byte(token))
	rows, err := s.db.Read.QueryContext(ctx, `SELECT id, name, token_hash FROM machines`)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = rows.Close() }()

	var id int64
	var name string
	for rows.Next() {
		var rid int64
		var rname string
		var hash []byte
		if err := rows.Scan(&rid, &rname, &hash); err != nil {
			return 0, "", err
		}
		if subtle.ConstantTimeCompare(hash, sum[:]) == 1 {
			id, name = rid, rname
		}
	}
	if err := rows.Err(); err != nil {
		return 0, "", err
	}
	if name == "" {
		return 0, "", server.NotFound("No machine has this token.")
	}
	return id, name, nil
}

// Lookup returns the id of the machine called name.
func (s *Store) Lookup(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT id FROM machines WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, server.NotFound("Machine %q doesn't exist.", name)
	}
	return id, err
}

const overviewQuery = `
SELECT m.id, m.name, m.created_at, m.updated_at,
       u.id, u.cpu_load, u.memory_used_percent, u.disk_used_percent, u.uptime_seconds, u.created_at, u.updated_at
FROM machines m
LEFT JOIN machine_usage u ON u.id = (
  SELECT id FROM machine_usage WHERE machine_id = m.id ORDER BY created_at DESC, id DESC LIMIT 1)`

type scanner interface{ Scan(dest ...any) error }

func scanOverview(row scanner) (Overview, error) {
	var o Overview
	var created, updated int64
	var uid, uptime, ucreated, uupdated *int64
	var cpu, memory, disk *float64
	err := row.Scan(&o.ID, &o.Name, &created, &updated, &uid, &cpu, &memory, &disk, &uptime, &ucreated, &uupdated)
	if err != nil {
		return o, err
	}
	o.CreatedAt, o.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if uid != nil {
		u := &Usage{ID: *uid, CPULoad: *cpu, MemoryUsedPercent: *memory, DiskUsedPercent: *disk, UptimeSeconds: *uptime,
			CreatedAt: time.Unix(*ucreated, 0).UTC(), UpdatedAt: time.Unix(*uupdated, 0).UTC()}
		o.Usage, o.LastSeen = u, &u.CreatedAt
	}
	return o, nil
}

// List returns every machine by name, with its latest reading.
func (s *Store) List(ctx context.Context) ([]Overview, error) {
	rows, err := s.db.Read.QueryContext(ctx, overviewQuery+` ORDER BY m.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Overview{}
	for rows.Next() {
		o, err := scanOverview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Get returns the machine called name, with its latest reading.
func (s *Store) Get(ctx context.Context, name string) (Overview, error) {
	o, err := scanOverview(s.db.Read.QueryRowContext(ctx, overviewQuery+` WHERE m.name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return o, server.NotFound("Machine %q doesn't exist.", name)
	}
	return o, err
}

// AddUsage stores one reading for a machine.
func (s *Store) AddUsage(ctx context.Context, machineID int64, in UsageInput) (Usage, error) {
	u := Usage{CPULoad: in.CPULoad, MemoryUsedPercent: in.MemoryUsedPercent, DiskUsedPercent: in.DiskUsedPercent,
		UptimeSeconds: in.UptimeSeconds, CreatedAt: time.Unix(in.CreatedAt.Unix(), 0).UTC(), UpdatedAt: time.Unix(time.Now().Unix(), 0).UTC()}
	err := s.db.Write.QueryRowContext(ctx,
		`INSERT INTO machine_usage (machine_id, cpu_load, memory_used_percent, disk_used_percent, uptime_seconds, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		machineID, u.CPULoad, u.MemoryUsedPercent, u.DiskUsedPercent, u.UptimeSeconds, u.CreatedAt.Unix(), u.UpdatedAt.Unix()).Scan(&u.ID)
	return u, err
}

// ListUsage returns a machine's readings newest first, from since to until (zero means no bound).
// It fetches one row more than the page limit; server.NewList cuts it.
func (s *Store) ListUsage(ctx context.Context, machineID int64, since, until time.Time, p server.Page) ([]Usage, error) {
	lo, hi := int64(0), int64(1)<<62
	if !since.IsZero() {
		lo = since.Unix()
	}
	if !until.IsZero() {
		hi = until.Unix()
	}
	rows, err := s.db.Read.QueryContext(ctx,
		`SELECT id, cpu_load, memory_used_percent, disk_used_percent, uptime_seconds, created_at, updated_at
		 FROM machine_usage
		 WHERE machine_id = ? AND created_at >= ? AND created_at <= ? AND (created_at, id) < (?, ?)
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		machineID, lo, hi, p.Before.CreatedAt, p.Before.ID, p.Size())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Usage
	for rows.Next() {
		var u Usage
		var created, updated int64
		if err := rows.Scan(&u.ID, &u.CPULoad, &u.MemoryUsedPercent, &u.DiskUsedPercent, &u.UptimeSeconds, &created, &updated); err != nil {
			return nil, err
		}
		u.CreatedAt, u.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

func (in UsageInput) validate() error {
	switch {
	case in.CreatedAt.IsZero():
		return server.Invalid("created_at is required.")
	case in.CPULoad < 0:
		return server.Invalid("cpu_load can't be negative.")
	case in.MemoryUsedPercent < 0 || in.MemoryUsedPercent > 100:
		return server.Invalid("memory_used_percent must be from 0 to 100.")
	case in.DiskUsedPercent < 0 || in.DiskUsedPercent > 100:
		return server.Invalid("disk_used_percent must be from 0 to 100.")
	case in.UptimeSeconds < 0:
		return server.Invalid("uptime_seconds can't be negative.")
	}
	return nil
}

const usageLifetime = 14 * 24 * time.Hour

// PruneUsage deletes usage readings older than 14 days, now and then every day until ctx ends.
func (s *Store) PruneUsage(ctx context.Context, log *slog.Logger) {
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		if err := s.pruneUsage(ctx, time.Now()); err != nil && ctx.Err() == nil {
			log.Error("prune machine usage", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Store) pruneUsage(ctx context.Context, now time.Time) error {
	_, err := s.db.Write.ExecContext(ctx, `DELETE FROM machine_usage WHERE created_at < ?`, now.Add(-usageLifetime).Unix())
	return err
}
