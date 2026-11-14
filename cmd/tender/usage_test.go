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

Update Tender to the latest release and restart its services. Run as root.

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
	for _, command := range []string{"agent", "hub", "refresh"} {
		out.Reset()
		if err := runInstall([]string{command, "-h"}, strings.NewReader(""), session{out: &out}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("install %s -h returned %v", command, err)
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
	if err := runInstall(nil, strings.NewReader(""), session{out: &out}); !errors.Is(err, errUsageShown) {
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

func TestHubOptionErrors(t *testing.T) {
	for args, want := range map[string]string{
		"--nope":   `unknown option --nope. Run "tender hub --help" for usage.`,
		"--listen": `--listen needs a value. Run "tender hub --help" for usage.`,
		"extra":    `hub takes no arguments, got "extra". Run "tender hub --help" for usage.`,
		"--config": `unknown option --config. Run "tender hub --help" for usage.`,
	} {
		err := serveHub(strings.Fields(args), quiet)
		if err == nil || err.Error() != want {
			t.Errorf("hub %s: %v, want %q", args, err, want)
		}
	}
}
