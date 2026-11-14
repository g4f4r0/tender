package main

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/g4f4r0/tender/internal/backups"
)

var destinationsUsage = groupUsage("destinations", "Manage the places backups go: S3 buckets, with the keys and the repository password Tender keeps for them.", []row{
	{"list", "List the destinations"},
	{"add NAME --endpoint URL --bucket B", "Add a destination; asks for its keys"},
	{"remove NAME", "Remove a destination no machine uses"},
})

// runDestinations runs "tender destinations list|add|remove".
func runDestinations(args []string, s session) error {
	return runGroup("destinations", destinationsUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return destinationsList(a, s) },
		"add":    func(a []string) error { return destinationsAdd(a, s) },
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
		say(s.out, "No destinations yet. Add one with: tender destinations add NAME --endpoint URL --bucket BUCKET")
		return nil
	}
	rows := make([][]string, len(list))
	for i, d := range list {
		rows[i] = []string{d.Name, d.Type, d.Endpoint, d.Bucket, orDash(d.Path)}
	}
	printTable(s.out, []string{"NAME", "TYPE", "ENDPOINT", "BUCKET", "PATH"}, rows)
	return nil
}

func destinationsAdd(args []string, s session) error {
	fs := newFlagSet("destinations add", "NAME --endpoint URL --bucket BUCKET [--path PATH]\n[--hub URL]",
		"Add a destination. It asks for the access key ID, the secret access key and the repository password with "+
			"typing hidden, or reads each from a file. Leave the password empty and the hub makes one and shows it once.\n\n"+
			"Every machine's backups at this destination are encrypted with its password, which cannot change later.")
	hubURL := hubFlag(fs)
	endpoint := fs.String("endpoint", "", "`URL` the S3 endpoint, such as https://ACCOUNT.r2.cloudflarestorage.com")
	bucket := fs.String("bucket", "", "`BUCKET` the bucket name")
	path := fs.String("path", "", "`PATH` a folder inside the bucket (default: the bucket's root)")
	typ := fs.String("type", "s3", "`TYPE` the kind of destination; only s3 for now")
	keyFile := fs.String("access-key-id-file", "", "`PATH` file with the access key ID, instead of asking")
	secretFile := fs.String("secret-access-key-file", "", "`PATH` file with the secret access key, instead of asking")
	passwordFile := fs.String("password-file", "", "`PATH` file with the repository password, instead of asking")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a destination name")
	if err != nil {
		return err
	}
	if *endpoint == "" || *bucket == "" {
		return usageError(fs, "destinations add needs --endpoint and --bucket")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}

	// Read every secret before the hub hears anything.
	in := backups.DestinationInput{Name: name, Type: *typ, Endpoint: *endpoint, Bucket: *bucket, Path: *path}
	if in.AccessKeyID, err = s.secret("access key ID", *keyFile, "access-key-id-file"); err != nil {
		return err
	}
	if in.SecretAccessKey, err = s.secret("secret access key", *secretFile, "secret-access-key-file"); err != nil {
		return err
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" {
		return usageError(fs, "the access key ID and the secret access key cannot be empty")
	}
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

func destinationsRemove(args []string, s session) error {
	fs := newFlagSet("destinations remove", "NAME [--hub URL]",
		"Remove a destination. The hub refuses while a machine backs up to it. The backups already in the bucket stay there.")
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
	say(s.out, "Removed destination %s. The backups already in its bucket stay there.", name)
	return nil
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
