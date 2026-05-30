#!/usr/bin/env bash
#
# auraCP installer — lightweight server control panel.
# Supported: Debian 12 (bookworm), Debian 13 (trixie),
#            Ubuntu 22.04 (jammy), Ubuntu 24.04 (noble) — on amd64 + arm64.
#
# v0.2.0 stack (replaces v0.1.x Caddy + FrankenPHP):
#   - nginx (1.30 mainline, with cache_purge module)
#   - PHP-FPM (multiple versions side-by-side from deb.sury.org)
#   - go-acme/lego in auracpd (in-process ACME — no certbot)
#   - Node.js from nodejs.org tarballs, optional pm2-runtime in systemd
#   - MariaDB / PostgreSQL / MongoDB / Redis / Typesense / Docker / UFW + fail2ban — optional
#
# Usage:
#   sudo ./install.sh                 # interactive
#   sudo ./install.sh --yes           # non-interactive, use defaults/flags
#   sudo ./install.sh --dry-run       # show the plan, change nothing
#
# Selection flags (override defaults; imply --non-interactive when given):
#   --db=mariadb|postgres|both|none   --node=yes|no      --php=yes|no
#   --php-version=8.3|8.4|8.5         --python=yes|no    --redis=yes|no
#   --mariadb-version=10.11|11.4|11.8 --postgres-version=16|17|18
#   --node-version=20|22|24           --mongodb=yes|no   --mongodb-version=8.0|7.0
#   --typesense=yes|no                --docker=yes|no
#   --security=yes|no                 --port=8443
#   --panel-domain=panel.example.com  (front the panel via nginx; auracpd issues its LE cert)
#
# Or via env: AURACP_MARIADB, AURACP_POSTGRES, AURACP_NODE, AURACP_PHP,
#   AURACP_PHP_VERSION, AURACP_PYTHON, AURACP_REDIS, AURACP_SECURITY, AURACP_PORT
#
set -euo pipefail

# ──────────────────────────────────────────────────────────────────────────
# config & defaults
# ──────────────────────────────────────────────────────────────────────────
AURACP_VERSION="0.3.40"
PANEL_PORT="${AURACP_PORT:-8443}"
PANEL_DOMAIN="${AURACP_PANEL_DOMAIN:-}"   # optional: front the panel at this domain
NODE_MAJOR="24"                         # Node 24 LTS baseline

ASSUME_YES=0
DRY_RUN=0
INTERACTIVE=1                           # auto-disabled when selection flags are passed

# optional components (yes/no); defaults chosen for a typical PHP+DB host
OPT_PHP="${AURACP_PHP:-yes}"
# Space-separated list — install one or many PHP-FPM versions side-by-side from
# deb.sury.org. Sites pin to whichever they need via the Create form. Adding
# extra versions later is also possible from Settings → PHP Versions.
# Default selects ALL three supported versions so operators don't have to come
# back to the installer to add another one mid-week; pruning unused versions
# is one click in the panel UI.
PHP_VERSIONS="${AURACP_PHP_VERSIONS:-${AURACP_PHP_VERSION:-8.3 8.4 8.5}}"
PHP_DEFAULT="${AURACP_PHP_DEFAULT:-8.4}"   # 8.4 is the panel default for new PHP sites unless overridden
OPT_NODE="${AURACP_NODE:-yes}"
OPT_PYTHON="${AURACP_PYTHON:-no}"
OPT_MARIADB="${AURACP_MARIADB:-yes}"
OPT_POSTGRES="${AURACP_POSTGRES:-no}"
OPT_REDIS="${AURACP_REDIS:-no}"
OPT_MONGODB="${AURACP_MONGODB:-no}"
OPT_TYPESENSE="${AURACP_TYPESENSE:-no}"
OPT_DOCKER="${AURACP_DOCKER:-no}"
OPT_SECURITY="${AURACP_SECURITY:-yes}"  # UFW firewall + fail2ban
TYPESENSE_VERSION="${AURACP_TYPESENSE_VERSION:-30.2}"

# Per-engine version defaults — overridable via flags / env / TUI.
MARIADB_VERSION="${AURACP_MARIADB_VERSION:-11.8}"     # 11.8 | 11.4 | 10.11  (LTS)
POSTGRES_VERSION="${AURACP_POSTGRES_VERSION:-18}"     # 18 | 17 | 16
MONGODB_VERSION="${AURACP_MONGODB_VERSION:-8.0}"      # 8.0 | 7.0

# paths + detected OS (filled in by preflight)
PREFIX="/opt/auracp"
DATA_DIR="/var/lib/auracp"
ETC_DIR="/etc/auracp"
ARCH=""
OS_ID=""           # debian | ubuntu
OS_CODENAME=""     # trixie | bookworm | noble | jammy …

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
  OS_ID="$os_id"; OS_CODENAME="$codename"
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
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) [ "$DRY_RUN" -eq 1 ] || die "Unsupported architecture: $(uname -m)" ; ARCH="amd64" ;;
  esac

  if [ "$DRY_RUN" -eq 0 ] && command -v ss >/dev/null 2>&1; then
    check_port_or_own 80    nginx
    check_port_or_own 443   nginx
    check_port_or_own "$PANEL_PORT" auracpd
  fi
  ok "Preflight passed (arch: ${ARCH}, panel port: ${PANEL_PORT})."
}

# check_port_or_own PORT EXPECTED_PROCESS — dies if PORT is held by anything
# other than the named auraCP service. Allows re-running on a host where the
# panel/nginx is already up (e.g. after `dpkg -i auracp.deb`).
check_port_or_own() {
  local port="$1" expected="$2"
  ss -ltn "( sport = :$port )" 2>/dev/null | grep -q LISTEN || return 0
  local who
  who=$(ss -ltnpH "( sport = :$port )" 2>/dev/null | sed -n 's/.*users:((\"\([^\"]*\)\".*/\1/p' | head -1)
  if [ -z "$who" ] || [ "$who" = "$expected" ]; then
    ok "Port $port already held by ${who:-${expected}} — leaving it alone."
    return 0
  fi
  die "Port $port is in use by '${who}' (expected ${expected} or free). Free it before installing."
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
      # Accept one version or a comma-separated list: --php-version=8.3,8.4,8.5
      --php-version=*)  PHP_VERSIONS="$(echo "${arg#*=}" | tr ',' ' ')" ;;
      --php-default=*)  PHP_DEFAULT="${arg#*=}" ;;
      --mariadb-version=*)  MARIADB_VERSION="${arg#*=}" ;;
      --postgres-version=*) POSTGRES_VERSION="${arg#*=}" ;;
      --node-version=*)     NODE_MAJOR="${arg#*=}" ;;
      --python=*)      sawSelection=1; OPT_PYTHON="${arg#*=}" ;;
      --redis=*)       sawSelection=1; OPT_REDIS="${arg#*=}" ;;
      --mongodb=*)     sawSelection=1; OPT_MONGODB="${arg#*=}" ;;
      --mongodb-version=*) MONGODB_VERSION="${arg#*=}" ;;
      --typesense=*)   sawSelection=1; OPT_TYPESENSE="${arg#*=}" ;;
      --docker=*)      sawSelection=1; OPT_DOCKER="${arg#*=}" ;;
      --security=*)    sawSelection=1; OPT_SECURITY="${arg#*=}" ;;
      -h|--help) grep '^#' "$0" | sed 's/^#\s\?//'; exit 0 ;;
      *) die "Unknown option: $arg (try --help)" ;;
    esac
  done
  # Validate every entry in PHP_VERSIONS (the list may contain one or many).
  for _v in $PHP_VERSIONS; do
    case "$_v" in 8.3|8.4|8.5) ;; *) die "--php-version must list values from 8.3 / 8.4 / 8.5 (got $_v)";; esac
  done
  case "$MARIADB_VERSION" in 10.11|11.4|11.8) ;; *) die "--mariadb-version must be 10.11 / 11.4 / 11.8";; esac
  case "$POSTGRES_VERSION" in 16|17|18) ;;       *) die "--postgres-version must be 16 / 17 / 18";; esac
  case "$NODE_MAJOR"      in 20|22|24) ;;        *) die "--node-version must be 20 / 22 / 24";; esac
  case "$MONGODB_VERSION" in 7.0|8.0) ;;        *) die "--mongodb-version must be 7.0 / 8.0";; esac
  [ "$sawSelection" -eq 1 ] && INTERACTIVE=0
  return 0
}

