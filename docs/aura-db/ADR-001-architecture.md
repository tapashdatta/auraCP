# ADR-001 — Aura DB Architecture

**Status:** Accepted (pre-implementation; binding on v0.3.0+).
**Date:** 2026-05-29.
**Authors:** auraCP maintainers.
**Supersedes:** none.
**Superseded by:** none.

**Companions:**
- `docs/aura-db/SECURITY.md` — the security model (canonical).
- `docs/aura-db/SDK.md` — embedding interfaces (Auth / ConnectionStore /
  AuditSink).

---

## 1. Context

auraCP currently ships **Adminer** as its database management UI, integrated
behind the panel's SSO at `/_adminer/`. Adminer is small, lightweight, and
covers MariaDB + PostgreSQL in one binary — properties that fit auraCP's
lightweight motto.

Operators have flagged that Adminer's UX is significantly behind alternatives
they're used to:

- Server-rendered HTML forms (no SPA fluidity, full-page POST per action).
- No SQL editor with syntax / autocomplete / multi-tab results.
- No query history.
- No ER / designer view.
- No visual EXPLAIN.
- Limited cross-engine consistency (MariaDB vs Postgres views differ).
- Information density poor by modern standards.

Available alternatives have been evaluated:

| Tool             | Lightweight fit                       | UX ceiling | Decision                  |
| ---------------- | ------------------------------------- | ---------- | ------------------------- |
| **phpMyAdmin**   | Same FPM-on-demand model as Adminer (≈0 RAM idle); ~70 MB disk | Marginally better than Adminer; MariaDB-only | Rejected — disk cost + only solves half (no Postgres) |
| **pgAdmin 4 (web)** | Python + gunicorn persistent daemon, ~200-300 MB RAM idle | Excellent for Postgres   | Rejected — fundamentally incompatible with the motto |
| **CloudBeaver**  | Java/Eclipse RCP backend, persistent JVM, ~500 MB RAM idle | Excellent (multi-engine) | Rejected — JVM cost |
| **DBeaver desktop** | N/A (operator-side)                | Excellent                | Available as a separate workflow; doesn't replace the panel-side need |

No off-the-shelf tool combines: zero idle cost, native panel integration,
modern UX, multi-engine consistency. **Building our own is the only path
that satisfies all three.**

The product name for this new tool is **Aura DB**.

---

## 2. Decision

Build **Aura DB**, a database administration tool for MariaDB / MySQL and
PostgreSQL, distributed in two deployment modes from a single Go module:

1. **Integrated** — embedded into `auracpd`; surfaces as a tab in the panel
   at `/dbadmin`. Reuses panel auth, audit, connection store.
2. **Standalone** — a separate `aura-db` binary with its own user database,
   audit log, secret store, and HTTPS listener.

The Adminer integration is **removed** in v0.3.0 (cutover release). Operators
who still want Adminer can install it themselves; auraCP no longer ships it.

### 2.1 Scope of v0.3.0 (the cutover release)

In:

- Connection management (multi-server, profile-based, tag-driven policy).
- Schema browser (tree, lazy-loaded, search-as-you-type, type-aware icons).
- Row grid (virtual-scrolled, paginated, sortable, per-column filter, click-
  to-edit, double-click modal editor with type-aware inputs).
- SQL editor (Monaco, lazy-loaded, schema-aware autocomplete, per-statement
  result tabs).
- Query history (per-user, full-text searchable, SQLite-backed).
- Schema inspector (right pane: columns, indexes, FKs, triggers, DDL).
- Import/export (CSV, JSON, SQL, Markdown).
- Keyboard-first navigation (command palette, table jump, run, close,
  export).
- Audit log integration (every state change recorded).
- Full RBAC per §6 of SECURITY.md.
- Statement classifier with hard-forbidden list (no override).
- Adminer removal (binary, FPM pool, SSO wrapper, all related paths).

Out (deferred to later releases):

- v0.3.1: ER diagram, visual EXPLAIN flame-tree, slow-query stream, saved
  queries, schema diff.
- v0.3.2: Live multi-row edit, import wizard, replication/WAL inspector,
  user & permission UI, optional AI assist (BYO key).
- v0.4.0+: row-level redaction, JIT DB accounts, OIDC SSO for standalone,
  anomaly detection.

---

## 3. Architecture

### 3.1 Package layout

