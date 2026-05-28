# auraCP — Architecture

**Status:** v0.2.0 · 2026-05-28

---

## 1. Core idea: control plane vs data plane

auraCP separates two concerns cleanly:

- **Control plane** — the `auracpd` Go daemon. Owns no web traffic. It stores *desired state* in
  SQLite, renders config from templates, executes privileged system actions, owns ACME state
  (in-process via `go-acme/lego`), and serves the admin UI/API on `:8443`. Idle footprint ~15-25 MB.
- **Data plane** — what actually serves sites: a front **nginx** (1.30 mainline) server, per-site
  **PHP-FPM** pools / Node / Python backends, and the **MariaDB** + **PostgreSQL** engines. This
  is where performance lives; the control plane just configures it.

```
                          ┌────────────────────────────────────────────┐
   Admin browser  ─────►  │  auracpd (Go)  :8443                          │
   CLI (auracp)   ─────►  │  ├─ embedded Svelte SPA + JSON API            │
   API client     ─────►  │  ├─ SQLite (desired state)                    │
                          │  ├─ template renderers (nginx, systemd, FPM)  │
                          │  ├─ go-acme/lego (HTTP-01 + Cloudflare DNS-01)│
                          │  └─ system executor (root → drop to siteuser) │
                          └───────────────┬──────────────────────────────┘
                                          │ writes config, reload, provision
              ┌───────────────────────────┼───────────────────────────────┐
              ▼                            ▼                               ▼
   ┌────────────────────┐      ┌────────────────────────┐      ┌────────────────────┐
   │ nginx (front)      │      │ Per-site backends       │      │ Databases          │
   │ TLS via lego-issued│ ───► │ PHP-FPM pool (unix sock)│      │ MariaDB            │
   │ certs, fastcgi/    │      │ Node (systemd unit)     │      │ PostgreSQL         │
   │ proxy_cache        │      │ Python (systemd unit)   │      │ (per-DB engine)    │
   │ routing per domain │      │ each as its own UID     │      │                    │
   └────────────────────┘      └────────────────────────┘      └────────────────────┘
```

End users (website visitors) only ever hit **nginx**. If `auracpd` is down, sites keep serving;
already-issued certs keep working until expiry (renewal pauses until auracpd is back).

---

## 2. Technology choices

| Layer | Choice | Why |
|---|---|---|
| Control-plane language | **Go** | single static binary, tiny idle RAM, ideal for shelling out |
| Panel datastore | **SQLite** (pure-Go, WAL) | embedded, zero-config, crash-safe, no cgo |
| Admin UI | **Svelte** (compiled, embedded via `go:embed`) | tiny bundle, no runtime on server |
| Front web server | **nginx** (mainline 1.30) | ~10 MB / worker, battle-tested, fastcgi_cache + proxy_cache built-in |
| Auto-HTTPS | **go-acme/lego in auracpd** | in-process ACME; HTTP-01 by default + Cloudflare DNS-01 for wildcards / proxied domains |
| PHP runtime | **PHP-FPM, pool per site** (Unix socket per domain) | classic CloudPanel-class isolation; multi-version side-by-side via `deb.sury.org`. Extension list is conditional on host components — `php-mysql` / `php-pgsql` / `php-redis` only pull when MariaDB / Postgres / Redis are present, so PHP-only or PHP-+-one-engine hosts stay lean. |
| Page cache | **nginx fastcgi_cache + proxy_cache** | full-page cache per site; bypass on logged-in/POST |
| Object cache | **Redis** | WordPress / Drupal / Laravel / Django expect it |
| Node runtime | **Node 24 LTS** (nodejs.org tarball, multi-version managed by auracpd) | per-site systemd unit; **systemd-only is the default** runner, **PM2 wrapper** opt-in (`pm2-runtime` foreground — no daemon). Site Create UI surfaces both as a radio choice. |
| Python | **gunicorn / uvicorn** via per-site systemd unit | standard modern Python web stack |
| Databases | **MariaDB** + **PostgreSQL** | per-database engine choice |
| Secrets | NaCl secretbox + `/etc/auracp/secret.key` | encrypt DB passwords, Cloudflare tokens at rest |

**Why nginx + PHP-FPM (v0.2.0 lineup)** rather than the v0.1.x Caddy + FrankenPHP stack: nginx
uses ~3-4x less RAM per worker than Caddy, the FastCGI+pool model is the canonical CloudPanel-class
isolation pattern, and FrankenPHP's "PHP version" knob was theatre (the bundled PHP is statically
embedded in each FrankenPHP release). Trade-off: auto-HTTPS becomes auracpd's responsibility via
`go-acme/lego` instead of being free from Caddy. See [docs/UPGRADE-v0.2.md](UPGRADE-v0.2.md) for
the upgrade path from v0.1.x.

