package install

import (
	"bytes"
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// The pinned restic. The archive hash comes from SHA256SUMS, checked with restic's GPG key
// CF8F18F2844575973F79D4E191A6868BD3F7A907. The binary hash is the decompressed official build.
const (
	resticVersion       = "0.19.1"
	resticURL           = "https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2"
	resticArchiveSHA256 = "f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c"
	resticBinarySHA256  = "20d4142678d0d95ec11a4759def1b73fd9190abc9ca19e4b62d067c0b387e639"
	resticPath          = "/usr/local/bin/restic"

	maxArchiveBytes = 64 << 20
	maxBinaryBytes  = 256 << 20
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// installRestic puts the pinned restic at /usr/local/bin/restic. It checks the archive's SHA-256
// before it decompresses and the binary's before it writes, so a wrong download changes nothing.
func (in *Installer) installRestic(ctx context.Context) error {
	if have, err := os.ReadFile(in.path(resticPath)); err == nil && sum(have) == in.ResticBinarySHA256 {
		return nil
	}
	in.say("Installing restic %s", resticVersion)
	archive, err := in.download(ctx)
	if err != nil {
		return err
	}
	if got := sum(archive); got != in.ResticArchiveSHA256 {
		return fmt.Errorf("the restic download failed its SHA-256 check (got %s, want %s), so nothing was installed. Try again, and report it at %s if it repeats", got, in.ResticArchiveSHA256, issuesURL)
	}
	bin, err := io.ReadAll(io.LimitReader(bzip2.NewReader(bytes.NewReader(archive)), maxBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("cannot decompress the restic download, so nothing was installed. Try again: %w", err)
	}
	if len(bin) > maxBinaryBytes {
		return errors.New("the restic download decompresses to more than 256 MB, so nothing was installed. Report it at " + issuesURL)
	}
	if got := sum(bin); got != in.ResticBinarySHA256 {
		return fmt.Errorf("the decompressed restic failed its SHA-256 check (got %s, want %s), so nothing was installed. Try again, and report it at %s if it repeats", got, in.ResticBinarySHA256, issuesURL)
	}
	_, err = in.write(ctx, resticPath, bin, 0o755, "")
	return err
}

func (in *Installer) download(ctx context.Context) ([]byte, error) {
	b, err := get(ctx, in.ResticURL, maxArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("cannot download restic. Check this machine's internet access and run the command again: %w", err)
	}
	return b, nil
}

// get downloads url over HTTP(S) and refuses an answer larger than limit bytes.
func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tender")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d MB", url, limit>>20)
	}
	return b, nil
}
