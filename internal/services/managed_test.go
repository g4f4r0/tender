package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// fakeActions queues actions in memory; a test finishes them with finish.
type fakeActions struct {
	mu     sync.Mutex
	list   []fakeAction
	refuse error
}

type fakeAction struct {
	machineID               int64
	typ, service, container string
	deleteData              bool
	status                  string
	output                  *string
}

func (f *fakeActions) Queue(_ context.Context, machineID int64, typ, service, container string, deleteData bool) (Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return Action{}, f.refuse
	}
	f.list = append(f.list, fakeAction{machineID: machineID, typ: typ, service: service, container: container, deleteData: deleteData, status: "pending"})
	id := int64(len(f.list))
	return Action{ID: id, Status: "pending", Action: map[string]any{"id": id, "type": typ}}, nil
}

func (f *fakeActions) Get(_ context.Context, _, id int64) (Action, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id < 1 || int(id) > len(f.list) {
		return Action{}, server.NotFound("Action %d doesn't exist on this machine.", id)
	}
	a := f.list[id-1]
	return Action{ID: id, Status: a.status, Output: a.output, Action: map[string]any{"id": id, "type": a.typ, "status": a.status}}, nil
}

func (f *fakeActions) Await(ctx context.Context, machineID, id int64) (Action, error) {
	return f.Get(ctx, machineID, id)
}

// finish ends the latest action as status, with output.
func (f *fakeActions) finish(status, output string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &f.list[len(f.list)-1]
	a.status = status
	if output != "" {
		a.output = &output
	}
}

func (f *fakeActions) last() fakeAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.list[len(f.list)-1]
}

func managedStore(t *testing.T) (*Store, *fakeActions) {
	t.Helper()
	s := NewStore(newDB(t))
	fa := &fakeActions{}
	s.Key, s.Actions = testKey, fa
	return s, fa
}

const notesCompose = "services:\n  web:\n    image: ghcr.io/me/notes:1\n    mem_limit: 512m\n"

