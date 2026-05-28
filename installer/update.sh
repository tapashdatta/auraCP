#!/usr/bin/env bash
#
# auracp-update — fetch the latest auraCP release from GitHub and install it.
# Idempotent: if you're already on the latest version, exits cleanly with 0.
#
# Usage:
#   sudo auracp-update                 # check + install if newer
#   sudo auracp-update --check         # report only; no install
#   sudo auracp-update --to v0.2.4     # pin to a specific tag (e.g. roll back)
#
# This is what /api/instance/update detaches & runs; safe to invoke by hand.
#
set -euo pipefail

REPO="${AURACP_REPO:-tapashdatta/auraCP}"
TARGET=""           # explicit --to tag, otherwise picked from /releases/latest
CHECK_ONLY=0

for a in "$@"; do
  case "$a" in
    --check) CHECK_ONLY=1 ;;
    --to=*)  TARGET="${a#*=}" ;;
    --to)    shift; TARGET="${1:-}" ;;
    -h|--help) grep '^#' "$0" | sed 's/^#\s\?//'; exit 0 ;;
    *) echo "unknown option: $a"; exit 1 ;;
  esac
done

if [ -t 1 ]; then
  C=$'\e[36m'; G=$'\e[32m'; Y=$'\e[33m'; R=$'\e[31m'; D=$'\e[2m'; Z=$'\e[0m'
else C=""; G=""; Y=""; R=""; D=""; Z=""; fi
msg() { printf '%s\n' "${C}::${Z} $*"; }
ok()  { printf '%s\n' "${G}✓${Z} $*"; }
die() { printf '%s\n' "${R}✗ $*${Z}" >&2; exit 1; }

ARCH=$(dpkg --print-architecture 2>/dev/null || die "dpkg not available — Debian/Ubuntu only.")
CURRENT=$(dpkg-query -W -f='${Version}' auracp 2>/dev/null || echo "")
[ -n "$CURRENT" ] || die "auracp is not installed; run installer/install.sh first."

# Pick the target tag — explicit (--to) or whatever GitHub says is latest.
if [ -z "$TARGET" ]; then
  msg "Asking GitHub for the latest release of ${REPO}…"
  TARGET=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
           | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1) \
    || die "Could not reach GitHub Releases. Check connectivity and retry."
  [ -n "$TARGET" ] || die "GitHub returned no tag_name. The repo may have no releases yet."
fi
LATEST_VER="${TARGET#v}"   # strip leading v if present

printf "  %-12s %s\n" "Current:" "$CURRENT"
printf "  %-12s %s\n\n" "Available:" "$LATEST_VER"

if [ "$CURRENT" = "$LATEST_VER" ]; then
  ok "Already on the latest version ($CURRENT)."
  exit 0
fi
# dpkg's own version comparator handles 0.1.18 < 0.2.0 etc.
if dpkg --compare-versions "$CURRENT" gt "$LATEST_VER"; then
  printf '%s\n' "${Y}!${Z} Installed ($CURRENT) is newer than the released $LATEST_VER. Nothing to do."
  exit 0
fi

if [ "$CHECK_ONLY" -eq 1 ]; then
  printf '%s\n' "Update is available: $LATEST_VER. Re-run without --check to install."
  exit 0
fi

[ "$(id -u)" -eq 0 ] || die "Run as root (sudo)."

URL="https://github.com/${REPO}/releases/download/${TARGET}/auracp_${LATEST_VER}_${ARCH}.deb"
TMP=$(mktemp -t auracp.XXXXXX.deb)
trap 'rm -f "$TMP"' EXIT

msg "Downloading ${TARGET} (${ARCH})…"
curl -fL --progress-bar -o "$TMP" "$URL" \
  || die "Download failed. URL: $URL"

msg "Installing…"
export DEBIAN_FRONTEND=noninteractive
dpkg -i "$TMP" \
  || { apt-get install -fy && dpkg -i "$TMP"; } \
  || die "dpkg install failed."

ok "Upgraded to ${LATEST_VER}. auracpd has been restarted by the package postinst."