```
pkg/dbadmin/                          (public, importable)
  ├─ engine.go                        Engine struct + Handler()
  ├─ types.go                         User, Connection, Action, Event
  ├─ classifier/
  │   ├─ mysql.go                     Vitess-backed
  │   ├─ postgres.go                  pg_query_go-backed
  │   ├─ forbidden.go                 the non-negotiable list
  │   └─ testdata/forbidden/          CVE corpus + fuzzing inputs
  ├─ driver/
  │   ├─ mysql.go                     go-sql-driver/mysql wrapper
  │   ├─ postgres.go                  pgx/v5 wrapper
  │   └─ tunnel.go                    SSH tunneling for remote DBs
  ├─ schema/
  │   ├─ mysql.go                     information_schema reader
  │   └─ postgres.go                  pg_catalog reader
  ├─ rows.go                          paginated SELECT + parameterized UPDATE/DELETE
  ├─ explain.go                       EXPLAIN normalizer (both engines)
  ├─ history.go                       query history (SQLite-backed)
  ├─ export.go                        CSV / JSON / SQL / Markdown emitters
  └─ standalone/
      ├─ auth.go                      argon2id + TOTP impl of dbadmin.Auth
      ├─ store.go                     SQLite impl of dbadmin.ConnectionStore
      └─ audit.go                     file+chain impl of dbadmin.AuditSink

internal/api/dbadmin.go              auraCP-side glue:
                                       - bridges dbadmin.Auth → panel session
                                       - bridges dbadmin.ConnectionStore → panel `databases` table
                                       - bridges dbadmin.AuditSink → panel `audit_log` table
                                       - mounts dbadmin.Engine.Handler() at /api/dbadmin/*

cmd/aura-db/                          standalone binary
  └─ main.go                          composes dbadmin.Engine + standalone impls
                                      + embeds the Svelte UI

web/src/screens/dbadmin/             Svelte 5 SPA module
  ├─ DbAdmin.svelte                   shell (3-pane)
  ├─ panes/                           ConnectionTree, SchemaTree, TabStrip, Inspector
  ├─ workspace/                       RowGrid, SqlEditor, ResultPanel, ExplainTree, ErDiagram, DiffView
  ├─ lib/                             palette, keymap, pool, types
  └─ icons/                           custom 16px SVGs
```

### 3.2 Two binaries, one codebase

```
                          pkg/dbadmin
                          (engine + drivers + classifier)
                                 │
                ┌────────────────┴────────────────┐
                │                                 │
       internal/api/dbadmin.go            cmd/aura-db/main.go
       (panel glue, uses panel auth)      (uses pkg/dbadmin/standalone)
                │                                 │
                ▼                                 ▼
           auracpd binary                  aura-db binary
           (integrated mode)               (standalone mode)
```

Both binaries embed the **same** Svelte SPA via `go:embed`. The frontend
is bit-for-bit identical between modes; only the backend glue differs.

### 3.3 Three embedding interfaces

The engine consumes three interfaces. Implementations are pluggable. The
panel provides one set; standalone provides another. Operators / third
parties can provide their own. Full reference in `SDK.md`.

```go
// Auth: who is the operator + what may they do?
type Auth interface {
    Authenticate(*http.Request) (User, error)
    HasPermission(User, ConnectionID, Action) bool
    StepUpRequired(Action) bool
    VerifyStepUp(*http.Request) error
}

// ConnectionStore: where do connection records live; how do we decrypt creds?
type ConnectionStore interface {
    List(context.Context, User) ([]Connection, error)
    Get(context.Context, ConnectionID) (Connection, error)
    Credentials(context.Context, ConnectionID) (Credentials, error)
    Save(context.Context, Connection, Credentials) (ConnectionID, error)
    Delete(context.Context, ConnectionID) error
}

// AuditSink: where do audit events go?
type AuditSink interface {
    Record(context.Context, Event)
}
```

### 3.4 Frontend layout (three-pane workspace)

```
┌─ 40px global bar ─────────────────────────────────────────────────────┐
│ ◧ Aura DB     conn: prod-mariadb · db: mysite_db   ● Connected  ⌘K ⊕ │
├──────────┬──────────────────────────────────────────────┬─────────────┤
│ Connect. │ ▦ users  ⌥ orders·SQL  ◐ EXPLAIN  + new tab │ Inspector   │
│  Tree    │ ╶─────────────────────────────────────────╴ │             │
│ (260 px) │                                              │ Columns     │
│          │     ┌─────────────────────────────────┐     │ Indexes     │
│ Schema   │     │  data grid | SQL editor |       │     │ FKs         │
│ Tree     │     │  result panel                   │     │ Triggers    │
│          │     │                                 │     │ DDL         │
│ History  │     └─────────────────────────────────┘     │ Stats       │
│          │     ╔═════════════════════════════════╗     │ (320 px)    │
│          │     ║ Results · 1,243 rows · 24ms     ║     │             │
│          │     ╚═════════════════════════════════╝     │             │
└──────────┴──────────────────────────────────────────────┴─────────────┘
```

