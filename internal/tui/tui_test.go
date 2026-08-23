package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

func snap(rows map[int]state.Row, offers map[int]state.Offer) snapshot {
	s := snapshot{
		rows: rows, offers: offers,
		applied:  state.Marks{},
		skipped:  state.Marks{},
		declined: state.Marks{},
		labels:   map[int]string{},
	}
	for id := range rows {
		s.session = append(s.session, id)
		s.labels[id] = "Acme — Engineer"
	}
	return s
}

func actionIDs(r rowInfo) []actionID {
	out := make([]actionID, 0, len(r.Actions))
	for _, a := range r.Actions {
		out = append(out, a.id)
	}
	return out
}

func eq(a []actionID, b ...actionID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRowActions(t *testing.T) {
	link := map[int]state.Offer{1: {ID: 1, URL: "https://example.test/job"}}

	t.Run("a failed row ends at debug and drops the link", func(t *testing.T) {
		// "see error" is already a link, and the posting is not what you act on
		// next — o and the tracker still reach it.
		s := snap(map[int]state.Row{1: {ID: 1, P2Status: state.P2Failed}}, link)
		if got := actionIDs(describe(1, s)); !eq(got, actError, actRetry, actDebug) {
			t.Errorf("got %v, want [error retry debug]", got)
		}
	})

	t.Run("an unavailable posting is offered no retry", func(t *testing.T) {
		// The runner refuses expired postings, so a retry button could not work.
		s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Unavailable}}, link)
		if got := actionIDs(describe(1, s)); !eq(got, actError, actDebug) {
			t.Errorf("got %v, want [error debug]", got)
		}
	})

	t.Run("evaled with no report number is a failed eval wearing a success label", func(t *testing.T) {
		s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled}}, link)
		info := describe(1, s)
		if info.Kind != kindFailed || info.Phase != "p2" {
			t.Errorf("kind %v phase %q, want failed/p2", info.Kind, info.Phase)
		}
	})

	t.Run("a gated row keeps its link", func(t *testing.T) {
		// Unlike a failed one, the posting is still worth reading before
		// deciding to overrule the gate.
		s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored, P1Score: "2.0", P2Status: state.P2Gated}}, link)
		info := describe(1, s)
		if !eq(actionIDs(info), actProceed, actLink) {
			t.Errorf("got %v, want [proceed link]", actionIDs(info))
		}
		// How close it was is the whole input to the override decision, so the
		// pre-screen score takes the score column a gated row has nothing else
		// to put in.
		if got := stripANSI(info.scoreText()); got != "P1 2.0" {
			t.Errorf("score %q, want the labelled pre-screen score", got)
		}
	})

	t.Run("an eval that earned no CV is labelled in the score column", func(t *testing.T) {
		// The score is the reason the row stopped, so the score column is where
		// the reason belongs — "P2 2.8" against a completed row's bare "4.4".
		// It used to be a "below threshold" status column, and a status column
		// is measured across the whole window: one skipped row took seventeen
		// columns off twenty labels.
		s := snap(map[int]state.Row{
			1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "078", P3Status: state.P3Completed},
			2: {ID: 2, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "079", P3Status: state.P3Skipped},
		}, nil)
		s.scores = map[int]float64{1: 4.4, 2: 2.8}

		if got := stripANSI(describe(1, s).scoreText()); got != "4.4" {
			t.Errorf("completed score %q, want a bare 4.4", got)
		}
		skipped := describe(2, s)
		if got := stripANSI(skipped.scoreText()); got != "P2 2.8" {
			t.Errorf("skipped score %q, want the labelled eval score", got)
		}
		// It is finished, so it keeps the finished glyph — dimmed, because the
		// score column already says on what terms.
		if got := stripANSI(skipped.marker()); got != "✓" {
			t.Errorf("marker %q, want the finished glyph", got)
		}
	})

	t.Run("a declined row stops asking", func(t *testing.T) {
		s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored, P1Score: "2.0", P2Status: state.P2Gated}}, link)
		s.declined[1] = nowForTest()
		if got := actionIDs(describe(1, s)); !eq(got, actLink) {
			t.Errorf("got %v, want just [link]", got)
		}
	})

	t.Run("a placeholder url is not a link", func(t *testing.T) {
		s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored}},
			map[int]state.Offer{1: {ID: 1, URL: "manual:snipe-1"}})
		if got := actionIDs(describe(1, s)); len(got) != 0 {
			t.Errorf("got %v, want none — an unopenable link should not be offered", got)
		}
	})
}

