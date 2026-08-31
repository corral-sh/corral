package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BoxRow is one line in the dashboard, assembled by the CLI from Lima state
// and Corral metadata.
type BoxRow struct {
	Name     string
	Project  string
	Status   string // Lima status or "" when only metadata exists
	CPUs     int
	Memory   int64
	DiskUsed int64
	LastUsed time.Time
	Sessions int
	Agents   []string
	Drifted  bool
	// Network is the box's network mode; LastDenial the newest egress-denied
	// destination (broker mode), "" if none.
	Network        string
	LastDenial     string
	LastDenialTime time.Time
	// IdleSince is set when the box has no live session; LiveSessions otherwise.
	IdleSince    time.Time
	LiveSessions int
	// Live metrics (running boxes only)
	Load     string
	MemUsed  int64
	MemTotal int64
	// HostMem is the Mac's phys_footprint for the VM process — what the box
	// really costs. The guest never returns memory to the host (no balloon),
	// so this is a high-water mark until the box stops.
	HostMem  int64
	RootUsed int64
	RootSize int64
	Uptime   string
	// MetricsErr is the last live-metrics probe failure, so the detail panel
	// says why instead of showing "collecting…" forever (a real regression surfaced this).
	MetricsErr string
}

// DashboardAction is what the user asked the dashboard to do on exit.
type DashboardAction struct {
	Kind string // "", "launch", "shell"
	Box  BoxRow
}

// DashboardSource supplies data to the dashboard.
type DashboardSource struct {
	// Rows returns the current set of boxes.
	Rows func(ctx context.Context) ([]BoxRow, error)
	// Metrics fills the live fields of a running box.
	Metrics func(ctx context.Context, name string) (BoxRow, error)
	// Toggle starts or stops a box (blocking); the dashboard shows a spinner.
	Toggle func(ctx context.Context, row BoxRow, report func(string)) error
	// Delete removes a box.
	Delete func(ctx context.Context, row BoxRow, report func(string)) error
	// Logs returns the tail of the box's boot/provisioning log for the in-
	// dashboard log pane. nil disables the `l` key.
	Logs         func(ctx context.Context, row BoxRow) (string, error)
	DefaultAgent string
	Version      string
}

// RunDashboard shows the interactive overview and returns the chosen action.
func RunDashboard(ctx context.Context, src DashboardSource) (DashboardAction, error) {
	m := &dashModel{src: src, ctx: ctx}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return DashboardAction{}, err
	}
	return m.action, m.err
}

type dashModel struct {
	src      DashboardSource
	ctx      context.Context
	rows     []BoxRow
	cursor   int
	loading  bool
	busy     string // message while an operation runs
	busyLog  []string
	opCh     chan string // progress lines of the running operation; nil when idle
	confirm  string      // pending confirmation kind
	status   string
	err      error
	action   DashboardAction
	width    int
	height   int
	lastTick time.Time
	logs     *logPane // non-nil while the log pane is open
}

// logPane is the in-dashboard log viewer opened with `l`. It replaces the
// box list until dismissed, so the user comes back to the dashboard instead
// of the shell prompt.
type logPane struct {
	box     BoxRow
	view    viewport.Model
	follow  bool
	loading bool
	err     error
	lines   int
}

type logsMsg struct {
	box string
	out string
	err error
}
type logsTick time.Time

// logPaneChrome is the space left for the viewport after the header, the
// status line and the help line.
const logPaneChrome = 6

type rowsMsg struct {
	rows []BoxRow
	err  error
}
type metricsMsg struct {
	row BoxRow
	err error
}
type opDoneMsg struct {
	err error
	msg string
}
type opLogMsg string
type refreshTick time.Time

func (m *dashModel) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(m.fetchRows(), refreshEvery())
}

func refreshEvery() tea.Cmd {
	return tea.Tick(4*time.Second, func(t time.Time) tea.Msg { return refreshTick(t) })
}

func (m *dashModel) fetchRows() tea.Cmd {
	return func() tea.Msg {
		rows, err := m.src.Rows(m.ctx)
		return rowsMsg{rows: rows, err: err}
	}
}

