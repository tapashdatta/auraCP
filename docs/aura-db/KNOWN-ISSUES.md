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

## Source: PR #8 adversarial review (workflow run wf_d3fe5294-f67)

The 4-lens review of the HTTP wire surface (`pkg/dbadmin/httpapi/`,
23 files, ~3,930 LOC) produced 52 raw findings (1 critical, 15 high,
20 medium, 13 low, 2 nit). After dedupe + triage: 7 must-fix items
were promoted and landed in PR #8 itself — WebSocket CSWSH defense
(real same-origin check + CSRF handshake gate), audit emission on
every authn/CSRF/rate-limit denial, WS audit emission on every deny
branch, WS error frames routed through `mapErr()` to strip driver
detail, `request_id` snake_case in the error envelope, kebab-case
error code constants matching `pkg/dbadmin/errors.go`, and `errors.Is`
for driver sentinels in `handleQuery`. The remaining 39 findings are
deferred below.

### Deferred high findings — PR #8.5

#### DEF-1 — Step-up `/verify` shares the generic mutating rate-limit bucket

The step-up verification endpoint is bucketed against the same
10 req/s burst-20 limiter as every other mutating route. SECURITY.md
§4.4 specifies a stricter step-up rate-limit (10 per 15 minutes per
user with progressive lockout); the current limiter doesn't model
windows that long. Fix in PR #8.5: dedicated `rateClassStepUp` with
a sliding 15-minute counter + per-IP secondary lock.

#### DEF-2 — `handleQuery` / `handleExplain` call `Conns().Get` before `authorize`

The lookup happens before the authorization check, so a 404 for an
existing connection the user doesn't have access to differs in
timing from a 404 for a non-existent connection. Enables connection
enumeration via timing side-channel. Fix in PR #8.5: gate
`Conns().Get` behind `HasPermission` (or return the same 404 with
constant-time padding).

#### DEF-22 — WS stream ignores operator-configured Limits

`handleSQLStream` uses hardcoded 30-min timeout, 10M-row cap, 1 GiB
byte cap regardless of `Config.Query.TimeoutMax / ResultRowsMax /
ResultBytesMax`. Per SECURITY.md §14 operators must be able to tune
these. Fix in PR #8.5: route through `Config()` like the REST
handlers.

#### DEF-23 — WS write loop ignores `writeFrame` errors

`flush()` and the row pump call `_ = writeFrame(...)` and discard
the error. A slow client that stops reading causes the server-side
`writeFrame` to block until `wsWriteWait` elapses, then the next
iteration blocks again — the loop never exits even though the
client is gone. Fix in PR #8.5: propagate writeFrame errors, break
the row pump on EOF / closed-pipe.

#### DEF-24 — WS handler never sends pings; long queries die at the 60s read deadline

`SetPongHandler` resets the read deadline on inbound pongs, but
nothing initiates pings from the server. A query that takes longer
than `wsPongWait` (60s) without producing rows triggers the read
deadline and tears down the connection mid-stream. Fix in PR #8.5:
ticker goroutine that sends ping frames every 30s for the lifetime
of the stream.

### Deferred medium findings — PR #8.5

- **DEF-3** — `recoverer` audit `Record` echoes raw `panic` value;
  possible credential/SQL residue in audit log. Fix: format with
  `%T` only, log full panic + stack server-side.
- **DEF-4** — `handleRevealPassword` returns plaintext in JSON body
  rather than the signed one-time URL pattern SDK §7.3 mandates.
  Fix: mint a short-TTL signed URL and return that.
- **DEF-5** — `creds.Zero()` is a no-op for the captured local `pw`
  string due to Go string interning. Fix: hold as `[]byte` from
  retrieval through emission.
- **DEF-6** — Post-upgrade per-message CSRF / handshake token not
  implemented (the subprotocol-token claim added in PR #8 only
  validates the initial handshake). Fix: revalidate token on every
  inbound open frame.
- **DEF-12** — Audit emission happens AFTER response is written;
  a crash between Write and Record loses the audit. Fix: record
  THEN write.
- **DEF-13** — `/sql/stream` has no rate limit AND no per-user
  concurrent-stream cap. Fix: bucket WS upgrades + cap N concurrent.
- **DEF-14** — `SetWriteDeadline` called outside `mu` in
  `writeWSError` vs `writeFrame`. Fix: take the lock or use a
  channel-serialized writer.
- **DEF-15** — `/history` pagination accepts negative `limit`/`offset`
  silently. Fix: validate at decode time.
- **DEF-16** — `handlePatchHistory` applies Star and Tag as two
  non-atomic store calls. Fix: a single Patch op on the store.
