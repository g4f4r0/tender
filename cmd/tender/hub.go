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

var hubUsage = `Usage: tender hub [--config PATH] [--database PATH] [--listen ADDR]
       tender hub COMMAND [options]

Run the hub, or manage what it knows about machines, destinations and backups. With no command,
tender hub runs the hub: it keeps every machine's state, serves the REST API and the MCP endpoint,
and sends alerts.

Commands:
` + table([]row{
	{"add-machine NAME", "Add a machine and print its token once"},
	{"rotate-token NAME", "Replace a machine's token and print the new one"},
	{"add-destination NAME", "Add a place to send backups: an S3 bucket"},
	{"set-backup MACHINE", "Set a machine's destination, schedule and retention"},
	{"test-email", "Send a test alert email"},
	{"help", "Print this help"},
}) + `
Run "tender hub COMMAND --help" for a command's options. Every command reads the hub config
(` + config.DefaultHubPath + `, optional) and takes --config PATH and --database PATH.
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
		return nil, fmt.Errorf("cannot open the hub database %s. Check that its folder exists and that you can write to it, or pass --database PATH: %w", path, err)
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
	fs := newFlagSet("hub", "[--config PATH] [--database PATH] [--listen ADDR]",
		"Run the hub: it keeps every machine's state, serves the REST API and the MCP endpoint, and sends alerts. "+
			"Run it as a service; \"sudo tender install hub\" sets that up. "+
			"\"tender hub help\" lists the administration commands.")
	configPath := fs.String("config", "", "`PATH` the hub config file (default: "+config.DefaultHubPath+")")
	database := fs.String("database", "", "`PATH` the SQLite database (default: "+config.DefaultDatabase+")")
	listen := fs.String("listen", "", "`ADDR` the address to listen on (default: "+config.DefaultListen+")")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "hub takes no arguments, got %q", fs.Arg(0))
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
		return fmt.Errorf("the hub stopped. Check that nothing else uses %s, or pass --listen ADDR: %w", cfg.Listen, err)
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
	var fs *flag.FlagSet
	var run func(ctx context.Context, d *db.DB, arg string) error
	var what string        // what the name argument names
	var check func() error // rejects missing options before the database opens

	switch name {
	case "add-machine":
		fs, what = hubFlags(name, "NAME",
			"Add a machine to the hub and print its token once. Install the token on that machine with \"tender install agent\". "+
				"A name is lowercase letters, digits and hyphens."), "a machine name"
		run = func(ctx context.Context, d *db.DB, machine string) error {
			token, err := machines.NewStore(d).Create(ctx, machine)
			if err == nil {
				_, err = fmt.Fprintln(out, token)
			}
			return err
		}
	case "rotate-token":
		fs, what = hubFlags(name, "NAME",
			"Replace a machine's token and print the new one. The old token stops working at once, "+
				"so install the new one on the machine."), "a machine name"
		run = func(ctx context.Context, d *db.DB, machine string) error {
			token, err := machines.NewStore(d).RotateToken(ctx, machine)
			if err == nil {
				_, err = fmt.Fprintln(out, token)
			}
			return err
		}
	case "add-destination":
		fs, what = hubFlags(name, "NAME --type s3 --endpoint URL --bucket BUCKET [--path PATH]",
			"Add a place to send backups: an S3-compatible bucket, such as Cloudflare R2. "+
				"Each machine gets its own folder in it. The keys and the repository password stay on each machine; "+
				"save them there with \"tender install credential\"."), "a destination name"
		var in backups.Destination
		fs.StringVar(&in.Type, "type", "", "`TYPE` the kind of destination: s3")
		fs.StringVar(&in.Endpoint, "endpoint", "", "`URL` the S3 endpoint, such as https://ACCOUNT.r2.cloudflarestorage.com")
		fs.StringVar(&in.Bucket, "bucket", "", "`BUCKET` the bucket name")
		fs.StringVar(&in.Path, "path", "", "`PATH` a folder inside the bucket (default: the bucket's top)")
		check = func() error {
			if in.Type == "" || in.Endpoint == "" || in.Bucket == "" {
				return usageError(fs, "add-destination needs --type, --endpoint and --bucket")
			}
			return nil
		}
		run = func(ctx context.Context, d *db.DB, destination string) error {
			in.Name = destination
			added, err := backups.NewStore(d).AddDestination(ctx, in)
			if err == nil {
				_, err = fmt.Fprintf(out, "Added destination %s. Each machine backs up to %s\n", added.Name, added.Repository("<machine>"))
			}
			return err
		}
	case "set-backup":
		fs, what = hubFlags(name, "MACHINE --destination NAME --schedule CRON --retention-days DAYS",
			"Set where, when and for how long a machine's backups are kept: a destination, a cron schedule in UTC "+
				"and the days to keep snapshots."), "a machine name"
		destination := fs.String("destination", "", "`NAME` the destination to back up to")
		schedule := fs.String("schedule", "", "`CRON` the schedule in UTC, such as \"30 3 * * *\"")
		retention := fs.Int("retention-days", 0, "`DAYS` how long to keep snapshots")
		check = func() error {
			if *destination == "" || *schedule == "" || *retention == 0 {
				return usageError(fs, "set-backup needs --destination, --schedule and --retention-days")
			}
			return nil
		}
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
		return fmt.Errorf("unknown hub command %q. Run \"tender hub help\" for the commands.", name)
	}

	arg, err := parseWithName(fs, args, what, out)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	cfg, err := hubConfig(fs.Lookup("config").Value.String(), fs.Lookup("database").Value.String(), "")
	if err != nil {
		return err
	}
	if err := dropToDatabaseOwner(cfg.Database); err != nil {
		return err
	}
	ctx := context.Background()
	d, err := openHub(ctx, cfg.Database)
	if err != nil {
		return err
	}
	return errors.Join(hubError(run(ctx, d, arg)), d.Close())
}

// hubFlags returns the FlagSet of "tender hub NAME" with the options every hub command has.
func hubFlags(name, args, summary string) *flag.FlagSet {
	fs := newFlagSet("hub "+name, args, summary)
	fs.String("config", "", "`PATH` the hub config file (default: "+config.DefaultHubPath+")")
	fs.String("database", "", "`PATH` the SQLite database (default: "+config.DefaultDatabase+")")
	return fs
}

// parseWithName parses flags and the one name argument, which may come before, between or after
// them. what says what the name is, for the error.
func parseWithName(fs *flag.FlagSet, args []string, what string, out io.Writer) (string, error) {
	var names []string
	for {
		if err := parseFlags(fs, args, out); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		names, args = append(names, fs.Arg(0)), fs.Args()[1:]
	}
	command := strings.TrimPrefix(fs.Name(), "hub ")
	switch len(names) {
	case 1:
		return names[0], nil
	case 0:
		return "", usageError(fs, "%s needs %s", command, what)
	default:
		return "", usageError(fs, "%s takes one name, got %d: %s", command, len(names), strings.Join(names, " "))
	}
}

// hubError words a store's answer for the command line: the API's sentence, plus what to do.
func hubError(err error) error {
	var e *server.Error
	if !errors.As(err, &e) {
		return err
	}
	msg := strings.TrimSuffix(e.Detail, ".")
	msg = strings.ToLower(msg[:1]) + msg[1:]
	switch {
	case strings.Contains(msg, "doesn't exist"):
		return fmt.Errorf("%s. Check the spelling, or add it first. Run \"tender hub help\" for the commands.", msg)
	case e.Status == http.StatusConflict:
		return fmt.Errorf("%s. Choose another name.", msg)
	default:
		return fmt.Errorf("%s. Fix it and run the command again.", msg)
	}
}
