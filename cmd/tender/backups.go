package main

import (
	"fmt"
	"net/http"

	"github.com/g4f4r0/tender/internal/backups"
)

var backupsUsage = groupUsage("backups", "See how each machine's backups are doing, and set when and where each machine backs up.", []row{
	{"status [MACHINE]", "Show the destination, schedule and last backup"},
	{"set MACHINE --destination NAME ...", "Choose where, when and for how long a machine backs up"},
	{"unset MACHINE", "Stop backing up a machine"},
})

// runBackups runs "tender backups status|set|unset".
func runBackups(args []string, s session) error {
	return runGroup("backups", backupsUsage, args, s.out, map[string]func([]string) error{
		"status": func(a []string) error { return backupsStatus(a, s) },
		"set":    func(a []string) error { return backupsSet(a, s) },
		"unset":  func(a []string) error { return backupsUnset(a, s) },
	})
}

func backupsStatus(args []string, s session) error {
	fs := newFlagSet("backups status", "[MACHINE] [--hub URL]",
		"Show every machine's backups, or one machine's: where they go, the schedule (UTC), how long they are kept, "+
			"the last backup and its status.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usageError(fs, "backups status takes at most one machine name, got %d", len(rest))
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var list []backups.Summary
	if len(rest) == 1 {
		var one backups.Summary
		if err := hub.do(http.MethodGet, machinePath(rest[0], "/backups"), nil, &one); err != nil {
			return err
		}
		list = []backups.Summary{one}
	} else if list, err = listAll[backups.Summary](hub, "/v1/backups"); err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No machines yet. Add one with: tender machines add NAME")
		return nil
	}
	rows := make([][]string, len(list))
	unset := ""
	for i, sum := range list {
		rows[i] = []string{sum.Machine, "not set", "-", "-", "never", "-", fmt.Sprint(sum.SnapshotCount)}
		if st := sum.Settings; st != nil {
			rows[i][1], rows[i][2], rows[i][3] = st.Destination.Name, st.Schedule, fmt.Sprintf("%d days", st.RetentionDays)
		} else if unset == "" {
			unset = sum.Machine
		}
		if b := sum.LastBackup; b != nil {
			rows[i][4], rows[i][5] = ago(s.now(), b.CreatedAt), b.Status
		}
	}
	printTable(s.out, []string{"MACHINE", "DESTINATION", "SCHEDULE", "KEEPS", "LAST BACKUP", "STATUS", "SNAPSHOTS"}, rows)
	if unset != "" {
		say(s.out, "\n%s has no backup settings. Set them with: tender backups set %s --destination NAME --schedule \"30 3 * * *\" --retention-days 30", unset, unset)
	}
	return nil
}

func backupsSet(args []string, s session) error {
	fs := newFlagSet("backups set", "MACHINE --destination NAME --schedule CRON --retention-days N\n[--hub URL]",
		"Choose where, when and for how long a machine's backups are kept. The agent picks the change up within ten minutes.")
	hubURL := hubFlag(fs)
	destination := fs.String("destination", "", "`NAME` the destination to back up to")
	schedule := fs.String("schedule", "", "`CRON` when to back up, five fields in UTC, such as \"30 3 * * *\"")
	days := fs.Int("retention-days", 0, "`N` how many days to keep backups")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	if *destination == "" || *schedule == "" || *days < 1 {
		return usageError(fs, "backups set needs --destination, --schedule and --retention-days (1 or more)")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var st backups.Settings
	in := backups.SettingsInput{Destination: *destination, Schedule: *schedule, RetentionDays: *days}
	if err := hub.do(http.MethodPut, machinePath(machine, "/backups/settings"), in, &st); err != nil {
		return err
	}
	say(s.out, "%s backs up to %s at %q (UTC) and keeps %d days.", machine, st.Destination.Name, st.Schedule, st.RetentionDays)
	return nil
}

func backupsUnset(args []string, s session) error {
	fs := newFlagSet("backups unset", "MACHINE [--hub URL]",
		"Stop backing up a machine. Its backups stay where they are, in the destination.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	machine, err := oneArg(fs, rest, "a machine name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, machinePath(machine, "/backups/settings"), nil, nil); err != nil {
		return err
	}
	say(s.out, "%s no longer backs up. Its backups stay in the destination.", machine)
	return nil
}
