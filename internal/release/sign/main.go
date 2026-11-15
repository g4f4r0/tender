// Command sign signs a release's SHA256SUMS for the release workflow, with only the standard library.
// Usage: TENDER_RELEASE_SIGNING_KEY=... go run ./internal/release/sign DIR
//
// The key is the base64 of a 32-byte ed25519 seed. It refuses a key whose public key isn't one of
// the release keys every update checks against, so a release is never signed by a key no binary
// trusts. It writes DIR/SHA256SUMS.sig, the base64 of the signature, and never prints the key.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/g4f4r0/tender/internal/release"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, release.PublicKeys(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, keys []ed25519.PublicKey, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: sign DIR, the folder with the release's SHA256SUMS")
	}
	encoded := strings.TrimSpace(getenv("TENDER_RELEASE_SIGNING_KEY"))
	if encoded == "" {
		return errors.New("TENDER_RELEASE_SIGNING_KEY is empty. Set it to the release key from the release environment's secret")
	}
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("TENDER_RELEASE_SIGNING_KEY must be the base64 of a 32-byte ed25519 seed")
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	if !slices.ContainsFunc(keys, func(k ed25519.PublicKey) bool { return pub.Equal(k) }) {
		return fmt.Errorf("the signing key's public key %s is not one of the release keys in internal/release/keys.go, "+
			"so no Tender would trust the signature. Add it there first", base64.StdEncoding.EncodeToString(pub))
	}
	path := filepath.Join(args[0], "SHA256SUMS")
	sums, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	if len(sums) == 0 {
		return fmt.Errorf("%s is empty", path)
	}
	sig := ed25519.Sign(key, sums)
	if !ed25519.Verify(pub, sums, sig) {
		return errors.New("the signature does not verify")
	}
	if err := os.WriteFile(path+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Signed %s with the release key %s.\n", path, base64.StdEncoding.EncodeToString(pub))
	return err
}
