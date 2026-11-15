// Command tender orchestrates restic backups across machines and serves their state to AI clients.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/logs"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var usage = `Tender backs up whole servers on a schedule and reports every run to a hub.
It also watches and controls each machine's Docker services.
AI clients can check on it and manage it over MCP.

Usage: tender COMMAND [options]

Work with the hub, from any machine that can reach it:
` + table([]row{
	{"status", "Show the machines, their last backup, services and open problems"},
	{"machines COMMAND", "list, add, remove, rotate-token"},
	{"destinations COMMAND", "list, add, update, test, remove"},
	{"backups COMMAND", "status, set, unset"},
	{"services COMMAND", "list, start, stop, restart, logs, alerts"},
	{"logs MACHINE [--unit U ...]", "Print a machine's journal"},
	{"actions COMMAND", "list, show"},
	{"sinks COMMAND", "list, add, show, update, remove"},
	{"channels COMMAND", "list, add, show, update, remove, test"},
	{"notifications COMMAND", "list"},
}) + `
Set up this machine (run as root):
` + table([]row{
	{"install daemon --hub URL --machine NAME", "Install and start the daemon"},
	{"install hub [--smtp-host HOST ...]", "Install and start the hub"},
	{"install refresh", "Restart the installed services on this binary"},
	{"update [--version vX.Y.Z]", "Update Tender to the latest release"},
}) + `
Run as services:
` + table([]row{
	{"hub [--listen ADDR] [--database PATH]", "Run the hub (its service does this)"},
	{"daemon [--config PATH]", "Run the daemon (its service does this)"},
	{"mcp --stdio [--database PATH]", "Serve the MCP tools over stdio, on the hub machine"},
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
	"status":        runStatus,
	"machines":      runMachines,
	"destinations":  runDestinations,
	"backups":       runBackups,
	"services":      runServices,
	"logs":          runLogs,
	"actions":       runActions,
	"sinks":         runSinks,
	"channels":      runChannels,
	"notifications": runNotifications,
}

// migrations lists every domain's migrations in dependency order: machines first.
func migrations() []db.Migrations {
	return []db.Migrations{machines.Migrations, backups.Migrations, notifications.Migrations, services.Migrations, actions.Migrations, logs.Migrations, server.Migrations}
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
	case "daemon", "agent": // "agent" is what tender-agent.service, from v0.2.1 and earlier, runs until it is moved
		err = runDaemon(os.Args[2:], log)
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
