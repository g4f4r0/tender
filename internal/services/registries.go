package services

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/g4f4r0/tender/internal/server"
	"github.com/g4f4r0/tender/internal/services/deploy"
)

var registryPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:[0-9]{1,5})?$`)

func checkRegistry(name, host, username, password string) error {
	switch {
	case !server.ValidName(name):
		return server.Invalid("name must be lowercase letters, digits and dashes, starting with a letter or digit, at most 63, got %q.", name)
	case !registryPattern.MatchString(host):
		return server.Invalid("host must be the registry's host, as image names start with it, such as ghcr.io or registry.example.com:5000, got %q.", host)
	case username == "":
		return server.Invalid("username is required.")
	case password == "":
		return server.Invalid("password is required: the registry's password or access token.")
	case password == deploy.Hidden:
		return server.Invalid("password is \"***\", which is how the hub hides a secret. Give the password itself.")
	case strings.ContainsAny(username+password, "\n\r\x00"):
		return server.Invalid("username and password can't hold a line break.")
	}
	return nil
}

const selectRegistries = `SELECT id, name, host, username, password, created_at, updated_at FROM registries`

func scanRegistry(r scanner) (Registry, string, error) {
	var g Registry
	var sealed string
	var created, updated int64
	err := r.Scan(&g.ID, &g.Name, &g.Host, &g.Username, &sealed, &created, &updated)
	g.Password, g.PasswordSet = deploy.Hidden, sealed != ""
	g.CreatedAt, g.UpdatedAt = at(created), at(updated)
	return g, sealed, err
}

// Registries returns every registry, by name, with its password hidden.
func (s *Store) Registries(ctx context.Context) ([]Registry, error) {
	rs, err := s.db.Read.QueryContext(ctx, selectRegistries+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := []Registry{}
	for rs.Next() {
		g, _, err := scanRegistry(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rs.Err()
}

func registryNotFound(name string) error {
	return server.NotFound("Registry %q doesn't exist. Run tender registries list to see the names.", name)
}

func (s *Store) registry(ctx context.Context, name string) (Registry, string, error) {
	g, sealed, err := scanRegistry(s.db.Read.QueryRowContext(ctx, selectRegistries+` WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return g, "", registryNotFound(name)
	}
	return g, sealed, err
}

// AddRegistry stores a registry, its password encrypted.
func (s *Store) AddRegistry(ctx context.Context, in RegistryInput) (Registry, error) {
	if err := checkRegistry(in.Name, in.Host, in.Username, in.Password); err != nil {
		return Registry{}, err
	}
	sealed, err := s.seal(in.Password)
	if err != nil {
		return Registry{}, err
	}
	now := time.Now().Unix()
	if err := s.uniqueRegistry(ctx, in.Name, in.Host, 0); err != nil {
		return Registry{}, err
	}
	if _, err := s.db.Write.ExecContext(ctx, `INSERT INTO registries (name, host, username, password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.Host, in.Username, sealed, now, now); err != nil {
		return Registry{}, err
	}
	g, _, err := s.registry(ctx, in.Name)
	return g, err
}

// uniqueRegistry is a 409 when another registry than id has the name or the host.
func (s *Store) uniqueRegistry(ctx context.Context, name, host string, id int64) error {
	var other, otherHost string
	err := s.db.Read.QueryRowContext(ctx, `SELECT name, host FROM registries WHERE (name = ? OR host = ?) AND id != ? LIMIT 1`, name, host, id).
		Scan(&other, &otherHost)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case other == name:
		return server.Conflict("Registry %q already exists. Change it with tender registries update.", name)
	}
	return server.Conflict("Registry %q already has the host %s; a host has one login.", other, otherHost)
}

// UpdateRegistry applies a merge-patch to a registry. A password of "***" keeps the stored one.
func (s *Store) UpdateRegistry(ctx context.Context, name string, p RegistryPatch) (Registry, error) {
	g, sealed, err := s.registry(ctx, name)
	if err != nil {
		return Registry{}, err
	}
	host, username := g.Host, g.Username
	if p.Host != nil {
		host = *p.Host
	}
	if p.Username != nil {
		username = *p.Username
	}
	changed := p.Password != nil && *p.Password != deploy.Hidden
	password := "the stored one"
	if changed {
		password = *p.Password
	}
	if err := checkRegistry(name, host, username, password); err != nil {
		return Registry{}, err
	}
	if changed {
		if sealed, err = s.seal(password); err != nil {
			return Registry{}, err
		}
	}
	if err := s.uniqueRegistry(ctx, name, host, g.ID); err != nil {
		return Registry{}, err
	}
	if _, err := s.db.Write.ExecContext(ctx, `UPDATE registries SET host = ?, username = ?, password = ?, updated_at = ? WHERE id = ?`,
		host, username, sealed, time.Now().Unix(), g.ID); err != nil {
		return Registry{}, err
	}
	g, _, err = s.registry(ctx, name)
	return g, err
}

// DeleteRegistry deletes a registry. Services that pull from it pull without a login from then on.
func (s *Store) DeleteRegistry(ctx context.Context, name string) error {
	res, err := s.db.Write.ExecContext(ctx, `DELETE FROM registries WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return registryNotFound(name)
	}
	return nil
}

// logins returns every registry's login, passwords opened, for a deploy.
func (s *Store) logins(ctx context.Context) ([]deploy.Login, error) {
	rs, err := s.db.Read.QueryContext(ctx, selectRegistries+` ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type sealedLogin struct {
		deploy.Login
		name, sealed string
	}
	var list []sealedLogin
	for rs.Next() {
		g, sealed, err := scanRegistry(rs)
		if err != nil {
			return nil, errors.Join(err, rs.Close())
		}
		list = append(list, sealedLogin{Login: deploy.Login{Host: g.Host, Username: g.Username}, name: g.Name, sealed: sealed})
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return nil, err
	}
	out := []deploy.Login{}
	for _, l := range list {
		var err error
		if l.Password, err = s.open("registry "+l.name+"'s password", l.sealed); err != nil {
			return nil, err
		}
		out = append(out, l.Login)
	}
	return out, nil
}
