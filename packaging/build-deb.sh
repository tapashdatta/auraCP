#!/usr/bin/env bash
#
# Build a .deb for auraCP. Uses dpkg-deb when available (Debian/Ubuntu/CI);
# otherwise falls back to assembling the ar archive by hand (works on macOS).
#
# Usage: packaging/build-deb.sh <amd64|arm64> [version]
# Expects the binaries at dist/auracpd-linux-<arch> and dist/auracp-linux-<arch>
# (produced by `make dist`).
#
set -euo pipefail

ARCH="${1:?usage: build-deb.sh <amd64|arm64> [version]}"
VERSION="${2:-0.1.0}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIND="$ROOT/dist"
PKG="$BIND/auracp_${VERSION}_${ARCH}"
DEB="$BIND/auracp_${VERSION}_${ARCH}.deb"

[ -f "$BIND/auracpd-linux-$ARCH" ] || { echo "missing $BIND/auracpd-linux-$ARCH (run: make dist)"; exit 1; }

rm -rf "$PKG"
mkdir -p "$PKG/DEBIAN" \
         "$PKG/opt/auracp/bin" \
         "$PKG/opt/auracp/installer" \
         "$PKG/opt/auracp/packaging" \
         "$PKG/etc/systemd/system" \
         "$PKG/var/lib/auracp" \
         "$PKG/etc/auracp"

install -m 0755 "$BIND/auracpd-linux-$ARCH" "$PKG/opt/auracp/bin/auracpd"
install -m 0755 "$BIND/auracp-linux-$ARCH"  "$PKG/opt/auracp/bin/auracp"
install -m 0644 "$ROOT/packaging/auracpd.service" "$PKG/etc/systemd/system/auracpd.service"
# v0.2.15: watchdog timer that survives the "Restart=always didn't fire after
# clean stop" systemd corner case during in-panel upgrades.
install -m 0755 "$ROOT/packaging/auracpd-watchdog.sh"      "$PKG/opt/auracp/bin/auracpd-watchdog"
install -m 0644 "$ROOT/packaging/auracpd-watchdog.service" "$PKG/etc/systemd/system/auracpd-watchdog.service"
install -m 0644 "$ROOT/packaging/auracpd-watchdog.timer"   "$PKG/etc/systemd/system/auracpd-watchdog.timer"
# v0.2.25: ship the Adminer SSO wrapper alongside the installer; install_adminer
# copies it into /opt/auracp/adminer/index.php once PHP-FPM is in place.
# v0.2.31: dropped adminer-plugins.php — the new wrapper uses Adminer's own
# auth POST flow and doesn't need a plugin subclass.
install -m 0644 "$ROOT/packaging/adminer-wrapper.php" "$PKG/opt/auracp/packaging/adminer-wrapper.php"
# v0.2.39: ship the Adminer theme CSS alongside the wrapper. Adminer
# auto-loads adminer.css from its own directory if present.
install -m 0644 "$ROOT/packaging/adminer.css" "$PKG/opt/auracp/packaging/adminer.css"
# Bundle the data-plane installer + uninstaller so users don't need the repo.
install -m 0755 "$ROOT/installer/install.sh"   "$PKG/opt/auracp/installer/install.sh"
install -m 0755 "$ROOT/installer/uninstall.sh" "$PKG/opt/auracp/installer/uninstall.sh"
install -m 0755 "$ROOT/installer/update.sh"    "$PKG/opt/auracp/installer/update.sh"
chmod 700 "$PKG/etc/auracp"

INSTALLED_SIZE=$(du -sk "$PKG" | cut -f1)

cat > "$PKG/DEBIAN/control" <<EOF
Package: auracp
Version: ${VERSION}
Architecture: ${ARCH}
Maintainer: auraCP <info@localhost>
Installed-Size: ${INSTALLED_SIZE}
Section: admin
Priority: optional
Description: auraCP — lightweight server control panel
 A minimal, modern control panel for hosting WordPress, PHP, Node.js, Python,
 static and reverse-proxy sites. Single static binary with the admin UI embedded.
 The data plane (nginx, PHP-FPM, MariaDB / PostgreSQL, …) is installed
 separately by sudo auracp-install. Future upgrades via sudo auracp-update.
