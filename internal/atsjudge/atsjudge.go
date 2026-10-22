// Package atsjudge is the ATS simulator of docs/PRD-TAILORING-REWORK.md §8.1.
// Workday documents its screen as one question per qualification: does the CV
// show it, and which of them is every basic one. This asks that question once
// per qualification of the posting's evidence table, of a model that is not the
// writer, and checks every answer in code.
//
// The model's verdict is not trusted on its own. It must quote the CV, and a
// "met" or "partly" whose quote is not in the CV is counted as unmet, because a
// small model that wants to agree will agree without evidence (the same trick
// phase1.verifyGaps plays on gap quotes).
package atsjudge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// DefaultModel is the other-family model installed next to the Qwen3 writer.
const DefaultModel = "gemma3:4b-it-q8_0"

// numCtx is fixed so the same prompt is the same request on every run: a one
// page CV is about a thousand tokens, and the cache key is not the context.
const numCtx = 8192

// minQuote is the shortest quote that counts. Below it a "quote" is a word the
// model could not help but copy ("Go", "SQL").
const minQuote = 4

// ErrNotCached is what a Judge without a model answers for a call it has not
// seen. gate tailor reads the cache with such a Judge and never calls a model.
var ErrNotCached = errors.New("atsjudge: answer not in the cache")

const system = `You are an applicant-tracking system screener. You read a CV against one qualification from a job posting and decide whether the CV shows that the candidate meets it.

Answer "met" when the CV states experience, skills or credentials that satisfy the qualification as written. Answer "partly" when the CV shows some of what is asked or something close to it, but not all of it. Answer "unmet" when the CV shows nothing relevant. Use only what is written in the CV; do not assume anything it does not say.

Reply with exactly two lines and nothing else:
VERDICT: met, partly or unmet
QUOTE: the shortest passage of the CV, copied word for word, that is your evidence (empty for unmet)`

// Qual is one qualification of the posting.
type Qual struct {
	Text string
	Must bool
	// Supported is whether the ledger backs the requirement: stage 2 graded it
	// Strong or Transferable. Only supported qualifications enter the evidence
	// metrics, since an honest CV cannot show a Gap and must not be scored down
	// for it.
	Supported bool

	// Strength and Evidence are stage 2's grade and the CV line it matched,
	// kept so a miss can be explained: was the evidence selected onto the page?
	Strength, Evidence string
}

// QualsFromRows reads the qualifications off stage 2's rows, so there is one
// parser of the evidence table (adapt.RowsFromReport).
func QualsFromRows(rows []phase2.Row) []Qual {
	out := make([]Qual, len(rows))
	for i, r := range rows {
		out[i] = Qual{Text: r.Requirement, Must: r.MustHave,
			Supported: r.Strength == fit.Strong || r.Strength == fit.Transferable,
			Strength:  r.Strength, Evidence: r.Evidence}
	}
	return out
}

// Verdicts.
const (
	Met    = "met"
	Partly = "partly"
	Unmet  = "unmet"
)

// Answer is the judge's verdict on one qualification after the code check.
type Answer struct {
	Qual          Qual
	Verdict       string
	Quote         string
	PointerFailed bool // the model said met or partly and its quote was not in the CV
	Errored       bool // the model gave no usable reply, counted as unmet
}

// Result is one CV scored against one posting.
type Result struct {
	Answers       []Answer
	Grade         string // A to D
	PointerFailed int
	Errored       int // answers the model never produced; they score as unmet

	// MustEvidence is the must-haves judged met over the must-haves the ledger
	// supports, and PrefEvidence the same for the preferred ones. Both count a
	// supported qualification only; "partly" is not "met" here. Has* is false
	// when the denominator is 0, and the metric is then left out, not zero.
	MustEvidence, PrefEvidence float64
	HasMust, HasPref           bool
}

// Options configure a Judge.
type Options struct {
	Model string // DefaultModel when empty
	// AllowSameFamily lets the judge be a Qwen model or one of the snipe-*
	// Modelfiles, which are all Qwen. The writer is Qwen3, and a model grades
	// its own family's prose kindly.
	AllowSameFamily bool
	// NoThink sends think:false. A model that does not think may reject it, so
	// it is off unless the caller knows the model reasons.
	NoThink bool
}

