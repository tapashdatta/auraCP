#!/usr/bin/env bash
#
# auraCP uninstaller — removes the panel, the data-plane packages it installed,
# and all panel-created artifacts (site users, vhosts, per-site services, data),
# so you can start from a clean host.
#
# Usage:
#   sudo ./installer/uninstall.sh                 # interactive confirm
#   sudo ./installer/uninstall.sh --yes           # no prompt
#   sudo ./installer/uninstall.sh --dry-run       # show what would happen
#   sudo ./installer/uninstall.sh --keep-databases  # keep MariaDB/PostgreSQL + their data
#
# NOTE: this is destructive — it deletes hosted sites, their Linux users and
# home directories, and (by default) the database engines and all their data.
# It does NOT remove base tools (curl, cron, …) or system python3.
#
set -uo pipefail   # intentionally not -e: cleanup continues past missing items

YES=0
DRY=0
KEEP_DB=0
for a in "$@"; do
  case "$a" in
    --yes|-y) YES=1 ;;
    --dry-run) DRY=1 ;;
    --keep-databases) KEEP_DB=1 ;;
    -h|--help) grep '^#' "$0" | sed 's/^#\s\?//'; exit 0 ;;
    *) echo "unknown option: $a"; exit 1 ;;
  esac
done

if [ -t 1 ]; then
  R=$'\e[31m'; G=$'\e[32m'; Y=$'\e[33m'; C=$'\e[36m'; D=$'\e[2m'; Z=$'\e[0m'
else R=""; G=""; Y=""; C=""; D=""; Z=""; fi
msg(){ printf '%s\n' "${C}::${Z} $*"; }
ok(){ printf '%s\n' "${G}✓${Z} $*"; }
run(){ if [ "$DRY" -eq 1 ]; then printf '%s\n' "${D}[dry-run]${Z} $*"; else eval "$@" || true; fi; }

[ "$DRY" -eq 1 ] || [ "$(id -u)" -eq 0 ] || { echo "${R}run as root (sudo)${Z}"; exit 1; }

cat <<EOF
${Y}This will remove auraCP and everything it installed:${Z}
  • auracpd panel (package, service, /opt/auracp, /etc/auracp, /var/lib/auracp)
  • all hosted sites: their Linux users, /home dirs, vhosts, per-site services
  • Caddy, FrankenPHP, Node.js
  • Redis, Typesense, Docker, UFW + fail2ban (if installed)
$( [ "$KEEP_DB" -eq 1 ] && echo "  • (keeping MariaDB / PostgreSQL and their data)" || echo "  ${R}• MariaDB and PostgreSQL — including ALL database data${Z}" )
  (base tools like curl/cron and system python3 are left untouched)
EOF

if [ "$DRY" -eq 0 ] && [ "$YES" -eq 0 ] && [ -t 0 ]; then
  read -r -p "Type 'remove' to proceed: " ans < /dev/tty || true
  [ "$ans" = "remove" ] || { echo "aborted."; exit 1; }
fi

export DEBIAN_FRONTEND=noninteractive

# ── 1. panel + per-site backends ────────────────────────────────────────────
msg "Removing per-site backend services…"
for f in /etc/systemd/system/auracp-site-*.service; do
  [ -e "$f" ] || continue
  run "systemctl disable --now $(basename "$f")"
  run "rm -f $f"
done

msg "Removing hosted-site Linux users…"
if getent group auracp-sftp >/dev/null 2>&1; then
  members=$(getent group auracp-sftp | awk -F: '{print $4}' | tr ',' ' ')
  for u in $members; do
    [ -n "$u" ] || continue
    run "pkill -9 -u $u 2>/dev/null"
    run "userdel -rf $u"
  done
  run "groupdel auracp-sftp"
fi
run "rm -f /etc/ssh/sshd_config.d/auracp-sftp.conf"
run "systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null"

msg "Removing auracpd panel…"
run "systemctl disable --now auracpd"
# Belt-and-suspenders: kill any leftover process and ensure :8443 is free.
run "pkill -9 -f /opt/auracp/bin/auracpd 2>/dev/null"
run "pkill -9 -x auracpd 2>/dev/null"
# Purge the panel package — handles any installed version of 'auracp'.
run "apt-get purge -y auracp"
# Belt-and-suspenders for any leftover auracp-prefixed packages.
pkgs=$(dpkg-query -W -f='${Package}\n' 'auracp*' 2>/dev/null || true)
if [ -n "$pkgs" ]; then run "apt-get purge -y $pkgs"; fi
run "rm -f /etc/systemd/system/auracpd.service"
run "rm -rf /etc/systemd/system/auracpd.service.d"   # panel-domain drop-in et al
run "rm -rf /opt/auracp /etc/auracp /var/lib/auracp"
# bundled-installer command symlinks (from the .deb postinst)
run "rm -f /usr/local/bin/auracp /usr/local/bin/auracpd /usr/local/bin/auracp-install /usr/local/bin/auracp-uninstall"
run "systemctl daemon-reload"
ok "Panel removed."

# ── 2. web server + PHP + node ──────────────────────────────────────────────
msg "Removing Caddy…"
run "systemctl disable --now caddy"
run "pkill -9 -x caddy 2>/dev/null"          # ensure :80 and :443 are free
run "rm -f /etc/systemd/system/caddy.service"
run "rm -f /usr/bin/caddy"
run "rm -rf /etc/caddy /var/lib/caddy"
run "id caddy >/dev/null 2>&1 && userdel -rf caddy"
run "systemctl daemon-reload"

msg "Removing FrankenPHP…"
run "rm -f /usr/bin/frankenphp"

msg "Removing Node.js…"
run "apt-get purge -y nodejs"
run "rm -f /etc/apt/sources.list.d/nodesource.list /etc/apt/keyrings/nodesource.gpg"

# ── 3. databases ────────────────────────────────────────────────────────────
if [ "$KEEP_DB" -eq 0 ]; then
  msg "Removing MariaDB (+ data)…"
  run "systemctl disable --now mariadb"
  run "apt-get purge -y mariadb-server mariadb-client mariadb-common"
  run "rm -rf /var/lib/mysql /etc/mysql"
  msg "Removing PostgreSQL (+ data)…"
  run "systemctl disable --now postgresql"
  run "apt-get purge -y postgresql postgresql-common 'postgresql-*'"
  run "rm -rf /var/lib/postgresql /etc/postgresql"
else
  msg "Keeping databases (--keep-databases)."
fi

# ── 4. optional components ──────────────────────────────────────────────────
msg "Removing Redis…"
run "apt-get purge -y redis-server redis-tools"

msg "Removing Typesense…"
run "systemctl disable --now typesense-server"
run "apt-get purge -y typesense-server"

msg "Removing Docker…"
run "systemctl disable --now docker"
run "apt-get purge -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker.io"
run "rm -rf /var/lib/docker /etc/docker /etc/apt/sources.list.d/docker.list"

msg "Removing firewall + fail2ban…"
run "ufw --force reset 2>/dev/null"
run "ufw --force disable 2>/dev/null"
run "apt-get purge -y ufw fail2ban"

# ── 5. apt cleanup ──────────────────────────────────────────────────────────
msg "Cleaning up apt…"
run "apt-get autoremove -y --purge"
run "apt-get clean"

echo
ok "auraCP and its packages removed. The host is clean."
[ "$DRY" -eq 1 ] && echo "${D}(dry-run — nothing was actually changed)${Z}"
echo "Reinstall any time with: sudo ./installer/install.sh"
