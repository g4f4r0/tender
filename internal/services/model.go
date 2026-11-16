package services

import (
	"encoding/json"
	"time"
)

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
	// DockerOff says Docker is turned off on the machine: its socket is missing or refuses
	// connections, and neither docker.service nor docker.socket is enabled. Only a report says it:
	// the hub then forgets the machine's Docker, so GET shows docker null.
	DockerOff = "off"

	// Why a daemon can't reach Docker, once it had it.
	ReasonSocketMissing    = "socket_missing"    // the socket is gone
	ReasonPermissionDenied = "permission_denied" // the daemon may not use the socket
	ReasonRefused          = "refused"           // the socket refuses connections: Docker is stopped
	ReasonTimeout          = "timeout"           // Docker didn't answer within 5 seconds
	ReasonError            = "error"             // anything else
)

var reasons = map[string]bool{ReasonSocketMissing: true, ReasonPermissionDenied: true, ReasonRefused: true, ReasonTimeout: true, ReasonError: true}

// The limits of a service's logs, as the actions domain asks for them, for all its containers.
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

// The states of a service, from its containers': each runs, some do, none do, or Docker is
// unreachable so nobody knows.
const (
	ServiceRunning  = "running"
	ServiceDegraded = "degraded"
	ServiceStopped  = "stopped"
	ServiceUnknown  = "unknown"
)

// The deploy statuses of a managed service, as the CHECK constraint in the migrations has them.
const (
	DeployDeploying = "deploying" // a deploy waits for the daemon or runs
	DeployDeployed  = "deployed"  // the last deploy succeeded
	DeployFailed    = "failed"    // the last deploy or remove failed; last_error says why
	DeployRemoving  = "removing"  // a remove waits for the daemon or runs
)

// The actions the daemon does for a managed service, by the actions domain's names for them.
const (
	ActionDeploy = "deploy"
	ActionRemove = "remove"
)

// Service is one service on a machine: a compose project, or a container without one, as GET
// /v1/machines/{machine}/services/{service} returns it.
type Service struct {
	ID      int64  `json:"id"`
	Machine string `json:"machine"` // the machine's name
	Name    string `json:"name"`    // the compose project name, or the lone container's name
	// Managed says Tender deployed it, from a compose file the hub keeps. An unmanaged service is
	// one Tender found running; it can be started, stopped and watched, but not deployed.
	Managed bool `json:"managed"`
	// State is running when every container that should run does, degraded when some do, stopped
	// when none do, and unknown while the machine's Docker is unreachable. A container that exited
	// 0 and that Docker wouldn't restart finished its job, so it doesn't count.
	State         string            `json:"state"`
	DeployStatus  *string           `json:"deploy_status"` // deploying, deployed, failed or removing; null when unmanaged
	LastError     *string           `json:"last_error"`    // why the last deploy or remove failed: its first line, then the end of compose's output
	DeployedAt    *time.Time        `json:"deployed_at"`   // when a deploy last succeeded, or null
	ActionID      *int64            `json:"action_id"`     // the latest deploy or remove, in the machine's actions
	Compose       *string           `json:"compose"`       // the compose file, as given; null when unmanaged
	Files         map[string]string `json:"files"`         // relative path to content, for the files the compose mounts
	Env           map[string]string `json:"env"`           // every key, each value "***"
	Routes        []Route           `json:"routes"`
	AlertsEnabled bool              `json:"alerts_enabled"` // one switch for every container
	Containers    []Container       `json:"containers"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`

	machineID  int64
	actionType string
	sealedEnv  map[string]string
}

// Container is one Docker container of a service.
type Container struct {
	ID int64 `json:"id"`
	// Name is its name in the service: its compose service name, such as web, with -2 and up for
	// the second and later replicas, or its container name for a lone container.
	Name          string     `json:"name"`
	Runtime       string     `json:"runtime"` // what runs it: docker, also for Podman's Docker-compatible socket
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
	StoppedByTender  bool     `json:"stopped_by_tender"`  // a Tender stop left it stopped, so it doesn't alert as down
	// RemovedAt is when a report first lacked the container, while its state is removed. The hub
	// deletes the container 7 days after it; a container back under the name before then keeps it.
	RemovedAt *time.Time `json:"removed_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"` // the daemon's report updates it every minute

	// How many reports in a row found it down, restarting or unhealthy. The alert rules read them.
	DownReports       int `json:"-"`
	RestartingReports int `json:"-"`
	UnhealthyReports  int `json:"-"`
	MemoryHighReports int `json:"-"`

	serviceID int64
}

