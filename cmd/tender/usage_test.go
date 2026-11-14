package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestFlagSetUsage(t *testing.T) {
	var out bytes.Buffer
	err := runUpdate([]string{"--help"}, "dev", &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help returned %v, want flag.ErrHelp", err)
	}
	want := `Usage: tender update [--version vX.Y.Z]

Update tender to the latest release and restart its services. Run as root.

Options:
  --version vX.Y.Z   the release to install (default: the latest)
`
	if out.String() != want {
		t.Errorf("usage =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestEveryUsageFitsAndHasNoGoDefaults(t *testing.T) {
	var out bytes.Buffer
	_ = runUpdate([]string{"-h"}, "dev", &out)
	all := []string{out.String()}
	for _, command := range []string{"agent", "hub", "credential", "refresh"} {
		out.Reset()
		if err := runInstall([]string{command, "-h"}, strings.NewReader(""), &out); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("install %s -h returned %v", command, err)
		}
		all = append(all, out.String())
	}
	for _, name := range []string{"add-machine", "rotate-token", "add-destination", "set-backup", "test-email"} {
		var out bytes.Buffer
		if err := hubCommand(name, []string{"--help"}, &out); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("hub %s --help returned %v", name, err)
		}
		all = append(all, out.String())
	}
	all = append(all, usage, hubUsage, installUsage)
	for _, text := range all {
		if strings.Contains(text, "Usage of") || strings.Contains(text, "-config string") {
			t.Errorf("Go's default usage leaked:\n%s", text)
		}
		if !strings.Contains(text, "Usage: tender ") && !strings.HasPrefix(text, "tender backs up") {
			t.Errorf("usage does not start with Usage: tender:\n%s", text)
		}
		for _, line := range strings.Split(text, "\n") {
			if len(line) > 99 {
				t.Errorf("line over 99 characters: %q", line)
			}
		}
	}
}

func TestInstallWithoutCommandPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if err := runInstall(nil, strings.NewReader(""), &out); !errors.Is(err, errUsageShown) {
		t.Errorf("err = %v, want errUsageShown", err)
	}
	if out.String() != installUsage {
		t.Errorf("printed %q", out.String())
	}
}

func TestOptionErrorsSayWhatToDo(t *testing.T) {
	for args, want := range map[string]string{
		"--bogus":   `unknown option --bogus. Run "tender update --help" for usage.`,
		"--version": `--version needs a value. Run "tender update --help" for usage.`,
		"extra":     `update takes no arguments, got "extra". Run "tender update --help" for usage.`,
	} {
		err := runUpdate(strings.Fields(args), "dev", &bytes.Buffer{})
		if err == nil || err.Error() != want {
			t.Errorf("update %s: %v, want %q", args, err, want)
		}
	}
}

func TestHubCommandErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no name":        {[]string{"add-machine"}, `add-machine needs a machine name. Run "tender hub help" for usage.`},
		"two names":      {[]string{"add-machine", "a", "b"}, `add-machine takes one name, got 2: a b. Run "tender hub help" for usage.`},
		"unknown":        {[]string{"nope"}, `unknown hub command "nope". Run "tender hub help" for the commands.`},
		"unknown option": {[]string{"add-machine", "--nope"}, `unknown option --nope. Run "tender hub help" for usage.`},
	} {
		t.Run(name, func(t *testing.T) {
			err := hubCommand(tc.args[0], tc.args[1:], &bytes.Buffer{})
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
