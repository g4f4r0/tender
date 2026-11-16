package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/g4f4r0/tender/internal/config"
	"github.com/g4f4r0/tender/internal/db"
	"github.com/g4f4r0/tender/internal/install"
	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/tokens"
)

var tokensUsage = groupUsage("tokens", "Make and remove the API tokens that people and AI clients send to the hub. "+
	"The hub refuses every request from people and AI clients without one. Make the first one on the hub machine with: "+
	"sudo tender tokens add NAME", []row{
	{"list", "List the API tokens and when each was last used"},
	{"add NAME", "Make an API token and print it once"},
	{"remove NAME --yes", "Delete an API token; clients that use it are refused"},
})

// runTokens runs "tender tokens list|add|remove".
func runTokens(args []string, s session) error {
	return runGroup("tokens", tokensUsage, args, s.out, map[string]func([]string) error{
		"list":   func(a []string) error { return tokensList(a, s) },
		"add":    func(a []string) error { return tokensAdd(a, s) },
		"remove": func(a []string) error { return tokensRemove(a, s) },
	})
}

func tokensList(args []string, s session) error {
	fs := newFlagSet("tokens list", "[--hub URL]", "List the API tokens and when each was last used. The tokens themselves are never shown again.")
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
	list, err := listAll[tokens.Token](hub, "/v1/tokens")
	if err != nil {
		return err
	}
	rows := make([][]string, len(list))
	for i, t := range list {
		rows[i] = []string{t.Name, "never", ago(s.now(), t.CreatedAt)}
		if t.LastUsedAt != nil {
			rows[i][1] = ago(s.now(), *t.LastUsedAt)
		}
	}
	printTable(s.out, []string{"NAME", "LAST USED", "CREATED"}, rows)
	return nil
}

func tokensAdd(args []string, s session) error {
	fs := newFlagSet("tokens add", "NAME [--hub URL] [--database PATH]",
		"Make an API token and print it, once. Save it in ~/.config/tender/token, set TENDER_TOKEN to it, or give it to an AI client.\n\n"+
			"Run as root on the hub machine with no token, as \"sudo tender tokens add NAME\", it writes to the hub's database "+
			"directly: that is how the first token is made. Otherwise it asks the hub, with the token you have.")
	hubURL := hubFlag(fs)
	database := fs.String("database", "", "`PATH` the hub's database, when writing to it directly (default: "+config.EnvDatabase+", else the one in /etc/tender/hub.env)")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a token name, such as laptop or claude-code")
	if err != nil {
		return err
	}
	token, _, err := apiToken(fs)
	if err != nil {
		return err
	}
	var created tokens.TokenCreated
	if token == "" && isRoot() {
		path := *database
		if path == "" {
			path = os.Getenv(config.EnvDatabase)
		}
		if path == "" {
			path = install.HubDatabase(config.Root())
		}
		if created, err = addTokenToDatabase(context.Background(), path, name); err != nil {
			return err
		}
	} else {
		if *database != "" {
			return usageError(fs, "--database works only as root, without a token, on the hub machine")
		}
		hub, err := newHubAPI(fs, *hubURL)
		if err != nil {
			return err
		}
		if err := hub.do(http.MethodPost, "/v1/tokens", tokens.TokenInput{Name: name}, &created); err != nil {
			return err
		}
	}
	say(s.out, "Made API token %s.\n", created.Name)
	say(s.out, "The token, shown only now. Save it before you go on:")
	say(s.out, "  %s\n", created.Value)
	say(s.out, "Use it from the tender command:")
	say(s.out, "  export %s=%s\n", envToken, created.Value)
	say(s.out, "Or save it for the tender command on another machine:")
	say(s.out, "  %s", saveTokenLine(created.Value))
	return nil
}

// saveTokenLine is a shell line that saves token as the tender command's token file, readable by
// its owner only.
func saveTokenLine(token string) string {
	return "mkdir -p ~/.config/tender && (umask 077; printf %s '" + token + "' > ~/.config/tender/token)"
}

