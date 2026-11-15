package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g4f4r0/tender/internal/backups"
	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/machines"
	"github.com/g4f4r0/tender/internal/notifications"
	"github.com/g4f4r0/tender/internal/server"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// testKey is the hub's encryption key in these tests: 32 bytes, 0 to 31.
var testKey = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}()

// seeded returns a database with two machines and data for the first:
// g4f4r0-apps has nothing. g4f4r0-code has two readings, a destination and settings, two
// snapshots, three runs (a completed backup, a failed check, a failed backup) and notifications 1 to 3.
func seeded(t *testing.T) *db.DB {
	t.Helper()
	d := schema(t)
	ctx := context.Background()
	ms, bs := machines.NewStore(d), backups.NewStore(d, testKey)
	for _, name := range []string{"g4f4r0-code", "g4f4r0-apps"} {
		if _, err := ms.Create(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	for i, cpu := range []float64{0.5, 1.5} {
		_, err := ms.AddUsage(ctx, 1, machines.UsageInput{CPULoad: cpu, MemoryUsedPercent: 40, DiskUsedPercent: 50,
			UptimeSeconds: 3600, CreatedAt: base.Add(time.Duration(i) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.AddDestination(ctx, backups.DestinationInput{Name: "r2-code", Config: map[string]any{"endpoint": "https://acct.r2.cloudflarestorage.com", "bucket": "b", "access_key_id": "key-id", "secret_access_key": "key-secret"}, Password: "repo-password"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.SetSettings(ctx, 1, "r2-code", "30 3 * * *", 30); err != nil {
		t.Fatal(err)
	}
	size := int64(2048)
	_, err := bs.ReplaceSnapshots(ctx, 1, []backups.SnapshotInput{
		{ResticID: "aaaa1111", SizeBytes: &size, Paths: []string{"/etc"}, CreatedAt: base},
		{ResticID: "bbbb2222", Paths: []string{"/etc", "/srv"}, CreatedAt: base.Add(24 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := "repository is locked"
	for i, h := range []backups.HistoryInput{
		{Type: "backup", Status: "completed", CreatedAt: base},
		{Type: "check", Status: "failed", Error: &boom, CreatedAt: base.Add(time.Hour)},
		{Type: "backup", Status: "failed", Error: &boom, CreatedAt: base.Add(24 * time.Hour)},
	} {
		if _, err := bs.CreateHistory(ctx, 1, h); err != nil {
			t.Fatalf("history %d: %v", i, err)
		}
	}
	must(t, d, `INSERT INTO notifications (id, machine_id, type, severity, status, content, created_at, updated_at) VALUES
		(1, 1, 'backup_failed', 'critical', 'sent', 'Backup failed on g4f4r0-code', 100, 100),
		(2, 1, 'disk_full', 'warning', 'resolved', 'Disk was full', 200, 250),
		(3, NULL, 'hub_restarted', 'info', 'sent', 'The hub restarted', 300, 300)`)
	return d
}

// connect connects a real MCP client to the tools over the SDK's in-memory transport.
func connect(t *testing.T, d *db.DB) *sdk.ClientSession { return connectSending(t, d, nil) }

// connectSending is connect with send_test_email sending through sender.
func connectSending(t *testing.T, d *db.DB, sender notifications.Sender) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, ct := sdk.NewInMemoryTransports()
	done := make(chan struct{})
	go func() { _ = newTools(d, testKey, sender, quiet).Run(ctx, st); close(done) }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		cancel()
		<-done
	})
	return cs
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	return res
}

// structured decodes a successful result's structured output into v.
func structured(t *testing.T, res *sdk.CallToolResult, v any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", errorText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"data":null`)) || bytes.Contains(raw, []byte(`"paths":null`)) {
		t.Errorf("output holds a null list: %s", raw)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func errorText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolsList(t *testing.T) {
	cs := connect(t, seeded(t))
	// No list-change notifications: they make clients hold a listen request open.
	if caps := cs.InitializeResult().Capabilities; caps.Tools == nil || caps.Tools.ListChanged {
		t.Errorf("tools capability = %+v, want tools without listChanged", caps.Tools)
	}
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	reads := []string{"get_action", "get_backup_status", "get_machine_logs", "get_machine_usage", "get_recipient", "get_service_logs", "list_actions",
		"list_backup_history", "list_backup_snapshots", "list_destinations", "list_machines", "list_notifications", "list_recipients", "list_services"}
	// Per write tool: destructive, idempotent.
	writes := map[string][2]bool{
		"add_machine": {false, false}, "rotate_machine_token": {true, false}, "remove_machine": {true, true},
		"add_destination": {false, false}, "update_destination": {false, true}, "remove_destination": {true, true},
		"set_backup": {false, true}, "unset_backup": {true, true},
		"add_recipient": {false, false}, "remove_recipient": {true, true}, "send_test_email": {false, false},
		"start_service": {false, true}, "stop_service": {true, true}, "restart_service": {true, false}, "set_service_alerts": {false, true},
		"test_destination": {false, true},
	}
	// The tools that reach outside the hub.
	open := map[string]bool{"send_test_email": true, "test_destination": true}
	var gotReads, gotWrites []string
	for _, tool := range list.Tools {
		a := tool.Annotations
		if a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint != open[tool.Name] {
			t.Errorf("%s annotations = %+v, want open world only for %v", tool.Name, a, open)
			continue
		}
		if tool.OutputSchema == nil || tool.InputSchema == nil || len(tool.Description) < 40 {
			t.Errorf("%s lacks a schema or a description", tool.Name)
		}
		if w, ok := writes[tool.Name]; ok {
			gotWrites = append(gotWrites, tool.Name)
			if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint != w[0] || a.IdempotentHint != w[1] {
				t.Errorf("%s annotations = %+v, want not read-only, destructive %v, idempotent %v", tool.Name, a, w[0], w[1])
			}
			continue
		}
		gotReads = append(gotReads, tool.Name)
		if !a.ReadOnlyHint || !a.IdempotentHint {
			t.Errorf("%s annotations = %+v, want readOnly and idempotent", tool.Name, a)
		}
	}
	if !reflect.DeepEqual(slices.Sorted(slices.Values(gotReads)), reads) {
		t.Errorf("read tools = %v, want %v", gotReads, reads)
	}
	if want := slices.Sorted(maps.Keys(writes)); !reflect.DeepEqual(slices.Sorted(slices.Values(gotWrites)), want) {
		t.Errorf("write tools = %v, want %v", gotWrites, want)
	}
}

// Each tool's description names every field of what it returns, or the tool whose result it
// matches ("as list_services shows it"), so a client knows what it gets without calling it. Every
// paged tool states its page size.
func TestToolDescriptionsSayWhatTheyReturn(t *testing.T) {
	// Fields in a result's type that a tool never fills, and why.
	sameAs := regexp.MustCompile(`as (\w+) shows it`)
	never := map[string]string{
		"set_backup credentials": "only the machine's own daemon gets the keys, over REST",
	}
	list, err := connect(t, seeded(t)).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Properties map[string]schema `json:"properties"`
		Items      *schema           `json:"items"`
	}
	for _, tool := range list.Tools {
		var in, out schema
		for _, c := range []struct {
			from any
			to   *schema
		}{{tool.InputSchema, &in}, {tool.OutputSchema, &out}} {
			b, _ := json.Marshal(c.from)
			if err := json.Unmarshal(b, c.to); err != nil {
				t.Fatal(err)
			}
		}
		if _, paged := in.Properties["cursor"]; paged && !strings.Contains(tool.Description, "1 to 500, default 50") {
			t.Errorf("%s is paged, but its description doesn't give the page size (1 to 500, default 50)", tool.Name)
		}
		fields := out.Properties
		if data, ok := fields["data"]; ok && data.Items != nil {
			fields = data.Items.Properties // a list: the fields of each element
		}
		if m := sameAs.FindStringSubmatch(tool.Description); m != nil && m[1] != tool.Name && slices.ContainsFunc(list.Tools,
			func(o *sdk.Tool) bool { return o.Name == m[1] }) {
			continue // it names the tool whose fields it has; that tool's own description is checked
		}
		for name := range fields {
			if _, ok := never[tool.Name+" "+name]; !ok && !regexp.MustCompile(`\b`+name+`\b`).MatchString(tool.Description) {
				t.Errorf("%s returns %s, but its description doesn't say so", tool.Name, name)
			}
		}
	}
}

// restJSON returns what the REST route answers as the reader.
func restJSON(t *testing.T, h http.Handler, path string) any {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Tailscale-User-Login", "johan@example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var v any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	return v
}

// The tools and the REST routes read through the same store methods, so they answer alike.
func TestToolsMatchREST(t *testing.T) {
	d := seeded(t)
	cs, rest := connect(t, d), newHub(d, testKey, nil, quiet)
	for _, c := range []struct {
		tool string
		args map[string]any
		path string
	}{
		{"list_machines", nil, "/v1/machines"},
		{"get_machine_usage", map[string]any{"machine": "g4f4r0-code"}, "/v1/machines/g4f4r0-code/usage"},
		{"get_machine_usage", map[string]any{"machine": "g4f4r0-apps"}, "/v1/machines/g4f4r0-apps/usage"},
		{"get_backup_status", nil, "/v1/backups"},
		{"list_backup_snapshots", map[string]any{"machine": "g4f4r0-code"}, "/v1/machines/g4f4r0-code/backups/snapshots"},
		{"list_backup_history", map[string]any{"machine": "g4f4r0-code", "status": "failed"}, "/v1/machines/g4f4r0-code/backups/history?status=failed"},
		{"list_destinations", nil, "/v1/backups/destinations"},
		{"list_notifications", nil, "/v1/notifications"},
		{"list_notifications", map[string]any{"machine": "g4f4r0-code", "severity": "critical"}, "/v1/machines/g4f4r0-code/notifications?severity=critical"},
	} {
		var got any
		structured(t, callTool(t, cs, c.tool, c.args), &got)
		if want := restJSON(t, rest, c.path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s %v:\n tool %v\n REST %v", c.tool, c.args, got, want)
		}
	}
}

func TestListMachines(t *testing.T) {
	cs := connect(t, seeded(t))
	var l server.List[machines.Overview]
	structured(t, callTool(t, cs, "list_machines", nil), &l)
	if len(l.Data) != 2 || l.Data[0].Name != "g4f4r0-apps" || l.Data[1].Name != "g4f4r0-code" {
		t.Fatalf("machines = %+v", l.Data)
	}
	apps, code := l.Data[0], l.Data[1]
	if apps.Usage != nil || apps.LastSeen != nil {
		t.Errorf("a machine without readings has usage %+v, last_seen %v", apps.Usage, apps.LastSeen)
	}
	if code.Usage == nil || code.Usage.CPULoad != 1.5 || code.LastSeen == nil || !code.LastSeen.Equal(time.Date(2026, 11, 14, 4, 0, 0, 0, time.UTC)) {
		t.Errorf("latest reading = %+v, last_seen %v", code.Usage, code.LastSeen)
	}
}

func TestGetMachineUsage(t *testing.T) {
	cs := connect(t, seeded(t))
	var l server.List[machines.Usage]
	structured(t, callTool(t, cs, "get_machine_usage", map[string]any{"machine": "g4f4r0-code", "limit": 1}), &l)
	if len(l.Data) != 1 || l.Data[0].CPULoad != 1.5 || l.NextCursor == "" {
		t.Fatalf("first page = %+v, cursor %q", l.Data, l.NextCursor)
	}
	var next server.List[machines.Usage]
	structured(t, callTool(t, cs, "get_machine_usage", map[string]any{"machine": "g4f4r0-code", "limit": 1, "cursor": l.NextCursor}), &next)
	if len(next.Data) != 1 || next.Data[0].CPULoad != 0.5 || next.NextCursor != "" {
		t.Errorf("second page = %+v, cursor %q", next.Data, next.NextCursor)
	}

	var win server.List[machines.Usage]
	structured(t, callTool(t, cs, "get_machine_usage", map[string]any{"machine": "g4f4r0-code", "since": "2026-11-14T03:30:00Z", "until": "2026-11-14T04:00:00Z"}), &win)
	if len(win.Data) != 1 || win.Data[0].CPULoad != 1.5 {
		t.Errorf("window = %+v, want the 04:00 reading", win.Data)
	}

	for _, args := range []map[string]any{{"machine": "g4f4r0-code", "since": "yesterday"}, {"machine": "g4f4r0-code", "limit": 501}, {"machine": "g4f4r0-code", "cursor": "x"}} {
		if res := callTool(t, cs, "get_machine_usage", args); !res.IsError {
			t.Errorf("%v: want a tool error, got %s", args, errorText(res))
		}
	}
}

func TestBackupTools(t *testing.T) {
	cs := connect(t, seeded(t))

	var all server.List[backups.Summary]
	structured(t, callTool(t, cs, "get_backup_status", nil), &all)
	if len(all.Data) != 2 || all.Data[0].Machine != "g4f4r0-apps" || all.Data[0].Settings != nil || all.Data[0].LatestSnapshot != nil {
		t.Errorf("every machine = %+v", all.Data)
	}
	var one server.List[backups.Summary]
	structured(t, callTool(t, cs, "get_backup_status", map[string]any{"machine": "g4f4r0-code"}), &one)
	if len(one.Data) != 1 {
		t.Fatalf("one machine = %+v", one.Data)
	}
	s := one.Data[0]
	if s.Settings == nil || s.Settings.Destination.Name != "r2-code" || s.Settings.RetentionDays != 30 || s.SnapshotCount != 2 ||
		s.LatestSnapshot == nil || s.LatestSnapshot.ResticID != "bbbb2222" || s.LastBackup == nil || s.LastBackup.Status != "failed" ||
		s.LastCheck == nil || s.LastCheck.Error == nil || *s.LastCheck.Error != "repository is locked" {
		t.Errorf("summary = %+v", s)
	}
	if !reflect.DeepEqual(one.Data[0], all.Data[1]) {
		t.Errorf("one machine differs from its entry in the full list")
	}

	var snaps server.List[backups.Snapshot]
	structured(t, callTool(t, cs, "list_backup_snapshots", map[string]any{"machine": "g4f4r0-code"}), &snaps)
	if len(snaps.Data) != 2 || snaps.Data[0].ResticID != "bbbb2222" || snaps.Data[1].SizeBytes == nil || *snaps.Data[1].SizeBytes != 2048 {
		t.Errorf("snapshots = %+v", snaps.Data)
	}
	var none server.List[backups.Snapshot]
	structured(t, callTool(t, cs, "list_backup_snapshots", map[string]any{"machine": "g4f4r0-apps"}), &none)
	if none.Data == nil || len(none.Data) != 0 {
		t.Errorf("no snapshots = %#v, want []", none.Data)
	}

	for _, c := range []struct {
		args map[string]any
		want int
	}{
		{map[string]any{"machine": "g4f4r0-code"}, 3},
		{map[string]any{"machine": "g4f4r0-code", "type": "backup"}, 2},
		{map[string]any{"machine": "g4f4r0-code", "status": "failed"}, 2},
		{map[string]any{"machine": "g4f4r0-code", "type": "check", "status": "completed"}, 0},
		{map[string]any{"machine": "g4f4r0-code", "limit": 2}, 2},
	} {
		var runs server.List[backups.History]
		structured(t, callTool(t, cs, "list_backup_history", c.args), &runs)
		if len(runs.Data) != c.want || runs.Data == nil {
			t.Errorf("history %v = %d runs, want %d", c.args, len(runs.Data), c.want)
		}
	}
	for _, args := range []map[string]any{{"machine": "g4f4r0-code", "type": "restore"}, {"machine": "g4f4r0-code", "status": "done"}} {
		if res := callTool(t, cs, "list_backup_history", args); !res.IsError {
			t.Errorf("%v: want a tool error", args)
		}
	}
}

func TestListNotifications(t *testing.T) {
	cs := connect(t, seeded(t))
	for _, c := range []struct {
		args map[string]any
		want []int64
	}{
		{nil, []int64{3, 2, 1}},
		{map[string]any{"machine": "g4f4r0-code"}, []int64{2, 1}},
		{map[string]any{"status": "sent"}, []int64{3, 1}},
		{map[string]any{"severity": "warning"}, []int64{2}},
		{map[string]any{"machine": "g4f4r0-apps"}, []int64{}},
		{map[string]any{"limit": 1}, []int64{3}},
	} {
		var l struct {
			Data []struct {
				ID      int64   `json:"id"`
				Machine *string `json:"machine"`
			} `json:"data"`
		}
		structured(t, callTool(t, cs, "list_notifications", c.args), &l)
		got := []int64{}
		for _, n := range l.Data {
			got = append(got, n.ID)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("list_notifications %v = %v, want %v", c.args, got, c.want)
		}
	}
	for _, args := range []map[string]any{{"status": "open"}, {"severity": "fatal"}} {
		if res := callTool(t, cs, "list_notifications", args); !res.IsError {
			t.Errorf("%v: want a tool error", args)
		}
	}
}

func TestUnknownMachineIsAToolError(t *testing.T) {
	cs := connect(t, seeded(t))
	for tool, args := range map[string]map[string]any{
		"get_machine_usage":     {"machine": "nope"},
		"get_backup_status":     {"machine": "nope"},
		"list_backup_snapshots": {"machine": "nope"},
		"list_backup_history":   {"machine": "nope"},
		"list_notifications":    {"machine": "nope"},
	} {
		res := callTool(t, cs, tool, args) // a protocol error would fail here
		if msg := errorText(res); !res.IsError || !strings.Contains(msg, `"nope" doesn't exist`) || !strings.Contains(msg, "list_machines") {
			t.Errorf("%s: isError %v, %q, want the missing machine and a hint to call list_machines", tool, res.IsError, msg)
		}
	}
	for _, tool := range []string{"get_machine_usage", "list_backup_snapshots", "list_backup_history"} {
		if res := callTool(t, cs, tool, map[string]any{}); !res.IsError {
			t.Errorf("%s without a machine: want a tool error", tool)
		}
		if res := callTool(t, cs, tool, map[string]any{"machine": ""}); !res.IsError || !strings.Contains(errorText(res), "list_machines") {
			t.Errorf("%s with an empty machine: %q", tool, errorText(res))
		}
	}
}

// post sends one JSON-RPC request to /mcp, as the reader when login is set.
func post(t *testing.T, srv *httptest.Server, login bool, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if login {
		req.Header.Set("Tailscale-User-Login", "johan@example.com")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return res, string(b)
}

func TestMCPOverHTTP(t *testing.T) {
	srv := httptest.NewServer(newHub(seeded(t), testKey, nil, quiet))
	t.Cleanup(srv.Close)

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}`
	// No login: the hub is reached through the owner's own network access control.
	res, body := post(t, srv, false, initialize)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") ||
		!strings.Contains(body, `"serverInfo":{"name":"tender"`) {
		t.Fatalf("initialize: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}

	res, body = post(t, srv, true, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_backup_status","arguments":{"machine":"g4f4r0-code"}}}`)
	var rpc struct {
		Result struct {
			IsError           bool `json:"isError"`
			StructuredContent struct {
				Data []backups.Summary `json:"data"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &rpc); err != nil || res.StatusCode != 200 || rpc.Result.IsError ||
		len(rpc.Result.StructuredContent.Data) != 1 || rpc.Result.StructuredContent.Data[0].Machine != "g4f4r0-code" {
		t.Errorf("tools/call: %d %s (%v)", res.StatusCode, body, err)
	}

	// An unknown machine is a result, not a JSON-RPC error.
	_, body = post(t, srv, true, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_backup_history","arguments":{"machine":"nope"}}}`)
	if !strings.Contains(body, `"isError":true`) || strings.Contains(body, `"error":{`) {
		t.Errorf("unknown machine over HTTP: %s", body)
	}

	// Stateless: there is no stream to open or session to close.
	for _, method := range []string{"GET", "DELETE"} {
		req, err := http.NewRequest(method, srv.URL+"/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Tailscale-User-Login", "johan@example.com")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := res.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp: %d, want 405", method, res.StatusCode)
		}
	}
}

// "tender mcp" opens the hub's database as it is: it never creates or migrates one.
func TestMCPStdioNeedsAnExistingSchema(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "hub.db")
	if err := runMCP([]string{"--stdio", "--database", missing}, quiet); err == nil || !strings.Contains(err.Error(), "no database") {
		t.Errorf("missing database: %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("tender mcp created the database")
	}

	empty := filepath.Join(t.TempDir(), "empty.db")
	d, err := db.Open(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runMCP([]string{"--stdio", "--database", empty}, quiet); err == nil || !strings.Contains(err.Error(), "no current schema") {
		t.Errorf("database without the schema: %v", err)
	}

	if err := runMCP(nil, quiet); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("without --stdio: %v", err)
	}
}

// With a key that doesn't open the stored secrets, "tender mcp" refuses to start, instead of
// storing new secrets the hub can't open.
func TestMCPStdioRefusesTheWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.Migrate(ctx, migrations()...); err != nil {
		t.Fatal(err)
	}
	if _, err := backups.NewStore(d, testKey).AddDestination(ctx, backups.DestinationInput{Name: "r2", Config: map[string]any{"endpoint": "https://acct.r2.cloudflarestorage.com", "bucket": "b", "access_key_id": "key-id", "secret_access_key": "key-secret"}, Password: "repo-password"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvEncryptionKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err := runMCP([]string{"--stdio", "--database", path}, quiet); err == nil || !strings.Contains(err.Error(), "does not open the secrets") {
		t.Errorf("wrong key: %v", err)
	}
}
