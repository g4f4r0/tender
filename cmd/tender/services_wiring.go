package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/services"
)

// This file wires the services domain to the actions and notifications domains.

// newServices returns the services store with its secrets' key, which queues its actions through as.
func newServices(d *db.DB, key []byte, as *actions.Store) *services.Store {
	s := services.NewStore(d)
	s.Key = key
	if as != nil {
		s.Actions = serviceActions{as}
	}
	return s
}

// serviceActions carries the services domain's actions to the actions domain.
type serviceActions struct{ as *actions.Store }

func action(a actions.Action) services.Action {
	return services.Action{ID: a.ID, Status: a.Status, Output: a.Output, Action: a}
}

func (q serviceActions) Queue(ctx context.Context, machineID int64, typ, service, container string, deleteData bool) (services.Action, error) {
	if typ == actions.TypeDeploy || typ == actions.TypeRemove {
		a, err := q.as.CreateDeploy(ctx, machineID, typ, service, deleteData)
		return action(a), err
	}
	in := actions.Input{Type: typ, Service: &service}
	if container != "" {
		in.Container = &container
	}
	a, err := q.as.Create(ctx, machineID, in)
	return action(a), err
}

func (q serviceActions) Get(ctx context.Context, machineID, id int64) (services.Action, error) {
	a, err := q.as.Get(ctx, machineID, id)
	return action(a), err
}

func (q serviceActions) Await(ctx context.Context, machineID, id int64) (services.Action, error) {
	a, err := q.as.Await(ctx, machineID, id, actions.Wait)
	return action(a), err
}

// The services domain's events, besides the notifications domain's own service rules.
const eventServiceDeployFailed = "service.deploy_failed"

var serviceEvents = []notifications.EventType{
	{Name: eventServiceDeployFailed, Severities: warning, Title: "deploy failed", Tool: "get_service",
		Fires: "A deploy of a managed service failed: a rule of Tender's refused its compose file, compose failed, or its routes could " +
			"not be exposed. One problem per service; a service whose alerts are off has none.",
		Resolves: "A later deploy of the service succeeds, the service is removed, or its alerts are turned off."},
}

// deployFindings are the service.deploy_failed findings of machine, one per service whose last
// deploy failed.
func deployFindings(machine string, failures []services.Failure) []notifications.Finding {
	var out []notifications.Finding
	for _, f := range failures {
		why, _, _ := strings.Cut(f.Error, "\n")
		why = strings.TrimSuffix(strings.TrimPrefix(why, "Could not deploy "+f.Service+": "), ":")
		if !strings.HasSuffix(why, ".") {
			why += "."
		}
		out = append(out, notifications.Finding{Event: eventServiceDeployFailed, Severity: notifications.SeverityWarning, Service: f.Service,
			Message: fmt.Sprintf("The deploy of %s failed: %s See the whole reason with: tender services show %s %s",
				notifications.Where(machine, f.Service, ""), why, machine, f.Service)})
	}
	return out
}
