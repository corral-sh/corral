#!/bin/bash
# Corral base provisioning (runs as root, once, at box creation).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

echo "[corral] base provisioning started"

# ---------------------------------------------------------------------------
# Packages: everything here comes from Ubuntu's signed archive.
# ---------------------------------------------------------------------------
apt-get update
apt-get install -y --no-install-recommends \
  bash ca-certificates curl wget git git-lfs sudo openssh-client gnupg \
  build-essential make cmake pkg-config \
  jq ripgrep fd-find fzf tree htop less vim nano unzip zip xz-utils tzdata \
  locales bash-completion procps net-tools iproute2 dnsutils socat \
  libssl-dev

ln -sf /usr/bin/fdfind /usr/local/bin/fd
locale-gen en_US.UTF-8 >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# Corral runtime directory
# ---------------------------------------------------------------------------
install -d -m 0755 /opt/corral /opt/corral/bin /corral /corral/agents /corral/runtime
# Repository provision scripts record a non-zero exit here (world-writable: they
# may run as the box user); base.sh is the first script of every boot, so the
# record is per boot. corral reads it after start and refuses to continue.
install -d -m 1777 /corral/runtime/provision
rm -f /corral/runtime/provision/*.failed

# Launcher: receives forwarded env as CORRAL_FWD_<NAME> over SSH (see sshd
# AcceptEnv below), exports it under its real name, cds into the project and
# execs the requested command through a login shell so /etc/profile.d applies.
cat >/opt/corral/bin/corral-launch <<'EOF'
#!/bin/bash
# usage: corral-launch <workdir> <cmd> [args...]
wd="$1"; shift
for v in $(compgen -e CORRAL_FWD_ || true); do
  export "${v#CORRAL_FWD_}=${!v}"
  unset "$v"
done
# Guest-side shadows (protect_git_metadata → git-shadow, hide → hide, box_dirs → boxdirs) must be
# in place before the agent runs. Re-applied per session (new submodules, a
# .env created since); if one cannot be, the session does not start — a
# silently missing control is what a hostile checkout would hope for.
for unit in git-shadow boxdirs hide offline broker; do
  [ -f "/etc/corral/$unit.conf" ] || continue
  if ! sudo -n /usr/bin/systemctl restart "corral-$unit.service" 2>/dev/null \
     || ! systemctl is-active --quiet "corral-$unit.service"; then
    echo "corral: could not apply corral-$unit inside the box; refusing to start." >&2
    echo "  inspect: corral shell, then 'journalctl -u corral-$unit'; opt out in config, then corral rebuild" >&2
    exit 1
  fi
done
# source = "clone": nothing is mounted; clone the repository here, now, with
# the credential the session carries (CORRAL_GIT_TOKEN_* via the helper). Refuse
# to start without it rather than drop the agent into an empty directory.
if [ "${CORRAL_SOURCE:-mount}" = "clone" ] && [ ! -d "$wd/.git" ]; then
  : "${CORRAL_CLONE_URL:?clone mode without CORRAL_CLONE_URL}"
  echo "corral: cloning ${CORRAL_CLONE_URL}${CORRAL_CLONE_REF:+@$CORRAL_CLONE_REF} into the box" >&2
  mkdir -p "$wd"
  if ! { [ -z "${CORRAL_CLONE_REF:-}" ] && git clone --quiet "$CORRAL_CLONE_URL" "$wd"; } \
     && ! git clone --quiet --branch "$CORRAL_CLONE_REF" "$CORRAL_CLONE_URL" "$wd" 2>/dev/null \
     && ! { git clone --quiet "$CORRAL_CLONE_URL" "$wd" && git -C "$wd" checkout --quiet "$CORRAL_CLONE_REF"; }; then
    echo "corral: clone failed — check git_tokens for this host and that ref '${CORRAL_CLONE_REF:-}' exists; refusing to start." >&2
    exit 1
  fi
fi
if [ -d "$wd" ]; then cd "$wd"; else cd "$HOME"; fi
exec bash -lc 'exec "$@"' -- "$@"
EOF
chmod 0755 /opt/corral/bin/corral-launch

# Git credential helper: tokens arrive as CORRAL_GIT_TOKEN_<host with . and - as _>
# and are only ever offered for that exact host over https. The username is
# CORRAL_GIT_USER_<host> when the config set one (GitLab deploy tokens need
# gitlab+deploy-token-<id>), otherwise oauth2 — right for personal access tokens.
cat >/opt/corral/bin/git-credential-corral <<'EOF'
#!/bin/bash
[ "${1:-}" = "get" ] || exit 0
protocol=""; host=""
while IFS= read -r line; do
  [ -z "$line" ] && break
  case "$line" in
    protocol=*) protocol=${line#protocol=} ;;
    host=*) host=${line#host=} ;;
  esac
done
[ "$protocol" = "https" ] || exit 0
hkey="$(printf '%s' "$host" | tr '.:-' '___' | tr '[:lower:]' '[:upper:]')"
key="CORRAL_GIT_TOKEN_$hkey"
token="${!key:-}"
[ -n "$token" ] || exit 0
ukey="CORRAL_GIT_USER_$hkey"
user="${!ukey:-oauth2}"
printf 'username=%s\n' "$user"
printf 'password=%s\n' "$token"
EOF
chmod 0755 /opt/corral/bin/git-credential-corral
git config --system credential.helper /opt/corral/bin/git-credential-corral

# ---------------------------------------------------------------------------
# sshd: accept Corral forwarded environment (values travel inside the
# encrypted SSH channel, never on a command line).
# ---------------------------------------------------------------------------
install -d -m 0755 /etc/ssh/sshd_config.d
cat >/etc/ssh/sshd_config.d/20-corral.conf <<'EOF'
AcceptEnv CORRAL_FWD_* CORRAL_GIT_TOKEN_* CORRAL_GIT_USER_* TERM COLORTERM LANG LC_*
EOF
systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true

# ---------------------------------------------------------------------------
# Identity file so tools (and agents) can tell they are inside a box.
# ---------------------------------------------------------------------------
cat >/etc/corral-release <<EOF
CORRAL=1
CORRAL_GUEST_OS=$(. /etc/os-release && echo "$PRETTY_NAME")
CORRAL_PROVISIONED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF

# MOTD
cat >/etc/update-motd.d/01-corral <<'EOF'
#!/bin/sh
printf '\n  \033[1;33m🐎 Corral\033[0m  — you are inside an isolated VM. Your Mac home directory is not here.\n'
printf '  Project: %s\n' "${CORRAL_PROJECT:-see /corral/runtime/context.json}"
if [ -f /etc/corral/broker.conf ]; then
  printf '  \033[1;33mNetwork: broker\033[0m — egress only to the allow-list on your Mac (HTTP(S)_PROXY set), sudo removed. Blocked? `corral egress` on the Mac.\n'
fi
if [ -f /etc/corral/offline.conf ]; then
  printf '  \033[1;31mNetwork: offline\033[0m — no internet from this box, sudo removed. Installs need `corral rebuild`.\n'
fi
printf '\n'
EOF
chmod 0755 /etc/update-motd.d/01-corral
chmod -x /etc/update-motd.d/10-help-text /etc/update-motd.d/50-motd-news 2>/dev/null || true

echo "[corral] base provisioning done"