func newTestQueue(t *testing.T, s snapshot) *queueTab {
	t.Helper()
	q := newQueueTab(paths.Layout{Home: t.TempDir(), Code: t.TempDir()}, state.NewCache(), config.DefaultGoals)
	q.setSize(100, 30)
	q.setSnapshot(s)
	return q
}

func press(q *queueTab, key string) (bool, tea.Cmd) {
	return q.Update(keyMsg(key))
}

// The delegation contract: a tab reports whether it consumed the key, and the
// root's globals are the fallback. Everything the ink version did with
// hand-written guards falls out of this.
func TestDelegation(t *testing.T) {
	s := snap(map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "010", P3Status: state.P3Completed},
	}, map[int]state.Offer{1: {ID: 1, URL: "https://example.test/j"}})

	t.Run("q is not consumed outside a text field, so it still quits", func(t *testing.T) {
		q := newTestQueue(t, s)
		if handled, _ := press(q, "q"); handled {
			t.Error("the queue swallowed q — the root would never see the quit")
		}
	})

	t.Run("a focused field consumes q, with no typing() guard", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		if handled, _ := press(q, "q"); !handled {
			t.Fatal("a focused text field must claim printable keys")
		}
		if q.jd.Value() != "q" {
			t.Errorf("field holds %q, want \"q\"", q.jd.Value())
		}
	})

	t.Run("ctrl+c is never consumed, even by a focused field", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		if handled, _ := press(q, "ctrl+c"); handled {
			t.Error("a text field trapped the quit")
		}
	})

	t.Run("left at a row's first stop falls through to the tab switch", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fList)
		q.act = 0
		if handled, _ := press(q, "left"); handled {
			t.Error("left at the left edge was consumed — the tab switch is unreachable")
		}
	})

	t.Run("left inside a row's actions is consumed", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fList)
		q.act = 1
		if handled, _ := press(q, "left"); !handled {
			t.Error("left should walk back through the row's actions")
		}
		if q.act != 0 {
			t.Errorf("act %d, want 0", q.act)
		}
	})

	t.Run("right past the last stop falls through", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fList)
		q.act = q.current().stops() - 1
		if handled, _ := press(q, "right"); handled {
			t.Error("right at the right edge was consumed")
		}
	})
}

// The reader pane owns its keys while open — no global-hotkey guard, it simply
// returns before reaching them.
func TestDetailPaneOwnsKeys(t *testing.T) {
	s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored}}, nil)
	q := newTestQueue(t, s)
	q.detailOpen = true

	if handled, _ := press(q, "q"); !handled {
		t.Error("q should close the pane, not quit the app")
	}
	if q.detailOpen {
		t.Error("q did not close the pane")
	}
	q.detailOpen = true
	if handled, _ := press(q, "ctrl+c"); handled {
		t.Error("the pane trapped ctrl+c — a modal must never block a quit")
	}
}

