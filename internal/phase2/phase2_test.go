package phase2

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// A row the Phase 1 cost gate turned back carries p3_status=skipped. Overruling
// the gate re-evaluates it, and the new eval must not inherit the old refusal —
// the row would otherwise claim it made no CV because of a gate it just cleared.
func TestCommitClearsTheGatesSkip(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	st, err := state.Open(l.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(7, func(r *state.Row) {
		r.P1Status, r.P1Score = state.P1Scored, "2.4"
		r.P2Status, r.P3Status = state.P2Gated, state.P3Skipped
		r.Error = "p1-gated(threshold=2.5)"
	}); err != nil {
		t.Fatal(err)
	}

	e := &Evaluator{L: l, Store: st}
	c := composed{
		eval:   Eval{Status: state.P2Evaled, ID: "7", ReportNum: "042", ReportPath: filepath.Join(l.Reports(), "042-x-2026-01-01.md")},
		report: "# report\n",
	}
	if err := e.commit(context.Background(), 7, c); err != nil {
		t.Fatal(err)
	}

	got, _ := st.Get(7)
	if got.P3Status != "" {
		t.Errorf("p3_status is %q after a fresh eval, want it cleared", got.P3Status)
	}
	if got.P2Status != state.P2Evaled || got.ReportNum != "042" || got.Error != "" {
		t.Errorf("row is %+v, want evaled/042 with no error", got)
	}
}

// Retrieval's whole job in stage 2 is to put demonstrations in front of the
// grader. A catalogue line outranks nearly everything on cosine and is capped
// at Transferable the moment it is cited, so letting it take every slot spends
// the call on evidence that cannot score Strong.
func TestCatalogueTakesAtMostOneCandidateSlot(t *testing.T) {
	at := func(sim float64, source string) embed.Atom {
		return embed.Atom{Text: source + fmt.Sprint(sim), Source: source, Vec: []float64{sim, math.Sqrt(1 - sim*sim)}}
	}
	q := []float64{1, 0}

	// Every catalogue line beats every bullet, which is the real corpus's shape.
	var demo, catalogue []embed.Atom
	for _, s := range []float64{0.55, 0.54, 0.53, 0.52} {
		demo = append(demo, at(s, "projects"))
	}
	for _, s := range []float64{0.9, 0.8, 0.7} {
		catalogue = append(catalogue, at(s, "skills"))
	}

	got, err := TopCandidates(q, demo, catalogue, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
	cat := 0
	for _, c := range got {
		if fit.Catalogue(c.Source) {
			cat++
		}
	}
	if cat != 1 {
		t.Errorf("got %d catalogue candidates, want exactly 1: %+v", cat, got)
	}
	// And it is presented in the slot it earned, not the one it displaced.
	if !fit.Catalogue(got[0].Source) {
		t.Errorf("the top-scoring candidate should rank first, got %+v", got)
	}

	// Nothing to displace: fewer demonstrations than slots leaves room outright.
	got, err = TopCandidates(q, demo[:1], catalogue, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d candidates from 1 demonstration, want 2", len(got))
	}

	// A catalogue line that loses to every demonstration is not forced in.
	got, err = TopCandidates(q, demo, []embed.Atom{at(0.1, "skills")}, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got {
		if fit.Catalogue(c.Source) {
			t.Errorf("a losing catalogue line should not be substituted: %+v", got)
		}
	}
}
