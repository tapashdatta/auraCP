# auraCP — Scope

**Status:** Draft v0.1 · 2026-05-27
**Owner:** info@iskcon.london

---

## 1. Vision

auraCP is a **lightweight, self-hosted server control panel** — a modern alternative to
[CloudPanel](https://www.cloudpanel.io). It replicates CloudPanel's feature set and UX while running
on a smaller, modern stack, taking **as few server resources as possible**, and adding two
capabilities CloudPanel lacks: **PostgreSQL per site** and **Node.js (v24 LTS) preinstalled on every
site**. No email server is included.

### Design principles

1. **Minimal footprint.** Install nothing that isn't used. The control plane is a single Go binary;
   the data plane installs packages on demand. (See §6.)
2. **Feature parity with CloudPanel.** Same screens, same per-site tabs, same workflows.
3. **Modern + elegant.** Enterprise-grade UI, light theme by default with a dark toggle.
4. **Secure by default.** Per-site Linux user isolation, automatic HTTPS, fail2ban, firewall.

---

## 2. Target environment

| Item | Decision |
|---|---|
| OS | **Debian 13** (Trixie) primary; Debian 12 / Ubuntu 24.04 best-effort later |
| Arch | x86-64 and ARM64 |
| Panel port | `8443` (HTTPS) |
| Min resources | 1 vCPU / 1 GB RAM / 10 GB disk target (CloudPanel needs ~2 GB+) |

---

## 3. In scope (v1)

### 3.1 Site types
WordPress · PHP · Node.js · Static HTML · Python · Reverse Proxy.
(WordPress is internally a PHP site provisioned with `wp-cli`.)

### 3.2 Per-site management tabs
Mirror CloudPanel exactly:

| Tab | Function |
|---|---|
| **Settings** | Domain, document root, HTTPS redirect, HTTP/3, per-runtime settings (PHP/Node/Python) |
| **Vhost** | Editable Caddyfile template; reload on save |
| **Databases** | Create/manage databases & users — **choose MariaDB or PostgreSQL per database** |
| **Cache** | Full-page cache (Souin in Caddy); TTL, purge, hit ratio |
| **SSL/TLS** | Auto Let's Encrypt (Caddy); import cert; Cloudflare DNS-01 / wildcard |
| **Security** | Basic auth, block bots, block IPs, fail2ban |
| **SSH/FTP** | Per-site SSH & SFTP users, chroot-jailed |
| **File Manager** | Browse/edit files scoped to the site home (privilege-dropped) |
| **Cron Jobs** | Per-site cron entries, run as the site user |
| **Logs** | Live tail: access, error, runtime (PHP/Node/Python) |

### 3.3 Instance / admin
Users (panel admins, RBAC) · Events (audit log) · Instance (services, timezone, reboot) ·
Backups (local + remote: S3, Spaces, Dropbox, Google Drive, SFTP, rclone) · Security (UFW firewall
rules) · Settings.

### 3.4 Platform features
- Site manager list (Domain · Site User · App · Status · Manage) + "Add Site" flow.
- Free **SSL/TLS** (Let's Encrypt, automatic).
- **Cloudflare integration** (DNS-01 challenges, cache purge).
- **High-performance** data plane: HTTP/2 + HTTP/3, Brotli/zstd, full-page cache.
- **Node 24 LTS preinstalled** on every site.
- **Per-site PostgreSQL or MariaDB.**
- 2FA (TOTP) for panel users.
- CLI (`auracp`) mirroring the UI for automation.
- API (token-authenticated).

---

## 4. Out of scope (v1)

- Email server (mail, IMAP/SMTP, webmail) — explicitly excluded.
- Multi-server / cluster orchestration (single host only).
- DNS hosting (we *integrate* with Cloudflare, we don't run a nameserver).
- Marketplace / one-click app catalog beyond WordPress.
- Windows hosting.
- Billing / multi-tenant reseller features.

---

## 5. Differentiators vs CloudPanel

| Area | CloudPanel | auraCP |
|---|---|---|
| Control plane | Symfony 6 (PHP, runs as root-equiv user) | **Go** single binary (~15–25 MB idle) |
| Web server | nginx + Varnish + PHP-FPM | **Caddy + FrankenPHP + Souin** |
| SSL | hand-rolled PHP ACME client | **Caddy automatic HTTPS** |
| Databases | MySQL/MariaDB only | **MariaDB *and* PostgreSQL, per database** |
| Node.js | only for Node sites | **Node 24 LTS on every site by default** |
| PHP versions | installs 7.1 → 8.5 (+~25 ext each) | **8.3+ only, on demand** |
| Monitoring | separate compiled agent | **in-process goroutine** |
| Footprint | heavy | **minimal — install only what's used** |

---

## 6. Minimal-package policy

A standing constraint (see also the architecture doc):

- **PHP:** only **8.3 and newer**. Never the legacy 7.x/8.0–8.2 range.
- **Per site type, install only what's needed** — a Static site pulls no runtime; a Node site
  doesn't pull PHP; etc. (Node 24 is the one baseline runtime present everywhere.)
- **No mail stack** (no postfix), **no Varnish/proftpd/memcached/uwsgi** bloat from the CloudPanel
  dependency set unless a feature genuinely requires it.
- Every package added to the installer must be justified against an in-use feature.

---

## 7. Success criteria

- Fresh Debian 13 box → panel reachable on `:8443` in under ~2 minutes.
- Create one site of each of the 6 types via UI and CLI; each serves over valid HTTPS.
- Provision both a MariaDB and a PostgreSQL database on the same site.
- Per-site isolation verified (each backend runs as its own Linux user; SFTP chroot-jailed).
- Idle RAM materially below a comparable CloudPanel install.
