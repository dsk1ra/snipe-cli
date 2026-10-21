package verify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

type fakeChecker func(doc, claim string) bool

func (f fakeChecker) Check(_ context.Context, doc, claim string) (bool, error) {
	return f(doc, claim), nil
}

type errChecker struct{}

func (errChecker) Check(context.Context, string, string) (bool, error) {
	return false, errors.New("boom")
}

// yes supports everything; the tests that matter script their own.
var yes = fakeChecker(func(_, _ string) bool { return true })

func tokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words(strings.ToLower(s)) {
		m[w] = true
	}
	return m
}

// subset is a stand-in verifier: supported when every word of the claim is in
// the document. Crude, but false in exactly the ways the corruptions are.
var subset = fakeChecker(func(doc, claim string) bool {
	d := tokens(doc)
	for w := range tokens(claim) {
		if !d[w] {
			return false
		}
	}
	return true
})

func entry(texts ...string) (ledger.Entry, ledger.Bullet) {
	e := ledger.Entry{ID: "acme", Kind: "experience", Title: "Backend Engineer"}
	for i, t := range texts {
		e.Bullets = append(e.Bullets, ledger.Bullet{Text: t, Facts: []ledger.Fact{{ID: "acme/" + string(rune('1'+i)) + "a", Text: t}}})
	}
	return e, e.Bullets[0]
}

const src = "Built a simulated trading engine in Go handling 10k orders per second"

func TestFiguresMovedBetweenEntries(t *testing.T) {
	cases := map[string]string{
		"moved":   "Built a simulated trading engine in Go handling 40% more orders per second",
		"added":   "Built a simulated trading engine in Go handling 10k orders per second across 12 venues",
		"dropped": "Built a simulated trading engine in Go handling orders per second",
	}
	for name, d := range cases {
		if Figures(src, d) == "" {
			t.Errorf("%s: figures check passed %q", name, d)
		}
	}
	if r := Figures(src, "Developed a simulated trading engine in Go handling 10k orders per second"); r != "" {
		t.Errorf("same figures failed: %s", r)
	}
}

func TestEntityClosure(t *testing.T) {
	e, b := entry(src)
	e.Bullets[0].Facts[0].Entities = []string{"Go", "PostgreSQL"}
	forms := forms(e, map[string][]string{"PostgreSQL": {"Postgres"}}, []string{"Datadog"})

	for _, c := range []struct {
		name, draft string
		ok          bool
	}{
		{"tool from another entry", "Built a simulated trading engine in Go with Kafka handling 10k orders per second", false},
		{"new acronym", "Built a simulated trading engine in Go with AWS handling 10k orders per second", false},
		{"symbol token", "Built a simulated trading engine in Go with C++ handling 10k orders per second", false},
		{"hyphenated name", "Built a simulated Kubernetes-based engine in Go handling 10k orders per second", false},
		{"sentence-initial capital", "Developed a simulated trading engine in Go handling 10k orders per second", true},
		{"lowercase new word", "Built a simulated trading engine in Go serving 10k orders per second", true},
		{"surface form of a recorded entity", "Built a simulated trading engine in Go on Postgres handling 10k orders per second", true},
		{"allowed term", "Built a simulated trading engine in Go with Datadog handling 10k orders per second", true},
	} {
		if r := EntityClosure(b.Text, c.draft, forms); (r == "") != c.ok {
			t.Errorf("%s: reason %q", c.name, r)
		}
	}
}

func TestEntityClosurePendingInferenceDoesNotCount(t *testing.T) {
	e, b := entry(src)
	e.Bullets[0].Facts = append(e.Bullets[0].Facts, ledger.Fact{Text: "Used Docker", Entities: []string{"Docker"}, Source: "inferred", Status: ""})
	draft := "Built a simulated trading engine in Go on Docker handling 10k orders per second"
	if Code(e, b, draft, nil, Options{}) == "" {
		t.Fatal("a pending inference opened the closure")
	}
	e.Bullets[0].Facts[1].Status = "approved"
	if r := Code(e, b, draft, nil, Options{}); r != "" {
		t.Fatalf("approved inference rejected: %s", r)
	}
}

func TestLineCost(t *testing.T) {
	long := src + " and then some more words to push the bullet well past the width of one rendered line on the page"
	if LineCost(src, long) == "" {
		t.Error("a longer draft passed")
	}
	if r := LineCost(long, src); r != "" {
		t.Errorf("a shorter draft failed: %s", r)
	}
}

func TestShape(t *testing.T) {
	for name, d := range map[string]string{
		"two sentences": "Built an engine. It handled 10k orders.",
		"newline":       "Built an engine\nfor orders",
		"empty":         "  ",
		"tag list":      "Go/Kafka/Redis/Postgres/Docker engine for the orders pipeline",
	} {
		if Shape(src, d) == "" {
			t.Errorf("%s passed", name)
		}
	}
	if r := Shape(src, src+"."); r != "" {
		t.Errorf("one sentence failed: %s", r)
	}
	if r := Shape("Raised p99 to 99.9% on v2.1", "Raised p99 to 99.9% on v2.1."); r != "" {
		t.Errorf("decimals read as sentence breaks: %s", r)
	}
}

