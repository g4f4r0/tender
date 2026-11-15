package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

var notificationsUsage = groupUsage("notifications", "See what the hub told its channels. A notification is one message: a problem "+
	"adds one when it starts, such as service.down, one when it gets worse, service.down.escalated, and one when it clears, "+
	"service.down.resolved.", []row{
	{"list [MACHINE]", "List notifications, newest first, or only the open problems"},
})

// runNotifications runs "tender notifications list".
func runNotifications(args []string, s session) error {
	return runGroup("notifications", notificationsUsage, args, s.out, map[string]func([]string) error{
		"list": func(a []string) error { return notificationsList(a, s) },
	})
}

func notificationsList(args []string, s session) error {
	fs := newFlagSet("notifications list", "[MACHINE] [--open] [--status STATUS] [--severity SEVERITY] [--limit N] [--hub URL]",
		"List notifications, newest first: of every machine, or of one. With --open, list the open problems instead: the latest "+
			"notification of each problem that hasn't resolved.")
	hubURL := hubFlag(fs)
	open := fs.Bool("open", false, "only the open problems")
	status := fs.String("status", "", "`STATUS` only pending, sent (a channel delivered it) or failed (every channel failed)")
	severity := fs.String("severity", "", "`SEVERITY` only info, warning or critical")
	limit := fs.Int("limit", 50, "`N` how many of the newest, 1 to 500")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usageError(fs, "notifications list takes at most one machine name")
	}
	if *limit < 1 || *limit > 500 {
		return usageError(fs, "--limit must be from 1 to 500")
	}
	q := url.Values{"limit": {strconv.Itoa(*limit)}}
	for k, v := range map[string]string{"status": *status, "severity": *severity} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if *open {
		q.Set("open", "true")
	}
	path := "/v1/notifications"
	if len(rest) == 1 {
		path = machinePath(rest[0], "/notifications")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var page server.List[notifications.Notification]
	if err := hub.do(http.MethodGet, path+"?"+q.Encode(), nil, &page); err != nil {
		return err
	}
	if len(page.Data) == 0 {
		if *open {
			say(s.out, "No open problems.")
		} else {
			say(s.out, "No notifications.")
		}
		return nil
	}
	rows := make([][]string, len(page.Data))
	for i, n := range page.Data {
		about := "-"
		switch {
		case n.Machine != nil && n.Service != nil:
			about = *n.Machine + "/" + *n.Service
		case n.Machine != nil:
			about = *n.Machine
		case n.Channel != nil:
			about = "channel " + *n.Channel
		}
		rows[i] = []string{strconv.FormatInt(n.ID, 10), ago(s.now(), n.CreatedAt), n.Event, n.Severity, about, n.Status, oneLine(n.Message)}
	}
	printTable(s.out, []string{"ID", "ADDED", "EVENT", "SEVERITY", "ABOUT", "STATUS", "MESSAGE"}, rows)
	if page.NextCursor != "" {
		say(s.out, "%s", fmt.Sprintf("\nThese are the newest %d. Raise --limit to see more.", len(page.Data)))
	}
	return nil
}
