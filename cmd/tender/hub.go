package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

const defaultDatabase = config.DefaultDatabase

var hubUsage = `Usage: tender hub [--database PATH] [--listen ADDR]
       tender hub help

` + wrap("Run the hub: it keeps every machine's state, serves the REST API and the MCP endpoint, and sends "+
	"alerts to the notification channels. Run it as a service; \"sudo tender install hub\" sets that up and writes the settings to "+
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
	{config.EnvHubURL, "The hub's URL, which notifications link to (default: http://HOSTNAME)"},
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
	if err := writable(path); err != nil {
		return nil, fmt.Errorf("the hub can't write its database %s: %w. Its service may only write in %s: keep the database there, "+
			"or let it write that folder with ReadWritePaths= in a drop-in, made with: sudo systemctl edit tenderhub", path, err, filepath.Dir(config.DefaultDatabase))
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open the hub database %s. Check that its folder exists and that you can write to it, or pass --database PATH: %w", path, err)
	}
	if err := d.Migrate(ctx, migrations()...); err != nil {
		return nil, errors.Join(err, d.Close())
	}
	return d, nil
}

// writable is an error unless this process may write path, and its folder, for the WAL beside it.
// A folder that doesn't exist yet is left to db.Open to report.
func writable(path string) error {
	const w = 2 // W_OK
	for _, p := range []string{filepath.Dir(path), path} {
		err := syscall.Access(p, w)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// newHub returns the hub's HTTP server with every domain's routes and the MCP endpoint. key is
// the encryption key and sender sends email; sender is nil when email is off.
func newHub(d *db.DB, key []byte, sender notifications.Sender, log *slog.Logger) *server.Server {
	ms := machines.NewStore(d)
	srv := server.New(d, log, ms)
	ms.Routes(srv)
	backups.NewStore(d, key).Routes(srv)
	notifications.NewStore(d, key).Routes(srv, sender, hubIdentity())
	services.NewStore(d).Routes(srv)
	as := newActions(d, key)
	as.Routes(srv)
	logs.NewStore(d, key).Routes(srv)
	newSecurity(d, ms).Routes(srv)
	newUpdates(d, ms, as).Routes(srv)
	newCloudflareStore(d, key).Routes(srv)
	srv.Handle("GET /metrics", srv.People(metricsRoute(metricSources(d), time.Now)))

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
	notifications.NewStore(d, key).RegisterTools(s, ms.Lookup, sender, hubIdentity())
	services.NewStore(d).RegisterTools(s, ms.Lookup)
	as := newActions(d, key)
	as.RegisterTools(s, ms.Lookup)
	logs.NewStore(d, key).RegisterTools(s)
	newSecurity(d, ms).RegisterTools(s, ms.Lookup)
	newUpdates(d, ms, as).RegisterTools(s)
	newCloudflareStore(d, key).RegisterTools(s)
	return s
}

// newActions returns the actions store, which hears about services from the services store and
// hands the destination of a test, opened with key, to the daemon that runs it.
func newActions(d *db.DB, key []byte) *actions.Store {
	as := actions.NewStore(d, services.NewStore(d))
	as.Destinations = destinationTargets{backups.NewStore(d, key)}
	if keep, err := logsKeep(os.Getenv); err == nil {
		as.Keep = keep
	}
	return as
}

// destinationTargets gives the actions domain the backups destinations it tests.
type destinationTargets struct{ s *backups.Store }

func (t destinationTargets) Check(ctx context.Context, name string) error {
	_, err := t.s.Destination(ctx, name)
	return err
}

func (t destinationTargets) Target(ctx context.Context, name string) (json.RawMessage, error) {
	tg, err := t.s.Target(ctx, name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tg)
}

// envLogsKeep overrides how long a logs action keeps its output, for tests only.
const envLogsKeep = "TENDER_LOGS_KEEP"

// logsKeep is how long a logs action keeps its output: an hour, or envLogsKeep.
func logsKeep(getenv func(string) string) (time.Duration, error) {
	v := getenv(envLogsKeep)
	if v == "" {
		return actions.DefaultKeep, nil
	}
	keep, err := time.ParseDuration(v)
	if err != nil || keep <= 0 {
		return 0, fmt.Errorf("%s must be a duration such as 1h, got %q", envLogsKeep, v)
	}
	return keep, nil
}

// Settings for tests only: the folder of the exec adapters, and how long a channel's deliveries
// fail before channel.failing fires.
const (
	envTestAdaptersDir = "TENDER_TEST_ADAPTERS_DIR"
	envFailingAfter    = "TENDER_CHANNEL_FAILING_AFTER"
)

// testAdaptersDir sets the exec adapters' folder from TENDER_TEST_ADAPTERS_DIR, and warns when
// it is set: its programs need not be root's, which is for tests only.
func testAdaptersDir(getenv func(string) string, log *slog.Logger) {
	notifications.AdaptersDir = getenv(envTestAdaptersDir)
	if notifications.AdaptersDir != "" {
		log.Warn("exec adapters run from a test folder whose programs need not belong to root. Unset "+envTestAdaptersDir+
			" outside tests, so only root's programs in /etc/tender/adapters run", "folder", notifications.AdaptersDir)
	}
}

// failingAfter is how long a channel's deliveries fail before channel.failing: an hour, or envFailingAfter.
func failingAfter(getenv func(string) string) (time.Duration, error) {
	v := getenv(envFailingAfter)
	if v == "" {
		return time.Hour, nil
	}
	after, err := time.ParseDuration(v)
	if err != nil || after <= 0 {
		return 0, fmt.Errorf("%s must be a duration such as 1h, got %q", envFailingAfter, v)
	}
	return after, nil
}

// checkKey is an error unless key opens every secret stored in the database at path.
func checkKey(ctx context.Context, d *db.DB, key []byte, path string) error {
	if err := backups.NewStore(d, key).CheckKey(ctx); err != nil {
		return fmt.Errorf("%s does not open the secrets in %s: %w. Use the key they were stored with, from /etc/tender/hub.env; "+
			"without it they can't be read, and each destination needs its keys saved again", config.EnvEncryptionKey, path, err)
	}
	if err := notifications.NewStore(d, key).CheckKey(ctx); err != nil {
		return fmt.Errorf("%s does not open the secrets in %s: %w. Use the key they were stored with, from /etc/tender/hub.env; "+
			"without it they can't be read, and each channel with a secret needs it saved again", config.EnvEncryptionKey, path, err)
	}
	if err := logs.NewStore(d, key).CheckKey(ctx); err != nil {
		return fmt.Errorf("%s does not open the secrets in %s: %w. Use the key they were stored with, from /etc/tender/hub.env; "+
			"without it they can't be read, and each log sink needs its secrets saved again", config.EnvEncryptionKey, path, err)
	}
	if err := newCloudflareStore(d, key).CheckKey(ctx); err != nil {
		return fmt.Errorf("%s does not open the secrets in %s: %w. Use the key they were stored with, from /etc/tender/hub.env; "+
			"without it they can't be read, and the Cloudflare API token needs saving again", config.EnvEncryptionKey, path, err)
	}
	return nil
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
	if _, err := logsKeep(os.Getenv); err != nil {
		return err
	}
	if _, err := failingAfter(os.Getenv); err != nil {
		return err
	}
	testAdaptersDir(os.Getenv, log)
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
	if err := checkKey(ctx, d, cfg.EncryptionKey, cfg.Database); err != nil {
		return err
	}

	hub := newHub(d, cfg.EncryptionKey, sender, log)
	ms := machines.NewStore(d)
	go hub.PruneKeys(ctx)
	go ms.PruneUsage(ctx, log)
	as := newActions(d, nil)
	go as.Sweep(ctx, log)
	up := newUpdates(d, ms, as)
	go up.Run(ctx, log)
	engine := notifications.NewEngine(notifications.NewStore(d, cfg.EncryptionKey), sender, hubIdentity(), log)
	if after, err := failingAfter(os.Getenv); err == nil {
		engine.FailingAfter = after
	}
	go engine.Run(ctx, loadMachines(ms, backups.NewStore(d, nil), services.NewStore(d), logs.NewStore(d, nil), newSecurity(d, ms), up))
	// No write timeout: a daemon's long poll for actions holds its request up to 30 s.
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Guard(hub, cfg.Hosts, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	srv.RegisterOnShutdown(hub.StopWaiting)
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
