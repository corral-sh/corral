package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestWordmarkIsRectangular(t *testing.T) {
	rows := wordmark()
	if len(rows) != 6 {
		t.Fatalf("rows %d", len(rows))
	}
	w := lipgloss.Width(rows[0])
	for i, r := range rows {
		if lipgloss.Width(r) != w {
			t.Errorf("row %d width %d, want %d: %q", i, lipgloss.Width(r), w, r)
		}
	}
	if w+2 > BigBannerWidth {
		t.Errorf("wordmark %d wider than BigBannerWidth %d", w+2, BigBannerWidth)
	}
	for _, r := range "CORRAL" {
		if _, ok := glyphs[r]; !ok {
			t.Errorf("no glyph for %q", r)
		}
	}
}
