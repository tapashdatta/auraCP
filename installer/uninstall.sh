#!/usr/bin/env bash
#
# auraCP uninstaller — removes the panel, the data-plane packages it installed,
# and all panel-created artifacts (site users, vhosts, per-site services, data),
# so the host returns to its baseline.
#
# v0.2.0 stack: removes nginx + PHP-FPM (multi-version) + Node + MariaDB +
# PostgreSQL + Redis + Typesense + Docker + UFW + fail2ban as installed.
# (Pre-v0.2.0 hosts: residual Caddy / FrankenPHP / Souin artifacts are also
# scrubbed by the content-based apt sweep + the legacy keyring entries below.)
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
  • nginx, PHP-FPM (all installed versions), Node.js
  • Redis, Typesense, Docker, UFW + fail2ban (if installed)
  • legacy v0.1.x residue: Caddy, FrankenPHP, Souin keyrings + sources
$( [ "$KEEP_DB" -eq 1 ] && echo "  • (keeping MariaDB / PostgreSQL and their data)" || echo "  ${R}• MariaDB and PostgreSQL — including ALL database data${Z}" )
  (base tools like curl/cron and system python3 are left untouched)
EOF

if [ "$DRY" -eq 0 ] && [ "$YES" -eq 0 ] && [ -t 0 ]; then
  read -r -p "Type 'remove' to proceed: " ans < /dev/tty || true
  [ "$ans" = "remove" ] || { echo "aborted."; exit 1; }
fi

export DEBIAN_FRONTEND=noninteractive

# ── 0. apt-source preflight ──────────────────────────────────────────────────
# Strip third-party apt sources installed by any auraCP version BEFORE we run
# apt-get. Content-based so it catches deb822 .sources files, renamed .list
# files, and entries inside /etc/apt/sources.list itself. Includes both v0.2.0
# additions (packages.sury.org, nginx.org) and v0.1.x leftovers (NodeSource,
# pkg.dunglas.dev/static-php/frankenphp).
msg "Sweeping apt for third-party sources auraCP-versions installed…"
sweep_pat='deb\.nodesource\.com|mirror\.mariadb\.org|apt\.postgresql\.org|download\.docker\.com|pkg\.dunglas\.dev|static-php|frankenphp|dl\.typesense\.org|packages\.sury\.org|nginx\.org/packages'
for src in /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
  [ -e "$src" ] || continue
  if grep -qE "$sweep_pat" "$src" 2>/dev/null; then
    printf '  %s\n' "removing $src"
    run "rm -f '$src'"
  fi
done
if [ -e /etc/apt/sources.list ] && grep -qE "$sweep_pat" /etc/apt/sources.list 2>/dev/null; then
  printf '  %s\n' "scrubbing matching lines from /etc/apt/sources.list"
  run "sed -i.bak -E '/${sweep_pat}/d' /etc/apt/sources.list"
fi
# Orphaned keyrings — current (v0.2.0) and legacy (v0.1.x).
for kr in /etc/apt/keyrings/nodesource.gpg \
          /usr/share/keyrings/nodesource.gpg \
          /etc/apt/keyrings/nodesource-keyring.gpg \
          /etc/apt/keyrings/static-php*.gpg \
          /usr/share/keyrings/static-php*.gpg \
          /etc/apt/keyrings/frankenphp*.gpg \
          /usr/share/keyrings/frankenphp*.gpg \
          /etc/apt/keyrings/pkg-dunglas*.gpg \
          /usr/share/keyrings/pkg-dunglas*.gpg \
          /usr/share/keyrings/mariadb.asc \
          /usr/share/keyrings/pgdg.gpg \
          /etc/apt/keyrings/docker.gpg \
          /etc/apt/keyrings/docker-archive-keyring.gpg \
          /usr/share/keyrings/sury-php.gpg \
          /usr/share/keyrings/nginx-archive-keyring.gpg; do
  [ -e "$kr" ] && run "rm -f '$kr'"
done