// The stale-focus bug class: the ink version kept the focused stop in a field,
// so a poll that changed a row's kind left the cursor pointing at a stop that no
// longer existed. Here the actions are recomputed and the index is clamped.
func TestFocusSurvivesARowChangingKind(t *testing.T) {
	failed := snap(map[int]state.Row{1: {ID: 1, P2Status: state.P2Failed}}, nil)
	q := newTestQueue(t, failed)
	q.setFocus(fList)
	q.act = 3 // stop 0 is the row body, so debug is the fourth
	if got := q.current().actionAt(q.act); got != actDebug {
		t.Fatalf("setup: act %d is %q, want debug", q.act, got)
	}

	// A retry succeeds: the row is now completed and has one action.
	done := snap(map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "007", P3Status: state.P3Completed},
	}, nil)
	q.setSnapshot(done)

	if q.act >= q.current().stops() {
		t.Fatalf("act %d is past the %d stops the row now has", q.act, q.current().stops())
	}
	if got := q.current().actionAt(q.act); got != actDetail {
		t.Errorf("focus landed on %q, want a stop that exists", got)
	}
}

func TestCursorClampsWhenTheListShrinks(t *testing.T) {
	big := snap(map[int]state.Row{1: {ID: 1}, 2: {ID: 2}, 3: {ID: 3}}, nil)
	q := newTestQueue(t, big)
	q.setFocus(fList)
	q.cursor = 2

	q.setSnapshot(snap(map[int]state.Row{1: {ID: 1}}, nil))
	if q.cursor != 0 {
		t.Errorf("cursor %d, want 0 after the list shrank to one row", q.cursor)
	}
}

func TestEmptyListDoesNotPanic(t *testing.T) {
	q := newTestQueue(t, snap(nil, nil))
	q.setFocus(fList)
	for _, k := range []string{"down", "up", "left", "right", "enter", "o", "a", "x", "y", "n"} {
		press(q, k)
	}
	_ = q.View()
}

// Help is rendered from the same key.Binding values the handler matches on, so
// the footer cannot describe a key that does not work.
func TestHelpIsContextual(t *testing.T) {
	s := snap(map[int]state.Row{1: {ID: 1, P1Status: state.P1Scored, P1Score: "2.0", P2Status: state.P2Gated}}, nil)
	q := newTestQueue(t, s)

	q.setFocus(fJD)
	if hasHelp(q, "mark applied") {
		t.Error("the footer offered a row action while a text field had focus")
	}

	q.setFocus(fList)
	q.act = 1 // the proceed? question
	if !hasHelp(q, "evaluate anyway") {
		t.Error("the footer did not offer the gate's own answer keys")
	}
	if hasHelp(q, "do it") {
		t.Error("the footer offered a generic Enter over a question that takes y/n")
	}
}

func hasHelp(q *queueTab, desc string) bool {
	for _, b := range q.ShortHelp() {
		if b.Help().Desc == desc {
			return true
		}
	}
	return false
}

// An action's effect must be on the next frame, not the next poll. Everything
// the list draws comes from the snapshot, and the snapshot was only re-read on
// the one-second tick — so a submitted JD sat invisible for up to a second
// while navigation, which is pure in-memory, was instant.
func TestAnActionLandsWithoutWaitingForThePoll(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	if err := os.MkdirAll(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(l)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(pollCmd(l, m.cache, m.watch)())

	q := m.tabs[0].(*queueTab)
	// With a run holding the lock the submit queues instead of spawning, which
	// is the path the complaint named and the one that forks nothing.
	m.snap.running = true
	q.snap.running = true
	q.setFocus(fJD)
	q.jd.SetValue(strings.Repeat("a serious job description. ", 8))
	q.setFocus(fAdd)

	// Update, then whatever it asks for, then whatever THAT asks for — the loop
	// the runtime runs, without the tick that would hide the bug by refreshing
	// anyway.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for i := 0; cmd != nil && i < 4; i++ {
		out := cmd()
		if out == nil {
			break
		}
		_, cmd = m.Update(out)
	}

	if _, err := os.Stat(filepath.Join(l.JDs(), "1.txt")); err != nil {
		t.Fatalf("the JD never reached disk: %v", err)
	}
	// The empty-state line, not the row text: the footer note also carries the
	// id, and matching that passed against the bug.
	if view := stripANSI(m.View().Content); strings.Contains(view, "Nothing yet") {
		t.Errorf("the list is still a poll behind the action it just took:\n%s", view)
	}
}

