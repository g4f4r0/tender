package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const dir = "/var/lib/tender/services/notes"

// rendered is the config compose renders for a compose file whose web service is web, with the
// volumes, networks, secrets and configs of rest.
func rendered(t *testing.T, web, rest string) Rendered {
	t.Helper()
	if rest != "" {
		rest = "," + rest
	}
	r, err := ParseRendered([]byte(`{"services":{"web":` + web + `}` + rest + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Each rule passes a compose file that keeps it and fails one that breaks it, with a sentence that
// names the service in the compose file and the fix.
func TestCheckRules(t *testing.T) {
	ok := `{"image":"nginx:1.27","mem_limit":"536870912","volumes":[{"type":"bind","source":"` + dir + `/data","target":"/data"},
		{"type":"volume","source":"db","target":"/var/lib/db"}],"env_file":[{"path":"` + dir + `/app.env"}],"cap_add":["CHOWN"]}`
	okRest := `"volumes":{"db":{}},"networks":{"default":{}},"secrets":{"key":{"file":"` + dir + `/key.txt"}}`
	if err := CheckRules(rendered(t, ok, okRest), dir); err != nil {
		t.Fatalf("a compose file that keeps every rule: %v", err)
	}
	if err := CheckRules(rendered(t, `{"image":"nginx","deploy":{"resources":{"limits":{"memory":"268435456"}}}}`, ""), dir); err != nil {
		t.Errorf("a memory limit under deploy: %v", err)
	}
	// What compose renders for a common file passes: the keys it adds, the allowed keys, x- ones.
	common := `{"image":"nginx:1.27","command":null,"entrypoint":null,"environment":{"A":"$$x"},"networks":{"default":null,"back":{"aliases":["db"]}},
		"mem_limit":"536870912","mem_reservation":"1","memswap_limit":"1","cpus":0.5,"cpu_shares":512,"pids_limit":100,"restart":"always",
		"healthcheck":{"test":["CMD","true"],"interval":"10s"},"depends_on":{"db":{"condition":"service_healthy","required":true}},
		"labels":{"a":"b"},"logging":{"driver":"json-file","options":{"max-size":"10m"}},"user":"1000","working_dir":"/app","expose":["80"],
		"deploy":{"replicas":1,"resources":{"limits":{"cpus":"0.5","memory":"536870912","pids":10},"reservations":{"memory":"1"}}},
		"ulimits":{"nofile":{"soft":1,"hard":2}},"stop_signal":"SIGTERM","stop_grace_period":"10s","init":true,"read_only":true,"tmpfs":["/tmp"],
		"shm_size":"67108864","cap_drop":["ALL"],"cap_add":["NET_BIND_SERVICE"],"security_opt":["no-new-privileges:true"],"profiles":["x"],
		"hostname":"web","domainname":"x","dns":["1.1.1.1"],"extra_hosts":["a:1.2.3.4"],"sysctls":{"net.core.somaxconn":"1024"},"tty":true,
		"stdin_open":true,"platform":"linux/amd64","pull_policy":"always","scale":1,"network_mode":"","x-note":1,
		"secrets":[{"source":"key"}],"configs":["c"],
		"volumes":[{"type":"bind","source":"` + dir + `/data","target":"/data","bind":{"create_host_path":true}},
			{"type":"volume","source":"db","target":"/db","volume":{"nocopy":true}},{"type":"tmpfs","target":"/run","tmpfs":{"size":1}}]}`
	commonRest := `"name":"notes","x-common":{},"volumes":{"db":{"name":"notes_db"}},"networks":{"default":{"name":"notes_default","ipam":{}},
		"back":{"name":"notes_back","internal":true}},"secrets":{"key":{"name":"notes_key","file":"` + dir + `/key.txt"}},
		"configs":{"c":{"name":"notes_c","content":"x"}}`
	if err := CheckRules(rendered(t, common, commonRest), dir); err != nil {
		t.Errorf("a common rendered file: %v", err)
	}
	if err := CheckRules(rendered(t, `{"image":"x","mem_limit":"1","network_mode":"service:web","ipc":"service:web","pid":"service:web"}`, ""), dir); err != nil {
		t.Errorf("namespaces of a service in the same file: %v", err)
	}
	for _, c := range []struct {
		name, web, rest, want string
	}{
		{"build", `{"image":"x","mem_limit":"1","build":{"context":"."}}`, "", "service web in the compose file has build, and Tender deploys images only"},
		{"no image", `{"mem_limit":"1"}`, "", "service web in the compose file has no image"},
		{"ports", `{"image":"x","mem_limit":"1","ports":[{"target":80,"published":"8080"}]}`, "",
			"service web in the compose file publishes ports, which Tender's routes do instead: remove ports from web and add a route to its port, such as --route web:80:tailscale"},
		{"container_name", `{"image":"x","mem_limit":"1","container_name":"web"}`, "", "sets container_name"},
		{"privileged", `{"image":"x","mem_limit":"1","privileged":true}`, "", "is privileged"},
		{"devices", `{"image":"x","mem_limit":"1","devices":[{"source":"/dev/sda","target":"/dev/sda"}]}`, "", "has devices"},
		{"network host", `{"image":"x","mem_limit":"1","network_mode":"host"}`, "", "sets network_mode: host"},
		{"pid host", `{"image":"x","mem_limit":"1","pid":"host"}`, "", "sets pid: host"},
		{"ipc host", `{"image":"x","mem_limit":"1","ipc":"host"}`, "", "sets ipc: host"},
		{"uts host", `{"image":"x","mem_limit":"1","uts":"host"}`, "", "sets uts, which Tender's rules don't allow: remove uts from web"},
		{"userns host", `{"image":"x","mem_limit":"1","userns_mode":"host"}`, "", "sets userns_mode, which Tender's rules don't allow: remove userns_mode from web"},
		{"unconfined", `{"image":"x","mem_limit":"1","security_opt":["seccomp:unconfined"]}`, "", "has security_opt seccomp:unconfined"},
		{"cap_add", `{"image":"x","mem_limit":"1","cap_add":["CAP_SYS_ADMIN"]}`, "", "adds the capability SYS_ADMIN"},
		{"cap_add all", `{"image":"x","mem_limit":"1","cap_add":["all"]}`, "", "adds the capability ALL"},
		{"docker socket", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/docker.sock","target":"/var/run/docker.sock"}]}`, "",
			"mounts the Docker socket"},
		{"bind outside", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"/etc","target":"/etc"}]}`, "",
			"service web in the compose file mounts /etc from the machine, outside " + dir + ": mount a path relative to the compose file, such as ./data:/etc, or a named volume"},
		{"bind beside", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `-other/x","target":"/x"}]}`, "", "outside " + dir},
		{"env_file outside", `{"image":"x","mem_limit":"1","env_file":["/etc/secrets.env"]}`, "", "reads env_file /etc/secrets.env, outside"},
		{"no memory limit", `{"image":"x"}`, "", "has no memory limit, which Tender's memory-high alert needs: add mem_limit, such as mem_limit: 512m, to web"},
		{"zero memory limit", `{"image":"x","mem_limit":"0"}`, "", `sets mem_limit to "0", which is no memory limit: give web a size, such as mem_limit: 512m`},
		{"no memory limit -1", `{"image":"x","mem_limit":"-1"}`, "", `sets mem_limit to "-1", which is no memory limit`},
		{"no memory limit -1 number", `{"image":"x","mem_limit":-1}`, "", `sets mem_limit to -1, which is no memory limit`},
		{"deploy memory -1", `{"image":"x","deploy":{"resources":{"limits":{"memory":"-1"}}}}`, "",
			`sets deploy.resources.limits.memory to "-1", which is no memory limit`},
		{"mem_limit -1 beside a deploy limit", `{"image":"x","mem_limit":"-1","deploy":{"resources":{"limits":{"memory":"268435456"}}}}`, "",
			`sets mem_limit to "-1", which is no memory limit`},
		// Namespaces: container:<name> joins any container's, such as one on the host network.
		{"network container", `{"image":"x","mem_limit":"1","network_mode":"container:tender-caddy-1"}`, "",
			"sets network_mode: container:tender-caddy-1, which joins another container's, such as one on the machine's own network"},
		{"pid container", `{"image":"x","mem_limit":"1","pid":"container:abc"}`, "", "sets pid: container:abc, which joins another container's"},
		{"ipc container", `{"image":"x","mem_limit":"1","ipc":"container:abc"}`, "", "sets ipc: container:abc, which joins another container's"},
		{"network service elsewhere", `{"image":"x","mem_limit":"1","network_mode":"service:other"}`, "", "sets network_mode: service:other, which Tender's rules don't allow"},
		{"network bogus", `{"image":"x","mem_limit":"1","network_mode":"macvlan0"}`, "", "sets network_mode: macvlan0, which Tender's rules don't allow"},
		// Keys outside the allowlist are refused, each with a sentence naming it.
		{"device_cgroup_rules", `{"image":"x","mem_limit":"1","device_cgroup_rules":["b 8:* rmw"]}`, "",
			"service web in the compose file sets device_cgroup_rules, which Tender's rules don't allow: remove device_cgroup_rules from web"},
		{"volumes_from", `{"image":"x","mem_limit":"1","volumes_from":["container:tender-caddy-1"]}`, "", "sets volumes_from, which Tender's rules don't allow"},
		{"cgroup host", `{"image":"x","mem_limit":"1","cgroup":"host"}`, "", "sets cgroup, which Tender's rules don't allow"},
		{"cgroup_parent", `{"image":"x","mem_limit":"1","cgroup_parent":"/"}`, "", "sets cgroup_parent, which Tender's rules don't allow"},
		{"runtime", `{"image":"x","mem_limit":"1","runtime":"runc-unsafe"}`, "", "sets runtime, which Tender's rules don't allow"},
		{"gpus", `{"image":"x","mem_limit":"1","gpus":"all"}`, "", "sets gpus, which Tender's rules don't allow"},
		{"label_file", `{"image":"x","mem_limit":"1","label_file":["/etc/x"]}`, "", "sets label_file, which Tender's rules don't allow"},
		{"isolation", `{"image":"x","mem_limit":"1","isolation":"process"}`, "", "sets isolation, which Tender's rules don't allow"},
		{"use_api_socket", `{"image":"x","mem_limit":"1","use_api_socket":true}`, "", "sets use_api_socket, which Tender's rules don't allow"},
		{"provider", `{"image":"x","mem_limit":"1","provider":{"type":"x"}}`, "", "sets provider, which Tender's rules don't allow"},
		{"reserved devices", `{"image":"x","mem_limit":"1","deploy":{"resources":{"reservations":{"devices":[{"capabilities":["gpu"]}]}}}}`, "", "has devices"},
		{"deploy generic resources", `{"image":"x","mem_limit":"1","deploy":{"resources":{"reservations":{"generic_resources":[{}]}}}}`, "",
			"sets deploy.resources.reservations.generic_resources, which Tender's rules don't allow"},
		{"deploy placement", `{"image":"x","mem_limit":"1","deploy":{"placement":{}}}`, "", "sets deploy.placement, which Tender's rules don't allow"},
		{"label disable", `{"image":"x","mem_limit":"1","security_opt":["label=disable"]}`, "", "has security_opt label=disable"},
		{"cap bpf", `{"image":"x","mem_limit":"1","cap_add":["BPF"]}`, "", "adds the capability BPF"},
		{"sysctl kernel", `{"image":"x","mem_limit":"1","sysctls":{"kernel.shmmax":"1"}}`, "", "sets sysctl kernel.shmmax, and Tender allows only net.*"},
		{"sysctl list", `{"image":"x","mem_limit":"1","sysctls":["kernel.msgmax=1"]}`, "", "sets sysctl kernel.msgmax"},
		{"log driver", `{"image":"x","mem_limit":"1","logging":{"driver":"syslog"}}`, "", "logs with driver syslog"},
		{"mount type", `{"image":"x","mem_limit":"1","volumes":[{"type":"npipe","source":"x","target":"/x"}]}`, "", "has a mount of type npipe"},
		{"mount subpath", `{"image":"x","mem_limit":"1","volumes":[{"type":"volume","source":"db","target":"/x","volume":{"subpath":"../.."}}]}`, `"volumes":{"db":{}}`,
			"mounts /x with volume.subpath, which Tender's rules don't allow"},
		{"bind propagation", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/data","target":"/x","bind":{"propagation":"rshared"}}]}`, "",
			"with propagation rshared"},
		{"service network option", `{"image":"x","mem_limit":"1","networks":{"default":{"driver_opts":{"x":"y"}}}}`, "", "joins network default with driver_opts"},
		{"top-level key", `{"image":"x","mem_limit":"1"}`, `"models":{"m":{}}`, "the compose file has models at its top, which Tender's rules don't allow"},
		{"volume key", `{"image":"x","mem_limit":"1"}`, `"volumes":{"db":{"labels":{},"x-ok":1,"weird":1}}`, "volume db sets weird, which Tender's rules don't allow"},
		{"volume name of another", `{"image":"x","mem_limit":"1"}`, `"volumes":{"db":{"name":"blog_db"}}`, "volume db sets name blog_db, which could be another service's volume"},
		{"volume name pattern", `{"image":"x","mem_limit":"1"}`, `"volumes":{"a b":{}}`, `volume "a b" can't be a volume's name`},
		{"volume name slash", `{"image":"x","mem_limit":"1"}`, `"volumes":{"a/../../x":{}}`, `volume "a/../../x" can't be a volume's name`},
		{"network driver", `{"image":"x","mem_limit":"1"}`, `"networks":{"lan":{"driver":"macvlan"}}`, "network lan has driver macvlan"},
		{"network name of another", `{"image":"x","mem_limit":"1"}`, `"networks":{"default":{"name":"host"}}`, "network default sets name host"},
		{"network driver_opts", `{"image":"x","mem_limit":"1"}`, `"networks":{"n":{"driver_opts":{"com.docker.network.bridge.name":"docker0"}}}`,
			"network n sets driver_opts, which Tender's rules don't allow"},
		{"external secret", `{"image":"x","mem_limit":"1"}`, `"secrets":{"key":{"external":true}}`, "secret key is external"},
		{"config key", `{"image":"x","mem_limit":"1"}`, `"configs":{"c":{"file":"` + dir + `/c","template_driver":"x"}}`, "config c sets template_driver"},
		// A bind of the folder itself, or of what Tender writes there, could change the next deploy.
		{"bind folder", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `","target":"/x"}]}`, "",
			"service web in the compose file mounts " + dir + ", which is the service's folder or what Tender keeps in it: mount a folder of its own, such as ./data:/x, or a named volume"},
		{"bind compose.yaml", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/compose.yaml","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"bind override", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/compose.tender.yaml","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"bind .env", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/.env","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"bind volumes", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/volumes","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"bind in volumes", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/volumes/db","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"bind manifest", `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"` + dir + `/.tender-files.json","target":"/x"}]}`, "", "which is the service's folder or what Tender keeps"},
		{"external volume", `{"image":"x","mem_limit":"1"}`, `"volumes":{"db":{"external":true}}`, "volume db is external"},
		{"volume driver", `{"image":"x","mem_limit":"1"}`, `"volumes":{"db":{"driver":"nfs"}}`, "volume db sets its own driver"},
		{"external network", `{"image":"x","mem_limit":"1"}`, `"networks":{"proxy":{"external":true}}`, "network proxy is external"},
		{"secret outside", `{"image":"x","mem_limit":"1"}`, `"secrets":{"key":{"file":"/root/key"}}`, "secret key reads /root/key, outside"},
		{"config outside", `{"image":"x","mem_limit":"1"}`, `"configs":{"nginx":{"file":"/etc/nginx.conf"}}`, "config nginx reads /etc/nginx.conf, outside"},
	} {
		err := CheckRules(rendered(t, c.web, c.rest), dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := ParseRendered([]byte(`{"services":{}}`)); err == nil || !strings.Contains(err.Error(), "has no services") {
		t.Errorf("no services: %v", err)
	}
	if _, err := ParseRendered([]byte(`not json`)); err == nil {
		t.Error("output that isn't JSON was read")
	}
}

// A path compose reads or binds in the folder may not go through a symbolic link, or be anything
// but a plain folder or file; one that isn't there yet is fine.
func TestCheckLinks(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "data", "ok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(d, "data", "x")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(d, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	bind := func(src string) Rendered {
		return rendered(t, `{"image":"x","mem_limit":"1","volumes":[{"type":"bind","source":"`+src+`","target":"/x"}]}`, "")
	}
	for _, ok := range []string{d + "/data/ok", d + "/data/ok/new/deeper", d + "/new"} {
		if err := CheckLinks(bind(ok), d); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for src, want := range map[string]string{
		d + "/data/x":     "service web in the compose file mounts " + d + "/data/x, and " + d + "/data/x in its folder is a symbolic link, which could reach outside the folder",
		d + "/data/x/etc": "and " + d + "/data/x in its folder is a symbolic link",
		d + "/fifo":       "and " + d + "/fifo in its folder isn't a plain folder or file",
	} {
		if err := CheckLinks(bind(src), d); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
	for what, r := range map[string]Rendered{
		"env_file": rendered(t, `{"image":"x","mem_limit":"1","env_file":[{"path":"`+d+`/data/x/etc/shadow"}]}`, ""),
		"secret":   rendered(t, `{"image":"x","mem_limit":"1"}`, `"secrets":{"k":{"file":"`+d+`/data/x/etc/shadow"}}`),
		"config":   rendered(t, `{"image":"x","mem_limit":"1"}`, `"configs":{"c":{"file":"`+d+`/data/x/etc/shadow"}}`),
	} {
		if err := CheckLinks(r, d); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
			t.Errorf("%s through a link: %v", what, err)
		}
	}
}

func TestCheckRouted(t *testing.T) {
	r := rendered(t, `{"image":"x"}`, "")
	if err := CheckRouted(r, []Published{{Container: "web", Port: 80, LocalPort: 20000}}); err != nil {
		t.Error(err)
	}
	err := CheckRouted(r, []Published{{Container: "api", Port: 80, LocalPort: 20001}})
	if err == nil || err.Error() != "a route goes to api, which the compose file has no service for. Its services are web: route to one of them" {
		t.Errorf("a route to no service: %v", err)
	}
}

// The override publishes only the routed ports, on 127.0.0.1; labels every container; sets a
// restart policy only where the file has none; and binds each named volume to the folder.
func TestOverride(t *testing.T) {
	r, err := ParseRendered([]byte(`{"services":{"web":{"image":"x"},"worker":{"image":"y","restart":"on-failure"}},"volumes":{"db":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Override(r, "notes", dir, []Published{{Container: "web", Port: 80, LocalPort: 20000}, {Container: "web", Port: 9090, LocalPort: 20001}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Services map[string]struct {
			Labels  map[string]string `json:"labels"`
			Restart string            `json:"restart"`
			Ports   []string          `json:"ports"`
		} `json:"services"`
		Volumes map[string]struct {
			Driver     string            `json:"driver"`
			DriverOpts map[string]string `json:"driver_opts"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v:\n%s", err, b)
	}
	web, worker := got.Services["web"], got.Services["worker"]
	if web.Labels[Label] != "notes" || worker.Labels[Label] != "notes" {
		t.Errorf("labels = %v, %v", web.Labels, worker.Labels)
	}
	if web.Restart != "unless-stopped" || worker.Restart != "" {
		t.Errorf("restart = %q, %q; want unless-stopped where the file sets none", web.Restart, worker.Restart)
	}
	if strings.Join(web.Ports, ",") != "127.0.0.1:20000:80,127.0.0.1:20001:9090" || worker.Ports != nil {
		t.Errorf("ports = %v, %v", web.Ports, worker.Ports)
	}
	v := got.Volumes["db"]
	if v.Driver != "local" || v.DriverOpts["type"] != "none" || v.DriverOpts["o"] != "bind" || v.DriverOpts["device"] != dir+"/volumes/db" {
		t.Errorf("volume db = %+v", v)
	}
}

func TestImageHost(t *testing.T) {
	for image, want := range map[string]string{
		"nginx": "docker.io", "library/nginx:1": "docker.io", "me/app": "docker.io", "ghcr.io/me/app:1": "ghcr.io",
		"registry.example.com:5000/app": "registry.example.com:5000", "localhost/app": "localhost",
	} {
		if got := imageHost(image); got != want {
			t.Errorf("imageHost(%q) = %q, want %q", image, got, want)
		}
	}
}
