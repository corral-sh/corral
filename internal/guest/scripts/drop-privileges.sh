#!/bin/bash
# Corral: drop the box user's root-equivalent privileges (runs as root, at
# every boot, for network = "broker" and "offline").
#
# Rendered BEFORE the project's own provision scripts, so repository code never
# runs with sudo in a box whose purpose is to deny the agent root: a project
# script with sudo — or with the docker group, whose daemon mounts / into any
# container — could pre-empt every in-guest control the lockdown then applies.
# It installs /opt/corral/bin/corral-drop-privileges, which the broker and
# offline lockdown units call again before every session.
set -euo pipefail

install -d -m 0755 /opt/corral/bin
cat >/opt/corral/bin/corral-drop-privileges <<'DROP_EOF'
#!/bin/bash
# usage: corral-drop-privileges   (root)
# Removes the blanket sudo grant and every member of the root-equivalent groups.
# The scoped `systemctl restart corral-*.service` sudoers entries stay: they
# only re-apply the controls.
set -euo pipefail
# sudo/admin: sudo. docker: the daemon runs containers as root with any host
# path mounted. lxd/incus-admin: same, for system containers. disk: raw block
# devices, i.e. the root filesystem.
groups="sudo admin docker lxd incus-admin disk"
rm -f /etc/sudoers.d/90-cloud-init-users
for g in $groups; do
  for u in $({ getent group "$g" || true; } | cut -d: -f4 | tr ',' ' '); do
    gpasswd -d "$u" "$g" >/dev/null 2>&1 || true
  done
done
# A process that started while the user was still in a group keeps that gid
# (the user's systemd manager, an early login), so the docker socket itself is
# made root-only too; the socket.d drop-in keeps it so across daemon restarts.
if [ -S /run/docker.sock ]; then
  chown root:root /run/docker.sock
  chmod 0660 /run/docker.sock
fi
# Fail closed: a member left behind means the lockdown did not happen.
for g in $groups; do
  members=$(getent group "$g" | cut -d: -f4) || true   # getent exits 2 for a group this image lacks
  if [ -n "$members" ]; then
    echo "corral-drop-privileges: group $g still has members ($members)" >&2
    exit 1
  fi
done
DROP_EOF
chmod 0755 /opt/corral/bin/corral-drop-privileges
if systemctl cat docker.socket >/dev/null 2>&1; then
  install -d -m 0755 /etc/systemd/system/docker.socket.d
  printf '[Socket]\nSocketGroup=root\n' >/etc/systemd/system/docker.socket.d/corral.conf
  systemctl daemon-reload
fi
/opt/corral/bin/corral-drop-privileges
echo "[corral] root-equivalent privileges removed from the box user"
