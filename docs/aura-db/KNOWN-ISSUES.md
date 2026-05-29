# Aura DB — Known Issues + Deferred Work

This document tracks issues identified during review that we have NOT
fixed in the current PR, with the rationale for the deferral and the
target PR for the fix. Every entry is honest about the trade-off; we do
NOT pretend something is fixed when it isn't.

This is a living document. When an issue lands a fix, move the entry to
the corresponding ADR or release notes; don't leave dead entries here.

---

## Source: PR #2.5 adversarial review

PR #2.5 swapped the tokenizer-only statement classification for an
AST-primary cascade (Vitess for MariaDB/MySQL, pg_query_go for
Postgres). The cascade keeps the forbidden-token matcher as a
second-gate defense; AST + tokenizer + matcher all run and the
strictest verdict wins. The following residual limitations are
acknowledged and tracked here.

### Accepted — PR #2.5 ships with these limitations

#### Dynamic SQL (PREPARE … EXECUTE) tables remain unknown

The prepared statement body is opaque to the AST — the parser sees a
string literal. ParsedStatement.Tables stays nil for those statements.
The per-table authorization layer in PR #4 must treat empty Tables as
"unknown tables touched" and either refuse or downgrade to
per-connection authorization. There is no clean fix short of executing
the PREPARE side-effect free, which the panel will not do.

#### Vitess refuses GRANT / REVOKE and some vendor extensions

Vitess marks GRANT and REVOKE keywords as UNUSED in its grammar. Those
statements fall back to the PR #2 tokenizer per-statement —
classification is unchanged (KindGrant / KindRevoke / ClassDangerous),
but Tables stays nil because the AST didn't see the statement. Same
applies to a small list of MySQL-vendor extensions that vitess hasn't
caught up with. The cascade logs a single INFO-level fallback per
event with the sha256-prefix of the SQL (never the raw text).

#### Postgres search_path is not resolved

Unqualified Postgres references (`SELECT * FROM users` with no schema)
leave Target.Schema empty. The per-table auth layer in PR #4 must
either consult the connection's current search_path or treat empty
schema as "any schema the user has access to". The classifier does
not have a Postgres connection in scope, so it cannot resolve this.

#### CGO_ENABLED=0 builds disable the Postgres AST

libpg_query is C-only and pg_query_go is therefore cgo. Builds with
CGO_ENABLED=0 silently degrade the Postgres parser to the PR #2
tokenizer (a single INFO log line announces this at process start).
MySQL/MariaDB stays on the Vitess AST because Vitess is pure Go.
Operators who build no-cgo binaries do so deliberately; the
degradation matches PR #2 behavior and preserves all security
guarantees of that PR.

### Deferred — depends on PR #2.5 AST availability

#### PR #7.5 redaction can now consume the AST — [resolved via token sweep]

