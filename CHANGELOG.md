# Changelog

All notable changes to auraCP.

## v0.3.3 — 2026-05-30

Metadata-only patch over v0.3.2. The v0.3.2 release tag fired the
GitHub Actions release workflow against a working tree where
`go mod tidy` had drifted:

  - `github.com/go-webauthn/webauthn v0.17.4` was listed as
    `// indirect` in go.mod even though v0.3.2-D directly imports
    it from `pkg/dbadmin/standalone/mfa_webauthn.go` +
    `auth_webauthn.go`
  - `go.mongodb.org/mongo-driver/v2 v2.6.0` was listed as
    `// indirect` even though v0.3.2-F directly imports it from
    `pkg/dbadmin/driver/mongo.go` + `schema/mongo.go` +
    `rows/mongo.go`
  - go.sum had a stale `go.mongodb.org/mongo-driver v1.17.9` entry
    (v1 is no longer in the build graph)

CI's `go build ./...` with `-mod=readonly` (Go default) rejected
the mismatch. Running `go mod tidy` is the one-line fix.

Functionally identical to v0.3.2 — same features, same hardening,
same SPA bundle. Only go.mod + go.sum changed.

Tag v0.3.2 is preserved in history as the broken-build marker;
v0.3.3 is the installable release.

## v0.3.2 — 2026-05-30 (release-broken — see v0.3.3)

Combined v0.3.1 hardening + v0.3.2 feature release. The 14 `.5`
follow-up PRs deferred from v0.3.0 are all closed, and six new
features ship on top.

### v0.3.1 hardening (14 PRs, ~309 deferred items closed)

PR #2.5 — Classifier AST upgrade. Vitess (MariaDB) + pg_query_go
  (Postgres) parsers as the primary path; tokenizer fallback per-
  statement; forbidden-token matcher always-on against raw token
  stream. ParsedStatement.Tables now populated for AST-parsed
  statements (unblocks per-table grants in v0.3.2-B).

PR #3.5 — Driver hardening: unix-socket tunnel listener
  (/run/aura-db/tunnels/), Connection.QueryIdleTimeout,
  Limits.MaxBytesPerCell, MySQL NewConnector for credential
  lifetime, TLS registry cleanup, engine-identity verification,
  idle-sweeper floor 1s.

PR #4.5 — Schema reader: singleflight cacheFetch (preserves
  generation-counter race protection), Config.Query.Timeout
  plumbing via ForWithOptions, CappedError typed error,
  Postgres ViewSummary.Updatable correct, cross-user cache
  poisoning fixed via CacheConfig.Bucket, Postgres expression-
  index columns surfaced, MySQL system-schema case-bypass,
  fillTriggers error narrowing, normalizePostgresValue handles
  pgtype.UUID/Numeric/Interval/Range.

PR #5.5 — Row ops: Read LIMIT+1 + ReadResult.Capped, CountByOpts,
  IN/NOT IN max 1000 entries, Postgres Insert RETURNING <pk>,
  OpLike/ILike case-sensitivity documented, empty NOT IN rejected,
  NaN/Inf/[]byte rejection in IN lists, assertPKMatch nil
  rejection, UpdateByPK refuses PK mutation.

PR #6.5 — EXPLAIN: 30 items including Nested Loop multiplicative
  rows, PG node metadata (Sort Key, Group Key, Workers, JIT),
  MariaDB shape coverage (windowing, having_subqueries, Impossible
  WHERE), rollupMariaDBTotals, validateSQLForExplain (no SQL
  smuggling), PlanningTimed bool, parseFloatWithSuffix (10M →
  10485760), bm25-like changes to mergeMetrics, etc.

PR #7.5 — History: H4 extended redaction (IDENTIFIED VIA,
  postgresql:// URIs, dblink, COPY FROM PROGRAM), OpenOpts.
  RequireFTS5 + HasFTS(), retention loop (MaxRows + StartRetentionLoop
  + chunked DeleteOlderThan), entry_tags normalized table, writer
  semaphore, prepared-stmt cache, UTF-8 safe truncation, schema
  migration v1→v2.

PR #8.5 — HTTP handler: 31 items including rateClassStepUp
  (10/15min sliding window), conn-enumeration timing fix,
  stream WS limits from Config().Query.*Max, writeFrame error
  propagation, 30s WS ping ticker, panic %T redaction, signed-URL
  password reveal, asyncSink AuditSink wrapper, /sql/stream
  rate-limit + queryGate, /history pagination clamps, per-user
  query semaphore (16), denialAudit on 404s, LRU eviction of
  ratelimit buckets, etc.

