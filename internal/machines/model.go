package machines

import "time"

// Machine is an enrolled machine, as GET /v1/machines and /v1/machines/{machine} return it.
type Machine struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Usage is one reading, as GET /v1/machines/{machine}/usage lists it.
type Usage struct {
	ID                int64     `json:"id"`
	CPULoad           float64   `json:"cpu_load"`
	MemoryUsedPercent float64   `json:"memory_used_percent"`
	DiskUsedPercent   float64   `json:"disk_used_percent"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	CreatedAt         time.Time `json:"created_at"` // when the agent measured it
	UpdatedAt         time.Time `json:"updated_at"`
}

// UsageInput is the body of POST /v1/machines/{machine}/usage.
type UsageInput struct {
	CPULoad           float64   `json:"cpu_load"`
	MemoryUsedPercent float64   `json:"memory_used_percent"`
	DiskUsedPercent   float64   `json:"disk_used_percent"`
	UptimeSeconds     int64     `json:"uptime_seconds"`
	CreatedAt         time.Time `json:"created_at"`
}
