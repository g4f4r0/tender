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

	"github.com/g4f4r0/tender/internal/install"
)

var installUsage = `Usage: tender install COMMAND

Set up this machine. Run as root, for example with sudo.

Commands:
` + table([]row{
	{"agent --hub URL --machine NAME", "Install and start the agent on this machine"},
	{"hub", "Install and start the hub on this machine"},
	{"refresh", "Restart the installed services on this binary"},
}) + `
Run "tender install COMMAND --help" for a command's options. The hub token comes from stdin
(one line) or a token file, never from an argument. The SMTP password is asked for with typing
hidden, or comes from a file.
`

// runInstall runs "tender install agent|hub|refresh". It needs root.
func runInstall(args []string, stdin io.Reader, s session) error {
	out := s.out
	if len(args) == 0 {
		_, _ = io.WriteString(out, installUsage)
		return errUsageShown
	}
	var agent install.AgentOptions
	var hub install.HubOptions
	var tokenFile, smtpPasswordFile string
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
		fs = newFlagSet("install hub", "[--smtp-host HOST --smtp-from SENDER]\n[--smtp-port PORT --smtp-username USER]\n[--smtp-password-file PATH]",
			"Install and start the hub on this machine, then publish it with Tailscale Serve. Run as root.\n\n"+
				"The hub keeps its settings in /etc/tender/hub.env, with the key that encrypts the secrets it stores. "+
				"The key is made once and stays on every later run. Back that file up.\n\n"+
				"Alert emails are optional. Pass --smtp-host and --smtp-from to turn them on; "+
				"with --smtp-username it asks for the SMTP password with typing hidden. Run the command again to change them.")
		fs.StringVar(&hub.SMTPHost, "smtp-host", "", "`HOST` the SMTP server")
		fs.StringVar(&hub.SMTPFrom, "smtp-from", "", "`SENDER` the sender, such as \"Tender <alerts@example.net>\"")
		fs.IntVar(&hub.SMTPPort, "smtp-port", 0, "`PORT` the SMTP port, implicit TLS (default: 465)")
		fs.StringVar(&hub.SMTPUsername, "smtp-username", "", "`USER` the SMTP login name, for servers that ask for one")
		fs.StringVar(&smtpPasswordFile, "smtp-password-file", "", "`PATH` file with the SMTP password (default: ask)")
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
	if fs.NArg() > 0 {
		return usageError(fs, "install %s takes no arguments, got %q", args[0], fs.Arg(0))
	}

	switch args[0] {
	case "agent":
		if (agent.Hub == "") != (agent.Machine == "") {
			return usageError(fs, "install agent needs both --hub and --machine, or neither on a machine that already has an agent")
		}
	case "hub":
		if hub.SMTPPort < 0 || hub.SMTPPort > 65535 {
			return usageError(fs, "--smtp-port must be a port number, 1 to 65535")
		}
		if smtpPasswordFile != "" && hub.SMTPUsername == "" {
			return usageError(fs, "--smtp-password-file needs --smtp-username")
		}
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("install %s needs root. Run it again with sudo.", args[0])
	}

	// Read the secret before anything changes on the machine.
	switch {
	case args[0] == "hub" && hub.SMTPUsername != "":
		var err error
		if hub.SMTPPassword, err = s.secret("SMTP password for "+hub.SMTPUsername, smtpPasswordFile, "smtp-password-file"); err != nil {
			return err
		}
		if hub.SMTPPassword == "" {
			return errors.New("the SMTP password is empty. Run the command again and type it, or pass --smtp-password-file PATH.")
		}
	case args[0] != "agent":
	case tokenFile != "":
		var err error
		if agent.Token, err = readFileLine(tokenFile); err != nil {
			return err
		}
	case isTerminal(stdin):
		// Nobody piped a token in; an installed agent keeps its own, a new one gets a clear error.
	default:
		var err error
		if agent.Token, err = readLine(stdin); err != nil {
			return fmt.Errorf("cannot read the token from stdin. Pipe it in, for example: tender install agent < FILE: %w", err)
		}
	}

	in, err := install.New(out)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "agent":
		return in.Agent(ctx, agent)
	case "hub":
		return in.Hub(ctx, hub)
	default:
		return in.Refresh(ctx)
	}
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
		return "", fmt.Errorf("cannot read %s. Check the path and that you can read it: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return readLine(f)
}
