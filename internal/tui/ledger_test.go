package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// ledgerFixture is a ledger small enough to read: two entries, the first bullet
// cut into cumulative facts the way the real one is, a framing on the second.
func ledgerFixture() *ledger.Ledger {
	const b1 = "Cut developer environment setup from 2 hours to 30 minutes for an 800-student cohort by rewriting the Java, C++ and Python guides"
	return &ledger.Ledger{CVHash: "h", Entries: []ledger.Entry{
		{ID: "napier", Kind: "experience", Title: "Teaching Assistant - Edinburgh Napier University", Bullets: []ledger.Bullet{
			{Hash: "b1", Text: b1, Facts: []ledger.Fact{
				{ID: "napier/1a", Text: "Cut developer environment setup from 2 hours to 30 minutes", Figures: []string{"2 hours", "30 minutes"}, MustKeep: true},
				{ID: "napier/1b", Text: "for an 800-student cohort", Figures: []string{"800-student"}},
				{ID: "napier/1c", Text: "rewriting the Java, C++ and Python guides", Entities: []string{"Java", "C++", "Python", "guides"}},
			}},
		}},
		{ID: "ubwis", Kind: "experience", Title: "Software Engineer - UBWIS", Bullets: []ledger.Bullet{
			{Hash: "b2", Text: "Recovered 31% of failed renewals with automated dunning", Facts: []ledger.Fact{
				{ID: "ubwis/1a", Text: "Recovered 31% of failed renewals", Figures: []string{"31%"}, Status: "approved"},
				{ID: "ubwis/1b", Text: "with automated dunning", Entities: []string{"dunning"}},
			}, Framings: []ledger.Framing{{Phrase: "billing automation", From: []string{"ubwis/1a", "ubwis/1b"}}}},
		}},
	}}
}

// openLedgerTab saves the fixture where the layout says and opens the screen the
// way L does.
func openLedgerTab(t *testing.T) (*queueTab, paths.Layout) {
	t.Helper()
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	if err := ledger.Save(l.CVLedger(), ledgerFixture()); err != nil {
		t.Fatal(err)
	}
	q := newQueueTab(l, state.NewCache(), config.Goals{})
	q.setSize(100, 30)
	lpress(t, q, "L")
	if q.ledger == nil {
		t.Fatal("L did not open the screen")
	}
	return q, l
}

// lpress feeds a key and then every message its commands produce, failing on an
// error note.
func lpress(t *testing.T, q *queueTab, k string) {
	t.Helper()
	pressMsg(t, q, keyMsg(k))
}

func pressMsg(t *testing.T, q *queueTab, k tea.KeyPressMsg) {
	t.Helper()
	_, cmd := q.Update(k)
	for cmd != nil {
		m := cmd()
		if e, ok := m.(msg); ok && e.isErr {
			t.Fatalf("%s: %s", k, e.text)
		}
		_, cmd = q.Update(m)
	}
}

func typeText(t *testing.T, q *queueTab, s string) {
	t.Helper()
	for _, r := range s {
		lpress(t, q, string(r))
	}
}

func reload(t *testing.T, l paths.Layout) *ledger.Ledger {
	t.Helper()
	led, err := ledger.Load(l.CVLedger())
	if err != nil {
		t.Fatal(err)
	}
	return led
}

func fact(led *ledger.Ledger, e, b, i int) ledger.Fact { return led.Entries[e].Bullets[b].Facts[i] }

