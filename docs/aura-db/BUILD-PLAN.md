# Aura DB — Build Plan

**Status:** active execution plan.
**Companions:** `INDEX.md` (foundation set), `ADR-001-architecture.md`,
`SECURITY.md`, `SDK.md`.

This is the concrete plan for going from "foundation docs committed" to
"v0.3.0 ships." Breaks the work into reviewable PRs, each shippable and
testable in isolation. Tracks dependencies so we know what can move in
parallel later if we want to.

Every PR follows the same shape:

1. Lives on its own branch off `aura-db-foundation`.
2. Includes its own tests (unit minimum, integration where applicable).
3. Ends with `go build ./...` + `go vet ./...` + targeted `go test`
   passing.
4. Has its own commit message documenting what it adds + what it
   intentionally defers to a later PR.
5. Merges into `aura-db-foundation` once green. The foundation branch
   becomes the integration branch; main only sees `aura-db-foundation`
   when we cut v0.3.0.

LOC budgets below are estimates, not contracts. A 20% overrun is
normal; >50% triggers a re-scope discussion.

---

## PR sequence

### PR #1 — `pkg/dbadmin` skeleton + test helpers

**Goal:** the engine's public surface compiles. No drivers, no
classifier, no HTTP routes. Just types + interfaces + in-memory test
implementations.

**Delivers:**

- `pkg/dbadmin/types.go` — User, ConnectionID, Connection, Credentials,
  Role, Action, EngineKind, Tag, Origin, SSHTunnel, Target, Event.
- `pkg/dbadmin/errors.go` — ErrNotFound, ErrUnauthenticated,
  ErrStepUpRequired.
- `pkg/dbadmin/auth.go` — Auth interface.
- `pkg/dbadmin/conns.go` — ConnectionStore interface.
- `pkg/dbadmin/audit.go` — AuditSink interface.
- `pkg/dbadmin/engine.go` — Engine struct, Options, New, Handler
  (placeholder), Shutdown.
- `pkg/dbadmin/config.go` — Config + defaults from SECURITY.md §14.
- `pkg/dbadmin/dbadmintest/` — in-memory Auth + ConnectionStore +
  AuditSink for tests, with builder API.
- `pkg/dbadmin/engine_test.go` — smoke test: engine boots with test
  impls and Handler() returns 401 on unauthed request.

**Verifies:** the SDK contract is self-consistent and round-trips
through Go's type system. Sets the foundation every later PR builds
on.

**LOC budget:** ~1,500 Go.

**Dependencies:** none.

---

### PR #2 — Classifier (tokenizer-based first cut)

**Goal:** every SQL statement gets lexed and classified before
execution. Forbidden statements never reach a driver.

**Strategy chosen:** ship a hand-written SQL tokenizer + token-sequence
matcher rather than pull in Vitess (~100 MB transitive) or pg_query_go
(cgo) up front. The tokenizer is **not regex** — it's proper lexical
analysis that handles string literals, line + block comments, quoted
identifiers, dollar-quoting (Postgres), and `;`-separated multi-
statement queries correctly. The forbidden detector walks token
sequences (not raw text), so `'/* SELECT */ LOAD_FILE(...`-style
bypasses don't work against it.

**Delivers:**

- `pkg/dbadmin/classifier/classifier.go` — top-level Classify(engine,
  sql) → ParsedQuery. Parser interface (pluggable so PR #2.5 can drop
  in Vitess + pg_query_go for AST-level work).
- `pkg/dbadmin/classifier/lexer.go` — production-grade SQL tokenizer.
- `pkg/dbadmin/classifier/mysql.go` — MySQL/MariaDB statement-class
  detector + forbidden patterns.
- `pkg/dbadmin/classifier/postgres.go` — Postgres equivalent.
- `pkg/dbadmin/classifier/forbidden.go` — the hard-forbidden patterns
  (shared types + multi-token matcher).
- `pkg/dbadmin/classifier/redact.go` — sensitive parameter redaction
  for audit (CREATE USER ... IDENTIFIED BY, etc.).
- `pkg/dbadmin/classifier/testdata/forbidden/` — CVE corpus
  (LOAD_FILE, INTO OUTFILE, COPY FROM PROGRAM, pg_read_file,
  plpythonu, etc.) with attack variants (comment-obfuscated,
  whitespace-padded, case-mixed, string-concat).
- `pkg/dbadmin/classifier/classifier_test.go` — table-driven tests.
- `pkg/dbadmin/classifier/lexer_test.go` — tokenizer tests.
- `pkg/dbadmin/classifier/fuzz_test.go` — fuzz harness.

