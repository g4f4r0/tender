package services

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/g4f4r0/tender/internal/apps/expose"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

// Actions is what the store needs from the actions domain: it queues a service's actions and reads
// how they went. cmd/tender wires it.
type Actions interface {
	// Queue queues an action of typ for the machine's service: a deploy or remove of a managed one,
	// or a start, stop or restart of it, or of its one container when container isn't empty.
	Queue(ctx context.Context, machineID int64, typ, service, container string, deleteData bool) (Action, error)
	// Get returns one of the machine's actions, or a 404 server.Error once the hub no longer has it.
	Get(ctx context.Context, machineID, id int64) (Action, error)
	// Await returns the action once it finished, or as it is after the tools' wait.
	Await(ctx context.Context, machineID, id int64) (Action, error)
}

// Tunnels is the hub's side of the cloudflare exposure: it keeps each machine's tunnel routing
// its cloudflare routes. CloudflareTunnels implements it; without one, cloudflare routes are
// refused.
type Tunnels interface {
	// Ready is a server.Error that says how to set them unless the hub has its Cloudflare settings.
	Ready(ctx context.Context) error
	// Apply makes the tunnel of machine route exactly routes, its cloudflare routes, with their DNS
	// records. It sets every route it can, and returns an *expose.RouteError for each one it
	// couldn't; any other error is Cloudflare failing as a whole. The tunnel it returns has the
	// token the machine's daemon runs it with, "" without routes, and no ID when Apply couldn't
	// tell.
	Apply(ctx context.Context, machine string, routes []expose.Route) (expose.Tunnel, error)
}

// tunnelSyncTimeout is how long one sync of a machine's tunnel may take, retries and all.
var tunnelSyncTimeout = time.Minute

// tunnelLocks holds a lock per machine id, so one sync of a machine's tunnel runs at a time: two
// at once could both make its tunnel, or an older one delete a newer one's DNS record.
var tunnelLocks sync.Map