// addTokenToDatabase makes a token in the hub's database at path, without the hub. The database
// must have the current schema, which the hub makes when it starts. The files SQLite makes beside
// it are given to the database's owner, so the hub can still open them.
func addTokenToDatabase(ctx context.Context, path, name string) (tokens.TokenCreated, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return tokens.TokenCreated{}, fmt.Errorf("there is no hub database at %s, so this is not the hub machine, or the hub has not run yet. "+
			"On the hub machine, run \"sudo tender tokens add NAME\" there, or pass --database PATH; anywhere else, set %s to a token you have", path, envToken)
	}
	d, err := db.Open(path)
	if err != nil {
		return tokens.TokenCreated{}, fmt.Errorf("cannot open the hub database %s. Check that you can read and write it, or pass --database PATH: %w", path, err)
	}
	defer giveToOwner(path, fi)
	created, err := func() (tokens.TokenCreated, error) {
		if err := d.CheckSchema(ctx, migrations()...); err != nil {
			return tokens.TokenCreated{}, fmt.Errorf("the database at %s has no current schema. Start the hub once on this version, "+
				"with \"sudo systemctl restart tenderhub\", then run this again: %w", path, err)
		}
		return tokens.NewStore(d).Create(ctx, name)
	}()
	return created, errors.Join(err, d.Close())
}

// ownerWait is how long "install hub" waits for the hub it started to make its database.
var ownerWait = time.Minute

// makeOwnerToken waits up to ownerWait for the hub that "install hub" just started to make its
// database at path with the current schema, then makes the API token owner in it, or owner-2 and
// up when that name is taken, and returns its name and value.
func makeOwnerToken(ctx context.Context, path string) (string, string, error) {
	deadline := time.Now().Add(ownerWait)
	for n := 1; ; {
		name := "owner"
		if n > 1 {
			name += "-" + strconv.Itoa(n)
		}
		created, err := addTokenToDatabase(ctx, path, name)
		var e *server.Error
		switch {
		case err == nil:
			return created.Name, created.Value, nil
		case errors.As(err, &e) && e.Status == http.StatusConflict && n < 9:
			n++
			continue
		case errors.As(err, &e) || !time.Now().Before(deadline):
			return "", "", err
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// giveToOwner gives the database's WAL and shared-memory files to the owner of the database, fi,
// when root made them, so the hub can open them.
func giveToOwner(path string, fi os.FileInfo) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return
	}
	for _, p := range []string{path + "-wal", path + "-shm"} {
		_ = os.Lchown(p, int(st.Uid), int(st.Gid)) // a file that doesn't exist needs nothing
	}
}

// hubHasTokens reports whether the hub's database at path has an API token. It opens it read-only
// and makes no file, so it is safe as root beside a running hub.
func hubHasTokens(ctx context.Context, path string) (bool, error) {
	d, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false, err
	}
	defer func() { _ = d.Close() }()
	var n int
	err = d.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens`).Scan(&n)
	return n > 0, err
}

func tokensRemove(args []string, s session) error {
	fs := newFlagSet("tokens remove", "NAME --yes [--hub URL]",
		"Delete an API token. Every client that sends it is refused from its next request on. Deleting the last one locks "+
			"everyone out until a new one is made on the hub machine with sudo tender tokens add NAME, so the command needs --yes.")
	hubURL := hubFlag(fs)
	yes := fs.Bool("yes", false, "confirm the removal")
	rest, err := parseWithArgs(fs, args, s.out)
	if err != nil {
		return err
	}
	name, err := oneArg(fs, rest, "a token name")
	if err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("this deletes the API token %s, and every client that uses it is refused. Run \"tender tokens remove %s --yes\" to confirm", name, name)
	}
	hub, err := newHubAPI(fs, *hubURL)
	if err != nil {
		return err
	}
	if err := hub.do(http.MethodDelete, "/v1/tokens/"+url.PathEscape(name), nil, nil); err != nil {
		return err
	}
	say(s.out, "Deleted API token %s. Clients that send it are refused.", name)
	return nil
}
