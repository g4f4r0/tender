// Command tender orchestrates restic backups across machines and serves their state to AI clients.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  tender hub [--database PATH] [--listen ADDR]   run the hub
  tender agent [--config PATH]                   run the agent
  tender mcp --stdio [--database PATH]           serve the MCP tools over stdio, on the hub machine
  tender version                                 print the version

Run "tender hub help" for the hub's other commands.
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: dropTime}))

	var err error
	switch os.Args[1] {
	case "hub":
		err = runHub(os.Args[2:], log)
	case "agent":
		err = runAgent(os.Args[2:], log)
	case "mcp":
		err = runMCP(os.Args[2:], log)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return // the flag package already printed the usage
	}
	if err != nil {
		log.Error("tender failed", "error", err)
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
