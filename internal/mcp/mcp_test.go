package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type in struct {
	Name string `json:"name,omitempty" jsonschema:"what to look up"`
}

type out struct {
	Items []string `json:"items"`
}

func connect(t *testing.T, log *slog.Logger) *sdk.ClientSession {
	t.Helper()
	s := mcp.New("test", log)
	mcp.AddReadOnly(s, "echo", "Echo", "Echo the name.", func(_ context.Context, in in) (out, error) {
		switch in.Name {
		case "missing":
			return out{}, server.NotFound("Thing %q doesn't exist.", in.Name)
		case "broken":
			return out{}, errors.New("sqlite: disk I/O error")
		case "none":
			return out{Items: []string{}}, nil
		}
		return out{Items: []string{in.Name}}, nil
	})
	ctx := context.Background()
	st, ct := sdk.NewInMemoryTransports()
	go func() { _ = s.Run(ctx, st) }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{"name": name}})
	if err != nil {
		t.Fatalf("call %q: protocol error %v, want a tool result", name, err)
	}
	return res
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestToolIsReadOnlyWithSchemas(t *testing.T) {
	cs := connect(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(list.Tools) != 1 {
		t.Fatalf("ListTools = %v, %v", list, err)
	}
	tool := list.Tools[0]
	a := tool.Annotations
	if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want readOnly, idempotent, not open world", a)
	}
	in, err := json.Marshal(tool.InputSchema)
	if err != nil || !strings.Contains(string(in), "what to look up") {
		t.Errorf("input schema %s lacks the field description (%v)", in, err)
	}
	if bytes.Contains(in, []byte(`"required"`)) {
		t.Errorf("input schema %s requires a field that is optional", in)
	}
	if tool.OutputSchema == nil {
		t.Error("no output schema")
	}
}

func TestToolResults(t *testing.T) {
	var logs bytes.Buffer
	cs := connect(t, slog.New(slog.NewTextHandler(&logs, nil)))

	res := call(t, cs, "a")
	if res.IsError || !strings.Contains(text(res), `"items":["a"]`) {
		t.Errorf("ok call = %v %s", res.IsError, text(res))
	}
	res = call(t, cs, "none")
	if res.IsError || !strings.Contains(text(res), `"items":[]`) {
		t.Errorf("empty call = %v %s", res.IsError, text(res))
	}

	// A client error keeps its message.
	res = call(t, cs, "missing")
	if !res.IsError || !strings.Contains(text(res), `Thing "missing" doesn't exist.`) {
		t.Errorf("client error = %v %s", res.IsError, text(res))
	}

	// Any other error is logged and hidden.
	res = call(t, cs, "broken")
	if !res.IsError || strings.Contains(text(res), "sqlite") || !strings.Contains(logs.String(), "disk I/O") {
		t.Errorf("internal error = %v %s, logs %s", res.IsError, text(res), logs.String())
	}

	// An unknown tool is a protocol error.
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "nope"}); err == nil {
		t.Error("unknown tool: want a protocol error")
	}
}

func TestMachine(t *testing.T) {
	lookup := func(_ context.Context, name string) (int64, error) {
		switch name {
		case "a":
			return 7, nil
		case "broken":
			return 0, errors.New("disk")
		}
		return 0, server.NotFound("Machine %q doesn't exist.", name)
	}
	ctx := context.Background()
	if id, err := mcp.Machine(ctx, lookup, "a"); err != nil || id != 7 {
		t.Errorf("a = %d, %v", id, err)
	}
	for _, name := range []string{"", "nope"} {
		if _, err := mcp.Machine(ctx, lookup, name); err == nil || !strings.Contains(err.Error(), "list_machines") {
			t.Errorf("%q: %v, want a hint to call list_machines", name, err)
		}
	}
	if _, err := mcp.Machine(ctx, lookup, "broken"); err == nil || strings.Contains(err.Error(), "list_machines") {
		t.Errorf("broken: %v, want the lookup's own error", err)
	}
}
