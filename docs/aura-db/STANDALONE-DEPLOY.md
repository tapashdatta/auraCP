# Aura DB — Standalone Deployment

This page documents the standalone deployment mode (the `aura-db`
binary) shipped in PR #9. Standalone runs as a self-contained service
with its own user/session/connection store, audit log, and key
management. It does not require the panel (`auracpd`); the panel-aware
integration mode lives in a separate package.

## Filesystem layout

| Path | Mode | Owner | Purpose |
|------|------|-------|---------|
| `/etc/aura-db/config.yaml` | 0640 | root:aurabd | YAML config |
| `/etc/aura-db/kek.key`     | 0400 | root:root  | KEK (32 bytes raw) |
| `/etc/aura-db/audit-sign.key` | 0400 | root:root | optional HMAC signing key for chain heads |
| `/etc/aura-db/keys/<conn>.key` | 0600 | root:root | per-connection SSH tunnel keys |
| `/var/lib/aura-db/aura.db` | 0600 | root:root  | SQLite (users, sessions, conns, grants) |
| `/var/lib/aura-db/history.db` | 0600 | root:root | SQLite (query history) |
| `/var/lib/aura-db/audit.log` | 0640 | aurabd:aurabd-audit | append-only NDJSON audit log |
| `/var/run/aura-db.pid` | 0644 | root:root | PID file (also under `$XDG_RUNTIME_DIR` fallback) |

The installer is responsible for creating directories and applying
ownership. `aura-db serve` refuses to start when modes are broader than
required.

## First run

1. `aura-db kek-rotate --generate --backup-old-to /root/initial-kek.bak`
   (one-time bootstrap; rotating from the auto-generated install key to
   an operator-owned key).
2. `aura-db user-create --username admin --enroll-totp` — provisions
   the first user. Save the TOTP provisioning URI + recovery codes to
   your password manager; they are printed once.
3. `aura-db serve` — bind to `127.0.0.1:7878` by default. Front with
   nginx + Let's Encrypt to expose externally.

## Signals

| Signal | Behavior |
|--------|----------|
| SIGTERM, SIGINT | Initiate graceful shutdown (30 s drain). |
| SIGHUP  | Reopen the audit log file (compatible with `logrotate`). |
| SIGUSR1 | Print diagnostics (PID, audit drop count, listen addr) to stderr. |

`aura-db serve` does NOT reload its config on SIGHUP — restart the
process for config changes.

## Logging

Default `stderr`, JSON via `logging.format: json`. Sensitive material
(SQL bodies, passwords, KEK bytes, raw session tokens) is never
emitted. Session correlation uses the first 8 hex chars of the
SHA-256(token) hash.

## Audit log

Append-only NDJSON. Each event embeds the SHA-256 digest of the prior
event so any tampering is detectable. `aura-db audit verify` walks the
chain; exit code 4 means a break.

Optional HMAC-signed chain heads are emitted every 1000 events / 5
minutes (configurable) and can be shipped to syslog / webhook / S3 by
configuring `audit.forwarders`. See CONFIG-REFERENCE.md.

## Graceful shutdown order

1. `Engine.Shutdown(ctx)` — refuse new requests, wait for in-flight.
2. `http.Server.Shutdown(ctx)` — stop the listener.
3. `FileAuditSink.Close()` — drain the queue and fsync.
4. `history.Store.Close()`.
5. `standalone.Store.Close()`.
6. KEK bytes zeroed in memory.
7. PID file removed.

If the 30-second shutdown context expires, the process exits with code
1 and the step that timed out is logged.
