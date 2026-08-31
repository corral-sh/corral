package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Every column must start at the same visible cell in the header and in each
// row, whatever ANSI styling a cell carries, and the table must fit the width.
func TestTableColumnsAlign(t *testing.T) {
	lipgloss.SetColorProfile(lipgloss.ColorProfile()) // whatever the test env has
	m := &dashModel{width: 200, rows: []BoxRow{
		{Name: "corral-ee07a8", Status: "Stopped", Project: "/Users/x/Code/corral", CPUs: 4, Memory: 8 << 30, LastUsed: time.Now().Add(-10 * time.Hour), Drifted: true},
		{Name: "api-3f9a2c", Status: "Running", Project: "/Users/x/Code/a-very-long-project-directory-name-that-goes-on-and-on-and-on", CPUs: 2, Memory: 4 << 30, LastUsed: time.Now()},
	}}
	lines := strings.Split(strings.TrimRight(m.renderTable(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %d", len(lines))
	}
	// Column start = visible width of everything before the cell. The header
	// and rows share the same layout function, so compare where "CPU" data
	// lands against the header's CPU label by visible offset of the numeric block.
	offset := func(line, marker string) int {
		i := strings.Index(line, marker)
		if i < 0 {
			t.Fatalf("%q not in %q", marker, line)
		}
		return lipgloss.Width(line[:i])
	}
	hdr := lines[0]
	// STATE column: header label and the row markers must start at the same cell.
	if a, b, c := offset(hdr, "STATE"), offset(lines[1], "⚠ config changed"), offset(lines[2], "ok"); a != b || a != c {
		t.Errorf("STATE column misaligned: header %d, row1 %d, row2 %d", a, b, c)
	}
	// LAST USED right-aligned block ends where the header's does.
	if a, b := offset(hdr, "LAST USED")+len("LAST USED"), offset(lines[1], "10h ago")+len("10h ago"); a != b {
		t.Errorf("LAST USED misaligned: header ends %d, row ends %d", a, b)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > 200 {
			t.Errorf("line wider than terminal: %d", w)
		}
	}
	if projectWidth(300) != 60 || projectWidth(80) != 20 {
		t.Errorf("project width clamp: %d %d", projectWidth(300), projectWidth(80))
	}
}

func TestWelcomeAndDetailRender(t *testing.T) {
	src := DashboardSource{Version: "0.4.3", DefaultAgent: "claude"}
	empty := PreviewDashboard(nil, 120, src)
	if !strings.Contains(empty, "three steps") || !strings.Contains(empty, "corral claude") {
		t.Fatalf("welcome card missing:\n%s", empty)
	}
	row := BoxRow{Name: "b", Status: "Running", Project: "/p", Agents: []string{"claude"}, CPUs: 4, Memory: 4 << 30, Network: "broker", LastDenial: "example.com:443", LastDenialTime: time.Now(), Drifted: true}
	wide := PreviewDashboard([]BoxRow{row}, 160, src)
	for _, want := range []string{"last denied example.com:443", "cpu / mem", "⚠ config changed since this box was built"} {
		if !strings.Contains(wide, want) {
			t.Errorf("detail missing %q", want)
		}
	}
	// Narrow: single column, still everything present, nothing wider than the terminal.
	narrow := PreviewDashboard([]BoxRow{row}, 107, src)
	for _, l := range strings.Split(narrow, "\n") {
		if lipgloss.Width(l) > 107 {
			t.Errorf("line wider than 107: %d %q", lipgloss.Width(l), l)
		}
	}
}

// `l` opens the log pane in place of the list, and esc brings the list back —
// the dashboard must not exit for a read-only peek.
func TestLogPaneOpensAndReturns(t *testing.T) {
	logs := "==> ha.stderr.log <==\nboot line 1\nboot line 2"
	src := DashboardSource{Version: "0.6.0", DefaultAgent: "claude",
		Logs: func(context.Context, BoxRow) (string, error) { return logs, nil }}
	row := BoxRow{Name: "b", Status: "Running", Project: "/p"}
	m := &dashModel{src: src, ctx: context.Background(), width: 120, height: 30, rows: []BoxRow{row}}

	mod, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = mod.(*dashModel)
	if m.logs == nil || cmd == nil {
		t.Fatalf("l should open the pane and schedule a fetch")
	}
	if m.action.Kind != "" {
		t.Fatalf("l must not set an exit action, got %q", m.action.Kind)
	}
	mod, _ = m.Update(cmd())
	m = mod.(*dashModel)
	view := m.View()
	for _, want := range []string{"logs · ", "boot line 2", "3 lines", "esc/l"} {
		if !strings.Contains(view, want) {
			t.Errorf("log pane missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "PROJECT") {
		t.Errorf("box list must be hidden while the pane is open")
	}
	for _, l := range strings.Split(view, "\n") {
		if lipgloss.Width(l) > 120 {
			t.Errorf("line wider than terminal: %d", lipgloss.Width(l))
		}
	}

	mod, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	m = mod.(*dashModel)
	if !m.logs.follow || !strings.Contains(m.View(), "following") {
		t.Errorf("f should toggle follow")
	}

	mod, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mod.(*dashModel)
	if m.logs != nil || m.action.Kind != "" || !strings.Contains(m.View(), "PROJECT") {
		t.Errorf("esc should return to the box list without quitting")
	}
}

// A stale logsMsg for a box whose pane was closed must not reopen anything.
func TestLogPaneIgnoresStaleMessage(t *testing.T) {
	m := &dashModel{src: DashboardSource{}, ctx: context.Background(), width: 100, height: 30}
	mod, _ := m.Update(logsMsg{box: "gone", out: "x"})
	if mod.(*dashModel).logs != nil {
		t.Fatal("stale logsMsg opened a pane")
	}
}

// Progress lines reported by a running operation must reach the busy view:
// runOp wires the report callback to opLogMsg via a channel and a re-armed
// listener. (A no-op report callback here once left busyLog permanently empty.)
func TestOpProgressReachesBusyLog(t *testing.T) {
	lines := []string{"booting the VM", "waiting for SSH", "ready"}
	m := &dashModel{ctx: context.Background(), src: DashboardSource{
		Toggle: func(_ context.Context, _ BoxRow, report func(string)) error {
			for _, l := range lines {
				report(l)
			}
			return nil
		},
	}}
	_, cmd := m.runOp("toggle", BoxRow{Name: "b", Status: "Running"})
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("runOp must return a batch of operation + listener, got %T", cmd())
	}
	// The operation runs first: it fills the buffered channel and closes it.
	done, ok := batch[0]().(opDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("operation: %+v", done)
	}
	// Pump the listener through Update the way the program loop would.
	listen := batch[1]
	for i := 0; ; i++ {
		msg := listen()
		if msg == nil {
			break // channel closed — listener retires
		}
		lm, ok := msg.(opLogMsg)
		if !ok {
			t.Fatalf("listener produced %T", msg)
		}
		if string(lm) != lines[i] {
			t.Fatalf("line %d = %q, want %q", i, lm, lines[i])
		}
		mod, next := m.Update(lm)
		m = mod.(*dashModel)
		if next == nil {
			t.Fatal("opLogMsg must re-arm the listener")
		}
		listen = next
	}
	if len(m.busyLog) != len(lines) || m.busyLog[len(lines)-1] != "ready" {
		t.Fatalf("busyLog = %q, want the reported lines", m.busyLog)
	}
	mod, _ := m.Update(done)
	m = mod.(*dashModel)
	if m.busyLog != nil || m.opCh != nil {
		t.Fatal("opDoneMsg must clear the busy state")
	}
}

// While an operation runs, state-changing keys stay blocked but `l` must open
// the log pane in follow mode — the box's log is being written at exactly that
// moment, so the pane is the natural way to trail a start/stop.
func TestLogPaneOpensDuringOperation(t *testing.T) {
	src := DashboardSource{
		Logs: func(context.Context, BoxRow) (string, error) { return "boot line", nil },
	}
	row := BoxRow{Name: "b", Status: "Stopped", Project: "/p"}
	m := &dashModel{src: src, ctx: context.Background(), width: 120, height: 30,
		rows: []BoxRow{row}, busy: "starting b"}

	if !strings.Contains(m.View(), "l full log") {
		t.Error("busy view must hint that the log pane is available")
	}
	mod, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = mod.(*dashModel)
	if m.confirm != "" || m.busy != "starting b" {
		t.Fatal("state-changing keys must stay blocked while busy")
	}
	mod, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = mod.(*dashModel)
	if m.logs == nil || cmd == nil {
		t.Fatal("l while busy must open the log pane and schedule the fetch/tick")
	}
	if !m.logs.follow {
		t.Error("a pane opened during an operation must follow, so it trails the log")
	}
	mod, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mod.(*dashModel)
	if m.logs != nil || m.busy != "starting b" {
		t.Error("esc must return to the busy view, with the operation still running")
	}
}
