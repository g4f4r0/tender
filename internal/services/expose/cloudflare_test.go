package expose

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAccount  = "0123456789abcdef0123456789abcdef"
	testAPIToken = "cf-api-token-marker-0123456789abcdefghij"
)

// fakeCloudflare is an in-memory Cloudflare API: tunnels, their ingress and tokens, zones and DNS
// records, with listings of at most two items a page, so every listing takes several pages.
type fakeCloudflare struct {
	mu      sync.Mutex
	tunnels []fakeTunnel
	ingress map[string][]ingressRule
	config  map[string]string // by tunnel id, the body of the latest PUT of its configuration
	apps    []accessApp       // the account's Access applications
	team    string            // the Access organization's auth_domain
	zones   []zone
	records map[string][]dnsRecord // by zone id
	calls   []string               // "METHOD path", in order
	fail    map[string][]int       // "METHOD path" to the statuses its next calls answer
	echo    string                 // put in a 400's error message, to see it redacted
	loose   bool                   // lists every tunnel, whatever the name asked for
	next    int
}

type fakeTunnel struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	DeletedAt *string `json:"deleted_at"`
}

func newFakeCloudflare(t *testing.T) (*fakeCloudflare, *Cloudflare) {
	f := &fakeCloudflare{ingress: map[string][]ingressRule{}, config: map[string]string{}, team: "myteam.cloudflareaccess.com", records: map[string][]dnsRecord{}, fail: map[string][]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, &Cloudflare{Base: srv.URL, AccountID: testAccount, Token: testAPIToken, Backoff: time.Millisecond}
}

func (f *fakeCloudflare) id() string {
	f.next++
	return fmt.Sprintf("%032x", f.next)
}

func (f *fakeCloudflare) addZone(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	z := zone{ID: f.id(), Name: name}
	f.zones = append(f.zones, z)
	return z.ID
}

func (f *fakeCloudflare) addRecord(zoneID string, r dnsRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.ID = f.id()
	f.records[zoneID] = append(f.records[zoneID], r)
}

func (f *fakeCloudflare) addTunnel(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("%08x-0000-4000-8000-%012x", f.next+1, f.next+1)
	f.next++
	f.tunnels = append(f.tunnels, fakeTunnel{ID: id, Name: name})
	return id
}

func (f *fakeCloudflare) reply(w http.ResponseWriter, status int, result any, info any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"success": status < 300, "errors": []any{}, "messages": []any{}, "result": result}
	if info != nil {
		body["result_info"] = info
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeCloudflare) refuse(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": code, "message": msg}}, "result": nil})
}

// page answers one page of items, two a page.
func page[T any](f *fakeCloudflare, w http.ResponseWriter, r *http.Request, items []T) {
	const per = 2
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	n = max(n, 1)
	total := max((len(items)+per-1)/per, 1)
	lo, hi := min((n-1)*per, len(items)), min(n*per, len(items))
	f.reply(w, 200, append([]T{}, items[lo:hi]...), map[string]int{"page": n, "per_page": per, "total_pages": total, "count": hi - lo, "total_count": len(items)})
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	if st := f.fail[call]; len(st) > 0 {
		f.fail[call] = st[1:]
		if st[0] == 400 && f.echo != "" {
			f.refuse(w, 400, 1001, "bad request with "+f.echo)
			return
		}
		f.refuse(w, st[0], 1000+st[0], http.StatusText(st[0]))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testAPIToken {
		f.refuse(w, 403, 10000, "Authentication error")
		return
	}
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	acct := "/accounts/" + testAccount + "/cfd_tunnel"
	var body map[string]any
	var raw []byte
	if r.Body != nil {
		raw, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/accounts/") && !strings.HasPrefix(r.URL.Path, "/accounts/"+testAccount+"/"):
		f.refuse(w, 404, 7003, "Could not route to "+r.URL.Path+", perhaps your object identifier is invalid?")
	case call == "GET "+acct:
		var out []fakeTunnel
		for _, t := range f.tunnels {
			if (f.loose || q.Get("name") == "" || t.Name == q.Get("name")) && (q.Get("is_deleted") != "false" || t.DeletedAt == nil) {
				out = append(out, t)
			}
		}
		page(f, w, r, out)
	case call == "POST "+acct:
		if body["config_src"] != "cloudflare" {
			f.refuse(w, 400, 1001, "config_src must be cloudflare")
			return
		}
		id := fmt.Sprintf("%08x-0000-4000-8000-%012x", f.next+1, f.next+1)
		f.next++
		t := fakeTunnel{ID: id, Name: body["name"].(string)}
		f.tunnels = append(f.tunnels, t)
		f.reply(w, 200, t, nil)
	case r.Method == "PUT" && len(parts) == 5 && parts[4] == "configurations":
		var in struct {
			Config struct {
				Ingress []ingressRule `json:"ingress"`
			} `json:"config"`
		}
		b, _ := json.Marshal(body)
		_ = json.Unmarshal(b, &in)
		f.ingress[parts[3]] = in.Config.Ingress
		f.config[parts[3]] = string(raw)
		f.reply(w, 200, map[string]any{"tunnel_id": parts[3]}, nil)
	case r.Method == "GET" && len(parts) == 5 && parts[4] == "token":
		f.reply(w, 200, "token-of-"+parts[3], nil)
	case call == "GET /accounts/"+testAccount+"/access/apps":
		page(f, w, r, f.apps)
	case call == "GET /accounts/"+testAccount+"/access/organizations":
		f.reply(w, 200, map[string]any{"name": "My team", "auth_domain": f.team}, nil)
	case call == "GET /zones":
		if q.Get("account.id") != testAccount {
			f.refuse(w, 400, 1001, "account.id is wrong")
			return
		}
		page(f, w, r, f.zones)
	case len(parts) >= 3 && parts[0] == "zones" && parts[2] == "dns_records":
		f.dns(w, r, parts, body)
	default:
		f.refuse(w, 404, 7000, "No route for that URI")
	}
}

func (f *fakeCloudflare) dns(w http.ResponseWriter, r *http.Request, parts []string, body map[string]any) {
	zoneID, q := parts[1], r.URL.Query()
	if !slices.ContainsFunc(f.zones, func(z zone) bool { return z.ID == zoneID }) {
		f.refuse(w, 404, 7003, "no such zone")
		return
	}
	in := func() dnsRecord {
		b, _ := json.Marshal(body)
		var rec dnsRecord
		_ = json.Unmarshal(b, &rec)
		return rec
	}
	recs := f.records[zoneID]
	switch {
	case r.Method == "GET" && len(parts) == 3:
		var out []dnsRecord
		for _, rec := range recs {
			if (q.Get("type") == "" || rec.Type == q.Get("type")) && (q.Get("name.exact") == "" || rec.Name == q.Get("name.exact")) &&
				(q.Get("comment.exact") == "" || (rec.Comment != nil && *rec.Comment == q.Get("comment.exact"))) {
				out = append(out, rec)
			}
		}
		page(f, w, r, out)
	case r.Method == "POST" && len(parts) == 3:
		rec := in()
		rec.ID = f.id()
		f.records[zoneID] = append(recs, rec)
		f.reply(w, 200, rec, nil)
	case len(parts) == 4:
		i := slices.IndexFunc(recs, func(rec dnsRecord) bool { return rec.ID == parts[3] })
		if i < 0 {
			f.refuse(w, 404, 81044, "Record does not exist.")
			return
		}
		if r.Method == "DELETE" {
			f.records[zoneID] = slices.Delete(recs, i, i+1)
			f.reply(w, 200, map[string]string{"id": parts[3]}, nil)
			return
		}
		rec := in()
		rec.ID = parts[3]
		recs[i] = rec
		f.reply(w, 200, rec, nil)
	default:
		f.refuse(w, 405, 10405, "Method not allowed")
	}
}

func (f *fakeCloudflare) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeCloudflare) exactly(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeCloudflare) zoneRecords(zoneID string) []dnsRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records[zoneID])
}

