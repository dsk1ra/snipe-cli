package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// tab is what the root delegates to. Update returns whether it consumed the
// message; anything it declines falls through to the global keymap.
//
// This one interface is the whole navigation model. There is no global focus
// state: the root knows which tab is active, each tab knows what is focused
// inside it, and neither can contradict the other.
type tab interface {
	Update(tea.Msg) (bool, tea.Cmd)
	View() string
	setSize(w, h int)
	setSnapshot(snapshot)
	setFrame(int)
	ShortHelp() []key.Binding
	FullHelp() [][]key.Binding
	Title() string
	// Badge is the count the tab bar shows after the title, 0 for none. Only the
	// tab holding the data can answer, and no one else may compute one — that is
	// how two places end up disagreeing about how many follow-ups are due.
	Badge() int
}

// showDayMsg asks the root to put the queue on a given day, as a DayKey. Sent by
// the activity tab, which knows which day the cursor is on and nothing at all
// about what the queue holds.
type showDayMsg string

type Model struct {
	l     paths.Layout
	cache *state.Cache
	keys  globalKeys
	help  help.Model

	// queue is tabs[0], kept typed because the root already wires it by hand —
	// the follow-up count below, and the day handoff in Update.
	queue *queueTab
	// watch decides whether a poll has anything to re-read. It is the model's
	// rather than the poll's because a Cmd runs in its own goroutine and is
	// built fresh every tick.
	watch *watcher

	tabs     []tab
	active   int
	showHelp bool

	note   msg
	noteAt time.Time
	frame  int

	// fromTab is the tab the reader pane was opened from, or -1. The pane lives
	// on the queue, and every tab can open one — so closing it has to go back
	// where it was asked for rather than stranding you on the queue.
	fromTab int

	width, height int
	snap          snapshot
}

func New(l paths.Layout) *Model {
	c := state.NewCache()
	q, fu := newQueueTab(l, c, goalsFor(l)), newFollowupsTab(l, c)
	// The queue's stats pane shows the follow-up count, and only the follow-ups
	// tab knows it. Bound as a function rather than copied into the snapshot: the
	// two tabs load on different clocks, and a copy is a frame behind — the tab
	// bar said (1) while the pane beneath it still said 0.
	q.due = fu.Badge
	m := &Model{
		l: l, cache: c,
		keys:    newGlobalKeys(),
		help:    help.New(),
		queue:   q,
		watch:   newWatcher(),
		tabs:    []tab{q, newActivityTab(l, c), fu},
		fromTab: -1,
	}
	return m
}

// goalsFor reads the targets out of profile.yml, falling back to the defaults.
//
// Soft on purpose. config.Load treats a missing cv.md as fatal because the
// pipeline cannot run without one, but the TUI can — and refusing to start the
// only screen that would have told you what is missing helps nobody.
func goalsFor(l paths.Layout) config.Goals {
	if cfg, err := config.Load(l); err == nil {
		return cfg.Goals
	}
	return config.DefaultGoals
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(pollCmd(m.l, m.cache, m.watch), tickCmd(false))
}