EOF

cat > "$PKG/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
# v0.2.16: belt-and-braces — if a previous panel-initiated upgrade got killed
# mid-flight (the v0.2.13–0.2.15 setsid-only spawn was vulnerable to systemd's
# cgroup kill), the dpkg state may have been left half-configured. Running
# `dpkg --configure -a` here is harmless when there's nothing pending, and
# completes any partial install when there is. Eats failures so it can't
# brick the current install attempt.
dpkg --configure -a 2>/dev/null || true
mkdir -p /var/lib/auracp /etc/auracp
chmod 700 /etc/auracp
ln -sf /opt/auracp/bin/auracp             /usr/local/bin/auracp
ln -sf /opt/auracp/installer/install.sh   /usr/local/bin/auracp-install
ln -sf /opt/auracp/installer/uninstall.sh /usr/local/bin/auracp-uninstall
ln -sf /opt/auracp/installer/update.sh    /usr/local/bin/auracp-update
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
  systemctl enable auracpd >/dev/null 2>&1 || true
  # The first 'restart' kicks off the new binary. If we end up in the
  # documented systemd corner case where Restart=always doesn't trigger
  # after a clean 'systemctl stop' (e.g. prerm's stop timing out and
  # going to SIGKILL leaves the unit "deactivated"), this brings it back.
  systemctl restart auracpd || true
  # Defensive: if for any reason the daemon isn't listening within ~10s,
  # kick it one more time. Eats curl exit codes so the postinst never
  # fails the dpkg install — the panel can be recovered by SSH.
  i=0
  while [ $i -lt 10 ]; do
    if curl -kfsS https://127.0.0.1:8443/api/health -o /dev/null --max-time 1 2>/dev/null; then
      break
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ $i -ge 10 ]; then
    systemctl restart auracpd || true
  fi
  # v0.2.15: enable + start the watchdog timer so any future stuck state
  # (e.g. after an in-panel upgrade from a pre-v0.2.15 release) recovers
  # by itself within ~60s instead of leaving the operator at a 502.
  systemctl enable --now auracpd-watchdog.timer >/dev/null 2>&1 || true
  # v0.2.32: if Adminer was installed previously (i.e. PHP is on the host),
  # refresh the SSO wrapper from the just-unpacked packaging directory.
  # Without this an upgrade leaves the daemon on the new version but the
  # wrapper PHP on the prior version — which is exactly what bit the
  # Adminer "No active panel session" flow on v0.2.31. install_adminer
  # does this normally, but it's gated behind a full auracp-install run;
  # this line makes panel-pill / auracp-update upgrades self-healing too.
  if [ -d /opt/auracp/adminer ] && [ -f /opt/auracp/packaging/adminer-wrapper.php ]; then
    install -m 0644 /opt/auracp/packaging/adminer-wrapper.php /opt/auracp/adminer/index.php
    # v0.2.39: refresh the theme CSS too so design changes self-deploy.
    if [ -f /opt/auracp/packaging/adminer.css ]; then
      install -m 0644 /opt/auracp/packaging/adminer.css /opt/auracp/adminer/adminer.css
    fi
    # Stale subclass file from < v0.2.31 — current wrapper doesn't use it.
    rm -f /opt/auracp/adminer/adminer-plugins.php
    # Reload any installed PHP-FPM versions so an op-code cache (if enabled)
    # picks up the new wrapper. Safe to ignore failures — opcache will
    # re-validate on next access by mtime anyway.
    for unit in $(systemctl list-units --type=service --state=active --no-legend 'php*-fpm.service' 2>/dev/null | awk '{print $1}'); do
      systemctl reload "$unit" 2>/dev/null || true
    done
  fi

  # v0.2.35: install wp-cli if PHP is present but the binary is missing.
  # Covers upgrades from < v0.2.33 (when wp-cli was added to install_php_fpm) —
  # those hosts have PHP-FPM but no /usr/local/bin/wp, and clicking
  # WordPress auto-install fails with the wp-cli-missing message.
  # Idempotent: skips if /usr/local/bin/wp already exists.
  if command -v php >/dev/null 2>&1 && [ ! -x /usr/local/bin/wp ]; then
    if curl -fsSL "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar" -o /tmp/wp-cli.phar 2>/dev/null; then
      chmod +x /tmp/wp-cli.phar
      mv /tmp/wp-cli.phar /usr/local/bin/wp
      echo "auraCP: installed wp-cli at /usr/local/bin/wp"
    fi
  fi
  echo
  echo "auraCP panel installed and running on https://<server-ip>:8443"
  echo "Next step — provision the data plane (nginx, MariaDB/Postgres, PHP-FPM, Node, …):"
  echo "  sudo auracp-install"
  echo "Or non-interactively:"
  echo "  sudo auracp-install --yes --db=both --node=yes --php=yes --panel-domain=panel.example.com"
  echo
  echo "Future upgrades:  sudo auracp-update"
