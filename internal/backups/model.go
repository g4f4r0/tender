package backups

import "time"

// History types and statuses, matching the CHECK constraints in migrations/001_init.sql.
const (
	TypeBackup  = "backup"
	TypeCleanup = "cleanup"
	TypeCheck   = "check"

	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

// Destination is a named place backups go, as GET /v1/backups/destinations/{destination} returns it.
// Config holds the type's settings. Its secrets show as "***", with <field>_set true, and the
// password is never part of it.
type Destination struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`     // s3, local, sftp, rest or rclone
	Config    map[string]any `json:"config"`   // the type's fields, secrets hidden
	Location  string         `json:"location"` // the restic repository, without a machine's folder
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// DaemonSettings is Settings as the machine's own daemon gets them: with the credentials, and an
// s3 destination's place where daemons before v0.3 read it, so one that isn't updated yet keeps
// backing up. A daemon reads an older hub's answer, and its own saved copy, through it too.
type DaemonSettings struct {
	Settings
	Destination DaemonDestination `json:"destination"`
}

// DaemonDestination is a destination with an s3 destination's place in the fields before v0.3.
type DaemonDestination struct {
	Destination
	Endpoint string `json:"endpoint,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
	Path     string `json:"path,omitempty"`
}

// settings returns the settings with the destination's whole config in the credentials, made
// from the fields before v0.3 when an older hub sent no config.
func (ds DaemonSettings) settings() Settings {
	s := ds.Settings
	s.Destination = ds.Destination.Destination
	if c := s.Credentials; c != nil && c.Config == nil && (s.Destination.Type == "s3" || s.Destination.Type == "") &&
		c.AccessKeyID != "" && c.SecretAccessKey != "" {
		cc := *c
		cc.Config = map[string]any{"endpoint": ds.Destination.Endpoint, "bucket": ds.Destination.Bucket, "path": ds.Destination.Path,
			"access_key_id": c.AccessKeyID, "secret_access_key": c.SecretAccessKey}
		s.Credentials = &cc
	}
	if s.Destination.Type == "" {
		s.Destination.Type = "s3"
	}
	return s
}

// DestinationInput is the body of POST /v1/backups/destinations. The secrets in Config are
// write-only: the hub stores them encrypted and never returns them on a people route. Without a
// password, the hub generates one and returns it once, in DestinationCreated.
type DestinationInput struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"` // s3, the default, local, sftp, rest or rclone
	Config   map[string]any `json:"config"`
	Password string         `json:"password,omitempty"` // encrypts every machine's repository at this destination
}

// DestinationPatch is the JSON Merge Patch body of PATCH /v1/backups/destinations/{destination}:
// fields of config to change, null to remove an optional one. A secret given as "***" stays as it
// is. The type and the password can't change.
type DestinationPatch struct {
	Config map[string]any `json:"config"`
}

// DestinationCreated is the 201 response of POST /v1/backups/destinations. Password is set only
// when the hub generated it, and this is the only time it is shown.
type DestinationCreated struct {
	Destination
	Password string `json:"password,omitempty"`
}

// Credentials are what a daemon needs to open its repository. Only the daemon's own
// GET /v1/machines/{machine}/backups/settings, with its token, includes them.
type Credentials struct {
	Password string         `json:"password"`
	Config   map[string]any `json:"config,omitempty"` // the destination's whole config, secrets in the clear

	// An s3 destination's keys, as daemons before v0.3 read them.
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// Target is a destination with everything a daemon needs to reach it, for a test action: the hub
// hands it to the daemon that runs the test, with the action, and never stores it with it.
type Target struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"` // secrets in the clear
	Password string         `json:"password"`
}

// TestInput is the body of POST /v1/backups/destinations/{destination}/test.
type TestInput struct {
	Machine string `json:"machine"` // the machine whose daemon tries the destination
}

// Settings is a machine's backup settings, as GET /v1/machines/{machine}/backups/settings returns it.
type Settings struct {
	Destination   Destination  `json:"destination"`
	Schedule      string       `json:"schedule"` // standard 5-field cron, UTC
	RetentionDays int          `json:"retention_days"`
	Credentials   *Credentials `json:"credentials,omitempty"` // only for the machine's own daemon
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// SettingsInput is the body of PUT /v1/machines/{machine}/backups/settings.
type SettingsInput struct {
	Destination   string `json:"destination"` // the destination's name
	Schedule      string `json:"schedule"`
	RetentionDays int    `json:"retention_days"`
}

// Snapshot is one restic snapshot, as GET /v1/machines/{machine}/backups/snapshots lists it.
type Snapshot struct {
	ID        int64     `json:"id"`
	ResticID  string    `json:"restic_id"`
	SizeBytes *int64    `json:"size_bytes"`
	Paths     []string  `json:"paths"`
	CreatedAt time.Time `json:"created_at"` // restic's snapshot time
	UpdatedAt time.Time `json:"updated_at"`
}

// SnapshotInput is one element of the PUT /v1/machines/{machine}/backups/snapshots body.
type SnapshotInput struct {
	ResticID  string    `json:"restic_id"`
	SizeBytes *int64    `json:"size_bytes"`
	Paths     []string  `json:"paths"`
	CreatedAt time.Time `json:"created_at"`
}

// History is one run, as GET /v1/machines/{machine}/backups/history/{id} returns it.
type History struct {
	ID         int64     `json:"id"`
	SnapshotID *int64    `json:"snapshot_id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Error      *string   `json:"error"`
	CreatedAt  time.Time `json:"created_at"` // when the run started, by the daemon's clock
	UpdatedAt  time.Time `json:"updated_at"`
}

// HistoryInput is the body of POST /v1/machines/{machine}/backups/history.
// Online, the daemon posts status in_progress when a run starts and PATCHes it when it ends.
// When the hub was unreachable during the run, the daemon posts the finished run once, later,
// with its final status, error and snapshot. Error and SnapshotID are only accepted then.
type HistoryInput struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Error      *string   `json:"error,omitempty"`
	SnapshotID *int64    `json:"snapshot_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// HistoryPatch is the JSON Merge Patch body of PATCH /v1/machines/{machine}/backups/history/{id}.
// Absent fields stay as they are.
type HistoryPatch struct {
	Status     *string `json:"status,omitempty"`
	Error      *string `json:"error,omitempty"`
	SnapshotID *int64  `json:"snapshot_id,omitempty"`
}

// Summary is one machine's backup state, as GET /v1/machines/{machine}/backups and each
// element of GET /v1/backups return it. A part that doesn't exist yet is null.
type Summary struct {
	Machine        string    `json:"machine"`
	Settings       *Settings `json:"settings"`
	LatestSnapshot *Snapshot `json:"latest_snapshot"`
	LastBackup     *History  `json:"last_backup"` // the newest run of type backup
	LastCheck      *History  `json:"last_check"`  // the newest run of type check
	SnapshotCount  int       `json:"snapshot_count"`
}
