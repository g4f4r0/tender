package destinations

import (
	"path/filepath"

	"github.com/g4f4r0/tender/internal/server"
)

// local is a folder on each daemon's machine, such as a mounted disk.
type local struct{}

type localConfig struct {
	Path string `json:"path"`
}

func (local) Fields() []string  { return []string{"path"} }
func (local) Secrets() []string { return nil }

func (t local) Check(c Config) (Config, error) {
	var v localConfig
	if err := decode("local", t, c, &v); err != nil {
		return nil, err
	}
	if v.Path == "" {
		return nil, required("path", "an absolute folder on each machine, such as /mnt/backup")
	}
	if !filepath.IsAbs(v.Path) {
		return nil, server.Invalid("config.path must be absolute, such as /mnt/backup.")
	}
	if err := cleanPath("path", v.Path); err != nil {
		return nil, err
	}
	v.Path = filepath.Clean(v.Path)
	if v.Path == "/" {
		return nil, server.Invalid("config.path can't be /. Use a folder of its own, such as /mnt/backup.")
	}
	return encode(v), nil
}

func (local) Location(c Config) string { return "local:" + str(c, "path") }

func (t local) Open(c Config, machine string, _ Host) (Repo, error) {
	return Repo{Repository: t.Location(c) + "/" + machine}, nil
}
