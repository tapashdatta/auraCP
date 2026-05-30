# Aura DB — Standalone Config Reference

Path: `/etc/aura-db/config.yaml`. Defaults match SECURITY.md §14. Every
field below is optional; omitting a key keeps the secure default.

```yaml
listen: "127.0.0.1:7878"
tls:
  cert_file: ""             # PEM. Empty = plain HTTP (front via nginx).
  key_file: ""
  min_version: "TLS1.3"     # TLS1.2 permitted via explicit downgrade.
  cipher_suite: "mozilla_modern"
  mtls:
    enabled: false
    ca_bundle: ""           # required when mtls.enabled

storage:
  db_path: "/var/lib/aura-db/aura.db"           # users + sessions + conns
  audit_log_path: "/var/lib/aura-db/audit.log"  # NDJSON append-only
  history_db_path: "/var/lib/aura-db/history.db"

kek:
  file: "/etc/aura-db/kek.key"
  rotate_on_boot: false      # dev-only; refused unless AURA_DB_ALLOW_ROTATE_ON_BOOT=1

session:
  idle_ttl: 15m
  absolute_ttl: 8h
  max_concurrent: 5
  bind_to_ip_class: true     # bind to /24 (v4) or /56 (v6)
  bind_to_ua_hash: true

auth:
  password_min_length: 14
  argon2:
    time: 3
    memory_kib: 65536        # 64 MiB
    threads: 4
    key_len: 32
    salt_len: 16
  hibp_check: true
  mfa:
    required_for: ["writer", "dba", "owner"]
    totp_enabled: true
    recovery_codes: 8

rate_limits:
  login_per_ip_15m: 10
  login_per_user_15m: 5
  query_per_user_per_min: 30
  query_per_ip_per_min: 60
  lockout_escalation_minutes: [15, 30, 60, 120, 240, 480, 1440]

query:
  timeout_default: 30s
  timeout_max: 5m
  result_rows_default: 10000
  result_rows_max: 100000
  result_bytes_default: 52428800
  result_bytes_max: 524288000
  concurrent_per_user_per_conn: 1
  concurrent_max: 3
  sql_input_max_bytes: 1048576
  pool_size_per_conn: 4
  pool_idle_timeout: 5m

audit:
  sample_read_queries: 0.01
  redact_sensitive_params: true
  chain_signing:
    enabled: false
    key_file: "/etc/aura-db/audit-sign.key"   # required when enabled, mode 0400
    every_events: 1000
    every: 5m
  forwarders: []                # see "Forwarders" below
  retention_days: 365

network:
  csp_report_uri: ""

logging:
  level: "info"                 # debug | info | warn | error
  format: "text"                # text | json
  destination: "stderr"         # stderr | stdout | file:/abs/path

csp:
  report_uri: ""
  require_trusted_types: true

forbidden_list:
  additional_function_names: []
```

## Forwarders

Audit forwarders are configured under `audit.forwarders` as a list of
maps. Supported kinds:

```yaml
forwarders:
  - kind: syslog
    address: "logsrv.internal:6514"
    protocol: tcp             # tcp | udp
    facility: local6
  - kind: webhook
    url: "https://siem.example/ingest"
    secret_file: "/etc/aura-db/webhook.secret"
```

`secret_file` is a path; never inline secrets in the YAML (the loader
refuses any document that defines `secrets:`, `kek_inline:`, or
`password:`).

## What is NOT in the config

Per SECURITY.md §3 ("Confidentiality is a server property"):

- KEK bytes (`/etc/aura-db/kek.key` only)
- DB passwords (encrypted in the SQLite store)
- Session tokens (server-side state)
- MFA secrets (encrypted in the users table)
- Audit signing key (path-only reference under
  `audit.chain_signing.key_file`)

## Validation summary

`aura-db serve` refuses to start when:
- `session.idle_ttl > session.absolute_ttl`
- `auth.password_min_length < 14`
- `auth.argon2.time < 2` or `memory_kib < 32768`
- `rate_limits.lockout_escalation_minutes` non-increasing or has a
  non-positive entry
- `tls.cert_file` set without `key_file` (or vice versa)
- `tls.mtls.enabled` without `ca_bundle` + `cert_file`
- `kek.rotate_on_boot: true` without env
  `AURA_DB_ALLOW_ROTATE_ON_BOOT=1`
- any storage path is non-absolute
