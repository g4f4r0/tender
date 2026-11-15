package updates

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
)

type noInput struct{}

type updatesResult struct {
	Overview
	Rollouts []Rollout `json:"rollouts" jsonschema:"the rollouts the hub keeps, newest first, at most 50"`
}

type settingsInput struct {
	Mode   string `json:"mode" jsonschema:"auto: the hub rolls out each new stable release by itself; manual: only when asked"`
	Window string `json:"window,omitempty" jsonschema:"when auto mode may start a rollout, two UTC times such as 02:00-05:00; leave out for any time"`
}

type startInput struct {
	Version  string   `json:"version,omitempty" jsonschema:"the stable release to install, such as 0.3.1; leave out for the latest"`
	Machines []string `json:"machines,omitempty" jsonschema:"the machines to update, by name; leave out for every machine"`
}

const rolloutFields = "A rollout has id, version, status (in_progress, completed or failed), auto (auto mode started it), error, " +
	"machines (each with machine, position, status pending, in_progress, completed or failed, action_id and error), created_at and updated_at. "

// RegisterTools adds the updates tools to m. They use the same store methods as the REST routes.
func (s *Store) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "get_updates", "Get Tender's updates",
		"Get how Tender updates itself: mode (auto or manual) and window (UTC, or null for any time) of the hub's settings; latest, "+
			"the newest stable release the hub learned of from GitHub (version, auto: whether auto mode may roll it out, checked_at, "+
			"error); data, each machine with its version, updates (its own setting: inherit, auto or manual), mode (what applies) and "+
			"update_available; rollout, the one in progress or null; and rollouts, the last 50, newest first. "+rolloutFields+
			"The hub checks GitHub every 6 hours.",
		func(ctx context.Context, _ noInput) (updatesResult, error) {
			o, err := s.Overview(ctx)
			if err != nil {
				return updatesResult{}, err
			}
			list, err := s.Rollouts(ctx)
			return updatesResult{Overview: o, Rollouts: list}, err
		})

	mcp.AddWrite(m, "set_update_settings", "Set the update settings",
		"Set how Tender updates itself on every machine that inherits the hub's mode: auto, where the hub rolls each new stable "+
			"release out, one machine at a time with the hub's own machine first, inside the window; or manual, where nothing updates "+
			"until start_update. A machine can override the mode with update_machine. The result is the settings: mode, window, "+
			"created_at and updated_at.",
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in settingsInput) (Settings, error) {
			var w *string
			if in.Window != "" {
				w = &in.Window
			}
			return s.SetSettings(ctx, SettingsInput{Mode: in.Mode, Window: w})
		})

	mcp.AddWrite(m, "start_update", "Update Tender on machines",
		"Start a rollout that updates Tender to a stable release, the latest by default, on the machines named or on every machine. "+
			"It updates one machine at a time, the hub's own machine first. Each machine checks the release's signature, waits up to 30 "+
			"minutes for a running backup, swaps the binary and restarts; if it isn't healthy within 2 minutes it rolls back, and the "+
			"rollout stops there with an update.failed alert. It refuses prereleases and downgrades, and machines before v0.3.0. "+
			"The result is the rollout, which goes on after the tool returns: follow it with get_updates. "+rolloutFields,
		mcp.WriteOptions{OpenWorld: true},
		func(ctx context.Context, in startInput) (Rollout, error) {
			return s.Start(ctx, RolloutInput(in))
		})
}
