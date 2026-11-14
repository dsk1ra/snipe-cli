package adapt

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

// fakeOllama answers /api/chat with whatever handle returns for the request,
// and keeps every request body for the test to read.
type fakeOllama struct {
	*ollama.Client
	mu   sync.Mutex
	reqs []map[string]any
}

type pair struct {
	N    int
	A, B string
}

func newFake(t *testing.T, handle func(call int, user string) []pair) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, body)
		call := len(f.reqs)
		f.mu.Unlock()
		msgs := body["messages"].([]any)
		user := msgs[len(msgs)-1].(map[string]any)["content"].(string)
		var rows []map[string]any
		for _, p := range handle(call, user) {
			rows = append(rows, map[string]any{"n": p.N, "a": p.A, "b": p.B})
		}
		content, _ := json.Marshal(map[string]any{"bullets": rows})
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": string(content)}})
	}))
	t.Cleanup(srv.Close)
	f.Client = ollama.New(srv.URL, nil)
	return f
}

func rewritePosting() Posting {
	return Posting{
		Rows: []phase2.Row{
			row("Kafka-based messaging between services", true, fit.Strong, ev("Store Platform", svcs)),
			row("Operating services on AWS", true, fit.Strong, ev("Engineer - Acme", deploy)),
		},
		Terms: append(terms(KindTool, "Kafka", "Java", "Spark", "AWS"), terms(KindCapability, "event-driven microservices", "database-per-service")...),
	}
}

const goodB = "Designed 10 event-driven Java and Spring Boot services over a Kafka log, each owning its own database"

func TestRewriteMapsNumbersToBullets(t *testing.T) {
	l := fixture()
	plans := PlanAll(l, []string{svcs, deploy}, rewritePosting())
	f := newFake(t, func(int, string) []pair {
		return []pair{
			{2, "Ran the platform on AWS from Terraform and GitHub Actions, shipping each merge to production", ""},
			{1, "", goodB},
			{9, "Built 10 Java and Spring Boot services", ""}, // no such bullet
			{0, "Built 10 Java and Spring Boot services", ""},
			{1, "Built 10 Java and Spring Boot services over Kafka", ""}, // a second answer for 1
		}
	})
	got, st, err := Rewrite(t.Context(), f.Client, "sys", plans, l, "jd")
	if err != nil {
		t.Fatal(err)
	}
	want := []Draft{
		{Hash: ledger.Hash(deploy), Kind: "A", Text: "Ran the platform on AWS from Terraform and GitHub Actions, shipping each merge to production"},
		{Hash: ledger.Hash(svcs), Kind: "B", Text: goodB},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("drafts = %+v, want %+v", got, want)
	}
	if st.Rejects["number"] != 3 || st.Rejects["empty"] != 2 || st.Calls != 1 || st.Kept != 2 {
		t.Errorf("stats = %+v", st)
	}
}

