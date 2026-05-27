# auraCP — Debian/Ubuntu VM Test Checklist

End-to-end validation on a real host. Everything OS-mutating (Linux users, systemd units, Caddy
reloads, DB provisioning, SFTP jails) only truly runs on Linux, so this is the pass that exercises it.

**Targets to cover:** Debian 13 (trixie) and Ubuntu 24.04, on x86-64 **and** ARM64 if possible.

---

## 0. Build the artifacts (on your Mac / CI)

```bash
make dist          # static binaries → dist/auracpd-linux-{amd64,arm64}, auracp-linux-*
make deb           # dist/auracp_<ver>_{amd64,arm64}.deb   (uses dpkg-deb on Debian, portable writer elsewhere)
```

Copy the repo (or at least `dist/` + `installer/`) to the VM. A fresh, throwaway VM is ideal
(multipass, LXD, or a cheap cloud instance). Point a test domain's DNS at the VM if you want real SSL.

---

## 1. Provision the host

Two paths — pick one:

**A. Installer (recommended)** — installs the data plane *and* auraCP:
```bash
sudo ./installer/install.sh --dry-run                      # review the plan first
sudo ./installer/install.sh --yes --db=both --node=yes --php=yes --php-version=8.4 \
     --python=no --redis=no --typesense=no --docker=no --security=yes
```
If `dist/auracp_<ver>_<arch>.deb` is present it's installed via apt; otherwise the local binary is used.

**B. Package only** — if the data plane is already present:
```bash
sudo apt install ./dist/auracp_0.1.0_amd64.deb
```

**Expect:** Caddy, (chosen) MariaDB/PostgreSQL/Node/PHP services installed; `auracpd` enabled.

---

## 2. Reach the panel

```bash
systemctl status auracpd caddy                 # both active
journalctl -u auracpd | grep -A2 'initial admin'   # initial admin email + password (shown once)
ss -ltnp | grep -E ':8443|:80|:443'            # auracpd on 8443, caddy on 80/443
```
Open `https://<vm-ip>:8443`, log in, **enable 2FA**, change the admin password.

---

## 3. Create one site of each type (UI + CLI)

In the UI (**Add Site**) create: WordPress, PHP, Node.js, Python, Static, Reverse Proxy.
Also try the CLI: `auracp sites`.

For each, verify the real OS effects:
```bash
id <site-user>                                  # Linux user exists
ls -la /home/<site-user>/htdocs /home/<site-user>/logs
cat /etc/caddy/sites/<domain>.caddy             # rendered vhost
systemctl status auracp-site-<domain>           # backend unit (php/node/python only)
sudo -u caddy caddy validate --config /etc/caddy/Caddyfile
curl -kIL https://<domain>                      # 200/301; valid cert if DNS + Let's Encrypt
ps -eo user,args | grep -E 'frankenphp|node|gunicorn' | grep <site-user>   # runs as its own user
```
Use **Let's Encrypt staging** during testing to avoid rate limits (set in Caddy global config).

---

## 4. Per-database engine (the differentiator)

On a PHP/WordPress site → **Databases** tab:
- Add a **MariaDB** database; then add a **PostgreSQL** database on the *same* site.
```bash
mariadb -e "SHOW DATABASES;" | grep <db>
sudo -u postgres psql -c "\l" | grep <db>
```
Confirm the generated password is shown once and connect with it.

---

## 5. Per-site config write-backs

On a site, toggle and confirm each re-renders `/etc/caddy/sites/<domain>.caddy` and reloads cleanly:
- **Cache** on → `cache { ttl … }` block present.
- **Security → Basic auth** on (set user/pass) → `basic_auth` block; `curl` without creds → 401.
- **Security → Block bad bots** on → `curl -A SemrushBot https://<domain>` → 403.
- **SSL → Cloudflare DNS-01** (after setting the token under Instance → Cloudflare) → `tls { dns cloudflare … }`.
- **SSL tab** shows live issuer/expiry/domains once a cert is issued.

---

## 6. SSH/FTP users + isolation

Site → **SSH/FTP** → add an SFTP-only user and an SSH+SFTP user.
```bash
id <extra-user>                                 # exists, in auracp-sftp group
sftp <extra-user>@<vm-ip>                        # lands chroot-jailed in the site home
ssh <sftp-only-user>@<vm-ip>                     # refused (nologin shell)
```
Delete an extra user → confirm `id <user>` fails but the **site home is preserved**.

---

## 7. Cron, Logs, File Manager, Backups

- **Cron** → add a job → `crontab -u <site-user> -l` shows it; delete → it's gone.
- **Logs** → access/error tabs show real lines after you `curl` the site.
- **File Manager** → browse htdocs; confirm `../` traversal is rejected (stays in docroot).
- **Backups** (Settings tab) → Create Backup → `ls /var/lib/auracp/backups/<domain>/` has a `.tar.gz`;
  retention keeps the newest 5. If a remote is configured (Instance → Remote Backups), confirm
  `rclone ls <remote>` shows the upload.

---

## 8. RBAC + audit

- Create a **ROLE_USER** with read-only `sites` permission. Log in as them:
  - sites list loads; **Add Site** / delete are blocked (403); Users area hidden/forbidden.
- Edit a user's permission grid; confirm capabilities change.
- **Instance → Recent Activity** lists your actions with the correct actor/time.

---

## 9. Cross-arch sanity

Repeat §1–§3 on an **ARM64** VM with `dist/auracp_<ver>_arm64.deb`. Confirm the static binary runs
(`file /opt/auracp/bin/auracpd` → ARM aarch64) and sites serve.

---

## 10. Teardown

```bash
sudo apt remove auracp        # stops/disables the daemon (prerm)
# delete test sites first via the panel so their Linux users / vhosts are removed cleanly
```

---

## What "pass" looks like

- Fresh VM → panel on `:8443` within ~2 minutes.
- All six site types create, isolate (own Linux user), and serve over HTTPS.
- Both DB engines provision on one site.
- Config toggles, SSH/FTP, cron, logs, files, backups, SSL status all reflect real system state.
- RBAC blocks unauthorized actions; audit log records them.
- Idle `auracpd` RSS in the tens of MB (`systemctl status auracpd` / `ps -o rss`), well under a
  comparable legacy panel.

Record anything that needed a manual fix — those become installer/provisioning bug fixes.
