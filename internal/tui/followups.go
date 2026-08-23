package tui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

// rejectGrace is how long a mis-keyed `r` stays recoverable. Nothing is written
// to the tracker until it elapses, so undo costs nothing.
var rejectGrace = 5 * time.Second

func init() {
	if v := os.Getenv("SNIPE_REJECT_GRACE_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			rejectGrace = time.Duration(ms) * time.Millisecond
		}
	}
}

// proceedTo is the next stage for each actionable status.
var proceedTo = map[string]string{
	tracker.StatusApplied:   "Responded",
	tracker.StatusResponded: "Interview",
	tracker.StatusInterview: "Offer",
}

type followupsTab struct {
	l     paths.Layout
	cache *state.Cache
	keys  followupKeys
	snap  snapshot

	due     []tracker.Due
	cadence tracker.Cadence
	idx     int
	inList  bool
	loadErr error

	// pending rejections, by application number. A rejection is not written
	// until its deadline passes, which is what makes `u` an undo rather than a
	// second tracker edit.
	pending map[int]time.Time

	width, height int
}

func newFollowupsTab(l paths.Layout, c *state.Cache) *followupsTab {
	return &followupsTab{
		l: l, cache: c, keys: newFollowupKeys(),
		cadence: tracker.LoadCadence(l.ProfileYML()),
		pending: map[int]time.Time{},
	}
}

func (f *followupsTab) Title() string { return "follow-ups" }

// Badge counts what is actionable TODAY — urgent and overdue only. A "waiting"
// entry is not a thing to do, and counting it makes the badge a number the user
// learns to ignore.
func (f *followupsTab) Badge() int {
	n := 0
	for _, d := range f.due {
		if d.Urgency == tracker.Urgent || d.Urgency == tracker.Overdue {
			n++
		}
	}
	return n
}
func (f *followupsTab) setSize(w, h int) { f.width, f.height = w, h }

// setFrame is this tab's clock rather than an animation — nothing here spins.
// It carries the pending rejections because a grace window has to close on time
// whether or not anything on disk has moved, and the poll stopped promising that
// when it started skipping trees nobody had touched.
func (f *followupsTab) setFrame(int) { f.flushPending() }

func (f *followupsTab) setSnapshot(s snapshot) {
	f.snap = s
	f.reload()
	if f.idx >= len(f.due) {
		f.idx = max(0, len(f.due)-1)
	}
}

func (f *followupsTab) reload() {
	apps, err := tracker.LoadApplications(f.l.Tracker())
	if err != nil {
		f.loadErr = err
		return
	}
	fus, err := tracker.LoadFollowups(f.l.Followups())
	if err != nil {
		f.loadErr = err
		return
	}
	f.loadErr = nil
	f.due = tracker.DueList(apps, fus, f.cadence, time.Now())
}

// flushPending writes the rejections whose grace window has closed. It rides the
// poll rather than a timer goroutine, so there is nothing to cancel on quit and
// no write can land after the model is gone.
func (f *followupsTab) flushPending() {
	now := time.Now()
	for num, at := range f.pending {
		if now.Before(at) {
			continue
		}
		delete(f.pending, num)
		_ = tracker.SetStatus(f.l.Tracker(), num, "Rejected")
	}
}

func (f *followupsTab) current() (tracker.Due, bool) {
	if f.idx < 0 || f.idx >= len(f.due) {
		return tracker.Due{}, false
	}
	return f.due[f.idx], true
}

func (f *followupsTab) Update(m tea.Msg) (bool, tea.Cmd) {
	k, ok := m.(tea.KeyPressMsg)
	if !ok {
		return false, nil
	}
	switch {
	case key.Matches(k, f.keys.Down):
		if !f.inList {
			f.inList = true
			f.idx = 0
			return true, nil
		}
		if f.idx < len(f.due)-1 {
			f.idx++
		}
		return true, nil

	case key.Matches(k, f.keys.Up):
		if !f.inList {
			return false, nil
		}
		if f.idx > 0 {
			f.idx--
			return true, nil
		}
		f.inList = false // ↑ off the top steps back out
		return true, nil

	case key.Matches(k, f.keys.Out):
		if !f.inList {
			return false, nil
		}
		f.inList = false
		return true, nil
	}

	if !f.inList {
		return false, nil
	}
	e, ok := f.current()
	if !ok {
		return false, nil
	}
	switch {
	case key.Matches(k, f.keys.Nudged):
		return true, f.nudge(e)
	case key.Matches(k, f.keys.Rejected):
		return true, f.reject(e)
	case key.Matches(k, f.keys.Proceeded):
		return true, f.proceed(e)
	case key.Matches(k, f.keys.Undo):
		return true, f.undo(e)
	case key.Matches(k, f.keys.Open):
		return true, f.openReport(e)
	}
	return false, nil
}