- **DEF-25** — `AuditSink.Record` is called synchronously inline;
  a slow sink stalls every request. Fix: bounded async queue with
  drop-policy.
- **DEF-26** — Saved-queries store is unbounded per (user, conn).
  Fix: cap at 256 per user, evict LRU on overflow.
- **DEF-27** — REST `/query` materializes the entire result in
  memory before writing. Fix: stream JSON array via encoder.
- **DEF-28** — Filter / sort / columns slices from URL query are
  unbounded. Fix: cap at 32 each.
- **DEF-29** — `/history` `limit`/`offset` have no upper cap;
  `SearchHistory` `q` length uncapped. Fix: clamp to MaxListLimit.
- **DEF-30** — Export stub accepts arbitrary JSON without exporter
  machinery caps. Fix: real exporter (PR #16) gates this.
- **DEF-33** — SDK §7 documents WS `/connections/{id}/slow-log/stream`
  which is missing; PR #8 ships an undocumented `/sql/stream`. Fix:
  reconcile SDK.md to match implementation, or rename the route.
- **DEF-34** — Five routes ship without SDK §7 entries: rows insert,
  history search/patch/delete, saved-queries delete. Fix: update SDK.
- **DEF-35** — `httpapi` exports `New`, `Options`, and 33 `Code*`
  constants; the constants leak the implementation as public API.
  Fix: lowercase the constants OR document the wire form in SDK and
  remove the Go re-exports.
- **DEF-36** — Test coverage for §7 routes is partial (~25% by
  the synthesis count). Fix: round out per-route happy-path +
  error-envelope assertions.

### Deferred low / nit findings — PR #8.5

- **DEF-7** — Rate limiter never evicts buckets — unbounded memory
  growth keyed by user ID. Fix: LRU eviction at 10K entries.
- **DEF-8** — Reclassify guard in `handleQuery` is byte-identical
  reclassify; the SHA-256 computed is discarded. Fix: drop the
  compute, or use the hash for audit correlation.
- **DEF-9** — `/import` endpoint authorizes but returns 200 with
  no work done. Fix: implement or return `not-implemented`.
- **DEF-10** — Export endpoint returns stub `SignedURL` pointing
  at an unregistered route. Fix: implement in PR #16.
- **DEF-11** — Catch-all 404 handler runs `authn` but emits no
  audit. Fix: emit denial event on every 404 that ran auth.
- **DEF-17** — Connection-creation validation doesn't name the
  missing field. Fix: include field name in error message.
- **DEF-18** — `parseFilter` doesn't JSON-decode the value as the
  comment promises. Fix: decode or update the comment.
- **DEF-19** — WS query/exec failure emits error frame but no
  Close control frame. Fix: WriteControl after writeWSError.
- **DEF-20** — `writeError` after successful `WriteHeader` causes
  a "superfluous WriteHeader" log line. Fix: guard with a
  written-flag on the response writer.
- **DEF-21** — Rate-limit key namespace can collide if user IDs
  begin with `r:` or `w:`. Fix: namespace separator that user IDs
  can't contain.
- **DEF-31** — `/import` uses `ParseMultipartForm` without
  `RemoveAll` — tmp file accrual on error paths. Fix: deferred
  cleanup.
- **DEF-32** — No per-user concurrent-query cap; burst 100 reads
  vs `PoolSizePerConn=4`. Fix: per-user semaphore.
- **DEF-37** — WS frame schema not specified in SDK.md but ships
  on a stable URL. Fix: document the frame shapes.
- **DEF-38** — `connectionInput` accepts TLS certs + `PoolSize`
  fields that are silently discarded. Fix: error on unknown.
- **DEF-39** — `connectionDTO` omits SDK fields `owner` and
  `origin`. Fix: add to the DTO.

---

## Source: PR #9 adversarial review (workflow run wf_feeb557d-b15)

The 4-lens review of the standalone runtime (`pkg/dbadmin/standalone/`)
produced 50 findings (0 critical, 6 high, 7 medium, ~37 low/nit).
Thirteen must-fix items landed in PR #9 itself (SEC-01 case-rotation
lockout bypass, SEC-02 TOTP replay, SEC-03 MFA password oracle, SEC-04
AEAD missing AAD, SEC-07 XFF not consulted, C2 Save+Grant non-atomic,
C3 Grant error masking, C4 forwarder ctx leak, C11 canonical-marshal
fragility, audit-forwarders-unwired, connstore-get-no-tenant-filter,
stepup-no-session-rebinding). The remaining 37 findings are deferred
below.