func ptrTo(s string) *string { return &s }

// The first apply creates the tunnel tender-<machine>, sets its ingress to the routes and a 404,
// and makes a proxied CNAME, commented, in each host's longest matching zone. Applying again
// creates nothing twice. Every listing reads all its pages.
func TestCloudflareAppliesRoutes(t *testing.T) {
	f, c := newFakeCloudflare(t)
	for _, z := range []string{"a.test", "b.test", "c.test", "example.com"} {
		f.addZone(z)
	}
	shop := f.addZone("shop.example.com") // the last page of zones
	example := f.zones[3].ID
	// Were the name filter loose, tender-apps would be on the second page.
	f.loose = true
	for _, name := range []string{"other1", "other2", "tender-db"} {
		f.addTunnel(name)
	}
	tun, err := c.Apply(t.Context(), "apps", []Route{web, api})
	if err != nil {
		t.Fatal(err)
	}
	if tun.Name != "tender-apps" || tun.Token != "token-of-"+tun.ID || f.count("POST /accounts/") != 1 {
		t.Fatalf("tunnel %+v, creates %d", tun, f.count("POST /accounts/"))
	}
	wantIngress := []ingressRule{{Hostname: "api.shop.example.com", Service: "http://127.0.0.1:20002"},
		{Hostname: "blog.example.com", Service: "http://127.0.0.1:20001"}, {Service: "http_status:404"}}
	if got := f.ingress[tun.ID]; !slices.Equal(got, wantIngress) {
		t.Errorf("ingress %+v", got)
	}
	check := func(zoneID, host string) {
		t.Helper()
		recs := f.zoneRecords(zoneID)
		if len(recs) != 1 || recs[0].Name != host || recs[0].Type != "CNAME" || recs[0].Content != tun.ID+".cfargotunnel.com" ||
			!recs[0].Proxied || recs[0].TTL != 1 || recs[0].Comment == nil || *recs[0].Comment != DNSComment {
			t.Errorf("records of %s: %+v", host, recs)
		}
	}
	check(shop, "api.shop.example.com")
	check(example, "blog.example.com")

	// The tunnel is found on its page; nothing is created or changed again.
	f.calls = nil
	again, err := c.Apply(t.Context(), "apps", []Route{web, api})
	if err != nil || again.ID != tun.ID {
		t.Fatalf("again: %v %+v", err, again)
	}
	if n := f.count("POST ") + f.count("DELETE ") + f.count("PUT /zones/"); n != 0 {
		t.Errorf("a second apply made %d changes: %q", n, f.calls)
	}
	if n := f.exactly("GET /zones"); n != 3 {
		t.Errorf("5 zones were read in %d pages, want 3", n)
	}
	if n := f.exactly("GET /accounts/" + testAccount + "/cfd_tunnel"); n != 2 {
		t.Errorf("4 tunnels were read in %d pages, want 2", n)
	}
}