// nudge records one follow-up: a row appended to data/follow-ups.md, the same
// table the cadence reads, so the clock resets and the entry leaves the due list
// on its own. No separate "nudged" flag to keep in sync.
func (f *followupsTab) nudge(e tracker.Due) tea.Cmd {
	fus, err := tracker.LoadFollowups(f.l.Followups())
	if err != nil {
		return failf("%v", err)
	}
	err = tracker.AppendFollowup(f.l.Followups(), tracker.Followup{
		Num:     tracker.NextFollowupNum(fus),
		AppNum:  e.App.Num,
		Date:    time.Now().Format("2006-01-02"),
		Company: e.App.Company,
		Role:    e.App.Role,
		Channel: "manual",
		Notes:   "via TUI",
	})
	if err != nil {
		return failf("%v", err)
	}
	f.reload()
	next := fmt.Sprintf("next due in %dd", f.cadence.AppliedSubsequent)
	if e.FollowupCount+1 >= f.cadence.AppliedMaxFollowups && e.App.Status == tracker.StatusApplied {
		next = "that was the final follow-up"
	}
	return notef("%s nudged ✓ — %s · u undo", e.App.Company, next)
}

// undo peels the latest recorded nudge off the entry, one per press. It reads
// the file rather than session state, so it survives a restart and can also roll
// back a nudge recorded outside the TUI.
func (f *followupsTab) undo(e tracker.Due) tea.Cmd {
	// A rejection still inside its grace window outranks the nudge log: it is
	// the more recent action and the one the user is most likely undoing.
	if _, ok := f.pending[e.App.Num]; ok {
		delete(f.pending, e.App.Num)
		return notef("rejection cancelled for %s", e.App.Company)
	}
	fus, err := tracker.LoadFollowups(f.l.Followups())
	if err != nil {
		return failf("%v", err)
	}
	last := -1
	for _, fu := range fus {
		if fu.AppNum == e.App.Num && fu.Num > last {
			last = fu.Num
		}
	}
	if last < 0 {
		return failf("no nudges recorded for %s", e.App.Company)
	}
	if err := tracker.RemoveFollowup(f.l.Followups(), last); err != nil {
		return failf("%v", err)
	}
	f.reload()
	return notef("rolled back the last nudge for %s", e.App.Company)
}

// reject and proceed end the cadence instead of nudging it: both flip the
// tracker's Status cell, which is the only thing deciding whether an entry is a
// follow-up at all.
func (f *followupsTab) reject(e tracker.Due) tea.Cmd {
	if _, ok := f.pending[e.App.Num]; ok {
		return failf("%s is already being rejected · u undo", e.App.Company)
	}
	f.pending[e.App.Num] = time.Now().Add(rejectGrace)
	return notef("%s → Rejected in %.0fs · u undo", e.App.Company, rejectGrace.Seconds())
}

func (f *followupsTab) proceed(e tracker.Due) tea.Cmd {
	to, ok := proceedTo[e.App.Status]
	if !ok {
		return failf("no next stage after %s", e.App.Status)
	}
	if err := tracker.SetStatus(f.l.Tracker(), e.App.Num, to); err != nil {
		return failf("%v", err)
	}
	f.reload()
	return notef("%s → %s", e.App.Company, to)
}

var bracketNum = regexp.MustCompile(`\[0*(\d+)\]`)

