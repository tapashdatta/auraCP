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
- **Automatic HTTPS** (Caddy + Let's Encrypt), HTTP/3, Brotli/zstd, Souin full-page cache, and
  **Cloudflare** DNS-01 for wildcard certs.
- **FrankenPHP** worker mode for PHP (PHP 8.3+ only).
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
| Web server / SSL | **Caddy** (auto-HTTPS, HTTP/3) + **Souin** cache |
| PHP runtime | **FrankenPHP** |
| Databases | **MariaDB** + **PostgreSQL** |
| State | **SQLite** |

## Install

On a fresh **Debian 13** or **Ubuntu 24.04** host (x86-64 or ARM64), no repo clone needed
— the `.deb` bundles the installer and exposes it as the `auracp-install` command:

```bash
# 1) install the panel  (one-line)
sudo dpkg -i ./auracp_0.1.2_amd64.deb

# 2) provision the data plane  (interactive package menu)
sudo auracp-install

# …or fully non-interactive in a single line:
sudo dpkg -i ./auracp_0.1.2_amd64.deb && \
sudo auracp-install --yes --db=both --node=yes --php=yes --panel-domain=panel.example.com
```

Then open the panel — `https://panel.example.com` if you set a domain, otherwise
`https://<server-ip>:8443` (self-signed) — and **create your admin account** on the first-run setup screen.

`auracp-install` locks the required packages (auracpd, Caddy) and lets you choose optional ones:
MariaDB, PostgreSQL, Node.js, PHP/FrankenPHP, Python, Redis, Typesense, Docker, UFW + fail2ban.
Run with `--dry-run` first to preview the plan. To remove everything: `sudo auracp-uninstall`.

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
