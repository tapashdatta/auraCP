package standalone

import (
	"context"
	"fmt"
)

// migration is a single (version, body) tuple. Bodies execute under a
// transaction; on success we bump schema_version.
type migration struct {
	Version int
	SQL     string
}

// migrations is the ordered list. Append-only — never edit a past entry.
var migrations = []migration{
	{
		Version: 1,
		SQL: `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS users (
			id              TEXT PRIMARY KEY,
			username        TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash   TEXT NOT NULL,
			password_ver    INTEGER NOT NULL DEFAULT 1,
			mfa_secret_enc  BLOB,
			mfa_required    INTEGER NOT NULL DEFAULT 0,
			disabled        INTEGER NOT NULL DEFAULT 0,
			created_at      INTEGER NOT NULL,
			updated_at      INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS recovery_codes (
			user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			code_hash  TEXT NOT NULL,
			used_at    INTEGER,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (user_id, code_hash)
		);

		CREATE TABLE IF NOT EXISTS sessions (
			token_hash             BLOB PRIMARY KEY,
			user_id                TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at             INTEGER NOT NULL,
			last_used_at           INTEGER NOT NULL,
			expires_at             INTEGER NOT NULL,
			absolute_expires_at    INTEGER NOT NULL,
			ip_class               TEXT NOT NULL,
			ua_hash                TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_user_created ON sessions(user_id, created_at);

		CREATE TABLE IF NOT EXISTS step_up_flags (
			token_hash   BLOB NOT NULL REFERENCES sessions(token_hash) ON DELETE CASCADE,
			action_class TEXT NOT NULL,
			jti          TEXT NOT NULL,
			expires_at   INTEGER NOT NULL,
			PRIMARY KEY (token_hash, action_class)
		);

		CREATE TABLE IF NOT EXISTS login_attempts (
			scope        TEXT NOT NULL,
			attempted_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_login_attempts_scope_time ON login_attempts(scope, attempted_at);

		CREATE TABLE IF NOT EXISTS lockouts (
			scope      TEXT PRIMARY KEY,
			count      INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS connections (
			id                 TEXT PRIMARY KEY,
			name               TEXT NOT NULL UNIQUE,
			engine             INTEGER NOT NULL,
			host               TEXT NOT NULL,
			port               INTEGER NOT NULL,
			database           TEXT NOT NULL DEFAULT '',
			username           TEXT NOT NULL,
			creds_enc          BLOB NOT NULL,
			tags               TEXT NOT NULL DEFAULT '',
			use_ssl            INTEGER NOT NULL DEFAULT 1,
			sslmode            TEXT NOT NULL DEFAULT '',
			ssh_tunnel_json    TEXT NOT NULL DEFAULT '',
			origin             TEXT NOT NULL DEFAULT 'manual',
			owner              TEXT NOT NULL,
			created_at         INTEGER NOT NULL,
			updated_at         INTEGER NOT NULL,
			accept_insecure_at INTEGER
		);

		CREATE TABLE IF NOT EXISTS connection_grants (
			user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
			role          INTEGER NOT NULL,
			granted_by    TEXT NOT NULL,
			granted_at    INTEGER NOT NULL,
			PRIMARY KEY (user_id, connection_id)
		);
		CREATE INDEX IF NOT EXISTS idx_grants_conn ON connection_grants(connection_id);
		`,
	},
	{
		Version: 2,
		// Add last_totp_step column to users table to enforce TOTP
		// single-use semantics. A code that matched at step N is rejected
		// if N <= last_totp_step (the same code can never be reused, and
		// older codes within the ±1 lookback window are also rejected).
		// Defaults to 0 so the very first TOTP verification always wins.
		SQL: `ALTER TABLE users ADD COLUMN last_totp_step INTEGER NOT NULL DEFAULT 0;`,
	},
}

// migrate brings the database up to the latest schema version.
func (s *Store) migrate(ctx context.Context) error {
	// Ensure schema_version exists so we can read current version.
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("standalone: bootstrap schema_version: %w", err)
	}

	var current int
	row := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("standalone: read schema_version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("standalone: begin migrate %d: %w", m.Version, err)
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("standalone: migrate %d: %w", m.Version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO schema_version (version, applied_at) VALUES (?, ?)`,
			m.Version, s.clock().UnixNano()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("standalone: record migrate %d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("standalone: commit migrate %d: %w", m.Version, err)
		}
	}
	return nil
}
