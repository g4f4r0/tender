package updates

import "time"

// The update modes, and the statuses of rollouts and their machines.
const (
	ModeAuto    = "auto"
	ModeManual  = "manual"
	ModeInherit = "inherit" // a machine that follows the hub's mode

	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Settings are the hub's update settings, as GET and PUT /v1/updates/settings have them.
type Settings struct {
	Mode      string    `json:"mode"`   // auto or manual
	Window    *string   `json:"window"` // when auto mode may start a rollout, such as 02:00-05:00 UTC; null for any time
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SettingsInput is the body of PUT /v1/updates/settings.
type SettingsInput struct {
	Mode   string  `json:"mode"`
	Window *string `json:"window,omitempty"`
}

// Latest is what the hub last learned from GitHub about the newest stable release.
type Latest struct {
	Version   *string    `json:"version"`    // such as 0.3.1, or null before the hub could ask
	Auto      bool       `json:"auto"`       // auto mode may roll it out: its tender-release.json doesn't say no
	CheckedAt *time.Time `json:"checked_at"` // when the hub last asked
	Error     *string    `json:"error"`      // why the last look failed, or null
}

// MachineVersion is one machine in GET /v1/updates.
type MachineVersion struct {
	Machine         string  `json:"machine"`
	Version         *string `json:"version"`          // the Tender version its daemon reports; null before v0.3.0
	Updates         string  `json:"updates"`          // the machine's setting: inherit, auto or manual
	Mode            string  `json:"mode"`             // what applies: auto or manual
	UpdateAvailable bool    `json:"update_available"` // the latest release is newer than its version
}

// Overview is GET /v1/updates.
type Overview struct {
	Settings
	Latest  Latest           `json:"latest"`
	Rollout *Rollout         `json:"rollout"` // the rollout in progress, or null
	Data    []MachineVersion `json:"data"`
}

// Step is one machine of a rollout.
type Step struct {
	Machine   string    `json:"machine"`
	Position  int       `json:"position"`
	Status    string    `json:"status"`    // pending, in_progress, completed or failed
	ActionID  *int64    `json:"action_id"` // its update action, once queued
	Error     *string   `json:"error"`     // why it failed
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Rollout updates machines to one release, one at a time.
type Rollout struct {
	ID        int64     `json:"id"`
	Version   string    `json:"version"`
	Status    string    `json:"status"` // in_progress, completed or failed
	Auto      bool      `json:"auto"`   // auto mode started it
	Error     *string   `json:"error"`
	Machines  []Step    `json:"machines"` // in the order they update
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RolloutInput is the body of POST /v1/updates/rollouts. An empty version is the latest release, and
// no machines is every machine.
type RolloutInput struct {
	Version  string   `json:"version,omitempty"`
	Machines []string `json:"machines,omitempty"`
}