PR #9.5 — Standalone: 29 items including KEK fstat-on-FD (TOCTOU
  fix), Webhook https:// + HMAC, RotateKEK closes commit/swap
  window, HIBP https:// enforcement, PHCWithFakeWorkloadMatching-
  Stored, recoverPrevHash rotated-sibling fallback, ULID
  clock-step-back clamp, session lookup ConstantTimeCompare,
  audit mode 0600, single `now` through Login, audit Reopen
  re-stat + re-chmod, http server shutdown before engine,
  Validate enforces enums, audit verify --json, WriteKEKFile
  fsyncs tmp + parent dir, recoverPrevHash JSON+hex validation,
  Save honors caller CreatedAt.

PR #10.5 — Panel glue: 27 items including secret.Box.EncryptAAD/
  DecryptAAD with HMAC-derived sub-key + dbadmin:creds: context
  binding, stepUpStore.InvalidateSession on logout, Config.Max
  surfaced from settings, panel_users → aura_db_grants FK
  cascade trigger, slog request-id middleware (X-Request-Id),
  ErrStepUpUnavailable sentinel, stepUpKey gains connectionID,
  RolesFor(ROLE_ADMIN) short-circuit, Action.Class() type,
  panelConns.Delete tx, json.Marshal detail, RolesFor zero-role
  filter, loadOrCreateSigningKey corruption refuse, CSRF bypass
  path.Clean, mapSaveErr UNIQUE→ErrConflict, GET /api/dbadmin/
  healthz + /readyz.

PR #11.5 — SPA shell: 55 items spanning a11y polish across the
  whole SPA. Skip link + <main> landmark, doc.title per route,
  responsive @media(max-width:720px), btn--primary AA contrast
  in dark, toastBus + ToastRegion, Tab close visible+keyboard,
  DropdownMenu WAI-ARIA menu, fonts self-hosted via lib/fonts.css
  (no Google Fonts at runtime), CSP meta tag (default-src 'self'),
  Toggle role=switch, table-row keyboard activation, TopNav
  aria-current="page", ResizeHandle keyboard (Arrow/Home/End),
  StatusDot 'connecting' state, EngineGlyph disambiguated, brand
  string 'Aura DB' consistent.

PR #12.5 — Row grid: 23 items including rAF-latched scroll
  (multi-event-per-frame coalesce), virtualWindow buffer 4→8,
  O(n)-per-edit slice removed, .rowgrid__body contain:paint
  (Safari sticky), --rg-grid-cols CSS var (single layout var
  instead of inline grid-template-columns per row), array-kind
  JSON.parse memo, dispose() lifecycle, deleteRows partial-fail
  restore in ascending index order, Esc-during-edit blur guard,
  filterParse `is null xyz` rejection, cells:Set saving indicator,
  integer kind split, grid root tabindex=-1, aria-rowcount=-1
  when unknown, NOT IN regex precedence, SchemaBrowser dblclick
  timer, commitEdit row-payload swap, listRows dead code removed.

PR #13.5 — SQL editor: 21 items including mid-stream rows
  preserved on error, SaveQueryModal (focus-trapped, name +
  description + tags + duplicate detection), loadIntoEditor
  dirty-check, runOne finalize() refreshes history on success
  AND error, replaceDoc semantic cursor anchor, empty-doc Save
  toast, CM6 contenteditable aria-label/role/multiline, Cancel
  aria-keyshortcuts ⌘., ErrorPanel role=alert + pushToast,
  sidebar accordions aria-expanded, saved-query × button +
  Delete keyboard, SaveQueryModal reuses Modal trap,
  ClassifierChip 🔒 glyph on FORBIDDEN, status+error live
  regions split, bundle ceilings tightened (main 95KB, editor
  175KB), ⌘+F discoverable, classifier.flush() pre-exec,
  Cmd+Shift+F preload-on-hover, isCommentOnly filter.

