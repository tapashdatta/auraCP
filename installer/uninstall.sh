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

# Quietly stop+disable a systemd unit only if it actually exists — avoids the
# scary "Failed to disable unit: …does not exist" message on partial installs.
stop_unit() {
  local svc="$1"
  if [ "$DRY" -eq 1 ]; then
    printf '%s\n' "${D}[dry-run]${Z} systemctl disable --now ${svc} (if present)"
    return
  fi
  systemctl list-unit-files --type=service --no-legend 2>/dev/null \
    | awk '{print $1}' | grep -qx "${svc}.service" || return 0
  systemctl disable --now "$svc" >/dev/null 2>&1 || true
}

# Purge only those of the listed packages that are actually installed. Avoids
# apt's regex pattern-matching (which "postgresql-*" triggers) and the wall of
# "Package … is not installed, so not removed" lines.
purge_installed() {
  local pkgs="" p
  for p in "$@"; do
    if dpkg-query -W -f='${db:Status-Status}\n' "$p" 2>/dev/null | grep -qx 'installed'; then
      pkgs="$pkgs $p"
    fi
  done
  [ -n "$pkgs" ] || return 0
  run "apt-get purge -y$pkgs"
}

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
stop_unit auracpd
# Belt-and-suspenders: kill any leftover process and ensure :8443 is free.
run "pkill -9 -f /opt/auracp/bin/auracpd 2>/dev/null"
run "pkill -9 -x auracpd 2>/dev/null"
# Purge any installed auracp* packages (no globs into apt — we enumerate first).
auracp_pkgs=$(dpkg-query -W -f='${Package} ${db:Status-Status}\n' 'auracp*' 2>/dev/null \
              | awk '$2=="installed"{print $1}' | tr '\n' ' ')
[ -n "$auracp_pkgs" ] && run "apt-get purge -y $auracp_pkgs"
run "rm -f /etc/systemd/system/auracpd.service"
run "rm -rf /etc/systemd/system/auracpd.service.d"   # panel-domain drop-in et al
run "rm -rf /opt/auracp /etc/auracp /var/lib/auracp"
# bundled-installer command symlinks (from the .deb postinst)
run "rm -f /usr/local/bin/auracp /usr/local/bin/auracpd /usr/local/bin/auracp-install /usr/local/bin/auracp-uninstall"
run "systemctl daemon-reload"
ok "Panel removed."

# ── 2. web server + PHP + node ──────────────────────────────────────────────
msg "Removing Caddy…"
stop_unit caddy
run "pkill -9 -x caddy 2>/dev/null"          # ensure :80 and :443 are free
run "rm -f /etc/systemd/system/caddy.service"
run "rm -f /usr/bin/caddy"
run "rm -rf /etc/caddy /var/lib/caddy"
# Caddy run as root also stashes auto-managed certs/state under /root by default.
run "rm -rf /root/.local/share/caddy /root/.config/caddy"
run "id caddy >/dev/null 2>&1 && userdel -rf caddy"
run "systemctl daemon-reload"

msg "Removing FrankenPHP…"
run "rm -f /usr/bin/frankenphp"
# The FrankenPHP installer (frankenphp.dev/install.sh) adds an apt source for
# static PHP builds (pkg.dunglas.dev → /etc/apt/sources.list.d/static-php*.list)
# and a keyring under /etc/apt/keyrings or /usr/share/keyrings. Scrub the lot —
# its names have varied across versions, so glob both common locations.
run "rm -f /etc/apt/sources.list.d/static-php*.list /etc/apt/sources.list.d/frankenphp*.list"
run "rm -f /etc/apt/keyrings/static-php*.gpg /etc/apt/keyrings/frankenphp*.gpg /etc/apt/keyrings/pkg-dunglas*.gpg"
run "rm -f /usr/share/keyrings/static-php*.gpg /usr/share/keyrings/frankenphp*.gpg /usr/share/keyrings/pkg-dunglas*.gpg"

msg "Removing Node.js…"
# Legacy NodeSource leftovers from older installers (apt-managed nodejs).
purge_installed nodejs
run "rm -f /etc/apt/sources.list.d/nodesource.list"
run "rm -f /etc/apt/keyrings/nodesource.gpg /usr/share/keyrings/nodesource.gpg"
# auraCP-managed Node lives under /opt/auracp/node (removed with the panel above);
# also clean the system-PATH symlinks the installer added.
run "rm -f /usr/local/bin/node /usr/local/bin/npm /usr/local/bin/npx"

# ── 3. databases ────────────────────────────────────────────────────────────
if [ "$KEEP_DB" -eq 0 ]; then
  msg "Removing MariaDB (+ data)…"
  stop_unit mariadb
  purge_installed mariadb-server mariadb-client mariadb-common
  run "rm -rf /var/lib/mysql /etc/mysql"
  # third-party apt source/keyring added by the installer for mariadb.org
  run "rm -f /etc/apt/sources.list.d/mariadb.list /usr/share/keyrings/mariadb.asc"

  msg "Removing PostgreSQL (+ data)…"
  stop_unit postgresql
  # Enumerate installed postgresql* packages — avoids apt's glob match against
  # every postgresql-* package in your apt sources (hundreds of "not installed").
  pg_pkgs=$(dpkg-query -W -f='${Package} ${db:Status-Status}\n' 'postgresql*' 2>/dev/null \
            | awk '$2=="installed"{print $1}' | tr '\n' ' ')
  [ -n "$pg_pkgs" ] && run "apt-get purge -y $pg_pkgs"
  run "rm -rf /var/lib/postgresql /etc/postgresql"
  # third-party apt source/keyring added by the installer for apt.postgresql.org
  run "rm -f /etc/apt/sources.list.d/pgdg.list /usr/share/keyrings/pgdg.gpg"
else
  msg "Keeping databases (--keep-databases)."
fi

# ── 4. optional components ──────────────────────────────────────────────────
msg "Removing Redis…"
stop_unit redis-server
purge_installed redis-server redis-tools
run "rm -rf /var/lib/redis /etc/redis /var/log/redis"

msg "Removing Typesense…"
stop_unit typesense-server
purge_installed typesense-server
run "rm -rf /etc/typesense-server /var/lib/typesense"

msg "Removing Docker…"
stop_unit docker
purge_installed docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin docker.io
run "rm -rf /var/lib/docker /etc/docker"
run "rm -f /etc/apt/sources.list.d/docker.list"
run "rm -f /etc/apt/keyrings/docker.gpg /etc/apt/keyrings/docker-archive-keyring.gpg"

msg "Removing firewall + fail2ban…"
if command -v ufw >/dev/null 2>&1; then
  run "ufw --force reset"
  run "ufw --force disable"
fi
purge_installed ufw fail2ban

# ── 5. residual / temp + apt cleanup ────────────────────────────────────────
msg "Cleaning installer temp files…"
run "rm -f /tmp/nodesource_setup.sh /tmp/get-docker.sh /tmp/typesense-server.deb"
run "rm -f /tmp/auracp-cron-* 2>/dev/null"
# any backup tarballs the panel produced
run "rm -rf /var/lib/auracp/backups"

msg "Refreshing apt (sources we removed are now gone)…"
run "apt-get update -y"
run "apt-get autoremove -y --purge"
run "apt-get autoclean"
run "apt-get clean"

echo
ok "auraCP and its packages removed. The host is back to its baseline."
[ "$DRY" -eq 1 ] && echo "${D}(dry-run — nothing was actually changed)${Z}"
echo "Reinstall any time with: sudo ./installer/install.sh"
