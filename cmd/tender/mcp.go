package main

import (
	"context"
	"errors"
	"flag"
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
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	stdio := fs.Bool("stdio", false, "serve over standard input and output")
	database := fs.String("database", defaultDatabase, "SQLite database file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*stdio || fs.NArg() > 0 {
		return errors.New("usage: tender mcp --stdio [--database PATH]")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := os.Stat(*database); err != nil {
		return fmt.Errorf("no database at %s: run \"tender hub\" once to create it, or pass --database", *database)
	}
	d, err := db.Open(*database)
	if err != nil {
		return err
	}
	if err := d.CheckSchema(ctx, migrations()...); err != nil {
		return errors.Join(fmt.Errorf("the database at %s has no current schema: run \"tender hub\" once to create it: %w", *database, err), d.Close())
	}
	return errors.Join(newTools(d, log).RunStdio(ctx), d.Close())
}