# mongodb_supported — returns 0 (true) only on officially supported combinations.
# Source: https://www.mongodb.com/docs/v8.0/administration/production-notes/#std-label-prod-notes-supported-platforms
#   Debian  12 (bookworm) — amd64 only
#   Ubuntu 22.04 (jammy)  — amd64 + arm64
#   Ubuntu 24.04 (noble)  — amd64 + arm64
# NOT supported: Debian 13 (trixie) on any arch; Debian on arm64 (any version).
mongodb_supported() {
  case "${OS_ID:-}:${OS_CODENAME:-}:${ARCH:-}" in
    debian:bookworm:amd64) return 0 ;;
    ubuntu:jammy:amd64|ubuntu:jammy:arm64) return 0 ;;
    ubuntu:noble:amd64|ubuntu:noble:arm64) return 0 ;;
    *) return 1 ;;
  esac
}

# ──────────────────────────────────────────────────────────────────────────
# upstream-repo probes
# ──────────────────────────────────────────────────────────────────────────
# mariadb.org and apt.postgresql.org publish per-OS-codename repos — older
# engine versions stop being built once an OS is released, so a hardcoded
# version list goes 404 on newer hosts (e.g. MariaDB 10.11 on Debian 13).
# These helpers probe what's actually published and cache the answer.

_MARIADB_PROBED=0; _MARIADB_AVAILABLE=""
mariadb_available() {
  if [ "$_MARIADB_PROBED" -eq 0 ]; then
    local v distro="${OS_ID:-debian}"
    for v in 11.8 11.4 10.11; do
      if curl -fsI -o /dev/null --max-time 5 \
          "https://mirror.mariadb.org/repo/${v}/${distro}/dists/${OS_CODENAME}/InRelease" 2>/dev/null; then
        _MARIADB_AVAILABLE="$_MARIADB_AVAILABLE $v"
      fi
    done
    _MARIADB_AVAILABLE="${_MARIADB_AVAILABLE# }"
    _MARIADB_PROBED=1
  fi
  echo "$_MARIADB_AVAILABLE"
}

_POSTGRES_PROBED=0; _POSTGRES_AVAILABLE=""
postgres_available() {
  if [ "$_POSTGRES_PROBED" -eq 0 ]; then
    if curl -fsI -o /dev/null --max-time 5 \
        "https://apt.postgresql.org/pub/repos/apt/dists/${OS_CODENAME}-pgdg/InRelease" 2>/dev/null; then
      _POSTGRES_AVAILABLE="18 17 16"
    fi
    _POSTGRES_PROBED=1
  fi
  echo "$_POSTGRES_AVAILABLE"
}

# PHP — deb.sury.org publishes one repo per codename. We probe it once.
_PHP_PROBED=0; _PHP_AVAILABLE=""
php_available() {
  if [ "$_PHP_PROBED" -eq 0 ]; then
    local base="https://packages.sury.org"
    local repo="php"; [ "$OS_ID" = "ubuntu" ] && repo="php"
    if curl -fsI -o /dev/null --max-time 5 "${base}/${repo}/dists/${OS_CODENAME}/InRelease" 2>/dev/null; then
      _PHP_AVAILABLE="8.5 8.4 8.3"
    fi
    _PHP_PROBED=1
  fi
  echo "$_PHP_AVAILABLE"
}

snap_to_available() {
  local want="$1" list="$2"
  case " $list " in
    *" $want "*) echo "$want" ;;
    *)           echo "${list%% *}" ;;
  esac
}

# ──────────────────────────────────────────────────────────────────────────
# interactive selection
# ──────────────────────────────────────────────────────────────────────────
select_components() {
  if [ "$INTERACTIVE" -eq 0 ] || [ ! -r /dev/tty ]; then
    msg "Using preset selection (non-interactive)."
    return
  fi
  if command -v whiptail >/dev/null 2>&1; then
    select_whiptail
  else
    select_readline
  fi
}

prompt_panel_domain() {
  [ -n "$PANEL_DOMAIN" ] && return 0
  [ "$ASSUME_YES" -eq 1 ] && return 0
  [ -r /dev/tty ] || return 0
  printf '\n%s\n' "${C_B}Panel domain (optional)${C_RESET}"
  printf '%s\n' "  Setting a domain lets auracpd issue a real Let's Encrypt cert for the panel."
  printf '%s\n' "  Point its DNS A record at this server, or leave blank to use IP:${PANEL_PORT}."
  read -r -p "  Panel domain: " PANEL_DOMAIN < /dev/tty || true
}

# v0.2.24: TUI is now a state machine — each whiptail dialog returns 0 (Next)
# or 1 (Back / Cancel). The outer loop walks an ordered step list, skipping
# steps whose gate is off (e.g. the PHP-versions step is skipped if PHP is
# unchecked on the main screen). Back from the first step exits the installer;
# Back from any other step returns to the previous *enabled* step with all
# prior selections preserved (the values live in shell vars across calls).
#
# Each step's --cancel-button is relabeled "← Back" on non-first steps so
# the user knows what Cancel does at that point. The first step keeps the
# default "Cancel" label, since there's nothing to go back to.

# Cached availability probes — checked once at TUI start, not on every step.
_PHP_AVAIL=""; _MARIADB_AVAIL=""; _POSTGRES_AVAIL=""
prime_avail_cache() {
  _PHP_AVAIL=$(php_available)
  _MARIADB_AVAIL=$(mariadb_available)
  _POSTGRES_AVAIL=$(postgres_available)
}