### Deferred high findings — PR #9.5

- **OPS-01** — First-run bootstrap is broken: `kek-rotate --generate`
  requires an existing KEK file. **Reason:** operational/ergonomic gap,
  not a security boundary or interface contract violation; single lens.
  **Target:** PR #9.5.

### Deferred medium findings — PR #9.5

- **SEC-05** — KEK file mode check is fstat-the-path BEFORE open
  (TOCTOU); also accepts mode==0. **Reason:** defense-in-depth gap,
  exploitable only by an attacker who already has write access to the
  KEK file's parent dir (root-owned); single lens. **Target:** PR #9.5.
- **SEC-06** — Webhook forwarder does not enforce HTTPS and does not
  require an HMAC secret. **Reason:** single lens, partially mitigated
  by audit-forwarders-unwired must-fix (forwarder is currently inert);
  fix alongside wiring. **Target:** PR #9.5.
- **SEC-08** — KEK rotation has a window where on-disk key file does
  not match in-DB ciphertexts. **Reason:** operational concern with
  manual-recovery path; single lens. **Target:** PR #9.5.
- **SEC-09** — HIBPClient does not enforce HTTPS and CLI tooling fails
  open on network errors. **Reason:** opt-in feature; single lens.
  **Target:** PR #9.5.
- **SEC-10** — `PHCWithFakeWorkload` uses current policy params; older
  stored hashes leak via timing. **Reason:** single lens; real-world
  impact bounded by infrequent policy rotation. **Target:** PR #9.5.
- **C1** — Session expiry uses strict `>` not `>=`: off-by-one at
  boundary. **Reason:** single tick correctness gap, not exploitable
  in practice. **Target:** PR #9.5.
- **C5** — Login leaks unused `now` variable; TOTP and session use
  separate clock reads. **Reason:** mostly cosmetic/consistency, no
  exploitability. **Target:** PR #9.5.
- **C7** — Tail-file follow mode uses `time.Sleep(500ms)` and ignores
  process-level signals. **Reason:** operator-experience for tail
  subcommand; not a boundary. **Target:** PR #9.5.
- **OPS-02** — `user-create` + `user-passwd` advertise `--grant` and
  `--role` flags that are silent no-ops. **Reason:** operator-ergonomics;
  docs/code drift; not a security boundary. **Target:** PR #9.5.
- **OPS-03** — KEK file ownership/uid is never checked. **Reason:**
  defense-in-depth on a doc-stated invariant; single lens. **Target:**
  PR #9.5.
- **OPS-04** — No `/healthz` or `/readyz` endpoint. **Reason:**
  operator-tooling gap surfaced by the panel-integration story; not a
  security/interface boundary. **Target:** PR #10 (panel-integrated).
- **OPS-05** — Logging defaults to `text` format. **Reason:**
  default-tuning preference. **Target:** PR #9.5.
- **OPS-06** — TLS `min_version` + logging fields are not validated at
  config load. **Reason:** validation hygiene. **Target:** PR #9.5.
- **OPS-07** — KEK rotation procedure lacks verification / retention /
  destruction guidance. **Reason:** documentation completeness.
  **Target:** PR #9.5.
- **OPS-08** — No documentation or tooling for online SQLite backup.
  **Reason:** missing tooling/docs; not a security boundary. **Target:**
  PR #9.5.
- **OPS-09** — No sample systemd unit file shipped. **Reason:**
  packaging gap. **Target:** PR #9.5.
- **OPS-10** — PID file defaults to `/var/run` and falls back to `$HOME`
  silently. **Reason:** operator-ergonomics; modernize path defaults.
  **Target:** PR #9.5.
- **OPS-11** — `audit verify` output is human-only; no `--json` mode.
  **Reason:** tooling ergonomics for monitoring integration. **Target:**
  PR #9.5.

### Deferred low + nit findings — PR #9.5

- **SEC-11** — Recovery-code consume runs Argon2id against every unused
  code per attempt. **Reason:** bounded amplification; requires
  authenticated session. **Target:** PR #9.5.
- **SEC-12** — Audit log Reopen blanks prior recovered hash on a fresh
  post-rotate file. **Reason:** no security loss; operational
  verification ergonomics. **Target:** PR #9.5.
- **SEC-13** — ULID monotonicity loses entropy on rollover and is not
  safe on clock-step-backward. **Reason:** collision plausibility
  astronomically low. **Target:** PR #9.5.
- **SEC-14** — Session token compare on lookup uses bytewise SQL
  equality. **Reason:** leak is on SHA-256 hash not the token; practical
  risk negligible. **Target:** PR #9.5.
