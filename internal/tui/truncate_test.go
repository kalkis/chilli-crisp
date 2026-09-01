package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestTruncateMeasuresDisplayCells pins the property the old rune-counting
// version got wrong: the result must fit the budget the caller gave, measured
// the way a terminal draws it.
func TestTruncateMeasuresDisplayCells(t *testing.T) {
	cases := []struct {
		name string
		in   string
		w    int
		want string // exact expectation; "" means only the width is asserted
	}{
		{"fits", "hello", 10, "hello"},
		{"exact fit", "hello", 5, "hello"},
		{"cut", "hello world", 8, "hello w…"},
		// Seven CJK runes are fourteen cells: rune counting would have let
		// this through untouched and overflowed the pane by six columns.
		{"wide runes", "日本語テキスト", 8, ""},
		{"emoji", "🌶🌶🌶🌶🌶", 4, ""},
		{"single cell", "hello", 1, "…"},
		{"no room", "hello", 0, ""},
		{"negative room", "hello", -3, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncate(c.in, c.w)
			if c.want != "" && got != c.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
			}
			if w := lipgloss.Width(got); c.w > 0 && w > c.w {
				t.Errorf("truncate(%q, %d) is %d cells wide: %q", c.in, c.w, w, got)
			}
		})
	}
}

// TestTruncateLeavesANSIIntact is the kv-editor case: rows being edited carry
// a styled textinput view, whose escape bytes must not eat the budget or be
// cut in half.
func TestTruncateLeavesANSIIntact(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Render("hello world")
	got := truncate(styled, 8)

	if w := lipgloss.Width(got); w != 8 {
		t.Errorf("styled text truncated to %d cells, want 8: %q", w, got)
	}
	if !strings.Contains(got, "hello w") {
		t.Errorf("escape bytes were counted as content: %q", got)
	}
	// A cut inside a styled run must close the sequence, or the style bleeds
	// into the rest of the line.
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("truncated styled text should end with a reset: %q", got)
	}
}
