#!/usr/bin/env bash
# Build ark-server (Go, no cgo) for release.
#
#   scripts/release/build-server.sh [--out DIR] [--sidecar DIR] [--no-archive] [TARGET...]
#
# TARGET is GOOS/GOARCH (default: linux/amd64 linux/arm64 darwin/arm64
# darwin/amd64 windows/amd64). For each target it writes
#   DIR/ark-server-<version>-<goos>-<goarch>/      binary, LICENSE, README, systemd unit
#   DIR/ark-server-<version>-<goos>-<goarch>.tar.gz (.zip for windows)
# With --sidecar DIR it also copies the binary to
#   DIR/ark-server-<rust target triple>[.exe]
# which is the name Tauri's bundle.externalBin expects. "darwin/universal"
# (macOS only, needs lipo) builds both darwin arches and merges them.
#
# The web UI and the agent docs (GET /api/agent/v1/docs) are embedded
# (go generate); the build fails when the embedded docs are missing or stale
# (`ark-server docs-check`, also run on every built binary the host can
# execute). The version comes from VERSION
# (ldflags -X main.version). Output is reproducible for a given commit:
# -trimpath, -buildvcs=false, CGO_ENABLED=0.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
out="$root/dist/server"
sidecar=""
archive=1
targets=()
while [ $# -gt 0 ]; do
  case "$1" in
    --out) out="$2"; shift 2 ;;
    --sidecar) sidecar="$2"; shift 2 ;;
    --no-archive) archive=0; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) targets+=("$1"); shift ;;
  esac
done
[ ${#targets[@]} -gt 0 ] || targets=(linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64)

version="$(tr -d ' \r\n' < "$root/VERSION")"
mkdir -p "$out"
out="$(cd "$out" && pwd)"

triple() {
  case "$1" in
    linux/amd64) echo x86_64-unknown-linux-gnu ;;
    linux/arm64) echo aarch64-unknown-linux-gnu ;;
    darwin/amd64) echo x86_64-apple-darwin ;;
    darwin/arm64) echo aarch64-apple-darwin ;;
    darwin/universal) echo universal-apple-darwin ;;
    windows/amd64) echo x86_64-pc-windows-msvc ;;
    windows/arm64) echo aarch64-pc-windows-msvc ;;
    *) echo "unknown target $1" >&2; return 1 ;;
  esac
}

echo "ark-server $version: embedding ark_studio/web/ and the agent docs"
(cd "$root/ark_studio/server" && go generate ./internal/webui ./internal/agentdocs)
(cd "$root/ark_studio/server" && go run ./cmd/ark-server docs-check --docs ../../docs)
hostos="$(cd "$root/ark_studio/server" && go env GOHOSTOS)"; hostarch="$(cd "$root/ark_studio/server" && go env GOHOSTARCH)"

build_one() { # goos goarch dest
  (cd "$root/ark_studio/server" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=$version" -o "$3" ./cmd/ark-server)
}

sha() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

for t in "${targets[@]}"; do
  goos="${t%/*}"; goarch="${t#*/}"
  exe=""; [ "$goos" = windows ] && exe=".exe"
  name="ark-server-$version-$goos-$goarch"
  stage="$out/$name"
  rm -rf "$stage"; mkdir -p "$stage"
  if [ "$goarch" = universal ]; then
    build_one darwin arm64 "$stage/ark-server-arm64"
    build_one darwin amd64 "$stage/ark-server-amd64"
    lipo -create -output "$stage/ark-server" "$stage/ark-server-arm64" "$stage/ark-server-amd64"
    rm "$stage/ark-server-arm64" "$stage/ark-server-amd64"
  else
    build_one "$goos" "$goarch" "$stage/ark-server$exe"
  fi
  echo "built $t -> $stage/ark-server$exe ($(wc -c < "$stage/ark-server$exe") bytes)"
  if [ "$goos" = "$hostos" ] && { [ "$goarch" = "$hostarch" ] || [ "$goarch" = universal ]; }; then
    "$stage/ark-server$exe" docs-check
  fi
  if [ -n "$sidecar" ]; then
    mkdir -p "$sidecar"
    cp "$stage/ark-server$exe" "$sidecar/ark-server-$(triple "$t")$exe"
  fi
  [ "$archive" = 1 ] || continue
  cp "$root/LICENSE" "$stage/LICENSE"
  cp "$root/scripts/release/server-package/README.md" "$stage/README.md"
  if [ "$goos" != windows ]; then
    cp "$root/scripts/release/server-package/ark-server@.service" "$stage/"
    cp "$root/scripts/release/server-package/server.example.json" "$stage/"
  else
    cp "$root/scripts/release/server-package/server.example.json" "$stage/"
  fi
  (cd "$out" && rm -f "$name.tar.gz" "$name.zip"
    if [ "$goos" = windows ]; then
      if command -v zip >/dev/null; then zip -qr "$name.zip" "$name"
      else powershell.exe -NoProfile -Command "Compress-Archive -Force -Path '$name' -DestinationPath '$name.zip'"; fi
    else
      tar -czf "$name.tar.gz" "$name"
    fi)
done

if [ "$archive" = 1 ]; then
  (cd "$out" && shopt -s nullglob && files=(*.tar.gz *.zip) && sha "${files[@]}" > SHA256SUMS-server.txt)
  echo "archives and SHA256SUMS-server.txt in $out"
fi