# step_enabled <step> → exits 0 if the step should be shown.
step_enabled() {
  case "$1" in
    components|domain) return 0 ;;
    php)      yesno "$OPT_PHP"      && [ -n "$_PHP_AVAIL" ] ;;
    mariadb)  yesno "$OPT_MARIADB"  && [ -n "$_MARIADB_AVAIL" ] ;;
    postgres) yesno "$OPT_POSTGRES" && [ -n "$_POSTGRES_AVAIL" ] ;;
    node)     yesno "$OPT_NODE" ;;
    *) return 1 ;;
  esac
}
# Ordered list of all possible steps. next_step / prev_step walk this skipping
# disabled ones.
_STEPS_ALL="components php mariadb postgres node domain"
next_step() {
  local found=0 s
  for s in $_STEPS_ALL; do
    if [ "$found" -eq 1 ] && step_enabled "$s"; then echo "$s"; return; fi
    [ "$s" = "$1" ] && found=1
  done
  echo ""   # past the end → done
}
prev_step() {
  local prev="" s
  for s in $_STEPS_ALL; do
    [ "$s" = "$1" ] && { echo "$prev"; return; }
    step_enabled "$s" && prev="$s"
  done
  echo ""
}

# ─── individual step dialogs ────────────────────────────────────────────────
# Each function returns 0 (Next), or 1 (Back / Cancel). The component step
# uses "Cancel" as its second-button label (since it's the first step);
# every later step uses "← Back".

tui_components() {
  # Build checklist args dynamically so OS-unsupported options are hidden.
  local args=()
  args+=(MARIADB   "MariaDB database engine"                    "$(onoff "$OPT_MARIADB")")
  args+=(POSTGRES  "PostgreSQL database engine"                 "$(onoff "$OPT_POSTGRES")")
  # MongoDB only shown when the detected OS+arch is officially supported.
  mongodb_supported && \
    args+=(MONGODB "MongoDB ${MONGODB_VERSION} document database" "$(onoff "$OPT_MONGODB")")
  args+=(NODE      "Node.js ${NODE_MAJOR} LTS runtime"          "$(onoff "$OPT_NODE")")
  args+=(PHP       "PHP-FPM (deb.sury.org; pick versions next)" "$(onoff "$OPT_PHP")")
  args+=(PYTHON    "Python 3 (gunicorn/uvicorn)"                "$(onoff "$OPT_PYTHON")")
  args+=(REDIS     "Redis (object cache)"                       "$(onoff "$OPT_REDIS")")
  args+=(TYPESENSE "Typesense search server"                    "$(onoff "$OPT_TYPESENSE")")
  args+=(DOCKER    "Docker engine"                              "$(onoff "$OPT_DOCKER")")
  args+=(SECURITY  "UFW firewall + fail2ban"                    "$(onoff "$OPT_SECURITY")")
  local n_items=$(( ${#args[@]} / 3 ))
  local win_h=$(( n_items + 9 ))   # checklist window height scales with item count

  local chosen
  chosen=$(whiptail --title "auraCP — optional components" \
    --checklist "Space to toggle, Enter to confirm.\nRequired (auracpd, nginx) are always installed." \
    "$win_h" 74 "$n_items" \
    "${args[@]}" \
    3>&1 1>&2 2>&3 < /dev/tty) || return 1

  OPT_MARIADB=no OPT_POSTGRES=no OPT_MONGODB=no OPT_NODE=no OPT_PHP=no OPT_PYTHON=no OPT_REDIS=no OPT_TYPESENSE=no OPT_DOCKER=no OPT_SECURITY=no
  case "$chosen" in *MARIADB*) OPT_MARIADB=yes;; esac
  case "$chosen" in *POSTGRES*) OPT_POSTGRES=yes;; esac
  case "$chosen" in *MONGODB*) OPT_MONGODB=yes;; esac
  case "$chosen" in *NODE*) OPT_NODE=yes;; esac
  case "$chosen" in *PHP*) OPT_PHP=yes;; esac
  case "$chosen" in *PYTHON*) OPT_PYTHON=yes;; esac
  case "$chosen" in *REDIS*) OPT_REDIS=yes;; esac
  case "$chosen" in *TYPESENSE*) OPT_TYPESENSE=yes;; esac
  case "$chosen" in *DOCKER*) OPT_DOCKER=yes;; esac
  case "$chosen" in *SECURITY*) OPT_SECURITY=yes;; esac
  # If the user just checked PHP/MariaDB/Postgres but the repo isn't available
  # on this distro, warn here so we don't surprise them with a skipped step.
  if yesno "$OPT_PHP" && [ -z "$_PHP_AVAIL" ]; then
    whiptail --title "PHP unavailable" --msgbox \
      "deb.sury.org publishes no PHP repo for ${OS_ID} ${OS_CODENAME}.\nPHP will be skipped." 10 60 < /dev/tty || true
    OPT_PHP=no
  fi
  if yesno "$OPT_MARIADB" && [ -z "$_MARIADB_AVAIL" ]; then
    whiptail --title "MariaDB unavailable" --msgbox \
      "mariadb.org publishes no MariaDB build for ${OS_ID} ${OS_CODENAME}.\nMariaDB will be skipped." 10 60 < /dev/tty || true
    OPT_MARIADB=no
  fi
  if yesno "$OPT_POSTGRES" && [ -z "$_POSTGRES_AVAIL" ]; then
    whiptail --title "PostgreSQL unavailable" --msgbox \
      "apt.postgresql.org publishes no PGDG repo for ${OS_ID} ${OS_CODENAME}.\nPostgreSQL will be skipped." 10 60 < /dev/tty || true
    OPT_POSTGRES=no
  fi
  return 0
}

tui_php() {
  local pver_args=() pver_count=0 v selected
  for v in $_PHP_AVAIL; do
    case " $PHP_VERSIONS " in *" $v "*) pver_args+=("$v" "PHP $v" ON) ;; *) pver_args+=("$v" "PHP $v" OFF) ;; esac
    pver_count=$((pver_count + 1))
  done
  selected=$(whiptail --title "PHP versions" --checklist \
    "Pick one or more PHP-FPM versions to install side-by-side.\nSites pin per-site in the Create form; more can be added later from Settings → PHP Versions." \
    14 70 "$pver_count" --cancel-button "← Back" "${pver_args[@]}" 3>&1 1>&2 2>&3 < /dev/tty) || return 1
  PHP_VERSIONS=$(echo "$selected" | tr -d '"')
  if [ -z "$PHP_VERSIONS" ]; then
    OPT_PHP=no
  elif [ -z "$PHP_DEFAULT" ]; then
    PHP_DEFAULT="${PHP_VERSIONS%% *}"
  fi
  return 0
}

tui_mariadb() {
  local mver_args=() mver_count=0 v
  MARIADB_VERSION=$(snap_to_available "$MARIADB_VERSION" "$_MARIADB_AVAIL")
  for v in $_MARIADB_AVAIL; do
    case "$v" in
      11.8)  mver_args+=("$v" "11.8 LTS (current)"            "$(req "$MARIADB_VERSION" "$v")") ;;
      11.4)  mver_args+=("$v" "11.4 LTS"                      "$(req "$MARIADB_VERSION" "$v")") ;;
      10.11) mver_args+=("$v" "10.11 LTS (oldest supported)"  "$(req "$MARIADB_VERSION" "$v")") ;;
      *)     mver_args+=("$v" "MariaDB $v"                    "$(req "$MARIADB_VERSION" "$v")") ;;
    esac
    mver_count=$((mver_count + 1))
  done
  MARIADB_VERSION=$(whiptail --title "MariaDB version" --radiolist \
    "Available for ${OS_ID} ${OS_CODENAME} (from mariadb.org):" 12 64 "$mver_count" \
    --cancel-button "← Back" "${mver_args[@]}" 3>&1 1>&2 2>&3 < /dev/tty) || return 1
  return 0
}

