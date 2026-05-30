package dbadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/auracp/auracp/internal/secret"
	"github.com/auracp/auracp/internal/store"
	"github.com/auracp/auracp/pkg/dbadmin"
)

// EnrollParams describes a panel-managed database to mirror as an Aura DB
// connection so an operator can open it from the panel's "Manage" button
// (or the /dbadmin deep-link) without re-entering credentials.
type EnrollParams struct {
	Engine   string // panel engine string: "mariadb" | "postgres"
	Database string // database name
	Username string // database user
	Password string // plaintext — available only at create time
	OwnerID  string // panel user id (string) to grant RoleOwner; "" = admins-only
}

// EnsureConnection idempotently creates an aura_db_connections row for a
// panel-managed database, pointing at TCP loopback (127.0.0.1) on the
// engine's standard port. Credentials are encrypted under the panel KEK with
// the dbadmin CredsAAD label (same as connections created via the Aura DB UI).
//
// Idempotent: matched by (engine, database, host) so re-creating the same
// database — or calling this twice — never duplicates a connection.
//
// When OwnerID is non-empty the creator gets RoleOwner (so a site-manager who
// created the database can immediately open it); admins see every connection
// regardless. When OwnerID is empty the connection is admins-only.
func EnsureConnection(ctx context.Context, st *store.Store, box *secret.Box, p EnrollParams) error {
	if st == nil || box == nil {
		return errors.New("dbadmin enroll: nil store or secret box")
	}
	var (
		engine dbadmin.EngineKind
		port   int
	)
	switch strings.ToLower(p.Engine) {
	case "mariadb", "mysql":
		engine, port = dbadmin.EngineMariaDB, 3306
	case "postgres", "postgresql":
		engine, port = dbadmin.EnginePostgres, 5432
	default:
		return fmt.Errorf("dbadmin enroll: unsupported engine %q", p.Engine)
	}
	const host = "127.0.0.1"

	// Idempotency: skip if a connection for this engine+database already
	// exists on the loopback host.
	var existing int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM aura_db_connections WHERE engine = ? AND database = ? AND host = ?`,
		int(engine), p.Database, host).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}

	conns := newPanelConns(st, box)
	conn := dbadmin.Connection{
		Name:     p.Database,
		Engine:   engine,
		Host:     host,
		Port:     port,
		Database: p.Database,
		Username: p.Username,
		Origin:   dbadmin.OriginPanelSite,
		Owner:    p.OwnerID,
	}
	creds := dbadmin.Credentials{Password: p.Password}

	_, err := conns.Save(ctx, conn, creds)
	if errors.Is(err, dbadmin.ErrConflict) {
		// Another database elsewhere already uses this display name. Retry
		// with an engine-qualified name; matching for auto-connect is on
		// engine+database, not the display name, so this stays discoverable.
		conn.Name = fmt.Sprintf("%s (%s)", p.Database, engine.String())
		_, err = conns.Save(ctx, conn, creds)
	}
	return err
}