KNOWN-ISSUES entry "classifier.RedactSensitiveInline misses non-
standard credential forms" (originally targeted at PR #7.5) has been
closed in PR #7.5 by extending the existing token-stream walker
rather than wiring through the AST. Coverage now includes IDENTIFIED
VIA … AS/USING, CONNECTION '<dsn>', dblink_connect[_u], COPY FROM
PROGRAM, and a post-pass over every string literal for credentialed
URI schemes (postgresql://, mysql://, mongodb://, …). The
token-stream approach degrades gracefully under CGO_ENABLED=0 (no
AST available); future enhancement to leverage the AST for stricter
context-checking is a low-priority cleanup.

#### Binary size budget

The Vitess + pg_query_go additions raise the static aura-db binary
size by an estimated ~10 MiB on linux/amd64. The CI matrix needs to
add the `45 MiB hard ceiling` check from the PR #2.5 design. Until
that lands, operators rebuilding from source should verify the
binary size manually.

#### Cross-compile to linux/arm64 needs aarch64 toolchain

The pg_query_go cgo step needs `aarch64-linux-gnu-gcc` on the
builder when GOARCH=arm64 GOOS=linux. The existing CI Dockerfile
already includes the C toolchain for the macOS build host; the
Linux ARM64 cross-compile target needs one line of apt install.

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
**Status (PR #6.5): all deferred items below are RESOLVED.** See
`pkg/dbadmin/explain/` for the implementing changes; the bullets
are retained for historical context.

### Resolved high findings — PR #6.5

- **H5** [resolved] — MariaDB Nested Loop now sets `RowsExpected`
  multiplicatively (outer × inner) on the join node itself; see
  `parseMySQLNestedLoop` in mysql.go and `TestMySQL_NestedLoop_MultiplicativeRows`.
- **H6** [resolved] — Postgres per-node metadata (Sort Key, Group
  Key, Hash Keys, Output, Subplan Name, Workers Planned/Launched,
  Parallel Aware, JIT, Triggers, Settings) is decoded by `pgPlan`
  and surfaced on the new `Node.Extras` map (a typed
  `map[string]any` with stable lowerCamelCase keys). See
  `walkPgNode` in postgres.go and `TestPostgres_Extras_H6`.
- **H7** [resolved] — MariaDB shapes for windowing, having /
  select-list subqueries, "Impossible WHERE", and coexisting
  subquery+table all parse explicitly. Residual unknown shapes emit
  `MariaDB block shape not recognized: keys=[...]` on
  `Plan.Warnings`. See `parseMySQLBlock`, `isImpossibleWhereBlock`,
  `attachMySQLSubqueries`, and `TestMySQL_ShapeCoverage_H7`.
- **H9** [resolved] — MariaDB `Plan.Total` is now a rolled-up view
  of the whole tree (cost = max, buffers = additive); doc.go
  documents per-engine divergence in the matrix. See
  `rollupMariaDBTotals` and `TestPlan_Total_H9_MariaDB`.
- **H11** [resolved] — Engine-parity field availability matrix
  added to `doc.go`.

### Resolved medium findings — PR #6.5

- **M1** [resolved] — `validateSQLForExplain` rejects multi-statement
  payloads and leading line comments with embedded semicolons before
  the EXPLAIN keyword is prepended. See `TestExplain_ValidateSQL_M1`
  + `TestExplain_ValidateSQL_GateInExplain`.
- **M2** [resolved with H6] — JIT / Triggers / Settings surface on
  `Plan.Root.Extras["jit"|"triggers"|"settings"]`.
- **M3** [resolved] — New `Plan.PlanningTimed bool` disambiguates
  "not measured" from "sub-microsecond". See
  `TestPlan_PlanningTimed_M3`.
- **M4** [resolved] — `asFloat64` recognizes K/M/G/T/P suffixes
  (base 1024) so MariaDB's `data_read_per_join` "10M" parses to
  10485760. See `parseFloatWithSuffix` + `TestAsFloat64_KMG_M4`.
- **M5** [resolved] — `parseMySQLTable` keeps `RowsExpected` =
  `rows_examined_per_scan` and surfaces `rows_produced_per_join` on
  `Node.Extras["rowsProducedPerJoin"]`. See `TestParseMySQLTable_M5`.
- **M6** [resolved] — MariaDB warning code+level prefix the message;
  codes are also collected on `Plan.Root.Extras["warningCodes"]` for
  filterable triage. See `TestMySQL_Normalize_Warnings`.
- **M7** [resolved] — `ExplainWithConfig` plumbs
  `cfg.Query.TimeoutMax` into the EXPLAIN-wrapping limits when
  caller didn't set one. See `TestExplainWithConfig_M7`.
- **M8** [resolved with H2] — No remaining Sscanf paths.
- **M9** [resolved] — `ExplainOpts.OmitRaw` drops `Plan.Raw` when
  set. See `TestExplainOpts_OmitRaw_M9`.
- **M10** [resolved] — `mergeMetrics` no longer sums RowsExpected
  additively (it takes the max across siblings); join nodes set
  cardinality multiplicatively (H5). The Ordering / Grouping wrapper
  paths preserve their own cost via `combineWrapperMetrics` instead
  of double-counting via parent rollup.
- **M11** [resolved] — `Normalizer` interface unexported as
  `normalizer` to stop implying a public extension surface.
- **M12** [resolved] — Engine-specific Raw shape documented in
  `doc.go` (Postgres = JSON array, MariaDB = JSON object).
- **M13** [resolved] — `EngineMariaDB` / `EnginePostgres` consts
  added; normalizers use them. See `TestEngineConstants_M13`.
- **M14** [resolved] — `ExplainOpts.PGOptions` (typed struct) toggles
  BUFFERS / VERBOSE / SETTINGS / WAL; ANALYZE remains controlled by
  the ClassRead gate. See `buildPostgresExplainFlags` +
  `TestPostgresExplainFlags_M14`.
- **M15** [resolved] — `Node.Extras` keeps the full set of Postgres
  condition fields ("filter"/"indexCond"/"hashCond"/"mergeCond"/
  "recheckCond") even though `Node.Filter` still collapses to one.
  See `TestPostgres_Filter_Recheck_M15`.

### Resolved low + nit findings — PR #6.5

- **L1** [resolved] — `asInt64(float64)` now rounds half-away-from-
  zero. See `TestAsInt64`.
- **L2** [resolved] — Shared Dirtied Blocks surfaced on
  `Metrics.BuffersDirtied`. See `TestPostgres_BuffersDirtied_L2`.
- **L3** [resolved with M15] — Recheck Cond preserved on Extras.
- **L4** [resolved] — `index_merge` / `index_subquery` /
  `unique_subquery` mapped explicitly. See `TestMysqlAccessKind_L4`.
- **L5** [resolved] — Nested unions recurse via the generic block
  dispatcher. See `TestMySQL_NestedUnion_L5`.
- **L6** [resolved] — Non-`table` entries in `nested_loop` now
  re-enter `parseMySQLBlock` rather than dropping. See
  `TestMySQL_BlockNLJoin_L6`.
- **L7** [resolved] — Wrapper `cost_info` is combined with child
  metrics via `combineWrapperMetrics` instead of being overwritten.
- **L8** [resolved] — `readSingleJSONRow` asserts the iterator is
  exhausted after the first row. See
  `TestExplain_AssertSecondNextEOF_L8`.
- **L9** [resolved with H7] — Unknown shapes emit a warning naming
  the keys actually seen.
- **N1** [resolved] — `Plan` / `Node` / `Metrics` document their
  additive-stability contract in their respective doc comments.
- **N2** [resolved] — `Metrics.CostStart` is actively zeroed on
  every MariaDB path (`parseMySQLTable` + `rollupMariaDBTotals`).
  See `TestMariaDB_CostStart_AlwaysZero_N2`.

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

#### H4 — RedactSensitiveInline misses non-standard credential forms [resolved]

Closed in PR #7.5. `classifier.RedactSensitiveInline` now covers:

- MariaDB `IDENTIFIED VIA <plugin> AS|USING '<hash>'`
- Postgres `CREATE/ALTER SUBSCRIPTION … CONNECTION '<dsn>'`
- `dblink_connect[_u]('host=… password=…')` — every string arg in
  the call is redacted
- `COPY … FROM/TO PROGRAM '<shell command>'`
- Any string literal carrying a credentialed URI:
  `postgresql://u:p@…`, `postgres://`, `mysql://`, `mariadb://`,
  `mongodb://`, `mongodb+srv://`, `redis://`, `rediss://`. Bare
  URIs without userinfo are preserved.

Test coverage in `classifier_test.go` (TestRedactSensitiveInline
new cases) and `history_test.go` (TestAppend_Redacts*). The fix is
a token-stream walker extension, not an AST consumer — degrades
under CGO_ENABLED=0 the same way the rest of the classifier does.

#### H8 — LIKE fallback Search is silent O(n) scan [resolved]

Closed in PR #7.5:

- `history.OpenWithOpts(ctx, dsn, engine, OpenOpts{RequireFTS5: true})`
  errors at Open time if FTS5 is unavailable.
- `Store.HasFTS() bool` exposes the active code path; callers
  surface this as a "search is in degraded mode" badge.

Tests: TestOpenWithOpts_RequireFTS5_SucceedsWhenAvailable,
TestHasFTS_ReportsFallback.

#### H9 — No retention enforcement; storage grows unbounded [resolved]

Closed in PR #7.5:

- `OpenOpts.MaxRows` ceiling enforced at Append time (oldest
  evicted via id-IN subselect; CASCADE removes entry_tags rows).
- `Store.StartRetentionLoop(ctx, period, retention)` launches a
  background goroutine that calls `DeleteOlderThan` every period;
  returns a cancel func for shutdown.
- `DeleteOlderThan` now chunks in 1000-row batches and honors
  ctx cancellation between batches.

Tests: TestOpenWithOpts_MaxRows_EvictsOldest,
TestDeleteOlderThan_ChunkedSweep, TestStartRetentionLoop_RemovesOldEntries.

### Deferred medium findings — PR #7.5 [resolved]

All resolved in PR #7.5:

- Negative `opts.Offset` in `Search` now validated (matches `List`).
- `:memory:` detection recognizes `file::memory:?…` shared-cache
  URIs and `file:…?mode=memory` form; DSN-pragma append uses `?`/`&`
  separator selection so DSNs with pre-existing query strings don't
  get corrupted.
- FTS5 query input is length-capped at 4 KiB and ASCII control
  bytes are stripped before phrase-wrapping.
- bm25 ranking accepts per-column weights via `OpenOpts.FTSBM25Weights`;
  ordering tiebreaker is `bm25 ASC, executed DESC, id DESC` so
  identical scores no longer ping-pong across pages.
- `OpenOpts.BusyTimeoutMS` / `MaxOpenConns` / `MaxWriters` make
  panel-UI contention tunable instead of hard-coded.
- FTS5 storage overhead (~1.8×) documented in `doc.go`.
- `ListOpts.ClassPtr *classifier.QueryClass` replaces the
  `IncludeClass` workaround; the old `Class` + `IncludeClass`
  pair is retained for back-compat.
- `entry_tags(tag, entry_id)` normalized table added; `List`/`Search`
  Tag filters JOIN against it for O(log n) tag lookups. Serialized
  tags column on `entries` retained for FTS5 indexing of tag tokens
  and for forward compatibility.
- Process-wide writer semaphore (`OpenOpts.MaxWriters`, default 8)
  bounds concurrent Append/Star/Tag/Delete; configurable per Store.
- Prepared statements cached at Open for Append + entry_tags
  rewrites (`stmtAppend`, `stmtTagsClear`, `stmtTagsInsert`).
- Admin-scoped partial index `idx_entries_starred_executed (starred,
  executed DESC) WHERE starred=1` covers cross-user starred listings.
- `initSchema` now runs the FTS5 CREATE VIRTUAL TABLE probe
  separately from the trigger install — trigger errors surface
  cleanly instead of being swallowed as "FTS5 not available."

### Deferred low + nit findings — PR #7.5

- `closed`/`Exec` TOCTOU — left as-is (the `sql.ErrConnDone` path
  is benign; locking every op is gratuitous).
- `MaxSQLLength` truncation now snaps to the previous rune boundary
  via `truncateUTF8`. [resolved]
- `Append`'s timestamp guard rejects `time.Time{}`, `time.Unix(0,0)`,
  and any pre-1970 caller-supplied junk (clamps to `time.Now()`).
  [resolved]
- Admin starred index added (see medium block above). [resolved]
- Append uses `*sql.Stmt` cache (`stmtAppend`). [resolved]
- `MaxSQLLength=256KiB` still truncates 50-statement migration
  pastes — kept; operators using bulk DDL should be running it
  via a migration tool, not the SQL editor.
- `doc.go` Concurrency section now calls out the in-memory
  single-conn pinning. [resolved]
- `DeleteOlderThan` IDs-callback — kept as a future nit; the engine
  audit layer can subscribe to the count for now.
- Error sentinel naming consistency — kept as-is (no-op).

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

### Deferred high findings — PR #8.5 [landed]

All five deferred-high items below were addressed in PR #8.5:

- DEF-1 → new `rateClassStepUp` with 10/15-min sliding window
  (`ratelimit.go`), `/step-up/verify` rewired in `router.go`.
- DEF-2 → `handleQuery` / `handleExplain` now call
  `AuthSurface().HasPermission(user, conn, ActionConnView)` BEFORE
  `Conns().Get` (`handlers_sql.go`).
- DEF-22 → WS stream limits clamp against `Config().Query.*Max`
  (`handlers_stream.go`).
- DEF-23 → `writeFrame` errors propagate; row pump exits on
  EOF / closed-pipe (`handlers_stream.go`).
- DEF-24 → server-side ping ticker emits a ping every 30 s for the
  stream lifetime (`handlers_stream.go`).

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

### Deferred medium findings — PR #8.5 [landed]

PR #8.5 landed every deferred-medium item below except DEF-30 (gated by
PR #16's real exporter) and the SDK-reconciliation items
DEF-33/34/35/36 (DEF-35 now ships a doc comment freezing the wire
form; DEF-33/34 are SDK-side work and remain deferred to a SDK
revision; DEF-36 is a coverage backlog item rolled into the test
plan for the next PR).

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

### Deferred low / nit findings — PR #8.5 [landed]

All low/nit items below ship in PR #8.5 except DEF-9 (`/import` work)
and DEF-10 (Export `SignedURL`), both gated by PR #16. DEF-37
(WS frame schema in SDK.md) is SDK-side documentation work and remains
deferred.

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

### PR #9.5 resolution status

All deferred items above (except **OPS-04**, reassigned to PR #10 for
panel-integrated readiness probes) are addressed in PR #9.5 against
`pkg/dbadmin/standalone/`. Highlights:

- **SEC-05** — `LoadKEK` now `OpenFile`-then-`fstat`s the descriptor,
  rejects mode 0 and any bits broader than 0400. KEK plaintext slices
  are zeroed after `copy` into the [32]byte.
- **SEC-06** — `WebhookForwarder.Ship` rejects non-https URLs and empty
  HMAC secrets at the type level (`bootstrap.go::buildForwarder`
  already enforced both at boot; defense-in-depth covers direct
  constructors).
- **SEC-08** — `RotateKEK` takes a `keyPath` and writes the new key
  file via `WriteKEKFile` AFTER the SQLite commit, collapsing the
  previous "commit tx, swap file later" window. Surface a loud error
  if the file write fails post-commit.
- **SEC-09** — `HIBPClient.Check` asserts `https://` (or loopback for
  test fixtures) before any request goes out.
- **SEC-10** — Added `PHCWithFakeWorkloadMatchingStored` so callers
  with a representative stored hash can match the real Verify
  parameters; `auth_login.go` keeps the policy-params decoy for the
  user-not-found case where matching is impossible.
- **SEC-11 / C9** — `consumeRecoveryCode` iterates EVERY unused code
  before settling on the outcome; the UPDATE runs once with the
  matched hash, returning `ErrInvalidRecoveryCode` on race loss.
- **SEC-12** — Cold-start `recoverPrevHash` walks rotated siblings if
  the current file is empty so the chain anchor survives rotation +
  process restart.
- **SEC-13** — ULID minting clamps `ms` forward against clock
  step-back; same-ms entropy overflow re-seeds and bumps `ms`.
- **SEC-14** — `getSessionByTokenHash` does a `subtle.ConstantTimeCompare`
  on the fetched hash before returning the row.
- **SEC-15** — Audit-log mode invariant tightened from 0640 to the
  documented 0600.
- **C1** — Session expiry uses inclusive boundary (`>=` /
  `<=`) in `Authenticate` and `CleanupExpiredSessions`.
- **C5** — `Login` snapshots `now` once and threads it through
  `createSessionAndCommitTOTPStepAt`.
- **C6** — `Reopen` re-checks the audit file mode and re-`chmod`s
  after `OpenFile`.
- **C8** — `serve.go` shuts down the HTTP server FIRST, then drains
  the engine, with separate `context.WithTimeout` budgets.
- **C10** — `RotateKEK` uses a single `rotateNS` timestamp for the
  whole batch.
- **OPS-01** — `kek-rotate` redirects first-run users to `kek-init`
  with an actionable error when the configured KEK file is missing.
- **OPS-03** — `LoadKEK` calls `checkKEKOwner` (uid match) via the
  unix-tagged `kek_owner_unix.go` helper.
- **OPS-05** — Default `logging.format` is `json`.
- **OPS-06** — `Validate` enforces `tls.min_version`, `logging.level`,
  `logging.format`, and `logging.destination` enums.
- **OPS-11** — `VerifyResult` gained JSON tags + `MarshalJSON` +
  `HumanString` so the CLI can emit machine-readable output.
- **OPS-12** — `WriteKEKFile` fsyncs both the temp file before rename
  and the parent directory after, matching `KEY-ROTATION.md`.
- **OPS-14** — Code comment on `AuditForwarderConfig.Kind` no longer
  lists `s3`.
- **OPS-16** — `Validate` checks `kek.file` mode at config-load time
  when the file already exists.
- **user-attrs-leak-token-hash** — `Auth` keeps the full token hash in
  an internal `sessionTokenIndex` keyed by the truncated session_id;
  `User.Attrs` no longer exposes the full hash.
- **audit-recover-prevhash-trusts-tail** — `recoverPrevHash` validates
  each candidate line parses as a well-formed event before adopting
  its SHA-256 as the running head.
- **cfg-validate-skips-merge-then-validate** — Docstring on
  `standalone.Config.Validate` calls out the drift contract with
  `dbadmin.Config.validate`.
- **save-uses-clock-for-createdat-not-handler-time** — `Connections.Save`
  honors caller-supplied `CreatedAt` when non-zero.

OPS-02 / C7 / OPS-13 / OPS-15 are CLI-cmd-surface items handled in the
matching CLI files (`cmd/aura-db/*.go`) when those subcommands ship.
OPS-07 / OPS-08 / OPS-09 are documentation/packaging entries tracked
in `docs/aura-db/KEY-ROTATION.md` follow-ups.

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
  **Resolved in PR #17:** Adminer was removed; the test (and the
  route it asserted coexistence with) were deleted together.
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

### PR #10.5 resolution status

All deferred mediums + lows above (except those marked as routed
elsewhere) landed in PR #10.5 alongside the OPS-04 readiness probes
reassigned from PR #9.5. Specifically:

**Mediums resolved:** PD-SEC-03 (AEAD with `dbadmin:creds:v1` AAD via
`secret.Box.EncryptAAD`/`DecryptAAD` with legacy-ciphertext
auto-migration on first read), PD-SEC-04 (panel logout invokes the new
`dbadmin.mountCloser.InvalidateSession` hook via
`api.Server.SetLogoutHook`), INT-4 (panel `Config` now surfaces
`QueryTimeoutMaxSec` / `QueryResultRowsMax` / `QueryResultBytesMaxMiB`
from the settings table), INT-5 (already resolved in PR #17 via Adminer
removal), INT-6 (`CREATE TRIGGER trg_aura_db_grants_cascade_panel_user`
in `migrate.go` cascades `panel_users` deletes into `aura_db_grants`),
INT-9 (new `WithRequestIDMiddleware` + `SharedLogger` in `slog.go`;
`X-Request-Id` response header on every `/api/dbadmin/*` request;
`RequestIDFromContext` for handler use), SDK-1 (new
`dbadmin.ErrStepUpUnavailable` sentinel; `auth.VerifyStepUp` returns
it on missing-TOTP enrollment).

**Lows resolved:** PD-SEC-06 (HasPermission narrowed: ROLE_ADMIN
authorization no longer implies step-up bypass; orthogonal gate
preserved), C2 (`audit.go` uses `json.Marshal` for the panel mirror
detail field — exotic bytes no longer break audit_log JSON), C3
(`RolesFor` now filters `role > RoleNone` at the SQL boundary), C4
(`loadOrCreateSigningKey` refuses to start on a corrupted on-disk
key — logs the error via `slog.Default().Error` and returns the
wrapped error from `Mount()`), C5 (`mirrorEvent.reqID` captured at
enqueue time; warn logs join the same request-ID stream), C6
(`middleware.Secure` now normalizes via `path.Clean` before the
`/api/dbadmin/` prefix check — `/api/dbadmin/../auth/login` no longer
bypasses the panel CSRF gate), C7 (`mapSaveErr` translates SQLite
UNIQUE-constraint to `dbadmin.ErrConflict`), INT-12 (stepUpKey now
includes connectionID — per-connection scoping), INT-14 / SDK-6
(`RolesFor(ROLE_ADMIN)` short-circuits to `nil`; HasPermission's admin
branch is the authoritative gate, List uses the admin SQL branch),
SDK-3 (new `Action.Class()` in `pkg/dbadmin/types.go`; stepup store
keyed by `ActionClass` so one approval authorizes sibling actions in
the class), SDK-4 (`panelConns.Delete` runs under an explicit
`BeginTx`/`Commit`).

**Deferred to future PRs:** PD-SEC-05 (deprecated `ResolveCurrentUser`
is already unexported; no public surface to remove), PD-SEC-07 (defense-
in-depth filter on Get/Credentials — current upstream gate is
authoritative and adding a redundant filter risks divergence; revisit
if a host bypass scenario surfaces), INT-7 (backup decrypt-on-restore
UX needs the `aura-db backup`/`restore` CLI surface, not the adapter),
INT-13 / SDK-7 (`panelConns.Grant` routing belongs in PR #11's panel UI
work), SDK-5 (`ErrForbidden`→403 on global actions remains intentional
per SECURITY.md §10.3 — global actions have no existence-leak
concern).

**OPS-04 (reassigned from PR #9.5):** `GET /api/dbadmin/healthz` —
always-200 liveness probe (process is up). `GET /api/dbadmin/readyz` —
503 when the engine is shutting down, the audit chain has closed, or
the panel SQLite store fails a `PingContext`. Both endpoints are
public (no session cookie required) so external orchestrators
(systemd, k8s, uptime monitors) can probe without panel identity.

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

## Source: PR #12 adversarial review (workflow run wf_0d0b427e-524)

The 4-lens review of the row-grid SPA shell + edit pipeline
(`web-aura-db/` grid composable, edit path, filter/sort wire, a11y
surface) produced 62 findings (4 critical, 12 high, 8 medium, 1 low,
0 nit on the must-fix side; 1 high, 18 medium, 17 low, 1 nit on the
defer side). After dedupe + triage: 25 must-fix items landed in PR #12
itself — WIRE-01 (envelope-unwrap of `{error:{code,message,request_id,
details}}`), WIRE-02 (hyphen-form code constants shared client/server),
WIRE-03 (server-side comma split for `IN`/`NOT IN` filter values),
edit-1 (optimistic concurrency via before-values on PATCH + `pk-mismatch`
toast), WIRE-04 (camelCase `databaseTypeName` read), WIRE-05
(`PrimaryKey` stamped from schema reader in `handleReadRows`), WIRE-06
(PK moved off URL path into JSON body + `rowsAffected===0` surfacing),
WIRE-07 (AbortController signal threaded through `api.js` to `fetch`),
WIRE-08 (`Total` populated via `?withTotal=1` Count gate on filter
change), edit-2 / edit-7 / edit-13 (undo + rollback now key on
`pkKey` not `rowIdx`, refresh safe), edit-3 (insert pushes to undo
stack with server-returned PK), edit-4 (no-op detection compares
parsed-value to parsed-value), edit-5 (`\N` sentinel reserved for
NULL, `''` writes empty string), edit-6 (separate `opSeq` for edit
ops vs `reqId` reload guard), edit-8 (insert payload routes through
`parseEditValue`), edit-10 (undo-delete snapshots `columnOrder` at
delete time), a11y-2 (filter bar moved out of grid root), a11y-3
(new-row + header + filter `aria-rowindex` slots renumbered
contiguously), a11y-4 (resize handle replaced with in-tree
`ResizeHandle.svelte` separator pattern), a11y-6 (Tab leaves the
grid in cell mode), a11y-10 (error/warning toasts use `role=alert`),
a11y-16 (ArrowUp at row 0 enters header row with roving tabindex),
edit-1-followup-a11y-24 (Backspace → `edit.clear` mapping removed).
The remaining 37 findings are deferred below.

### Deferred high findings — PR #12.5

- **a11y-1** — aria-rowindex/aria-rowcount slot mismatch (structural
  rows missing slots); reason: polish around rowindex alignment;
  addressed structurally by a11y-3 / a11y-26 fixes — keep tracked but
  not gating. **Target:** PR #12.5.

### Deferred medium findings — PR #12.5

- **perf-2** — Scroll listener queues rAF per event with no
  de-duplication; reason: perf-medium with isolated impact (extra
  reactive recomputes during scroll), not user-blocking and easy to
  address in a follow-up. **Target:** PR #12.5.
- **perf-3** — `virtualWindow` buffer is 4 rows (spec asks >=5);
  reason: cosmetic flash on fast flick-scroll, one-line constant
  change deferrable to perf pass. **Target:** PR #12.5.
- **perf-4** — Cell edit/undo/redo does `rows.data.slice()` — O(n)
  per commit; reason: sub-ms cost on 1k-row pages, structural
  improvement not correctness — defer to perf pass. **Target:**
  PR #12.5.
- **perf-5** — Sticky header+filterbar inside `overflow:auto` —
  Safari repaint hazard; reason: needs Safari verification, if real
  it's a perf medium not a blocker — defer pending browser test.
  **Target:** PR #12.5.
- **edit-9** — Bulk delete partial-failure restore at stale indices;
  reason: display-order quirk on partial failure (rare); `reload()`
  workaround documented in fix. **Target:** PR #12.5.
- **edit-11** — Cell blur commits without checking whether blur was
  triggered by Esc; reason: currently safe by side-effect, flagged as
  fragile rather than broken — refactor follow-up. **Target:**
  PR #12.5.
- **edit-12** — Filter parser downgrades `is null xyz` to ILIKE;
  reason: annoyance not data loss; tighten regex in follow-up.
  **Target:** PR #12.5.
- **a11y-5** — Roving tabindex: grid root and focused cell both
  `tabindex=0`; reason: two tab-stops instead of one, nonconformant
  but not a keyboard trap. **Target:** PR #12.5.
- **a11y-7** — `aria-multiselectable` not set; reason: single
  attribute polish, defer. **Target:** PR #11.5 (overlaps with the
  PR #11.5 a11y polish pass).
- **a11y-8** — `aria-readonly` not exposed; reason: visible
  "READ-ONLY" pill is announced, `aria-readonly` is canonical
  addition — defer to a11y polish pass. **Target:** PR #11.5.
- **a11y-9** — `aria-busy` not set during loads; reason:
  announcement quality, deferrable. **Target:** PR #11.5.
- **a11y-11** — Sort change has no aria-live announcement; reason:
  `aria-sort` is set on header, live-region polish. **Target:**
  PR #11.5.
- **a11y-12** — PK column header has no accessible label; reason:
  polish, visual glyph only — deferrable. **Target:** PR #11.5.
- **a11y-13** — Empty-string sentinel `·` reads as "middle dot";
  reason: polish, minor announcement quality. **Target:** PR #11.5.
- **a11y-14** — Density/page-size selects have terse `aria-label`s;
  reason: label-copy polish, one-line fix in follow-up. **Target:**
  PR #11.5.
- **WIRE-10** — `NOT IN` operator unreachable from filter input;
  reason: becomes free once WIRE-03 lands server-side, add regex in
  follow-up. **Target:** PR #12.5.
- **WIRE-11** — `SchemaBrowser` double-click opens two tabs; reason:
  UX bug outside the grid's correctness envelope, small debounce fix
  in follow-up. **Target:** PR #12.5.
- **WIRE-13** — `commitEdit` doesn't refetch — server coercion
  invisible; reason: behavioral polish, rare-enough to defer until
  `updateRow` returns full row. **Target:** PR #12.5.

### Deferred low findings — PR #12.5

- **perf-6** — Inline `grid-template-columns` on every row vs CSS
  variable; reason: polish, compounds perf-1 but only matters during
  resize — bundle into perf-1 fix or defer. **Target:** PR #12.5.
- **perf-7** — `renderCell` re-runs per scroll tick; array-kind
  re-parses JSON; reason: visible only on large JSON/array columns,
  memoization is a polish improvement. **Target:** PR #12.5.
- **perf-9** — `$effect` creates new grid without disposing
  AbortController/pending; reason: memory churn on tab-switch,
  covered partly by WIRE-07 signal threading — remaining cleanup is
  low-risk. **Target:** PR #12.5.
- **edit-14** — No per-cell saving indicator; reason: UX polish,
  pending count is shown in toolbar so feedback exists. **Target:**
  PR #12.5.
- **edit-15** — `INT` column accepts decimals via `Number()`; reason:
  driver-dependent behavior, predictable enough — sub-classification
  can land in a follow-up. **Target:** PR #12.5.
- **a11y-15** — Filter `aria-label` lowercase "filter X" vs "Filter
  column X"; reason: copy polish. **Target:** PR #11.5.
- **a11y-17** — Gutter rowheader missing `aria-colindex`; reason:
  index-consistency polish. **Target:** PR #11.5.
- **a11y-18** — Edit input lacks `aria-label` tying it to the cell;
  reason: announcement quality polish. **Target:** PR #11.5.
- **a11y-19** — `Cmd+Home`/`End` jump to first/last page instead of
  first/last cell; reason: spec deviation, consider rebinding in
  a11y polish pass. **Target:** PR #11.5.
- **a11y-20** — `aria-rowcount` lies when total is unknown; reason:
  tied to WIRE-08 (no total); once total is populated this becomes
  moot — one-line guard otherwise. **Target:** PR #12.5 (now mostly
  moot post-WIRE-08).
- **a11y-21** — Truncated JSON/binary/array cells have no accessible
  summary; reason: polish for AT, tooltip path exists. **Target:**
  PR #11.5.
- **a11y-22** — No empty-state markup or "no rows" announcement;
  reason: UX/AT polish. **Target:** PR #11.5.
- **a11y-23** — Freeze-left / column-menu / column-reorder have no
  UI; reason: feature gap, persisted state is stable — hide unused
  fields until UI lands. **Target:** PR #12.5 (or later when the
  feature UI lands).
