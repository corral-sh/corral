#!/bin/bash
# Python from Ubuntu's archive plus pipx for isolated CLI tools.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get install -y --no-install-recommends python3 python3-pip python3-venv python3-dev pipx
python3 --version