func (m *dashModel) fetchMetrics() tea.Cmd {
	var cmds []tea.Cmd
	for _, r := range m.rows {
		if r.Status != "Running" || m.src.Metrics == nil {
			continue
		}
		name := r.Name
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
			defer cancel()
			row, err := m.src.Metrics(ctx, name)
			if row.Name == "" {
				row.Name = name
			}
			return metricsMsg{row: row, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *dashModel) selected() (BoxRow, bool) {
	if len(m.rows) == 0 || m.cursor < 0 || m.cursor >= len(m.rows) {
		return BoxRow{}, false
	}
	return m.rows[m.cursor], true
}

func (m *dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.logs != nil {
			m.logs.view.Width, m.logs.view.Height = m.logViewportSize()
		}
		return m, nil
	case rowsMsg:
		m.loading = false
		if msg.err != nil {
			m.status = Bad.Render("refresh failed: " + msg.err.Error())
			return m, nil
		}
		m.mergeRows(msg.rows)
		return m, m.fetchMetrics()
	case metricsMsg:
		for i := range m.rows {
			if m.rows[i].Name != msg.row.Name {
				continue
			}
			if msg.err != nil {
				m.rows[i].MetricsErr = firstLine(msg.err.Error())
				continue
			}
			m.rows[i].MetricsErr = ""
			m.rows[i].Load = msg.row.Load
			m.rows[i].MemUsed, m.rows[i].MemTotal, m.rows[i].HostMem = msg.row.MemUsed, msg.row.MemTotal, msg.row.HostMem
			m.rows[i].RootUsed, m.rows[i].RootSize = msg.row.RootUsed, msg.row.RootSize
			m.rows[i].Uptime = msg.row.Uptime
		}
		return m, nil
	case logsMsg:
		if m.logs == nil || m.logs.box.Name != msg.box {
			return m, nil
		}
		m.logs.loading = false
		m.logs.err = msg.err
		if msg.err == nil {
			atBottom := m.logs.view.AtBottom()
			m.logs.view.SetContent(strings.TrimRight(msg.out, "\n"))
			m.logs.lines = m.logs.view.TotalLineCount()
			if atBottom || m.logs.follow {
				m.logs.view.GotoBottom()
			}
		}
		return m, nil
	case logsTick:
		if m.logs != nil && m.logs.follow {
			return m, tea.Batch(m.fetchLogs(), logsTickEvery())
		}
		return m, nil
	case refreshTick:
		m.lastTick = time.Time(msg)
		if m.logs != nil {
			// The list is hidden; skip the refresh but keep the clock going.
			return m, refreshEvery()
		}
		if m.busy == "" {
			return m, tea.Batch(m.fetchRows(), refreshEvery())
		}
		return m, refreshEvery()
	case opLogMsg:
		m.busyLog = append(m.busyLog, string(msg))
		if len(m.busyLog) > 4 {
			m.busyLog = m.busyLog[1:]
		}
		if m.opCh == nil {
			return m, nil
		}
		return m, listenOp(m.opCh) // re-arm for the next line
	case opDoneMsg:
		m.busy = ""
		m.busyLog = nil
		m.opCh = nil
		if msg.err != nil {
			m.status = Bad.Render(msg.err.Error())
		} else {
			m.status = Ok.Render(msg.msg)
		}
		return m, m.fetchRows()
	case tea.KeyMsg:
		if m.logs != nil {
			return m.handleLogsKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func logsTickEvery() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return logsTick(t) })
}

func (m *dashModel) fetchLogs() tea.Cmd {
	if m.logs == nil || m.src.Logs == nil {
		return nil
	}
	row := m.logs.box
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		out, err := m.src.Logs(ctx, row)
		return logsMsg{box: row.Name, out: out, err: err}
	}
}

// logViewportSize is the viewport's inner size for the current terminal.
func (m *dashModel) logViewportSize() (int, int) {
	w, h := m.width, m.height
	if w == 0 {
		w = Width()
	}
	if h == 0 {
		h = 40
	}
	return max(20, w-6), max(3, h-logPaneChrome)
}

func (m *dashModel) openLogs(row BoxRow) tea.Cmd {
	w, h := m.logViewportSize()
	vp := viewport.New(w, h)
	vp.SetContent(Subtle.Render("loading…"))
	m.logs = &logPane{box: row, view: vp, loading: true}
	m.status = ""
	return m.fetchLogs()
}

func (m *dashModel) handleLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "l":
		m.logs = nil
		return m, m.fetchRows()
	case "f":
		m.logs.follow = !m.logs.follow
		if m.logs.follow {
			m.logs.view.GotoBottom()
			return m, tea.Batch(m.fetchLogs(), logsTickEvery())
		}
		return m, nil
	case "r":
		m.logs.loading = true
		return m, m.fetchLogs()
	case "g", "home":
		m.logs.view.GotoTop()
		return m, nil
	case "G", "end":
		m.logs.view.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.logs.view, cmd = m.logs.view.Update(msg)
	return m, cmd
}