func TestVerifyEqualLengthRewritePasses(t *testing.T) {
	e, b := entry(src)
	draft := "Developed a simulated trading engine in Go serving 10k orders per second"
	r, err := Verify(context.Background(), yes, e, b, draft, nil)
	if err != nil || r.Class != Supported {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestVerifyDroppedSimulatedFailsPreservation(t *testing.T) {
	e, _ := entry(src)
	// "mock", not a word Qualifiers knows: this exercises the model's direction.
	e.Bullets[0].Text = "Built a trading engine in Go handling 10k orders per second on mock data"
	e.Bullets[0].Facts = []ledger.Fact{
		{ID: "acme/1a", Text: "Built a trading engine in Go", MustKeep: false},
		{ID: "acme/1b", Text: "The engine ran on mock data", MustKeep: true},
	}
	b := e.Bullets[0]
	draft := "Built a trading engine in Go handling 10k orders per second"

	var preserved []string
	c := fakeChecker(func(doc, claim string) bool {
		if strings.Contains(claim, "mock") { // preservation: doc is the draft
			preserved = append(preserved, claim)
			return strings.Contains(doc, "mock")
		}
		return true
	})
	r, err := Verify(context.Background(), c, e, b, draft, nil)
	if err != nil || r.Class != Dropped || !strings.HasPrefix(r.Reason, "preservation") {
		t.Fatalf("got %+v, %v", r, err)
	}
	if len(preserved) != 1 {
		t.Errorf("preservation asked %v", preserved)
	}
	// And the faithful draft keeps it.
	if r, _ := Verify(context.Background(), c, e, b, "Built a trading engine in Go handling 10k orders per second on mock data", nil); r.Class != Supported {
		t.Errorf("faithful draft: %+v", r)
	}
}

func TestVerifyInferredRetry(t *testing.T) {
	e, b := entry(src)
	e.Bullets[0].Facts = append(e.Bullets[0].Facts,
		ledger.Fact{ID: "acme/1x", Text: "INFERRED: used Docker", Entities: []string{"Docker"}, Source: "inferred", Status: "approved"})
	draft := "Built a simulated trading engine in Go on Docker handling 10k orders per second"
	needsInference := fakeChecker(func(doc, _ string) bool { return strings.Contains(doc, "INFERRED") })

	r, err := Verify(context.Background(), needsInference, e, b, draft, nil, Options{MustKeepOnly: true})
	if err != nil || r.Class != Inferred {
		t.Fatalf("got %+v, %v", r, err)
	}

	// The same draft with the inference rejected has nothing to fall back on.
	e.Bullets[0].Facts[1].Status = "rejected"
	if r, _ := Verify(context.Background(), needsInference, e, b, draft, []string{"Docker"}, Options{MustKeepOnly: true}); r.Class != Dropped {
		t.Errorf("rejected inference: %+v", r)
	}
}

func TestVerifyDroppedWhenBothAttributionsFail(t *testing.T) {
	e, b := entry(src)
	e.Bullets[0].Facts = append(e.Bullets[0].Facts, ledger.Fact{Text: "Used Docker", Source: "inferred", Status: "approved"})
	no := fakeChecker(func(_, _ string) bool { return false })
	r, _ := Verify(context.Background(), no, e, b, "Developed a simulated trading engine in Go serving 10k orders per second", nil)
	if r.Class != Dropped {
		t.Errorf("got %+v", r)
	}
}

func TestVerifyCodeChecksRunBeforeModel(t *testing.T) {
	e, b := entry(src)
	called := false
	c := fakeChecker(func(_, _ string) bool { called = true; return true })
	r, _ := Verify(context.Background(), c, e, b, "Built a simulated trading engine in Go with Kafka handling 10k orders per second", nil)
	if r.Class != Dropped || !strings.HasPrefix(r.Reason, "entities") || called {
		t.Errorf("got %+v, model called: %v", r, called)
	}
}

func TestVerifyCheckerErrorIsNeverAPass(t *testing.T) {
	e, b := entry(src)
	r, err := Verify(context.Background(), errChecker{}, e, b, "Developed a simulated trading engine in Go serving 10k orders per second", nil)
	if err == nil || r.Class != Dropped {
		t.Errorf("got %+v, %v", r, err)
	}
}

func TestVerifySplit(t *testing.T) {
	e, b := entry("Built a simulated trading engine in Go, handling 10k orders per second; cut p99 latency")
	var claims []string
	c := fakeChecker(func(_, claim string) bool { claims = append(claims, claim); return true })
	if _, err := Verify(context.Background(), c, e, b, b.Text, nil, Options{MustKeepOnly: true}); err != nil || len(claims) != 1 {
		t.Fatalf("default should send the whole bullet once: %v", claims)
	}

	claims = nil
	r, _ := Verify(context.Background(), c, e, b, b.Text, nil, Options{Split: ByClause, MustKeepOnly: true})
	if r.Class != Supported || len(claims) < 2 {
		t.Fatalf("split sent %v", claims)
	}

	// One bad clause fails the whole bullet.
	bad := fakeChecker(func(_, claim string) bool { return !strings.Contains(claim, "latency") })
	if r, _ := Verify(context.Background(), bad, e, b, b.Text, nil, Options{Split: ByClause, MustKeepOnly: true}); r.Class != Dropped {
		t.Errorf("bad clause: %+v", r)
	}
}

func TestByClause(t *testing.T) {
	got := ByClause("Built a trading engine in Go, handling 10k orders; cut latency")
	if len(got) != 2 || got[0] != "Built a trading engine in Go" || got[1] != "handling 10k orders, cut latency" {
		t.Errorf("got %q", got)
	}
}

// The two misses E2 found in MiniCheck's whole-bullet check, now caught in code.
func TestQualifiersAndScope(t *testing.T) {
	src := "Built a SIEM tested at 250M+ simulated authentication events a quarter"
	if Qualifiers(src, "Built a SIEM tested at 250M+ authentication events a quarter") == "" {
		t.Error("dropped 'simulated' passed")
	}
	if r := Qualifiers(src, "Built a SIEM tested at 250M+ simulated authentication events"); r != "" {
		t.Errorf("kept qualifier failed: %s", r)
	}
	if Scope("Designed the key exchange", "Led the key exchange", nil) == "" {
		t.Error("Designed → Led passed")
	}
	if r := Scope("Designed the key exchange", "Led the key exchange", []string{"led"}); r != "" {
		t.Errorf("licensed verb failed: %s", r)
	}
	if r := Scope("Technical lead of a two-developer team", "Technical lead of two developers", nil); r != "" {
		t.Errorf("verb already in the source failed: %s", r)
	}
}

// E3 shipped "...10 Java and Spring Boot services, each owning its own
// database" with "covering orders, stock, pricing, delivery, loyalty and
// finance" gone. That clause was not must_keep, so only the default catches it.
func TestVerifyDroppedPlainFactFailsPreservation(t *testing.T) {
	e, _ := entry("Built a trading engine in Go covering orders and stock")
	e.Bullets[0].Facts = []ledger.Fact{
		{ID: "acme/1a", Text: "Built a trading engine in Go"},
		{ID: "acme/1b", Text: "The engine covers orders and stock"},
	}
	b := e.Bullets[0]
	keeps := fakeChecker(func(doc, claim string) bool {
		return !strings.Contains(claim, "orders and stock") || strings.Contains(doc, "orders")
	})
	draft := "Built a trading engine in Go"
	if r, _ := Verify(context.Background(), keeps, e, b, draft, nil); r.Class != Dropped || !strings.HasPrefix(r.Reason, "preservation") {
		t.Errorf("default let a dropped fact through: %+v", r)
	}
	if r, _ := Verify(context.Background(), keeps, e, b, draft, nil, Options{MustKeepOnly: true}); r.Class != Supported {
		t.Errorf("MustKeepOnly should ignore it: %+v", r)
	}
}

func TestStuffing(t *testing.T) {
	src := "Took a 700-recipient newsletter send from 8 minutes to 28 seconds with a Redis-backed worker pool"
	stuffed := src + ", eliminating blocking for asynchronous workloads"
	if Stuffing(src, stuffed, []string{"asynchronous workloads"}, nil) == "" {
		t.Error("appended term passed")
	}
	mid := "Took a 700-recipient newsletter send from 8 minutes to 28 seconds using asynchronous workloads on a Redis-backed worker pool"
	if r := Stuffing(src, mid, []string{"asynchronous workloads"}, nil); r != "" {
		t.Errorf("mid-sentence use failed: %s", r)
	}
	ents := map[string][]string{"PostgreSQL": {"Postgres"}}
	if r := Stuffing("Ran reports on Postgres", "Ran the reports on PostgreSQL", []string{"PostgreSQL"}, ents); r != "" {
		t.Errorf("entity swap failed: %s", r)
	}
	if r := Stuffing(src, src, []string{"worker pool"}, nil); r != "" {
		t.Errorf("term the source ends on failed: %s", r)
	}
}

func TestLineCostMargin(t *testing.T) {
	src := strings.Repeat("word ", 40)[:200] // 2 lines, 200 of 216 characters
	if LineCost(src, src+" more") == "" {
		t.Error("a longer draft ending near the line end passed")
	}
	if r := LineCost(src, src[:190]); r != "" {
		t.Errorf("a shorter draft failed: %s", r)
	}
}

func TestShapeRejectsNewEmDash(t *testing.T) {
	if Shape("Built services for orders and stock", "Built services—orders and stock") == "" {
		t.Error("new em dash passed")
	}
	if r := Shape("Built services — orders", "Built services — orders and stock"); r != "" {
		t.Errorf("em dash already in the source failed: %s", r)
	}
}