# ── 0c. auraCP-laid config snippets in shared /etc subdirs ──────────────────
msg "Removing auraCP-laid config snippets…"
for d in /etc/apt/preferences.d /etc/sudoers.d /etc/cron.d /etc/cron.daily \
         /etc/cron.hourly /etc/cron.weekly /etc/logrotate.d /etc/rsyslog.d \
         /etc/profile.d /etc/security/limits.d /etc/sysctl.d \
         /etc/fail2ban/jail.d /etc/fail2ban/filter.d /etc/modules-load.d \
         /etc/tmpfiles.d; do
  [ -d "$d" ] || continue
  for f in "$d"/auracp* "$d"/*auracp*; do
    [ -e "$f" ] && run "rm -f '$f'"
  done
done
if [ -e /etc/hosts ] && grep -q '# auracp' /etc/hosts 2>/dev/null; then
  run "sed -i '/# auracp/d' /etc/hosts"
fi

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
run "pkill -9 -f /opt/auracp/bin/auracpd 2>/dev/null"
run "pkill -9 -x auracpd 2>/dev/null"
auracp_pkgs=$(dpkg-query -W -f='${Package} ${db:Status-Status}\n' 'auracp*' 2>/dev/null \
              | awk '$2=="installed"{print $1}' | tr '\n' ' ')
[ -n "$auracp_pkgs" ] && run "apt-get purge -y $auracp_pkgs"
run "rm -f /etc/systemd/system/auracpd.service"
run "rm -rf /etc/systemd/system/auracpd.service.d"
run "rm -rf /opt/auracp /etc/auracp /var/lib/auracp"
run "rm -f /usr/local/bin/auracp /usr/local/bin/auracpd /usr/local/bin/auracp-install /usr/local/bin/auracp-uninstall"
run "systemctl daemon-reload"
ok "Panel removed."

# ── 2. nginx + PHP-FPM + Node ──────────────────────────────────────────────
msg "Removing nginx…"
stop_unit nginx
run "pkill -9 -x nginx 2>/dev/null"
purge_installed nginx nginx-common nginx-mod-http-cache-purge libnginx-mod-cache-purge
run "rm -rf /etc/nginx /var/lib/nginx /var/log/nginx /var/cache/nginx /run/php-fpm"
# Legacy v0.1.x Caddy residue (idempotent — won't error if absent).
run "rm -rf /etc/systemd/system/caddy.service /etc/systemd/system/caddy.service.d"
run "rm -f /usr/bin/caddy"
run "rm -rf /etc/caddy /var/lib/caddy /root/.local/share/caddy /root/.config/caddy"
run "id caddy >/dev/null 2>&1 && userdel -rf caddy"
run "systemctl daemon-reload"

msg "Removing PHP-FPM (all versions)…"
# Enumerate installed php*-fpm packages and purge them all.
php_pkgs=$(dpkg-query -W -f='${Package} ${db:Status-Status}\n' 'php*' 2>/dev/null \
           | awk '$2=="installed"{print $1}' | tr '\n' ' ')
[ -n "$php_pkgs" ] && run "apt-get purge -y $php_pkgs"
run "rm -rf /etc/php /var/lib/php /var/log/php*"
# Legacy v0.1.x FrankenPHP residue.
run "rm -f /usr/bin/frankenphp"

msg "Removing Node.js…"
purge_installed nodejs
run "rm -f /etc/apt/sources.list.d/nodesource.list"
run "rm -f /etc/apt/keyrings/nodesource.gpg /usr/share/keyrings/nodesource.gpg"
run "rm -f /usr/local/bin/node /usr/local/bin/npm /usr/local/bin/npx"

# ── 3. databases ────────────────────────────────────────────────────────────
if [ "$KEEP_DB" -eq 0 ]; then
  msg "Removing MariaDB (+ data)…"
  stop_unit mariadb
  purge_installed mariadb-server mariadb-client mariadb-common
  run "rm -rf /var/lib/mysql /etc/mysql"
  run "rm -f /etc/apt/sources.list.d/mariadb.list /usr/share/keyrings/mariadb.asc"

  msg "Removing PostgreSQL (+ data)…"
  stop_unit postgresql
  pg_pkgs=$(dpkg-query -W -f='${Package} ${db:Status-Status}\n' 'postgresql*' 2>/dev/null \
            | awk '$2=="installed"{print $1}' | tr '\n' ' ')
  [ -n "$pg_pkgs" ] && run "apt-get purge -y $pg_pkgs"
  run "rm -rf /var/lib/postgresql /etc/postgresql"
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

# ── 5. service users + groups that apt-purge keeps for re-install rebinding ─
msg "Removing service users + groups left behind by purged packages…"
if [ "$KEEP_DB" -eq 0 ]; then
  for u in mysql postgres; do
    id "$u" >/dev/null 2>&1 && run "userdel -rf $u 2>/dev/null"
  done
fi
for u in redis _typesense typesense; do
  id "$u" >/dev/null 2>&1 && run "userdel -rf $u 2>/dev/null"
done
if getent group docker >/dev/null 2>&1; then
  run "groupdel docker 2>/dev/null"
fi

# ── 6. residual / temp + apt cleanup ────────────────────────────────────────
msg "Cleaning installer temp files…"
run "rm -f /tmp/nodesource_setup.sh /tmp/get-docker.sh /tmp/typesense-server.deb"
run "rm -f /tmp/auracp-cron-* 2>/dev/null"
run "rm -f /etc/apt/sources.list.bak /etc/hosts.bak"
run "rm -rf /var/lib/auracp/backups"

msg "Refreshing apt (sources we removed are now gone)…"
run "apt-get update -y"
run "apt-get autoremove -y --purge"
run "apt-get autoclean"
run "apt-get clean"

echo
ok "auraCP and its packages removed. The host is back to its baseline."
[ "$DRY" -eq 1 ] && echo "${D}(dry-run — nothing was actually changed)${Z}"
echo "Reinstall any time with: sudo auracp-install  (or download a fresh .deb from GitHub Releases)"