tui_postgres() {
  local pver_args=() pver_count=0 v
  POSTGRES_VERSION=$(snap_to_available "$POSTGRES_VERSION" "$_POSTGRES_AVAIL")
  for v in $_POSTGRES_AVAIL; do
    case "$v" in
      18) pver_args+=("$v" "PostgreSQL 18 (current)" "$(req "$POSTGRES_VERSION" "$v")") ;;
      *)  pver_args+=("$v" "PostgreSQL $v"           "$(req "$POSTGRES_VERSION" "$v")") ;;
    esac
    pver_count=$((pver_count + 1))
  done
  POSTGRES_VERSION=$(whiptail --title "PostgreSQL version" --radiolist \
    "Available for ${OS_ID} ${OS_CODENAME} (from apt.postgresql.org):" 12 64 "$pver_count" \
    --cancel-button "← Back" "${pver_args[@]}" 3>&1 1>&2 2>&3 < /dev/tty) || return 1
  return 0
}

tui_node() {
  NODE_MAJOR=$(whiptail --title "Node.js version" --radiolist \
    "Pick the system-wide Node.js LTS:" 12 60 3 \
    --cancel-button "← Back" \
    24 "Node 24 LTS (recommended)" "$(req "$NODE_MAJOR" 24)" \
    22 "Node 22 LTS"               "$(req "$NODE_MAJOR" 22)" \
    20 "Node 20 LTS"               "$(req "$NODE_MAJOR" 20)" \
    3>&1 1>&2 2>&3 < /dev/tty) || return 1
  return 0
}

tui_domain() {
  PANEL_DOMAIN=$(whiptail --title "Panel domain (optional)" --inputbox \
    "Setting a domain lets auracpd issue a real Let's Encrypt cert for the panel.\nPoint its DNS A record at this server, or leave blank to use IP:${PANEL_PORT}." \
    12 70 "$PANEL_DOMAIN" --cancel-button "← Back" 3>&1 1>&2 2>&3 < /dev/tty) || return 1
  return 0
}

# ─── state-machine driver ───────────────────────────────────────────────────
select_whiptail() {
  prime_avail_cache
  local step="components" rc
  while [ -n "$step" ]; do
    case "$step" in
      components) tui_components ;;
      php)        tui_php ;;
      mariadb)    tui_mariadb ;;
      postgres)   tui_postgres ;;
      node)       tui_node ;;
      domain)     tui_domain ;;
    esac
    rc=$?
    if [ "$rc" -eq 0 ]; then
      step=$(next_step "$step")
    else
      # Back on the first step = cancel the whole installer.
      if [ "$step" = "components" ]; then
        die "Installation cancelled."
      fi
      step=$(prev_step "$step")
      [ -z "$step" ] && step="components"
    fi
  done
}

# plain-text fallback when whiptail isn't available
select_readline() {
  msg "Select optional components (required ones install automatically):"
  OPT_MARIADB=$(ask "Install MariaDB?"   "$OPT_MARIADB")
  OPT_POSTGRES=$(ask "Install PostgreSQL?" "$OPT_POSTGRES")
  OPT_NODE=$(ask "Install Node.js ${NODE_MAJOR} LTS?" "$OPT_NODE")
  OPT_PHP=$(ask "Install PHP-FPM (deb.sury.org)?" "$OPT_PHP")
  if yesno "$OPT_PHP"; then
    local pver_list v out=""
    pver_list=$(php_available)
    if [ -z "$pver_list" ]; then
      warn "deb.sury.org has no PHP repo for ${OS_ID} ${OS_CODENAME} — skipping PHP."
      OPT_PHP=no
    else
      read -r -p "  PHP versions to install (space-separated, from: $pver_list) [${PHP_VERSIONS}]: " v < /dev/tty || true
      v="${v:-$PHP_VERSIONS}"
      for _v in $v; do
        case " $pver_list " in *" $_v "*) out="$out $_v" ;; *) warn "skipping unknown PHP version: $_v" ;; esac
      done
      PHP_VERSIONS="${out# }"
      if [ -z "$PHP_VERSIONS" ]; then OPT_PHP=no; else
        [ -z "$PHP_DEFAULT" ] && PHP_DEFAULT="${PHP_VERSIONS%% *}"
      fi
    fi
  fi
  if yesno "$OPT_MARIADB"; then
    local mver_list
    mver_list=$(mariadb_available)
    if [ -z "$mver_list" ]; then
      warn "mariadb.org has no MariaDB for ${OS_ID} ${OS_CODENAME} — skipping MariaDB."
      OPT_MARIADB=no
    else
      MARIADB_VERSION=$(snap_to_available "$MARIADB_VERSION" "$mver_list")
      read -r -p "  MariaDB LTS [$(echo "$mver_list" | tr ' ' '/')] (${MARIADB_VERSION}): " v < /dev/tty || true
      case " $mver_list " in *" ${v:-$MARIADB_VERSION} "*) MARIADB_VERSION="${v:-$MARIADB_VERSION}";; esac
    fi
  fi
  if yesno "$OPT_POSTGRES"; then
    local pver_list
    pver_list=$(postgres_available)
    if [ -z "$pver_list" ]; then
      warn "apt.postgresql.org has no PGDG repo for ${OS_ID} ${OS_CODENAME} — skipping Postgres."
      OPT_POSTGRES=no
    else
      POSTGRES_VERSION=$(snap_to_available "$POSTGRES_VERSION" "$pver_list")
      read -r -p "  PostgreSQL major [$(echo "$pver_list" | tr ' ' '/')] (${POSTGRES_VERSION}): " v < /dev/tty || true
      case " $pver_list " in *" ${v:-$POSTGRES_VERSION} "*) POSTGRES_VERSION="${v:-$POSTGRES_VERSION}";; esac
    fi
  fi
  if yesno "$OPT_NODE"; then
    read -r -p "  Node.js LTS [24/22/20] (${NODE_MAJOR}): " v < /dev/tty || true
    case "${v:-$NODE_MAJOR}" in 20|22|24) NODE_MAJOR="${v:-$NODE_MAJOR}";; esac
  fi
  OPT_PYTHON=$(ask "Install Python 3?" "$OPT_PYTHON")
  OPT_REDIS=$(ask "Install Redis?" "$OPT_REDIS")
  if mongodb_supported; then
    OPT_MONGODB=$(ask "Install MongoDB ${MONGODB_VERSION}?" "$OPT_MONGODB")
  else
    OPT_MONGODB=no
    warn "MongoDB: no server packages for ${OS_ID} ${OS_CODENAME} ${ARCH} — skipping (supported: Debian 12 amd64, Ubuntu 22.04/24.04 amd64+arm64)"
  fi
  OPT_TYPESENSE=$(ask "Install Typesense search server?" "$OPT_TYPESENSE")
  OPT_DOCKER=$(ask "Install Docker engine?" "$OPT_DOCKER")
  OPT_SECURITY=$(ask "Enable security hardening (UFW + fail2ban)?" "$OPT_SECURITY")
  if [ -z "$PANEL_DOMAIN" ]; then
    read -r -p "  Panel domain (optional; blank = IP:${PANEL_PORT}): " PANEL_DOMAIN < /dev/tty || true
  fi
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
  printf '  %-22s %s\n' "nginx (1.30 mainline)" "${C_GRN}required${C_RESET}"
  m="$(mark "$OPT_MARIADB")";  yesno "$OPT_MARIADB"  && m="$m ${C_DIM}(${MARIADB_VERSION})${C_RESET}"
  printf '  %-22s %s\n' "MariaDB" "$m"
  m="$(mark "$OPT_POSTGRES")"; yesno "$OPT_POSTGRES" && m="$m ${C_DIM}(${POSTGRES_VERSION})${C_RESET}"
  printf '  %-22s %s\n' "PostgreSQL" "$m"
  m="$(mark "$OPT_NODE")";     yesno "$OPT_NODE"     && m="$m ${C_DIM}(${NODE_MAJOR})${C_RESET}"
  printf '  %-22s %s\n' "Node.js" "$m"
  m="$(mark "$OPT_PHP")";      yesno "$OPT_PHP"      && m="$m ${C_DIM}(${PHP_VERSIONS})${C_RESET}"
  printf '  %-22s %s\n' "PHP-FPM" "$m"
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

