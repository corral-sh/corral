package box

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/corral-sh/corral/internal/lima"
)

// Session is a live `corral` process attached to a box. Recorded in the
// metadata so that other invocations (and the idle sweep) can tell whether a
// running box is in use without a daemon; liveness is checked by PID.
type Session struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// SessionStart records this process as a session on the box. It is called
// before the box is created or started: a box that is booting for a
// session must never look idle to a concurrent sweep, and its previous
// session may have ended long ago. Without metadata yet (first creation) the
// session is remembered and written by Create. Idempotent per process.
func (b *Box) SessionStart() {
	if b.Meta == nil {
		b.pendingSession = true
		return
	}
	pid := os.Getpid()
	live := b.Meta.pruneSessions()
	for _, s := range live {
		if s.PID == pid {
			b.Meta.ActiveSessions = live
			_ = SaveMeta(b.Meta)
			return
		}
	}
	b.Meta.ActiveSessions = append(live, Session{PID: pid, StartedAt: time.Now()})
	_ = SaveMeta(b.Meta)
}

// SessionOpen reports whether this process holds a session on the box.
func (b *Box) SessionOpen() bool {
	if b.pendingSession {
		return true
	}
	if b.Meta == nil {
		return false
	}
	pid := os.Getpid()
	for _, s := range b.Meta.ActiveSessions {
		if s.PID == pid {
			return true
		}
	}
	return false
}

// adoptPendingSession moves a session registered before the metadata existed
// into freshly written metadata (Create / RecoverMeta).
func (b *Box) adoptPendingSession() {
	if !b.pendingSession || b.Meta == nil {
		return
	}
	b.pendingSession = false
	b.Meta.ActiveSessions = append(b.Meta.ActiveSessions, Session{PID: os.Getpid(), StartedAt: time.Now()})
}

// SessionEnd removes this process from the box and stamps the idle clock.
func (b *Box) SessionEnd() {
	b.pendingSession = false
	if b.Meta == nil {
		return
	}
	// Re-read: another session may have registered meanwhile.
	if m, err := LoadMeta(b.Name); err == nil {
		b.Meta = m
	}
	var keep []Session
	for _, s := range b.Meta.pruneSessions() {
		if s.PID != os.Getpid() {
			keep = append(keep, s)
		}
	}
	b.Meta.ActiveSessions = keep
	b.Meta.LastSessionEnd = time.Now()
	_ = SaveMeta(b.Meta)
}

// pruneSessions drops sessions whose process is gone (crashed, killed, or the
// terminal closed) so a stale entry can never keep a box alive forever.
func (m *Meta) pruneSessions() []Session {
	var live []Session
	for _, s := range m.ActiveSessions {
		if pidAlive(s.PID) {
			live = append(live, s)
		}
	}
	return live
}

var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 probes existence without delivering anything.
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// IdleSince reports when the box last had a session end, if no session is
// live now. ok is false while a session is attached (or nothing is known).
func (m *Meta) IdleSince() (since time.Time, ok bool) {
	if len(m.pruneSessions()) > 0 {
		return time.Time{}, false
	}
	since = m.LastSessionEnd
	if since.IsZero() {
		since = m.LastUsed // boxes created before sessions were tracked
	}
	if since.IsZero() {
		return time.Time{}, false
	}
	return since, true
}

// IdleStopped describes one box stopped by an idle sweep.
type IdleStopped struct {
	Name string
	Idle time.Duration
}

// IdleSweep stops every running box that has been idle longer than its
// idle_stop. limit returns the threshold for a box (0 = never). skip names a
// box to leave alone (the one about to be entered).
func IdleSweep(ctx context.Context, lc *lima.Client, limit func(*Meta) time.Duration, skip string) ([]IdleStopped, error) {
	insts, err := lc.List(ctx)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, i := range insts {
		if i.Running() {
			running[i.Name] = true
		}
	}
	metas, err := AllMeta()
	if err != nil {
		return nil, err
	}
	var out []IdleStopped
	for _, m := range metas {
		if m.Name == skip || m.IsGolden() || !running[m.Name] {
			continue
		}
		since, ok := m.IdleSince()
		if !ok {
			continue
		}
		max := limit(m)
		if max <= 0 {
			continue
		}
		if idle := time.Since(since); idle > max {
			if err := lc.Stop(ctx, m.Name, nil); err != nil {
				return out, fmt.Errorf("idle-stop %s: %w", m.Name, err)
			}
			StopBroker(m)
			Audit(AuditEvent{Event: "idle-stop", Box: m.Name, Duration: idle.Truncate(time.Minute).String()})
			out = append(out, IdleStopped{Name: m.Name, Idle: idle})
		}
	}
	return out, nil
}
