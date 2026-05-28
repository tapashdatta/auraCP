#!/usr/bin/env bash
#
# auraCP installer — lightweight server control panel for Debian 13.
#
# Required packages are always installed; everything else is the admin's choice
# (MariaDB and/or PostgreSQL, Node.js, PHP, Python, Redis, security hardening).
# Install only what you'll use — that's the whole point of auraCP.
#
# Usage:
#   sudo ./install.sh                 # interactive
#   sudo ./install.sh --yes           # non-interactive, use defaults/flags
#   sudo ./install.sh --dry-run       # show the plan, change nothing
#
# Selection flags (override defaults; imply --non-interactive when given):
#   --db=mariadb|postgres|both|none   --node=yes|no      --php=yes|no
#   --php-version=8.3|8.4|8.5         --python=yes|no     --redis=yes|no
#   --typesense=yes|no                --docker=yes|no
#   --security=yes|no                 --port=8443
#   --panel-domain=panel.example.com  (front the panel; Caddy issues its SSL cert)
#
# Or via env: AURACP_MARIADB, AURACP_POSTGRES, AURACP_NODE, AURACP_PHP,
#   AURACP_PHP_VERSION, AURACP_PYTHON, AURACP_REDIS, AURACP_SECURITY, AURACP_PORT
#
set -euo pipefail

# ──────────────────────────────────────────────────────────────────────────
# config & defaults
# ──────────────────────────────────────────────────────────────────────────
AURACP_VERSION="0.1.1"
PANEL_PORT="${AURACP_PORT:-8443}"
PANEL_DOMAIN="${AURACP_PANEL_DOMAIN:-}"   # optional: front the panel at this domain
NODE_MAJOR="24"                         # Node 24 LTS — the baseline default

ASSUME_YES=0
DRY_RUN=0
INTERACTIVE=1                           # auto-disabled when selection flags are passed

# optional components (yes/no); defaults chosen for a typical PHP+DB host
OPT_PHP="${AURACP_PHP:-yes}"
PHP_VERSION="${AURACP_PHP_VERSION:-8.4}"
OPT_NODE="${AURACP_NODE:-yes}"
OPT_PYTHON="${AURACP_PYTHON:-no}"
OPT_MARIADB="${AURACP_MARIADB:-yes}"
OPT_POSTGRES="${AURACP_POSTGRES:-no}"
OPT_REDIS="${AURACP_REDIS:-no}"
OPT_TYPESENSE="${AURACP_TYPESENSE:-no}"
OPT_DOCKER="${AURACP_DOCKER:-no}"
OPT_SECURITY="${AURACP_SECURITY:-yes}"  # UFW firewall + fail2ban
TYPESENSE_VERSION="${AURACP_TYPESENSE_VERSION:-27.1}"

# paths
PREFIX="/opt/auracp"
DATA_DIR="/var/lib/auracp"
ETC_DIR="/etc/auracp"
CADDY_ARCH=""

# ──────────────────────────────────────────────────────────────────────────
# ui helpers
# ──────────────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
  C_RESET=$'\e[0m'; C_DIM=$'\e[2m'; C_B=$'\e[1m'
  C_GRN=$'\e[32m'; C_YEL=$'\e[33m'; C_RED=$'\e[31m'; C_CYN=$'\e[36m'
else
  C_RESET=""; C_DIM=""; C_B=""; C_GRN=""; C_YEL=""; C_RED=""; C_CYN=""
fi
msg()  { printf '%s\n' "${C_CYN}::${C_RESET} $*"; }
ok()   { printf '%s\n' "${C_GRN}✓${C_RESET} $*"; }
warn() { printf '%s\n' "${C_YEL}!${C_RESET} $*" >&2; }
die()  { printf '%s\n' "${C_RED}✗ $*${C_RESET}" >&2; exit 1; }

# run: execute a privileged command, or just print it in --dry-run mode.
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} $*"
  else
    eval "$@"
  fi
}

yesno() { # case-insensitive truthiness, portable to bash 3.2+
  case "$1" in
    y|Y|yes|YES|Yes|1|on|ON|On|true|TRUE|True) return 0 ;;
    *) return 1 ;;
  esac
}