func (m *Model) Update(raw tea.Msg) (tea.Model, tea.Cmd) {
	switch t := raw.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = t.Width, t.Height
		// Minus the footer's gutter, so help truncates at the column the hint
		// actually ends at rather than one past it.
		m.help.SetWidth(max(1, t.Width-4))
		m.resizeTabs()
		return m, nil

	case snapshotMsg:
		m.snap = snapshot(t)
		for _, tb := range m.tabs {
			tb.setSnapshot(m.snap)
		}
		if m.snap.err != nil {
			m.setNote(msg{text: m.snap.err.Error(), isErr: true})
		}
		return m, nil

	case tickMsg:
		m.frame++
		for _, tb := range m.tabs {
			tb.setFrame(m.frame)
		}
		// The footer message is feedback on the last keypress, not a log: it is
		// swept, with no countdown. The sweep rides this tick, so a message
		// lives 5 s plus at most one tick.
		if m.note.text != "" && time.Since(m.noteAt) >= msgTTL {
			m.note = msg{}
		}
		// Fast ticks are for the spinners, and only a live run has any. In slow
		// mode every tick IS a poll interval, so both halves of this read disk
		// once a second whatever the rate.
		animating := m.snap.running
		if !animating || m.frame%framesPerPoll == 0 {
			return m, tea.Batch(pollCmd(m.l, m.cache, m.watch), tickCmd(animating))
		}
		return m, tickCmd(animating)

	case showDayMsg:
		// The switch is the queue's call, not the activity tab's: a day with
		// nothing in it says so and leaves you where you were, rather than
		// dropping you into a list that does not contain what you asked for.
		cmd, ok := m.queue.focusDay(string(t))
		if ok {
			m.setTab(0)
		}
		return m, cmd

	case detailMsg:
		// Routed to the queue rather than delegated to the active tab. The
		// reader pane lives there, and Update only ever reaches the tab you are
		// on — which is why `o` on the follow-ups board used to open nothing at
		// all: it produced this message and nobody was listening.
		_, cmd := m.queue.Update(t)
		if m.queue.detailOpen && m.active != 0 {
			m.fromTab = m.active
			m.setTab(0)
		}
		return m, cmd

	case reviewMsg, reviewDoneMsg:
		// The skills screen lives on the queue, like the reader pane; a key
		// that changed tabs before the files were read must not drop it.
		_, cmd := m.queue.Update(t)
		return m, cmd

	case refreshMsg:
		// Something the UI is waiting on changed out of process — a spawned run
		// taking the lock. Nothing to say, only something to look at again.
		return m, pollCmd(m.l, m.cache, m.watch)

	case msg:
		m.setNote(t)
		// Re-read now rather than at the next tick. An action writes its change
		// to disk BEFORE it reports, and everything the panes draw comes from
		// the snapshot — so without this the queue sat a whole poll behind
		// itself: the note said "running #123" while the list still said
		// "Nothing yet". Navigation felt instant by comparison because it only
		// ever moves an in-memory cursor.
		//
		// One line covers every action because every action reports through a
		// msg and nothing else produces one. A poll is 4–13 ms over a few
		// hundred cached artifacts, so the extra read is not worth conditioning.
		return m, pollCmd(m.l, m.cache, m.watch)
	}

	key, isKey := raw.(tea.KeyPressMsg)

	// The help overlay is closed by the next keypress — Ctrl-C excepted, since
	// a help screen must not trap a quit.
	if m.showHelp && isKey {
		if key.String() == "ctrl+c" {
			return m, tea.Quit
		}
		m.showHelp = false
		return m, nil
	}

	// Delegate first. The globals are the fallback, not the gate.
	if handled, cmd := m.tabs[m.active].Update(raw); handled {
		if m.fromTab >= 0 && !m.queue.detailOpen {
			m.setTab(m.fromTab) // the key that closed a borrowed reader
			m.fromTab = -1
		}
		return m, cmd
	}
	if !isKey {
		return m, nil
	}
	return m, m.globalKey(key)
}

func (m *Model) globalKey(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, m.keys.Quit):
		return tea.Quit
	case key.Matches(k, m.keys.Help):
		m.showHelp = true
		return nil
	case key.Matches(k, m.keys.NextTab):
		m.setTab(m.active + 1)
		return nil
	case key.Matches(k, m.keys.PrevTab):
		m.setTab(m.active - 1)
		return nil
	case key.Matches(k, m.keys.Tab1):
		m.setTab(0)
		return nil
	case key.Matches(k, m.keys.Tab2):
		m.setTab(1)
		return nil
	case key.Matches(k, m.keys.Tab3):
		m.setTab(2)
		return nil
	case key.Matches(k, m.keys.Stop):
		return stopRunner(m.l)
	case key.Matches(k, m.keys.Pause):
		return pauseRunner(m.l)
	}
	return nil
}

func (m *Model) setTab(i int) {
	n := len(m.tabs)
	m.active = ((i % n) + n) % n
	m.note = msg{}
}

func (m *Model) setNote(n msg) {
	m.note = n
	m.noteAt = time.Now()
}

// bodyHeight is the screen minus the chrome: the tab bar and the footer. The
// bordered frame is part of the body, not chrome around it.
//
// Read by both resizeTabs and View, so the height a tab is given is the height
// it is drawn into. Two expressions is how those drift.
func (m *Model) bodyHeight() int {
	return max(1, m.height-lipgloss.Height(m.tabBar())-1)
}

// framed reports whether there is room to spend two rows and two columns on a
// border. Below that it costs more than it gives — the same trade the stats pane
// makes on width, and the reason a four-row terminal still renders something
// usable rather than a box with no room inside it.
func (m *Model) framed() bool { return m.height >= 8 && m.width >= 24 }

