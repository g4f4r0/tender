package services

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/g4f4r0/tender/internal/apps/expose"
	"github.com/g4f4r0/tender/internal/server"
)

// errCloudflareNotSet is how a cloudflare route says the hub has no Cloudflare settings, and how to
// set them.
const errCloudflareNotSet = "A cloudflare route needs the hub's Cloudflare settings, and it has none yet. Set them with: " +
	"tender exposures cloudflare set --account-id ID --api-token-file PATH, or PUT /v1/exposures/cloudflare"

// CloudflareTunnels is the hub's Tunnels: it keeps each machine's tunnel in the Cloudflare account
// of the hub's settings. It keeps each machine's tunnel token in memory only, from the last Apply,
// and asks Cloudflare again after the hub restarts.
type CloudflareTunnels struct {
	Settings *CloudflareStore

	mu     sync.Mutex
	tokens map[string]string
}

// NewCloudflareTunnels returns the tunnels of the Cloudflare account in settings.
func NewCloudflareTunnels(settings *CloudflareStore) *CloudflareTunnels {
	return &CloudflareTunnels{Settings: settings, tokens: map[string]string{}}
}

// Ready is a 422 with how to set them unless the hub has its Cloudflare settings.
func (t *CloudflareTunnels) Ready(ctx context.Context) error {
	_, err := t.Settings.Cloudflare(ctx)
	var e *server.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return server.Invalid("%s.", errCloudflareNotSet)
	}
	return err
}

// Apply makes machine's tunnel route exactly routes, with their DNS records. A route that can't be
// set is an *expose.RouteError, and the others are set. Without settings, no routes is nothing to
// do, and any route is an error saying how to set them.
func (t *CloudflareTunnels) Apply(ctx context.Context, machine string, routes []expose.Route) error {
	c, err := t.Settings.Client(ctx)
	var e *server.Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		if len(routes) == 0 {
			return nil
		}
		return errors.New(errCloudflareNotSet)
	}
	if err != nil {
		return err
	}
	tun, err := c.Apply(ctx, machine, routes)
	if err == nil || tun.ID != "" {
		t.mu.Lock()
		if tun.Token != "" {
			t.tokens[machine] = tun.Token
		} else {
			delete(t.tokens, machine)
		}
		t.mu.Unlock()
	}
	return err
}

// Token returns the token machine's daemon runs its tunnel with: the one from the last Apply, or,
// after the hub restarted, from applying routes again. It is "" without routes.
func (t *CloudflareTunnels) Token(ctx context.Context, machine string, routes []expose.Route) (string, error) {
	if len(routes) == 0 {
		return "", nil
	}
	t.mu.Lock()
	token, ok := t.tokens[machine]
	t.mu.Unlock()
	if ok {
		return token, nil
	}
	err := t.Apply(ctx, machine, routes)
	t.mu.Lock()
	token = t.tokens[machine]
	t.mu.Unlock()
	return token, err
}