- **a11y-25** — Toast dismiss button inside `role=status`; reason:
  live-region structure polish. **Target:** PR #11.5.
- **a11y-26** — Header/filter row missing `aria-rowindex`; reason:
  bundled with a11y-3 — fix together, tracking separately is
  unnecessary. **Target:** PR #12.5 (converges with the a11y-3
  must-fix landed in PR #12; close on next sweep).
- **WIRE-14** — `api.listRows` is dead code; reason: cleanup, no
  functional impact. **Target:** PR #12.5.
- **WIRE-15** — Filter wire format colon-quoting undocumented;
  reason: latent concern, document grammar in follow-up. **Target:**
  PR #12.5.

### Deferred nit findings — PR #12.5

- **WIRE-16** — `RowGrid.svelte` is a one-line re-export of
  `TableScreen`; reason: pure cleanup. **Target:** PR #12.5.

---

## Source: PR #13 adversarial review (workflow run wf_c41a9c9d-a14)

The 4-lens review of the SQL query editor (`web-aura-db/` SqlEditor +
CodeMirror pane, classifier wiring, exec/cancel registry, streaming
result pipeline, saved-query + history sidebars) produced 35 findings
(2 critical, 8 high, 13 medium, 11 low, 1 nit). After dedupe + triage:
10 must-fix items landed in PR #13 itself — EXEC-1 (cursorPos selection
listener wired so Cmd+Enter actually executes the statement under the
cursor, not the first statement), EXEC-2 (`execAll()` implemented and
wired to Cmd+Shift+Enter so the headline "run all" keymap stops aliasing
"run current"), EXEC-3 (rapid double-Execute now cancels in-flight tabs
before issuing the new exec frame), EXEC-4 (FIFO tab eviction cancels
in-flight executions and clears `sqlStream.handlers` so evicted tabs no
longer leak WS handles + silently drop rows), EXEC-5 (`cancelCurrent`
unsubscribes per-ref handlers so cancelled tabs cannot flip back to
done/error on late frames), EXEC-6 (`<SqlEditor />` mounted under
`{#key routeState.params.id}` so connection switch re-inits CM dialect,
classifier engine, and schemaCache binding), EXEC-7 (row append mutates
`tab.rows` in place + batched flush + virtualized ResultGrid adopted
from PR #12), INT-2 (`schemaCache.invalidate()` called on DDL exec end
so autocomplete stops serving stale schemas), SEC-1 (saved queries
keyed on `(userID, connID)` with regression test before any persistent
store lands), a11y-04 (result tablist now implements the WAI-ARIA tabs
contract end-to-end: ArrowLeft/Right/Home/End, roving tabindex,
aria-controls/aria-labelledby, panel role + Cmd+W scope guard),
a11y-05 (ResultGrid mirrors TableScreen's `role=grid` /
`aria-rowcount` / `aria-colcount` / `role=row` / `role=gridcell` so the
extraction doesn't regress AT semantics), a11y-12 (SqlEditor lazy-loaded
behind `routeState.name === 'query'` so the ~140 KB gz CodeMirror tax
no longer hits the 11 non-editor routes; `bundleBudget.test.js` ceiling
tightened with a separate `sqlEditor-*` chunk assertion). The remaining
25 findings are deferred below.

