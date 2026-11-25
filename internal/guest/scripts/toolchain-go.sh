#!/bin/bash
# Go toolchain from go.dev, checksum-verified against go.dev/dl metadata.
# Unpinned it installs the newest stable release on the day the box (or its
# golden image) is built; `toolchain_versions = { go = "1.25.14" }` exports
# CORRAL_GO_VERSION and installs exactly that release. The checksum comes
# from the same metadata that lists the release — it catches a corrupt
# download, the pin is what makes the version reproducible.
set -euo pipefail
arch=$(uname -m); case "$arch" in aarch64) garch=arm64 ;; x86_64) garch=amd64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac
if [ -n "${CORRAL_GO_VERSION:-}" ]; then
  ver="go$CORRAL_GO_VERSION"
  # The default listing holds only the supported releases; an older pin is
  # found only in the full one.
  meta=$(curl -fsSL "https://go.dev/dl/?mode=json&include=all")
else
  meta=$(curl -fsSL "https://go.dev/dl/?mode=json")
  ver=$(printf '%s' "$meta" | jq -r '[.[] | select(.stable)][0].version')
fi
[ -n "$ver" ] && [ "$ver" != "null" ] || { echo "could not resolve a stable go release" >&2; exit 1; }
if [ -x /usr/local/go/bin/go ] && [ "$(/usr/local/go/bin/go version | awk '{print $3}')" = "$ver" ]; then
  echo "[corral] $ver already installed"; exit 0
fi
file="$ver.linux-$garch.tar.gz"
# The checksum is looked up in the chosen release's own entry, never the
# first entry of the listing: with a pin the two differ.
sha=$(printf '%s' "$meta" | jq -r --arg v "$ver" --arg f "$file" '.[] | select(.version==$v) | .files[] | select(.filename==$f and .kind=="archive") | .sha256' | head -n1)
[ -n "$sha" ] || { echo "corral: no go.dev checksum for $file — is $ver a real release? check toolchain_versions" >&2; exit 1; }
if [ -n "${CORRAL_GO_VERSION:-}" ]; then
  echo "[corral] installing $ver (pinned by toolchain_versions)"
else
  echo "[corral] installing $ver (newest stable) — pin it with toolchain_versions = { go = \"${ver#go}\" }"
fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$file" "https://go.dev/dl/$file"
echo "$sha  $tmp/$file" | sha256sum -c -
rm -rf /usr/local/go && tar -C /usr/local -xzf "$tmp/$file"
cat >/etc/profile.d/go.sh <<'EOF'
export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
EOF
/usr/local/go/bin/go version
