package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/g4f4r0/tender/internal/apps"
	"github.com/g4f4r0/tender/internal/apps/expose"
)

// The Cloudflare settings can be set, shown and removed from the command line and over MCP,
// against the real hub and a fake Cloudflare API, and the token never shows.
func TestCloudflareSettingsThroughTheCLIAndMCP(t *testing.T) {
	const account, token = "0123456789abcdef0123456789abcdef", "cf-api-token-marker-0123456789abcdefghij"
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 10000, "message": "Authentication error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success": true, "errors": [], "result": [], "result_info": {"page": 1, "total_pages": 1}}`))
	}))
	t.Cleanup(cf.Close)
	t.Setenv(expose.EnvTestCloudflare, cf.URL)
	d := seeded(t)
	hub := httptest.NewServer(newHub(d, testKey, nil, quiet))
	t.Cleanup(hub.Close)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := &answers{}

	if _, err := cli(t, hub.URL, none, "exposures", "cloudflare", "show"); err == nil || !strings.Contains(err.Error(), "Cloudflare isn't set up") {
		t.Fatalf("show before set: %v", err)
	}
	if _, err := cli(t, hub.URL, none, "exposures", "cloudflare", "set", "--api-token-file", file); err == nil || !strings.Contains(err.Error(), "needs --account-id") {
		t.Errorf("set without an account: %v", err)
	}
	out, err := cli(t, hub.URL, none, "exposures", "cloudflare", "set", "--account-id", account, "--api-token-file", file)
	if err != nil || !strings.Contains(out, "Saved the Cloudflare settings. Cloudflare accepted the API token for account "+account+".") {
		t.Fatalf("set: %v %q", err, out)
	}
	out, err = cli(t, hub.URL, &answers{left: []string{"cf-a-token-cloudflare-doesnt-know"}}, "exposures", "cloudflare", "set", "--account-id", account)
	if err == nil || !strings.Contains(err.Error(), "Authentication error (10000)") || strings.Contains(err.Error(), "doesnt-know") {
		t.Errorf("a refused token, asked for: %v %q", err, out)
	}
	out, err = cli(t, hub.URL, none, "exposures", "cloudflare", "show")
	if err != nil || !strings.Contains(out, "Cloudflare account: "+account) || !strings.Contains(out, "API token: set, hidden") || strings.Contains(out, token) {
		t.Errorf("show: %v %q", err, out)
	}

	res, err := http.Get(hub.URL + "/v1/exposures/cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	var st apps.CloudflareSettings
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil || st.APIToken != "***" || !st.APITokenSet {
		t.Errorf("GET: %v %+v", err, st)
	}
	_ = res.Body.Close()

	cs := connect(t, d)
	var got apps.CloudflareSettings
	structured(t, callTool(t, cs, "get_cloudflare_settings", map[string]any{}), &got)
	if got.AccountID != account || got.APIToken != "***" {
		t.Errorf("get_cloudflare_settings %+v", got)
	}
	if r := callTool(t, cs, "set_cloudflare_settings", map[string]any{"account_id": account}); r.IsError {
		t.Errorf("set_cloudflare_settings keeping the token: %s", errorText(r))
	}
	if r := callTool(t, cs, "set_cloudflare_settings", map[string]any{"account_id": account, "api_token": "***"}); !r.IsError ||
		!strings.Contains(errorText(r), "how the hub hides a secret") {
		t.Errorf("set_cloudflare_settings with ***: %s", errorText(r))
	}

	out, err = cli(t, hub.URL, none, "exposures", "cloudflare", "remove")
	if err != nil || !strings.Contains(out, "Removed the Cloudflare settings.") {
		t.Errorf("remove: %v %q", err, out)
	}
	if r := callTool(t, cs, "remove_cloudflare_settings", map[string]any{}); !r.IsError || !strings.Contains(errorText(r), "nothing to remove") {
		t.Errorf("remove_cloudflare_settings again: %s", errorText(r))
	}
}
