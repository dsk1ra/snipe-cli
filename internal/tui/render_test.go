package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// A render pass over every tab at several sizes. It asserts little beyond "does
// not panic and does not overflow", which is most of what goes wrong in a
// terminal UI: an index off the end of a row's actions, a negative width in a
// repeat, a division by a zero cell count.
func TestRenderEveryTabAtEverySize(t *testing.T) {
	m := New(paths.Layout{Home: t.TempDir(), Code: t.TempDir()})

	rows := map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P1Score: "4.4", P2Status: state.P2Evaled, ReportNum: "078", P3Status: state.P3Completed},
		2: {ID: 2, P1Status: state.P1Scored, P1Score: "2.0", P2Status: state.P2Gated, P3Status: state.P3Skipped},
		3: {ID: 3, P1Status: state.P1Scored, P2Status: state.P2Failed, Error: "ollama timeout"},
		4: {ID: 4, P1Status: state.P1Unavailable, Error: "HTTP 404 — posting expired"},
		5: {ID: 5},
	}
	offers := map[int]state.Offer{
		1: {ID: 1, URL: "https://example.test/a"},
		2: {ID: 2, URL: "https://example.test/b"},
		5: {ID: 5, URL: "manual:snipe-5"},
	}
	s := snap(rows, offers)
	s.running = true
	s.pid = 4242
	s.queue = []int{6, 7}
	s.stats = state.Lifetime(rows)
	s.avg, s.avgOK = 3.4, true

	// A quarter-screen terminal is the normal case here, not the edge case.
	for _, size := range [][2]int{{40, 12}, {80, 24}, {200, 60}, {20, 6}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.Update(snapshotMsg(s))
		for tab := 0; tab < 3; tab++ {
			m.setTab(tab)
			view := m.View().Content
			for i, line := range strings.Split(view, "\n") {
				if w := lineWidth(line); w > size[0] {
					t.Errorf("%dx%d tab %d line %d is %d cols wide: %q",
						size[0], size[1], tab, i, w, line)
				}
			}
		}
	}
}

// lineWidth measures printable columns independently of lipgloss, so the test
// checks the layout rather than agreeing with the library about it.
//
// OSC and CSI have to be told apart by their introducer. Terminating both on
// the first "m" — the CSI final byte for SGR — walks straight into any URL
// containing that letter, and "example" does.
func lineWidth(s string) int {
	n, i := 0, 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case ']': // OSC: ends at BEL or ST (ESC \)
				j := i + 2
				for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
					j++
				}
				if j < len(s) && s[j] == 0x1b {
					j++
				}
				i = j + 1
				continue
			case '[': // CSI: ends at a final byte in @ through ~
				j := i + 2
				for j < len(s) && (s[j] < '@' || s[j] > '~') {
					j++
				}
				i = j + 1
				continue
			}
		}
		r := []rune(s[i:])[0]
		n += lipgloss.Width(string(r))
		i += len(string(r))
	}
	return n
}

// Walking every action on every row kind, then firing each, must not panic — the
// side effects are all guarded by "does this exist" checks rather than by the
// caller happening to be on a valid stop.
func TestWalkAndFireEveryStop(t *testing.T) {
	rows := map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "078", P3Status: state.P3Completed},
		2: {ID: 2, P1Status: state.P1Scored, P1Score: "2.0", P2Status: state.P2Gated},
		3: {ID: 3, P2Status: state.P2Failed},
		4: {ID: 4, P1Status: state.P1Unavailable},
	}
	q := newTestQueue(t, snap(rows, map[int]state.Offer{1: {ID: 1, URL: "https://example.test/a"}}))
	q.setFocus(fList)
	for i := range q.snap.session {
		q.cursor = i
		for act := 0; act < q.current().stops(); act++ {
			q.act = act
			// The command is not run — that would open a browser — but building
			// it exercises every lookup the action does.
			if cmd := q.fire(); cmd != nil {
				_ = cmd
			}
		}
	}
}

