#!/bin/bash
# End-to-end test on a Mac: the Lima template is otherwise only exercised
# by hand. Creates throwaway boxes under a temporary CORRAL_HOME, asserts the
# guest facts the security model rests on, and deletes everything. Run as a
# release-checklist gate: `make e2e` (~4 min: the fresh home builds its own
# golden image; set CORRAL_E2E_HOME to reuse one between runs).
set -uo pipefail

BIN=${BIN:-$PWD/bin/corral}
export CORRAL_PLAIN=1
# Short path on purpose: LIMA_HOME feeds a UNIX socket path capped at 104 bytes.
export CORRAL_HOME=${CORRAL_E2E_HOME:-/tmp/corral-e2e-$$}
KEEP_HOME=${CORRAL_E2E_HOME:-}
WORK=$(mktemp -d /tmp/corral-e2e-w-XXXX); WORK=$(cd "$WORK" && pwd -P)   # resolved: the guest sees the real path
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
CHECK_TIMEOUT=${CHECK_TIMEOUT:-300}   # a hung session must fail the run, not block it
check(){ # check <description> <shell snippet>; passes when the snippet exits 0
  local d=$1 snippet=$2 out
  if out=$(perl -e 'alarm shift; exec @ARGV' "$CHECK_TIMEOUT" bash -c "$snippet" 2>&1); then ok "$d"; else bad "$d" "$(printf '%s' "$out" | tail -3)"; fi
}
guest() { "$BIN" -C "$1" run -- bash -lc "$2"; }   # guest <project> <shell inside the box>
export BIN; export -f guest

cleanup() {
  echo "── cleanup"
  "$BIN" delete --all --yes >/dev/null 2>&1 || true
  "$BIN" golden prune --yes >/dev/null 2>&1 || true
  pkill -f "corral broker --box" 2>/dev/null || true
  [ -z "$KEEP_HOME" ] && rm -rf "$CORRAL_HOME"
  rm -rf "$WORK"
}
trap cleanup EXIT

[ -x "$BIN" ] || { echo "build first: make build" >&2; exit 2; }
echo "── e2e: $("$BIN" version | head -1)  home=$CORRAL_HOME"

# ── Box A: mount mode with hide, git-metadata shadow, broker network, isolated agent state
A="$WORK/alpha"; export A
( mkdir -p "$A/secrets" && cd "$A" && git init -q && echo 'TOKEN=supersecret' > .env && echo x > secrets/k && echo '# hello' > README.md
  printf 'hide = [".env", "secrets/"]\nbox_dirs = ["node_modules"]\nnetwork = "broker"\nagent_state = "isolated"\nsnapshot = "auto"\n' > .corral.toml
  git add -A && git -c user.email=e2e@example.com -c user.name=e2e commit -qm init )
echo "── box A (mount · hide · broker · isolated): first start builds the golden image"
check "box A starts"                                    '"$BIN" -C "$A" up'
check "no auto snapshot yet (box was just created, not started from stopped)" '! "$BIN" -C "$A" snapshot list | grep -q auto-'
check "only the project is mounted from the Mac (virtiofs)" 'm=$(guest "$A" "mount -t virtiofs" | awk "{print \$3}"); echo "$m"; [ "$(echo "$m" | grep -c .)" = 1 ] && [ "$m" = "$A" ]'
check "hidden .env reads empty inside the box"          '[ "$(guest "$A" "wc -c < .env")" = 0 ]'
check "hidden secrets/ is empty inside the box"        '[ -z "$(guest "$A" "ls -A secrets")" ]'
check ".env still intact on the Mac"                    'grep -q supersecret "$A/.env"'
check "box_dirs: node_modules is a bind mount from the box disk" 'guest "$A" "findmnt -no SOURCE,TARGET node_modules" | grep -q "corral/local"'
check "box_dirs: a file written there in the box is not on the Mac" 'guest "$A" "touch node_modules/from-box && ls node_modules" | grep -q from-box && [ ! -e "$A/node_modules/from-box" ]'
check "sshd AcceptEnv is the Corral allow-list"      'guest "$A" "cat /etc/ssh/sshd_config.d/20-corral.conf" | grep -q "AcceptEnv CORRAL_FWD_\* CORRAL_GIT_TOKEN_\* CORRAL_GIT_USER"'
check ".git/hooks is shadowed (empty) in the box"       '[ -z "$(guest "$A" "ls -A .git/hooks")" ]'
check ".git/config cannot be rewritten from the box"    '! guest "$A" "git config --local e2e.x 1" >/dev/null 2>&1'
check "agent state is isolated (no host mount at /corral/agents)" '! guest "$A" mount | grep -q /corral/agents'
check "broker: HTTPS_PROXY points at the Mac gateway"   'guest "$A" "echo \$HTTPS_PROXY" | grep -q "^http://192.168.5.2:"'
check "broker: allowed host reachable through the proxy" 'c=$(guest "$A" "curl -s -o /dev/null -w %{http_code} --max-time 20 https://api.anthropic.com/"); echo "$c"; [ "$c" != 000 ]'
check "broker: denied host refused by the proxy (403 on CONNECT)" 'guest "$A" "curl -s -o /dev/null --max-time 20 https://example.com/; echo exit=\$?" | grep -q exit=56'
check "broker: direct connection bypassing the proxy is rejected" 'guest "$A" "curl -s -o /dev/null --noproxy \"*\" --max-time 8 https://1.1.1.1/; echo exit=\$?" | grep -qE "exit=(7|28)"'
check "broker: DNS is not available in the guest"       '! guest "$A" "getent hosts example.com" >/dev/null 2>&1'
check "sudo removed from the box user"                  '! guest "$A" "sudo -n true" >/dev/null 2>&1'
check "denial recorded: corral egress lists example.com" '"$BIN" -C "$A" egress | grep -q example.com:443'
check "stop box A"                                      '"$BIN" -C "$A" stop'
# Scoped to this run's box: another box of the developer's may legitimately have a broker running.
check "broker process gone after stop"                  '! pgrep -f "corral broker --box alpha-" >/dev/null'
check "snapshot create (stopped)"                       '"$BIN" -C "$A" snapshot create e2e'
check "snapshot listed"                                 '"$BIN" -C "$A" snapshot list | grep -q e2e'
check "session start from stopped takes an auto snapshot" 'guest "$A" true >/dev/null 2>&1; "$BIN" -C "$A" snapshot list | grep -q auto-'
check "undo restores the newest auto snapshot"          '"$BIN" -C "$A" undo'
check "box boots to a ready session after undo"    'guest "$A" "echo up-after-undo" | grep -q up-after-undo'
check "broker lockdown active again after the restore"   'guest "$A" "systemctl is-active corral-broker.service" | grep -qx active'

# ── Box B: offline
B="$WORK/beta"; export B
( mkdir -p "$B" && cd "$B" && git init -q && printf 'network = "offline"\n' > .corral.toml )
echo "── box B (offline): clone of the golden"
check "box B starts"                                    '"$BIN" -C "$B" up'
check "offline: egress rejected"                        'guest "$B" "curl -s -o /dev/null --max-time 8 https://1.1.1.1/; echo exit=\$?" | grep -qE "exit=(7|28)"'
check "offline: the Mac (gateway) is still reachable"   'guest "$B" "getent hosts host.lima.internal" | grep -q 192.168.5.2'
check "offline: sudo removed"                           '! guest "$B" "sudo -n true" >/dev/null 2>&1'
check "info shows no config drift"                      '! "$BIN" -C "$B" info | grep -q "configuration changed"'

echo "── $PASS passed, $FAIL failed"
[ "$FAIL" = 0 ]
