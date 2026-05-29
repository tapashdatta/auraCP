# Aura DB — Known Issues + Deferred Work

This document tracks issues identified during review that we have NOT
fixed in the current PR, with the rationale for the deferral and the
target PR for the fix. Every entry is honest about the trade-off; we do
NOT pretend something is fixed when it isn't.

This is a living document. When an issue lands a fix, move the entry to
the corresponding ADR or release notes; don't leave dead entries here.

---

## Source: PR #3 adversarial review (workflow run wf_2ae2ea6a-3b3)

The 4-lens review of the driver layer (security / correctness / limits
/ SDK consistency) produced ~50 findings. Critical and high-severity
issues were fixed in PR #3 itself. The rest are tracked below.

### Deferred — PR #3.5 (driver hardening follow-up)

These need real work but aren't critical for v0.3.0 functional shipping.
PR #3.5 lands before v0.3.0 GA.

#### Tunnel: local-listener exposure to other host processes

**Finding (security lens):** the SSH-tunnel's local listener binds to
`127.0.0.1:0` and accepts connections from any local UID. The auracpd
deployment model assumes a dedicated host, but if Aura DB ever runs
alongside untrusted code on the same box, any local process can dial
the tunnel and reach the remote DB through it.

**Why deferred:** the single-host deployment assumption is documented
in SECURITY.md §2.3 and reflected in auraCP's installer (no
multi-tenant story). Fixing this requires either (a) migrating to a
unix socket with 0600 perms or (b) per-connection nonces — both are
substantial work that doesn't pay off until the multi-tenant deployment
story actually exists.

