package install

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	hubEnvPath    = etcDir + "/hub.env"
	defaultListen = "127.0.0.1:8080"
	keyVariable   = "TENDER_ENCRYPTION_KEY"
)

const hubEnvHeader = `# Settings of the Tender hub, read by systemd when the service starts.
# Keep this file private: the encryption key opens every secret the hub stores.
# After a change, run: sudo systemctl restart tenderhub
`

// hubEnv returns the content of hub.env: old with the encryption key (made when old has none) and
// the SMTP settings in o. Lines it doesn't set, such as comments and TENDER_LISTEN, stay as they
// are. newKey says a key was made.
func hubEnv(old []byte, o HubOptions) (data []byte, newKey bool, err error) {
	text := string(old)
	if text == "" {
		text = hubEnvHeader
	}
	key := envValue([]byte(text), keyVariable)
	if key == "" {
		if key, err = newEncryptionKey(); err != nil {
			return nil, false, err
		}
		newKey = true
	} else if raw, derr := base64.StdEncoding.DecodeString(key); derr != nil || len(raw) != 32 {
		return nil, false, fmt.Errorf("%s in %s is not 32 random bytes in base64. Restore the key from your backup of that file. "+
			"Tender keeps it as it is, because the hub's stored secrets need the same key", keyVariable, hubEnvPath)
	}

	settings := [][2]string{{keyVariable, key}, {"TENDER_SMTP_HOST", o.SMTPHost}, {"TENDER_SMTP_FROM", o.SMTPFrom},
		{"TENDER_SMTP_USERNAME", o.SMTPUsername}, {"TENDER_SMTP_PASSWORD", o.SMTPPassword}}
	if o.SMTPPort != 0 {
		settings = append(settings, [2]string{"TENDER_SMTP_PORT", strconv.Itoa(o.SMTPPort)})
	}
	clear := slices.Clone(o.Clear)
	if slices.Contains(clear, "TENDER_SMTP_USERNAME") {
		clear = append(clear, "TENDER_SMTP_PASSWORD") // a password without its user is no use
	}
	for _, name := range clear {
		if !strings.HasPrefix(name, "TENDER_SMTP_") {
			return nil, false, fmt.Errorf("%s can't be cleared", name)
		}
		text = unsetEnv(text, name)
	}
	for _, kv := range settings {
		if kv[1] == "" {
			continue
		}
		if strings.ContainsAny(kv[1], "\r\n") {
			return nil, false, fmt.Errorf("the value of %s has a line break. Use one line", kv[0])
		}
		text = setEnv(text, kv[0], kv[1])
	}
	host, from := envValue([]byte(text), "TENDER_SMTP_HOST"), envValue([]byte(text), "TENDER_SMTP_FROM")
	if (host == "") != (from == "") {
		return nil, false, errors.New("alert emails need both an SMTP server and a sender. Pass --smtp-host and --smtp-from together, " +
			`or both empty, --smtp-host "" --smtp-from "", to turn alert emails off`)
	}
	return []byte(text), newKey, nil
}

// newEncryptionKey returns 32 random bytes, base64 encoded.
func newEncryptionKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot make an encryption key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

var plainValue = regexp.MustCompile(`^[A-Za-z0-9+/=@._:-]+$`)

// setEnv sets name in an env file's text: it replaces the line that has it, or adds one at the end.
func setEnv(text, name, value string) string {
	line := name + "=" + quoteEnv(value)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		if k, _, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) == name {
			lines[i] = line
			return strings.Join(lines, "\n") + "\n"
		}
	}
	return strings.Join(append(lines, line), "\n") + "\n"
}

// unsetEnv removes the line that sets name from an env file's text.
func unsetEnv(text, name string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines = slices.DeleteFunc(lines, func(l string) bool {
		k, _, ok := strings.Cut(l, "=")
		return ok && strings.TrimSpace(k) == name
	})
	return strings.Join(lines, "\n") + "\n"
}

// hubUnit renders the hub's unit for the address it listens on: systemd lets it bind that port
// only. listen is TENDER_LISTEN, or empty for the default.
func hubUnit(unit []byte, listen string) ([]byte, error) {
	if listen == "" {
		listen = defaultListen
	}
	_, port, err := net.SplitHostPort(listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("TENDER_LISTEN in %s is %q, which is not an address with a port, such as %s. Fix it and run this again", hubEnvPath, listen, defaultListen)
	}
	const bind = "\nSocketBindAllow=tcp:8080\n"
	if !bytes.Contains(unit, []byte(bind)) {
		return nil, errors.New("the embedded tenderhub unit has no SocketBindAllow line")
	}
	return bytes.Replace(unit, []byte(bind), []byte("\nSocketBindAllow=tcp:"+port+"\n"), 1), nil
}

// quoteEnv writes a value the way systemd's EnvironmentFile reads it: plain, or in double quotes
// with backslash and quote escaped.
func quoteEnv(v string) string {
	if plainValue.MatchString(v) {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// envValue returns the value of name in an env file's text, without its quotes, or "".
func envValue(text []byte, name string) string {
	for _, l := range strings.Split(string(text), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.TrimSpace(k) != name {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			return strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(v[1 : len(v)-1])
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1]
		}
		return v
	}
	return ""
}