- **SEC-15** — Audit-log 0640 mode check does not match docs' 0600
  expectation. **Reason:** documentation alignment. **Target:** PR #9.5.
- **C6** — Audit `Reopen` does NOT verify the new file's mode before
  reopening. **Reason:** defense-in-depth gap on logrotate misconfig.
  **Target:** PR #9.5.
- **C8** — `Engine.Shutdown` + `srv.Shutdown` share the same 30s
  deadline; ordering backwards. **Reason:** graceful-shutdown ordering;
  no security loss. **Target:** PR #9.5.
- **C9** — `consumeRecoveryCode` race produces misleading UX under
  concurrent recovery. **Reason:** confusing error not security loss.
  **Target:** PR #9.5.
- **C10** — `RotateKEK` uses per-row clock and PID-file check is racy.
  **Reason:** operator-discipline required; non-monotonic `updated_at`.
  **Target:** PR #9.5.
- **OPS-12** — `KEY-ROTATION.md` claims fsync of directory; `WriteKEKFile`
  doesn't fsync. **Reason:** doc-vs-code drift; durability gap rarely
  triggered. **Target:** PR #9.5.
- **OPS-13** — `serve --dry-run` claims to print routes but only prints
  config path + listen. **Reason:** doc-vs-code drift. **Target:** PR #9.5.
- **OPS-14** — `AuditForwarderConfig` defines an `s3` kind in code
  comment unsupported elsewhere. **Reason:** comment drift; addressed
  alongside forwarders wiring. **Target:** PR #9.5.
- **OPS-15** — SIGUSR1 diagnostics dump is minimal. **Reason:**
  diagnostic ergonomics. **Target:** PR #9.5.
- **OPS-16** — `Config.Validate` doesn't enforce `kek.file` mode at
  `LoadConfig`; `--dry-run` side-effects. **Reason:** related to OPS-13
  dry-run cleanup. **Target:** PR #9.5.
- **user-attrs-leak-token-hash** — `Auth.Authenticate` puts full session
  token hash hex into `User.Attrs`. **Reason:** bounded; storage-side
  identifier cannot be reversed to the cookie. **Target:** PR #9.5.
- **audit-recover-prevhash-trusts-tail** — `recoverPrevHash` blindly
  trusts the last line of the audit file on boot. **Reason:** `audit
  verify` will still detect divergence. **Target:** PR #9.5.
- **cfg-validate-skips-merge-then-validate** — `standalone.Config.Validate`
  duplicates `dbadmin.Config.validate` logic. **Reason:** drift risk for
  future invariants. **Target:** PR #9.5.
- **save-uses-clock-for-createdat-not-handler-time** — `Connections.Save`
  overwrites `Connection.CreatedAt` / `UpdatedAt`. **Reason:** test-only
  surprise; documented in follow-up. **Target:** PR #9.5.

---

## Source: PR #10 adversarial review (workflow run wf_0425c646-dd8)

The 4-lens review of the panel-integration glue (`internal/api/dbadmin/`
+ 4 edited panel files) produced 35 raw findings (2 critical, 4 high,
12 medium, 14 low, 3 nit). After dedupe + triage: 7 must-fix items
were promoted and landed in PR #10 itself — audit signing key moved
out of the settings table (PD-SEC-01); CSRF cookie/header names made
configurable so the panel's existing `auracp_csrf` / `X-CSRF-Token`
contract aligns with dbadmin (PD-SEC-02/INT-1); nginx panel-domain
template emits `Upgrade` + `Connection: upgrade` headers so
`/api/dbadmin/sql/stream` works end-to-end (INT-2); FileAuditSink
size-based rotation with chain preservation across files (INT-3);
mountCloser bounded by `Config.ShutdownTimeout` (C1/INT-8); panel
audit mirror moved to a bounded async queue (INT-10/SDK-2);
`ResolveCurrentUser` replaced with `ResolveIdentity` returning a
minimal `IdentitySummary` (no PasswordHash / TOTPSecret) (INT-11).
The remaining 25 findings are deferred below.

### Deferred medium findings — PR #10.5

- **PD-SEC-03** — Encrypted-at-rest secrets share KEK without AAD /
  context binding. **Reason:** a panel-state ciphertext could be
  swapped into `aura_db_connections.creds_enc` and decrypt; cross-
  domain leak is bounded by who has raw-SQL access. **Target:** PR #10.5
  (add AEAD with `dbadmin:creds:` AAD prefix; mirror panel-state
  encryption tags).
