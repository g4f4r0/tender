package config

import (
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/secret"
)

// The hub's environment variables. systemd reads them from /etc/tender/hub.env.
const (
	EnvEncryptionKey = "TENDER_ENCRYPTION_KEY"
	EnvListen        = "TENDER_LISTEN"
	EnvDatabase      = "TENDER_DATABASE"
	EnvSMTPHost      = "TENDER_SMTP_HOST"
	EnvSMTPPort      = "TENDER_SMTP_PORT"
	EnvSMTPUsername  = "TENDER_SMTP_USERNAME"
	EnvSMTPPassword  = "TENDER_SMTP_PASSWORD"
	EnvSMTPFrom      = "TENDER_SMTP_FROM"
)

// The hub's settings when neither the environment nor a flag sets them.
const (
	DefaultDatabase = "/var/lib/tender/hub/hub.db"
	DefaultListen   = "127.0.0.1:8080"
	DefaultSMTPPort = 465
)

// HubEnv is the hub's settings from its environment.
type HubEnv struct {
	EncryptionKey []byte
	Listen        string
	Database      string
	SMTP          *SMTP // nil when TENDER_SMTP_HOST is not set: email is off
}

// SMTP says how the hub sends email: over TLS from the first byte, as on port 465.
type SMTP struct {
	Host     string
	Port     int
	Username string // empty means no login
	Password string
	From     string // Tender <alerts@example.com>
}

// LoadHubEnv reads the hub's settings from getenv, such as os.Getenv. The encryption key is required.
func LoadHubEnv(getenv func(string) string) (HubEnv, error) {
	h := HubEnv{Listen: DefaultListen, Database: DefaultDatabase}
	var err error
	if h.EncryptionKey, err = LoadEncryptionKey(getenv); err != nil {
		return HubEnv{}, err
	}
	if h.EncryptionKey == nil {
		return HubEnv{}, noKeyError()
	}
	if v := strings.TrimSpace(getenv(EnvListen)); v != "" {
		h.Listen = v
	}
	if v := strings.TrimSpace(getenv(EnvDatabase)); v != "" {
		h.Database = v
	}
	if h.SMTP, err = LoadSMTP(getenv); err != nil {
		return HubEnv{}, err
	}
	return h, nil
}

func noKeyError() error {
	return errors.New(EnvEncryptionKey + " is not set. The hub stores secrets encrypted with it. Make a key with \"openssl rand -base64 32\" " +
		"and put it in /etc/tender/hub.env as " + EnvEncryptionKey + "=<the key>, or run \"sudo tender install hub\", which does both")
}

// LoadEncryptionKey returns the decoded key, or nil when the variable is not set. A key that is
// set but wrong is an error: a typo must not turn into a hub that can't read its own data.
func LoadEncryptionKey(getenv func(string) string) ([]byte, error) {
	v := strings.TrimSpace(getenv(EnvEncryptionKey))
	if v == "" {
		return nil, nil
	}
	key, err := secret.ParseKey(v)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid: %w. It must be 32 random bytes in base64: make one with \"openssl rand -base64 32\"", EnvEncryptionKey, err)
	}
	return key, nil
}

// LoadSMTP reads the email settings from getenv. It returns nil when TENDER_SMTP_HOST is not set,
// which turns email off; the other variables without a host are an error, so a missing line
// can't turn email off by accident.
func LoadSMTP(getenv func(string) string) (*SMTP, error) {
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }
	s := &SMTP{Host: get(EnvSMTPHost), Username: get(EnvSMTPUsername), Password: getenv(EnvSMTPPassword), From: get(EnvSMTPFrom)}
	if s.Host == "" {
		for _, name := range []string{EnvSMTPFrom, EnvSMTPUsername, EnvSMTPPassword, EnvSMTPPort} {
			if getenv(name) != "" {
				return nil, fmt.Errorf("%s is set but %s is not. Set the SMTP server's host to turn email on, or remove the TENDER_SMTP_ lines to leave it off", name, EnvSMTPHost)
			}
		}
		return nil, nil
	}
	s.Port = DefaultSMTPPort
	if v := get(EnvSMTPPort); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%s must be a port from 1 to 65535, got %q. Fix it, or remove it for %d", EnvSMTPPort, v, DefaultSMTPPort)
		}
		s.Port = port
	}
	if s.From == "" {
		return nil, fmt.Errorf("%s is required when %s is set. Set it to the sender, like Tender <alerts@example.com>", EnvSMTPFrom, EnvSMTPHost)
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return nil, fmt.Errorf("%s is not an email address (%w). Use a form like alerts@example.com or Tender <alerts@example.com>", EnvSMTPFrom, err)
	}
	return s, nil
}
