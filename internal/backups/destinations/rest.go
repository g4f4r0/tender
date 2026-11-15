package destinations

import (
	"net/url"
	"strings"

	"github.com/g4f4r0/tender/internal/server"
)

// rest is restic's own REST server, rest-server. The login goes to restic in its environment,
// never in the repository URL, so it can't show up in an error or a process list.
type rest struct{}

type restConfig struct {
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func (rest) Fields() []string  { return []string{"url", "username", "password"} }
func (rest) Secrets() []string { return []string{"password"} }

func (t rest) Check(c Config) (Config, error) {
	var v restConfig
	if err := decode("rest", t, c, &v); err != nil {
		return nil, err
	}
	u, err := url.Parse(v.URL)
	switch {
	case v.URL == "":
		return nil, required("url", "the rest-server's address, such as https://backup.example.com:8000")
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return nil, server.Invalid("config.url must be an http or https URL, such as https://backup.example.com:8000.")
	case u.User != nil:
		return nil, server.Invalid("config.url can't hold a login. Put it in config.username and config.password, which are kept encrypted.")
	case u.RawQuery != "" || u.Fragment != "":
		return nil, server.Invalid("config.url can't have a query or a fragment.")
	case (v.Username == "") != (v.Password == ""):
		return nil, server.Invalid("config.username and config.password go together: give both, or neither for a server without logins.")
	case strings.ContainsAny(v.Username, ":\n\r"):
		return nil, server.Invalid("config.username can't hold a colon or a line break.")
	}
	if err := cleanPath("url", u.Path); err != nil {
		return nil, err
	}
	v.URL = strings.TrimRight(v.URL, "/")
	return encode(v), nil
}

func (rest) Location(c Config) string { return "rest:" + str(c, "url") }

func (t rest) Open(c Config, machine string, _ Host) (Repo, error) {
	r := Repo{Repository: t.Location(c) + "/" + machine}
	if user := str(c, "username"); user != "" {
		r.Env = []string{"RESTIC_REST_USERNAME=" + user, "RESTIC_REST_PASSWORD=" + str(c, "password")}
	}
	return r, nil
}
