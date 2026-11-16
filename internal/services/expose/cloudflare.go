package expose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// CloudflareAPI is the Cloudflare API the hub calls. EnvTestCloudflare replaces it, for the
// end-to-end test's fake.
const (
	CloudflareAPI     = "https://api.cloudflare.com/client/v4"
	EnvTestCloudflare = "TENDER_TEST_CLOUDFLARE"
)

// CloudflareBase is the Cloudflare API's base URL: CloudflareAPI, or EnvTestCloudflare when set.
func CloudflareBase(getenv func(string) string) string {
	if b := strings.TrimRight(getenv(EnvTestCloudflare), "/"); b != "" {
		return b
	}
	return CloudflareAPI
}

// DNSComment marks the DNS records Tender keeps. Tender deletes only records that carry it.
const DNSComment = "managed by tender"

// CloudflarePermissions are the API token's permissions Tender needs, as Cloudflare's dashboard
// names them.
const CloudflarePermissions = "Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read"

// CloudflareAccessPermissions are the API token's permissions a route with access also needs, to
// read the account's Access applications and its team name.
const CloudflareAccessPermissions = "Access: Apps and Policies Read and Access: Organizations, Identity Providers, and Groups Read"

var accountIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// CheckAccountID is an error unless id looks like a Cloudflare account ID: 32 lowercase hex digits.
func CheckAccountID(id string) error {
	if !accountIDPattern.MatchString(id) {
		return errors.New("account_id must be a Cloudflare account ID, 32 lowercase hex digits, as the dashboard shows under Account home")
	}
	return nil
}

// Cloudflare is the hub's client of the Cloudflare API for one account. For each machine it keeps
// one remotely managed tunnel, named tender-<machine>, whose ingress is the machine's cloudflare
// routes, and a proxied CNAME per route's host, commented DNSComment. It never logs, and its
// errors never hold the API token or a tunnel token.
type Cloudflare struct {
	Base      string // the API's base URL; CloudflareAPI when empty
	AccountID string
	Token     string        // the API token
	Client    *http.Client  // http.Client with a 30 s timeout when nil
	Retries   int           // tries again after a 5xx, 429 or network error; 2 when 0, none when negative
	Backoff   time.Duration // the wait before the first try again, doubling; 1 s when 0
}

// CloudflareError is a call Cloudflare refused or couldn't answer.
type CloudflareError struct {
	Status    int  // the HTTP status, or 0 when Cloudflare couldn't be reached
	Retryable bool // a 5xx, a 429 or a network error, which may pass later; a 4xx won't
	msg       string
}

func (e *CloudflareError) Error() string { return e.msg }

