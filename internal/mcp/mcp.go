// Package mcp serves the hub's read-only tools to AI clients over MCP. It is the only package
// that imports the MCP SDK; domains register their tools here and never see the protocol.
package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g4f4r0/tender/internal/server"
)

// Server holds the tools.
type Server struct {
	sdk *sdk.Server
	log *slog.Logger
}

// New returns a server with no tools.
func New(version string, log *slog.Logger) *Server {
	impl := &sdk.Implementation{Name: "tender", Title: "Tender", Version: version}
	opts := &sdk.ServerOptions{Instructions: "Read-only state of the machines that run tender: usage, backups and notifications. " +
		"Start with list_machines."}
	return &Server{sdk: sdk.NewServer(impl, opts), log: log}
}

// AddReadOnly adds a tool that only reads. In and Out are structs: their fields become the input
// and output schemas, and `jsonschema:"..."` tags become the descriptions. Initialize every slice
// in Out to []: a nil slice is null in the output, and the SDK's schema for a slice allows it.
// A returned error reaches the client as a tool result with isError set, not as a protocol error.
func AddReadOnly[In, Out any](s *Server, name, title, description string, h func(context.Context, In) (Out, error)) {
	closed := false
	tool := &sdk.Tool{
		Name:        name,
		Title:       title,
		Description: description,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closed},
	}
	sdk.AddTool(s.sdk, tool, func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		out, err := h(ctx, in)
		return nil, out, s.clientError(name, err)
	})
}

// clientError keeps the message of an error the client can act on, such as an unknown machine
// or a bad value, and hides the rest, as the REST routes do.
func (s *Server) clientError(tool string, err error) error {
	var e *server.Error
	if err == nil || errors.As(err, &e) {
		return err
	}
	s.log.Error("tool failed", "tool", tool, "error", err)
	return &server.Error{Status: http.StatusInternalServerError, Detail: "The hub failed to read its data."}
}

// MachineLookup returns the id of the machine called name, or a server NotFound error.
type MachineLookup func(ctx context.Context, name string) (int64, error)

// Machine resolves the machine a tool was asked about. An unknown name is an error that
// tells the client how to find the right one.
func Machine(ctx context.Context, lookup MachineLookup, name string) (int64, error) {
	if name == "" {
		return 0, server.Invalid("machine is required. Call list_machines to see the names.")
	}
	id, err := lookup(ctx, name)
	var e *server.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return 0, server.NotFound("%s Call list_machines to see the names.", e.Detail)
	}
	return id, err
}

// Handler serves the tools over Streamable HTTP: JSON responses, no sessions. It does no
// authentication; the hub wraps it. The hub listens on 127.0.0.1 behind Tailscale Serve, so
// the SDK's check that Host is a loopback name would refuse every request and is off.
func (s *Server) Handler() http.Handler {
	return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.sdk },
		&sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
}

// Run serves one client over t until it disconnects. Tests use it with in-memory transports.
func (s *Server) Run(ctx context.Context, t sdk.Transport) error { return s.sdk.Run(ctx, t) }

// RunStdio serves one client over standard input and output until it disconnects.
func (s *Server) RunStdio(ctx context.Context) error { return s.Run(ctx, &sdk.StdioTransport{}) }