**Verifies:** SECURITY.md §6.3 is enforced in code. CVE corpus is
covered. Fuzzing finds no statement that gets reclassified down from
forbidden to read/write.

**Known limitations (covered in PR #2.5):**

- No AST. Aliased function calls and semantically-equivalent rewrites
  that the corpus doesn't cover may slip through token-sequence
  matching. Token-sequence matching is the same defense pgAdmin and
  DBeaver use for their pre-execution filters; it has held up well in
  practice but is acknowledged as second-best to a full AST walk.
- ParsedStatement.Tables (the list of objects each statement touches)
  is left empty in this PR. The handler can't yet enforce per-table
  authorization (only per-connection); per-table grants are deferred.
- Postgres-only function-language denylist (plpythonu, plperlu, etc.)
  matches by token sequence in `LANGUAGE` clauses; future AST work
  can verify the language directly from the parsed CREATE FUNCTION
  node.

**LOC budget:** ~2,000 Go (lexer ~600, classifier ~400, forbidden
~300, tests ~700).

**Dependencies:** PR #1.

### PR #2.5 — Classifier AST upgrade (shipped)

**Goal:** swap the tokenizer-based statement detection for full AST
classification using Vitess (MariaDB/MySQL) + pg_query_go (Postgres).
The forbidden-token matcher stays — it's belt-and-braces.

Cascade: AST primary, tokenizer fallback per-statement, forbidden
matcher always-on against the raw token stream. The Postgres AST
parser links libpg_query through cgo; builds with CGO_ENABLED=0
silently degrade to the PR #2 tokenizer for Postgres (MySQL stays on
AST because Vitess is pure Go).

