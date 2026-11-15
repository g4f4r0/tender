package deploy

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The folder holds the compose file and .env for root alone, and the files for the containers to
// read; a file the service no longer has goes on the next deploy, and nothing else does.
func TestWriteFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	d := Deployment{Name: "notes", Compose: "services: {}\n", Env: map[string]string{"B": "two $HOME", "A": "1"},
		Files: map[string]string{"config/app.toml": "x = 1\n", "old.txt": "old"}}
	if err := writeFolder(dir, d); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, ".env")); got != "A='1'\nB='two $HOME'\n" {
		t.Errorf(".env = %q", got)
	}
	for p, want := range map[string]os.FileMode{"": 0o700, "compose.yaml": 0o600, ".env": 0o600, "config/app.toml": 0o644, "config": 0o755} {
		if got := mode(t, filepath.Join(dir, p)); got != want {
			t.Errorf("%s has mode %o, want %o", p, got, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "data.db"), []byte("the container's"), 0o600); err != nil {
		t.Fatal(err)
	}
	delete(d.Files, "old.txt")
	d.Compose = "services: {web: {image: x}}\n"
	if err := writeFolder(dir, d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file the service no longer has is still there: %v", err)
	}
	if read(t, filepath.Join(dir, "data.db")) != "the container's" || read(t, filepath.Join(dir, "compose.yaml")) != d.Compose {
		t.Error("the second deploy changed the wrong files")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".tender-*")); len(left) != 1 || filepath.Base(left[0]) != manifestName {
		t.Errorf("temporary files are left: %v", left)
	}
}

// A symbolic link a container made in the folder can't take a write outside it.
func TestWriteFolderStaysInside(t *testing.T) {
	dir, outside := filepath.Join(t.TempDir(), "notes"), t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	err := writeFolder(dir, Deployment{Name: "notes", Compose: "x", Files: map[string]string{"config/app.toml": "x"}})
	if err == nil {
		t.Error("a file was written through a link out of the folder")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("the folder outside got %v", entries)
	}
}

func TestMakeVolumes(t *testing.T) {
	dir := t.TempDir()
	r, err := ParseRendered([]byte(`{"services":{"web":{"image":"x"}},"volumes":{"db":{},"cache":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := makeVolumes(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"volumes/db", "volumes/cache"} {
		if fi, err := os.Stat(filepath.Join(dir, v)); err != nil || !fi.IsDir() {
			t.Errorf("%s: %v", v, err)
		}
	}
}

func TestCheckPath(t *testing.T) {
	for _, p := range []string{"nginx.conf", "config/app.toml", "a/b/c_d-e.1"} {
		if err := CheckPath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for p, want := range map[string]string{
		"":                       "must be 1 to 255 bytes",
		"/etc/passwd":            "is absolute",
		"../x":                   "can't go outside",
		"a/../../x":              "must be a clean path",
		"./x":                    "must be a clean path",
		"a//b":                   "must be a clean path",
		"a/":                     "must be a clean path",
		"..":                     "can't go outside",
		"compose.yaml":           "is a file Tender writes itself",
		"compose.tender.yaml":    "is a file Tender writes itself",
		".env":                   "is a file Tender writes itself",
		"volumes/db/x":           "is in volumes/",
		"a b":                    "may only hold letters",
		strings.Repeat("a", 256): "must be 1 to 255 bytes",
	} {
		if err := CheckPath(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", p, err, want)
		}
	}
}

func TestCheckFilesAndEnv(t *testing.T) {
	if err := CheckFiles(map[string]string{"a": "x", "a/b": "y"}); err == nil || !strings.Contains(err.Error(), `"a" is a file, so "a/b" can't be inside it`) {
		t.Errorf("a file and a folder of one name: %v", err)
	}
	if err := CheckFiles(map[string]string{"a": "\x00"}); err == nil {
		t.Error("a binary file was taken")
	}
	if err := CheckFiles(map[string]string{"a": strings.Repeat("x", MaxFilesBytes+1)}); err == nil {
		t.Error("files over 1 MiB were taken")
	}
	if err := CheckEnv(map[string]string{"DB_URL": "postgres://u:p@db/x?a=$b", "_X1": ""}); err != nil {
		t.Error(err)
	}
	for name, env := range map[string]map[string]string{
		"lowercase key": {"db": "x"},
		"digit first":   {"1X": "x"},
		"hidden value":  {"KEY": Hidden},
		"single quote":  {"KEY": "it's"},
		"line break":    {"KEY": "a\nb"},
		// compose reads these from .env as its own settings, such as COMPOSE_FILE.
		"COMPOSE_":      {"COMPOSE_FILE": "/etc/evil.yaml"},
		"DOCKER_":       {"DOCKER_HOST": "tcp://x"},
		"COMPOSE_ bare": {"COMPOSE_PROJECT_NAME": "blog"},
	} {
		err := CheckEnv(env)
		if err == nil {
			t.Errorf("%s was taken", name)
		}
		if strings.HasPrefix(name, "COMPOSE_") || name == "DOCKER_" {
			if err == nil || !strings.Contains(err.Error(), "starts with COMPOSE_ or DOCKER_, which docker compose reads from .env as its own settings") {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	for _, n := range []string{"notes", "a", "app2"} {
		if CheckName(n) != nil {
			t.Errorf("name %s refused", n)
		}
	}
	for _, n := range []string{"", "Notes", "my-app", "2app", strings.Repeat("a", 33)} {
		if CheckName(n) == nil {
			t.Errorf("name %q taken", n)
		}
	}
	for h, want := range map[string]bool{"app.example.com": true, "a.b": true, "example": false, "App.example.com": false, "a..b": false, "a.b.": false} {
		if ValidHost(h) != want {
			t.Errorf("ValidHost(%q) = %v", h, !want)
		}
	}
}
