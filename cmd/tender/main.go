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

const usage = `tender backs up whole servers on a schedule and reports to a hub.

Usage:
  tender hub [--config PATH] [--database PATH] [--listen ADDR]   run the hub
  tender agent [--config PATH]                   run the agent
  tender mcp --stdio [--database PATH]           serve the MCP tools over stdio, on the hub machine
  tender install agent|hub|credential|refresh    set up this machine, as root
  tender update [--version vX.Y.Z]               update tender to the latest release, as root
  tender version                                 print the version
  tender help                                    print this help

Run "tender hub help" for the hub's other commands and "tender install" for the install options.
Docs: https://github.com/g4f4r0/tender
`

// migrations lists every domain's migrations in dependency order: machines first.
func migrations() []db.Migrations {
	return []db.Migrations{machines.Migrations, backups.Migrations, notifications.Migrations, server.Migrations}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
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
		err = runInstall(os.Args[2:], os.Stdin, os.Stdout)
	case "update":
		err = runUpdate(os.Args[2:], cleanVersion, os.Stdout)
	case "version":
		fmt.Println("tender " + cleanVersion)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "tender: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return // the flag package already printed the usage
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