- **PD-SEC-04** — Step-up flag survives panel logout (stale entries
  in `stepUpStore`). **Reason:** not directly exploitable today
  (Authenticate gates first), but confused-deputy risk if a future
  WS reconnect path trusts a cached User. **Target:** PR #10.5
  (logout hook from panel into adapter).
- **INT-4** — `Config.Max` ceilings (`TimeoutMax`, `ResultRowsMax`,
  `ResultBytesMax`) not surfaced to panel config YAML. **Reason:**
  operators can't tune; defaults match SECURITY.md §14. **Target:**
  PR #10.5.
- **INT-5** — `TestAdapter_AdminerCoexists` validates only mux pass-
  through, not the nginx config. **Reason:** Adminer is served by
  nginx, not auracpd's mux, so the test asserts the wrong layer.
  **Target:** PR #10.5 (add nginx template render test).
- **INT-6** — `aura_db_grants` has no FK to `panel_users`; orphan
  grants survive user delete. **Reason:** orphan rows accumulate
  but cause no security exposure. **Target:** PR #10.5.
- **INT-7** — Backup without `/etc/auracp/secret.key` silently
  fails to decrypt `aura_db_connections.creds_enc`. **Reason:**
  operator-visible only after restore; documented in
  `KEY-ROTATION.md`. **Target:** PR #10.5 (loud failure + backup
  manifest).
- **INT-9** — Logger split: `slog.Default` (dbadmin) vs `log.Printf`
  (panel), no shared request-ID. **Reason:** correlation across
  log streams is manual. **Target:** PR #10.5 (shared slog handler
  with request-ID injection middleware).
- **SDK-1** — `VerifyStepUp` returns `ErrUnauthenticated` for
  missing-TOTP enrollment instead of a distinct sentinel. **Reason:**
  client can't tell "not enrolled" from "session expired" — both
  return 401. **Target:** PR #10.5 (`ErrStepUpUnavailable` sentinel
  in `pkg/dbadmin`).

### Deferred low / nit findings — PR #10.5

- **PD-SEC-05** — `ResolveCurrentUser` once returned full `store.User`;
  the deprecated function still exists internally for panel use.
  **Reason:** in-package users don't leak; consider removing in a
  later cleanup. **Target:** PR #10.5.
- **PD-SEC-06** — Adapter `HasPermission` does not consult
  `act.RequiresStepUp()` for ROLE_ADMIN paths. **Reason:** admin
  trust assumption is documented in SECURITY.md §4. **Target:**
  PR #10.5 (admin step-up parity with non-admin).
- **PD-SEC-07** — `panelConns.Get/Credentials` have no inline
  authorization filter; rely on `Auth.HasPermission` upstream.
  **Reason:** defense-in-depth gap, no current bypass. **Target:**
  PR #10.5.
- **C2** — `panelAudit.Record` uses `fmt.%q` producing JSON-invalid
  detail for exotic bytes. **Reason:** rare with redacted SQL; only
  affects panel audit_log mirror. **Target:** PR #10.5 (json.Marshal
  detail).
- **C3** — `panelConns.RolesFor` returns `RoleNone` rows (no role
  >= filter). **Reason:** caller filters; minor over-fetch. **Target:**
  PR #10.5.
- **C4** — `loadOrCreateSigningKey` silently regenerates on
  corruption. **Reason:** key-file should be operator-managed; silent
  regen masks tamper. **Target:** PR #10.5 (refuse start; surface
  via boot log).
- **C5** — Panel-mirror `AddAudit` ignores ctx. **Reason:** no
  cancellation honored; benign since the call is fast. **Target:**
  PR #10.5 (`AddAuditContext`).
- **C6** — CSRF bypass prefix uses raw `r.URL.Path` (`../` traversal
  benign via ServeMux 307). **Reason:** Go's ServeMux normalizes
  before matching, but the bypass check should also normalize.
  **Target:** PR #10.5 (path.Clean + HasPrefix).
- **C7** — `panelConns.Save` returns raw UNIQUE-constraint error on
  duplicate name. **Reason:** wire error envelope is acceptable but
  not great. **Target:** PR #10.5 (map to ErrConflict).
- **INT-12** — Step-up key is `{session, action}` not `{session,
  action, connectionID}`. **Reason:** step-up scope is broader than
  intended; documented in SDK-3. **Target:** PR #10.5 (per-conn
  scoping).
- **INT-13** — `ConnectionStore` has no Grant route; `panelConns.Grant`
  is unreachable from the engine. **Reason:** grants today are
  managed via direct SQL or future panel UI. **Target:** PR #10.5
  or PR #11 (panel UI).