// The ▶ becomes a spinner when a run holds the lock, and the child takes that
// lock some milliseconds after Start() returns — it has a config load and a
// cv.md read to get through first. Without a watch the glyph sat unchanged
// until the next one-second tick, so launching read as a keypress that did
// nothing.
func TestTheSpinnerDoesNotWaitForTheTick(t *testing.T) {
	l := paths.Layout{Home: t.TempDir(), Code: t.TempDir()}
	if err := os.MkdirAll(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan tea.Msg, 1)
	go func() { done <- awaitRunning(l)() }()

	select {
	case got := <-done:
		t.Fatalf("reported %T before anything held the lock", got)
	case <-time.After(60 * time.Millisecond):
	}

	lock, err := state.Acquire(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	select {
	case got := <-done:
		if _, ok := got.(refreshMsg); !ok {
			t.Errorf("got %T, want a refresh once the lock changed hands", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("the run took the lock and nothing asked for a re-read")
	}
}

// A paste is filed by what it is, not only by where the cursor is. Routing on
// focus alone put a 1,884-character posting on the URL line; the other way
// round, a link would have been submitted as the job description itself.
func TestPasteGoesToTheFieldItBelongsIn(t *testing.T) {
	const link = "https://www.linkedin.com/jobs/view/4457808322/?refId=abc&trackingId=xyz"
	jd := strings.Repeat("We are seeking a driven Junior Software Developer. ", 30)

	cases := []struct {
		name         string
		focus        qFocus
		paste        string
		wantJD       string
		wantURL      string
		wantFocus    qFocus
		wantRedirect bool
	}{
		{"prose on the URL field is a description", fURL, jd, jd, "", fJD, true},
		{"a link on the URL field stays", fURL, link, "", link, fURL, false},
		{"a link on the JD field is a link", fJD, link, "", link, fURL, true},
		{"prose on the JD field stays", fJD, jd, jd, "", fJD, false},
		{"a link with nowhere focused still lands on the URL", fNone, link, "", link, fURL, true},
		// One token that net/url reads as a relative path. A person reads it as
		// a link, so guessing either way is a guess — it stays put.
		{"a bare host is not moved", fJD, "linkedin.com/jobs/123", "linkedin.com/jobs/123", "", fJD, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newTestQueue(t, snap(map[int]state.Row{}, nil))
			q.setFocus(c.focus)
			cmd := q.onPaste(c.paste)

			if q.jd.Value() != c.wantJD {
				t.Errorf("JD field got %.40q…, want %.40q…", q.jd.Value(), c.wantJD)
			}
			if q.url.Value() != c.wantURL {
				t.Errorf("URL field got %.60q, want %.60q", q.url.Value(), c.wantURL)
			}
			if q.focus != c.wantFocus {
				t.Errorf("focus %v, want %v", q.focus, c.wantFocus)
			}
			// A paste that quietly lands somewhere else is worse than one that
			// lands in the wrong place, so a redirect has to say so.
			if got := cmd != nil; got != c.wantRedirect {
				t.Errorf("said something: %v, want %v", got, c.wantRedirect)
			}
		})
	}
}

// The JD field appends rather than replaces, so two pastes build one posting.
//
// It pins the newline as a SPACE deliberately. pasteJD joins with "\n" and the
// field is a textinput, whose sanitizer replaces every newline with a space —
// so a pasted posting reaches batch/jds/<id>.txt as one paragraph, with
// "Requirements: - Go - Linux" where the source had a list. Byte-for-byte the
// same length, which is why nothing downstream noticed. Asserting what actually
// happens is what makes the day someone swaps in a textarea show up here.
func TestASecondJDPasteAppends(t *testing.T) {
	q := newTestQueue(t, snap(map[int]state.Row{}, nil))
	q.setFocus(fJD)
	q.onPaste("first half")
	q.onPaste("second half")
	if got := q.jd.Value(); got != "first half second half" {
		t.Errorf("got %q, want the two joined — by a space, since textinput flattens the newline", got)
	}
}

// A row is done when Phase 3 has run, not when the eval landed.
//
// Done as soon as P2 evaled put the finished ✓ on rows whose CV had not been
// generated yet, and `o` then answered "no result folder" on them — correctly,
// because there was not one. It also had the list claiming more finished rows
// than the Completed counter beside it.
func TestARowIsDoneOnlyOncePhaseThreeHasRun(t *testing.T) {
	rows := map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "301", P3Status: state.P3Completed},
		2: {ID: 2, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "302", P3Status: state.P3Skipped},
		3: {ID: 3, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "303", P3Status: state.None},
	}
	s := snap(rows, map[int]state.Offer{})

	for id, want := range map[int]rowKind{1: kindDone, 2: kindDone, 3: kindPending} {
		if got := describe(id, s).Kind; got != want {
			t.Errorf("#%d is kind %d, want %d", id, got, want)
		}
	}
	// Skipped is finished without a CV; awaiting Phase 3 is not finished at all.
	if !describe(2, s).NoCV {
		t.Error("#2 skipped Phase 3 but does not say it made no CV")
	}
	if describe(3, s).NoCV {
		t.Error("#3 is still waiting for Phase 3, not refused by the gate")
	}
	// An eval that landed is worth reading whether or not the CV followed.
	if !eq(actionIDs(describe(3, s)), actDetail) {
		t.Errorf("#3 lost its report action: %v", actionIDs(describe(3, s)))
	}

	// A row awaiting Phase 3 is OPEN, and stays open whether or not a runner
	// holds the lock. A run works the queue; these rows are not in it, and
	// reading the lock as "this row is moving" lit four abandoned rows up as
	// in-flight.
	if !describe(3, s).inFlight() {
		t.Error("#3 is evaluated with no Phase 3 behind it — that is unfinished")
	}
	if got := stripANSI(describe(3, s).marker()); got != "!" {
		t.Errorf("#3 shows %q, want the glyph for open and unattended", got)
	}
	s.running = true
	if got := describe(3, s).Kind; got == kindRunning {
		t.Error("#3 reads as running because something else holds the lock")
	}
	if got := stripANSI(describe(3, s).marker()); got != "!" {
		t.Errorf("#3 shows %q while a run is active, want the same still glyph — "+
			"the spinner is the render's call, not the row's", got)
	}
}