# ──────────────────────────────────────────────────────────────────────────
# preflight
# ──────────────────────────────────────────────────────────────────────────
preflight() {
  [ "$DRY_RUN" -eq 1 ] || [ "$(id -u)" -eq 0 ] || die "Run as root (sudo)."

  local os_id="" os_ver="" codename=""
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    os_id="${ID:-}"; os_ver="${VERSION_ID:-}"; codename="${VERSION_CODENAME:-}"
  fi
  case "$os_id" in
    debian)
      case "$os_ver" in
        12|13) ok "Debian ${os_ver} (${codename}) supported." ;;
        *) warn "Debian ${os_ver:-?} is untested; 13 (trixie) recommended. Continuing." ;;
      esac ;;
    ubuntu)
      case "$os_ver" in
        22.04|24.04) ok "Ubuntu ${os_ver} (${codename}) supported." ;;
        *) warn "Ubuntu ${os_ver:-?} is untested; 24.04 LTS recommended. Continuing." ;;
      esac ;;
    "")
      warn "Could not detect OS (not Linux?). Preflight limited — use --dry-run off-server." ;;
    *)
      warn "Unsupported distro '${os_id}'. Debian or Ubuntu required." ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64) CADDY_ARCH="amd64" ;;
    aarch64|arm64) CADDY_ARCH="arm64" ;;
    *) [ "$DRY_RUN" -eq 1 ] || die "Unsupported architecture: $(uname -m)" ; CADDY_ARCH="amd64" ;;
  esac

  if [ "$DRY_RUN" -eq 0 ] && command -v ss >/dev/null 2>&1; then
    for p in 80 443 "$PANEL_PORT"; do
      if ss -ltn "( sport = :$p )" 2>/dev/null | grep -q LISTEN; then
        die "Port $p is already in use. Free it before installing."
      fi
    done
  fi
  ok "Preflight passed (arch: ${CADDY_ARCH}, panel port: ${PANEL_PORT})."
}

# ──────────────────────────────────────────────────────────────────────────
# arg parsing
# ──────────────────────────────────────────────────────────────────────────
parse_args() {
  local sawSelection=0
  for arg in "$@"; do
    case "$arg" in
      --yes|-y) ASSUME_YES=1 ;;
      --dry-run) DRY_RUN=1 ;;
      --non-interactive) INTERACTIVE=0 ;;
      --port=*) PANEL_PORT="${arg#*=}" ;;
      --panel-domain=*) PANEL_DOMAIN="${arg#*=}" ;;
      --db=*)
        sawSelection=1
        case "${arg#*=}" in
          mariadb)  OPT_MARIADB=yes; OPT_POSTGRES=no ;;
          postgres) OPT_MARIADB=no;  OPT_POSTGRES=yes ;;
          both)     OPT_MARIADB=yes; OPT_POSTGRES=yes ;;
          none)     OPT_MARIADB=no;  OPT_POSTGRES=no ;;
          *) die "--db must be mariadb|postgres|both|none" ;;
        esac ;;
      --node=*)        sawSelection=1; OPT_NODE="${arg#*=}" ;;
      --php=*)         sawSelection=1; OPT_PHP="${arg#*=}" ;;
      --php-version=*) PHP_VERSION="${arg#*=}" ;;
      --python=*)      sawSelection=1; OPT_PYTHON="${arg#*=}" ;;
      --redis=*)       sawSelection=1; OPT_REDIS="${arg#*=}" ;;
      --typesense=*)   sawSelection=1; OPT_TYPESENSE="${arg#*=}" ;;
      --docker=*)      sawSelection=1; OPT_DOCKER="${arg#*=}" ;;
      --security=*)    sawSelection=1; OPT_SECURITY="${arg#*=}" ;;
      -h|--help) grep '^#' "$0" | sed 's/^#\s\?//'; exit 0 ;;
      *) die "Unknown option: $arg (try --help)" ;;
    esac
  done
  case "$PHP_VERSION" in 8.3|8.4|8.5) ;; *) die "--php-version must be 8.3, 8.4 or 8.5 (8.3+ only)";; esac
  [ "$sawSelection" -eq 1 ] && INTERACTIVE=0
  return 0
}

