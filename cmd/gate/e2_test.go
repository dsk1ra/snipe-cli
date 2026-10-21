package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/minicheck"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

func e2Ledger() *ledger.Ledger {
	return &ledger.Ledger{Entries: []ledger.Entry{
		{ID: "acme", Kind: "experience", Title: "Acme", Bullets: []ledger.Bullet{
			{Hash: "a", Text: "Built a simulated trading engine in Go handling 10k orders per second"},
			{Hash: "b", Text: "Supervised the basic migration of 3 services to Postgres"},
			{Hash: ledger.InferredHash, Text: "Developed a prototype Redis cache for 99% of reads"},
		}},
		{ID: "other", Kind: "project", Title: "Other", Bullets: []ledger.Bullet{
			{Hash: "c", Text: "Cut deploy time by 65% and saved 120 hours per quarter"},
		}},
		{ID: ledger.DigestID, Kind: "digest", Bullets: []ledger.Bullet{
			{Hash: "d", Text: "Wrote a supervised article about Kafka"},
		}},
	}}
}

func e2Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := ledger.Save(paths.Layout{Home: home}.CVLedger(), e2Ledger()); err != nil {
		t.Fatal(err)
	}
	return home
}

// fakeOllama answers every chat with a verdict computed from the prompt: the
// claim is supported iff it is contained in the document, spacing and the final
// full stop aside. That is right for every seeded kind, so a correct pipeline
// scores 1.0 and the test can see the plumbing, not a model.
func fakeOllama(t *testing.T, last *map[string]any) *ollama.Client {
	t.Helper()
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.Join(strings.Fields(s), " "), ". "))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string                           `json:"model"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		var raw map[string]any
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		_ = json.Unmarshal(b.Bytes(), &req)
		_ = json.Unmarshal(b.Bytes(), &raw)
		if last != nil {
			*last = raw
		}
		user := req.Messages[len(req.Messages)-1].Content
		doc, claim, _ := strings.Cut(strings.TrimPrefix(user, "Document: "), "\nClaim: ")
		ok := strings.Contains(norm(doc), norm(claim))
		reply := map[bool]string{true: "Yes", false: "No"}[ok]
		if req.Model == ollama.ModelEval {
			reply = map[bool]string{true: `{"supported":true}`, false: `{"supported":false}`}[ok]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}})
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

func TestE2MinicheckEndToEnd(t *testing.T) {
	home := e2Home(t)
	out := filepath.Join(t.TempDir(), "r.json")
	var buf bytes.Buffer
	o := e2Opts{Checker: "minicheck", Split: "whole", N: 150, Out: out}
	pass, err := runE2(context.Background(), &buf, home, o, &minicheck.Client{OL: fakeOllama(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + buf.String())
	if !pass {
		t.Errorf("fake that is right every time did not pass:\n%s", buf.String())
	}
	for _, want := range []string{"sample:", "added-tool", "recall on corruptions", "precision", "first call", "p95", "wall time", "gate: PASS"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q", want)
		}
	}
	var res e2File
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Cases) == 0 || res.Recall != 1 || res.Precision != 1 || !res.Pass {
		t.Errorf("file: %d cases, recall %v, precision %v", len(res.Cases), res.Recall, res.Precision)
	}
	for _, c := range res.Cases {
		if strings.Contains(c.Source, "Kafka") || strings.Contains(c.Source, "Redis") {
			t.Errorf("digest or inferred bullet was used: %q", c.Source)
		}
		if (c.Kind == verify.KindFaithful) == (c.Verdict == "flagged") {
			t.Errorf("%s judged %s", c.Kind, c.Verdict)
		}
	}
}

func TestE2ClauseSplitRuns(t *testing.T) {
	o := e2Opts{Checker: "minicheck", Split: "clause", N: 150, Out: filepath.Join(t.TempDir(), "r.json")}
	var buf bytes.Buffer
	if _, err := runE2(context.Background(), &buf, e2Home(t), o, &minicheck.Client{OL: fakeOllama(t, nil)}); err != nil {
		t.Fatal(err)
	}
}

func TestEvalCheckerRequest(t *testing.T) {
	var req map[string]any
	ok, err := evalChecker{OL: fakeOllama(t, &req)}.Check(context.Background(), "built a thing", "built a thing")
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	if req["model"] != ollama.ModelEval {
		t.Errorf("model = %v", req["model"])
	}
	if req["think"] != false {
		t.Errorf("think = %v, want false", req["think"])
	}
	if opts := req["options"].(map[string]any); opts["temperature"] != float64(0) {
		t.Errorf("temperature = %v", opts["temperature"])
	}
	format, _ := json.Marshal(req["format"])
	if !strings.Contains(string(format), `"supported":{"type":"boolean"}`) || !strings.Contains(string(format), `"required":["supported"]`) {
		t.Errorf("format = %s", format)
	}
	msgs := req["messages"].([]any)
	if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "false if any detail") {
		t.Errorf("system = %q", sys)
	}
	ok, err = evalChecker{OL: fakeOllama(t, nil)}.Check(context.Background(), "built a thing", "led a thing")
	if err != nil || ok {
		t.Errorf("unsupported claim: got %v, %v", ok, err)
	}
}

func manyCases() []verify.Case {
	var cs []verify.Case
	for i := 0; i < 60; i++ {
		for _, k := range e2Kinds {
			if k == verify.KindQualifier && i%3 != 0 {
				continue // a scarce kind
			}
			cs = append(cs, verify.Case{Source: string(rune('a' + i%26)), Draft: k + string(rune('0'+i%10)), Kind: k, WantSupported: k == verify.KindFaithful})
		}
	}
	return cs
}

func TestSampleDeterministicAndBalanced(t *testing.T) {
	cs := manyCases()
	a, b := sampleCases(cs, 100), sampleCases(cs, 100)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same input gave a different sample")
	}
	if len(a) != 100 {
		t.Fatalf("got %d cases", len(a))
	}
	n := map[string]int{}
	for _, c := range a {
		n[c.Kind]++
	}
	if n[verify.KindFaithful] > 15 {
		t.Errorf("faithful = %d, cap is 15", n[verify.KindFaithful])
	}
	for _, k := range e2Kinds[:4] {
		if n[k] < 20 {
			t.Errorf("%s starved: %v", k, n)
		}
	}
	if n[verify.KindQualifier] != 20 { // all it has
		t.Errorf("qualifier = %d, want all 20", n[verify.KindQualifier])
	}
	if got := sampleCases(cs[:7], 100); len(got) > 7 {
		t.Errorf("sampled %d of 7", len(got))
	}
}

func TestE2Verdict(t *testing.T) {
	for _, c := range []struct {
		r, p float64
		pass bool
		want string
	}{
		{0.96, 0.90, true, "gate: PASS"},
		{0.95, 0.80, true, "gate: PASS"},
		{0.94, 0.90, false, "gate: FAIL  recall 0.940"},
		{0.99, 0.70, false, "precision 0.700"},
	} {
		line, pass := e2Verdict(c.r, c.p)
		if pass != c.pass || !strings.Contains(line, c.want) {
			t.Errorf("%v/%v: %v %q", c.r, c.p, pass, line)
		}
	}
}
