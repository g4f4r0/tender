package deploy

import (
	"encoding/json"
	"strings"
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
		{"uts host", `{"image":"x","mem_limit":"1","uts":"host"}`, "", "sets uts: host"},
		{"userns host", `{"image":"x","mem_limit":"1","userns_mode":"host"}`, "", "sets userns_mode: host"},
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
		{"zero memory limit", `{"image":"x","mem_limit":"0"}`, "", "has no memory limit"},
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
