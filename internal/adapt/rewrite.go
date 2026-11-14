package adapt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// The rewrite step of docs/PRD-TAILORING-REWORK.md §6.3. The model gets one
// narrow brief per bullet (the plan) and writes two drafts of it; code then
// drops what the cheap checks reject. Nothing here decides whether a draft is
// true. That is verify.Verify, with MiniCheck, and it runs after this.

const (
	// perCall is how many bullets share one call. Decompose hit its output cap
	// with eight bullets in one reply, so the cap is set where the output
	// budget below stays small, not where the context would fill.
	perCall = 5

	// excerptMax is how much of the posting the model sees. It is there for
	// register only, and every extra sentence is one more thing to copy.
	excerptMax = 1000

	// rewriteCtx is the window the other 30B stages already use (judgment and
	// the Phase 3 judge). Ollama keys a loaded model on its context size, so any
	// other value reloads 18 GB between stages, and changes greedy output
	// (generation ledger §8).
	rewriteCtx = 12288

	// lineChars mirrors cvselect's unexported charsPerLine, which a draft's
	// length cap is derived from. TestLineChars fails if the two drift.
	lineChars = 108

	// charsPerToken is deliberately pessimistic: tool names and symbols
	// tokenise worse than prose, and a reply cut off by NumPredict is invalid
	// JSON, which loses the whole chunk.
	charsPerToken = 2.5
)

// Stats is what one Rewrite did, for the run log and the §8.1 metrics.
type Stats struct {
	Plans    int // plans given
	Skipped  int // Verbatim, unknown to the ledger, or nothing to adapt: no call, the original ships
	Calls    int
	Failed   int // calls that returned an error; their bullets ship as the original
	Received int // non-empty drafts the model gave for a bullet number it was asked about
	Kept     int
	// Rejects counts the drafts dropped, by reason: "number" (out of range or
	// repeated), "empty", "unchanged", "duplicate" (B equal to A), "forbidden",
	// and verify.Code's own "figures", "entities", "line cost" and "shape".
	Rejects map[string]int
}

// ShapeRejects is the §8.1 `shape_rejects` metric for this call.
func (s Stats) ShapeRejects() int { return s.Rejects["shape"] }

// job is a plan with the ledger entry its bullet lives in.
type job struct {
	plan  Plan
	entry ledger.Entry
}

