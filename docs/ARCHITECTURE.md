# auraCP — Architecture

**Status:** Draft v0.1 · 2026-05-27

---

## 1. Core idea: control plane vs data plane

auraCP separates two concerns cleanly:

- **Control plane** — the `auracpd` Go daemon. Owns no web traffic. It stores *desired state* in
  SQLite, renders config from templates, executes privileged system actions, and serves the admin
  UI/API on `:8443`. Idle footprint ~15–25 MB.
- **Data plane** — what actually serves sites: a front **Caddy** server, per-site **FrankenPHP** /
  Node / Python backends, and the **MariaDB** + **PostgreSQL** engines. This is where performance
  lives; the control plane just configures it.

```
                          ┌────────────────────────────────────────────┐
   Admin browser  ─────►  │  auracpd (Go)  :8443                          │
   CLI (auracp)   ─────►  │  ├─ embedded Svelte SPA + JSON API            │
   API client     ─────►  │  ├─ SQLite (desired state)                    │
                          │  ├─ template renderers (Caddy, systemd)       │
                          │  └─ system executor (root → drop to siteuser) │
                          └───────────────┬──────────────────────────────┘
                                          │ writes config, reload, provision
              ┌───────────────────────────┼───────────────────────────────┐
              ▼                            ▼                               ▼
   ┌────────────────────┐      ┌────────────────────────┐      ┌────────────────────┐
   │ Caddy (front)      │      │ Per-site backends       │      │ Databases          │
   │ TLS, HTTP/2/3,     │ ───► │ FrankenPHP (php/wp)     │      │ MariaDB            │
   │ Souin cache,       │      │ Node 24 (systemd)       │      │ PostgreSQL         │
   │ routing per domain │      │ Python (systemd)        │      │ (per-DB engine)    │
   └────────────────────┘      └────────────────────────┘      └────────────────────┘
                                   each runs as its own
                                   isolated Linux user
```

End users (website visitors) only ever hit **Caddy**. If `auracpd` is down, sites keep serving.

---

## 2. Technology choices

| Layer | Choice | Why |
|---|---|---|
| Control-plane language | **Go** | single static binary, tiny idle RAM, ideal for shelling out |
| Panel datastore | **SQLite** | embedded, zero-config, crash-safe |
| Admin UI | **Svelte** (compiled, embedded via `go:embed`) | tiny bundle, no runtime on server |
| Front web server | **Caddy** | automatic HTTPS, HTTP/3, simple config, Go-native |
| PHP runtime | **FrankenPHP** (worker mode) | far less RAM than PHP-FPM; built on Caddy |
| Cache | **Souin** (Caddy module) | in-process full-page cache, replaces Varnish |
| SSL | **Caddy ACME** + `caddy-dns/cloudflare` | free, automatic, wildcard via DNS-01 |
| Node runtime | **Node 24 LTS** | preinstalled baseline on every site |
| Databases | **MariaDB** + **PostgreSQL** | per-database engine choice |
| Secrets | NaCl secretbox / age | encrypt DB passwords, Cloudflare tokens at rest |

Caddy is built once with `xcaddy` to include `caddy-dns/cloudflare` and the Souin cache module.

---

## 3. Process & privilege model

- `auracpd` runs as a **root systemd service** (`auracpd.service`). It needs root to manage Linux
  users, write to `/etc/caddy`, install systemd units, and reload services.
- **File operations on site content drop privileges** to the site's Linux user (fork + `setuid`,
  or `runuser -u <siteuser>`), so the file manager and deploy actions can't escape a site's home.
- This is deliberately simpler and tighter than CloudPanel, which runs the panel as a `clp` user
  granted `NOPASSWD: ALL` sudo (effectively root) and bridges through a `clpctlWrapper`. auraCP has
  no PHP-as-user indirection.
- A privileged **system executor** package wraps every shell-out with explicit, audited commands
  (no string interpolation of untrusted input into shells).

---

## 4. Per-site model

Each site maps to:

- a **dedicated Linux user** (e.g. `iskcon-ldn`), home `/home/<user>`;
- document root `/home/<user>/htdocs/<domain>`, logs `/home/<user>/logs/`;
- a **chroot-jailed SFTP** entry (OpenSSH `Match Group`);
- a **front Caddy site block** in `/etc/caddy/sites/<domain>.caddy`;
- a **backend** depending on type:

