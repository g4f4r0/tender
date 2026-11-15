// Package db opens the hub's SQLite database and applies each domain's migrations.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"runtime"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// DB holds two pools on one SQLite file: a single writer and several readers.
// Never query Write inside an open write transaction: its one connection deadlocks.
type DB struct {
	Write *sql.DB
	Read  *sql.DB
}

// secure_delete zeroes what a write frees, so content the hub deletes or clears, such as logs,
// doesn't stay in free pages; Checkpoint then takes it out of the WAL.
const pragmas = "_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)"

// Open opens or creates the database at path in WAL mode.
func Open(path string) (*DB, error) {
	file := "file:" + url.PathEscape(path)

	write, err := sql.Open("sqlite", file+"?_pragma=journal_mode(WAL)&"+pragmas+"&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open write pool: %w", err)
	}
	write.SetMaxOpenConns(1)
	if err := write.Ping(); err != nil {
		return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), write.Close())
	}

	read, err := sql.Open("sqlite", file+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open read pool: %w", err), write.Close())
	}
	read.SetMaxOpenConns(max(4, runtime.NumCPU()))

	return &DB{Write: write, Read: read}, nil
}

// Checkpoint copies the WAL into the database file and empties it, so the WAL keeps no old copy
// of a page whose content was cleared. A reader in the middle of a query can keep it from
// finishing; done says whether it did.
func (d *DB) Checkpoint(ctx context.Context) (done bool, err error) {
	var busy, logFrames, checkpointed int
	if err := d.Write.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return false, err
	}
	return busy == 0, nil
}

// Ping checks that both pools answer.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.Write.PingContext(ctx); err != nil {
		return err
	}
	return d.Read.PingContext(ctx)
}

// Close closes both pools.
func (d *DB) Close() error {
	rerr := d.Read.Close()
	if err := d.Write.Close(); err != nil {
		return err
	}
	return rerr
}
