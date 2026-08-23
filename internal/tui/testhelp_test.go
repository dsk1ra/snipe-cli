package tui

import (
	"regexp"
	"time"

	tea "charm.land/bubbletea/v2"
)

func nowForTest() time.Time { return time.Now() }

// keyMsg builds the KeyPressMsg a terminal would deliver for a key name.
func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "up", "down", "left", "right", "enter", "esc", "tab", "backspace", "delete", "space":
		return tea.KeyPressMsg{Code: named[s]}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

var named = map[string]rune{
	"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
	"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab,
	"backspace": tea.KeyBackspace, "delete": tea.KeyDelete, "space": tea.KeySpace,
}

// ansiRe matches an SGR colour sequence. Tests assert on what a row SAYS; the
// colours are asserted separately where they carry meaning of their own.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }
