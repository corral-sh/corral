package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Tagline is the product line used in banners, the README and the site.
const Tagline = "Your agent, unleashed. Your Mac, untouched."

// Block letters (ANSI Shadow), composed per glyph so columns always line up.
var glyphs = map[rune][6]string{
	'C': {" ██████╗", "██╔════╝", "██║     ", "██║     ", "╚██████╗", " ╚═════╝"},
	'O': {" ██████╗ ", "██╔═══██╗", "██║   ██║", "██║   ██║", "╚██████╔╝", " ╚═════╝ "},
	'R': {"██████╗ ", "██╔══██╗", "██████╔╝", "██╔══██╗", "██║  ██║", "╚═╝  ╚═╝"},
	'A': {" █████╗ ", "██╔══██╗", "███████║", "██╔══██║", "██║  ██║", "╚═╝  ╚═╝"},
	'L': {"██╗     ", "██║     ", "██║     ", "██║     ", "███████╗", "╚══════╝"},
}

// BigBannerWidth is the visible width of the block-letter banner.
const BigBannerWidth = 56

// wordmark renders "CORRAL" in block letters, one string per row.
func wordmark() []string {
	rows := make([]string, 6)
	for _, r := range "CORRAL" {
		g := glyphs[r]
		for i := range rows {
			rows[i] += g[i]
		}
	}
	return rows
}

// BigBanner is the full-size product header: the block-letter wordmark in
// amber, then the tagline and a subtitle (version, counts…). It needs about
// BigBannerWidth columns; callers fall back to Banner on narrow terminals.
func BigBanner(subtitle string) string {
	var sb strings.Builder
	for _, row := range wordmark() {
		sb.WriteString("  " + Title.Render(row) + "\n")
	}
	// The tagline stands alone under the wordmark: the wordmark is the
	// mark here, a glyph in front of the line competed with it.
	sb.WriteString("  " + lipgloss.NewStyle().Bold(true).Foreground(Gold).Render(Tagline))
	if subtitle != "" {
		sb.WriteString(Subtle.Render("   ·   " + subtitle))
	}
	sb.WriteString("\n")
	return sb.String()
}

// Greeting is the card shown when an agent session starts: what is about to
// run, where, and under which boundary. rows are key/value pairs in order.
func Greeting(w io.Writer, boxName string, rows [][2]string) {
	var lines []string
	lines = append(lines, Logo+" "+Title.Render("Corral")+"  "+Subtle.Render(Tagline))
	lines = append(lines, "")
	lines = append(lines, Key.Render("box       ")+Bold.Render(boxName))
	for _, r := range rows {
		lines = append(lines, Key.Render(fmt.Sprintf("%-10s", r[0]))+r[1])
	}
	fmt.Fprintln(w, Panel.Render(strings.Join(lines, "\n")))
}
