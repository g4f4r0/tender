package server

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
)

// Guard keeps web pages from using the hub through a browser that can reach it. The people
// routes have no login, so without it a page could drive them: with a cross-site request, or by
// pointing its own domain at the hub's address (DNS rebinding) and reading the answers.
//
// Guard answers 403 to:
//  1. a request whose Host is not the hub's: an IP address, a name without dots (localhost or a
//     short tailnet name), a Tailscale name under .ts.net, or one of hosts (TENDER_HOSTS);
//  2. a browser request from another origin, as http.CrossOriginProtection decides. The tender
//     command, daemons and MCP clients send no Origin, so they pass.
func Guard(h http.Handler, hosts []string, log *slog.Logger) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Warn("refused a request from another website", "method", r.Method, "path", r.URL.Path, "origin", r.Header.Get("Origin"))
		WriteProblem(w, http.StatusForbidden, "Forbidden", "The hub refuses requests from other websites.")
	}))
	inner := cop.Handler(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHost(r.Host, hosts) {
			log.Warn("refused a request for another host", "method", r.Method, "path", r.URL.Path, "host", r.Host)
			WriteProblem(w, http.StatusForbidden, "Forbidden", "This hub doesn't answer to the name "+quoteHost(r.Host)+
				". Reach it by its Tailscale name or an IP address, or add the name to TENDER_HOSTS in /etc/tender/hub.env.")
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// allowedHost reports whether host, a Host header with or without a port, names this hub. Names
// a web page can point at any address need a dot and a public domain, so they must be listed.
func allowedHost(host string, hosts []string) bool {
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.ToLower(strings.TrimSuffix(strings.Trim(name, "[]"), "."))
	switch {
	case name == "":
		return false
	case net.ParseIP(name) != nil, !strings.Contains(name, "."), strings.HasSuffix(name, ".ts.net"):
		return true
	}
	return slices.Contains(hosts, name)
}

func quoteHost(host string) string {
	if len(host) > 100 {
		host = host[:100]
	}
	return "\"" + strings.ToValidUTF8(host, "") + "\""
}
