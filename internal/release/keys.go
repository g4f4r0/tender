// Package release holds the public keys that sign Tender's releases. The update checks a release
// against them, and the release workflow's signing program checks that its key is one of them.
package release

import (
	"crypto/ed25519"
	"encoding/base64"
)

// keys are the public keys that sign Tender's releases: base64 of 32-byte ed25519 keys. The release
// workflow signs each release's SHA256SUMS with the private key, which lives only in the release
// environment's secret and in the maintainer's secret store. It is a list, so a new key can ship
// before the old one is retired.
var keys = []string{"FFCQetX09NSfXliwZi1eVws0iGQkNwZch0dLQTru3TI="}

// testKey is one more key, set only in the end-to-end test's builds with
// -ldflags "-X github.com/g4f4r0/tender/internal/release.testKey=...". Release builds leave it empty.
var testKey string

// PublicKeys returns the keys a release's signature may come from.
func PublicKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range append(keys, testKey) {
		if raw, err := base64.StdEncoding.DecodeString(k); err == nil && len(raw) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(raw))
		}
	}
	return out
}
