package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/server"
)

// How long a command waits for the daemon to finish an action, and how often it asks. Tests shorten them.
var (
	actionWait  = actions.Wait
	actionCheck = time.Second
)

var actionsUsage = groupUsage("actions", "See what was asked of a machine's daemon: service starts, stops, restarts and logs, "+
	"and reads of its journal. The hub keeps each machine's last 100.", []row{
	{"list MACHINE", "List a machine's actions, newest first"},
	{"show MACHINE ID", "Show one action, with its output"},
})

// runActions runs "tender actions list|show".
func runActions(args []string, s session) error {
	return runGroup("actions", actionsUsage, args, s.out, map[string]func([]string) error{
		"list": func(a []string) error { return actionsList(a, s) },
		"show": func(a []string) error { return actionsShow(a, s) },
	})
}

func actionsPath(machine string, id int64) string {
	return machinePath(machine, "/actions/"+strconv.FormatInt(id, 10))
}

// queueAction queues an action and waits for the daemon to finish it. A failed action is an error
// with the daemon's reason; one still waiting after actionWait is an error that says how to check.
func queueAction(hub *hubAPI, machine string, in actions.Input) (actions.Action, error) {
	var a actions.Action
	if err := hub.do(http.MethodPost, machinePath(machine, "/actions"), in, &a); err != nil {
		return a, err
	}
	return awaitAction(hub, machine, a)
}

// awaitAction waits for the daemon to finish a queued action, as queueAction says.
func awaitAction(hub *hubAPI, machine string, a actions.Action) (actions.Action, error) {
	deadline := time.Now().Add(actionWait)
	for !a.Finished() && time.Now().Before(deadline) {
		time.Sleep(min(actionCheck, time.Until(deadline)))
		if err := hub.do(http.MethodGet, actionsPath(machine, a.ID), nil, &a); err != nil {
			return a, err
		}
	}
	switch {
	case a.Status == actions.StatusFailed && a.Output != nil:
		return a, errors.New(*a.Output)
	case a.Status == actions.StatusFailed:
		return a, fmt.Errorf("the daemon on %s could not do this.", machine)
	case a.Finished():
		return a, nil
	}
	state := "is still pending: the daemon hasn't picked it up. It runs if the daemon picks it up within 15 minutes of being asked, and fails otherwise."
	if a.Status == actions.StatusInProgress {
		state = "is in progress: the daemon started it. The hub fails it if the daemon doesn't say how it ended within 15 minutes."
	}
	return a, fmt.Errorf("the daemon on %s hasn't finished within %d seconds. Action %d %s Check it with: tender actions show %s %d",
		machine, int(actionWait.Seconds()), a.ID, state, machine, a.ID)
}

// printLogs prints a logs action's output, or says there is none.
func printLogs(s session, a actions.Action, what string) error {
	if a.Output == nil || *a.Output == "" {
		say(s.out, "%s has no logs.", what)
		return nil
	}
	_, err := io.WriteString(s.out, *a.Output)
	return err
}

func runLogs(args []string, s session) error {
	fs := newFlagSet("logs", "MACHINE [--unit UNIT] [--priority P] [--since D] [--lines N] [--hub URL]",
		"Print the newest entries of a machine's systemd journal, one per line: the time in UTC, the unit and the message. "+
			"The daemon on the machine reads them with journalctl; this waits up to 60 seconds for it. Logs can hold secrets: "+
			"the hub clears them 1 hour after they were read.")
	hubURL := hubFlag(fs)
	unit := fs.String("unit", "", "`UNIT` only this systemd unit, such as tenderd or nginx.service")
	priority := fs.String("priority", "", "`P` only this priority and the more severe ones: "+strings.Join(actions.Priorities, ", "))
	since := fs.String("since", "", "`D` only entries from this far back, such as 30m, 1h or 7d, at most 7d")
	lines := fs.Int("lines", actions.DefaultLines, "`N` how many of the newest entries, up to 2000")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageError(fs, "logs needs one machine name")
	}
	in := actions.Input{Type: actions.TypeLogs, Unit: optional(*unit), Priority: optional(*priority), Since: optional(*since), Lines: lines}
	if err := checkLogFlags(fs, *lines, *since); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	a, err := queueAction(hub, rest[0], in)
	if err != nil {
		return err
	}
	return printLogs(s, a, rest[0])
}

// checkLogFlags checks --lines and --since before anything reaches the hub.
func checkLogFlags(fs *flag.FlagSet, lines int, since string) error {
	if lines < 1 || lines > actions.MaxLines {
		return usageError(fs, "--lines must be from 1 to %d", actions.MaxLines)
	}
	if since != "" {
		if _, err := actions.ParseSince(since); err != nil {
			return usageError(fs, "--since must be a duration such as 30m, 1h or 7d, at most 7d")
		}
	}
	return nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func actionsList(args []string, s session) error {
	fs := newFlagSet("actions list", "MACHINE [--hub URL]", "List a machine's last 100 actions, newest first.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return usageError(fs, "actions list needs one machine name")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var list server.List[actions.Summary]
	if err := hub.do(http.MethodGet, machinePath(rest[0], "/actions"), nil, &list); err != nil {
		return err
	}
	if len(list.Data) == 0 {
		say(s.out, "Nothing was asked of %s yet.", rest[0])
		return nil
	}
	rows := make([][]string, len(list.Data))
	for i, a := range list.Data {
		rows[i] = []string{strconv.FormatInt(a.ID, 10), a.Type, describe(a), a.Status, ago(s.now(), a.CreatedAt)}
	}
	printTable(s.out, []string{"ID", "TYPE", "OF", "STATUS", "ASKED"}, rows)
	return nil
}

// describe says what an action is about: its service, or the journal and its filters.
func describe(a actions.Summary) string {
	if a.Service != nil {
		return *a.Service
	}
	if a.Destination != nil {
		return "destination " + *a.Destination
	}
	parts := []string{"journal"}
	if a.Unit != nil {
		parts = append(parts, "unit "+*a.Unit)
	}
	if a.Priority != nil {
		parts = append(parts, "priority "+*a.Priority)
	}
	if a.Since != nil {
		parts = append(parts, "since "+*a.Since)
	}
	return strings.Join(parts, ", ")
}

func actionsShow(args []string, s session) error {
	fs := newFlagSet("actions show", "MACHINE ID [--hub URL]", "Show one action and its output: the logs, or why it failed.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return usageError(fs, "actions show needs a machine name and an action id")
	}
	id, err := strconv.ParseInt(rest[1], 10, 64)
	if err != nil {
		return usageError(fs, "the action id must be a number, got %q", rest[1])
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var a actions.Action
	if err := hub.do(http.MethodGet, actionsPath(rest[0], id), nil, &a); err != nil {
		return err
	}
	say(s.out, "Action %d on %s: %s of %s, %s, asked %s.", a.ID, a.Machine, a.Type, describe(a.Summary), strings.ReplaceAll(a.Status, "_", " "),
		ago(s.now(), a.CreatedAt))
	switch {
	case a.OutputCleared:
		say(s.out, "Its logs were cleared %s: the hub keeps logs only for a while after they are read, because logs can hold secrets.",
			ago(s.now(), a.UpdatedAt))
	case a.Output != nil && *a.Output != "":
		_, err = io.WriteString(s.out, "\n"+*a.Output)
	}
	return err
}