func TestRewriteRequestShape(t *testing.T) {
	l := fixture()
	f := newFake(t, func(int, string) []pair { return nil })
	plans := PlanAll(l, []string{svcs}, rewritePosting())
	if _, _, err := Rewrite(t.Context(), f.Client, "the system prompt", plans, l, strings.Repeat("Q", 3000)); err != nil {
		t.Fatal(err)
	}
	req := f.reqs[0]
	if req["think"] != false {
		t.Errorf("think = %v, want false", req["think"])
	}
	opts := req["options"].(map[string]any)
	if opts["temperature"] != 0.0 || opts["num_ctx"] != float64(12288) || opts["num_predict"] == nil {
		t.Errorf("options = %v", opts)
	}
	if req["format"] == nil {
		t.Error("no schema")
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["content"] != "the system prompt" {
		t.Errorf("messages = %v", msgs)
	}
	user := msgs[1].(map[string]any)["content"].(string)
	if strings.Count(user, "Q") != excerptMax {
		t.Errorf("posting excerpt is %d chars, want %d", strings.Count(user, "Q"), excerptMax)
	}
}

func TestRewriteFilter(t *testing.T) {
	l := fixture()
	plans := PlanAll(l, []string{svcs}, rewritePosting())
	for name, tc := range map[string]struct{ a, b, reason string }{
		"capitalised forbidden term": {"Built 10 Java and Spring Boot services over a Kafka log, with Spark", goodB, "forbidden"},
		"unknown capitalised name":   {"Built 10 Java and Spring Boot services over a Kafka log backed by Redis", goodB, "entities"},
		"lower-case forbidden term":  {"Built 10 Java and Spring Boot services over Kafka, each following database-per-service", goodB, "forbidden"},
		"figure changed":             {"Built 12 Java and Spring Boot services over a Kafka log, each owning its own database", goodB, "figures"},
		"figure dropped":             {"Built Java and Spring Boot services over a Kafka log, each owning its own database", goodB, "figures"},
		"longer than the source":     {svcs + ", so that the teams behind them could release independently of each other and of the finance system", goodB, "line cost"},
		"two sentences":              {"Built 10 Java and Spring Boot services. Kafka carries the traffic", goodB, "shape"},
		"identical to the source":    {svcs, goodB, "unchanged"},
		"source with a final stop":   {svcs + ".", goodB, "unchanged"},
		"B repeats A":                {goodB, goodB, "duplicate"},
	} {
		f := newFake(t, func(int, string) []pair { return []pair{{1, tc.a, tc.b}} })
		_, st, err := Rewrite(t.Context(), f.Client, "sys", plans, l, "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Rejects[tc.reason] != 1 {
			t.Errorf("%s: want one %q reject, got %v", name, tc.reason, st.Rejects)
		}
	}

	// shape_rejects is what the §8.1 metric reads.
	f := newFake(t, func(int, string) []pair {
		return []pair{{1, "Built 10 Java and Spring Boot services. Kafka carries the traffic", goodB}}
	})
	got, st, _ := Rewrite(t.Context(), f.Client, "sys", plans, l, "")
	if st.ShapeRejects() != 1 || len(got) != 1 || got[0].Kind != "B" {
		t.Errorf("got %+v, stats %+v", got, st)
	}
}

func TestRewriteChunksAtFive(t *testing.T) {
	l := fixture()
	var sel []string
	for range 7 {
		sel = append(sel, svcs)
	}
	plans := PlanAll(l, sel, rewritePosting())
	f := newFake(t, func(int, string) []pair { return nil })
	_, st, err := Rewrite(t.Context(), f.Client, "sys", plans, l, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Calls != 2 || len(f.reqs) != 2 {
		t.Fatalf("calls = %d, want 2", st.Calls)
	}
	for i, want := range []int{5, 2} {
		msgs := f.reqs[i]["messages"].([]any)
		if got := strings.Count(msgs[1].(map[string]any)["content"].(string), "\n### "); got != want {
			t.Errorf("call %d carries %d bullets, want %d", i+1, got, want)
		}
	}
	// Every draft may run to its cap, and the budget must cover that.
	if n := f.reqs[0]["options"].(map[string]any)["num_predict"].(float64); n < 5*2*80 || n > 2000 {
		t.Errorf("num_predict = %v for five two-line bullets", n)
	}
}

func TestRewriteSplitsWhenPromptWouldOverflowWindow(t *testing.T) {
	l := fixture()
	huge := job{plan: BuildPlan(l, svcs, rewritePosting())}
	huge.plan.Bullet.Facts = append([]ledger.Fact{}, huge.plan.Bullet.Facts...)
	huge.plan.Bullet.Facts[0].Text = strings.Repeat("filler ", 6000)
	small := job{plan: BuildPlan(l, deploy, rewritePosting())}
	got := chunks([]job{small, huge, small}, "sys", "jd", l)
	if len(got) != 3 {
		t.Errorf("chunks = %d, want the oversized bullet alone", len(got))
	}
}

func TestRewriteMakesNoCallWhenNothingToAdapt(t *testing.T) {
	l := fixture()
	p := rewritePosting()
	plans := []Plan{
		BuildPlan(l, "A line the ledger has never seen", p), // Verbatim
		BuildPlan(l, billing, Posting{}),                    // no targets, no terms
	}
	if !plans[0].Verbatim || worthAdapting(plans[1]) {
		t.Fatal("fixture drifted")
	}
	f := newFake(t, func(int, string) []pair { t.Error("called"); return nil })
	got, st, err := Rewrite(t.Context(), f.Client, "sys", plans, l, "jd")
	if err != nil || len(got) != 0 || st.Calls != 0 || st.Skipped != 2 {
		t.Errorf("got %v, %+v, %v", got, st, err)
	}
	if len(f.reqs) != 0 {
		t.Errorf("%d requests", len(f.reqs))
	}
}

func TestRewriteErrorOnlyWhenEveryCallFailed(t *testing.T) {
	l := fixture()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }))
	defer srv.Close()
	plans := PlanAll(l, []string{svcs}, rewritePosting())
	got, st, err := Rewrite(t.Context(), ollama.New(srv.URL, nil), "sys", plans, l, "")
	if err == nil || len(got) != 0 || st.Failed != 1 {
		t.Errorf("got %v, %+v, %v", got, st, err)
	}
}

// lineChars is a copy of an unexported constant; this is what keeps it honest.
func TestLineChars(t *testing.T) {
	for k := 1; k <= 3; k++ {
		if cvselect.BulletLines(strings.Repeat("x", k*lineChars)) != k || cvselect.BulletLines(strings.Repeat("x", k*lineChars+1)) != k+1 {
			t.Fatalf("lineChars %d no longer matches cvselect", lineChars)
		}
	}
}

func TestPromptFileStatesTheHardRules(t *testing.T) {
	b, err := os.ReadFile("../../prompts/p3-rewrite.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"forbidden term", "figure", "qualifiers to keep", "employer", "one sentence", "approved framing", "sentence structure", "Max lines"} {
		if !strings.Contains(strings.ToLower(string(b)), strings.ToLower(want)) {
			t.Errorf("prompt does not mention %q", want)
		}
	}
}

