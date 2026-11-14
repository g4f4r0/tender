package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/install"
)

var installUsage = `Usage: tender install COMMAND

Set up this machine. Run as root, for example with sudo.

Commands:
` + table([]row{
	{"agent --hub URL --machine NAME", "Install and start the agent on this machine"},
	{"hub", "Install and start the hub on this machine"},
	{"credential NAME", "Save a destination's key or password from stdin"},
	{"refresh", "Restart the installed services on this binary"},
}) + `
Run "tender install COMMAND --help" for a command's options. The hub token and the credentials
come from stdin (one line) or a token file, never from arguments.
`

// runInstall runs "tender install agent|hub|credential|refresh". It needs root.
func runInstall(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		_, _ = io.WriteString(out, installUsage)
		return errUsageShown
	}
	var agent install.AgentOptions
	var hub install.HubOptions
	var tokenFile, emailTo, emailFrom, smtpHost, smtpUsername string
	var smtpPort int
	var fs *flag.FlagSet
	switch args[0] {
	case "help", "-h", "--help":
		_, err := io.WriteString(out, installUsage)
		return err
	case "agent":
		fs = newFlagSet("install agent", "--hub URL --machine NAME [--token-file PATH] [--force]",
			"Install and start the agent on this machine. Run as root.\n\n"+
				"The hub token comes from --token-file or from one line on stdin, never from an argument. "+
				"On a machine that already has an agent, leave out --hub and --machine: the config and the token stay, "+
				"and the command repairs the install.")
		fs.StringVar(&agent.Hub, "hub", "", "`URL` the hub's address, such as https://hub.example.ts.net")
		fs.StringVar(&agent.Machine, "machine", "", "`NAME` this machine's name on the hub")
		fs.StringVar(&tokenFile, "token-file", "", "`PATH` file with the hub token (default: one line on stdin)")
		fs.BoolVar(&agent.Force, "force", false, "replace an existing /etc/tender/agent.toml")
	case "hub":
		fs = newFlagSet("install hub", "[--force]\n[--email-to ADDRESSES --email-from SENDER --smtp-host HOST]\n[--smtp-port PORT --smtp-username USER]",
			"Install and start the hub on this machine, then publish it with Tailscale Serve. Run as root.\n\n"+
				"Alert emails are optional: pass all five --email and --smtp options, or none. "+
				"Then save the SMTP password with: sudo tender install credential smtp-password < FILE")
		fs.BoolVar(&hub.Force, "force", false, "replace an existing /etc/tender/hub.toml")
		fs.StringVar(&emailTo, "email-to", "", "`ADDRESSES` comma-separated addresses that get alerts")
		fs.StringVar(&emailFrom, "email-from", "", "`SENDER` the sender, such as \"Tender <alerts@example.net>\"")
		fs.StringVar(&smtpHost, "smtp-host", "", "`HOST` the SMTP server")
		fs.IntVar(&smtpPort, "smtp-port", 0, "`PORT` the SMTP port, implicit TLS (usually 465)")
		fs.StringVar(&smtpUsername, "smtp-username", "", "`USER` the SMTP user name")
	case "credential":
		fs = newFlagSet("install credential", "NAME",
			"Save a credential on this machine, read from one line on stdin. Run as root.\n\n"+
				"A destination needs three, named after it: r2.password (the repository password), "+
				"r2.access-key-id and r2.secret-access-key. Alerts by email need smtp-password.\n\n"+
				"Example: sudo tender install credential r2.password < password.txt")
	case "refresh":
		fs = newFlagSet("install refresh", "",
			"Check the pinned restic, rewrite the service units and restart the installed services. "+
				"The configs and the credentials stay. Run as root. \"tender update\" runs this for you.")
	default:
		return fmt.Errorf("unknown install command %q. Run \"tender install --help\" for the commands.", args[0])
	}
	if err := parseFlags(fs, args[1:], out); err != nil {
		return err
	}

	switch args[0] {
	case "agent":
		if fs.NArg() > 0 {
			return usageError(fs, "install agent takes no arguments, got %q", fs.Arg(0))
		}
		if (agent.Hub == "") != (agent.Machine == "") {
			return usageError(fs, "install agent needs both --hub and --machine, or neither on a machine that already has an agent")
		}
	case "hub":
		if fs.NArg() > 0 {
			return usageError(fs, "install hub takes no arguments, got %q", fs.Arg(0))
		}
		var err error
		if hub.Email, err = emailFlags(emailTo, emailFrom, smtpHost, smtpUsername, smtpPort); err != nil {
			return usageError(fs, "%v", err)
		}
	case "refresh":
		if fs.NArg() > 0 {
			return usageError(fs, "install refresh takes no arguments, got %q", fs.Arg(0))
		}
	case "credential":
		if fs.NArg() != 1 {
			return usageError(fs, "install credential needs one credential name, such as r2.password")
		}
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("install %s needs root. Run it again with sudo.", args[0])
	}

	// Read the secret before anything changes on the machine.
	var secret string
	switch {
	case args[0] != "agent" && args[0] != "credential":
	case tokenFile != "":
		var err error
		if secret, err = readFileLine(tokenFile); err != nil {
			return err
		}
	case args[0] == "agent" && isTerminal(stdin):
		// Nobody piped a token in; an installed agent keeps its own, a new one gets a clear error.
	default:
		var err error
		if secret, err = readLine(stdin); err != nil {
			return fmt.Errorf("cannot read the value from stdin. Pipe it in, for example: tender install %s < FILE: %w", args[0], err)
		}
	}

	in, err := install.New(out)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "agent":
		agent.Token = secret
		return in.Agent(ctx, agent)
	case "hub":
		return in.Hub(ctx, hub)
	case "refresh":
		return in.Refresh(ctx)
	default:
		return in.Credential(ctx, fs.Arg(0), secret)
	}
}

// emailFlags returns the [email] settings, nil when no flag is set. The five come together.
func emailFlags(to, from, host, username string, port int) (*config.Email, error) {
	if to == "" && from == "" && host == "" && username == "" && port == 0 {
		return nil, nil
	}
	if to == "" || from == "" || host == "" || username == "" || port == 0 {
		return nil, errors.New("alert emails need --email-to, --email-from, --smtp-host, --smtp-port and --smtp-username together. Pass all five, or none")
	}
	e := &config.Email{Host: host, Port: port, Username: username, From: from}
	for _, a := range strings.Split(to, ",") {
		e.To = append(e.To, strings.TrimSpace(a))
	}
	return e, nil
}

// isTerminal reports whether r is a terminal or /dev/null, where no one is piping a secret in.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// readLine reads one line, without its line ending. An empty stdin gives an empty string.
func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readFileLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the token file %s. Check the path and that you can read it: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return readLine(f)
}
