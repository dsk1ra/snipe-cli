package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

type actView int

const (
	viewYear actView = iota
	viewMonth
	viewDay
)

// The two heat-map ramps, one per series, each four steps from barely-lit to
// bright. Nothing in bubbles draws a heat map, so this stays hand-rolled — it is
// the one widget in the app that is genuinely custom.
//
// Both are the app's own warm family rather than the contribution-graph greens
// they replaced: yellow runs up to the gold accent the tab bar and the actions
// already use, orange up to the warn colour. Hue tells you which series you are
// looking at, brightness tells you how much — so the series toggle now reads at
// a glance instead of only from the "> scans" marker under the grid.
var (
	yellows = []color.Color{
		lipgloss.Color("#4a3a00"),
		lipgloss.Color("#8a6d00"),
		lipgloss.Color("#d4af37"),
		lipgloss.Color("#ffe766"),
	}
	oranges = []color.Color{
		lipgloss.Color("#4a2200"),
		lipgloss.Color("#a35200"),
		lipgloss.Color("#ff8700"),
		lipgloss.Color("#ffc27a"),
	}
)

var gold = []color.Color{
	lipgloss.Color("#b8860b"), lipgloss.Color("#d4af37"), lipgloss.Color("#ffd700"),
	lipgloss.Color("#ffe766"), lipgloss.Color("#fff2a8"), lipgloss.Color("#ffe766"),
	lipgloss.Color("#ffd700"), lipgloss.Color("#d4af37"),
}

var monthNames = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

type activityTab struct {
	l     paths.Layout
	cache *state.Cache
	keys  activityKeys
	snap  snapshot

	view     actView
	showApps bool // false = scans
	at       time.Time
	hour     int
	cursor   bool // arrows move the selected cell rather than the period

	width, height int
}

func newActivityTab(l paths.Layout, c *state.Cache) *activityTab {
	now := time.Now()
	return &activityTab{l: l, cache: c, keys: newActivityKeys(), at: now, hour: now.Hour()}
}

func (a *activityTab) Title() string          { return "activity" }
func (a *activityTab) Badge() int             { return 0 }
func (a *activityTab) setSize(w, h int)       { a.width, a.height = w, h }
func (a *activityTab) setSnapshot(s snapshot) { a.snap = s }

// setFrame is a no-op: nothing on this tab steps per frame. The busiest-day
// shimmer runs on the wall clock instead, so it keeps its own pace whether the
// frame tick is fast (a run is spinning somewhere) or slow (nothing is).
func (a *activityTab) setFrame(int) {}

// shimmer is a whole second, so the gold ramp steps once a second however often
// the screen is redrawn.
func shimmer() int { return int(time.Now().Unix()) }
func (a *activityTab) buckets() state.Buckets {
	return a.pick(a.cache.Activity(a.l.Evals(), a.l.Applied()))
}
func (a *activityTab) pick(x state.Activity) state.Buckets {
	if a.showApps {
		return x.Apps
	}
	return x.Scans
}

func (a *activityTab) Update(m tea.Msg) (bool, tea.Cmd) {
	k, ok := m.(tea.KeyPressMsg)
	if !ok {
		return false, nil
	}
	switch {
	case key.Matches(k, a.keys.Year):
		a.view = viewYear
		return true, nil
	case key.Matches(k, a.keys.Month):
		a.view = viewMonth
		return true, nil
	case key.Matches(k, a.keys.Day):
		a.view = viewDay
		return true, nil
	case key.Matches(k, a.keys.Toggle):
		a.showApps = !a.showApps
		return true, nil
	// ‹ › step the period even in cell mode, so there is always a way out of a
	// year without walking to its edge.
	case key.Matches(k, a.keys.Prev):
		a.step(-1)
		return true, nil
	case key.Matches(k, a.keys.Next):
		a.step(1)
		return true, nil
	case key.Matches(k, a.keys.Cursor):
		a.cursor = !a.cursor
		return true, nil
	case key.Matches(k, a.keys.Open):
		// The tab does not know what the queue holds, so it asks rather than
		// navigates: the root switches only if the day has rows.
		return true, func() tea.Msg { return showDayMsg(state.DayKey(a.at)) }
	}

	if !a.cursor {
		return false, nil // ←→ fall through to the tab switch
	}
	switch k.String() {
	case "left":
		a.moveCell(-1, 0)
	case "right":
		a.moveCell(1, 0)
	case "up":
		a.moveCell(0, -1)
	case "down":
		a.moveCell(0, 1)
	case "esc":
		a.cursor = false
	default:
		return false, nil
	}
	return true, nil
}

