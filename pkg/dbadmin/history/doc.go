// Package history persists every SQL query the operator runs through
// Aura DB — what they ran, when, against which connection, how long it
// took, what came back, what errored. The grid's "History" tab reads
// from here; saved-queries (PR #5.5/v0.3.1) layer on top.
//
// What this package owns:
//
//   - The Store interface: Append + List + Search + Get + Star + Tag +
//     Delete + DeleteOlderThan. Pluggable so the engine can plug a
//     panel-side store (sharing the panel's SQLite) or a standalone
//     store (its own SQLite file).
//   - A SQLite-backed default implementation using modernc.org/sqlite
//     (already in go.mod). Uses FTS5 when the SQLite build supports
//     it; falls back to LIKE-based search otherwise.
//   - Redaction at the storage boundary: every Entry's SQL field
//     passes through classifier.RedactSensitiveInline before
//     persistence, so CREATE USER passwords / IDENTIFIED BY values
//     never sit in the history table.
//
// What this package does NOT do:
//
//   - Auth. The Store accepts a UserID on every operation and
//     default-denies if it's empty; the engine layer is responsible
//     for filling it with the calling operator's ID. Cross-user
//     visibility (admin reading another operator's history) is a
//     policy decision that lives in the engine — for admin reads the
//     engine should fan out across known UserIDs rather than passing
//     an empty UserID to the Store.
//   - Schema-aware autocomplete suggestions. The history is the data
//     source for the "recently used tables" UX, but the suggester
//     itself is part of the SQL editor (frontend, v0.3.1).
//   - Retention enforcement on its own schedule. The engine calls
//     DeleteOlderThan from a periodic goroutine; this package just
//     exposes the operation.
//
// Concurrency:
//
//   - Store implementations are safe for concurrent use. The SQLite
//     default uses WAL mode + busy-timeout for sane multi-writer
//     behavior; reads are full-MVCC.
//
// Retention defaults:
//
//   - 365 days (SECURITY.md §9.2 says the panel audit log keeps 365
//     days; we mirror that here for consistency). Operators tune via
//     the panel's existing Config.Query block (the engine surfaces a
//     RetentionDays knob in PR #8).
//
// Known redaction gaps:
//
//   - classifier.RedactSensitiveInline only covers CREATE/ALTER
//     USER…IDENTIFIED BY, CREATE/ALTER ROLE…PASSWORD, and CREATE
//     USER…WITH PASSWORD. It does NOT cover MariaDB's IDENTIFIED
//     VIA <plugin> AS '<hash>', CREATE SUBSCRIPTION
//     CONNECTION='postgresql://user:pw@…', dblink_connect, or
//     COPY FROM PROGRAM 'curl https://user:pw@…' style leakage.
//     Operators who run those forms today will see the credentials
//     persisted in the history table. The classifier extension to
//     close this gap is tracked in KNOWN-ISSUES.md as a PR #7.5
//     item; until then, audit-sensitive deployments should either
//     disable history for credential-rotation runbooks or route
//     them through a separate admin tool that bypasses Aura DB.
package history
