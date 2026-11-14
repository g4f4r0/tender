package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
)

// newMailer returns the mailer for cfg, with the SMTP password from the credential smtp-password.
func newMailer(cfg config.Email) (*notifications.Mailer, error) {
	password, err := config.ReadCredential(config.CredentialsDir(), "smtp-password")
	if err != nil {
		return nil, err
	}
	return notifications.NewMailer(cfg, password), nil
}

// hubName is the host name that emails say they were sent from.
func hubName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// loadMachines returns what the notification rules need to know about every machine, from the
// machines and backups stores. The notifications domain imports neither.
func loadMachines(ms *machines.Store, bs *backups.Store) func(context.Context) ([]notifications.Machine, error) {
	return func(ctx context.Context) ([]notifications.Machine, error) {
		overviews, err := ms.List(ctx)
		if err != nil {
			return nil, err
		}
		summaries, err := bs.Summaries(ctx)
		if err != nil {
			return nil, err
		}
		completed, err := bs.LastCompleted(ctx)
		if err != nil {
			return nil, err
		}
		finished, err := bs.LastFinished(ctx)
		if err != nil {
			return nil, err
		}
		byName := map[string]backups.Summary{}
		for _, s := range summaries {
			byName[s.Machine] = s
		}
		out := make([]notifications.Machine, len(overviews))
		for i, o := range overviews {
			m := notifications.Machine{ID: o.ID, Name: o.Name, CreatedAt: o.CreatedAt, LastSeen: o.LastSeen, LastCompletedBackup: completed[o.Name]}
			if o.Usage != nil {
				m.DiskUsedPercent = o.Usage.DiskUsedPercent
			}
			s := byName[o.Name]
			if s.Settings != nil {
				m.HasBackupSettings, m.BackupSettingsCreatedAt = true, s.Settings.CreatedAt
			}
			if h, ok := finished[o.Name]; ok {
				m.LastBackup = &notifications.Run{Status: h.Status, Time: h.CreatedAt}
				if h.Error != nil {
					m.LastBackup.Error = *h.Error
				}
			}
			out[i] = m
		}
		return out, nil
	}
}

// testEmail is "tender hub test-email": it sends the test message and says where it went.
func testEmail(args []string, out io.Writer) error {
	fs := newFlagSet("hub test-email", "[--config PATH]",
		"Send a test email through the hub's SMTP settings, to check that alerts will arrive.")
	configPath := fs.String("config", "", "`PATH` the hub config file (default: "+config.DefaultHubPath+")")
	if err := parseFlags(fs, args, out); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usageError(fs, "test-email takes no arguments, got %q", fs.Arg(0))
	}
	cfg, err := config.LoadHub(*configPath)
	if err != nil {
		return err
	}
	if cfg.Email == nil {
		return errors.New("the hub config has no [email] table, so there is nothing to test. Add one (docs: " + docsURL + "), or run \"sudo tender install hub --force\" with the --email and --smtp options")
	}
	if err := dropToDatabaseOwner(cfg.Database); err != nil {
		return err
	}
	mailer, err := newMailer(*cfg.Email)
	if err != nil {
		return err
	}
	msg, err := notifications.TestMessage(hubName())
	if err != nil {
		return err
	}
	if err := mailer.Send(context.Background(), msg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Sent the test email to %s through %s:%d\n", strings.Join(mailer.To(), ", "), cfg.Email.Host, cfg.Email.Port)
	return err
}