func (a *activityTab) step(n int) {
	switch a.view {
	case viewYear:
		a.at = a.at.AddDate(n, 0, 0)
	case viewMonth:
		a.at = a.at.AddDate(0, n, 0)
	default:
		a.at = a.at.AddDate(0, 0, n)
	}
}

// moveCell walks the grid: in year/month a column is a week, so ←→ is ±7 days
// and ↑↓ is ±1. In day view ←→ steps an hour and rolls into the next day.
func (a *activityTab) moveCell(dx, dy int) {
	if a.view == viewDay {
		h := a.hour + dx + dy
		switch {
		case h < 0:
			h, a.at = 23, a.at.AddDate(0, 0, -1)
		case h > 23:
			h, a.at = 0, a.at.AddDate(0, 0, 1)
		}
		a.hour = h
		return
	}
	a.at = a.at.AddDate(0, 0, dx*7+dy)
}

func (a *activityTab) ShortHelp() []key.Binding {
	b := []key.Binding{a.keys.Year, a.keys.Month, a.keys.Day, a.keys.Toggle, a.keys.Prev, a.keys.Next}
	if a.view != viewDay && len(a.dayRows()) > 0 {
		b = append(b, a.keys.Open)
	}
	if a.cursor {
		return append(b, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "leave the cursor")))
	}
	return append(b, a.keys.Cursor)
}

func (a *activityTab) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{a.keys.Year, a.keys.Month, a.keys.Day},
		{a.keys.Prev, a.keys.Next, a.keys.Toggle},
		{a.keys.Cursor, a.keys.Open},
	}
}

// dayPaneW is the fixed right column. A year needs gutter + 53 weekly cells and
// not one column more, so on any terminal wide enough to draw one there is a
// column of waste beside it — which is exactly where the day being pointed at
// goes.
const dayPaneW = 40

// gridMinW is the narrowest the calendar can be and still say something: the
// weekday gutter and a season of weeks. Below the width where that and a day
// pane both fit, the pane is the one that goes — the same trade the queue's side
// pane makes.
const gridMinW = gutter + 13

func (a *activityTab) View() string {
	b := a.buckets()
	head := styHeading.Render(a.periodLabel())
	if a.cursor {
		head += styDim.Render("  cell cursor")
	}
	stats := a.statsBlock(b)
	h := max(3, a.height-lipgloss.Height(stats)-1)

	// The day view is its own readout already — 24 hourly bars across the full
	// width, with the selected hour named underneath. A second pane beside it
	// would take the columns the bars are drawn in to repeat what they say.
	if a.view == viewDay {
		return lipgloss.JoinVertical(lipgloss.Left, head, a.dayView(b, h-1), "", stats)
	}

	if a.width < gridMinW+1+dayPaneW {
		return lipgloss.JoinVertical(lipgloss.Left, head, a.heatmap(b, a.width), "", stats)
	}
	gridW := a.width - dayPaneW - 1
	left := lipgloss.JoinVertical(lipgloss.Left, head, a.heatmap(b, gridW-3))
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top,
			styRule.Width(gridW).Height(h).MaxHeight(h).Render(left),
			// PaddingLeft rather than " "+pane: JoinHorizontal lays out blocks,
			// so a concatenated space indents the first line and nothing else.
			lipgloss.NewStyle().PaddingLeft(1).Render(a.dayPane(dayPaneW, h))),
		"", stats)
}

// dayRows is the offers the queue files under the day the cursor is on.
//
// Filed by the queue's own day — when an offer first appeared — and not by the
// eval mtimes the heat map counts. They are different measures and they differ
// by more than rounding: an offer fetched on the 11th and evaluated on the 12th
// lands in one day here and the other there, and 59 of the JDs on disk have no
// eval at all. So the pane says "added" where the map says "scans", rather than
// putting two numbers that mean different things under one word — which is the
// mistake the "Rate" column had already made once.
func (a *activityTab) dayRows() []int {
	key := state.DayKey(a.at)
	var out []int
	for _, id := range a.snap.session {
		if t, ok := a.snap.days[id]; ok && state.DayKey(t) == key {
			out = append(out, id)
		}
	}
	return out
}

