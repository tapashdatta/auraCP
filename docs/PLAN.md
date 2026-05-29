# auraCP — Implementation Plan

**Status:** Draft v0.1 · 2026-05-27

This plan sequences the build. See [SCOPE.md](SCOPE.md), [ARCHITECTURE.md](ARCHITECTURE.md), and
[DEVELOPMENT.md](DEVELOPMENT.md). The full working plan lives at
`~/.claude/plans/does-this-file-help-humming-valiant.md`.

---

## Guiding constraints

- **Minimal packages** — PHP 8.3+ only; install per site type on demand; no mail/Varnish/proftpd bloat.
- **Node 24 LTS** is the one baseline runtime on every site.
- **Feature + UX parity** with established panels; **light-default UI with dark toggle**.
- Target **Debian 13**.

---

## Status snapshot

| # | Item | State |
|---|---|---|
| — | Reference-panel analysis (architecture) | ✅ done |
| — | Feature design extracted (screens, tabs, forms) | ✅ done |
| — | Stack decisions (Go · Caddy/FrankenPHP · Svelte · SQLite) | ✅ done |
| — | UI design prototype (`design/auracp-prototype.html`) | ✅ done |
| — | Project docs (this set) | ✅ done |
| P0a | Svelte UI app (light/dark, 3 core screens) | ✅ done |
| P0b | Go daemon + SQLite + API + embedded UI + CLI | ✅ done |
| P0c | Panel auth (login + sessions + TOTP 2FA) | ✅ done |
| P1  | Interactive installer (Debian+Ubuntu, x86+ARM64; optional packages incl. Redis/Typesense/Docker) | 🚧 written + dry-run validated; pending VM test |
| P2–P5 | Provisioning engine (all site types, DB per-engine), security foundation, cron/logs/files, RBAC, API+create-flow wired | 🚧 built + compiles + cross-compiles + dry-run smoke-tested; pending VM test |
| —   | Cross-compile linux/amd64 + linux/arm64 (static, no cgo) | ✅ verified |
| P6 | Backups (local+rclone), admin users/RBAC w/ granular CRUD perms, instance/settings, config write-backs (cache/SSL/security/cloudflare), SSH-FTP users, SSL status, audit log — all wired UI↔API | ✅ built + smoke-tested (dry-run) |
| P7 | Packaging: `.deb` (amd64+arm64) + systemd unit + installer `.deb` support; `make dist`/`make deb` | ✅ built + verified |
| REMAINING | **Real-server validation on Debian/Ubuntu VM** — see [TESTING.md](TESTING.md) | ⬜ |
| FUTURE | Web DB-admin UI (browse/query) — implemented as **Aura DB**, the native in-panel console at `/dbadmin/` (Svelte SPA + Go engine under `/api/dbadmin/`). Adminer was bundled in v0.2.x and removed in PR #17 (v0.3.0). | ✅ done |

---

## Phases

### P0 — Foundation
- **P0a UI:** real Svelte app from the prototype — design tokens (light default + dark toggle),
  TopBar, Sites manager, Add-Site chooser + create forms, Site detail with 10 tabs. Lean bundle.
- **P0b Daemon:** Go module, SQLite store + migrations, `auracpd` skeleton, session auth + TOTP,
  serve the embedded Svelte build, login flow. JSON API contract stubbed.

### P1 — Installer
Adapt the bootstrap installer for Debian 13: preflight checks (OS/arch/ports/disk), swap, then
provision **only**: custom Caddy build (xcaddy + cloudflare DNS + Souin), FrankenPHP, **Node 24
LTS**, MariaDB, PostgreSQL, OpenSSH SFTP group, fail2ban, UFW. **No mail.** Justify each package.

### P2 — Static + Reverse Proxy sites
Simplest types end-to-end: site user, dirs, SFTP jail, Caddy render → reload → automatic SSL.
Establishes the whole create/update/delete pipeline and the `SiteManager` pattern.

### P3 — PHP + WordPress
Per-site FrankenPHP systemd units (worker mode), `wp-cli` WordPress install, automatic DB creation,
per-site PHP settings (8.3+ only).

### P4 — Node.js + Python
Per-site systemd units + Caddy reverse_proxy wiring; sequential port allocation; runtime settings.
(Node 24 already present from P1; Python installed on demand.)

### P5 — Databases + Security
MariaDB **and** PostgreSQL management UI with **per-database engine choice**; database users;
per-site SSH/FTP users; privilege-dropped file manager.

### P6 — Remaining tabs
SSL/TLS tab (status/import), Cloudflare integration (DNS-01 + cache purge), Cache (Souin) controls,
Cron jobs, live Logs (SSE tail). Admin: Users/RBAC, Events/audit, Instance, Backups (local + remote).

### P7 — Hardening & polish
Audit log coverage, backups/restore, rate limiting, privilege-model review, monitoring on the
dashboard, ARM64 build, CLI + API parity, packaging (`.deb` + install script), docs site.

---

## Milestone exit criteria

- **M1 (P0):** panel builds to a single binary; login works; UI navigates all screens with mock data.
- **M2 (P1–P2):** fresh Debian 13 → installer → create a Static and a Reverse Proxy site over HTTPS.
- **M3 (P3–P4):** create WordPress/PHP/Node/Python sites, each isolated and serving.
- **M4 (P5–P6):** full per-site tab functionality incl. dual-engine databases and Cloudflare.
- **M5 (P7):** hardened, packaged, documented v1.

---

## Verification (per the scope success criteria)

- `go build ./...` → `auracpd` + `auracp`; `web` builds and embeds.
- On a Debian 13 VM: installer brings up Caddy, FrankenPHP, Node 24, MariaDB, PostgreSQL; panel on `:8443`.
- Create each of the 6 site types (UI + CLI); `curl -I https://<domain>` → 200 with valid cert.
- Provision a MariaDB **and** a PostgreSQL DB on one site.
- Isolation: each backend runs as its own user; SFTP chroot-jailed.
- Idle RAM clearly below a comparable traditional-panel install.