// The frame fills the screen and the footer is its last row.
//
// An empty queue draws three lines of body, and without the pad the hints
// rendered directly under them — stranded mid-screen with thirty blank rows
// below. The tab a body comes from cannot fix that: only the root knows how much
// height there was to fill.
func TestFrameFillsTheScreenWithTheFooterLast(t *testing.T) {
	m := New(paths.Layout{Home: t.TempDir(), Code: t.TempDir()})

	for _, size := range []struct{ w, h int }{{120, 40}, {80, 24}, {60, 8}, {40, 4}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		for tab := 0; tab < 3; tab++ {
			m.active = tab
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size.h {
				t.Errorf("%dx%d tab %d: %d lines, want %d", size.w, size.h, tab, len(lines), size.h)
				continue
			}
			// Non-blank is the whole point: a stranded footer leaves blank rows
			// BELOW it. At a comfortable size it must be the footer verbatim; at
			// a cramped one clampWidth has had to shorten the padded row, so only
			// the invariant survives.
			last := strings.TrimRight(lines[len(lines)-1], " ")
			if last == "" {
				t.Errorf("%dx%d tab %d: last row is blank", size.w, size.h, tab)
			}
			if size.w == 120 && size.h == 40 && last != strings.TrimRight(m.footer(), " ") {
				t.Errorf("%dx%d tab %d: last row is not the footer:\n got %q\nwant %q",
					size.w, size.h, tab, last, m.footer())
			}
			for i, l := range lines {
				if lipgloss.Width(l) > size.w {
					t.Errorf("%dx%d tab %d: row %d is %d wide", size.w, size.h, tab, i, lipgloss.Width(l))
				}
			}
			// The border is spent only where there is room for it. A four-row
			// terminal that draws one costs two of its four rows on decoration
			// and has nothing left to decorate.
			bordered := strings.Contains(lines[1], "╭")
			if want := m.framed(); bordered != want {
				t.Errorf("%dx%d tab %d: bordered=%v, want %v", size.w, size.h, tab, bordered, want)
			}
		}
	}
}

// Every column lands at the same screen offset on every row.
//
// This is the whole of what turns twenty rows into a table rather than a wall:
// a score that moves left and right with the length of the company name has to
// be re-found on each line.
func TestColumnsLineUp(t *testing.T) {
	// Sentinels, so a match cannot be part of a label. The markers are
	// deliberately multi-byte — an offset counted in bytes rather than display
	// columns reports a false misalignment for exactly these rows.
	cells := []rowCells{
		{marker: "✓", mark: "  ", label: "x", score: "S", actions: "A"},
		{marker: "·", mark: " >", label: strings.Repeat("y", 40), score: "SSSSS", actions: "AAAA"},
		{marker: "✗", mark: " -", label: strings.Repeat("z", 200), score: "S", actions: "A"},
	}
	const w = 100
	lines := layoutRows(cells, w, false)
	show := func() string { return "\n" + strings.Join(lines, "\n") }

	// Both remaining columns are right-aligned, so it is their RIGHT edge that
	// lines up.
	edges := []struct {
		name     string
		sentinel string
		content  func(rowCells) string // non-nil for a right-aligned column
	}{
		{"score", "S", func(c rowCells) string { return c.score }},
		{"actions", "A", func(c rowCells) string { return c.actions }},
	}

	for _, e := range edges {
		want := -1
		for i, l := range lines {
			at := columnOf(l, e.sentinel)
			if at < 0 {
				continue // this row does not use the column
			}
			if e.content != nil {
				at += lipgloss.Width(e.content(cells[i]))
			}
			if want < 0 {
				want = at
				continue
			}
			if at != want {
				t.Errorf("%s column: row %d at %d, row 0 at %d%s", e.name, i, at, want, show())
			}
		}
	}
	for i, l := range lines {
		if lipgloss.Width(l) > w {
			t.Errorf("row %d is %d wide, want <= %d%s", i, lipgloss.Width(l), w, show())
		}
	}
}

// A column no row uses costs nothing, and the space goes to the label — which
// is the field that actually wants it, since it is the one being truncated.
func TestAnUnusedColumnWidensTheLabel(t *testing.T) {
	// Long enough that it is still truncated with the score column GONE —
	// otherwise the ellipsis this measures is not there to find.
	long := strings.Repeat("a company name far too long for any sensible column ", 3)
	row := func(score string) string {
		return stripANSI(layoutRows([]rowCells{
			{marker: "✓", mark: "  ", label: long, score: score, actions: "link"},
		}, 80, false)[0])
	}
	// The label is what gets truncated, so the ellipsis is where the extra room
	// shows up: the actions stay pinned to the right edge either way.
	shown := func(line string) int { return strings.Index(line, "…") }

	with, without := row("P2 4.8"), row("")
	if shown(without) <= shown(with) {
		t.Errorf("dropping the score column did not widen the label:\n%s\n%s", with, without)
	}
	if got, want := shown(without)-shown(with), len("P2 4.8")+2; got != want {
		t.Errorf("label gained %d columns, want the score column and its gap (%d)", got, want)
	}
}

