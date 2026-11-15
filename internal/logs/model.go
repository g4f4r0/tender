package logs

import (
	"time"
)

// Sources says which logs go to a sink. A source left out (null) sends nothing.
type Sources struct {
	Journal  *JournalSource  `json:"journal"`
	Services *ServicesSource `json:"services"`
}

// JournalSource is the journal's entries for Units, all units when it is empty, of MinPriority
// or more severe.
type JournalSource struct {
	Units       []string `json:"units"`
	MinPriority string   `json:"min_priority"`
}

// ServicesSource is the logs of the Docker containers called Names, all of them when it is empty.
type ServicesSource struct {
	Names []string `json:"names"`
}

// DefaultSources is every unit's journal from info up, and every container.
func DefaultSources() Sources {
	return Sources{Journal: &JournalSource{Units: []string{}, MinPriority: "info"}, Services: &ServicesSource{Names: []string{}}}
}

// Sink is a log sink as people see it: its secrets show as "***" with <field>_set.
type Sink struct {
	ID       int64          `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"`
	Enabled  bool           `json:"enabled"`
	Sources  Sources        `json:"sources"`
	Machines []string       `json:"machines"`
	// Status is what each machine's daemon last said about sending to it.
	Status    []Status  `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SinkCreated is a new sink, with the secret the hub made, shown only this once.
type SinkCreated struct {
	Sink
	Secret string `json:"secret,omitempty"`
}

// Status is one machine's sending to one sink. CreatedAt is when the machine first reported on
// the sink, and UpdatedAt its latest report.
type Status struct {
	Machine        string     `json:"machine"`
	RecordsSent    int64      `json:"records_sent"`
	RecordsDropped int64      `json:"records_dropped"`
	LastSentAt     *time.Time `json:"last_sent_at"`
	LastError      *string    `json:"last_error"`
	FailingSince   *time.Time `json:"failing_since"`
	LastDroppedAt  *time.Time `json:"last_dropped_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SinkInput adds a sink. Enabled defaults to true, Sources to DefaultSources and Machines to
// every machine.
type SinkInput struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"`
	Enabled  *bool          `json:"enabled,omitempty"`
	Sources  *Sources       `json:"sources,omitempty"`
	Machines []string       `json:"machines,omitempty"`
}

// DaemonSink is a sink as its daemons get it: enabled, for their machine, with its secrets.
type DaemonSink struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Config    map[string]any `json:"config"`
	Sources   Sources        `json:"sources"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// StatusInput is a daemon's report on one sink.
type StatusInput struct {
	SinkID         int64      `json:"sink_id"`
	RecordsSent    int64      `json:"records_sent"`
	RecordsDropped int64      `json:"records_dropped"`
	LastSentAt     *time.Time `json:"last_sent_at"`
	LastError      *string    `json:"last_error"`
	FailingSince   *time.Time `json:"failing_since"`
	LastDroppedAt  *time.Time `json:"last_dropped_at"`
}

// StatusReport is a daemon's report on every sink it sends to.
type StatusReport struct {
	Data []StatusInput `json:"data"`
}

// Health is one machine's sending to one enabled sink that is for it, for the alert rules.
type Health struct {
	Machine string
	Sink    string
	Status
}