// insetW and insetH are what a tab actually gets to draw on: the frame minus its
// border and its horizontal padding, or the whole body when there is no frame.
func (m *Model) insetW() int {
	if m.framed() {
		return max(1, m.width-4)
	}
	return max(1, m.width)
}

func (m *Model) insetH() int {
	if m.framed() {
		return max(1, m.bodyHeight()-2)
	}
	return m.bodyHeight()
}

func (m *Model) resizeTabs() {
	for _, tb := range m.tabs {
		tb.setSize(m.insetW(), m.insetH())
	}
}

func (m *Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	var body string
	if m.showHelp {
		body = m.helpView()
	} else {
		body = m.tabs[m.active].View()
	}
	// The body is held to exactly the frame it was allotted — padded when it
	// draws less, truncated when it draws more. Both halves matter and neither
	// tab can do it for itself: an empty queue drew three lines and left the
	// hints stranded mid-screen, while a short terminal drew ten into five and
	// pushed the footer off the bottom.
	h := m.bodyHeight()
	frame := lipgloss.NewStyle().Height(h).MaxHeight(h).Render(body)
	if m.framed() {
		frame = styBox.Width(m.width).Height(h).
			MaxWidth(m.width).MaxHeight(h).Render(body)
	}

	v := tea.NewView(clampWidth(lipgloss.JoinVertical(lipgloss.Left,
		m.tabBar(),
		frame,
		m.footer(),
	), m.width))
	// v2 declares the terminal mode on the view rather than as a program
	// option, so the alt screen lives here.
	v.AltScreen = true
	return v
}

// clampWidth truncates every line to the terminal width.
//
// One guarantee in one place, rather than making six components each defend
// against a pathological size. It also contains a property of JoinVertical that
// is easy to miss: it pads every line to the width of the widest one, so a
// single overflowing component pushes the whole layout past the edge and wraps
// it — which looks like every pane is broken rather than one.
func clampWidth(s string, w int) string {
	if w <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > w {
			lines[i] = truncate(line, w)
		}
	}
	return strings.Join(lines, "\n")
}

// tabBar is centred over the frame: three labels with a wide gap, the active one
// bold and gold, the rest dim.
//
// Bold and coloured marks "you are here" — deliberately not underlined, so it
// does not read as a focused menu selection. Widget focus is a separate thing
// and starts at none.
func (m *Model) tabBar() string {
	parts := make([]string, 0, len(m.tabs))
	for i, tb := range m.tabs {
		label := fmt.Sprintf("%d %s", i+1, strings.ToUpper(tb.Title()))
		if i == m.active {
			label = styTabOn.Render(label)
		} else {
			label = styTabOff.Render(label)
		}
		if b := tb.Badge(); b > 0 {
			label += styAccent.Render(fmt.Sprintf(" (%d)", b))
		}
		parts = append(parts, label)
	}
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center,
		strings.Join(parts, "    "))
}

// footer carries the contextual hint on the left and the last keypress's
// feedback on the right. Both come from one place: the hint is rendered from
// the focused component's own bindings, so it cannot drift from what the keys
// actually do.
func (m *Model) footer() string {
	hint := m.help.ShortHelpView(append(m.tabs[m.active].ShortHelp(), m.keys.Help))
	right := ""
	if m.note.text != "" {
		if m.note.isErr {
			right = styErr.Render(m.note.text)
		} else {
			right = styFaint.Render(m.note.text)
		}
	}
	// Two columns of gutter, so the hint starts under the frame's own padding
	// rather than under its border.
	w := m.width - 4
	if w < 1 {
		return ""
	}
	gap := w - lipgloss.Width(hint) - lipgloss.Width(right)
	if gap < 1 {
		// Narrow window: the message wins — it is the answer to what was just
		// pressed, and the hint is one `?` away.
		if right != "" {
			return "  " + truncate(right, w)
		}
		return "  " + truncate(hint, w)
	}
	return "  " + hint + strings.Repeat(" ", gap) + right
}

// helpView is the ? overlay: every binding the active tab knows about, plus the
// globals. Same key.Binding values the footer renders, so there is exactly one
// list to maintain.
func (m *Model) helpView() string {
	groups := append(m.tabs[m.active].FullHelp(), []key.Binding{
		m.keys.PrevTab, m.keys.NextTab, m.keys.Stop, m.keys.Pause, m.keys.Quit,
	})
	return m.help.FullHelpView(groups)
}
