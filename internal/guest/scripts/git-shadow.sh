#!/bin/bash
# Corral: shadow host-executed git metadata (runs as root, once, at box
# creation; the unit it installs runs at every boot and before every session).
#
# The project is a live mount of the host checkout. Two places in it are run by
# your *Mac*, not by the box, and appear in no diff a human reviews:
# .git/hooks/* and .git/config (core.hooksPath, core.sshCommand, core.fsmonitor,
# filter/diff drivers). An agent that writes them has code waiting for your next
# host-side git command. This bind-mounts a guest-local copy of config and an
# empty hooks directory over the mounted ones — for the top-level repository and
# every submodule under .git/modules — so in-box edits never reach the host copy.
set -euo pipefail

install -d -m 0755 /etc/corral /corral/shadow

cat >/opt/corral/bin/corral-git-shadow <<'SHADOW_EOF'
#!/bin/bash
# usage: corral-git-shadow   (root; reads PROJECT from /etc/corral/git-shadow.conf)
set -euo pipefail
# shellcheck disable=SC1091
. /etc/corral/git-shadow.conf
: "${PROJECT:?PROJECT missing from git-shadow.conf}"
SHADOW=/corral/shadow/git

# The mount is established by Lima's boot sequence, not by a systemd mount
# unit, so wait for it rather than order after it.
deadline=$((SECONDS + 180))
until mountpoint -q "$PROJECT"; do
  if ((SECONDS >= deadline)); then
    echo "corral-git-shadow: project mount $PROJECT not ready after 180s" >&2
    exit 1
  fi
  sleep 1
done

if [ ! -d "$PROJECT/.git" ]; then
  # Not a repository, or a worktree/submodule checkout whose gitdir is outside
  # the mount (then the host copy is not reachable from the box anyway).
  echo "corral-git-shadow: $PROJECT/.git is not a directory; nothing to shadow"
  exit 0
fi

# virtiofs presents host files as root; the shadow must belong to the box
# user so git inside the box can read config and install its own hooks.
# Lima creates the box user with the host's uid (often 501), so look at the
# home directory rather than assuming a uid range.
owner=""
for h in /home/*; do
  [ -d "$h" ] && owner=$(stat -c '%u:%g' "$h") && break
done
: "${owner:?no user home under /home}"

shadow_gitdir() {
  local g="$1" key d
  key=$(printf '%s' "$g" | sha256sum | cut -c1-16)
  d="$SHADOW/$key"
  install -d -m 0755 "$d" "$d/hooks"
  # config: fresh copy from the host at every (re)start — the host is the
  # source of truth. git rewrites config by rename(2), which cannot replace a
  # mountpoint, so `git config --local` / `git remote add` fail inside the box
  # with EBUSY: the copy is effectively read-only. Documented in SECURITY.md.
  if [ -f "$g/config" ]; then
    if mountpoint -q "$g/config"; then umount "$g/config"; fi
    cp -f "$g/config" "$d/config"
    chown "$owner" "$d/config"
    chmod 0644 "$d/config"
    mount --bind "$d/config" "$g/config"
  fi
  # hooks: an empty, guest-owned directory. Created on the mount if absent so
  # the agent cannot create the real one.
  [ -d "$g/hooks" ] || mkdir -p "$g/hooks"
  if ! mountpoint -q "$g/hooks"; then
    chown "$owner" "$d/hooks"
    mount --bind "$d/hooks" "$g/hooks"
  fi
  echo "corral-git-shadow: shadowed $g/{config,hooks}"
}

shadow_gitdir "$PROJECT/.git"
if [ -d "$PROJECT/.git/modules" ]; then
  while IFS= read -r cfg; do
    shadow_gitdir "$(dirname "$cfg")"
  done < <(find "$PROJECT/.git/modules" -type f -name config 2>/dev/null)
fi
SHADOW_EOF
chmod 0755 /opt/corral/bin/corral-git-shadow

cat >/etc/systemd/system/corral-git-shadow.service <<'EOF2'
[Unit]
# Restarted before every session; systemd's default 5-starts-per-10s limit would refuse the 6th.
StartLimitIntervalSec=0
Description=Corral: shadow host-executed git metadata in the project mount
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/corral/bin/corral-git-shadow

[Install]
WantedBy=multi-user.target
EOF2

# The launcher re-applies the shadow before every session (new submodules,
# a fresh clone at the same path) — this is the only command it needs root for.
cat >/etc/sudoers.d/corral-git-shadow <<'EOF3'
ALL ALL=(root) NOPASSWD: /usr/bin/systemctl restart corral-git-shadow.service
EOF3
chmod 0440 /etc/sudoers.d/corral-git-shadow

systemctl daemon-reload
systemctl enable corral-git-shadow.service >/dev/null 2>&1
systemctl restart corral-git-shadow.service || echo "[corral] git shadow deferred to next boot (mount not ready during provisioning)"
echo "[corral] git metadata shadow installed"
