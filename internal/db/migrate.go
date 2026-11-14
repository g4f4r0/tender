package db

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migrations is one domain's set of SQL files, named like 001_init.sql, in Files under dir.
type Migrations struct {
	Domain string
	Files  fs.FS
	Dir    string
}

type migration struct {
	domain  string
	version int
	name    string
	sql     string
}

const migrationsTable = `CREATE TABLE IF NOT EXISTS migrations (
  id         INTEGER PRIMARY KEY,
  domain     TEXT NOT NULL,
  version    INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (domain, version)
) STRICT`

// Migrate applies every migration not yet recorded, in the order the sets are given and by
// version within a set. It runs in one transaction: on any failure nothing is applied.
func (d *DB) Migrate(ctx context.Context, sets ...Migrations) error {
	var all []migration
	for _, set := range sets {
		ms, err := load(set)
		if err != nil {
			return err
		}
		all = append(all, ms...)
	}

	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Rollback after a successful Commit returns sql.ErrTxDone and changes nothing.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, migrationsTable); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	now := time.Now().Unix()
	for _, m := range all {
		var applied int
		err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM migrations WHERE domain = ? AND version = ?`, m.domain, m.version).Scan(&applied)
		if err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %s/%s: %w", m.domain, m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO migrations (domain, version, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			m.domain, m.version, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func load(set Migrations) ([]migration, error) {
	entries, err := fs.ReadDir(set.Files, set.Dir)
	if err != nil {
		return nil, fmt.Errorf("read %s migrations: %w", set.Domain, err)
	}
	var ms []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version < 1 {
			return nil, fmt.Errorf("%s migration %q: name must start with a version, like 001_init.sql", set.Domain, e.Name())
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("%s migrations %q and %q share version %d", set.Domain, other, e.Name(), version)
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(set.Files, path.Join(set.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{domain: set.Domain, version: version, name: e.Name(), sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}
