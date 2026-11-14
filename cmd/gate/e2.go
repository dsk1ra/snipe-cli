package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/minicheck"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// gate e2 is experiment E2 of docs/PRD-TAILORING-REWORK.md (§8.2): can a model
// checker tell a seeded corruption of a CV bullet from the bullet itself? It
// runs ONE checker per invocation. MiniCheck (5 GB) and the 30B (18 GB) cannot
// share the 6 GB card, and loading the second would evict the first mid-run,
// which would put a reload into the latency numbers.

const (
	e2MinRecall    = 0.95
	e2MinPrecision = 0.80
	e2FaithfulCap  = 0.15 // share of the sample the faithful variants may take
)

// e2Kinds is the round-robin order. Corruptions first so a tiny --n keeps them.
var e2Kinds = []string{verify.KindTool, verify.KindQualifier, verify.KindScope, verify.KindFigure, verify.KindFaithful}

// evalChecker asks the 30B whether a document supports a claim. The schema
// leaves it nothing to say but a boolean, and thinking is off so it cannot
// spend the output cap before answering.
type evalChecker struct{ OL *ollama.Client }

const evalSystem = "You check claims against a document. Answer whether the document fully supports the claim. " +
	"Answer false if any detail of the claim (a tool, a scope word such as led or architected, a qualifier such as simulated or supervised, or a figure) is not in the document."

func (c evalChecker) Check(ctx context.Context, doc, claim string) (bool, error) {
	var out struct {
		Supported bool `json:"supported"`
	}
	err := c.OL.Chat(ctx, ollama.Request{
		Model:   ollama.ModelEval,
		System:  evalSystem,
		User:    "Document: " + doc + "\nClaim: " + claim,
		Schema:  ollama.Object(ollama.Obj(ollama.F("supported", ollama.Obj(ollama.F("type", "boolean"))))),
		Options: ollama.Options{Temperature: 0, NumCtx: 4096, NumPredict: 32},
		NoThink: true,
		Timeout: 120 * time.Second,
		Label:   "e2.eval",
	}, &out)
	return out.Supported, err
}

// sampleCases takes at most n cases, round-robin over kinds so none starves,
// faithful variants capped at e2FaithfulCap of n. Within a kind the picks are
// evenly spaced over the ledger, and the result keeps ledger order: the same
// input always gives the same sample, with no random source at all.
func sampleCases(cases []verify.Case, n int) []verify.Case {
	byKind := map[string][]int{}
	for i, c := range cases {
		byKind[c.Kind] = append(byKind[c.Kind], i)
	}
	faithCap := max(1, int(float64(n)*e2FaithfulCap))
	quota := map[string]int{}
	for total, progress := 0, true; total < n && progress; {
		progress = false
		for _, k := range e2Kinds {
			if total >= n {
				break
			}
			if quota[k] >= len(byKind[k]) || (k == verify.KindFaithful && quota[k] >= faithCap) {
				continue
			}
			quota[k]++
			total++
			progress = true
		}
	}
	var keep []int
	for k, q := range quota {
		idx := byKind[k]
		for j := 0; j < q; j++ {
			keep = append(keep, idx[j*len(idx)/q])
		}
	}
	sort.Ints(keep)
	out := make([]verify.Case, len(keep))
	for i, ix := range keep {
		out[i] = cases[ix]
	}
	return out
}

// e2Cases is every seeded case of every real bullet. The digest entry and the
// per-entry holders of approved inferences are not CV bullets.
func e2Cases(led *ledger.Ledger) []verify.Case {
	var out []verify.Case
	for _, e := range led.Entries {
		if e.ID == ledger.DigestID {
			continue
		}
		for _, b := range e.Bullets {
			if b.Hash == ledger.InferredHash || strings.TrimSpace(b.Text) == "" {
				continue
			}
			out = append(out, verify.Cases(led, e, b)...)
		}
	}
	return out
}

// timed records every Check: how long it took and what it said. The first call
// is kept apart because it pays for loading the model; folding it into the
// mean would make a 5 GB load look like a slow check.
type timed struct {
	inner verify.Checker
	calls []timedCall
}

