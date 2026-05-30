// Package saved persists the SQL Editor's "Saved Queries" sidebar — the
// named snippets an operator stashes for re-use. v0.3.2-A replaces the
// in-memory store wired by httpapi.handlers_saved with a durable,
// per-(connection, user) SQLite-backed store so operator work survives
// daemon restarts.
//
// What this package owns:
//
//   - The Store interface: Append (create), Get, List, Search, Star,
//     Tag, Update, Delete, Close. The shape mirrors history.Store so
//     the same SQLite-process patterns (WAL pragma, busy_timeout,
//     bounded writer semaphore, prepared-statement cache) apply
//     identically.
//   - A SQLite-backed default implementation using modernc.org/sqlite.
//     FTS5 is used when the SQLite build supports it; falls back to
//     LIKE search otherwise. HasFTS exposes the runtime mode so the UI
//     can surface a "degraded search" caption (parity with history).
//   - Per-(connection, owner) uniqueness on Name so two users can each
//     own a snippet called "my-query" on the same connection. The
//     uniqueness lives in a single UNIQUE INDEX on
//     (connection_id, owner_id, name).
//
// What this package does NOT do:
//
//   - Auth. The Store accepts an OwnerID (== user_id) on every
//     operation and default-denies when empty. The httpapi layer pulls
//     OwnerID from the authenticated session and passes it down; the
//     Store never inspects roles or grants. Cross-user disclosure is
//     prevented by the (conn, owner) index — a List for owner B
//     against connection C returns zero rows even when owner A has
//     entries there.
//
// Retention / capacity:
//
//   - Per-(connection, owner) cap via OpenOpts.MaxPerOwner (default
//     256, matching the in-memory store's savedQueriesPerUser). When
//     Create would push the count over the cap, the OLDEST entry for
//     the same (conn, owner) tuple is deleted in the same transaction.
//     This preserves the "latest N saves" invariant the SDK documents.
//
// Concurrency:
//
//   - The Store is safe for concurrent use. WAL + busy_timeout cover
//     multi-writer behavior; reads use SQLite's MVCC snapshot. The
//     in-memory DSN (`:memory:` / `file::memory:?…`) pins the pool to
//     a single connection because modernc.org/sqlite gives each
//     connection a fresh DB unless shared-cache is requested.
//   - A process-wide writer semaphore (OpenOpts.MaxWriters, default 8)
//     bounds concurrent in-flight Create/Update/Star/Tag/Delete calls
//     so bursty UI saves don't pile up against the busy_timeout retry
//     path.
//
// Schema:
//
//   - Single table `saved_queries`: id (TEXT PK, ULID-shaped),
//     connection_id, owner_id (== user_id), name, statement,
//     description, tags (serialized fenced-comma form for FTS + LIKE),
//     starred (0/1), created_at + updated_at (UnixNano). UNIQUE index
//     on (connection_id, owner_id, name) enforces the per-owner-per-
//     connection uniqueness contract. Range index on
//     (connection_id, owner_id, created_at DESC) backs List ordering.
//   - Optional FTS5 virtual table `saved_queries_fts` over
//     (name, statement, description, tags). Triggers mirror writes
//     from the base table.
//
// Wire mapping:
//
//   - savedQueryDTO (httpapi/dto.go) ↔ Record here. The httpapi layer
//     supplies ID via newRequestID and CreatedAt via time.Now().UTC();
//     this package stores them verbatim. Tags are normalized to
//     []string{} on read (never nil) by the httpapi adapter so the
//     wire form stays stable.
package saved