- **Left pane (260 px):** Connection tree, schema tree (lazy-loaded),
  query history. Collapsible to a 48 px icon rail.
- **Center pane:** Tab strip + workspace. Tabs persist across reloads,
  reorderable, splittable.
- **Right pane (320 px):** Context inspector. Updates based on selection.
  Collapsible.

### 3.5 Visual language

| | |
|---|---|
| **Type** | Display: JetBrains Mono Variable for the data grid; Inter (or Geist) for chrome. Distinctive, not generic system stacks. |
| **Palette** | Dark-first. Near-black `#0E0F12` canvas, `#16181D` surface, `#1E2129` elevated. Accent: `#3DDC97` (saturated cyan-green) for actions, `#F0A458` (soft amber) for warnings. Light mode ships but dark is the photo-shoot. |
| **Density** | Three modes: Compact / Default / Spacious. Default is the screenshot. |
| **Iconography** | Custom 16 px stroke icons per object type. Drawn in-house, not Lucide. |
| **Motion** | Subtle. 120 ms ease-out on hover, 200 ms on tab switches. Skeleton-fill on grid pagination, no spinners. |
| **Empty states** | First-class, illustrated. Sets the product tone. |

---

## 4. Technology choices

### 4.1 Database drivers

- MariaDB / MySQL: **`github.com/go-sql-driver/mysql`**. Pure Go, no cgo,
  active maintenance, broadest MariaDB / MySQL version coverage.
- PostgreSQL: **`github.com/jackc/pgx/v5`**. Native protocol implementation
  (bypasses `database/sql` where useful for `EXPLAIN ANALYZE` and copy
  protocols). Faster than `lib/pq` and better feature coverage. Pure Go.

Rationale: pure Go = no cgo = trivial cross-compilation (amd64 + arm64,
the auraCP target matrix). Both drivers are actively maintained and have
strong CVE response track records.

### 4.2 SQL parsers (for the classifier)

- MariaDB / MySQL: **`vitess.io/vitess/go/vt/sqlparser`**. Vitess's parser
  is the most mature pure-Go MySQL parser available. Used in production
  at scale by Vitess itself and PlanetScale.
