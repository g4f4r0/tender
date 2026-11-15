package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

var channelsUsage = groupUsage("channels", "Choose where notifications go: email addresses, Slack, webhooks that get signed JSON, "+
	"and exec adapters on the hub. Each channel gets the notifications of its events at least as severe as its minimum severity.", []row{
	{"list", "List the channels"},
	{"add NAME --type TYPE ...", "Add a channel: email, slack, webhook or exec"},
	{"show NAME", "Show one channel"},
	{"update NAME ...", "Change a channel's settings, or turn it on or off"},
	{"remove NAME", "Remove a channel"},
	{"test [NAME]", "Send a test to every enabled channel, or to one"},
})

// runChannels runs "tender channels list|add|show|update|remove|test".
func runChannels(args []string, s session) error {
	return runGroup("channels", channelsUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return channelsList(a, s) },
		"add":    func(a []string) error { return channelsAdd(a, s) },
		"show":   func(a []string) error { return channelsShow(a, s) },
		"update": func(a []string) error { return channelsUpdate(a, s) },
		"remove": func(a []string) error { return channelsRemove(a, s) },
		"test":   func(a []string) error { return channelsTest(a, s) },
	})
}

func channelPath(name string) string { return "/v1/notifications/channels/" + url.PathEscape(name) }

// target is where a channel sends, as the list shows it. Secrets show as such.
func target(c notifications.Channel) string {
	for _, k := range []string{"to", "url", "command"} {
		if v, ok := c.Config[k].(string); ok {
			return v
		}
	}
	if c.Type == notifications.TypeSlack {
		return "Slack webhook (secret)"
	}
	return "-"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func channelsList(args []string, s session) error {
	fs := newFlagSet("channels list", "[--hub URL]", "List the channels notifications go to. Their secrets are never shown.")
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
	list, err := listAll[notifications.Channel](hub, "/v1/notifications/channels")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No channels yet, so notifications are only recorded. Add one with: tender channels add ops --type email --to ops@example.com")
		return nil
	}
	rows := make([][]string, len(list))
	for i, c := range list {
		rows[i] = []string{c.Name, c.Type, target(c), strings.Join(c.Events, ","), c.MinSeverity, onOff(c.Enabled)}
	}
	printTable(s.out, []string{"NAME", "TYPE", "SENDS TO", "EVENTS", "MIN SEVERITY", "ON"}, rows)
	return nil
}

// channelFlags are the options of add and update that set config.
type channelFlags struct {
	to, url, command, webhookURLFile, secretFile, minSeverity, events *string
	on, off                                                           *bool
}

