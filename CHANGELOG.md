# Changelog

All notable changes to auraCP.

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
