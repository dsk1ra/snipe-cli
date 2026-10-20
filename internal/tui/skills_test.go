package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/aliases"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// The skills screen end to end: an add reaches skills.yml and the block, a
// compound item is refused, a rejection reaches the queue file.
func TestSkillsReview(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	os.MkdirAll(l.JDs(), 0o755)
	os.WriteFile(l.CV(), []byte("# X\n\n## Skills\n\n**Backend:** Apache Kafka, Java\n**Cloud:** Terraform / Infrastructure as Code\n"), 0o644)
	os.WriteFile(filepath.Join(l.JDs(), "7.txt"), []byte("We build on Kafka and Java, with IaC throughout."), 0o644)
	aliases.Save(l.SkillSuggestions(), []aliases.Suggestion{
		{Term: "Kafka", IDs: []int{7, 9, 11}, Targets: []aliases.Target{{Item: "Apache Kafka", Sim: 0.81}, {Item: "Java", Sim: 0.5}}},
		{Term: "IaC", IDs: []int{7, 9}, Targets: []aliases.Target{{Item: "Terraform / Infrastructure as Code", Sim: 0.75}}},
		{Term: "mentorship", IDs: []int{9, 11}, Targets: []aliases.Target{{Item: "Java", Sim: 0.70}}},
	})

	q := newQueueTab(l, state.NewCache(), config.Goals{})
	q.setSize(110, 30)
	q.Update(openSkills(l, rowInfo{ID: 7, Label: "Acme — Engineer"})())
	if q.review == nil {
		t.Fatal("the screen did not open")
	}
	press := func(k string) {
		t.Helper()
		_, cmd := q.Update(keyMsg(k))
		for cmd != nil {
			m := cmd()
			if e, ok := m.(msg); ok && e.isErr && k != "enter" {
				t.Fatalf("%s: %s", k, e.text)
			}
			_, cmd = q.Update(m)
		}
	}
	view := stripANSI(q.View())
	if !strings.Contains(view, `#7    "We build on Kafka and Java`) {
		t.Errorf("no quote from this posting:\n%s", view)
	}

	press("enter") // Kafka → Apache Kafka
	if b, _ := os.ReadFile(l.SkillsYML()); !strings.Contains(string(b), "Apache Kafka: [Kafka]") {
		t.Errorf("skills.yml is %q", b)
	}
	for _, ln := range strings.Split(stripANSI(q.View()), "\n") {
		if strings.HasPrefix(ln, "  Backend:") && strings.Contains(ln, "Apache") {
			t.Errorf("the block still prints cv.md's spelling: %q", ln)
		}
	}

	press("enter") // IaC → a compound item: refused, nothing written
	if b, _ := os.ReadFile(l.SkillsYML()); strings.Contains(string(b), "IaC") {
		t.Errorf("aliased a compound item: %q", b)
	}
	press("n")
	ss, _ := aliases.Load(l.SkillSuggestions())
	var rejected []string
	for _, s := range ss {
		if s.Rejected {
			rejected = append(rejected, s.Term)
		}
	}
	if len(ss) != 2 || strings.Join(rejected, ",") != "IaC" {
		t.Errorf("queue after the decisions: %+v", ss)
	}

	press("esc")
	if q.review != nil {
		t.Error("esc did not close the screen")
	}
}
