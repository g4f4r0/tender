// Command tender orchestrates restic backups across machines and serves their state to AI clients.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  tender hub [--database PATH] [--listen ADDR]   run the hub
  tender version                                 print the version
`

// migrations lists every domain's migrations in dependency order: machines first.
func migrations() []db.Migrations {
	return []db.Migrations{machines.Migrations, backups.Migrations, notifications.Migrations, server.Migrations}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: dropTime}))

	var err error
	switch os.Args[1] {
	case "hub":
		err = runHub(os.Args[2:], log)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("tender failed", "error", err)
		os.Exit(1)
	}
}

func runHub(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	database := fs.String("database", "/var/lib/tender/hub.db", "SQLite database file")
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := db.Open(*database)
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}()
	if err := d.Migrate(ctx, migrations()...); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(d, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("hub listening", "address", *listen, "database", *database, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("hub stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// dropTime removes slog's timestamp; journald records its own.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}
