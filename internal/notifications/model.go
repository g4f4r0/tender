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
	Machine   *string   `json:"machine"` // the machine's name; null for an alert about the hub itself
	Type      string    `json:"type"`
	Severity  string    `json:"severity"` // info, warning or critical
	Status    string    `json:"status"`   // pending, sent or resolved
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Recipient is an email address that gets every alert, as GET /v1/notifications/recipients lists it.
type Recipient struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecipientInput is the body of POST /v1/notifications/recipients.
type RecipientInput struct {
	Email string `json:"email"`
}

// TestResult is the 202 response of POST /v1/notifications/test.
type TestResult struct {
	SentTo []string `json:"sent_to"`
}
