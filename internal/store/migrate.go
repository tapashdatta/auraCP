package store

import (
	"database/sql"
	"strings"
	"sync"
)

// extraMigrators is a registry of sub-package migrators run after the
// core panel schema is applied. Sub-packages register via
// RegisterExtraMigrator at init time to avoid importing the store
// package back into themselves.
var (
	extraMigratorsMu sync.RWMutex
	extraMigrators   []func(*sql.DB) error
)

// RegisterExtraMigrator appends a migrator to the chain. Idempotency is
// the caller's responsibility (use CREATE TABLE IF NOT EXISTS, etc.).
// Safe to call from package init.
func RegisterExtraMigrator(fn func(*sql.DB) error) {
	if fn == nil {
		return
	}
	extraMigratorsMu.Lock()
	defer extraMigratorsMu.Unlock()
	extraMigrators = append(extraMigrators, fn)
}

// Schema migrations. Kept as ordered statements; a real migration table comes
// later. Mirrors the data model in docs/ARCHITECTURE.md (trimmed for P0).
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS sites (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		type          TEXT NOT NULL,                 -- wordpress|php|nodejs|python|static|reverseproxy
		domain        TEXT NOT NULL UNIQUE,
		site_user     TEXT NOT NULL,
		root_path     TEXT NOT NULL,
		app           TEXT NOT NULL,                 -- display label, e.g. "PHP 8.4"
		node_version  TEXT,                          -- nullable
		port          INTEGER NOT NULL DEFAULT 0,    -- backend loopback port (app types)
		upstream      TEXT NOT NULL DEFAULT '',      -- reverse-proxy target
		php_version   TEXT NOT NULL DEFAULT '',      -- php/wordpress only
		pm2_enabled   INTEGER NOT NULL DEFAULT 0,    -- nodejs only — run via pm2-runtime
		status        TEXT NOT NULL DEFAULT 'up',    -- up|warn|down
		status_text   TEXT NOT NULL DEFAULT 'Online',
		created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS panel_users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		email         TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL DEFAULT 'ROLE_ADMIN',  -- ROLE_ADMIN|ROLE_SITE_MANAGER|ROLE_USER
		permissions   TEXT NOT NULL DEFAULT '',            -- JSON CRUD matrix (empty = role default)
		totp_secret   TEXT,
		created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS site_config (
		site_domain TEXT NOT NULL,
		key         TEXT NOT NULL,
		value       TEXT NOT NULL,
		PRIMARY KEY (site_domain, key)
	)`,
	`CREATE TABLE IF NOT EXISTS ssh_users (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		site_domain  TEXT NOT NULL,
		username     TEXT NOT NULL UNIQUE,
		type         TEXT NOT NULL DEFAULT 'sftp',         -- ssh|sftp
		password_enc TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS database_servers (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		engine     TEXT NOT NULL,                    -- mariadb|postgres
		host       TEXT NOT NULL DEFAULT '127.0.0.1',
		port       INTEGER NOT NULL,
		version    TEXT,
		is_default INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS node_runtimes (
		version    TEXT PRIMARY KEY,             -- e.g. "22.11.0"
		path       TEXT NOT NULL,                -- /opt/auracp/node/<version>
		is_default INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		ts      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		actor   TEXT NOT NULL,
		action  TEXT NOT NULL,
		target  TEXT NOT NULL DEFAULT '',
		detail  TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE IF NOT EXISTS backups (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		site_domain TEXT NOT NULL,
		kind        TEXT NOT NULL,                  -- site|database|panel
		path        TEXT NOT NULL,
		size_bytes  INTEGER NOT NULL DEFAULT 0,
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS cron_jobs (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		site_domain  TEXT NOT NULL,
		site_user    TEXT NOT NULL,
		schedule     TEXT NOT NULL,
		command      TEXT NOT NULL,
		enabled      INTEGER NOT NULL DEFAULT 1,
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS databases (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		site_domain   TEXT NOT NULL,
		engine        TEXT NOT NULL,                  -- mariadb|postgres
		name          TEXT NOT NULL,
		db_user       TEXT NOT NULL,
		password_enc  TEXT NOT NULL,                  -- encrypted at rest
		created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(engine, name)
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token       TEXT PRIMARY KEY,
		user_id     INTEGER NOT NULL REFERENCES panel_users(id) ON DELETE CASCADE,
		mfa_pending INTEGER NOT NULL DEFAULT 0,       -- 1 = password ok, awaiting TOTP
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at  TIMESTAMP NOT NULL
	)`,
	// v0.2.0: ACME state — auracpd's in-process lego owns issuance/renewal,
	// nginx serves the resulting cert. One row per domain (panel + every site).
	`CREATE TABLE IF NOT EXISTS certificates (
		domain      TEXT PRIMARY KEY,
		issuer      TEXT NOT NULL DEFAULT 'letsencrypt',
		cert_path   TEXT,
		key_path    TEXT,
		issued_at   INTEGER,                          -- unix ts
		expires_at  INTEGER,                          -- unix ts
		status      TEXT NOT NULL DEFAULT 'pending',  -- pending|issued|failed|renewing
		last_error  TEXT NOT NULL DEFAULT '',
		attempts    INTEGER NOT NULL DEFAULT 0
	)`,
	// v0.2.0: php-fpm versions installed side-by-side (8.3 / 8.4 / 8.5) via
	// deb.sury.org. Mirrors node_runtimes shape so the UI is symmetric.
	`CREATE TABLE IF NOT EXISTS php_runtimes (
		version    TEXT PRIMARY KEY,             -- "8.4"
		installed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		is_default INTEGER NOT NULL DEFAULT 0
	)`,
	// v0.2.0: per-site PHP runtime overrides written into the FPM pool config.
	// Triple (domain, key, value); allows the panel to tune memory_limit /
	// upload_max_filesize / etc. per site without touching the pool template.
	`CREATE TABLE IF NOT EXISTS php_settings (
		domain TEXT NOT NULL,
		key    TEXT NOT NULL,
		value  TEXT NOT NULL,
		PRIMARY KEY (domain, key)
	)`,
	// v0.2.15: per-user "allowed sites" scope. JSON array of domains; empty
	// string means "all sites" (back-compat default). Non-admin users with a
	// non-empty scope are limited to those domains in the sites listing and on
	// per-site mutations. ALTER TABLE is idempotent via the error-swallow in
	// migrate() below ("duplicate column" on re-run is fine).
	`ALTER TABLE panel_users ADD COLUMN sites_scope TEXT NOT NULL DEFAULT ''`,
}

func (s *Store) migrate() error {
	for _, stmt := range migrations {
		if _, err := s.DB.Exec(stmt); err != nil {
			// Idempotent ALTERs: on second startup the column already exists.
			// SQLite returns "duplicate column name: <col>" — safe to skip.
			if isAlterAddColumn(stmt) && isDuplicateColumn(err) {
				continue
			}
			return err
		}
	}
	// Extra migrations registered by sub-packages. Used by internal/api/dbadmin
	// to append its aura_db_* tables without creating an import cycle
	// (store cannot import internal/api/dbadmin, but the integration
	// package can install its migrator via RegisterExtraMigrator at
	// package init time).
	extraMigratorsMu.RLock()
	defer extraMigratorsMu.RUnlock()
	for _, fn := range extraMigrators {
		if err := fn(s.DB); err != nil {
			return err
		}
	}
	return nil
}

func isAlterAddColumn(stmt string) bool {
	return strings.HasPrefix(strings.TrimSpace(stmt), "ALTER TABLE")
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column")
}
