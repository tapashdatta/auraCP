<h1 align="center">auraCP</h1>

<p align="center">
  A lightweight, modern, self-hosted server control panel.<br>
  Host WordPress · PHP · Node.js · Python · Static · Reverse-Proxy sites — on a tiny footprint.
</p>

<p align="center">
  <img src="https://github.com/tapashdatta/auraCP/actions/workflows/ci.yml/badge.svg" alt="CI">
</p>

---

auraCP is a single Go binary (with the admin UI embedded) that provisions and manages sites,
databases, certificates, and users on a Debian/Ubuntu host. The control plane idles in tens of MB
and leaves the server's resources for the sites it hosts.

## Highlights

- **Six site types** — WordPress, PHP, Node.js, Python, Static HTML, Reverse Proxy, each isolated as
  its own Linux user with a chroot-jailed SFTP account.
- **PostgreSQL *and* MariaDB** — choose the engine **per database**.
- **Node.js 24 LTS** preinstalled on every site.
- **Automatic HTTPS** (in-process **go-acme/lego** in auracpd) — HTTP-01 by default, **Cloudflare**
  DNS-01 for wildcards or proxied domains, daily renewal scheduler.
- **PHP-FPM, one pool per site** with a per-site Unix socket and isolated UID; **multiple PHP
  versions side-by-side** (8.3 / 8.4 / 8.5) from `deb.sury.org`, pin per site. PHP install
  pulls only the DB / cache extensions for engines actually selected on the host (no
  `php-mysql` when MariaDB wasn't picked, etc.).
- **Node.js** runs as a per-site **systemd** unit by default; optional **PM2 wrapper**
  (`pm2-runtime` — no daemon) for apps that need cluster mode or ship `ecosystem.config.js`.
- **nginx fastcgi_cache + proxy_cache** for full-page caching; **Redis** for object cache.
- Per-site tabs: Settings · Vhost · Databases · Cache · SSL/TLS · Security · SSH/FTP · File Manager ·
  Cron · Logs · Backups.
- **Security-first** — no-shell command execution, validated input, encrypted secrets, sessions +
  **TOTP 2FA**, CSRF, security headers, login rate-limiting, and **granular per-resource CRUD RBAC**.
- **Backups** (local + rclone remotes), **audit log**, live **instance metrics** & service status.
- **Light + dark** enterprise UI; runs on **Debian/Ubuntu**, **x86-64 & ARM64**.

## Stack

| Layer | Choice |
|---|---|
| Control plane | **Go** (single static binary, pure-Go SQLite — no cgo) |
| Admin UI | **Svelte** SPA, compiled and embedded via `go:embed` |
| Web server | **nginx** (1.30 mainline) with `fastcgi_cache` + `proxy_cache` |
| Auto-HTTPS | **go-acme/lego** in auracpd (in-process, HTTP-01 + Cloudflare DNS-01) |
| PHP runtime | **PHP-FPM, pool per site** (Unix socket, isolated UID) — multi-version via `deb.sury.org` |
| Node.js | per-site **systemd** unit running `node` directly (PM2 opt-in via `pm2-runtime`) |
| Python | **gunicorn / uvicorn** via per-site systemd unit |
| Object cache | **Redis** (per-site DB or shared) |
| Databases | **MariaDB** + **PostgreSQL** (choose per database) |
| State | **SQLite** (pure-Go, WAL) |

## Install

On a fresh **Debian 13** or **Ubuntu 22.04 / 24.04** host (x86-64 or ARM64), no repo
clone needed — the `.deb` bundles the installer and exposes it as the `auracp-install` command.

```bash
# 1) download the package for your arch (plain curl — repo is public)
ARCH=$(dpkg --print-architecture)        # → amd64 or arm64
curl -fL -o auracp.deb \
  "https://github.com/tapashdatta/auraCP/releases/download/v0.2.8/auracp_0.2.8_${ARCH}.deb"

# 2) install the panel
sudo dpkg -i ./auracp.deb

# 3) provision the data plane  (interactive package menu + panel-domain prompt)
sudo auracp-install
```

**One-liner** (fully non-interactive, with a panel domain):

```bash
ARCH=$(dpkg --print-architecture) && \
curl -fL -o /tmp/auracp.deb "https://github.com/tapashdatta/auraCP/releases/download/v0.2.8/auracp_0.2.8_${ARCH}.deb" && \
sudo dpkg -i /tmp/auracp.deb && \
sudo auracp-install --yes --db=both --node=yes --php=yes --panel-domain=panel.example.com
```

Then open the panel — `https://panel.example.com` if you set a domain, otherwise
`https://<server-ip>:8443` (self-signed) — and **create your admin account** on the first-run setup screen.

`auracp-install` locks the required packages (auracpd, nginx) and lets you choose optional ones:
MariaDB, PostgreSQL, Node.js, PHP-FPM (multi-version), Python, Redis, Typesense, Docker, UFW +
fail2ban. Run with `--dry-run` first to preview the plan. To remove everything:
`sudo auracp-uninstall` (returns the host to baseline — no orphan apt sources or service users).

**Upgrading from v0.1.x?** The data plane changed (Caddy → nginx, FrankenPHP → PHP-FPM); see
[docs/UPGRADE-v0.2.md](docs/UPGRADE-v0.2.md) for the destructive upgrade path.

## Build from source

```bash
make ui            # build the Svelte UI and embed it
make build         # native auracpd + auracp
make dist          # static linux/amd64 + linux/arm64 binaries
make deb           # .deb packages for both arches
make run           # run locally in record-only mode (no host changes)
```

Requires Go ≥ 1.23 and Node 24 (dev only — the shipped binary needs neither).

## Documentation

- [docs/SCOPE.md](docs/SCOPE.md) — vision, features, differentiators
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — control vs data plane, data model, security
- [docs/PLAN.md](docs/PLAN.md) — milestones & status
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) — dev setup & conventions
- [docs/TESTING.md](docs/TESTING.md) — Debian/Ubuntu VM validation checklist
