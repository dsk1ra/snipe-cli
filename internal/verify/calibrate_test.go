package verify

import (
	"context"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

func testLedger() (*ledger.Ledger, ledger.Entry, ledger.Bullet) {
	acme, _ := entry(
		"Built a simulated trading engine in Go handling 10k orders per second",
		"Supervised the basic migration of 3 services to Postgres",
	)
	other := ledger.Entry{ID: "other", Bullets: []ledger.Bullet{{Text: "Cut deploy time by 65% and saved 120 hours per quarter"}}}
	return &ledger.Ledger{Entries: []ledger.Entry{acme, other}}, acme, acme.Bullets[0]
}

func TestCasesAreFalseByConstruction(t *testing.T) {
	led, e, b := testLedger()
	got := map[string]Case{}
	for _, c := range Cases(led, e, b) {
		got[c.Kind] = c
		if c.Draft == c.Source {
			t.Errorf("%s: draft equals source", c.Kind)
		}
		if c.WantSupported != (c.Kind == KindFaithful) {
			t.Errorf("%s: WantSupported = %v", c.Kind, c.WantSupported)
		}
	}
	for _, k := range []string{KindTool, KindQualifier, KindScope, KindFigure, KindFaithful} {
		if _, ok := got[k]; !ok {
			t.Errorf("no %s case", k)
		}
	}
	if !got[KindFaithful].NeedsTick || got[KindTool].NeedsTick {
		t.Error("only the faithful variant needs a human tick")
	}
	if Code(e, b, got[KindTool].Draft, nil, Options{}) == "" {
		t.Error("added tool passed the code checks")
	}
	if Figures(b.Text, got[KindFigure].Draft) == "" {
		t.Error("moved figure passed the figures check")
	}
	if Figures(b.Text, got[KindQualifier].Draft) != "" || EntityClosure(b.Text, got[KindQualifier].Draft, nil) != "" {
		t.Error("a dropped qualifier must slip past the code checks: only the model can see it")
	}
}

func TestCasesSkipWhatDoesNotApply(t *testing.T) {
	led := &ledger.Ledger{}
	e, b := entry("Wrote unit tests for the billing service")
	for _, c := range Cases(led, e, b) {
		if c.Kind == KindQualifier || c.Kind == KindFigure {
			t.Errorf("built %s from nothing: %+v", c.Kind, c)
		}
	}
}

func allCases() []Case {
	led, _, _ := testLedger()
	var cs []Case
	for _, e := range led.Entries {
		for _, b := range e.Bullets {
			cs = append(cs, Cases(led, e, b)...)
		}
	}
	return cs
}

func TestCalibrateSeparatingChecker(t *testing.T) {
	rep, err := Calibrate(context.Background(), subset, allCases())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recall != 1 || rep.Precision != 1 || rep.FalseAlarm != 0 {
		t.Errorf("recall %.2f precision %.2f false alarms %d", rep.Recall, rep.Precision, rep.FalseAlarm)
	}
	if rep.ByKind[KindQualifier] != 1 {
		t.Errorf("per-kind recall %v", rep.ByKind)
	}
}

func TestCalibrateLenientChecker(t *testing.T) {
	rep, _ := Calibrate(context.Background(), yes, allCases())
	if rep.Recall != 0 {
		t.Errorf("a checker that approves everything has recall %.2f", rep.Recall)
	}
}

func TestCalibrateRejectEverythingIsCaughtByPrecision(t *testing.T) {
	no := fakeChecker(func(_, _ string) bool { return false })
	rep, _ := Calibrate(context.Background(), no, allCases())
	if rep.Recall != 1 || rep.Precision >= 0.8 || rep.FalseAlarm == 0 {
		t.Errorf("recall %.2f precision %.2f false alarms %d", rep.Recall, rep.Precision, rep.FalseAlarm)
	}
}

func TestCalibrateNeedsBothDirections(t *testing.T) {
	// Attribution alone cannot see a dropped qualifier; the report must say
	// which direction caught it.
	rep, _ := Calibrate(context.Background(), subset, allCases())
	for _, o := range rep.Outcomes {
		if o.Kind == KindQualifier && (!o.Attribution || o.Preservation) {
			t.Errorf("qualifier drop: attribution %v preservation %v", o.Attribution, o.Preservation)
		}
	}
}

func TestCalibrateError(t *testing.T) {
	if _, err := Calibrate(context.Background(), errChecker{}, allCases()); err == nil {
		t.Error("checker error swallowed")
	}
}
