package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RunWithProgress executes work while showing a spinner, the elapsed time and
// the last few log lines the work reports. When stdout is not a TTY the lines
// are printed plainly instead.
func RunWithProgress(ctx context.Context, title string, work func(report func(string)) error) error {
	verbose := os.Getenv("CORRAL_VERBOSE") == "1"
	if !IsTTY() || os.Getenv("CORRAL_PLAIN") == "1" {
		Step(os.Stderr, "%s", title)
		start := time.Now()
		seen := map[string]bool{}
		err := work(func(line string) {
			if verbose {
				fmt.Fprintln(os.Stderr, Subtle.Render("  "+line))
				return
			}
			// Lima repeats its requirement lines (several requirements, retries);
			// a phase is announced once.
			if ms, ok := Milestone(line); ok && !seen[ms] {
				seen[ms] = true
				fmt.Fprintln(os.Stderr, Subtle.Render(fmt.Sprintf("  %-38s %s", ms, time.Since(start).Truncate(time.Second))))
			}
		})
		if err != nil {
			Failure(os.Stderr, "%s", title)
			return err
		}
		Success(os.Stderr, "%s", title)
		return nil
	}

	m := newProgressModel(title)
	m.verbose = verbose
	p := tea.NewProgram(m, tea.WithOutput(os.Stderr), tea.WithContext(ctx))

	var (
		wg      sync.WaitGroup
		workErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Give the program a moment to start so early sends are not dropped.
		time.Sleep(30 * time.Millisecond)
		workErr = work(func(line string) { p.Send(logMsg(line)) })
		p.Send(doneMsg{err: workErr})
	}()
	if _, err := p.Run(); err != nil && workErr == nil {
		workErr = err
	}
	wg.Wait()
	return workErr
}

type logMsg string
type doneMsg struct{ err error }
type tickMsg time.Time

type progressModel struct {
	title   string
	spinner spinner.Model
	lines   []string
	start   time.Time
	done    bool
	err     error
	maxLine int
	verbose bool
	seen    map[string]bool
}

func newProgressModel(title string) *progressModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = lipgloss.NewStyle().Foreground(Amber)
	return &progressModel{title: title, spinner: s, start: time.Now(), maxLine: 6}
}

func (m *progressModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick())
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.err = context.Canceled
			m.done = true
			return m, tea.Quit
		}
	case logMsg:
		line := strings.TrimSpace(string(msg))
		if line == "" {
			return m, nil
		}
		if !m.verbose {
			// Collapse Lima's chatter into milestones; keep the raw line
			// only when it is not one of the known phases.
			ms, ok := Milestone(line)
			if !ok {
				return m, nil
			}
			if m.seen == nil {
				m.seen = map[string]bool{}
			}
			if m.seen[ms] {
				return m, nil
			}
			m.seen[ms] = true
			line = fmt.Sprintf("%-38s %s", ms, time.Since(m.start).Truncate(time.Second))
		}
		m.lines = append(m.lines, line)
		if len(m.lines) > m.maxLine {
			m.lines = m.lines[1:]
		}
		return m, nil
	case doneMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	case tickMsg:
		return m, tick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *progressModel) View() string {
	elapsed := time.Since(m.start).Truncate(time.Second)
	var sb strings.Builder
	switch {
	case m.done && m.err != nil:
		fmt.Fprintf(&sb, "%s %s %s\n", Bad.Render("✗"), m.title, Subtle.Render(elapsed.String()))
	case m.done:
		fmt.Fprintf(&sb, "%s %s %s\n", Ok.Render("✓"), m.title, Subtle.Render(elapsed.String()))
	default:
		fmt.Fprintf(&sb, "%s %s %s\n", m.spinner.View(), m.title, Subtle.Render(elapsed.String()))
	}
	if !m.done || m.err != nil {
		width := Width() - 6
		for _, l := range m.lines {
			sb.WriteString("  " + Subtle.Render(Truncate(l, width)) + "\n")
		}
	}
	return sb.String()
}

// Confirm asks a yes/no question on the terminal. Non-TTY returns def.
func Confirm(w io.Writer, question string, def bool) bool {
	if !IsTTY() {
		return def
	}
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(w, "%s %s %s ", Warn.Render("?"), question, Subtle.Render(hint))
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "" {
		return def
	}
	return answer == "y" || answer == "yes"
}

// Milestone maps one raw line of limactl / provisioning output to the phase
// it belongs to. ok=false means the line is noise (progress bars, debug).
// The phases are what a user needs to know while waiting: what is happening
// now and roughly how far along the boot is.
func Milestone(line string) (string, bool) {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "[corral]"):
		// Our own provisioning scripts announce themselves.
		i := strings.Index(l, "[corral]")
		msg := strings.TrimSpace(line[i+len("[corral]"):])
		return "provisioning: " + Truncate(msg, 60), true
	case strings.Contains(l, "cloning golden"):
		return "cloning the golden image", true
	case strings.Contains(l, "attempting to download") || strings.Contains(l, "downloading"):
		return "downloading the Ubuntu image", true
	case strings.Contains(l, "download") && strings.Contains(l, "digest"):
		return "verifying the image digest", true
	case strings.Contains(l, "starting the instance"):
		return "booting the VM", true
	case strings.Contains(l, "waiting for the essential requirement") || strings.Contains(l, "waiting for ssh"):
		return "waiting for SSH", true
	case strings.Contains(l, "essential requirement") && strings.Contains(l, "satisfied"):
		return "SSH is up · provisioning", true
	case strings.Contains(l, "mounting") || strings.Contains(l, "mount"):
		return "mounting the project", true
	case strings.Contains(l, "optional requirement"):
		return "finishing provisioning", true
	case strings.Contains(l, "ready. run"):
		return "ready", true
	case strings.Contains(l, "stopping") || strings.Contains(l, "shutting down"):
		return "shutting down", true
	case strings.Contains(l, "level=fatal") || strings.Contains(l, "level=error") || strings.Contains(l, "error"):
		return "error: " + Truncate(line, 70), true
	}
	return "", false
}