| Type | Backend | Caddy front does |
|---|---|---|
| Static | none | `file_server` from htdocs |
| PHP / WordPress | per-site **FrankenPHP** systemd unit (as site user) | `reverse_proxy` to it |
| Node.js | per-site **systemd** unit (`node …`) | `reverse_proxy` to `127.0.0.1:<port>` |
| Python | per-site **systemd** unit (gunicorn/uvicorn) | `reverse_proxy` to `127.0.0.1:<port>` |
| Reverse Proxy | external upstream | `reverse_proxy` to user URL |

Backend ports are allocated **sequentially** (max existing + 1), mirroring CloudPanel's pool model.
**Node 24** is installed system-wide as the baseline; a site may pin a different version later.

---

## 5. Site-creation flow (the central path)

1. Validate domain + type.
2. `osuser`: create Linux user, htdocs/logs dirs, SFTP jail.
3. Type-specific backend: install FrankenPHP/systemd unit (+ `wp-cli` for WordPress); none for static.
4. Optional DB provision: pick engine (MariaDB|Postgres), create DB + user, store encrypted creds.
5. `webserver`: render `/etc/caddy/sites/<domain>.caddy` from template → `caddy reload`.
6. Caddy obtains the certificate automatically (HTTP-01 or Cloudflare DNS-01).
7. Optional cron entries; persist everything to SQLite.

All steps are idempotent and recorded in the audit log; failures roll back created resources.

---

## 6. Data model (SQLite)

Derived from CloudPanel's Doctrine entities, trimmed and extended:

- `panel_users` (email, password_hash, role, totp_secret)
- `sites` (type, domain, site_user, root_path, php_version?, node_version, upstream?, status)
- `database_servers` (engine[mariadb|postgres], host, port, version, is_default) — **two local rows**
- `databases` (site_id, server_id, name) · `database_users` (db_id, username, password_enc)
- `cron_jobs` (site_id, schedule, command, enabled)
- `ssh_users` · `ftp_users` (site_id, username, type, home, chroot)
- `certificates` (site_id, domains, issuer, expires_at, source)
- `vhost_templates` (site_id, content) — editable Caddyfile per site
- `basic_auth` · `blocked_bots` · `blocked_ips` · `firewall_rules` (UFW)
- `php_settings` / `nodejs_settings` / `python_settings` (per-site)
- `cloudflare` (api_token_enc, zone_map) · `settings` (k/v) · `audit_log` · `sessions`
- Monitoring (CPU/mem/disk/load): computed live; persist only if trends are needed.

The `database_servers` + per-database `server_id` is what enables **per-database engine choice** —
the differentiator over CloudPanel's single-engine instance.

---

## 7. Go package layout

```
cmd/auracpd     daemon: HTTPS API on :8443 + embedded SPA
cmd/auracp      CLI (auracp site:create …)
internal/
  api           REST/JSON handlers, session auth, TOTP
  site          lifecycle orchestrator; SiteManager interface per type
  webserver     Caddy config render (templates) + reload
  php           per-site FrankenPHP units
  runtime       node/python systemd units
  db            MariaDB + PostgreSQL provisioning
  ssl           cert state (delegated to Caddy)
  cloudflare    API client: DNS-01, cache purge
  osuser        site user + chroot SFTP jail
  cron          per-site crontab management
  files         privilege-dropped file manager
  logs          per-site log tail/stream (SSE)
  store         SQLite access + migrations
  systemd       unit install/reload helpers
  exec          audited privileged command executor
web/            Svelte SPA (built → embedded)
templates/      Caddyfile fragments + systemd unit templates
```

`SiteManager` interface (`Create`/`Update`/`Delete`) has one implementation per site type — the
clean triad pattern observed in CloudPanel, expressed in Go.

---

## 8. UI architecture

- Svelte SPA, **light theme default with a dark toggle**, design tokens as CSS custom properties
  (`--ink`, `--aura`, `--line`, type scale) switched via `data-theme` on `<html>`.
- Aesthetic: "instrument-grade console" — monospace for all technical identifiers, hairline
  borders, one mint "aura" accent, characterful display type. Reference prototype:
  `design/auracp-prototype.html`.
- Lean bundle: no heavy UI framework; minimal deps; assets fingerprinted and embedded in the
  Go binary via `go:embed` so the panel ships as one file.
- Talks to `auracpd` over the JSON API; long-running actions stream progress (SSE).

---

## 9. Security posture

- Per-site Linux user isolation + chroot SFTP; file ops privilege-dropped.
- Automatic HTTPS everywhere, including the panel (`:8443`).
- TOTP 2FA for panel users; token-scoped API.
- fail2ban for SSH/FTP; UFW firewall managed from the panel.
- Secrets encrypted at rest; audited command executor with no shell interpolation of user input.
- Panel daemon is the only root component; the data plane runs unprivileged per-site.