// openReport finds the report by the number in the Report *cell*, not the row's
// own #, which is a different sequence.
func (f *followupsTab) openReport(e tracker.Due) tea.Cmd {
	num := fmt.Sprintf("%03d", e.App.Num)
	if m := bracketNum.FindStringSubmatch(e.App.Report); m != nil {
		n, _ := strconv.Atoi(m[1])
		num = fmt.Sprintf("%03d", n)
	}
	path, ok := f.cache.ReportFile(f.l.Reports(), num)
	if !ok {
		return failf("no report found for %s", e.App.Company)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return failf("%v", err)
	}
	return func() tea.Msg {
		return detailMsg{title: e.App.Company + " — " + e.App.Role, body: string(body)}
	}
}

func (f *followupsTab) ShortHelp() []key.Binding {
	if !f.inList {
		return []key.Binding{f.keys.Down}
	}
	return []key.Binding{f.keys.Up, f.keys.Down, f.keys.Nudged, f.keys.Rejected, f.keys.Proceeded, f.keys.Undo, f.keys.Open}
}

func (f *followupsTab) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{f.keys.Up, f.keys.Down, f.keys.Out},
		{f.keys.Nudged, f.keys.Rejected, f.keys.Proceeded},
		{f.keys.Undo, f.keys.Open},
	}
}

func urgencyStyle(u tracker.Urgency) lipgloss.Style {
	switch u {
	case tracker.Urgent:
		return styErr
	case tracker.Overdue:
		return styWarn
	case tracker.Cold:
		return styDim
	}
	return styFaint
}

func (f *followupsTab) View() string {
	head := styPaneHead.Render("Follow-ups") + "\n\n"
	if f.loadErr != nil {
		return head + styErr.Render(f.loadErr.Error())
	}
	if len(f.due) == 0 {
		return head + styDim.Render("nothing to follow up — every application is on schedule")
	}

	var counts [4]int
	for _, d := range f.due {
		counts[d.Urgency]++
	}
	counted := styFaint.Render(fmt.Sprintf("%d actionable  ", len(f.due))) +
		styErr.Render(fmt.Sprintf("%d urgent", counts[tracker.Urgent])) + styDim.Render(" · ") +
		styWarn.Render(fmt.Sprintf("%d overdue", counts[tracker.Overdue])) + styDim.Render(" · ") +
		styFaint.Render(fmt.Sprintf("%d waiting", counts[tracker.Waiting])) + styDim.Render(" · ") +
		styDim.Render(fmt.Sprintf("%d cold", counts[tracker.Cold]))

	var b strings.Builder
	b.WriteString(head + counted + "\n\n")
	// Two rows for the pane heading, two for the counts and its blank.
	window := f.height - 4
	if window < 1 {
		window = 1
	}
	start := 0
	if f.idx >= window {
		start = f.idx - window + 1
	}
	for i := start; i < len(f.due) && i < start+window; i++ {
		b.WriteString(f.rowView(f.due[i], f.inList && i == f.idx) + "\n")
	}
	return b.String()
}

func (f *followupsTab) rowView(d tracker.Due, focused bool) string {
	tag := urgencyStyle(d.Urgency).Render(pad(d.Urgency.String(), 8))
	if _, ok := f.pending[d.App.Num]; ok {
		tag = styErr.Render(pad("→reject", 8))
	}

	var when string
	switch {
	case d.Urgency == tracker.Cold:
		when = "no more follow-ups"
	case d.DaysUntilNext < 0:
		when = fmt.Sprintf("due %dd ago", -d.DaysUntilNext)
	case d.DaysUntilNext == 0:
		when = "due today"
	default:
		when = fmt.Sprintf("due in %dd", d.DaysUntilNext)
	}
	nudges := ""
	if d.FollowupCount > 0 {
		nudges = fmt.Sprintf(" ·%d sent", d.FollowupCount)
	}
	tail := styDim.Render(fmt.Sprintf("%s%s · %s", when, nudges, d.App.Status))

	room := f.width - lipgloss.Width(tag) - lipgloss.Width(tail) - 4
	label := truncate(d.App.Company+" — "+d.App.Role, room)
	if focused {
		label = styFocused.Render(label)
	}
	return fmt.Sprintf(" %s %s  %s", tag, label, tail)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