// dayPane is what the cursor is actually pointing at: the day, what it holds,
// and the way into it.
//
// Before this the cursor drew a mark under a cell and reported nothing at all —
// not the date, not the count. The heat map could tell you the 11th was busy and
// had no way to tell you what made it busy.
func (a *activityTab) dayPane(w, h int) string {
	ids := a.dayRows()
	lines := []string{styHeading.Render(truncate(a.at.Format("Mon 2 Jan 2006"), w))}

	applied := 0
	rows := make([]rowInfo, 0, len(ids))
	for _, id := range ids {
		info := describe(id, a.snap)
		if info.Applied {
			applied++
		}
		rows = append(rows, info)
	}
	if len(rows) == 0 {
		return strings.Join(append(lines, "", styDim.Render("nothing scanned")), "\n")
	}
	lines = append(lines,
		styFaint.Render(fmt.Sprintf("%d added · %d applied", len(rows), applied)), "")

	// Two rows are held back for the hint and its blank.
	window := max(1, h-len(lines)-2)
	shown := rows
	if len(shown) > window {
		shown = shown[:max(1, window-1)]
	}
	for _, info := range shown {
		score := info.scoreText()
		label := labelText(info.Label, w-4-lipgloss.Width(score)-1)
		lines = append(lines, spread(info.marker()+info.mark()+" "+label, score, w))
	}
	if n := len(rows) - len(shown); n > 0 {
		lines = append(lines, styDim.Render(fmt.Sprintf("… %d more", n)))
	}
	for len(lines) < h-1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, styDim.Render(truncate("o  open this day in the queue", w))), "\n")
}

func (a *activityTab) periodLabel() string { return "‹ " + a.periodName() + " ›" }

// periodDetail is the busiest slot inside the period on screen, what to call a
// slot, and how many of them saw any work.
//
// A day is measured in hours, not in days. Scoped by day it reported "busiest:
// 22 Aug (4), active days: 1" — the day you were already looking at, and the
// count of itself. True, and worth nothing.
func (a *activityTab) periodDetail(b state.Buckets) (busiest, unit string, active int) {
	if a.view == viewDay {
		best, bestH := 0, -1
		for h := range 24 {
			n := b.Hours[fmt.Sprintf("%s:%d", a.prefix(), h)]
			if n == 0 {
				continue
			}
			active++
			if n > best {
				best, bestH = n, h
			}
		}
		if bestH < 0 {
			return "—", "hour", 0
		}
		return fmt.Sprintf("%02d:00 (%d)", bestH, best), "hour", active
	}

	// Days worked, not days elapsed: 156 over eight days and 156 over thirty-one
	// are different months, and the total alone cannot tell them apart.
	busiest = "—"
	for k, v := range b.Days {
		if v == 0 || !strings.HasPrefix(k, a.prefix()) {
			continue
		}
		active++
	}
	if k, v := b.Best(a.prefix()); k != "" {
		if t, err := time.Parse("2006-01-02", k); err == nil {
			busiest = fmt.Sprintf("%d %s (%d)", t.Day(), monthNames[int(t.Month())-1], v)
		}
	}
	return busiest, "day", active
}

// periodName is the period without the stepper arrows, for labelling a number
// that belongs to it rather than to today.
func (a *activityTab) periodName() string {
	switch a.view {
	case viewYear:
		return fmt.Sprintf("%d", a.at.Year())
	case viewMonth:
		return fmt.Sprintf("%s %d", monthNames[int(a.at.Month())-1], a.at.Year())
	default:
		return fmt.Sprintf("%d %s %d", a.at.Day(), monthNames[int(a.at.Month())-1], a.at.Year())
	}
}

// prefix is the day-key prefix for whatever period is on screen.
func (a *activityTab) prefix() string {
	switch a.view {
	case viewYear:
		return a.at.Format("2006-")
	case viewMonth:
		return a.at.Format("2006-01")
	default:
		return a.at.Format("2006-01-02")
	}
}

