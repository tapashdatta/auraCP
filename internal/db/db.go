// Package db provisions databases on the local engines. Each database picks its
// engine (MariaDB or PostgreSQL) independently. Identifiers are validated and
// commands run via arg arrays; generated passwords are stored encrypted.
package db

import (
	"context"
	"fmt"

	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/internal/system"
	"github.com/auracp/auracp/internal/validate"
)

const (
	MariaDB  = "mariadb"
	Postgres = "postgres"
)

type Manager struct {
	r     *system.Runner
	store *store.Store
	sec   *secret.Box
}

func New(r *system.Runner, st *store.Store, sec *secret.Box) *Manager {
	return &Manager{r: r, store: st, sec: sec}
}

// Create provisions a database + user on the chosen engine and records it.
// password is supplied by the caller (generated, alphanumeric — safe to embed).
func (m *Manager) Create(ctx context.Context, engine, siteDomain, name, dbUser, password string) error {
	if engine != MariaDB && engine != Postgres {
		return fmt.Errorf("unsupported engine: %q", engine)
	}
	if err := validate.Identifier(name); err != nil {
		return err
	}
	if err := validate.Identifier(dbUser); err != nil {
		return err
	}
	if err := validate.Domain(siteDomain); err != nil {
		return err
	}

	switch engine {
	case MariaDB:
		stmt := fmt.Sprintf(
			"CREATE DATABASE IF NOT EXISTS `%s`; "+
				"CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s'; "+
				"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'; FLUSH PRIVILEGES;",
			name, dbUser, password, name, dbUser)
		if _, err := m.r.Run(ctx, "mariadb", "-e", stmt); err != nil {
			return fmt.Errorf("mariadb provision: %w", err)
		}
	case Postgres:
		// Run as the postgres superuser via dropped-privilege exec.
		create := fmt.Sprintf("CREATE USER \"%s\" WITH PASSWORD '%s';", dbUser, password)
		if _, err := m.r.RunAs(ctx, "postgres", "psql", "-c", create); err != nil {
			return fmt.Errorf("postgres create user: %w", err)
		}
		mkdb := fmt.Sprintf("CREATE DATABASE \"%s\" OWNER \"%s\";", name, dbUser)
		if _, err := m.r.RunAs(ctx, "postgres", "psql", "-c", mkdb); err != nil {
			return fmt.Errorf("postgres create db: %w", err)
		}
	}

	enc, err := m.sec.Encrypt(password)
	if err != nil {
		return err
	}
	return m.store.CreateDatabaseRecord(store.Database{
		SiteDomain: siteDomain, Engine: engine, Name: name, DBUser: dbUser,
	}, enc)
}

// Drop removes a database and its user from the engine and the record.
func (m *Manager) Drop(ctx context.Context, engine, name, dbUser string) error {
	if err := validate.Identifier(name); err != nil {
		return err
	}
	switch engine {
	case MariaDB:
		stmt := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`; DROP USER IF EXISTS '%s'@'localhost';", name, dbUser)
		if _, err := m.r.Run(ctx, "mariadb", "-e", stmt); err != nil {
			return err
		}
	case Postgres:
		if _, err := m.r.RunAs(ctx, "postgres", "psql", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS \"%s\";", name)); err != nil {
			return err
		}
		_, _ = m.r.RunAs(ctx, "postgres", "psql", "-c", fmt.Sprintf("DROP USER IF EXISTS \"%s\";", dbUser))
	default:
		return fmt.Errorf("unsupported engine: %q", engine)
	}
	return m.store.DeleteDatabaseRecord(engine, name)
}
