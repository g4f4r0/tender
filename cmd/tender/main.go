// Command tender orchestrates restic backups across machines and serves their state to AI clients.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var usage = `Tender backs up whole servers on a schedule and reports every run to a hub.
AI clients read the hub's state over MCP, read-only.

Usage: tender COMMAND [options]

Work with the hub, from any machine that can reach it:
` + table([]row{
	{"status", "Show the machines, their last backup and the open alerts"},
	{"machines COMMAND", "list, add, remove, rotate-token"},
	{"destinations COMMAND", "list, add, remove"},
	{"backups COMMAND", "status, set, unset"},
	{"recipients COMMAND", "list, add, remove"},
	{"email test", "Send a test email to every recipient"},
}) + `
Set up this machine (run as root):
` + table([]row{
	{"install agent --hub URL --machine NAME", "Install and start the agent"},
	{"install hub [--smtp-host HOST ...]", "Install and start the hub"},
	{"install refresh", "Restart the installed services on this binary"},
	{"update [--version vX.Y.Z]", "Update Tender to the latest release"},
}) + `
Run as services:
` + table([]row{
	{"hub [--listen ADDR] [--database PATH]", "Run the hub (its service does this)"},
	{"agent [--config PATH]", "Run the agent (its service does this)"},
	{"mcp --stdio [--database PATH]", "Serve the read-only MCP tools over stdio"},
}) + `
Other:
` + table([]row{
	{"version", "Print the version"},
	{"help", "Print this help"},
}) + `
Commands that talk to the hub take --hub URL, or read TENDER_HUB (default ` + defaultHub + `).
Run "tender COMMAND --help" for a command's options.
Docs: ` + docsURL + `
`

// hubCommands are the commands that talk to the hub over its REST API, and need no root.
var hubCommands = map[string]func([]string, session) error{
	"status":       runStatus,
	"machines":     runMachines,
	"destinations": runDestinations,
	"backups":      runBackups,
	"recipients":   runRecipients,
	"email":        runEmail,
}

// migrations lists every domain's migrations in dependency order: machines first.
func migrations() []db.Migrations {
	return []db.Migrations{machines.Migrations, backups.Migrations, notifications.Migrations, server.Migrations}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cleanVersion := strings.TrimPrefix(version, "v")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: dropTime}))

	var err error
	switch os.Args[1] {
	case "hub":
		err = runHub(os.Args[2:], log)
	case "agent":
		err = runAgent(os.Args[2:], log)
	case "mcp":
		err = runMCP(os.Args[2:], log)
	case "install":
		err = runInstall(os.Args[2:], os.Stdin, newSession())
	case "update":
		err = runUpdate(os.Args[2:], cleanVersion, os.Stdout)
	case "version":
		fmt.Println("Tender " + cleanVersion)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		run, ok := hubCommands[os.Args[1]]
		if !ok {
			fmt.Fprintf(os.Stderr, "tender: unknown command %q. Run \"tender help\" for the commands.\n", os.Args[1])
			os.Exit(2)
		}
		err = run(os.Args[2:], newSession())
	}
	if errors.Is(err, flag.ErrHelp) {
		return // the usage is printed
	}
	if errors.Is(err, errUsageShown) {
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tender:", err)
		os.Exit(1)
	}
}

// dropTime removes slog's timestamp; journald records its own.
func dropTime(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && len(groups) == 0 {
		return slog.Attr{}
	}
	return a
}
