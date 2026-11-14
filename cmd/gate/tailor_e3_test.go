package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

func TestStratify(t *testing.T) {
	var cs []cand
	for i := 0; i < 40; i++ {
		cs = append(cs, cand{ID: fmt.Sprintf("%03d", i+1), Score: 1 + float64(i)*0.1}) // 1.0 .. 4.9
	}
	rev := make([]cand, len(cs))
	for i, c := range cs {
		rev[len(cs)-1-i] = c
	}
	a, b := stratify(cs, 8, 4.0), stratify(rev, 8, 4.0)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("input order changed the sample:\n%v\n%v", a, b)
	}
	if len(a) != 8 {
		t.Fatalf("got %d", len(a))
	}
	hi, lo, top := 0, 9.0, 0.0
	for _, c := range a {
		if c.Score >= 4.0 {
			hi++
		}
		lo, top = math.Min(lo, c.Score), math.Max(top, c.Score)
	}
	if hi < 2 {
		t.Errorf("%d at or above the infer bar, want a quarter", hi)
	}
	if lo > 1.6 || top < 4.5 {
		t.Errorf("sample covers only %.1f..%.1f", lo, top)
	}
	// Fewer high ones than a quarter: take all there are.
	few := stratify(append(cs[:30:30], cand{"900", 4.5, ""}), 8, 4.0)
	if hi := countHigh(few, 4.0); hi != 1 {
		t.Errorf("one posting clears the bar, sample has %d", hi)
	}
	if got := stratify(cs[:5], 8, 4.0); len(got) != 5 {
		t.Errorf("smaller than n: %d", len(got))
	}
}

func countHigh(cs []cand, hi float64) (n int) {
	for _, c := range cs {
		if c.Score >= hi {
			n++
		}
	}
	return n
}

