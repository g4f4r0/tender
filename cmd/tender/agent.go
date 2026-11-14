package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/machines"
)

func runAgent(args []string, log *slog.Logger) error {
	fs := newFlagSet("agent", "[--config PATH] [--state PATH]",
		"Run the agent: it backs up this machine on the schedule the hub sets and reports every run to the hub. "+
			"Run it as a service; \"sudo tender install agent\" sets that up.")
	configPath := fs.String("config", config.DefaultAgentPath, "`PATH` the agent config file")
	state := fs.String("state", defaultState(), "`PATH` the folder for the agent's state")
	if err := parseFlags(fs, args, os.Stdout); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "agent takes no arguments, got %q", fs.Arg(0))
	}

	cfg, err := config.LoadAgent(*configPath)
	if err != nil {
		return err
	}
	token, err := config.ReadCredential(config.CredentialsDir(), "hub-token")
	if err != nil {
		return err
	}
	hub := client.New(cfg.Hub, token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		postUsage(ctx, hub, cfg.Machine, log)
	}()

	a := backups.NewAgent(hub, cfg.Machine, *state, log)
	a.Paths, a.Exclude = cfg.Backups.Paths, cfg.Backups.Exclude
	if bin := os.Getenv("TENDER_RESTIC_BIN"); bin != "" {
		a.ResticBin = bin
	}
	log.Info("agent started", "machine", cfg.Machine, "hub", cfg.Hub, "version", version)
	a.Run(ctx)
	log.Info("agent stopping")
	wg.Wait()
	return nil
}

func defaultState() string {
	if d := os.Getenv("STATE_DIRECTORY"); d != "" {
		return d
	}
	return "/var/lib/tender/agent"
}

// postUsage sends a reading now and every 60 seconds until ctx ends. A missed reading is not queued.
func postUsage(ctx context.Context, hub *client.Client, machine string, log *slog.Logger) {
	path := "/v1/machines/" + url.PathEscape(machine) + "/usage"
	send := func() {
		in, err := machines.ReadUsage("/proc")
		if err != nil {
			log.Error("read usage", "error", err)
			return
		}
		if err := hub.Do(ctx, http.MethodPost, path, client.NewKey(), in, nil); err != nil && ctx.Err() == nil {
			log.Warn("could not send usage", "error", err)
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
