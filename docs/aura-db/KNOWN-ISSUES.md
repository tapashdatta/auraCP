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
