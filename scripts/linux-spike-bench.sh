#!/bin/bash
# SPIKE (#74), throwaway: lifecycle timings and mount speed for a Linux host
# (Lima + QEMU/KVM). Mount type from CORRAL_SPIKE_MOUNT (9p | virtiofs).
# Writes a table to $GITHUB_STEP_SUMMARY when set.
set -euo pipefail

BIN=$PWD/bin/corral
export CORRAL_PLAIN=1 CORRAL_HOME=/tmp/ch
SUMMARY=${GITHUB_STEP_SUMMARY:-/dev/null}
mk() { local d; d=$(mktemp -d /tmp/p-XXXX); d=$(cd "$d" && pwd -P)
  ( cd "$d" && git init -q && echo hi > README.md && git add -A && git -c user.email=s@example.com -c user.name=s commit -qm init )
  echo "$d"; }
P=$(mk); Q=$(mk)
git archive --format=tar HEAD > "$P/src.tar"   # a real source tree: this repo

row() { echo "| $1 | $2 |" | tee -a "$SUMMARY"; }
t() { # t <label> <cmd…>: run, record wall seconds
  local label=$1 s; shift; s=$(date +%s)
  "$@"
  row "$label" "$(( $(date +%s) - s )) s"
}

{ echo "### Linux host spike — mount: ${CORRAL_SPIKE_MOUNT:-9p}"; echo; echo "| step | result |"; echo "|---|---|"; } >> "$SUMMARY"

t "first up (golden build + clone)" "$BIN" -C "$P" up
t "run -- true on a running box"     "$BIN" -C "$P" run -- true
row "project mount" "$("$BIN" -C "$P" run -- bash -lc "findmnt -no FSTYPE,OPTIONS --target '$P' | cut -c1-60")"

# Same workload on the host mount and on the box's own disk (the baseline).
# The script lives in the project, which the box sees at the same path.
cat > "$P/bench.sh" <<'SH'
ms() { echo $(( ($(date +%s%N) - $1) / 1000000 )); }
case $1 in home) dir=$HOME/bench ;; *) dir=$1 ;; esac
mkdir -p "$dir/b" && cd "$dir/b"
s=$(date +%s%N); for i in $(seq 3000); do echo "$i" > "f$i"; done; w=$(ms "$s")
s=$(date +%s%N); cat f* > /dev/null; r=$(ms "$s")
s=$(date +%s%N); tar -xf "$2"; x=$(ms "$s")
s=$(date +%s%N); grep -rc func . > /dev/null || true; g=$(ms "$s")
s=$(date +%s%N); dd if=/dev/zero of=big bs=1M count=512 conv=fsync status=none; d=$(ms "$s")
cd .. && rm -rf b
echo "3000 small writes ${w}ms · read back ${r}ms · untar repo ${x}ms · grep -r ${g}ms · 512MiB write ${d}ms"
SH
row "host mount"     "$("$BIN" -C "$P" run -- bash "$P/bench.sh" "$P" "$P/src.tar")"
row "box disk (~)"   "$("$BIN" -C "$P" run -- bash "$P/bench.sh" home "$P/src.tar")"

"$BIN" -C "$P" stop
t "warm start (stopped → running)" "$BIN" -C "$P" up
t "second box (clone from golden)" "$BIN" -C "$Q" up
row "CORRAL_HOME on disk" "$(du -sh "$CORRAL_HOME" | cut -f1)"
t "delete both" "$BIN" delete --all --yes
