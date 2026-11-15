package main

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/services"
)

// maxLineRunes cuts a message to one line of a table; the notification has the whole text.
const maxLineRunes = 100

func runStatus(args []string, s session) error {
	fs := newFlagSet("status", "[--hub URL]",
		"Show the hub's summary: each machine, when it last reported, its last backup, how many of its services run and the open problems.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	ms, err := listAll[machines.Overview](hub, "/v1/machines")
	if err != nil {
		return err
	}
	sums, err := listAll[backups.Summary](hub, "/v1/backups")
	if err != nil {
		return err
	}
	engines, err := listAll[services.MachineServices](hub, "/v1/services")
	withServices := !answered(err, http.StatusNotFound) // a hub from before services has no such route
	if err != nil && withServices {
		return err
	}
	problems, err := listAll[notifications.Notification](hub, "/v1/notifications?limit=500&open=true")
	if err != nil {
		return err
	}

	if len(ms) == 0 {
		say(s.out, "No machines yet. Add one with: tender machines add NAME")
	} else {
		lastBackup := map[string]*backups.History{}
		for _, sum := range sums {
			lastBackup[sum.Machine] = sum.LastBackup
		}
		summary := map[string]string{}
		for _, e := range engines {
			running, present := 0, 0
			for _, sv := range e.Data {
				switch sv.State {
				case services.StateRunning:
					running++
					present++
				case services.StateRemoved: // its container is gone
				default:
					present++
				}
			}
			switch {
			case e.Docker != nil && *e.Docker == services.DockerUnreachable:
				summary[e.Machine] = "Docker unreachable"
			case present > 0:
				summary[e.Machine] = fmt.Sprintf("%d/%d running", running, present)
			}
		}
		rows := make([][]string, len(ms))
		for i, m := range ms {
			rows[i] = []string{m.Name, "never", "never", "-", "-"}
			if v := summary[m.Name]; v != "" {
				rows[i][4] = v
			}
			if m.LastSeen != nil {
				rows[i][1] = ago(s.now(), *m.LastSeen)
			}
			if b := lastBackup[m.Name]; b != nil {
				rows[i][2], rows[i][3] = ago(s.now(), b.CreatedAt), b.Status
			}
		}
		header := []string{"MACHINE", "LAST SEEN", "LAST BACKUP", "STATUS", "SERVICES"}
		if !withServices {
			header = header[:4]
			for i := range rows {
				rows[i] = rows[i][:4]
			}
		}
		printTable(s.out, header, rows)
	}

	say(s.out, "")
	if len(problems) == 0 {
		say(s.out, "No open problems.")
		return nil
	}
	say(s.out, "Open problems: %d", len(problems))
	rows := make([][]string, len(problems))
	for i, n := range problems {
		rows[i] = []string{n.Severity, ago(s.now(), n.CreatedAt), channels.Base(n.Event), oneLine(n.Message)}
	}
	printTable(s.out, []string{"SEVERITY", "SINCE", "EVENT", "PROBLEM"}, rows)
	return nil
}

// oneLine is the first line of text, cut to maxLineRunes with "..." when it is longer.
func oneLine(text string) string {
	line, _, more := strings.Cut(strings.TrimSpace(text), "\n")
	if utf8.RuneCountInString(line) > maxLineRunes {
		line, more = string([]rune(line)[:maxLineRunes-3]), true
	}
	if more {
		line = strings.TrimRight(line, " .,:;") + "..."
	}
	return line
}
