package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/atsjudge"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

const atsReportText = `# Report

## B) Evidence

| JD Requirement | Candidate evidence | Strength |
|---|---|---|
| **[must]** Kafka consumers in production | Acme: Wrote Kafka consumers | Strong |
| **[must]** Terraform and AWS in production | none | Gap |
| Rust or Go services | UBWIS | Transferable |
`

// Whatever the CV, the fake judge says met with the first line of the
// qualification's evidence, so the verdict depends on the page alone.
func atsFake(t *testing.T) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []ollama.Message }
		_ = json.NewDecoder(r.Body).Decode(&req)
		user := req.Messages[len(req.Messages)-1].Content
		reply := "VERDICT: unmet\nQUOTE: "
		if strings.Contains(user, "Qualification:\nKafka") && strings.Contains(user, "Wrote Kafka consumers") {
			reply = "VERDICT: met\nQUOTE: Wrote Kafka consumers"
		}
		if strings.Contains(user, "Qualification:\nRust") && strings.Contains(user, "Rust") {
			reply = "VERDICT: met\nQUOTE: built it in Rust"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

func mk(must float64, has bool) atsjudge.Result {
	return atsjudge.Result{MustEvidence: must, HasMust: has, Grade: "B"}
}

func TestPairedArithmetic(t *testing.T) {
	rows := []atsRow{
		{"001", mk(0.5, true), mk(1, true)},    // improved
		{"002", mk(1, true), mk(0.5, true)},    // regressed
		{"003", mk(0.5, true), mk(0.5, true)},  // tied
		{"004", mk(0, true), mk(1, true)},      // improved
		{"005", mk(0, false), mk(1, true)},     // undefined on one arm: out of the pairing
		{"006", mk(0.25, true), mk(0.5, true)}, // improved
	}
	p := paired(rows, mustOf)
	if p.N != 5 || p.Improved != 3 || p.Regressed != 1 || p.Tied != 1 {
		t.Errorf("counts = %+v", p)
	}
	if want := (0.5 + 1 + 0.5 + 0 + 0.25) / 5; abs(p.MeanV-want) > 1e-12 {
		t.Errorf("verbatim mean = %v, want %v", p.MeanV, want)
	}
	if want := (1 + 0.5 + 0.5 + 1 + 0.5) / 5; abs(p.MeanA-want) > 1e-12 || abs(p.Diff-(p.MeanA-p.MeanV)) > 1e-12 {
		t.Errorf("adapt mean = %v, diff = %v", p.MeanA, p.Diff)
	}
	if got := paired(nil, mustOf); got.N != 0 || got.Diff != 0 {
		t.Errorf("empty = %+v", got)
	}

	var b bytes.Buffer
	atsReport(&b, "m", rows)
	for _, want := range []string{"3 improved, 1 regressed, 1 tied", "paired Δ +0.250 over 5", "+0.50", "-0.50"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, b.String())
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestControlWarnsWhenTheJudgeSaysMetToABlankCV(t *testing.T) {
	yes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "VERDICT: met\nQUOTE: Ada Lovelace"}})
	}))
	defer yes.Close()
	items := []atsItem{{ID: "001", Quals: qualsOf(atsReportText), Verbatim: "Ada Lovelace\nada@example.com\n"}}

	j, _ := atsjudge.New(ollama.New(yes.URL, nil), t.TempDir(), atsjudge.Options{})
	var b bytes.Buffer
	m, ok, err := atsControl(context.Background(), &b, j, items)
	if err != nil || !ok || m != 1 || !strings.Contains(b.String(), "WARNING") {
		t.Errorf("a yes-man: %v %v %v\n%s", m, ok, err, b.String())
	}

	// The real fake cannot quote Kafka out of a CV with no bullets.
	j, _ = atsjudge.New(atsFake(t), t.TempDir(), atsjudge.Options{})
	b.Reset()
	items[0].Verbatim = "Ada Lovelace\nada@example.com\n\nWork Experience\n Acme\n • Wrote Kafka consumers\n"
	items[0].Verbatim = strings.Repeat("header\n", 6) + items[0].Verbatim
	if m, ok, err = atsControl(context.Background(), &b, j, items); err != nil || !ok || m != 0 || strings.Contains(b.String(), "WARNING") {
		t.Errorf("honest judge: %v %v %v\n%s", m, ok, err, b.String())
	}
}

