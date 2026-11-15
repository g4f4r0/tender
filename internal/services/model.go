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
	StateRemoved    = "removed" // the container is gone from Docker; Tender sets it, and keeps the row 7 days

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
	DockerUnreachable = "unreachable" // the daemon can't reach it; DockerReason says why

	// Why a daemon can't reach Docker, once it had it.
	ReasonSocketMissing    = "socket_missing"    // the socket is gone
	ReasonPermissionDenied = "permission_denied" // the daemon may not use the socket
	ReasonRefused          = "refused"           // the socket refuses connections: Docker is stopped
	ReasonTimeout          = "timeout"           // Docker didn't answer within 5 seconds
	ReasonError            = "error"             // anything else
)

var reasons = map[string]bool{ReasonSocketMissing: true, ReasonPermissionDenied: true, ReasonRefused: true, ReasonTimeout: true, ReasonError: true}

// The limits of a service's logs, as the actions domain asks for them.
const (
	MaxLines       = 2000
	MaxOutputBytes = 256 << 10
)

// The actions the daemon does to a container, by the actions domain's names for them.
const (
	actStart   = "start"
	actStop    = "stop"
	actRestart = "restart"
	actLogs    = "logs"
)

// Service is one Docker container, as GET /v1/machines/{machine}/services/{service} returns it.
type Service struct {
	ID            int64      `json:"id"`
	Machine       string     `json:"machine"` // the machine's name
	Name          string     `json:"name"`    // the container name, such as myapp-web-1
	Runtime       string     `json:"runtime"` // what runs it: docker, also for Podman's Docker-compatible socket
	Project       *string    `json:"project"` // the compose project, or null
	ContainerID   string     `json:"container_id"`
	Image         string     `json:"image"`
	State         string     `json:"state"`     // created, running, paused, restarting, exited, removing, dead, unknown while Docker is unreachable, or removed
	Health        *string    `json:"health"`    // healthy, unhealthy, starting, or null without a health check
	ExitCode      *int64     `json:"exit_code"` // set while the container is exited or dead
	RestartCount  int64      `json:"restart_count"`
	RestartPolicy string     `json:"restart_policy"` // no, always, unless-stopped or on-failure
	StartedAt     *time.Time `json:"started_at"`
	// Usage from the latest report, while it runs; null when stopped or when Docker couldn't say.
	MemoryBytes      *int64   `json:"memory_bytes"`       // less the page cache it can drop
	MemoryLimitBytes *int64   `json:"memory_limit_bytes"` // null without a limit
	CPUPercent       *float64 `json:"cpu_percent"`        // 100 is one whole CPU; null on its first report and after a restart
	AlertsEnabled    bool     `json:"alerts_enabled"`
	StoppedByTender  bool     `json:"stopped_by_tender"` // a Tender stop left it stopped, so it doesn't alert as down
	// RemovedAt is when a report first lacked the container, while its state is removed. The hub
	// deletes the service 7 days after it; a container back under the name before then keeps it.
	RemovedAt *time.Time `json:"removed_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"` // the daemon's report updates it every minute

	// How many reports in a row found it down, restarting or unhealthy. The alert rules read them.
	DownReports       int `json:"-"`
	RestartingReports int `json:"-"`
	UnhealthyReports  int `json:"-"`
	MemoryHighReports int `json:"-"`
}

// ServiceInput is one element of the PUT /v1/machines/{machine}/services body, as the daemon
// reads it from Docker.
type ServiceInput struct {
	Name          string     `json:"name"`
	Runtime       string     `json:"runtime"` // docker; a daemon before v0.3 leaves it out, which means docker
	Project       *string    `json:"project"`
	ContainerID   string     `json:"container_id"`
	Image         string     `json:"image"`
	State         string     `json:"state"`
	Health        *string    `json:"health"`
	ExitCode      *int64     `json:"exit_code"`
	RestartCount  int64      `json:"restart_count"`
	RestartPolicy string     `json:"restart_policy"`
	StartedAt     *time.Time `json:"started_at"`

	MemoryBytes      *int64   `json:"memory_bytes"`
	MemoryLimitBytes *int64   `json:"memory_limit_bytes"`
	CPUPercent       *float64 `json:"cpu_percent"`
	// StatsUnavailable says the container runs but its stats couldn't be read, so its usage is
	// unknown, not gone: the hub keeps the last memory numbers. Stopped containers leave it false.
	StatsUnavailable bool `json:"stats_unavailable,omitempty"`
}

// Report is the body of PUT /v1/machines/{machine}/services: {"docker": "running", "services": [...]}
// when the Engine API answers, the full list and [] for none, or {"docker": "unreachable",
// "docker_reason": "refused"} when the daemon can't reach it, with why. Then the hub keeps the last list with every state
// unknown. Make one with RunningReport or UnreachableReport, so a running report always carries
// its list and an unreachable one never does.
type Report struct {
	Docker       string          `json:"docker"`
	DockerReason string          `json:"docker_reason,omitempty"` // why, when unreachable
	Services     *[]ServiceInput `json:"services,omitempty"`
}

// MachineServices is one machine's services, as GET /v1/machines/{machine}/services returns it
// and each element of GET /v1/services.
type MachineServices struct {
	Machine string  `json:"machine"`
	Docker  *string `json:"docker"` // running or unreachable, from the latest report; null before any
	// DockerReason says why Docker is unreachable: socket_missing, permission_denied, refused,
	// timeout or error. It is null while Docker runs.
	DockerReason *string   `json:"docker_reason"`
	Data         []Service `json:"data"`

	unreachableReports int
	reportedAt         *time.Time
}

// UnreachableReports is how many reports in a row said Docker is unreachable.
func (m MachineServices) UnreachableReports() int { return m.unreachableReports }

// ReportedAt is when the machine's daemon last sent a services report, or nil before the first.
func (m MachineServices) ReportedAt() *time.Time { return m.reportedAt }

// ServicePatch is the JSON Merge Patch body of PATCH /v1/machines/{machine}/services/{service}.
type ServicePatch struct {
	AlertsEnabled *bool `json:"alerts_enabled"`
}

// RunningReport is a report of the containers Docker listed, [] when there are none.
func RunningReport(list []ServiceInput) Report {
	if list == nil {
		list = []ServiceInput{}
	}
	return Report{Docker: DockerRunning, Services: &list}
}

// UnreachableReport is a report that the daemon can't reach Docker, and why. It carries no list.
func UnreachableReport(reason string) Report {
	return Report{Docker: DockerUnreachable, DockerReason: reason}
}