- **INT-14** — `panelAuth.Authenticate` runs full `RolesFor` scan
  for ROLE_ADMIN per request. **Reason:** ROLE_ADMIN gets implicit
  RoleOwner on every conn so the scan is wasted work. **Target:**
  PR #10.5 (skip RolesFor for ROLE_ADMIN; rely on direct allow).
- **SDK-3** — Step-up store keys on raw Action, not Action class.
  **Reason:** dbadmin.Action has no public Class() method; adapter
  works around it. **Target:** PR #10.5 (add Class() to pkg/dbadmin).
- **SDK-4** — `panelConns.Delete` relies on implicit transaction
  for FK cascade. **Reason:** SQLite's default behavior is fine but
  fragile. **Target:** PR #10.5 (explicit BEGIN/COMMIT).
- **SDK-5** — Engine maps `ErrForbidden` → 403 not 404 on global
  actions — existence leak on wire. **Reason:** documented behavior
  (404 only for connection-scoped). Confirm intentional. **Target:**
  PR #10.5 (re-read SECURITY.md §10.3 and either fix or document
  the carve-out).
- **SDK-6** — ROLE_ADMIN `allIDs` scan in `RolesFor` is wasted work.
  **Reason:** duplicate of INT-14. **Target:** PR #10.5.
- **SDK-7** — `panelConns.Grant` exposed but not routed by engine.
  **Reason:** duplicate of INT-13; nit. **Target:** PR #10.5.

---

## Source: PR #11 adversarial review (workflow run wf_e02a04bb-606)

The 4-lens review of the Aura DB shell SPA (`web-aura-db/` + supporting
panel touch-points) produced 55 findings (1 critical, 8 high, 18 medium,
17 low, 11 nit). After dedupe + triage: 13 must-fix items were promoted
and landed in PR #11 itself — WS subprotocol now carries
`aura.csrf.<token>` so the panel's PR #8 CSWSH gate accepts browser
upgrades (WS-CSRF-MISSING-SUBPROTOCOL); WS reconnect loop hard-capped
with `document.visibilityState` gating + terminal `stream_unavailable`
emission (WS-RECONNECT-STORM, WS-EXEC-WHILE-FAILED);
`encodeURIComponent` applied to every dynamic path segment at the
`api.js` boundary (CONN-ID-PATH-TRAVERSAL); global
`:focus-visible` ring + targeted overrides for `.btn`/`.tab`/`.tree-row`
/`.dropdown__item`/`.toggle`/`.input`/`.select` (a11y-01); LeftTree
downgraded to `role=listbox`/`option` so AT no longer expects the
unimplemented tree contract (a11y-02); Modal focus-trap, focus-restore,
Escape-to-close, and initial-focus management (a11y-03); StatusBar
center + right wrapped in `role=status aria-live=polite`, error
transitions promoted to `aria-live=assertive` (a11y-04); nginx WS
upgrade regex corrected to `^/api/dbadmin(/.*)?/sql/stream$` with
positive `regexp.MatchString` test (INT-1); panel `/login?next=`
contract implemented via `aura_post_login` cookie with `/dbadmin/`
allowlist (INT-2); Sign Out replaced with POST `/api/auth/logout`
honoring `X-CSRF-Token` (INT-3); `make build` now depends on
`ui-dbadmin` and `webui_test.go` asserts a non-empty embedded
`index.html` (INT-4). The remaining 42 findings are deferred below;
note that **CSP-STYLE-UNSAFE-INLINE** was reclassified to deferred per
the synthesis-lens recommendation (no XSS sinks exist today, so the
amplifier is latent), pending the inline-style migration tracked in
**dc-9** which it pairs with.

### Deferred medium findings — PR #11.5

- **CSP-STYLE-UNSAFE-INLINE** — Panel-wide CSP allows `'unsafe-inline'`
  for `style-src`, which Aura DB inherits. **Reason:** triage rule would
  promote any CSP issue, but the synthesis lens kept it deferred: the
  SPA has zero XSS sinks today (per ERROR-RENDER-SAFE-TODAY), so the
  amplifier is latent. The real work is the dc-9 inline-style migration;
  dropping `'unsafe-inline'` is a one-line follow-up once that lands.
  **Target:** PR #11.5 (converges with dc-9).
- **a11y-05** — Tab strip declares `role=tablist` but tabs navigate
  routes rather than tabpanels. **Reason:** non-boundary medium —
  semantic mismatch but not a keyboard trap. **Target:** PR #11.5
  (drop the role or implement the WAI-ARIA tabs contract end-to-end).
- **a11y-06** — No skip link and no `<main>` landmark. **Reason:**
  non-boundary medium; tedious for keyboard users but not a full
  lockout. **Target:** PR #11.5.
