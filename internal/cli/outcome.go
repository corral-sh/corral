package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/corral-sh/corral/internal/box"
	"github.com/corral-sh/corral/internal/guest"
	"github.com/corral-sh/corral/internal/policy"
	"github.com/corral-sh/corral/internal/ui"
)

// Exit codes of Corral's own outcomes. The agent's exit status passes
// through unchanged; these are sysexits(3) values (and GNU timeout's 124) that
// agents do not use, so an unattended caller can tell a task failure from
// "requeue" without parsing output. Documented in docs/FEATURES.md.
const (
	ExitPreflightRefused = 78  // EX_CONFIG: fix the configuration, do not retry
	ExitAdmissionRefused = 75  // EX_TEMPFAIL: requeue
	ExitUnreachable      = 69  // EX_UNAVAILABLE: the box died or SSH was lost
	ExitTimeout          = 124 // --timeout elapsed
)

// Outcome names, as written to --result and the audit log.
const (
	OutcomeExit             = "exit"
	OutcomePreflightRefused = "preflight-refused"
	OutcomeAdmissionRefused = "admission-refused"
	OutcomeUnreachable      = "unreachable"
	OutcomeTimeout          = "timeout"
)

// runResult is the record `run --result <file>` writes: what happened, in a
// shape a queue can act on. Names only, never values.
type runResult struct {
	Box       string    `json:"box"`
	Project   string    `json:"project,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Outcome   string    `json:"outcome"`
	ExitCode  int       `json:"exit_code"`
	Reason    string    `json:"reason,omitempty"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended"`
	Duration  string    `json:"duration"`
	Forwarded []string  `json:"forwarded_env,omitempty"`
}

// writeResult writes r as JSON to path (0600); a failure to write is reported
// but never changes the run's exit code.
func writeResult(path string, r runResult) {
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		data = append(data, '\n')
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		ui.Warning(os.Stderr, "could not write --result %s: %v", path, err)
	}
}

// admission decides whether one more box may start. running counts the
// boxes already running (not this one); usedBytes is their measured host
// footprint; wantBytes is the new box's memory cap. A zero hostBytes (memory
// unknown) skips the budget check; a zero maxRunning skips the count check.
func admission(running, maxRunning int, usedBytes, wantBytes, hostBytes, reserveBytes uint64) (ok bool, reason string) {
	if maxRunning > 0 && running >= maxRunning {
		return false, fmt.Sprintf("max_running = %d and %d box(es) are running", maxRunning, running)
	}
	if hostBytes > 0 {
		budget := uint64(0)
		if hostBytes > reserveBytes {
			budget = hostBytes - reserveBytes
		}
		if usedBytes+wantBytes > budget {
			return false, fmt.Sprintf("running boxes use %s and this box may take %s; only %s of %s is available once %s is kept for macOS (memory_reserve)",
				ui.HumanBytes(int64(usedBytes)), ui.HumanBytes(int64(wantBytes)), ui.HumanBytes(int64(budget)), ui.HumanBytes(int64(hostBytes)), ui.HumanBytes(int64(reserveBytes))) //nolint:gosec // sizes fit
		}
	}
	return true, ""
}

// admit runs the admission check for b before it is started. Boxes already
// running are never re-admitted — a session on a live box costs nothing new.
func admit(ctx context.Context, b *box.Box) error {
	cfg := b.Cfg
	if cfg.MaxRunning == 0 && policy.HostMemoryBytes() == 0 {
		return nil
	}
	insts, err := b.Lima.List(ctx)
	if err != nil {
		return nil //nolint:nilerr // admission is advisory when Lima cannot be asked; the start itself will report
	}
	foot := b.Lima.HostFootprints(ctx)
	running, used := 0, uint64(0)
	var names []string
	for _, in := range insts {
		if in.Name == b.Name || !in.Running() {
			continue
		}
		running++
		names = append(names, in.Name)
		if f := foot[in.Name]; f > 0 {
			used += uint64(f) //nolint:gosec // footprint is non-negative
		}
	}
	want, _ := policy.ParseSize(cfg.Memory)
	reserve, _ := policy.ParseSize(cfg.MemoryReserve)
	ok, reason := admission(running, cfg.MaxRunning, used, want, policy.HostMemoryBytes(), reserve)
	if ok {
		return nil
	}
	sort.Strings(names)
	hint := "corral gc"
	if len(names) > 0 {
		hint = "corral stop " + strings.Join(names, " ") + "  (or corral gc)"
	}
	return &exitError{code: ExitAdmissionRefused, outcome: OutcomeAdmissionRefused,
		msg: fmt.Sprintf("not starting %s: %s — free memory first: %s", b.Name, reason, hint)}
}

// killSession ends every guest process that carries CORRAL_SESSION=id:
// SIGTERM, a grace period, then SIGKILL. Killing ssh on the Mac is not
// enough when the session has no pty (no local TTY — launchd), because
// nothing then hangs up the remote command.
func killSession(ctx context.Context, b *box.Box, id string) {
	if id == "" {
		return
	}
	script := `id=` + guest.ShellQuote("CORRAL_SESSION="+id) + `
victims() { for p in /proc/[0-9]*; do tr '\0' '\n' <"$p/environ" 2>/dev/null | grep -qxF "$id" && echo "${p#/proc/}"; done; }
v=$(victims); [ -n "$v" ] && kill -TERM $v 2>/dev/null
for _ in 1 2 3 4 5 6 7 8 9 10; do v=$(victims); [ -z "$v" ] && exit 0; sleep 0.5; done
v=$(victims); [ -n "$v" ] && kill -KILL $v 2>/dev/null; exit 0`
	kctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _ = b.Lima.Run(kctx, b.Name, "bash", "-c", script)
}

// unreachableReason explains an SSH loss: is the instance gone, stopped, or
// did the guest kernel OOM-kill something? Best effort, names only.
func unreachableReason(ctx context.Context, b *box.Box) string {
	_, st, err := b.Status(ctx)
	switch {
	case err != nil:
		return "lima cannot be queried: " + err.Error()
	case st == box.StateMissing:
		return "the Lima instance no longer exists"
	case st == box.StateStopped:
		return "the box stopped during the session"
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := b.Lima.Run(probe, b.Name, "bash", "-c", "journalctl -k --no-pager -o cat 2>/dev/null | grep -i 'out of memory' | tail -1")
	if err == nil && strings.TrimSpace(out) != "" {
		return "guest OOM-kill: " + strings.TrimSpace(out)
	}
	return "SSH connection lost while the box kept running"
}
