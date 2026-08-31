#!/bin/bash
# Corral: keep listed project directories on the box disk (runs as root; the
# unit it installs runs at every boot and before every session).
#
# virtiofs is slow on install-heavy trees (a tester measured yarn install at
# 1453 s on the mount vs 50 s on the box disk — 87 435 files). `box_dirs =
# ["node_modules", "build"]` bind-mounts a box-disk directory (/corral/local/<rel>,
# owned by the box user) over each listed project directory, so installs run at
# disk speed while the rest of the checkout stays live on the Mac. The Mac sees
# the directory empty; the contents live in the VM and survive reboots (not a
# rebuild). Like `hide`, this is a convenience, not a boundary.
set -euo pipefail

install -d -m 0755 /etc/corral /corral/local

cat >/opt/corral/bin/corraldirs <<'BOXDIRS_EOF'
#!/bin/bash
# usage: corraldirs   (root; reads PROJECT and DIRS from /etc/corral/boxdirs.conf)
set -euo pipefail
# shellcheck disable=SC1091
. /etc/corral/boxdirs.conf
: "${PROJECT:?PROJECT missing from boxdirs.conf}"
LOCAL=/corral/local

deadline=$((SECONDS + 180))
until mountpoint -q "$PROJECT"; do
  if ((SECONDS >= deadline)); then
    echo "corraldirs: project mount $PROJECT not ready after 180s" >&2
    exit 1
  fi
  sleep 1
done

owner=""
for h in /home/*; do
  [ -d "$h" ] && owner=$(stat -c '%u:%g' "$h") && break
done
: "${owner:?no user home under /home}"

while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  rel="${rel%/}"
  p="$PROJECT/$rel"
  if [ ! -d "$p" ]; then
    # Create the mount point on the checkout (the same thing `npm ci` would do);
    # a read-only mount without the directory cannot be served.
    if ! mkdir -p "$p" 2>/dev/null; then
      echo "corraldirs: $rel does not exist and the project is read-only; skipped" >&2
      continue
    fi
  fi
  if mountpoint -q "$p"; then
    echo "corraldirs: $rel already on the box disk"
    continue
  fi
  local_dir="$LOCAL/$rel"
  mkdir -p "$local_dir"
  chown "$owner" "$local_dir"
  chmod 0755 "$local_dir"
  mount --bind "$local_dir" "$p"
  echo "corraldirs: $rel is on the box disk ($local_dir)"
done <<<"$DIRS"
BOXDIRS_EOF
chmod 0755 /opt/corral/bin/corraldirs

cat >/etc/systemd/system/corral-boxdirs.service <<'EOF2'
[Unit]
# Restarted before every session; systemd's default 5-starts-per-10s limit would refuse the 6th.
StartLimitIntervalSec=0
Description=Corral: listed project directories live on the box disk (bind mounts over the project mount)
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/corral/bin/corraldirs

[Install]
WantedBy=multi-user.target
EOF2

cat >/etc/sudoers.d/corral-boxdirs <<'EOF3'
ALL ALL=(root) NOPASSWD: /usr/bin/systemctl restart corral-boxdirs.service
EOF3
chmod 0440 /etc/sudoers.d/corral-boxdirs

systemctl daemon-reload
systemctl enable corral-boxdirs.service >/dev/null 2>&1
systemctl restart corral-boxdirs.service || echo "[corral] box_dirs deferred to next boot (mount not ready during provisioning)"
echo "[corral] box_dirs installed"
