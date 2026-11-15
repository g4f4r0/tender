package channels

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// SendTimeout bounds one delivery: one HTTP request, or one run of an exec adapter. Tests shorten it.
var SendTimeout = 10 * time.Second

// allowLocal lets tests send to their fake endpoints on loopback, and to a Slack URL on any host.
var allowLocal atomic.Bool

// AllowLocalForTests lets tests reach loopback and use any host as a Slack webhook. Only tests
// call it.
func AllowLocalForTests(on bool) { allowLocal.Store(on) }

// tailnet holds Tailscale's addresses, which a tailnet's machines use to reach each other.
var tailnet = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

// refused says why the hub doesn't send to ip, or is "" when it does. Loopback, link-local,
// multicast and unspecified addresses are refused, so a channel can't reach the hub's own
// services or a cloud's metadata endpoint. Private and tailnet addresses are allowed, since a
// self-hosted receiver such as ntfy or a pager bridge usually lives there. plain is a request
// without TLS, which may only go to a private or tailnet address: anything public needs https.
func refused(ip netip.Addr, plain bool) string {
	ip = ip.Unmap()
	switch {
	case ip.IsLoopback():
		if allowLocal.Load() {
			return ""
		}
		return "a loopback address"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "a link-local address"
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return "a multicast address"
	case ip.IsUnspecified(), ip.Is4() && ip.As4()[0] == 0:
		return "an unspecified address"
	case !plain || ip.IsPrivate():
		return ""
	}
	for _, p := range tailnet {
		if p.Contains(ip) {
			return ""
		}
	}
	return "a public address over plain http"
}

// refusal is a dial to an address the hub doesn't send to.
type refusal struct{ ip, why string }

func (r *refusal) Error() string {
	if r.why == "a public address over plain http" {
		return fmt.Sprintf("the URL leads to %s, a public address, and the hub doesn't send to one over plain http. Use an https URL.", r.ip)
	}
	return fmt.Sprintf("the URL leads to %s, %s, and the hub doesn't send to loopback, link-local, multicast or unspecified addresses. Use the receiver's own private, tailnet or public address.", r.ip, r.why)
}

// client makes the client for https, or for plain http when plain. It checks the address it
// dials, after DNS, so a name that resolves to a refused address is refused too. It uses no
// proxy and never follows a redirect, so a body and its signature only ever go to the URL in
// the config.
func client(plain bool) *http.Client {
	dialer := &net.Dialer{Timeout: SendTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return &refusal{host, "not an address"}
		}
		if why := refused(ip, plain); why != "" {
			return &refusal{ip.Unmap().String(), why}
		}
		return nil
	}}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = dialer.DialContext
	return &http.Client{
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var httpsClient, plainClient = client(false), client(true)

// post sends body to target. A 2xx answer is delivered; a 4xx other than 429 is Permanent;
// anything else is worth trying again. The answer's body is never read: an error is the status
// alone, such as "HTTP 404 Not Found", so a receiver can't put text into the hub's records. what
// names the receiver in errors, which never hold the URL, since it can carry a key.
func post(ctx context.Context, target, what string, body []byte, header http.Header) error {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
		return &Permanent{fmt.Errorf("the %s URL is not valid", what)}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Tender")
	c := httpsClient
	if req.URL.Scheme == "http" {
		c = plainClient
	}
	resp, err := c.Do(req)
	if err != nil {
		var r *refusal
		if errors.As(err, &r) {
			return &Permanent{errors.New(strings.ToUpper(r.Error()[:1]) + r.Error()[1:])}
		}
		return fmt.Errorf("could not reach the %s: %s", what, reason(err))
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // lets the connection be used again
	if resp.StatusCode/100 == 2 {
		return nil
	}
	err = errors.New(strings.TrimSpace(fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))))
	if resp.StatusCode/100 == 4 && resp.StatusCode != http.StatusTooManyRequests {
		return &Permanent{err}
	}
	return err
}

// reason is why a request failed, without the URL that net/http puts in its errors.
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fmt.Sprintf("no answer within %d seconds", int(SendTimeout/time.Second))
	}
	return strings.TrimSpace(err.Error())
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// checkURL is an error unless raw is an http or https URL with a host. A host that is an
// address must be one the hub sends to; a name is checked each time the hub dials it. field
// names it.
func checkURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return fmt.Errorf("%s must be an http or https URL, such as https://example.com/tender.", field)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if why := refused(ip, u.Scheme == "http"); why != "" {
			r := &refusal{ip.Unmap().String(), why}
			return fmt.Errorf("%s: %s", field, r.Error())
		}
	}
	return nil
}
