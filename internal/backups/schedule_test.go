package backups

import (
	"path/filepath"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseSchedule(t *testing.T) {
	s, err := parseSchedule("30 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at("2026-11-14 10:00")); !got.Equal(at("2026-11-15 03:30")) {
		t.Errorf("next = %v, want 2026-11-15 03:30 UTC", got)
	}
	for _, bad := range []string{"", "30 3 * *", "61 3 * * *", "@every nonsense", "every day"} {
		if _, err := parseSchedule(bad); err == nil {
			t.Errorf("parseSchedule(%q): want an error", bad)
		}
	}
}

func TestIsDue(t *testing.T) {
	s, _ := parseSchedule("30 3 * * *")
	cases := []struct {
		name      string
		last, now string
		want      bool
	}{
		{"before the next time", "2026-11-14 03:30", "2026-11-15 03:29", false},
		{"at the next time", "2026-11-14 03:30", "2026-11-15 03:30", true},
		{"ran a minute late, nothing owed", "2026-11-15 03:31", "2026-11-15 12:00", false},
		{"down for three days: owed once", "2026-11-10 03:31", "2026-11-14 09:00", true},
	}
	for _, c := range cases {
		if got := isDue(s, at(c.last), at(c.now)); got != c.want {
			t.Errorf("%s: due = %v, want %v", c.name, got, c.want)
		}
	}
	if !isDue(s, time.Time{}, at("2026-11-14 09:00")) {
		t.Error("never backed up: want due")
	}
}

func TestStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent-state.json")
	st, err := loadState(path)
	if err != nil || !st.LastBackup.IsZero() {
		t.Fatalf("missing file: %+v, %v", st, err)
	}
	want := agentState{LastBackup: at("2026-11-14 03:31"), LastCheck: at("2026-11-10 04:00")}
	if err := saveState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(path)
	if err != nil || !got.LastBackup.Equal(want.LastBackup) || !got.LastCheck.Equal(want.LastCheck) {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
}