// Tunnel is a machine's tunnel. Token is what cloudflared on the machine runs it with; it is a
// secret, and never in JSON.
type Tunnel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"-"`
}

// TunnelName is the name of machine's tunnel.
func TunnelName(machine string) string { return "tender-" + machine }

type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

// maxResponse bounds what the client reads of one answer.
const maxResponse = 8 << 20

// redact takes the client's secrets out of s.
func (c *Cloudflare) redact(s string, more ...string) string {
	for _, v := range append([]string{c.Token}, more...) {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// call sends one request, tries again on what may pass, and decodes the result into out.
func (c *Cloudflare) call(ctx context.Context, method, path string, query url.Values, body, out any) (*envelope, error) {
	base := c.Base
	if base == "" {
		base = CloudflareAPI
	}
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	retries, wait := c.Retries, c.Backoff
	if retries == 0 {
		retries = 2
	}
	if wait == 0 {
		wait = time.Second
	}
	for try := 0; ; try++ {
		env, err := c.once(ctx, method, u, path, data)
		var ce *CloudflareError
		if err == nil || !errors.As(err, &ce) || !ce.Retryable || try >= retries {
			if err == nil && out != nil {
				if uerr := json.Unmarshal(env.Result, out); uerr != nil {
					return nil, &CloudflareError{Retryable: true, msg: fmt.Sprintf("Cloudflare's answer to %s %s is not what Tender expects", method, path)}
				}
			}
			return env, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func (c *Cloudflare) once(ctx context.Context, method, u, path string, data []byte) (*envelope, error) {
	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is known; keep the reason
		}
		return nil, &CloudflareError{Retryable: true, msg: c.redact(fmt.Sprintf("Tender could not reach the Cloudflare API for %s %s: %v. Try again later", method, path, err))}
	}
	defer func() { _ = res.Body.Close() }()
	var env envelope
	raw, rerr := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	decodeErr := json.Unmarshal(raw, &env)
	status := strconv.Itoa(res.StatusCode) + " " + http.StatusText(res.StatusCode)
	switch {
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return nil, &CloudflareError{Status: res.StatusCode, Retryable: true,
			msg: fmt.Sprintf("Cloudflare answered %s %s with %s. Try again later", method, path, status)}
	case res.StatusCode >= 400:
		msg := fmt.Sprintf("Cloudflare refused %s %s: %s", method, path, status)
		if decodeErr == nil {
			var parts []string
			for _, e := range env.Errors {
				parts = append(parts, fmt.Sprintf("%s (%d)", strings.TrimRight(e.Message, ". "), e.Code))
			}
			if len(parts) > 0 {
				msg += ", " + strings.Join(parts, "; ")
			}
		}
		if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
			msg += ". Check the account ID, and that the API token has " + CloudflarePermissions
		}
		return nil, &CloudflareError{Status: res.StatusCode, msg: c.redact(msg)}
	case rerr != nil || decodeErr != nil || !env.Success:
		return nil, &CloudflareError{Status: res.StatusCode, Retryable: true,
			msg: fmt.Sprintf("Cloudflare's answer to %s %s is not what Tender expects", method, path)}
	}
	return &env, nil
}

// maxPages bounds a listing, so a server that always says there is more can't keep the hub busy.
const maxPages = 1000

// list gets every page of a listing into out, one item at a time.
func list[T any](ctx context.Context, c *Cloudflare, path string, query url.Values) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "50")
		var items []T
		env, err := c.call(ctx, http.MethodGet, path, q, nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(items) == 0 {
			return all, nil
		}
	}
	return nil, &CloudflareError{Retryable: true, msg: fmt.Sprintf("Cloudflare's listing of %s has more than %d pages", path, maxPages)}
}

func (c *Cloudflare) account() string { return "/accounts/" + url.PathEscape(c.AccountID) }

// Verify checks the account ID and the API token: it lists the account's tunnels and zones, which
// needs the tunnel and zone permissions. A token without DNS Edit passes, and fails on the first
// DNS change.
func (c *Cloudflare) Verify(ctx context.Context) error {
	q := url.Values{"per_page": {"1"}}
	if _, err := c.call(ctx, http.MethodGet, c.account()+"/cfd_tunnel", q, nil, nil); err != nil {
		return err
	}
	q.Set("account.id", c.AccountID)
	_, err := c.call(ctx, http.MethodGet, "/zones", q, nil, nil)
	return err
}

// findTunnel returns the tunnel called name, or nil.
func (c *Cloudflare) findTunnel(ctx context.Context, name string) (*Tunnel, error) {
	list, err := list[Tunnel](ctx, c, c.account()+"/cfd_tunnel", url.Values{"name": {name}, "is_deleted": {"false"}})
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.Name == name {
			return &t, nil
		}
	}
	return nil, nil
}

type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type dnsRecord struct {
	ID      string  `json:"id,omitempty"`
	Type    string  `json:"type"`
	Name    string  `json:"name"`
	Content string  `json:"content"`
	Proxied bool    `json:"proxied"`
	TTL     int     `json:"ttl"`
	Comment *string `json:"comment"`
}

func (r dnsRecord) ours() bool { return r.Comment != nil && *r.Comment == DNSComment }

// zoneOf returns the zone that is host's longest matching suffix, or nil.
func zoneOf(zones []zone, host string) *zone {
	var best *zone
	for i, z := range zones {
		name := strings.ToLower(strings.TrimSuffix(z.Name, "."))
		if (host == name || strings.HasSuffix(host, "."+name)) && (best == nil || len(name) > len(best.Name)) {
			best = &zones[i]
		}
	}
	return best
}

type ingressRule struct {
	Hostname      string         `json:"hostname,omitempty"`
	Service       string         `json:"service"`
	OriginRequest *originRequest `json:"originRequest,omitempty"` // only on a route with access
}

// originRequest makes cloudflared check each request's Cf-Access-Jwt-Assertion against the
// account's team and the host's Access applications before it proxies it.
type originRequest struct {
	Access originAccess `json:"access"`
}

type originAccess struct {
	Required bool     `json:"required"`
	TeamName string   `json:"teamName"`
	AudTag   []string `json:"audTag"`
}

// accessApp is a Cloudflare Access application, as far as Tender reads it.
type accessApp struct {
	AUD          string              `json:"aud"`
	Domain       string              `json:"domain"`
	Destinations []accessDestination `json:"destinations"`
}

// accessDestination is one of the places an Access application protects. A public one is a
// hostname, maybe with a path, in uri.
type accessDestination struct {
	Type string `json:"type"`
	URI  string `json:"uri"`
}

// accessHost is the host part of an Access application's domain or destination, such as
// app.example.com for App.Example.com./admin: in lowercase, without its path or a trailing dot.
func accessHost(s string) string {
	s, _, _ = strings.Cut(s, "/")
	return strings.ToLower(strings.TrimSuffix(s, "."))
}

// protects says the application's domain, or one of its public destinations, is host.
func (a accessApp) protects(host string) bool {
	if a.Domain != "" && accessHost(a.Domain) == host {
		return true
	}
	for _, d := range a.Destinations {
		if d.Type == "public" && accessHost(d.URI) == host {
			return true
		}
	}
	return false
}

// accessError is the failure of route r, which requires Cloudflare Access: why it can't have it,
// then fix, what to do.
func accessError(r Route, why, fix string) *RouteError {
	return &RouteError{Service: r.Service, Container: r.Container,
		Err: fmt.Errorf("the route to %s in %s requires Cloudflare Access, but %s. %s", r.Container, r.Service, why, fix)}
}

// noAccess fails each of routes when Cloudflare refused an Access call with a 4xx, which another
// try won't change. Any other error is Cloudflare failing as a whole, and is returned as it is.
func noAccess(routes []Route, err error) ([]error, error) {
	var ce *CloudflareError
	if !errors.As(err, &ce) || ce.Retryable {
		return nil, err
	}
	why := "Cloudflare refused to show the account's Access settings to the API token (" + strconv.Itoa(ce.Status) + " " + http.StatusText(ce.Status) + ")"
	fix := "The API token also needs " + CloudflareAccessPermissions + ". Add them, or deploy the route without access"
	if ce.Status != http.StatusUnauthorized && ce.Status != http.StatusForbidden {
		why, fix = strings.TrimRight(ce.Error(), ". "), "Deploy the route without access, or try again once Cloudflare Access is set up for the account"
	}
	errs := make([]error, len(routes))
	for i, r := range routes {
		errs[i] = accessError(r, why, fix)
	}
	return errs, nil
}

// access returns the origin check of each of routes that requires Cloudflare Access, by host: the
// account's team name, and the aud of every Access application that protects the host. A route
// with access that can't have it gets a *RouteError. With no route with access, it calls nothing.
func (c *Cloudflare) access(ctx context.Context, routes []Route) (map[string]*originRequest, []error, error) {
	var want []Route
	for _, r := range routes {
		if r.Access {
			want = append(want, r)
		}
	}
	if len(want) == 0 {
		return nil, nil, nil
	}
	apps, err := list[accessApp](ctx, c, c.account()+"/access/apps", nil)
	if err != nil {
		errs, err := noAccess(want, err)
		return nil, errs, err
	}
	var errs []error
	var found []Route
	auds := map[string][]string{}
	for _, r := range want {
		host := strings.ToLower(r.Host)
		var tags []string
		for _, a := range apps {
			if a.AUD != "" && a.protects(host) && !slices.Contains(tags, a.AUD) {
				tags = append(tags, a.AUD)
			}
		}
		if len(tags) == 0 {
			errs = append(errs, accessError(r, "no Access application in the Cloudflare account protects "+r.Host,
				"Add a self-hosted Access application for it, or deploy the route without access"))
			continue
		}
		auds[r.Host] = tags
		found = append(found, r)
	}
	if len(found) == 0 {
		return nil, errs, nil
	}
	var org struct {
		AuthDomain string `json:"auth_domain"`
	}
	if _, err := c.call(ctx, http.MethodGet, c.account()+"/access/organizations", nil, nil, &org); err != nil {
		more, err := noAccess(found, err)
		return nil, append(errs, more...), err
	}
	team, ok := strings.CutSuffix(strings.ToLower(strings.TrimSuffix(org.AuthDomain, ".")), ".cloudflareaccess.com")
	if !ok || team == "" || strings.Contains(team, ".") {
		for _, r := range found {
			errs = append(errs, accessError(r, fmt.Sprintf("the account's Zero Trust team domain is %q, not one such as myteam.cloudflareaccess.com", org.AuthDomain),
				"Check the team domain in Zero Trust, under Settings, or deploy the route without access"))
		}
		return nil, errs, nil
	}
	out := map[string]*originRequest{}
	for _, r := range found {
		out[r.Host] = &originRequest{Access: originAccess{Required: true, TeamName: team, AudTag: auds[r.Host]}}
	}
	return out, errs, nil
}

// Apply makes machine's tunnel serve exactly routes, each at https://<host>, and returns the
// tunnel with its token. It creates the tunnel on the first route, creates or updates a proxied
// CNAME for each host in the zone that is the host's longest suffix, replaces the tunnel's ingress
// with the routes whose record is in place and a 404 for any other host, and deletes the records of
// its own, with DNSComment and this tunnel as their target, whose host no route wants.
//
// A route with Access gets an ingress rule whose originRequest makes cloudflared require a valid
// Cloudflare Access token for its host, from the account's team and the aud of each Access
// application that protects the host. Only then does Apply read the account's Access applications
// and organization; a route without Access keeps a plain rule.
//
// A route that can't be set, because its host is bad or taken by another route, is in no zone of
// the account, has a record Tender didn't make, Cloudflare refuses its record, or it has Access
// but no Access application protects its host or the token may not read them, gets a
// *RouteError and is left out of the ingress; every other route is set, and its record stays. The
// error is those, joined, with the tunnel. Cloudflare failing as a whole, such as a 5xx after its
// tries, a refused ingress or no token, is a plain error with no tunnel. With no route to set and
// no tunnel it changes nothing and returns no tunnel; with no route set it returns no token.
func (c *Cloudflare) Apply(ctx context.Context, machine string, routes []Route) (Tunnel, error) {
	good, errs := hostRoutes("cloudflare", routes)
	fail := func(err error) (Tunnel, error) { return Tunnel{}, errors.Join(append(errs, err)...) }
	// Every host a route wants keeps its record of Tender's, even when its route fails this time.
	want := map[string]bool{}
	for _, r := range routes {
		want[strings.ToLower(r.Host)] = true
	}
	zones, err := list[zone](ctx, c, "/zones", url.Values{"account.id": {c.AccountID}})
	if err != nil {
		return fail(err)
	}
	type placed struct {
		Route
		zone *zone
	}
	var todo []placed
	for _, r := range good {
		z := zoneOf(zones, r.Host)
		if z == nil {
			errs = append(errs, routeError(r, "no zone of the Cloudflare account holds "+r.Host,
				"Add its domain to the account, or pick a host in one of its zones"))
			continue
		}
		todo = append(todo, placed{r, z})
	}
	toSet := make([]Route, len(todo))
	for i, p := range todo {
		toSet[i] = p.Route
	}
	origins, refused, err := c.access(ctx, toSet)
	if err != nil {
		return fail(err)
	}
	errs = append(errs, refused...)
	todo = slices.DeleteFunc(todo, func(p placed) bool { return p.Access && origins[p.Host] == nil })
	name := TunnelName(machine)
	t, err := c.findTunnel(ctx, name)
	if err != nil {
		return fail(err)
	}
	if t == nil && len(todo) == 0 {
		return Tunnel{}, errors.Join(errs...)
	}
	if t == nil {
		var made Tunnel
		if _, err := c.call(ctx, http.MethodPost, c.account()+"/cfd_tunnel", nil,
			map[string]string{"name": name, "config_src": "cloudflare"}, &made); err != nil {
			return fail(err)
		}
		t = &made
	}

	target := t.ID + ".cfargotunnel.com"
	ingress := make([]ingressRule, 0, len(todo)+1)
	for _, p := range todo {
		if err := c.keepRecord(ctx, p.zone, p.Route, target); err != nil {
			var re *RouteError
			if !errors.As(err, &re) {
				return fail(err)
			}
			errs = append(errs, err)
			continue
		}
		ingress = append(ingress, ingressRule{Hostname: p.Host, Service: "http://127.0.0.1:" + strconv.Itoa(p.LocalPort), OriginRequest: origins[p.Host]})
	}
	ingress = append(ingress, ingressRule{Service: "http_status:404"})
	tunnelPath := c.account() + "/cfd_tunnel/" + url.PathEscape(t.ID)
	if _, err := c.call(ctx, http.MethodPut, tunnelPath+"/configurations", nil,
		map[string]any{"config": map[string]any{"ingress": ingress}}, nil); err != nil {
		return fail(err)
	}

	for _, z := range zones {
		recs, err := list[dnsRecord](ctx, c, "/zones/"+url.PathEscape(z.ID)+"/dns_records",
			url.Values{"type": {"CNAME"}, "comment.exact": {DNSComment}})
		if err != nil {
			return fail(err)
		}
		for _, rec := range recs {
			if rec.ours() && rec.Type == "CNAME" && strings.EqualFold(rec.Content, target) && !want[strings.ToLower(strings.TrimSuffix(rec.Name, "."))] {
				if _, err := c.call(ctx, http.MethodDelete, "/zones/"+url.PathEscape(z.ID)+"/dns_records/"+url.PathEscape(rec.ID), nil, nil, nil); err != nil {
					return fail(err)
				}
			}
		}
	}
	if len(ingress) == 1 {
		return Tunnel{ID: t.ID, Name: t.Name}, errors.Join(errs...)
	}
	var token string
	if _, err := c.call(ctx, http.MethodGet, tunnelPath+"/token", nil, nil, &token); err != nil {
		return fail(err)
	}
	if token == "" {
		return fail(&CloudflareError{Retryable: true, msg: "Cloudflare gave an empty token for tunnel " + name})
	}
	return Tunnel{ID: t.ID, Name: t.Name, Token: token}, errors.Join(errs...)
}

// keepRecord makes r's host a proxied CNAME to target in z, commented DNSComment. A record of
// Tender's for the host, even one pointing at another machine's tunnel, is updated. Any other
// record for the host is left alone, and the route fails with a *RouteError, as it does when
// Cloudflare refuses the record with a 4xx; any other failure is a plain error.
func (c *Cloudflare) keepRecord(ctx context.Context, z *zone, r Route, target string) error {
	host := r.Host
	path := "/zones/" + url.PathEscape(z.ID) + "/dns_records"
	recs, err := list[dnsRecord](ctx, c, path, url.Values{"name.exact": {host}})
	if err != nil {
		return refusedRoute(r, err)
	}
	comment := DNSComment
	rec := dnsRecord{Type: "CNAME", Name: host, Content: target, Proxied: true, TTL: 1, Comment: &comment}
	var mine *dnsRecord
	for i, x := range recs {
		if !strings.EqualFold(strings.TrimSuffix(x.Name, "."), host) {
			continue
		}
		if !x.ours() || x.Type != "CNAME" || mine != nil {
			return routeError(r, fmt.Sprintf("%s already has a DNS record of type %s in Cloudflare that Tender didn't make", host, x.Type),
				"Delete that record in the Cloudflare dashboard, or pick another host")
		}
		mine = &recs[i]
	}
	switch {
	case mine == nil:
		_, err = c.call(ctx, http.MethodPost, path, nil, rec, nil)
	case !strings.EqualFold(mine.Content, target) || !mine.Proxied:
		_, err = c.call(ctx, http.MethodPut, path+"/"+url.PathEscape(mine.ID), nil, rec, nil)
	}
	return refusedRoute(r, err)
}

// refusedRoute is err as r's *RouteError when Cloudflare refused r's record with a 4xx, which
// another try won't change, and err itself otherwise.
func refusedRoute(r Route, err error) error {
	var ce *CloudflareError
	if errors.As(err, &ce) && !ce.Retryable {
		return routeError(r, strings.TrimRight(ce.Error(), ". "), "")
	}
	return err
}