// columnOf is where a substring starts, measured in DISPLAY columns. strings.Index
// returns a byte offset, and a row whose marker is "✓" is two bytes wider than
// its own left edge.
func columnOf(line, sentinel string) int {
	plain := stripANSI(line)
	i := strings.Index(plain, sentinel)
	if i < 0 {
		return -1
	}
	return lipgloss.Width(plain[:i])
}

// The actions column does not resize as the cursor moves.
//
// A focused row shows its routine actions whatever the width — they are what →
// is about to walk. If the column were sized to whichever row happens to be
// focused, moving between a row offering "proceed? | link" and one offering
// "report | link" would shift every label on screen by two columns.
func TestTheActionsColumnHoldsStillWhileNavigating(t *testing.T) {
	const w = 64
	rows := []rowCells{
		{marker: "✓", mark: "  ", label: "a done row with a long enough name", score: "4.8", routine: "report | link"},
		{marker: "·", mark: "  ", label: "a gated row with a long enough name", score: "P1 2.4", actions: "proceed?", routine: "link"},
	}

	widths := map[int]bool{}
	for focus := range rows {
		cells := append([]rowCells{}, rows...)
		cells[focus].focused = true
		lines := layoutRows(cells, w, true)
		// Where the score ends is where the label column ends.
		widths[columnOf(lines[0], "4.8")] = true
	}
	if len(widths) != 1 {
		t.Errorf("the label column moved as focus changed: score seen at %v", widths)
	}
}

// Narrow enough, and the routine actions give way to the label. They are the
// same two words on almost every row and one keystroke away; a company name
// truncated to nothing is not recoverable by any keystroke.
func TestRoutineActionsYieldToTheLabel(t *testing.T) {
	rows := []rowCells{
		{marker: "✓", mark: "  ", label: "Information Tech Consultants Ltd — Java Developer", score: "4.8", routine: "report | link"},
		{marker: "·", mark: "  ", label: "Engine by Starling — Staff Software Engineer", score: "P1 2.4", actions: "proceed?", routine: "link"},
	}
	wide := stripANSI(layoutRows(rows, 120, false)[0])
	narrow := stripANSI(layoutRows(rows, 56, false)[0])

	if !strings.Contains(wide, "report") {
		t.Errorf("a wide window dropped the routine actions:\n%s", wide)
	}
	if strings.Contains(narrow, "report") {
		t.Errorf("a narrow window kept them at the label's expense:\n%s", narrow)
	}
	// The row that is asking a question keeps its action at every width.
	if !strings.Contains(stripANSI(layoutRows(rows, 56, false)[1]), "proceed?") {
		t.Error("a gated row lost the one action that is a question")
	}
}

// Exactly one action on the whole list is ever shown as focused.
//
// The action index is per-list, not per-row, so "is this the focused action"
// has to be asked together with "is this the focused row". Asking only the
// first lit up the same slot on every row at once: with the cursor on one
// row's "report", every other row's first action was inverted too.
func TestOnlyTheCursorsActionIsHighlighted(t *testing.T) {
	rows := map[int]state.Row{}
	offers := map[int]state.Offer{}
	labels := map[int]string{}
	var sess []int
	for id := 1; id <= 5; id++ {
		rows[id] = state.Row{ID: id, P1Status: state.P1Scored, P1Score: "4.0",
			P2Status: state.P2Evaled, ReportNum: "07" + strconv.Itoa(id), P3Status: state.P3Completed}
		offers[id] = state.Offer{ID: id, URL: "https://example.test/x"}
		labels[id] = "Company " + strconv.Itoa(id) + " — Some Role"
		sess = append(sess, id)
	}
	s := snapshot{rows: rows, offers: offers, labels: labels, session: sess, scores: map[int]float64{}}

	q := newQueueTab(paths.Layout{Home: t.TempDir(), Code: t.TempDir()}, state.NewCache(), config.DefaultGoals)
	q.setSize(110, 20)
	q.setSnapshot(s)
	// listView windows against this, and it is queuePane that normally sets it.
	// Left at zero the window is one row, and a bug that lights up every row
	// has only one row to light up.
	q.listWindow = len(sess) + 4 // the indicator rows and the day heading
	q.setFocus(fList)
	q.cursor = 2

	// Walk every action slot, including the row body.
	for act := 0; act <= 2; act++ {
		q.act = act
		lit := 0
		for _, line := range strings.Split(q.listView(110), "\n") {
			lit += strings.Count(line, "\x1b[7m")
		}
		if lit > 1 {
			t.Errorf("act=%d: %d highlighted spans across the list, want at most 1", act, lit)
		}
	}
}