// ContainerInput is one container of the PUT /v1/machines/{machine}/services body, as the daemon
// reads it from Docker.
type ContainerInput struct {
	Name    string  `json:"name"`    // the Docker container name, such as myapp-web-1
	Runtime string  `json:"runtime"` // docker; a daemon before v0.3 leaves it out, which means docker
	Project *string `json:"project"` // the compose project, which is its service, or null
	// ComposeService and ContainerNumber are the compose service and replica number Docker's
	// compose labels give it, which name it in its service. A daemon before v0.4 leaves them out,
	// and the hub takes them from the container name.
	ComposeService  *string    `json:"compose_service,omitempty"`
	ContainerNumber *int       `json:"container_number,omitempty"`
	ContainerID     string     `json:"container_id"`
	Image           string     `json:"image"`
	State           string     `json:"state"`
	Health          *string    `json:"health"`
	ExitCode        *int64     `json:"exit_code"`
	RestartCount    int64      `json:"restart_count"`
	RestartPolicy   string     `json:"restart_policy"`
	StartedAt       *time.Time `json:"started_at"`

	MemoryBytes      *int64   `json:"memory_bytes"`
	MemoryLimitBytes *int64   `json:"memory_limit_bytes"`
	CPUPercent       *float64 `json:"cpu_percent"`
	// StatsUnavailable says the container runs but its stats couldn't be read, so its usage is
	// unknown, not gone: the hub keeps the last memory numbers. Stopped containers leave it false.
	StatsUnavailable bool `json:"stats_unavailable,omitempty"`
}

// Report is the body of PUT /v1/machines/{machine}/services: {"docker": "running", "containers": [...]}
// when the Engine API answers, the full list and [] for none, or {"docker": "unreachable",
// "docker_reason": "refused"} when the daemon can't reach it, with why. Then the hub keeps the last
// list with every state unknown. {"docker": "off"} says Docker is turned off: the hub forgets the
// machine's Docker and marks its containers removed. A daemon before v0.4 sends the list as
// services. Make one with RunningReport, UnreachableReport or OffReport, so a running report always
// carries its list and the others never do.
type Report struct {
	Docker       string            `json:"docker"`
	DockerReason string            `json:"docker_reason,omitempty"` // why, when unreachable
	Containers   *[]ContainerInput `json:"containers,omitempty"`
	Services     *[]ContainerInput `json:"services,omitempty"` // the list from a daemon before v0.4
}

