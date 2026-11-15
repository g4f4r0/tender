package tokens

import (
	"context"

	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/server"
)

type noInput struct{}

type tokenInput struct {
	Name string `json:"name" jsonschema:"token name, as list_tokens shows it"`
}

// RegisterTools adds list_tokens and delete_token to m. Making a token is not a tool, so an
// agent can't give itself more access: a person makes one with "tender tokens add".
func (s *Store) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "list_tokens", "List API tokens",
		"List the API tokens that may use this hub's REST API and MCP endpoint, by name. Each has id, name, last_used_at "+
			"(the last request with it, to the minute, RFC 3339 UTC, or null before the first), created_at and updated_at. "+
			"The tokens themselves are never shown: each is shown once, to the person who made it.",
		func(ctx context.Context, _ noInput) (server.List[Token], error) {
			list, err := s.List(ctx)
			return server.List[Token]{Data: list}, err
		})

	mcp.AddWrite(m, "delete_token", "Delete an API token",
		"Delete an API token by name. Every client that uses it, which may be this one, is refused from the next request on, "+
			"and deleting the last token locks everyone out of the REST API until the owner runs \"sudo tender tokens add NAME\" "+
			"on the hub machine. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, in tokenInput) (mcp.Removed, error) {
			if in.Name == "" {
				return mcp.Removed{}, server.Invalid("name is required. Call list_tokens to see the names.")
			}
			if err := s.Delete(ctx, in.Name); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: in.Name}, nil
		})
}
