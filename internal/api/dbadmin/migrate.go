package dbadmin

import (
	"database/sql"

	"github.com/auracp/auracp/internal/store"
)

// init wires the Aura DB migrator into the panel's migration chain so
// fresh databases get the aura_db_* tables on first boot, before Mount
// runs. Mount also calls RunMigrations explicitly; both paths are safe
// because every statement is idempotent.
func init() {
	store.RegisterExtraMigrator(RunMigrations)
}

// migrations is the additive DDL appended to the panel's existing
// migration chain. Idempotent — every statement is IF NOT EXISTS so a
// re-run on an existing database is a no-op.
//
// Table naming uses the "aura_db_" prefix so namespace collisions with
// the panel's tables are structurally impossible. Aura DB owns the
// lifecycle of these tables; the panel is unaware of them.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS aura_db_connections (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL UNIQUE,
		engine          INTEGER NOT NULL,
		host            TEXT NOT NULL,
		port            INTEGER NOT NULL,
		database        TEXT NOT NULL DEFAULT '',
		username        TEXT NOT NULL,
		creds_enc       TEXT NOT NULL,
		tags            TEXT NOT NULL DEFAULT '',
		use_ssl         INTEGER NOT NULL DEFAULT 1,
		sslmode         TEXT NOT NULL DEFAULT '',
		ssh_tunnel_json TEXT NOT NULL DEFAULT '',
		origin          TEXT NOT NULL DEFAULT 'manual',
		owner           TEXT NOT NULL,
		created_at      INTEGER NOT NULL,
		updated_at      INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS aura_db_grants (
		user_id        TEXT NOT NULL,
		connection_id  TEXT NOT NULL REFERENCES aura_db_connections(id) ON DELETE CASCADE,
		role           INTEGER NOT NULL,
		granted_by     TEXT NOT NULL,
		granted_at     INTEGER NOT NULL,
		PRIMARY KEY (user_id, connection_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_aura_db_grants_user ON aura_db_grants(user_id)`,
	// PR #10.5 / FIX-INT-6: panel_user delete must cascade into
	// aura_db_grants so orphan grant rows do not survive a user
	// deletion. A declarative FK to panel_users(id) is awkward here
	// because aura_db_grants.user_id is TEXT (the panel writes the
	// user_id as a string for cross-engine consistency) while
	// panel_users.id is INTEGER; SQLite's strict FK enforcement
	// requires matching declared affinity. We solve it with a trigger
	// that runs on every DELETE FROM panel_users — same semantic
	// effect (orphan grants deleted with the user), no schema
	// gymnastics. Idempotent: CREATE TRIGGER IF NOT EXISTS.
	`CREATE TRIGGER IF NOT EXISTS trg_aura_db_grants_cascade_panel_user
		AFTER DELETE ON panel_users
		BEGIN
			DELETE FROM aura_db_grants WHERE user_id = CAST(OLD.id AS TEXT);
		END`,
}

// RunMigrations applies the Aura DB schema additions to the panel's
// SQLite database. Safe to call on every boot — every statement is
// idempotent. Returns the first failure encountered (later statements
// are skipped).
func RunMigrations(db *sql.DB) error {
	if db == nil {
		return nil
	}
	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
