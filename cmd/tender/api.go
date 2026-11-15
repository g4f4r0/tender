package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/g4f4r0/tender/internal/client"
	"github.com/g4f4r0/tender/internal/server"
)

const defaultHub = "http://127.0.0.1:8080"

// session is what a command that talks to the hub needs from outside: where to write, how to ask
// for a secret with echo off, and the time. Tests replace all three.
type session struct {
	out    io.Writer
	prompt func(label string) (string, error)
	now    func() time.Time
}

func newSession() session {
	return session{out: os.Stdout, prompt: promptSecret, now: time.Now}
}

// say writes one line.
func say(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format+"\n", a...)
}

// envToken is the environment variable the tender command reads its API token from.
const envToken = "TENDER_TOKEN"

// hubFlag adds --hub and --token to fs. TENDER_HUB sets the default of --hub; newHubAPI reads
// TENDER_TOKEN when --token is not passed, so the token never shows in a usage text.
func hubFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("TENDER_HUB")
	if def == "" {
		def = defaultHub
	}
	fs.String("token", "", "`TOKEN` the API token (default: $"+envToken+")")
	return fs.String("hub", def, "`URL` the hub's address (default: $TENDER_HUB, else "+defaultHub+")")
}

// apiToken is the API token for a command: --token when passed, else TENDER_TOKEN.
func apiToken(fs *flag.FlagSet) string {
	if f := fs.Lookup("token"); f != nil && f.Value.String() != "" {
		return f.Value.String()
	}
	return os.Getenv(envToken)
}

// hubAPI is a connection to the hub for one command.
type hubAPI struct {
	c     *client.Client
	url   string
	token bool // whether the command has an API token to send
}

// newHubAPI checks the hub's address and returns a client for it, which sends the API token.
func newHubAPI(fs *flag.FlagSet, raw string) (*hubAPI, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, usageError(fs, "the hub address %q is not an http or https URL, such as %s", raw, defaultHub)
	}
	raw = strings.TrimRight(raw, "/")
	token := apiToken(fs)
	return &hubAPI{c: client.New(raw, token), url: raw, token: token != ""}, nil
}

// do sends one request. in and out may be nil.
func (h *hubAPI) do(method, path string, in, out any) error {
	return h.explain(h.c.Do(context.Background(), method, path, "", in, out))
}

// hubAnswer is the hub's answer to a failed request, worded for the command line.
type hubAnswer struct {
	status int
	msg    string
}

func (e *hubAnswer) Error() string { return e.msg }

// answered reports whether err is the hub answering status.
func answered(err error, status int) bool {
	var a *hubAnswer
	return errors.As(err, &a) && a.status == status
}

// explain words a failed request for the command line: the hub's own sentence, or what to check.
func (h *hubAPI) explain(err error) error {
	var answer *client.Error
	var network *url.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &answer) && answer.Status == http.StatusUnauthorized && !h.token:
		return &hubAnswer{answer.Status, "the hub at " + h.url + " needs an API token. Set " + envToken + " to one, or pass --token TOKEN. " +
			"Make the first one on the hub machine with: sudo tender tokens add NAME"}
	case errors.As(err, &answer) && answer.Status == http.StatusUnauthorized:
		return &hubAnswer{answer.Status, "the hub at " + h.url + " refused the API token in " + envToken + " or --token: it was deleted or mistyped. " +
			"List the tokens on the hub machine with: sudo tender tokens list, and make a new one with: sudo tender tokens add NAME"}
	case errors.As(err, &answer):
		if answer.Detail != "" {
			return &hubAnswer{answer.Status, answer.Detail}
		}
		return &hubAnswer{answer.Status, fmt.Sprintf("the hub at %s answered %d %s. Look at its log: journalctl -u tenderhub.", h.url, answer.Status, answer.Title)}
	case errors.As(err, &network):
		return fmt.Errorf("cannot reach the hub at %s. Check that the hub is running (systemctl status tenderhub) "+
			"and that --hub or TENDER_HUB has its address.", h.url)
	}
	return err
}

// listAll reads a collection, following next_cursor to the end.
func listAll[T any](h *hubAPI, path string) ([]T, error) {
	var all []T
	next := path
	for {
		var page server.List[T]
		if err := h.do(http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == "" {
			return all, nil
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		next = path + sep + "cursor=" + url.QueryEscape(page.NextCursor)
	}
}

// machinePath is "/v1/machines/NAME" plus rest.
func machinePath(name, rest string) string {
	return "/v1/machines/" + url.PathEscape(name) + rest
}

// runGroup runs "tender NAME COMMAND": it prints the group's usage for no command or help.
func runGroup(name, usageText string, args []string, out io.Writer, commands map[string]func([]string) error) error {
	if len(args) == 0 {
		_, _ = io.WriteString(out, usageText)
		return errUsageShown
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(out, usageText)
		return err
	}
	run, ok := commands[args[0]]
	if !ok {
		return fmt.Errorf("unknown %s command %q. Run \"tender %s --help\" for the commands.", name, args[0], name)
	}
	return run(args[1:])
}

// groupUsage is the usage of "tender NAME": a summary and its commands.
func groupUsage(name, summary string, rows []row) string {
	return "Usage: tender " + name + " COMMAND\n\n" + wrap(summary, lineWidth, "") + "\n\nCommands:\n" + table(rows) +
		"\nRun \"tender " + name + " COMMAND --help\" for a command's options.\n"
}

// oneArg returns the single argument a command takes, or a usage error that names what is missing.
func oneArg(fs *flag.FlagSet, args []string, what string) (string, error) {
	switch len(args) {
	case 1:
		return args[0], nil
	case 0:
		return "", usageError(fs, "%s needs %s", fs.Name(), what)
	default:
		return "", usageError(fs, "%s takes one argument, got %d: %s", fs.Name(), len(args), strings.Join(args, " "))
	}
}

// noArgs rejects arguments for a command that takes none.
func noArgs(fs *flag.FlagSet, args []string) error {
	if len(args) > 0 {
		return usageError(fs, "%s takes no arguments, got %q", fs.Name(), args[0])
	}
	return nil
}

// printTable writes rows as a plain table under an upper-case header, columns aligned.
func printTable(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// ago says how long before now t was: "3 hours ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, name)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return unit(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return unit(int(d/time.Hour), "hour")
	default:
		return unit(int(d/(24*time.Hour)), "day")
	}
}

// errNoTerminal means there is no terminal to ask on.
var errNoTerminal = errors.New("no terminal")

// promptSecret asks for a secret on the terminal with echo off. It uses /dev/tty, so it works
// when stdin is a pipe, as it is for "curl ... | sudo sh".
func promptSecret(label string) (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errNoTerminal
	}
	defer func() { _ = tty.Close() }()
	_, _ = fmt.Fprintf(tty, "%s (typing is hidden): ", label)
	b, err := term.ReadPassword(int(tty.Fd()))
	_, _ = fmt.Fprintln(tty)
	if err != nil {
		return "", fmt.Errorf("cannot read %s from the terminal: %w", strings.ToLower(label), err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// secret returns one secret: the first line of file when set, else what the person types with
// echo off. what names it in messages, option is the flag that takes a file.
func (s session) secret(what, file, option string) (string, error) {
	if file != "" {
		return readFileLine(file)
	}
	v, err := s.prompt(strings.ToUpper(what[:1]) + what[1:])
	if errors.Is(err, errNoTerminal) {
		return "", fmt.Errorf("there is no terminal to ask for the %s. Save it in a file and add --%s PATH.", what, option)
	}
	return v, err
}