func (m *dashModel) mergeRows(rows []BoxRow) {
	sort.Slice(rows, func(i, j int) bool {
		ri, rj := rows[i].Status == "Running", rows[j].Status == "Running"
		if ri != rj {
			return ri
		}
		return rows[i].LastUsed.After(rows[j].LastUsed)
	})
	// Keep live metrics across refreshes.
	old := map[string]BoxRow{}
	for _, r := range m.rows {
		old[r.Name] = r
	}
	for i := range rows {
		if o, ok := old[rows[i].Name]; ok && rows[i].Status == "Running" {
			rows[i].Load, rows[i].MemUsed, rows[i].MemTotal, rows[i].HostMem = o.Load, o.MemUsed, o.MemTotal, o.HostMem
			rows[i].RootUsed, rows[i].RootSize, rows[i].Uptime = o.RootUsed, o.RootSize, o.Uptime
		}
	}
	m.rows = rows
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
}

func (m *dashModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.busy != "" {
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.confirm != "" {
		switch msg.String() {
		case "y", "Y":
			kind := m.confirm
			m.confirm = ""
			row, _ := m.selected()
			return m.runOp(kind, row)
		default:
			m.confirm = ""
			m.status = Subtle.Render("cancelled")
		}
		return m, nil
	}
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case "r":
		m.loading = true
		return m, m.fetchRows()
	case "enter", "c":
		if row, ok := m.selected(); ok {
			m.action = DashboardAction{Kind: "launch", Box: row}
			return m, tea.Quit
		}
	case "h":
		if row, ok := m.selected(); ok {
			m.action = DashboardAction{Kind: "shell", Box: row}
			return m, tea.Quit
		}
	case "l":
		if row, ok := m.selected(); ok && m.src.Logs != nil {
			return m, m.openLogs(row)
		}
	case "s":
		if row, ok := m.selected(); ok && row.Status != "" {
			return m.runOp("toggle", row)
		}
	case "x", "d", "delete", "backspace":
		if _, ok := m.selected(); ok {
			m.confirm = "delete"
		}
	}
	return m, nil
}

func (m *dashModel) runOp(kind string, row BoxRow) (tea.Model, tea.Cmd) {
	switch kind {
	case "toggle":
		if row.Status == "Running" {
			m.busy = "stopping " + row.Name
		} else {
			m.busy = "starting " + row.Name
		}
	case "delete":
		m.busy = "deleting " + row.Name
	}
	// The operation's report callback feeds a channel; listenOp turns each
	// line into an opLogMsg so the last few show under the busy spinner.
	ch := make(chan string, 32)
	m.opCh = ch
	report := func(line string) {
		select {
		case ch <- line:
		default: // never block limactl's stream on a slow UI; drop the line instead
		}
	}
	busy := m.busy
	op := func() tea.Msg {
		var err error
		switch kind {
		case "toggle":
			err = m.src.Toggle(m.ctx, row, report)
		case "delete":
			err = m.src.Delete(m.ctx, row, report)
		}
		close(ch)
		verb := strings.TrimSuffix(strings.Fields(busy)[0], "ing") + "ed"
		if verb == "stoped" {
			verb = "stopped"
		}
		return opDoneMsg{err: err, msg: row.Name + " " + verb}
	}
	return m, tea.Batch(op, listenOp(ch))
}

// listenOp delivers the next progress line of the running operation; the
// opLogMsg case re-arms it until the operation closes the channel.
func listenOp(ch chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return nil
		}
		return opLogMsg(line)
	}
}

func (m *dashModel) View() string {
	if m.logs != nil {
		return m.renderLogs()
	}
	var sb strings.Builder
	subtitle := "v" + m.src.Version + "  ·  " + len2(m.rows) + "  ·  " + m.runningCount() + " running"
	width := m.width
	if width == 0 {
		width = Width()
	}
	if width >= BigBannerWidth {
		sb.WriteString(BigBanner(subtitle))
	} else {
		fmt.Fprintf(&sb, "%s %s  %s\n", Logo, Title.Render("Corral"), Subtle.Render(subtitle))
	}
	sb.WriteString("\n")

	if m.loading && len(m.rows) == 0 {
		sb.WriteString(Subtle.Render("  loading boxes…") + "\n")
	} else if len(m.rows) == 0 {
		sb.WriteString(m.renderWelcome())
	} else {
		sb.WriteString(m.renderTable())
	}

	sb.WriteString("\n")
	if row, ok := m.selected(); ok && len(m.rows) > 0 {
		sb.WriteString(m.renderDetail(row))
	}

	sb.WriteString("\n")
	switch {
	case m.busy != "":
		sb.WriteString("  " + Warn.Render("⏳ "+m.busy+"…") + "\n")
		for _, l := range m.busyLog {
			sb.WriteString("    " + Subtle.Render(l) + "\n")
		}
	case m.confirm == "delete":
		row, _ := m.selected()
		sb.WriteString("  " + Bad.Render(fmt.Sprintf("Delete box %s? The VM disk is removed; your project and agent login are kept. [y/N]", row.Name)) + "\n")
	case m.status != "":
		sb.WriteString("  " + m.status + "\n")
	default:
		sb.WriteString("\n")
	}
	sb.WriteString("\n" + m.renderHelp())
	return sb.String()
}

