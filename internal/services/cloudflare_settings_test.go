package services

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	account  = "0123456789abcdef0123456789abcdef"
	apiToken = "cf-api-token-marker-0123456789abcdefghij"
)

var cfKey = bytes.Repeat([]byte{7}, 32)

// fakeCloudflareAPI is a Cloudflare API that knows one account and token, and answers status when it isn't 0.
func fakeCloudflareAPI(t *testing.T, status *atomic.Int32, calls *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case status.Load() != 0:
			w.WriteHeader(int(status.Load()))
			_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 9, "message": "down"}]}`))
		case r.Header.Get("Authorization") != "Bearer "+apiToken:
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 10000, "message": "Authentication error"}]}`))
		case r.URL.Path == "/accounts/"+account+"/cfd_tunnel" || (r.URL.Path == "/zones" && r.URL.Query().Get("account.id") == account):
			_, _ = w.Write([]byte(`{"success": true, "errors": [], "result": [], "result_info": {"page": 1, "total_pages": 1}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 7003, "message": "Could not route"}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The settings are stored once Cloudflare accepts the token, with the token sealed and shown as
// "***"; a later set may leave the token out to keep it.
func TestCloudflareSettings(t *testing.T) {
	d := newDB(t)
	var fail, calls atomic.Int32
	s := NewCloudflareStore(d, cfKey)
	s.API = fakeCloudflareAPI(t, &fail, &calls)
	ctx := t.Context()

	if _, err := s.Cloudflare(ctx); status(err) != 404 || !strings.Contains(err.Error(), "tender exposures cloudflare set") {
		t.Fatalf("none yet: %v", err)
	}
	if _, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account}); status(err) != 422 || !strings.Contains(err.Error(), "api_token is required") {
		t.Errorf("no token yet: %v", err)
	}
	st, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account, APIToken: ptr(apiToken)})
	if err != nil || st.AccountID != account || st.APIToken != Hidden || !st.APITokenSet || calls.Load() != 2 {
		t.Fatalf("set: %v %+v (%d calls)", err, st, calls.Load())
	}
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), apiToken) || !strings.Contains(string(b), `"api_token":"***","api_token_set":true`) {
		t.Errorf("JSON %s", b)
	}
	var sealed []byte
	if err := d.Read.QueryRow(`SELECT api_token FROM service_cloudflare_settings`).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte(apiToken)) {
		t.Errorf("the token is stored in plain text: %v", err)
	}
	if st, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account}); err != nil || !st.APITokenSet {
		t.Errorf("keeping the token: %v", err)
	}
	c, err := s.Client(ctx)
	if err != nil || c.Token != apiToken || c.AccountID != account || c.Base != s.API {
		t.Errorf("client: %v %+v", err, c)
	}
	if err := s.CheckKey(ctx); err != nil {
		t.Error(err)
	}
	other := NewCloudflareStore(d, bytes.Repeat([]byte{8}, 32))
	if err := other.CheckKey(ctx); err == nil {
		t.Error("a wrong key passed CheckKey")
	}
	if _, err := NewCloudflareStore(d, nil).Client(ctx); status(err) != 503 {
		t.Errorf("no key: %v", err)
	}
}

// A token Cloudflare refuses is a 422 that says so, Cloudflare down is a 502, and bad values never
// reach Cloudflare; none of them stores anything or shows the token.
func TestCloudflareSettingsRefused(t *testing.T) {
	d := newDB(t)
	var fail, calls atomic.Int32
	s := NewCloudflareStore(d, cfKey)
	s.API = fakeCloudflareAPI(t, &fail, &calls)
	ctx := t.Context()
	for _, in := range []CloudflareInput{
		{AccountID: "", APIToken: ptr(apiToken)},
		{AccountID: strings.ToUpper(account), APIToken: ptr(apiToken)},
		{AccountID: account, APIToken: ptr("short")},
		{AccountID: account, APIToken: ptr(apiToken + " x")},
		{AccountID: account, APIToken: ptr(Hidden)},
	} {
		if _, err := s.SetCloudflare(ctx, in); status(err) != 422 {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("bad values reached Cloudflare %d times", calls.Load())
	}
	_, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account, APIToken: ptr("cf-a-token-cloudflare-doesnt-know")})
	if status(err) != 422 || !strings.Contains(err.Error(), "Authentication error (10000)") || !strings.Contains(err.Error(), "Zone DNS Edit") ||
		strings.Contains(err.Error(), "doesnt-know") {
		t.Errorf("refused token: %v", err)
	}
	if _, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: strings.Repeat("f", 32), APIToken: ptr(apiToken)}); status(err) != 422 {
		t.Errorf("wrong account: %v", err)
	}
	fail.Store(503)
	s.backoff = time.Millisecond
	if _, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account, APIToken: ptr(apiToken)}); status(err) != 502 || !strings.Contains(err.Error(), "could not check the settings with Cloudflare") {
		t.Errorf("Cloudflare down: %v", err)
	}
	if _, err := s.Cloudflare(ctx); status(err) != 404 {
		t.Errorf("a refused set stored something: %v", err)
	}
}

// Removing the settings refuses while cloudflare routes need them, and answers 404 when there are none.
func TestCloudflareSettingsRemoved(t *testing.T) {
	d := newDB(t)
	var fail, calls atomic.Int32
	s := NewCloudflareStore(d, cfKey)
	s.API = fakeCloudflareAPI(t, &fail, &calls)
	ctx := t.Context()
	if err := s.DeleteCloudflare(ctx); status(err) != 404 {
		t.Errorf("nothing to remove: %v", err)
	}
	if _, err := s.SetCloudflare(ctx, CloudflareInput{AccountID: account, APIToken: ptr(apiToken)}); err != nil {
		t.Fatal(err)
	}
	routes := 2
	s.InUse = func(context.Context) (int, error) { return routes, nil }
	if err := s.DeleteCloudflare(ctx); status(err) != 409 || !strings.Contains(err.Error(), "2 cloudflare routes") {
		t.Errorf("in use: %v", err)
	}
	routes = 0
	if err := s.DeleteCloudflare(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cloudflare(ctx); status(err) != 404 {
		t.Errorf("after remove: %v", err)
	}
}