### Deferred medium findings — PR #13.5

- **EXEC-8** — Mid-stream error clears rows-collected-so-far from view;
  reason: inconsistent UX (rows shown on cancel, hidden on error) but
  no data loss — rows remain in `activeTab.rows`. Pick one rule and
  document. **Target:** PR #13.5.
- **EXEC-9** — Save Query uses `window.prompt` — no description, no
  duplicate handling; reason: functional save works, rough UX; tied to
  SEC-1 / INT-5 (saved-query overhaul). Overlaps with a11y-10 (Modal
  pattern reuse). **Target:** PR #13.5.
- **EXEC-10** — Loading history/saved REPLACES current buffer without
  dirty-check; reason: undo stack preserved via CM6 so recovery exists;
  dirty-flag + confirm dialog is good polish but not blocking.
  **Target:** PR #13.5.
- **EXEC-11** — Errored executions don't refresh history; reason: audit
  captures errors server-side, sidebar lags until next success — trivial
  `finalize()` refactor. **Target:** PR #13.5.
- **a11y-01** — CodeMirror editor has no `aria-label`; reason: important
  SR polish but not blocking — one-line `EditorView.contentAttributes`
  facet. **Target:** PR #13.5.
- **a11y-02** — Execute button uses native `disabled` with no reason
  exposed; reason: SR users cannot hear why button is disabled — needs
  `Btn.svelte` refactor to support `aria-disabled` + tooltip pattern.
  **Target:** PR #11.5 (converges with shared Btn pattern work).
- **a11y-03** — Cancel button has no `aria-keyshortcuts`; Cmd+. is
  undiscoverable; reason: discoverability polish — mirror Execute's
  kbd hint. **Target:** PR #13.5.
- **a11y-06** — `ErrorPanel` renders without `role=alert`; reason: SR
  regression vs PR #11 toast standard, but visible error UI works for
  sighted users — route through `pushToast()` bus. **Target:** PR #13.5.
- **a11y-07** — Sidebar accordions don't collapse, no `aria-expanded`;
  reason: static sections styled like accordions — design intent unmet
  but no functional break. **Target:** PR #13.5.
- **a11y-08** — Saved-query items have no keyboard-delete affordance;
  reason: asymmetric save/delete UX; `deleteSaved` API exists but no UI
  calls it. **Target:** PR #13.5 (with saved-query overhaul, SEC-1 /
  INT-5).
- **INT-3** — Bundle test ceiling gives only 15% headroom; ADR stale;
  reason: process/docs hygiene — tighten ceiling and update ADR in a
  docs PR. Not a functional blocker. **Target:** PR #13.5.
- **INT-5** — Saved queries UI gives no signal storage is in-memory
  only; reason: operators may lose work on daemon restart — add
  "session-only" caption now or gate Save behind a flag; tied to the
  saved-query overhaul (SEC-1). **Target:** PR #13.5.
- **INT-6** — Format button has no preload-on-hover; reason: ~76 KB gz
  fetched on first click, subsequent free — polish via `onmouseenter`
  handler. **Target:** PR #13.5.

### Deferred low findings — PR #13.5

- **SEC-2** — `/sql/classify` accepts any authenticated user — audit-log
  griefing surface; reason: bounded — canonical server-side re-classify
  in `handleQuery` is the real gate; classifier fingerprint is
  open-source. Only real risk is audit-log flooding by low-priv
  sessions. Drop the audit event on the UX-only route. **Target:**
  PR #13.5.
- **EXEC-12** — Format cursor preservation is byte-offset, not semantic;
  reason: polish; minor disorientation on large reformats. **Target:**
  PR #13.5.
- **EXEC-13** — Empty-doc Save silently no-ops; reason: trivial
  papercut; one-line `statusMsg` fix. **Target:** PR #13.5.
- **a11y-09** — Format button does not signal busy state via
  `aria-busy`; reason: `Btn.svelte` loading spinner exists; `aria-busy`
  is polish. **Target:** PR #11.5 (a11y polish pass).
- **a11y-10** — Save flow uses `window.prompt` bypassing Modal pattern;
  reason: overlaps with EXEC-9. **Target:** PR #13.5 (with saved-query
  Modal overhaul).
- **a11y-11** — Escape from result grid doesn't return focus to editor;
  reason: polish — Shift-Tab works. **Target:** PR #11.5 (a11y polish
  pass).
- **a11y-13** — `ClassifierChip` FORBIDDEN vs DANGEROUS share red;
  reason: semantic collision but labels differentiate — add lock glyph
  in polish PR. **Target:** PR #11.5 (a11y palette pass).
- **a11y-14** — Streaming progress `aria-live` region reused for
  transient text; reason: polite region works for completion summaries;
  splitting status vs error lives is polish; overlaps with a11y-06.
  **Target:** PR #13.5.
- **a11y-15** — IBM Plex Mono loads render-blocking from Google Fonts;
  reason: FOUT on slow networks; trivial swap to `@fontsource`.
  **Target:** PR #11.5 (converges with dc-13 / FONTS-NO-SRI-THIRD-PARTY).
- **INT-7** — `@codemirror/search` included for invisible feature;
  reason: ~12-15 KB for Ctrl+F panel not in keyboard help. Decide
  keep+document or drop. **Target:** PR #13.5.