// MachineServices is one machine's services, as GET /v1/machines/{machine}/services returns it
// and each element of GET /v1/services.
type MachineServices struct {
	Machine string  `json:"machine"`
	Docker  *string `json:"docker"` // running or unreachable, from the latest report; null before any, and once Docker is turned off
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

// Route makes one container port of a managed service reachable.
type Route struct {
	ID        int64   `json:"id"`
	Container string  `json:"container"`  // a service in the compose file
	Port      int     `json:"port"`       // its container port
	Expose    string  `json:"expose"`     // tailscale, cloudflare or public
	Host      *string `json:"host"`       // the full hostname for cloudflare and public; null for tailscale
	HTTPSPort *int    `json:"https_port"` // tailscale only: the HTTPS port on the machine's tailnet name
	LocalPort int     `json:"local_port"` // the 127.0.0.1 port the container is published on
	// URL is where the route answers: https://<host>, or https://<tailnet name>:<https_port>. It is
	// null for a tailscale route until the machine's daemon has said its tailnet name.
	URL       *string   `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RouteInput is one route of a service's input. The hub assigns local_port, and a tailscale route's
// https_port when it is left out.
type RouteInput struct {
	Container string  `json:"container" jsonschema:"a service in the compose file, such as web"`
	Port      int     `json:"port" jsonschema:"its container port, such as 80"`
	Expose    string  `json:"expose" jsonschema:"tailscale (on the machine's tailnet name), cloudflare (through a Cloudflare tunnel) or public (the machine's own Caddy, ports 80 and 443)"`
	Host      *string `json:"host,omitempty" jsonschema:"the full hostname, such as app.example.com: required for cloudflare and public, and not for tailscale"`
	HTTPSPort *int    `json:"https_port,omitempty" jsonschema:"tailscale only: the HTTPS port on the machine's tailnet name; left out, the hub picks the lowest free one from 10000"`
}

// Input is the body of POST /v1/machines/{machine}/services: a new managed service.
type Input struct {
	Name    string            `json:"name"`
	Compose string            `json:"compose"`
	Files   map[string]string `json:"files,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Routes  []RouteInput      `json:"routes,omitempty"`
}

// Change is what a PATCH of a managed service changes. A field left nil stays as it is.
type Change struct {
	Compose *string
	// Files and Env are merged into the stored ones: a nil value removes the key. ClearFiles
	// removes every file first, for a patch whose files is null.
	Files      map[string]*string
	ClearFiles bool
	Env        map[string]*string
	ClearEnv   bool
	Routes     *[]RouteInput // replaces every route
}

// empty says the change changes nothing.
func (c Change) empty() bool {
	return c.Compose == nil && c.Files == nil && !c.ClearFiles && c.Env == nil && !c.ClearEnv && c.Routes == nil
}

// Queued is the answer of a route or tool that queues an action for a service: the service and
// the action.
type Queued struct {
	Service Service `json:"service" jsonschema:"the service, as get_service returns it"`
	Action  any     `json:"action" jsonschema:"the action, as get_action returns it"`

	actionID int64
}

// Action is an action the actions domain queued for a service.
type Action struct {
	ID     int64
	Status string  // pending, in_progress, completed or failed
	Output *string // compose's output, or why it failed
	Action any     // the whole action, as the API shows it
}

// Registry is a container registry managed services pull private images from, as the API shows it.
type Registry struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Host        string    `json:"host"` // such as ghcr.io
	Username    string    `json:"username"`
	Password    string    `json:"password"` // always "***"
	PasswordSet bool      `json:"password_set"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RegistryInput is the body of POST /v1/registries.
type RegistryInput struct {
	Name     string `json:"name" jsonschema:"a name for the registry: lowercase letters, digits and dashes"`
	Host     string `json:"host" jsonschema:"the registry's host, as image names start with it, such as ghcr.io or registry.example.com:5000; docker.io for Docker Hub"`
	Username string `json:"username" jsonschema:"the login name"`
	Password string `json:"password" jsonschema:"the password or access token, which the hub stores encrypted and never shows"`
}

// RegistryPatch is the merge-patch body of PATCH /v1/registries/{registry}. A password of "***"
// keeps the stored one.
type RegistryPatch struct {
	Host     *string `json:"host,omitempty"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
}

// rawPatch is the body of PATCH /v1/machines/{machine}/services/{service}: a field left out is
// nil, and one given as null is "null".
type rawPatch struct {
	AlertsEnabled *bool           `json:"alerts_enabled"`
	Compose       json.RawMessage `json:"compose"`
	Files         json.RawMessage `json:"files"`
	Env           json.RawMessage `json:"env"`
	Routes        json.RawMessage `json:"routes"`
}

// actInput is the optional body of POST /v1/machines/{machine}/services/{service}/start, stop and
// restart.
type actInput struct {
	Container *string `json:"container"`
}

// RunningReport is a report of the containers Docker listed, [] when there are none.
func RunningReport(list []ContainerInput) Report {
	if list == nil {
		list = []ContainerInput{}
	}
	return Report{Docker: DockerRunning, Containers: &list}
}

// UnreachableReport is a report that the daemon can't reach Docker, and why. It carries no list.
func UnreachableReport(reason string) Report {
	return Report{Docker: DockerUnreachable, DockerReason: reason}
}

// OffReport is a report that Docker is turned off on the machine. It carries no list and no reason.
func OffReport() Report { return Report{Docker: DockerOff} }