// flagSet reports whether the flag name was given.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// eventList splits --events, such as "service.*,backup.failed".
func eventList(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func addChannelFlags(fs *flag.FlagSet, update bool) channelFlags {
	f := channelFlags{
		to:             fs.String("to", "", "`EMAIL` the address an email channel sends to"),
		url:            fs.String("url", "", "`URL` where a webhook channel POSTs the signed event JSON"),
		command:        fs.String("command", "", "`NAME` the program an exec channel runs, from /etc/tender/adapters on the hub"),
		webhookURLFile: fs.String("webhook-url-file", "", "`PATH` file with a Slack channel's incoming webhook URL, instead of asking"),
		minSeverity:    fs.String("min-severity", "", "`SEVERITY` the least severe notification it gets: info, warning or critical (default: warning)"),
		events:         fs.String("events", "", "`PATTERNS` the events it gets, comma-separated: *, an area such as service.*, or an event such as backup.failed (default: *)"),
		off:            fs.Bool("off", false, "turn it off: it gets no notifications until it is turned on"),
	}
	if update {
		f.secretFile = fs.String("secret-file", "", "`PATH` file with a new signing secret for a webhook channel, 24 characters or more")
		f.on = fs.Bool("on", false, "turn it on")
	}
	return f
}

func channelsAdd(args []string, s session) error {
	fs := newFlagSet("channels add", "NAME --type TYPE\n[--to EMAIL | --url URL | --webhook-url-file PATH | --command NAME]\n"+
		"[--min-severity SEVERITY] [--events PATTERNS] [--off] [--hub URL]",
		"Add a channel. An email channel needs --to and the hub's SMTP settings. A slack channel asks for its incoming webhook "+
			"URL with typing hidden, or reads it from --webhook-url-file, because the URL is its key. A webhook channel needs --url; "+
			"the hub makes its signing secret and shows it once. An exec channel needs --command, a program root installed in "+
			"/etc/tender/adapters on the hub.")
	hubURL := hubFlag(fs)
	typ := fs.String("type", "", "`TYPE` email, slack, webhook or exec")
	f := addChannelFlags(fs, false)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a channel name")
	if err != nil {
		return err
	}
	in := notifications.ChannelInput{Name: name, Type: *typ, MinSeverity: *f.minSeverity, Config: map[string]string{}}
	if set := flagSet(fs, "events"); set {
		if in.Events = eventList(*f.events); len(in.Events) == 0 {
			return usageError(fs, "--events needs at least one pattern, such as '*' or 'service.*,backup.failed'")
		}
	}
	switch *typ {
	case notifications.TypeEmail:
		in.Config["to"] = *f.to
	case notifications.TypeWebhook:
		in.Config["url"] = *f.url
	case notifications.TypeExec:
		in.Config["command"] = *f.command
	case notifications.TypeSlack:
		if in.Config["webhook_url"], err = s.secret("Slack webhook URL", *f.webhookURLFile, "webhook-url-file"); err != nil {
			return err
		}
	case "":
		return usageError(fs, "channels add needs --type: email, slack, webhook or exec")
	}
	if *f.off {
		in.Enabled = new(bool)
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var c notifications.Channel
	if err := hub.do(http.MethodPost, "/v1/notifications/channels", in, &c); err != nil {
		return err
	}
	say(s.out, "Added channel %s. It gets the notifications of %s from %s up. Check that it arrives with: tender channels test %s",
		c.Name, strings.Join(c.Events, ","), c.MinSeverity, c.Name)
	if v, ok := c.Config["secret"].(string); ok && v != "***" {
		say(s.out, "\nIts signing secret, made by the hub and shown only now:")
		say(s.out, "  %s\n", v)
		say(s.out, "%s", wrap("Give it to the webhook's receiver, which checks the Tender-Signature header with it. "+
			"Tender cannot show it again; replace it with: tender channels update "+c.Name+" --secret-file PATH", lineWidth, ""))
	}
	return nil
}

func channelsShow(args []string, s session) error {
	fs := newFlagSet("channels show", "NAME [--hub URL]", "Show one channel. Its secrets are never shown.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a channel name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var c notifications.Channel
	if err := hub.do(http.MethodGet, channelPath(name), nil, &c); err != nil {
		return err
	}
	rows := [][]string{{"name", c.Name}, {"type", c.Type}, {"on", onOff(c.Enabled)}, {"events", strings.Join(c.Events, ",")}, {"min severity", c.MinSeverity}}
	keys := make([]string, 0, len(c.Config))
	for k := range c.Config {
		if !strings.HasSuffix(k, "_set") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := fmt.Sprint(c.Config[k])
		if v == "***" {
			v = "set (secret, never shown)"
		}
		rows = append(rows, []string{k, v})
	}
	rows = append(rows, []string{"added", ago(s.now(), c.CreatedAt)}, []string{"changed", ago(s.now(), c.UpdatedAt)})
	printTable(s.out, []string{"FIELD", "VALUE"}, rows)
	return nil
}

func channelsUpdate(args []string, s session) error {
	fs := newFlagSet("channels update", "NAME [--to EMAIL | --url URL | --command NAME]\n[--webhook-url-file PATH | --secret-file PATH]\n"+
		"[--min-severity SEVERITY] [--events PATTERNS]\n[--on | --off] [--hub URL]",
		"Change a channel. Options left out stay as they are. Secrets are replaced from a file and never shown. "+
			"Turning a channel off drops what it still owed.")
	hubURL := hubFlag(fs)
	f := addChannelFlags(fs, true)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a channel name")
	if err != nil {
		return err
	}
	var p notifications.ChannelPatch
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	config := map[string]*string{}
	for _, o := range []struct {
		flag, key string
		v         *string
	}{{"to", "to", f.to}, {"url", "url", f.url}, {"command", "command", f.command}} {
		if set[o.flag] {
			config[o.key] = o.v
		}
	}
	// Secrets come from files, never from the command line, where other users could see them.
	for _, o := range []struct {
		flag, key string
		path      *string
	}{{"webhook-url-file", "webhook_url", f.webhookURLFile}, {"secret-file", "secret", f.secretFile}} {
		if !set[o.flag] {
			continue
		}
		v, err := readFileLine(*o.path)
		if err != nil {
			return err
		}
		config[o.key] = &v
	}
	if len(config) > 0 {
		p.Config = config
	}
	if set["min-severity"] {
		p.MinSeverity = f.minSeverity
	}
	if set["events"] {
		if p.Events = eventList(*f.events); len(p.Events) == 0 {
			return usageError(fs, "--events needs at least one pattern, such as '*' or 'service.*,backup.failed'")
		}
	}
	switch {
	case *f.on && *f.off:
		return usageError(fs, "channels update takes --on or --off, not both")
	case *f.on || *f.off:
		p.Enabled = f.on
	}
	if p.Config == nil && p.MinSeverity == nil && p.Enabled == nil && p.Events == nil {
		return usageError(fs, "channels update needs something to change, such as --off, --min-severity critical or --events 'service.*'")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var c notifications.Channel
	if err := hub.do(http.MethodPatch, channelPath(name), p, &c); err != nil {
		return err
	}
	say(s.out, "Updated channel %s: %s, the notifications of %s from %s up.", c.Name, onOff(c.Enabled), strings.Join(c.Events, ","), c.MinSeverity)
	return nil
}

func channelsRemove(args []string, s session) error {
	fs := newFlagSet("channels remove", "NAME [--hub URL]", "Remove a channel, so it gets no more notifications.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a channel name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, channelPath(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed channel %s. It gets no more notifications.", name)
	return nil
}

// errTestFailed says a test didn't reach every channel; the lines above it say which and why.
var errTestFailed = errors.New("the test didn't reach every channel. Fix the channel's settings with \"tender channels update\" and test again.")

func channelsTest(args []string, s session) error {
	fs := newFlagSet("channels test", "[NAME] [--hub URL]",
		"Send a test to every enabled channel, or to one, even when it is off, and say how each went. Each waits at most 10 seconds, "+
			"or 30 for email.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	var in notifications.TestInput
	switch len(rest) {
	case 0:
	case 1:
		in.Channel = rest[0]
	default:
		return usageError(fs, "channels test takes at most one channel name")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var res server.List[notifications.Delivery]
	if err := hub.do(http.MethodPost, "/v1/notifications/test", in, &res); err != nil {
		return err
	}
	rows := make([][]string, len(res.Data))
	failed := false
	for i, d := range res.Data {
		result := d.Status
		if d.Error != nil {
			result += ": " + *d.Error
		}
		failed = failed || d.Status != notifications.DeliveryDelivered
		rows[i] = []string{d.Channel, result}
	}
	printTable(s.out, []string{"CHANNEL", "RESULT"}, rows)
	if failed {
		return errTestFailed
	}
	return nil
}
