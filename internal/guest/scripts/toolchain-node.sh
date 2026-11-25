#!/bin/bash
# Node.js LTS from nodejs.org, checksum-verified against SHASUMS256.txt.
# Unpinned it installs the newest v$MAJOR.* release on the day the box (or its
# golden image) is built; `toolchain_versions = { node = "X.Y.Z" }` exports
# CORRAL_NODE_VERSION and installs exactly that release. Either way the
# checksum comes from the release's own SHASUMS256.txt — it catches a corrupt
# download, the pin is what makes the version reproducible.
set -euo pipefail
MAJOR="${CORRAL_NODE_MAJOR:-22}"
PINNED="${CORRAL_NODE_VERSION:-}"
if command -v node >/dev/null 2>&1; then
  have=$(node -p 'process.versions.node')
  if { [ -n "$PINNED" ] && [ "$have" = "$PINNED" ]; } || { [ -z "$PINNED" ] && [ "${have%%.*}" = "$MAJOR" ]; }; then
    echo "[corral] node v$have already installed"; exit 0
  fi
fi
arch=$(uname -m); case "$arch" in aarch64) narch=arm64 ;; x86_64) narch=x64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac
if [ -n "$PINNED" ]; then
  ver="v$PINNED"
  echo "[corral] installing node $ver (pinned by toolchain_versions)"
else
  ver=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r --arg m "v$MAJOR." '[.[] | select(.version|startswith($m))][0].version')
  [ -n "$ver" ] && [ "$ver" != "null" ] || { echo "could not resolve node v$MAJOR" >&2; exit 1; }
  echo "[corral] installing node $ver (newest v$MAJOR release) — pin it with toolchain_versions = { node = \"${ver#v}\" }"
fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
tarball="node-$ver-linux-$narch.tar.xz"
curl -fsSL -o "$tmp/SHASUMS256.txt" "https://nodejs.org/dist/$ver/SHASUMS256.txt" ||
  { echo "corral: node $ver is not a release on nodejs.org — check toolchain_versions" >&2; exit 1; }
grep -q " $tarball\$" "$tmp/SHASUMS256.txt" || { echo "no checksum for $tarball" >&2; exit 1; }
curl -fsSL -o "$tmp/$tarball" "https://nodejs.org/dist/$ver/$tarball"
(cd "$tmp" && grep " $tarball\$" SHASUMS256.txt | sha256sum -c -)
rm -rf /usr/local/lib/nodejs && mkdir -p /usr/local/lib/nodejs
tar -xJf "$tmp/$tarball" -C /usr/local/lib/nodejs --strip-components=1
for b in node npm npx corepack; do ln -sf "/usr/local/lib/nodejs/bin/$b" "/usr/local/bin/$b"; done
# npm release-age gate reduces exposure to freshly published (possibly hijacked) packages.
npm config set --location=global min-release-age "${CORRAL_NPM_MIN_RELEASE_AGE:-7}" 2>/dev/null || true
node --version && npm --version
