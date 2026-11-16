// Package deploy deploys a managed service on its machine: the daemon side of the services domain.
// It checks the compose file against Tender's rules, writes the service's folder and the override
// that publishes its routes, runs docker compose, and applies the machine's exposures (package
// expose). The hub checks a service's input with the same rules for names, files and env.
package deploy

import "github.com/g4f4r0/tender/internal/services/expose"

// Ways a route is exposed.
const (
	ExposeTailscale  = "tailscale"  // over the machine's tailnet name, with Tailscale Serve
	ExposeCloudflare = "cloudflare" // at a host, through the machine's Cloudflare tunnel
	ExposePublic     = "public"     // at a host, through the machine's own Caddy on ports 80 and 443
)

// Hidden is how the API shows a secret: an env value or a registry password.
const Hidden = "***"

// Label is the Docker label Tender puts on every container of a managed service, with the
// service's name.
const Label = "dev.tender.service"

// Deployment is what the daemon gets with a deploy or remove action: the service with its env in
// the clear, the logins of the registries, and the machine's whole set of routes for its exposures.
type Deployment struct {
	Name       string            `json:"name"`
	Compose    string            `json:"compose"`
	Files      map[string]string `json:"files"`
	Env        map[string]string `json:"env"`
	Routes     []Published       `json:"routes"`     // this service's routes, which the override publishes
	Registries []Login           `json:"registries"` // the daemon uses those whose host an image is on
	Exposures  Exposures         `json:"exposures"`
	// CloudflareError is why the hub couldn't route this service's cloudflare routes through the
	// machine's tunnel, or "": the deploy or remove fails with it once the containers are done.
	CloudflareError string `json:"cloudflare_error,omitempty"`
}

// Published is a route as the override publishes it: 127.0.0.1:<local_port>:<port> on container,
// a service in the compose file.
type Published struct {
	Container string `json:"container"`
	Port      int    `json:"port"`
	LocalPort int    `json:"local_port"`
	Expose    string `json:"expose"` // tailscale, cloudflare or public
}

// Login is a registry's login, as the daemon gets it.
type Login struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Exposures is the whole set of routes of one machine, as GET /v1/machines/{machine}/exposures
// returns it to the machine's daemon and every deploy and remove carries it.
type Exposures struct {
	Routes []Exposed `json:"routes"`
	// CloudflareTunnelToken runs the machine's Cloudflare tunnel; null when it has none.
	CloudflareTunnelToken *string `json:"cloudflare_tunnel_token"`
}

// Exposed is one route of a machine, as its exposure adapters take it.
type Exposed struct {
	Service   string  `json:"service"`
	Container string  `json:"container"`
	Expose    string  `json:"expose"`
	LocalPort int     `json:"local_port"`
	Host      *string `json:"host"`
	HTTPSPort *int    `json:"https_port"`
}

// ExposureStatus is the body of PUT /v1/machines/{machine}/exposures/status: what the daemon
// knows about its exposures.
type ExposureStatus struct {
	TailscaleDNSName *string `json:"tailscale_dns_name"` // such as apps.tail1234.ts.net; null without Tailscale
}

// ExposeRoutes returns the routes of list exposed as typ, as its adapter takes them.
func ExposeRoutes(list []Exposed, typ string) []expose.Route {
	out := []expose.Route{}
	for _, r := range list {
		if r.Expose != typ {
			continue
		}
		er := expose.Route{Service: r.Service, Container: r.Container, LocalPort: r.LocalPort}
		if r.Host != nil {
			er.Host = *r.Host
		}
		if r.HTTPSPort != nil {
			er.HTTPSPort = *r.HTTPSPort
		}
		out = append(out, er)
	}
	return out
}
