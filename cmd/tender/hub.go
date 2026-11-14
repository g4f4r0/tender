package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/server"
)

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
