package phase3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/minicheck"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// fakeRewriter answers /api/tags and the rewrite call. drafts maps a source
// line to its draft A and B.
type fakeRewriter struct {
	mu     sync.Mutex
	models []string
	drafts map[string][2]string
	chats  int
}

var sourceRe = regexp.MustCompile(`(?m)^### (\d+)\nSource: (.*)$`)

func (f *fakeRewriter) serve(t *testing.T) *ollama.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			var tags []map[string]string
			for _, m := range f.models {
				tags = append(tags, map[string]string{"name": m})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": tags})
		case "/api/chat":
			var body struct {
				Messages []struct{ Content string }
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.chats++
			f.mu.Unlock()
			var reply []map[string]any
			for _, m := range sourceRe.FindAllStringSubmatch(body.Messages[1].Content, -1) {
				n := 0
				for _, c := range m[1] {
					n = n*10 + int(c-'0')
				}
				d := f.drafts[m[2]]
				reply = append(reply, map[string]any{"n": n, "a": d[0], "b": d[1]})
			}
			out, _ := json.Marshal(map[string]any{"bullets": reply})
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": string(out)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return ollama.New(srv.URL, nil)
}

// fakeChecker is MiniCheck by rule.
type fakeChecker func(doc, claim string) bool

func (f fakeChecker) Check(_ context.Context, doc, claim string) (bool, error) {
	return f(doc, claim), nil
}

func yes(string, string) bool { return true }

const infer = "Used Kubernetes"

// adaptLedger decomposes judgeCV by hand. Acme has five facts, so one approved
// inference stays inside the 20% budget.
func adaptLedger(withInference bool) *ledger.Ledger {
	es := ledger.Parse(judgeCV, "")
	acme, tool := &es[0], &es[1]
	acme.Bullets[0].Facts = []ledger.Fact{
		{ID: "acme-ltd/1a", Text: "Built REST services"},
		{ID: "acme-ltd/1b", Text: "Handling 40k req/s", Figures: []string{"40k"}},
		{ID: "acme-ltd/1c", Text: "On Postgres", Entities: []string{"Postgres"}},
	}
	acme.Bullets[1].Facts = []ledger.Fact{
		{ID: "acme-ltd/2a", Text: "Led a team of four", MustKeep: true},
		{ID: "acme-ltd/2b", Text: "Through a payments migration"},
	}
	tool.Bullets[0].Facts = []ledger.Fact{{ID: "tiny-tool/1a", Text: "Wrote a small tool in Rust", Entities: []string{"Rust"}}}
	if withInference {
		acme.Bullets = append(acme.Bullets, ledger.Bullet{Hash: ledger.InferredHash, Render: new(bool), Facts: []ledger.Fact{
			{ID: "acme-ltd/9a", Text: infer, Entities: []string{"Kubernetes"}, Source: "inferred", Status: "approved", Approved: "1@2026-01-01"},
		}})
	}
	return &ledger.Ledger{
		CVHash: ledger.CVHash(judgeCV, ""), Entries: es,
		Entities: map[string][]string{"PostgreSQL": {"Postgres", "PostgreSQL"}},
	}
}

func adaptRig(t *testing.T, led *ledger.Ledger, f *fakeRewriter, chk fakeChecker) *Tailor {
	t.Helper()
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	l := paths.Layout{Home: t.TempDir(), Code: repo}
	if led != nil {
		if err := ledger.Save(l.CVLedger(), led); err != nil {
			t.Fatal(err)
		}
	}
	return &Tailor{L: l, Cfg: &config.Config{CVRaw: judgeCV}, OL: f.serve(t), Writer: WriterAdapt, Checker: chk}
}

func adaptJob(extra ...phase2.Row) AdaptJob {
	rows := append([]phase2.Row{
		{Requirement: "PostgreSQL experience", MustHave: true, Strength: fit.Strong, Evidence: b1},
		{Requirement: "Payments migration on Kubernetes", MustHave: true, Strength: fit.Strong, Evidence: b2},
	}, extra...)
	return AdaptJob{
		ID: 1, Report: "007", Score: 4.5, JD: "A backend role.", Rows: rows,
		Terms:   []adapt.Term{{Text: "PostgreSQL", Kind: adapt.KindTool}, {Text: "Kubernetes", Kind: adapt.KindTool}},
		Content: VerbatimContent(judgeCV, judgeCV, "", 4, nil),
	}
}

func lines(c fill.Content) []string { return contentBullets(c) }

func models(extra ...string) []string { return append([]string{"snipe-eval"}, extra...) }

func TestAdaptShipsASupportedDraft(t *testing.T) {
	f := &fakeRewriter{models: models(minicheck.Model + ":latest"), drafts: map[string][2]string{
		b1: {"Built REST services handling 40k req/s on PostgreSQL", ""},
	}}
	tl := adaptRig(t, adaptLedger(false), f, yes)
	job := adaptJob()
	var st adapt.Stats
	job.Stats = &st
	got, tf, err := tl.Adapt(context.Background(), nil, job)
	if err != nil || tf == nil {
		t.Fatalf("err %v, truth %v", err, tf)
	}
	if st.Calls == 0 || st.Kept != 1 {
		t.Errorf("AdaptJob.Stats not filled: %+v", st)
	}
	if want := "Built REST services handling 40k req/s on PostgreSQL"; !slices.Contains(lines(got), want) {
		t.Errorf("the verified draft is not in the content: %q", lines(got))
	}
	if slices.Contains(lines(got), b1) || !slices.Contains(lines(got), b2) || !slices.Contains(lines(got), b3) {
		t.Errorf("only the first line should have changed: %q", lines(got))
	}
	if slices.Contains(lines(job.Content), "Built REST services handling 40k req/s on PostgreSQL") {
		t.Error("the input content was mutated")
	}
}

func TestAdaptNeverShipsADraftThatFailsPreservation(t *testing.T) {
	f := &fakeRewriter{models: models(minicheck.Model), drafts: map[string][2]string{
		b2: {"Ran a payments migration with a small team", ""},
	}}
	// Attribution passes; the draft no longer says the team was four.
	chk := func(doc, claim string) bool {
		return claim != "Led a team of four" || strings.Contains(doc, "Led a team of four")
	}
	got, tf, err := adaptRig(t, adaptLedger(false), f, chk).Adapt(context.Background(), nil, adaptJob())
	if err != nil || tf == nil {
		t.Fatalf("err %v, truth %v", err, tf)
	}
	if !slices.Contains(lines(got), b2) || slices.Contains(lines(got), "Ran a payments migration with a small team") {
		t.Errorf("a draft that drops a must-keep fact shipped: %q", lines(got))
	}
}

func TestAdaptFallsBackToVerbatim(t *testing.T) {
	good := map[string][2]string{b1: {"Built REST services handling 40k req/s on PostgreSQL", ""}}
	stale := adaptLedger(false)
	stale.CVHash = "an older cv.md"
	for name, c := range map[string]struct {
		led    *ledger.Ledger
		models []string
	}{
		"no ledger":        {nil, models(minicheck.Model)},
		"stale ledger":     {stale, models(minicheck.Model)},
		"no minicheck":     {adaptLedger(false), models("snipe-embed")},
		"similar-name tag": {adaptLedger(false), models(minicheck.Model + "-x")},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRewriter{models: c.models, drafts: good}
			job := adaptJob()
			got, tf, err := adaptRig(t, c.led, f, yes).Adapt(context.Background(), nil, job)
			if err != nil {
				t.Fatalf("a fallback is not an error: %v", err)
			}
			if tf != nil || !slices.Equal(lines(got), lines(job.Content)) {
				t.Errorf("not verbatim: truth %v, %q", tf, lines(got))
			}
			if f.chats != 0 {
				t.Errorf("%d rewrite call(s) made for an offer that cannot be verified", f.chats)
			}
		})
	}
}

