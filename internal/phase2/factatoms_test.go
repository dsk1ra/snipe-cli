package phase2

import (
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

func TestFactAtomsSplitsBulletsAndKeepsTheRest(t *testing.T) {
	led := &ledger.Ledger{Entries: []ledger.Entry{{ID: "acme", Bullets: []ledger.Bullet{
		{Text: "Wrote Kafka consumers in Go and cut lag by 90%", Facts: []ledger.Fact{
			{Text: "Wrote Kafka consumers in Go"}, {Text: "Cut consumer lag by 90%"}, {Text: "Invented", Status: "rejected"}}},
		{Text: "Only rejected", Facts: []ledger.Fact{{Text: "x", Status: "rejected"}}},
	}}}}
	atoms := []embed.Atom{
		{Text: "Acme: Wrote Kafka consumers in Go and cut lag by 90%", Source: "experience"},
		{Text: "Acme: Only rejected", Source: "experience"},
		{Text: "Skills — Languages: Go", Source: "skills"},
	}
	got := FactAtoms(atoms, led)
	if len(got) != 4 {
		t.Fatalf("%d atoms: %+v", len(got), got)
	}
	if got[0].Text != "Acme: Wrote Kafka consumers in Go" || got[1].Text != "Acme: Cut consumer lag by 90%" ||
		got[0].Parent != atoms[0].Text || got[0].Source != "experience" {
		t.Errorf("facts: %+v", got[:2])
	}
	if got[2].Text != atoms[1].Text || got[3].Text != atoms[2].Text || got[2].Parent != "" {
		t.Errorf("a bullet with no live facts and the catalogue pass through: %+v", got[2:])
	}
	if len(FactAtoms(atoms, nil)) != 3 {
		t.Error("no ledger leaves the atoms alone")
	}
}
