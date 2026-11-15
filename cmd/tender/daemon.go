package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/security"
	"github.com/g4f4r0/tender/internal/services"
	"github.com/g4f4r0/tender/internal/updates"
)

func runDaemon(args []string, log *slog.Logger) error {
	fs := newFlagSet("daemon", "[--config PATH] [--state PATH] [--check]",
		"Run the daemon: it backs up this machine on the schedule the hub sets and reports every run to the hub, "+
			"reports this machine's Docker services and security checks, and runs the actions the hub queues: service starts, "+
			"stops, restarts and logs, reads of this machine's journal with journalctl, tests of backup destinations, upgrades "+
			"of its packages and updates of Tender. It sends the journal and the containers' logs to the log sinks the hub sets. "+
			"It runs as root. It reaches Docker at "+
			services.DefaultSocket+", or at the socket in TENDER_DOCKER_SOCKET. "+
			"Run it as a service; \"sudo tender install daemon\" sets that up.")
	configPath := fs.String("config", config.DefaultDaemonPath, "`PATH` the daemon config file")
	state := fs.String("state", defaultState(), "`PATH` the folder for the daemon's state")
	check := fs.Bool("check", false, "check that the config, the hub token and the state folder can be read, and exit")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "daemon takes no arguments, got %q", fs.Arg(0))
	}

	cfg, err := config.LoadDaemon(*configPath)
	if err != nil {
		return err
	}
	dir := config.CredentialsDir()
	token, err := config.ReadCredential(dir, "hub-token")
	if err != nil {
		return fmt.Errorf("cannot read the hub token in %s. Run \"sudo tender install daemon --token-file PATH\" to save it.", dir)
	}
	if *check {
		if fi, err := os.Stat(*state); err != nil || !fi.IsDir() {
			return fmt.Errorf("cannot find the daemon's state folder %s. Run \"sudo tender install refresh\" to make it", *state)
		}
		fmt.Println("The daemon's config, hub token and state folder are in order.")
		return nil
	}
	hub := client.New(cfg.Hub, token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		postUsage(ctx, hub, cfg.Machine, *state, log)
	}()

	socket := os.Getenv("TENDER_DOCKER_SOCKET")
	if socket == "" {
		socket = services.DefaultSocket
	}
	sd := services.NewDaemon(hub, cfg.Machine, socket, log)
	sd.StateDir = *state
	a := backups.NewDaemon(hub, cfg.Machine, *state, log)
	a.Paths, a.Exclude, a.Busy = cfg.Backups.Paths, cfg.Backups.Exclude, busy(*state)
	if bin := os.Getenv("TENDER_RESTIC_BIN"); bin != "" {
		a.ResticBin = bin
	}
	if dir := os.Getenv(envSSHDir); dir != "" {
		a.Host.SSHDir = dir
	}
	if file := os.Getenv(envRcloneConfig); file != "" {
		a.Host.RcloneConfig = file
	}
	root := config.Root()
	collector := &security.Collector{Root: root, Published: publishedPorts(services.NewDocker(socket))}
	sec := security.NewDaemon(hub, cfg.Machine, collector, log)
	ad := actions.NewDaemon(hub, cfg.Machine, log)
	ad.Services, ad.Backups, ad.StateDir, ad.Busy = sd, a, *state, busy(*state)
	ad.System = systemActions{Job: updates.Job{Binary: filepath.Join(root, "/usr/local/bin/tender"), StateDir: *state}, collect: sec.Collect}
	ld := logs.NewDaemon(hub, cfg.Machine, *state, socket, log)
	wg.Add(4)
	go func() {
		defer wg.Done()
		ld.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		sec.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		sd.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		ad.Run(ctx)
	}()

	log.Info("daemon started", "machine", cfg.Machine, "hub", cfg.Hub, "version", version)
	a.Run(ctx)
	log.Info("daemon stopping")
	wg.Wait()
	return nil
}

// The folders a destination reads on the daemon's machine, for tests and unusual installs: the SSH
// key folder of sftp destinations and rclone's config file.
const (
	envSSHDir       = "TENDER_SSH_DIR"
	envRcloneConfig = "TENDER_RCLONE_CONFIG"
)

func defaultState() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return d
	}
	return "/var/lib/tender/daemon"
}

// sendUsage reads this machine's usage and sends it to the hub, with the version of Tender.
func sendUsage(ctx context.Context, hub *client.Client, machine string) error {
	in, err := machines.ReadUsage("/proc")
	if err != nil {
		return fmt.Errorf("read usage: %w", err)
	}
	in.Version = daemonVersion()
	return hub.Do(ctx, http.MethodPost, "/v1/machines/"+url.PathEscape(machine)+"/usage", client.NewKey(), in, nil)
}

// postUsage sends a reading now and every 60 seconds until ctx ends. A missed reading is not queued.
// The first reading the hub takes leaves the reported-version marker in state, which tells an update
// job that this daemon reached the hub on the new version.
func postUsage(ctx context.Context, hub *client.Client, machine, state string, log *slog.Logger) {
	reported := false
	send := func() {
		err := sendUsage(ctx, hub, machine)
		if err != nil && ctx.Err() == nil {
			log.Warn("could not send usage", "error", err)
		}
		if err == nil && !reported {
			if werr := config.WriteReportedVersion(state, daemonVersion()); werr != nil {
				log.Error("save the reported version", "error", werr)
			}
			reported = true
		}
	}
	send()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}