// A pasted job link is 400 columns of tracking parameters. The queue pane is
// rendered at a Width that WRAPS rather than cuts, so an untruncated one added
// five rows to a pane whose height was already spent — and what MaxHeight then
// clipped off the bottom was the Add button and the frame's own border.
func TestALongURLDoesNotPushTheComposerOffTheFrame(t *testing.T) {
	long := "https://www.linkedin.com/jobs/search-results/?currentJobId=4457808322&eBP=" +
		strings.Repeat("CwEAAAGgKV1HLNRHRwdqGmIq5aWol", 12)

	for _, w := range []int{28, 60, 80, 120, 200} {
		m := New(paths.Layout{Home: t.TempDir(), Code: t.TempDir()})
		m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
		q := m.tabs[0].(*queueTab)
		q.url.SetValue(long)
		q.jd.SetValue(strings.Repeat("x", 1884))

		if got := lipgloss.Height(q.View()); got > q.height {
			t.Errorf("w=%d: the pane is %d lines in a %d-line budget — the overflow is clipped off the bottom",
				w, got, q.height)
		}
		frame := stripANSI(m.View().Content)
		if lipgloss.Height(frame) > 30 {
			t.Errorf("w=%d: the frame is %d lines on a 30-line terminal", w, lipgloss.Height(frame))
		}
		// The button the overflow took with it.
		if w >= 60 && !strings.Contains(frame, "Add to queue") {
			t.Errorf("w=%d: the Add button is off screen:\n%s", w, frame)
		}
	}
}

// datedSession builds a session of scored, evaluated offers whose days are the
// given whole-day offsets back from now — one entry per row, oldest first, the
// order Timeline hands them over in.
func datedSession(offsets []int) snapshot {
	s := snapshot{
		rows: map[int]state.Row{}, offers: map[int]state.Offer{},
		labels: map[int]string{}, scores: map[int]float64{}, days: map[int]time.Time{},
		applied: state.Marks{}, skipped: state.Marks{}, declined: state.Marks{},
	}
	for i, off := range offsets {
		id := i + 1
		s.rows[id] = state.Row{ID: id, P1Status: state.P1Scored, P1Score: "4.0",
			P2Status: state.P2Evaled, ReportNum: "078", P3Status: state.P3Completed}
		s.offers[id] = state.Offer{ID: id, URL: "https://example.test/x"}
		s.labels[id] = "Company " + strconv.Itoa(id) + " — Some Role"
		s.scores[id] = 4.0
		s.days[id] = time.Now().AddDate(0, 0, -off)
		s.session = append(s.session, id)
	}
	return s
}

func newDatedQueue(t *testing.T, offsets []int, window int) *queueTab {
	t.Helper()
	q := newQueueTab(paths.Layout{Home: t.TempDir(), Code: t.TempDir()}, state.NewCache(), config.DefaultGoals)
	q.setSize(90, window+9) // +9 is the heading, its blank, and the composer
	q.setSnapshot(datedSession(offsets))
	// listView windows against this, and it is queuePane that normally sets it.
	q.listWindow = window
	return q
}

