package destinations

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/server"
)

func check(t *testing.T, typ string, c Config) (Config, error) {
	t.Helper()
	tp, ok := Get(typ)
	if !ok {
		t.Fatalf("no type %s", typ)
	}
	return tp.Check(c)
}

func TestNames(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "local,rclone,rest,s3,sftp" {
		t.Errorf("Names() = %s", got)
	}
	if _, ok := Get("gcs"); ok {
		t.Error("gcs is not a type")
	}
}

// Every type refuses what it can't use with a 422 that says how to fix it, and cleans up what it takes.
func TestCheck(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		config    Config
		says      string // "" means valid
		want      Config // the cleaned config, when valid
	}{
		{"s3", "s3", Config{"endpoint": "https://a.r2.cloudflarestorage.com", "bucket": "bkt", "path": "/x/y/", "access_key_id": "k", "secret_access_key": "s"}, "",
			Config{"endpoint": "https://a.r2.cloudflarestorage.com", "bucket": "bkt", "path": "x/y", "access_key_id": "k", "secret_access_key": "s"}},
		{"s3 bad endpoint", "s3", Config{"endpoint": "a.example.com", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}, "config.endpoint must be a URL", nil},
		{"s3 endpoint with a login", "s3", Config{"endpoint": "https://u:p@a.example.com", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}, "config.endpoint", nil},
		{"s3 no secret", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "access_key_id": "k"}, "secret_access_key is required", nil},
		{"s3 dot dot", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "a/../b", "access_key_id": "k", "secret_access_key": "s"}, "plain path", nil},
		{"s3 unknown field", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "region": "x", "access_key_id": "k", "secret_access_key": "s"}, "It takes endpoint, bucket", nil},

		{"s3 bucket with a query", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt?x=1", "access_key_id": "k", "secret_access_key": "s"}, "S3 bucket name", nil},
		{"s3 bucket with a slash", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt/other", "access_key_id": "k", "secret_access_key": "s"}, "S3 bucket name", nil},
		{"s3 bucket in capitals", "s3", Config{"endpoint": "https://a.example.com", "bucket": "Backups", "access_key_id": "k", "secret_access_key": "s"}, "S3 bucket name", nil},
		{"s3 bucket with two dots", "s3", Config{"endpoint": "https://a.example.com", "bucket": "my..bkt", "access_key_id": "k", "secret_access_key": "s"}, "S3 bucket name", nil},
		{"s3 path with a query", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "x?y", "access_key_id": "k", "secret_access_key": "s"}, "plain path", nil},
		{"s3 path with a fragment", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "x#y", "access_key_id": "k", "secret_access_key": "s"}, "plain path", nil},
		{"s3 path with an escaped dot dot", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "a/%2E%2E/b", "access_key_id": "k", "secret_access_key": "s"}, "plain path", nil},
		{"s3 path with an escaped slash", "s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "a%2F..", "access_key_id": "k", "secret_access_key": "s"}, "plain path", nil},
		{"s3 endpoint with a path", "s3", Config{"endpoint": "https://a.example.com/other", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}, "address alone", nil},
		{"s3 endpoint with a query", "s3", Config{"endpoint": "https://a.example.com?x=1", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}, "address alone", nil},
		{"s3 endpoint with a slash", "s3", Config{"endpoint": "https://a.example.com/", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"}, "",
			Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "", "access_key_id": "k", "secret_access_key": "s"}},

		{"local", "local", Config{"path": "/mnt/backup/"}, "", Config{"path": "/mnt/backup"}},
		{"local with a space", "local", Config{"path": "/mnt/my backup"}, "plain path", nil},
		{"local with a percent", "local", Config{"path": "/mnt/a%2F..%2Fetc"}, "plain path", nil},
		{"local relative", "local", Config{"path": "backup"}, "absolute", nil},
		{"local root", "local", Config{"path": "/"}, "can't be /", nil},
		{"local none", "local", Config{}, "config.path is required", nil},

		{"sftp", "sftp", Config{"host": "backup.example.com", "user": "tender", "path": "/srv/restic/"}, "",
			Config{"host": "backup.example.com", "port": float64(22), "user": "tender", "path": "/srv/restic"}},
		{"sftp ipv6 and port", "sftp", Config{"host": "fd7a::1", "port": 2222, "user": "u", "path": "backups"}, "",
			Config{"host": "fd7a::1", "port": float64(2222), "user": "u", "path": "backups"}},
		{"sftp option as host", "sftp", Config{"host": "-oProxyCommand=x", "user": "u", "path": "p"}, "config.host must be", nil},
		{"sftp host with a user", "sftp", Config{"host": "u@h", "user": "u", "path": "p"}, "config.host must be", nil},
		{"sftp option as user", "sftp", Config{"host": "h", "user": "-l", "path": "p"}, "config.user must be", nil},
		{"sftp port", "sftp", Config{"host": "h", "port": 70000, "user": "u", "path": "p"}, "config.port", nil},
		{"sftp port as text", "sftp", Config{"host": "h", "port": "22", "user": "u", "path": "p"}, "config.port must be int", nil},
		{"sftp no path", "sftp", Config{"host": "h", "user": "u"}, "config.path is required", nil},
		{"sftp path with a query", "sftp", Config{"host": "h", "user": "u", "path": "r?x"}, "plain path", nil},
		{"sftp path with an escape", "sftp", Config{"host": "h", "user": "u", "path": "r/%2e%2e/x"}, "plain path", nil},
		{"sftp key in config", "sftp", Config{"host": "h", "user": "u", "path": "p", "key": "x"}, "doesn't take", nil},

		{"rest", "rest", Config{"url": "https://backup.example.com:8000/", "username": "apps", "password": "pw"}, "",
			Config{"url": "https://backup.example.com:8000", "username": "apps", "password": "pw"}},
		{"rest without login", "rest", Config{"url": "http://10.0.0.5:8000"}, "", Config{"url": "http://10.0.0.5:8000"}},
		{"rest login in url", "rest", Config{"url": "https://apps:pw@backup.example.com"}, "can't hold a login", nil},
		{"rest half a login", "rest", Config{"url": "https://backup.example.com", "username": "apps"}, "go together", nil},
		{"rest query", "rest", Config{"url": "https://backup.example.com/?x=1"}, "query", nil},
		{"rest bad scheme", "rest", Config{"url": "ftp://backup.example.com"}, "http or https", nil},
		{"rest escaped slash", "rest", Config{"url": "https://backup.example.com/a%2F..%2Fb"}, "plain path", nil},
		{"rest empty query", "rest", Config{"url": "https://backup.example.com/a?"}, "query", nil},
		{"rest fragment", "rest", Config{"url": "https://backup.example.com/a#b"}, "query or a fragment", nil},

		{"rclone", "rclone", Config{"remote": "gdrive:backups/"}, "", Config{"remote": "gdrive:backups"}},
		{"rclone remote root", "rclone", Config{"remote": "b2:"}, "", Config{"remote": "b2:"}},
		{"rclone local path", "rclone", Config{"remote": "/tmp/x"}, "remote's name, a colon", nil},
		{"rclone option", "rclone", Config{"remote": "-x:y"}, "remote's name", nil},
		{"rclone dot dot", "rclone", Config{"remote": "r:../etc"}, "plain path", nil},
		{"rclone query", "rclone", Config{"remote": "r:a?b"}, "plain path", nil},
		{"rclone escape", "rclone", Config{"remote": "r:a%2F.."}, "plain path", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := check(t, c.typ, c.config)
			if c.says == "" {
				if err != nil {
					t.Fatalf("Check: %v", err)
				}
				for k, v := range c.want {
					if got[k] != v {
						t.Errorf("%s = %v (%T), want %v", k, got[k], got[k], v)
					}
				}
				if len(got) != len(c.want) {
					t.Errorf("cleaned config = %v, want %v", got, c.want)
				}
				return
			}
			var e *server.Error
			if !errors.As(err, &e) || e.Status != 422 || !strings.Contains(e.Detail, c.says) || !strings.HasSuffix(e.Detail, ".") {
				t.Errorf("Check = %v, want a 422 sentence containing %q", err, c.says)
			}
		})
	}
}

