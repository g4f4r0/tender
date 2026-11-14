package backups

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/robfig/cron/v3"
)

// checkEvery is how long a successful check stays fresh.
const checkEvery = 7 * 24 * time.Hour

// parseSchedule reads a standard 5-field cron expression. It is evaluated in UTC because
// isDue only ever passes UTC times.
func parseSchedule(spec string) (cron.Schedule, error) {
	s, err := cron.ParseStandard(spec)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", spec, err)
	}
	return s, nil
}

// isDue reports whether a run is owed: the first scheduled time after last has passed. This
// covers a time that passed while the agent was down (it runs once, not once per missed time).
// With no last run, a backup is due at once.
func isDue(s cron.Schedule, last, now time.Time) bool {
	if last.IsZero() {
		return true
	}
	return !s.Next(last.UTC()).After(now.UTC())
}

// agentState is what the agent remembers between starts, in <state>/agent-state.json.
type agentState struct {
	LastBackup time.Time `json:"last_backup"` // when the latest backup run ended, any status
	LastCheck  time.Time `json:"last_check"`  // when the latest successful check ended
}

func loadState(path string) (agentState, error) {
	var st agentState
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return agentState{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return st, nil
}

func saveState(path string, st agentState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}