PR #14.5 — Inspector: 30 items including copper ramp retune,
  client-side severity shim (CORR-7), costStep log buckets,
  inspector re-classify on stmt change, RawPlanView large-
  payload defer, inline "tree truncated" marker, FlameNodeBar
  <mark> match highlight, fmtMs negative→em-dash, FlameTree
  ROW_H 24px, HotspotChip --estimate/--loops CSS, Spinner +
  elapsed counter + Abort on loading, RAW tablist promotion,
  search input toolbar, document.title on mount, stashForEditor
  on Close, AbortController + 60s deadline countdown, h/r
  shortcuts gated by editable target, RawPlanView decode
  simplified to object → JSON.parse.

PR #15.5 — Palette + history: 19 items including replay.js
  cleanup-after-newTab, 25-conn cap banner, Cmd-K scope-aware,
  star.id guard, drop duplicate engine in connection subtitle,
  loadForConn error count, monotonic _fetchToken, ☆/★ glyph
  state (not color-only), token vars for danger/star colors,
  palette Loading… while priming, multi-line saved-query
  preview collapse, selection border 3→2px, glyph col 28→20px,
  date-range pill styling, ⌘K hint in footer, "No commands
  match '{query}'. Press ⌘⌫" empty-state, section header
  padding 8/14/6, backdrop blur 8→4px.

PR #16.5 — Export: 34 items including RFC 5987 percent-encoded
  filename* + RFC 6266 ASCII filename=, exportLockManager TTL
  evict + LRU cap, byte-cap pre-write, jobID + X-Aura-Export-
  JobID, CSV NaN/+Inf/-Inf handled, NDJSON $truncated →
  __auracp_truncated, Postgres standard_conforming_strings,
  Retry-After + countdown banner, pre-flight count probe,
  X-Aura-Export-Rows + onProgress, ExportModal lazy chunk,
  sanitizeExportFilename mirrored client-side, ms-precision
  timestamp filenames, post-success "Done" CTA, format menu
  pre-selects, shared icons.chevron, filter/sort pills above
  form, row-count badge, NDJSON []byte JSON passthrough,
  countingWriter atomic.Int64, Btn component everywhere.

### v0.3.2 features (6 PRs)

**v0.3.2-A — Saved queries persistence.** Replaces the in-memory
savedQueriesStore with a SQLite-backed `pkg/dbadmin/saved` package
mirroring the history store pattern. Schema: saved_queries with
UNIQUE(connection_id, owner_id, name), partial index on starred,
FTS5 search with LIKE fallback. Star/tag/update/description all
durable. 12 new tests.

