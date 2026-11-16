package services

// The hub's Cloudflare settings, for the cloudflare exposure: PUT, GET and DELETE
// /v1/exposures/cloudflare, their MCP tools, and the client the hub's cloudflare adapter uses.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/mcp"
	"github.com/g4f4r0/tender/internal/secret"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/expose"
)

// Hidden is what a secret shows as.
const Hidden = "***"

// CloudflareSettings are the hub's Cloudflare settings as people see them: the API token is
// always "***", with api_token_set.
type CloudflareSettings struct {
	AccountID   string    `json:"account_id"`
	APIToken    string    `json:"api_token"`
	APITokenSet bool      `json:"api_token_set"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CloudflareInput is the body of PUT /v1/exposures/cloudflare. APIToken left out keeps the
// stored one.
type CloudflareInput struct {
	AccountID string  `json:"account_id"`
	APIToken  *string `json:"api_token,omitempty"`
}

// CloudflareStore keeps the hub's Cloudflare settings. key seals and opens the API token.
type CloudflareStore struct {
	db  *db.DB
	key []byte
	// API is the Cloudflare API's base URL; expose.CloudflareAPI when empty.
	API string
	// InUse counts the cloudflare routes, which need the settings; nil counts none. The store of
	// the services' routes sets it.
	InUse func(ctx context.Context) (int, error)
	// backoff is the client's wait before it tries again; the client's default when 0.
	backoff time.Duration
}

// NewCloudflareStore returns the store over d, with the hub's encryption key.
func NewCloudflareStore(d *db.DB, key []byte) *CloudflareStore {
	return &CloudflareStore{db: d, key: key}
}

var errNoCloudflareKey = &server.Error{Status: http.StatusServiceUnavailable,
	Detail: "This process has no TENDER_ENCRYPTION_KEY, so it can't store or read the Cloudflare API token. Set it to the hub's key and run the command again."}

func errNoCloudflare() error {
	return server.NotFound("Cloudflare isn't set up on this hub. Set it up with: tender exposures cloudflare set --account-id ID --api-token-file PATH")
}

func (s *CloudflareStore) row(ctx context.Context) (CloudflareSettings, []byte, error) {
	var st CloudflareSettings
	var sealed []byte
	var created, updated int64
	err := s.db.Read.QueryRowContext(ctx, `SELECT account_id, api_token, created_at, updated_at FROM service_cloudflare_settings WHERE id = 1`).
		Scan(&st.AccountID, &sealed, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil, errNoCloudflare()
	}
	if err != nil {
		return st, nil, err
	}
	st.APIToken, st.APITokenSet = Hidden, true
	st.CreatedAt, st.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return st, sealed, nil
}

// Cloudflare returns the settings, or a 404 when there are none.
func (s *CloudflareStore) Cloudflare(ctx context.Context) (CloudflareSettings, error) {
	st, _, err := s.row(ctx)
	return st, err
}

func (s *CloudflareStore) token(sealed []byte) (string, error) {
	if len(s.key) == 0 {
		return "", errNoCloudflareKey
	}
	plain, err := secret.Open(s.key, sealed)
	if err != nil {
		return "", errors.New("open the Cloudflare API token: " + err.Error())
	}
	return string(plain), nil
}

// Client returns the client of the Cloudflare API with the stored settings, for the hub's
// cloudflare adapter, or a 404 when there are none.
func (s *CloudflareStore) Client(ctx context.Context) (*expose.Cloudflare, error) {
	st, sealed, err := s.row(ctx)
	if err != nil {
		return nil, err
	}
	token, err := s.token(sealed)
	if err != nil {
		return nil, err
	}
	return &expose.Cloudflare{Base: s.API, AccountID: st.AccountID, Token: token, Backoff: s.backoff}, nil
}

// checkToken is an error unless token could be a Cloudflare API token.
func checkToken(token string) error {
	switch {
	case token == Hidden:
		return server.Invalid("api_token is \"***\", which is how the hub hides a secret. Give the token itself, or leave it out to keep the stored one.")
	case len(token) < 20 || len(token) > 200 || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }):
		return server.Invalid("api_token must be a Cloudflare API token, 20 to 200 printable characters with no spaces. Create one under My Profile, API Tokens, with %s.",
			expose.CloudflarePermissions)
	}
	return nil
}

// SetCloudflare replaces the settings, after checking with Cloudflare that the token reaches the
// account's tunnels and zones. It answers 422 when Cloudflare refuses them and 502 when
// Cloudflare can't be asked; neither stores anything.
func (s *CloudflareStore) SetCloudflare(ctx context.Context, in CloudflareInput) (CloudflareSettings, error) {
	if err := expose.CheckAccountID(in.AccountID); err != nil {
		return CloudflareSettings{}, server.Invalid("%s.", err)
	}
	var token string
	if in.APIToken != nil {
		if err := checkToken(*in.APIToken); err != nil {
			return CloudflareSettings{}, err
		}
		token = *in.APIToken
	} else {
		_, sealed, err := s.row(ctx)
		var e *server.Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return CloudflareSettings{}, server.Invalid("api_token is required: the hub has no Cloudflare API token yet.")
		}
		if err != nil {
			return CloudflareSettings{}, err
		}
		if token, err = s.token(sealed); err != nil {
			return CloudflareSettings{}, err
		}
	}
	if len(s.key) == 0 {
		return CloudflareSettings{}, errNoCloudflareKey
	}
	c := expose.Cloudflare{Base: s.API, AccountID: in.AccountID, Token: token, Backoff: s.backoff}
	if err := c.Verify(ctx); err != nil {
		var ce *expose.CloudflareError
		if errors.As(err, &ce) && !ce.Retryable {
			return CloudflareSettings{}, server.Invalid("%s.", ce.Error())
		}
		if errors.As(err, &ce) {
			return CloudflareSettings{}, &server.Error{Status: http.StatusBadGateway, Detail: "The hub could not check the settings with Cloudflare: " + ce.Error() + "."}
		}
		return CloudflareSettings{}, err
	}
	sealed, err := secret.Seal(s.key, []byte(token))
	if err != nil {
		return CloudflareSettings{}, err
	}
	now := time.Now().Unix()
	if _, err := s.db.Write.ExecContext(ctx, `INSERT INTO service_cloudflare_settings (id, account_id, api_token, created_at, updated_at)
		VALUES (1, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET account_id = excluded.account_id, api_token = excluded.api_token,
		updated_at = excluded.updated_at`, in.AccountID, sealed, now, now); err != nil {
		return CloudflareSettings{}, err
	}
	return s.Cloudflare(ctx)
}

// DeleteCloudflare removes the settings. It refuses while cloudflare routes need them. The
// tunnels and DNS records in Cloudflare stay.
func (s *CloudflareStore) DeleteCloudflare(ctx context.Context) error {
	if s.InUse != nil {
		n, err := s.InUse(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return server.Conflict("%d cloudflare routes need these settings. Take the routes off their services first, or redeploy them with another expose.", n)
		}
	}
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM service_cloudflare_settings WHERE id = 1`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return server.NotFound("Cloudflare isn't set up on this hub. There is nothing to remove.")
	}
	return nil
}

