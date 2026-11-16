package main

import (
	"net/http"
	"os"

	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/services"
	"github.com/g4f4r0/tender/internal/services/expose"
)

// newCloudflareStore returns the Cloudflare settings store, which calls the Cloudflare API, or the
// end-to-end test's fake in its place.
func newCloudflareStore(d *db.DB, key []byte) *services.CloudflareStore {
	s := services.NewCloudflareStore(d, key)
	s.API = expose.CloudflareBase(os.Getenv)
	s.InUse = services.NewStore(d).CloudflareRoutes
	return s
}

// hubTunnels returns the hub's Cloudflare tunnels over d, with key.
func hubTunnels(d *db.DB, key []byte) *services.CloudflareTunnels {
	return services.NewCloudflareTunnels(newCloudflareStore(d, key))
}

var exposuresUsage = groupUsage("exposures", "Set up how services' routes reach people. Tailscale and public routes need no settings; "+
	"cloudflare routes need the hub's Cloudflare account and API token.", []row{
	{"cloudflare show", "Show the Cloudflare settings"},
	{"cloudflare set --account-id ID", "Set the Cloudflare account and API token"},
	{"cloudflare remove", "Remove the Cloudflare settings"},
})

var cloudflareUsage = groupUsage("exposures cloudflare", "Set the Cloudflare account and API token that services' cloudflare routes use. "+
	"The hub keeps one tunnel per machine in that account, named tender-MACHINE, and a proxied DNS record for each route.", []row{
	{"show", "Show the Cloudflare settings"},
	{"set --account-id ID [--api-token-file PATH]", "Set the Cloudflare account and API token"},
	{"remove", "Remove the Cloudflare settings"},
})

// runExposures runs "tender exposures cloudflare show|set|remove".
func runExposures(args []string, s session) error {
	return runGroup("exposures", exposuresUsage, args, s.out, map[string]func([]string) error{
		"cloudflare": func(a []string) error {
			return runGroup("exposures cloudflare", cloudflareUsage, a, s.out, map[string]func([]string) error{
				"show":   func(a []string) error { return cloudflareShow(a, s) },
				"set":    func(a []string) error { return cloudflareSet(a, s) },
				"remove": func(a []string) error { return cloudflareRemove(a, s) },
			})
		},
	})
}

const cloudflarePath = "/v1/exposures/cloudflare"

func printCloudflare(s session, st services.CloudflareSettings) {
	say(s.out, "Cloudflare account: %s", st.AccountID)
	say(s.out, "API token: set, hidden")
	say(s.out, "Updated %s.", ago(s.now(), st.UpdatedAt))
}

func cloudflareShow(args []string, s session) error {
	fs := newFlagSet("exposures cloudflare show", "[--hub URL]",
		"Show the Cloudflare account that services' cloudflare routes use. The API token is never shown.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	var st services.CloudflareSettings
	if err := hub.do(http.MethodGet, cloudflarePath, nil, &st); err != nil {
		return err
	}
	printCloudflare(s, st)
	return nil
}

func cloudflareSet(args []string, s session) error {
	fs := newFlagSet("exposures cloudflare set", "--account-id ID [--api-token-file PATH] [--hub URL]",
		"Set the Cloudflare account and API token that services' cloudflare routes use. The token needs "+expose.CloudflarePermissions+
			", and a route with access also "+expose.CloudflareAccessPermissions+". It asks for the token with typing hidden, or reads it from --api-token-file. The hub checks the token with Cloudflare "+
			"before it stores it.")
	hubURL := hubFlag(fs)
	account := fs.String("account-id", "", "`ID` the Cloudflare account ID, from the dashboard's Account home")
	tokenFile := fs.String("api-token-file", "", "`PATH` file with the Cloudflare API token, instead of asking")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	if *account == "" {
		return usageError(fs, "exposures cloudflare set needs --account-id")
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	token, err := s.secret("Cloudflare API token", *tokenFile, "api-token-file")
	if err != nil {
		return err
	}
	var st services.CloudflareSettings
	if err := hub.do(http.MethodPut, cloudflarePath, services.CloudflareInput{AccountID: *account, APIToken: &token}, &st); err != nil {
		return err
	}
	say(s.out, "Saved the Cloudflare settings. Cloudflare accepted the API token for account %s.", st.AccountID)
	return nil
}

func cloudflareRemove(args []string, s session) error {
	fs := newFlagSet("exposures cloudflare remove", "[--hub URL]",
		"Remove the hub's Cloudflare account and API token. It refuses while a service has a cloudflare route. "+
			"The tunnels and DNS records already in Cloudflare stay.")
	hubURL := hubFlag(fs)
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	if err := noArgs(fs, rest); err != nil {
		return err
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, cloudflarePath, nil, nil); err != nil {
		return err
	}
	say(s.out, "Removed the Cloudflare settings. The tunnels and DNS records in Cloudflare stay.")
	return nil
}
