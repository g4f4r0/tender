package services

import (
	"context"
	"errors"
	"net/http"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type listInput struct {
	Machine string `json:"machine,omitempty" jsonschema:"only this machine's services, as list_machines names it; leave out for every machine's"`
}

type serviceInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, as list_services shows it"`
}

type alertsInput struct {
	Machine string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service string `json:"service" jsonschema:"service name, as list_services shows it"`
	Enabled bool   `json:"enabled" jsonschema:"true to get alerts for this service's containers, false to stop them and resolve its open problems"`
}

type deployInput struct {
	Machine string             `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Name    string             `json:"name" jsonschema:"the service's name: lowercase letters, digits and dashes, starting with a letter and not ending with a dash, at most 32. It is the compose project name"`
	Compose *string            `json:"compose,omitempty" jsonschema:"the text of the docker compose file; required for a new service, left out to keep the stored one"`
	Files   map[string]*string `json:"files,omitempty" jsonschema:"config files the compose file mounts, by path relative to it (such as config/nginx.conf), each with its text; merged into the stored ones, null removes one. At most 64 files and 1 MiB"`
	Env     map[string]*string `json:"env,omitempty" jsonschema:"values for the compose file's ${KEY} interpolation, written to the service's .env: keys in capitals, merged into the stored ones, null removes one. The hub stores them encrypted and shows them as ***"`
	Routes  *[]RouteInput      `json:"routes,omitempty" jsonschema:"every route of the service, replacing the stored ones; left out keeps them"`
}

type removeInput struct {
	Machine    string `json:"machine" jsonschema:"machine name, as list_machines shows it"`
	Service    string `json:"service" jsonschema:"service name, as list_services shows it"`
	DeleteData bool   `json:"delete_data,omitempty" jsonschema:"true deletes the service's folder and the data of its named volumes; false (the default) keeps it in /var/lib/tender/services/.removed/ on the machine"`
}

type registryInput struct {
	Name string `json:"name" jsonschema:"registry name, as list_registries shows it"`
}

type registryPatchInput struct {
	Name     string  `json:"name" jsonschema:"registry name, as list_registries shows it"`
	Host     *string `json:"host,omitempty" jsonschema:"the registry's host, such as ghcr.io"`
	Username *string `json:"username,omitempty" jsonschema:"the login name"`
	Password *string `json:"password,omitempty" jsonschema:"a new password or access token"`
}

// ServiceFields says what a service holds, for the tools that return one.
const ServiceFields = "A service is a compose project on a machine, or a container without one. It has id, machine, name, managed (true when " +
	"Tender deployed it from a compose file; false for one Tender only found running, which can be started, stopped and watched but not " +
	"deployed), state (running when every container that should run does, degraded when some do, stopped when none do, unknown while " +
	"Docker is unreachable), deploy_status (deploying, deployed, failed or removing; null when unmanaged), last_error (why the last " +
	"deploy or remove failed), deployed_at, action_id, compose, files, env (every key, each value ***), routes (each with container, port, " +
	"expose, host, https_port, local_port and url, where it answers), alerts_enabled (one switch for all its containers), containers, " +
	"created_at and updated_at. Each container has name (its compose service, such as web, with -2 and up for later replicas, or the " +
	"container name for a lone container), runtime, container_id, image, state (created, running, paused, restarting, exited, removing, " +
	"dead, unknown while Docker is unreachable, or removed once Docker no longer has it), health (healthy, unhealthy, starting, or null), " +
	"exit_code, restart_count, restart_policy, started_at, memory_bytes (less the page cache it can drop), memory_limit_bytes, " +
	"cpu_percent (100 is one whole CPU), stopped_by_tender (a Tender stop left it stopped, so it doesn't alert as down), removed_at, " +
	"created_at and updated_at. A removed container goes 7 days after it was removed, and an unmanaged service with its last one."

const deployWait = "It waits up to 60 seconds for the machine's daemon. A deploy that takes longer, such as one that pulls large images or " +
	"waits for health checks, comes back with the action pending or in_progress: check it with get_action, and the service with get_service."

// RegisterTools adds the services and registries tools to m. They use the same store methods as
// the REST routes. lookup resolves machine names to ids.
func (s *Store) RegisterTools(m *mcp.Server, lookup mcp.MachineLookup) {
	mcp.AddReadOnly(m, "list_services", "List services",
		"List the services on every machine or one, by machine: per machine, docker (running, unreachable when its daemon can't reach "+
			"the Docker engine, or null before any report and once Docker is turned off on the machine), docker_reason (while unreachable: "+
			"socket_missing, permission_denied, refused, timeout or error; else null) and data, its services by name. The daemon reports "+
			"the containers once a minute. "+ServiceFields,
		func(ctx context.Context, in listInput) (server.List[MachineServices], error) {
			var id int64
			if in.Machine != "" {
				var err error
				if id, err = mcp.Machine(ctx, lookup, in.Machine); err != nil {
					return server.List[MachineServices]{}, err
				}
			}
			list, err := s.Machines(ctx, id)
			return server.List[MachineServices]{Data: list}, err
		})
	mcp.AddReadOnly(m, "get_service", "Get a service", "Get one service on a machine by name. "+ServiceFields,
		func(ctx context.Context, in serviceInput) (Service, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Service{}, err
			}
			return s.Get(ctx, id, in.Service)
		})
	mcp.AddWrite(m, "deploy_service", "Deploy a service",
		"Create or change a managed service on a machine and deploy it with docker compose, through the machine's daemon. A new service "+
			"needs compose; for one that exists, the fields given change it and the others stay. A service Tender didn't deploy can't be "+
			"changed. The daemon checks the compose file first and refuses: build, ports (routes publish them), host network_mode, pid, ipc, "+
			"uts or userns_mode, privileged, devices, security_opt with unconfined, cap_add of ALL, SYS_ADMIN, SYS_MODULE, SYS_PTRACE, "+
			"SYS_RAWIO, DAC_READ_SEARCH or NET_ADMIN, bind mounts outside the service's folder, the Docker socket, external or driver-set "+
			"volumes, external networks, container_name, and a compose service without a memory limit (mem_limit). A refused rule is the "+
			"tool's error, naming the compose service and the fix. Named volumes are kept in the service's folder, so the machine's backup "+
			"has them. A route publishes a container's port on 127.0.0.1 only and exposes it: tailscale on the machine's tailnet name, "+
			"cloudflare or public at a host. The result is the service and the deploy action. "+ServiceFields+" A failed deploy is the "+
			"tool's error, with the end of compose's output; the previous containers stay as compose left them. "+deployWait,
		mcp.WriteOptions{Destructive: true, OpenWorld: true},
		func(ctx context.Context, in deployInput) (Queued, error) {
			id, err := mcp.Machine(ctx, lookup, in.Machine)
			if err != nil {
				return Queued{}, err
			}
			var q Queued
			current, err := s.row(ctx, id, in.Name)
			switch {
			// A service Tender only watches is created too: Create takes its name over once its
			// containers are down, and otherwise says how to take them down.
			case isNotFound(err), err == nil && !current.Managed:
				if in.Compose == nil {
					return Queued{}, server.Invalid("compose is required for a new service: the text of its docker compose file.")
				}
				create := Input{Name: in.Name, Compose: *in.Compose, Files: map[string]string{}, Env: map[string]string{}}
				for k, v := range in.Files {
					if v != nil {
						create.Files[k] = *v
					}
				}
				for k, v := range in.Env {
					if v != nil {
						create.Env[k] = *v
					}
				}
				if in.Routes != nil {
					create.Routes = *in.Routes
				}
				q, err = s.Create(ctx, id, create)
			case err != nil:
				return Queued{}, err
			default:
				q, err = s.Update(ctx, id, in.Name, Change{Compose: in.Compose, Files: in.Files, Env: in.Env, Routes: in.Routes})
			}
			if err != nil {
				return q, err
			}
			return s.await(ctx, id, q)
		})
	mcp.AddWrite(m, "redeploy_service", "Redeploy a service",
		"Deploy a managed service again as it is stored, through the machine's daemon: it pulls the images again, so a new image under the "+
			"same tag runs, and recreates the containers that changed. The result is the service and the deploy action. "+ServiceFields+
			" A failed deploy is the tool's error. "+deployWait,
		mcp.WriteOptions{Destructive: true, OpenWorld: true},
		func(ctx context.Context, in serviceInput) (Queued, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Queued{}, err
			}
			q, err := s.Redeploy(ctx, id, in.Service)
			if err != nil {
				return q, err
			}
			return s.await(ctx, id, q)
		})
	mcp.AddWrite(m, "remove_service", "Remove a service",
		"Remove a managed service from a machine, through its daemon: docker compose down stops and removes its containers, its routes "+
			"stop answering, and the hub forgets the service once that succeeded. Its folder, with the data of its named volumes, is kept in "+
			"/var/lib/tender/services/.removed/ on the machine, or deleted with delete_data true, which can't be undone. A service Tender "+
			"didn't deploy can't be removed. The result is the service and the remove action. A failed remove is the tool's error. "+deployWait,
		mcp.WriteOptions{Destructive: true},
		func(ctx context.Context, in removeInput) (Queued, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Queued{}, err
			}
			q, err := s.Remove(ctx, id, in.Service, in.DeleteData)
			if err != nil {
				return q, err
			}
			return s.await(ctx, id, q)
		})
	mcp.AddWrite(m, "set_service_alerts", "Turn a service's alerts on or off",
		"Turn alerts on or off for one service, all its containers together. Alerts are on for every new service: an email when a "+
			"container stops, keeps restarting, turns unhealthy or uses more than 90% of its memory limit, and when a deploy fails. Off, "+
			"its open problems resolve within a minute. The result is the service. "+ServiceFields,
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in alertsInput) (Service, error) {
			id, err := s.toolService(ctx, lookup, in.Machine, in.Service)
			if err != nil {
				return Service{}, err
			}
			return s.SetAlerts(ctx, id, in.Service, in.Enabled)
		})

	const registryFields = "A registry has id, name, host, username, password (always ***), password_set, created_at and updated_at."
	mcp.AddReadOnly(m, "list_registries", "List registries",
		"List the container registries managed services pull private images from, by name. A deploy uses the login of the registry "+
			"whose host an image is on. "+registryFields,
		func(ctx context.Context, _ struct{}) (server.List[Registry], error) {
			list, err := s.Registries(ctx)
			return server.List[Registry]{Data: list}, err
		})
	mcp.AddWrite(m, "add_registry", "Add a registry",
		"Add the login of a container registry, such as ghcr.io, for managed services to pull private images. The hub stores the "+
			"password encrypted and gives it only to a daemon that deploys a service. The password goes through this conversation; for "+
			"a real one, the owner is safer typing it into \"tender registries add\". "+registryFields,
		mcp.WriteOptions{}, s.AddRegistry)
	mcp.AddWrite(m, "update_registry", "Update a registry",
		"Change a registry's host, username or password; the fields left out stay. "+registryFields,
		mcp.WriteOptions{Idempotent: true},
		func(ctx context.Context, in registryPatchInput) (Registry, error) {
			return s.UpdateRegistry(ctx, in.Name, RegistryPatch{Host: in.Host, Username: in.Username, Password: in.Password})
		})
	mcp.AddWrite(m, "remove_registry", "Remove a registry",
		"Remove a registry's login. Services whose images are on it can no longer pull them, unless they are public. "+
			"The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in registryInput) (mcp.Removed, error) {
			if err := s.DeleteRegistry(ctx, in.Name); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: "registry " + in.Name}, nil
		})
}

func isNotFound(err error) bool {
	var e *server.Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// toolService resolves a tool's machine and checks its service exists, with errors that say how
// to find the right names.
func (s *Store) toolService(ctx context.Context, lookup mcp.MachineLookup, machine, service string) (int64, error) {
	id, err := mcp.Machine(ctx, lookup, machine)
	if err != nil {
		return 0, err
	}
	if service == "" {
		return 0, server.Invalid("service is required. Call list_services to see the names.")
	}
	if _, err := s.row(ctx, id, service); err != nil {
		if isNotFound(err) {
			return 0, server.NotFound("Service %q doesn't exist on %s. Call list_services to see the names.", service, machine)
		}
		return 0, err
	}
	return id, nil
}

// await waits for the service's action, then returns the service as it is and the action. A
// failed action is the error, with its output.
func (s *Store) await(ctx context.Context, machineID int64, q Queued) (Queued, error) {
	if q.actionID == 0 || s.Actions == nil {
		return q, nil
	}
	a, err := s.Actions.Await(ctx, machineID, q.actionID)
	if err != nil {
		return q, err
	}
	q.Action = a.Action
	if sv, err := s.Get(ctx, machineID, q.Service.Name); err == nil {
		q.Service = sv
	} else if !isNotFound(err) { // a remove that succeeded leaves no service
		return q, err
	}
	if a.Status == "failed" {
		detail := "The action failed."
		if a.Output != nil {
			detail = *a.Output
		}
		return q, &server.Error{Status: http.StatusBadGateway, Detail: detail}
	}
	return q, nil
}
