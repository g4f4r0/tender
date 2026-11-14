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
// It never holds credentials; each machine keeps the keys for its own destination.
type Destination struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`     // s3
	Endpoint  string    `json:"endpoint"` // https://<account>.r2.cloudflarestorage.com
	Bucket    string    `json:"bucket"`
	Path      string    `json:"path"` // folder inside the bucket, may be empty
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Repository is the restic repository of one machine at this destination: a folder named after
// the machine, so machines can share a bucket. Each machine has its own repository because
// restic's prune and check lock the whole repository.
func (d Destination) Repository(machine string) string {
	repo := "s3:" + d.Endpoint + "/" + d.Bucket
	if d.Path != "" {
		repo += "/" + d.Path
	}
	return repo + "/" + machine
}

// DestinationInput is the body of POST /v1/backups/destinations. The secrets are write-only:
// the hub stores them encrypted and never returns them on a people route. Without a password,
// the hub generates one and returns it once, in DestinationCreated.
type DestinationInput struct {
	Name            string `json:"name"`
	Type            string `json:"type"` // s3, the default
	Endpoint        string `json:"endpoint"`
	Bucket          string `json:"bucket"`
	Path            string `json:"path"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Password        string `json:"password,omitempty"` // encrypts every machine's repository at this destination
}

// DestinationPatch is the JSON Merge Patch body of PATCH /v1/backups/destinations/{destination}.
// The password can't change: the repositories are encrypted with it.
type DestinationPatch struct {
	Endpoint        *string `json:"endpoint,omitempty"`
	Bucket          *string `json:"bucket,omitempty"`
	Path            *string `json:"path,omitempty"`
	AccessKeyID     *string `json:"access_key_id,omitempty"`
	SecretAccessKey *string `json:"secret_access_key,omitempty"`
}

// DestinationCreated is the 201 response of POST /v1/backups/destinations. Password is set only
// when the hub generated it, and this is the only time it is shown.
type DestinationCreated struct {
	Destination
	Password string `json:"password,omitempty"`
}

// Credentials are what an agent needs to open its repository. Only the agent's own
// GET /v1/machines/{machine}/backups/settings, with its token, includes them.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Password        string `json:"password"`
}

// Settings is a machine's backup settings, as GET /v1/machines/{machine}/backups/settings returns it.
type Settings struct {
	Destination   Destination  `json:"destination"`
	Schedule      string       `json:"schedule"` // standard 5-field cron, UTC
	RetentionDays int          `json:"retention_days"`
	Credentials   *Credentials `json:"credentials,omitempty"` // only for the machine's own agent
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
	CreatedAt  time.Time `json:"created_at"` // when the run started, by the agent's clock
	UpdatedAt  time.Time `json:"updated_at"`
}

// HistoryInput is the body of POST /v1/machines/{machine}/backups/history.
// Online, the agent posts status in_progress when a run starts and PATCHes it when it ends.
// When the hub was unreachable during the run, the agent posts the finished run once, later,
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
