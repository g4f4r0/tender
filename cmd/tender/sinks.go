package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/logs"
)

var sinksUsage = groupUsage("sinks", "Manage the log sinks: where each machine's daemon sends its journal and its Docker "+
	"containers' logs, straight from the machine. The logs never pass through the hub.", []row{
	{"list", "List the sinks and how sending to them goes"},
	{"add NAME --type TYPE ...", "Add a sink"},
	{"show NAME", "Show a sink and each machine's sending to it"},
	{"update NAME ...", "Change a sink"},
	{"remove NAME", "Remove a sink"},
})

// runSinks runs "tender sinks list|add|show|update|remove".
func runSinks(args []string, s session) error {
	return runGroup("sinks", sinksUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return sinksList(a, s) },
		"add":    func(a []string) error { return sinksAdd(a, s) },
		"show":   func(a []string) error { return sinksShow(a, s) },
		"update": func(a []string) error { return sinksUpdate(a, s) },
		"remove": func(a []string) error { return sinksRemove(a, s) },
	})
}

func sinkPath(name string) string { return "/v1/logs/sinks/" + url.PathEscape(name) }

// sinkTarget is where a sink sends: its endpoint or URL.
func sinkTarget(k logs.Sink) string {
	for _, f := range []string{"endpoint", "url"} {
		if v, ok := k.Config[f].(string); ok {
			return v
		}
	}
	return "-"
}

// sinkHealth sums up how sending to a sink goes on the machines that reported on it.
func sinkHealth(k logs.Sink) string {
	if !k.Enabled {
		return "disabled"
	}
	if len(k.Status) == 0 {
		return "no report yet"
	}
	var bad []string
	for _, st := range k.Status {
		switch {
		case st.FailingSince != nil:
			bad = append(bad, "failing on "+st.Machine)
		case st.LastDroppedAt != nil && (st.LastSentAt == nil || st.LastDroppedAt.After(*st.LastSentAt)):
			bad = append(bad, "dropped on "+st.Machine)
		}
	}
	if len(bad) == 0 {
		return "ok"
	}
	return strings.Join(bad, ", ")
}

func orAll(names []string) string {
	if len(names) == 0 {
		return "all"
	}
	return strings.Join(names, ",")
}