func TestCreateAndReconcile(t *testing.T) {
	s, fa := managedStore(t)
	ctx := context.Background()
	q, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose, Env: map[string]string{"SECRET_KEY": "s3cret"},
		Files:  map[string]string{"config/app.toml": "x = 1\n"},
		Routes: []RouteInput{{Container: "web", Port: 8080, Expose: deploy.ExposeTailscale}, {Container: "web", Port: 9090, Expose: deploy.ExposeTailscale, HTTPSPort: ptr(8443)}}})
	if err != nil {
		t.Fatal(err)
	}
	sv := q.Service
	if !sv.Managed || *sv.DeployStatus != DeployDeploying || sv.Env["SECRET_KEY"] != deploy.Hidden || *sv.ActionID != 1 || q.actionID != 1 ||
		len(sv.Routes) != 2 || sv.Routes[0].LocalPort != 20000 || *sv.Routes[0].HTTPSPort != 10000 || sv.Routes[1].LocalPort != 20001 || sv.Routes[0].URL != nil {
		t.Fatalf("created = %+v, routes %+v", sv, sv.Routes)
	}
	if a := fa.last(); a.typ != ActionDeploy || a.service != "notes" || a.machineID != 1 {
		t.Errorf("queued %+v", a)
	}
	var stored string
	if err := s.db.Read.QueryRow(`SELECT env FROM services WHERE name = 'notes'`).Scan(&stored); err != nil || strings.Contains(stored, "s3cret") {
		t.Errorf("env is stored in the clear: %s, %v", stored, err)
	}
	b, _ := json.Marshal(sv)
	if strings.Contains(string(b), "s3cret") {
		t.Errorf("the API shows the env: %s", b)
	}

	// The machine's tailnet name gives a tailscale route its URL.
	if err := s.SetExposureStatus(ctx, 1, deploy.ExposureStatus{TailscaleDNSName: ptr("apps.tail1234.ts.net.")}); err != nil {
		t.Fatal(err)
	}
	fa.finish("completed", "done")
	got, err := s.Get(ctx, 1, "notes")
	if err != nil || *got.DeployStatus != DeployDeployed || got.DeployedAt == nil || got.LastError != nil ||
		got.Routes[0].URL == nil || *got.Routes[0].URL != "https://apps.tail1234.ts.net:10000" {
		t.Fatalf("after the deploy = %+v, %v", got, err)
	}

	// A failed deploy keeps why, and alerts while the service's alerts are on.
	if _, err := s.Redeploy(ctx, 1, "notes"); err != nil {
		t.Fatal(err)
	}
	fa.finish("failed", "service web in the compose file has no memory limit\nthe rest")
	if got, _ := s.Get(ctx, 1, "notes"); *got.DeployStatus != DeployFailed || *got.LastError != "service web in the compose file has no memory limit\nthe rest" {
		t.Errorf("after a failed deploy = %+v", got)
	}
	if f, err := s.Failures(ctx); err != nil || len(f[1]) != 1 || f[1][0].Service != "notes" {
		t.Errorf("failures = %+v, %v", f, err)
	}
	if _, err := s.SetAlerts(ctx, 1, "notes", false); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Failures(ctx); len(f) != 0 {
		t.Errorf("failures with alerts off = %+v", f)
	}

	// The daemon gets the env opened, the logins and the machine's routes.
	if _, err := s.AddRegistry(ctx, RegistryInput{Name: "ghcr", Host: "ghcr.io", Username: "me", Password: "token"}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Deployment(ctx, 1, "notes")
	if err != nil {
		t.Fatal(err)
	}
	var d deploy.Deployment
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Env["SECRET_KEY"] != "s3cret" || d.Files["config/app.toml"] != "x = 1\n" || len(d.Routes) != 2 || d.Routes[1] != (deploy.Published{Container: "web", Port: 9090, LocalPort: 20001, Expose: deploy.ExposeTailscale}) ||
		len(d.Registries) != 1 || d.Registries[0].Password != "token" || len(d.Exposures.Routes) != 2 || *d.Exposures.Routes[1].HTTPSPort != 8443 {
		t.Errorf("deployment = %+v", d)
	}

	for name, in := range map[string]Input{
		"bad name":       {Name: "My-App", Compose: notesCompose},
		"no compose":     {Name: "blog"},
		"bad file":       {Name: "blog", Compose: notesCompose, Files: map[string]string{"../x": "y"}},
		"hidden env":     {Name: "blog", Compose: notesCompose, Env: map[string]string{"K": "***"}},
		"bad expose":     {Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: "magic"}}},
		"public no host": {Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposePublic}}},
		"tailnet host":   {Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale, Host: ptr("a.b")}}},
		"bad port":       {Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 0, Expose: deploy.ExposeTailscale}}},
		"no cloudflare":  {Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeCloudflare, Host: ptr("blog.example.com")}}},
	} {
		if _, err := s.Create(ctx, 1, in); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422", name, err)
		}
	}
	if _, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose}); status(err) != http.StatusConflict {
		t.Errorf("a second notes: %v", err)
	}
	// Another service can't have a tailnet port notes has.
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale, HTTPSPort: ptr(8443)}}}); status(err) != http.StatusConflict ||
		!strings.Contains(err.Error(), "https_port 8443 is taken by service notes on this machine") {
		t.Errorf("a taken https_port: %v", err)
	}
	if _, err := s.Get(ctx, 1, "blog"); status(err) != http.StatusNotFound {
		t.Errorf("a refused service was kept: %v", err)
	}
	// A deploy that can't be queued leaves nothing behind.
	fa.refuse = server.Invalid("no")
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose}); err == nil {
		t.Error("a deploy that couldn't be queued was created")
	}
	if _, err := s.Get(ctx, 1, "blog"); status(err) != http.StatusNotFound {
		t.Errorf("a service without its deploy was kept: %v", err)
	}
}

