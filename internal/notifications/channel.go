package notifications

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/notifications/channels"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
)

// The built-in channel types.
const (
	TypeEmail   = "email"
	TypeSlack   = "slack"
	TypeWebhook = "webhook"
	TypeExec    = "exec"
)

// AdaptersDir overrides the folder of the exec adapters, for tests only. Empty means
// /etc/tender/adapters, whose programs must be root's.
var AdaptersDir string

// adapters returns every built-in channel type. Email goes through sender, nil when email is off.
func adapters(sender Sender, hub Hub) map[string]channels.Adapter {
	return map[string]channels.Adapter{
		TypeEmail:   emailAdapter{sender: sender, hub: hub},
		TypeSlack:   channels.Slack{},
		TypeWebhook: channels.Webhook{},
		TypeExec:    channels.Exec{Dir: AdaptersDir, AnyOwner: AdaptersDir != ""},
	}
}

// typeNames lists the types, for messages.
func typeNames() string {
	names := make([]string, 0, 4)
	for name := range adapters(nil, Hub{}) {
		names = append(names, name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// stored is one channel as the hub holds it: its config in the clear, in memory only.
type stored struct {
	Channel
	config channels.Config
}

const masked = "***"

// errNoKey is what a process without TENDER_ENCRYPTION_KEY answers when a channel's secret is needed.
var errNoKey = &server.Error{Status: http.StatusServiceUnavailable,
	Detail: "This process has no TENDER_ENCRYPTION_KEY, so it can't store or read channel secrets. Set it to the hub's key and run the command again."}

// public is the channel as the API shows it: secrets as "***" with <name>_set, except the made
// fields, which the answer to a create shows once.
func (s *Store) public(c stored, made []string) Channel {
	out := c.Channel
	out.Config = map[string]any{}
	a := s.adapters[c.Type]
	for k, v := range c.config {
		out.Config[k] = v
	}
	if a == nil {
		return out
	}
	for _, f := range a.Fields() {
		if !f.Secret {
			continue
		}
		_, set := c.config[f.Name]
		out.Config[f.Name+"_set"] = set
		if set && !slices.Contains(made, f.Name) {
			out.Config[f.Name] = masked
		}
	}
	return out
}

// seal turns cfg into the JSON the database keeps: each secret field encrypted, in base64.
func (s *Store) seal(typ string, cfg channels.Config) (string, error) {
	out := map[string]string{}
	for k, v := range cfg {
		out[k] = v
	}
	for _, f := range s.adapters[typ].Fields() {
		v, ok := cfg[f.Name]
		if !f.Secret || !ok {
			continue
		}
		if len(s.key) == 0 {
			return "", errNoKey
		}
		b, err := secret.Seal(s.key, []byte(v))
		if err != nil {
			return "", fmt.Errorf("seal channel secret: %w", err)
		}
		out[f.Name] = base64.StdEncoding.EncodeToString(b)
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

// unseal reads the stored JSON of a channel of type typ. With open, it opens the secrets;
// without, they stay sealed, which is enough to show the channel.
func (s *Store) unseal(name, typ, raw string, open bool) (channels.Config, error) {
	var cfg channels.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("the config of channel %s is damaged: %w", name, err)
	}
	if cfg == nil {
		cfg = channels.Config{}
	}
	a := s.adapters[typ]
	if a == nil {
		return cfg, nil
	}
	for _, f := range a.Fields() {
		v, ok := cfg[f.Name]
		if !f.Secret || !ok || !open {
			continue
		}
		if len(s.key) == 0 {
			return nil, errNoKey
		}
		sealed, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("the %s of channel %s is damaged", f.Name, name)
		}
		plain, err := secret.Open(s.key, sealed)
		if err != nil {
			return nil, fmt.Errorf("open the %s of channel %s: %w. Start the hub with the TENDER_ENCRYPTION_KEY it first stored it with", f.Name, name, err)
		}
		cfg[f.Name] = string(plain)
	}
	return cfg, nil
}

const channelColumns = `id, name, type, config, enabled, min_severity, created_at, updated_at`

// loadChannels returns the channels that where selects, by id, with their secrets open when open.
func (s *Store) loadChannels(ctx context.Context, q querier, open bool, where string, args ...any) ([]stored, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+channelColumns+` FROM notification_channels `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var raws []string
	var out []stored
	for rows.Next() {
		var c stored
		var raw string
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &raw, &c.Enabled, &c.MinSeverity, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
		out, raws = append(out, c), append(raws, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].config, err = s.unseal(out[i].Name, out[i].Type, raws[i], open); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// querier is a database pool or a transaction.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) channel(ctx context.Context, q querier, open bool, name string) (stored, error) {
	cs, err := s.loadChannels(ctx, q, open, `WHERE name = ?`, name)
	if err != nil {
		return stored{}, err
	}
	if len(cs) == 0 {
		return stored{}, server.NotFound("Channel %q doesn't exist. List the channels to see their names.", name)
	}
	return cs[0], nil
}

// Channels returns every channel, in the order they were added.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	cs, err := s.loadChannels(ctx, s.db.Read, false, "")
	out := make([]Channel, len(cs))
	for i, c := range cs {
		out[i] = s.public(c, nil)
	}
	return out, err
}

// Channel returns one channel by name.
func (s *Store) Channel(ctx context.Context, name string) (Channel, error) {
	c, err := s.channel(ctx, s.db.Read, false, name)
	if err != nil {
		return Channel{}, err
	}
	return s.public(c, nil), nil
}

func validSeverity(v string) error {
	if severityRank(v) == 0 && v != SeverityInfo {
		return server.Invalid("min_severity must be info, warning or critical.")
	}
	return nil
}

// check is a 422 unless cfg has exactly the fields of the type, the required ones set, and the
// type's own check passes.
func check(a channels.Adapter, typ string, cfg channels.Config) error {
	var known []string
	for _, f := range a.Fields() {
		known = append(known, f.Name)
		if v, ok := cfg[f.Name]; f.Required && (!ok || v == "") {
			return server.Invalid("A %s channel needs config.%s.", typ, f.Name)
		}
	}
	for k := range cfg {
		if !slices.Contains(known, k) {
			return server.Invalid("A %s channel has no config.%s. Its config takes %s.", typ, k, strings.Join(known, ", "))
		}
	}
	if err := a.Check(cfg); err != nil {
		return server.Invalid("%s", err.Error())
	}
	return nil
}

// AddChannel stores a new channel. The answer shows, once, any secret the hub made for it, such
// as a webhook's signing secret.
func (s *Store) AddChannel(ctx context.Context, in ChannelInput) (Channel, error) {
	if in.MinSeverity == "" {
		in.MinSeverity = SeverityWarning
	}
	enabled := in.Enabled == nil || *in.Enabled
	cfg := channels.Config{}
	for k, v := range in.Config {
		cfg[k] = v
	}
	a := s.adapters[in.Type]
	switch {
	case !server.ValidName(in.Name):
		return Channel{}, server.Invalid("Channel names are lowercase letters, digits and hyphens, up to 63 characters, such as ops-email.")
	case a == nil:
		return Channel{}, server.Invalid("type must be one of %s.", typeNames())
	}
	if err := validSeverity(in.MinSeverity); err != nil {
		return Channel{}, err
	}
	var made []string
	if m, ok := a.(channels.Maker); ok {
		var err error
		if made, err = m.Make(cfg); err != nil {
			return Channel{}, server.Invalid("%s", err.Error())
		}
	}
	if err := check(a, in.Type, cfg); err != nil {
		return Channel{}, err
	}
	raw, err := s.seal(in.Type, cfg)
	if err != nil {
		return Channel{}, err
	}
	now := time.Now().Unix()
	res, err := s.db.Write.ExecContext(ctx,
		`INSERT INTO notification_channels (name, type, config, enabled, min_severity, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, in.Name, in.Type, raw, enabled, in.MinSeverity, now, now)
	if err != nil {
		return Channel{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Channel{}, err
	} else if n == 0 {
		return Channel{}, server.Conflict("A channel called %s already exists. Choose another name, or change that one.", in.Name)
	}
	c, err := s.channel(ctx, s.db.Write, false, in.Name)
	if err != nil {
		return Channel{}, err
	}
	c.config = cfg // with the made secrets in the clear, shown once
	return s.public(c, made), nil
}

// PatchChannel changes a channel's config, switch or min_severity. A secret can be replaced but
// never read. Turning a channel off fails what it still owed, so it sends nothing stale later.
func (s *Store) PatchChannel(ctx context.Context, name string, p ChannelPatch) (Channel, error) {
	if p.MinSeverity != nil {
		if err := validSeverity(*p.MinSeverity); err != nil {
			return Channel{}, err
		}
	}
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer func() { _ = tx.Rollback() }()
	c, err := s.channel(ctx, tx, true, name)
	if err != nil {
		return Channel{}, err
	}
	a := s.adapters[c.Type]
	if a == nil {
		return Channel{}, server.Invalid("This hub doesn't know the type %s. Update Tender, or remove the channel.", c.Type)
	}
	for k, v := range p.Config {
		f := slices.IndexFunc(a.Fields(), func(f channels.Field) bool { return f.Name == k })
		switch {
		case f >= 0 && a.Fields()[f].Secret && v != nil && *v == masked:
			return Channel{}, server.Invalid("config.%s is a secret, which the API never shows. Send its new value, or leave it out to keep it.", k)
		case v == nil:
			delete(c.config, k)
		default:
			c.config[k] = *v
		}
	}
	if err := check(a, c.Type, c.config); err != nil {
		return Channel{}, err
	}
	raw, err := s.seal(c.Type, c.config)
	if err != nil {
		return Channel{}, err
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.MinSeverity != nil {
		c.MinSeverity = *p.MinSeverity
	}
	now := time.Now()
	c.UpdatedAt = now.UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_channels SET config = ?, enabled = ?, min_severity = ?, updated_at = ? WHERE id = ?`,
		raw, c.Enabled, c.MinSeverity, now.Unix(), c.ID); err != nil {
		return Channel{}, err
	}
	if !c.Enabled {
		if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status = 'failed', error = 'The channel was turned off before this was delivered.',
			updated_at = ? WHERE channel_id = ? AND status = 'pending'`, now.Unix(), c.ID); err != nil {
			return Channel{}, err
		}
	}
	return s.public(c, nil), tx.Commit()
}

// DeleteChannel removes a channel and its deliveries.
func (s *Store) DeleteChannel(ctx context.Context, name string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM notification_channels WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return server.NotFound("Channel %q doesn't exist. List the channels to see their names.", name)
	}
	return nil
}

// CheckKey is an error unless the store's key opens every channel's secrets.
func (s *Store) CheckKey(ctx context.Context) error {
	if len(s.key) == 0 {
		return nil // nothing to check it with
	}
	_, err := s.loadChannels(ctx, s.db.Read, true, "")
	return err
}