// A state tree of six offers, enough for sample() to read and save.
func sampleHome(t *testing.T) *tailorEnv {
	t.Helper()
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	rows := map[int]state.Row{}
	for i := 1; i <= 6; i++ {
		num := fmt.Sprintf("%03d", i)
		rows[i+100] = state.Row{ID: i + 100, ReportNum: num, P2Status: state.P2Evaled}
		must(t, os.MkdirAll(l.Evals(), 0o755))
		must(t, os.MkdirAll(l.JDs(), 0o755))
		must(t, os.MkdirAll(l.Reports(), 0o755))
		ev := fmt.Sprintf(`{"status":"evaled","score":%.1f,"company":"Co%d"}`, 2+float64(i)*0.5, i)
		must(t, os.WriteFile(filepath.Join(l.Evals(), fmt.Sprintf("%d.json", i+100)), []byte(ev), 0o644))
		must(t, os.WriteFile(filepath.Join(l.JDs(), fmt.Sprintf("%d.txt", i+100)), []byte("jd"), 0o644))
		must(t, os.WriteFile(filepath.Join(l.Reports(), num+"-co-2026-01-01.md"), []byte("r"), 0o644))
	}
	must(t, state.Save(l.State(), rows))
	cfg := &config.Config{}
	cfg.Thresholds.InferMinScore = 4.0
	return &tailorEnv{l: l, cfg: cfg}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSampleSavedAndReused(t *testing.T) {
	e := sampleHome(t)
	var out bytes.Buffer
	ids, err := e.sample(&out, 4, false)
	must(t, err)
	if len(ids) != 4 {
		t.Fatalf("ids %v", ids)
	}
	if !strings.Contains(out.String(), "Co") || !strings.Contains(out.String(), "with score >= 4.0") {
		t.Errorf("the chosen ids are not printed with score and company:\n%s", out.String())
	}
	saved, err := os.ReadFile(samplePath(e))
	must(t, err)
	if got := parseOfferIDs(strings.Split(string(saved), "\n")); !reflect.DeepEqual(got, ids) {
		t.Errorf("file holds %v, run %v", got, ids)
	}

	// The state changes; the saved list does not, until --resample.
	must(t, os.Remove(filepath.Join(e.l.Reports(), "003-co-2026-01-01.md")))
	again, err := e.sample(&bytes.Buffer{}, 4, false)
	must(t, err)
	if !reflect.DeepEqual(again, ids) {
		t.Errorf("rerun drew %v, first run %v", again, ids)
	}
	fresh, err := e.sample(&bytes.Buffer{}, 4, true)
	must(t, err)
	for _, id := range fresh {
		if id == "003" {
			t.Error("resample picked an offer with no report")
		}
	}
}

// ── metrics on a hand-built page and ledger ─────────────────────────────────

const (
	b1 = "Built a live membership platform with Stripe subscriptions"
	b2 = "Cut p99 latency of the order saga by 40%"
)

func tinyLedger() *ledger.Ledger {
	return &ledger.Ledger{
		Entities: map[string][]string{"PostgreSQL": {"Postgres"}},
		Entries: []ledger.Entry{
			{ID: "ubwis", Kind: "experience", Title: "UBWIS", Bullets: []ledger.Bullet{{
				Hash: ledger.Hash(b1), Text: b1,
				Facts: []ledger.Fact{
					{ID: "ubwis/1a", Text: b1, Entities: []string{"Stripe"}},
					{ID: "ubwis/1b", Text: "Moved billing data to Postgres", Entities: []string{"Postgres"}},
				},
			}}},
			{ID: "acme", Kind: "experience", Title: "Acme", Bullets: []ledger.Bullet{{
				Hash: ledger.Hash(b2), Text: b2,
				Facts: []ledger.Fact{{ID: "acme/1a", Text: b2, Figures: []string{"p99", "40%"}, MustKeep: true}},
			}}},
		},
	}
}

const tinyReport = `## Keywords

Stripe, PostgreSQL, Kubernetes

| JD Requirement | Candidate evidence | Strength |
|---|---|---|
| **[must]** Stripe and PostgreSQL experience | built | strong |
| Kubernetes in production | none | gap |
`

func tinyOffer() offer {
	return offer{ID: "012", Company: "Acme", Score: 4.2, Text: tinyReport, Reqs: []string{"Stripe and PostgreSQL experience"}}
}

func TestScoreOfferVerbatim(t *testing.T) {
	out := armOut{Page: fixturePage + "\n Postgres tuning\n", Selected: []string{b1, b2}}
	r := scoreOffer(tinyOffer(), out, tinyLedger(), 4.0)
	want := map[string]float64{
		"supported_share": 1, "inferred_share": 0, "unapproved": 0, "entity_violations": 0,
		"rewrite_rate": 0, "keep_rate": 1,
		// Stripe and PostgreSQL are supported and named; the page spells the
		// second Postgres. Kubernetes has no fact, so it is not asked of the page.
		"term_parity": 0.5, "boolean_pass": 0,
	}
	for k, v := range want {
		if got, ok := r.Metrics[k]; !ok || math.Abs(got-v) > 1e-9 {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
	if _, ok := r.Metrics["shape_rejects"]; ok {
		t.Error("verbatim ran no rewrite, so shape_rejects is undefined")
	}
	if !reflect.DeepEqual(r.BooleanMissing, []string{"PostgreSQL"}) {
		t.Errorf("missing %v", r.BooleanMissing)
	}
	if len(r.Kill) != 0 || len(r.Changed) != 0 {
		t.Errorf("nothing changed: %+v", r)
	}
}

func TestScoreOfferAdapt(t *testing.T) {
	page := strings.Replace(fixturePage, "by 40%", "substantially", 1) + "\n PostgreSQL tuning\n"
	shipped := "Built a live membership platform with Stripe and Kubernetes subscriptions"
	out := armOut{
		Page: page, Selected: []string{b1, b2},
		Stats: &adapt.Stats{Rejects: map[string]int{"shape": 2, "figures": 1}},
		Truth: &adapt.TruthFile{
			Offer:   "012",
			Entries: []adapt.EntryTruth{{Entry: "ubwis", Supported: 3, Inferred: 1}, {Entry: "acme", Supported: 1}},
			Bullets: []adapt.BulletTruth{
				{Entry: "ubwis", Source: "A", Kind: "supported", Class: "supported", Original: b1, Shipped: shipped, Why: "A: evidence 2"},
				{Entry: "acme", Source: "original", Kind: "verbatim", Class: "supported", Original: b2, Shipped: b2},
			},
		},
	}
	r := scoreOffer(tinyOffer(), out, tinyLedger(), 4.0)
	check := func(k string, want float64) {
		t.Helper()
		if got, ok := r.Metrics[k]; !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, want)
		}
	}
	check("shape_rejects", 2)
	check("supported_share", 0.75) // the minimum over roles
	check("inferred_share", 0.25)  // the maximum
	check("unapproved", 0)
	check("entity_violations", 1) // Kubernetes: not in the source, not in the entry
	check("rewrite_rate", 0.5)
	check("keep_rate", 0.5) // p99 survived, 40% did not
	check("term_parity", 1)
	check("boolean_pass", 1)
	if len(r.Changed) != 1 || r.Changed[0].Closure == "" || r.Changed[0].Why == "" {
		t.Fatalf("changed %+v", r.Changed)
	}
	if len(r.Kill) != 1 || !strings.Contains(r.Kill[0], "Kubernetes") || !strings.Contains(r.Kill[0], b1) {
		t.Errorf("kill %q", r.Kill)
	}

	// A claim that is neither supported nor inferred names its bullet.
	out.Truth.Entries[1].Unapproved = 1
	out.Truth.Bullets[1].Class = "dropped"
	r = scoreOffer(tinyOffer(), out, tinyLedger(), 4.0)
	if r.Metrics["unapproved"] != 1 || !strings.Contains(strings.Join(r.Kill, "\n"), "unapproved: [acme]") {
		t.Errorf("unapproved %v, kill %q", r.Metrics["unapproved"], r.Kill)
	}
}

func TestPageHas(t *testing.T) {
	for _, c := range []struct {
		s, term string
		want    bool
	}{
		{"wrote Go services", "Go", true},
		{"worked at Google", "Go", false},
		{"deploys with C++ and rust", "C++", true},
		{"Kafkaesque", "Kafka", false},
		{"cut by 40%, then", "40%", true},
		{"cut by 140%", "40%", false},
	} {
		if got := pageHas(c.s, c.term); got != c.want {
			t.Errorf("pageHas(%q, %q) = %v", c.s, c.term, got)
		}
	}
}

// ── kill rule ───────────────────────────────────────────────────────────────

func TestKillRule(t *testing.T) {
	clean := e3Result{Offer: "001", Metrics: map[string]float64{"unapproved": 0, "entity_violations": 0}}
	var out bytes.Buffer
	if code := killRule(&out, []e3Result{clean, {Offer: "002", Err: "boom"}}); code != 0 || !strings.Contains(out.String(), "kill rule: clear") {
		t.Errorf("code %d\n%s", code, out.String())
	}

	bad := e3Result{
		Offer: "012", Company: "Acme", Metrics: map[string]float64{"unapproved": 0, "entity_violations": 2},
		Kill: []string{"entity: unrecorded term Kubernetes, Helm\n        shipped:  the shipped line"},
	}
	out.Reset()
	if code := killRule(&out, []e3Result{clean, bad}); code != 1 {
		t.Errorf("code %d", code)
	}
	for _, want := range []string{"KILL: offer 012 (Acme)", "entity_violations 2", "Kubernetes, Helm", "the shipped line"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "kill rule: clear") {
		t.Error("cleared a breach")
	}
}

// ── resume ──────────────────────────────────────────────────────────────────

func fakeRunner(t *testing.T, calls map[string]int) *e3Runner {
	return &e3Runner{
		arm: "adapt", led: tinyLedger(), inferMin: 4, keyL: "L1", keyP: "P1",
		path: filepath.Join(t.TempDir(), "cache", "e3-adapt.json"), log: &bytes.Buffer{},
		offer: func(id string) (offer, error) {
			if id == "404" {
				return offer{}, fmt.Errorf("no report %s", id)
			}
			o := tinyOffer()
			o.ID = id
			return o, nil
		},
		run: func(o offer) (armOut, error) {
			calls[o.ID]++
			switch o.ID {
			case "500":
				return armOut{}, fmt.Errorf("render failed")
			case "666":
				panic("bad page")
			}
			return armOut{Page: fixturePage, Selected: []string{b1, b2}}, nil
		},
	}
}

func TestResume(t *testing.T) {
	calls := map[string]int{}
	r := fakeRunner(t, calls)
	ids := []string{"001", "404", "500", "666", "002"}

	rs := r.do(ids)
	if len(rs) != 5 {
		t.Fatalf("an erroring offer aborted the run: %d results", len(rs))
	}
	for i, wantErr := range []bool{false, true, true, true, false} {
		if (rs[i].Err != "") != wantErr {
			t.Errorf("%s: err %q", ids[i], rs[i].Err)
		}
	}
	if !strings.Contains(rs[3].Err, "panic") {
		t.Errorf("panic not recorded: %q", rs[3].Err)
	}

	// Same key: the good offers are not run again, the failed ones are.
	rs = r.do(ids)
	if calls["001"] != 1 || calls["002"] != 1 {
		t.Errorf("finished offers reran: %v", calls)
	}
	if calls["500"] != 2 || calls["666"] != 2 {
		t.Errorf("failed offers were not retried: %v", calls)
	}
	if rs[0].Metrics["lines"] == 0 {
		t.Error("the cached result lost its metrics")
	}

	// The file on disk is what resumed.
	var f e3File
	b, err := os.ReadFile(r.path)
	must(t, err)
	must(t, json.Unmarshal(b, &f))
	if g, ok := f.get("001"); !ok || g.Page == "" || g.Arm != "adapt" || g.Ledger != "L1" {
		t.Errorf("results file: %+v", g)
	}

	// A new ledger or prompt hash invalidates; --aa needs the second run too.
	r.keyP = "P2"
	r.do([]string{"001"})
	if calls["001"] != 2 {
		t.Errorf("a changed prompt hash must rerun: %v", calls)
	}
	r.aa = true
	r.do([]string{"001"})
	if calls["001"] != 4 { // once for the stale entry's missing B, and its B run
		t.Errorf("--aa reran %d times", calls["001"]-2)
	}
	if g := loadE3(r.path); func() bool { x, _ := g.get("001"); return x.B == nil }() {
		t.Error("no A/A metrics saved")
	}
	r.do([]string{"001"})
	if calls["001"] != 4 {
		t.Errorf("a complete A/A result was run again: %v", calls)
	}
}

func TestReportPairsAgainstBaseline(t *testing.T) {
	mk := func(id string, lf float64, aa float64) e3Result {
		return e3Result{Offer: id, Metrics: map[string]float64{"front_load": lf}, B: map[string]float64{"front_load": aa}, AdaptS: 30}
	}
	base := []e3Result{mk("001", 0.2, 0.2), mk("002", 0.4, 0.4)}
	rs := []e3Result{mk("001", 0.5, 0.5), mk("002", 0.7, 0.7), {Offer: "003", Err: "x"}}
	var out bytes.Buffer
	report(&out, "adapt", rs, base)
	s := out.String()
	for _, want := range []string{"1 FAILED", "+0.300", "within the 200 s", "must_have_evidence", "n/a (needs judge)", "beyond floor"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}

func TestE3Verdict(t *testing.T) {
	text := "Ran the platform on AWS from Terraform and GitHub Actions"
	led := &ledger.Ledger{Entries: []ledger.Entry{{ID: "x", Bullets: []ledger.Bullet{
		{Text: text, Framings: []ledger.Framing{{Phrase: "DevOps", Approved: true}}},
	}}}}
	m := func(boolean float64) map[string]float64 {
		return map[string]float64{"boolean_pass": boolean, "one_page": 1, "keep_rate": 1, "front_load": 0.3, "exp_starved": 0}
	}
	base := []e3Result{
		{Offer: "1", Page: "Work\n • " + text + "\n", BooleanMissing: []string{"DevOps"}, Metrics: m(0)},
		{Offer: "2", Page: "Work\n • something else\n", BooleanMissing: []string{"DevOps"}, Metrics: m(0)},
		{Offer: "3", Page: "Work\n", Metrics: m(1)},
	}
	run := func(rs []e3Result) string {
		var b strings.Builder
		e3Verdict(&b, rs, base, led)
		return b.String()
	}
	out := run([]e3Result{{Offer: "1", Metrics: m(1)}, {Offer: "2", Metrics: m(0)}, {Offer: "3", Metrics: m(1)}})
	for _, want := range []string{"PASS  primary", "1 recovered of 1 recoverable, 0 lost", "PASS  safety", "PASS  no regress", "verdict: met"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Recovering one and losing another is no gain.
	out = run([]e3Result{{Offer: "1", Metrics: m(1)}, {Offer: "2", Metrics: m(0)}, {Offer: "3", Metrics: m(0)}})
	if !strings.Contains(out, "FAIL  primary") || !strings.Contains(out, "verdict: NOT MET") {
		t.Errorf("a lost offer passed:\n%s", out)
	}
	// A page that grew onto a second page is a regression whatever it gained.
	two := m(1)
	two["one_page"] = 0
	out = run([]e3Result{{Offer: "1", Metrics: two}, {Offer: "2", Metrics: m(0)}, {Offer: "3", Metrics: m(1)}})
	if !strings.Contains(out, "FAIL  no regress  one_page") {
		t.Errorf("a two-page CV passed:\n%s", out)
	}
}
