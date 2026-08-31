#!/bin/bash
# Corral: hide listed project paths inside the box (runs as root, once, at
# box creation; the unit it installs runs at every boot and before every session).
#
# virtiofs cannot exclude paths from a mount, so `hide = [".env", "secrets/"]`
# is applied guest-side: an empty, box-user-owned tmpfs is bind-mounted over each
# listed directory and an empty file over each listed file. The agent sees an
# empty .env and can write to it; the writes stay on the VM. This is hygiene,
# not a boundary — root in the box can unmount it.
set -euo pipefail

install -d -m 0755 /etc/corral /corral/shadow

cat >/opt/corral/bin/corral-hide <<'HIDE_EOF'
#!/bin/bash
# usage: corral-hide   (root; reads PROJECT and HIDE from /etc/corral/hide.conf)
set -euo pipefail
# shellcheck disable=SC1091
. /etc/corral/hide.conf
: "${PROJECT:?PROJECT missing from hide.conf}"
SHADOW=/corral/shadow/hide

deadline=$((SECONDS + 180))
until mountpoint -q "$PROJECT"; do
  if ((SECONDS >= deadline)); then
    echo "corral-hide: project mount $PROJECT not ready after 180s" >&2
    exit 1
  fi
  sleep 1
done

owner=""
for h in /home/*; do
  [ -d "$h" ] && owner=$(stat -c '%u:%g' "$h") && break
done
: "${owner:?no user home under /home}"

install -d -m 0755 "$SHADOW"
while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  p="$PROJECT/${rel%/}"
  if [ -d "$p" ]; then
    if ! mountpoint -q "$p"; then
      mount -t tmpfs -o "size=16m,mode=0755,uid=${owner%:*},gid=${owner#*:}" tmpfs "$p"
    fi
    echo "corral-hide: hidden directory $rel"
  elif [ -e "$p" ]; then
    key=$(printf '%s' "$p" | sha256sum | cut -c1-16)
    : >"$SHADOW/$key"
    chown "$owner" "$SHADOW/$key"
    chmod 0600 "$SHADOW/$key"
    if ! mountpoint -q "$p"; then
      mount --bind "$SHADOW/$key" "$p"
    fi
    echo "corral-hide: hidden file $rel"
  else
    echo "corral-hide: $rel does not exist in the project; nothing to hide"
  fi
done <<<"$HIDE"
HIDE_EOF
chmod 0755 /opt/corral/bin/corral-hide

cat >/etc/systemd/system/corral-hide.service <<'EOF2'
[Unit]
# Restarted before every session; systemd's default 5-starts-per-10s limit would refuse the 6th.
StartLimitIntervalSec=0
Description=Corral: hide listed project paths in the project mount
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/corral/bin/corral-hide

[Install]
WantedBy=multi-user.target
EOF2

cat >/etc/sudoers.d/corral-hide <<'EOF3'
ALL ALL=(root) NOPASSWD: /usr/bin/systemctl restart corral-hide.service
EOF3
chmod 0440 /etc/sudoers.d/corral-hide

systemctl daemon-reload
systemctl enable corral-hide.service >/dev/null 2>&1
systemctl restart corral-hide.service || echo "[corral] hide deferred to next boot (mount not ready during provisioning)"
echo "[corral] hide installed"
