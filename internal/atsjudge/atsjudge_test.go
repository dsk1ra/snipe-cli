package atsjudge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

const page = `                  Ada Lovelace
          ada@example.com   Edinburgh

Work Experience
 Acme                                  2022 - 2024
    • Wrote Kafka consumers in Go and cut p99
      latency of the order saga by 40%
`

type reply struct{ Verdict, Quote string }

// fake answers by the qualification in the user message and counts requests.
func fake(t *testing.T, replies map[string]reply, calls *int, bodies *[]map[string]any) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []ollama.Message `json:"messages"`
			Options  map[string]any   `json:"options"`
			Think    *bool            `json:"think"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		*calls++
		if bodies != nil {
			*bodies = append(*bodies, map[string]any{"opts": req.Options, "think": req.Think, "msgs": req.Messages})
		}
		user := req.Messages[len(req.Messages)-1].Content
		rep := reply{"unmet", ""}
		for q, v := range replies {
			if strings.Contains(user, "Qualification:\n"+q+"\n") {
				rep = v
			}
		}
		out := "VERDICT: " + rep.Verdict + "\nQUOTE: " + rep.Quote
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": out}})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

func newJudge(t *testing.T, ol *ollama.Client, o Options) *Judge {
	t.Helper()
	j, err := New(ol, t.TempDir(), o)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestMetWithRealQuoteStaysMet(t *testing.T) {
	calls := 0
	var bodies []map[string]any
	j := newJudge(t, fake(t, map[string]reply{
		"Kafka experience": {"met", "WROTE kafka   consumers in Go"}, // case and spacing differ from the page
	}, &calls, &bodies), Options{})
	r, err := j.Score(context.Background(), []Qual{{Text: "Kafka experience", Must: true, Supported: true}}, page)
	if err != nil {
		t.Fatal(err)
	}
	if a := r.Answers[0]; a.Verdict != Met || a.PointerFailed || r.PointerFailed != 0 {
		t.Fatalf("answer = %+v", a)
	}
	if !r.HasMust || r.MustEvidence != 1 || r.Grade != "A" {
		t.Errorf("result = %+v", r)
	}
	// The request is pinned: temperature 0, a fixed context, no think flag for a model that may reject it.
	b := bodies[0]
	if o := b["opts"].(map[string]any); o["temperature"] != 0.0 || o["num_ctx"] != float64(numCtx) {
		t.Errorf("options = %v", o)
	}
	if b["think"] != (*bool)(nil) {
		t.Errorf("think sent: %v", b["think"])
	}
	msgs := b["msgs"].([]ollama.Message)
	if msgs[0].Role != "system" || !strings.Contains(msgs[1].Content, "Qualification:\nKafka experience") || !strings.Contains(msgs[1].Content, "Wrote Kafka consumers") {
		t.Errorf("messages = %+v", msgs)
	}
	if strings.Contains(msgs[1].Content, "   ") {
		t.Errorf("layout padding reached the model: %q", msgs[1].Content)
	}
}

func TestNoThinkIsSentWhenAsked(t *testing.T) {
	calls := 0
	var bodies []map[string]any
	j := newJudge(t, fake(t, nil, &calls, &bodies), Options{Model: "qwen3:8b", AllowSameFamily: true, NoThink: true})
	if _, err := j.Score(context.Background(), []Qual{{Text: "anything at all"}}, page); err != nil {
		t.Fatal(err)
	}
	if p, _ := bodies[0]["think"].(*bool); p == nil || *p {
		t.Errorf("think = %v", bodies[0]["think"])
	}
}

func TestInventedQuoteIsDowngradedAndCounted(t *testing.T) {
	calls := 0
	j := newJudge(t, fake(t, map[string]reply{
		"Terraform":     {"met", "Provisioned AWS with Terraform"}, // not in the CV
		"Go":            {"partly", "Go"},                          // in the CV but under 4 characters
		"Saga latency":  {"partly", "cut p99 latency of the order saga by 40%"},
		"Anything else": {"met", ""},
	}, &calls, nil), Options{})
	qs := []Qual{{Text: "Terraform", Must: true, Supported: true}, {Text: "Go", Must: true}, {Text: "Saga latency"}, {Text: "Anything else"}}
	r, err := j.Score(context.Background(), qs, page)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range r.Answers {
		got = append(got, a.Verdict)
	}
	if want := "unmet unmet partly unmet"; strings.Join(got, " ") != want {
		t.Errorf("verdicts = %v, want %s", got, want)
	}
	if r.PointerFailed != 3 || !r.Answers[0].PointerFailed || r.Answers[2].PointerFailed {
		t.Errorf("pointer_failed = %d, answers %+v", r.PointerFailed, r.Answers)
	}
	if r.MustEvidence != 0 || !r.HasMust {
		t.Errorf("an invented quote counted as evidence: %+v", r)
	}
}

func TestGrade(t *testing.T) {
	m, p, u := Met, Partly, Unmet
	for _, c := range []struct {
		name       string
		must, pref []string
		want       string
	}{
		{"all met, no preferred", []string{m, m}, nil, "A"},
		{"all met, half the preferred", []string{m}, []string{m, u}, "A"},
		{"all met, two partly make half", []string{m}, []string{p, p}, "A"},
		{"all met, under half the preferred", []string{m, m}, []string{m, u, u}, "B"},
		{"all met, none of the preferred", []string{m}, []string{u}, "B"},
		{"a partly must-have is not met", []string{m, p}, nil, "C"},
		{"half the must-haves", []string{m, u}, []string{m, m}, "C"},
		{"four partlys make half", []string{p, p, p, p}, nil, "C"},
		{"a quarter is not half", []string{p, p, u, u}, nil, "D"},
		{"under half", []string{m, u, u}, []string{m}, "D"},
		{"none", []string{u}, nil, "D"},
		{"no must-have is vacuously met", nil, nil, "A"},
		{"no must-have, weak preferred", nil, []string{u}, "B"},
	} {
		if got := Grade(c.must, c.pref); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLedgerSupportedDenominator(t *testing.T) {
	rows := []phase2.Row{
		{Requirement: "Kafka consumers", MustHave: true, Strength: fit.Strong},
		{Requirement: "Terraform in production", MustHave: true, Strength: fit.Gap},
		{Requirement: "Event-driven design", MustHave: true, Strength: fit.Transferable},
		{Requirement: "Rust", MustHave: false, Strength: fit.Gap},
		{Requirement: "Go services", MustHave: false, Strength: fit.Strong},
		{Requirement: "No strength graded", MustHave: true},
	}
	calls := 0
	j := newJudge(t, fake(t, map[string]reply{
		"Kafka consumers":         {"met", "Wrote Kafka consumers"},
		"Terraform in production": {"met", "Wrote Kafka consumers"}, // met, but the ledger does not support it
		"Event-driven design":     {"partly", "order saga"},
		"Go services":             {"met", "consumers in Go"},
	}, &calls, nil), Options{})
	r, err := j.Score(context.Background(), QualsFromRows(rows), page)
	if err != nil {
		t.Fatal(err)
	}
	// Two supported must-haves (Strong, Transferable), one judged met; the Gap
	// the judge called met is outside both numerator and denominator.
	if !r.HasMust || r.MustEvidence != 0.5 {
		t.Errorf("must_have_evidence = %v (has %v), want 0.5", r.MustEvidence, r.HasMust)
	}
	if !r.HasPref || r.PrefEvidence != 1 {
		t.Errorf("preferred_evidence = %v (has %v), want 1", r.PrefEvidence, r.HasPref)
	}
	// Grade sees every qualification: a partly and an ungradable must-have are not met.
	if r.Grade != "C" {
		t.Errorf("grade %s", r.Grade)
	}

	// No supported must-have: the metric is undefined, not zero.
	r, _ = j.Score(context.Background(), QualsFromRows(rows[1:2]), page)
	if r.HasMust || r.HasPref {
		t.Errorf("undefined metric reported: %+v", r)
	}
}

func TestFamilyRefusal(t *testing.T) {
	for _, m := range []string{"qwen3:8b", "Qwen2.5:7b", "snipe-eval", "snipe-cv"} {
		_, err := New(nil, t.TempDir(), Options{Model: m})
		if err == nil || !strings.Contains(err.Error(), "writer's family") || !strings.Contains(err.Error(), "--allow-same-family") {
			t.Errorf("%s: err = %v", m, err)
		}
		if _, err := New(nil, t.TempDir(), Options{Model: m, AllowSameFamily: true}); err != nil {
			t.Errorf("%s with the override: %v", m, err)
		}
	}
	for _, m := range []string{"", "gemma3:4b-it-q8_0", "granite3.3:8b"} {
		if j, err := New(nil, t.TempDir(), Options{Model: m}); err != nil || j.Model == "" {
			t.Errorf("%q refused: %v", m, err)
		}
	}
}

func TestCacheHitMakesNoSecondCall(t *testing.T) {
	calls := 0
	ol := fake(t, map[string]reply{"Kafka experience": {"met", "Wrote Kafka consumers"}}, &calls, nil)
	dir := t.TempDir()
	qs := []Qual{{Text: "Kafka experience", Must: true, Supported: true}, {Text: "Rust"}}
	j, _ := New(ol, dir, Options{})
	first, err := j.Score(context.Background(), qs, page)
	if err != nil || calls != 2 || j.Calls != 2 {
		t.Fatalf("calls %d, %v", calls, err)
	}

	// A new Judge on the same directory: same answers, no call, even through a
	// different amount of padding in the page.
	j2, _ := New(ol, dir, Options{})
	again, err := j2.Score(context.Background(), qs, strings.ReplaceAll(page, "    •", "          •"))
	if err != nil || calls != 2 || j2.Calls != 0 {
		t.Fatalf("second run called the model: calls %d, %v", calls, err)
	}
	if again.Grade != first.Grade || again.MustEvidence != first.MustEvidence {
		t.Errorf("cached result differs: %+v vs %+v", again, first)
	}

	// A changed CV, or a changed qualification, is a new question.
	if _, err := j2.Score(context.Background(), qs, page+"\nSkills: Rust\n"); err != nil || calls != 4 {
		t.Errorf("changed CV: calls %d, %v", calls, err)
	}
	// The cache is per model, and a judge with no model answers only from it.
	ro, _ := New(nil, dir, Options{})
	if _, err := ro.Score(context.Background(), qs, page); err != nil {
		t.Errorf("read-only judge missed a cached answer: %v", err)
	}
	if _, err := ro.Score(context.Background(), []Qual{{Text: "never asked"}}, page); !errors.Is(err, ErrNotCached) {
		t.Errorf("err = %v, want ErrNotCached", err)
	}
	if CachePath(dir, "gemma3:4b-it-q8_0") != dir+"/atsjudge-gemma3_4b-it-q8_0.json" {
		t.Errorf("cache path = %s", CachePath(dir, "gemma3:4b-it-q8_0"))
	}
}

func TestBlankKeepsOnlyTheHeader(t *testing.T) {
	long := "Ada Lovelace\nada@example.com\n\nSkills\nGo\nRust\nWork Experience\nAcme\n• Wrote Kafka consumers\n"
	b := Blank(long)
	if n := len(strings.Split(b, "\n")); n != 3 || strings.Contains(b, "Kafka") || strings.Contains(b, "Go") {
		t.Errorf("blank = %q", b)
	}
	if Blank("one line") != "one line" {
		t.Error("a short page must pass through")
	}
}

// A judge that says met to everything still cannot pass the blank CV unless it
// can quote it; one that quotes the header scores the floor it deserves.
func TestBlankControlScoring(t *testing.T) {
	calls := 0
	j := newJudge(t, fake(t, map[string]reply{
		"Kafka consumers": {"met", "Wrote Kafka consumers"}, // would be a real quote on the full page
		"Edinburgh":       {"met", "ada@example.com Edinburgh"},
	}, &calls, nil), Options{})
	qs := []Qual{{Text: "Kafka consumers", Must: true, Supported: true}, {Text: "Edinburgh", Must: true, Supported: true}}
	long := "Ada Lovelace\nada@example.com Edinburgh\nSoftware Engineer\nSkills\nGo\nWork Experience\n" + page
	r, err := j.Score(context.Background(), qs, Blank(long))
	if err != nil {
		t.Fatal(err)
	}
	// The Kafka answer quotes a bullet the blank CV no longer has.
	if r.MustEvidence != 0.5 || r.PointerFailed != 1 {
		t.Errorf("blank control: must %v, pointer_failed %d", r.MustEvidence, r.PointerFailed)
	}
	full, _ := j.Score(context.Background(), qs, long)
	if full.MustEvidence != 1 {
		t.Errorf("full page must %v", full.MustEvidence)
	}
}

// gemma3 wraps the passage in typographic quotes and sometimes bolds the label.
func TestParseReplyIsLenientAboutQuoting(t *testing.T) {
	for _, in := range []string{
		"VERDICT: met\nQUOTE: Wrote Kafka consumers",
		"**VERDICT:** met\nQUOTE: “Wrote Kafka consumers”",
		"verdict: Met\nquote: 'Wrote Kafka consumers'\n\nExtra commentary.",
	} {
		v, q, ok := parseReply(in)
		if !ok || v != "met" || q != "Wrote Kafka consumers" {
			t.Errorf("%q -> %q %q %v", in, v, q, ok)
		}
	}
	if _, _, ok := parseReply("I think the CV is fine."); ok {
		t.Error("a reply without a verdict parsed")
	}
}

func TestQuoteFoundAcceptsElisionNotInvention(t *testing.T) {
	cv := norm("Testing & Ways of Working: TDD, Agile / Scrum, unit testing\nEducation BSc Computer Science Napier University 2025 First Class\nWrote Kafka consumers for the billing service")
	for _, c := range []struct {
		quote string
		want  bool
	}{
		{"Wrote Kafka consumers", true},                                  // verbatim
		{"Testing & Ways of Working: Agile / Scrum, unit testing", true}, // one list item dropped
		{"Wrote Kafka billing service", true},                            // two words dropped
		{"Wrote Kafka consumers for the Azure billing service", false},   // a word the CV does not have
		{"unit testing Wrote Kafka", false},                              // right words, wrong order
		{"Testing Wrote consumers billing", false},                       // a stitch across far-apart lines
	} {
		if got := quoteFound(cv, norm(c.quote)); got != c.want {
			t.Errorf("%q = %v, want %v", c.quote, got, c.want)
		}
	}
}
