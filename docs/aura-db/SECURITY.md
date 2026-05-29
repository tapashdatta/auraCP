# Aura DB — Security Model

**Status:** authoritative. This document defines the security architecture for
Aura DB. Every other design decision must be consistent with it; if a feature
spec contradicts this document, the document wins until this document is
explicitly revised.

**Version:** v0.1 (pre-implementation). Will be re-versioned with every Aura DB
release that changes a control listed below. Past versions are kept in git
history; the canonical version is whatever is at `main`.

**Audience:** auraCP maintainers, security reviewers, operators evaluating
Aura DB for production, and external contributors. This document is public.

**Companions:**
- `docs/aura-db/ADR-001-architecture.md` — the architectural decision record.
- `docs/aura-db/SDK.md` — the embedding interfaces (Auth / ConnectionStore /
  AuditSink) referenced throughout this document.
- `SECURITY.md` (repo root) — disclosure policy and contact details.

---

## 1. Scope

Aura DB is a web-based database management tool for MariaDB / MySQL and
PostgreSQL. It ships in two deployment modes from a single codebase:

- **Integrated** — embedded into `auracpd` (the auraCP control panel). Auth,
  audit, and connection metadata come from the panel.
- **Standalone** — a separate `aura-db` binary with its own user database,
  audit log, and secret store.

This document covers controls that apply to **both** deployment modes. Where a
control differs between modes, both variants are specified explicitly.

### In scope

- Authentication of operators to Aura DB.
- Authorization of operator actions against database connections.
- Execution boundary between operator-supplied SQL and target databases.
- Confidentiality of stored credentials, query results in transit, and the
  Aura DB session itself.
- Integrity of the audit trail.
- Defense against the standard web-app threat surface (XSS, CSRF, SSRF, click-
  jacking, open redirect, prototype pollution, supply-chain injection).
- Defense against database-specific abuse (RCE via `LOAD_FILE`, SSRF via
  `COPY FROM PROGRAM`, etc.).

### Out of scope

- Securing the database server itself. Operators are responsible for MariaDB /
  PostgreSQL hardening (network exposure, account hygiene, OS-level patching).