// Judge is the simulator. OL may be nil, and then it answers only from the cache.
type Judge struct {
	OL    *ollama.Client
	Model string
	Calls int // model calls made, for the cache-hit tests and the run log

	opt   Options
	path  string
	cache map[string]cached
}

type cached struct {
	Verdict       string `json:"verdict"`
	Quote         string `json:"quote,omitempty"`
	PointerFailed bool   `json:"pointer_failed,omitempty"`
	Errored       bool   `json:"-"` // no usable reply; scored unmet and never cached
}

type cacheFile struct {
	Model   string            `json:"model"`
	Updated string            `json:"updated"`
	Answers map[string]cached `json:"answers"`
}

// CachePath is derived data, so it lives with the other caches.
func CachePath(cacheDir, model string) string {
	return filepath.Join(cacheDir, "atsjudge-"+strings.NewReplacer(":", "_", "/", "_").Replace(model)+".json")
}

// New refuses a model of the writer's family and loads the cache.
func New(ol *ollama.Client, cacheDir string, o Options) (*Judge, error) {
	if o.Model == "" {
		o.Model = DefaultModel
	}
	m := strings.ToLower(o.Model)
	if (strings.HasPrefix(m, "qwen") || strings.HasPrefix(m, "snipe-")) && !o.AllowSameFamily {
		return nil, fmt.Errorf("atsjudge: %s is in the writer's family (Qwen3); a judge grading its own family's prose is the self-preference PRD §10 rules out. Pick another family, or pass --allow-same-family to override", o.Model)
	}
	j := &Judge{OL: ol, Model: o.Model, opt: o, path: CachePath(cacheDir, o.Model), cache: map[string]cached{}}
	if b, err := os.ReadFile(j.path); err == nil {
		var f cacheFile
		if json.Unmarshal(b, &f) == nil && f.Model == o.Model && f.Answers != nil { // a torn file is a cold cache
			j.cache = f.Answers
		}
	}
	return j, nil
}