// The list is a log grouped by day and filled from the BOTTOM: a dated heading
// opens each run of same-day rows, the newest row ends up nearest the composer,
// and the whole thing fits the window it was given.
//
// The window has to count LINES, not rows, because a heading is a line too and
// how many headings fall inside a window depends on where that window starts.
// Filling upward from a known end is what makes that answerable in one pass;
// forward from a guessed start, every row added can change whether the row above
// it still needed a heading.
func TestListGroupsByDayFillingFromTheBottom(t *testing.T) {
	// Three groups. A window of 9 is two indicator rows plus seven lines, which
	// buys three headings and four rows — so the oldest two rows fall off the
	// top, not the newest.
	q := newDatedQueue(t, []int{5, 5, 5, 2, 2, 0}, 9)

	lines := strings.Split(q.listView(90), "\n")
	if len(lines) != 9 {
		t.Fatalf("rendered %d lines into a window of 9:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	var heads, rows []string
	for _, l := range lines[1 : len(lines)-1] { // between the two indicators
		if plain := stripANSI(l); strings.HasPrefix(plain, "── ") {
			heads = append(heads, plain)
		} else {
			rows = append(rows, plain)
		}
	}
	if len(heads) != 3 {
		t.Errorf("got %d day headings, want one per group: %q", len(heads), heads)
	}
	if !strings.HasPrefix(heads[2], "── today ") {
		t.Errorf("last heading is %q, want today's", heads[2])
	}
	for i, want := range []string{"Company 3", "Company 4", "Company 5", "Company 6"} {
		if i >= len(rows) || !strings.Contains(rows[i], want) {
			t.Fatalf("row %d is %q, want %s — the window kept the wrong end", i, rows, want)
		}
	}
	// The newest row sits on the composer, with only the blank ↓ indicator
	// between: that is the whole point of anchoring to the bottom.
	if !strings.Contains(rows[len(rows)-1], "Company 6") {
		t.Errorf("last row is %q, want the newest", rows[len(rows)-1])
	}
	if got := strings.TrimSpace(stripANSI(lines[0])); got != "↑ 2 more" {
		t.Errorf("top indicator says %q, want the two rows that fell off", got)
	}
}

// Scrolling up carries the window with the cursor, and the cursor's row is drawn
// wherever it lands.
//
// The window is derived on every frame from the cursor rather than nudged by the
// movement keys, so this is also what stops a cursor moved by a poll and a
// cursor moved by a key from disagreeing.
func TestCursorStaysInsideTheWindow(t *testing.T) {
	q := newDatedQueue(t, []int{5, 5, 5, 2, 2, 0}, 9)
	q.setFocus(fList) // lands on the newest row

	for range 5 { // and now to the oldest
		q.moveUp()
	}
	if q.cursor != 0 {
		t.Fatalf("cursor is at %d, want the top of the session", q.cursor)
	}
	out := stripANSI(q.listView(90))
	if !strings.Contains(out, "Company 1") {
		t.Errorf("the cursor's own row is not in the window:\n%s", out)
	}
	if strings.Contains(out, "Company 6") {
		t.Errorf("the window did not move with the cursor:\n%s", out)
	}
}

// A window with no room for both drops the heading, not the row.
//
// Reserving the two indicator rows leaves a four-row pane one line for content.
// Spending it on a dated rule renders a date over nothing, which is strictly
// less than the row it displaced.
func TestATinyWindowKeepsTheRowOverTheHeading(t *testing.T) {
	q := newDatedQueue(t, []int{5, 5, 2, 0}, 2)

	out := stripANSI(q.listView(90))
	if strings.Contains(out, "── ") {
		t.Errorf("drew a day heading with no room for it:\n%s", out)
	}
	if !strings.Contains(out, "Company 4") {
		t.Errorf("drew no row at all:\n%s", out)
	}
}

// Both ways into the list land on the newest row.
//
// setFocus has always opened it there, but the ↓ path reset the cursor to 0
// straight afterwards — so Tab entered at the bottom of the list and ↓ entered
// at the top of history, several hundred rows from anything recent. ↓ is the one
// people press.
func TestBothWaysIntoTheListLandOnTheNewestRow(t *testing.T) {
	last := len(datedSession([]int{5, 5, 2, 0}).session) - 1

	for _, tc := range []struct {
		name  string
		enter func(*queueTab)
	}{
		{"↓ from the tab level", func(q *queueTab) { q.Update(keyMsg("down")) }},
		{"tab cycling round", func(q *queueTab) {
			for range 3 {
				q.Update(keyMsg("tab"))
			}
		}},
	} {
		q := newDatedQueue(t, []int{5, 5, 2, 0}, 12)
		tc.enter(q)

		if q.focus != fList {
			t.Errorf("%s: focus is %v, want the list", tc.name, q.focus)
		}
		if q.cursor != last {
			t.Errorf("%s: cursor at %d, want the newest row (%d)", tc.name, q.cursor, last)
		}
	}
}

// ↑ off the newest row still walks back through history rather than stepping
// straight out — entering at the bottom is only useful if that is where the
// reading starts.
func TestUpFromTheEntryRowWalksBack(t *testing.T) {
	q := newDatedQueue(t, []int{5, 5, 2, 0}, 12)
	q.Update(keyMsg("down"))
	q.Update(keyMsg("up"))

	if q.focus != fList {
		t.Fatalf("↑ stepped out of the list instead of moving in it")
	}
	if want := len(q.snap.session) - 2; q.cursor != want {
		t.Errorf("cursor at %d, want %d", q.cursor, want)
	}
}

// The side pane is goals over counters, pinned to opposite ends of the column.
func TestTheSidePaneHoldsGoalsAboveTheCounters(t *testing.T) {
	q := newDatedQueue(t, []int{2, 0}, 12)
	q.height = 40
	out := stripANSI(q.sidePane())

	goals, stats := strings.Index(out, "Goals"), strings.Index(out, "Stats")
	if goals < 0 || stats < 0 {
		t.Fatalf("a block is missing:\n%s", out)
	}
	if goals > stats {
		t.Errorf("the counters came out above the goals:\n%s", out)
	}
	if want := "scans today"; !strings.Contains(out, want) {
		t.Errorf("no %q row:\n%s", want, out)
	}
	// The counters end the column: whatever slack there is opens between them.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "Follow-ups") {
		t.Errorf("the column does not end on the counters, it ends on %q", last)
	}
}

// A column too short for both keeps the counters and drops the goals whole.
//
// Trimmed by the line instead, all that survived at eighteen rows was the
// "Goals" heading with nothing underneath it — a label over empty space, paid
// for out of the rows the counters needed.
func TestAShortColumnDropsTheGoalsWhole(t *testing.T) {
	q := newDatedQueue(t, []int{2, 0}, 12)

	for _, h := range []int{6, 10, 14} {
		q.height = h
		out := stripANSI(q.sidePane())
		if !strings.Contains(out, "Queue") {
			t.Errorf("h=%d: the counters were dropped:\n%s", h, out)
		}
		if strings.Contains(out, "Goals") && !strings.Contains(out, "scans today") {
			t.Errorf("h=%d: the goals heading survived alone:\n%s", h, out)
		}
	}
}

// A bar lights at least one cell for any progress at all, and bands the same way
// the score column does.
func TestTheGoalBarNeverRoundsProgressToNothing(t *testing.T) {
	if got := stripANSI(bar(1, 20, 10)); !strings.HasPrefix(got, "▓") {
		t.Errorf("one of twenty drew %q, want a lit cell", got)
	}
	if got := stripANSI(bar(0, 10, 10)); strings.Contains(got, "▓") {
		t.Errorf("nothing done drew %q, want an empty bar", got)
	}
	if got := stripANSI(bar(99, 10, 10)); strings.Contains(got, "░") {
		t.Errorf("target beaten drew %q, want it full", got)
	}
	if bar(5, 0, 10) != "" {
		t.Error("an unset target still drew a bar")
	}
}

// A pasted job description must not cost the frame it lands on.
//
// truncate used to drop one rune and re-measure the whole string, so showing
// the first 90 columns of an 8 KB paste walked 8,000 × 8,000 cells. Every
// frame paid it, which is why a paste appeared to arrive about a second late:
// the render was still drawing the frame before it. 780 ms then, ~1.5 ms now.
//
// The budget is deliberately loose — this catches an order of magnitude, not a
// slow machine.
func TestAPastedJDDoesNotCostTheFrame(t *testing.T) {
	q := newTestQueue(t, snap(map[int]state.Row{}, nil))
	q.setFocus(fJD)
	q.onPaste(strings.Repeat("Senior Go engineer, remote, Linux tooling. ", 500)) // ~21 KB

	start := time.Now()
	q.View()
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("one frame took %v — truncate is walking the whole paste again", d)
	}
	if !strings.Contains(stripANSI(q.View()), "21,500 chars") {
		t.Error("the field stopped reporting what it holds")
	}
}