func e3Fixture(t *testing.T) paths.Layout {
	t.Helper()
	l := paths.Layout{Home: t.TempDir()}
	good := "Ada\nada@example.com\n\nWork Experience\n Acme\n   • Wrote Kafka consumers in Go\n   • Built it in Rust\n"
	for arm, page := range map[string]string{"verbatim": "Ada\nada@example.com\n\nWork Experience\n Acme\n • Built it in Rust\n", "adapt": good} {
		f := e3File{Arm: arm, Offers: []e3Result{
			{Offer: "001", Arm: arm, Page: page, Metrics: map[string]float64{}},
			{Offer: "002", Arm: arm, Err: "boom"},
			{Offer: "003", Arm: arm, Page: page, Metrics: map[string]float64{}},
		}}
		if arm == "adapt" {
			f.Offers = f.Offers[:2] // 003 is only on one arm
		}
		if err := f.save(e3Path(l, arm)); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func TestATSItemsPairsTheArms(t *testing.T) {
	l := e3Fixture(t)
	var w bytes.Buffer
	items := atsItems(l, func(string) (string, error) { return atsReportText, nil }, &w)
	if len(items) != 1 || items[0].ID != "001" || len(items[0].Quals) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if !items[0].Quals[0].Must || !items[0].Quals[0].Supported || items[0].Quals[1].Supported || items[0].Quals[2].Must {
		t.Errorf("quals = %+v", items[0].Quals)
	}
}

func TestTailorReadsTheJudgeCacheAndOnlyWhenItCoversEveryOffer(t *testing.T) {
	defer func(old []metric) { suite = old }(append([]metric(nil), suite...))
	l := e3Fixture(t)
	report := func(string) (string, error) { return atsReportText, nil }
	items := atsItems(l, report, &bytes.Buffer{})

	// Nothing cached: no number, the row keeps saying a judge is needed.
	rs := loadE3(e3Path(l, "adapt")).Offers[:1]
	withCachedATS(l, report, rs, nil)
	if _, ok := rs[0].Metrics["must_have_evidence"]; ok || naOf("must_have_evidence") != needsJudge {
		t.Fatal("filled without a cache")
	}

	// gate ats fills the cache, then gate tailor reads it without a model.
	j, _ := atsjudge.New(atsFake(t), l.CacheDir(), atsjudge.Options{})
	for _, it := range items {
		for _, pg := range []string{it.Verbatim, it.Adapt} {
			if _, err := j.Score(context.Background(), it.Quals, pg); err != nil {
				t.Fatal(err)
			}
		}
	}
	rs = loadE3(e3Path(l, "adapt")).Offers[:1]
	rs[0].Metrics = map[string]float64{} // omitempty drops an empty map from the file; a scored offer always has some
	base := loadE3(e3Path(l, "verbatim")).Offers
	base[0].Metrics = map[string]float64{}
	withCachedATS(l, report, rs, base)
	// Kafka is met on the adapt page; Terraform is a Gap and outside the denominator.
	if got := rs[0].Metrics["must_have_evidence"]; got != 1 {
		t.Errorf("adapt must_have_evidence = %v", got)
	}
	if got := base[0].Metrics["must_have_evidence"]; got != 0 {
		t.Errorf("verbatim must_have_evidence = %v", got)
	}
	if naOf("must_have_evidence") != "" {
		t.Error("covered, but the row still says needs judge")
	}

	// An offer the cache has not seen keeps the row n/a.
	suite = append([]metric(nil), suite...)
	for i := range suite {
		if suite[i].name == "must_have_evidence" {
			suite[i].na = needsJudge
		}
	}
	other := []e3Result{rs[0], {Offer: "009", Page: "a different page\n", Metrics: map[string]float64{}}}
	withCachedATS(l, report, other, nil)
	if naOf("must_have_evidence") != needsJudge {
		t.Error("a partial cache filled the row")
	}
}

func naOf(name string) string {
	for _, m := range suite {
		if m.name == name {
			return m.na
		}
	}
	return "?"
}
