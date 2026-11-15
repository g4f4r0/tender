package deploy

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/g4f4r0/tender/internal/server"
)

// The limits of a managed service.
const (
	MaxComposeBytes = 256 << 10
	MaxFiles        = 64
	MaxFilesBytes   = 1 << 20
	MaxNameLength   = 32
	maxPathBytes    = 255
	maxEnvKeys      = 256
	maxEnvBytes     = 64 << 10
	// MaxRoutes is how many routes a service may have.
	MaxRoutes = 64
	// MaxBodyBytes is the most a service's request body may be: the compose file and the files,
	// with room for JSON's escapes.
	MaxBodyBytes = 4 << 20
)

var (
	namePattern      = regexp.MustCompile(`^[a-z][a-z0-9]{0,31}$`)
	envKeyPattern    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	containerPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	segmentPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hostLabel        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// The files Tender writes itself in a service's folder, which its files can't be.
var reserved = []string{"compose.yaml", "compose.tender.yaml", ".env", manifestName}

// manifestName lists the files Tender wrote at the last deploy, so the next one removes those that
// are gone.
const manifestName = ".tender-files.json"

// ValidName reports whether s can be a managed service's name: the compose project name, lowercase
// letters and digits, starting with a letter, at most 32.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// ValidContainer reports whether s can be a service in a compose file, which a route names.
func ValidContainer(s string) bool { return containerPattern.MatchString(s) }

// CheckName is an error unless name can be a managed service's name.
func CheckName(name string) error {
	if !ValidName(name) {
		return server.Invalid("name must be lowercase letters and digits, starting with a letter, at most %d, with no dashes: "+
			"it is the compose project name. Got %q.", MaxNameLength, name)
	}
	return nil
}

// CheckCompose is an error unless compose can be a compose file's text.
func CheckCompose(compose string) error {
	switch {
	case strings.TrimSpace(compose) == "":
		return server.Invalid("compose is required: the text of the docker compose file.")
	case len(compose) > MaxComposeBytes:
		return server.Invalid("compose is %d bytes; it may be at most 256 KiB.", len(compose))
	case !utf8.ValidString(compose) || strings.ContainsRune(compose, 0):
		return server.Invalid("compose must be text.")
	}
	return nil
}

// CheckPath is an error unless p is fit to be one of a service's files: a relative path inside the
// service's folder, without . or .. parts, that isn't one of the files Tender writes or in volumes/.
func CheckPath(p string) error {
	bad := func(why string) error {
		return server.Invalid("files: %q %s. A file's path is relative to the compose file, such as config/nginx.conf.", p, why)
	}
	switch {
	case p == "" || len(p) > maxPathBytes:
		return bad("must be 1 to 255 bytes")
	case strings.HasPrefix(p, "/"):
		return bad("is absolute")
	case path.Clean(p) != p:
		return bad("must be a clean path, without ./, ../, // or a trailing /")
	case slices.Contains(reserved, p):
		return bad("is a file Tender writes itself")
	}
	for i, seg := range strings.Split(p, "/") {
		switch {
		case seg == "." || seg == "..":
			return bad("can't go outside the service's folder")
		case !segmentPattern.MatchString(seg):
			return bad("may only hold letters, digits, dots, dashes and underscores between slashes")
		case i == 0 && seg == "volumes":
			return bad("is in volumes/, where Tender keeps the named volumes' data")
		}
	}
	return nil
}

// CheckFiles is an error unless files can be a service's files.
func CheckFiles(files map[string]string) error {
	if len(files) > MaxFiles {
		return server.Invalid("files has %d files; a service may have at most %d.", len(files), MaxFiles)
	}
	total := 0
	for _, p := range SortedKeys(files) {
		if err := CheckPath(p); err != nil {
			return err
		}
		// A file can't also be a folder of another one.
		for q := range files {
			if strings.HasPrefix(q, p+"/") {
				return server.Invalid("files: %q is a file, so %q can't be inside it.", p, q)
			}
		}
		if !utf8.ValidString(files[p]) || strings.ContainsRune(files[p], 0) {
			return server.Invalid("files: %q must be text.", p)
		}
		total += len(files[p])
	}
	if total > MaxFilesBytes {
		return server.Invalid("files hold %d bytes in all; a service's files may hold at most 1 MiB.", total)
	}
	return nil
}

// CheckEnv checks the env's keys and values. Values go to .env single-quoted, so $ stays literal,
// which is why they can't hold a single quote or a line break.
func CheckEnv(env map[string]string) error {
	if len(env) > maxEnvKeys {
		return server.Invalid("env has %d keys; a service may have at most %d.", len(env), maxEnvKeys)
	}
	total := 0
	for _, k := range SortedKeys(env) {
		v := env[k]
		switch {
		case !envKeyPattern.MatchString(k):
			return server.Invalid("env: %q is not a valid key: use capital letters, digits and underscores, not starting with a digit.", k)
		case v == Hidden:
			return server.Invalid("env: %s is \"***\", which is how the hub hides a value. Give the value itself.", k)
		case strings.ContainsAny(v, "'\n\r\x00"):
			return server.Invalid("env: %s holds a single quote or a line break, which .env can't keep: Tender writes each value "+
				"single-quoted, so a $ in it stays as it is.", k)
		case !utf8.ValidString(v):
			return server.Invalid("env: %s must be text.", k)
		}
		total += len(k) + len(v)
	}
	if total > maxEnvBytes {
		return server.Invalid("env holds %d bytes in all; at most 64 KiB.", total)
	}
	return nil
}

// CheckEnvKeys is an error when an env of n keys is too many.
func CheckEnvKeys(n int) error {
	if n > maxEnvKeys {
		return server.Invalid("env would have %d keys; a service may have at most %d.", n, maxEnvKeys)
	}
	return nil
}

// ValidHost reports whether h is a full hostname, such as app.example.com: lowercase, with at
// least two labels and no trailing dot.
func ValidHost(h string) bool {
	if len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// SortedKeys returns the keys of m in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
