#!/bin/bash
# OpenJDK 17 (LTS) from Ubuntu's signed archive. JAVA_HOME for every login shell.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if command -v javac >/dev/null 2>&1 && javac -version 2>&1 | grep -q "^javac 17"; then
  echo "[corral] java already installed: $(javac -version 2>&1)"
else
  echo "[corral] installing OpenJDK 17"
  apt-get install -y --no-install-recommends openjdk-17-jdk-headless
fi
jhome=$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")
cat >/etc/profile.d/corral-java.sh <<EOF2
export JAVA_HOME="$jhome"
EOF2
chmod 0644 /etc/profile.d/corral-java.sh
java -version 2>&1 | head -1