func (j *Judge) save() error {
	b, err := json.MarshalIndent(cacheFile{Model: j.Model, Updated: time.Now().Format(time.RFC3339), Answers: j.cache}, "", " ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(j.path, append(b, '\n'))
}

func sum(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// Render is the CV text the judge reads: each line trimmed and its runs of
// spaces collapsed, blank lines dropped. pdftotext -layout pads with dozens of
// spaces to place columns, which costs context and means nothing to a reader.
func Render(page string) string {
	var out []string
	for _, l := range strings.Split(page, "\n") {
		if f := strings.Join(strings.Fields(l), " "); f != "" {
			out = append(out, f)
		}
	}
	return strings.Join(out, "\n")
}

// Blank is the control CV: the first three lines of the page, the name, the
// title and the contact line. Six lines also took in the first two lines of the
// skills block, and a judge that credits "Languages: Go, Java, Rust" against a
// Go requirement is right, which put the floor at 0.31 for the wrong reason. A
// judge that says "met" to this is saying it to anything.
func Blank(page string) string {
	lines := strings.Split(Render(page), "\n")
	return strings.Join(lines[:min(len(lines), 3)], "\n")
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ask returns the checked answer for one qualification, from the cache or one
// model call. cvNorm is norm(cv).
func (j *Judge) ask(ctx context.Context, q, cv, cvNorm, cvHash string) (cached, error) {
	key := sum(system, q, cvHash)
	if c, ok := j.cache[key]; ok {
		return c, nil
	}
	if j.OL == nil {
		return cached{}, ErrNotCached
	}
	var raw struct {
		Verdict string `json:"verdict"`
		Quote   string `json:"quote"`
	}
	// Free text, not a schema. Ollama's grammar for a JSON string only closes on
	// an ASCII quote, and gemma3 writes typographic ones, so a schema-constrained
	// reply never ended: 200 tokens of ”} ”} ”} on every call, and 139 answers
	// in an hour and three quarters. Two labelled lines parse without that.
	text, err := j.OL.Text(ctx, ollama.Request{
		Model: j.Model, System: system,
		User: "Qualification:\n" + q + "\n\nCV:\n" + cv,
		// A verdict and one quote; the cap is for a model that does not stop.
		Options: ollama.Options{Temperature: 0, NumCtx: numCtx, NumPredict: 160},
		NoThink: j.opt.NoThink, Timeout: 3 * time.Minute, Label: "ats.judge",
	})
	if err != nil {
		if ctx.Err() != nil {
			return cached{}, err
		}
		return cached{Verdict: Unmet, Errored: true}, nil
	}
	v, qt, ok := parseReply(text)
	if !ok {
		return cached{Verdict: Unmet, Errored: true}, nil
	}
	raw.Verdict, raw.Quote = v, qt
	j.Calls++
	c := cached{Verdict: raw.Verdict, Quote: strings.TrimSpace(raw.Quote)}
	switch c.Verdict {
	case Met, Partly:
		if nq := norm(c.Quote); utf8.RuneCountInString(nq) < minQuote || !strings.Contains(cvNorm, nq) {
			c.Verdict, c.PointerFailed = Unmet, true
		}
	case Unmet:
	default: // the schema forbids it; a fake or a changed server might not
		c.Verdict, c.Quote = Unmet, ""
	}
	j.cache[key] = c
	return c, j.save()
}

var (
	verdictRe = regexp.MustCompile(`(?im)^\s*\**\s*VERDICT\s*:?\s*\**\s*(met|partly|unmet)\b`)
	quoteRe   = regexp.MustCompile(`(?im)^\s*\**\s*QUOTE\s*:?\s*\**\s*(.*)$`)
)

// parseReply reads the two labelled lines. Quote marks the model wraps the
// passage in are stripped; whether the passage is really in the CV is the
// pointer check's job, not this one's.
func parseReply(text string) (verdict, quote string, ok bool) {
	m := verdictRe.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	verdict = strings.ToLower(m[1])
	if q := quoteRe.FindStringSubmatch(text); q != nil {
		quote = strings.Trim(strings.TrimSpace(q[1]), "\"“”'`*")
	}
	return verdict, quote, true
}

// Score judges every qualification against the CV text (a page, as Render takes it).
func (j *Judge) Score(ctx context.Context, quals []Qual, page string) (Result, error) {
	cv := Render(page)
	cvNorm, cvHash := norm(cv), sum(cv)
	var r Result
	for _, q := range quals {
		c, err := j.ask(ctx, q.Text, cv, cvNorm, cvHash)
		if err != nil {
			return Result{}, err
		}
		r.Answers = append(r.Answers, Answer{Qual: q, Verdict: c.Verdict, Quote: c.Quote, PointerFailed: c.PointerFailed, Errored: c.Errored})
		if c.PointerFailed {
			r.PointerFailed++
		}
		if c.Errored {
			r.Errored++
		}
	}
	r.summarise()
	return r, nil
}

func (r *Result) summarise() {
	var must, pref []string
	var mustSup, mustMet, prefSup, prefMet int
	for _, a := range r.Answers {
		if a.Qual.Must {
			must = append(must, a.Verdict)
		} else {
			pref = append(pref, a.Verdict)
		}
		if !a.Qual.Supported {
			continue
		}
		switch {
		case a.Qual.Must:
			mustSup++
			mustMet += b2i(a.Verdict == Met)
		default:
			prefSup++
			prefMet += b2i(a.Verdict == Met)
		}
	}
	r.Grade = Grade(must, pref)
	if r.HasMust = mustSup > 0; r.HasMust {
		r.MustEvidence = float64(mustMet) / float64(mustSup)
	}
	if r.HasPref = prefSup > 0; r.HasPref {
		r.PrefEvidence = float64(prefMet) / float64(prefSup)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// share is met=1, partly=0.5, unmet=0, averaged. 0 for an empty list, which no
// caller reads.
func share(vs []string) float64 {
	var s float64
	for _, v := range vs {
		switch v {
		case Met:
			s++
		case Partly:
			s += 0.5
		}
	}
	if len(vs) == 0 {
		return 0
	}
	return s / float64(len(vs))
}

// Grade is Workday's rule on the verdicts of the must-have and the preferred
// qualifications. A: every must-have met and at least half the preferred met,
// or no preferred. B: every must-have met. C: at least half the must-haves met.
// D: otherwise. "Every must-have met" needs the verdict met, so a "partly" never
// reaches A or B; for the half tests a "partly" counts half. A posting with no
// must-have has every one of them met.
func Grade(must, pref []string) string {
	all := true
	for _, v := range must {
		all = all && v == Met
	}
	switch {
	case all && (len(pref) == 0 || share(pref) >= 0.5):
		return "A"
	case all:
		return "B"
	case share(must) >= 0.5:
		return "C"
	}
	return "D"
}
