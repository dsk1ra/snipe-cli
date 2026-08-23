package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Colours carried over from the ink implementation so the two look the same
// side by side during the port.
var (
	colDim     = lipgloss.Color("240")
	colFaint   = lipgloss.Color("243")
	colAccent  = lipgloss.Color("#d4af37")
	colErr     = lipgloss.Color("#d70000")
	colWarn    = lipgloss.Color("#ff8700")
	colOK      = lipgloss.Color("2")
	colYellow  = lipgloss.Color("3")
	colHeading = lipgloss.Color("7")
)

var (
	styDim      = lipgloss.NewStyle().Foreground(colDim)
	styFaint    = lipgloss.NewStyle().Foreground(colFaint)
	styAccent   = lipgloss.NewStyle().Foreground(colAccent)
	styErr      = lipgloss.NewStyle().Foreground(colErr)
	styWarn     = lipgloss.NewStyle().Foreground(colWarn)
	styOK       = lipgloss.NewStyle().Foreground(colOK)
	styHeading  = lipgloss.NewStyle().Foreground(colHeading).Bold(true)
	styFocused  = lipgloss.NewStyle().Reverse(true)
	styTabOn    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styTabOff   = lipgloss.NewStyle().Foreground(colDim)
	styAction   = lipgloss.NewStyle().Foreground(colYellow)
	styLink     = lipgloss.NewStyle().Foreground(lipgloss.Color("4")).Underline(true)
	styKeyLabel = lipgloss.NewStyle().Foreground(colFaint)

	// The frame. lipgloss sizes a block TOTAL — Width(n) and Height(n) include
	// the border and the padding — so every measurement below is the space the
	// pane occupies on screen, not the space left inside it.
	styBox = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1)

	// A pane divider drawn as one pane's right edge. Cheaper than a third
	// column, and it cannot drift out of alignment with what it divides.
	styRule = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, true, false, false).
		BorderForeground(colDim).Padding(0, 1)

	styPaneHead = lipgloss.NewStyle().Bold(true).Underline(true)
)

// styField is a bordered input or button; focus moves its border, not its fill,
// so a focused field does not change width.
func styField(focused bool) lipgloss.Style {
	s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if focused {
		return s.BorderForeground(colAccent)
	}
	return s.BorderForeground(colDim)
}

// spread lays a label and a value on one line of width w, pushed to the edges.
// It is the shape every stats row takes, and it degrades to a single space when
// the pane is too narrow to hold both.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// scoreStyle bands a 0–5 score the same way the eval report does, so "how close
// was it" reads identically wherever it appears.
func scoreStyle(s float64) lipgloss.Style {
	switch {
	case s < 2:
		return lipgloss.NewStyle().Foreground(colErr)
	case s < 3:
		return lipgloss.NewStyle().Foreground(colWarn)
	case s < 4:
		return lipgloss.NewStyle().Foreground(colYellow)
	default:
		return lipgloss.NewStyle().Foreground(colOK)
	}
}

// osc8 wraps ALREADY-STYLED text in an OSC-8 terminal hyperlink.
//
// Order matters: lipgloss segments its input and re-emits a style sequence per
// segment, so handing it a string that already contains escapes makes it style
// each escape byte as if it were a character — a 4-character "link" rendered as
// 40 columns of styled escape soup. Style first, wrap second.
func osc8(styled, url string) string {
	if url == "" {
		return styled
	}
	return fmt.Sprintf("\x1b]8;;%s\x07%s\x1b]8;;\x07", url, styled)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// truncate cuts to w display columns, marking the cut with an ellipsis. Width
// is counted in grapheme clusters, so a CJK title or an emoji costs what it
// actually renders, and an escape sequence costs nothing.
//
// ansi.Truncate rather than a loop that drops one rune and re-measures the
// whole string: that was O(n²), and every frame ran it over the WHOLE pasted
// job description to show its first 90 columns. An 8 KB paste cost 780 ms a
// frame, which is what made a paste feel like it landed a second late — the
// render was still drawing the previous one. It is also correct where the loop
// was not: cutting runes can land inside an escape sequence.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// pad and lpad set a cell to w display columns. Measured with lipgloss.Width, so
// a styled string costs what it renders rather than what it contains.
func pad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func lpad(s string, w int) string {
	if n := w - lipgloss.Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}