type timedCall struct {
	doc, claim string
	ok         bool
	dur        time.Duration
}

func (t *timed) Check(ctx context.Context, doc, claim string) (bool, error) {
	start := time.Now()
	ok, err := t.inner.Check(ctx, doc, claim)
	t.calls = append(t.calls, timedCall{doc, claim, ok, time.Since(start)})
	return ok, err
}

// replay answers from what timed already recorded, so the whole-set Calibrate
// can produce the Report without a second round of model calls.
type replay map[[2]string]bool

func (r replay) Check(_ context.Context, doc, claim string) (bool, error) {
	return r[[2]string{doc, claim}], nil
}

type e2Opts struct {
	Checker string // minicheck | eval
	Split   string // whole | clause
	N       int
	Out     string
}

type e2Row struct {
	Source       string  `json:"source"`
	Draft        string  `json:"draft"`
	Kind         string  `json:"kind"`
	WantFlagged  bool    `json:"want_flagged"`
	Verdict      string  `json:"verdict"` // flagged | supported
	Attribution  bool    `json:"attribution"`
	Preservation bool    `json:"preservation"`
	Seconds      float64 `json:"seconds"`
}

type e2File struct {
	Checker     string             `json:"checker"`
	Split       string             `json:"split"`
	Recall      float64            `json:"recall"`
	Precision   float64            `json:"precision"`
	ByKind      map[string]float64 `json:"recall_by_kind"`
	FalseAlarm  int                `json:"false_alarms"`
	Faithful    int                `json:"faithful"`
	FirstCall   float64            `json:"first_call_seconds"`
	MeanCheck   float64            `json:"mean_check_seconds"`
	P95Check    float64            `json:"p95_check_seconds"`
	WallSeconds float64            `json:"wall_seconds"`
	Pass        bool               `json:"pass"`
	Cases       []e2Row            `json:"cases"`
}

// e2Verdict is the §8.2 gate for E2.
func e2Verdict(recall, precision float64) (string, bool) {
	if recall >= e2MinRecall && precision >= e2MinPrecision {
		return fmt.Sprintf("gate: PASS  recall %.3f (>= %.2f)  precision %.3f (>= %.2f)", recall, e2MinRecall, precision, e2MinPrecision), true
	}
	return fmt.Sprintf("gate: FAIL  recall %.3f (need >= %.2f)  precision %.3f (need >= %.2f)", recall, e2MinRecall, precision, e2MinPrecision), false
}

func p95(ds []float64) float64 {
	if len(ds) == 0 {
		return 0
	}
	s := append([]float64(nil), ds...)
	sort.Float64s(s)
	return s[(len(s)*95+99)/100-1] // nearest rank
}