# ──────────────────────────────────────────────────────────────────────────
# interactive selection
# ──────────────────────────────────────────────────────────────────────────
select_components() {
  # Interactive unless a selection flag forced preset mode, or there's no
  # terminal at all. Use /dev/tty (not stdin) so `curl … | bash` still prompts.
  if [ "$INTERACTIVE" -eq 0 ] || [ ! -r /dev/tty ]; then
    msg "Using preset selection (non-interactive)."
    return
  fi
  if command -v whiptail >/dev/null 2>&1; then
    select_whiptail
  else
    select_readline
  fi
  if [ -z "$PANEL_DOMAIN" ]; then
    read -r -p "  Panel domain (optional; blank = access via IP:${PANEL_PORT}): " PANEL_DOMAIN < /dev/tty || true
  fi
}

select_whiptail() {
  local chosen
  chosen=$(whiptail --title "auraCP — optional components" \
    --checklist "Space to toggle, Enter to confirm.\nRequired (auracpd, Caddy) are always installed." \
    20 74 7 \
    MARIADB "MariaDB database engine"          "$(onoff "$OPT_MARIADB")" \
    POSTGRES "PostgreSQL database engine"        "$(onoff "$OPT_POSTGRES")" \
    NODE    "Node.js ${NODE_MAJOR} LTS runtime" "$(onoff "$OPT_NODE")" \
    PHP     "PHP ${PHP_VERSION} (FrankenPHP)"    "$(onoff "$OPT_PHP")" \
    PYTHON  "Python 3 (gunicorn/uvicorn)"        "$(onoff "$OPT_PYTHON")" \
    REDIS   "Redis (object cache)"               "$(onoff "$OPT_REDIS")" \
    TYPESENSE "Typesense search server"           "$(onoff "$OPT_TYPESENSE")" \
    DOCKER  "Docker engine"                       "$(onoff "$OPT_DOCKER")" \
    SECURITY "UFW firewall + fail2ban"           "$(onoff "$OPT_SECURITY")" \
    3>&1 1>&2 2>&3 < /dev/tty) || die "Installation cancelled."

  OPT_MARIADB=no OPT_POSTGRES=no OPT_NODE=no OPT_PHP=no OPT_PYTHON=no OPT_REDIS=no OPT_TYPESENSE=no OPT_DOCKER=no OPT_SECURITY=no
  case "$chosen" in *MARIADB*) OPT_MARIADB=yes;; esac
  case "$chosen" in *POSTGRES*) OPT_POSTGRES=yes;; esac
  case "$chosen" in *NODE*) OPT_NODE=yes;; esac
  case "$chosen" in *PHP*) OPT_PHP=yes;; esac
  case "$chosen" in *PYTHON*) OPT_PYTHON=yes;; esac
  case "$chosen" in *REDIS*) OPT_REDIS=yes;; esac
  case "$chosen" in *TYPESENSE*) OPT_TYPESENSE=yes;; esac
  case "$chosen" in *DOCKER*) OPT_DOCKER=yes;; esac
  case "$chosen" in *SECURITY*) OPT_SECURITY=yes;; esac

  if yesno "$OPT_PHP"; then
    PHP_VERSION=$(whiptail --title "PHP version" --radiolist "auraCP supports PHP 8.3+ only." 12 60 3 \
      8.5 "PHP 8.5" "$(req "$PHP_VERSION" 8.5)" \
      8.4 "PHP 8.4" "$(req "$PHP_VERSION" 8.4)" \
      8.3 "PHP 8.3" "$(req "$PHP_VERSION" 8.3)" \
      3>&1 1>&2 2>&3 < /dev/tty) || PHP_VERSION=8.4
  fi
}

# plain-text fallback when whiptail isn't available
select_readline() {
  msg "Select optional components (required ones install automatically):"
  OPT_MARIADB=$(ask "Install MariaDB?"   "$OPT_MARIADB")
  OPT_POSTGRES=$(ask "Install PostgreSQL?" "$OPT_POSTGRES")
  OPT_NODE=$(ask "Install Node.js ${NODE_MAJOR} LTS?" "$OPT_NODE")
  OPT_PHP=$(ask "Install PHP (FrankenPHP)?" "$OPT_PHP")
  if yesno "$OPT_PHP"; then
    read -r -p "  PHP version [8.3/8.4/8.5] (${PHP_VERSION}): " v < /dev/tty || true
    case "${v:-$PHP_VERSION}" in 8.3|8.4|8.5) PHP_VERSION="${v:-$PHP_VERSION}";; esac
  fi
  OPT_PYTHON=$(ask "Install Python 3?" "$OPT_PYTHON")
  OPT_REDIS=$(ask "Install Redis?" "$OPT_REDIS")
  OPT_TYPESENSE=$(ask "Install Typesense search server?" "$OPT_TYPESENSE")
  OPT_DOCKER=$(ask "Install Docker engine?" "$OPT_DOCKER")
  OPT_SECURITY=$(ask "Enable security hardening (UFW + fail2ban)?" "$OPT_SECURITY")
}