// Taking a route away deletes its record, and only records of Tender's that point at this
// machine's tunnel: the owner's records and other machines' stay. A host with a record Tender
// didn't make fails, and is left alone. A record of Tender's that points at another machine's
// tunnel moves to this one.
func TestCloudflareDeletesOnlyItsOwnRecords(t *testing.T) {
	f, c := newFakeCloudflare(t)
	example := f.addZone("example.com")
	tun, err := c.Apply(t.Context(), "apps", []Route{web, {Service: "x", Container: "y", LocalPort: 20003, Host: "gone.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	dbTunnel := f.addTunnel("tender-db")
	f.addRecord(example, dnsRecord{Type: "CNAME", Name: "db.example.com", Content: dbTunnel + ".cfargotunnel.com", Proxied: true, TTL: 1, Comment: ptrTo(DNSComment)})
	f.addRecord(example, dnsRecord{Type: "CNAME", Name: "mine.example.com", Content: tun.ID + ".cfargotunnel.com", TTL: 1, Comment: ptrTo("made by hand")})
	f.addRecord(example, dnsRecord{Type: "A", Name: "www.example.com", Content: "192.0.2.1", TTL: 1})
	f.addRecord(example, dnsRecord{Type: "CNAME", Name: "moved.example.com", Content: dbTunnel + ".cfargotunnel.com", TTL: 1, Comment: ptrTo(DNSComment)})

	moved := Route{Service: "m", Container: "s", LocalPort: 20004, Host: "moved.example.com"}
	if _, err := c.Apply(t.Context(), "apps", []Route{web, moved}); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, r := range f.zoneRecords(example) {
		names[r.Name] = r.Content
	}
	if _, ok := names["gone.example.com"]; ok {
		t.Error("the route's record stays after the route went")
	}
	for _, n := range []string{"blog.example.com", "db.example.com", "mine.example.com", "www.example.com"} {
		if _, ok := names[n]; !ok {
			t.Errorf("%s was deleted", n)
		}
	}
	if names["moved.example.com"] != tun.ID+".cfargotunnel.com" || names["db.example.com"] != dbTunnel+".cfargotunnel.com" {
		t.Errorf("records %v", names)
	}

	_, err = c.Apply(t.Context(), "apps", []Route{web, {Service: "w", Container: "s", LocalPort: 20005, Host: "www.example.com"}})
	if err == nil || !strings.Contains(err.Error(), "www.example.com already has a DNS record of type A in Cloudflare that Tender didn't make") {
		t.Errorf("a foreign record: %v", err)
	}
	for _, r := range f.zoneRecords(example) {
		if r.Name == "www.example.com" && (r.Type != "A" || r.Content != "192.0.2.1") {
			t.Errorf("the owner's record changed: %+v", r)
		}
	}

	// With no routes, the records of this tunnel go, its ingress is the 404 alone, and it gives no token.
	empty, err := c.Apply(t.Context(), "apps", nil)
	if err != nil || empty.Token != "" || empty.ID != tun.ID {
		t.Fatalf("no routes: %v %+v", err, empty)
	}
	if got := f.ingress[tun.ID]; !slices.Equal(got, []ingressRule{{Service: "http_status:404"}}) {
		t.Errorf("ingress %+v", got)
	}
	for _, r := range f.zoneRecords(example) {
		if strings.HasPrefix(r.Content, tun.ID) && r.Comment != nil && *r.Comment == DNSComment {
			t.Errorf("record %s stays", r.Name)
		}
	}
}

// A machine with no routes and no tunnel gets none; a host in no zone fails before the tunnel exists.
func TestCloudflareMakesNoTunnelItDoesntNeed(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	if tun, err := c.Apply(t.Context(), "apps", nil); err != nil || tun.ID != "" || f.count("POST ") != 0 {
		t.Errorf("no routes: %v %+v %q", err, tun, f.calls)
	}
	_, err := c.Apply(t.Context(), "apps", []Route{{Service: "a", Container: "s", LocalPort: 20001, Host: "app.example.org"}})
	if err == nil || !strings.Contains(err.Error(), "no zone of the Cloudflare account holds app.example.org") || f.count("POST ") != 0 {
		t.Errorf("host in no zone: %v %q", err, f.calls)
	}
	if _, err := c.Apply(t.Context(), "apps", []Route{{Service: "a", Container: "s", LocalPort: 20001, Host: "notexample.com"}}); err == nil {
		t.Error("notexample.com matched the zone example.com")
	}
}

// A 4xx fails at once, with Cloudflare's reason, the permissions to check on 401 and 403, and no
// try again; a 5xx or 429 is tried again, and fails as retryable when it lasts.
func TestCloudflareTellsRefusalsFromOutages(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	list := "GET /accounts/" + testAccount + "/cfd_tunnel"

	f.fail[list] = []int{403}
	err := c.Verify(t.Context())
	var ce *CloudflareError
	if !errors.As(err, &ce) || ce.Retryable || ce.Status != 403 || f.count(list) != 1 ||
		!strings.Contains(err.Error(), "403 Forbidden, Forbidden (1403)") || !strings.Contains(err.Error(), CloudflarePermissions) {
		t.Errorf("403: %v (%d calls)", err, f.count(list))
	}

	f.calls = nil
	f.fail[list] = []int{503, 502, 500}
	err = c.Verify(t.Context())
	if !errors.As(err, &ce) || !ce.Retryable || ce.Status != 500 || f.count(list) != 3 || !strings.Contains(err.Error(), "Try again later") {
		t.Errorf("lasting 5xx: %v (%d calls)", err, f.count(list))
	}

	f.calls = nil
	f.fail[list] = []int{429, 503}
	if err := c.Verify(t.Context()); err != nil || f.count(list) != 3 {
		t.Errorf("a 5xx that passes: %v (%d calls)", err, f.count(list))
	}

	f.calls = nil
	c.Retries = -1
	f.fail[list] = []int{503}
	if err := c.Verify(t.Context()); err == nil || f.count(list) != 1 {
		t.Errorf("no retries: %v (%d calls)", err, f.count(list))
	}

	wrong := *c
	wrong.Token = "a-token-cloudflare-doesnt-know"
	if err := wrong.Verify(t.Context()); !errors.As(err, &ce) || ce.Status != 403 || !strings.Contains(err.Error(), "Authentication error (10000)") {
		t.Errorf("wrong token: %v", err)
	}
	other := *c
	other.AccountID = strings.Repeat("f", 32)
	if err := other.Verify(t.Context()); !errors.As(err, &ce) || ce.Status != 404 {
		t.Errorf("wrong account: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>not json</html>")) }))
	defer srv.Close()
	odd := Cloudflare{Base: srv.URL, AccountID: testAccount, Token: testAPIToken, Retries: -1}
	if err := odd.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "not what Tender expects") {
		t.Errorf("not JSON: %v", err)
	}
	gone := Cloudflare{Base: "http://127.0.0.1:1", AccountID: testAccount, Token: testAPIToken, Retries: -1}
	if err := gone.Verify(t.Context()); !errors.As(err, &ce) || !ce.Retryable || !strings.Contains(err.Error(), "could not reach the Cloudflare API") {
		t.Errorf("unreachable: %v", err)
	}
}

