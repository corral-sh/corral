#!/bin/bash
# Corral per-user provisioning (runs as the box user, once).
set -euo pipefail
mkdir -p "$HOME/.local/bin" "$HOME/.config"
# git: safe defaults; identity is forwarded at launch when git_identity = true.
git config --global init.defaultBranch main
git config --global pull.rebase false
git config --global --add safe.directory '*'
# Friendlier shell
grep -q 'CORRAL_PS1' "$HOME/.bashrc" 2>/dev/null || cat >>"$HOME/.bashrc" <<'EOF'
# CORRAL_PS1
if [ -n "$CORRAL" ]; then
  PS1='\[\033[1;33m\]🐎 \[\033[0;36m\]\u@box\[\033[0m\]:\[\033[1;34m\]\w\[\033[0m\]\$ '
fi
EOF
echo "[corral] user provisioning done for $(id -un)"
