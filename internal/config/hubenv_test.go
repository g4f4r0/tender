package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

const testKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // bytes 0 to 31

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestLoadHubEnvDefaults(t *testing.T) {
	h, err := LoadHubEnv(env(map[string]string{EnvEncryptionKey: testKey}))
	if err != nil {
		t.Fatal(err)
	}
	if h.Listen != DefaultListen || h.Database != DefaultDatabase || h.SMTP != nil || len(h.EncryptionKey) != 32 || h.EncryptionKey[31] != 31 {
		t.Errorf("got %+v", h)
	}
}

func TestLoadHubEnv(t *testing.T) {
	h, err := LoadHubEnv(env(map[string]string{
		EnvEncryptionKey: " " + testKey + "\n", EnvListen: "0.0.0.0:9000", EnvDatabase: "/tmp/hub.db",
		EnvSMTPHost: "smtp.example.net", EnvSMTPPort: "2465", EnvSMTPUsername: "api_token", EnvSMTPPassword: " pa ss ",
		EnvSMTPFrom: "Tender <alerts@example.net>",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := SMTP{Host: "smtp.example.net", Port: 2465, Username: "api_token", Password: " pa ss ", From: "Tender <alerts@example.net>"}
	if h.Listen != "0.0.0.0:9000" || h.Database != "/tmp/hub.db" || h.SMTP == nil || *h.SMTP != want {
		t.Errorf("got %+v, smtp %+v", h, h.SMTP)
	}
}

func TestSMTPDefaultsAndOptionalLogin(t *testing.T) {
	s, err := LoadSMTP(env(map[string]string{EnvSMTPHost: "localhost", EnvSMTPFrom: "alerts@example.net"}))
	if err != nil || s == nil || s.Port != 465 || s.Username != "" || s.Password != "" {
		t.Errorf("got %+v, %v; want port 465 and no login", s, err)
	}
	if s, err := LoadSMTP(env(nil)); s != nil || err != nil {
		t.Errorf("no host: %+v, %v; want email off", s, err)
	}
}

func TestLoadHubEnvRejects(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	cases := map[string]struct {
		vars map[string]string
		want string
	}{
		"no key":            {nil, "openssl rand -base64 32"},
		"blank key":         {map[string]string{EnvEncryptionKey: "  "}, "openssl rand -base64 32"},
		"key not base64":    {map[string]string{EnvEncryptionKey: "not base64!"}, "not base64"},
		"key too short":     {map[string]string{EnvEncryptionKey: short}, "16 bytes"},
		"from without host": {map[string]string{EnvEncryptionKey: testKey, EnvSMTPFrom: "a@b.co"}, EnvSMTPHost},
		"password alone":    {map[string]string{EnvEncryptionKey: testKey, EnvSMTPPassword: "x"}, EnvSMTPHost},
		"host without from": {map[string]string{EnvEncryptionKey: testKey, EnvSMTPHost: "h"}, EnvSMTPFrom},
		"bad from":          {map[string]string{EnvEncryptionKey: testKey, EnvSMTPHost: "h", EnvSMTPFrom: "nobody"}, EnvSMTPFrom},
		"bad port":          {map[string]string{EnvEncryptionKey: testKey, EnvSMTPHost: "h", EnvSMTPFrom: "a@b.co", EnvSMTPPort: "70000"}, EnvSMTPPort},
		"port not a number": {map[string]string{EnvEncryptionKey: testKey, EnvSMTPHost: "h", EnvSMTPFrom: "a@b.co", EnvSMTPPort: "tls"}, EnvSMTPPort},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadHubEnv(env(c.vars))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
			if err != nil && strings.Contains(err.Error(), testKey) {
				t.Errorf("error shows the key: %v", err)
			}
		})
	}
}
