#!/bin/bash
# Node.js LTS from nodejs.org, checksum-verified against SHASUMS256.txt.
set -euo pipefail
MAJOR="${CORRAL_NODE_MAJOR:-22}"
if command -v node >/dev/null 2>&1 && [ "$(node -p 'process.versions.node.split(".")[0]')" = "$MAJOR" ]; then
  echo "[corral] node $(node --version) already installed"; exit 0
fi
arch=$(uname -m); case "$arch" in aarch64) narch=arm64 ;; x86_64) narch=x64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac
ver=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r --arg m "v$MAJOR." '[.[] | select(.version|startswith($m))][0].version')
[ -n "$ver" ] && [ "$ver" != "null" ] || { echo "could not resolve node v$MAJOR" >&2; exit 1; }
echo "[corral] installing node $ver"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
tarball="node-$ver-linux-$narch.tar.xz"
curl -fsSL -o "$tmp/$tarball" "https://nodejs.org/dist/$ver/$tarball"
curl -fsSL -o "$tmp/SHASUMS256.txt" "https://nodejs.org/dist/$ver/SHASUMS256.txt"
(cd "$tmp" && grep " $tarball\$" SHASUMS256.txt | sha256sum -c -)
rm -rf /usr/local/lib/nodejs && mkdir -p /usr/local/lib/nodejs
tar -xJf "$tmp/$tarball" -C /usr/local/lib/nodejs --strip-components=1
for b in node npm npx corepack; do ln -sf "/usr/local/lib/nodejs/bin/$b" "/usr/local/bin/$b"; done
# npm release-age gate reduces exposure to freshly published (possibly hijacked) packages.
npm config set --location=global min-release-age "${CORRAL_NPM_MIN_RELEASE_AGE:-7}" 2>/dev/null || true
node --version && npm --version
