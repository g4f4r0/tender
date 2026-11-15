package notifications

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type listInput struct {
	Machine  string `json:"machine,omitempty" jsonschema:"only this machine's notifications; leave out for every machine"`
	Status   string `json:"status,omitempty" jsonschema:"only notifications with this status: pending, sent or resolved"`
	Severity string `json:"severity,omitempty" jsonschema:"only notifications with this severity: info, warning or critical"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most notifications to return, 1 to 500, default 50"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"next_cursor from the previous page"`
}

type addChannelInput struct {
	Name        string            `json:"name" jsonschema:"the channel's name: lowercase letters, digits and hyphens, such as ops-slack"`
	Type        string            `json:"type" jsonschema:"email, slack, webhook or exec"`
	Config      map[string]string `json:"config" jsonschema:"email: {\"to\": \"ops@example.com\"}; slack: {\"webhook_url\": \"https://hooks.slack.com/services/...\"}; webhook: {\"url\": \"https://...\"}, and the hub makes the signing secret; exec: {\"command\": \"my-adapter\"}, a program root installed in /etc/tender/adapters on the hub"`
	Enabled     *bool             `json:"enabled,omitempty" jsonschema:"whether it gets alerts; default true"`
	MinSeverity string            `json:"min_severity,omitempty" jsonschema:"the least severe alert it gets: info, warning or critical; default warning"`
}

type updateChannelInput struct {
	Channel     string             `json:"channel" jsonschema:"the channel's name"`
	Config      map[string]*string `json:"config,omitempty" jsonschema:"config keys to change, such as {\"to\": \"oncall@example.com\"}; a secret can be replaced, never read; leave a key out to keep it"`
	Enabled     *bool              `json:"enabled,omitempty" jsonschema:"turn it on (true) or off (false)"`
	MinSeverity *string            `json:"min_severity,omitempty" jsonschema:"info, warning or critical"`
}

type channelInput struct {
	Channel string `json:"channel" jsonschema:"the channel's name, as list_channels shows it"`
}

type testInput struct {
	Channel string `json:"channel,omitempty" jsonschema:"only this channel, even if it is off; leave out for every enabled channel"`
}

type noInput struct{}

// RegisterTools adds the notifications tools to s. They use the same store methods as the REST
// routes. lookup resolves machine names to ids. send_test_notification sends email through
// sender, which is nil when email is off, and names hub in what it sends.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup, sender Sender, hub Hub) {
	mcp.AddReadOnly(m, "list_notifications", "List notifications",
		"List alerts, newest first, optionally of one machine, status or severity. Each has id, machine (the machine it is about), "+
			"service (the Docker service it is about, else null), type, severity (info, warning or critical), status (pending and sent are open, resolved is closed), content, "+
			"channel (the channel a channel_failing alert is about, else null), created_at and updated_at (RFC 3339 UTC), and deliveries: "+
			"each event sent to each channel, with channel, event (opened, escalated or resolved), status (pending, delivered or failed), "+
			"attempts and error. Leave status out to get all of them. At most limit a page, 1 to 500, default 50; "+
			"pass next_cursor as cursor for older ones.",
		func(ctx context.Context, in listInput) (server.List[Notification], error) {
			var machineID int64
			if in.Machine != "" {
				id, err := mcp.Machine(ctx, lookup, in.Machine)
				if err != nil {
					return server.List[Notification]{}, err
				}
				machineID = id
			}
			page, err := server.NewPage(in.Limit, in.Cursor)
			if err != nil {
				return server.List[Notification]{}, err
			}
			items, err := s.List(ctx, machineID, in.Status, in.Severity, page)
			return server.NewList(items, page, cursor), err
		})

	const channelShape = "Each channel has id, name, type (email, slack, webhook or exec), config, enabled, min_severity (info, warning or critical: " +
		"the least severe alert it gets), created_at and updated_at (RFC 3339 UTC). A secret in config, such as a Slack webhook_url or a webhook's " +
		"secret, shows as \"***\" with <name>_set true."
	mcp.AddReadOnly(m, "list_channels", "List notification channels",
		"List the channels alerts go to, in the order they were added. "+channelShape,
		func(ctx context.Context, _ noInput) (server.List[Channel], error) {
			cs, err := s.Channels(ctx)
			return server.List[Channel]{Data: cs}, err
		})

	mcp.AddWrite(m, "add_channel", "Add a notification channel",
		"Add a channel that gets every alert at least as severe as its min_severity, from now on: an email address, a Slack incoming webhook, "+
			"a webhook that gets the signed event JSON, or an exec adapter on the hub. For a webhook the hub makes the signing secret, and this "+
			"result is the only time it shows it: pass it on to whoever runs the receiver. Call send_test_notification to check that it arrives. "+
			"The result is the channel. "+channelShape,
		mcp.WriteOptions{},
		func(ctx context.Context, in addChannelInput) (Channel, error) {
			return s.AddChannel(ctx, ChannelInput(in))
		})

	mcp.AddWrite(m, "update_channel", "Update a notification channel",
		"Change a channel's config, turn it on or off, or change its min_severity; anything left out stays as it is. A secret can be "+
			"replaced but never read. Turning a channel off drops what it still owed. The result is the channel. "+channelShape,
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in updateChannelInput) (Channel, error) {
			return s.PatchChannel(ctx, in.Channel, ChannelPatch{Config: in.Config, Enabled: in.Enabled, MinSeverity: in.MinSeverity})
		})

	mcp.AddWrite(m, "remove_channel", "Remove a notification channel",
		"Remove a channel, so it gets no more alerts, with its record of deliveries. Alerts are still recorded when none is left. "+
			"The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in channelInput) (mcp.Removed, error) {
			if err := s.DeleteChannel(ctx, in.Channel); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: "channel " + in.Channel}, nil
		})

	t := s.sending(sender, hub)
	mcp.AddWrite(m, "send_test_notification", "Send a test notification",
		"Send the test event to every enabled channel, or to one, once each, to check that alerts arrive. It waits for each, at most "+
			"10 seconds (30 for email). The result is data, one delivery per channel: id, notification_id (null for a test), channel, "+
			"event (test), status (delivered or failed), attempts (1), error, which says why one failed, and created_at and updated_at "+
			"(RFC 3339 UTC). It is refused when no channel is enabled.",
		mcp.WriteOptions{OpenWorld: true},
		func(ctx context.Context, in testInput) (server.List[Delivery], error) {
			return t.SendTest(ctx, in.Channel)
		})

}

func cursor(n Notification) server.Cursor {
	return server.Cursor{CreatedAt: n.CreatedAt.Unix(), ID: n.ID}
}