func TestAdaptKillRuleRevertsTheOffendingLine(t *testing.T) {
	draft := "Led a team of four through a Kubernetes payments migration"
	f := &fakeRewriter{models: models(minicheck.Model), drafts: map[string][2]string{b2: {draft, ""}}}
	// The facts alone do not support Kubernetes; the approved inference does.
	chk := func(doc, claim string) bool {
		return !strings.Contains(claim, "Kubernetes") || strings.Contains(doc, infer)
	}
	tl := adaptRig(t, adaptLedger(true), f, chk)

	got, tf, err := tl.Adapt(context.Background(), nil, adaptJob())
	if err != nil || !slices.Contains(lines(got), draft) {
		t.Fatalf("setup: the inferred draft should ship under the budget (%v): %q", err, lines(got))
	}
	if e := tf.Entries[0]; e.Inferred != 1 || e.Supported != 5 {
		t.Fatalf("setup: %+v", e)
	}

	// Now the accounting says the role is over budget: the line goes back.
	tl.check = func(es []adapt.EntryTruth) error {
		if es[0].Inferred > 0 {
			return adapt.Check([]adapt.EntryTruth{{Entry: es[0].Entry, Inferred: 9, Supported: 1}})
		}
		return nil
	}
	got, tf, err = tl.Adapt(context.Background(), nil, adaptJob())
	if err != nil || !slices.Contains(lines(got), b2) || slices.Contains(lines(got), draft) {
		t.Fatalf("the offending line shipped (%v): %q", err, lines(got))
	}
	if e := tf.Entries[0]; e.Inferred != 0 || len(e.InferredUsed) != 0 {
		t.Errorf("truth still counts the reverted claim: %+v", e)
	}
}

