package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Guard(ok, []string{"tender.example.com"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cases := []struct {
		name, method, host string
		header             map[string]string
		want               int
	}{
		{"loopback with port", "POST", "127.0.0.1:8080", nil, 204},
		{"localhost", "GET", "localhost:8080", nil, 204},
		{"ipv6 loopback", "GET", "[::1]:8080", nil, 204},
		{"tailnet IP", "GET", "100.101.102.103:8080", nil, 204},
		{"short tailnet name", "GET", "apps:8080", nil, 204},
		{"Tailscale name", "POST", "apps.tail1234.ts.net", nil, 204},
		{"listed name, any case, trailing dot", "GET", "Tender.Example.com.", nil, 204},
		// DNS rebinding: a page's own domain pointed at the hub.
		{"rebound domain", "GET", "attacker.example:8080", nil, 403},
		{"rebound domain, same-origin POST", "POST", "attacker.example:8080",
			map[string]string{"Origin": "http://attacker.example:8080"}, 403},
		{"lookalike of ts.net", "GET", "apps.ts.net.attacker.example", nil, 403},
		{"no host", "GET", "", nil, 403},
		// Cross-site requests from a page elsewhere.
		{"cross-site POST", "POST", "127.0.0.1:8080", map[string]string{"Origin": "https://evil.example"}, 403},
		{"cross-site fetch metadata", "POST", "apps.tail1234.ts.net", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-origin browser POST", "POST", "apps.tail1234.ts.net",
			map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://apps.tail1234.ts.net"}, 204},
		{"cross-site GET only reads", "GET", "127.0.0.1:8080", map[string]string{"Sec-Fetch-Site": "cross-site"}, 204},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/v1/machines", nil)
			r.Host = c.host
			for k, v := range c.header {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, c.want, w.Body)
			}
			if c.want == 403 && !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("refusal is %q, want problem details", w.Header().Get("Content-Type"))
			}
		})
	}
}
