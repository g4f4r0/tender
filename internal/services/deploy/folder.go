package deploy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
)

// writeFolder writes the service's compose file, .env and files in dir, each atomically, and removes the
// files of the last deploy that are gone. It works through an os.Root, so a symbolic link that a
// container made in a mounted folder can't take a write outside dir.
func writeFolder(dir string, d Deployment) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := writeAtomic(root, "compose.yaml", []byte(d.Compose), 0o600); err != nil {
		return err
	}
	if err := writeAtomic(root, ".env", envFile(d.Env), 0o600); err != nil {
		return err
	}
	for _, p := range SortedKeys(d.Files) {
		if dir := path.Dir(p); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("make the folder of %s: %w", p, err)
			}
		}
		if err := writeAtomic(root, p, []byte(d.Files[p]), 0o644); err != nil {
			return err
		}
	}
	var last []string
	if b, err := root.ReadFile(manifestName); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	for _, p := range last {
		if _, keep := d.Files[p]; !keep && CheckPath(p) == nil {
			if err := root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove %s, which the service no longer has: %w", p, err)
			}
		}
	}
	list, _ := json.Marshal(SortedKeys(d.Files))
	return writeAtomic(root, manifestName, list, 0o600)
}

// writeAtomic writes name in root through a temporary file beside it, then renames it over name.
func writeAtomic(root *os.Root, name string, content []byte, mode os.FileMode) error {
	var b [8]byte
	_, _ = rand.Read(b[:])
	tmp := path.Join(path.Dir(name), ".tender-"+hex.EncodeToString(b[:]))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	_, err = f.Write(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Chmod(tmp, mode)
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// envFile is the service's .env: each value single-quoted, so compose takes it as it is, $ and all.
func envFile(env map[string]string) []byte {
	var b strings.Builder
	for _, k := range SortedKeys(env) {
		b.WriteString(k + "='" + env[k] + "'\n")
	}
	return []byte(b.String())
}

// makeVolumes makes the folder of each named volume, volumes/<volume> in dir, which the override
// binds it to.
func makeVolumes(dir string, r Rendered) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll("volumes", 0o755); err != nil {
		return fmt.Errorf("make the volumes folder: %w", err)
	}
	for _, v := range SortedKeys(r.Volumes) {
		if err := root.Mkdir(path.Join("volumes", v), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("make the folder of volume %s: %w", v, err)
		}
	}
	return nil
}