- **INT-8** — Tab `klass` captured as `'unknown'` before classifier
  debounce returns; reason: cosmetic per-tab label issue; server-side
  class is correct on the wire — add `classifier.flush()` in polish PR.
  **Target:** PR #13.5.

### Deferred nit findings — PR #13.5

- **SEC-3** — Comment-only statements submitted as exec frames; reason:
  not a security issue — splitter correctly does not mis-emit DROP;
  server-side classifier and driver handle no-op gracefully. UX
  papercut: empty result tab opens for nothing. **Target:** PR #13.5.

### Resolved in PR #13.5

Frontend-only follow-up landed in PR #13.5 (no version bump). Scope:
all deferred items above NOT retargeted to PR #11.5 (a11y/font polish).
Resolutions:

- **EXEC-8** — Mid-stream error preserves already-collected rows (status
  line keeps showing "executing…" partial state; the error path no
  longer wipes the row buffer — partial rows remain in
  `activeTab.rows`, consistent with the cancel path).
- **EXEC-9** — `window.prompt` swapped for `SaveQueryModal.svelte`
  (focus-trapped, supports name + description + tags + duplicate
  detection with a "Replace" affordance).
- **EXEC-10** — `loadIntoEditor` is now dirty-checked: a non-empty
  divergent buffer routes through a `ConfirmDialog` ("Replace editor
  buffer?") before clobber. Replay path (`_loadIntoEditorRaw`) is
  internal and also dirty-checked at the call site.
- **EXEC-11** — `runOne` collapses success + error paths through a
  single `finalize()` callback so the history sidebar refreshes on
  BOTH outcomes (previously the error branch skipped refresh).
- **EXEC-12** — `replaceDoc(view, next, { preserveCursor: 'semantic' })`
  added; Format calls it so the caret lands on the same non-whitespace
  token after reformat instead of being byte-clamped.
- **EXEC-13** — Empty-doc Save no longer silently no-ops: a warning
  toast fires ("Nothing to save — editor is empty") and the error
  live region echoes it.
- **a11y-01** — CM6 contenteditable now carries `aria-label`,
  `aria-multiline`, `role=textbox`, and `aria-keyshortcuts` via
  `EditorView.contentAttributes.of(...)`.
- **a11y-03** — Cancel button advertises `aria-keyshortcuts="Meta+Period"`
  and renders the ⌘. kbd hint.
- **a11y-06** — `ErrorPanel` now sets `role=alert` + `aria-live=assertive`
  AND pushes each new (code+message) tuple through `pushToast` so AT
  users get an interrupt regardless of focus location.
- **a11y-07** — Sidebar accordions (History, Saved) wrap their `<h3>`
  text in a `<button aria-expanded aria-controls>` and toggle the
  panel body. Caret glyph reflects state.
- **a11y-08** — Saved-query items expose a visible `×` button + a
  `Delete`/`Backspace` keyboard shortcut on the row that opens a
  `ConfirmDialog` and calls `api.deleteSaved`.
- **a11y-10** — Save flow now lives in `SaveQueryModal.svelte` (reuses
  the `<Modal>` focus-trap component). Only the Modal-pattern-reuse
  half of a11y-10 lands here; the broader Btn/Modal a11y polish stays
  with PR #11.5.
- **a11y-13 (palette half)** — `ClassifierChip` renders a 🔒 lock glyph
  + an SR-only "lock " token in front of the FORBIDDEN label so the
  colour collision with DANGEROUS no longer reduces both to "red
  pill". Both pills also carry distinct `title` tooltips.
- **a11y-14 (split half)** — Status + error live regions split into
  two DOM nodes: polite `role=status` for transient progress, and
  assertive `role=alert` for errors. Errors no longer race status
  updates in the same live region.
- **INT-3** — Bundle ceilings tightened: main ≤ 95 KB gz
  (was 110 KB), editor chunk ≤ 175 KB gz (was 200 KB). Empirical
  landing at PR #13.5 is 56.96 KB gz / 149.80 KB gz, ~6-15% headroom.
- **INT-5** — Saved sidebar shows a "Session-only — not persisted
  across panel restarts" caption above the list; `SaveQueryModal`
  echoes the caveat next to the action.
- **INT-6** — Format button hover/focus preloads
  `../lib/sqlEditor/sqlFormatter.js`; first click no longer pays the
  ~76 KB gz fetch latency.
- **INT-7** — Decision: KEEP `@codemirror/search`. The ~12-15 KB gz
  buys Ctrl+F find-in-buffer which is table-stakes in SQL workbenches
  and is now advertised via the editor's `aria-keyshortcuts`. The
  cost is absorbed by the tightened editor chunk ceiling.
- **INT-8** — `await classifier?.flush?.()` is called at the head of
  both `execCurrent` and `execAll` so the per-tab `klass` label and
  the forbidden gate are classifier-truth instead of a stale
  `'unknown'` from the 250ms debounce.
- **SEC-2 (frontend-side)** — `createClassifierStore.run()` now
  dedupes identical SQL against `state.lastSql` (UX-cache — the
  canonical server re-classify still runs at exec time, so the gate
  cannot widen) and applies a 60-call rolling-minute rate ceiling per
  store. A runaway editor loop can no longer flood `/sql/classify`.
  The corresponding server-side audit-event drop is a panel handler
  change (`auracpd`), not web-aura-db; tracked separately.
- **SEC-3** — `splitStatements` now filters comment-only statements
  via a new `isCommentOnly(text)` helper (strips `--` / `/* */`
  spans and checks for residual non-whitespace). `-- foo;` no longer
  spawns a phantom empty result tab.

---

## Source: PR #14 adversarial review (workflow run wf_a73c150e-d26)

The 4-lens review of the EXPLAIN inspector (`web-aura-db/`
ExplainInspectorScreen + FlameTree/LeftTree, NodeDetail panel,
MetricsRibbon, AnalyzeToggle confirm-gate, RawPlanView, WarningBanner,
plan-store + cost/step classification helpers, editor ⇄ inspector
sessionStorage handoff) produced 48 findings (1 critical, 8 high, 18
medium, 17 low, 2 nit) across CORRECTNESS, A11Y, DESIGN COHERENCE, and
INTEGRATION lenses. After dedupe + triage: 10 must-fix items landed in
PR #14 itself — A11Y-1 (critical: AnalyzeToggle typed-confirm input
moved INTO the ConfirmDialog body so the destructive-statement gate is
reachable inside the focus trap; Confirm now `disabled` until the
operator types `ANALYZE`, with inline case-mismatch error instead of a
silent return), A11Y-2 (dual-control bypass removed — single
`role=switch` button owns the toggle; Space on the visual control now
routes through `onToggle` and the confirm gate, killing the silent
analyze-prop desync), CORR-1 (MetricsRibbon renders em-dash for
`executionTimeMs===0` when `analyzed=false` and for MariaDB
`planningTimeMs===0` so the documented "not measured ≠ zero" wire
contract holds end-to-end; `fmtMsOrDash(v, { zeroIsMissing: true })`
added to `explainFormat.js`), CORR-2 (collapsed-parent selection
orphan: `toggleExpand` now walks `selectedId` upward to the nearest
visible ancestor on collapse so NodeDetail and aria-activedescendant
stay in sync with the visible tree), CORR-3 (PG `loops=0`
never-executed branches render `rowsActual` / `timeTotalMs` / buffers as
em-dash with a "not executed" tag and are excluded from
share-of-total denominators — server-side `executed bool` carried on
Metrics), A11Y-3 (FlameTree drops the wrapper `<g id=flame-node-${id}>`
and passes the id to FlameNodeBar's root `<g role=treeitem>` so
aria-activedescendant resolves to the labeled treeitem, fixing NVDA /
JAWS / VoiceOver row announcements), A11Y-4 (light-theme per-step fg
overrides extended to cover `.flame-row__relation`, `__index`,
`__tail`, and `__pct` — not just `__kind` — so the cost-pct chip the
design relies on as the WCAG-1.4.1 non-color signal stays legible on
step-4/5 bars), A11Y-6 (ArrowLeft handler restructured to match the
WAI-ARIA tree pattern: collapse-if-expanded, else select parent —
collapsed parents no longer re-expand on ArrowLeft), INT-1 (Inspector
validates `sessionStorage['explain:pending'].connId` against
`routeState.params.id` on mount and treats a mismatch as "no pending"
— closes the cross-connection statement-leak path where conn-A's
statement could run against conn-B's database), INT-2
(`<ExplainInspectorComp />` wrapped under `{#key routeState.params.id}`
in App.svelte, mirroring the SqlEditor pattern from PR #13, so
A→B navigation force-remounts and the breadcrumb can no longer
mis-attribute a stale plan). The remaining 38 findings are deferred
below.

### Deferred high findings — PR #14.5

- **DC-1** — Color ramp: copper bleeds across steps 3-5; reason: design
  coherence — the ordinal ramp under-sells the hottest step but does
  not cause data misinterpretation; numeric pct chip remains the
  load-bearing signal. Tune alongside the log-bucket change in
  CORR-8. **Target:** PR #14.5.

### Deferred medium findings — PR #14.5

- **CORR-7** — `WarningBanner` does not distinguish critical from
  informational warnings; reason: UI polish — server warnings flow
  through without a severity field, so the MariaDB no-ANALYZE caveat
  renders identically to a routine estimate-mismatch note. Cosmetic
  severity-styling concern; does not corrupt data. Wire-contract
  change needed (severity field). **Target:** PR #14.5 (pair with
  A11Y-12).
- **CORR-8** — `costStep` buckets are linear — collapse to ~2 colors
  in real plans; reason: design tuning of the color ramp. Tree still
  functions; numeric pct chip remains the load-bearing signal. Pairs
  with DC-1. **Target:** PR #14.5.
- **CORR-9** — `AnalyzeToggle.currentClass` hydrated once and never
  re-classified; reason: server still enforces the security gate, so
  this is UX-only. Worst case is a 422 from the server on a
  deeplinked-then-toggled flow. **Target:** PR #14.5.
- **DC-2** — Flame `ROW_H=22` breaks the 24px tree grid; reason:
  visual cadence drift between LeftTree (24px) and FlameTree (22px).
  Token cleanup. **Target:** PR #14.5.
- **DC-3** — `HotspotChip` `--estimate` vs `--loops` modifiers have no
  CSS; reason: two hotspot kinds render identically — operator can
  read the chip label; pre-attentive distinction is a polish
  enhancement. **Target:** PR #14.5 (pair with A11Y-14).
