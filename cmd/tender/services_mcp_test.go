package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g4f4r0/tender/internal/actions"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services"
)

// fakeDaemon takes g4f4r0-code's actions from the store and finishes them as finish says, until
// the test ends.
func fakeDaemon(t *testing.T, as *actions.Store, finish func(actions.Action) actions.Patch) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			list, err := as.Wait(ctx, 1, time.Second, nil)
			if err != nil {
				return
			}
			for _, a := range list {
				if as.Claim(ctx, 1, a.ID) == nil {
					_ = as.Finish(ctx, 1, a.ID, finish(a))
				}
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
		{Name: "cli-proxy-api", ContainerID: "c1", Image: "cpa", State: "running", MemoryBytes: ptr(int64(200 << 20)), CPUPercent: ptr(3.5)},
		{Name: "worker", ContainerID: "c2", Image: "w", State: "exited", ExitCode: ptr(int64(1))},
	}); err != nil {
		t.Fatal(err)
	}
	fakeDaemon(t, newActions(d), func(a actions.Action) actions.Patch {
		switch {
		case a.Service != nil && *a.Service == "worker":
			return actions.Patch{Status: actions.StatusFailed, Output: ptr("Could not " + a.Type + " worker: Docker answered 500: boom.")}
		case a.Type == actions.TypeLogs && a.Service == nil:
			unit := "kernel"
			if a.Unit != nil {
				unit = *a.Unit
			}
			return actions.Patch{Status: actions.StatusCompleted, Output: ptr("2026-11-15T10:00:00.000000000Z " + unit + " journal line\n")}
		case a.Type == actions.TypeLogs:
			return actions.Patch{Status: actions.StatusCompleted, Output: ptr(strings.Repeat("log line\n", *a.Lines))}
		}
		return actions.Patch{Status: actions.StatusCompleted}
	})
	cs, rest := connect(t, d), newHub(d, testKey, nil, quiet)

	var list server.List[services.MachineServices]
	structured(t, callTool(t, cs, "list_services", nil), &list)
	if want := restJSON(t, rest, "/v1/services"); len(list.Data) != 1 || len(list.Data[0].Data) != 2 || !reflect.DeepEqual(mustJSON(t, list), want) {
		t.Errorf("list_services = %+v, REST %v", list, want)
	}
	if d := list.Data[0].Docker; d == nil || *d != "running" || list.Data[0].Data[0].RestartPolicy != "no" || *list.Data[0].Data[0].CPUPercent != 3.5 {
		t.Errorf("docker = %v, service = %+v", d, list.Data[0].Data[0])
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
		var a actions.Action
		structured(t, callTool(t, cs, tool, map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api"}), &a)
		if a.Status != actions.StatusCompleted || a.Type != strings.TrimSuffix(tool, "_service") {
			t.Errorf("%s = %+v", tool, a)
		}
		if sv, _ := ss.Get(ctx, 1, "cli-proxy-api"); sv.StoppedByTender != (tool == "stop_service") {
			t.Errorf("after %s, stopped_by_tender = %v", tool, sv.StoppedByTender)
		}
	}
	var logs actions.Action
	structured(t, callTool(t, cs, "get_service_logs", map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api", "lines": 3, "since": "1h"}), &logs)
	if logs.Output == nil || *logs.Output != "log line\nlog line\nlog line\n" || *logs.Since != "1h" {
		t.Errorf("logs = %+v", logs)
	}
	structured(t, callTool(t, cs, "get_service_logs", map[string]any{"machine": "g4f4r0-code", "service": "cli-proxy-api"}), &logs)
	if *logs.Lines != actions.DefaultLines {
		t.Errorf("default lines = %d", *logs.Lines)
	}
	var journal actions.Action
	structured(t, callTool(t, cs, "get_machine_logs", map[string]any{"machine": "g4f4r0-code", "unit": "tenderd", "priority": "err", "since": "7d"}), &journal)
	if journal.Output == nil || *journal.Output != "2026-11-15T10:00:00.000000000Z tenderd journal line\n" || journal.Service != nil || *journal.Priority != "err" {
		t.Errorf("machine logs = %+v", journal)
	}
	if res := callTool(t, cs, "get_machine_logs", map[string]any{"machine": "g4f4r0-code", "unit": "-x"}); !res.IsError || !strings.Contains(errorText(res), "unit must be") {
		t.Errorf("flag as a unit = %s", errorText(res))
	}

	var got actions.Action
	structured(t, callTool(t, cs, "get_action", map[string]any{"machine": "g4f4r0-code", "id": journal.ID}), &got)
	if want := restJSON(t, rest, "/v1/machines/g4f4r0-code/actions/"+strconv.FormatInt(journal.ID, 10)); !reflect.DeepEqual(mustJSON(t, got), want) {
		t.Errorf("get_action = %+v, REST %v", got, want)
	}
	if res := callTool(t, cs, "get_action", map[string]any{"machine": "g4f4r0-code", "id": 999}); !res.IsError {
		t.Error("an unknown action was found")
	}
	var recent server.List[actions.Summary]
	structured(t, callTool(t, cs, "list_actions", map[string]any{"machine": "g4f4r0-code"}), &recent)
	if want := restJSON(t, rest, "/v1/machines/g4f4r0-code/actions"); len(recent.Data) != 6 || !reflect.DeepEqual(mustJSON(t, recent), want) {
		t.Errorf("list_actions = %d actions, REST %v", len(recent.Data), want)
	}
	if strings.Contains(string(mustRaw(t, recent)), "log line") {
		t.Error("list_actions shows logs")
	}

	res := callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "worker"})
	if !res.IsError || errorText(res) != "Could not restart worker: Docker answered 500: boom." {
		t.Errorf("failed restart = %s", errorText(res))
	}
	res = callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "nope"})
	if !res.IsError || errorText(res) != `Service "nope" doesn't exist on g4f4r0-code. Call list_services to see the names.` {
		t.Errorf("unknown service = %s", errorText(res))
	}
	// lines below 1 is a mistake to name, as REST and the CLI do, not a quiet default.
	for _, n := range []int{0, -5} {
		for tool, args := range map[string]map[string]any{
			"get_service_logs": {"machine": "g4f4r0-code", "service": "cli-proxy-api", "lines": n},
			"get_machine_logs": {"machine": "g4f4r0-code", "lines": n},
		} {
			if res := callTool(t, cs, tool, args); !res.IsError || !strings.Contains(errorText(res), "lines must be a whole number from 1 to 2000") {
				t.Errorf("%s with lines %d: %+v, want the lines error", tool, n, res)
			}
		}
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
	actions.Wait = time.Second
	t.Cleanup(func() { actions.Wait = 60 * time.Second })
	d := seeded(t)
	ss := services.NewStore(d)
	if _, err := ss.Replace(context.Background(), 1, []services.ServiceInput{{Name: "web", ContainerID: "c1", Image: "i", State: "running"}}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, d)
	start := time.Now()
	var a actions.Action
	structured(t, callTool(t, cs, "restart_service", map[string]any{"machine": "g4f4r0-code", "service": "web"}), &a)
	if took := time.Since(start); a.Status != actions.StatusPending || took < actions.Wait || took > actions.Wait+2*time.Second {
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