**v0.3.2-B — Per-table grants.** New `(user, conn, schema, table,
action)` matrix consumed by authorize() when ParsedStatement.Tables
is populated (which PR #2.5 unlocked for AST-parsed statements).
New Auth.HasTablePermission interface method (additive — preserves
SDK shape). New table_grants / aura_db_table_grants tables with
CASCADE off panel_users. Policy: additive — connection-level grant
is precondition; per-table denies are subtractive when any row
exists; fallback parses refuse with "unknown tables touched" if
Config.RequireTableGrants=true. Three new grant-management routes
(POST/DELETE /connections/{id}/grants/{schema}/{table}).

**v0.3.2-C — Slow-log streaming endpoint.** Closes the SDK §7 gap
flagged by PR #8 DEF-33. WS `GET /api/dbadmin/connections/{id}/slow-
log/stream` (subprotocol "aura.slowlog.v1"). MariaDB TABLE-mode
streams `mysql.slow_log` with follow + 2s poll; Postgres snapshot
mode via `pg_stat_statements`. Refuses with operator-actionable
hint when prereqs missing (slow_query_log=ON / extension
unavailable). Reuses every WS hardening from /sql/stream
(CSWSH, CSRF subprotocol, ping ticker, rate-limit, audit).
New `dbadmin.ActionSlowLogRead` action.

**v0.3.2-D — WebAuthn step-up.** Adds FIDO2/WebAuthn alongside
TOTP via go-webauthn/webauthn. Schema migration v4 adds
webauthn_credentials + webauthn_challenges tables. Four routes
(register/begin, register/finish, assert/begin; assert/finish
rides on the existing /login or /step-up/verify body via the
new WebAuthn field). Recovery codes still work; TOTP+WebAuthn
coexist; any one enrolled factor satisfies step-up.
WebAuthnEnabled opt-in (default false). Per-credential sign_count
replay defense (mirrors SEC-02 last_totp_step pattern).

**v0.3.2-E — CSV/NDJSON import.** Replaces the PR #8 /import stub
with streaming bulk-load. New `pkg/dbadmin/tableimport` package
symmetric to export/. Multipart/form-data: file + schema + table
+ format (csv|ndjson) + onConflict (skip|update|error). Reverses
SEC-1 formula sanitization (strips apostrophe prefix on read).
Type-coercion uses schema reader's column types as source of
truth (int64 vs float64 distinction preserved on NDJSON).
SQL format DELIBERATELY EXCLUDED — security boundary disallows
arbitrary statement execution via "import". Caps: 64 MiB body,
100K rows, 5 min timeout. Two audit events (start + outcome
with rowsImported/Skipped/Failed). ImportModal lazy chunk
(3.32 KB gz).

**v0.3.2-F — MongoDB driver.** Third engine: `dbadmin.EngineMongo`.
Uses `go.mongodb.org/mongo-driver/v2`. Classifier refuses raw
SQL for Mongo (RAW_SQL_ON_MONGO ForbiddenMatch) so all ops
route through structured rows.Operator. Driver implements
Conn/Rows; schema.Reader implements ListDatabases/Tables (collections)
/ GetTable (samples 100 docs to infer columns; PrimaryKey=["_id"]
always). Predicate mapping: $eq/$ne/$lt/$lte/$gt/$gte; Like→
$regex; Like+i→$regex+$options:"i"; In→$in; IsNull→{col:nil}.
UpdateResult.LastInsertKey (NEW additive field) carries ObjectID
hex on insert. EXPLAIN refused. ValidateMongoIdentifier (1..120
bytes, rejects / \ " $ \x00 + whitespace) — separate from
SQL ValidateIdentifier.

### Installer / packaging changes

- `aura-db` standalone binary cross-compiled in `dist`; .deb postinst
  symlinks it to `/usr/local/bin/aura-db` when present
- Postinst creates `/var/lib/auracp/aura-db/` (mode 0750) for the
  audit chain NDJSON
- Postinst creates `/etc/auracp/secrets/` (mode 0700) for the
  audit signing key (PR #10.5 PD-SEC-01 moved off the settings
  table to a 0600 root-owned file)
- Uninstall removes the new `/usr/local/bin/aura-db` symlink
  (existing `rm -rf /var/lib/auracp /etc/auracp` already covers
  the dirs)
- Makefile `build` target now produces `bin/aura-db` alongside
  `bin/auracpd` + `bin/auracp`
- Makefile `dist` target builds aura-db-linux-{amd64,arm64}
- Makefile exports `CGO_CFLAGS=-DHAVE_STRCHRNUL=1` by default
  to work around the macOS SDK 15+ libpg_query incompatibility
  (Linux unaffected)

### Bundle (gzipped)

| Chunk | v0.3.0 | v0.3.2 | Loaded |
|---|---|---|---|
| Main `index-*.js` | 46 KB | 57 KB | Eager |
| `HistoryScreen-*.js` | 3 KB | 3 KB | `/history` |
| `ExplainInspectorScreen-*.js` | 10 KB | 13 KB | `/explain` |
| `SqlEditor-*.js` | 146 KB | 150 KB | `/query` |
| `sqlFormatter-*.js` | 76 KB | 76 KB | Format click |
| `ExportModal-*.js` | n/a | 4 KB | Export menu |
| `ImportModal-*.js` | n/a | 3 KB | Import button |
| CSS | 6 KB | 11 KB | Eager |

### Test coverage

- 317 Vitest tests (was 252 at v0.3.0 cut + 65 new for `.5`
  + v0.3.2 SPA work)
- 13 `pkg/dbadmin/*` packages pass `go test -race -count=1`
  (was 10 at v0.3.0; +saved, +tableimport, +driver mongo
  + new test files across the .5 series)
- Adversarial reviews retired: 309 deferred items closed
  across the `.5` series.

---

## v0.3.0 — 2026-05-30

The Aura DB release. auraCP ships its own native database admin tool,
**Aura DB**, replacing the bundled Adminer.

Aura DB is a refined, dense, dark-default database workbench targeting
MariaDB and PostgreSQL. It is delivered as both an embedded panel
component (mounted at `/dbadmin/` inside auracpd) and a standalone
binary (`aura-db`). Both modes share the same Go engine and Svelte SPA.

Built over 17 reviewed PRs against the `aura-db-foundation` branch,
each shipping after a four-lens adversarial review pass.

### Added

- **Foundation packages** (PR #1–#7):
  - `pkg/dbadmin/` — engine + Auth / ConnectionStore / AuditSink
    interfaces + types + config
  - `pkg/dbadmin/classifier/` — tokenizer-based SQL classifier with
    forbidden-pattern denylist; rejects `LOAD_FILE`, `INTO OUTFILE`,
    `COPY FROM PROGRAM`, `pg_read_file`, `plpythonu`, etc.
  - `pkg/dbadmin/driver/` — MariaDB + Postgres drivers; SSH tunneling
    via `golang.org/x/crypto/ssh` with `known_hosts` enforcement;
    per-connection pool with idle eviction
  - `pkg/dbadmin/schema/` — `information_schema` + `pg_catalog`
    readers with TTL+LRU cache
  - `pkg/dbadmin/rows/` — paginated reads + PK-anchored writes +
    engine-aware SQL builder + optimistic-concurrency `Where` snapshot
  - `pkg/dbadmin/explain/` — engine-specific EXPLAIN normalizer
    returning a `Plan` / `Node` / `Metrics` tree; defense-in-depth
    `ClassRead` gate prevents `EXPLAIN ANALYZE` from running DELETE
  - `pkg/dbadmin/history/` — SQLite-backed query history with FTS5
    (LIKE fallback); SQL+Error redaction; per-Entry dialect; tag
    fence and comma-rejection; default-deny on empty UserID

- **HTTP wire surface** (PR #8): REST + WebSocket endpoints at
  `/api/dbadmin/*`. CSRF via configurable cookie/header names;
  classifier double-gate; per-route timeouts; rate limiting;
  audit emission on every mutating route and every denial.

- **Standalone implementations** (PR #9): pure-Go reference impls of
  Auth + ConnectionStore + AuditSink. AES-256-GCM with per-row AAD
  for credentials at rest (V2 ciphertext format; V1 backwards-readable).
  Argon2id PHC-encoded passwords (t=3, m=64MiB, p=4). SHA-256
  hash-chained audit log (all-zero genesis sentinel). DB-backed
  sessions bound to IP class (/24 IPv4, /56 IPv6) + UA hash. TOTP
  with replay window via `last_totp_step`. KEK file mode 0400
  enforced at boot AND on rotation. New `aura-db kek-init`
  subcommand for first-run bootstrap.

- **`cmd/aura-db`**: standalone binary with subcommands
  `serve`, `user-create`, `user-passwd`, `kek-rotate`, `kek-init`,
  `audit verify`, `audit tail`, `version`.

- **Panel-integrated glue** (PR #10): `internal/api/dbadmin/` adapters
  wrap the panel's existing session, SQLite store, and audit
  pipeline. New `aura_db_connections` + `aura_db_grants` tables
  registered via `store.RegisterExtraMigrator`. Credentials encrypted
  via the panel's existing `secret.Box`. Audit dual-write: SHA-256
  hash chain at `/var/lib/auracp/aura-db/audit.ndjson` plus mirror
  summary into the panel `audit_log`. `IdentitySummary` replaces the
  internal `currentUser` so the adapter never sees `PasswordHash` or
  `TOTPSecret`.

- **Svelte SPA shell** (PR #11): refined-minimalism, dark-default,
  oxidized-copper accent (`#b87333`), IBM Plex Sans/Mono. Layout:
  40px TopNav + 280px LeftTree + 32px TabBar + 24px StatusBar;
  hairline-bordered, edge-to-edge, no floating cards. `AuraDBClient`
  with one method per route; typed `AuraDBError`. `sqlStream.js`
  WebSocket client with exponential backoff + visibility-gated
  pause + MAX_ATTEMPTS=20 hard cap + CSRF subprotocol.

- **Row grid** (PR #12): hand-rolled virtualization (~25 LOC
  pure function); CSS Grid column alignment; density toggle
  (24/28/32px); per-column filter inputs with operator-prefix
  parsing (=, !=, >, <, like, ilike, in, is null); multi-sort via
  Shift+Click; offset-based pagination; inline edit with optimistic
  concurrency (server-side `rows.UpdateByPKOpts.Where`); 50-entry
  session undo/redo; toast bus mapping `AuraDBError` codes to
  friendly strings; layout persisted to localStorage per
  (connId, schema, table).

- **SQL editor** (PR #13): CodeMirror 6 minimal subset; schema-aware
  autocomplete with per-connection cache; live classifier preview
  (250ms debounce with monotonic seq); WS streaming execution via
  `sqlStream`; per-tab cancel with `execRegistry` prior-cancel
  semantics; FIFO tab eviction (cap 8) cancels in-flight handles;
  terminal state machine prevents cancelled→done flip on late
  frames; multi-statement Execute All; SQL formatter
  (`sql-formatter` v15) dynamic-imported as its own chunk;
  classifier endpoint `POST /sql/classify` (UX-only; server still
  re-classifies before dispatch).

- **EXPLAIN inspector** (PR #14): pure-SVG flame-tree over the
  `/sql/explain` payload; MariaDB ANALYZE-only metrics render as
  em-dash (not "0"); PG `loops=0` branches flagged "not executed";
  5-step ordinal color scale with numeric cost-% chip (WCAG 1.4.1);
  ANALYZE confirm dialog requires typing literal "ANALYZE" for
  non-read statements; sessionStorage handoff from SQL editor
  (validates connId against route); inspector lazy-loaded
  (~10KB gzipped chunk).

- **Command palette + history screen** (PR #15): Cmd+K opens a
  Linear/Raycast-style overlay with fuzzy match (hand-rolled ~50
  LOC with prefix + word-boundary + consecutive + length bonuses);
  sections for Connections / Recent History / Saved Queries /
  Actions; flat score-sorted order when query is non-empty;
  combobox + listbox ARIA with focus trap. Dedicated `/history`
  screen with date-range / connection / status / class filters,
  star toggle, replay-into-editor via `sessionStorage["editor:pending"]`.
  Tree filter relocated to Cmd+Shift+K; Cmd+G jumps to history;
  "/" opens palette actions-only mode.

- **Streaming export** (PR #16): HTTP streaming with chunked transfer
  + `Content-Disposition: attachment`. Three formats — CSV (RFC 4180
  with formula-injection guard per OWASP), NDJSON (ordered keys),
  SQL (engine-aware INSERT statements with backtick/double-quote
  quoting and binary as `X'..'` / `'\x..'::bytea`). Hard caps: 1M
  rows / 1GB bytes / 1h timeout. Per-user concurrency semaphore
  (409 on contention). Two audit events per export (start +
  outcome with row count + bytes + truncated flag). Truncation
  surfaced via `X-Truncated` trailer + audit. Mid-stream errors
  surfaced via `X-Export-Error` trailer + format-specific inline
  marker. SqlEditor result tabs gain a client-side export.

### Changed

- **Adminer removal + cutover** (PR #17): the bundled Adminer
  (PHP-FPM mount at `/_adminer/`) is gone. The panel SPA's "Manage"
  button now deep-links into the Aura DB SPA at
  `/dbadmin/#/connections?engine=...&name=...` instead of minting
  an SSO token. Net **1,742-LOC reduction** in install/build/web
  layers. v0.2.x → v0.3.0 upgrades clean up the legacy artifacts
  via `purge_legacy_adminer()` (installer) and the .deb postinst
  block.

  Operator migration guide: [docs/aura-db/MIGRATING-FROM-ADMINER.md](docs/aura-db/MIGRATING-FROM-ADMINER.md).

### Security

Every Aura DB PR shipped after a four-lens adversarial review
(security / correctness / a11y / integration) with a synthesizer
triaging findings. Notable security catches across the series:

- **CSWSH on `/sql/stream`** (PR #8 review): `originAllowed` was a
  stub returning `true` on every cross-origin handshake. Fixed with
  strict scheme+host+port equality vs `r.Host` + CSRF gate on the
  WS upgrade via `X-Aura-Csrf` header OR `aura.csrf.<token>`
  subprotocol entry (constant-time compare against
  `__Host-aura_csrf`).
- **Silent denial audit** (PR #8): authn / CSRF / rate-limit
  short-circuited before the outer `audit()` middleware. Every
  401/403/429 was forensically invisible. Each denying middleware
  now emits an audit event inline before `writeError`.
- **WS error frame leaked raw driver text** (PR #8): post-upgrade
  error frames echoed `err.Error()` defeating the REST `mapErr`
  sanitizer. Now routed through `mapErr` for the sanitized code +
  public message.
- **Audit signing key leak** (PR #10): the HMAC chain key was
  stored in the panel `settings` table, readable by any
  authenticated user via `GET /api/settings`. Moved to a sibling
  file `/etc/auracp/secrets/aura-db-audit.key` at mode 0600;
  defense-in-depth denylist on `getSettings`.
- **CSRF cookie mismatch** (PR #10): panel mints `auracp_csrf`;
  dbadmin demanded `__Host-aura_csrf`; no production code minted
  the latter, so every mutating `/api/dbadmin/*` request 403'd.
  `httpapi.Options.CSRFCookieName` + `CSRFHeaderName` made
  configurable; panel mount passes the panel's existing names.
- **Lockout case-bypass** (PR #9): users.username was
  `COLLATE NOCASE` but lockouts.scope was case-sensitive. Username
  rotation evaded per-user lockout. Fixed by lowercasing username
  before constructing the lockout scope key.
- **TOTP replay window** (PR #9): `VerifyTOTP` was pure-stateless,
  so a captured code was accepted by every verifier for the full
  ±90s window. Added `users.last_totp_step` column; verify rejects
  step <= LastTOTPStep.
- **Password oracle on MFA users** (PR #9): the `ErrMFARequired`
  branch did not call `recordLoginFailure`. Attackers could grind
  passwords against MFA accounts at full Argon2id throughput
  without tripping the lockout. MFA-required + correct-password +
  missing/wrong TOTP now bumps the per-user lockout counter.
- **AEAD missing AAD** (PR #9): credential blobs and MFA secrets
  could be swapped between rows undetected. Added V2 ciphertext
  format with `conn:<id>` / `mfa:<userID>` AAD; V1 retained as
  read-only for upgrade-on-write.
- **CSV formula injection** (PR #16, CWE-1236): cells starting with
  `= + - @ \t \r` execute as formulas in Excel/Sheets even when
  quoted. Now unconditionally prefixed with apostrophe (OWASP
  convention).
- **Optimistic concurrency** (PR #12): row PATCH was blind, silently
  clobbering concurrent edits. Added `rows.UpdateByPKOpts.Where`
  threading a snapshot of non-PK columns; backend rejects with 409
  if the snapshot moved.

### Fixed

The full list of must-fix items applied per PR lives in each commit
message. Deferred items (309 total across the series — non-boundary
highs, mediums, lows, nits) are tracked in
[docs/aura-db/KNOWN-ISSUES.md](docs/aura-db/KNOWN-ISSUES.md) and
scheduled for the .5 follow-up PRs (v0.3.1).

### Tooling & docs

- New `aura-db` binary with full CLI surface
- Operator docs: [STANDALONE-DEPLOY.md](docs/aura-db/STANDALONE-DEPLOY.md),
  [KEY-ROTATION.md](docs/aura-db/KEY-ROTATION.md),
  [CONFIG-REFERENCE.md](docs/aura-db/CONFIG-REFERENCE.md),
  [MIGRATING-FROM-ADMINER.md](docs/aura-db/MIGRATING-FROM-ADMINER.md)
- Architecture / design / security docs:
  [ADR-001-architecture.md](docs/aura-db/ADR-001-architecture.md),
  [SDK.md](docs/aura-db/SDK.md),
  [SECURITY.md](docs/aura-db/SECURITY.md)

### Bundle size

| Chunk | Gzipped | Loaded |
|---|---|---|
| Main (`index-*.js`) | ~54 KB | Eager (every page) |
| `HistoryScreen-*.js` | ~3 KB | Lazy on `/history` |
| `ExplainInspector-*.js` | ~10 KB | Lazy on `/explain` |
| `SqlEditor-*.js` | ~146 KB | Lazy on `/query` (CodeMirror lives here) |
| `sqlFormatter-*.js` | ~76 KB | Dynamic on Format click |
| CSS | ~9 KB | Eager |

### Test coverage

- 252 Vitest tests across the SPA (helpers + components)
- All 10 `pkg/dbadmin/*` packages pass `go test -race -count=1`
- Adversarial-review test fixtures exercise wire-contract conformance,
  optimistic concurrency, CSV escaping, classifier denials, audit
  chain integrity, TOTP replay rejection, KEK rotation atomicity

---

For older releases see git log: `git log --oneline --grep="^v0\."`.