- Securing the host on which Aura DB runs. Operators are responsible for OS
  hardening, kernel patching, firewall configuration. (In integrated mode,
  auraCP's `install_security` step does this for the panel host.)
- Application-level encryption of data **inside** target databases. If a row
  contains a credit card, Aura DB faithfully renders it; encrypting that row
  is the application owner's responsibility.
- Protecting against an attacker with root on the Aura DB host. Root can read
  `/etc/aura-db/secret.key`. We do not defend against this; we make it
  auditable.

---

## 2. Threat model

### 2.1 Assets, in priority order

1. **Production database contents.** Customer PII, financial records, etc.
   Aura DB sits between an operator and these. Compromise → data breach.
2. **Database credentials.** Encrypted at rest in Aura DB's connection store.
   Compromise → unfettered direct database access, bypassing every audit
   control.
3. **Operator session.** Compromise → attacker assumes the operator's role.
4. **Audit log.** Compromise (modification or deletion) → undetected breach,
   incident response defeated.
5. **Aura DB binary integrity.** Compromise (e.g., via supply chain) → all of
   the above, undetectably.

### 2.2 Threat actors

| Actor                                | Capability                                                                              | Primary mitigation surface                                |
| ------------------------------------ | --------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| **External unauthenticated**         | Network reach to Aura DB's HTTPS endpoint. No credentials.                              | Auth gateway, TLS, CSP, rate limit, no info disclosure.   |
| **External credential-stuffer**      | Lists of leaked username/password pairs.                                                | Argon2id, lockout, MFA mandatory for write roles.         |
| **Authenticated `viewer`**           | Valid session, read-only on assigned connections.                                       | Role enforcement, query classifier, no privilege leak.    |
| **Authenticated `writer` / `dba`**   | Valid session, write or DDL on assigned connections.                                    | Statement classifier, step-up auth, audit, dangerous-op gate. |
| **Compromised operator browser**    | XSS, malicious extension, hostile workstation. Session token + DOM access.              | Strict CSP, session binding, MFA on dangerous ops, short TTL. |
| **Compromised target database**      | Hostile responses (oversized, malformed, byte-pattern attack on the driver).             | Pinned drivers, output caps, no driver-side eval, fuzzing. |
| **Insider operator with audit-log access** | Wants to cover tracks.                                                            | Append-only log, cryptographic chaining, optional offsite. |
| **Supply-chain attacker**            | Compromises a Go module / npm package / GitHub Action.                                  | Pinned deps, SBOM, sigstore signing, reproducible builds. |
| **Root on Aura DB host**             | Full filesystem, can read `secret.key`, can patch the binary.                           | **Out of scope.** Detection only via remote audit shipping. |

### 2.3 Non-goals

- Cryptographic protection against a malicious co-tenant on the host. If you
  share the host with hostile code, that code can read `/proc/self/mem` of
  Aura DB and steal everything it has decrypted. Run Aura DB on a dedicated
  host or under hardened isolation (systemd MAC, namespaces) if this is in
  your threat model.
- Defense against firmware / hardware compromise. We do not perform measured
  boot or remote attestation.
- Mitigation of denial-of-service from a privileged authenticated user. A
  `dba` can issue an expensive query; the resource limits in §6.5 cap the
  blast radius but do not prevent the operator from doing it.

---

## 3. Security principles

These are the rules every feature decision is checked against. If a feature
needs to break a principle, it goes through an ADR with a documented
exception and a compensating control.

1. **Secure by default.** Unsafe options ship disabled. The first-run
   configuration is the secure configuration. Every relaxation requires an
   explicit operator action, logged.
2. **Least privilege, per resource.** A user's authority is a function of
   `(user, connection, action)`. There is no global "admin" — being `owner`
   on one connection grants nothing on another.
3. **No SQL regex.** SQL classification uses a real parser. Adminer and
   phpMyAdmin shipped CVEs for years by regexing SQL; we do not repeat that.
4. **Parse, don't sanitize, user input.** All cell rendering, query
   parameters, and connection strings are parsed into typed structures before
   any use. No `string` ever reaches an `os.Exec` argument.
5. **Defense in depth.** Every control assumes the layer above failed. The
   server validates auth even after the proxy required it. The classifier
   validates queries even after the role allowed them. The output renderer
   escapes even after the column type says it's safe.
6. **Auditable by construction.** Every state-changing action emits an audit
   event before the action begins. If the action subsequently fails, the
   event is updated, not deleted.
7. **No hidden persistence.** Aura DB's state lives in exactly two places:
   the connection store (SQLite or panel DB) and the audit log. No
   `/tmp/aurabd-cache-*`, no implicit logging into stderr, no shadow copies.
   An operator can `cat` both files and know what we know.
8. **Confidentiality is a server property.** Sensitive material (credentials,
   query parameters for `CREATE USER`, etc.) must never reach the browser. The
   server executes; the browser only sees the result.

---

## 4. Trust boundaries

```
       ┌───────────────────────────────────────────────────────────┐
       │  Operator's browser                                       │
       │  (untrusted code execution surface — XSS, extensions)     │
       └───────────────────────┬───────────────────────────────────┘
                               │ TLS 1.3, mTLS optional
                               │ Strict CSP, SameSite=Strict cookies
       ┌───────────────────────┴───────────────────────────────────┐
       │  Reverse proxy / nginx  (auracpd integrated mode)         │
       │  or aura-db binary's own listener (standalone mode)       │
       │  - terminates TLS                                         │
       │  - rate-limits                                            │
       │  - enforces HSTS, CSP                                     │
       └───────────────────────┬───────────────────────────────────┘
                               │ loopback, unix socket, or in-proc
       ┌───────────────────────┴───────────────────────────────────┐
       │  Aura DB engine (pkg/dbadmin)                             │
       │                                                           │
       │   ┌─────────────┐   ┌──────────────┐   ┌──────────────┐  │
       │   │   Auth      │   │   Classifier  │   │   Audit      │  │
       │   │   provider  │   │   (real SQL   │   │   sink       │  │
       │   │             │   │    parser)    │   │              │  │
       │   └─────────────┘   └──────────────┘   └──────────────┘  │
       │                              │                            │
       │                              │ parameterized exec         │
       └──────────────────────────────┼────────────────────────────┘
                                      │ TLS 1.2+ required for prod
                                      │ SSH tunnel optional
       ┌──────────────────────────────┴────────────────────────────┐
       │  Target database  (MariaDB / Postgres)                     │
       │  May be malicious — drivers validated, output capped.      │
       └────────────────────────────────────────────────────────────┘
```

Each `┴` is a trust boundary. Data crossing one is validated, capped, or
typed. Credentials cross **only** the engine→database boundary; they never
exist in the browser.

---

## 5. Authentication

### 5.1 Identity sources

| Mode         | Provider              | Notes                                                  |
| ------------ | --------------------- | ------------------------------------------------------ |
| Integrated   | auraCP panel sessions | 1:1 mapping panel user → Aura DB user. No second login. |
| Standalone   | Local user DB         | argon2id passwords, mandatory MFA for write roles.     |

In both modes, the engine consumes `dbadmin.Auth` (see `SDK.md`). The
integrated and standalone implementations differ only in the backing store.

### 5.2 Password storage (standalone only)

- Algorithm: **argon2id** with parameters `t=3, m=64MiB, p=2`, 16-byte salt,
  32-byte tag. These parameters are tunable per release; tuning bumps the
  password format version field and triggers transparent re-hashing on next
  successful login.
- Plaintext passwords are never logged, echoed, or written to disk. The
  password input flows: request body → argon2id verify → discarded.
- Minimum length: 14 characters. No composition rules. We do not enforce
  character classes (NIST SP 800-63B); we enforce length.
- Passwords are checked against `haveibeenpwned.com/range/<hash-prefix>` on
  set, using k-anonymity (only the SHA-1 prefix is sent). Operator can
  override per-user with a documented reason.

### 5.3 MFA

| Trigger                         | MFA requirement                            |
| ------------------------------- | ------------------------------------------ |
| Initial sign-in                 | Required for `writer`+ roles on any conn.  |
| Sign-in for `viewer`-only users | Optional, encouraged.                      |
| Any step-up event (§5.5)        | Required regardless of role.               |

Supported factors:

- **WebAuthn / passkeys** (primary). Resident keys preferred; non-resident
  accepted.
- **TOTP** (fallback). 30-second window, 6 digits, ±1 step tolerance.
- **Recovery codes**. 8 single-use codes generated at enrollment, displayed
  once. Used only when both WebAuthn and TOTP are unavailable.

Hardware tokens (U2F over CTAP1) are accepted via WebAuthn's compatibility
mode.

### 5.4 Sessions

- Cookie name: `aura_session`. Flags: `Secure`, `HttpOnly`, `SameSite=Strict`,
  `Path=/`. No domain attribute (host-only).
- Token: 256-bit random, base64url-encoded. Server-side state in the panel
  session store (integrated) or `aurabd-sessions` table (standalone).
- TTL: **15 minutes idle, 8 hours absolute.** Idle resets on any
  authenticated request; absolute does not. After 8 hours: re-auth required,
  including MFA if the role requires it.
- Bound to: client IP `/24` (IPv4) or `/56` (IPv6), and UA hash. Crossing the
  binding invalidates the session and triggers a re-auth with MFA.
- Concurrent sessions per user: capped at 5. Oldest evicted on the sixth
  sign-in (operator gets a notification in the UI).
- Sign-out invalidates the server-side session immediately. The cookie is
  cleared client-side. Stale cookies presented after sign-out fail closed.

### 5.5 Step-up authentication

Step-up = a fresh MFA verification at the time of a privileged action. It
does **not** create a new session; it sets a short-lived flag on the existing
session.

| Action                                                          | Step-up required | Flag TTL |
| --------------------------------------------------------------- | ---------------- | -------- |
| Viewing a connection's stored password                          | Yes              | 30s      |
| Saving a connection with `sslmode=disable`                      | Yes              | 60s      |
| Switching a `prod`-tagged connection from read-only to writable | Yes              | 5min     |
| Executing any `ddl` class query (`CREATE`, `ALTER`)             | Yes              | 5min     |
| Executing any `dangerous` class query (`DROP`, `TRUNCATE`, `GRANT`, `REVOKE`, `SET GLOBAL`) | Yes | 60s (per action) |
| Executing a `write-row-mass` query (DELETE/UPDATE without WHERE) | Yes              | 60s (per action) |
| Exporting > 100,000 rows                                        | Yes              | 5min     |
| Restoring from backup                                           | Yes              | 60s (per restore) |
| Modifying a user's role (owner only)                            | Yes              | 5min     |
| Changing audit-log forwarding configuration                     | Yes              | 60s      |

The step-up flag is **per-action class**, not global. Stepping up to run a
`DROP` does not authorize a subsequent `GRANT`.

### 5.6 Rate limiting & lockout

| Endpoint class                  | Per-IP limit                       | Per-user limit                      |
| ------------------------------- | ---------------------------------- | ----------------------------------- |
| `/login`, `/mfa/verify`         | 10 attempts / 15min, then 15min lockout | 5 attempts / 15min, then 15min lockout, page operator at 3x in 1h |
| `/api/dbadmin/*/query`          | 60 / minute burst, 600 / hour      | 30 / minute burst, 1800 / hour      |
| `/api/dbadmin/*` (other)        | 600 / minute                       | 300 / minute                        |
| WebSocket connect               | 30 / hour                          | 10 / hour                           |

Lockouts decay linearly: 15min → 30min → 1h → 2h → 4h → 8h → 24h.

After three lockouts of the same user account in a rolling 1-hour window,
Aura DB emits an `auth.lockout.escalated` audit event and, if configured,
delivers a webhook to the operator's incident channel.

---

## 6. Authorization

### 6.1 RBAC model

Authority is `(user, connection, role)`. There is no global admin. Roles are
strictly ordered and additive:

| Role       | Read schema | Read rows | Write rows  | DDL  | Manage conn-users | Manage connection itself |
| ---------- | ----------- | --------- | ----------- | ---- | ----------------- | ------------------------ |
| `viewer`   | ✓           | –         | –           | –    | –                 | –                        |
| `analyst`  | ✓           | ✓ (read)  | –           | –    | –                 | –                        |
| `writer`   | ✓           | ✓         | ✓ (with WHERE) | – | –                 | –                        |
| `dba`      | ✓           | ✓         | ✓           | ✓    | –                 | –                        |
| `owner`    | ✓           | ✓         | ✓           | ✓    | ✓                 | ✓                        |

A user with no grant on a connection has **no access**. The connection
doesn't appear in their list; the API returns the same 404 it would for a
nonexistent connection. No "you don't have access" responses, which would
leak existence.

### 6.2 Connection tags

Tags decorate connections with policy:

| Tag         | Effect                                                                                       |
| ----------- | -------------------------------------------------------------------------------------------- |
| `prod`      | Forces TLS to the DB. Read-only by default, even for `dba`. Writes require step-up + audit reason. Red strip in UI. |
| `staging`   | TLS to the DB required. Standard role enforcement. Amber strip.                              |
| `dev`       | No additional constraints. Green strip.                                                      |
| `4-eye`     | Adds a second-`owner` approval gate to every `dangerous` action. Action queues until approved or rejected (default 30min timeout). |
| `read-only` | Hard-locks the connection to read-only. Cannot be overridden by any role. Useful for replica connections. |

Tags are set at connection creation. Changing a tag from `dev` → `prod`
requires `owner` + step-up.

### 6.3 The query classifier

Every SQL statement is parsed by an engine-specific parser before execution:

- MariaDB / MySQL: `vitess.io/vitess/go/vt/sqlparser`
- Postgres: `github.com/pganalyze/pg_query_go/v5` (libpg_query bindings)

Parse failure → reject with a clear error. We do not "try our best to
guess." If we can't parse it, we won't run it.

#### 6.3.1 Classes

| Class              | Examples                                              | Default policy (per role) |
| ------------------ | ----------------------------------------------------- | -------------------------------------- |
| `read`             | `SELECT`, `SHOW`, `EXPLAIN`, `DESCRIBE`, `WITH`-RO    | `analyst`+                             |
| `write-row`        | `INSERT`, `UPDATE ... WHERE`, `DELETE ... WHERE`      | `writer`+                              |
| `write-row-mass`   | `UPDATE` / `DELETE` **without** WHERE; `TRUNCATE`     | `writer`+, **step-up + typed confirm** |
| `ddl`              | `CREATE`, `ALTER`, `DROP`, `RENAME`                   | `dba`+, **step-up**                    |
| `dangerous`        | `GRANT`, `REVOKE`, `SET GLOBAL`, `SET PERSIST`, `LOAD DATA INFILE`, `FLUSH PRIVILEGES`, `KILL`, replication commands | `owner` only, **step-up always** |
| `forbidden`        | See §6.3.2                                            | **Blocked. No override.**              |

A query with multiple statements is classified as the **strictest** class
among its statements. Mixed `SELECT; DROP TABLE foo;` is `ddl`-class
overall, requires step-up, and we surface both classifications in the
confirmation dialog so the operator understands what they're authorizing.

#### 6.3.2 The forbidden list

Refused at the parser level. No flag, no override, no operator escape hatch
in the UI. Operators who need these run `mysql` / `psql` over SSH (which
means they already have shell-level trust on the host).

**MariaDB / MySQL:**

- `LOAD_FILE(...)` in any expression context.
- `... INTO OUTFILE ...` / `... INTO DUMPFILE ...`.
- `LOAD DATA INFILE` from a server-side path. (Operator-uploaded CSV import
  uses Aura DB's own importer, which streams via stdin.)
- `SELECT ... INTO ...` with a file destination.
- Any reference to `sys_exec`, `sys_eval`, or other UDF functions matching
  a denylist regex on function names.
- Use of the `LOCAL_INFILE` client capability bit. Driver-level: we disable
  `allowAllFiles` and `allowCleartextPasswords` on the driver config.

**PostgreSQL:**

- `COPY ... FROM PROGRAM` / `COPY ... TO PROGRAM`.
- `COPY ... FROM '<path>'` / `COPY ... TO '<path>'` with a server-side path.
- `pg_read_file`, `pg_read_binary_file`, `pg_ls_dir`, `pg_stat_file`.
- `lo_import` / `lo_export` with a path argument.
- `CREATE EXTENSION ... FROM ...` (only `CREATE EXTENSION foo` is allowed,
  using the existing control file).
- `dblink_connect_u` (the unprivileged variant that ignores auth).
- Function definitions where `LANGUAGE` is `plpythonu`, `plperlu`, `plsh`,
  `plr`, or any other `*u` variant.

These rules are codified in `pkg/dbadmin/classifier/forbidden.go` and
covered by tests that pull from a curated CVE corpus.

### 6.4 Parameterization

The grid editor, schema inspector actions, and import wizard build queries
via prepared statements. SQL never enters those code paths as a string. The
following are the only places raw SQL is allowed:

1. The SQL editor (operator types it).
2. Saved queries with `:param` placeholders, where Aura DB substitutes
   parameters via the driver's parameterization API (not string interp).
3. Schema-DDL rendering (read-only).

The classifier runs against (1) and the placeholder-expanded form of (2).

### 6.5 Resource limits

| Limit                          | Default                | Configurable per | Hard ceiling |
| ------------------------------ | ---------------------- | ---------------- | ------------ |
| Query timeout                  | 30 seconds             | connection       | 5 minutes    |
| Result row cap                 | 10,000 rows            | connection       | 100,000      |
| Result byte cap                | 50 MB                  | connection       | 500 MB       |
| Concurrent queries per user per connection | 1            | global           | 3            |
| Per-user export rows           | 100,000                | global           | 10,000,000   |
| SQL editor input length        | 1 MiB                  | global           | 16 MiB       |
| Connection-pool size per conn  | 4                      | connection       | 16           |

Hitting a hard ceiling on configuration is a `permission denied`-class error
that requires `owner` + step-up to override (and the override is logged).

Timeouts are enforced via `context.WithTimeout` passed into the driver's
`QueryContext` / `ExecContext`. The driver MUST honor the context; both
`pgx` and `go-sql-driver/mysql` do. We test this with a deliberately slow
target.

### 6.6 The 4-eye gate

For connections tagged `4-eye`:

- A `dangerous`-class action does not execute immediately.
- It is queued with: requester, target, full statement, requester's stated
  reason (mandatory free-text field), timestamp.
- A different `owner` on the same connection must approve via the UI within
  the configured window (default 30 minutes).
- The approver can see everything the requester typed before approving.
- Approval triggers execution; rejection records the rejection and discards.
- Both requester and approver are recorded in the audit event.

A requester cannot approve their own action even if they hold multiple
owner grants. The implementation enforces this via `(approver_user_id !=
requester_user_id)` at the DB level, not in app code.

---

## 7. Credential management

### 7.1 At rest

- Connection credentials (username, password, optional client cert / key)
  are encrypted with AES-256-GCM.
- Key source:
  - Integrated mode: panel's `/etc/auracp/secret.key` (existing).
  - Standalone mode: `/etc/aura-db/secret.key`, generated at install time,
    mode `0600 root:root`, never world-readable.
- Each encrypted record has a unique nonce. Nonces are stored alongside the
  ciphertext, not derived from any input.
- Key rotation procedure documented in `docs/aura-db/KEY-ROTATION.md` (to
  be drafted in v0.3.1). Rotation re-encrypts all records and zeroes the
  old key in memory.

### 7.2 In flight (to the target database)

- Default driver TLS settings:
  - Postgres: `sslmode=require` minimum. `prod`-tagged: `sslmode=verify-full`
    with a per-connection CA pin.
  - MariaDB / MySQL: `tls=preferred` minimum. `prod`-tagged: `tls=true` with
    cert verification against system CA bundle or per-connection CA pin.
- We reject saving a connection where the user-supplied DSN contains
  `sslmode=disable` or `tls=false` (or equivalents) unless step-up + an
  explicit "I accept the risk" confirmation is provided. The exception is
  recorded in the audit log on every subsequent use of that connection.
- SSH tunneling is built in. Aura DB establishes the tunnel using
  `golang.org/x/crypto/ssh` with key-based auth only (no password auth, no
  agent forwarding). Tunnel keys live at `/etc/aura-db/keys/<conn>.key`,
  mode `0600 root:root`.

### 7.3 In the browser

- **Credentials never reach the browser.** Connection list responses include
  username (so the operator knows which account they're using) but **never**
  the password.
- "Show password" requires step-up. The password is delivered over a one-
  time signed URL that expires in 30 seconds and is invalidated after a
  single retrieval. The UI auto-redacts after 30 seconds even if not
  refreshed.
- Query parameters that the classifier identifies as credentials
  (the `IDENTIFIED BY` clause in `CREATE USER`, etc.) are redacted in:
  - The audit log.
  - The query history.
  - Server-side error messages.
- Connection import / export (`aura-db export-config`) writes credentials
  encrypted with a user-supplied passphrase (argon2id-derived key). The
  bundle is portable across hosts; the passphrase never persists.

---

## 8. Output rendering — XSS and content abuse

Every byte rendered in the browser is potentially attacker-controlled
(stored XSS from the database side). Defense:

### 8.1 Structural defenses

- Frontend is Svelte. Default text rendering escapes. **`{@html}` is
  banned** throughout `web/src/screens/dbadmin/`. CI enforces this with a
  lint rule that fails the build on any `@html` directive in that path.
- JSON columns render via a sandboxed tree component that walks parsed JSON
  and builds DOM nodes by type (`<span>` for strings, `<button>` for
  collapsible objects). No `innerHTML`. No `document.write`. No `eval`.
- BLOB / `bytea` columns render as hex (16 bytes / line, ASCII gutter, like
  `xxd`). Operator can request a download via a signed URL; the file is
  streamed with `Content-Disposition: attachment` and an opaque filename.
- URLs in cells render as plain text by default. An operator-settable per-
  column flag opts into clickable links, in which case anchors get
  `rel="noopener noreferrer nofollow"` and `target="_blank"`. Schemes are
  validated against `(http|https|mailto)`; everything else stays plain text.
- Image columns require explicit per-column opt-in. Rendered with
  `loading="lazy"` and a CSP `img-src` that names exactly the configured
  hosts.

### 8.2 Content Security Policy

```
default-src 'none';
script-src 'self' 'nonce-{nonce}';
style-src 'self' 'nonce-{nonce}';
img-src 'self' data:;
font-src 'self';
connect-src 'self';
frame-ancestors 'none';
form-action 'self';
base-uri 'none';
require-trusted-types-for 'script';
```

No `unsafe-inline`. No `unsafe-eval`. No `data:` script source. `script-src`
nonces rotate per response. `frame-ancestors 'none'` makes click-jacking
structurally impossible. `Trusted Types` rejects any DOM sink mutation
from non-vetted policy.

### 8.3 Other headers

- `Strict-Transport-Security: max-age=31536000; includeSubDomains; preload`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: same-origin`
- `Permissions-Policy: camera=(), microphone=(), geolocation=(), interest-cohort=()`
- `Cross-Origin-Opener-Policy: same-origin`
- `Cross-Origin-Embedder-Policy: require-corp`
- `Cross-Origin-Resource-Policy: same-origin`

### 8.4 CSRF

- Cookies are `SameSite=Strict`. This alone defeats classic CSRF.
- All state-changing requests also carry a CSRF token in the
  `X-Aura-CSRF` header, validated against a per-session token. Belt and
  braces.
- The CSRF token rotates on every step-up event.

### 8.5 Click-jacking

- `frame-ancestors 'none'` (above) refuses to be framed.
- The login page additionally sets `X-Frame-Options: DENY` for clients that
  don't honor CSP3.

### 8.6 Open redirect & SSRF

- The application has no operator-supplied redirect URLs. The "after login"
  destination is server-determined.
- The connection-test feature does NOT make outbound HTTP. It opens a TCP
  connection to the configured host:port and performs the database
  handshake — that's it. No URL fetching anywhere in Aura DB except
  haveibeenpwned (which is hard-coded to the canonical hostname and
  validated via TLS pinning of the public CA).

---

## 9. Audit log

### 9.1 What we log

Every state-changing or sensitive action emits an audit event. Read-only
operations are sampled (1% by default; configurable, can be 100%).

Schema:

```
event_id        ULID, monotonic
timestamp       RFC3339 with nanoseconds, UTC
user_id         opaque
user_role_at_time  string (the role granting the action)
source_ip       string
user_agent_hash string
action          enum (auth.login, auth.logout, auth.mfa.failed,
                      conn.create, conn.update, conn.delete,
                      conn.password.view, conn.role.grant,
                      query.read, query.write, query.ddl, query.dangerous,
                      query.forbidden.blocked, query.timeout, query.cap_exceeded,
                      audit.config.change, ...)
target          structured (connection_id, schema, object_name)
statement       string (redacted for dangerous param values)
parameters_redacted_json  string
result_rows     int
duration_ms     int
error           string (empty on success)
step_up_jti     string (when applicable)
prev_event_hash string (chain link — see §9.3)
```

### 9.2 Storage

- Integrated mode: panel's existing `audit_log` table (we extend its event
  enum). Append-only enforced at the application layer; the table has no
  `UPDATE` or `DELETE` grants for the `auracpd` DB user.
- Standalone mode: `/var/lib/aura-db/audit.log`, a newline-delimited JSON
  file, mode `0640 aurabd:aurabd-audit`. The `aurabd-audit` group has
  read-only access for log shippers. The audit file is opened with
  `O_APPEND` only; never `O_TRUNC`, never `O_WRONLY` without `O_APPEND`.

The audit file is rotated daily via the installer's standard logrotate
config. Rotated files are renamed `audit.log.YYYY-MM-DD`, gzipped after
24 hours, retained for 365 days, then deleted unless an offsite shipper
is configured.

### 9.3 Tamper evidence

Each event embeds the SHA-256 of the previous event. A `dba` with shell
access can edit `audit.log` arbitrarily; the chain breaks where they did
so. `aura-db audit verify` walks the chain and reports the first break
with timestamp + line number.

Optional remote attestation: every N events (default 1000) or every M
minutes (default 5), Aura DB computes the chain head, signs it with the
panel's secret key, and ships the signed head to a configurable endpoint
(e.g. syslog, S3, a SIEM webhook). An attacker who tampers with the local
audit log cannot tamper with the already-shipped head; reconstruction
proves the breach.

### 9.4 Reading the log

- UI: every connection has an "Audit" tab. Last 1000 events visible. Full
  log searchable via `aura-db audit search` (CLI).
- Filters: user, action, target, date range, result class.
- Exports: CSV / NDJSON / WORM-archive bundle (NDJSON + chain head + key
  fingerprint, all in a tar).
- Reading the audit log is itself audited. `audit.log.access` events
  record who read what range.

### 9.5 Forwarding

Optional, configured per-deployment:

- **Syslog (RFC 5424)** over TCP-TLS or UDP. Configurable host:port and
  facility.
- **Webhook** with HMAC-SHA256 signature using a per-endpoint shared
  secret.
- **S3 / S3-compatible** archive of rotated files, encrypted client-side
  with a customer-supplied key.

Forwarding is fail-safe: if shipping fails, the local audit log still
contains the events, and the forwarding goroutine retries with
exponential backoff. Forwarding never blocks the main request path.

---

## 10. Network & transport

### 10.1 Listener

- HTTPS only. HTTP listener (if configured) serves nothing but the ACME
  challenge path and a `301 → https` for everything else.
- TLS 1.3 only by default. TLS 1.2 with the MozillaIntermediate cipher set
  available as an explicit downgrade.
- Cert sources:
  - Integrated mode: panel's existing LE-managed cert.
  - Standalone mode: lego-managed LE (re-using the same library auracpd
    uses) or operator-supplied PEM files.
- Optional `mTLS` for additional auth: client certs validated against an
  operator-supplied CA bundle. When `mTLS` is enabled, a missing or
  invalid client cert is a 401 *before* any other auth runs.

### 10.2 Rate limiting (transport layer)

Implemented in front of the application:

- Per-IP connection rate: 100 new connections / minute, then queued.
- Per-IP request rate: §5.6 above.
- Slow-loris defense: idle connections > 30s closed. Header read timeout
  10s. Body read timeout: 60s for query endpoints, 30s otherwise.

### 10.3 No information disclosure

- Login responses are constant-time regardless of whether the username
  exists. We always compute argon2id against a dummy hash if the user is
  not found, then return the same `invalid credentials` response after a
  ±50ms jitter.
- Error responses do not include stack traces, file paths, library
  versions, or any internal identifier. They include only a sanitized
  message and a short opaque request ID.
- `/api/dbadmin/connections` returns only connections the user has a
  grant on. Listing a connection by ID directly returns 404 if the user
  has no grant, identical to a truly-nonexistent connection.
- The HTTP server banner is generic. `Server: aura-db` (no version). Tooling
  that wants to identify the exact version can hit `/version` and
  authenticate.

---

## 11. Supply chain

### 11.1 Dependencies

Aura DB's dependency surface is intentionally small:

**Go module (`pkg/dbadmin`):**

- Go stdlib
- `github.com/jackc/pgx/v5` (Postgres driver, pure Go)
- `github.com/go-sql-driver/mysql` (MariaDB / MySQL driver, pure Go)
- `vitess.io/vitess/go/vt/sqlparser` (MySQL parser)
- `github.com/pganalyze/pg_query_go/v5` (Postgres parser; embeds libpg_query)
- `golang.org/x/crypto/ssh` (SSH tunnel)
- `golang.org/x/crypto/argon2` (password hashing — standalone only)
- `github.com/oklog/ulid/v2` (audit event IDs)

That's it. No web framework, no ORM, no logger library, no JSON parser
beyond stdlib.

**Standalone binary (`cmd/aura-db`):**

- Above, plus what auracpd already uses for its standalone binary
  (acme/lego, embedded UI via `go:embed`).

**Frontend (`web/src/screens/dbadmin/`):**

- Svelte (already in the panel)
- Monaco editor (lazy-loaded only when the SQL editor opens)
- `sql-formatter` (pretty-print SQL, ~30 KB)
- `dagre-d3` (ER diagram layout, v0.3.1)
- A small custom icon set (drawn in-house, ~5 KB)

### 11.2 Pinning & verification

- `go.mod` and `go.sum` checked in. `GOFLAGS=-mod=readonly` in CI.
- `go.sum` verified via `go mod verify` in CI on every PR.
- npm: `package-lock.json` checked in. `npm ci` only (never `npm install`).
- `npm audit --omit=dev --audit-level=high` blocks the build.

### 11.3 SBOM

- Every release ships `aura-db.cdx.json` (CycloneDX) generated by
  `cyclonedx-gomod` + `cyclonedx-npm` + merged via `cyclonedx-cli`.
- SBOM signed with the same sigstore identity as the binary.

### 11.4 Signing & reproducible builds

- Release binaries are signed with sigstore (`cosign sign-blob`). The
  signing identity is a GitHub OIDC token from the `auracp/auracp` repo's
  `release` workflow, recorded in the public transparency log
  (`rekor.sigstore.dev`).
- Build reproducibility: `-trimpath -ldflags='-buildid='`, `SOURCE_DATE_EPOCH`
  honored, deterministic Go module ordering. Documented in
  `docs/aura-db/REPRODUCIBLE-BUILDS.md`. Independent reproduction is
  expected for every release; CI runs a second builder and bit-compares.

### 11.5 Audit cadence

- Quarterly internal review of:
  - Forbidden-list completeness against the public CVE corpus.
  - Classifier correctness via fuzzing (new corpora each quarter).
  - Dependency CVE list.
- Annual third-party audit (target: when product enters paid tier, before
  then community-driven).
- Findings disclosed publicly with the next release.

---

## 12. Disclosure & response

### 12.1 Reporting

- `SECURITY.md` at the repo root has the canonical reporting flow.
- Email: `security@auracp.io` (PGP key fingerprint published).
- GitHub Security Advisories: enabled, preferred channel for non-urgent.

We acknowledge receipt within 24 hours. Initial assessment within 72.

### 12.2 Embargo & coordinated disclosure

- 90-day default embargo from report.
- Extendable in 30-day increments by mutual agreement if a fix is
  genuinely in flight.
- We publish the advisory on the patched release's tag, with CVE assignment
  via GitHub Security Advisories. Credit to the reporter unless they
  request anonymity.

### 12.3 Incident response

If we detect a real exploit (not a theoretical finding):

1. Within 4 hours: assess scope, identify affected versions.
2. Within 24 hours: patch released or, if patching takes longer, a
   mitigation advisory (workaround, config change) goes out.
3. Within 7 days: post-mortem published with timeline, root cause,
   remediation, and prevention measures.

### 12.4 Severity classification

We use CVSS v4.0 base scores. Categories:

| Severity  | CVSS    | Response                                          |
| --------- | ------- | ------------------------------------------------- |
| Critical  | 9.0-10  | Patch within 24h. All operators notified.         |
| High      | 7.0-8.9 | Patch within 7d. Advisory at release.             |
| Medium    | 4.0-6.9 | Patch in next minor release. Advisory at release. |
| Low       | 0.1-3.9 | Patch in next minor release.                      |

---

## 13. Compliance posture

Aura DB is not certified against any compliance framework. We document
the design with these frameworks in mind so operators using Aura DB
in regulated environments have something to point at:

- **SOC 2 Type II** common criteria — access controls, audit logging,
  encryption at rest, encryption in transit, change management, incident
  response. Aura DB's design supports operator SOC 2 evidence collection.
- **HIPAA technical safeguards** — access control (§5–6), audit controls
  (§9), integrity (§9.3), transmission security (§10). Aura DB does not
  store PHI; operators using it against PHI databases retain BAA
  responsibility for the database itself.
- **PCI-DSS 4.0** — req 2 (configuration), req 7 (access), req 8
  (identification), req 10 (logging), req 11 (testing). Aura DB
  configured per this document satisfies these for the management
  surface; the database itself remains the operator's compliance
  surface.

Compliance is not a feature toggle. The controls above are always on.

---

## 14. Configuration reference (security-relevant)

Defaults are the secure defaults. Each override below is logged at
boot and re-logged on every change.

```yaml
# /etc/aura-db/config.yaml (standalone) or settings panel (integrated)

aura_db:
  session:
    idle_ttl: 15m
    absolute_ttl: 8h
    bind_to_ip_class: true       # disable only behind trusted L7 proxy
    bind_to_ua_hash: true
    max_concurrent_per_user: 5

  mfa:
    required_for: [writer, dba, owner]
    webauthn_enabled: true
    totp_enabled: true
    recovery_codes: 8

  rate_limits:
    login_per_ip_15m: 10
    login_per_user_15m: 5
    query_per_user_per_min: 30
    query_per_ip_per_min: 60

  query:
    timeout_default: 30s
    timeout_max: 5m
    result_rows_default: 10000
    result_rows_max: 100000
    result_bytes_default: 50MB
    result_bytes_max: 500MB
    concurrent_per_user_per_conn: 1
    concurrent_max: 3

  audit:
    sample_read_queries: 0.01   # 1% sampling; set to 1.0 for full
    chain_signing_interval: 5m
    chain_signing_events: 1000
    forwarders: []              # syslog / webhook / s3 — see docs
    retention_days: 365

  network:
    tls_min: TLS1.3              # operator may explicitly downgrade
    cipher_suite: mozilla_modern
    mtls:
      enabled: false
      ca_bundle: ""

  csp:
    report_uri: ""               # optional CSP violation collector
    require_trusted_types: true

  forbidden_list:
    additional_function_names: []  # operator-added denylist entries
    # The built-in forbidden list cannot be reduced. Operators may
    # only ADD to it.
```

---

## 15. Open questions / future work

Things deliberately deferred but tracked. Each will be revisited at the
release indicated.

| Topic                                          | Target release | Notes                                                    |
| ---------------------------------------------- | -------------- | -------------------------------------------------------- |
| HSM / KMS support for the encryption key       | v0.3.2         | Currently file-based. AWS KMS, GCP KMS, Vault integration. |
| Per-column row-level redaction policy           | v0.4.0         | Mask emails, credit cards by default; operator config.   |
| SAML / OIDC SSO for standalone mode             | v0.3.2         | Integrated mode inherits from auracpd; standalone needs its own. |
| Just-in-time database accounts (Hashicorp Boundary-style) | v0.4.0 | Ephemeral DB users per Aura DB session.            |
| Differential privacy on aggregate queries       | not planned    | Documented here as explicitly out of scope.             |
| WASM sandboxing for client-side query plan visualisation | v0.4.0 | Run untrusted explainers (e.g. third-party plan renderers) in WASM. |
| Hardware-backed WebAuthn enforcement (no software authenticators) | v0.3.2 | Configurable per-tenant. |
| Detection: anomalous query patterns per user    | v0.4.0         | Heuristic baseline + flag deviations. |

---

## Appendix A — Forbidden-list verification

The forbidden list is non-negotiable but must be **complete**. Test corpus
lives at `pkg/dbadmin/classifier/testdata/forbidden/`:

- Public CVEs against phpMyAdmin, Adminer, pgAdmin, DBeaver involving
  the classes above, distilled into minimal repros (sql files).
- Fuzzing harness (`go test -run=FuzzClassifier`) generates statement
  permutations and asserts the classifier never misclassifies a
  forbidden form as `read`/`write-row`.

A PR that adds anything to the executable path of either classifier
without a corresponding test addition is blocked by CI.

---

## Appendix B — Things this document does NOT promise

To stay honest about what's actually guaranteed:

- **We do not promise zero CVEs.** We promise: short response time,
  transparent disclosure, and that the architecture limits blast radius.
- **We do not promise post-quantum cryptography.** TLS 1.3 with classical
  algorithms is the current state. Migration plan when stable PQ
  primitives ship in Go stdlib.
- **We do not promise resistance to compromise of the operator's
  workstation.** A keylogger on the operator's laptop defeats every
  control here. We protect everything we can control; the workstation
  is the operator's responsibility.
- **We do not promise resistance to a malicious database server.** A
  hostile target DB cannot exploit Aura DB via SQL responses (drivers
  are pinned, output capped) but it can return crafted data designed to
  confuse the operator. Operators are responsible for trusting the
  databases they connect to.

---

*This document is canonical. Discrepancies between this and any other
document, code comment, or feature spec resolve in favor of this
document. Updates require an ADR.*
