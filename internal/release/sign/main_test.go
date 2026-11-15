package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSign(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	sums := []byte("abc  tender_linux_amd64\ndef  tender-release.json\n")
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), sums, 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "TENDER_RELEASE_SIGNING_KEY" {
				return v
			}
			return ""
		}
	}
	good := base64.StdEncoding.EncodeToString(seed)
	var out bytes.Buffer
	for name, c := range map[string]struct {
		args []string
		key  string
		keys []ed25519.PublicKey
		want string
	}{
		"no key":       {[]string{dir}, "", []ed25519.PublicKey{pub}, "TENDER_RELEASE_SIGNING_KEY is empty"},
		"not base64":   {[]string{dir}, "%%%", []ed25519.PublicKey{pub}, "base64 of a 32-byte ed25519 seed"},
		"short":        {[]string{dir}, base64.StdEncoding.EncodeToString(seed[:16]), []ed25519.PublicKey{pub}, "base64 of a 32-byte ed25519 seed"},
		"not embedded": {[]string{dir}, good, nil, "is not one of the release keys"},
		"no folder":    {nil, good, []ed25519.PublicKey{pub}, "usage"},
		"no checksums": {[]string{t.TempDir()}, good, []ed25519.PublicKey{pub}, "SHA256SUMS"},
	} {
		if err := run(c.args, env(c.key), c.keys, &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "SHA256SUMS.sig")); err == nil {
			t.Fatalf("%s: wrote a signature", name)
		}
	}
	if err := run([]string{dir}, env(good), []ed25519.PublicKey{pub}, &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.sig"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || !ed25519.Verify(pub, sums, sig) {
		t.Errorf("signature %q does not verify: %v", b, err)
	}
	if strings.Contains(out.String(), good) {
		t.Error("the output shows the key")
	}
}