// A service that is already running unmanaged keeps its name: Tender can't deploy over it, and
// can't change, redeploy or remove it, but it can start, stop and restart it.
func TestUnmanaged(t *testing.T) {
	s, fa := managedStore(t)
	ctx := context.Background()
	if err := s.Replace(ctx, 1, []ContainerInput{composed("shop", "web", "c1"), composed("shop", "db", "c2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, 1, Input{Name: "shop", Compose: notesCompose}); status(err) != http.StatusConflict ||
		!strings.Contains(err.Error(), "Take its containers down (docker compose -p shop down)") {
		t.Errorf("deploy over an unmanaged service: %v", err)
	}
	want := `Tender didn't deploy service "shop", so it can't change it: it only watches, starts, stops and restarts it.`
	if _, err := s.Update(ctx, 1, "shop", Change{Compose: ptr(notesCompose)}); status(err) != http.StatusConflict || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("change: %v", err)
	}
	if _, err := s.Redeploy(ctx, 1, "shop"); status(err) != http.StatusConflict {
		t.Errorf("redeploy: %v", err)
	}
	if _, err := s.Remove(ctx, 1, "shop", false); status(err) != http.StatusConflict {
		t.Errorf("remove: %v", err)
	}
	if _, err := s.Deployment(ctx, 1, "shop"); status(err) != http.StatusConflict {
		t.Errorf("deployment: %v", err)
	}
	q, err := s.Act(ctx, 1, "shop", actRestart, "db")
	if err != nil || q.Service.DeployStatus != nil {
		t.Fatalf("restart = %+v, %v", q, err)
	}
	if a := fa.last(); a.typ != actRestart || a.service != "shop" || a.container != "db" {
		t.Errorf("queued %+v", a)
	}
	if _, err := s.Act(ctx, 1, "nope", actRestart, ""); status(err) != http.StatusNotFound {
		t.Errorf("an unknown service: %v", err)
	}
	// Once its containers are down, the name is free for a service Tender deploys, without waiting the
	// 7 days the removed containers are kept.
	if err := s.Replace(ctx, 1, []ContainerInput{}); err != nil {
		t.Fatal(err)
	}
	if q, err := s.Create(ctx, 1, Input{Name: "shop", Compose: notesCompose}); err != nil || !q.Service.Managed {
		t.Fatalf("deploy over an unmanaged service that is gone = %+v, %v", q, err)
	}
	if sv, err := s.row(ctx, 1, "shop"); err != nil || !sv.Managed || len(sv.Containers) != 0 {
		t.Errorf("the service = %+v, %v", sv, err)
	}
}

// A change merges files and env into the stored ones and keeps each route's ports; a remove
// deletes the service once the daemon is done.
func TestUpdateAndRemove(t *testing.T) {
	s, fa := managedStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose, Env: map[string]string{"A": "1", "B": "2"},
		Files: map[string]string{"a.txt": "a", "b.txt": "b"}, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}}); err != nil {
		t.Fatal(err)
	}
	fa.finish("completed", "")
	routes := []RouteInput{{Container: "api", Port: 9000, Expose: deploy.ExposePublic, Host: ptr("api.example.com")},
		{Container: "web", Port: 80, Expose: deploy.ExposeTailscale}}
	q, err := s.Update(ctx, 1, "notes", Change{Env: map[string]*string{"A": ptr(deploy.Hidden), "B": nil, "C": ptr("3")},
		Files: map[string]*string{"a.txt": nil, "c.txt": ptr("c")}, Routes: &routes})
	if err != nil {
		t.Fatal(err)
	}
	sv := q.Service
	if strings.Join(SortedKeysOf(sv.Env), ",") != "A,C" || strings.Join(SortedKeysOf(sv.Files), ",") != "b.txt,c.txt" || *sv.DeployStatus != DeployDeploying {
		t.Errorf("after the change = %+v", sv)
	}
	if len(sv.Routes) != 2 || sv.Routes[1].Container != "web" || sv.Routes[1].LocalPort != 20000 || *sv.Routes[1].HTTPSPort != 10000 ||
		sv.Routes[0].LocalPort != 20001 || *sv.Routes[0].URL != "https://api.example.com" {
		t.Errorf("routes = %+v", sv.Routes)
	}
	raw, _ := s.Deployment(ctx, 1, "notes")
	var d deploy.Deployment
	_ = json.Unmarshal(raw, &d)
	if d.Env["A"] != "1" || d.Env["C"] != "3" || len(d.Env) != 2 {
		t.Errorf("env after the change = %v", d.Env)
	}
	if _, err := s.Update(ctx, 1, "notes", Change{Env: map[string]*string{"D": ptr(deploy.Hidden)}}); status(err) != http.StatusUnprocessableEntity {
		t.Errorf("*** for a key the service lacks: %v", err)
	}
	// Another service can't take the host.
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Routes: []RouteInput{{Container: "web", Port: 80, Expose: deploy.ExposePublic, Host: ptr("api.example.com")}}}); status(err) != http.StatusConflict {
		t.Errorf("a taken host: %v", err)
	}

	q, err = s.Remove(ctx, 1, "notes", true)
	if err != nil || *q.Service.DeployStatus != DeployRemoving {
		t.Fatalf("remove = %+v, %v", q, err)
	}
	if a := fa.last(); a.typ != ActionRemove || !a.deleteData {
		t.Errorf("queued %+v", a)
	}
	if ex, _ := s.Exposures(ctx, 1); len(ex.Routes) != 0 {
		t.Errorf("a service being removed still has its routes exposed: %+v", ex.Routes)
	}
	if _, err := s.Update(ctx, 1, "notes", Change{Compose: ptr(notesCompose)}); status(err) != http.StatusConflict {
		t.Errorf("a change while removing: %v", err)
	}
	fa.finish("completed", "Deleted the service's folder and its data.")
	if _, err := s.Get(ctx, 1, "notes"); status(err) != http.StatusNotFound {
		t.Errorf("after the remove: %v", err)
	}
}