// describeSources is a sink's sources in a few words, such as "journal (info and up), services".
func describeSources(src logs.Sources) string {
	var parts []string
	if j := src.Journal; j != nil {
		p := "journal (" + j.MinPriority + " and up"
		if len(j.Units) > 0 {
			p += ", " + strings.Join(j.Units, ",")
		}
		parts = append(parts, p+")")
	}
	if sv := src.Services; sv != nil {
		p := "services"
		if len(sv.Names) > 0 {
			p += " (" + strings.Join(sv.Names, ",") + ")"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

func sinksList(args []string, s session) error {
	fs := newFlagSet("sinks list", "[--hub URL]", "List the log sinks, with how sending to them goes. Their secrets are never shown.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	list, err := listAll[logs.Sink](hub, "/v1/logs/sinks")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No log sinks yet. Add one with: tender sinks add NAME --type otlp --endpoint URL")
		return nil
	}
	rows := make([][]string, len(list))
	for i, k := range list {
		rows[i] = []string{k.Name, k.Type, sinkTarget(k), orAll(k.Machines), sinkHealth(k)}
	}
	printTable(s.out, []string{"NAME", "TYPE", "SENDS TO", "MACHINES", "STATUS"}, rows)
	return nil
}

// sinkFlags are the options of sinks add and update.
type sinkFlags struct {
	endpoint, url, headersFile, secretFile *string
	units, minPriority, services, machines *string
	noJournal, noServices                  *bool
}

func addSinkFlags(fs *flag.FlagSet) sinkFlags {
	return sinkFlags{
		endpoint:    fs.String("endpoint", "", "`URL` otlp: the collector's OTLP/HTTP logs URL, such as https://otel.example.com:4318/v1/logs"),
		headersFile: fs.String("headers-file", "", "`PATH` otlp: file of HTTP headers, one \"Name: value\" a line"),
		url:         fs.String("url", "", "`URL` webhook: where to POST the batches"),
		secretFile:  fs.String("secret-file", "", "`PATH` webhook: file with the signing secret; without it the hub makes one"),
		units:       fs.String("units", "", "`UNITS` the journal's units, comma-separated, or all"),
		minPriority: fs.String("min-priority", "", "`PRIORITY` the journal's least severe priority sent (default info)"),
		services:    fs.String("services", "", "`NAMES` the containers, comma-separated, or all"),
		machines:    fs.String("machines", "", "`NAMES` the machines that send, comma-separated, or all"),
		noJournal:   fs.Bool("no-journal", false, "send no journal"),
		noServices:  fs.Bool("no-services", false, "send no container logs"),
	}
}

// list splits a comma-separated list; all, or nothing, is the empty list, which means all.
func list(v string) []string {
	out := []string{}
	if v == "all" {
		return out
	}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readHeaders reads "Name: value" lines from path; blank lines and lines starting with # are skipped.
func readHeaders(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s. Check the path and that you can read it: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]any{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d of %s isn't a header. Write one \"Name: value\" a line, such as Authorization: Bearer KEY", n, path)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return out, sc.Err()
}

// patch turns the options set on the command line into the sink fields they change: config,
// sources and machines, as a JSON merge patch.
func (f sinkFlags) patch(fs *flag.FlagSet) (map[string]any, error) {
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	out := map[string]any{}
	config := map[string]any{}
	if set["endpoint"] {
		config["endpoint"] = *f.endpoint
	}
	if set["url"] {
		config["url"] = *f.url
	}
	if set["headers-file"] {
		h, err := readHeaders(*f.headersFile)
		if err != nil {
			return nil, err
		}
		config["headers"] = h
	}
	if set["secret-file"] {
		v, err := readFileLine(*f.secretFile)
		if err != nil {
			return nil, err
		}
		config["secret"] = v
	}
	if len(config) > 0 {
		out["config"] = config
	}
	sources := map[string]any{}
	journal := map[string]any{}
	if set["units"] {
		journal["units"] = list(*f.units)
	}
	if set["min-priority"] {
		journal["min_priority"] = *f.minPriority
	}
	switch {
	case *f.noJournal && len(journal) > 0:
		return nil, usageError(fs, "--no-journal can't go with --units or --min-priority")
	case *f.noJournal:
		sources["journal"] = nil
	case len(journal) > 0:
		sources["journal"] = journal
	}
	switch {
	case *f.noServices && set["services"]:
		return nil, usageError(fs, "--no-services can't go with --services")
	case *f.noServices:
		sources["services"] = nil
	case set["services"]:
		sources["services"] = map[string]any{"names": list(*f.services)}
	}
	if len(sources) > 0 {
		out["sources"] = sources
	}
	if set["machines"] {
		out["machines"] = list(*f.machines)
	}
	return out, nil
}

func sinksAdd(args []string, s session) error {
	fs := newFlagSet("sinks add", "NAME --type TYPE [OPTIONS] [--hub URL]",
		"Add a log sink. Each machine's daemon starts sending to it within a minute, from then on; logs from before aren't "+
			"sent. Secrets come from files, so they stay out of your shell history.\n\n"+
			"otlp --endpoint URL [--headers-file PATH] sends OTLP/HTTP JSON to an OpenTelemetry collector or service.\n\n"+
			"webhook --url URL [--secret-file PATH] POSTs signed JSON batches, as docs/log-shipping.md describes. Without a "+
			"secret, the hub makes one and shows it once.\n\n"+
			"By default every machine sends every unit's journal from info up and every container's logs.")
	hubURL := hubFlag(fs)
	typ := fs.String("type", "", "`TYPE` otlp or webhook")
	disabled := fs.Bool("disabled", false, "add it without sending to it yet")
	flags := addSinkFlags(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a sink name")
	if err != nil {
		return err
	}
	if *typ == "" {
		return usageError(fs, "sinks add needs --type otlp or --type webhook")
	}
	p, err := flags.patch(fs)
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	in := map[string]any{"name": name, "type": *typ, "config": map[string]any{}}
	if c, ok := p["config"]; ok {
		in["config"] = c
	}
	if m, ok := p["machines"]; ok {
		in["machines"] = m
	}
	if src, ok := p["sources"].(map[string]any); ok {
		full := map[string]any{"journal": map[string]any{}, "services": map[string]any{}}
		for k, v := range src {
			full[k] = v
		}
		in["sources"] = full
	}
	if *disabled {
		in["enabled"] = false
	}
	var created logs.SinkCreated
	if err := hub.do(http.MethodPost, "/v1/logs/sinks", in, &created); err != nil {
		return err
	}
	say(s.out, "Added log sink %s. Machines start sending to it within a minute.", created.Name)
	if created.Secret != "" {
		say(s.out, "\nThe signing secret, made by the hub and shown only now:")
		say(s.out, "  %s\n", created.Secret)
		say(s.out, "%s", wrap("Give it to the receiver, which checks each batch's Tender-Signature header with it. Tender cannot show it "+
			"again; to replace it, run tender sinks update "+created.Name+" --secret-file PATH.", lineWidth, ""))
	}
	return nil
}

func sinksShow(args []string, s session) error {
	fs := newFlagSet("sinks show", "NAME [--hub URL]", "Show a log sink and how sending to it goes on each machine. Its secrets are never shown.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a sink name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var k logs.Sink
	if err := hub.do(http.MethodGet, sinkPath(name), nil, &k); err != nil {
		return err
	}
	enabled := "yes"
	if !k.Enabled {
		enabled = "no"
	}
	printTable(s.out, []string{"NAME", k.Name}, [][]string{
		{"TYPE", k.Type}, {"SENDS TO", sinkTarget(k)}, {"ENABLED", enabled}, {"SOURCES", describeSources(k.Sources)}, {"MACHINES", orAll(k.Machines)},
	})
	if len(k.Status) == 0 {
		say(s.out, "\nNo machine has reported on it yet. Daemons report every minute.")
		return nil
	}
	say(s.out, "")
	rows := make([][]string, len(k.Status))
	for i, st := range k.Status {
		last, problem := "never", "-"
		if st.LastSentAt != nil {
			last = ago(s.now(), *st.LastSentAt)
		}
		switch {
		case st.FailingSince != nil && st.LastError != nil:
			problem = "failing since " + ago(s.now(), *st.FailingSince) + ": " + *st.LastError
		case st.FailingSince != nil:
			problem = "failing since " + ago(s.now(), *st.FailingSince)
		case st.LastDroppedAt != nil && (st.LastSentAt == nil || st.LastDroppedAt.After(*st.LastSentAt)):
			problem = "dropped logs " + ago(s.now(), *st.LastDroppedAt)
		}
		rows[i] = []string{st.Machine, fmt.Sprint(st.RecordsSent), fmt.Sprint(st.RecordsDropped), last, problem}
	}
	printTable(s.out, []string{"MACHINE", "SENT", "DROPPED", "LAST SENT", "PROBLEM"}, rows)
	return nil
}

func sinksUpdate(args []string, s session) error {
	fs := newFlagSet("sinks update", "NAME [OPTIONS] [--hub URL]",
		"Change a log sink. Only the options you give change; the others stay. The options are those of \"tender sinks add\", "+
			"and --enable or --disable. The name and the type can't change. Daemons pick the change up within a minute.")
	hubURL := hubFlag(fs)
	enable := fs.Bool("enable", false, "send to it")
	disable := fs.Bool("disable", false, "stop sending to it, keeping it")
	flags := addSinkFlags(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a sink name")
	if err != nil {
		return err
	}
	if *enable && *disable {
		return usageError(fs, "--enable and --disable can't go together")
	}
	p, err := flags.patch(fs)
	if err != nil {
		return err
	}
	if *enable || *disable {
		p["enabled"] = *enable
	}
	if len(p) == 0 {
		return usageError(fs, "sinks update needs an option to change")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var k logs.Sink
	if err := hub.do(http.MethodPatch, sinkPath(name), p, &k); err != nil {
		return err
	}
	say(s.out, "Updated log sink %s. Machines pick the change up within a minute.", k.Name)
	return nil
}

func sinksRemove(args []string, s session) error {
	fs := newFlagSet("sinks remove", "NAME [--hub URL]",
		"Remove a log sink. Daemons stop sending to it within a minute and delete the logs they buffered for it. "+
			"The logs already sent stay at the sink.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a sink name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, sinkPath(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed log sink %s. The logs already sent stay at the sink.", name)
	return nil
}
