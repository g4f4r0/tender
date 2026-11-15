package notifications

import "time"

// Severities and statuses, matching the CHECK constraints in migrations/001_init.sql.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Notification is one message, as GET /v1/notifications lists it. It is added once and never
// reopened: a problem that gets worse or clears adds a new one, with the event
// <event>.escalated or <event>.resolved.
type Notification struct {
	ID        int64      `json:"id"`
	Machine   *string    `json:"machine"` // the name of the machine it is about
	Service   *string    `json:"service"`   // the service's name; null for one about something else
	Container *string    `json:"container"` // the container in the service; null for one about the whole service or something else
	Channel   *string    `json:"channel"` // the channel a channel.failing notification is about; else null
	Event     string     `json:"event"`   // such as service.down, service.down.escalated or service.down.resolved
	Severity  string     `json:"severity"`
	Message   string     `json:"message"`
	Status    string     `json:"status"`  // pending, sent (a channel delivered it) or failed (every channel failed)
	SentAt    *time.Time `json:"sent_at"` // when a channel first delivered it; null until then
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	Deliveries []Delivery `json:"deliveries"` // its delivery to each channel, oldest first
}

// Channel is a place notifications go, as GET /v1/notifications/channels lists it. Its config shows each
// secret as "***", with <name>_set true; only the answer to the create shows a secret the hub
// made, once.
type Channel struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"` // email, slack, webhook or exec
	Config      map[string]any `json:"config"`
	Enabled     bool           `json:"enabled"`
	MinSeverity string         `json:"min_severity"` // info, warning or critical: the least severe notification it gets
	Events      []string       `json:"events"`       // the events it gets: "*", an area such as "service.*", or an event
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// ChannelInput is the body of POST /v1/notifications/channels.
type ChannelInput struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Config      map[string]string `json:"config"`
	Enabled     *bool             `json:"enabled,omitempty"`      // default true
	MinSeverity string            `json:"min_severity,omitempty"` // default warning
	Events      []string          `json:"events,omitempty"`       // default ["*"]
}

// ChannelPatch is the merge-patch body of PATCH /v1/notifications/channels/{channel}. A config
// key replaces that value, a secret too; null removes an optional one.
type ChannelPatch struct {
	Config      map[string]*string `json:"config,omitempty"`
	Enabled     *bool              `json:"enabled,omitempty"`
	MinSeverity *string            `json:"min_severity,omitempty"`
	Events      []string           `json:"events,omitempty"`
}

// Delivery is one notification on its way to one channel.
type Delivery struct {
	ID             int64     `json:"id"`
	NotificationID int64     `json:"notification_id"`
	Channel        string    `json:"channel"`
	Status         string    `json:"status"` // pending, delivered or failed
	Attempts       int       `json:"attempts"`
	Error          *string   `json:"error"` // why the last try failed
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Delivery statuses, matching the CHECK constraint in migrations/004_channels.sql.
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// TestInput is the optional body of POST /v1/notifications/test: one channel, or every enabled one.
type TestInput struct {
	Channel string `json:"channel,omitempty"`
}