build_plan() {
  local mariadb_l postgres_l mongodb_l node_l php_l python_l redis_l \
        typesense_l docker_l security_l panel_l
  yesno "$OPT_MARIADB"   && mariadb_l="install  (${MARIADB_VERSION})"   || mariadb_l="skip"
  yesno "$OPT_POSTGRES"  && postgres_l="install  (${POSTGRES_VERSION})"  || postgres_l="skip"
  if mongodb_supported; then
    yesno "$OPT_MONGODB" && mongodb_l="install  (${MONGODB_VERSION})" || mongodb_l="skip"
  fi
  yesno "$OPT_NODE"      && node_l="install  (${NODE_MAJOR})"            || node_l="skip"
  yesno "$OPT_PHP"       && php_l="install  (${PHP_VERSIONS})"           || php_l="skip"
  yesno "$OPT_PYTHON"    && python_l="install"  || python_l="skip"
  yesno "$OPT_REDIS"     && redis_l="install"   || redis_l="skip"
  yesno "$OPT_TYPESENSE" && typesense_l="install" || typesense_l="skip"
  yesno "$OPT_DOCKER"    && docker_l="install"  || docker_l="skip"
  yesno "$OPT_SECURITY"  && security_l="install" || security_l="skip"
  if [ -n "$PANEL_DOMAIN" ]; then panel_l="https://${PANEL_DOMAIN}"
  else panel_l="https://<server-ip>:${PANEL_PORT}"; fi
  cat <<EOF
auraCP ${AURACP_VERSION} on ${OS_ID:-?} ${OS_CODENAME:-?} (${ARCH})

  auracpd + CLI .......  required
  nginx ...............  required (1.30 mainline)
  MariaDB .............  ${mariadb_l}
  PostgreSQL ..........  ${postgres_l}
$(mongodb_supported && printf "  MongoDB .............  %s\n" "${mongodb_l}")  Node.js .............  ${node_l}
  PHP-FPM .............  ${php_l}
  Python 3 ............  ${python_l}
  Redis ...............  ${redis_l}
  Typesense ...........  ${typesense_l}
  Docker ..............  ${docker_l}
  UFW + fail2ban ......  ${security_l}

  Panel URL ...........  ${panel_l}

Proceed with this installation?
EOF
}

confirm() {
  [ "$DRY_RUN" -eq 1 ]    && { print_plan; return 0; }
  [ "$ASSUME_YES" -eq 1 ] && { print_plan; return 0; }
  [ -r /dev/tty ]         || { print_plan; return 0; }

  if [ "$INTERACTIVE" -ne 0 ] && command -v whiptail >/dev/null 2>&1; then
    if whiptail --title "auraCP installer — review" \
        --yes-button "Install" --no-button "Cancel" \
        --yesno "$(build_plan)" 24 70 < /dev/tty; then
      return 0
    fi
    die "Aborted."
  fi

  print_plan
  local a; read -r -p "Proceed with this plan? [Y/n]: " a < /dev/tty || true
  case "${a:-y}" in y|Y|"") ;; *) die "Aborted." ;; esac
}

# ──────────────────────────────────────────────────────────────────────────
# install steps
# ──────────────────────────────────────────────────────────────────────────

# wait_apt_lock — block until apt/dpkg releases the lock (max 120 s).
# Needed when a previous apt-get install triggers post-install scripts that
# briefly hold the lock again (e.g. unattended-upgrades, triggers, ldconfig).
wait_apt_lock() {
  local i=0
  while pgrep -x apt-get >/dev/null 2>&1 || pgrep -x dpkg >/dev/null 2>&1; do
    [ $i -eq 0 ] && msg "Waiting for apt lock to be released…"
    i=$((i+1)); sleep 2
    [ $i -le 60 ] || { warn "apt lock still held after 2 min — proceeding anyway"; break; }
  done
}

apt_refresh() {
  run "export DEBIAN_FRONTEND=noninteractive"
  run "apt-get update -y"
}

install_base() { # required
  msg "Installing base packages…"
  run "apt-get install -y --no-install-recommends ca-certificates curl gnupg cron logrotate rsync unzip lsb-release apt-transport-https"
  ok "Base packages ready."
}

install_core_deps() { # always — cron + curl + ca-certs the rest of the installer assumes
  msg "Installing core dependencies (cron, curl, ca-certificates)…"
  run "apt-get install -y --no-install-recommends cron curl ca-certificates"
  run "systemctl enable --now cron 2>/dev/null"
}