**Fix in PR #3.5:** unix-socket listener at `/run/aura-db/tunnels/
<conn-id>.sock` mode 0600, owned by auracpd. go-sql-driver/mysql
supports unix-network DSN; pgx supports `host=/path/to/socket`. The
SSHTunnel-side machinery doesn't need to change.

#### Tunnel: data-copy idle timeout enforcement

**Finding (limits lens):** PR #3 added a sliding `tunnelIdleTimeout`
(5min) via `idleDeadlineConn`. That's the minimum fix — what's still
missing is configurability per connection (some operators may have
long-running queries that legitimately stall for >5min on a slow
backend). The hard 5min isn't operator-tunable in this PR.

**Fix in PR #3.5:** expose `Connection.QueryIdleTimeout` (defaults to
the engine's `Config.Query.TimeoutMax` or 5min, whichever is shorter).

#### LimitedRows: per-cell streaming size cap

**Finding (limits lens):** a single row with a 1 GiB BLOB / JSONB
column blasts past `MaxBytes` in one swallow because the cap check
fires AFTER the row is decoded. The byte cap is therefore "MaxBytes +
one row" rather than a hard ceiling.

**Why deferred:** properly streaming individual cell decoding requires
diving into pgx's `RawValues()` + custom binary decode (per-type) and
go-sql-driver's `sql.RawBytes` path. Significant work; the workaround
in the meantime is the `Config.Query.ResultBytesMax` hard ceiling +
operator-set query timeouts.

**Fix in PR #3.5:** per-cell `MaxBytesPerCell` cap; if a row exceeds
it, return `ErrCapped` and DROP the row (don't return it). For
fundamentally large columns, point operators at COPY / dump tools.

#### Conn.Query: no classifier interlock at the driver level

**Finding (security lens):** `Conn.Query` accepts arbitrary SQL with no
classifier hook. Any future caller of `Pool.Withdraw().Query(...)`
bypasses the security model.

**Why deferred:** the engine layer (PR #8) is supposed to be the ONLY
caller of `Conn.Query` and runs the classifier. The driver layer
intentionally has zero awareness of the classifier (separation of
concerns). The risk is that a future PR adds a *second* caller of
`Conn.Query` and forgets to classify.

**Fix:** CI grep rule rejecting any new call to `Conn.Query` /
`Conn.Exec` outside `internal/api/dbadmin.go`. Tracked for PR #8.

#### MySQL credential lifetime in memory

**Finding (security lens):** `cfg.Passwd` is a Go string that
go-sql-driver retains for the life of the `*sql.DB`. `Credentials.Zero()`
zeros the engine's reference but not the driver's copy.

**Why deferred:** the proper fix uses `mysql.NewConnector(cfg) +
sql.OpenDB(connector)` so we keep a reference to `cfg` and can blank
`cfg.Passwd` after the connector is built. The current code uses the
DSN-based `sql.Open` path which is simpler but holds a string copy.

**Fix in PR #3.5:** migrate to `NewConnector` + null-out cfg.Passwd
after construction.

### Deferred — PR #4 (schema reader)

These overlap with schema-reader work:

#### Postgres pgtype.Numeric / Interval / Range type preservation

**Finding (correctness lens):** `postgresRows.Next` normalizes only
`time.Time` and `[16]byte` (UUID). pgx's `pgtype.Numeric`,
`pgtype.Interval`, `pgtype.Range`, etc. fall through unchanged and
JSON-marshal poorly.

**Why deferred:** the schema reader (PR #4) knows the column type
authoritatively; richer per-column conversion belongs there. PR #3's
current behavior is "acceptable for v0.3.0 alpha"; PR #4 will fill in
proper rendering.

#### ColumnInfo.PrimaryKey never populated

**Finding (correctness lens):** the driver layer leaves `PrimaryKey`
at the zero value. The frontend's edit-cell logic depends on this
being correct.

**Why deferred:** authoritative PK detection requires
`information_schema` / `pg_catalog` queries (the schema reader).
PR #4 fills it in via a separate path.

### Deferred — PR #8 (HTTP handler)

#### Backend error message redaction

**Finding (security lens):** `classifyMySQLErr` / `classifyPostgresErr`
embed verbatim backend messages in `ErrSyntax` (operator needs them to
fix the query). For `ErrAuth` and `ErrPermission`, PR #3 fixed the
verbatim leak via `errors.Join` — but the engine layer must still
choose what to surface to the browser vs the audit log.

**Why deferred:** the HTTP handler (PR #8) decides response shape per
SECURITY.md §10.3 — short fixed-form messages to the browser, full
fidelity to the audit log via `errors.Unwrap`. PR #3 plumbed the chain;
PR #8 surfaces the right ends to the right consumers.

### Deferred — PR #3.5 (lower priority polish)

These are quality-of-implementation issues with no security or
correctness impact but are worth fixing:

- **`registerMySQLTLS` registry leak:** TLS configs accumulate per
  unique (host, port, sslmode, cert-hash) tuple over process lifetime.
  PR #3 added per-credential hashing so they don't *collide*, but each
  is still leaked. Fix: track registered names per `*mysqlConn` and
  `DeregisterTLSConfig` on Close.
- **MySQL engine-identity verification post-connect:** after Ping
  succeeds, run `SELECT VERSION()` and assert it contains "MariaDB" or
  "MySQL" — catches the rare misconfiguration where a Connection
  labeled MariaDB points at a different engine on the same port.
- **`postgresConn.Close` error tracking parity with `mysqlConn`:**
  pgxpool.Close has no return today, but mirror `firstErr` pattern
  for forward-compatibility.
- **idleSweeper interval floor:** the hard 30s minimum can be the
  same length as `IdleTimeout` for very aggressive eviction operators.
  Drop the floor to 1s; the sweep is cheap.

---

## Source: PR #4 adversarial review (workflow run wf_5d1d6f67-0f8)

The 4-lens review of the schema reader (`pkg/dbadmin/schema/`) produced
70 findings (17 high, 29 medium, 17 low, 7 nit). The 10 must-fix items
landed in PR #4 itself. Everything below is deferred.

### Deferred — high-severity, PR #4.5 (schema reader follow-up)

These need real work but the must-fix set covers the immediate
correctness / security blockers for v0.3.0 alpha.

#### H6 — singleflight: in-flight load coalescing

**Finding (limits lens):** when N concurrent callers request the same
uncached key, the cache fires N parallel loads against the backend.
For a slow GetTable that fans out to columns / indexes / FKs /
triggers, that's a thundering herd against information_schema.

**Fix in PR #4.5:** wrap `cacheFetch` in `golang.org/x/sync/singleflight`
so the first caller does the load and the rest wait for the result.
The generation-counter race protection (H7) interacts with this — the
singleflight slot must capture the generation at slot-acquisition time.

#### H9 — Config.Query.Timeout plumbing into schema reads

**Finding (SDK lens):** schema reads currently use this package's own
`defaultLimits()` (30s / 50K-rows / 50MB). The engine's
`Config.Query.Timeout` is documented as the canonical operator-tunable
read budget but does NOT apply to schema reads in PR #4.

**Fix in PR #4.5:** thread the engine `Config` into `For(...)` (or a
new `ForWithLimits`) so operators can shrink the schema-read budget
without monkeying with this package's defaults.

#### H12 — ErrCapped partial-result handling

**Finding (limits lens):** if `Conn.Query` returns `ErrCapped` mid-loop
(row or byte cap tripped), the schema reader currently returns the
partial slice silently. A 5-column table whose index list got truncated
to 4 entries is worse than a clean error.

**Fix in PR #4.5:** on `ErrCapped`, propagate the error AND attach the
partial slice via a typed `CappedError{Got: partial}` so callers can
choose to display "partial result, increase limits".

#### H14 — ViewSummary parity (Postgres `is_updatable`)

**Finding (correctness lens):** Postgres `ViewSummary.Updatable` is
hard-wired to `false`. MySQL reads `information_schema.views.is_updatable`.

**Fix in PR #4.5:** Postgres views are updatable iff they meet
PostgreSQL's "simple view" rules. Query
`information_schema.views.is_updatable` (Postgres has the same view)
and surface it.

### Deferred — medium-severity, PR #4.5 polish

These have real impact but are quality / performance issues, not
correctness blockers:

- **Cross-user cache poisoning:** the cache is keyed by schema/table
  name only, not by the connecting role. Two operators with different
  visibility into the same schema see the same cached `GetTable`
  result. Fix: include a per-conn cache-bucket discriminator (or move
  the cache from per-engine to per-connection).
- **Postgres index expression columns dropped:** the column-trim loop
  in `fillIndexes` silently drops expression-index entries (indkey 0).
  Fix: surface them as `(expr)` placeholders via `pg_get_indexdef`.
- **MySQL system-schema case-bypass:** the `ListDatabases` filter
  excludes lowercase `mysql`, `sys`, etc., but case-insensitive
  collations let `MYSQL` slip through. Fix: lower-case the filter on
  both sides.
- **N+1 GetTable batch:** the engine fans out per-table `GetTable`
  calls for the table-tree view. Fix: a batch path that does
  `WHERE table_schema = ?` once and groups in Go.
- **Slow `information_schema.statistics`:** on busy MySQL servers
  this view is a known hot spot. Fix: pre-filter by `table_name` IN
  (...) when the caller knows the tables of interest.
- **`ListFunctions` pulls full bodies:** `pg_get_function_arguments`
  + full result type for hundreds of functions adds up. Fix: split
  into list (cheap) + GetFunction (expensive, on-demand).
- **`evictLRULocked` O(n):** linear scan + linear filter. With
  `MaxEntries=1000` this is fine, but a true LRU (doubly-linked list)
  would be O(1).
- **`MaxEntries` byte-based:** today MaxEntries counts cache keys,
  not bytes. A single 50 MB GetTable counts as "1" even though it
  dominates the byte footprint. Fix: track approximate per-entry
  size and cap by both count AND bytes.
- **Trigger fetch error swallowing:** `fillTriggers` discards the
  error to "best-effort" past privilege issues; a transport error
  gets the same treatment. Fix: surface non-permission errors,
  swallow only `ErrPermission`.

---

## Source: PR #5 adversarial review (workflow run wf_6e1fdb99-665)

The 4-lens review of the row operations layer (`pkg/dbadmin/rows/`)
produced 41 findings (0 critical, 7 high, 13 medium, 16 low, 5 nit).
Seven items landed as must-fix in PR #5 itself; the rest are deferred
to PR #5.5 (engine-parity & limits hardening) and tracked below.

### Deferred high findings — PR #5.5

- **H1** — Read swallows ErrCapped when Limit == MaxRows. Workaround:
  use Limit < MaxRows. **Fix in PR #5.5:** request LIMIT+1 from the
  backend so cap fires only on true overflow, OR treat ErrCapped as a
  clean stop with a CappedResult{Rows, Capped: true}.
- **H2** — Count skips identifier validation that Read enforces.
  **Fix in PR #5.5:** introduce dedicated CountOpts{Schema, Table,
  Filter} so unused ReadOpts fields cannot be passed.
- **H5** — Unbounded IN/NOT IN list size. Postgres caps at 65535 bind
  params. **Fix in PR #5.5:** add maxInListSize ~1000 in flattenInValue.
- **H6** — Postgres Insert returns LastInsertID=0 (no RETURNING).
  **Fix in PR #5.5:** rewrite buildInsert for Postgres to add
  RETURNING <pk> and route via Query.
- **H8** — OpLike case-sensitivity diverges across engines without doc
  warning. **Fix in PR #5.5:** document on Op constants + doc.go.

### Deferred medium findings — PR #5.5

- **M1** — Schema-cache staleness on empty Columns + post-ALTER scenarios.
- **M3** — Empty NOT IN emits 1=1 (matches everything); silently turns
  blocklist filters into full exposure.
- **M4** — flattenInValue accepts NaN/Inf in []float64.
- **M5** — flattenInValue missing []int32/[]uint64/[]time.Time/[]bool cases.
- **M6** — assertPKMatch doesn't reject nil PK values.
- **M7** — UpdateByPK allows mutating PK columns.
- **M8** — Read doesn't assert column-count alignment with the schema reader.
- **M9** — Count is unbounded; no estimate fallback, no cap, no TTL cache.
- **M10** — No per-value size cap on Set/Values.
- **M11** — Operator borrows driver.Conn with no ownership doc.
- **M14** — Predicate.Value any doesn't enumerate accepted Go types
  for JSON deserialization.
- **M15** — Integration-test cleanup uses test's own ctx; cleanup may
  run against poisoned context.

### Deferred low + nit findings — PR #5.5

- **L1** — opSQL lookup map.
- **L2** — flattenInValue nested-slice rejection.
- **L3** — OpILike MySQL collation rewrite warning.
- **L5** — integration-test scratch-table guardrails.
- **L6** — LIMIT/OFFSET bound-parameter alternative.
- **L7** — New() negative-Limits rejection.
- **L8** — quoteIdent unknown-engine default.
- **L9** — Predicate.Value ignored for IsNull.
- **L10** — empty IN error vs silent 1=0.
- **L11** — deep OFFSET cap.
- **L12** — Columns=nil vs []string{} doc.
- **L13** — cross-package ErrInvalidIdentifier alias.
- **L14** — composite-PK WHERE-order regression test.
- **N1** — Op typed-string compile guard.

---

## Source: PR #6 adversarial review (workflow run wf_57a37769-701)

The 4-lens review of the EXPLAIN normalization layer
(`pkg/dbadmin/explain/`) produced 40 findings (1 critical, 12 high,
17 medium, 9 low, 2 nit). Seven must-fix items landed in PR #6
itself (C1 structural ClassRead gate on Analyze, H1 post-fetch
byte cap, H2 Sscanf→strconv+NaN/Inf sanitization, H3 depth +
node-count caps, H4 RowsActual overflow clamp, H8 lowerCamelCase
JSON tags + shape test, H10 truthful Plan.Warnings docstring).
Everything below is deferred to PR #6.5.

### Deferred high findings — PR #6.5

- **H5** — MariaDB rollup semantics: `mergeMetrics` sums
  `RowsExpected` additively, but join cardinality is multiplicative
  (a Nested Loop with 100 outer × 10 inner produces 1000, not 110).
  **Fix in PR #6.5:** model join cardinality on the JOIN node itself
  using outer.RowsExpected × inner.RowsExpected for Nested Loop.
- **H6** — Missing Postgres per-node metadata: Sort Key, Group Key,
  Hash Keys, Output, Subplan Name, Workers Planned/Launched, Parallel
  Aware, JIT, Triggers, Settings are decoded by neither `pgPlan` nor
  surfaced on `Node`. Operators inspecting parallel plans see less
  than `EXPLAIN ANALYZE` console output. **Fix in PR #6.5:** add the
  fields to `pgPlan` + extend `Node` with a typed `Extras` map.
- **H7** — MariaDB shape coverage gaps: windowing, having_subqueries,
  select_list_subqueries, "Impossible WHERE", and a coexisting
  subquery+table shape are silently mapped to `Kind: "Unknown"`
  without emitting a warning. **Fix in PR #6.5:** handle each shape
  explicitly and append an "MariaDB block shape not recognized:
  <keys>" warning for the residual unknowns.
- **H9** — `Plan.Total` semantics diverge per engine: MariaDB only
  fills `CostTotal` (no row/time/buffer rollup); Postgres mirrors
  `Root.Metrics`. doc.go says "Mirrors Root.Metrics", which is true
  only for Postgres. **Fix in PR #6.5:** roll up MariaDB metrics to
  match, or document the divergence per-engine.
- **H11** — Engine-parity field availability matrix missing from
  doc.go. The README-style table that says "Postgres fills Buffers*,
  RowsActual, TimeStartMS; MariaDB fills CostTotal, RowsExpected only"
  is essential for callers. **Fix in PR #6.5:** add the matrix.

### Deferred medium findings — PR #6.5

- **M1** — Brittle EXPLAIN wrap: string prepend with no
  multi-statement / leading-comment check. `--; DROP TABLE x;` slips
  through the wrap.
- **M2** — Postgres JIT / Triggers / Settings fields dropped during
  decode (overlaps H6).
- **M3** — `PlanningTimeMS=0` is ambiguous: it means both "not
  measured" and "sub-microsecond". Add an explicit `PlanningTimed
  bool` or document the convention.
- **M4** — `asFloat64("1K")` returns 0 (silent partial-parse). MariaDB
  emits "1K" / "10M" for `data_read_per_join`; we drop the value.
- **M5** — `parseMySQLTable` overwrites `RowsExpected` with
  `RowsProducedPJ` when the latter is > 0, but the former is the
  examined-per-scan count which is sometimes more useful.
- **M6** — MariaDB `warnings[].Code` and `warnings[].Level` are
  discarded; only `Message` is kept. Operators triaging warnings need
  the code.
- **M7** — `defaultExplainTimeout=60s` is hardcoded; not plumbed from
  `Config.Query.TimeoutMax`. Operators with shorter budgets get an
  effective 60s on EXPLAIN paths.
- **M8** — `fmt.Sscanf` perf: post-H2 strconv migration covers most
  paths, but any remaining Sscanf call should also move (the H2 fix
  covers all known call sites).
- **M9** — `Plan.Raw` always retained; no `OmitRaw` option to drop
  the bytes when the response body is constrained.
- **M10** — Double-counting in `mergeMetrics` via wrapper nesting:
  an Ordering wrapper passes child metrics up AND the parent's own
  metrics include the same children's contribution.
- **M11** — `Normalizer` interface exported but no public
  implementation slot; reads as forward-compat but inviting
  third-party extensions we don't intend to support.
- **M12** — `Plan.Raw` shape is engine-specific (Postgres = JSON
  array, MariaDB = JSON object) but undocumented; the frontend's
  "raw tab" needs to know.
- **M13** — Engine string literals `"mariadb"` / `"postgres"`
  duplicated across mysql.go + postgres.go + tests; should be const.
- **M14** — Postgres EXPLAIN options are hardcoded to `BUFFERS,
  FORMAT JSON` (+ ANALYZE); no plumbing for SETTINGS / VERBOSE / WAL.
- **M15** — `Node.Filter` collapses five Postgres conditions (Filter,
  Index Cond, Hash Cond, Merge Cond, Recheck Cond) via `firstNonEmpty`;
  the lost ones (e.g., Bitmap Heap Scan's Recheck Cond when Filter is
  also present) are silently dropped.

### Deferred low + nit findings — PR #6.5

- **L1** — `asInt64(float64)` truncates (1.9 → 1); should round.
- **L2** — Shared Dirtied Blocks decoded but discarded;
  Local/Temp/IO timing fields absent entirely.
- **L3** — `firstNonEmpty` drops Bitmap Heap Scan's Recheck Cond
  (overlaps M15).
- **L4** — `mysqlAccessKind` misses `index_merge` / `index_subquery`
  / `unique_subquery`; they fall through to "Table Scan (<access>)".
- **L5** — Nested unions silently skipped (no recursion for
  union-within-union).
- **L6** — `parseMySQLNestedLoop` entries without a `table` key are
  dropped silently (e.g., when a `block-nl-join` operator appears).
- **L7** — Wrapper `cost_info` is overwritten by child metrics in
  `parseMySQLBlock` instead of merged.
- **L8** — `readSingleJSONRow` doesn't assert that a second `Next()`
  returns EOF; a malformed driver returning two rows passes silently.
- **L9** — "Unknown" fallback in `parseMySQLBlock` has no warning
  collector (overlaps H7).
- **N1** — `Plan` struct lacks an explicit additive-stability
  statement (forward-compat note for future field additions).
- **N2** — `Metrics.CostStart` is documented as "always 0 on MariaDB"
  but the field is never actively zeroed — operators relying on the
  doc could see junk if a future MariaDB version starts populating it.

---

## Source: PR #7 adversarial review (workflow run wf_0543ab7a-d75)

The 4-lens review of the query-history layer (`pkg/dbadmin/history/`)
produced 37 findings (0 critical, 11 high, 13 medium, 10 low, 3 nit).
Nine were promoted to MUST-FIX and landed in PR #7 itself: LIKE
ESCAPE clauses, fenced + comma-rejecting tag storage, redacting
`Entry.Error`, per-Entry dialect for redaction, default-deny on empty
`UserID`, `Search` honoring `opts.Tag`, JSON wire-format camelCase,
unexporting `SQLiteStore` (Open now returns `Store`), and deleting
dead `errors.Is` import-keeper noise. The rest are deferred below.

### Deferred high findings — PR #7.5

#### H4 — RedactSensitiveInline misses non-standard credential forms

`classifier.RedactSensitiveInline` only covers `CREATE/ALTER USER …
IDENTIFIED BY '<pw>'` and `CREATE/ALTER ROLE … WITH PASSWORD '<pw>'`.
It does not redact:

- MariaDB `IDENTIFIED VIA <plugin> AS '<hash>'`
- Postgres `CREATE SUBSCRIPTION … CONNECTION 'postgresql://u:p@…'`
- `dblink_connect('host=… user=… password=…')`
- `postgresql://`, `mysql://`, `mongodb://` URIs in any DDL
- `COPY FROM PROGRAM 'curl -u user:pw https://…'`

