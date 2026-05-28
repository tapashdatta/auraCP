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

# Cache the previously-installed .deb so a verified-broken upgrade can roll
# back without curl'ing GitHub again (no network needed for the rescue path).
PREV_DEB="/var/cache/auracp/auracp_${CURRENT}_${ARCH}.deb"
mkdir -p /var/cache/auracp
if [ -f "/var/cache/apt/archives/auracp_${CURRENT}_${ARCH}.deb" ]; then
  cp -f "/var/cache/apt/archives/auracp_${CURRENT}_${ARCH}.deb" "$PREV_DEB" 2>/dev/null || true
fi

dpkg -i "$TMP" \
  || { apt-get install -fy && dpkg -i "$TMP"; } \
  || die "dpkg install failed."

# Verify the new daemon is actually up before reporting success. Belt-and-
# suspenders for the case where the package postinst's `systemctl restart`
# raced something (dpkg prerm timing out → SIGKILL → leaving the unit in a
# 'stopped + Restart=always-not-triggered-because-clean-stop' limbo). The
# /api/health endpoint is unauth and returns 200 with {"status":"ok"} the
# instant net/http is listening — perfect signal.
msg "Verifying daemon health…"
HEALTH_OK=0
for i in $(seq 1 30); do
  if curl -kfsS https://127.0.0.1:8443/api/health -o /dev/null --max-time 2 2>/dev/null; then
    HEALTH_OK=1
    ok "auracpd responding (took ${i}s)."
    break
  fi
  sleep 1
done

if [ "$HEALTH_OK" -eq 0 ]; then
  warn "auracpd is not responding 30s after install — attempting an explicit restart."
  systemctl restart auracpd >/dev/null 2>&1 || true
  sleep 5
  if curl -kfsS https://127.0.0.1:8443/api/health -o /dev/null --max-time 5 2>/dev/null; then
    ok "auracpd recovered after explicit restart."
    HEALTH_OK=1
  fi
fi

if [ "$HEALTH_OK" -eq 0 ]; then
  if [ -f "$PREV_DEB" ]; then
    warn "auracpd still down. Rolling back to ${CURRENT}…"
    if dpkg -i "$PREV_DEB" >/dev/null 2>&1 && systemctl restart auracpd; then
      sleep 3
      if curl -kfsS https://127.0.0.1:8443/api/health -o /dev/null --max-time 5 2>/dev/null; then
        die "Rollback to ${CURRENT} restored the panel. Check 'journalctl -u auracpd' for why ${LATEST_VER} failed."
      fi
    fi
  fi
  die "auracpd not responding after ${LATEST_VER} install. SSH and run:
  systemctl status auracpd --no-pager
  journalctl -u auracpd -n 100 --no-pager
  systemctl restart auracpd
If the daemon still won't start, re-install the previous .deb manually:
  curl -fL -o /tmp/auracp.deb https://github.com/${REPO}/releases/download/v${CURRENT}/auracp_${CURRENT}_${ARCH}.deb
  dpkg -i /tmp/auracp.deb && systemctl restart auracpd"
fi

ok "Upgraded to ${LATEST_VER} and verified responding on :8443."
