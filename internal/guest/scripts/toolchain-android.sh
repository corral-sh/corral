#!/bin/bash
# Android SDK: command-line tools pinned by SHA-256 from dl.google.com, then
# platform-tools / build-tools / platform via sdkmanager (Google's signed repo).
# Includes the JDK (toolchain "java") — sdkmanager and Gradle need it.
#
# On an arm64 box the SDK's own build tools (aapt2, the NDK) are amd64-only
# binaries. Rosetta runs them, but it provides no x86_64 *userland*: the
# dynamic loader and libc must come from Ubuntu's amd64 archive — arm64's
# ports mirror carries none — so this script enables amd64 multiarch for the
# handful of libraries aapt2 needs. Without it the failure reads as "Rosetta is
# broken" (a tester lost a day to it).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

CLT_BUILD="${CORRAL_ANDROID_CLT_BUILD:-15859902}"
CLT_SHA256="${CORRAL_ANDROID_CLT_SHA256:-4e4c464f145a7512b57d088ac6c278c03c9eea610886b35a5e0804e74eedf583}"
PLATFORM="${CORRAL_ANDROID_PLATFORM:-android-35}"
BUILD_TOOLS="${CORRAL_ANDROID_BUILD_TOOLS:-35.0.0}"
SDK=/opt/android-sdk

# --- JDK (same as toolchain-java) ---------------------------------------------
if ! command -v javac >/dev/null 2>&1; then
  echo "[corral] installing OpenJDK 17 (required by the Android SDK)"
  apt-get install -y --no-install-recommends openjdk-17-jdk-headless
fi
jhome=$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")
printf 'export JAVA_HOME="%s"\n' "$jhome" >/etc/profile.d/corral-java.sh
chmod 0644 /etc/profile.d/corral-java.sh

# --- amd64 userland for the SDK's x86_64 tools (arm64 boxes only) ------------
if [ "$(uname -m)" = "aarch64" ]; then
  if [ ! -e /proc/sys/fs/binfmt_misc/rosetta ]; then
    echo "corral: the Android build tools (aapt2) are amd64 binaries; set rosetta = true in .corral.toml and rebuild" >&2
    exit 1
  fi
  if ! dpkg --print-foreign-architectures | grep -qx amd64; then
    echo "[corral] enabling amd64 multiarch (loader + libc for aapt2 under Rosetta)"
    # Pin the existing (ports) sources to arm64, then add the amd64 archive.
    for f in /etc/apt/sources.list.d/*.sources; do
      [ -f "$f" ] || continue
      grep -q '^Architectures:' "$f" || sed -i 's/^Types: deb$/Types: deb\nArchitectures: arm64/' "$f"
    done
    # shellcheck disable=SC1091
    . /etc/os-release
    cat >/etc/apt/sources.list.d/corral-amd64.sources <<EOF2
Types: deb
Architectures: amd64
URIs: http://archive.ubuntu.com/ubuntu
Suites: ${VERSION_CODENAME} ${VERSION_CODENAME}-updates ${VERSION_CODENAME}-security
Components: main universe
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF2
    dpkg --add-architecture amd64
    apt-get update
  fi
  apt-get install -y --no-install-recommends libc6:amd64 libstdc++6:amd64 zlib1g:amd64 libncurses6:amd64
fi

# --- command-line tools, checksum-verified ------------------------------------
if [ ! -x "$SDK/cmdline-tools/latest/bin/sdkmanager" ]; then
  echo "[corral] installing Android command-line tools $CLT_BUILD"
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  zip="commandlinetools-linux-${CLT_BUILD}_latest.zip"
  curl -fsSL -o "$tmp/$zip" "https://dl.google.com/android/repository/$zip"
  echo "$CLT_SHA256  $tmp/$zip" | sha256sum -c -
  install -d -m 0755 "$SDK/cmdline-tools"
  unzip -q "$tmp/$zip" -d "$tmp"
  rm -rf "$SDK/cmdline-tools/latest"
  mv "$tmp/cmdline-tools" "$SDK/cmdline-tools/latest"
else
  echo "[corral] Android command-line tools already installed"
fi

export ANDROID_HOME="$SDK" ANDROID_SDK_ROOT="$SDK" JAVA_HOME="$jhome"
sdkmanager="$SDK/cmdline-tools/latest/bin/sdkmanager"
yes | "$sdkmanager" --licenses >/dev/null 2>&1 || true
"$sdkmanager" --install "platform-tools" "platforms;$PLATFORM" "build-tools;$BUILD_TOOLS" >/dev/null

# The box user owns the SDK so Gradle can add components during a build.
chown -R "$CORRAL_USER" "$SDK"

cat >/etc/profile.d/corral-android.sh <<EOF2
export ANDROID_HOME="$SDK"
export ANDROID_SDK_ROOT="$SDK"
export PATH="\$PATH:$SDK/cmdline-tools/latest/bin:$SDK/platform-tools:$SDK/build-tools/$BUILD_TOOLS"
EOF2
chmod 0644 /etc/profile.d/corral-android.sh
"$SDK/platform-tools/adb" --version | head -1
echo "[corral] android sdk: $PLATFORM, build-tools $BUILD_TOOLS"
