package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/suggest"
)

// The tailored-text screen: the text is there before any suggestion is, a
// suggestion is marked on its line, a decision reaches the file, and pressing
// the same key again takes it back.
func TestTailorScreen(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	dir := filepath.Join(l.Output(), "2026-01-01_acme_007")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "cv-content.json"), []byte(`{
		"experience":[{"company":"Acme Ltd","bullets":["Built REST services on Postgres","Led a team of 4"]}],
		"skills":[{"category":"Data","items":"PostgreSQL, Redis"}]}`), 0o644)

	q := newQueueTab(l, state.NewCache(), config.Goals{})
	q.setSize(100, 30)
	q.Update(openTailor(l, q.cache, rowInfo{ID: 7, Label: "Acme — Engineer", Report: "007"})())
	if q.tailor == nil {
		t.Fatal("the screen did not open")
	}
	view := stripANSI(q.View())
	for _, want := range []string{"Built REST services on Postgres", "Led a team of 4", "Skills · Data", "PostgreSQL"} {
		if !strings.Contains(view, want) {
			t.Errorf("the tailored text is missing %q:\n%s", want, view)
		}
	}

	suggest.Save(dir, []suggest.Item{{ID: "B1", Kind: suggest.Bullet, Where: "Acme Ltd",
		Old: "Built REST services on Postgres", New: "Built RESTful API services on Postgres", Why: `"RESTful APIs"`}})
	q.tailor.done(tailorOpMsg{op: "suggest"})
	if v := stripANSI(q.View()); !strings.Contains(v, "• Built REST services") || !strings.Contains(v, "now  Built RESTful API services") {
		t.Errorf("the suggestion is not marked:\n%s", v)
	}

	state := func() string {
		got, _ := suggest.Load(dir)
		return got[0].State
	}
	q.Update(keyMsg("enter"))
	if state() != suggest.Accepted || !strings.Contains(stripANSI(q.View()), "✓ Built RESTful API services") {
		t.Errorf("accept: state %q", state())
	}
	q.Update(keyMsg("enter"))
	if state() != "" {
		t.Errorf("a second enter did not undo it: %q", state())
	}
	q.Update(keyMsg("n"))
	if state() != suggest.Rejected {
		t.Errorf("reject: %q", state())
	}

	q.Update(keyMsg("esc"))
	if q.tailor != nil {
		t.Error("esc did not close the screen")
	}
}