Documented in `pkg/dbadmin/history/doc.go` so operators aren't
surprised; the fix is a classifier upgrade in PR #7.5.

#### H8 — LIKE fallback Search is silent O(n) scan

When the SQLite build lacks FTS5, Search degrades to LIKE without
telling the caller. At 10⁵ entries the LIKE branch is full-table
scan; at 10⁶ it stalls the UI. Fix in PR #7.5:

- `OpenOpts{RequireFTS5 bool}` that errors at Open time if FTS5
  isn't available.
- `Store.HasFTS() bool` so callers can warn in the UI when the
  search is running degraded.

#### H9 — No retention enforcement; storage grows unbounded

The package exposes `DeleteOlderThan` but the engine layer doesn't
call it on a schedule yet. A 90-day-old install can sit on millions
of rows. Fix in PR #7.5:

- `MaxRows` ceiling enforced at Append time (oldest evicted).
- `StartRetentionLoop(ctx, period, cutoff)` helper that the engine
  wires into the panel's periodic-task scheduler.
- Chunked `DeleteOlderThan` (1000-row batches) so a 365-day-overdue
  sweep doesn't lock the DB for a multi-second window.

### Deferred medium findings — PR #7.5

- Negative `opts.Offset` in `Search` not validated (`List` validates
  but Search doesn't).
- `:memory:` detection is string-equality only —
  `file::memory:?cache=shared` falls into the WAL branch and
  produces a malformed DSN.
- FTS5 quote-wrap doesn't cap input length or strip control bytes.
- `bm25` raw score on short SQL fragments is degenerate; no
  deterministic tiebreaker beyond `executed DESC`.
- `MaxOpenConns=4` + 5s `busy_timeout` can stall the panel UI for
  the full 5s under contention.
- FTS5 storage overhead (~1.8× the entries table) is undocumented
  and there's no opt-out.
- `ListOpts.IncludeClass` is a workaround for the zero-value
  `Class` problem; switch to `Class *classifier.QueryClass`.
- The `tags` column should be normalized to a separate `entry_tags`
  table with `PRIMARY KEY(tag, entry_id)` to fix the unindexed
  full-scan on Tag filter at scale.
- bm25 weights + deterministic tiebreaker (currently `bm25 ASC,
  executed DESC` — operators may expect explicit weighting).
- Write semaphore to bound concurrent SQLite writers.
- Prepared-statement cache for `Append` (current per-call `?`
  binding doesn't reuse a `*sql.Stmt`).
- Partial index for admin `OnlyStarred` listings (current index
  only covers `(user_id, starred, executed DESC) WHERE starred=1`,
  which isn't usable when admin views run without a user filter).
- `initSchema` FTS block swallows trigger-creation errors alongside
  missing-FTS5 errors — should split the probe from the trigger
  install so the latter surfaces.

### Deferred low + nit findings — PR #7.5

- Concurrency TOCTOU between `closed.Load()` and `db.ExecContext`
  in every op (acceptable today; the second call returns
  `sql.ErrConnDone`, but cleaner to lock-and-check).
- `MaxSQLLength` truncates at byte boundary; can split a UTF-8 rune.
- `Append`'s `IsZero()` guard doesn't catch `time.Unix(0,0)` or
  pre-1970 timestamps (caller-supplied junk passes through).
- Partial starred index `(user_id, starred, executed) WHERE
  starred=1` unusable for admin-mode listings that scan all users.
- Append doesn't use a held `*sql.Stmt` — per-conn cache is cold
  under bursty load.
- `MaxSQLLength=256KiB` silently truncates 50-statement migrations
  pasted whole.
- `doc.go` Concurrency section doesn't mention that `:memory:`
  databases pin to a single connection.
- `DeleteOlderThan` returns only the count; an `IDs callback` for
  audit parity with the panel's existing delete flows is a nit.
- Error sentinel naming style consistency between `ErrNotFound`,
  `ErrInvalidInput`, `ErrClosed` — fine as-is; nit (no-op).

---

## Open issues — not yet scheduled

### LimitedRows concurrent-Next semantics

PR #3 added an atomic-guard that returns `ErrConcurrentNext` when two
callers race. The alternative (block-and-serialize) would be quieter
but hides real bugs in caller code. Current behavior surfaces the
violation. Revisit if real operators report issues.

### Postgres `require` sslmode meaning

PR #3 treats `require` as "encrypt-only, no verify" (matching libpq's
documented behavior). Some operators may expect `require` to imply
some level of validation; this is documented in driver code.

---

## How to use this document

When opening a PR that closes one of these items:
1. Remove the entry from this doc.
2. Reference the original issue in your PR's commit message.
3. If the fix changes the public surface, update SDK.md.
4. If the fix changes the security posture, update SECURITY.md.

When adding a new known issue:
1. Append under the right "Deferred — PR #X" section.
2. State the finding + why deferred + target PR.
3. If the issue surfaces in operator-visible behavior, link to a
   reproducer or describe the symptom.

This document is canonical for "what we know is broken but accept for
now." Every entry must have a clear path to resolution.