func SortedKeysOf(m map[string]string) []string { return deploy.SortedKeys(m) }

func TestNoKey(t *testing.T) {
	s, _ := managedStore(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose, Env: map[string]string{"A": "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckKey(ctx); err != nil {
		t.Errorf("the right key: %v", err)
	}
	other := NewStore(s.db)
	other.Key = []byte("ffffffffffffffffffffffffffffffff")
	if err := other.CheckKey(ctx); err == nil {
		t.Error("another key opened the env")
	}
	other.Key, other.Actions = nil, s.Actions
	if _, err := other.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Env: map[string]string{"A": "1"}}); status(err) != http.StatusServiceUnavailable {
		t.Errorf("env without a key: %v", err)
	}
}

func TestLastError(t *testing.T) {
	long := "why\n" + strings.Repeat("compose line\n", 1000)
	got := lastError(&long)
	if !strings.HasPrefix(got, "why\n...\ncompose line\n") || len(got) > maxErrorTail+20 {
		t.Errorf("lastError of a long output = %d bytes, starting %q", len(got), got[:30])
	}
	if lastError(nil) != "The action failed without saying why." {
		t.Error("no output")
	}
}

func TestRegistries(t *testing.T) {
	s, _ := managedStore(t)
	ctx := context.Background()
	g, err := s.AddRegistry(ctx, RegistryInput{Name: "ghcr", Host: "ghcr.io", Username: "me", Password: "token"})
	if err != nil || g.Password != deploy.Hidden || !g.PasswordSet {
		t.Fatalf("add = %+v, %v", g, err)
	}
	for name, in := range map[string]RegistryInput{
		"bad name":    {Name: "GH CR", Host: "x.io", Username: "u", Password: "p"},
		"bad host":    {Name: "x", Host: "https://x.io", Username: "u", Password: "p"},
		"no user":     {Name: "x", Host: "x.io", Password: "p"},
		"no password": {Name: "x", Host: "x.io", Username: "u"},
		"hidden":      {Name: "x", Host: "x.io", Username: "u", Password: deploy.Hidden},
	} {
		if _, err := s.AddRegistry(ctx, in); status(err) != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.AddRegistry(ctx, RegistryInput{Name: "ghcr", Host: "x.io", Username: "u", Password: "p"}); status(err) != http.StatusConflict {
		t.Errorf("a second ghcr: %v", err)
	}
	if _, err := s.AddRegistry(ctx, RegistryInput{Name: "other", Host: "ghcr.io", Username: "u", Password: "p"}); status(err) != http.StatusConflict {
		t.Errorf("a second login for ghcr.io: %v", err)
	}
	if _, err := s.UpdateRegistry(ctx, "ghcr", RegistryPatch{Password: ptr("new")}); err != nil {
		t.Fatal(err)
	}
	logins, err := s.logins(ctx)
	if err != nil || len(logins) != 1 || logins[0] != (deploy.Login{Host: "ghcr.io", Username: "me", Password: "new"}) {
		t.Errorf("logins = %+v, %v", logins, err)
	}
	list, _ := s.Registries(ctx)
	if b, _ := json.Marshal(list); strings.Contains(string(b), "new") || len(list) != 1 {
		t.Errorf("the list shows the password: %s", b)
	}
	if err := s.DeleteRegistry(ctx, "ghcr"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRegistry(ctx, "ghcr"); status(err) != http.StatusNotFound {
		t.Errorf("a second delete: %v", err)
	}
}

func TestName(t *testing.T) {
	two := 2
	list := []ContainerInput{
		running("redis", "r1"),
		composed("shop", "web", "c1"),
		{Name: "shop-web-2", ContainerID: "c2", Project: ptr("shop"), ComposeService: ptr("web"), ContainerNumber: &two},
		{Name: "shop-worker-1", ContainerID: "c3", Project: ptr("shop")},
		{Name: "odd", ContainerID: "c4", Project: ptr("shop")},
		{Name: "a-shop-web-1", ContainerID: "aaaaaaaaaaaaaaaa", Project: ptr("shop"), ComposeService: ptr("web")},
	}
	got := Name(list)
	// Two that would both be web are told apart in the order of their container names: a-shop-web-1 first.
	want := []Named{{"redis", "redis"}, {"shop", "web-c1"}, {"shop", "web-2"}, {"shop", "worker"}, {"shop", "odd"}, {"shop", "web"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Name = %v, want %v", got, want)
	}
}

// A new route gets the next local port after the last one the machine got, so a port a route let
// go of isn't handed out again right away: a failed deploy can leave the daemon's exposures
// pointing at it. After 29999 the next route takes the lowest free port.
func TestLocalPortsAreNotReusedRightAway(t *testing.T) {
	s, _ := managedStore(t)
	ctx := context.Background()
	route := func(port int) []RouteInput {
		return []RouteInput{{Container: "web", Port: port, Expose: deploy.ExposeTailscale}}
	}
	local := func(name string) int {
		t.Helper()
		sv, err := s.row(ctx, 1, name)
		if err != nil || len(sv.Routes) != 1 {
			t.Fatalf("%s: %+v, %v", name, sv.Routes, err)
		}
		return sv.Routes[0].LocalPort
	}
	if _, err := s.Create(ctx, 1, Input{Name: "notes", Compose: notesCompose, Routes: route(80)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, 1, "notes", Change{Routes: ptr(route(81))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, 1, Input{Name: "blog", Compose: notesCompose, Routes: route(80)}); err != nil {
		t.Fatal(err)
	}
	if n, b := local("notes"), local("blog"); n != 20001 || b != 20002 {
		t.Errorf("notes has %d and blog %d; want 20001 and 20002, with 20000, which notes let go of, left alone", n, b)
	}
	if _, err := s.db.Write.Exec(`UPDATE service_exposures SET last_local_port = 29999 WHERE machine_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, 1, Input{Name: "shop", Compose: notesCompose, Routes: route(80)}); err != nil {
		t.Fatal(err)
	}
	if p := local("shop"); p != 20000 {
		t.Errorf("after 29999 shop has %d; want the lowest free, 20000", p)
	}
	if _, err := s.Create(ctx, 1, Input{Name: "wiki", Compose: notesCompose, Routes: route(80)}); err != nil {
		t.Fatal(err)
	}
	if p := local("wiki"); p != 20003 {
		t.Errorf("wiki has %d; want 20003, the next free after shop's", p)
	}
}

// tender, the project of Tender's own containers, shows as a service Tender didn't deploy once
// Caddy runs; changing or redeploying it says the name is Tender's, not that Tender didn't deploy it.
func TestTheTenderProjectCantBeDeployed(t *testing.T) {
	s, _ := managedStore(t)
	ctx := context.Background()
	if _, err := s.db.Write.Exec(`INSERT INTO services (machine_id, name, created_at, updated_at) VALUES (1, 'tender', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	compose := notesCompose
	if _, err := s.Update(ctx, 1, "tender", Change{Compose: &compose}); err == nil || !strings.Contains(err.Error(), "the compose project of Tender") {
		t.Errorf("update: %v", err)
	}
	if _, err := s.Redeploy(ctx, 1, "tender"); err == nil || !strings.Contains(err.Error(), "the compose project of Tender") {
		t.Errorf("redeploy: %v", err)
	}
}