// CheckKey is an error unless the hub's key opens the stored API token.
func (s *CloudflareStore) CheckKey(ctx context.Context) error {
	_, sealed, err := s.row(ctx)
	var e *server.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.token(sealed)
	return err
}

// Routes registers the Cloudflare settings routes on srv.
func (s *CloudflareStore) Routes(srv *server.Server) {
	srv.Handle("GET /v1/exposures/cloudflare", srv.People(s.getRoute))
	srv.Handle("PUT /v1/exposures/cloudflare", srv.People(s.putRoute))
	srv.Handle("DELETE /v1/exposures/cloudflare", srv.People(s.deleteRoute))
}

func (s *CloudflareStore) getRoute(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Cloudflare(r.Context())
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *CloudflareStore) putRoute(w http.ResponseWriter, r *http.Request) error {
	var in CloudflareInput
	if err := server.Decode(w, r, &in); err != nil {
		return err
	}
	st, err := s.SetCloudflare(r.Context(), in)
	if err != nil {
		return err
	}
	server.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *CloudflareStore) deleteRoute(w http.ResponseWriter, r *http.Request) error {
	if err := s.DeleteCloudflare(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type noCloudflareInput struct{}

type setCloudflareInput struct {
	AccountID string  `json:"account_id" jsonschema:"the Cloudflare account ID, 32 lowercase hex digits, from the dashboard's Account home"`
	APIToken  *string `json:"api_token,omitempty" jsonschema:"the Cloudflare API token, with Account Cloudflare Tunnel Edit, Zone DNS Edit and Zone Read; leave out to keep the stored one"`
}

const cloudflareFields = "The settings are account_id, api_token (always \"***\"), api_token_set, created_at and updated_at."

// RegisterTools adds the Cloudflare settings tools to m. They use the same store methods as the
// REST routes.
func (s *CloudflareStore) RegisterTools(m *mcp.Server) {
	mcp.AddReadOnly(m, "get_cloudflare_settings", "Get the Cloudflare settings",
		"Get the hub's Cloudflare settings, which services' cloudflare routes need: the hub keeps one tunnel per machine, named "+
			"tender-<machine>, and a proxied DNS record per route in that account. "+cloudflareFields+
			" It fails with not found when Cloudflare isn't set up.",
		func(ctx context.Context, _ noCloudflareInput) (CloudflareSettings, error) { return s.Cloudflare(ctx) })

	mcp.AddWrite(m, "set_cloudflare_settings", "Set the Cloudflare settings",
		"Set the Cloudflare account and API token that services' cloudflare routes use. The token needs "+expose.CloudflarePermissions+
			". The hub first asks Cloudflare whether the token reaches the account's tunnels and zones, and stores nothing if it "+
			"doesn't. The result is the settings. "+cloudflareFields,
		mcp.WriteOptions{Idempotent: true, OpenWorld: true},
		func(ctx context.Context, in setCloudflareInput) (CloudflareSettings, error) {
			return s.SetCloudflare(ctx, CloudflareInput(in))
		})

	mcp.AddWrite(m, "remove_cloudflare_settings", "Remove the Cloudflare settings",
		"Remove the hub's Cloudflare account and API token. It refuses while a service has a cloudflare route. The tunnels and DNS "+
			"records already in Cloudflare stay. The result names what was removed.",
		mcp.WriteOptions{Destructive: true, Idempotent: true},
		func(ctx context.Context, _ noCloudflareInput) (mcp.Removed, error) {
			if err := s.DeleteCloudflare(ctx); err != nil {
				return mcp.Removed{}, err
			}
			return mcp.Removed{Removed: "the Cloudflare settings"}, nil
		})
}
