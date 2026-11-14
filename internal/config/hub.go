package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/mail"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultHubPath is where the hub reads its config when --config isn't given.
const DefaultHubPath = "/etc/tender/hub.toml"

// DefaultDatabase and DefaultListen are the hub's settings when neither the file nor a flag sets them.
const (
	DefaultDatabase = "/var/lib/tender/hub/hub.db"
	DefaultListen   = "127.0.0.1:8080"
)

// Hub is /etc/tender/hub.toml.
type Hub struct {
	Listen   string `toml:"listen"`
	Database string `toml:"database"`
	Email    *Email `toml:"email"` // nil when the file has no [email] table: alerts stay unsent
}

// Email says where alert emails go. The SMTP password is the credential smtp-password.
type Email struct {
	Host     string   `toml:"host"`
	Port     int      `toml:"port"` // implicit TLS, 465 when left out
	Username string   `toml:"username"`
	From     string   `toml:"from"` // tender <noreply@example.net>
	To       []string `toml:"to"`
}

// LoadHub reads and validates the hub config. An empty path means DefaultHubPath, where a missing
// file means the defaults; a path given by the user must exist. Unknown keys are errors.
func LoadHub(path string) (Hub, error) {
	h := Hub{Listen: DefaultListen, Database: DefaultDatabase}
	explicit := path != ""
	if !explicit {
		path = DefaultHubPath
	}
	md, err := toml.DecodeFile(path, &h)
	if err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return h, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return Hub{}, fmt.Errorf("there is no hub config at %s. Check the path passed to --config: %w", path, err)
		}
		return Hub{}, fmt.Errorf("cannot read config %s. Fix its TOML syntax and check that you can read it: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		names := make([]string, len(keys))
		for i, k := range keys {
			names[i] = k.String()
		}
		return Hub{}, fmt.Errorf("config %s: unknown keys: %s. Fix the spelling or remove them", path, strings.Join(names, ", "))
	}
	if h.Email != nil {
		if err := h.Email.validate(); err != nil {
			return Hub{}, fmt.Errorf("config %s: [email] %w. Fix it and start the hub again", path, err)
		}
	}
	return h, nil
}

func (e *Email) validate() error {
	if e.Port == 0 {
		e.Port = 465
	}
	switch {
	case e.Host == "":
		return errors.New("host is required")
	case e.Port < 1 || e.Port > 65535:
		return errors.New("port must be from 1 to 65535")
	case e.Username == "":
		return errors.New("username is required")
	case len(e.To) == 0:
		return errors.New("to needs at least one address")
	}
	if _, err := mail.ParseAddress(e.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	for _, to := range e.To {
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("to: %w", err)
		}
	}
	return nil
}
