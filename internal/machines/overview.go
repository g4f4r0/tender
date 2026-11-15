package machines

import "time"

// Overview is a machine with its latest reading, as GET /v1/machines and
// GET /v1/machines/{machine} return it. It adds to Machine, so a Machine decodes from it.
type Overview struct {
	Machine
	LastSeen *time.Time `json:"last_seen"` // when the daemon measured its latest reading; null before the first
	Usage    *Usage     `json:"usage"`     // the latest reading
}
