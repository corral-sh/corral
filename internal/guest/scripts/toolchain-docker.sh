#!/bin/bash
# Docker Engine INSIDE the box (Ubuntu's docker.io package). The host's Docker
# socket is never exposed — a container escape here lands in the VM, not on
# your Mac.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get install -y --no-install-recommends docker.io docker-compose-v2 docker-buildx
systemctl enable --now docker
# CORRAL_USER comes from the generated provision header; the box user has the
# host's uid, so neither uid 1000 nor SUDO_USER would find them. Fail loudly:
# a box whose user needs sudo for every docker command is a broken toolchain.
usermod -aG docker "$CORRAL_USER"
id -nG "$CORRAL_USER" | tr ' ' '\n' | grep -qx docker
docker --version
