# Upgrading from auraCP v0.1.x to v0.2.0

**TL;DR:** v0.2.0 swaps the data plane (Caddy → nginx, FrankenPHP → PHP-FPM). The two cannot
coexist on the same host — they fight over `:80`/`:443` and over `/etc/php`. The supported path
is **uninstall v0.1.x, snapshot what you need, install v0.2.0, restore.**

This is intentional: v0.2.0 is a major version bump (`0.2.0`, not `0.1.19`) precisely so the
break is visible. A future minor will add an `auracp migrate` command, but that's not in v0.2.0.

---

## What changes

| Component | v0.1.x | v0.2.0 |
|---|---|---|
| Front web server | Caddy 2.x (custom build with Souin + cloudflare DNS) | **nginx 1.30 mainline** (from nginx.org) |
| PHP runtime | FrankenPHP per-site systemd unit | **PHP-FPM, one pool per site** (Unix socket), multi-version via `deb.sury.org` |
| Auto-HTTPS | delegated to Caddy | **`go-acme/lego` in `auracpd`** (HTTP-01 + Cloudflare DNS-01) |
| Page cache | Souin (Caddy module) | **nginx `fastcgi_cache` + `proxy_cache`** |
| Per-site `php_version` knob | label only — FrankenPHP bundles one PHP | **honoured** — pool config names the version |
| Cert paths on disk | inside `/var/lib/caddy/.local/share/caddy/...` | **`/etc/auracp/ssl/<domain>.{crt,key}`** |
| Site Caddyfile location | `/etc/caddy/sites/<domain>.caddy` | **`/etc/nginx/sites-available/<domain>.conf`** + symlink |

What's unchanged: SQLite state (sites/users/databases/cron/audit_log all carry over), per-site
Linux users, chroot SFTP setup, MariaDB + PostgreSQL state, Node + Python sites, the panel's API
shape and UI (apart from string labels).

---

## Pre-upgrade snapshot

The uninstall is destructive. Before running it, snapshot **everything you cannot regenerate**:

```bash
# 1) the panel's state database — sites/users/databases/cron/audit_log/RBAC live here
sudo cp /var/lib/auracp/auracp.db ~/auracp.db.backup-$(date +%F)

# 2) the panel's secret key (decrypts secrets in the DB — DO NOT lose this)
sudo cp /etc/auracp/secret.key ~/auracp-secret.key.backup-$(date +%F)
sudo chmod 600 ~/auracp-secret.key.backup-*

# 3) every site's home dir (htdocs + logs + per-user dotfiles)
sudo tar -C /home -czf ~/auracp-homes-$(date +%F).tgz \
  $(getent group auracp-sftp | awk -F: '{print $4}' | tr ',' ' ')

# 4) database dumps (if you have any DBs)
sudo mysqldump --all-databases --single-transaction --quick > ~/all-mariadb-$(date +%F).sql 2>/dev/null || true
sudo -u postgres pg_dumpall > ~/all-postgres-$(date +%F).sql 2>/dev/null || true
```

The panel domain setting, Cloudflare token, and remote backup destinations all live in the
SQLite state — they come back when you restore the DB.

---

## The upgrade itself

```bash
# 1) Uninstall v0.1.x (keeps databases — we'll keep MariaDB / PostgreSQL data in place):
sudo auracp-uninstall --keep-databases --yes
# OR if you want a completely clean OS first and will restore DBs from your dumps:
# sudo auracp-uninstall --yes

# 2) `apt-get update` should be silent now — confirm:
sudo apt update

# 3) Install v0.2.0:
ARCH=$(dpkg --print-architecture)
curl -fL -o /tmp/auracp.deb \
  "https://github.com/tapashdatta/auraCP/releases/download/v0.2.0/auracp_0.2.0_${ARCH}.deb"
sudo dpkg -i /tmp/auracp.deb
sudo auracp-install --yes --db=both --php=yes --php-version=8.4 --node=yes \
                    --panel-domain=panel.example.com

# 4) Stop auracpd before swapping the state DB in:
sudo systemctl stop auracpd

# 5) Restore the state + secret key:
sudo cp ~/auracp.db.backup-* /var/lib/auracp/auracp.db
sudo cp ~/auracp-secret.key.backup-* /etc/auracp/secret.key
sudo chown root:root /var/lib/auracp/auracp.db /etc/auracp/secret.key
sudo chmod 600 /etc/auracp/secret.key

# 6) Restore home dirs (per-site Linux users + htdocs):
sudo tar -C /home -xzf ~/auracp-homes-*.tgz

# 7) Start auracpd — it'll auto-create the new tables (certificates, php_runtimes, php_settings)
#    via migrations on first start, then reconcile installed php-fpm versions:
sudo systemctl start auracpd
```

At this point each existing site needs its nginx vhost and PHP-FPM pool **rewritten** — the old
Caddyfiles and FrankenPHP units are gone. The panel does this automatically per site, but you
have to trigger it. Two paths:

**Option A — touch every site via the UI:** open each site's Settings, hit "Save". This calls
`POST /api/sites/{domain}/reapply` which re-renders the vhost + (for PHP) the pool config.

**Option B — one-liner:** the `auracp` CLI can do it in a loop:

```bash
for d in $(auracp site:list --format=domain); do
  auracp site:reapply "$d"
done
```

Cert issuance runs in the background; watch `journalctl -u auracpd -f` to see each cert land
(or fail with a hint — usually Cloudflare orange-cloud blocking HTTP-01; see the SSL/TLS tab
to switch to DNS-01).

---

## Rollback (if v0.2.0 doesn't work for you)

Same path in reverse:

```bash
sudo auracp-uninstall --keep-databases --yes
# install the last v0.1.x release (latest was v0.1.18):
ARCH=$(dpkg --print-architecture)
curl -fL -o /tmp/auracp.deb \
  "https://github.com/tapashdatta/auraCP/releases/download/v0.1.18/auracp_0.1.18_${ARCH}.deb"
sudo dpkg -i /tmp/auracp.deb
sudo auracp-install --yes --db=both --node=yes --php=yes
# … and restore the same backups (the schema is forward-compatible — v0.1.x ignores the new tables).
```

The v0.2.0 schema is a strict superset of v0.1.x's, so the rollback DB import will work.
v0.1.x will simply not know about `certificates`, `php_runtimes`, or `php_settings` — harmless
ignored rows.

---

## What if I don't want to rewrite all my sites?

You don't. The DB carries the full per-site definition; touching each site re-renders the
nginx vhost and (for PHP) the FPM pool from that definition. No content move, no file edits.
The 1-2 minutes per site is the cert issuance, not config work.

---

## What about my Cloudflare token / panel domain / remote backups?

All in the SQLite state — they come back when you restore the DB. The panel-domain systemd
drop-in gets re-created on first auracpd start since the setting is persisted; just check
`systemctl cat auracpd` afterwards to confirm `-panel-domain=…` is on the ExecStart line.