ask() { # prompt default → echoes yes/no
  local def_label="y/N"; yesno "$2" && def_label="Y/n"
  local a; read -r -p "  $1 [$def_label]: " a < /dev/tty || true
  if [ -z "$a" ]; then echo "$2"; else yesno "$a" && echo yes || echo no; fi
}
onoff() { yesno "$1" && echo ON || echo OFF; }
req()   { [ "$1" = "$2" ] && echo ON || echo OFF; }

# ──────────────────────────────────────────────────────────────────────────
# plan summary
# ──────────────────────────────────────────────────────────────────────────
print_plan() {
  local m
  echo
  printf '%s\n' "${C_B}auraCP ${AURACP_VERSION} — installation plan${C_RESET}"
  printf '%s\n' "${C_DIM}────────────────────────────────────────────${C_RESET}"
  printf '  %-22s %s\n' "auracpd + CLI" "${C_GRN}required${C_RESET}"
  printf '  %-22s %s\n' "Caddy (HTTP/3, SSL)" "${C_GRN}required${C_RESET}"
  printf '  %-22s %s\n' "MariaDB" "$(mark "$OPT_MARIADB")"
  printf '  %-22s %s\n' "PostgreSQL" "$(mark "$OPT_POSTGRES")"
  printf '  %-22s %s\n' "Node.js ${NODE_MAJOR} LTS" "$(mark "$OPT_NODE")"
  m="$(mark "$OPT_PHP")"; yesno "$OPT_PHP" && m="$m ${C_DIM}(${PHP_VERSION})${C_RESET}"
  printf '  %-22s %s\n' "PHP / FrankenPHP" "$m"
  printf '  %-22s %s\n' "Python 3" "$(mark "$OPT_PYTHON")"
  printf '  %-22s %s\n' "Redis" "$(mark "$OPT_REDIS")"
  printf '  %-22s %s\n' "Typesense" "$(mark "$OPT_TYPESENSE")"
  printf '  %-22s %s\n' "Docker" "$(mark "$OPT_DOCKER")"
  printf '  %-22s %s\n' "UFW + fail2ban" "$(mark "$OPT_SECURITY")"
  printf '  %-22s %s\n' "Panel domain" "${PANEL_DOMAIN:-<none — IP access>}"
  printf '%s\n' "${C_DIM}────────────────────────────────────────────${C_RESET}"
  if [ -n "$PANEL_DOMAIN" ]; then
    printf '  panel: %s\n\n' "https://${PANEL_DOMAIN}"
  else
    printf '  panel: %s\n\n' "https://<server-ip>:${PANEL_PORT}"
  fi
}
mark() { yesno "$1" && echo "${C_GRN}install${C_RESET}" || echo "${C_DIM}skip${C_RESET}"; }

confirm() {
  [ "$DRY_RUN" -eq 1 ] && return 0
  [ "$ASSUME_YES" -eq 1 ] && return 0
  [ -t 0 ] || return 0
  local a; read -r -p "Proceed with this plan? [Y/n]: " a < /dev/tty || true
  case "${a:-y}" in y|Y|"") ;; *) die "Aborted." ;; esac
}

# ──────────────────────────────────────────────────────────────────────────
# install steps
# ──────────────────────────────────────────────────────────────────────────
apt_refresh() {
  run "export DEBIAN_FRONTEND=noninteractive"
  run "apt-get update -y"
}

install_base() { # required
  msg "Installing base packages…"
  run "apt-get install -y --no-install-recommends ca-certificates curl gnupg cron logrotate rsync unzip"
  ok "Base packages ready."
}