func mondayOf(t time.Time) time.Time {
	off := (int(t.Weekday()) + 6) % 7 // Go's week starts Sunday; the grid's starts Monday
	return time.Date(t.Year(), t.Month(), t.Day()-off, 0, 0, 0, 0, t.Location())
}

// shade bands a count against the busiest cell on screen, in the ramp belonging
// to the series being drawn.
func (a *activityTab) shade(count, max int) color.Color {
	if max <= 0 || count <= 0 {
		return lipgloss.Color("236")
	}
	ramp := yellows
	if a.showApps {
		ramp = oranges
	}
	i := (count * len(ramp)) / (max + 1)
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	return ramp[i]
}

const gutter = 4 // weekday labels down the left

func (a *activityTab) heatmap(b state.Buckets, w int) string {
	var first, last time.Time
	if a.view == viewYear {
		first = time.Date(a.at.Year(), 1, 1, 0, 0, 0, 0, a.at.Location())
		last = time.Date(a.at.Year(), 12, 31, 0, 0, 0, 0, a.at.Location())
	} else {
		first = time.Date(a.at.Year(), a.at.Month(), 1, 0, 0, 0, 0, a.at.Location())
		last = first.AddDate(0, 1, -1)
	}
	inRange := func(t time.Time) bool { return !t.Before(first) && !t.After(last) }

	var cols []time.Time
	for w := mondayOf(first); !w.After(last); w = w.AddDate(0, 0, 7) {
		cols = append(cols, w)
	}
	if len(cols) == 0 {
		return ""
	}
	cellW := (w - gutter) / len(cols)
	switch {
	case a.view == viewYear && cellW > 3:
		cellW = 3
	case a.view == viewMonth && cellW > 8:
		cellW = 8
	}
	if cellW < 1 {
		cellW = 1
	}

	// A year is ~53 weeks, so below about 57 columns even a one-cell week does
	// not fit. Drop the oldest weeks rather than overflow: the recent end is the
	// part anyone is looking at, and one component wider than the terminal pads
	// every other line in the layout to match it.
	if fits := (w - gutter) / cellW; fits > 0 && len(cols) > fits {
		cols = cols[len(cols)-fits:]
	}

	max := 0
	for _, w := range cols {
		for r := 0; r < 7; r++ {
			d := w.AddDate(0, 0, r)
			if inRange(d) && b.Days[state.DayKey(d)] > max {
				max = b.Days[state.DayKey(d)]
			}
		}
	}

	weekdays := []string{"Mon", "", "Wed", "", "Fri", "", ""}
	sel := state.DayKey(a.at)
	var out strings.Builder
	for r := 0; r < 7; r++ {
		out.WriteString(styDim.Render(pad(weekdays[r], gutter)))
		for _, w := range cols {
			d := w.AddDate(0, 0, r)
			if !inRange(d) {
				out.WriteString(strings.Repeat(" ", cellW))
				continue
			}
			k := state.DayKey(d)
			cell := lipgloss.NewStyle().Background(a.shade(b.Days[k], max)).Render(strings.Repeat(" ", cellW))
			if a.cursor && k == sel {
				// White, not the gold accent: the accent IS the third step of
				// the yellow ramp now, so a gold mark on a gold grid marks
				// nothing. The cursor has to contrast with both series.
				cell = lipgloss.NewStyle().Foreground(colHeading).Render(strings.Repeat("▔", cellW))
			}
			out.WriteString(cell)
		}
		out.WriteByte('\n')
	}
	out.WriteString(a.axis(cols, cellW, w))
	return out.String()
}

// axis labels the columns: month names in year view, the week's Monday date in
// month view. A week belongs to the month of its Thursday — the majority of its
// days — so an edge week does not steal a label from its neighbour.
func (a *activityTab) axis(cols []time.Time, cellW, w int) string {
	line := []rune(strings.Repeat(" ", len(cols)*cellW))
	// next is where the previous label ended. A month name is three characters
	// and a week can be one cell, so without this the labels overwrite each
	// other and September plus October render as "SeOct".
	next := 0
	put := func(x int, s string) {
		if x < next {
			return
		}
		for i, r := range s {
			if x+i < len(line) {
				line[x+i] = r
			}
		}
		next = x + len(s) + 1
	}
	for ci, w := range cols {
		x := ci * cellW
		if a.view == viewYear {
			th := w.AddDate(0, 0, 3)
			if th.Year() != a.at.Year() {
				continue // edge weeks belong to the neighbouring year: no label
			}
			if ci == 0 || th.Month() != cols[ci-1].AddDate(0, 0, 3).Month() {
				put(x, monthNames[int(th.Month())-1])
			}
		} else {
			put(x, fmt.Sprintf("%2d", w.Day()))
		}
	}
	return styDim.Render(strings.Repeat(" ", gutter) + truncate(string(line), w-gutter))
}