Tables on each ParsedStatement are populated for AST-parsed statements
(unblocks per-table authorization in PR #4). Statements that fall back
to the tokenizer leave Tables nil — hosts that depend on per-table
authorization must treat that as "unknown tables touched" and refuse.

**Known limitations of the AST cascade (resolved or accepted):**

- Quoted-identifier `LANGUAGE "plpythonu"` was a tokenizer false
  negative; PR #2.5 adds an AST-level detector for it.
- Dynamic SQL (`PREPARE … EXECUTE`) is opaque; Tables remains empty
  and per-table auth in PR #4 must refuse "unknown tables touched".
- Vitess refuses GRANT / REVOKE / some vendor extensions; those fall
  back to the tokenizer per-statement (classification unchanged from
  PR #2 for those inputs).
- search_path is not resolved by the classifier; unqualified Postgres
  references leave Target.Schema empty.

**Dependencies:** PR #2.

---

### PR #3 — Driver layer + connection management

**Goal:** the engine can actually talk to MariaDB and Postgres.

**Delivers:**

- `pkg/dbadmin/driver/driver.go` — abstract Driver interface.
- `pkg/dbadmin/driver/mysql.go` — go-sql-driver/mysql implementation.
- `pkg/dbadmin/driver/postgres.go` — pgx/v5 implementation.
- `pkg/dbadmin/driver/tunnel.go` — SSH tunneling (golang.org/x/crypto/ssh).
- `pkg/dbadmin/driver/pool.go` — per-connection pool with on-demand
  open and idle close.
- `pkg/dbadmin/driver/limits.go` — context-with-timeout, result row
  cap, byte cap.
- `pkg/dbadmin/driver/driver_test.go` — integration tests against
  `docker compose` MariaDB + Postgres in CI.

**Verifies:** end-to-end query execution with policy enforcement.
Resource limits from SECURITY.md §6.5 are applied.

**LOC budget:** ~1,500 Go.

**Dependencies:** PR #1.

**CI changes:** GitHub Actions workflow needs MariaDB 11.x + Postgres
16 service containers.

---

### PR #4 — Schema readers

**Goal:** the engine can enumerate databases, tables, columns,
indexes, FKs, triggers, functions for both engines.

**Delivers:**

- `pkg/dbadmin/schema/schema.go` — abstract Schema interface returning
  a normalized model.
- `pkg/dbadmin/schema/mysql.go` — `information_schema` queries.
- `pkg/dbadmin/schema/postgres.go` — `pg_catalog` queries.
- `pkg/dbadmin/schema/cache.go` — per-connection cache with TTL +
  invalidation hook.
- `pkg/dbadmin/schema/schema_test.go` — integration tests.

**Verifies:** schema browsing works against real engines. Caching is
correct (no stale columns after an ALTER).

**LOC budget:** ~1,200 Go.

**Dependencies:** PR #1, PR #3.

---

### PR #5 — Row operations

**Goal:** paginated reads + parameterized writes for the row grid.

**Delivers:**

- `pkg/dbadmin/rows.go` — Read (paginated, sortable, filterable),
  Update (by PK, parameterized), Delete (by PK, parameterized),
  Insert.
- `pkg/dbadmin/rows_test.go`.
- Pre-flight checks: every Update/Delete requires a PK. Operations on
  tables without a PK are refused with a clear error.

**Verifies:** the row grid's backing API works without going through
the SQL editor. No raw-SQL exposure for grid actions.

**LOC budget:** ~700 Go.

**Dependencies:** PR #1, PR #3, PR #4.

---

### PR #6 — EXPLAIN normalization

**Goal:** EXPLAIN output from both engines lands in a unified plan
tree the frontend can render.

**Delivers:**

- `pkg/dbadmin/explain/explain.go` — Plan struct (nodes, costs, rows,
  buffers).
- `pkg/dbadmin/explain/mysql.go` — parses `EXPLAIN FORMAT=JSON` output.
- `pkg/dbadmin/explain/postgres.go` — parses
  `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` output.
- `pkg/dbadmin/explain/explain_test.go`.

**Verifies:** explain renderer has a stable input format regardless of
engine.

**LOC budget:** ~600 Go.

**Dependencies:** PR #1, PR #3.

**Note:** the *frontend* flame-tree renderer is v0.3.1 work; this PR
just gets the data shape right so v0.3.0 can ship a basic plan view
and v0.3.1 swaps the renderer.

---

### PR #7 — Query history

**Goal:** every query the user runs is recorded, searchable, restorable
into a new editor tab.

**Delivers:**

- `pkg/dbadmin/history/history.go` — append + query interface.
- `pkg/dbadmin/history/sqlite.go` — SQLite-backed default
  implementation (used by both standalone and integrated modes; in
  integrated mode it points at the panel's existing SQLite store).
- `pkg/dbadmin/history/history_test.go`.

**Verifies:** history persistence + full-text search work.

**LOC budget:** ~500 Go.

**Dependencies:** PR #1.

---

### PR #8 — HTTP handler (the engine's wire surface)

**Goal:** the REST + WebSocket surface documented in SDK.md §7 is
mounted and serves real responses.

**Delivers:**

- `pkg/dbadmin/handler/handler.go` — mux setup + middleware chain
  (auth, CSRF, rate limit, audit).
- `pkg/dbadmin/handler/connections.go` — `/connections/*` handlers.
- `pkg/dbadmin/handler/schema.go` — `/connections/{id}/schemas/*`
  handlers.
- `pkg/dbadmin/handler/rows.go` — `/connections/{id}/schemas/.../rows/*`
  handlers.
- `pkg/dbadmin/handler/query.go` — `/connections/{id}/query` (the
  classifier + driver + history flow).
- `pkg/dbadmin/handler/explain.go` — `/connections/{id}/explain`.
- `pkg/dbadmin/handler/history.go` — `/connections/{id}/history`.
- `pkg/dbadmin/handler/audit.go` — `/connections/{id}/audit`.
- `pkg/dbadmin/handler/middleware.go` — request ID, rate limiting,
  CSRF, header hardening per SECURITY.md §8 + §10.
- `pkg/dbadmin/handler/handler_test.go` — uses `httptest` with the
  in-memory dbadmintest impls.

**Verifies:** the full request lifecycle works end-to-end against the
in-memory test impls. Headers, rate limits, CSRF, audit emission are
all present and correct.

**LOC budget:** ~2,500 Go.

**Dependencies:** PR #1, PR #2, PR #3, PR #4, PR #5, PR #6, PR #7.

---

### PR #9 — Standalone implementations + cmd/aura-db

**Goal:** the standalone binary exists and runs. First-run setup
works.

**Delivers:**

- `pkg/dbadmin/standalone/auth.go` — argon2id + WebAuthn + TOTP +
  recovery codes. Sessions table. Session binding. Step-up flags.
- `pkg/dbadmin/standalone/conns.go` — SQLite + secret-box encryption.
- `pkg/dbadmin/standalone/audit.go` — file-backed chain-signed sink.
- `pkg/dbadmin/standalone/setup.go` — first-run wizard logic.
- `cmd/aura-db/main.go` — listener + TLS (LE via lego, same as panel)
  + first-run gate + signal handling + graceful shutdown.
- `installer/aura-db-install.sh` — install script for standalone mode.
- `packaging/aura-db.service` — systemd unit.
- `packaging/aura-db.docker/Dockerfile` — distroless image.

**Verifies:** `make aura-db && ./aura-db` brings up a working
standalone binary. First-run wizard works in a browser. SQLite state
persists across restarts.

**LOC budget:** ~3,500 Go + ~200 shell + ~50 packaging.

**Dependencies:** PR #1, PR #2, PR #3, PR #4, PR #5, PR #6, PR #7, PR #8.

---

### PR #10 — Panel-integrated glue

**Goal:** the engine, served from auracpd, plumbed into the panel's
existing auth + connection store + audit.

**Delivers:**

- `internal/api/dbadmin.go` — PanelAuth + PanelConnections +
  PanelAudit (bridges from `dbadmin.*` interfaces to the panel's
  internal store).
- `internal/store/dbadmin_grants.go` — new table + queries for
  per-(user, connection, role) grants.
- `internal/store/migrate.go` — schema migration for
  `dbadmin_grants`.
- `cmd/auracpd/main.go` — engine boot + handler mounting at
  `/api/dbadmin`.
- `internal/api/api.go` — mux registration of the dbadmin handler +
  permission wiring.

**Verifies:** the panel can list, browse, and query databases via the
new engine. Existing panel auth flows through naturally.

**LOC budget:** ~1,000 Go.

**Dependencies:** PR #1, PR #2, PR #3, PR #4, PR #5, PR #6, PR #7, PR #8.

---

### PR #11 — Frontend shell + connection tree + schema tree

**Goal:** the three-pane layout exists and shows real data from
real backends.

**Delivers:**

- `web/src/screens/dbadmin/DbAdmin.svelte` — shell + 3-pane layout.
- `web/src/screens/dbadmin/panes/ConnectionTree.svelte`.
- `web/src/screens/dbadmin/panes/SchemaTree.svelte` — lazy-loaded,
  search-as-you-type.
- `web/src/screens/dbadmin/panes/TabStrip.svelte` — placeholder; full
  tab management comes in PR #12.
- `web/src/screens/dbadmin/panes/Inspector.svelte` — placeholder.
- `web/src/screens/dbadmin/lib/api.js` — typed API client.
- `web/src/screens/dbadmin/lib/keymap.js` — global keymap scaffold.
- `web/src/screens/dbadmin/icons/` — custom 16 px icon set.
- Theme tokens reused from `web/src/app.css` (the gray light + dark
  cool-neutral palette already shipped).

**Verifies:** the UI loads, shows connections, lets the operator browse
to a schema, lists tables.

**LOC budget:** ~3,000 Svelte/JS + ~500 CSS.

**Dependencies:** PR #10.

---

### PR #12 — Row grid

**Goal:** the central feature operators use most.

**Delivers:**

- `web/src/screens/dbadmin/workspace/RowGrid.svelte` — virtual-scrolled
  via custom windowing (no react-window equivalent in Svelte; we write
  ours). Column types color-coded (PK gold, FK cyan, nullable italic).
  Sort + per-column filter. Click cell → inline edit; double-click →
  modal editor with type-aware input.
- `web/src/screens/dbadmin/workspace/Cell.svelte` — type-aware cell
  renderer (text, number, date, JSON, BLOB-hex, NULL).
- `web/src/screens/dbadmin/workspace/CellEditor.svelte` — modal editor
  with type-aware input components.
- Keyboard navigation: j/k row, h/l column, Enter edit, Escape cancel,
  Ctrl+S commit.

**Verifies:** operators can browse, sort, filter, edit rows in real
databases. Tests against MariaDB + Postgres via the integration test
harness.

**LOC budget:** ~3,500 Svelte/JS.

**Dependencies:** PR #11.

---

### PR #13 — SQL editor

**Goal:** Monaco-based editor with schema-aware autocomplete and
multi-result tabs.

**Delivers:**

- `web/src/screens/dbadmin/workspace/SqlEditor.svelte` — Monaco
  lazy-loaded only when this component first mounts (`import()`
  on-demand).
- `web/src/screens/dbadmin/workspace/ResultPanel.svelte` — multi-tab
  results, each with its own grid.
- `web/src/screens/dbadmin/lib/sql-completion.js` — schema-aware
  completion provider (table names, column names, function names).
- Keymap: ⌘↵ run, ⌘⇧↵ run-and-keep-results, ⌘E new tab, ⌘W close tab.

**Verifies:** operators can type a query, get autocomplete from the
loaded schema, run, see results, run a second query in a new tab,
recall a recent query from history.

**LOC budget:** ~2,500 Svelte/JS.

**Dependencies:** PR #11, PR #12 (shares the grid component).

---

### PR #14 — Inspector pane

**Goal:** the right pane responds to selection context.

**Delivers:**

- `web/src/screens/dbadmin/panes/Inspector.svelte` — driven by a
  reactive context store.
- Sub-components: ColumnList, IndexList, ForeignKeyList, TriggerList,
  DDLView, RowDetail, ResultStats, ExplainSummary.

**Verifies:** clicking a table → columns + indexes + FKs visible.
Selecting a row → full JSON view. After a query → execution stats +
inline EXPLAIN summary.

**LOC budget:** ~1,500 Svelte/JS.

**Dependencies:** PR #11, PR #12, PR #13.

---

### PR #15 — Command palette + history UI

**Goal:** ⌘K opens a fuzzy-matched command palette. History tab works.

**Delivers:**

- `web/src/screens/dbadmin/lib/palette.svelte.js` — palette store.
- `web/src/screens/dbadmin/panes/Palette.svelte`.
- `web/src/screens/dbadmin/panes/HistoryPanel.svelte`.

**Verifies:** every action documented in keymap is reachable via ⌘K.
History is searchable + restorable.

**LOC budget:** ~1,200 Svelte/JS.

**Dependencies:** PR #11.

---

### PR #16 — Export

**Goal:** export from any grid or query result as CSV / JSON / SQL /
Markdown.

**Delivers:**

- `pkg/dbadmin/export/csv.go`, `json.go`, `sql.go`, `markdown.go`.
- Frontend: export button + modal with format selection + row-cap
  warning if over 10,000 rows.
- Background streaming: large exports stream to a one-time signed URL,
  not buffered in memory.

**Verifies:** export works for grids and editor result panels. Large
exports don't OOM.

**LOC budget:** ~800 Go + ~400 Svelte.

**Dependencies:** PR #5, PR #11, PR #12.

---

### PR #17 — Adminer removal + cutover

**Goal:** Adminer goes away. v0.3.0 release candidate.

**Delivers:**

- `installer/install.sh` — remove `install_adminer`, the FPM pool
  template for adminer, the SSO wrapper PHP.
- `internal/webserver/template/templates/00-panel.tmpl` — remove the
  `/_adminer/` location block.
- `internal/api/*.go` — remove the Adminer-SSO endpoint (`/api/sites/
  {domain}/databases/{engine}/{name}/manage`).
- `web/src/screens/SiteDetail.svelte` — replace "Manage" buttons with
  links to `/dbadmin?conn=<id>`.
- `packaging/build-deb.sh` — drop the postinst auto-upgrade hook for
  Adminer.
- `docs/aura-db/MIGRATING-FROM-ADMINER.md` — user-facing migration
  doc.
- Version bump: 0.2.62 → 0.3.0. Smoke probe agent + CLI version
  strings.

**Verifies:** clean `auracp-install` on a fresh VM has no Adminer
artifacts. The "Manage" workflow uses Aura DB. Operators upgrading
from v0.2.x see Aura DB in place.

**LOC budget:** ~500 (mostly deletions).

**Dependencies:** PR #10, PR #11, PR #12, PR #13.

---

### PR #18 — Release: v0.3.0

**Goal:** ship.

**Delivers:**

- `make release` produces both the integrated `auracp_0.3.0_amd64.deb`
  and the standalone `aura-db_0.3.0_amd64.deb` + arm64 variants.
- Sigstore signing of both, with attestation.
- SBOM published.
- GitHub release notes covering: new tool, removed Adminer,
  permissions migration (operator action required for non-default
  grants), security-relevant config defaults.
- `docs/aura-db/USING.md` (operator UI guide), `OPERATING.md`,
  `STANDALONE-INSTALL.md` finalized.

**Verifies:** end-to-end install on a fresh VM works for both modes.

**LOC budget:** ~300 (mostly release plumbing + final docs).

**Dependencies:** every prior PR green.

---

## Critical-path dependency graph

```
PR1 ─── PR2 ──┐
   ├── PR3 ──┤
   │         ├── PR6 ── PR8 ── PR10 ── PR11 ── PR12 ── PR13 ── PR14 ── PR17 ── PR18
   ├── PR4 ──┤                                       │
   ├── PR5 ──┤                                       └── PR15
   └── PR7 ──┘                                              └── PR16
                  PR9 (standalone) ───────────────────────────────┘
```

What can run in parallel after PR #1 is green:

- PR #2 (classifier) ‖ PR #3 (drivers) ‖ PR #7 (history).
- PR #4 (schema) and PR #5 (rows) both need PR #3.
- PR #6 (explain) needs PR #3.
- PR #8 (handler) needs PR #2–#7.
- PR #9 (standalone) needs PR #8.
- PR #10 (panel glue) needs PR #8.
- PR #11–#15 (frontend) can largely interleave once PR #10 lands.

Optimistic timeline if one engineer: ~6 weeks linear. With two
engineers parallelizing frontend + standalone tracks after PR #8:
~4 weeks. v0.3.0 ship target: 4-6 calendar weeks after PR #1 merges.

---

## Test policy per PR

Every PR must include:

1. **Unit tests** for any new exported function in `pkg/dbadmin/`.
2. **Integration tests** when a PR touches the driver layer or query
   path. CI spins up MariaDB 11.x + Postgres 16 service containers.
3. **Fuzz harness** for the classifier (PR #2 onward).
4. **Frontend smoke tests** via Playwright for any PR touching
   `web/src/screens/dbadmin/`.

PRs without their tests are blocked at review. We do not retroactively
"add tests later."

---

## Definition of done per PR

- `go build ./...` clean.
- `go vet ./...` clean.
- `go test ./...` green (including integration if applicable).
- Frontend `npm run build` + `npm run test` green.
- New exported APIs documented in code (godoc).
- Any SECURITY.md / SDK.md surface change reflected in those docs in
  the same PR.
- Branch merged into `aura-db-foundation` (the integration branch).
- Build plan updated: mark PR as ✓ done, link the merge commit.

---

## Status tracker

| PR  | Title                                  | Status   | Merge commit |
| --- | -------------------------------------- | -------- | ------------ |
| 1   | `pkg/dbadmin` skeleton + test helpers  | ✓ done   | c6419e9      |
| 2   | Classifier (tokenizer-based)           | ✓ done   | 8514a37      |
| 2.5 | Classifier AST upgrade                 | shipped  |              |
| 3   | Driver layer                           | ✓ done   | 97de7d3      |
| 3.5 | Driver hardening follow-up             | deferred |              |
| 4   | Schema readers                         | ✓ done   | da65065      |
| 4.5 | Schema reader follow-up                | deferred |              |
| 5   | Row operations                         | ✓ done   | 4921bdb      |
| 5.5 | Row ops engine-parity + limits         | deferred |              |
| 6   | EXPLAIN normalization                  | ✓ done   | 0411605      |
| 6.5 | EXPLAIN polish + rollup fixes          | deferred |              |
| 7   | Query history                          | ✓ done   | e62468e      |
| 7.5 | History redaction/retention follow-up  | deferred |              |
| 8   | HTTP handler                           | ✓ done   | 417aa04      |
| 8.5 | HTTP handler hardening follow-up       | deferred |              |
| 9   | Standalone implementations + cmd       | ✓ done   | 5de6bfb      |
| 9.5 | Standalone hardening follow-up         | deferred |              |
| 10  | Panel-integrated glue                  | ✓ done   | 10235b1      |
| 10.5| Panel glue hardening follow-up         | deferred |              |
| 11  | Frontend shell + trees                 | ✓ done   | 052726e      |
| 11.5| SPA shell hardening follow-up          | deferred |              |
| 12  | Row grid                               | ✓ done   | f7434e5      |
| 12.5| Row grid hardening follow-up           | deferred |              |
| 13  | SQL editor                             | ✓ done   | 74a8f8b      |
| 13.5| SQL editor hardening follow-up         | deferred |              |
| 14  | Inspector pane                         | ✓ done   | 3fd7761      |
| 14.5| Inspector hardening follow-up          | deferred |              |
| 15  | Command palette + history UI           | ✓ done   | fe58825      |
| 15.5| Palette + history hardening follow-up  | deferred |              |
| 16  | Export                                 | ✓ done   | b7d5cad      |
| 16.5| Export hardening follow-up             | deferred |              |
| 17  | Adminer removal + cutover              | ✓ done   | 2c43e2d      |
| 18  | Release v0.3.0                         | ✓ done   | (this tag)   |

This table is the single source of truth for v0.3.0 progress.
