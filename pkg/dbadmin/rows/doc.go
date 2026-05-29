// Package rows provides safe, parameterized row operations for Aura DB:
// paginated reads with filter + sort, primary-key-anchored row updates
// and deletes, and inserts with optional column-set RETURNING.
//
// Why this exists separately from the driver and the classifier:
//
//   - The grid editor in the panel UI must mutate rows without exposing
//     raw SQL to the operator. Those mutations need to be safe by
//     construction. No operator string ever reaches the database as
//     part of the SQL. Operator-supplied integers (LIMIT, OFFSET) are
//     validated (Limit must be in [0, Operator.MaxRows], Offset must be
//     in [0, maxOffset]) and then formatted directly into the SQL text
//     — this is safe because Go's int type cannot encode SQL injection,
//     but is worth noting for auditors. The empty-IN-list sentinel
//     (1=0 for IN) is a static SQL fragment, not operator-derived.
//     Empty NOT IN is REJECTED rather than emitting 1=1 — see
//     ErrInvalidPredicate (M3).
//   - The classifier protects the SQL editor's freeform path. This
//     package protects the row-grid + cell-editor path. Both paths
//     run through the same engine but use entirely different shapes.
//   - Identifier handling is centralized here: every schema, table,
//     and column name is validated and engine-aware-quoted before
//     being concatenated into SQL. Values pass as bind parameters.
//
// Security posture:
//
//   - SCHEMA + TABLE + COLUMN names: validated via
//     schema.ValidateIdentifier (the package import), then quoted via
//     the engine-specific quoteIdent helper. Operators can NOT supply
//     an identifier that contains a quote character; the validator
//     rejects it.
//   - VALUES: passed as bind parameters through driver.Conn.Exec /
//     driver.Conn.Query. Never concatenated. Per-value size cap of 1
//     MiB applies to UpdateByPK.Set and InsertOpts.Values entries
//     (Operator.MaxValueBytes; see ErrValueTooLarge).
//   - PREDICATE OPERATORS: drawn from a fixed enum (see Op constants).
//     Operator strings like "; DROP TABLE x" do not match any constant
//     and are rejected at build time.
//   - PK ENFORCEMENT: Update / Delete refuse to run against a table
//     with no primary key. This eliminates the "I meant to update one
//     row but the WHERE was empty" class. Operators who actually need
//     mass DELETE / UPDATE use the SQL editor, where the classifier
//     flags it as write-row-mass + requires step-up. UpdateByPK also
//     refuses to mutate primary-key columns (ErrPKMutation) — callers
//     wanting to rekey a row must DELETE + INSERT under a transaction
//     at the SQL-editor layer.
//   - READ ROW CAP: Read accepts a Limit; the package refuses Limit
//     beyond Config.Query.ResultRowsMax (passed via Operator
//     construction). Hardcoded fallback: 10,000. Read internally asks
//     the backend for LIMIT+1 rows so it can set ReadResult.Capped
//     when the result was truncated — the previous version silently
//     swallowed driver.ErrCapped when Limit == MaxRows.
//   - IN/NOT IN LIST CAP: a single IN/NOT IN list may not exceed
//     maxInListSize (1000) entries. Postgres caps total bind
//     parameters per statement at 65,535; an unbounded IN list could
//     hit that ceiling without warning.
//
// Engine-parity notes:
//
//   - LIKE / ILIKE: Postgres LIKE is case-sensitive; MariaDB LIKE is
//     collation-dependent (case-insensitive under utf8mb4_general_ci,
//     case-sensitive under utf8mb4_bin). For deterministic case
//     semantics, use OpILike — native on Postgres, rewritten to
//     `LOWER(col) LIKE LOWER(?)` on MariaDB. The LOWER() rewrite is
//     correct for ASCII; non-ASCII case folding diverges between
//     engines (e.g. ß / ẞ under utf8mb4_bin).
//
//   - LastInsertID: on MariaDB, Insert uses Exec and reads
//     LAST_INSERT_ID() (works for any AUTO_INCREMENT column). On
//     Postgres, pgx has no LastInsertId() channel — Insert detects a
//     single-column PK via schema.Reader and appends RETURNING <pk>
//     via Query so the result is still surfaced. Multi-column-PK
//     tables on Postgres still return LastInsertID=0.
//
// Ownership:
//
//   - The Operator BORROWS its driver.Conn. It does NOT take ownership
//     and will NOT close the conn on any error path. The caller
//     (typically an HTTP handler) opens the conn, defers Close, then
//     passes the conn into rows.New for the lifetime of the request.
//   - An Operator is safe for a single goroutine; concurrent use is
//     unsupported (mirrors the driver.Conn contract).
package rows
