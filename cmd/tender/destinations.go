package main

import (
	"errors"
	"flag"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/backups/destinations"
)

var destinationsUsage = groupUsage("destinations", "Manage the places backups go: S3 buckets, a local folder, SFTP servers, restic's "+
	"REST server and rclone remotes, with the keys and the repository password Tender keeps for them.", []row{
	{"list", "List the destinations"},
	{"add NAME [--type TYPE] ...", "Add a destination; asks for its keys"},
	{"update NAME ...", "Change a destination's place or keys"},
	{"test NAME MACHINE", "Try a destination from a machine"},
	{"remove NAME", "Remove a destination no machine uses"},
})

// runDestinations runs "tender destinations list|add|update|test|remove".
func runDestinations(args []string, s session) error {
	return runGroup("destinations", destinationsUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return destinationsList(a, s) },
		"add":    func(a []string) error { return destinationsAdd(a, s) },
		"update": func(a []string) error { return destinationsUpdate(a, s) },
		"test":   func(a []string) error { return destinationsTest(a, s) },
		"remove": func(a []string) error { return destinationsRemove(a, s) },
	})
}

func destinationsList(args []string, s session) error {
	fs := newFlagSet("destinations list", "[--hub URL]", "List the destinations. Their keys and passwords are never shown.")
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
	list, err := listAll[backups.Destination](hub, "/v1/backups/destinations")
	if err != nil {
		return err
	}
	if len(list) == 0 {
		say(s.out, "No destinations yet. Add one with: tender destinations add NAME --type TYPE ... (run tender destinations add --help for the types)")
		return nil
	}
	rows := make([][]string, len(list))
	for i, d := range list {
		rows[i] = []string{d.Name, d.Type, d.Location}
	}
	printTable(s.out, []string{"NAME", "TYPE", "LOCATION"}, rows)
	return nil
}

// placeFlags are the flags that say where a destination is, by the config field each sets and
// the types that take it.
var placeFlags = []struct {
	flag, field, usage string
	types              []string
}{
	{"endpoint", "endpoint", "`URL` s3: the endpoint, such as https://ACCOUNT.r2.cloudflarestorage.com", []string{"s3"}},
	{"bucket", "bucket", "`BUCKET` s3: the bucket", []string{"s3"}},
	{"path", "path", "`PATH` s3: a folder in the bucket; local: the folder; sftp: the folder on the server", []string{"s3", "local", "sftp"}},
	{"host", "host", "`HOST` sftp: the server", []string{"sftp"}},
	{"port", "port", "`PORT` sftp: the SSH port (default 22)", []string{"sftp"}},
	{"user", "user", "`USER` sftp: the SSH user", []string{"sftp"}},
	{"url", "url", "`URL` rest: the rest-server, such as https://backup.example.com:8000", []string{"rest"}},
	{"username", "username", "`NAME` rest: the login, if the server has logins; asks for its password", []string{"rest"}},
	{"remote", "remote", "`REMOTE:PATH` rclone: the remote and folder, such as gdrive:backups", []string{"rclone"}},
}

// secretFlags are the flags that read a secret config field from a file, instead of asking.
var secretFlags = []struct {
	flag, field, what string
	types             []string
}{
	{"access-key-id-file", "access_key_id", "access key ID", []string{"s3"}},
	{"secret-access-key-file", "secret_access_key", "secret access key", []string{"s3"}},
	{"rest-password-file", "password", "REST server password", []string{"rest"}},
}

type destinationFlags struct {
	place   map[string]*string
	secrets map[string]*string
}

func addDestinationFlags(fs *flag.FlagSet, asks bool) destinationFlags {
	f := destinationFlags{place: map[string]*string{}, secrets: map[string]*string{}}
	for _, p := range placeFlags {
		f.place[p.flag] = fs.String(p.flag, "", p.usage)
	}
	for _, sf := range secretFlags {
		usage := "`PATH` " + strings.Join(sf.types, ", ") + ": file with the " + sf.what
		if asks {
			usage += ", instead of asking"
		}
		f.secrets[sf.flag] = fs.String(sf.flag, "", usage)
	}
	return f
}

// config turns the flags set on the command line into config fields for typ. A flag for another
// type is a usage error.
func (f destinationFlags) config(fs *flag.FlagSet, typ string) (map[string]any, error) {
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	config := map[string]any{}
	for _, p := range placeFlags {
		if !set[p.flag] {
			continue
		}
		if !slices.Contains(p.types, typ) {
			return nil, usageError(fs, "--%s is not for a %s destination", p.flag, typ)
		}
		v := *f.place[p.flag]
		if p.field == "port" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, usageError(fs, "--port must be a number, got %q", v)
			}
			config[p.field] = n
			continue
		}
		config[p.field] = v
	}
	for _, sf := range secretFlags {
		if set[sf.flag] && !slices.Contains(sf.types, typ) {
			return nil, usageError(fs, "--%s is not for a %s destination", sf.flag, typ)
		}
	}
	return config, nil
}