install_nginx() { # required — nginx 1.30 mainline from nginx.org
  msg "Installing nginx (mainline, from nginx.org)…"
  run "install -d -m 0755 /usr/share/keyrings"
  run "curl -fsSL https://nginx.org/keys/nginx_signing.key | gpg --dearmor -o /usr/share/keyrings/nginx-archive-keyring.gpg"
  # nginx.org publishes per-distro repos at /packages/mainline/{debian,ubuntu}/.
  local distro="${OS_ID:-debian}"
  echo "deb [signed-by=/usr/share/keyrings/nginx-archive-keyring.gpg] https://nginx.org/packages/mainline/${distro} ${OS_CODENAME} nginx" \
    | run "tee /etc/apt/sources.list.d/nginx.list >/dev/null"
  wait_apt_lock
  run "apt-get update -y"
  run "apt-get install -y nginx"
  # Bootstrap nginx.conf with auraCP includes (FastCGI + proxy cache zones,
  # gzip on, server_tokens off, plus the sites-enabled include line so all
  # vhosts auracpd writes to /etc/nginx/sites-available + symlinks become live.)
  if [ "$DRY_RUN" -eq 0 ]; then
    install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled \
                       /var/cache/nginx/auracp /var/lib/auracp/acme \
                       /etc/auracp/ssl /run/php-fpm
    cat > /etc/nginx/nginx.conf <<'EOF'
# auraCP-managed nginx.conf — do not edit by hand.
user www-data;
worker_processes auto;
pid /run/nginx.pid;

events {
    worker_connections 4096;
    use epoll;
    multi_accept on;
}

http {
    server_tokens off;
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    types_hash_max_size 2048;

    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_min_length 1024;
    gzip_types text/plain text/css text/xml application/json application/javascript application/xml+rss image/svg+xml;

    # auraCP cache zones — used opt-in per site via fastcgi_cache / proxy_cache directives.
    fastcgi_cache_path /var/cache/nginx/auracp levels=1:2 keys_zone=auracp_fastcgi:10m max_size=512m inactive=60m use_temp_path=off;
    fastcgi_cache_key  "$scheme$request_method$host$request_uri";
    proxy_cache_path   /var/cache/nginx/auracp_proxy levels=1:2 keys_zone=auracp_proxy:10m max_size=512m inactive=60m use_temp_path=off;
    proxy_cache_key    "$scheme$request_method$host$request_uri";

    # Per-site vhosts live under sites-available and are symlinked in.
    include /etc/nginx/conf.d/*.conf;
    include /etc/nginx/sites-enabled/*;
}
EOF
    # Disable the default vhost shipped by the nginx.org package.
    rm -f /etc/nginx/conf.d/default.conf
    # tmpfiles entry so /run/php-fpm survives reboots.
    cat > /etc/tmpfiles.d/auracp.conf <<'EOF'
d /run/php-fpm 0755 root root -
EOF
    systemd-tmpfiles --create /etc/tmpfiles.d/auracp.conf >/dev/null 2>&1 || true
  fi
  run "systemctl enable --now nginx"
  ok "nginx ready."
}

install_php_fpm() {
  msg "Installing PHP-FPM (versions: ${PHP_VERSIONS}; from deb.sury.org)…"
  # deb.sury.org publishes one signing key for the whole project (works on
  # Debian + Ubuntu); the per-distro path is /php/ for both.
  run "install -d -m 0755 /usr/share/keyrings"
  run "curl -fsSL https://packages.sury.org/php/apt.gpg | gpg --dearmor -o /usr/share/keyrings/sury-php.gpg"
  echo "deb [signed-by=/usr/share/keyrings/sury-php.gpg] https://packages.sury.org/php/ ${OS_CODENAME} main" \
    | run "tee /etc/apt/sources.list.d/sury-php.list >/dev/null"
  wait_apt_lock
  run "apt-get update -y"

  # NOTE: opcache is statically embedded in php<ver>-cli and php<ver>-fpm
  # since PHP 7.0 — there's no separate php<ver>-opcache package to list.
  # Core extensions every PHP site needs; DB / cache client libraries are
  # added conditionally so we don't install php8.5-mysql on a host that
  # never selected MariaDB, etc. The same DB/cache toggles apply to every
  # selected PHP version.
  local v pkgs
  for v in $PHP_VERSIONS; do
    msg "  → PHP ${v}"
    pkgs="php${v}-fpm php${v}-cli php${v}-mbstring php${v}-xml php${v}-curl"
    pkgs="$pkgs php${v}-gd php${v}-zip php${v}-bcmath php${v}-intl"
    yesno "$OPT_MARIADB"  && pkgs="$pkgs php${v}-mysql"
    yesno "$OPT_POSTGRES" && pkgs="$pkgs php${v}-pgsql"
    yesno "$OPT_REDIS"    && pkgs="$pkgs php${v}-redis"
    run "apt-get install -y --no-install-recommends $pkgs"
    # auraCP owns all PHP-FPM pools — disable the default `www` pool the
    # package auto-creates so it doesn't conflict with per-site pools the
    # panel writes.
    if [ "$DRY_RUN" -eq 0 ] && [ -f "/etc/php/${v}/fpm/pool.d/www.conf" ]; then
      mv -f "/etc/php/${v}/fpm/pool.d/www.conf" "/etc/php/${v}/fpm/pool.d/www.conf.disabled"
    fi
    # v0.2.46: drop a placeholder pool so FPM has at least one section to
    # load. Without it, removing the default www.conf (above) leaves an
    # empty pool.d/ and php-fpm refuses to start with:
    #   "No pool defined. at least one pool section must be specified"
    # The placeholder listens on an unreachable socket (no nginx route,
    # no http traffic), so it doesn't serve anything — it's a no-op pool
    # purely to keep systemd happy. The pool also acts as a safety net
    # when a site is later deleted: even if the last site pool for this
    # PHP version goes away, FPM stays running.
    if [ "$DRY_RUN" -eq 0 ]; then
      cat > "/etc/php/${v}/fpm/pool.d/auracp-placeholder.conf" <<EOF
; auracp-managed — keeps php${v}-fpm able to start with zero site pools.
; Do not delete. Unreachable socket; serves no traffic.
[auracp-placeholder]
user = www-data
group = www-data
listen = /run/php-fpm/auracp-placeholder-${v}.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0600
pm = ondemand
pm.max_children = 1
pm.process_idle_timeout = 10s
EOF
    fi
    run "systemctl enable --now php${v}-fpm"
  done
  # Default version — first one listed unless explicitly overridden.
  [ -z "$PHP_DEFAULT" ] && PHP_DEFAULT="${PHP_VERSIONS%% *}"
  ok "PHP-FPM ready: ${PHP_VERSIONS} (default ${PHP_DEFAULT})."
  purge_legacy_adminer
  install_wp_cli
}

# v0.2.33: wp-cli — required ONLY for `wp core install` (the WordPress
# setup wizard). v0.2.50 moved core download + wp-config writing into
# native Go in internal/wpinstall, so wp-cli is no longer involved in
# the path that hit the phar template loader regression in 2.10+.
#
# Pin: wp-cli 2.10.0 — predates the config-command phar bug. The SHA
# check is REAL now (v0.2.49 and earlier declared a SHA constant but
# never invoked sha256sum). If the download SHA doesn't match, we abort
# the wp-cli install with a clear warning rather than silently falling
# back to whatever the rolling builds bucket happens to ship today —
# that fallback was how broken wp-cli's reached operator boxes in
# v0.2.49 and earlier.
WP_CLI_VERSION="2.10.0"
WP_CLI_SHA256="4c6a93cecae7f499ca481fa7a6d6d4299c8b93214e5e5308e26770dbfd3631df"
install_wp_cli() {
  msg "Installing wp-cli ${WP_CLI_VERSION}…"
  if [ "$DRY_RUN" -eq 0 ]; then
    local tmp
    tmp=$(mktemp /tmp/wp-cli.XXXXXX.phar)
    if ! curl -fsSL "https://github.com/wp-cli/wp-cli/releases/download/v${WP_CLI_VERSION}/wp-cli-${WP_CLI_VERSION}.phar" -o "$tmp"; then
      rm -f "$tmp"
      warn "wp-cli ${WP_CLI_VERSION} download failed; WordPress auto-install will be skipped on this host. (To install manually, fetch the phar from wp-cli.org and place at /usr/local/bin/wp with mode 0755.)"
      return 0
    fi
    # v0.2.50: SHA check is finally REAL. If it mismatches we DO NOT
    # fall through to a rolling build — that path delivered broken
    # wp-cli's in v0.2.49. Operator can override by editing this file
    # if they accept the risk, but the default is fail-safe.
    local got
    got=$(sha256sum "$tmp" | cut -d' ' -f1)
    if [ "$got" != "$WP_CLI_SHA256" ]; then
      rm -f "$tmp"
      warn "wp-cli sha256 mismatch (got ${got}, expected ${WP_CLI_SHA256}); skipping. WordPress auto-install will not work until this is resolved."
      warn "If you trust the downloaded build, update WP_CLI_SHA256 in installer/install.sh and re-run."
      return 0
    fi
    chmod +x "$tmp"
    install -m 0755 "$tmp" /usr/local/bin/wp
    rm -f "$tmp"
  fi
  ok "wp-cli ${WP_CLI_VERSION} ready at /usr/local/bin/wp (sha256 verified)."
}

# PR #17 (v0.3.0): Adminer was removed. Aura DB (the embedded /dbadmin/
# SPA + /api/dbadmin/ engine) is now the sole DB admin surface. This
# function purges any leftover Adminer artefacts on hosts upgrading
# from v0.2.x so operators don't see orphaned files (the PHP-FPM pool,
# the tmpfiles drop-in, the bundled wrapper directory, and the SSO
# token runtime dirs). Safe to call on a fresh install — every step
# is a no-op when the target is already absent.
purge_legacy_adminer() {
  # Pool file lives under /etc/php/<ver>/fpm/pool.d/auracp-adminer.conf —
  # glob across whatever PHP versions are installed.
  local pool reload_needed=0
  for pool in /etc/php/*/fpm/pool.d/auracp-adminer.conf; do
    [ -e "$pool" ] || continue
    run "rm -f $pool"
    reload_needed=1
  done
  if [ -d /opt/auracp/adminer ]; then
    run "rm -rf /opt/auracp/adminer"
  fi
  if [ -f /etc/tmpfiles.d/auracp-adminer.conf ]; then
    run "rm -f /etc/tmpfiles.d/auracp-adminer.conf"
  fi
  # /run is tmpfs but on long-running hosts the dirs may still be present
  # from before the reboot — clear them so nothing references the old
  # SSO contract.
  run "rm -rf /run/auracp/adminer-sso /run/auracp/adminer-sessions"
  if [ "$reload_needed" -eq 1 ] && [ -n "${PHP_VERSIONS:-}" ]; then
    local v
    for v in $PHP_VERSIONS; do
      run "systemctl reload php${v}-fpm" || true
    done
  fi
}

install_mariadb() {
  # Belt-and-suspenders: the TUI path already snaps MARIADB_VERSION to an
  # available value, but a user passing --mariadb-version= reaches here with
  # the raw value. Re-validate against the probe.
  local available
  available=$(mariadb_available)
  if [ -z "$available" ]; then
    warn "mariadb.org has no MariaDB published for ${OS_ID} ${OS_CODENAME} — skipping."
    return 0
  fi
  case " $available " in
    *" $MARIADB_VERSION "*) ;;
    *)
      local newest="${available%% *}"
      warn "MariaDB ${MARIADB_VERSION} isn't published for ${OS_ID} ${OS_CODENAME}; installing ${newest} instead (available: ${available})."
      MARIADB_VERSION="$newest"
      ;;
  esac
  msg "Installing MariaDB ${MARIADB_VERSION} (from mariadb.org)…"
  local distro="${OS_ID:-debian}"
  run "install -d -m 0755 /usr/share/keyrings"
  run "curl -fsSL https://mariadb.org/mariadb_release_signing_key.asc -o /usr/share/keyrings/mariadb.asc"
  echo "deb [signed-by=/usr/share/keyrings/mariadb.asc] https://mirror.mariadb.org/repo/${MARIADB_VERSION}/${distro} ${OS_CODENAME} main" \
    | run "tee /etc/apt/sources.list.d/mariadb.list >/dev/null"
  wait_apt_lock
  run "apt-get update -y"
  run "apt-get install -y mariadb-server"
  run "systemctl enable --now mariadb"
  ok "MariaDB ${MARIADB_VERSION} ready."
}

