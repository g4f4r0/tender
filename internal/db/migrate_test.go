package db

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func set(domain string, files map[string]string) Migrations {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return Migrations{Domain: domain, Files: fsys, Dir: "migrations"}
}

func count(t *testing.T, d *DB, query string) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateAppliesInOrderAndIsIdempotent(t *testing.T) {
	d := open(t)
	a := set("a", map[string]string{
		"002_more.sql": `ALTER TABLE a ADD COLUMN extra TEXT`,
		"001_init.sql": `CREATE TABLE a (id INTEGER PRIMARY KEY) STRICT`,
	})
	b := set("b", map[string]string{
		"001_init.sql": `CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a(id)) STRICT`,
	})

	for range 2 {
		if err := d.Migrate(context.Background(), a, b); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM migrations`); n != 3 {
		t.Fatalf("migrations rows = %d, want 3", n)
	}
	if n := count(t, d, `SELECT count(*) FROM pragma_table_info('a') WHERE name = 'extra'`); n != 1 {
		t.Fatal("002_more.sql was not applied after 001_init.sql")
	}
}

func TestMigrateRollsBackEverythingOnFailure(t *testing.T) {
	d := open(t)
	good := set("good", map[string]string{"001_init.sql": `CREATE TABLE good (id INTEGER PRIMARY KEY) STRICT`})
	bad := set("bad", map[string]string{"001_init.sql": `CREATE TABLE broken (`})

	if err := d.Migrate(context.Background(), good, bad); err == nil {
		t.Fatal("expected an error from the broken migration")
	}
	if n := count(t, d, `SELECT count(*) FROM sqlite_schema WHERE name IN ('good', 'migrations')`); n != 0 {
		t.Fatalf("%d tables survived a failed migration, want 0", n)
	}
}

func TestMigrateRejectsBadNames(t *testing.T) {
	d := open(t)
	for _, name := range []string{"init.sql", "000_init.sql"} {
		if err := d.Migrate(context.Background(), set("x", map[string]string{name: `SELECT 1`})); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	dup := set("x", map[string]string{"001_a.sql": `SELECT 1`, "001_b.sql": `SELECT 1`})
	if err := d.Migrate(context.Background(), dup); err == nil {
		t.Error("duplicate versions: expected an error")
	}
}

func TestReadPoolIsReadOnly(t *testing.T) {
	d := open(t)
	if _, err := d.Read.Exec(`CREATE TABLE nope (id INTEGER)`); err == nil {
		t.Fatal("the read pool accepted a write")
	}
}