// The glyph is the whole status column now, so each state has to be its own.
func TestMarkerVocabulary(t *testing.T) {
	rows := map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "301", P3Status: state.P3Completed},
		2: {ID: 2, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "302", P3Status: state.P3Skipped},
		3: {ID: 3, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "303", P3Status: state.None},
		4: {ID: 4, P1Status: state.P1Scored, P1Score: "1.9", P2Status: state.P2Gated, P3Status: state.P3Skipped},
		5: {ID: 5, P1Status: state.P1Scored, P2Status: state.P2Failed},
		6: {ID: 6}, // queued, untouched
	}
	s := snap(rows, map[int]state.Offer{})
	want := map[int]string{1: "✓", 2: "✓", 3: "!", 4: "?", 5: "✗", 6: "·"}
	seen := map[string][]int{}
	for id, glyph := range want {
		got := stripANSI(describe(id, s).marker())
		if got != glyph {
			t.Errorf("#%d shows %q, want %q", id, got, glyph)
		}
		seen[got] = append(seen[got], id)
	}
	// #1 and #2 share the tick on purpose — both are finished, and the colour
	// and the score column carry the difference. Nothing else may share.
	for glyph, ids := range seen {
		if len(ids) > 1 && glyph != "✓" {
			t.Errorf("%q is used by %v — two states that read the same", glyph, ids)
		}
	}
}

