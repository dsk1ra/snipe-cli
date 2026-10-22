package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
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

// A line `--writer adapt` changed is offered back to the person's own wording,
// flagged with what licensed it, and accepting it reverts the line at rebuild.
func TestTailorScreenReviewsAdaptedLines(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	dir := filepath.Join(l.Output(), "2026-01-01_acme_007")
	os.MkdirAll(dir, 0o755)
	shipped := "Designed 10 event-driven Java services on Kafka"
	orig := "Built 10 Java services talking through a Kafka log"
	os.WriteFile(filepath.Join(dir, "cv-content.json"), []byte(`{
		"experience":[{"company":"Acme Ltd","bullets":["`+shipped+`","Led a team of 4"]}]}`), 0o644)
	os.WriteFile(filepath.Join(dir, adapt.TruthName), []byte(`{"offer":"007","bullets":[
		{"entry":"Acme Ltd","hash":"h1","source":"B","kind":"re-led","class":"supported","original":"`+orig+`","shipped":"`+shipped+`","framings":["event-driven"],"why":"leads with the ask"},
		{"entry":"Acme Ltd","hash":"h2","source":"original","kind":"original","class":"supported","original":"Led a team of 4","shipped":"Led a team of 4"}]}`), 0o644)

	q := newQueueTab(l, state.NewCache(), config.Goals{})
	q.setSize(110, 30)
	q.Update(openTailor(l, q.cache, rowInfo{ID: 7, Label: "Acme — Engineer", Report: "007"})())
	if q.tailor == nil {
		t.Fatal("the screen did not open")
	}
	v := stripANSI(q.View())
	for _, want := range []string{"✎ " + shipped, "1 adapted line(s)", "your " + orig, "framing you approved: event-driven", "enter puts your line back"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "✎ Led a team") {
		t.Error("an unchanged line was offered back")
	}

	items, _ := suggest.Load(dir)
	if len(items) != 1 || items[0].Old != shipped || items[0].New != orig {
		t.Fatalf("the offer was not saved: %+v", items)
	}
	q.Update(keyMsg("enter"))
	items, _ = suggest.Load(dir)
	body, _ := os.ReadFile(filepath.Join(dir, "cv-content.json"))
	content, _ := fill.ParseContent(body)
	got, n := suggest.Apply(content, items)
	if items[0].State != suggest.Accepted || n != 1 || got.Experience[0].Bullets[0] != orig {
		t.Errorf("accepting should put the original back at rebuild: %+v %d %q", items[0], n, got.Experience[0].Bullets[0])
	}

	// Opening again keeps the decision and adds nothing.
	q.Update(keyMsg("esc"))
	q.Update(openTailor(l, q.cache, rowInfo{ID: 7, Label: "Acme — Engineer", Report: "007"})())
	if again, _ := suggest.Load(dir); len(again) != 1 || again[0].State != suggest.Accepted {
		t.Errorf("reopening changed the file: %+v", again)
	}
}
