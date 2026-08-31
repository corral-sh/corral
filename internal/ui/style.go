// Package ui holds the terminal look & feel: colours, banner, tables, the
// progress runner used for long Lima operations and the dashboard.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"golang.org/x/term"
)

// Palette — amber for the corral, slate for structure, green/red for state.
var (
	Amber  = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#F59E0B"}
	Gold   = lipgloss.AdaptiveColor{Light: "#92400E", Dark: "#FCD34D"}
	Slate  = lipgloss.AdaptiveColor{Light: "#475569", Dark: "#94A3B8"}
	Dim    = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#64748B"}
	Green  = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	Red    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	Blue   = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}
	Border = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#334155"}

	Title    = lipgloss.NewStyle().Bold(true).Foreground(Amber)
	Subtle   = lipgloss.NewStyle().Foreground(Dim)
	Bold     = lipgloss.NewStyle().Bold(true)
	Ok       = lipgloss.NewStyle().Foreground(Green)
	Warn     = lipgloss.NewStyle().Foreground(Gold)
	Bad      = lipgloss.NewStyle().Foreground(Red)
	Info     = lipgloss.NewStyle().Foreground(Blue)
	Key      = lipgloss.NewStyle().Foreground(Slate)
	Code     = lipgloss.NewStyle().Foreground(Gold)
	Header   = lipgloss.NewStyle().Bold(true).Foreground(Slate)
	Panel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border).Padding(0, 1)
	StatusOn = lipgloss.NewStyle().Foreground(Green).Bold(true)
	StatusOf = lipgloss.NewStyle().Foreground(Dim)
)

// Logo is the corral glyph used in banners.
const Logo = "🐎"

// Banner prints the product header.
func Banner(w io.Writer, version string) {
	fmt.Fprintf(w, "%s %s %s\n", Logo, Title.Render("Corral"), Subtle.Render("v"+version+" · "+Tagline))
}

// IsTTY reports whether stdout is a terminal.
func IsTTY() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// Width returns the terminal width or a sane default.
func Width() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 100
}

// Step prints a "→ message" line.
func Step(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", Info.Render("→"), fmt.Sprintf(format, a...))
}

// Success prints a "✓ message" line.
func Success(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", Ok.Render("✓"), fmt.Sprintf(format, a...))
}

// Warning prints a "! message" line.
func Warning(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", Warn.Render("!"), fmt.Sprintf(format, a...))
}

// Failure prints a "✗ message" line.
func Failure(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s %s\n", Bad.Render("✗"), fmt.Sprintf(format, a...))
}

// KV prints an aligned key/value pair.
func KV(w io.Writer, key, value string) {
	fmt.Fprintf(w, "  %s %s\n", Key.Render(fmt.Sprintf("%-18s", key)), value)
}

// Table renders rows with a styled header.
func Table(headers []string, rows [][]string) string {
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(Border)).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return Header.Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
	return t.Render()
}

// StatusBadge colours a Lima status.
// DriftBadge marks a box whose configuration changed since it was built.
// ok=true renders the quiet counterpart so tables keep a value in the column.
func DriftBadge(drifted bool) string {
	if drifted {
		return Warn.Render("⚠ config changed")
	}
	return Subtle.Render("ok")
}

// Section prints a grouped heading for key/value blocks (info, config).
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", Header.Render(title))
}

func StatusBadge(status string) string {
	switch status {
	case "Running":
		return StatusOn.Render("● running")
	case "Stopped":
		return StatusOf.Render("○ stopped")
	case "":
		return Subtle.Render("· not created")
	default:
		return Warn.Render("◌ " + strings.ToLower(status))
	}
}

// HumanBytes formats bytes as GiB/MiB.
func HumanBytes(b int64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.0f MiB", float64(b)/mib)
	case b >= kib:
		return fmt.Sprintf("%.0f KiB", float64(b)/kib)
	}
	return fmt.Sprintf("%d B", b)
}

// Ago renders a relative time.
func Ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Truncate shortens s to n runes with an ellipsis.
func Truncate(s string, n int) string {
	if n <= 1 || len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// ShortenHome replaces the home prefix with ~.
func ShortenHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}