// Rewrite asks the 30B for draft A and draft B of every plan worth adapting and
// returns the ones that pass the local filter. plans keep the caller's order.
// A bullet with no draft here ships as the original; the error is non-nil only
// when no call succeeded, so a partial result is still a result.
func Rewrite(ctx context.Context, ol *ollama.Client, system string, plans []Plan, led *ledger.Ledger, jdExcerpt string) ([]Draft, Stats, error) {
	st := Stats{Plans: len(plans), Rejects: map[string]int{}}
	var jobs []job
	for _, p := range plans {
		e, _ := find(led, p.Bullet.Text)
		if p.Verbatim || e == nil || !worthAdapting(p) {
			st.Skipped++
			continue
		}
		jobs = append(jobs, job{p, *e})
	}
	jd := clip(jdExcerpt, excerptMax)

	var out []Draft
	var firstErr error
	for _, chunk := range chunks(jobs, system, jd, led) {
		if err := ctx.Err(); err != nil {
			return out, st, err
		}
		st.Calls++
		var reply struct {
			Bullets []struct {
				N int    `json:"n"`
				A string `json:"a"`
				B string `json:"b"`
			} `json:"bullets"`
		}
		err := ol.Chat(ctx, ollama.Request{
			Model: ollama.ModelEval, System: system, User: render(chunk, jd, led),
			Schema:  schema(chunk),
			Options: ollama.Options{Temperature: 0, NumCtx: rewriteCtx, NumPredict: predict(chunk)},
			NoThink: true,
			Timeout: 180 * time.Second, Label: "p3.rewrite",
		}, &reply)
		if err != nil {
			st.Failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		seen := map[int]bool{}
		for _, r := range reply.Bullets {
			if r.N < 1 || r.N > len(chunk) || seen[r.N] {
				st.Rejects["number"]++
				continue
			}
			seen[r.N] = true
			j := chunk[r.N-1]
			var kept []string
			for _, d := range []struct{ kind, text string }{{"A", r.A}, {"B", r.B}} {
				text := strings.TrimSpace(d.text)
				if text != "" {
					st.Received++
				}
				if why := vet(j, led, text, kept); why != "" {
					st.Rejects[why]++
					if os.Getenv("SNIPE_ADAPT_DEBUG") != "" && text != "" {
						fmt.Printf("  debug rewrite rejected %s (%s): %s\n    source (%d line(s)): %s\n", d.kind, why, text, j.plan.MaxLines, j.plan.Bullet.Text)
					}
					continue
				}
				kept = append(kept, text)
				out = append(out, Draft{Hash: j.plan.Bullet.Hash, Kind: d.kind, Text: text})
				st.Kept++
			}
		}
	}
	if st.Calls > 0 && st.Failed == st.Calls {
		return out, st, firstErr
	}
	return out, st, nil
}

// worthAdapting is false when nothing could change: no requirement to lead
// with and no posting term the source does not already use.
func worthAdapting(p Plan) bool {
	if len(p.Targets) > 0 {
		return true
	}
	for _, t := range p.AllowedTerms {
		if !contains(p.Bullet.Text, t) {
			return true
		}
	}
	return false
}

// vet is the cheap local filter. It returns the reject reason, "" to keep.
// kept holds this bullet's earlier draft, so B is dropped when it only repeats A.
func vet(j job, led *ledger.Ledger, draft string, kept []string) string {
	src := j.plan.Bullet.Text
	switch {
	case draft == "":
		return "empty"
	case sameText(draft, src):
		return "unchanged"
	}
	for _, k := range kept {
		if sameText(draft, k) {
			return "duplicate"
		}
	}
	// verify.Code's entity check only sees capitalised names, so a lower-case
	// forbidden term ("microservices") would pass it.
	for _, t := range j.plan.Forbidden {
		if contains(draft, t) && !contains(src, t) {
			return "forbidden"
		}
	}
	var ents map[string][]string
	if led != nil {
		ents = led.Entities
	}
	o := verify.Options{Entities: ents, ExtraLine: j.plan.LinesFor(draft) > j.plan.MaxLines}
	if r := verify.Code(j.entry, j.plan.Bullet, draft, j.plan.AllowedTerms, o); r != "" {
		why, _, _ := strings.Cut(r, ":")
		return why
	}
	return ""
}

func sameText(a, b string) bool {
	n := func(s string) string { return strings.TrimRight(strings.Join(strings.Fields(s), " "), ".") }
	return n(a) == n(b)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

// ── Call shape ───────────────────────────────────────────────────────────────

// maxChars is the longest a draft of this bullet may be. The slack is under
// half a line, so a reply that runs into the cap is over the line budget and
// verify.LineCost drops it: the cap can never leave a clipped draft that passes.
func maxChars(p Plan) int {
	n := p.MaxLines
	if len(p.GrowFor) > 0 {
		n++
	}
	return n*lineChars + 40
}

func schema(chunk []job) ollama.Schema {
	n, longest := len(chunk), 0
	for _, j := range chunk {
		longest = max(longest, maxChars(j.plan))
	}
	capped := ollama.Obj(ollama.F("type", "string"), ollama.F("maxLength", longest))
	item := ollama.Object(ollama.Obj(
		ollama.F("n", ollama.Int(1, n)),
		ollama.F("a", capped),
		ollama.F("b", capped),
	))
	return ollama.Object(ollama.Obj(ollama.F("bullets", ollama.Arr(item, n))))
}

// predict budgets the reply as if every draft ran to its cap: two drafts per
// bullet plus ~20 tokens of JSON around them, and a little for the wrapper.
func predict(chunk []job) int {
	n := 32
	for _, j := range chunk {
		n += 2*int(float64(maxChars(j.plan))/charsPerToken+1) + 20
	}
	return n
}

// chunks splits the jobs into calls of at most perCall bullets, and fewer when
// the rendered prompt plus the output budget would not fit the window. With
// the real prompt (~2,400 tokens for five bullets) the second rule never
// fires; it is there so an entry with a huge fact list degrades to more calls
// and not to a silently truncated prompt.
func chunks(jobs []job, system, jd string, led *ledger.Ledger) [][]job {
	var out [][]job
	var cur []job
	size := int(float64(len(system)+len(jd))/charsPerToken) + 64
	for _, j := range jobs {
		cost := int(float64(len(renderOne(1, j, led)))/charsPerToken) + 1
		if len(cur) > 0 && (len(cur) == perCall || size+cost+predict(append(cur[:len(cur):len(cur)], j)) > rewriteCtx) {
			out = append(out, cur)
			cur, size = nil, int(float64(len(system)+len(jd))/charsPerToken)+64
		}
		cur = append(cur, j)
		size += cost
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// ── Prompt ───────────────────────────────────────────────────────────────────

func render(chunk []job, jd string, led *ledger.Ledger) string {
	var b strings.Builder
	b.WriteString("## Posting excerpt (register only)\n")
	if jd == "" {
		jd = "(none)"
	}
	b.WriteString(jd + "\n\n## Bullets\n")
	for i, j := range chunk {
		b.WriteString(renderOne(i+1, j, led))
	}
	return b.String()
}

func renderOne(n int, j job, led *ledger.Ledger) string {
	p := j.plan
	var facts []ledger.Fact
	for _, f := range p.Bullet.Facts {
		if f.Source != "inferred" {
			facts = append(facts, f)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n### %d\nSource: %s\nFacts:\n", n, p.Bullet.Text)
	for _, f := range facts {
		keep := ""
		if f.MustKeep {
			keep = " (must keep)"
		}
		fmt.Fprintf(&b, "- %s: %s%s\n", f.ID, f.Text, keep)
	}
	if len(p.Targets) > 0 {
		b.WriteString("Targets:\n")
		for _, t := range p.Targets {
			rank := "preferred"
			if t.Must {
				rank = "required"
			}
			by := ""
			if t.Fact != "" {
				by = ", answered by " + t.Fact
			}
			fmt.Fprintf(&b, "- %s (%s%s)\n", t.Requirement, rank, by)
		}
	}
	if p.Lead != "" {
		fmt.Fprintf(&b, "Open with: %s\n", p.Lead)
	}
	if len(p.AllowedTerms) == 0 {
		b.WriteString("Allowed terms: none\n")
	} else {
		b.WriteString("Allowed terms:\n")
		for _, t := range p.AllowedTerms {
			fmt.Fprintf(&b, "- %s: %s\n", t, license(led, p, &j.entry, facts, t))
		}
	}
	if len(p.Forbidden) == 0 {
		b.WriteString("Forbidden terms: none\n")
	} else {
		fmt.Fprintf(&b, "Forbidden terms: %s\n", strings.Join(p.Forbidden, ", "))
	}
	if len(p.MustKeep.Figures) > 0 {
		fmt.Fprintf(&b, "Figures to keep verbatim: %s\n", strings.Join(p.MustKeep.Figures, ", "))
	}
	var quals []string
	for _, q := range p.MustKeep.Qualifiers {
		if contains(p.Bullet.Text, q) {
			quals = append(quals, q)
		}
	}
	if len(quals) > 0 {
		fmt.Fprintf(&b, "Qualifiers to keep: %s\n", strings.Join(quals, ", "))
	}
	fmt.Fprintf(&b, "Max lines: %d (at most %d characters)\n", p.MaxLines, p.MaxLines*lineChars)
	if len(p.GrowFor) > 0 {
		fmt.Fprintf(&b, "One more line (at most %d characters) only for a draft that adds: %s\n",
			(p.MaxLines+1)*lineChars, strings.Join(p.GrowFor, ", "))
	}
	return b.String()
}

// license says why an allowed term is allowed, so the model can tell a swap of
// wording from a new claim and knows when a framing covers a change of verb.
func license(led *ledger.Ledger, p Plan, e *ledger.Entry, facts []ledger.Fact, term string) string {
	for _, fr := range p.Bullet.Framings {
		if fr.Approved && !fr.Rejected && strings.EqualFold(fr.Phrase, term) {
			return "approved framing of " + strings.Join(fr.From, ", ")
		}
	}
	switch {
	case mentions(facts, led, term):
		return "named in this line's facts"
	case mentions(inferenceStatus(e).approved, led, term):
		return "approved inference for this entry"
	}
	return "allowed"
}
