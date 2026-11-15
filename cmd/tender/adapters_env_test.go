package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/notifications"
)

// TENDER_TEST_ADAPTERS_DIR, for tests only, says so in the log whenever it is set; the old name
// does nothing.
func TestTestAdaptersDirWarns(t *testing.T) {
	defer func() { notifications.AdaptersDir = "" }()
	for env, want := range map[string]string{"TENDER_TEST_ADAPTERS_DIR": "/tmp/adapters", "TENDER_ADAPTERS_DIR": ""} {
		var logs bytes.Buffer
		testAdaptersDir(func(k string) string {
			if k == env {
				return "/tmp/adapters"
			}
			return ""
		}, slog.New(slog.NewTextHandler(&logs, nil)))
		if notifications.AdaptersDir != want || (want != "") != strings.Contains(logs.String(), "level=WARN") {
			t.Errorf("%s: folder %q, log %q", env, notifications.AdaptersDir, logs.String())
		}
	}
}