---

## 3. Process & privilege model

- `auracpd` runs as a **root systemd service** (`auracpd.service`). It needs root to manage Linux
  users, write to `/etc/nginx`, write per-site PHP-FPM pool configs under `/etc/php/<ver>/fpm/pool.d/`,
  install systemd units, and reload services.
- **File operations on site content drop privileges** to the site's Linux user (fork + `setuid`,
  or `runuser -u <siteuser>`), so the file manager and deploy actions can't escape a site's home.
- **PHP-FPM pools run as the site user** (`user =` and `group =` in the pool config) — each PHP
  request is already at the right UID before any user code runs. The Unix socket is owned by the
  site user with `listen.group = www-data` and `mode = 0660`, so only nginx can talk to it.
- A privileged **system executor** package wraps every shell-out with explicit, audited commands
  (no string interpolation of untrusted input into shells).

---

## 4. Per-site model

Each site maps to:

- a **dedicated Linux user** (e.g. `iskcon-ldn`), home `/home/<user>`;
- document root `/home/<user>/htdocs/<domain>`, logs `/home/<user>/logs/`;
- a **chroot-jailed SFTP** entry (OpenSSH `Match Group`);
- an **nginx `server{}` block** in `/etc/nginx/sites-available/<domain>.conf`, symlinked into
  `sites-enabled`;
- a **TLS cert** at `/etc/auracp/ssl/<domain>.{crt,key}` (issued by lego on demand, renewed daily);
- a **backend** depending on type:

| Type | Backend | nginx front does |
|---|---|---|
| Static | none | `try_files` from htdocs |
| PHP / WordPress | per-site **PHP-FPM pool** in the shared `php<ver>-fpm` daemon (as site user) | `fastcgi_pass unix:/run/php-fpm/<domain>.sock` |
| Node.js | per-site **systemd** unit (`node …`, optional `pm2-runtime`) | `proxy_pass http://127.0.0.1:<port>` |
| Python | per-site **systemd** unit (gunicorn/uvicorn) | `proxy_pass http://127.0.0.1:<port>` |
| Reverse Proxy | external upstream | `proxy_pass` to user URL |

Backend ports for Node/Python are allocated **sequentially** (max existing + 1). PHP sites have
no port — they get a Unix socket per domain. **Node 24** is installed system-wide as the baseline;
a site may pin a different version. **PHP versions are installed side-by-side** (8.3 / 8.4 / 8.5);
each site's pool config names its pinned version, so changing one site's PHP doesn't touch others.

---

## 5. Site-creation flow (the central path)

1. Validate domain + type + version pins.
2. `osuser`: create Linux user, htdocs/logs dirs, SFTP jail.
3. Type-specific backend:
   - PHP/WordPress → `phpruntime.WritePool(version, domain, user)` → reload `php<ver>-fpm`.
   - Node → `runtime.Apply(...)` → install/start `auracp-site-<domain>.service`. PM2 path uses
     `pm2-runtime` foreground inside the same unit (no separate PM2 daemon).
   - Python → gunicorn/uvicorn systemd unit.
   - Static / Reverse Proxy → nothing.
4. Optional DB provision: pick engine (MariaDB|Postgres), create DB + user, store encrypted creds.
5. `webserver`: render `/etc/nginx/sites-available/<domain>.conf` → symlink → `nginx -t` →
   `systemctl reload nginx`.
6. `acme.EnsureCert(domain)` in the background: HTTP-01 via the `/.well-known/acme-challenge/`
   location every vhost ships with. On success, the vhost is re-rendered with `ssl_certificate`
   paths and nginx reloads — the HTTPS server{} block goes live with the new cert.
7. Optional cron entries; persist everything to SQLite. Renewal scheduler picks the cert up daily.

All steps are idempotent and recorded in the audit log; failures roll back created resources.

---

## 6. ACME / auto-HTTPS

`internal/acme` owns the ACME state machine end-to-end:

- ECDSA P-256 account key at `/etc/auracp/acme/account.key` (0600).
- Per-domain state in the `certificates` table: `domain, issuer, cert_path, key_path,
  issued_at, expires_at, status, last_error, attempts`.
- **HTTP-01** by default: lego writes the challenge token to `/var/lib/auracp/acme/<token>`;
  nginx's `location /.well-known/acme-challenge/` (in every vhost's :80 server{}) serves it.
- **DNS-01 via Cloudflare** when a CF API token is configured in Settings — needed for
  wildcards and for domains proxied through Cloudflare (orange-cloud breaks HTTP-01).