- **DC-5** — Initial `onMount` fetch shows blank screen — no Spinner
  branch; reason: loading UX — first paint on deeplink is blank for up
  to 60s. Real gap but isolated; pair with the timeout-UX work in
  INT-7. **Target:** PR #14.5.
- **A11Y-5** — `NodeDetail` region `aria-label` is static instead of
  `aria-labelledby` to selected kind; reason: landmark-rotor
  announcement polish; `aria-live` atomic already announces selection.
  **Target:** PR #11.5 (a11y polish pass).
- **A11Y-7** — Body grid has no narrow-viewport stack; reason:
  phone/tablet usability gap; inspector is desktop-targeted today.
  Pair with DC-9. **Target:** PR #11.5 (responsive a11y pass).
- **A11Y-8** — Truncated bar text has no `title=` for hover reveal;
  reason: long table-name UX gap; detail panel already shows full
  text. **Target:** PR #11.5 (a11y polish pass).
- **A11Y-9** — `MetricsRibbon` warnings popover claims `role=dialog`
  but lacks trap / Esc / focus restore; reason: either downgrade to
  disclosure or wrap in Modal. Polish. **Target:** PR #11.5 (Modal
  pattern reuse).
- **A11Y-10** — `Cmd+E` is CM6-scoped — no `aria-keyshortcuts`,
  fallthrough outside editor; reason: discoverability +
  browser-default conflict (Firefox/Edge address bar). Pair with
  INT-10. **Target:** PR #14.5.
- **A11Y-11** — `ExplainInspector` has no `<main>` landmark or skip
  link; reason: landmark polish — carve into the broader landmark
  pass for the SPA. **Target:** PR #11.5 (a11y landmark pass).
- **A11Y-12** — `WarningBanner` `role=status` for all warnings —
  critical warnings may not be announced; reason: wire-contract change
  needed (severity field) or one-shot alert escalation. Pair with
  CORR-7. **Target:** PR #14.5.
- **INT-3** — `explain:return` `sessionStorage` write is dead code;
  reason: either remove the write or implement the editor-side read.
  Polish; current Open-in-Editor button still navigates. **Target:**
  PR #14.5.
- **INT-5** — `document.title` not updated on inspector mount; reason:
  multi-tab UX gap; broader SPA-wide pass. **Target:** PR #14.5.
- **INT-6** — Browser-back from inspector loses editor `docText`;
  reason: UX regression for edit-explain-tweak loops; significant but
  tracked separately. Solving INT-3 (`explain:return`) gets most of
  the benefit. **Target:** PR #14.5.
- **INT-7** — No abort/timeout indicator on 60s `api.explain`; reason:
  loading UX gap; pair with DC-5 (no Spinner branch on initial fetch).
  **Target:** PR #14.5.
- **INT-8** — Bare `h` / `r` shortcuts collide with typing & lack
  discoverability; reason: add modifier or scope to focus; not
  data-corrupting. **Target:** PR #14.5.

### Deferred low findings — PR #14.5

- **CORR-10** — `fmtRows` boundary at 10,000 vs convention 1,000;
  reason: cosmetic — document or flip; not blocking. **Target:**
  PR #14.5.
- **CORR-11** — `RawPlanView` renders 1MB+ in a single `<pre>`;
  reason: large-plan polish — lazy-mounted already; freeze only on
  tab open. **Target:** PR #14.5.
- **CORR-12** — No inline "tree truncated" marker; reason: banner
  already mentions truncation; inline marker is enhancement.
  **Target:** PR #14.5.
- **CORR-13** — Search input does not highlight matches inline;
  reason: match-or-dim already works; `<mark>` highlight is
  enhancement. Also blocked by A11Y-13 (no search UI exists yet).
  **Target:** PR #14.5.
- **DC-6** — Unicode chevrons / copy glyph diverge from SVG icon
  family; reason: cross-platform glyph rendering inconsistency.
  Polish. **Target:** PR #14.5.
- **DC-7** — `WarningBanner` dismiss model is per-instance, not
  per-session; reason: behavioral ambiguity — pick one model; not
  blocking. **Target:** PR #14.5.
- **DC-8** — `RAW` is a toggle button, not a TabBar; reason:
  convention divergence with DataGrip / pgMustard. Affordance polish.
  **Target:** PR #14.5.
- **DC-9** — Detail panel fixed 360px — no resize / responsive
  collapse; reason: width-token cleanup and ResizeHandle reuse. Also
  partially overlapping with A11Y-7's narrow-viewport need.
  **Target:** PR #14.5.
- **DC-10** — Hot? column mostly empty in NodeDetail metrics table;
  reason: density polish. **Target:** PR #14.5.
- **A11Y-13** — No `/` search shortcut and no search input UI; reason:
  search infra is wired in the store but no UI exists. Either build
  the UI or guard the prop. Follow-up feature. **Target:** PR #14.5.
- **A11Y-14** — `HotspotChip` relies on `title=` and terse labels;
  reason: label-quality polish; pair with DC-3. **Target:** PR #11.5
  (a11y polish pass).
- **A11Y-15** — Dead `onkeydown` on a `role=presentation` div in
  `FlameNodeBar`; reason: lint / cleanup — selection-by-keyboard is
  already owned by the tree handler. **Target:** PR #11.5 (a11y
  cleanup pass).
- **A11Y-16** — Enter on a tree leaf is a no-op; reason: minor parity
  with click model — selection already happens on Arrow. **Target:**
  PR #11.5 (a11y polish pass).
- **INT-4** — `fromHash` field in `explain:pending` is unused; reason:
  dead payload field — delete or honor in `onClose`. **Target:**
  PR #14.5.
- **INT-9** — No integration tests for SqlEditor → Inspector handoff;
  reason: test-debt — locks in fixes for INT-1 / INT-2 once those are
  addressed. **Target:** PR #14.5.
- **INT-10** — `Cmd+E` global fallthrough undocumented; reason:
  tooltip / scope clarification; pair with A11Y-10. **Target:**
  PR #14.5.
- **INT-11** — `RawPlanView` double-decode fallback hides wire-shape
  mismatch; reason: dead code paths + misleading comment; confirm
  with backend and simplify. **Target:** PR #14.5.

### Deferred nit findings — PR #14.5

- **CORR-14** — `fmtMs` accepts negatives; `fmtCost` vs `fmtRows`
  separator asymmetry; reason: cosmetic. **Target:** PR #14.5.
- **DC-11** — Engine pill style duplicated in two CSS rules; reason:
  drift risk — extract to a shared class or use `EngineGlyph`.
  **Target:** PR #14.5.

---

## Source: PR #15 adversarial review (workflow run wf_c284e2fc-e26)

The 4-lens review of the Command Palette + History sidebar overhaul
(`web-aura-db/` CommandPalette + palette.svelte.js fuzzy matcher /
registry / handoff, HistoryScreen role=table grid, StarButton optimistic
rollback, cross-connection history fanout, sessionStorage replay handoff
into SqlEditor, Cmd-K / Cmd-Shift-K / `/` actions-only / Cmd-Backspace
keybindings) produced 41 findings (0 critical, 6 high, 12 medium, 14
low, 9 nit) across CORRECTNESS, A11Y, DESIGN COHERENCE, and INTEGRATION
lenses. After dedupe + triage: 4 must-fix items landed in PR #15 itself
— C1/INT-1 (palette replay handoff: same-tab same-connection replay no
longer silently drops; `palette.pendingReplay` $state slot watched by
SqlEditor's $effect so the flagship "click Recent to replay" works when
SqlEditor is already mounted on the target connection; sessionStorage
retained only for cross-tab inheritance; `consumePending(id)` also fired
from the conn-switch $effect so same-tab cross-conn replay is covered),
C2 (groupBySection score ordering: when `palette.query` is non-empty
the palette renders `cmds` as a flat score-sorted list so Enter and the
cursor land on the user's best match instead of the first item in a
fixed ORDER bucket — section grouping retained only for the empty-query
browse view), A11Y-1 (palette dialog focus trap: onKeydown now
intercepts Tab/Shift-Tab and preventDefault keeps focus on the search
input so keyboard-only and SR users can no longer silently activate
page-behind topbar/tree controls while the modal visually occludes
them; matches the existing Esc + focus-restore-on-close plumbing),
A11Y-3 (HistoryScreen ARIA grid/table: dropped the role=table + bare
spans + tabindex=0-row hybrid in favor of a native
`<table><thead><tbody>` with an explicit per-row "Replay" button —
removes the dual tab-stop problem, fixes the spec-invalid no-role=cell
markup, and the star button's onkeydown now stopPropagation matches
its onclick). The remaining 37 findings are deferred below.

### Deferred high findings — PR #15.5

- **A11Y-2** — Dialog has no `aria-describedby`; footer kbd hints are
  `aria-hidden`; reason: high a11y severity but single-lens (A11Y only)
  — combobox/listbox core semantics are correct, so AT users navigate
  but get no instruction text. Either drop `aria-hidden` on the hint
  row and wire `aria-describedby` to it, or add an SR-only description
  paragraph. **Target:** PR #11.5 (a11y polish pass).

### Deferred medium findings — PR #15.5

- **C3** — Cmd+Enter (new tab) leaves `editor:pending` set in the
  source tab; reason: medium-severity edge case in the newTab replay
  flow — only causes data loss if the user returns to `/query` in the
  same tab within the 30s TTL. Single-lens CORRECTNESS finding.
  **Target:** PR #15.5 (clear sessionStorage in the source tab after
  the newTab handoff resolves).
- **C4/INT-2** — Cross-connection history fanout silently drops
  connections beyond cap 25; reason: reinforced across CORRECTNESS and
  INTEGRATION lenses but medium severity — affects power users with
  >25 connections only, and the failure is silent data
  incompleteness (not corruption). Add a UI banner / "showing first
  25 connections" affordance. **Target:** PR #15.5.
- **C5** — `Cmd-K` toggles palette while the user types in any input;
  reason: documented convention from Linear / Raycast; benign today
  because no current input collides with the chord, but flagged as a
  forward-compat concern. **Target:** PR #15.5 (scope-aware keymap or
  accept as documented convention).
- **A11Y-4** — Star button missing `aria-pressed`; reason: single-lens
  medium a11y polish — state is still announced via the `aria-label`
  flip ("Star" ↔ "Starred"), but `aria-pressed` is the canonical
  toggle pattern. **Target:** PR #11.5 (a11y polish pass).
- **A11Y-5** — Date-range segmented buttons missing `aria-pressed`;
  reason: single-lens medium a11y polish — visual selected state is
  present but not exposed in the AX tree. **Target:** PR #11.5 (a11y
  polish pass).
- **A11Y-6** — Result-count `aria-live` is unthrottled and not
  pluralised; reason: SR verbosity issue ("1 results", spam on every
  keystroke), not blocking. Throttle the live-region update to
  ~250ms and pluralise the copy. **Target:** PR #11.5 (a11y polish
  pass).
