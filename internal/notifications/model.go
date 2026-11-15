package notifications

import "time"

// Severities and statuses, matching the CHECK constraints in migrations/001_init.sql.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	StatusPending  = "pending"
	StatusSent     = "sent"
	StatusResolved = "resolved"
)

// Notification is one alert, as GET /v1/notifications lists it.
type Notification struct {
	ID        int64     `json:"id"`
	Machine   *string   `json:"machine"` // the name of the machine the alert is about
	Service   *string   `json:"service"` // the Docker service's name; null for an alert about something else
	Type      string    `json:"type"`
	Severity  string    `json:"severity"` // info, warning or critical
	Status    string    `json:"status"`   // pending, sent or resolved
	Content   string    `json:"content"`
	Channel   *string   `json:"channel"` // the channel a channel_failing alert is about; else null
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Deliveries []Delivery `json:"deliveries"` // what went to each channel, oldest first
}

// Channel is a place alerts go, as GET /v1/notifications/channels lists it. Its config shows each
// secret as "***", with <name>_set true; only the answer to the create shows a secret the hub
// made, once.
type Channel struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"` // email, slack, webhook or exec
	Config      map[string]any `json:"config"`
	Enabled     bool           `json:"enabled"`
	MinSeverity string         `json:"min_severity"` // info, warning or critical: the least severe alert it gets
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
}

// ChannelPatch is the merge-patch body of PATCH /v1/notifications/channels/{channel}. A config
// key replaces that value, a secret too; null removes an optional one.
type ChannelPatch struct {
	Config      map[string]*string `json:"config,omitempty"`
	Enabled     *bool              `json:"enabled,omitempty"`
	MinSeverity *string            `json:"min_severity,omitempty"`
}

// Delivery is one event an alert owes a channel, or one test.
type Delivery struct {
	ID             int64     `json:"id"`
	NotificationID *int64    `json:"notification_id"` // null for a test
	Channel        string    `json:"channel"`
	Event          string    `json:"event"`  // opened, escalated, resolved or test
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