- **a11y-07** — Document title is static "Aura DB" on every route.
  **Reason:** non-boundary medium; orientation issue only. **Target:**
  PR #11.5 (per-route `<svelte:head><title>` block).
- **a11y-08** — Layout breaks below ~720px — no mobile / narrow-viewport
  handling. **Reason:** non-boundary medium; web-aura-db is a desktop
  panel tool by product positioning. **Target:** PR #11.5 (or accept
  as a documented constraint).
- **a11y-09** — Primary button white-on-copper fails WCAG AA contrast
  in dark theme. **Reason:** non-boundary medium contrast issue;
  legible if borderline. **Target:** PR #11.5 (a11y polish pass).
- **a11y-10** — Errors flash as plain text with no toast, no
  `role=alert`, no dismissal. **Reason:** partially overlaps with the
  a11y-04 must-fix (`aria-live` regions now cover transient state),
  but the toast pattern itself is a follow-up. **Target:** PR #11.5
  or PR #12 (notification system).
- **a11y-11** — Several screens swallow fetch errors silently;
  `ErrorBoundary` exists but isn't wired. **Reason:** non-boundary
  medium UX gap, not a full lockout. **Target:** PR #11.5 (wire the
  boundary at the route layer).
- **a11y-12** — Long connection names break layout — no `title=`
  tooltip or truncation strategy. **Reason:** non-boundary medium;
  layout still functional. **Target:** PR #11.5 or PR #12 (connection
  list redesign).
- **a11y-13** — Tab close button invisible until row hover.
  **Reason:** reinforced by dc-3, but `Cmd-W` shortcut exists as
  keyboard fallback so it's a non-blocking hardship rather than a
  lockout. **Target:** PR #11.5 (or PR #13 when tabs evolve with the
  query editor).
- **a11y-14** — DropdownMenu does not handle Escape, ArrowUp/Down, or
  first-item focus on open. **Reason:** non-boundary medium; menu
  still openable/clickable. **Target:** PR #11.5 (WAI-ARIA menu
  pattern).
- **dc-1** — `StatusDot` has no `connecting` state; the pulse
  animation is dead CSS. **Reason:** design-coherence medium; per
  triage rule we defer non-boundary mediums. **Target:** PR #11.5.
- **dc-2** — `EngineGlyph` collides MySQL / MSSQL on the initial
  letter "M". **Reason:** design-coherence medium. **Target:**
  PR #11.5 (or PR #14 when MSSQL driver lands and the glyph set
  expands).
- **dc-3** — Tab close button hidden by `visibility: hidden` until
  hover (design-coherence view of a11y-13). **Reason:** design-
  coherence medium reinforcing the a11y deferral above. **Target:**
  PR #11.5 (fix converges with a11y-13).

### Deferred low findings — PR #11.5

- **FONTS-NO-SRI-THIRD-PARTY** — Google Fonts loaded cross-origin
  without an SRI integrity attribute. **Reason:** low severity,
  requires a CDN-compromise scenario; aligns with **dc-13**
  (self-host fonts) rather than a shippability blocker. **Target:**
  PR #11.5 (fix converges with dc-13).
- **OPEN-REDIRECT-NEXT-PARAM** — `AuthGate` builds `/login?next=`
  from `location.hash` without validating the prefix. **Reason:**
  no SPA-side bug today (the panel `/login` handler hardening
  landed in INT-2's allowlist); flagged as a reminder if the redirect
  contract is ever moved client-side. **Target:** PR #11.5 (or close
  as already-mitigated server-side).
- **CSP-HEADER-NOT-IN-SPA-HTML** — Aura DB `index.html` declares no
  `<meta http-equiv="Content-Security-Policy">`. **Reason:** defense-
  in-depth gap; the panel's response-header CSP is the canonical
  source today, so a missing meta only matters under a future
  regression. **Target:** PR #11.5.
- **a11y-15** — Initial render shows a FOUC. **Reason:** low-severity
  perf / polish issue. **Target:** PR #11.5 (preload critical CSS or
  defer the SPA mount until styles applied).
- **a11y-16** — Toggle uses `aria-pressed` but should use
  `role=switch`. **Reason:** semantic nit, readable as-is. **Target:**
  PR #11.5.
- **a11y-17** — Connection list table rows clickable but not keyboard-
  activatable. **Reason:** low; once a11y-02's listbox downgrade lands
  in LeftTree, the tree provides an alternative path. **Target:**
  PR #11.5 (or PR #12 when the connection list is redesigned).