- **Renewal loop**: a goroutine in `cmd/auracpd` ticks every 12 hours, selects certs within
  30 days of expiry, jitters ±2h, re-issues. Failures persist `last_error`; next tick retries.
- After each successful issuance, the manager calls `webserver.Reload(ctx)` so nginx picks up
  the new cert in place.

---

## 7. Data model (SQLite)

A trimmed, extended relational model:

- `panel_users` (email, password_hash, role, permissions JSON, totp_secret)
- `sites` (type, domain, site_user, root_path, php_version, node_version, port, upstream, pm2_enabled, status)
- `database_servers` (engine[mariadb|postgres], host, port, version, is_default) — **two local rows**
- `databases` (site_domain, engine, name, db_user, password_enc) · `database_users` rolled in
- `cron_jobs` (site_domain, site_user, schedule, command, enabled)
- `ssh_users` (site_domain, username, type, password_enc)
- **`certificates`** *(v0.2.0)* (domain, issuer, cert_path, key_path, issued_at, expires_at, status, last_error, attempts)
- **`php_runtimes`** *(v0.2.0)* (version, is_default) — what PHP versions are installed
- **`php_settings`** *(v0.2.0)* (domain, key, value) — per-site FPM pool overrides (memory_limit, …)
- `node_runtimes` (version, path, is_default)
- `site_config` (site_domain, key, value) — generic per-site toggles (cache, basic_auth, …)
- `cloudflare` lives in `settings` (api_token_enc + zone metadata)
- `settings` (k/v) · `audit_log` · `sessions` · `backups`

The `database_servers` + per-database `engine` is what enables **per-database engine choice** —
the differentiator over single-engine panels. The `php_runtimes` + per-site `php_version`
is what enables **per-site PHP version choice** — same idea, applied to PHP.

---

## 8. Go package layout

```
cmd/auracpd     daemon: HTTPS API on :8443 + embedded SPA + ACME renewal loop
cmd/auracp      CLI (auracp site:create …)
internal/
  api           REST/JSON handlers, session auth, TOTP, permissions
  site          lifecycle orchestrator (calls runtime + phpruntime + webserver + acme)
  webserver     nginx render (templates) + reload  (v0.2.0: was Caddy)
  phpruntime    multi-version PHP-FPM manager + per-site pool config writer  (v0.2.0)
  runtime       node/python systemd units (PHP no longer has per-site units)
  noderuntime   multi-version Node manager (nodejs.org tarballs)
  acme          go-acme/lego: account, HTTP-01, renewal loop, cert state  (v0.2.0)
  db            MariaDB + PostgreSQL provisioning
  ssl           live cert inspection on :443 (independent of acme; reads what nginx is serving)
  cloudflare    API client: DNS-01, cache purge
  osuser        site user + chroot SFTP jail
  cron          per-site crontab management
  files         privilege-dropped file manager
  logs          per-site log tail/stream (SSE)
  store         SQLite access + migrations (incl. certificates / php_runtimes / php_settings)
  secret        NaCl secretbox for secrets-at-rest
  system        audited privileged command executor
  paths         on-disk layout constants (nginx paths, FPM socket paths, SSL paths, …)
web/            Svelte SPA (built → embedded)
templates/      kept for legacy reference; v0.2.0 templates live alongside their packages
```

`SiteManager` interface (`Create`/`Update`/`Delete`) has one implementation in `internal/site`;
the polymorphism is on site type via switch, not multiple manager types. Clean per-type triad
pattern.

---

## 9. UI architecture

- Svelte 5 SPA, **light theme default with a dark toggle**, design tokens as CSS custom
  properties (`--ink`, `--aura`, `--line`, type scale) switched via `data-theme` on `<html>`.
- Aesthetic: "instrument-grade console" — monospace for all technical identifiers, hairline
  borders, one mint "aura" accent, characterful display type.
- Lean bundle: no heavy UI framework; minimal deps; assets fingerprinted and embedded in the
  Go binary via `go:embed` so the panel ships as one file.
- Talks to `auracpd` over the JSON API; long-running actions stream progress (SSE).

---

## 10. Security posture

- Per-site Linux user isolation + chroot SFTP; file ops privilege-dropped.
- PHP-FPM pools run as the site user — no shared php-fpm worker pool.
- Automatic HTTPS everywhere, including the panel (`:8443`).
- TOTP 2FA for panel users; token-scoped API; CSRF double-submit.
- fail2ban for SSH/auth/nginx; UFW firewall managed from the panel.
- Secrets encrypted at rest (NaCl secretbox); audited command executor with no shell
  interpolation of user input.
- Panel daemon is the only root component; the data plane runs unprivileged per-site.
