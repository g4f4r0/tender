package actions

import "time"

// Action types and statuses. They match the CHECK constraints in migrations/001_actions.sql.
const (
	TypeStart   = "start"
	TypeStop    = "stop"
	TypeRestart = "restart"
	TypeLogs    = "logs"

	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// The limits of a logs action.
const (
	DefaultLines   = 200
	MaxLines       = 2000
	MaxOutputBytes = 256 << 10
	MaxSince       = 7 * 24 * time.Hour
)

// Priorities are the journal's, most severe first. A machine logs action with a priority gets
// that priority and the more severe ones.
var Priorities = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// Summary is an action without its output, as GET /v1/machines/{machine}/actions lists them.
type Summary struct {
	ID            int64     `json:"id"`
	Machine       string    `json:"machine"`  // the machine's name
	Type          string    `json:"type"`     // start, stop, restart or logs
	Service       *string   `json:"service"`  // the service's name; null for the machine's journal
	Unit          *string   `json:"unit"`     // machine logs only: the systemd unit, or null for every unit
	Priority      *string   `json:"priority"` // machine logs only: this priority and the more severe ones, or null for all
	Since         *string   `json:"since"`    // logs only: how far back, such as 1h, or null for no limit
	Lines         *int      `json:"lines"`    // logs only: at most this many of the newest lines
	Status        string    `json:"status"`   // pending, in_progress, completed or failed
	OutputCleared bool      `json:"output_cleared"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Action is one action with its output, as GET /v1/machines/{machine}/actions/{id} returns it.
type Action struct {
	Summary
	Output *string `json:"output"` // the logs, or why it failed; null when cleared or when there is none
}

// Finished reports whether the action ended, well or not.
func (a Summary) Finished() bool { return a.Status == StatusCompleted || a.Status == StatusFailed }

// Input is the body of POST /v1/machines/{machine}/actions.
type Input struct {
	Type     string  `json:"type"`
	Service  *string `json:"service,omitempty"`  // required for start, stop and restart; with logs, that service's logs
	Unit     *string `json:"unit,omitempty"`     // machine logs only
	Priority *string `json:"priority,omitempty"` // machine logs only
	Since    *string `json:"since,omitempty"`    // logs only, such as 30m, 1h or 7d, at most 7d
	Lines    *int    `json:"lines,omitempty"`    // logs only, 1 to 2000, default 200
}

// Patch is the body of PATCH /v1/machines/{machine}/actions/{id}, with which the daemon reports
// how an action ended.
type Patch struct {
	Status string  `json:"status"` // completed or failed
	Output *string `json:"output,omitempty"`
}
