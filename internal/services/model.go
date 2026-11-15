package services

import "time"

// Docker's container states, and the health a container with a health check has. They match the
// CHECK constraints in migrations/001_services.sql.
const (
	StateCreated    = "created"
	StateRunning    = "running"
	StatePaused     = "paused"
	StateRestarting = "restarting"
	StateExited     = "exited"
	StateRemoving   = "removing"
	StateDead       = "dead"
	StateUnknown    = "unknown" // the machine's Docker is unreachable; Tender sets it, Docker never does

	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthStarting  = "starting"

	// Docker's restart policies. An empty one from Docker means no.
	PolicyNo            = "no"
	PolicyAlways        = "always"
	PolicyUnlessStopped = "unless-stopped"
	PolicyOnFailure     = "on-failure"

	// What a machine's daemon says about its Docker engine.
	DockerRunning     = "running"
	DockerUnreachable = "unreachable" // the socket is there but refuses connections or doesn't answer
)

// Action types and statuses.
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
)

// Service is one Docker container, as GET /v1/machines/{machine}/services/{service} returns it.
type Service struct {
	ID              int64      `json:"id"`
	Machine         string     `json:"machine"` // the machine's name
	Name            string     `json:"name"`    // the container name, such as myapp-web-1
	Project         *string    `json:"project"` // the compose project, or null
	ContainerID     string     `json:"container_id"`
	Image           string     `json:"image"`
	State           string     `json:"state"`     // created, running, paused, restarting, exited, removing, dead, or unknown while Docker is unreachable
	Health          *string    `json:"health"`    // healthy, unhealthy, starting, or null without a health check
	ExitCode        *int64     `json:"exit_code"` // set while the container is exited or dead
	RestartCount    int64      `json:"restart_count"`
	RestartPolicy   string     `json:"restart_policy"` // no, always, unless-stopped or on-failure
	StartedAt       *time.Time `json:"started_at"`
	AlertsEnabled   bool       `json:"alerts_enabled"`
	StoppedByTender bool       `json:"stopped_by_tender"` // a Tender stop left it stopped, so it doesn't alert as down
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"` // the daemon's report updates it every minute

	// How many reports in a row found it down, restarting or unhealthy. The alert rules read them.
	DownReports       int `json:"-"`
	RestartingReports int `json:"-"`
	UnhealthyReports  int `json:"-"`
}

// ServiceInput is one element of the PUT /v1/machines/{machine}/services body, as the daemon
// reads it from Docker.
type ServiceInput struct {
	Name          string     `json:"name"`
	Project       *string    `json:"project"`
	ContainerID   string     `json:"container_id"`
	Image         string     `json:"image"`
	State         string     `json:"state"`
	Health        *string    `json:"health"`
	ExitCode      *int64     `json:"exit_code"`
	RestartCount  int64      `json:"restart_count"`
	RestartPolicy string     `json:"restart_policy"`
	StartedAt     *time.Time `json:"started_at"`
}

// Report is the body of PUT /v1/machines/{machine}/services: {"docker": "running", "services": [...]}
// when the Engine API answers, the full list, or {"docker": "unreachable"} when its socket refuses
// connections or doesn't answer. Then the hub keeps the last list with every state unknown.
type Report struct {
	Docker   string         `json:"docker"`
	Services []ServiceInput `json:"services,omitempty"`
}

// MachineServices is one machine's services, as GET /v1/machines/{machine}/services returns it
// and each element of GET /v1/services.
type MachineServices struct {
	Machine string    `json:"machine"`
	Docker  *string   `json:"docker"` // running or unreachable, from the latest report; null before any
	Data    []Service `json:"data"`

	unreachableReports int
}

// UnreachableReports is how many reports in a row said Docker is unreachable.
func (m MachineServices) UnreachableReports() int { return m.unreachableReports }

// ServicePatch is the JSON Merge Patch body of PATCH /v1/machines/{machine}/services/{service}.
type ServicePatch struct {
	AlertsEnabled *bool `json:"alerts_enabled"`
}

// Action is something a person or an AI client asked the daemon to do to a service, as
// GET /v1/machines/{machine}/service-actions/{id} returns it. It stays after its service is gone.
type Action struct {
	ID        int64     `json:"id"`
	Machine   string    `json:"machine"` // the machine's name
	Service   string    `json:"service"` // the service's name
	Type      string    `json:"type"`    // start, stop, restart or logs
	Status    string    `json:"status"`  // pending, in_progress, completed or failed
	Lines     *int      `json:"lines"`   // logs only: how many of the last lines
	Output    *string   `json:"output"`  // the logs, or why it failed
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ActionInput is the body of POST /v1/machines/{machine}/services/{service}/actions.
type ActionInput struct {
	Type  string `json:"type"`
	Lines *int   `json:"lines,omitempty"` // logs only, 1 to 2000, default 200
}

// ActionPatch is the body of PATCH /v1/machines/{machine}/service-actions/{id}, with which the
// daemon reports how an action ended.
type ActionPatch struct {
	Status string  `json:"status"` // completed or failed
	Output *string `json:"output,omitempty"`
}

// Finished reports whether the action ended, well or not.
func (a Action) Finished() bool { return a.Status == StatusCompleted || a.Status == StatusFailed }
