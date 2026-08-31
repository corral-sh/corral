#!/bin/bash
# Corral: network = "offline" (runs as root, once, at box creation; the unit
# it installs runs at every boot and before every session).
#
# Lima has no network-less VM — host→guest SSH rides the user-mode network — so
# "offline" is enforced inside the guest: nftables rejects every outbound packet
# except to the Lima gateway subnet (the Mac at host.lima.internal, DNS), and the
# box user's blanket sudo grant is removed so the agent cannot undo it. The
# scoped sudoers entries for `systemctl restart corral-*.service` stay: they
# are the launcher's only root need and each unit is idempotent.
#
# Lockdown waits for the end-of-provisioning marker so toolchains and agents can
# still download during the first boot (and on `rebuild`, which recreates the VM).
set -euo pipefail

install -d -m 0755 /etc/corral

cat >/etc/corral/offline.nft <<'NFT_EOF'
table inet corral_offline {
  chain output {
    type filter hook output priority 0; policy drop;
    oifname "lo" accept
    ct state established,related accept
    ip daddr 192.168.5.0/24 accept
    reject
  }
  chain forward {
    type filter hook forward priority 0; policy drop;
  }
}
NFT_EOF

cat >/opt/corral/bin/corral-offline <<'OFFLINE_EOF'
#!/bin/bash
# usage: corral-offline   (root; reads /etc/corral/offline.conf)
set -euo pipefail
# shellcheck disable=SC1091
. /etc/corral/offline.conf

# Wait for this boot's provisioning to finish (the last user-mode step writes
# the current boot_id), up to 15 minutes; a rebuild that hangs stays online
# only until then. A stale marker from a previous boot or a golden clone does
# not count.
boot_id=$(cat /proc/sys/kernel/random/boot_id)
deadline=$((SECONDS + 900))
until grep -qsx "$boot_id" /home/*/.corral/provisioned; do
  if ((SECONDS >= deadline)); then
    echo "corral-offline: provisioning marker not seen after 900s; locking down anyway" >&2
    break
  fi
  sleep 3
done

nft delete table inet corral_offline 2>/dev/null || true
nft -f /etc/corral/offline.nft

# Remove the blanket sudo grant; keep the scoped corral-* restart entries.
rm -f /etc/sudoers.d/90-cloud-init-users
for u in $(getent group sudo | cut -d: -f4 | tr ',' ' ') $(getent group admin | cut -d: -f4 | tr ',' ' '); do
  gpasswd -d "$u" sudo >/dev/null 2>&1 || true
  gpasswd -d "$u" admin >/dev/null 2>&1 || true
done

echo "corral-offline: egress rejected (except 192.168.5.0/24); sudo grant removed"
OFFLINE_EOF
chmod 0755 /opt/corral/bin/corral-offline

cat >/etc/systemd/system/corral-offline.service <<'EOF2'
[Unit]
# Restarted before every session; systemd's default 5-starts-per-10s limit would refuse the 6th.
StartLimitIntervalSec=0
# Ordered after provisioning, not inside multi-user.target: a oneshot wanted by
# multi-user.target blocks that target until it exits, cloud-final.service (which
# runs the provision scripts that write the marker this script waits for) is
# ordered after multi-user.target, and the boot deadlocks for the 900 s fallback.
# cloud-init.target is ordered after both, so the marker is already there.
After=network.target cloud-final.service
Description=Corral: offline mode (reject egress, drop sudo)

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/opt/corral/bin/corral-offline

[Install]
WantedBy=cloud-init.target
EOF2

cat >/etc/sudoers.d/corral-offline <<'EOF3'
ALL ALL=(root) NOPASSWD: /usr/bin/systemctl restart corral-offline.service
EOF3
chmod 0440 /etc/sudoers.d/corral-offline

systemctl daemon-reload
systemctl disable corral-offline.service >/dev/null 2>&1 || true
systemctl enable corral-offline.service >/dev/null 2>&1
# Start without waiting: on first boot this blocks until provisioning is done.
systemctl start --no-block corral-offline.service
echo "[corral] offline mode installed (applies once provisioning finishes)"
