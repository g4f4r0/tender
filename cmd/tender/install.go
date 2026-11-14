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

const installUsage = `Usage:
  tender install agent --hub URL --machine NAME [--token-file PATH] [--force]   install and start the agent
  tender install hub [--force] [--email-to A,B --email-from ADDR --smtp-host HOST --smtp-port N --smtp-username USER]
  tender install credential NAME                 save a credential from stdin, such as r2-apps.restic-password

Run as root. The hub token and credentials are read from stdin (one line) or the token file, never from arguments.
Without --force, an existing config file is kept.
`

// runInstall runs "tender install agent|hub|credential". It needs root.
func runInstall(args []string, stdin io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(strings.TrimSpace(installUsage))
	}
	fs := flag.NewFlagSet("install "+args[0], flag.ContinueOnError)
	var agent install.AgentOptions
	var hub install.HubOptions
	var tokenFile, emailTo, emailFrom, smtpHost, smtpUsername string
	var smtpPort int
	switch args[0] {
	case "agent":
		fs.StringVar(&agent.Hub, "hub", "", "the hub's URL, such as https://apps.example.ts.net")
		fs.StringVar(&agent.Machine, "machine", "", "this machine's name on the hub")
		fs.StringVar(&tokenFile, "token-file", "", "file with the hub token (default: one line on stdin)")
		fs.BoolVar(&agent.Force, "force", false, "replace an existing agent.toml")
	case "hub":
		fs.BoolVar(&hub.Force, "force", false, "replace an existing hub.toml")
		fs.StringVar(&emailTo, "email-to", "", "comma-separated addresses that get alerts")
		fs.StringVar(&emailFrom, "email-from", "", `sender, such as "tender <noreply@example.net>"`)
		fs.StringVar(&smtpHost, "smtp-host", "", "SMTP server")
		fs.IntVar(&smtpPort, "smtp-port", 0, "SMTP port, implicit TLS")
		fs.StringVar(&smtpUsername, "smtp-username", "", "SMTP user name")
	case "credential":
	default:
		return fmt.Errorf("unknown install command %q\n%s", args[0], strings.TrimSpace(installUsage))
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch args[0] {
	case "agent":
		if fs.NArg() > 0 || agent.Hub == "" || agent.Machine == "" {
			return errors.New("usage: tender install agent --hub URL --machine NAME [--token-file PATH] [--force]")
		}
	case "hub":
		if fs.NArg() > 0 {
			return errors.New("tender install hub takes no arguments")
		}
		var err error
		if hub.Email, err = emailFlags(emailTo, emailFrom, smtpHost, smtpUsername, smtpPort); err != nil {
			return err
		}
	case "credential":
		if fs.NArg() != 1 {
			return errors.New("usage: tender install credential NAME   (the value comes from stdin)")
		}
	}
	if os.Geteuid() != 0 {
		return errors.New("tender install must run as root: use sudo")
	}

	// Read the secret before anything changes on the machine.
	var secret string
	if args[0] != "hub" {
		var err error
		if tokenFile != "" {
			secret, err = readFileLine(tokenFile)
		} else {
			secret, err = readLine(stdin)
		}
		if err != nil {
			return err
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
		return nil, errors.New("set all of --email-to, --email-from, --smtp-host, --smtp-port and --smtp-username, or none")
	}
	e := &config.Email{Host: host, Port: port, Username: username, From: from}
	for _, a := range strings.Split(to, ",") {
		e.To = append(e.To, strings.TrimSpace(a))
	}
	return e, nil
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
		return "", err
	}
	defer func() { _ = f.Close() }()
	return readLine(f)
}
