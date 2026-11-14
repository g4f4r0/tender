package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	key := newKey(t)
	a, err := Seal(key, []byte("marker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(key, []byte("marker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(a, []byte("marker-secret")) {
		t.Error("the sealed value holds the plaintext")
	}
	if bytes.Equal(a, b) {
		t.Error("two seals of one value are equal: the nonce is not random")
	}
	got, err := Open(key, a)
	if err != nil || string(got) != "marker-secret" {
		t.Errorf("Open = %q, %v", got, err)
	}
	empty, err := Seal(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(key, empty); err != nil || len(got) != 0 {
		t.Errorf("empty value: %q, %v", got, err)
	}
}

func TestWrongKeyAndDamage(t *testing.T) {
	key := newKey(t)
	sealed, err := Seal(key, []byte("marker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(newKey(t), sealed); err == nil || strings.Contains(err.Error(), "marker-secret") {
		t.Errorf("wrong key: %v, want a clean error", err)
	}
	damaged := bytes.Clone(sealed)
	damaged[len(damaged)-1] ^= 1
	if _, err := Open(key, damaged); err == nil {
		t.Error("a damaged value opened")
	}
	for _, short := range [][]byte{nil, {}, sealed[:10]} {
		if _, err := Open(key, short); err == nil {
			t.Errorf("a %d byte value opened", len(short))
		}
	}
	if _, err := Seal(key[:16], []byte("x")); err == nil {
		t.Error("a 16 byte key sealed")
	}
	if _, err := Open(nil, sealed); err == nil {
		t.Error("no key opened")
	}
}

func TestParseKey(t *testing.T) {
	key := newKey(t)
	got, err := ParseKey(base64.StdEncoding.EncodeToString(key))
	if err != nil || !bytes.Equal(got, key) {
		t.Errorf("ParseKey = %x, %v", got, err)
	}
	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(key[:16]), base64.StdEncoding.EncodeToString(append(key, 0))} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) was accepted", bad)
		}
	}
}
