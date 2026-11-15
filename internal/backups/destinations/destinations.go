// Package destinations holds the kinds of place backups go: S3-compatible buckets, a local
// folder, SFTP, restic's REST server and rclone. Each turns a destination's config into a restic
// repository and the environment of the restic child; nothing else in backups depends on the kind.
// Adding one is one file here and its tests.
package destinations

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/g4f4r0/tender/internal/server"
)

// Config is a destination's settings, as JSON decodes them. Secret fields hold plain text here;
// the store seals them before they reach the database.
type Config map[string]any

// Type is one kind of destination.
type Type interface {
	// Fields lists the config fields the type takes, for error messages and docs.
	Fields() []string
	// Secrets lists the config fields that are secret: stored encrypted, never shown.
	Secrets() []string
	// Check validates a whole config and returns it cleaned up, with defaults filled in. Its
	// errors are server.Invalid, worded for the person who sent it.
	Check(c Config) (Config, error)
	// Location is the restic repository the destination names, without a machine's folder. Two
	// destinations may not share one.
	Location(c Config) string
	// Open returns how restic reaches machine's repository at the destination, from the daemon's
	// machine.
	Open(c Config, machine string, h Host) (Repo, error)
}

// Host is what the daemon's machine offers a destination.
type Host struct {
	SSHDir       string                       // holds id_ed25519 and known_hosts, for sftp
	RcloneConfig string                       // rclone's config file, for rclone
	LookPath     func(string) (string, error) // finds a program; exec.LookPath when nil
}

func (h Host) lookPath(name string) (string, error) {
	if h.LookPath != nil {
		return h.LookPath(name)
	}
	return exec.LookPath(name)
}

// Repo is how restic reaches one machine's repository.
type Repo struct {
	Repository string   // RESTIC_REPOSITORY
	Env        []string // KEY=VALUE for the restic child only, such as keys
	Options    []string // restic -o options, such as sftp.args; never secret
}

var types = map[string]Type{
	"s3":     s3{},
	"local":  local{},
	"sftp":   sftp{},
	"rest":   rest{},
	"rclone": rclone{},
}

// Get returns the type called name.
func Get(name string) (Type, bool) {
	t, ok := types[name]
	return t, ok
}

// Names lists the types, sorted.
func Names() []string {
	out := make([]string, 0, len(types))
	for n := range types {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// IsSecret reports whether field is one of t's secrets.
func IsSecret(t Type, field string) bool { return slices.Contains(t.Secrets(), field) }

// decode reads c into v, a struct with json tags. A field the type doesn't take is a 422 naming
// the ones it does.
func decode(typ string, t Type, c Config, v any) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var wrongType *json.UnmarshalTypeError
		if errors.As(err, &wrongType) {
			return server.Invalid("config.%s must be %s.", wrongType.Field, wrongType.Type)
		}
		field := strings.TrimPrefix(err.Error(), "json: unknown field ")
		return server.Invalid("config has %s, which a %s destination doesn't take. It takes %s.", field, typ, strings.Join(t.Fields(), ", "))
	}
	return nil
}

// encode turns a typed config back into a Config, leaving out empty optional fields.
func encode(v any) Config {
	b, _ := json.Marshal(v)
	var c Config
	_ = json.Unmarshal(b, &c)
	return c
}

// trimPath removes the slashes around a path inside a bucket or remote.
func trimPath(p string) string { return strings.Trim(p, "/") }

// pathPart is one folder name in a destination's path. Nothing else is allowed: a ?, # or %
// escape would make restic open another repository than the one checked, and a space or a
// quote could split an argument.
var pathPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// cleanPath is a 422 unless p is a plain path of folder names of letters, digits, ., _ and -,
// with no empty, . or .. part and none starting with -. It may start or end with a slash, and
// may be empty; the caller says whether empty is allowed.
func cleanPath(field, p string) error {
	inner := strings.Trim(p, "/")
	if inner == "" {
		return nil
	}
	for _, part := range strings.Split(inner, "/") {
		if part == "." || part == ".." || !pathPart.MatchString(part) || strings.HasPrefix(part, "-") {
			return server.Invalid("config.%s must be a plain path of folder names made of letters, digits, ., _ and -, without . or .. parts, double slashes or a leading -.", field)
		}
	}
	return nil
}

func required(field, what string) error {
	return server.Invalid("config.%s is required: %s.", field, what)
}
