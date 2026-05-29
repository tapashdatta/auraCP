// Package dbadmin is the panel-side glue that mounts pkg/dbadmin inside
// the auracpd HTTP server. It maps the panel's existing session +
// permission + secret-encryption surfaces onto the three abstractions the
// engine requires (dbadmin.Auth, dbadmin.ConnectionStore, dbadmin.AuditSink)
// and exposes a single public entrypoint, Mount, that wires everything
// onto the panel's *http.ServeMux at /api/dbadmin/.
//
// Adminer was removed in PR #17 (v0.3.0). Aura DB is the sole DB
// admin surface; the legacy /_adminer/ nginx route, the PHP-FPM
// auracp-adminer pool, the SSO-token bridge under /run/auracp/
// adminer-sso/, and the bundled wrapper at /opt/auracp/adminer/ no
// longer exist. All database management flows through /api/dbadmin/
// (this package) and /dbadmin/ (the embedded Svelte SPA).
//
// Schema strategy (see migrate.go):
//
//   - All persistent state for Aura DB connections + grants lives in
//     namespaced tables inside the panel's existing auracp.db (aura_db_*
//     prefix). One backup file; one secret key.
//
// Authentication strategy (see auth.go):
//
//   - The panel session cookie ("auracp_session") drives identity. There
//     is no parallel session/cookie. Step-up flags are stored in-memory,
//     keyed by panel session token, with a 5-minute TTL.
//
// Audit strategy (see audit.go):
//
//   - Events are dual-written: (1) the SHA-256 hash-chained NDJSON log at
//     /var/lib/auracp/aura-db/audit.ndjson (forensic source of truth),
//     and (2) the panel's existing audit_log table (operator UX — the
//     panel "Audit" page sees Aura-DB rows alongside site events).
package dbadmin
