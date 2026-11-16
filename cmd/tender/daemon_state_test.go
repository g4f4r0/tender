package main

import "testing"

// systemd joins a unit's state folders with colons; the daemon's own folder is the first.
func TestDefaultStateTakesTheFirstStateDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"/var/lib/tender/daemon:/var/lib/tender/services": "/var/lib/tender/daemon",
		"/var/lib/tender/daemon":                          "/var/lib/tender/daemon",
		"":                                                "/var/lib/tender/daemon",
	} {
		t.Setenv("STATE_DIRECTORY", in)
		if got := defaultState(); got != want {
			t.Errorf("STATE_DIRECTORY=%q: got %q, want %q", in, got, want)
		}
	}
}
