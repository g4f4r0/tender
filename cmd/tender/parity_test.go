package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/server"
)

// mcpCoverage maps every route people may call to the MCP tool that does the same, so an AI client
// can do whatever a person can. A read of one item is covered by the list tool that returns the
// same objects. A value starting with "none: " says why no tool does it. Daemon-only routes are
// the machines' own and need no entry. docs/mcp.md shows this table.
var mcpCoverage = map[string]string{
	"GET /v1/machines":                                  "list_machines",
	"POST /v1/machines":                                 "add_machine",
	"GET /v1/machines/{machine}":                        "list_machines",
	"DELETE /v1/machines/{machine}":                     "remove_machine",
	"POST /v1/machines/{machine}/token":                 "rotate_machine_token",
	"GET /v1/machines/{machine}/usage":                  "get_machine_usage",
	"GET /v1/backups":                                   "get_backup_status",
	"GET /v1/machines/{machine}/backups":                "get_backup_status",
	"GET /v1/machines/{machine}/backups/settings":       "get_backup_status",
	"PUT /v1/machines/{machine}/backups/settings":       "set_backup",
	"DELETE /v1/machines/{machine}/backups/settings":    "unset_backup",
	"GET /v1/machines/{machine}/backups/snapshots":      "list_backup_snapshots",
	"GET /v1/machines/{machine}/backups/snapshots/{id}": "list_backup_snapshots",
	"GET /v1/machines/{machine}/backups/history":        "list_backup_history",
	"GET /v1/machines/{machine}/backups/history/{id}":   "list_backup_history",
	"GET /v1/backups/destinations":                      "list_destinations",
	"POST /v1/backups/destinations":                     "add_destination",
	"GET /v1/backups/destinations/{destination}":        "list_destinations",
	"PATCH /v1/backups/destinations/{destination}":      "update_destination",
	"DELETE /v1/backups/destinations/{destination}":     "remove_destination",
	"GET /v1/notifications":                             "list_notifications",
	"GET /v1/machines/{machine}/notifications":          "list_notifications",
	"GET /v1/machines/{machine}/notifications/{id}":     "list_notifications",
	"GET /v1/notifications/channels":                    "list_channels",
	"POST /v1/notifications/channels":                   "add_channel",
	"GET /v1/notifications/channels/{channel}":          "list_channels",
	"PATCH /v1/notifications/channels/{channel}":        "update_channel",
	"DELETE /v1/notifications/channels/{channel}":       "remove_channel",
	"POST /v1/notifications/test":                       "send_test_notification",
	"GET /v1/services":                                  "list_services",
	"GET /v1/machines/{machine}/services":               "list_services",
	"GET /v1/machines/{machine}/services/{service}":     "list_services",
	"PATCH /v1/machines/{machine}/services/{service}":   "set_service_alerts",
	"POST /v1/machines/{machine}/actions":               "start_service, stop_service, restart_service, get_service_logs, get_machine_logs",
	"GET /v1/machines/{machine}/actions":                "list_actions",
	"GET /v1/machines/{machine}/actions/{id}":           "get_action",
	"POST /mcp": "none: it is the MCP endpoint itself",
}

func toolNames(t *testing.T) []string {
	t.Helper()
	list, err := connect(t, seeded(t)).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// peopleRoutes are the routes people may call, as the hub registers them.
func peopleRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range newHub(seeded(t), testKey, nil, quiet).Routes() {
		if r.Caller == "" {
			t.Errorf("%s is registered without People, Daemon or DaemonOrPeople", r.Pattern)
		}
		if r.Caller != server.CallerDaemon {
			out = append(out, r.Pattern)
		}
	}
	return out
}

// Every route people may call has an MCP tool, or a reason it has none, and every tool named
// exists. A new route fails here until it is covered.
func TestEveryRouteHasATool(t *testing.T) {
	tools, routes := toolNames(t), peopleRoutes(t)
	for _, pattern := range routes {
		cov, ok := mcpCoverage[pattern]
		switch {
		case !ok:
			t.Errorf("%s has no MCP tool: add one, or an entry in mcpCoverage saying why not", pattern)
		case strings.HasPrefix(cov, "none: "):
		default:
			for _, tool := range strings.Split(cov, ", ") {
				if !slices.Contains(tools, tool) {
					t.Errorf("%s: no MCP tool %s", pattern, tool)
				}
			}
		}
	}
	for pattern := range mcpCoverage {
		if !slices.Contains(routes, pattern) {
			t.Errorf("mcpCoverage has %s, which isn't a route people may call", pattern)
		}
	}
	// Every tool covers some route, so none does what the REST API can't.
	for _, tool := range tools {
		if !slices.ContainsFunc(routes, func(p string) bool { return slices.Contains(strings.Split(mcpCoverage[p], ", "), tool) }) {
			t.Errorf("the tool %s covers no route in mcpCoverage", tool)
		}
	}
}

// The docs list every route people may call, every MCP tool and the coverage table, so they
// can't drift from the code.
func TestDocsCoverEveryRouteAndTool(t *testing.T) {
	api, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	mcpDoc, err := os.ReadFile("../../docs/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range peopleRoutes(t) {
		if pattern == "POST /mcp" {
			continue
		}
		if !strings.Contains(string(api), "`"+pattern+"`") {
			t.Errorf("docs/api.md doesn't describe `%s`", pattern)
		}
		if row := "| `" + pattern + "` |"; !strings.Contains(string(mcpDoc), row) {
			t.Errorf("docs/mcp.md's coverage table has no row for `%s`", pattern)
		}
	}
	for _, tool := range toolNames(t) {
		if !strings.Contains(string(mcpDoc), "### `"+tool+"`") {
			t.Errorf("docs/mcp.md has no section ### `%s`", tool)
		}
	}
}
