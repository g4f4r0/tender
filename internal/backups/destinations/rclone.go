package destinations

import (
	"errors"
	"regexp"
	"strings"

	"github.com/g4f4r0/tender/internal/server"
)

// rclone is any storage rclone reaches, through a remote configured on each daemon's machine in
// Host.RcloneConfig. restic starts rclone itself.
type rclone struct{}

type rcloneConfig struct {
	Remote string `json:"remote"`
}

// remoteName is an rclone remote's name: what rclone accepts, without a leading - or space.
var remoteName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+@ -]{0,62}$`)

func (rclone) Fields() []string  { return []string{"remote"} }
func (rclone) Secrets() []string { return nil }

func (t rclone) Check(c Config) (Config, error) {
	var v rcloneConfig
	if err := decode("rclone", t, c, &v); err != nil {
		return nil, err
	}
	name, path, ok := strings.Cut(v.Remote, ":")
	switch {
	case v.Remote == "":
		return nil, required("remote", "an rclone remote and a folder in it, such as gdrive:backups")
	case !ok || !remoteName.MatchString(name) || strings.HasSuffix(name, " "):
		return nil, server.Invalid("config.remote must be an rclone remote's name, a colon and a folder, such as gdrive:backups.")
	}
	if err := cleanPath("remote", path); err != nil {
		return nil, err
	}
	v.Remote = name + ":" + strings.TrimRight(path, "/")
	return encode(v), nil
}

func (rclone) Location(c Config) string { return "rclone:" + str(c, "remote") }

func (t rclone) Open(c Config, machine string, h Host) (Repo, error) {
	if _, err := h.lookPath("rclone"); err != nil {
		return Repo{}, errors.New("rclone is not installed on this machine, so it can't reach the rclone destination. " +
			"Install rclone and configure the remote in " + h.RcloneConfig + ", as the backups guide shows")
	}
	repo := t.Location(c)
	if !strings.HasSuffix(repo, ":") {
		repo += "/"
	}
	return Repo{Repository: repo + machine, Env: []string{"RCLONE_CONFIG=" + h.RcloneConfig}}, nil
}
