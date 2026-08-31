#!/bin/bash
# Flutter stable, pinned to a release commit. Flutter's Linux tarball is x64
# only; the supported arm64-Linux install is a clone of the release tag, so the
# clone is verified against the pinned commit hash (git's SHA is the checksum)
# before anything from it runs. The SDK belongs to the box user: flutter
# refuses to run from a tree owned by someone else.
set -euo pipefail

# Default release, commit pinned. `toolchain_versions = { flutter = "X[@commit]" }`
# exports CORRAL_FLUTTER_VERSION (and CORRAL_FLUTTER_COMMIT, possibly empty): a
# pinned commit is verified exactly like the default; without one the tag is
# fetched over TLS from github.com and the resolved commit is recorded so the
# project can pin it next time.
if [ -z "${CORRAL_FLUTTER_VERSION:-}" ]; then
  FLUTTER_VERSION=3.47.1
  FLUTTER_COMMIT=6655482ec06e547f90abf8ae7590466f4415978d
else
  FLUTTER_VERSION="$CORRAL_FLUTTER_VERSION"
  FLUTTER_COMMIT="${CORRAL_FLUTTER_COMMIT:-}"
fi
DEST=/opt/flutter
installed=$(git -C "$DEST" rev-parse HEAD 2>/dev/null || true)
installed_tag=$(git -C "$DEST" describe --tags --exact-match 2>/dev/null || true)

if [ -x "$DEST/bin/flutter" ] && { [ -n "$FLUTTER_COMMIT" ] && [ "$installed" = "$FLUTTER_COMMIT" ] || [ -z "$FLUTTER_COMMIT" ] && [ "$installed_tag" = "$FLUTTER_VERSION" ]; }; then
  echo "[corral] flutter $FLUTTER_VERSION already installed"
else
  echo "[corral] installing flutter $FLUTTER_VERSION ${FLUTTER_COMMIT:-"(tag only, no commit pinned)"}"
  rm -rf "$DEST"
  git clone --quiet --depth 1 --branch "$FLUTTER_VERSION" https://github.com/flutter/flutter.git "$DEST"
  head=$(git -C "$DEST" rev-parse HEAD)
  if [ -n "$FLUTTER_COMMIT" ] && [ "$head" != "$FLUTTER_COMMIT" ]; then
    echo "corral: flutter tag $FLUTTER_VERSION resolved to $head, expected $FLUTTER_COMMIT — refusing" >&2
    rm -rf "$DEST"
    exit 1
  fi
  if [ -z "$FLUTTER_COMMIT" ]; then
    echo "[corral] flutter $FLUTTER_VERSION is commit $head — pin it with toolchain_versions = { flutter = \"$FLUTTER_VERSION@$head\" }"
  fi
fi
mkdir -p /etc/corral
echo "$FLUTTER_VERSION $(git -C "$DEST" rev-parse HEAD)" >/etc/corral/flutter.version
chown -R "$CORRAL_USER" "$DEST"

cat >/etc/profile.d/corral-flutter.sh <<EOF2
export FLUTTER_ROOT="$DEST"
export PATH="\$PATH:$DEST/bin:\$HOME/.pub-cache/bin"
EOF2
chmod 0644 /etc/profile.d/corral-flutter.sh

# Dart SDK + engine artifacts for this host, as the box user (the tool writes
# into its own tree). Analytics off before the first command that could send.
runuser -u "$CORRAL_USER" -- env HOME="$CORRAL_HOME" PATH="$DEST/bin:$PATH" bash -c '
  set -e
  flutter config --no-analytics >/dev/null 2>&1 || true
  flutter precache --no-ios --no-web --no-macos --no-windows --no-linux >/dev/null
  if [ -d /opt/android-sdk ]; then flutter config --android-sdk /opt/android-sdk >/dev/null; yes | flutter doctor --android-licenses >/dev/null 2>&1 || true; fi
  flutter --version | head -2
'