func destinationsAdd(args []string, s session) error {
	fs := newFlagSet("destinations add", "NAME [--type TYPE] [OPTIONS] [--password-file PATH] [--hub URL]",
		"Add a destination. Each type takes its own options.\n\n"+
			"s3 --endpoint URL --bucket BUCKET [--path PATH] is any S3-compatible bucket, such as Cloudflare R2, Backblaze B2, "+
			"Wasabi, MinIO or Google Cloud Storage. It asks for the keys.\n\n"+
			"local --path PATH is a folder on each machine, such as a mounted disk.\n\n"+
			"sftp --host HOST [--port PORT] --user USER --path PATH is a folder on an SSH server. Each machine needs its SSH key in "+
			backups.DefaultSSHDir+", as the backups guide shows.\n\n"+
			"rest --url URL [--username NAME] is restic's rest-server. With a login, it asks for the password.\n\n"+
			"rclone --remote REMOTE:PATH is an rclone remote, configured on each machine in "+backups.DefaultRcloneConfig+".\n\n"+
			"It asks for the repository password with typing hidden, or reads it from a file. Leave it empty and the hub makes one "+
			"and shows it once. Every machine's backups at this destination are encrypted with it, and it cannot change later. "+
			"Try it with: tender destinations test NAME MACHINE")
	hubURL := hubFlag(fs)
	typ := fs.String("type", "s3", "`TYPE` s3, local, sftp, rest or rclone")
	flags := addDestinationFlags(fs, true)
	passwordFile := fs.String("password-file", "", "`PATH` file with the repository password, instead of asking")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a destination name")
	if err != nil {
		return err
	}
	if _, ok := destinations.Get(*typ); !ok {
		return usageError(fs, "--type must be one of %s", strings.Join(destinations.Names(), ", "))
	}
	config, err := flags.config(fs, *typ)
	if err != nil {
		return err
	}
	if *typ == "s3" && (config["endpoint"] == nil || config["bucket"] == nil) {
		return usageError(fs, "destinations add needs --endpoint and --bucket")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}

	// Read every secret before the hub hears anything.
	for _, sf := range secretFlags {
		if !slices.Contains(sf.types, *typ) || (sf.field == "password" && config["username"] == nil) {
			continue
		}
		v, err := s.secret(sf.what, *flags.secrets[sf.flag], sf.flag)
		if err != nil {
			return err
		}
		if v == "" {
			if *typ == "s3" {
				return usageError(fs, "the access key ID and the secret access key cannot be empty")
			}
			return usageError(fs, "the %s cannot be empty", sf.what)
		}
		config[sf.field] = v
	}
	in := backups.DestinationInput{Name: name, Type: *typ, Config: config}
	if *passwordFile != "" {
		in.Password, err = readFileLine(*passwordFile)
	} else {
		in.Password, err = s.passwordOrBlank()
	}
	if err != nil {
		return err
	}

	var created backups.DestinationCreated
	if err := hub.do(http.MethodPost, "/v1/backups/destinations", in, &created); err != nil {
		return err
	}
	say(s.out, "Added destination %s.", created.Name)
	if created.Password != "" {
		say(s.out, "\nThe repository password, made by the hub and shown only now:")
		say(s.out, "  %s\n", created.Password)
		say(s.out, "%s", wrap("Save it in a password manager now. Every machine's backups at this destination are encrypted "+
			"with it, and without it they cannot be restored. Tender cannot show it again.", lineWidth, ""))
	}
	return nil
}

// passwordOrBlank asks for the repository password. An empty answer means the hub makes one.
// Without a terminal it asks for nothing: the hub makes one.
func (s session) passwordOrBlank() (string, error) {
	v, err := s.prompt("Repository password, or empty for the hub to make one")
	if errors.Is(err, errNoTerminal) {
		return "", nil
	}
	return v, err
}

func destinationsUpdate(args []string, s session) error {
	fs := newFlagSet("destinations update", "NAME [OPTIONS] [--hub URL]",
		"Change where a destination is, or its keys. Only the options you give change; the others stay. New keys are read "+
			"from the files you name. The type and the repository password can't change. The options are those of "+
			"\"tender destinations add\".")
	hubURL := hubFlag(fs)
	flags := addDestinationFlags(fs, false)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a destination name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var d backups.Destination
	if err := hub.do(http.MethodGet, "/v1/backups/destinations/"+url.PathEscape(name), nil, &d); err != nil {
		return err
	}
	config, err := flags.config(fs, d.Type)
	if err != nil {
		return err
	}
	for _, sf := range secretFlags {
		if file := *flags.secrets[sf.flag]; file != "" {
			v, err := readFileLine(file)
			if err != nil {
				return err
			}
			config[sf.field] = v
		}
	}
	if len(config) == 0 {
		return usageError(fs, "destinations update needs an option to change")
	}
	if err := hub.do(http.MethodPatch, "/v1/backups/destinations/"+url.PathEscape(name), backups.DestinationPatch{Config: config}, &d); err != nil {
		return err
	}
	say(s.out, "Updated destination %s: %s.", d.Name, d.Location)
	return nil
}

func destinationsTest(args []string, s session) error {
	fs := newFlagSet("destinations test", "NAME MACHINE [--hub URL]",
		"Try a destination from a machine: its daemon opens that machine's repository there, which checks the address, the "+
			"login and the password, or makes the repository when there is none yet. This waits up to 60 seconds for it.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return usageError(fs, "destinations test needs a destination name and a machine name")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var a actions.Action
	if err := hub.do(http.MethodPost, "/v1/backups/destinations/"+url.PathEscape(rest[0])+"/test", actions.TestInput{Machine: rest[1]}, &a); err != nil {
		return err
	}
	a, err = awaitAction(hub, rest[1], a)
	if err != nil {
		return err
	}
	if a.Output != nil {
		_, err = s.out.Write([]byte(*a.Output))
	}
	return err
}

func destinationsRemove(args []string, s session) error {
	fs := newFlagSet("destinations remove", "NAME [--hub URL]",
		"Remove a destination. The hub refuses while a machine backs up to it. The backups already in it stay there.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a destination name")
	if err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, "/v1/backups/destinations/"+url.PathEscape(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed destination %s. The backups already in it stay there.", name)
	return nil
}