// Neither the API token nor a tunnel token is ever in an error or in a tunnel's JSON, even when
// Cloudflare puts one in its answer.
func TestCloudflareNeverShowsAToken(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	tun, err := c.Apply(t.Context(), "apps", []Route{web})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(tun)
	if strings.Contains(string(b), tun.Token) || strings.Contains(string(b), "token") {
		t.Errorf("a tunnel's JSON: %s", b)
	}
	f.echo = testAPIToken
	f.fail["PUT /accounts/"+testAccount+"/cfd_tunnel/"+tun.ID+"/configurations"] = []int{400}
	_, err = c.Apply(t.Context(), "apps", []Route{web})
	if err == nil || strings.Contains(err.Error(), testAPIToken) || !strings.Contains(err.Error(), "bad request with ***") {
		t.Errorf("an answer with the token: %v", err)
	}
	f.fail["GET /accounts/"+testAccount+"/cfd_tunnel/"+tun.ID+"/token"] = []int{500, 500, 500}
	_, err = c.Apply(t.Context(), "apps", []Route{web})
	if err == nil || strings.Contains(err.Error(), "token-of-") {
		t.Errorf("a failed token: %v", err)
	}
}

func TestCloudflareBase(t *testing.T) {
	if got := CloudflareBase(func(string) string { return "" }); got != CloudflareAPI {
		t.Errorf("default %q", got)
	}
	if got := CloudflareBase(func(k string) string {
		if k == EnvTestCloudflare {
			return "http://127.0.0.1:25091/client/v4/"
		}
		return ""
	}); got != "http://127.0.0.1:25091/client/v4" {
		t.Errorf("test base %q", got)
	}
	for _, id := range []string{testAccount} {
		if err := CheckAccountID(id); err != nil {
			t.Error(err)
		}
	}
	for _, id := range []string{"", "0123", strings.ToUpper(testAccount), testAccount + "0", "../../zones/0123456789abcdef01"} {
		if CheckAccountID(id) == nil {
			t.Errorf("%q passed", id)
		}
	}
}

