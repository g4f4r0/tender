package destinations

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/g4f4r0/tender/internal/server"
)

// sftp is a folder on a server reached over SSH. The SSH key and known_hosts live on each daemon's
// machine, in Host.SSHDir, put there by the machine's admin; Tender never handles the private key.
type sftp struct{}

type sftpConfig struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user"`
	Path string `json:"path"`
}

// The files sftp needs in Host.SSHDir.
const (
	SSHKey        = "id_ed25519"
	SSHKnownHosts = "known_hosts"
)

var (
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	userPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)
)

func (sftp) Fields() []string  { return []string{"host", "port", "user", "path"} }
func (sftp) Secrets() []string { return nil }

func (t sftp) Check(c Config) (Config, error) {
	var v sftpConfig
	if err := decode("sftp", t, c, &v); err != nil {
		return nil, err
	}
	if v.Port == 0 {
		v.Port = 22
	}
	switch {
	case v.Host == "":
		return nil, required("host", "the SSH server's name or address")
	case !hostPattern.MatchString(v.Host) && net.ParseIP(v.Host) == nil:
		return nil, server.Invalid("config.host must be a host name such as backup.example.com, or an IP address.")
	case v.Port < 1 || v.Port > 65535:
		return nil, server.Invalid("config.port must be from 1 to 65535.")
	case v.User == "":
		return nil, required("user", "the SSH user")
	case !userPattern.MatchString(v.User):
		return nil, server.Invalid("config.user must be a user name of letters, digits, _, . and -, up to 32 characters.")
	case v.Path == "":
		return nil, required("path", "the folder on the server, absolute or relative to the user's home")
	}
	if err := cleanPath("path", v.Path); err != nil {
		return nil, err
	}
	if v.Path != "/" {
		v.Path = strings.TrimSuffix(v.Path, "/")
	}
	return encode(v), nil
}

// Location is restic's sftp URL: sftp://user@host:port/path, where a path relative to the user's
// home follows one slash and an absolute one two.
func (sftp) Location(c Config) string {
	port, _ := c["port"].(float64)
	if p, ok := c["port"].(int); ok {
		port = float64(p)
	}
	host := str(c, "host")
	return "sftp://" + str(c, "user") + "@" + net.JoinHostPort(host, strconv.Itoa(int(port))) + "/" + str(c, "path")
}

func (t sftp) Open(c Config, machine string, h Host) (Repo, error) {
	key, known := filepath.Join(h.SSHDir, SSHKey), filepath.Join(h.SSHDir, SSHKnownHosts)
	for _, f := range []string{key, known} {
		if _, err := os.Stat(f); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return Repo{}, fmt.Errorf("the sftp destination needs %s on this machine, and it isn't there. "+
					"Make the key and known_hosts as the backups guide shows", f)
			}
			return Repo{}, fmt.Errorf("the sftp destination can't read %s: %w", f, err)
		}
	}
	if _, err := h.lookPath("ssh"); err != nil {
		return Repo{}, errors.New("ssh is not installed on this machine, so it can't reach the sftp destination. Install the OpenSSH client")
	}
	// restic splits sftp.args like a shell, so the paths go as they are; SSHDir has no spaces.
	args := strings.Join([]string{
		"-i", key, "-o", "IdentitiesOnly=yes", "-o", "UserKnownHostsFile=" + known, "-o", "StrictHostKeyChecking=yes",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=30", "-o", "ServerAliveInterval=60",
	}, " ")
	return Repo{Repository: t.Location(c) + "/" + machine, Options: []string{"sftp.args=" + args}}, nil
}