- **a11y-18** — Tree filter input has no associated `<label>` and no
  `aria-label`. **Reason:** low-severity labelling issue. **Target:**
  PR #11.5.
- **a11y-19** — TopNav nav buttons advertise no active state to
  assistive tech. **Reason:** one-line `aria-current="page"` fix.
  **Target:** PR #11.5.
- **a11y-20** — StatusBar font at 11px with `#6b727d` fails WCAG AA
  for small text. **Reason:** low contrast issue on tertiary text.
  **Target:** PR #11.5 (token bump or weight bump).
- **a11y-21** — Resize handle is mouse-only; no keyboard adjustment.
  **Reason:** latent — `ResizeHandle` is not yet mounted (per dc-4).
  **Target:** PR #11.5 (fix when wiring; pairs with dc-4) or PR #13
  if the editor pane lands the handle first.
- **dc-4** — `ResizeHandle` component exists but is never mounted.
  **Reason:** design-coherence low; defer. **Target:** PR #11.5 (or
  PR #13 when the query-editor pane wants resizable panels).
- **dc-5** — `AuthGate` has no brand presence. **Reason:** design-
  coherence low. **Target:** PR #11.5.
- **dc-6** — Brand-string drift: `AuraDB` vs `Aura DB`. **Reason:**
  design-coherence low. **Target:** PR #11.5.
- **dc-7** — Shadow discipline broken: `.dropdown` has a `box-shadow`
  against the in-file no-shadow rule. **Reason:** design-coherence
  low. **Target:** PR #11.5.
- **dc-8** — Sharp-corner rule violated by `.tab` top corners.
  **Reason:** design-coherence low. **Target:** PR #11.5.
- **dc-9** — Inline `style=` attributes scatter the muted-body recipe
  across components. **Reason:** design-coherence low. **Target:**
  PR #11.5 (migration converges with CSP-STYLE-UNSAFE-INLINE).
- **dc-13** — Fonts loaded over the network only; no local fallback.
  **Reason:** design-coherence low; pairs with FONTS-NO-SRI-THIRD-PARTY.
  **Target:** PR #11.5.
- **INT-5** — `AuthGate` `hasCookie` heuristic is a no-op (the panel
  mints CSRF on every response, so the cookie is always present).
  **Reason:** non-boundary low; the functional auth boundary still
  works via `api.js`'s 401 handler. **Target:** PR #11.5 (drop the
  heuristic or replace with a real probe).

### Deferred nit findings — PR #11.5

- **CSRF-COOKIE-REGEX-OK** — Positive confirmation: the CSRF cookie
  regex in `api.js` is correctly anchored. **Reason:** no change
  required. **Target:** none (kept as a positive entry for audit
  parity).
- **STORAGE-NO-SECRETS** — Positive confirmation: `localStorage` /
  `sessionStorage` contain only UI state (theme, last-route, etc.).
  **Reason:** no change required. **Target:** none.
- **ERROR-RENDER-SAFE-TODAY** — Positive confirmation: error messages
  are rendered as Svelte text interpolation; no `@html` / `innerHTML`
  anywhere in the SPA. **Reason:** no change required; underpins the
  CSP-STYLE-UNSAFE-INLINE deferral above. **Target:** none.
- **a11y-22** — Tree filter placeholder should read "Search
  connections". **Reason:** copy nit. **Target:** PR #11.5.
- **a11y-23** — `EmptyState` heading level fixed at `h3` — breaks the
  heading hierarchy on pages where it appears at top level.
  **Reason:** minor heading-order nit. **Target:** PR #11.5 (accept a
  `level` prop).
- **dc-10** — Welcome subtitle is mild marketing copy. **Reason:**
  design-coherence nit. **Target:** PR #11.5.
- **dc-11** — Light-theme `.btn--primary` dead override. **Reason:**
  design-coherence nit. **Target:** PR #11.5.
- **dc-12** — Raw `#fff` hex literal inside `EngineGlyph` SVG instead
  of a token. **Reason:** design-coherence nit. **Target:** PR #11.5.
- **dc-14** — Tabular-nums `.num` class doing double duty as a
  mono-cell helper. **Reason:** design-coherence nit. **Target:**
  PR #11.5.
- **INT-6** — Missing `.gitignore` entries for `web-aura-db` build
  artifacts. **Reason:** contributor-footgun nit. **Target:** PR #11.5.
- **INT-7** — `ui-dbadmin` Makefile target wipes `dist/.gitkeep` on
  every build. **Reason:** tiny stylistic loose-end alongside the
  INT-4 must-fix. **Target:** PR #11.5.

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