func len2(rows []BoxRow) string {
	if len(rows) == 1 {
		return "1 box"
	}
	return fmt.Sprintf("%d boxes", len(rows))
}

func (m *dashModel) runningCount() string {
	n := 0
	for _, r := range m.rows {
		if r.Status == "Running" {
			n++
		}
	}
	return fmt.Sprint(n)
}

// Table column widths. The project column takes what is left, capped so a
// wide terminal does not push the numeric columns to the far edge.
const (
	colBox    = 22
	colStatus = 14
	colCPU    = 3
	colMem    = 9
	colUsed   = 10
	colState  = 18 // "⚠ config changed"
	// indent(2) + cursor(2) + gaps(6) + fixed columns
	tableFixed = 2 + 2 + 6 + colBox + colStatus + colCPU + colMem + colUsed + colState
)

func projectWidth(width int) int {
	return min(60, max(20, width-tableFixed))
}

// padRight pads s to w visible cells, counting ANSI-styled text correctly
// (fmt's %-*s counts escape bytes and shifts every column after a badge).
func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

func (m *dashModel) renderTable() string {
	width := m.width
	if width == 0 {
		width = Width()
	}
	projW := projectWidth(width)
	cols := func(cursor, box, status, project, cpu, mem, used, state string) string {
		return cursor + " " + padRight(box, colBox) + " " + padRight(status, colStatus) + " " + padRight(project, projW) +
			" " + padLeft(cpu, colCPU) + " " + padLeft(mem, colMem) + " " + padLeft(used, colUsed) + "  " + state
	}
	var sb strings.Builder
	sb.WriteString("  " + Header.Render(cols("  ", "BOX", "STATUS", "PROJECT", "CPU", "MEMORY", "LAST USED", "STATE")) + "\n")
	for i, r := range m.rows {
		cursor := "  "
		if i == m.cursor {
			cursor = Title.Render("▶ ")
		}
		mem := "-"
		if r.Memory > 0 {
			mem = HumanBytes(r.Memory)
		}
		cpu := "-"
		if r.CPUs > 0 {
			cpu = fmt.Sprint(r.CPUs)
		}
		state := DriftBadge(r.Drifted)
		line := cols(cursor, Truncate(r.Name, colBox), StatusBadge(r.Status), Truncate(ShortenHome(r.Project), projW), cpu, mem, Ago(r.LastUsed), state)
		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		sb.WriteString("  " + line + "\n")
	}
	return sb.String()
}

// renderWelcome is the first-run screen: three steps instead of a grey line.
func (m *dashModel) renderWelcome() string {
	agent := m.src.DefaultAgent
	if agent == "" {
		agent = "claude"
	}
	lines := []string{
		Bold.Render("No boxes yet — three steps to the first one"),
		"",
		Title.Render("1") + "  " + "Check the Mac once            " + Code.Render("corral doctor"),
		Title.Render("2") + "  " + "Go to a project               " + Code.Render("cd ~/Code/<project>"),
		Title.Render("3") + "  " + "Start the agent in its box    " + Code.Render("corral "+agent),
		"",
		Subtle.Render("The first box builds a golden image (2–4 min, once); every further project is ready in ~15 s."),
		Subtle.Render("Commit a .corral.toml so the team shares one box definition: corral config init"),
	}
	w := m.width
	if w == 0 {
		w = Width()
	}
	return Panel.Width(min(w-4, 96)).Render(strings.Join(lines, "\n")) + "\n"
}

