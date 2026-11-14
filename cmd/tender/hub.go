package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

var hubUsage = `Usage: tender hub [--database PATH] [--listen ADDR]
       tender hub help

` + wrap("Run the hub: it keeps every machine's state, serves the REST API and the MCP endpoint, and sends "+
	"alert emails. Run it as a service; \"sudo tender install hub\" sets that up and writes the settings to "+
	"/etc/tender/hub.env, which systemd reads. Manage the hub with the other tender commands, which talk to its "+
	"REST API.", lineWidth, "") + `

Settings, as environment variables:
` + table([]row{
	{config.EnvEncryptionKey, "Required. Encrypts the secrets in the database."},
	{"", "Make one with: openssl rand -base64 32"},
	{config.EnvListen, "Address to listen on (default: " + config.DefaultListen + ")"},
	{config.EnvDatabase, "SQLite database file (default: " + config.DefaultDatabase + ")"},
	{config.EnvSMTPHost, "SMTP server for alert emails. Email is off without it."},
	{config.EnvSMTPPort, "SMTP port, TLS from the first byte (default: 465)"},
	{config.EnvSMTPUsername, "SMTP login name, for servers that ask for one"},
	{config.EnvSMTPPassword, "SMTP password"},
	{config.EnvSMTPFrom, "Sender of alert emails, like Tender <alerts@example.com>."},
	{"", "Required with " + config.EnvSMTPHost + "."},
}) + `
Options:
` + table([]row{
	{"--database PATH", "SQLite database file; overrides " + config.EnvDatabase},
	{"--listen ADDR", "Address to listen on; overrides " + config.EnvListen},
}) + `
The hub has no login: it listens on 127.0.0.1 and is reached through Tailscale Serve or your
own proxy.
Docs: ` + docsURL + `
`

func runHub(args []string, log *slog.Logger) error {
	if len(args) == 1 && args[0] == "help" {
		_, err := fmt.Fprint(os.Stdout, hubUsage)
		return err
	}
	return serveHub(args, log)
}

// openHub opens the database and applies the migrations.
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

// newHub returns the hub's HTTP server with every domain's routes and the MCP endpoint. key is
// the encryption key and sender sends email; sender is nil when email is off.
func newHub(d *db.DB, key []byte, sender notifications.Sender, log *slog.Logger) *server.Server {
	ms := machines.NewStore(d)
	srv := server.New(d, log, ms)
	ms.Routes(srv)
	backups.NewStore(d, key).Routes(srv)
	notifications.NewStore(d).Routes(srv, sender, hubName())

	// The SDK handler answers GET and DELETE with 405 in stateless mode, so only POST is routed.
	tools := newTools(d, key, sender, log).Handler()
	srv.Handle("POST /mcp", srv.People(func(w http.ResponseWriter, r *http.Request) error {
		tools.ServeHTTP(w, r)
		return nil
	}))
	return srv
}

// newTools returns the MCP server with every domain's tools. The hub and "tender mcp" share it.
func newTools(d *db.DB, key []byte, sender notifications.Sender, log *slog.Logger) *mcp.Server {
	ms := machines.NewStore(d)
	s := mcp.New(version, log)
	ms.RegisterTools(s)
	backups.NewStore(d, key).RegisterTools(s, ms.Lookup)
	notifications.NewStore(d).RegisterTools(s, ms.Lookup, sender, hubName())
	return s
}

// mailSender returns the sender for smtp, or a nil Sender when email is off.
func mailSender(smtp *config.SMTP) notifications.Sender {
	if smtp == nil {
		return nil
	}
	return notifications.NewMailer(*smtp)
}

func serveHub(args []string, log *slog.Logger) error {
	fs := newFlagSet("hub", "[--database PATH] [--listen ADDR]", "")
	fs.Usage = func() { _, _ = fmt.Fprint(fs.Output(), hubUsage) }
	database := fs.String("database", "", "`PATH` the SQLite database")
	listen := fs.String("listen", "", "`ADDR` the address to listen on")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "hub takes no arguments, got %q", fs.Arg(0))
	}
	cfg, err := hubSettings(os.Getenv, *database, *listen)
	if err != nil {
		return err
	}
	sender := mailSender(cfg.SMTP)

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
	if err := backups.NewStore(d, cfg.EncryptionKey).CheckKey(ctx); err != nil {
		return fmt.Errorf("%s does not open the secrets in %s: %w. Start the hub with the key it stored them with; "+
			"without it they can't be read, and each destination needs its keys saved again", config.EnvEncryptionKey, cfg.Database, err)
	}

	hub := newHub(d, cfg.EncryptionKey, sender, log)
	ms := machines.NewStore(d)
	go hub.PruneKeys(ctx)
	go ms.PruneUsage(ctx, log)
	engine := notifications.NewEngine(notifications.NewStore(d), sender, hubName(), log)
	go engine.Run(ctx, loadMachines(ms, backups.NewStore(d, nil)))
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           hub,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("hub listening", "address", cfg.Listen, "database", cfg.Database, "version", version, "email", sender != nil)

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

// hubSettings reads the hub's environment and applies the flags, which win over it. An empty flag is not set.
func hubSettings(getenv func(string) string, database, listen string) (config.HubEnv, error) {
	cfg, err := config.LoadHubEnv(getenv)
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
