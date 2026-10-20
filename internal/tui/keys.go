package tui

import "charm.land/bubbles/v2/key"

// Every key in the app is declared once, as a key.Binding carrying its own help
// text. The footer hint and the ? overlay both render from these bindings, so
// they cannot drift — the ink version kept HELP_ROWS and ROW_HINT as two
// separate hand-maintained lists and docs/TUI.md flagged that as a hazard.
//
// A component that cannot currently act on a binding calls SetEnabled(false) on
// it, and it disappears from the help as well as from the handler. That is the
// whole mechanism behind contextual hints; there is no hintFor() switch.

type globalKeys struct {
	NextTab key.Binding
	PrevTab key.Binding
	Tab1    key.Binding
	Tab2    key.Binding
	Tab3    key.Binding
	Help    key.Binding
	Quit    key.Binding
	Stop    key.Binding
	Pause   key.Binding
}

func newGlobalKeys() globalKeys {
	return globalKeys{
		NextTab: key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→", "next tab")),
		PrevTab: key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←", "prev tab")),
		Tab1:    key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "queue")),
		Tab2:    key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "activity")),
		Tab3:    key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "follow-ups")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Stop:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop after this stage")),
		// Pause drops the stage in flight; Stop lets it finish first. Both
		// resume with ▶, and the difference is only whether that one model call
		// is redone.
		Pause: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
	}
}

type queueKeys struct {
	Up      key.Binding
	Down    key.Binding
	Step    key.Binding // ↑↓ walk the composer's fields
	In      key.Binding // ↓/Enter dives from tab level into the list
	Out     key.Binding // Esc/↑ steps back out
	NextAct key.Binding // → walks the focused row's inline actions
	PrevAct key.Binding
	Fire    key.Binding // Enter on whatever is focused
	Cycle   key.Binding // Tab: inputs ⇄ ▶ ⇄ list
	Start   key.Binding
	Open    key.Binding
	Skills  key.Binding
	Applied key.Binding
	Skip    key.Binding
	Yes     key.Binding
	No      key.Binding
	Slash   key.Binding
	Find    key.Binding
}

func newQueueKeys() queueKeys {
	return queueKeys{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		Step:    key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "fields")),
		In:      key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "into the list")),
		Out:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "step out")),
		NextAct: key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "next action")),
		PrevAct: key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "prev action")),
		Fire:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "do it")),
		Cycle:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "input ⇄ ▶ ⇄ list")),
		Start:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "start / resume")),
		Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open the folder")),
		Skills:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "review skills")),
		Applied: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "mark applied")),
		Skip:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "mark skipped")),
		Yes:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "evaluate anyway")),
		No:      key.NewBinding(key.WithKeys("n", "backspace", "delete"), key.WithHelp("n", "dismiss the gate")),
		// The palette: the CLI subcommands that have no key of their own. See
		// paletteCmds.
		Slash: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "command")),
		// Fuzzy filter over the list. ctrl+f as well as f, because f is what the
		// hands reach for and ctrl+f is what everything else in the world binds.
		Find: key.NewBinding(key.WithKeys("f", "ctrl+f"), key.WithHelp("f", "find")),
	}
}

type activityKeys struct {
	Year   key.Binding
	Month  key.Binding
	Day    key.Binding
	Toggle key.Binding
	Prev   key.Binding
	Next   key.Binding
	Cursor key.Binding
	Open   key.Binding
}

func newActivityKeys() activityKeys {
	return activityKeys{
		Year:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "year")),
		Month: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "month")),
		Day:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "day")),
		// Not j/k: those are down/up in both other tabs, and a key that moves
		// the cursor in two places and switches the series in a third is a key
		// nobody can hold in their hands. tab is free here and already means
		// "cycle" in the queue.
		Toggle: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "scans ⇄ apps")),
		Prev:   key.NewBinding(key.WithKeys("<", ","), key.WithHelp("<", "back")),
		Next:   key.NewBinding(key.WithKeys(">", "."), key.WithHelp(">", "forward")),
		Cursor: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "cell cursor")),
		// o is "open" in the queue and the follow-ups too. Here it opens the
		// day, which is the only thing on this tab there is to open.
		Open: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open the day in the queue")),
	}
}

type followupKeys struct {
	Up        key.Binding
	Down      key.Binding
	Out       key.Binding
	Nudged    key.Binding
	Rejected  key.Binding
	Proceeded key.Binding
	Undo      key.Binding
	Open      key.Binding
}

func newFollowupKeys() followupKeys {
	return followupKeys{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		Out:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "step out")),
		Nudged:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "nudged")),
		Rejected:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rejected")),
		Proceeded: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "proceeded")),
		Undo:      key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "undo")),
		Open:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open report")),
	}
}
