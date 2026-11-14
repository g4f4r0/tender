package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

const defaultDatabase = config.DefaultDatabase

const hubUsage = `Usage:
  tender hub [--config PATH] [--database PATH] [--listen ADDR]    run the hub
  tender hub add-machine NAME                     add a machine and print its token once
  tender hub rotate-token NAME                    replace a machine's token and print the new one
  tender hub add-destination NAME --type s3 --endpoint URL --bucket BUCKET [--path PATH]
  tender hub set-backup MACHINE --destination NAME --schedule "30 3 * * *" --retention-days 30
  tender hub test-email [--config PATH]           send a test email
  tender hub help                                 print this help

Every command reads the hub config (--config PATH, default ` + config.DefaultHubPath + `; none is fine) and takes
--database PATH, which overrides its database (default ` + defaultDatabase + `). Run also takes --listen ADDR
(default ` + config.DefaultListen + `).
`

func runHub(args []string, log *slog.Logger) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return hubCommand(args[0], args[1:], os.Stdout)
	}
	return serveHub(args, log)
}

// openHub opens the database and applies the migrations, so the commands also work before the first run.
func openHub(ctx context.Context, path string) (*db.DB, error) {
	d, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	if err := d.Migrate(ctx, migrations()...); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

// newHub returns the hub's HTTP server with every domain's routes and the MCP endpoint.
func newHub(d *db.DB, log *slog.Logger) *server.Server {
	ms := machines.NewStore(d)
	srv := server.New(d, log, ms)
	ms.Routes(srv)
	backups.NewStore(d).Routes(srv)
	notifications.NewStore(d).Routes(srv)

	// MCP clients are tailnet readers too. The SDK handler answers GET and DELETE with 405 in
	// stateless mode, so only POST is routed.
	tools := newTools(d, log).Handler()
	srv.Handle("POST /mcp", srv.Reader(func(w http.ResponseWriter, r *http.Request) error {
		tools.ServeHTTP(w, r)
		return nil
	}))
	return srv
}

// newTools returns the MCP server with every domain's tools. The hub and "tender mcp" share it.
func newTools(d *db.DB, log *slog.Logger) *mcp.Server {
	ms := machines.NewStore(d)
	s := mcp.New(version, log)
	ms.RegisterTools(s)
	backups.NewStore(d).RegisterTools(s, ms.Lookup)
	notifications.NewStore(d).RegisterTools(s, ms.Lookup)
	return s
}

func serveHub(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	configPath := fs.String("config", "", "hub config file (default "+config.DefaultHubPath+")")
	database := fs.String("database", "", "SQLite database file (overrides the config)")
	listen := fs.String("listen", "", "address to listen on (overrides the config)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := hubConfig(*configPath, *database, *listen)
	if err != nil {
		return err
	}
	var mailer *notifications.Mailer
	if cfg.Email != nil {
		if mailer, err = newMailer(*cfg.Email); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d, err := openHub(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}()

	hub := newHub(d, log)
	ms := machines.NewStore(d)
	go hub.PruneKeys(ctx)
	go ms.PruneUsage(ctx, log)
	var sender notifications.Sender // stays nil when email is off
	if mailer != nil {
		sender = mailer
	} else {
		log.Info("email is off: the config has no [email] table, so notifications stay pending")
	}
	engine := notifications.NewEngine(notifications.NewStore(d), sender, hubName(), log)
	go engine.Run(ctx, loadMachines(ms, backups.NewStore(d)))
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           hub,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("hub listening", "address", cfg.Listen, "database", cfg.Database, "version", version)

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

// hubConfig loads the hub config and applies the flags, which win over the file. An empty flag is not set.
func hubConfig(path, database, listen string) (config.Hub, error) {
	cfg, err := config.LoadHub(path)
	if err != nil {
		return cfg, err
	}
	if database != "" {
		cfg.Database = database
	}
	if listen != "" {
		cfg.Listen = listen
	}
	return cfg, nil
}

// hubCommand runs one of the hub's administration commands, writing its result to out.
func hubCommand(name string, args []string, out io.Writer) error {
	if name == "help" {
		_, err := io.WriteString(out, hubUsage)
		return err
	}
	if name == "test-email" {
		return testEmail(args, out)
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String("config", "", "hub config file (default "+config.DefaultHubPath+")")
	database := fs.String("database", "", "SQLite database file (overrides the config)")
	var run func(ctx context.Context, d *db.DB, arg string) error

	switch name {
	case "add-machine":
		run = func(ctx context.Context, d *db.DB, machine string) error {
			token, err := machines.NewStore(d).Create(ctx, machine)
			if err == nil {
				_, err = fmt.Fprintln(out, token)
			}
			return err
		}
	case "rotate-token":
		run = func(ctx context.Context, d *db.DB, machine string) error {
			token, err := machines.NewStore(d).RotateToken(ctx, machine)
			if err == nil {
				_, err = fmt.Fprintln(out, token)
			}
			return err
		}
	case "add-destination":
		var in backups.Destination
		fs.StringVar(&in.Type, "type", "", "destination type, s3")
		fs.StringVar(&in.Endpoint, "endpoint", "", "S3 endpoint URL")
		fs.StringVar(&in.Bucket, "bucket", "", "bucket name")
		fs.StringVar(&in.Path, "path", "", "folder inside the bucket")
		run = func(ctx context.Context, d *db.DB, destination string) error {
			in.Name = destination
			added, err := backups.NewStore(d).AddDestination(ctx, in)
			if err == nil {
				_, err = fmt.Fprintf(out, "Added destination %s (%s)\n", added.Name, added.Repository())
			}
			return err
		}
	case "set-backup":
		destination := fs.String("destination", "", "destination name")
		schedule := fs.String("schedule", "", `cron schedule in UTC, such as "30 3 * * *"`)
		retention := fs.Int("retention-days", 0, "days to keep snapshots")
		run = func(ctx context.Context, d *db.DB, machine string) error {
			id, err := machines.NewStore(d).Lookup(ctx, machine)
			if err != nil {
				return err
			}
			st, err := backups.NewStore(d).SetSettings(ctx, id, *destination, *schedule, *retention)
			if err == nil {
				_, err = fmt.Fprintf(out, "%s backs up to %s on %q and keeps %d days\n",
					machine, st.Destination.Name, st.Schedule, st.RetentionDays)
			}
			return err
		}
	default:
		return fmt.Errorf("unknown hub command %q; run \"tender hub help\"", name)
	}

	arg, err := parseWithName(fs, args)
	if err != nil {
		return err
	}
	cfg, err := hubConfig(*configPath, *database, "")
	if err != nil {
		return err
	}
	ctx := context.Background()
	d, err := openHub(ctx, cfg.Database)
	if err != nil {
		return err
	}
	return errors.Join(run(ctx, d, arg), d.Close())
}

// parseWithName parses flags and the one name argument, which may come before, between or after them.
func parseWithName(fs *flag.FlagSet, args []string) (string, error) {
	var names []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		names, args = append(names, fs.Arg(0)), fs.Args()[1:]
	}
	if len(names) != 1 {
		return "", fmt.Errorf("%s takes one name; run \"tender hub help\"", fs.Name())
	}
	return names[0], nil
}