// Each type maps its config to a restic repository with the machine's folder, and keeps every
// secret out of it, in the child's environment.
func TestOpen(t *testing.T) {
	ssh := t.TempDir()
	for _, f := range []string{SSHKey, SSHKnownHosts} {
		if err := os.WriteFile(filepath.Join(ssh, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found := func(string) (string, error) { return "/usr/bin/x", nil }
	host := Host{SSHDir: ssh, RcloneConfig: "/etc/tender/rclone.conf", LookPath: found}
	for _, c := range []struct {
		typ      string
		config   Config
		repo     string
		env, opt []string
	}{
		{"s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "path": "p", "access_key_id": "k", "secret_access_key": "s"},
			"s3:https://a.example.com/bkt/p/apps", []string{"AWS_ACCESS_KEY_ID=k", "AWS_SECRET_ACCESS_KEY=s"}, nil},
		{"s3", Config{"endpoint": "https://a.example.com", "bucket": "bkt", "access_key_id": "k", "secret_access_key": "s"},
			"s3:https://a.example.com/bkt/apps", []string{"AWS_ACCESS_KEY_ID=k", "AWS_SECRET_ACCESS_KEY=s"}, nil},
		{"local", Config{"path": "/mnt/backup"}, "local:/mnt/backup/apps", nil, nil},
		{"sftp", Config{"host": "h.example.com", "user": "u", "path": "/srv/r"}, "sftp://u@h.example.com:22//srv/r/apps", nil,
			[]string{"sftp.args=-i " + ssh + "/id_ed25519 -o IdentitiesOnly=yes -o UserKnownHostsFile=" + ssh + "/known_hosts -o StrictHostKeyChecking=yes -o BatchMode=yes -o ConnectTimeout=30 -o ServerAliveInterval=60"}},
		{"sftp", Config{"host": "::1", "port": 2222, "user": "u", "path": "r"}, "sftp://u@[::1]:2222/r/apps", nil, nil},
		{"rest", Config{"url": "https://r.example.com:8000", "username": "apps", "password": "pw"}, "rest:https://r.example.com:8000/apps",
			[]string{"RESTIC_REST_USERNAME=apps", "RESTIC_REST_PASSWORD=pw"}, nil},
		{"rest", Config{"url": "http://r.example.com:8000"}, "rest:http://r.example.com:8000/apps", nil, nil},
		{"rclone", Config{"remote": "gdrive:backups"}, "rclone:gdrive:backups/apps", []string{"RCLONE_CONFIG=/etc/tender/rclone.conf"}, nil},
		{"rclone", Config{"remote": "b2:"}, "rclone:b2:apps", []string{"RCLONE_CONFIG=/etc/tender/rclone.conf"}, nil},
	} {
		tp, _ := Get(c.typ)
		clean, err := tp.Check(c.config)
		if err != nil {
			t.Fatalf("%s %v: %v", c.typ, c.config, err)
		}
		r, err := tp.Open(clean, "apps", host)
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if r.Repository != c.repo || !slices.Equal(r.Env, c.env) || (c.opt != nil && !slices.Equal(r.Options, c.opt)) {
			t.Errorf("%s: %+v, want %s %v %v", c.typ, r, c.repo, c.env, c.opt)
		}
		if strings.Contains(r.Repository, "pw") || strings.Contains(strings.Join(r.Options, " "), "pw") {
			t.Errorf("%s: a secret is in the repository or the options: %+v", c.typ, r)
		}
	}
}

// A daemon without what a type needs says what is missing, in a sentence.
func TestOpenSaysWhatIsMissing(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("not found") }
	empty := t.TempDir()
	sftp, _ := Get("sftp")
	cfg, _ := sftp.Check(Config{"host": "h", "user": "u", "path": "p"})
	if _, err := sftp.Open(cfg, "apps", Host{SSHDir: empty}); err == nil || !strings.Contains(err.Error(), filepath.Join(empty, SSHKey)) {
		t.Errorf("sftp without a key: %v, want the key's path", err)
	}
	for _, f := range []string{SSHKey, SSHKnownHosts} {
		_ = os.WriteFile(filepath.Join(empty, f), nil, 0o600)
	}
	if _, err := sftp.Open(cfg, "apps", Host{SSHDir: empty, LookPath: missing}); err == nil || !strings.Contains(err.Error(), "ssh is not installed") {
		t.Errorf("sftp without ssh: %v", err)
	}
	// OpenSSH refuses a group-readable key, as a key made for a daemon that ran as tender was.
	if err := os.Chmod(filepath.Join(empty, SSHKey), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := sftp.Open(cfg, "apps", Host{SSHDir: empty}); err == nil || !strings.Contains(err.Error(), "mode 0640") ||
		!strings.Contains(err.Error(), "sudo chmod 0600 "+filepath.Join(empty, SSHKey)) {
		t.Errorf("sftp with a group-readable key: %v, want how to fix it", err)
	}
	rc, _ := Get("rclone")
	cfg, _ = rc.Check(Config{"remote": "r:x"})
	if _, err := rc.Open(cfg, "apps", Host{RcloneConfig: "/etc/tender/rclone.conf", LookPath: missing}); err == nil ||
		!strings.Contains(err.Error(), "rclone is not installed") || !strings.Contains(err.Error(), "/etc/tender/rclone.conf") {
		t.Errorf("rclone without rclone: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	for typ, want := range map[string][]string{"s3": {"access_key_id", "secret_access_key"}, "rest": {"password"}, "sftp": nil, "local": nil, "rclone": nil} {
		tp, _ := Get(typ)
		if !slices.Equal(tp.Secrets(), want) {
			t.Errorf("%s secrets = %v, want %v", typ, tp.Secrets(), want)
		}
		for _, s := range tp.Secrets() {
			if !slices.Contains(tp.Fields(), s) {
				t.Errorf("%s: secret %s is not a field", typ, s)
			}
		}
	}
}
