#!/bin/bash
# Go toolchain from go.dev, checksum-verified against go.dev/dl metadata.
set -euo pipefail
arch=$(uname -m); case "$arch" in aarch64) garch=arm64 ;; x86_64) garch=amd64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac
meta=$(curl -fsSL "https://go.dev/dl/?mode=json")
ver=$(printf '%s' "$meta" | jq -r '[.[] | select(.stable)][0].version')
if [ -x /usr/local/go/bin/go ] && [ "$(/usr/local/go/bin/go version | awk '{print $3}')" = "$ver" ]; then
  echo "[corral] $ver already installed"; exit 0
fi
file="$ver.linux-$garch.tar.gz"
sha=$(printf '%s' "$meta" | jq -r --arg f "$file" '.[0].files[] | select(.filename==$f) | .sha256')
[ -n "$sha" ] || { echo "no checksum for $file" >&2; exit 1; }
echo "[corral] installing $ver"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$file" "https://go.dev/dl/$file"
echo "$sha  $tmp/$file" | sha256sum -c -
rm -rf /usr/local/go && tar -C /usr/local -xzf "$tmp/$file"
cat >/etc/profile.d/go.sh <<'EOF'
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
EOF
/usr/local/go/bin/go version
