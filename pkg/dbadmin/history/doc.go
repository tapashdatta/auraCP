// Package history persists every SQL query the operator runs through
// Aura DB — what they ran, when, against which connection, how long it
// took, what came back, what errored. The grid's "History" tab reads
// from here; saved-queries (PR #5.5/v0.3.1) layer on top.
//
// What this package owns:
//
//   - The Store interface: Append + List + Search + Get + Star + Tag +
//     Delete + DeleteOlderThan + StartRetentionLoop + HasFTS. Pluggable
//     so the engine can plug a panel-side store (sharing the panel's
//     SQLite) or a standalone store (its own SQLite file).
//   - A SQLite-backed default implementation using modernc.org/sqlite
//     (already in go.mod). Uses FTS5 when the SQLite build supports
//     it; falls back to LIKE-based search otherwise. The fallback is
//     observable via Store.HasFTS so callers can warn the UI; pass
//     OpenOpts.RequireFTS5 to make Open fail when FTS5 is missing.
//   - Redaction at the storage boundary: every Entry's SQL field
//     passes through classifier.RedactSensitiveInline before
//     persistence, so CREATE USER passwords / IDENTIFIED BY / VIA
//     hashes / CONNECTION strings / dblink credentials / COPY FROM
//     PROGRAM shell text / credentialed URIs (postgresql://,
//     mysql://, mongodb://, …) never sit in the history table.
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
//
// Retention:
//
//   - Time-based via StartRetentionLoop(ctx, period, retention). The
//     engine layer wires this in from the panel's periodic-task
//     scheduler. Defaults to 365 days (SECURITY.md §9.2 mirrors that
//     for the panel audit log). DeleteOlderThan is chunked in
//     1000-row batches so a long-overdue sweep doesn't lock the DB.
//   - Row-cap via OpenOpts.MaxRows. Append-time eviction of the
//     oldest entries when the cap is exceeded. Use a generous cap
//     (10⁶ or more) as a backstop alongside time-based retention.
//
// Concurrency:
//
//   - Store implementations are safe for concurrent use. The SQLite
//     default uses WAL mode + busy-timeout for sane multi-writer
//     behavior; reads are full-MVCC.
//   - In-memory databases (`:memory:` and `file::memory:?…` shared-
//     cache URIs) pin the connection pool to a single connection.
//     modernc.org/sqlite creates a fresh DB per connection unless
//     shared-cache is requested, so a pool > 1 would split writes
//     across disjoint databases.
//   - A process-wide writer semaphore (OpenOpts.MaxWriters, default 8)
//     bounds concurrent in-flight Append/Star/Tag/Delete calls. SQLite
//     serializes writes internally; the semaphore prevents bursty
//     goroutine piles from chewing the panel UI's request budget on
//     the busy_timeout retry path.
//
// Storage overhead:
//
//   - FTS5 adds roughly 1.8× the entries-table size for the
//     entries_fts index. On a panel storing 10⁶ rows × ~256 bytes
//     average SQL that's ~500 MB. Disable FTS5 by building against a
//     SQLite without it (Open will return Store with hasFTS=false
//     and LIKE search) or by leaving OpenOpts.RequireFTS5 unset —
//     the package degrades cleanly. There is currently no opt-out
//     toggle on the FTS5-available side; if the operator wants to
//     keep storage tight they should rotate the history DB more
//     aggressively (lower retention) rather than disabling FTS.
//
// Schema versions:
//
//   - v1 (PR #7): entries table + serialized tags column + per-user
//     starred index + FTS5 virtual table.
//   - v2 (PR #7.5): normalized entry_tags(tag, entry_id) table for
//     indexed tag-filter lookups; admin-scope starred partial index
//     for cross-user listings; FTS5 trigger install separated from
//     the probe so trigger errors surface. Forward-compatible: v1
//     databases initialize the v2 tables on next Open.
package history