// runE2 does the work behind gateE2 with the checker and the output path
// already chosen, so a test can drive it with a fake Ollama.
func runE2(ctx context.Context, w io.Writer, home string, o e2Opts, c verify.Checker) (bool, error) {
	led, err := ledger.Load(paths.Layout{Home: home}.CVLedger())
	if err != nil {
		return false, err
	}
	all := e2Cases(led)
	cases := sampleCases(all, o.N)
	if len(cases) == 0 {
		return false, fmt.Errorf("no cases: %s has no bullets", paths.Layout{Home: home}.CVLedger())
	}
	counts := map[string]int{}
	for _, cs := range cases {
		counts[cs.Kind]++
	}
	fmt.Fprintf(w, "sample: %d of %d cases (checker %s, split %s)\n", len(cases), len(all), o.Checker, o.Split)
	for _, k := range e2Kinds {
		fmt.Fprintf(w, "  %-18s %d\n", k, counts[k])
	}

	var vo verify.Options
	if o.Split == "clause" {
		vo.Split = verify.ByClause
	}
	t := &timed{inner: c}
	rows := make([]e2Row, len(cases))
	begin := time.Now()
	for i, cs := range cases {
		from := len(t.calls)
		rep, err := verify.Calibrate(ctx, t, cases[i:i+1], vo)
		if err != nil {
			return false, fmt.Errorf("case %d (%s): %w", i, cs.Kind, err)
		}
		out := rep.Outcomes[0]
		var sec time.Duration
		for _, tc := range t.calls[from:] {
			sec += tc.dur
		}
		rows[i] = e2Row{cs.Source, cs.Draft, cs.Kind, !cs.WantSupported, "supported", out.Attribution, out.Preservation, sec.Seconds()}
		if out.Flagged {
			rows[i].Verdict = "flagged"
		}
		if (i+1)%25 == 0 {
			fmt.Fprintf(w, "  %d/%d\n", i+1, len(cases))
		}
	}
	wall := time.Since(begin)

	rec := replay{}
	for _, tc := range t.calls {
		rec[[2]string{tc.doc, tc.claim}] = tc.ok
	}
	rep, err := verify.Calibrate(ctx, rec, cases, vo)
	if err != nil {
		return false, err
	}

	var secs []float64
	var first float64
	for i, tc := range t.calls {
		if i == 0 {
			first = tc.dur.Seconds() // pays for the model load
			continue
		}
		secs = append(secs, tc.dur.Seconds())
	}
	var mean float64
	for _, s := range secs {
		mean += s
	}
	if len(secs) > 0 {
		mean /= float64(len(secs))
	}

	fmt.Fprintln(w, "\nrecall on corruptions")
	for _, k := range e2Kinds[:4] {
		if counts[k] > 0 {
			fmt.Fprintf(w, "  %-18s %.3f  (%d)\n", k, rep.ByKind[k], counts[k])
		}
	}
	fmt.Fprintf(w, "  %-18s %.3f\n", "overall", rep.Recall)
	fmt.Fprintf(w, "precision          %.3f\n", rep.Precision)
	fmt.Fprintf(w, "false alarms       %d of %d faithful (%.3f)\n", rep.FalseAlarm, counts[verify.KindFaithful], ratio(rep.FalseAlarm, counts[verify.KindFaithful]))
	fmt.Fprintf(w, "first call         %.2fs (model load, not in the figures below)\n", first)
	fmt.Fprintf(w, "per check          mean %.3fs  p95 %.3fs  (%d checks)\n", mean, p95(secs), len(secs))
	fmt.Fprintf(w, "wall time          %.1fs\n", wall.Seconds())
	line, pass := e2Verdict(rep.Recall, rep.Precision)
	fmt.Fprintln(w, line)

	res := e2File{o.Checker, o.Split, rep.Recall, rep.Precision, rep.ByKind, rep.FalseAlarm, counts[verify.KindFaithful],
		first, mean, p95(secs), wall.Seconds(), pass, rows}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return pass, err
	}
	if err := os.MkdirAll(filepath.Dir(o.Out), 0o755); err != nil {
		return pass, err
	}
	if err := os.WriteFile(o.Out, b, 0o644); err != nil {
		return pass, err
	}
	fmt.Fprintln(w, "outcomes:", o.Out)
	return pass, nil
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func gateE2(args []string) int {
	const usage = "usage: gate e2 <home> [--checker minicheck|eval] [--split whole|clause] [--n 150] [--out report.json]"
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	home := args[0]
	fs := flag.NewFlagSet("e2", flag.ContinueOnError)
	o := e2Opts{}
	fs.StringVar(&o.Checker, "checker", "minicheck", "minicheck | eval")
	fs.StringVar(&o.Split, "split", "whole", "whole | clause")
	fs.IntVar(&o.N, "n", 150, "most cases to run")
	fs.StringVar(&o.Out, "out", "", "where to write the per-case outcomes")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || o.N < 1 || (o.Split != "whole" && o.Split != "clause") {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	ol := ollama.New("", nil)
	var c verify.Checker
	switch o.Checker {
	case "minicheck":
		c = &minicheck.Client{OL: ol}
	case "eval":
		c = evalChecker{OL: ol}
	default:
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	if o.Out == "" {
		o.Out = filepath.Join(paths.Layout{Home: home}.CacheDir(), fmt.Sprintf("e2-%s-%s.json", o.Checker, o.Split))
	}
	pass, err := runE2(context.Background(), os.Stdout, home, o, c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !pass {
		return 1
	}
	return 0
}
