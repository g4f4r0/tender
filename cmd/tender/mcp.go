package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/g4f4r0/tender/internal/db"
)

// runMCP serves the MCP tools over stdio from the hub's existing database, for a local client
// on the hub machine. It never creates or migrates the database.
func runMCP(args []string, log *slog.Logger) error {
	fs := newFlagSet("mcp", "--stdio [--database PATH]",
		"Serve the read-only MCP tools over standard input and output, for an AI client on the hub machine. "+
			"It reads the hub's database directly, so the hub must have run once.")
	stdio := fs.Bool("stdio", false, "serve over standard input and output (required)")
	database := fs.String("database", defaultDatabase, "`PATH` the hub's SQLite database file")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if !*stdio {
		return usageError(fs, "mcp needs --stdio")
	}
	if fs.NArg() > 0 {
		return usageError(fs, "mcp takes no arguments, got %q", fs.Arg(0))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dropToDatabaseOwner(*database); err != nil {
		return err
	}
	if _, err := os.Stat(*database); err != nil {
		return fmt.Errorf("no database at %s. Run \"tender hub\" once to create it, or pass --database PATH.", *database)
	}
	d, err := db.Open(*database)
	if err != nil {
		return fmt.Errorf("cannot open the hub database %s. Check that you can read it, or pass --database PATH: %w", *database, err)
	}
	if err := d.CheckSchema(ctx, migrations()...); err != nil {
		return errors.Join(fmt.Errorf("the database at %s has no current schema. Run \"tender hub\" once to create or update it: %w", *database, err), d.Close())
	}
	return errors.Join(newTools(d, log).RunStdio(ctx), d.Close())
}
