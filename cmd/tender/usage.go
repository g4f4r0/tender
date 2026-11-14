package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

const (
	docsURL   = "https://github.com/g4f4r0/tender"
	lineWidth = 98 // usage text wraps before this column
)

// errUsageShown means the usage went to the output already: exit 2 without an error line.
var errUsageShown = errors.New("usage shown")

// newFlagSet returns the FlagSet of "tender NAME". args is the synopsis after the name; a newline in
// it starts a continuation line. Its Usage prints:
//
//	Usage: tender NAME ARGS
//
//	SUMMARY (wrapped; a blank line starts a paragraph)
//
//	Options:
//	  --name VALUE   what it does (default: X)
//
// A flag's help text may start with a backquoted placeholder, "`URL` the hub's address", which
// names its VALUE. A default shows in parentheses, unless the text has its own.
func newFlagSet(name, args, summary string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // parseFlags words its own errors and shows the usage on -h
	fs.Usage = func() {
		w := fs.Output()
		lines := strings.Split(strings.TrimSpace("tender "+name+" "+args), "\n")
		for i := 1; i < len(lines); i++ {
			lines[i] = strings.TrimSpace(lines[i])
		}
		hang := strings.Repeat(" ", len("Usage: tender "+name+" "))
		_, _ = fmt.Fprintf(w, "Usage: %s\n", strings.Join(lines, "\n"+hang))
		_, _ = fmt.Fprintf(w, "\n%s\n", wrap(summary, lineWidth, ""))
		if options := optionLines(fs); options != "" {
			_, _ = fmt.Fprintf(w, "\nOptions:\n%s", options)
		}
	}
	return fs
}

// optionLines lists the flags as "  --name VALUE   help", aligned.
func optionLines(fs *flag.FlagSet) string {
	var left, right []string
	width := 0
	fs.VisitAll(func(f *flag.Flag) {
		placeholder, text := splitPlaceholder(f)
		l := "--" + f.Name
		if placeholder != "" {
			l += " " + placeholder
		}
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && !strings.Contains(text, "(default") {
			text += " (default: " + f.DefValue + ")"
		}
		left, right = append(left, l), append(right, text)
		width = max(width, len(l))
	})
	var b strings.Builder
	for i := range left {
		indent := strings.Repeat(" ", 2+width+3)
		text := wrap(right[i], lineWidth, indent)
		_, _ = fmt.Fprintf(&b, "  %-*s   %s\n", width, left[i], strings.TrimPrefix(text, indent))
	}
	return b.String()
}

// splitPlaceholder returns the VALUE name of a flag and its help text.
func splitPlaceholder(f *flag.Flag) (placeholder, text string) {
	if rest, ok := strings.CutPrefix(f.Usage, "`"); ok {
		if name, text, ok := strings.Cut(rest, "` "); ok {
			return name, text
		}
	}
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return "", f.Usage
	}
	return "VALUE", f.Usage
}

// wrap breaks text into lines of at most width characters. A blank line in text starts a new
// paragraph. Every line gets indent, so a caller can hang the text under a label.
func wrap(text string, width int, indent string) string {
	var lines []string
	for i, paragraph := range strings.Split(text, "\n\n") {
		if i > 0 {
			lines = append(lines, "")
		}
		line := ""
		for _, word := range strings.Fields(paragraph) {
			if line != "" && len(indent)+len(line)+1+len(word) > width {
				lines = append(lines, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		lines = append(lines, line)
	}
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}

// row is one line of a usage table: what to type, and what it does.
type row struct{ command, does string }

// table lays rows out as "  command   does" with the descriptions in one column.
func table(rows []row) string {
	width := 0
	for _, r := range rows {
		width = max(width, len(r.command))
	}
	var b strings.Builder
	for _, r := range rows {
		_, _ = fmt.Fprintf(&b, "  %-*s   %s\n", width, r.command, r.does)
	}
	return b.String()
}

// parseFlags parses args. On -h or --help it prints the usage to out and returns flag.ErrHelp,
// which means success. Any other failure comes back as a one-line error that says what to do.
func parseFlags(fs *flag.FlagSet, args []string, out io.Writer) error {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fs.SetOutput(out)
		fs.Usage()
		return flag.ErrHelp
	}
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: -"):
		msg = "unknown option --" + strings.TrimPrefix(msg, "flag provided but not defined: -")
	case strings.HasPrefix(msg, "flag needs an argument: -"):
		msg = "--" + strings.TrimPrefix(msg, "flag needs an argument: -") + " needs a value"
	default:
		msg = strings.Replace(msg, " for flag -", " for --", 1)
	}
	return usageError(fs, "%s", msg)
}

// usageError is an error about how a command was used. It says where the usage is.
func usageError(fs *flag.FlagSet, format string, a ...any) error {
	return fmt.Errorf("%s. Run %q for usage.", fmt.Sprintf(format, a...), helpCommand(fs.Name()))
}

// helpCommand is the command that prints the usage of "tender name".
func helpCommand(name string) string {
	if strings.HasPrefix(name, "hub ") {
		return "tender hub help"
	}
	return "tender " + name + " --help"
}