- **A11Y-7** — `Cmd+Shift+K` tree-filter shortcut missing
  `aria-keyshortcuts` / visible hint; reason: discoverability medium —
  works for users who know the chord, invisible to everyone else.
  **Target:** PR #15.5.
- **A11Y-8** — `/` actions-only mode has no visible or accessible
  filter indication; reason: mode indicator missing — functionality
  works (registry filtered to Actions section), but neither the
  visual chrome nor an `aria-live` region tells the user the palette
  is in actions-only mode. **Target:** PR #15.5.
- **A11Y-9** — History row + nested star button create dual tab stops;
  reason: medium severity but the role=table + native Replay button
  refactor in A11Y-3 already removes the row-level tab stop; this
  entry exists as a regression guard for the per-row star control.
  **Target:** PR #11.5 (a11y polish pass).
- **INT-4** — Cross-connection fanout surfaces 403 / permission errors
  as empty data; reason: partial-failure signal missing — a connection
  the user has lost access to silently drops out of the merged
  results. Add a per-connection error badge in the palette footer or
  a toast on first denial. **Target:** PR #15.5.
- **D-2** — Star icon: filled vs unfilled use the same glyph,
  color-only differentiation; reason: design polish — would be
  reinforced by A11Y on the color-only signal lens, but A11Y-4 flagged
  `aria-pressed` not the glyph variant. Swap to outline vs filled
  glyph. **Target:** PR #15.5.
- **D-3** — Hard-coded star + error colors bypass the accent token
  system; reason: token discipline issue surfaced by the design lens —
  no functional impact, but drifts from the rest of the SPA's accent
  palette. **Target:** PR #15.5.
- **D-4** — Palette has no loading state during `primeHistoryCache`
  fanout; reason: medium polish — first open of the palette shows a
  brief empty state before the fanout resolves; subsequent opens use
  cached data so the issue is open-once. **Target:** PR #15.5.
- **D-5** — Saved-queries preview shows raw multi-line SQL without
  normalisation; reason: medium polish — multi-statement saves
  collapse the palette row into a tall block. One-line `.replace(/
  \s+/g, ' ').trim()` normaliser. **Target:** PR #15.5.

### Deferred low findings — PR #15.5

- **C6** — Optimistic star rollback matches by `entry.id` collapses
  undefined IDs; reason: low severity defensive coding — unlikely in
  practice (the server should always assign an ID before the optimistic
  flip), but a future code path that calls star() before persistence
  would alias every undefined-id row together. **Target:** PR #15.5.
- **C7/D-1** — Connection commands duplicate the engine name in both
  subtitle and hint; reason: reinforced across CORRECTNESS and DESIGN
  lenses but visual polish only — not a correctness bug. Pick one
  position for the engine label. **Target:** PR #15.5.
- **A11Y-10** — Listbox section headers as non-option siblings of
  `role=option`; reason: listbox owns-children rule edge case — the
  ARIA spec requires `role=listbox` children to be `role=option` /
  `role=group`. Either wrap each section in `role=group` with
  `aria-label` from the header, or move headers out of the listbox.
  **Target:** PR #11.5 (a11y polish pass).
- **A11Y-11/D-11** — Palette row titles truncate without a `title=`
  attribute; reason: reinforced across A11Y and DESIGN lenses but low
  severity polish — long connection / history titles ellipsize with
  no hover-reveal. **Target:** PR #11.5 (a11y polish pass).
- **A11Y-12** — Palette input lacks an explicit accessible label;
  reason: combobox inherits its name from the dialog title today,
  which is marginal AT impact but spec-fragile. Add an explicit
  `aria-label="Search commands"` to the input. **Target:** PR #11.5
  (a11y polish pass).
- **A11Y-13** — History row Enter-to-replay is undocumented for AT;
  reason: SR discoverability — sighted users see the row hint, SR
  users have no announcement that Enter replays. **Target:** PR #11.5
  (a11y polish pass).
- **INT-6** — Rapid Cmd+Enter opens multiple tabs reading the
  last-written `sessionStorage`; reason: rapid-fire fanout edge case
  where N quick newTab handoffs all observe the most recent pending
  entry rather than their own. Bounded by user input speed. **Target:**
  PR #15.5.
- **INT-7** — `loadIntoEditor` clobbers an unsaved buffer with no
  confirmation; reason: pre-existing pattern in the codebase (the
  sidebar already does this); PR #15 widens the surface (palette
  replay now triggers it from more places) but does not introduce the
  destructive default. Pair with EXEC-10's dirty-check follow-up.
  **Target:** PR #13.5 (dirty-buffer confirmation overhaul).
- **INT-8** — `primeHistoryCache` TOCTOU on rapid connection switches;
  reason: brief stale-data window between switch and cache refill —
  replay correctness is unaffected because the connId is validated
  before navigation. **Target:** PR #15.5.
- **D-6** — Palette selected-row `border-left: 3px` vs tree `2px`;
  reason: single-lens low polish — selection accent thickness drifts
  between the two surfaces. **Target:** PR #15.5.
- **D-7** — HistoryScreen filter row reflows at narrow viewport;
  reason: responsive polish — date-range buttons + connection select
  wrap awkwardly below ~720px. Overlaps with the broader narrow-
  viewport gap. **Target:** PR #11.5 (responsive a11y pass).
- **D-8** — Palette result-row icon column is oversized for the
  13-14px glyphs it holds; reason: single-lens low polish — extra
  horizontal whitespace per row. **Target:** PR #15.5.
- **D-9** — Date-range control style is ambiguous (between pill and
  tab); reason: single-lens low design coherence — visual convention
  doesn't match either pattern fully. **Target:** PR #15.5.
- **D-10** — Palette doesn't show its own open shortcut (`Cmd+K`) in
  the footer; reason: low discoverability — every other shortcut
  appears in the hint row except the one that opened the palette.
  **Target:** PR #15.5.

### Deferred nit findings — PR #15.5

- **A11Y-14** — Palette input missing `inputmode` / `autocapitalize`
  for mobile; reason: mobile UX nit — web-aura-db is desktop-targeted
  today. **Target:** PR #11.5 (mobile a11y pass).
- **A11Y-15** — Empty-state in the palette uses non-live divs;
  reason: minor SR consistency nit — the empty state appears
  synchronously but isn't announced when the query string changes
  the result count to zero. **Target:** PR #11.5 (a11y polish pass).
- **INT-9** — CommandPalette is bundled into the main chunk vs the
  PR notes' claim that it's lazy-loaded; reason: PR description
  accuracy nit — no runtime impact, but the bundle budget assertion
  should reflect reality. **Target:** PR #15.5.
- **D-12** — Empty-state copy doesn't suggest clearing the filter;
  reason: copy nit — "No results" without an actionable next step.
  **Target:** PR #15.5.
- **D-13** — Section header padding asymmetric (10/4); reason:
  spacing nit. **Target:** PR #15.5.
- **D-14** — Backdrop blur 8px is GPU-heavy; reason: performance nit —
  visual cost is similar at 4px on the systems where blur is the
  expensive operation. **Target:** PR #15.5.
- **C8** — Refutation: cross-tab `sessionStorage` isolation; reason:
  explicit refutation entry, no action required. The pending-replay
  handoff is correctly scoped to the current tab. **Target:** none.
- **C9** — Refutation: `fuzzy.match` empty-query path; reason:
  explicit refutation entry, no action required. The matcher
  correctly returns the full registry (not an empty slice) for an
  empty query. **Target:** none.

---

## Source: PR #16 adversarial review (workflow run wf_0d867403-4ed)

The 4-lens review of the table-data export layer
(`pkg/dbadmin/export/` CSV / NDJSON / SQL encoders + handler,
`internal/api/dbadmin/handlers_export.go` streaming pipeline +
per-user export semaphore + 1 GiB / 1M-row caps + audit START /
FINISH emit, `web-aura-db/` ExportModal + api.exportTable +
TableScreen wiring, BUILD-PLAN §PR #16 acceptance criteria) produced
46 findings (0 critical, 9 high, 16 medium, 13 low, 8 nit) across
SECURITY, CORRECTNESS, INTEGRATION_UX, and DESIGN_COHERENCE lenses.
After dedupe + triage: 9 must-fix items landed in PR #16 itself —
SEC-1 (CSV formula-injection prefix-quote in `csvCell` for cells
starting with `=`/`+`/`-`/`@`/tab/CR per the OWASP cheatsheet),
SEC-2 (denial-path audit emission: `suppressAudit` moved AFTER
validation + authz so identifier-fuzzing / 403-probe paths leave an
`export-denied` trace per SECURITY.md §9.1), C1 (encoder Flush()
exposed on csv / ndjson / sql + called before the cw.BytesWritten()
probe AND before flushPair's http.Flusher; dead `_ = enc.Close`
method-value removed — bytes now actually reach the wire mid-stream
and the 1 GiB cap reads accurate counts), C2 (CSV truncation marker
no longer a `# truncated…` row that breaks RFC 4180 parsers; surfaced
via `X-Aura-Export-Truncated` response header + audit outcome flag
instead; SQL `-- truncated…` and NDJSON `{"$truncated":true}` retained
because they're valid in those formats), C3 (audit outcome event now
carries `truncated bool` — operators can distinguish a 1M-row
runaway-truncation from a 1M-row clean completion), C4 (audit FINISH
now uses `context.WithoutCancel(r.Context())` + 5s deadline so SQLite
/ http audit sinks don't drop the record on timeout-during-emit; START
emit gets the same treatment), ux-1 (ExportModal AbortController
wired into api.exportTable's existing signal opt; Escape / backdrop
on the inner Modal now triggers aborter.abort() and an AbortError
surfaces as `cancelled` rather than a hard error; "Close" relabels
to "Cancel" while busy so the per-user semaphore is released
promptly), ux-2 (SqlEditor result-panel export button reusing
ExportModal with the editor's resolved schema/table/columns — closes
the BUILD-PLAN §PR #16 acceptance gap for "export from any grid or
query result"), ux-3 (mid-stream server errors now emit a per-format
error trailer line — CSV header-suppressed sentinel row, NDJSON
`{"$error":"<code>"}`, SQL `-- ERROR: <code>` — AND set the
`X-Aura-Export-Error` HTTP trailer declared via `w.Header().Add(
"Trailer", ...)` before WriteHeader; api.js scans the tail for the
sentinel and routes via toastBus, ending the "200 OK + half a CSV
displayed as success" silent-corruption window). The remaining 37
findings are deferred below.

### Deferred medium findings — PR #16.5

- **SEC-3 / ux-4** — Content-Disposition `filename*` is not RFC 5987
  percent-encoded; reason: medium-severity spec violation reinforced
  across SECURITY, INTEGRATION_UX, and DESIGN lenses (3-lens
  reinforcement) but no exploit path today because `SanitizeFilename`
  strips dangerous characters before the header is emitted. Bundle
  the three duplicate ids into one deferral. **Target:** PR #16.5
  (RFC 5987 `filename*=UTF-8''<pct-encoded>` per the cheatsheet).
- **SEC-4** — `exportLockManager.slots` map grows without bound;
  reason: medium-severity limits issue only material under churning
  user IDs (federated IdP rotating subjects); single-lens. Stable-ID
  deployments are unaffected. **Target:** PR #16.5 (TTL eviction or
  LRU cap on the slots map).
- **SEC-5** — Byte-cap enforced post-row → exports can exceed 1 GiB
  by one row + truncation marker; reason: medium-severity overshoot
  bounded by single-row width. Tightens automatically once C1 (flush
  no-op) is applied because `cw.BytesWritten()` then reads accurate
  counts. **Target:** PR #16.5 (doc + tighten the check to fire
  before the row write rather than after).
