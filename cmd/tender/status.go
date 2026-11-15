package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

// maxAlertRunes cuts an alert to one line of the summary; the email has the whole text.
const maxAlertRunes = 100

func runStatus(args []string, s session) error {
	fs := newFlagSet("status", "[--hub URL]",
		"Show the hub's summary: each machine, when it last reported, its last backup, how many of its services run and the open alerts.")
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
	var alerts []notifications.Notification
	for _, status := range []string{notifications.StatusPending, notifications.StatusSent} {
		got, err := listAll[notifications.Notification](hub, "/v1/notifications?limit=500&status="+status)
		if err != nil {
			return err
		}
		alerts = append(alerts, got...)
	}
	sort.SliceStable(alerts, func(i, j int) bool { return alerts[i].CreatedAt.After(alerts[j].CreatedAt) })

	if len(ms) == 0 {
		say(s.out, "No machines yet. Add one with: tender machines add NAME")
	} else {
		lastBackup := map[string]*backups.History{}
		for _, sum := range sums {
			lastBackup[sum.Machine] = sum.LastBackup
		}
		summary := map[string]string{}
		for _, e := range engines {
			running := 0
			for _, sv := range e.Data {
				if sv.State == services.StateRunning {
					running++
				}
			}
			switch {
			case e.Docker != nil && *e.Docker == services.DockerUnreachable:
				summary[e.Machine] = "Docker unreachable"
			case len(e.Data) > 0:
				summary[e.Machine] = fmt.Sprintf("%d/%d running", running, len(e.Data))
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
	if len(alerts) == 0 {
		say(s.out, "No open alerts.")
		return nil
	}
	say(s.out, "Open alerts: %d", len(alerts))
	rows := make([][]string, len(alerts))
	for i, a := range alerts {
		rows[i] = []string{a.Severity, ago(s.now(), a.CreatedAt), oneLine(a.Content)}
	}
	printTable(s.out, []string{"SEVERITY", "SINCE", "ALERT"}, rows)
	return nil
}

// oneLine is the first line of text, cut to maxAlertRunes with "..." when it is longer.
func oneLine(text string) string {
	line, _, more := strings.Cut(strings.TrimSpace(text), "\n")
	if utf8.RuneCountInString(line) > maxAlertRunes {
		line, more = string([]rune(line)[:maxAlertRunes-3]), true
	}
	if more {
		line = strings.TrimRight(line, " .,:;") + "..."
	}
	return line
}