func TestLedgerProgressAndOpensOnFirstPending(t *testing.T) {
	q, _ := openLedgerTab(t)
	if q.ledger.cur != 0 {
		t.Errorf("opened on item %d, want the first pending one", q.ledger.cur)
	}
	view := stripANSI(q.View())
	for _, want := range []string{"1/5 reviewed", "Teaching Assistant - Edinburgh Napier University", "bullet 1/1",
		"1 2 hours · 2 30 minutes", "yes — every rewrite keeps it", "1 framing(s) to decide"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	lpress(t, q, "a")
	if v := stripANSI(q.View()); !strings.Contains(v, "2/5 reviewed") {
		t.Errorf("progress did not move:\n%s", v)
	}
}

// Every decision reaches the file before the next key, and a rejection stays in
// it as a rejection.
func TestLedgerDecisionsPersist(t *testing.T) {
	q, l := openLedgerTab(t)
	lpress(t, q, "a")
	if got := fact(reload(t, l), 0, 0, 0).Status; got != "approved" {
		t.Errorf("1a is %q after a", got)
	}
	if q.ledger.cur != 1 {
		t.Errorf("cursor %d: a should move to the next pending fact", q.ledger.cur)
	}
	lpress(t, q, "x")
	led := reload(t, l)
	if got := fact(led, 0, 0, 1); got.Status != "rejected" || got.Text != "for an 800-student cohort" {
		t.Errorf("1b after x: %+v", got)
	}

	lpress(t, q, "enter") // enter approves like a
	if got := fact(reload(t, l), 0, 0, 2).Status; got != "approved" {
		t.Errorf("1c after enter is %q", got)
	}

	lpress(t, q, "u") // takes back the enter, and lands on it
	if got := fact(reload(t, l), 0, 0, 2).Status; got != "" || q.ledger.cur != 2 {
		t.Errorf("undo: status %q, cursor %d", got, q.ledger.cur)
	}
	lpress(t, q, "u")
	lpress(t, q, "u")
	if got := fact(reload(t, l), 0, 0, 0).Status; got != "" {
		t.Errorf("1a still %q after undoing everything", got)
	}
	lpress(t, q, "u") // empty stack: a note, not a panic
}

func TestLedgerEdit(t *testing.T) {
	q, l := openLedgerTab(t)
	lpress(t, q, "e")
	if !q.ledger.capturing() {
		t.Fatal("e did not open the field")
	}
	// q and esc belong to the field, not the screen.
	lpress(t, q, "esc")
	if q.ledger == nil || fact(reload(t, l), 0, 0, 0).Status != "" {
		t.Fatal("esc in the field closed the screen or decided the fact")
	}

	lpress(t, q, "e")
	for range len("30 minutes") {
		lpress(t, q, "backspace")
	}
	typeText(t, q, "half an hour q")
	lpress(t, q, "enter")
	if q.ledger == nil {
		t.Fatal("a q typed into the field closed the screen")
	}
	got := fact(reload(t, l), 0, 0, 0)
	if got.Status != "approved" || got.Text != "Cut developer environment setup from 2 hours to half an hour q" {
		t.Errorf("after the edit: %+v", got)
	}

	// An edit is flagged for f, and one u takes back the text and the approval.
	lpress(t, q, "f")
	if q.ledger.cur != 0 {
		t.Errorf("f went to %d, want the edited fact", q.ledger.cur)
	}
	lpress(t, q, "u")
	got = fact(reload(t, l), 0, 0, 0)
	if got.Status != "" || strings.Contains(got.Text, "half") {
		t.Errorf("undo of an edit: %+v", got)
	}
}

func TestLedgerRemoveEntity(t *testing.T) {
	q, l := openLedgerTab(t)
	lpress(t, q, "tab") // 1b
	lpress(t, q, "tab") // 1c: Java, C++, Python, guides
	lpress(t, q, "d")
	if !q.ledger.capturing() {
		t.Fatal("d did not wait for a number")
	}
	lpress(t, q, "4") // guides
	got := fact(reload(t, l), 0, 0, 2)
	if strings.Join(got.Entities, ",") != "Java,C++,Python" || got.Status != "" {
		t.Errorf("entities after removing 4: %+v", got)
	}

	lpress(t, q, "d")
	lpress(t, q, "9") // out of range: nothing removed, mode ends
	lpress(t, q, "d")
	lpress(t, q, "x") // any other key cancels, and is not a reject
	if q.ledger.capturing() || fact(reload(t, l), 0, 0, 2).Status != "" {
		t.Error("a cancelled d still did something")
	}

	// Figures are numbered after the entities.
	lpress(t, q, "shift+tab")
	lpress(t, q, "shift+tab") // 1a: figures 1, 2
	lpress(t, q, "d")
	lpress(t, q, "2")
	if got := fact(reload(t, l), 0, 0, 0); strings.Join(got.Figures, ",") != "2 hours" {
		t.Errorf("figures after removing 2: %+v", got)
	}

	lpress(t, q, "u")
	if got := fact(reload(t, l), 0, 0, 0); strings.Join(got.Figures, ",") != "2 hours,30 minutes" {
		t.Errorf("undo of a removal: %+v", got)
	}
}

func TestLedgerMustKeepAndSkip(t *testing.T) {
	q, l := openLedgerTab(t)
	lpress(t, q, "m")
	if fact(reload(t, l), 0, 0, 0).MustKeep {
		t.Error("m did not clear must_keep on 1a")
	}
	lpress(t, q, "u")
	if !fact(reload(t, l), 0, 0, 0).MustKeep {
		t.Error("undo did not restore must_keep")
	}

	// 1a is pending, so tab walks pending facts only: 1a → 1b → 1c → ubwis/1b
	// (ubwis/1a is approved) → the framing → round to 1a.
	var seen []int
	for range 5 {
		lpress(t, q, "tab")
		seen = append(seen, q.ledger.cur)
	}
	if want := []int{1, 2, 4, 5, 0}; !equalInts(seen, want) {
		t.Errorf("tab visited %v, want %v", seen, want)
	}
	lpress(t, q, "shift+tab")
	if q.ledger.cur != 5 {
		t.Errorf("shift+tab went to %d, want 5", q.ledger.cur)
	}
	// j and k move one item at a time, decided or not.
	lpress(t, q, "k")
	lpress(t, q, "k")
	if q.ledger.cur != 3 {
		t.Errorf("k k from 5 landed on %d", q.ledger.cur)
	}
	lpress(t, q, "j")
	lpress(t, q, "down")
	lpress(t, q, "down")
	if q.ledger.cur != 5 {
		t.Errorf("moving down stopped at %d", q.ledger.cur)
	}
}

func equalInts(a, b []int) bool {
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

func TestLedgerFramingsAndFlagged(t *testing.T) {
	q, l := openLedgerTab(t)
	lpress(t, q, "shift+tab") // wraps to the framing, the last pending item
	if !q.ledger.pos[q.ledger.cur].framing {
		t.Fatal("did not land on the framing")
	}
	if v := stripANSI(q.View()); !strings.Contains(v, "billing automation") {
		t.Errorf("framing not shown:\n%s", v)
	}
	lpress(t, q, "e") // facts only: nothing to edit on a framing
	if q.ledger.capturing() {
		t.Error("e opened the field on a framing")
	}
	lpress(t, q, "x")
	if fr := reload(t, l).Entries[1].Bullets[0].Framings[0]; !fr.Rejected || fr.Approved {
		t.Errorf("framing after x: %+v", fr)
	}
	lpress(t, q, "f") // a rejected framing is flagged too
	if !q.ledger.pos[q.ledger.cur].framing {
		t.Errorf("f went to %d", q.ledger.cur)
	}
	lpress(t, q, "a")
	if fr := reload(t, l).Entries[1].Bullets[0].Framings[0]; !fr.Approved || fr.Rejected {
		t.Errorf("framing after a: %+v", fr)
	}
}

// Keys at the root's level: L opens, esc and q close, and q keeps its meaning of
// quit only once the screen is gone.
func TestLedgerKeys(t *testing.T) {
	q, _ := openLedgerTab(t)
	lpress(t, q, "esc")
	if q.ledger != nil {
		t.Error("esc did not close the screen")
	}
	lpress(t, q, "L")
	lpress(t, q, "q")
	if q.ledger != nil {
		t.Error("q did not close the screen")
	}
	if handled, _ := q.Update(keyMsg("q")); handled {
		t.Error("q with the screen closed should fall through to quit")
	}
	lpress(t, q, "L")
	if handled, _ := q.Update(keyMsg("ctrl+c")); handled {
		t.Error("ctrl+c must never be trapped")
	}
	hint := ""
	for _, b := range q.ShortHelp() {
		hint += b.Help().Desc + "|"
	}
	if !strings.Contains(hint, "approve") || !strings.Contains(hint, "undo") {
		t.Errorf("the footer does not show the ledger keys: %s", hint)
	}
}

func TestLedgerMissingFile(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	q := newQueueTab(l, state.NewCache(), config.Goals{})
	q.setSize(100, 30)
	_, cmd := q.Update(keyMsg("L"))
	_, cmd = q.Update(cmd())
	if e, ok := cmd().(msg); !ok || !e.isErr {
		t.Fatal("a missing ledger said nothing")
	}
	if q.ledger != nil {
		t.Error("the screen opened on an empty ledger")
	}
}

func TestLedgerView(t *testing.T) {
	q, _ := openLedgerTab(t)
	v := stripANSI(q.View())
	t.Log("\n" + v)
	for _, ln := range strings.Split(v, "\n") {
		if n := len([]rune(ln)); n > 100 {
			t.Errorf("line of %d columns: %q", n, ln)
		}
	}
	if !strings.Contains(v, "▸ ") || !strings.Contains(v, "• 1a") {
		t.Errorf("no cursor or pending mark:\n%s", v)
	}
}

// A terminal that reports keys without their text sends shift+l for L, and
// one that drops the modifier sends l, which is next tab. g is the key that
// survives both.
func TestLedgerOpensOnG(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{keyMsg("g"), {Code: 'l', Mod: tea.ModShift}} {
		home := t.TempDir()
		l := paths.Layout{Home: home, Code: home}
		if err := ledger.Save(l.CVLedger(), ledgerFixture()); err != nil {
			t.Fatal(err)
		}
		q := newQueueTab(l, state.NewCache(), config.Goals{})
		q.setSize(100, 30)
		pressMsg(t, q, k)
		if q.ledger == nil {
			t.Errorf("%q did not open the screen", k.String())
		}
	}
}