install_caddy() { # required — custom build with Cloudflare DNS + Souin cache
  msg "Installing Caddy (Cloudflare DNS + Souin cache)…"
  # No-build custom binary from the Caddy download API (keeps Go off the server).
  local url="https://caddyserver.com/api/download?os=linux&arch=${CADDY_ARCH}"
  url+="&p=github.com/caddy-dns/cloudflare&p=github.com/darkweak/souin/plugins/caddy"
  run "curl -fsSL '${url}' -o /usr/bin/caddy"
  run "chmod +x /usr/bin/caddy"
  run "id caddy >/dev/null 2>&1 || useradd --system --home /var/lib/caddy --shell /usr/sbin/nologin caddy"
  run "mkdir -p /etc/caddy/sites /var/lib/caddy"
  run "[ -f /etc/caddy/Caddyfile ] || printf 'import sites/*\\n' > /etc/caddy/Caddyfile"
  install_unit caddy "Caddy web server" "/usr/bin/caddy run --config /etc/caddy/Caddyfile" caddy
  ok "Caddy ready."
}

install_mariadb() {
  msg "Installing MariaDB…"
  run "apt-get install -y mariadb-server"
  run "systemctl enable --now mariadb"
  ok "MariaDB ready (engine available for per-database selection)."
}

install_postgres() {
  msg "Installing PostgreSQL…"
  run "apt-get install -y postgresql"
  run "systemctl enable --now postgresql"
  ok "PostgreSQL ready (engine available for per-database selection)."
}

install_node() {
  msg "Installing Node.js ${NODE_MAJOR} LTS (system-wide)…"
  run "curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x -o /tmp/nodesource_setup.sh"
  run "bash /tmp/nodesource_setup.sh"
  run "apt-get install -y nodejs"
  run "rm -f /tmp/nodesource_setup.sh"
  ok "Node.js ${NODE_MAJOR} ready (default runtime for every site)."
}

install_php() {
  msg "Installing FrankenPHP (PHP ${PHP_VERSION})…"
  # FrankenPHP ships PHP embedded; static binary keeps the footprint small.
  run "curl -fsSL https://frankenphp.dev/install.sh | sh"
  run "mv -f frankenphp /usr/bin/frankenphp 2>/dev/null || true"
  ok "FrankenPHP (PHP ${PHP_VERSION}) ready."
}

install_python() {
  msg "Installing Python 3…"
  run "apt-get install -y python3 python3-venv python3-pip"
  ok "Python 3 ready (per-site venvs created on demand)."
}

install_redis() {
  msg "Installing Redis…"
  run "apt-get install -y redis-server"
  run "systemctl enable --now redis-server"
  ok "Redis ready."
}

install_typesense() {
  msg "Installing Typesense ${TYPESENSE_VERSION} (search server)…"
  local deb="/tmp/typesense-server.deb"
  run "curl -fsSL 'https://dl.typesense.org/releases/${TYPESENSE_VERSION}/typesense-server-${TYPESENSE_VERSION}-${CADDY_ARCH}.deb' -o ${deb}"
  run "apt-get install -y ${deb}"
  run "rm -f ${deb}"
  run "systemctl enable --now typesense-server 2>/dev/null || true"
  ok "Typesense ready."
}

install_docker() {
  msg "Installing Docker engine…"
  # Official convenience script — supports Debian + Ubuntu on amd64 + arm64.
  run "curl -fsSL https://get.docker.com -o /tmp/get-docker.sh"
  run "sh /tmp/get-docker.sh"
  run "rm -f /tmp/get-docker.sh"
  run "systemctl enable --now docker"
  ok "Docker ready."
}

install_security() {
  msg "Installing security hardening (UFW + fail2ban)…"
  run "apt-get install -y ufw fail2ban"
  run "ufw allow 22/tcp"
  run "ufw allow 80,443/tcp"
  run "ufw allow ${PANEL_PORT}/tcp"
  run "ufw --force enable"
  run "systemctl enable --now fail2ban"
  ok "Firewall + fail2ban active."
}