- **C5** — Start and outcome audit events lack a stable correlation
  ID; reason: medium forensic-linkage gap — operators must text-match
  on Statement to pair the START with its FINISH. Single-lens
  CORRECTNESS finding. **Target:** PR #16.5 (mint a per-export ULID
  at handler entry; include in both audit emit calls).
- **C6** — CSV `+Inf` / `-Inf` / `NaN` floats emit invalid cell text;
  reason: medium-severity format inconsistency — CSV consumers
  expect either a quoted string or an empty cell, not the literal
  `+Inf`. No data corruption for finite floats (the common case).
  Single-lens. **Target:** PR #16.5 (emit empty for NaN / `"Infinity"`
  string and document the convention).
- **C7** — NDJSON `$truncated` sentinel collides with a column named
  `$truncated`; reason: medium-severity edge case with very low
  likelihood (column names starting with `$` are rare and often
  disallowed). Single-lens CORRECTNESS finding. **Target:** PR #16.5
  (namespace as `__auracp_truncated` or move to a separate trailing
  metadata line).
- **C8** — Postgres SQL preamble is missing `standard_conforming_strings
  = on`; reason: medium-severity portability issue — modern Postgres
  defaults to `on` so the export typically replays correctly, but a
  legacy 9.0-era target would mis-interpret backslash escapes.
  Trivial one-line fix; single-lens. **Target:** PR #16.5.
- **ux-5** — 409 export-in-progress shown as raw error string, no
  `Retry-After` handling; reason: medium UX polish — functional but
  ugly. The per-user semaphore returns a clear error envelope but
  the modal renders the raw text. Single-lens. **Target:** PR #16.5
  (parse `Retry-After`, render a "another export is running" banner
  with a countdown).
- **ux-6** — Empty-result export silently produces a header-only
  file; reason: medium UX trust issue — not incorrect, but a user
  who filtered to zero rows gets a CSV with just a header line and
  no signal that the filter matched nothing. Single-lens. **Target:**
  PR #16.5 (pre-flight count probe + "0 rows match" confirmation).
- **ux-7** — Progress meter only shows bytes, no rows / ETA / cancel;
  reason: medium UX polish — mostly subsumed once ux-1 cancel wiring
  lands (the must-fix already added the cancel control); rows + ETA
  on top. Single-lens. **Target:** PR #16.5 (extend the streaming
  progress event with `rowsWritten` so the modal can render
  N rows / ~ETA).
- **ux-8** — ExportModal bundled into the main chunk instead of
  lazy-loaded; reason: medium perf regression — +3KB gzipped on the
  initial bundle. Single-lens, doesn't block correctness. **Target:**
  PR #16.5 (dynamic import + `await import()` in the toolbar handler).
- **DC-1** — Footer "Close" button is misleading after export
  completes; reason: medium UX polish — CTA stays "Start export"
  after success rather than collapsing into a "Done" / auto-close.
  Single-lens DESIGN finding. **Target:** PR #16.5.
- **DC-2** — Three menu items open the SAME modal — the format menu
  is decorative; reason: medium UX confusion — "Export as CSV" /
  "Export as JSON" / "Export as SQL" all open the same modal where
  format is selected again. Single-lens design call. **Target:**
  PR #16.5 (either pre-select the format from the menu, or collapse
  to one "Export…" entry).
- **DC-3** — Export trigger uses a Unicode caret inconsistent with
  the rest of the toolbar; reason: medium visual-consistency issue —
  the other toolbar dropdowns use the shared `Caret` SVG. Single-lens.
  **Target:** PR #16.5.
- **DC-4** — Filter / sort silently apply with no preview in the
  modal; reason: medium UX surprise — the modal inherits the grid's
  current filter + sort but doesn't show them, so users can't tell
  whether a filter is in effect. Single-lens design call, not a
  correctness gap. **Target:** PR #16.5 (render a "Includes filter:
  X" pill + a "clear filter" toggle).
- **DC-5** — No row-count estimate or 1M-cap signalling pre-flight;
  reason: medium UX gap — related to ux-7 (progress) but distinct in
  that this is about *before* the export starts. Single-lens design
  polish. **Target:** PR #16.5 (pre-flight COUNT(*) capped at 1M+1
  with a "1M+ rows, will truncate" warning).

### Deferred low findings — PR #16.5

- **SEC-6** — JSON columns ambiguously encoded as base64 in NDJSON;
  reason: low-severity data-integrity surprise, not a security
  defect — operators inspecting JSONB columns in an NDJSON export
  see base64 instead of the nested JSON. Single-lens. **Target:**
  PR #16.5 (passthrough JSON columns as nested objects when the
  driver returns them typed).
- **C10** — SQL trailer ordering with `-- end` followed by
  `-- truncated` is misleading; reason: low cosmetic — readers can
  still infer truncation from the markers. **Target:** PR #16.5
  (reverse the order so the truncation marker precedes `-- end`).
- **C11 / SEC-7** — Per-user lock is a no-op for empty `userID`;
  reason: low / defense-in-depth — reinforced across two lenses but
  both rated low / nit and no current exploit path because authn
  rejects empty IDs upstream. Bundle the duplicate ids. **Target:**
  PR #16.5 (panic or `return ErrInternal` on empty userID rather
  than silently bypassing the cap).
- **C12** — NDJSON trailing-newline guarantee on an empty result
  set; reason: low — most NDJSON consumers tolerate a zero-byte
  file. Doc-only clarification. **Target:** PR #16.5 (document the
  empty-result contract in `pkg/dbadmin/export/doc.go`).
- **ux-9** — Filename input lets the user submit names the server
  silently overrides; reason: low UX surprise — the client accepts
  arbitrary names but `SanitizeFilename` rewrites them server-side,
  so the downloaded file's name differs from what the user typed.
  **Target:** PR #16.5 (mirror the sanitiser client-side so the
  preview matches what ships).
- **ux-10** — Two timestamped exports in the same second produce
  identical filenames; reason: low edge case — second download
  overwrites the first in the browser's default download dir.
  **Target:** PR #16.5 (millisecond precision or a short random
  suffix when the second-resolution timestamp collides).
- **ux-11** — `onClose` prop wired but `TableScreen` does not pass
  one; reason: low — no functional bug today because the inner Modal
  has its own close path; will become a real bug if a future caller
  expects the prop to fire. **Target:** PR #16.5 (drop the unused
  prop or pass it through correctly).
- **DC-7** — Filename input accepts the wrong extension and submits
  as-is; reason: low UX surprise — typing `foo.txt` for a CSV
  export ships a `.txt` file with CSV content. **Target:** PR #16.5
  (auto-correct the extension to match the selected format).
- **DC-8** — Status region collapses three states (idle / running /
  done) into identical text; reason: low UX polish — existing
  `Spinner` / pill components are ready to reuse. **Target:**
  PR #16.5.
- **DC-9** — "Include header" CSV-only toggle framing is ambiguous;
  reason: low UX — the toggle only affects CSV but is rendered at
  the top of the modal regardless of selected format, suggesting it
  applies to JSON / SQL too. **Target:** PR #16.5 (show only when
  format=CSV).
- **DC-10** — No limit input in the UI despite API support; reason:
  low capability gap — power users who want a 1000-row preview
  export must hit the JSON API directly. **Target:** PR #16.5 (add
  a "Limit" number input).
- **DC-11** — Column-checkbox grid lacks search / virtualization for
  wide tables; reason: low — affects 100+ column tables only.
  **Target:** PR #16.5.

### Deferred nit findings — PR #16.5

- **SEC-8** — Refutation: Audit FINISH event uses cancelled
  `streamCtx`; reason: nit per the SECURITY lens but reinforced by
  CORRECTNESS C4 as high — see the C4 must-fix above. This
  duplicate id is deferred in favor of C4 and exists as a regression
  guard. **Target:** none (closed by C4).
- **SEC-9** — Refutation: CSV truncation marker is not RFC 4180
  valid; reason: nit per the SECURITY lens but reinforced by
  CORRECTNESS C2 as high — see the C2 must-fix above. Duplicate id
  deferred. **Target:** none (closed by C2).
- **C13** — `csv.go` dead-branch from removed `IncludeHeader`
  plumbing; reason: nit — misleading comment but no functional bug
  for current callers. **Target:** PR #16.5 (delete the branch +
  update the doc comment).
- **C14** — `countingWriter` is not thread-safe; reason: nit — latent
  only, the current handler is single-goroutine. No observable bug
  today, but a future fanout would race. **Target:** PR #16.5 (atomic
  counter or document the single-goroutine invariant).
- **ux-12** — BUILD-PLAN promised Markdown format; not shipped;
  reason: nit — doc drift between BUILD-PLAN §PR #16 and the
  implementation (CSV / NDJSON / SQL ship; Markdown does not).
  Deferral note in PR-TRACKER suffices. **Target:** PR #16.5
  (either add the Markdown encoder or strike from BUILD-PLAN).
- **ux-13** — Direct streaming chosen over the signed-URL handoff
  pattern is undocumented; reason: nit — architectural deviation
  from the original SDK §7.3 sketch needs only a BUILD-PLAN note
  explaining why streaming was preferred for v0.3.0. **Target:**
  PR #16.5 (BUILD-PLAN explanatory note).
- **DC-12** — "Start export" label is two words where its neighbours
  use one; reason: nit copy polish. **Target:** PR #16.5.
- **DC-13** — ExportModal uses raw `<button>` rather than the shared
  `Btn` component; reason: nit abstraction drift. **Target:**
  PR #16.5.

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
