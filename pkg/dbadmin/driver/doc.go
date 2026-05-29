// Package driver opens database connections, executes queries, and streams
// results back, on behalf of the Aura DB engine. It is the only package
// that talks to MariaDB / MySQL or PostgreSQL directly; every other
// package consumes the abstractions defined here.
//
// Responsibilities:
//
//   - Open a per-(connection, user) backend connection, optionally through
//     an SSH tunnel.
//   - Execute classified SQL with strict resource limits (per-query
//     timeout, result row cap, result byte cap, concurrent-query cap).
//   - Iterate rows in a streaming, allocation-aware fashion so the HTTP
//     handler can write JSON to the response without buffering the full
//     result set.
//   - Map engine-specific errors to a small set of typed driver errors
//     the engine can reason about (ErrTimeout, ErrAuth, ErrUnavailable,
//     ErrSyntax, ErrPermission, ErrConflict).
//   - Pool backend connections per Aura DB connection so concurrent
//     queries reuse warm sockets, with idle eviction so an inactive site
//     drops to zero open backend connections within Config.Query.PoolIdleTimeout.
//
// Out of scope (lives elsewhere):
//
//   - Authorization. The engine calls Auth.HasPermission BEFORE invoking
//     the driver; the driver assumes "if you can call me, you're allowed."
//   - Classification. The engine runs the classifier before dispatch; the
//     driver assumes "if you handed me this SQL, it's not forbidden."
//   - Audit emission. The engine emits audit events; the driver just
//     returns its result + error.
//
// Security posture (see SECURITY.md §7 and §10):
//
//   - SSH tunnel keys are key-based only. Password auth is refused at the
//     library level; agent forwarding is disabled.
//   - The MySQL driver disables AllowAllFiles (LOAD DATA LOCAL INFILE),
//     AllowCleartextPasswords, and AllowOldPasswords explicitly. These are
//     belt-and-braces — the classifier already refuses LOAD DATA INFILE,
//     but defense in depth matters.
//   - TLS to the database is required when Connection.UseSSL is true; the
//     "prod" tag forces UseSSL on regardless of the operator's stored
//     value (enforced by the engine, not the driver — but the driver
//     refuses to downgrade once UseSSL is set).
//   - Result limits are enforced in the driver, not in the handler.
//     A misbehaving handler that forgot to wrap a result reader STILL
//     gets capped because the cap lives on the Rows wrapper itself.
//
// Stability: the public types and the Driver / Conn / Rows interfaces
// follow the same semver rules as the rest of the SDK (see
// docs/aura-db/SDK.md §8).
package driver