install_auracpd() { # required — the control plane
  msg "Installing auracpd…"
  local repo deb=""
  repo="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd)"
  # Find a prebuilt .deb without `ls` (which exits non-zero on no-match under set -e).
  for f in "$repo"/dist/auracp_*_"${CADDY_ARCH}".deb; do
    [ -f "$f" ] && { deb="$f"; break; }
  done

  # Preferred: install the prebuilt .deb (handles binary + systemd unit + enable).
  if [ -n "$deb" ]; then
    run "apt-get install -y '$deb'"
    ok "auracpd installed from $(basename "$deb")."
    return
  fi

  run "mkdir -p ${PREFIX}/bin ${DATA_DIR} ${ETC_DIR}"
  if [ -f "$repo/bin/auracpd" ]; then
    run "install -m 0755 '$repo/bin/auracpd' ${PREFIX}/bin/auracpd"
    run "install -m 0755 '$repo/bin/auracp'  ${PREFIX}/bin/auracp 2>/dev/null || true"
  else
    warn "no .deb or local binary found — downloading the latest release."
    run "curl -fsSL https://github.com/auracp/auracp/releases/latest/download/auracpd-linux-${CADDY_ARCH} -o ${PREFIX}/bin/auracpd"
    run "chmod +x ${PREFIX}/bin/auracpd"
  fi
  run "ln -sf ${PREFIX}/bin/auracp /usr/local/bin/auracp"
  install_unit auracpd "auraCP control panel" \
    "${PREFIX}/bin/auracpd -addr :${PANEL_PORT} -db ${DATA_DIR}/auracp.db -etc ${ETC_DIR}" root
  ok "auracpd installed and started on :${PANEL_PORT}."
}

# install_unit NAME DESC EXECSTART USER
install_unit() {
  local name="$1" desc="$2" exec="$3" user="$4"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} write /etc/systemd/system/${name}.service (User=${user})"
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} systemctl enable --now ${name}"
    return
  fi
  cat > "/etc/systemd/system/${name}.service" <<EOF
[Unit]
Description=${desc}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${user}
ExecStart=${exec}
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now "${name}"
}

# setup_panel_domain runs LAST — after Caddy and everything else is ready — so
# Caddy can immediately obtain the Let's Encrypt certificate for the panel domain.
setup_panel_domain() {
  [ -n "$PANEL_DOMAIN" ] || return 0
  msg "Configuring panel domain ${PANEL_DOMAIN} (Caddy will issue its Let's Encrypt cert)…"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} systemd drop-in adds -panel-domain=${PANEL_DOMAIN}; restart auracpd"
    return
  fi
  mkdir -p /etc/systemd/system/auracpd.service.d
  cat > /etc/systemd/system/auracpd.service.d/panel-domain.conf <<EOF
[Service]
ExecStart=
ExecStart=${PREFIX}/bin/auracpd -addr :${PANEL_PORT} -db ${DATA_DIR}/auracp.db -etc ${ETC_DIR} -panel-domain ${PANEL_DOMAIN}
EOF
  systemctl daemon-reload
  systemctl restart auracpd
  ok "Panel domain set. Point a DNS A record for ${PANEL_DOMAIN} at this server; Caddy issues the cert automatically."
}

finalize() {
  echo
  ok "auraCP installation complete."
  echo
  if [ -n "$PANEL_DOMAIN" ]; then
    printf '%s\n' "  Open ${C_B}https://${PANEL_DOMAIN}${C_RESET} and create your admin account (first-run setup)."
    printf '%s\n' "  ${C_DIM}Caddy issues a real Let's Encrypt cert once ${PANEL_DOMAIN} resolves to this server.${C_RESET}"
  else
    printf '%s\n' "  Open ${C_B}https://<server-ip>:${PANEL_PORT}${C_RESET} and create your admin account (first-run setup)."
    printf '%s\n' "  ${C_DIM}Self-signed cert — accept the browser warning, or set a Panel Domain in Settings.${C_RESET}"
  fi
  echo
}

# ──────────────────────────────────────────────────────────────────────────
# main
# ──────────────────────────────────────────────────────────────────────────
main() {
  parse_args "$@"
  printf '%s\n' "${C_B}auraCP ${AURACP_VERSION} installer${C_RESET}"
  preflight
  select_components
  print_plan
  confirm

  apt_refresh
  install_base
  install_caddy
  yesno "$OPT_MARIADB"  && install_mariadb
  yesno "$OPT_POSTGRES" && install_postgres
  yesno "$OPT_NODE"     && install_node
  yesno "$OPT_PHP"      && install_php
  yesno "$OPT_PYTHON"   && install_python
  yesno "$OPT_REDIS"    && install_redis
  yesno "$OPT_TYPESENSE" && install_typesense
  yesno "$OPT_DOCKER"   && install_docker
  yesno "$OPT_SECURITY" && install_security
  install_auracpd
  setup_panel_domain
  finalize
}

main "$@"