func TestAdaptWritesTruthAndQueuesInferencesAndFramings(t *testing.T) {
	f := &fakeRewriter{models: models(minicheck.Model), drafts: map[string][2]string{
		b1: {"Built REST services handling 40k req/s on PostgreSQL", ""},
	}}
	tl := adaptRig(t, adaptLedger(false), f, yes)
	job := adaptJob(phase2.Row{
		Requirement: "Terraform or similar deployment tooling", Strength: fit.Transferable, Evidence: b1,
	})
	job.Terms = append(job.Terms, adapt.Term{Text: "Terraform", Kind: adapt.KindTool}, adapt.Term{Text: "REST API design"})
	job.Rows = append(job.Rows, phase2.Row{Requirement: "Experience with REST API design", Strength: fit.Strong, Evidence: b1})
	job.Queue = true
	ck := state.LoadCheckpoint(t.TempDir(), 1)

	_, tf, err := tl.Adapt(context.Background(), ck, job)
	if err != nil || tf == nil {
		t.Fatalf("err %v, truth %v", err, tf)
	}
	dir := t.TempDir()
	if err := saveTruth(dir, tf); err != nil {
		t.Fatal(err)
	}
	got, err := adapt.ReadTruth(filepath.Join(dir, adapt.TruthName))
	if err != nil {
		t.Fatal(err)
	}
	if got.Offer != "007" || len(got.Entries) != 2 || len(got.Bullets) != 3 {
		t.Fatalf("truth.json: %+v", got)
	}
	if e := got.Entries[0]; e.Entry != "acme-ltd" || e.Supported != 5 || e.Inferred != 0 || e.Unapproved != 0 {
		t.Errorf("acme: %+v", e)
	}
	if b := got.Bullets[0]; b.Source != "A" || b.Kind != "supported" || b.Original != b1 {
		t.Errorf("first bullet: %+v", b)
	}

	// The proposal is in the ledger, pending, where the queue shows it.
	led, _ := ledger.Load(tl.L.CVLedger())
	var pending []ledger.Fact
	for _, b := range led.Entries[0].Bullets {
		for _, fa := range b.Facts {
			if fa.Source == "inferred" {
				pending = append(pending, fa)
			}
		}
	}
	if len(pending) != 1 || pending[0].Status != "" || pending[0].Entities[0] != "Terraform" {
		t.Fatalf("pending inferences: %+v", pending)
	}
	// So is the posting's phrase for what the line already says, undecided.
	if fr := led.Entries[0].Bullets[0].Framings; len(fr) != 1 || fr[0].Phrase != "REST API design" || fr[0].Approved || fr[0].Rejected || len(fr[0].From) == 0 {
		t.Fatalf("pending framings: %+v", fr)
	}

	// A resume: the 30B is not asked again, and nothing is queued twice. The
	// ledger changed on disk (the pending fact) without changing the key.
	calls := f.chats
	if _, _, err := tl.Adapt(context.Background(), ck, job); err != nil {
		t.Fatal(err)
	}
	if f.chats != calls {
		t.Errorf("resume repeated the rewrite: %d -> %d call(s)", calls, f.chats)
	}
	led, _ = ledger.Load(tl.L.CVLedger())
	if n := len(led.Entries[0].Bullets[len(led.Entries[0].Bullets)-1].Facts); n != 1 {
		t.Errorf("holding bullet has %d facts after a resume", n)
	}
	if n := len(led.Entries[0].Bullets[0].Framings); n != 1 {
		t.Errorf("first bullet has %d framings after a resume", n)
	}

	// An adapted run's truth must not outlive a verbatim one.
	if err := saveTruth(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := adapt.ReadTruth(filepath.Join(dir, adapt.TruthName)); err == nil {
		t.Error("truth.json survived a verbatim run")
	}
}

// snipe ledger hashes cv.md as the file holds it, trailing newline included,
// while Config.CVRaw is trimmed. The two must still agree.
func TestAdaptLedgerHashIsOfTheFile(t *testing.T) {
	f := &fakeRewriter{models: models(minicheck.Model), drafts: map[string][2]string{}}
	led := adaptLedger(false)
	led.CVHash = ledger.CVHash(judgeCV+"\n", "")
	tl := adaptRig(t, led, f, yes)
	if err := os.WriteFile(tl.L.CV(), []byte(judgeCV+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, tf, err := tl.Adapt(context.Background(), nil, adaptJob()); err != nil || tf == nil {
		t.Fatalf("a current ledger was refused: err %v, truth %v", err, tf)
	}
}

func TestParseWriter(t *testing.T) {
	for in, want := range map[string]string{"": WriterVerbatim, "verbatim": WriterVerbatim, "adapt": WriterAdapt} {
		if got, err := ParseWriter(in); err != nil || got != want {
			t.Errorf("ParseWriter(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseWriter("rewrite"); err == nil {
		t.Error("an unknown writer was accepted")
	}
}
