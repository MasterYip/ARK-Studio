#!/usr/bin/env bash
# Package a built linux ark-server as a .deb (Debian/Ubuntu, needs dpkg-deb).
#
#   scripts/release/build-server-deb.sh <ark-server binary> <amd64|arm64> [out dir]
#
# Installs /usr/bin/ark-server, the systemd *user* unit
# /usr/lib/systemd/user/ark-server@.service (ExecStart=/usr/bin/ark-server),
# and docs under /usr/share/doc/ark-server. Nothing is enabled or started:
# each user runs `systemctl --user enable --now ark-server@<name>` with their
# own ~/.config/arkstudio/<name>.json (see README.md in the package docs).
set -euo pipefail

bin="$1"; arch="$2"; out="${3:-$(dirname "$bin")}"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="$(tr -d ' \r\n' < "$root/VERSION")"
pkg="$(mktemp -d)"
trap 'rm -rf "$pkg"' EXIT

install -Dm0755 "$bin" "$pkg/usr/bin/ark-server"
sed -e 's|%h/.local/bin/ark-server|/usr/bin/ark-server|' \
  "$root/scripts/release/server-package/ark-server@.service" > "$pkg/unit"
install -Dm0644 "$pkg/unit" "$pkg/usr/lib/systemd/user/ark-server@.service"
rm "$pkg/unit"
install -Dm0644 "$root/scripts/release/server-package/README.md" "$pkg/usr/share/doc/ark-server/README.md"
install -Dm0644 "$root/scripts/release/server-package/server.example.json" "$pkg/usr/share/doc/ark-server/server.example.json"
install -Dm0644 "$root/LICENSE" "$pkg/usr/share/doc/ark-server/copyright"

size="$(du -sk "$pkg/usr" | cut -f1)"
mkdir -p "$pkg/DEBIAN"
cat > "$pkg/DEBIAN/control" <<EOF
Package: ark-server
Version: $version
Section: web
Priority: optional
Architecture: $arch
Installed-Size: $size
Maintainer: Raymon Yip <noreply@github.com>
Homepage: https://github.com/MasterYip/ARK-Studio
Recommends: git
Suggests: openssh-client, ripgrep
Description: ARK Studio server (single binary, web UI embedded)
 ARK (Auto-Research Kit) is a local-first, node-first research workbench.
 This package installs the ark-server binary and a systemd user unit
 template (ark-server@.service). Nothing is started automatically.
EOF

mkdir -p "$out"
deb="$out/ark-server_${version}_${arch}.deb"
dpkg-deb --root-owner-group --build "$pkg" "$deb" >/dev/null
echo "$deb"