func lockTunnel(machineID int64) func() {
	m, _ := tunnelLocks.LoadOrStore(machineID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// syncTunnel brings the machine's tunnel in Cloudflare up to date with the cloudflare routes its
// services have now, and stores the token its daemon runs the tunnel with. It reads the routes and
// applies them under the machine's lock, so the last sync applies the latest routes. It returns
// what failed for svc, which has or had cloudflare routes when uses, and logs the rest. It runs
// until its deadline even when ctx ends, so a sync isn't cut off halfway.
func (s *Store) syncTunnel(ctx context.Context, machineID int64, svc string, uses bool) error {
	if s.Tunnels == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tunnelSyncTimeout)
	defer cancel()
	unlock := lockTunnel(machineID)
	defer unlock()
	machine, err := s.machineName(ctx, machineID)
	if err != nil {
		return err
	}
	routes, err := s.machineRoutes(ctx, machineID)
	if err != nil {
		return err
	}
	tun, err := s.Tunnels.Apply(ctx, machine, deploy.ExposeRoutes(routes, deploy.ExposeCloudflare))
	if ctx.Err() != nil {
		err = fmt.Errorf("the hub's sync with Cloudflare took longer than %s, so it stopped. Deploy again later", tunnelSyncTimeout)
	} else if err == nil || tun.ID != "" {
		if serr := s.storeTunnelToken(ctx, machineID, tun.Token); serr != nil {
			err = errors.Join(err, serr)
		}
	}
	mine, others := expose.For(err, svc, uses)
	if others != nil {
		s.log().Warn("could not route all of the machine's cloudflare routes", "machine", machine, "error", others)
	}
	return mine
}

// storeTunnelToken keeps the machine's tunnel token, sealed, or forgets it when token is "".
func (s *Store) storeTunnelToken(ctx context.Context, machineID int64, token string) error {
	var sealed *string
	if token != "" {
		v, err := s.seal(token)
		if err != nil {
			return err
		}
		sealed = &v
	}
	now := time.Now().Unix()
	_, err := s.db.Write.ExecContext(ctx, `INSERT INTO service_exposures (machine_id, cloudflare_tunnel_token, created_at, updated_at) VALUES (?1, ?2, ?3, ?3)
		ON CONFLICT (machine_id) DO UPDATE SET cloudflare_tunnel_token = excluded.cloudflare_tunnel_token, updated_at = excluded.updated_at`,
		machineID, sealed, now)
	return err
}

// errNoKey is what a process without TENDER_ENCRYPTION_KEY answers when it needs a secret.
var errNoKey = &server.Error{Status: http.StatusServiceUnavailable,
	Detail: "This process has no TENDER_ENCRYPTION_KEY, so it can't store or read the services' env and the registry passwords. Set it to the hub's key and run the command again."}

func (s *Store) seal(v string) (string, error) {
	if len(s.Key) == 0 {
		return "", errNoKey
	}
	b, err := secret.Seal(s.Key, []byte(v))
	if err != nil {
		return "", fmt.Errorf("seal a secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) open(what, sealed string) (string, error) {
	if len(s.Key) == 0 {
		return "", errNoKey
	}
	b, err := hex.DecodeString(sealed)
	if err != nil {
		return "", fmt.Errorf("%s is damaged", what)
	}
	plain, err := secret.Open(s.Key, b)
	if err != nil {
		return "", fmt.Errorf("open %s: %w. Start the hub with the TENDER_ENCRYPTION_KEY it first stored it with", what, err)
	}
	return string(plain), nil
}

// storedRoute is a route with the service it belongs to.
type storedRoute struct {
	Route
	serviceID int64
}

func (s *Store) routes(ctx context.Context, where string, args ...any) ([]storedRoute, error) {
	rs, err := s.db.Read.QueryContext(ctx, `SELECT r.id, r.service_id, r.container, r.port, r.expose, r.host, r.https_port, r.local_port, r.created_at,
		r.updated_at FROM service_routes r WHERE `+where+` ORDER BY r.service_id, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []storedRoute
	for rs.Next() {
		var r storedRoute
		var created, updated int64
		if err := rs.Scan(&r.ID, &r.serviceID, &r.Container, &r.Port, &r.Expose, &r.Host, &r.HTTPSPort, &r.LocalPort, &created, &updated); err != nil {
			return nil, err
		}
		r.CreatedAt, r.UpdatedAt = at(created), at(updated)
		out = append(out, r)
	}
	return out, rs.Err()
}

// tailnetNames are the machines' names on their tailnet, by machine id, as their daemons said.
func (s *Store) tailnetNames(ctx context.Context) (map[int64]string, error) {
	rs, err := s.db.Read.QueryContext(ctx, `SELECT machine_id, tailscale_dns_name FROM service_exposures WHERE tailscale_dns_name IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[int64]string{}
	for rs.Next() {
		var id int64
		var name string
		if err := rs.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rs.Err()
}

// routeURL is where a route answers, without the port when it is 443, or nil for a tailscale route of a machine whose tailnet name
// isn't known yet.
func routeURL(r Route, tailnet string) *string {
	var u string
	switch {
	case r.Host != nil:
		u = "https://" + *r.Host
	case r.HTTPSPort != nil && tailnet != "" && *r.HTTPSPort == 443:
		u = "https://" + tailnet
	case r.HTTPSPort != nil && tailnet != "":
		u = "https://" + tailnet + ":" + strconv.Itoa(*r.HTTPSPort)
	default:
		return nil
	}
	return &u
}

// maxErrorTail is how much of the end of compose's output last_error keeps after its first line.
const maxErrorTail = 4 << 10

// lastError is what last_error keeps of a failed action's output: its first line, the sentence
// that says why, and the end of the rest.
func lastError(output *string) string {
	if output == nil || *output == "" {
		return "The action failed without saying why."
	}
	first, rest, _ := strings.Cut(*output, "\n")
	if len(rest) > maxErrorTail {
		rest = rest[len(rest)-maxErrorTail:]
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		}
		rest = "...\n" + rest
	}
	if rest == "" {
		return first
	}
	return first + "\n" + rest
}

// reconcile brings the deploy status of the machine's managed services (every machine's with
// machineID 0) in line with their latest action: a finished deploy leaves the service deployed or
// failed, and a finished remove deletes it or leaves it failed. A failed service is looked at too,
// so a daemon's late report that the action succeeded still counts.
func (s *Store) reconcile(ctx context.Context, machineID int64) error {
	if s.Actions == nil {
		return nil
	}
	rs, err := s.db.Read.QueryContext(ctx, `SELECT id, machine_id, deploy_status, action_id, action_type, last_error FROM services
		WHERE (?1 = 0 OR machine_id = ?1) AND action_id IS NOT NULL AND deploy_status IN ('deploying', 'removing', 'failed')`, machineID)
	if err != nil {
		return err
	}
	type pending struct {
		id, machineID, actionID int64
		status, actionType      string
		lastError               *string
	}
	var list []pending
	for rs.Next() {
		var p pending
		if err := rs.Scan(&p.id, &p.machineID, &p.status, &p.actionID, &p.actionType, &p.lastError); err != nil {
			return errors.Join(err, rs.Close())
		}
		list = append(list, p)
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return err
	}
	now := time.Now().Unix()
	resync := map[int64]bool{}
	for _, p := range list {
		q, err := s.Actions.Get(ctx, p.machineID, p.actionID)
		var e *server.Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			if p.status == DeployFailed {
				continue
			}
			gone := "The hub no longer has this service's last action, so it can't say how it ended. Deploy the service again."
			q, err = Action{ID: p.actionID, Status: "failed", Output: &gone}, nil
		}
		if err != nil {
			return err
		}
		switch q.Status {
		case "completed":
			if p.actionType == ActionRemove {
				_, err = s.db.Write.ExecContext(ctx, `DELETE FROM services WHERE id = ? AND action_id = ?`, p.id, p.actionID)
			} else if p.status != DeployDeployed {
				_, err = s.db.Write.ExecContext(ctx, `UPDATE services SET deploy_status = 'deployed', last_error = NULL, deployed_at = ?2, updated_at = ?2
					WHERE id = ?1 AND action_id = ?3`, p.id, now, p.actionID)
			}
		case "failed":
			msg := lastError(q.Output)
			if p.status == DeployFailed && p.lastError != nil && *p.lastError == msg {
				continue
			}
			_, err = s.db.Write.ExecContext(ctx, `UPDATE services SET deploy_status = 'failed', last_error = ?2, updated_at = ?3 WHERE id = ?1 AND action_id = ?4`,
				p.id, msg, now, p.actionID)
			if p.actionType == ActionRemove && p.status == DeployRemoving {
				resync[p.machineID] = true
			}
		}
		if err != nil {
			return err
		}
	}
	// A service whose remove failed is still there, maybe with its containers running: its
	// cloudflare routes, which left the tunnel when the remove was queued, come back.
	for id := range resync {
		_ = s.syncTunnel(ctx, id, "", false)
	}
	return nil
}

// Create stores a new managed service on the machine and queues its deploy.
func (s *Store) Create(ctx context.Context, machineID int64, in Input) (Queued, error) {
	if err := deploy.CheckName(in.Name); err != nil {
		return Queued{}, err
	}
	if err := checkInput(in.Compose, in.Files, in.Env, in.Routes); err != nil {
		return Queued{}, err
	}
	if err := s.canRoute(ctx, in.Routes); err != nil {
		return Queued{}, err
	}
	env, err := s.sealEnv(nil, in.Env, false)
	if err != nil {
		return Queued{}, err
	}
	files := in.Files
	if files == nil {
		files = map[string]string{}
	}
	filesJSON, _ := json.Marshal(files)
	envJSON, _ := json.Marshal(env)
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Queued{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO services (machine_id, name, managed, compose, files, env, deploy_status, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, 'deploying', ?, ?) ON CONFLICT (machine_id, name) DO NOTHING RETURNING id`,
		machineID, in.Name, in.Compose, string(filesJSON), string(envJSON), now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var managed bool
		if err := tx.QueryRowContext(ctx, `SELECT managed FROM services WHERE machine_id = ? AND name = ?`, machineID, in.Name).Scan(&managed); err != nil {
			return Queued{}, err
		}
		if !managed {
			return Queued{}, server.Conflict("Service %q already runs on this machine, and Tender didn't deploy it. Take its containers down "+
				"(docker compose -p %s down) before Tender deploys it, or give the new service another name.", in.Name, in.Name)
		}
		return Queued{}, server.Conflict("Service %q already exists on this machine. Change it with PATCH, or tender services deploy, which updates it.", in.Name)
	}
	if err != nil {
		return Queued{}, err
	}
	if err := saveRoutes(ctx, tx, machineID, id, in.Routes, now); err != nil {
		return Queued{}, err
	}
	if err := tx.Commit(); err != nil {
		return Queued{}, err
	}
	q, err := s.queue(ctx, machineID, in.Name, ActionDeploy, "", false, false)
	if err != nil {
		// Without its deploy the new service would wait for nothing, and its routes go with it.
		_, derr := s.db.Write.ExecContext(context.WithoutCancel(ctx), `DELETE FROM services WHERE id = ?`, id)
		if derr == nil {
			_ = s.syncTunnel(ctx, machineID, "", false)
		}
		return Queued{}, errors.Join(err, derr)
	}
	return q, nil
}

func checkInput(compose string, files, env map[string]string, routes []RouteInput) error {
	if err := deploy.CheckCompose(compose); err != nil {
		return err
	}
	if err := deploy.CheckFiles(files); err != nil {
		return err
	}
	if err := deploy.CheckEnv(env); err != nil {
		return err
	}
	return checkRoutes(routes)
}

// checkRoutes checks routes on their own; saveRoutes checks them against the machine's others.
func checkRoutes(routes []RouteInput) error {
	if len(routes) > deploy.MaxRoutes {
		return server.Invalid("routes has %d routes; a service may have at most %d.", len(routes), deploy.MaxRoutes)
	}
	hosts := map[string]bool{}
	for i, r := range routes {
		at := func(format string, a ...any) error {
			return server.Invalid("routes[%d]: "+format, append([]any{i}, a...)...)
		}
		switch {
		case !deploy.ValidContainer(r.Container):
			return at("container must be the name of a service in the compose file, got %q.", r.Container)
		case r.Port < 1 || r.Port > 65535:
			return at("port must be the container's port, from 1 to 65535.")
		}
		switch r.Expose {
		case deploy.ExposeTailscale:
			if r.Host != nil {
				return at("a tailscale route has no host: it answers on the machine's tailnet name. Leave host out.")
			}
			if r.HTTPSPort != nil && (*r.HTTPSPort < 1 || *r.HTTPSPort > 65535) {
				return at("https_port must be from 1 to 65535, or left out for the hub to pick one.")
			}
		case deploy.ExposeCloudflare, deploy.ExposePublic:
			if r.HTTPSPort != nil {
				return at("https_port is only for a tailscale route; a %s route answers on 443 at its host.", r.Expose)
			}
			if r.Host == nil || !deploy.ValidHost(*r.Host) {
				return at("a %s route needs host, a full hostname such as app.example.com, in lowercase.", r.Expose)
			}
			if hosts[*r.Host] {
				return at("host %s is in two routes; a host can route to one container only.", *r.Host)
			}
			hosts[*r.Host] = true
		default:
			return at("expose must be tailscale, cloudflare or public, got %q.", r.Expose)
		}
	}
	return nil
}

// canRoute refuses cloudflare routes on a hub without the cloudflare exposure, or without its
// Cloudflare settings.
func (s *Store) canRoute(ctx context.Context, routes []RouteInput) error {
	for _, r := range routes {
		if r.Expose != deploy.ExposeCloudflare {
			continue
		}
		if s.Tunnels == nil {
			return server.Invalid("This hub can't route through Cloudflare yet: set it up with PUT /v1/exposures/cloudflare first.")
		}
		return s.Tunnels.Ready(ctx)
	}
	return nil
}

// sealEnv returns the stored env after setting the values in set: a new value is sealed, and
// "***" keeps the stored one when keep is true. old is the stored, sealed env.
func (s *Store) sealEnv(old map[string]string, set map[string]string, keep bool) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range old {
		out[k] = v
	}
	for k, v := range set {
		if keep && v == deploy.Hidden {
			if _, ok := old[k]; !ok {
				return nil, server.Invalid("env: %s is \"***\", which is how the hub hides a value, but the service has no %s to keep. Give the value itself.", k, k)
			}
			continue
		}
		sealed, err := s.seal(v)
		if err != nil {
			return nil, err
		}
		out[k] = sealed
	}
	return out, nil
}

// routeKey says whether two routes are the same, so a change keeps a route's ports.
type routeKey struct {
	container, expose, host string
	port                    int
}

func keyOf(container string, port int, expose string, host *string) routeKey {
	k := routeKey{container: container, port: port, expose: expose}
	if host != nil {
		k.host = *host
	}
	return k
}

// The ports the hub assigns: 127.0.0.1 ports per machine for the containers, and the lowest free
// tailnet HTTPS port from httpsFrom when a tailscale route leaves it out.
const (
	localFrom, localTo = 20000, 29999
	httpsFrom          = 10000
)

// lastLocalPort is the local port the hub assigned last on the machine, or the highest one its
// routes have when that is higher, or localFrom-1 when it has assigned none.
func lastLocalPort(ctx context.Context, tx *sql.Tx, machineID int64) (int, error) {
	var last int
	err := tx.QueryRowContext(ctx, `SELECT max(coalesce((SELECT last_local_port FROM service_exposures WHERE machine_id = ?1), ?2),
		coalesce((SELECT max(r.local_port) FROM service_routes r JOIN services s ON s.id = r.service_id WHERE s.machine_id = ?1), ?2))`,
		machineID, localFrom-1).Scan(&last)
	return last, err
}

// nextLocalPort is the first free local port after last, or, once localTo is reached, the lowest
// free one. A port a route just let go of isn't handed out again right away: a failed deploy can
// leave the machine's exposures pointing at it.
func nextLocalPort(last int, used map[int]bool) (int, bool) {
	for p := last + 1; p <= localTo; p++ {
		if !used[p] {
			return p, true
		}
	}
	for p := localFrom; p <= last && p <= localTo; p++ {
		if !used[p] {
			return p, true
		}
	}
	return 0, false
}

// saveRoutes replaces the service's routes with routes. A route the service had keeps its local
// port, and its HTTPS port unless it gives another. A new route gets the next free local port after
// the last one the machine got, and the lowest free HTTPS port. A host is unique across the hub,
// and a local or HTTPS port across the machine's routes.
func saveRoutes(ctx context.Context, tx *sql.Tx, machineID, serviceID int64, routes []RouteInput, now int64) error {
	type kept struct {
		local     int
		https     *int
		createdAt int64
	}
	old := map[routeKey]kept{}
	rs, err := tx.QueryContext(ctx, `SELECT container, port, expose, host, https_port, local_port, created_at FROM service_routes WHERE service_id = ?`, serviceID)
	if err != nil {
		return err
	}
	for rs.Next() {
		var container, expose string
		var host *string
		var port int
		var k kept
		if err := rs.Scan(&container, &port, &expose, &host, &k.https, &k.local, &k.createdAt); err != nil {
			return errors.Join(err, rs.Close())
		}
		old[keyOf(container, port, expose, host)] = k
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return err
	}
	usedLocal, usedHTTPS := map[int]bool{}, map[int]string{}
	rs, err = tx.QueryContext(ctx, `SELECT s.name, r.local_port, r.https_port FROM service_routes r JOIN services s ON s.id = r.service_id
		WHERE s.machine_id = ? AND s.id != ?`, machineID, serviceID)
	if err != nil {
		return err
	}
	for rs.Next() {
		var name string
		var local int
		var https *int
		if err := rs.Scan(&name, &local, &https); err != nil {
			return errors.Join(err, rs.Close())
		}
		usedLocal[local] = true
		if https != nil {
			usedHTTPS[*https] = name
		}
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return err
	}
	type planned struct {
		RouteInput
		local     int
		createdAt int64
	}
	plan := make([]planned, len(routes))
	// First the routes the service keeps and the ports asked for, so a new route can't take them.
	for i, r := range routes {
		plan[i] = planned{RouteInput: r, createdAt: now}
		if r.Host != nil {
			var name, machine string
			err := tx.QueryRowContext(ctx, `SELECT s.name, m.name FROM service_routes r JOIN services s ON s.id = r.service_id JOIN machines m ON m.id = s.machine_id
				WHERE r.host = ? AND s.id != ?`, *r.Host, serviceID).Scan(&name, &machine)
			if err == nil {
				return server.Conflict("Host %s already routes to service %s on %s. A host can route to one service only.", *r.Host, name, machine)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if k, ok := old[keyOf(r.Container, r.Port, r.Expose, r.Host)]; ok {
			plan[i].local, plan[i].createdAt = k.local, k.createdAt
			usedLocal[k.local] = true
			if r.Expose == deploy.ExposeTailscale && r.HTTPSPort == nil {
				plan[i].HTTPSPort = k.https
			}
		}
		if p := plan[i].HTTPSPort; p != nil {
			if name, taken := usedHTTPS[*p]; taken {
				if name == "" {
					return server.Conflict("routes[%d]: https_port %d is in another route of this service. Each tailscale route needs its own port.", i, *p)
				}
				return server.Conflict("routes[%d]: https_port %d is taken by service %s on this machine. Pick another, or leave it out for the hub to pick one.", i, *p, name)
			}
			usedHTTPS[*p] = ""
		}
	}
	last, err := lastLocalPort(ctx, tx, machineID)
	if err != nil {
		return err
	}
	for i := range plan {
		if plan[i].local == 0 {
			p, ok := nextLocalPort(last, usedLocal)
			if !ok {
				return server.Conflict("This machine has no free local port left from %d to %d for another route.", localFrom, localTo)
			}
			usedLocal[p], plan[i].local, last = true, p, p
		}
		if plan[i].Expose == deploy.ExposeTailscale && plan[i].HTTPSPort == nil {
			p := httpsFrom
			for ; p <= 65535; p++ {
				if _, taken := usedHTTPS[p]; !taken {
					break
				}
			}
			if p > 65535 {
				return server.Conflict("This machine has no free tailnet HTTPS port left.")
			}
			usedHTTPS[p], plan[i].HTTPSPort = "", &p
		}
	}
	if last >= localFrom {
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_exposures (machine_id, last_local_port, created_at, updated_at) VALUES (?1, ?2, ?3, ?3)
			ON CONFLICT (machine_id) DO UPDATE SET last_local_port = excluded.last_local_port, updated_at = excluded.updated_at`, machineID, last, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_routes WHERE service_id = ?`, serviceID); err != nil {
		return err
	}
	for _, p := range plan {
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_routes (service_id, container, port, expose, host, https_port, local_port, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, serviceID, p.Container, p.Port, p.Expose, p.Host, p.HTTPSPort, p.local, p.createdAt, now); err != nil {
			return err
		}
	}
	return nil
}

// queue queues an action for the service and returns both. A deploy or remove marks the managed
// service deploying or removing first, then brings the machine's tunnel up to date with its routes
// as they will be, keeping what failed for the service, and only then queues the action: the
// daemon's poll only reads what the sync left. hadCloudflare says the service had cloudflare
// routes before this change, so dropping its last one still fails when Cloudflare does.
func (s *Store) queue(ctx context.Context, machineID int64, name, typ, container string, deleteData, hadCloudflare bool) (Queued, error) {
	if s.Actions == nil {
		return Queued{}, server.Invalid("This hub can't queue actions for services.")
	}
	deployOrRemove := typ == ActionDeploy || typ == ActionRemove
	var undo func()
	if deployOrRemove {
		var err error
		if undo, err = s.prepare(ctx, machineID, name, typ, hadCloudflare); err != nil {
			return Queued{}, err
		}
	}
	q, err := s.Actions.Queue(ctx, machineID, typ, name, container, deleteData)
	if err != nil {
		if undo != nil {
			undo()
		}
		return Queued{}, err
	}
	if deployOrRemove {
		if _, err := s.db.Write.ExecContext(ctx, `UPDATE services SET action_id = ?, updated_at = ? WHERE machine_id = ? AND name = ?`,
			q.ID, time.Now().Unix(), machineID, name); err != nil {
			return Queued{}, err
		}
	}
	sv, err := s.row(ctx, machineID, name)
	if err != nil {
		return Queued{}, err
	}
	return Queued{Service: sv, Action: q.Action, actionID: q.ID}, nil
}

// prepare marks the service deploying or removing, with no action yet, so a remove's routes leave
// the machine's and reconcile leaves the service be. Then it syncs the machine's tunnel and keeps
// what failed for the service in cloudflare_error, for its deploy or remove. undo puts the service
// back as it was and syncs again, for an action that couldn't be queued.
func (s *Store) prepare(ctx context.Context, machineID int64, name, typ string, hadCloudflare bool) (undo func(), err error) {
	var id int64
	var status string
	var actionID *int64
	var actionType *string
	var hasCloudflare bool
	if err := s.db.Read.QueryRowContext(ctx, `SELECT id, deploy_status, action_id, action_type,
		EXISTS (SELECT 1 FROM service_routes r WHERE r.service_id = services.id AND r.expose = 'cloudflare')
		FROM services WHERE machine_id = ? AND name = ?`, machineID, name).Scan(&id, &status, &actionID, &actionType, &hasCloudflare); err != nil {
		return nil, err
	}
	next := DeployDeploying
	if typ == ActionRemove {
		next = DeployRemoving
	}
	if _, err := s.db.Write.ExecContext(ctx, `UPDATE services SET deploy_status = ?, action_id = NULL, action_type = ?, updated_at = ? WHERE id = ?`,
		next, typ, time.Now().Unix(), id); err != nil {
		return nil, err
	}
	var cloudflareError *string
	if err := s.syncTunnel(ctx, machineID, name, hadCloudflare || hasCloudflare); err != nil {
		msg := err.Error()
		cloudflareError = &msg
	}
	if _, err := s.db.Write.ExecContext(context.WithoutCancel(ctx), `UPDATE services SET cloudflare_error = ? WHERE id = ?`, cloudflareError, id); err != nil {
		return nil, err
	}
	return func() {
		ctx := context.WithoutCancel(ctx)
		if _, err := s.db.Write.ExecContext(ctx, `UPDATE services SET deploy_status = ?, action_id = ?, action_type = ?, updated_at = ? WHERE id = ?`,
			status, actionID, actionType, time.Now().Unix(), id); err != nil {
			s.log().Warn("could not put the service back as it was before its action", "service", name, "error", err)
		}
		_ = s.syncTunnel(ctx, machineID, "", false)
	}, nil
}

// unmanaged is the 409 of a change only a managed service can have.
func unmanaged(name, what string) error {
	return server.Conflict("Tender didn't deploy service %q, so it can't %s it: it only watches, starts, stops and restarts it. "+
		"To have Tender deploy it, take it down and deploy it from its compose file under a name of its own.", name, what)
}

// changeable returns the managed service, reconciled, unless it is being removed. what says what
// the caller does to it, for the error when it isn't managed.
func (s *Store) changeable(ctx context.Context, machineID int64, name, what string) (Service, error) {
	if err := s.reconcile(ctx, machineID); err != nil {
		return Service{}, err
	}
	sv, err := s.row(ctx, machineID, name)
	if err != nil {
		return Service{}, err
	}
	if !sv.Managed {
		return Service{}, unmanaged(name, what)
	}
	if sv.DeployStatus != nil && *sv.DeployStatus == DeployRemoving {
		return Service{}, server.Conflict("Service %q is being removed, so it can't change now. Wait for its remove, action %d, to finish.", name, deref(sv.ActionID))
	}
	return sv, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// Update applies a change to one of the machine's managed services and queues its deploy.
func (s *Store) Update(ctx context.Context, machineID int64, name string, c Change) (Queued, error) {
	// The name's rules come first, so tender, the project of Tender's own containers, says why it
	// is refused rather than that Tender didn't deploy it.
	if err := deploy.CheckName(name); err != nil {
		return Queued{}, err
	}
	sv, err := s.changeable(ctx, machineID, name, "change")
	if err != nil {
		return Queued{}, err
	}
	compose := *sv.Compose
	if c.Compose != nil {
		compose = *c.Compose
	}
	files := map[string]string{}
	if !c.ClearFiles {
		for k, v := range sv.Files {
			files[k] = v
		}
	}
	for k, v := range c.Files {
		if v == nil {
			delete(files, k)
		} else {
			files[k] = *v
		}
	}
	sealed := sv.sealedEnv
	if c.ClearEnv {
		sealed = map[string]string{}
	}
	set, plainCheck := map[string]string{}, map[string]string{}
	var drop []string
	for k, v := range c.Env {
		if v == nil {
			drop = append(drop, k)
			continue
		}
		set[k] = *v
		if *v != deploy.Hidden {
			plainCheck[k] = *v
		}
	}
	routes := make([]RouteInput, len(sv.Routes))
	for i, r := range sv.Routes {
		routes[i] = RouteInput{Container: r.Container, Port: r.Port, Expose: r.Expose, Host: r.Host, HTTPSPort: r.HTTPSPort}
	}
	if c.Routes != nil {
		routes = *c.Routes
	}
	if err := checkInput(compose, files, plainCheck, routes); err != nil {
		return Queued{}, err
	}
	if err := s.canRoute(ctx, routes); err != nil {
		return Queued{}, err
	}
	env, err := s.sealEnv(sealed, set, true)
	if err != nil {
		return Queued{}, err
	}
	for _, k := range drop {
		delete(env, k)
	}
	if err := deploy.CheckEnvKeys(len(env)); err != nil {
		return Queued{}, err
	}
	filesJSON, _ := json.Marshal(files)
	envJSON, _ := json.Marshal(env)
	tx, err := s.db.Write.BeginTx(ctx, nil)
	if err != nil {
		return Queued{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE services SET compose = ?, files = ?, env = ?, updated_at = ? WHERE id = ?`,
		compose, string(filesJSON), string(envJSON), now, sv.ID); err != nil {
		return Queued{}, err
	}
	if c.Routes != nil {
		if err := saveRoutes(ctx, tx, machineID, sv.ID, routes, now); err != nil {
			return Queued{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Queued{}, err
	}
	hadCloudflare := slices.ContainsFunc(sv.Routes, func(r Route) bool { return r.Expose == deploy.ExposeCloudflare })
	return s.queue(ctx, machineID, name, ActionDeploy, "", false, hadCloudflare)
}

// Redeploy queues a deploy of the managed service as stored, which pulls its images again.
func (s *Store) Redeploy(ctx context.Context, machineID int64, name string) (Queued, error) {
	if err := deploy.CheckName(name); err != nil {
		return Queued{}, err
	}
	if _, err := s.changeable(ctx, machineID, name, "deploy"); err != nil {
		return Queued{}, err
	}
	return s.queue(ctx, machineID, name, ActionDeploy, "", false, false)
}

// Remove queues the managed service's remove. The service is deleted once the remove succeeds.
// With deleteData the daemon deletes the service's folder and its data; without, it keeps it aside.
func (s *Store) Remove(ctx context.Context, machineID int64, name string, deleteData bool) (Queued, error) {
	if _, err := s.changeable(ctx, machineID, name, "remove"); err != nil {
		return Queued{}, err
	}
	return s.queue(ctx, machineID, name, ActionRemove, "", deleteData, false)
}

// Act queues a start, stop or restart of the service, or of its one container when container
// isn't empty: managed or not, the daemon does it.
func (s *Store) Act(ctx context.Context, machineID int64, name, typ, container string) (Queued, error) {
	if _, err := s.row(ctx, machineID, name); err != nil {
		return Queued{}, err
	}
	return s.queue(ctx, machineID, name, typ, container, false, false)
}

// Exposures returns the machine's whole set of routes, for its daemon's exposure adapters: the
// routes of every managed service on it except those being removed, and the token of its
// Cloudflare tunnel, as the last sync stored it, when it has cloudflare routes. It only reads: the
// sync runs when a deploy or remove is queued. A token the hub can't open is logged, and the
// exposures go without it.
func (s *Store) Exposures(ctx context.Context, machineID int64) (deploy.Exposures, error) {
	routes, err := s.machineRoutes(ctx, machineID)
	if err != nil {
		return deploy.Exposures{}, err
	}
	out := deploy.Exposures{Routes: routes}
	if len(deploy.ExposeRoutes(routes, deploy.ExposeCloudflare)) == 0 {
		return out, nil
	}
	var sealed sql.NullString
	err = s.db.Read.QueryRowContext(ctx, `SELECT cloudflare_tunnel_token FROM service_exposures WHERE machine_id = ?`, machineID).Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if !sealed.Valid {
		return out, nil
	}
	token, err := s.open("the machine's Cloudflare tunnel token", sealed.String)
	if err != nil {
		s.log().Warn("could not open the machine's Cloudflare tunnel token", "machine_id", machineID, "error", err)
		return out, nil
	}
	out.CloudflareTunnelToken = &token
	return out, nil
}

func (s *Store) machineName(ctx context.Context, machineID int64) (string, error) {
	var machine string
	err := s.db.Read.QueryRowContext(ctx, `SELECT name FROM machines WHERE id = ?`, machineID).Scan(&machine)
	return machine, err
}

// machineRoutes returns the routes of every managed service on the machine, except those being
// removed.
func (s *Store) machineRoutes(ctx context.Context, machineID int64) ([]deploy.Exposed, error) {
	out := []deploy.Exposed{}
	rs, err := s.db.Read.QueryContext(ctx, `SELECT s.name, r.container, r.expose, r.local_port, r.host, r.https_port
		FROM service_routes r JOIN services s ON s.id = r.service_id
		WHERE s.machine_id = ? AND s.deploy_status != 'removing' ORDER BY s.name, r.id`, machineID)
	if err != nil {
		return out, err
	}
	for rs.Next() {
		var e deploy.Exposed
		if err := rs.Scan(&e.Service, &e.Container, &e.Expose, &e.LocalPort, &e.Host, &e.HTTPSPort); err != nil {
			return out, errors.Join(err, rs.Close())
		}
		out = append(out, e)
	}
	return out, errors.Join(rs.Err(), rs.Close())
}

// Deployment returns what the daemon needs to deploy or remove the machine's managed service, as
// JSON: the service with its env opened, the registries' logins and the machine's exposures. It
// runs in the daemon's poll, so it only reads: what the tunnel's sync, when the action was queued,
// couldn't do for this service's cloudflare routes goes to the daemon as cloudflare_error, which
// fails the deploy or remove once the containers are done.
func (s *Store) Deployment(ctx context.Context, machineID int64, name string) (json.RawMessage, error) {
	sv, err := s.row(ctx, machineID, name)
	if err != nil {
		return nil, err
	}
	if !sv.Managed {
		return nil, unmanaged(name, "deploy")
	}
	d := deploy.Deployment{Name: sv.Name, Compose: *sv.Compose, Files: sv.Files, Env: map[string]string{}, Routes: []deploy.Published{}}
	for k, v := range sv.sealedEnv {
		if d.Env[k], err = s.open("service "+sv.Name+"'s "+k, v); err != nil {
			return nil, err
		}
	}
	for _, r := range sv.Routes {
		d.Routes = append(d.Routes, deploy.Published{Container: r.Container, Port: r.Port, LocalPort: r.LocalPort, Expose: r.Expose})
	}
	if d.Registries, err = s.logins(ctx); err != nil {
		return nil, err
	}
	var cloudflareError sql.NullString
	if err := s.db.Read.QueryRowContext(ctx, `SELECT cloudflare_error FROM services WHERE id = ?`, sv.ID).Scan(&cloudflareError); err != nil {
		return nil, err
	}
	d.CloudflareError = cloudflareError.String
	if d.Exposures, err = s.Exposures(ctx, machineID); err != nil {
		return nil, err
	}
	return json.Marshal(d)
}

// SetExposureStatus keeps what the machine's daemon said about its exposures.
func (s *Store) SetExposureStatus(ctx context.Context, machineID int64, in deploy.ExposureStatus) error {
	if in.TailscaleDNSName != nil {
		name := strings.TrimSuffix(*in.TailscaleDNSName, ".")
		if !deploy.ValidHost(name) {
			return server.Invalid("tailscale_dns_name must be a hostname such as apps.tail1234.ts.net.")
		}
		in.TailscaleDNSName = &name
	}
	now := time.Now().Unix()
	_, err := s.db.Write.ExecContext(ctx, `INSERT INTO service_exposures (machine_id, tailscale_dns_name, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (machine_id) DO UPDATE SET tailscale_dns_name = excluded.tailscale_dns_name, updated_at = excluded.updated_at`,
		machineID, in.TailscaleDNSName, now, now)
	return err
}

// Failure is a managed service whose last deploy failed.
type Failure struct {
	Service string
	Error   string
}

// Failures returns, by machine id, the managed services whose last deploy failed, for
// service.deploy_failed.
func (s *Store) Failures(ctx context.Context) (map[int64][]Failure, error) {
	if err := s.reconcile(ctx, 0); err != nil {
		return nil, err
	}
	rs, err := s.db.Read.QueryContext(ctx, `SELECT machine_id, name, coalesce(last_error, '') FROM services
		WHERE deploy_status = 'failed' AND action_type = 'deploy' AND alerts_enabled = 1 ORDER BY machine_id, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[int64][]Failure{}
	for rs.Next() {
		var id int64
		var f Failure
		if err := rs.Scan(&id, &f.Service, &f.Error); err != nil {
			return nil, err
		}
		out[id] = append(out[id], f)
	}
	return out, rs.Err()
}

// CheckKey is an error unless the store's key opens the secrets stored with the services and the
// registries.
func (s *Store) CheckKey(ctx context.Context) error {
	var env, password sql.NullString
	if err := s.db.Read.QueryRowContext(ctx, `SELECT (SELECT env FROM services WHERE env != '{}' LIMIT 1), (SELECT password FROM registries LIMIT 1)`).
		Scan(&env, &password); err != nil {
		return err
	}
	if password.Valid {
		if _, err := s.open("a registry's password", password.String); err != nil {
			return err
		}
	}
	if env.Valid {
		var sealed map[string]string
		if err := json.Unmarshal([]byte(env.String), &sealed); err != nil {
			return err
		}
		for k, v := range sealed {
			if _, err := s.open("a service's "+k, v); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// CloudflareRoutes counts the cloudflare routes of every service, which need the hub's Cloudflare
// settings.
func (s *Store) CloudflareRoutes(ctx context.Context) (int, error) {
	var n int
	err := s.db.Read.QueryRowContext(ctx, `SELECT count(*) FROM service_routes WHERE expose = 'cloudflare'`).Scan(&n)
	return n, err
}

// PublicMachines returns the IDs of the machines that have public routes, except those of services
// being removed: their Caddy listens on 80/tcp and 443/tcp.
func (s *Store) PublicMachines(ctx context.Context) (map[int64]bool, error) {
	rs, err := s.db.Read.QueryContext(ctx, `SELECT DISTINCT s.machine_id FROM service_routes r JOIN services s ON s.id = r.service_id
		WHERE r.expose = 'public' AND s.deploy_status != 'removing'`)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			return nil, errors.Join(err, rs.Close())
		}
		out[id] = true
	}
	return out, errors.Join(rs.Err(), rs.Close())
}