// renderDetail shows the selected box in two columns: what it is and where
// its boundary sits on the left, resources and live guest state on the right.
func (m *dashModel) renderDetail(r BoxRow) string {
	kv := func(k, v string) string { return Key.Render(fmt.Sprintf("%-10s", k)) + v }
	left := []string{
		Bold.Render(r.Name) + "  " + StatusBadge(r.Status),
		kv("project", ShortenHome(r.Project)),
		kv("agents", strings.Join(r.Agents, ", ")),
	}
	if r.Network != "" {
		net := r.Network
		if r.Network == "broker" && r.LastDenial != "" {
			net += Subtle.Render("  · last denied " + r.LastDenial + " " + Ago(r.LastDenialTime))
		} else if r.Network == "broker" {
			net += Subtle.Render("  · no denials")
		}
		left = append(left, kv("network", net))
	}
	sessions := fmt.Sprintf("%d", r.Sessions)
	switch {
	case r.LiveSessions > 0:
		sessions += Ok.Render(fmt.Sprintf("  · %d live", r.LiveSessions))
	case !r.IdleSince.IsZero() && r.Status == "Running":
		sessions += Subtle.Render("  · idle since " + Ago(r.IdleSince))
	}
	right := []string{
		kv("cpu / mem", fmt.Sprintf("%d · %s", r.CPUs, HumanBytes(r.Memory))),
		kv("host disk", HumanBytes(r.DiskUsed)+Subtle.Render("  (VM image)")),
		kv("sessions", sessions),
		kv("last used", Ago(r.LastUsed)),
	}
	if r.Status == "Running" {
		live := Subtle.Render("collecting…")
		if r.MetricsErr != "" {
			live = Bad.Render("probe failed: " + Truncate(r.MetricsErr, 80))
		}
		if r.MemTotal > 0 {
			live = Ok.Render(fmt.Sprintf("load %s · mem %s/%s · disk %s/%s · up %s",
				r.Load, HumanBytes(r.MemUsed), HumanBytes(r.MemTotal), HumanBytes(r.RootUsed), HumanBytes(r.RootSize), r.Uptime))
		}
		right = append(right, kv("guest", live))
		if r.HostMem > 0 {
			// What the Mac actually pays: the guest never hands memory back.
			right = append(right, kv("host mem", HumanBytes(r.HostMem)+Subtle.Render("  (VM process; freed guest memory stays here until stop)")))
		}
	}
	w := m.width
	if w == 0 {
		w = Width()
	}
	inner := w - 8
	var body string
	if inner >= 100 {
		lw := inner / 2
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(lw).Render(strings.Join(left, "\n")),
			lipgloss.NewStyle().Width(inner-lw).Render(strings.Join(right, "\n")))
	} else {
		body = strings.Join(append(left, right...), "\n")
	}
	if r.Drifted {
		body += "\n" + Warn.Render("⚠ config changed since this box was built — run `corral rebuild` in the project to apply")
	}
	return Panel.Width(w - 4).Render(body)
}

// PreviewDashboard renders the dashboard body for rows at width; used by
// tests and the preview tool, not by the TUI itself.
func PreviewDashboard(rows []BoxRow, width int, src DashboardSource) string {
	m := &dashModel{rows: rows, width: width, src: src}
	return m.View()
}

// renderLogs draws the log pane: one header line, the viewport, a status
// line and its own key help.
func (m *dashModel) renderLogs() string {
	p := m.logs
	var sb strings.Builder
	title := Title.Render("Corral") + "  " + Subtle.Render("logs · ") + Bold.Render(p.box.Name) + "  " + StatusBadge(p.box.Status)
	if p.follow {
		title += "  " + Ok.Render("● following")
	}
	sb.WriteString("  " + title + "\n\n")
	sb.WriteString(lipgloss.NewStyle().PaddingLeft(2).Render(p.view.View()) + "\n")
	var status string
	switch {
	case p.err != nil:
		status = Bad.Render(p.err.Error())
	case p.loading:
		status = Subtle.Render("loading…")
	default:
		status = Subtle.Render(fmt.Sprintf("%d lines · %3.0f%%", p.lines, p.view.ScrollPercent()*100))
	}
	sb.WriteString("  " + status + "\n")
	keys := []string{
		"esc/l " + Subtle.Render("back"),
		"f " + Subtle.Render("follow"),
		"r " + Subtle.Render("reload"),
		"↑↓/pgup/pgdn " + Subtle.Render("scroll"),
		"g/G " + Subtle.Render("top/bottom"),
		"q " + Subtle.Render("back"),
	}
	sb.WriteString("  " + strings.Join(keys, Subtle.Render("  ·  ")))
	return sb.String()
}

func (m *dashModel) renderHelp() string {
	keys := []string{
		"enter/c " + Subtle.Render(m.src.DefaultAgent),
		"h " + Subtle.Render("shell"),
		"s " + Subtle.Render("start/stop"),
		"l " + Subtle.Render("logs"),
		"x " + Subtle.Render("delete"),
		"r " + Subtle.Render("refresh"),
		"q " + Subtle.Render("quit"),
	}
	return "  " + strings.Join(keys, Subtle.Render("  ·  "))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
