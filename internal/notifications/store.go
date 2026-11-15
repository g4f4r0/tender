package notifications

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/server"
)

// Store reads notifications and keeps the channels and their deliveries.
type Store struct {
	db       *db.DB
	key      []byte                      // seals the channels' secrets; nil where nothing reads or writes them
	adapters map[string]channels.Adapter // the channel types; they send only once sending set them up
	hub      Hub
}

// NewStore returns a store on d. key is TENDER_ENCRYPTION_KEY, decoded: it seals the secrets in
// channel configs. Without it, channels without secrets still work.
func NewStore(d *db.DB, key []byte) *Store {
	return &Store{db: d, key: key, adapters: adapters(nil, Hub{})}
}

// sending returns a copy of the store whose channels send email through sender, nil when email
// is off, and name hub in what they send.
func (s *Store) sending(sender Sender, hub Hub) *Store {
	c := *s
	c.adapters, c.hub = adapters(sender, hub), hub
	return &c
}

// The machine's name comes from the machines table, which the schema already ties this domain to.
const selectNotifications = `
SELECT n.id, m.name, n.service, n.channel, n.event, n.severity, n.message, n.status, n.sent_at, n.created_at, n.updated_at
FROM notifications n LEFT JOIN machines m ON m.id = n.machine_id`

// latestOpen holds for a notification n that is the latest of an open problem: no newer one has
// its base event, machine, service and channel, and it is neither a .resolved one nor a test.
const latestOpen = `n.event NOT LIKE '%.resolved' AND n.event != '` + EventTest + `'
	AND NOT EXISTS (SELECT 1 FROM notifications o WHERE ` + sameProblem + ` AND o.id > n.id)`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Notification, error) {
	var n Notification
	var created, updated int64
	var sent *int64
	err := row.Scan(&n.ID, &n.Machine, &n.Service, &n.Channel, &n.Event, &n.Severity, &n.Message, &n.Status, &sent, &created, &updated)
	n.CreatedAt, n.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	if sent != nil {
		at := time.Unix(*sent, 0).UTC()
		n.SentAt = &at
	}
	return n, err
}

// Filter narrows a list of notifications. An empty field takes any value.
type Filter struct {
	Status   string // pending, sent or failed
	Severity string // info, warning or critical
	Open     bool   // only the latest notification of each open problem
}

// List returns notifications newest first, optionally of one machine (machineID 0 means every
// machine) and narrowed by f. It fetches one row more than the page limit; server.NewList cuts it.
func (s *Store) List(ctx context.Context, machineID int64, f Filter, p server.Page) ([]Notification, error) {
	switch {
	case f.Status != "" && f.Status != StatusPending && f.Status != StatusSent && f.Status != StatusFailed:
		return nil, server.Invalid("status must be pending, sent or failed.")
	case f.Severity != "" && !validSeverityName(f.Severity):
		return nil, server.Invalid("severity must be info, warning or critical.")
	}
	where := `WHERE (? = 0 OR n.machine_id = ?) AND (? = '' OR n.status = ?) AND (? = '' OR n.severity = ?)`
	if f.Open {
		where += ` AND ` + latestOpen
	}
	rows, err := s.db.Read.QueryContext(ctx, selectNotifications+` `+where+`
		  AND (n.created_at, n.id) < (?, ?)
		ORDER BY n.created_at DESC, n.id DESC LIMIT ?`,
		machineID, machineID, f.Status, f.Status, f.Severity, f.Severity, p.Before.CreatedAt, p.Before.ID, p.Size())
	if err != nil {
		return nil, err
	}
	out, err := scanAll(rows)
	if err != nil {
		return nil, err
	}
	return out, s.withDeliveries(ctx, out)
}

func scanAll(rows *sql.Rows) ([]Notification, error) {
	defer func() { _ = rows.Close() }()
	var out []Notification
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Get returns one of a machine's notifications by the hub's id.
func (s *Store) Get(ctx context.Context, machineID, id int64) (Notification, error) {
	n, err := scan(s.db.Read.QueryRowContext(ctx, selectNotifications+` WHERE n.machine_id = ? AND n.id = ?`, machineID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, server.NotFound("Notification %d doesn't exist on this machine.", id)
	}
	if err != nil {
		return n, err
	}
	ns := []Notification{n}
	err = s.withDeliveries(ctx, ns)
	return ns[0], err
}

// key names a problem: its base event, the machine's name and, for a service's problem, the
// service's name, or for a channel's, the channel's name.
type key struct{ base, machine, service, channel string }

func keyOf(n Notification) key {
	k := key{base: channels.Base(n.Event)}
	for _, f := range []struct {
		from *string
		to   *string
	}{{n.Machine, &k.machine}, {n.Service, &k.service}, {n.Channel, &k.channel}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	return k
}

// about names what a problem is about, for the log.
func (k key) about() string {
	if k.channel != "" {
		return "channel " + k.channel
	}
	return k.machine
}

// open returns the latest notification of each open problem of a registered event, by key.
func (s *Store) open(ctx context.Context) (map[key]Notification, error) {
	rows, err := s.db.Read.QueryContext(ctx, selectNotifications+` WHERE `+latestOpen+` AND (m.id IS NOT NULL OR n.channel IS NOT NULL)`)
	if err != nil {
		return nil, err
	}
	ns, err := scanAll(rows)
	out := map[key]Notification{}
	for _, n := range ns {
		if _, ok := eventType(n.Event); ok {
			out[keyOf(n)] = n
		}
	}
	return out, err
}

func sortedByID(m map[key]Notification) []Notification {
	out := make([]Notification, 0, len(m))
	for _, n := range m {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b Notification) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// add adds a pending notification of event about a machine (machineID) or a channel (channel,
// with machineID 0), and returns it.
func add(ctx context.Context, tx *sql.Tx, machineID int64, service, channel, event, severity, message string, now time.Time) (Notification, error) {
	var id any // NULL for a channel's notification or a test
	if machineID != 0 {
		id = machineID
	}
	var nid int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO notifications (machine_id, service, channel, event, severity, message, status, created_at, updated_at)
		 VALUES (?, nullif(?, ''), nullif(?, ''), ?, ?, ?, 'pending', ?, ?) RETURNING id`,
		id, service, channel, event, severity, message, now.Unix(), now.Unix()).Scan(&nid)
	if err != nil {
		return Notification{}, err
	}
	return scan(tx.QueryRowContext(ctx, selectNotifications+` WHERE n.id = ?`, nid))
}