install_postgres() {
  local available
  available=$(postgres_available)
  if [ -z "$available" ]; then
    warn "apt.postgresql.org has no PGDG repo for ${OS_ID} ${OS_CODENAME} — skipping Postgres."
    return 0
  fi
  case " $available " in
    *" $POSTGRES_VERSION "*) ;;
    *)
      local newest="${available%% *}"
      warn "PostgreSQL ${POSTGRES_VERSION} isn't available on ${OS_ID} ${OS_CODENAME}; installing ${newest} instead."
      POSTGRES_VERSION="$newest"
      ;;
  esac
  msg "Installing PostgreSQL ${POSTGRES_VERSION} (from apt.postgresql.org)…"
  run "install -d -m 0755 /usr/share/keyrings"
  run "curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor -o /usr/share/keyrings/pgdg.gpg"
  echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://apt.postgresql.org/pub/repos/apt ${OS_CODENAME}-pgdg main" \
    | run "tee /etc/apt/sources.list.d/pgdg.list >/dev/null"
  wait_apt_lock
  run "apt-get update -y"
  run "apt-get install -y postgresql-${POSTGRES_VERSION}"
  run "systemctl enable --now postgresql"
  ok "PostgreSQL ${POSTGRES_VERSION} ready."
}

install_node() {
  msg "Installing Node.js ${NODE_MAJOR} (latest patch, from nodejs.org)…"
  local arch="x64"
  [ "$ARCH" = "arm64" ] && arch="arm64"

  local ver
  ver=$(curl -fsSL https://nodejs.org/dist/index.json 2>/dev/null \
        | python3 -c "
import sys, json
data = json.load(sys.stdin)
prefix = 'v${NODE_MAJOR}.'
for r in data:
    if r['version'].startswith(prefix):
        print(r['version'][1:]); break
" 2>/dev/null || true)
  if [ -z "$ver" ]; then
    die "Could not resolve latest Node ${NODE_MAJOR}.x from nodejs.org/dist. Check connectivity and re-run."
  fi

  local dir="${PREFIX}/node/${ver}"
  run "mkdir -p ${dir}"
  run "curl -fsSL https://nodejs.org/dist/v${ver}/node-v${ver}-linux-${arch}.tar.xz -o /tmp/node-${ver}.tar.xz"
  run "tar -xJf /tmp/node-${ver}.tar.xz -C ${dir} --strip-components=1"
  run "rm -f /tmp/node-${ver}.tar.xz"

  run "ln -sfn ${dir} ${PREFIX}/node/default"
  run "ln -sf  ${PREFIX}/node/default/bin/node /usr/local/bin/node"
  run "ln -sf  ${PREFIX}/node/default/bin/npm  /usr/local/bin/npm"
  run "ln -sf  ${PREFIX}/node/default/bin/npx  /usr/local/bin/npx"
  ok "Node.js ${ver} installed at ${dir} (and /usr/local/bin/{node,npm,npx})."
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

install_mongodb() {
  # Guard: only run on officially supported platform+arch combos.
  # Supported per https://www.mongodb.com/docs/v8.0/administration/production-notes/
  #   Debian 12 (bookworm) amd64 | Ubuntu 22.04 (jammy) amd64+arm64 | Ubuntu 24.04 (noble) amd64+arm64
  if ! mongodb_supported; then
    warn "MongoDB skipped — not supported on ${OS_ID} ${OS_CODENAME} ${ARCH}"
    return 0
  fi
  msg "Installing MongoDB ${MONGODB_VERSION} (repo.mongodb.org)…"
  run "install -d -m 0755 /usr/share/keyrings"
  run "curl -fsSL 'https://www.mongodb.org/static/pgp/server-${MONGODB_VERSION}.asc' | gpg --dearmor -o /usr/share/keyrings/mongodb-server-${MONGODB_VERSION}.gpg"
  local repo_os repo_comp
  if [ "${OS_ID:-}" = "ubuntu" ]; then
    repo_os="ubuntu"; repo_comp="multiverse"
  else
    repo_os="debian"; repo_comp="main"
  fi
  echo "deb [ arch=${ARCH} signed-by=/usr/share/keyrings/mongodb-server-${MONGODB_VERSION}.gpg ] https://repo.mongodb.org/apt/${repo_os} ${OS_CODENAME}/mongodb-org/${MONGODB_VERSION} ${repo_comp}" \
    | run "tee /etc/apt/sources.list.d/mongodb-org-${MONGODB_VERSION}.list >/dev/null"
  wait_apt_lock
  run "apt-get update -y"
  run "apt-get install -y mongodb-org"
  run "systemctl enable --now mongod"
  ok "MongoDB ${MONGODB_VERSION} ready (mongod listening on 127.0.0.1:27017)."
}

install_typesense() {
  msg "Installing Typesense ${TYPESENSE_VERSION} (search server)…"
  local deb="/tmp/typesense-server.deb"
  run "curl -fsSL 'https://dl.typesense.org/releases/${TYPESENSE_VERSION}/typesense-server-${TYPESENSE_VERSION}-${ARCH}.deb' -o ${deb}"
  run "apt-get install -y ${deb}"
  run "rm -f ${deb}"
  run "systemctl enable --now typesense-server 2>/dev/null || true"
  ok "Typesense ready."
}

install_docker() {
  msg "Installing Docker engine…"
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
  # When this installer is shipped inside the .deb (auracp-install command),
  # the panel package is already installed. Three sub-cases:
  #   (1) installed at the same version → just ensure the service is running.
  #   (2) installed at a NEWER version  → leave it alone, point at auracp-update.
  #   (3) installed at an OLDER version → refuse, point at auracp-update.
  if [ "$DRY_RUN" -eq 0 ] && dpkg-query -W -f='${Status}' auracp 2>/dev/null | grep -q "install ok installed"; then
    local installed
    installed=$(dpkg-query -W -f='${Version}' auracp 2>/dev/null || echo "")
    if [ "$installed" = "$AURACP_VERSION" ]; then
      msg "Panel already installed at v${installed} — ensuring service is running…"
      run "systemctl daemon-reload"
      run "systemctl enable --now auracpd"
      ok "auracpd ready."
      return
    fi
    if dpkg --compare-versions "$installed" gt "$AURACP_VERSION" 2>/dev/null; then
      warn "Panel v${installed} is installed (newer than this installer's v${AURACP_VERSION})."
      warn "Leaving the panel binary alone. Use 'sudo auracp-update' to check for updates."
      return
    fi
    die "Panel v${installed} is installed; this installer ships v${AURACP_VERSION}.
   Run 'sudo auracp-update' to fetch the latest release and upgrade in place,
   or 'sudo dpkg -i ./auracp_${AURACP_VERSION}_*.deb' to install this bundle directly."
  fi
  msg "Installing auracpd…"

  local repo deb=""
  repo="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd)"
  for f in "$repo"/dist/auracp_*_"${ARCH}".deb; do
    [ -f "$f" ] && { deb="$f"; break; }
  done

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
    run "curl -fsSL https://github.com/auracp/auracp/releases/latest/download/auracpd-linux-${ARCH} -o ${PREFIX}/bin/auracpd"
    run "chmod +x ${PREFIX}/bin/auracpd"
  fi
  run "ln -sf ${PREFIX}/bin/auracp /usr/local/bin/auracp"
  install_unit auracpd "auraCP control panel" \
    "${PREFIX}/bin/auracpd -addr :${PANEL_PORT} -db ${DATA_DIR}/auracp.db -etc ${ETC_DIR}" root
  ok "auracpd installed and started on :${PANEL_PORT}."
}

# install_unit NAME DESC EXECSTART USER [EXECRELOAD] [EXTRA_SERVICE_DIRECTIVES]
install_unit() {
  local name="$1" desc="$2" exec="$3" user="$4" reload="${5:-}" extra="${6:-}"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} write /etc/systemd/system/${name}.service (User=${user})"
    printf '%s\n' "${C_DIM}[dry-run]${C_RESET} systemctl enable --now ${name}"
    return
  fi
  {
    printf '[Unit]\nDescription=%s\nAfter=network-online.target\nWants=network-online.target\n\n' "$desc"
    printf '[Service]\nType=simple\nUser=%s\nExecStart=%s\n' "$user" "$exec"
    [ -n "$reload" ] && printf 'ExecReload=%s\n' "$reload"
    [ -n "$extra" ]  && printf '%s\n' "$extra"
    printf 'Restart=always\nRestartSec=3\nLimitNOFILE=1048576\n\n'
    printf '[Install]\nWantedBy=multi-user.target\n'
  } > "/etc/systemd/system/${name}.service"
  systemctl daemon-reload
  systemctl enable --now "${name}"
}

setup_panel_domain() {
  [ -n "$PANEL_DOMAIN" ] || return 0
  msg "Configuring panel domain ${PANEL_DOMAIN} (auracpd issues its Let's Encrypt cert)…"
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
  ok "Panel domain set. Point a DNS A record for ${PANEL_DOMAIN} at this server; auracpd issues the cert automatically."
}

finalize() {
  echo
  ok "auraCP installation complete."
  echo
  if [ -n "$PANEL_DOMAIN" ]; then
    printf '%s\n' "  Open ${C_B}https://${PANEL_DOMAIN}${C_RESET} and create your admin account (first-run setup)."
    printf '%s\n' "  ${C_DIM}auracpd issues a real Let's Encrypt cert once ${PANEL_DOMAIN} resolves to this server.${C_RESET}"
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
  prompt_panel_domain
  confirm

  apt_refresh
  install_base
  install_core_deps
  install_nginx
  yesno "$OPT_MARIADB"  && install_mariadb
  yesno "$OPT_POSTGRES" && install_postgres
  yesno "$OPT_NODE"     && install_node
  yesno "$OPT_PHP"      && install_php_fpm
  yesno "$OPT_PYTHON"   && install_python
  yesno "$OPT_REDIS"    && install_redis
  yesno "$OPT_MONGODB"  && install_mongodb
  yesno "$OPT_TYPESENSE" && install_typesense
  yesno "$OPT_DOCKER"   && install_docker
  yesno "$OPT_SECURITY" && install_security
  install_auracpd
  setup_panel_domain
  finalize
}

main "$@"