// One route in no zone, one whose host has a record Tender didn't make, two that share a host,
// and one whose record Cloudflare refuses each fail on their own, with a RouteError that names it; the good route is
// in the ingress, has its record and gets the token, and the owner's record stays as it was.
func TestCloudflareSetsTheGoodRoutesBesideBadOnes(t *testing.T) {
	f, c := newFakeCloudflare(t)
	example := f.addZone("example.com")
	f.addRecord(example, dnsRecord{Type: "A", Name: "www.example.com", Content: "192.0.2.1", TTL: 1})
	routes := []Route{web,
		{Service: "org", Container: "web", LocalPort: 20003, Host: "app.example.org"},
		{Service: "www", Container: "web", LocalPort: 20004, Host: "www.example.com"},
		{Service: "a", Container: "one", LocalPort: 20005, Host: "same.example.com"},
		{Service: "b", Container: "two", LocalPort: 20006, Host: "same.example.com"},
		{Service: "refused", Container: "web", LocalPort: 20007, Host: "refused.example.com"},
	}
	tun, err := c.Apply(t.Context(), "apps", routes[:5])
	if got := routeErrors(t, err); !slices.Equal(got, []string{"a/one", "b/two", "org/web", "www/web"}) {
		t.Errorf("route errors %q: %v", got, err)
	}
	for _, want := range []string{"no zone of the Cloudflare account holds app.example.org, so the route to web in org can't have it. Add its domain",
		"www.example.com already has a DNS record of type A in Cloudflare that Tender didn't make, so the route to web in www can't have it. Delete that record"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	if tun.Token != "token-of-"+tun.ID {
		t.Errorf("the good route got no token: %+v", tun)
	}
	if got := f.ingress[tun.ID]; !slices.Equal(got, []ingressRule{{Hostname: "blog.example.com", Service: "http://127.0.0.1:20001"}, {Service: "http_status:404"}}) {
		t.Errorf("ingress %+v", got)
	}
	names := map[string]dnsRecord{}
	for _, r := range f.zoneRecords(example) {
		names[r.Name] = r
	}
	if names["blog.example.com"].Content != tun.ID+".cfargotunnel.com" || names["www.example.com"].Type != "A" || names["www.example.com"].Content != "192.0.2.1" || len(names) != 2 {
		t.Errorf("records %+v", names)
	}

	// A record Cloudflare refuses with a 4xx fails that route only.
	f.fail["POST /zones/"+example+"/dns_records"] = []int{400}
	tun, err = c.Apply(t.Context(), "apps", []Route{web, routes[5]})
	if got := routeErrors(t, err); !slices.Equal(got, []string{"refused/web"}) || !strings.Contains(err.Error(), "so the route to web in refused can't have it") {
		t.Errorf("a refused record: %q %v", got, err)
	}
	if got := f.ingress[tun.ID]; len(got) != 2 || got[0].Hostname != "blog.example.com" || tun.Token == "" {
		t.Errorf("ingress %+v, tunnel %+v", got, tun)
	}

	// A 5xx that lasts is Cloudflare failing as a whole: a plain error, and no tunnel.
	f.fail["GET /zones"] = []int{503, 503, 503}
	tun, err = c.Apply(t.Context(), "apps", []Route{web})
	var re *RouteError
	if err == nil || errors.As(err, &re) || tun.ID != "" {
		t.Errorf("Cloudflare down: %v %+v", err, tun)
	}
}

// A host whose route fails keeps its record of Tender's: the delete pass takes only the records of
// hosts no route wants.
func TestCloudflareKeepsTheRecordOfAFailingRoute(t *testing.T) {
	f, c := newFakeCloudflare(t)
	example := f.addZone("example.com")
	tun, err := c.Apply(t.Context(), "apps", []Route{web, api2})
	if err != nil {
		t.Fatal(err)
	}
	// blog.example.com is now the host of two routes, so both fail, and its record stays.
	clash := Route{Service: "other", Container: "web", LocalPort: 20008, Host: web.Host}
	_, err = c.Apply(t.Context(), "apps", []Route{web, clash, api2})
	if got := routeErrors(t, err); !slices.Equal(got, []string{"blog/web", "other/web"}) {
		t.Errorf("route errors %q", got)
	}
	var hosts []string
	for _, r := range f.zoneRecords(example) {
		hosts = append(hosts, r.Name)
	}
	slices.Sort(hosts)
	if !slices.Equal(hosts, []string{"api.example.com", "blog.example.com"}) {
		t.Errorf("records %q", hosts)
	}
	if got := f.ingress[tun.ID]; len(got) != 2 || got[0].Hostname != "api.example.com" {
		t.Errorf("ingress %+v", got)
	}
}

var api2 = Route{Service: "shop", Container: "api", LocalPort: 20002, Host: "api.example.com"}

var gate = Route{Service: "zeron-mcp", Container: "gateway", LocalPort: 20010, Host: "gate.example.com", Access: true}

// accessCalls counts the calls to the account's Access endpoints.
func (f *fakeCloudflare) accessCalls() int {
	return f.count("GET /accounts/" + testAccount + "/access/")
}

// A route with access gets an ingress rule that requires Cloudflare Access, with the team name and
// the aud of every application whose domain or public destination is its host, in any case, with a
// trailing dot or a path; a private destination doesn't count. The route without access keeps its
// plain rule. Every page of the applications is read.
func TestCloudflareRequiresAccess(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	f.apps = []accessApp{
		{AUD: "aud-1", Domain: "Gate.Example.com./admin"},
		{AUD: "aud-other", Domain: "other.example.com", Destinations: []accessDestination{{Type: "private", URI: "gate.example.com"}}},
		{AUD: "aud-2", Domain: "other.example.com", Destinations: []accessDestination{{Type: "public", URI: "gate.example.com/api"}}},
		{AUD: "aud-1", Domain: "gate.example.com"},
		{AUD: "aud-gatekeeper", Domain: "gatekeeper.example.com"},
	}
	tun, err := c.Apply(t.Context(), "apps", []Route{web, gate})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.exactly("GET /accounts/" + testAccount + "/access/apps"); n != 3 {
		t.Errorf("5 applications were read in %d pages, want 3", n)
	}
	got := f.ingress[tun.ID]
	if len(got) != 3 || got[0].Hostname != "blog.example.com" || got[0].OriginRequest != nil || got[1].Hostname != "gate.example.com" ||
		got[1].OriginRequest == nil || got[2].Service != "http_status:404" {
		t.Fatalf("ingress %+v", got)
	}
	if a := got[1].OriginRequest.Access; !a.Required || a.TeamName != "myteam" || !slices.Equal(a.AudTag, []string{"aud-1", "aud-2"}) {
		t.Errorf("access %+v", a)
	}
	want := `{"config":{"ingress":[{"hostname":"blog.example.com","service":"http://127.0.0.1:20001"},` +
		`{"hostname":"gate.example.com","service":"http://127.0.0.1:20010","originRequest":{"access":{"required":true,"teamName":"myteam","audTag":["aud-1","aud-2"]}}},` +
		`{"service":"http_status:404"}]}}`
	if raw := f.config[tun.ID]; raw != want {
		t.Errorf("configuration\n%s\nwant\n%s", raw, want)
	}
	if tun.Token == "" {
		t.Error("no token")
	}
}

// A route with access whose host no Access application protects fails on its own, and keeps its
// record of Tender's; the route without access is set.
func TestCloudflareAccessWithoutAnApplication(t *testing.T) {
	f, c := newFakeCloudflare(t)
	example := f.addZone("example.com")
	plain := gate
	plain.Access = false
	tun, err := c.Apply(t.Context(), "apps", []Route{web, plain})
	if err != nil {
		t.Fatal(err)
	}
	f.apps = []accessApp{{AUD: "aud-1", Domain: "gatekeeper.example.com"}, {AUD: "aud-2", Domain: "example.com"}}
	_, err = c.Apply(t.Context(), "apps", []Route{web, gate})
	if got := routeErrors(t, err); !slices.Equal(got, []string{"zeron-mcp/gateway"}) {
		t.Errorf("route errors %q: %v", got, err)
	}
	want := "the route to gateway in zeron-mcp requires Cloudflare Access, but no Access application in the Cloudflare account protects gate.example.com. " +
		"Add a self-hosted Access application for it, or deploy the route without access"
	if err.Error() != want {
		t.Errorf("error %q\nwant %q", err, want)
	}
	if got := f.ingress[tun.ID]; len(got) != 2 || got[0].Hostname != "blog.example.com" {
		t.Errorf("ingress %+v", got)
	}
	var hosts []string
	for _, r := range f.zoneRecords(example) {
		hosts = append(hosts, r.Name)
	}
	slices.Sort(hosts)
	if !slices.Equal(hosts, []string{"blog.example.com", "gate.example.com"}) {
		t.Errorf("records %q", hosts)
	}
}

// A 401 or 403 on either Access call fails only the routes with access, with the permissions the
// token also needs. A team domain Tender can't read fails them too. A 5xx that lasts is
// Cloudflare failing as a whole.
func TestCloudflareAccessRefused(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	f.addZone("shop.example.com")
	f.apps = []accessApp{{AUD: "aud-1", Domain: "gate.example.com"}}
	other := Route{Service: "other", Container: "web", LocalPort: 20011, Host: "other.example.com", Access: true}
	apps, orgs := "GET /accounts/"+testAccount+"/access/apps", "GET /accounts/"+testAccount+"/access/organizations"
	for _, call := range []string{apps, orgs} {
		for _, status := range []int{401, 403} {
			f.fail[call] = []int{status}
			tun, err := c.Apply(t.Context(), "apps", []Route{web, api, gate, other})
			if got := routeErrors(t, err); !slices.Equal(got, []string{"other/web", "zeron-mcp/gateway"}) {
				t.Errorf("%s %d: route errors %q: %v", call, status, got, err)
				continue
			}
			if call == apps && (!strings.Contains(err.Error(), fmt.Sprintf("the route to gateway in zeron-mcp requires Cloudflare Access, but Cloudflare refused to show "+
				"the account's Access settings to the API token (%d %s). The API token also needs Access: Apps and Policies Read and "+
				"Access: Organizations, Identity Providers, and Groups Read. Add them, or deploy the route without access", status, http.StatusText(status))) ||
				!strings.Contains(err.Error(), "the route to web in other requires Cloudflare Access, but Cloudflare refused")) {
				t.Errorf("%s %d: %v", call, status, err)
			}
			// On the organization, other already failed for want of an application.
			if call == orgs && (!strings.Contains(err.Error(), "the route to gateway in zeron-mcp requires Cloudflare Access, but Cloudflare refused") ||
				!strings.Contains(err.Error(), "no Access application in the Cloudflare account protects other.example.com")) {
				t.Errorf("%s %d: %v", call, status, err)
			}
			if got := f.ingress[tun.ID]; len(got) != 3 || got[0].Hostname != "api.shop.example.com" || got[1].Hostname != "blog.example.com" || tun.Token == "" {
				t.Errorf("%s %d: ingress %+v, tunnel %+v", call, status, got, tun)
			}
		}
	}

	f.team = "login.example.com"
	_, err := c.Apply(t.Context(), "apps", []Route{web, gate})
	if got := routeErrors(t, err); !slices.Equal(got, []string{"zeron-mcp/gateway"}) || !strings.Contains(err.Error(), `team domain is "login.example.com"`) {
		t.Errorf("a team domain Tender can't read: %q %v", got, err)
	}
	f.team = "myteam.cloudflareaccess.com"

	f.fail[apps] = []int{503, 503, 503}
	tun, err := c.Apply(t.Context(), "apps", []Route{web, gate})
	var re *RouteError
	if err == nil || errors.As(err, &re) || tun.ID != "" || !strings.Contains(err.Error(), "Try again later") {
		t.Errorf("Cloudflare down: %v %+v", err, tun)
	}
}

// Without a route with access, neither Access endpoint is called and the ingress is as before.
func TestCloudflareWithoutAccessCallsNoAccessEndpoint(t *testing.T) {
	f, c := newFakeCloudflare(t)
	f.addZone("example.com")
	f.addZone("shop.example.com")
	f.fail["GET /accounts/"+testAccount+"/access/apps"] = []int{403}
	f.fail["GET /accounts/"+testAccount+"/access/organizations"] = []int{403}
	tun, err := c.Apply(t.Context(), "apps", []Route{web, api})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.accessCalls(); n != 0 {
		t.Errorf("%d calls to the Access endpoints: %q", n, f.calls)
	}
	want := `{"config":{"ingress":[{"hostname":"api.shop.example.com","service":"http://127.0.0.1:20002"},` +
		`{"hostname":"blog.example.com","service":"http://127.0.0.1:20001"},{"service":"http_status:404"}]}}`
	if raw := f.config[tun.ID]; raw != want {
		t.Errorf("configuration\n%s\nwant\n%s", raw, want)
	}
	// A route with access whose zone is missing fails before any Access call.
	if _, err := c.Apply(t.Context(), "apps", []Route{web, {Service: "x", Container: "y", LocalPort: 20012, Host: "app.example.org", Access: true}}); err == nil {
		t.Error("a host in no zone passed")
	}
	if n := f.accessCalls(); n != 0 {
		t.Errorf("%d calls to the Access endpoints for a route in no zone", n)
	}
}
