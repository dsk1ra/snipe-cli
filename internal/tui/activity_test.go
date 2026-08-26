package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// newDatedActivity builds a tab over a tree of eval artifacts stamped at the
// given times — the mtimes Activity reads as scan dates.
func newDatedActivity(t *testing.T, at time.Time, when ...time.Time) *activityTab {
	t.Helper()
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	evals := l.Evals()
	if err := os.MkdirAll(evals, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, w := range when {
		p := filepath.Join(evals, fmt.Sprintf("%d.json", i+1))
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, w, w); err != nil {
			t.Fatal(err)
		}
	}
	a := newActivityTab(l, state.NewCache())
	a.setSize(100, 24)
	a.at = at
	return a
}

// The middle column reports the period on screen, not today.
//
// Every number in it used to come from time.Now(): browsing to July reported
// August's total, and day view on the 11th printed "Today: 4" — the 22nd's
// count — beside a grid of the 11th's hours.
func TestTheStatsBlockFollowsThePeriod(t *testing.T) {
	jul := time.Date(2026, 7, 15, 10, 0, 0, 0, time.Local)
	aug := time.Date(2026, 8, 11, 10, 0, 0, 0, time.Local)
	a := newDatedActivity(t, aug, jul, jul, aug, aug, aug)

	a.view = viewMonth
	augOut := stripANSI(a.statsBlock(a.buckets()))
	if !strings.Contains(augOut, "Aug 2026: 3") {
		t.Errorf("August reported wrong:\n%s", augOut)
	}

	a.step(-1) // to July
	julOut := stripANSI(a.statsBlock(a.buckets()))
	if !strings.Contains(julOut, "Jul 2026: 2") {
		t.Errorf("stepping back did not move the total:\n%s", julOut)
	}
	// All-time stays all-time whichever month is on screen.
	for _, out := range []string{augOut, julOut} {
		if !strings.Contains(out, "all time: 5") {
			t.Errorf("all time moved with the period:\n%s", out)
		}
	}
}

// A day is measured in hours. Scoped by day, "busiest" and "active" reported the
// day you were already looking at and the count of itself.
func TestDayViewMeasuresInHours(t *testing.T) {
	day := time.Date(2026, 8, 11, 0, 0, 0, 0, time.Local)
	a := newDatedActivity(t, day,
		day.Add(9*time.Hour), day.Add(14*time.Hour), day.Add(14*time.Hour), day.Add(14*time.Hour))
	a.view = viewDay

	out := stripANSI(a.statsBlock(a.buckets()))
	for _, want := range []string{"busiest hour: 14:00 (3)", "active hours: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "active days") {
		t.Errorf("day view still counting days:\n%s", out)
	}
}

// j and k mean down and up in the other two tabs. Here they used to switch the
// series, so the same key moved a cursor in two places and changed the data in
// a third.
func TestTheSeriesToggleIsOffJK(t *testing.T) {
	a := newDatedActivity(t, time.Now())

	for _, k := range []string{"j", "k"} {
		before := a.showApps
		a.Update(keyMsg(k))
		if a.showApps != before {
			t.Errorf("%q still toggles the series", k)
		}
	}
	if a.Update(keyMsg("tab")); !a.showApps {
		t.Error("tab does not toggle the series")
	}
}

// The pane beside the calendar lists the day the cursor is on.
//
// Before it, the cursor drew a mark under a cell and reported nothing — not the
// date, not the count. The heat map could tell you the 11th was busy and had no
// way to tell you what made it busy.
func TestTheDayPaneListsTheCursorsDay(t *testing.T) {
	a := newDatedActivity(t, time.Now())
	a.setSize(102, 24)
	a.setSnapshot(datedSession([]int{3, 1, 1, 0}))
	a.view = viewMonth

	a.at = time.Now().AddDate(0, 0, -1) // two offers landed yesterday
	out := stripANSI(a.View())
	if !strings.Contains(out, "2 added") {
		t.Errorf("the pane did not count yesterday:\n%s", out)
	}
	for _, want := range []string{"Company 2", "Company 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("%s is missing from the pane:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Company 4") {
		t.Errorf("today's row leaked into yesterday's pane:\n%s", out)
	}
}

// Below the width where the calendar and the pane both fit, the pane goes — the
// same trade the queue's side pane makes.
func TestTheDayPaneGivesWayBeforeTheCalendar(t *testing.T) {
	a := newDatedActivity(t, time.Now())
	a.setSnapshot(datedSession([]int{0, 0}))
	a.view = viewMonth

	a.setSize(gridMinW+dayPaneW, 24)
	if out := stripANSI(a.View()); strings.Contains(out, "added") {
		t.Errorf("the pane survived a column too narrow for both:\n%s", out)
	}
	a.setSize(gridMinW+dayPaneW+1, 24)
	if out := stripANSI(a.View()); !strings.Contains(out, "added") {
		t.Errorf("the pane was dropped with room for it:\n%s", out)
	}
}

// o hands the day to the queue, which decides whether to take it.
//
// The activity tab knows which day the cursor is on and nothing about what the
// queue holds, so a day with no rows has to leave you where you were rather than
// drop you into a list that does not contain what you asked for.
func TestOpeningADayHandsItToTheQueue(t *testing.T) {
	m := New(paths.Layout{Home: t.TempDir(), Code: t.TempDir()})
	m.Update(tea.WindowSizeMsg{Width: 106, Height: 30})
	m.Update(snapshotMsg(datedSession([]int{3, 1, 1, 0})))
	m.setTab(1)
	a := m.tabs[1].(*activityTab)

	// Yesterday holds Company 2 and 3; the cursor lands on the LAST of them, so
	// the window — which fills upward — holds the whole day.
	a.at = time.Now().AddDate(0, 0, -1)
	m.Update(showDayMsg(state.DayKey(a.at)))
	if m.active != 0 {
		t.Fatalf("stayed on tab %d, want the queue", m.active)
	}
	if want := 2; m.queue.cursor != want {
		t.Errorf("cursor at %d, want %d — the last row of that day", m.queue.cursor, want)
	}
	if m.queue.focus != fList {
		t.Error("the queue did not take focus")
	}

	// A day with nothing in it leaves you where you were.
	m.setTab(1)
	m.Update(showDayMsg(state.DayKey(time.Now().AddDate(0, 0, -30))))
	if m.active != 1 {
		t.Errorf("switched to tab %d for a day with no rows", m.active)
	}
}

// Each series draws in its own ramp: scans yellow, applications orange.
//
// Hue carries which series is on screen and brightness carries how much, so the
// toggle reads from the grid itself rather than only from the "> scans" marker
// under it.
func TestEachSeriesHasItsOwnRamp(t *testing.T) {
	a := newDatedActivity(t, time.Now())

	// Against a busiest cell of 7, these four counts land one on each step.
	steps := []int{1, 2, 4, 7}
	walk := func(ramp []color.Color, name string) {
		for i, n := range steps {
			if got := a.shade(n, 7); got != ramp[i] {
				t.Errorf("%s: %d of 7 drew %v, want step %d (%v)", name, n, got, i, ramp[i])
			}
		}
	}
	walk(yellows, "scans")
	a.showApps = true
	walk(oranges, "applications")

	// An empty day is neither ramp: it stays the neutral ground both sit on.
	a.showApps = false
	if got, want := a.shade(0, 7), lipgloss.Color("236"); got != want {
		t.Errorf("an empty cell drew %v, want the neutral ground %v", got, want)
	}
}