- PostgreSQL: **`github.com/pganalyze/pg_query_go/v5`**. Wraps libpg_query
  (PostgreSQL's actual parser, extracted as a C library). Embeds the C
  via cgo at build time on platforms with cgo enabled.

Rationale: §3 of SECURITY.md explicitly forbids regex SQL classification.
Real parsers are non-negotiable.

Trade-off accepted: `pg_query_go` is the one cgo dependency in the
project. We accept it because:

1. The alternative (regex-based classification) is the historical CVE
   pattern we're avoiding.
2. cgo is only invoked at build time, not at runtime.
3. The CGO_ENABLED=0 fallback uses a JSON-RPC sidecar for the rare host
   where cgo is unavailable.
4. ARM64 + amd64 (our targets) have first-class cgo support.

### 4.3 Frontend

- **Svelte 5** with runes. Already in the panel; no new framework.
- **Monaco editor** for SQL. Industry standard; large but lazy-loaded only
  when the SQL editor tab opens. Bundle impact: ~500 KB compressed for the
  Monaco core, loaded once and cached.
- **sql-formatter** (~30 KB) for SQL pretty-print.
- **dagre-d3** (~80 KB, v0.3.1) for the ER diagram.

No web framework. No CSS framework (the panel's design tokens are sufficient).
No state library beyond Svelte runes.

### 4.4 Crypto

- **AES-256-GCM** for credential encryption. Keys from the panel's
  secret box in integrated mode, from `/etc/aura-db/secret.key` in
  standalone.
- **argon2id** for password hashing (standalone only). Parameters per §5.2
  of SECURITY.md.
- **`golang.org/x/crypto/ssh`** for SSH tunneling.

### 4.5 Data store (standalone mode)

- SQLite via `modernc.org/sqlite` (pure Go, no cgo). Schema:
  - `users` (id, username, password_hash, mfa_secret, recovery_codes, …)
  - `connections` (id, name, engine, host, port, database, username,
    creds_enc, tags, created_at, …)
  - `connection_grants` (user_id, connection_id, role)
  - `query_history` (id, user_id, connection_id, statement, parameters_redacted, …)
  - `saved_queries` (v0.3.1)
  - `sessions` (id, user_id, expires_at, ip_class, ua_hash, …)
  - `step_up_flags` (session_id, action_class, expires_at)
- Audit log lives in `/var/lib/aura-db/audit.log` (NDJSON, append-only, see
  SECURITY.md §9). Not in SQLite — separate file makes append-only easier
  to enforce and easier to ship to a SIEM.

### 4.6 What we deliberately don't use

- No web framework (Gin/Fiber/Echo). `net/http` + `http.ServeMux` is enough.
- No ORM. Plain `database/sql` (or pgx native) with parameterized queries.
- No GraphQL. REST + WebSocket where streaming is needed (slow-query tail).
- No JS bundler beyond Vite (already used). No state management library.
- No CSS-in-JS, no Tailwind. Plain CSS + design tokens.

Each "no" is intentional — every framework adds dependency surface,
build complexity, and learning curve. The simplicity is the feature.

---

## 5. Integration with auraCP

When built into the panel, Aura DB weaves into existing systems instead of
sitting beside them.

### 5.1 Auth

- Panel session = Aura DB session. No second login.
- Panel `users` table = Aura DB users. No second user database.
- Aura DB grants stored in a new `dbadmin_grants` table joined on
  `users.id` + `connections.id`.
- Step-up uses the panel's existing MFA verification flow.

### 5.2 Connection discovery

- Sites with databases (created via Site → Databases) appear in Aura DB's
  connection list automatically. Display name: `<site-domain> · <engine>`.
- Right-click a database in the site detail screen → "Open in Aura DB"
  deep-links to the schema browser.
- Default grant on auto-discovered connections: `dba` for the site owner.
- Tagging: auto-discovered connections from `cp.<panel-domain>.tld` etc.
  are tagged `dev` by default; operators can promote to `prod` via the
  connection settings UI (with step-up).

### 5.3 Lifecycle coupling

- Delete a site → its database goes away → the connection is removed from
  Aura DB automatically.
- Rename a site → the connection's display name and `default_schema`
  re-key.
- Future: migrate a database between engines → connection updates engine
  type, schema-cache flushes.

### 5.4 Backup integration

- The panel already has backup machinery (`internal/backup/`).
- Aura DB's connection inspector shows "last backup Xh ago, restore →".
- Restore from inside Aura DB calls into the panel's backup service.
  One workflow, no UI duplication.

### 5.5 Audit unification

- Aura DB events flow into the panel's `audit_log` table.
- The panel's existing `auracp doctor --audit` (if added) surfaces DB-admin
  activity alongside site lifecycle events.

### 5.6 Theme & UX shared infrastructure

- Aura DB extends the panel's design tokens (`--ink`, `--surface-*`, etc.).
- The toast system (`web/src/lib/toast.svelte.js`), dialog system
  (`web/src/lib/dialog.svelte.js`), and command palette infrastructure
  are shared.
- The panel's top nav adds a "Databases" tab when at least one connection
  exists.

### 5.7 TLS

- Aura DB uses the panel's LE-managed cert. No separate cert plumbing.
- URL: `https://<panel-domain>/dbadmin`. One DNS record, one cert.

### 5.8 Adminer removal

- v0.3.0 installer removes `/opt/auracp/adminer/`, the
  `auracp-adminer.sock` FPM pool, the SSO wrapper PHP, and the
  `/_adminer/` route from the panel vhost template.
- Migration path: any Adminer "open" deep-link in the panel UI is
  redirected to Aura DB's equivalent route.
- No data migration required — Adminer was stateless; its only state
  (sessions) is invalidated on cutover.

---

## 6. Distribution

### 6.1 Integrated mode

- Aura DB ships **inside the existing auracp .deb** as of v0.3.0. No new
  package, no new daemon, no new systemd unit.
- The auracpd binary grows by ~5–8 MB (driver + parser code + a few KB
  of frontend assets — the heavy Monaco stays lazy-loaded over HTTP).
- Startup cost: <100 ms additional for engine boot (connection schema
  reads happen on first connection-open, not at startup).

### 6.2 Standalone mode

- New artifact: `aura-db_<version>_<arch>.deb` + `.rpm` + raw binary
  + container image `auracp/aura-db:<version>`.
- Installer: `curl -fsSL https://aura-db.io/install.sh | sh` (style
  matches auraCP).
- systemd unit: `aura-db.service`, runs as `aurabd:aurabd`.
- Default listener: `0.0.0.0:8090` (HTTPS only, lego-managed cert by
  default; operator can supply PEM files).
- First-run wizard at `https://<host>:8090/setup`:
  1. Create owner account (username, password, mandatory TOTP
     enrollment).
  2. Optional: paste a panel-domain to auto-issue an LE cert.
  3. Optional: configure audit forwarding.
  4. Single-use setup token printed to stdout on first launch; required
     to access /setup. Token is regenerated on every restart until the
     first account is created.

### 6.3 Container image

- Multi-stage build, distroless base (`gcr.io/distroless/static-debian12`).
- Final image ~30 MB compressed.
- Volumes: `/var/lib/aura-db` (state), `/etc/aura-db` (config + secret
  key). Both must be persistent volumes; ephemeral container = data loss.
- Default port: 8090 (HTTPS). Operators put their own reverse proxy in
  front if needed.

### 6.4 Releases

- Aura DB shares the auraCP release cadence in integrated mode (versions
  match: auracp 0.3.0 = aura-db 0.3.0).
- Standalone has its own release tag for clarity: `aura-db/v0.3.0`.
- All releases signed via sigstore (see SECURITY.md §11.4).

---

## 7. Consequences

### 7.1 Positive

- **UX leap forward.** Operators stop apologizing for the DB admin UX.
  Visual EXPLAIN + ER diagram + schema diff are differentiators against
  every alternative.
- **Resource cost stays at motto-compatible levels.** Pure Go, no daemons,
  on-demand connections. Idle = 0 MB above the existing panel.
- **Surface reduction.** Removing the Adminer integration eliminates: a PHP
  dependency, an FPM pool we maintain, the SSO wrapper PHP, the
  `/_adminer/` route, the Adminer 4.x→5.x upgrade dance. Net code
  reduction.
- **Security improvement.** Aura DB has a defined threat model
  (SECURITY.md); Adminer has CVE history we inherit. Modern controls
  (step-up, classifier, audit chain) raise the floor.
- **New product surface.** Standalone Aura DB is a sellable / shareable
  thing in its own right. Many shops want a CloudBeaver alternative
  without the JVM cost.
- **Cross-engine consistency.** One UI, one keymap, one mental model for
  both MariaDB and Postgres — neither phpMyAdmin nor pgAdmin can offer
  that.

### 7.2 Negative

- **Substantial implementation cost.** ~6-8 weeks of focused work to
  reach v0.3.0 quality. We're absorbing the engineering cost of a
  category leader.
- **Maintenance liability.** Every CVE in the SQL parser, the driver,
  the frontend dependencies is now ours. Adminer was someone else's
  problem.
- **cgo dependency** (pg_query_go). One blot on the otherwise pure-Go
  story. Mitigated by JSON-RPC sidecar fallback for cgo-disabled hosts.
- **Frontend complexity.** Monaco + the grid + the EXPLAIN tree are
  non-trivial. Bundle size grows from ~200 KB gzipped to ~250 KB
  gzipped (excluding lazy-loaded Monaco).
- **Operator migration cost.** Operators with muscle memory in Adminer
  need to relearn the UI. We mitigate with a short Aura DB tour on
  first launch + a "command palette tour" surfacing keyboard shortcuts.
- **Customer support surface.** Aura DB-specific bug reports replace
  Adminer-specific ones. Net change probably neutral; new bugs but no
  more upstream cherry-picks.

### 7.3 Risks accepted

- **Schedule risk.** 6-8 weeks is optimistic if scope creeps. Mitigation:
  hard scope boundary on v0.3.0; differentiators move to v0.3.1.
- **Quality risk.** First release of a new product is rarely great.
  Mitigation: replace Adminer only when Aura DB has feature parity with
  Adminer for the operations Adminer was actually used for (schema
  browse, row edit, SQL run). The cherry features come later.
- **Driver / parser CVE surface.** Mitigation: §11.5 of SECURITY.md
  defines quarterly review + fuzzing + CVE corpus regression tests.

---

## 8. Alternatives considered

### 8.1 "Keep Adminer + theme it harder"

We already do this partially. Ceiling is "Adminer but pretty." Doesn't
close the operator UX gap. Effort budget could yield ~1 month of
polish before hitting structural limits. Rejected because the UX
ceiling is too low.

### 8.2 "Ship phpMyAdmin (MySQL) + pgAdmin (Postgres)"

phpMyAdmin: works (FPM-on-demand model, ~0 RAM idle, ~70 MB disk).
pgAdmin: ~200-300 MB RAM idle. Combined: motto-violating.

Even if pgAdmin were free, the dual-tool UX is worse than Adminer's
single-tool UX. Operators would have to learn two UIs. Rejected.

### 8.3 "Embed CloudBeaver"

CloudBeaver is GPLv2 + has good UX. But:
- JVM = ~500 MB RAM idle. Motto-violating.
- Separate process = separate auth = separate audit. Defeats panel
  integration.
- License terms restrict redistribution in a packaged product.

Rejected.

### 8.4 "Build Aura DB but reuse a server-side framework"

Considered Fiber, Echo, Gin. Each pulls 50-100 deps including loggers,
JSON parsers, validators. The benefit (routing, middleware) is small
relative to `net/http` + `http.ServeMux` for a single-route mount point.
Rejected for dependency surface.

### 8.5 "Build Aura DB in Rust"

Considered. Rust gives memory safety + performance + small binary.
Costs: new language for the project (existing auraCP team is Go-shop),
no shared code with the panel, cross-compile to ARM64 harder, no path
to share types with the existing Svelte frontend.

Go is sufficient. Pure-Go drivers are battle-tested. The classifier is
the only place performance matters and Go is plenty fast there.
Rejected.

### 8.6 "Defer it — patch Adminer better instead"

Considered as a fallback if engineering bandwidth doesn't materialize.
Tracked as Plan B; if v0.3.0 slips past 12 weeks, we ship a
heavily-themed Adminer with a "Aura DB coming in v0.4.0" banner and
re-evaluate.

---

## 9. Phased timeline

### Phase 0 — Foundation (this set of docs, 1 week)

- SECURITY.md ✓ (this commit's predecessor)
- ADR-001-architecture.md ← this doc
- SDK.md — interface contracts
- (No code changes.)

### Phase 1 — v0.3.0 (weeks 1-3 of implementation)

- `pkg/dbadmin/` scaffold: types, interfaces, classifier, drivers, schema
  readers, history.
- `internal/api/dbadmin.go` glue with panel auth + audit.
- Svelte SPA module: shell, panes, row grid, SQL editor, results, history,
  inspector, command palette.
- Adminer removal: installer, vhost template, SSO wrapper, FPM pool.
- Tests: unit (classifier 100% line coverage on the forbidden corpus),
  integration (real MariaDB + Postgres in CI), fuzz (classifier).
- Docs: user-facing `docs/aura-db/USING.md`, operator-facing
  `docs/aura-db/OPERATING.md`.
- **Cutover:** v0.3.0 release replaces Adminer.

### Phase 2 — v0.3.1 (weeks 4-5)

- ER diagram (auto from FK metadata).
- Visual EXPLAIN flame-tree (the big differentiator).
- Slow-query stream (WebSocket).
- Saved queries (named, tagged, parameterized).
- Schema diff (cross-connection).

### Phase 3 — v0.3.2 (weeks 6-8)

- Live multi-row edit (Excel-feel).
- Import wizard (CSV / JSON / SQL with guided mapping).
- Replication / WAL inspector.
- User & permission UI (visual grant matrix).
- Backup restore from inside Aura DB.
- Optional AI assist (BYO operator key, disabled by default).
- HSM / KMS support for the encryption key (SECURITY.md §15 deferred item).

### Phase 4 — Standalone GA

- `cmd/aura-db/` binary + installer + container image.
- First-run wizard.
- Config import/export from a panel-mode install.
- aura-db.io landing page + docs.

Total: ~8 weeks for the full vision; v0.3.0 ships in 3.

---

## 10. Decision-record protocol

Future architectural changes that affect Aura DB are recorded as
additional ADRs in `docs/aura-db/ADR-<NNN>-<slug>.md`. Each:

- States its status (Proposed / Accepted / Superseded).
- Cites the ADRs it supersedes (if any).
- Updates SECURITY.md / SDK.md if it changes a published surface.

`docs/aura-db/INDEX.md` is the index of ADRs and will be created when we
have a second one.

ADRs are non-negotiable. A code PR that contradicts an accepted ADR is
blocked at review. To change direction, propose a superseding ADR first.

---

*Decision recorded 2026-05-29. Binding on v0.3.0+. Subject only to
amendment via a superseding ADR.*