func (a *activityTab) dayView(b state.Buckets, height int) string {
	cellW := a.width / 24
	if cellW < 2 {
		cellW = 2
	}
	if cellW > 5 {
		cellW = 5
	}
	barH := (height - 2) / 2
	if barH < 1 {
		barH = 1
	}
	if barH > 4 {
		barH = 4
	}
	k := state.DayKey(a.at)
	counts := make([]int, 24)
	max := 0
	for h := 0; h < 24; h++ {
		counts[h] = b.Hours[fmt.Sprintf("%s:%d", k, h)]
		if counts[h] > max {
			max = counts[h]
		}
	}
	var out strings.Builder
	for line := 0; line < barH; line++ {
		for h := 0; h < 24; h++ {
			// Fill from the bottom up: a bar's top line only lights at full height.
			filled := 0
			if max > 0 {
				filled = (counts[h]*barH + max - 1) / max
			}
			cell := strings.Repeat(" ", cellW)
			if barH-line <= filled {
				cell = lipgloss.NewStyle().Background(a.shade(counts[h], max)).Render(cell)
			}
			if a.cursor && h == a.hour {
				cell = lipgloss.NewStyle().Foreground(colHeading).Render(strings.Repeat("│", cellW))
			}
			out.WriteString(cell)
		}
		out.WriteByte('\n')
	}
	labels := ""
	for _, t := range []string{"00", "06", "12", "18"} {
		labels += pad(t, 6*cellW)
	}
	out.WriteString(styDim.Render(truncate(labels, a.width)))
	if a.cursor {
		out.WriteString(styFaint.Render(fmt.Sprintf("   %02d:00 — %d", a.hour, counts[a.hour])))
	}
	return out.String()
}

// statsBlock is the series selector, then what the period on screen adds up to,
// then what all of it adds up to.
//
// The split is the point. Every number in the middle used to come from
// time.Now() regardless of where you had navigated, so browsing to July still
// reported August's total, and day view on the 11th printed "Today: 4" — the
// 22nd's count — beside a grid of the 11th's hours. Three time frames in one
// block with nothing to tell them apart.
func (a *activityTab) statsBlock(b state.Buckets) string {
	act := a.cache.Activity(a.l.Evals(), a.l.Applied())

	kind := func(label string, on bool) string {
		if on {
			return styAccent.Render("> " + label)
		}
		return styDim.Render("  " + label)
	}
	left := lipgloss.JoinVertical(lipgloss.Left,
		kind("scans", !a.showApps),
		kind("applications", a.showApps),
	)

	stat := func(l string, v any) string {
		return styDim.Render(l+": ") + fmt.Sprint(v)
	}

	busiest, unit, active := a.periodDetail(b)
	mid := lipgloss.JoinVertical(lipgloss.Left,
		stat(a.periodName(), b.SumPrefix(a.prefix())),
		styDim.Render("busiest "+unit+": ")+
			lipgloss.NewStyle().Foreground(gold[shimmer()%len(gold)]).Render(busiest),
		stat("active "+unit+"s", active),
	)

	// "Rate" named two different things one keystroke apart: this one is
	// applications over scans, and the queue's is CVs over completed evals.
	// Both now say what they measure.
	rate := "—"
	if s := act.Scans.Total(); s > 0 {
		rate = fmt.Sprintf("%d%% of scans", act.Apps.Total()*100/s)
	}
	right := lipgloss.JoinVertical(lipgloss.Left,
		stat("all time", b.Total()),
		// Consecutive days back from today, which is a fact about now rather
		// than about the period being browsed — so it stays in this column.
		stat("streak", fmt.Sprintf("%dd", b.Streak(time.Now()))),
		stat("applied on", rate),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, "      ", mid, "      ", right)
}