// The full request for the de-store services bullet, from the live ledger and
// a hand-written posting, so the lead can read what the model is sent. Nothing
// is asserted beyond the bullet being there.
func TestLivePromptForServicesBullet(t *testing.T) {
	home, _ := os.UserHomeDir()
	l, err := ledger.Load(filepath.Join(home, "projects/snipe-cli/batch/state/cv-ledger.json"))
	if err != nil || len(l.Entries) == 0 {
		t.Skip("no live ledger")
	}
	var text string
	for _, e := range l.Entries {
		for _, b := range e.Bullets {
			if e.ID == "de-store" && strings.Contains(b.Text, "10 Java and Spring Boot") {
				text = b.Text
			}
		}
	}
	if text == "" {
		t.Skip("services bullet not in this ledger")
	}
	p := Posting{
		Score: 4.3,
		Rows: []phase2.Row{
			row("Experience designing event-driven microservices on Kafka", true, fit.Strong, "Distributed Store Management Platform: "+text),
			row("Operating services on Kubernetes", false, fit.Transferable, "Distributed Store Management Platform: "+text),
		},
		Terms: append(terms(KindTool, "Kafka", "Kubernetes", "Spark", "Java"), terms(KindCapability, "microservices", "event-driven microservices")...),
	}
	plan := BuildPlan(l, text, p)
	e, _ := find(l, text)
	sys, err := os.ReadFile("../../prompts/p3-rewrite.md")
	if err != nil {
		t.Fatal(err)
	}
	jd := "We are hiring a backend engineer to design and run event-driven microservices on Kafka. You will own services end to end, from design through production, in a small team that ships daily."
	user := render([]job{{plan, *e}}, jd, l)
	if !strings.Contains(user, text) {
		t.Fatal("source missing from prompt")
	}
	t.Logf("\n===== SYSTEM =====\n%s\n===== USER =====\n%s\n===== num_predict %d, ~%d prompt tokens =====",
		sys, user, predict([]job{{plan, *e}}), int(float64(len(sys)+len(user))/charsPerToken))
	_ = fmt.Sprint
}

// Under FramingGrow, a draft may take one more line, and only one that adds an
// approved framing the posting asks for: the same extra length spent on
// anything else is still over the line budget.
func TestRewriteGrowsOnlyForAFraming(t *testing.T) {
	// The fixture's services line, lengthened to sit just under two lines so
	// that swapping in the framing tips it onto a third.
	l := fixture()
	b := &l.Entries[0].Bullets[1]
	b.Text += ", covering orders, stock, pricing, delivery, loyalty and finance"
	b.Hash = ledger.Hash(b.Text)
	src := b.Text

	p := rewritePosting()
	p.FramingGrow = true
	grow := PlanAll(l, []string{src}, p)
	if !reflect.DeepEqual(grow[0].GrowFor, []string{"event-driven microservices"}) {
		t.Fatalf("GrowFor = %v", grow[0].GrowFor)
	}
	framed := strings.Replace(src, "services", "event-driven microservices", 1)
	padded := src + " across every store"
	if n := grow[0].MaxLines; cvselect.BulletLines(framed) != n+1 || cvselect.BulletLines(padded) != n+1 {
		t.Fatalf("fixture lines: framed %d, padded %d, source %d", cvselect.BulletLines(framed), cvselect.BulletLines(padded), n)
	}
	if grow[0].LinesFor(framed) != grow[0].MaxLines+1 || grow[0].LinesFor(padded) != grow[0].MaxLines {
		t.Errorf("LinesFor: framed %d, padded %d", grow[0].LinesFor(framed), grow[0].LinesFor(padded))
	}

	run := func(plans []Plan, a string) Stats {
		f := newFake(t, func(int, string) []pair { return []pair{{1, a, ""}} })
		_, st, err := Rewrite(t.Context(), f.Client, "sys", plans, l, "")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := run(grow, framed); st.Kept != 1 {
		t.Errorf("framed draft one line longer was dropped: %v", st.Rejects)
	}
	if st := run(grow, padded); st.Rejects["line cost"] != 1 {
		t.Errorf("padded draft one line longer was kept: %v", st.Rejects)
	}
	if st := run(PlanAll(l, []string{src}, rewritePosting()), framed); st.Rejects["line cost"] != 1 {
		t.Errorf("without FramingGrow the framed draft was kept: %v", st.Rejects)
	}
	// The framing reworded around is no longer a framing added to the line.
	reled := "Designed 10 event-driven microservices in Java and Spring Boot for retail operations, each owning its own database and talking to the others only through a Kafka log"
	if st := run(grow, reled); st.Rejects["edit"] != 1 {
		t.Errorf("a re-led line that gains the framing passed: %v", st.Rejects)
	}
}
