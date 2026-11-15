package services

import (
	"context"
	"time"
)

// RuntimeDocker is the one built-in runtime: the Docker Engine API on a Unix socket. Podman's
// Docker-compatible socket speaks the same API.
const RuntimeDocker = "docker"

// runtimes are the runtimes this hub takes reports from.
var runtimes = map[string]bool{RuntimeDocker: true}

// Runtime is what the daemon runs services with. Each method fails with errNoDocker when the
// runtime isn't on this machine, errUnreachable when it doesn't answer and errDenied when the
// daemon may not use it, so the daemon tells an outage from a machine without the runtime.
type Runtime interface {
	// Name is the runtime's name in the service JSON, such as docker.
	Name() string
	// Ping checks that the runtime answers, within its own short timeout.
	Ping(ctx context.Context) error
	// Containers lists every service on the machine, running or not.
	Containers(ctx context.Context) ([]ServiceInput, error)
	// Stats reads one running service's memory and CPU times once.
	Stats(ctx context.Context, id string) (sample, error)
	// Act starts, stops or restarts the service called name.
	Act(ctx context.Context, typ, name string) error
	// Logs returns the newest lines of the service's output since then.
	Logs(ctx context.Context, name string, lines int, since time.Duration) (string, error)
}

// Name is docker.
func (d *Docker) Name() string { return RuntimeDocker }

var _ Runtime = (*Docker)(nil)