// There are three reasons a row has no output folder and only one of them is a
// fault. "No result folder yet" covered all three and distinguished none.
func TestOpenResultSaysWhyThereIsNoFolder(t *testing.T) {
	l := paths.Layout{Home: t.TempDir(), Code: t.TempDir()}
	q := newQueueTab(l, state.NewCache(), config.DefaultGoals)

	for _, tc := range []struct {
		name string
		info rowInfo
		want string
	}{
		{"turned back by the gate", rowInfo{ID: 1, Kind: kindDone, NoCV: true, Report: "1"}, "did not clear the gate"},
		{"phase 3 never ran", rowInfo{ID: 2, Kind: kindPending, AwaitingCV: true, Report: "2"}, "phase 3 never ran"},
		{"not evaluated at all", rowInfo{ID: 4, Kind: kindPending, Report: "4"}, "not been evaluated"},
		{"folder genuinely missing", rowInfo{ID: 3, Kind: kindDone, Report: "3"}, "output/ has no folder"},
	} {
		m, ok := q.openResult(tc.info)().(msg)
		if !ok || !m.isErr {
			t.Errorf("%s: did not report a problem", tc.name)
			continue
		}
		if !strings.Contains(m.text, tc.want) {
			t.Errorf("%s: said %q, want it to mention %q", tc.name, m.text, tc.want)
		}
	}
}

// The / palette: a named set of this binary's own subcommands, reachable from
// anywhere the keyboard is free.
func TestPalette(t *testing.T) {
	s := snap(map[int]state.Row{
		1: {ID: 1, P1Status: state.P1Scored, P2Status: state.P2Evaled, ReportNum: "010", P3Status: state.P3Completed},
	}, map[int]state.Offer{1: {ID: 1, URL: "https://example.test/j"}})

	t.Run("/ opens it, and a slash typed into a field does not", func(t *testing.T) {
		q := newTestQueue(t, s)
		if handled, _ := press(q, "/"); !handled || q.focus != fCmd {
			t.Fatalf("focus is %v, want fCmd", q.focus)
		}
		q = newTestQueue(t, s)
		q.setFocus(fJD)
		press(q, "/")
		if q.focus != fJD || q.jd.Value() != "/" {
			t.Errorf("focus %v, jd %q — a slash in a job description is a slash", q.focus, q.jd.Value())
		}
	})

	t.Run("a slash typed into the JD field drops the character counter", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		for _, k := range []string{"/", "s", "c", "a", "n"} {
			press(q, k)
		}
		out := stripANSI(q.View())
		if !strings.Contains(out, "/scan") || strings.Contains(out, "chars") {
			t.Errorf("the JD field is still counting a command:\n%s", out)
		}
	})

	t.Run("backspace over the empty command erases the slash into the JD field", func(t *testing.T) {
		q := newTestQueue(t, s)
		press(q, "/")
		press(q, "backspace")
		if q.focus != fJD {
			t.Fatalf("focus is %v, want fJD — the slash must be erasable", q.focus)
		}
		press(q, "a")
		if q.jd.Value() != "a" {
			t.Errorf("jd holds %q, want \"a\"", q.jd.Value())
		}
	})

	t.Run("esc cancels and leaves nothing behind", func(t *testing.T) {
		q := newTestQueue(t, s)
		press(q, "/")
		for _, k := range []string{"s", "c", "a", "n"} {
			press(q, k)
		}
		if q.cmd.Value() != "scan" {
			t.Fatalf("palette holds %q, want \"scan\"", q.cmd.Value())
		}
		press(q, "esc")
		if q.focus != fNone || q.cmd.Value() != "" {
			t.Errorf("focus %v, palette %q — esc must clear both", q.focus, q.cmd.Value())
		}
	})

	t.Run("a command typed into the JD box runs on enter", func(t *testing.T) {
		// Enter walks the composer — jd → url → add — but a field holding only a
		// command has nothing to walk to.
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		q.jd.SetValue("/scan")
		press(q, "enter")
		if q.busy == "" {
			t.Error("enter did not run it")
		}
		if q.focus == fURL {
			t.Error("enter walked on to ask a command for its URL")
		}
		if q.jd.Value() != "" {
			t.Errorf("the field still holds %q", q.jd.Value())
		}
	})

	t.Run("a real job description still walks the composer", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		q.jd.SetValue("Backend Engineer at Acme. We run the settlement platform.")
		press(q, "enter")
		if q.focus != fURL {
			t.Errorf("focus %v, want fURL", q.focus)
		}
		if q.busy != "" {
			t.Errorf("a job description ran as a command: %q", q.busy)
		}
	})

	t.Run("a name that does not parse stays put, to be fixed", func(t *testing.T) {
		q := newTestQueue(t, s)
		q.setFocus(fJD)
		q.jd.SetValue("/scna")
		press(q, "enter")
		if q.jd.Value() != "/scna" {
			t.Errorf("the field holds %q — a typo should be correctable, not retyped", q.jd.Value())
		}
		if q.busy != "" {
			t.Errorf("it ran %q anyway", q.busy)
		}
	})

	t.Run("the add button runs one too, instead of measuring it", func(t *testing.T) {
		// The JD length floor is a guard on a job description. A command is five
		// characters and is not one.
		q := newTestQueue(t, s)
		q.jd.SetValue("/scan")
		q.setFocus(fAdd)
		press(q, "enter")
		if q.busy == "" {
			t.Error("the button refused it — probably as a short job description")
		}
	})

	t.Run("tab out of the palette does not make it the remembered input", func(t *testing.T) {
		// lastInput is where Tab re-enters the composer. A cycle that lands back
		// on a half-typed command is not a cycle anybody asked for.
		q := newTestQueue(t, s)
		press(q, "/")
		press(q, "tab")
		if q.lastInput == fCmd {
			t.Error("the palette became the remembered input")
		}
	})
}

func TestParseCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"scan", []string{"scan"}},
		{"/scan", []string{"scan"}}, // the prompt already draws one
		{"  scan  ", []string{"scan"}},
		// Flags are passed through untouched: the palette knows the names, not
		// the flags, and `snipe scan -h` stays the documentation for both.
		{"scan -company acme", []string{"scan", "-company", "acme"}},
		{"", nil},
	} {
		got, err := parseCommand(tc.in)
		if err != nil {
			t.Errorf("parseCommand(%q): %v", tc.in, err)
			continue
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("parseCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	// An unknown name must name the set: the palette has no other way to say
	// what it can do.
	_, err := parseCommand("drain")
	if err == nil {
		t.Fatal("parseCommand(\"drain\") = nil error, want one")
	}
	for _, name := range paletteCmds {
		if !strings.Contains(err.Error(), "/"+name) {
			t.Errorf("%q does not offer /%s", err, name)
		}
	}
}

// The reader pane lives on the queue tab and every tab can open one: the
// follow-ups board reads a report the same way a row does. Update only reaches
// the ACTIVE tab, so a detailMsg raised anywhere else has to be routed by the
// root — without that, `o` on the follow-ups board opened nothing at all.
func TestReaderIsBorrowedByOtherTabs(t *testing.T) {
	m := New(paths.Layout{Home: t.TempDir(), Code: t.TempDir()})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.setTab(2)

	m.Update(detailMsg{title: "Acme — Engineer", body: "# report\n\nbody"})
	if !m.queue.detailOpen {
		t.Fatal("the reader stayed shut — the message went nowhere")
	}
	if m.active != 0 {
		t.Fatalf("active tab %d, want the queue: the pane it opened is drawn there", m.active)
	}

	m.Update(keyMsg("esc"))
	if m.queue.detailOpen {
		t.Error("esc did not close the reader")
	}
	if m.active != 2 {
		t.Errorf("active tab %d, want 2 — closing a borrowed reader goes back", m.active)
	}
}

// Enter walks the composer down — jd → url → Add — and ↑ has to walk it back.
//
// It did not: ↑ off Add and ↑ inside the URL field both went nowhere, so once
// Enter had left the JD field the only way back was three Tabs round the whole
// cycle. Whatever had just been pasted was on screen and unreachable.
func TestTheComposerWalksBothWays(t *testing.T) {
	q := newTestQueue(t, snap(map[int]state.Row{}, nil))
	q.setFocus(fJD)
	q.jd.SetValue("a job description")

	for _, want := range []qFocus{fURL, fAdd} {
		q.Update(keyMsg("enter"))
		if q.focus != want {
			t.Fatalf("enter went to %v, want %v", q.focus, want)
		}
	}
	for _, want := range []qFocus{fURL, fJD} {
		q.Update(keyMsg("up"))
		if q.focus != want {
			t.Fatalf("↑ went to %v, want %v", q.focus, want)
		}
	}
	if q.jd.Value() != "a job description" {
		t.Errorf("the JD field came back holding %q", q.jd.Value())
	}
	// ↓ is the same chain forward, so the two keys are each other's undo.
	for _, want := range []qFocus{fURL, fAdd, fList} {
		q.Update(keyMsg("down"))
		if q.focus != want {
			t.Fatalf("↓ went to %v, want %v", q.focus, want)
		}
	}
}

// Overruling the Phase 1 gate has to take the question off the row.
//
// It did not: nothing wrote P2Evaluating, so for the whole minutes-long eval
// the row on disk still read `p1-gated` — wearing "?" and still offering
// "proceed?" on an offer that was already being evaluated. The runner now marks
// it in flight before the call; see run.evalOne.
func TestAnOverruledGateStopsAsking(t *testing.T) {
	gated := state.Row{ID: 1, P1Status: state.P1Scored, P1Score: "1.9",
		P2Status: state.P2Gated, P3Status: state.P3Skipped}
	overruled := gated
	overruled.P2Status = state.P2Evaluating // what evalOne writes

	s := snap(map[int]state.Row{1: overruled}, map[int]state.Offer{})
	s.running = true
	info := describe(1, s)

	if got := stripANSI(info.marker()); got != "!" {
		t.Errorf("the still glyph is %q, want the open one — the spinner is the render's call", got)
	}
	if !info.inFlight() {
		t.Fatal("an eval under way does not read as in flight, so the render cannot spin it")
	}
	q := newTestQueue(t, s)
	if got := stripANSI(q.rowCells(info, false).marker); got == "?" || got == "!" {
		t.Errorf("the row renders %q while a run holds the lock, want a spinner frame", got)
	}
	for _, a := range actionIDs(info) {
		if a == actProceed {
			t.Error("the row still offers proceed? on an eval that is already running")
		}
	}
	// Stopping the run puts the question back, so the override can be retried
	// rather than leaving the row on a stage nothing is working on.
	if got := stripANSI(describe(1, snap(map[int]state.Row{1: gated}, nil)).marker()); got != "?" {
		t.Errorf("a gated row shows %q, want the question back", got)
	}
}

// Adding a JD queues it and stops there. It used to launch the runner whenever
// no run held the lock, so a paste was also a spend — the ▶ button was for
// resuming, not for starting. Now every add lands in queue.txt and waits.
func TestAddingAJDDoesNotStartTheRun(t *testing.T) {
	q := newTestQueue(t, snap(map[int]state.Row{}, nil))
	if err := os.MkdirAll(filepath.Dir(q.l.Queue()), 0o755); err != nil {
		t.Fatal(err)
	}
	q.jd.SetValue(strings.Repeat("We are seeking a driven Junior Software Developer. ", 4))

	if cmd := q.enqueue(); cmd == nil {
		t.Fatal("adding said nothing")
	}
	// The queue file IS the assertion: on the old path this stayed empty and a
	// `snipe run --only-id` was spawned instead.
	b, err := os.ReadFile(q.l.Queue())
	if err != nil {
		t.Fatalf("nothing was queued: %v", err)
	}
	if strings.TrimSpace(string(b)) != "1" {
		t.Errorf("queue.txt holds %q, want the new id", strings.TrimSpace(string(b)))
	}
}
