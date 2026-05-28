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
         "$PKG/etc/systemd/system" \
         "$PKG/var/lib/auracp" \
         "$PKG/etc/auracp"

install -m 0755 "$BIND/auracpd-linux-$ARCH" "$PKG/opt/auracp/bin/auracpd"
install -m 0755 "$BIND/auracp-linux-$ARCH"  "$PKG/opt/auracp/bin/auracp"
install -m 0644 "$ROOT/packaging/auracpd.service" "$PKG/etc/systemd/system/auracpd.service"
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
 The data plane (Caddy, FrankenPHP, databases) is installed separately by the
 auraCP installer.
EOF

cat > "$PKG/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
mkdir -p /var/lib/auracp /etc/auracp
chmod 700 /etc/auracp
ln -sf /opt/auracp/bin/auracp /usr/local/bin/auracp
if [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
  systemctl enable auracpd >/dev/null 2>&1 || true
  systemctl restart auracpd || true
  echo "auraCP started on https://<server-ip>:8443 — open it to create your admin account."
fi
exit 0
EOF

cat > "$PKG/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ -d /run/systemd/system ]; then
  systemctl stop auracpd || true
  systemctl disable auracpd >/dev/null 2>&1 || true
fi
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
