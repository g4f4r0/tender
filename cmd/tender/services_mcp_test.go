package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// fakeDaemon takes g4f4r0-code's actions from the store and finishes them as finish says, until
// the test ends.
func fakeDaemon(t *testing.T, ss *services.Store, finish func(services.Action) services.ActionPatch) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			list, err := ss.Wait(ctx, 1, time.Second, nil)
			if err != nil {
				return
			}
			for _, a := range list {
				_ = ss.Finish(ctx, 1, a.ID, finish(a))
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func TestServiceTools(t *testing.T) {
	d := seeded(t)
	ss := services.NewStore(d)
	ctx := context.Background()
	if _, err := ss.Replace(ctx, 1, []services.ServiceInput{
		{Name: "cli-proxy-api", ContainerID: "c1", Image: "cpa", State: "running"},
		{Name: "worker", ContainerID: "c2", Image: "w", State: "exited", ExitCode: ptr(int64(1))},
	}); err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, ss, func(a services.Action) services.ActionPatch {
		switch {
		case a.Service == "worker":
			return services.ActionPatch{Status: services.StatusFailed, Output: ptr("Could not " + a.Type + " worker: Docker answered 500: boom.")}
		case a.Type == services.TypeLogs:
			return services.ActionPatch{Status: services.StatusCompleted, Output: ptr(strings.Repeat("log line\n", *a.Lines))}
		}
		return services.ActionPatch{Status: services.StatusCompleted}
	})
	cs, rest := connect(t, d), newHub(d, testKey, nil, quiet)

	var list server.List[services.MachineServices]
	structured(t, callTool(t, cs, "list_services", nil), &list)
	if want := restJSON(t, rest, "/v1/services"); len(list.Data) != 1 || len(list.Data[0].Data) != 2 || !reflect.DeepEqual(mustJSON(t, list), want) {
		t.Errorf("list_services = %+v, REST %v", list, want)
	}
	if d := list.Data[0].Docker; d == nil || *d != "running" || list.Data[0].Data[0].RestartPolicy != "no" {
		t.Errorf("docker = %v, restart_policy = %q", d, list.Data[0].Data[0].RestartPolicy)
	}
	structured(t, callTool(t, cs, "list_services", map[string]any{"machine": "g4f4r0-apps"}), &list)
	if len(list.Data) != 1 || list.Data[0].Docker != nil || len(list.Data[0].Data) != 0 {
		t.Errorf("apps has %+v", list.Data)
	}
	if want := restJSON(t, rest, "/v1/machines/g4f4r0-apps/services"); !reflect.DeepEqual(mustJSON(t, list.Data[0]), want) {
		t.Errorf("apps: tool %+v, REST %v", list.Data[0], want)
	}
	if res := callTool(t, cs, "list_services", map[string]any{"machine": "nope"}); !res.IsError || !strings.Contains(errorText(res), "Call list_machines") {
		t.Errorf("unknown machine = %s", errorText(res))
	}

	for _, tool := range []string{"start_service", "stop_service", "restart_service"} {
		var a services.Action
		structured(t, callTool(t, cs, tool, map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api"}), &a)
		if a.Status != services.StatusCompleted || a.Type != strings.TrimSuffix(tool, "_service") {
			t.Errorf("%s = %+v", tool, a)
		}
	}
	var logs services.Action
	structured(t, callTool(t, cs, "get_service_logs", map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api", "lines": 3}), &logs)
	if logs.Output == nil || *logs.Output != "log line\nlog line\nlog line\n" {
		t.Errorf("logs = %+v", logs)
	}
	structured(t, callTool(t, cs, "get_service_logs", map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api"}), &logs)
	if *logs.Lines != services.DefaultLines {
		t.Errorf("default lines = %d", *logs.Lines)
	}

	res := callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "worker"})
	if !res.IsError || errorText(res) != "Could not restart worker: Docker answered 500: boom." {
		t.Errorf("failed restart = %s", errorText(res))
	}
	res = callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "nope"})
	if !res.IsError || errorText(res) != `Service "nope" doesn't exist on g4f4r0-code. Call list_services to see the names.` {
		t.Errorf("unknown service = %s", errorText(res))
	}
	if res := callTool(t, cs, "get_service_logs", map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api", "lines": 5000}); !res.IsError {
		t.Error("5000 lines were accepted")
	}

	var sv services.Service
	structured(t, callTool(t, cs, "set_service_alerts", map[string]any{"machine": "g4f4r0-code", "service": "worker", "enabled": false}), &sv)
	if sv.AlertsEnabled {
		t.Errorf("alerts still on: %+v", sv)
	}
	// list_services never carries logs.
	structured(t, callTool(t, cs, "list_services", nil), &list)
	if strings.Contains(string(mustRaw(t, list)), "log line") {
		t.Error("list_services shows logs")
	}
}

// A tool whose daemon doesn't answer returns the action as it is, still pending, after its wait.
func TestServiceToolWaitsThenReturnsPending(t *testing.T) {
	services.ActionWait = time.Second
	t.Cleanup(func() { services.ActionWait = 60 * time.Second })
	d := seeded(t)
	ss := services.NewStore(d)
	if _, err := ss.Replace(context.Background(), 1, []services.ServiceInput{{Name: "web", ContainerID: "c1", Image: "i", State: "running"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d)
	start := time.Now()
	var a services.Action
	structured(t, callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "web"}), &a)
	if took := time.Since(start); a.Status != services.StatusPending || took < services.ActionWait || took > services.ActionWait+2*time.Second {
		t.Errorf("after %s: %+v", took, a)
	}
}

func mustRaw(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustJSON is v as generic JSON, to compare with what a REST route answers.
func mustJSON(t *testing.T, v any) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(mustRaw(t, v), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
