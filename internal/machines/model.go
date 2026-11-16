package machines

import "time"

// Machine is an enrolled machine, as GET /v1/machines and /v1/machines/{machine} return it.
type Machine struct {
	ID                   int64     `json:"id"`
	Name                 string    `json:"name"`
	Version              *string   `json:"version"`                // the Tender version its daemon last reported, or null
	Updates              string    `json:"updates"`                // inherit, auto or manual
	SecurityAllowedPorts []string  `json:"security_allowed_ports"` // ports such as 41641/udp, and programs such as cloudflared, that may listen on a public address
	SecurityAlerts       bool      `json:"security_alerts"`        // whether its security alerts are on
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// The update modes of a machine.
const (
	UpdatesInherit = "inherit"
	UpdatesAuto    = "auto"
	UpdatesManual  = "manual"
)

// Patch is the JSON Merge Patch body of PATCH /v1/machines/{machine}.
type Patch struct {
	Updates              *string   `json:"updates,omitempty"`
	SecurityAllowedPorts *[]string `json:"security_allowed_ports,omitempty"`
	SecurityAlerts       *bool     `json:"security_alerts,omitempty"`
}

// MachineInput is the body of POST /v1/machines.
type MachineInput struct {
	Name string `json:"name"`
}

// MachineCreated is the 201 response of POST /v1/machines. It is the only time the token is shown.
type MachineCreated struct {
	Machine
	Token string `json:"token"`
}

// Token is the response of POST /v1/machines/{machine}/token, which replaces the machine's token.
type Token struct {
	Token string `json:"token"`
}

// Usage is one reading, as GET /v1/machines/{machine}/usage lists it.
type Usage struct {
	ID                int64     `json:"id"`
	CPULoad           float64   `json:"cpu_load"`
	MemoryUsedPercent float64   `json:"memory_used_percent"`
	DiskUsedPercent   float64   `json:"disk_used_percent"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	CreatedAt         time.Time `json:"created_at"` // when the daemon measured it
	UpdatedAt         time.Time `json:"updated_at"`
}

// UsageInput is the body of POST /v1/machines/{machine}/usage. Version is the daemon's Tender
// version; daemons before v0.3.0 leave it out.
type UsageInput struct {
	CPULoad           float64   `json:"cpu_load"`
	MemoryUsedPercent float64   `json:"memory_used_percent"`
	DiskUsedPercent   float64   `json:"disk_used_percent"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	Version           string    `json:"version,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}
