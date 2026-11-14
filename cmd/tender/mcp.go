package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
)

// runMCP serves the MCP tools over stdio from the hub's existing database, for a local client
// on the hub machine. It never creates or migrates the database.
func runMCP(args []string, log *slog.Logger) error {
	database := os.Getenv(config.EnvDatabase)
	if database == "" {
		database = defaultDatabase
	}
	fs := newFlagSet("mcp", "--stdio [--database PATH]",
		"Serve the MCP tools over standard input and output, for an AI client on the hub machine. "+
			"It reads and writes the hub's database directly, so the hub must have run once. Tools that store "+
			"destination keys need "+config.EnvEncryptionKey+" in the client's environment, and send_test_email needs the "+
			"TENDER_SMTP_ variables; without them those tools say what is missing.")
	stdio := fs.Bool("stdio", false, "serve over standard input and output (required)")
	fs.StringVar(&database, "database", database, "`PATH` the hub's SQLite database file (default: "+config.EnvDatabase+", else "+defaultDatabase+")")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if !*stdio {
		return usageError(fs, "mcp needs --stdio")
	}
	if fs.NArg() > 0 {
		return usageError(fs, "mcp takes no arguments, got %q", fs.Arg(0))
	}
	key, err := config.LoadEncryptionKey(os.Getenv)
	if err != nil {
		return err
	}
	smtp, err := config.LoadSMTP(os.Getenv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if _, err := os.Stat(database); err != nil {
		return fmt.Errorf("no database at %s. Run \"tender hub\" once to create it, or pass --database PATH.", database)
	}
	d, err := db.Open(database)
	if err != nil {
		return fmt.Errorf("cannot open the hub database %s. Check that you can read and write it, or pass --database PATH: %w", database, err)
	}
	if err := d.CheckSchema(ctx, migrations()...); err != nil {
		return errors.Join(fmt.Errorf("the database at %s has no current schema. Run \"tender hub\" once to create or update it: %w", database, err), d.Close())
	}
	// Without a key, the tools that store secrets refuse; with the wrong one, they would store
	// secrets the hub can't open.
	if key != nil {
		if err := checkKey(ctx, d, key, database); err != nil {
			return errors.Join(err, d.Close())
		}
	}
	return errors.Join(newTools(d, key, mailSender(smtp), log).RunStdio(ctx), d.Close())
}
