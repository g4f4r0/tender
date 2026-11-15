package logs

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

// sourcesInput is Sources as a tool takes it, where every part may be left out.
type sourcesInput struct {
	Journal  *journalInput  `json:"journal,omitempty" jsonschema:"the journal; leave out to send none"`
	Services *servicesInput `json:"services,omitempty" jsonschema:"the Docker containers' logs; leave out to send none"`
}

type journalInput struct {
	Units       []string `json:"units,omitempty" jsonschema:"systemd units such as nginx.service; leave out or empty for all"`
	MinPriority string   `json:"min_priority,omitempty" jsonschema:"the least severe priority sent: emerg, alert, crit, err, warning, notice, info (the default) or debug"`
}

type servicesInput struct {
	Names []string `json:"names,omitempty" jsonschema:"container names; leave out or empty for all"`
}

func (in *sourcesInput) sources() *Sources {
	if in == nil {
		return nil
	}
	var out Sources
	if j := in.Journal; j != nil {
		out.Journal = &JournalSource{Units: j.Units, MinPriority: j.MinPriority}
	}
	if sv := in.Services; sv != nil {
		out.Services = &ServicesSource{Names: sv.Names}
	}
	return &out
}

type addSinkInput struct {
	Name     string         `json:"name" jsonschema:"new sink name: lowercase letters, digits and hyphens, up to 63 characters"`
	Type     string         `json:"type" jsonschema:"otlp or webhook"`
	Config   map[string]any `json:"config" jsonschema:"the type's settings; see the tool's description for each type's fields"`
	Enabled  *bool          `json:"enabled,omitempty" jsonschema:"whether daemons send to it; default true"`
	Sources  *sourcesInput  `json:"sources,omitempty" jsonschema:"which logs go; leave out for every unit's journal from info up and every container"`
	Machines []string       `json:"machines,omitempty" jsonschema:"the machines that send to it, by name; leave out or empty for every machine"`
}

type updateSinkInput struct {
	Sink     string         `json:"sink" jsonschema:"sink name, as list_log_sinks shows it"`
	Config   map[string]any `json:"config,omitempty" jsonschema:"the config fields to change; null removes an optional one, and a secret given as *** stays as it is"`
	Enabled  *bool          `json:"enabled,omitempty" jsonschema:"whether daemons send to it"`
	Sources  *sourcesInput  `json:"sources,omitempty" jsonschema:"which logs go; replaces the sources as a whole, and a source left out sends nothing"`
	Machines *[]string      `json:"machines,omitempty" jsonschema:"the machines that send to it, by name; empty for every machine"`
}

type removeSinkInput struct {
	Sink string `json:"sink" jsonschema:"sink name, as list_log_sinks shows it"`
}

// sinkFields says each type's config fields and the sources, for the tool descriptions.
const sinkFields = "otlp (OpenTelemetry, OTLP/HTTP JSON): endpoint (the collector's /v1/logs URL), headers (secret, an object of " +
	"HTTP headers such as Authorization). webhook (Tender's signed JSON, documented in docs/log-shipping.md): url, secret (secret, " +
	"at least 16 characters; leave it out and the hub makes one and returns it once). sources is {\"journal\": {\"units\": [], " +
	"\"min_priority\": \"info\"}, \"services\": {\"names\": []}}: an empty list means all, and a source left out sends nothing."

// RegisterTools adds the logs tools to m. They use the same store methods as the REST routes.
func (s *Store) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "list_log_sinks", "List log sinks",
		"List the log sinks: where each machine's daemon sends its journal and its Docker containers' logs, straight from the "+
			"machine. Each has id, name, type (otlp or webhook), config (each secret shown as *** with <field>_set true, and the URL's query string as ?***), enabled, "+
			"sources, machines (empty for every machine), and status: per machine, records_sent, records_dropped, last_sent_at, "+
			"last_error (null while sending works), failing_since (null while sending works), last_dropped_at, created_at (its first "+
			"report) and updated_at (its latest, sent every minute); and created_at and updated_at. Times are RFC 3339 UTC. The logs themselves never reach the hub, so no "+
			"tool returns them from a sink; get_machine_logs and get_service_logs read them from the machine.",
		func(ctx context.Context, _ struct{}) (server.List[Sink], error) {
			list, err := s.Sinks(ctx)
			if list == nil {
				list = []Sink{}
			}
			return server.List[Sink]{Data: list}, err
		})

	const secrets = " Secrets you pass here go through this conversation; for real keys, the owner is safer typing them into " +
		"\"tender sinks add\", which reads them from files. They are stored encrypted and no tool, route or log ever shows them again."

	mcp.AddWrite(m, "add_log_sink", "Add a log sink",
		"Add a place for every machine's daemon, or the named machines', to send logs to, straight from each machine. The config "+
			"fields by type: "+sinkFields+" Daemons start sending within a minute, from the time the sink was added; logs from "+
			"before that aren't sent. The result is the sink, as list_log_sinks shows it, with secret when the hub made the "+
			"webhook's secret, shown only this once: give it to whoever checks the signatures."+secrets,
		mcp.WriteOptions{},
		func(ctx context.Context, in addSinkInput) (SinkCreated, error) {
			return s.AddSink(ctx, SinkInput{Name: in.Name, Type: in.Type, Config: in.Config, Enabled: in.Enabled, Sources: in.Sources.sources(), Machines: in.Machines})
		})

	mcp.AddWrite(m, "update_log_sink", "Update a log sink",
		"Change a sink's config, enabled, sources or machines; anything left out stays as it is. In config, null removes an optional "+
			"field and a secret given as *** stays as it is. The name and the type can't change. Daemons pick the change up within "+
			"a minute. The result is the sink, as list_log_sinks shows it."+secrets,
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in updateSinkInput) (Sink, error) {
			patch := map[string]any{}
			if in.Config != nil {
				patch["config"] = in.Config
			}
			if in.Enabled != nil {
				patch["enabled"] = *in.Enabled
			}
			if in.Sources != nil {
				src := in.Sources.sources()
				if src.Journal != nil { // replace the journal's settings as a whole, with the defaults for what is left out
					if err := checkSources(src); err != nil {
						return Sink{}, err
					}
				}
				patch["sources"] = map[string]any{"journal": toJSON(src.Journal), "services": toJSON(src.Services)}
			}
			if in.Machines != nil {
				patch["machines"] = toJSON(*in.Machines)
			}
			if len(patch) == 0 {
				return Sink{}, server.Invalid("Give config, enabled, sources or machines to change.")
			}
			return s.PatchSink(ctx, in.Sink, patch)
		})

	mcp.AddWrite(m, "remove_log_sink", "Remove a log sink",
		"Remove a sink. Daemons stop sending to it within a minute, keep the logs they buffered for it for 7 days, then delete them; nothing sends them anywhere. "+
			"Logs already sent stay at the sink. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in removeSinkInput) (mcp.Removed, error) {
			if err := s.DeleteSink(ctx, in.Sink); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: in.Sink}, nil
		})
}