fi
exit 0
EOF

cat > "$PKG/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
  # Stop the watchdog FIRST so it can't observe the brief stop-then-start
  # and double-restart auracpd during an upgrade.
  systemctl stop auracpd-watchdog.timer 2>/dev/null || true
  systemctl stop auracpd-watchdog.service 2>/dev/null || true
  # Only fully disable the watchdog on a real remove (arg "remove") — keep
  # it enabled across an upgrade (arg "upgrade") so the new postinst can
  # rely on it being present.
  if [ "${1:-}" = "remove" ] || [ "${1:-}" = "purge" ]; then
    systemctl disable auracpd-watchdog.timer >/dev/null 2>&1 || true
  fi
  systemctl stop auracpd || true
  systemctl disable auracpd >/dev/null 2>&1 || true
fi
rm -f /usr/local/bin/auracp /usr/local/bin/auracp-install /usr/local/bin/auracp-uninstall /usr/local/bin/auracp-update
exit 0
EOF

chmod 0755 "$PKG/DEBIAN/postinst" "$PKG/DEBIAN/prerm"

if command -v dpkg-deb >/dev/null 2>&1; then
  dpkg-deb --root-owner-group --build "$PKG" "$DEB" >/dev/null
else
  echo "dpkg-deb not found — assembling .deb manually (portable ar writer)…"
  TMP="$(mktemp -d)"
  echo "2.0" > "$TMP/debian-binary"
  tar --numeric-owner --owner=0 --group=0 -czf "$TMP/control.tar.gz" -C "$PKG/DEBIAN" .
  tar --numeric-owner --owner=0 --group=0 \
      --exclude=./DEBIAN -czf "$TMP/data.tar.gz" -C "$PKG" .
  # Write a plain ar archive (no symbol table) in the exact deb member order.
  python3 - "$DEB" "$TMP/debian-binary" "$TMP/control.tar.gz" "$TMP/data.tar.gz" <<'PY'
import sys, os
out, members = sys.argv[1], sys.argv[2:]
def header(name, size):
    return (name.ljust(16) + "0".ljust(12) + "0".ljust(6) + "0".ljust(6)
            + "100644".ljust(8) + str(size).ljust(10) + "`\n").encode()
with open(out, "wb") as o:
    o.write(b"!<arch>\n")
    for m in members:
        data = open(m, "rb").read()
        o.write(header(os.path.basename(m), len(data)))
        o.write(data)
        if len(data) % 2:   # members are padded to even length
            o.write(b"\n")
PY
  rm -rf "$TMP"
fi

echo "built: $DEB"
