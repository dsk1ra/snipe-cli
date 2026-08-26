// Package phase2 is the staged evaluator: three schema-constrained calls (JD
// parse → embedding evidence match → judgment) with the report assembled in
// code, so the model never writes markdown.
//
// Phase 1's score is deliberately withheld from every prompt here, to avoid
// anchoring. The composite is computed in code from the dimensions, and the
// seniority, stack, language and location caps are code-enforced.
package phase2

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// ── Stage 1: JD parse ────────────────────────────────────────────────────────

type Requirement struct {
	Text     string `json:"text"`
	MustHave bool   `json:"must_have"`
}

type Parsed struct {
	IsSinglePosting bool          `json:"is_single_posting"`
	Company         string        `json:"company"`
	Role            string        `json:"role"`
	SeniorityLevel  string        `json:"seniority_level"`
	YearsRequired   int           `json:"years_required"`
	RemotePolicy    string        `json:"remote_policy"`
	Location        string        `json:"location"`
	Domain          string        `json:"domain"`
	Requirements    []Requirement `json:"requirements"`
	TechStack       []string      `json:"tech_stack"`
	Keywords        []string      `json:"keywords"`
}

func stage1Schema() ollama.Schema {
	return ollama.Object(ollama.Obj(
		ollama.F("is_single_posting", ollama.Obj(ollama.F("type", "boolean"))),
		ollama.F("company", ollama.Str()),
		ollama.F("role", ollama.Str()),
		ollama.F("seniority_level", ollama.Str("Junior", "Mid", "Senior", "Staff+", "Unspecified")),
		ollama.F("years_required", ollama.Int(0, 30)),
		ollama.F("remote_policy", ollama.Str()),
		ollama.F("location", ollama.Str()),
		ollama.F("domain", ollama.Str()),
		ollama.F("requirements", ollama.Obj(
			ollama.F("type", "array"),
			ollama.F("minItems", 3),
			ollama.F("maxItems", 12),
			ollama.F("items", ollama.Object(ollama.Obj(
				ollama.F("text", ollama.Str()),
				ollama.F("must_have", ollama.Obj(ollama.F("type", "boolean"))),
			))),
		)),
		ollama.F("tech_stack", ollama.Arr(ollama.Str(), 15)),
		ollama.F("keywords", ollama.Obj(
			ollama.F("type", "array"),
			ollama.F("minItems", 8),
			ollama.F("maxItems", 20),
			ollama.F("items", ollama.Str()),
		)),
	))
}

func (e *Evaluator) stage1(ctx context.Context, id int, jd string) (Parsed, error) {
	system, err := e.prompt("p2-jdparse.md")
	if err != nil {
		return Parsed{}, err
	}
	var out Parsed
	err = e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: system,
		User:    "## Job Description\n" + text.CleanJD(jd, 8000),
		Schema:  stage1Schema(),
		Options: ollama.Options{Temperature: 0, NumCtx: e.NumCtx, NumPredict: 900},
		Timeout: e.Timeout, Label: "p2.jdparse", ID: id,
	}, &out)
	if err != nil {
		return Parsed{}, fmt.Errorf("stage 1 (JD parse): %w", err)
	}
	if len(out.Requirements) == 0 {
		return Parsed{}, fmt.Errorf("stage 1 returned no requirements")
	}
	return out, nil
}

// ── Stage 2: evidence matching ───────────────────────────────────────────────

// Row is one requirement graded against the CV.
type Row struct {
	Requirement  string  `json:"requirement"`
	MustHave     bool    `json:"must_have"`
	Strength     string  `json:"strength"`
	SameActivity bool    `json:"same_activity"`
	SameTooling  string  `json:"same_tooling"`
	Evidence     string  `json:"evidence"`
	Sim          float64 `json:"sim"`
	HasSim       bool    `json:"-"`
	Note         string  `json:"note"`
}

type match struct {
	Req          int    `json:"req"`
	Pick         string `json:"pick"`
	SameActivity bool   `json:"same_activity"`
	SameTooling  string `json:"same_tooling"`
	Note         string `json:"note"`
}

func stage2Schema() ollama.Schema {
	return ollama.Object(ollama.Obj(
		ollama.F("matches", ollama.Arr(ollama.Object(ollama.Obj(
			ollama.F("req", ollama.Obj(ollama.F("type", "integer"), ollama.F("minimum", 1))),
			// `pick` first so the model commits to a piece of evidence before it
			// judges it — the axes are then about a fixed line, not a moving one.
			ollama.F("pick", ollama.Str("A", "B", "C", "none")),
			ollama.F("same_activity", ollama.Obj(ollama.F("type", "boolean"))),
			// Three-way, not boolean: plenty of requirements name no technology
			// at all (a degree, communication, mentoring). Forcing a yes/no
			// there made the model answer "no" and silently cost a full match
			// 40% of its weight.
			ollama.F("same_tooling", ollama.Str("same", "different", "not_applicable")),
			ollama.F("note", ollama.Str()),
		)), 0)),
	))
}

const letters = "ABC"

// topKCandidates is deliberately 3.
//
// Measured: top-6 with a 320-char slice cut retrieval starvation hard (pick
// "none" 33 → 21 rows, Gap-despite-tech-in-CV 7 → 3) but made the model MORE
// generous overall — rho 0.522 → 0.468, pair accuracy 78.9% → 75.7%,
// mis-grounded 6.3% → 7.6%, one atom answering 5 of 12 requirements on one
// offer, and another overflowing stage 3. More candidates gave it more rope to
// find a plausible-looking line for every requirement.
const topKCandidates = 3

// TopCandidates ranks demonstrations and catalogue lines in separate pools.
//
// A catalogue line is a long, technology-dense string whose vector sits near
// the centroid of "software job requirement", so it wins cosine against
// requirements it has nothing to do with. Over 400 real requirements from the
// report corpus a Skills or Education line was rank-1 for 44.0% of them, and
// one line — `Skills — AI Engineering` — was rank-1 for a fifth of everything,
// ahead of `Skills — Languages` on a Go requirement.
//
// fit.Catalogue then caps whatever cites one at Transferable, so those slots
// are spent on evidence the grader has already decided cannot score Strong.
// One is defensible; the tail is not. 17.8% of requirements gave a catalogue
// line two of the three slots and 3.2% gave it all three, which is a call made
// with no demonstration in front of the model at all. Splitting the pools
// guarantees at least k-1 demonstrations — the all-three case goes to zero and
// catalogue slots fall from 0.92 to 0.71 per requirement — while leaving the
// catalogue line reachable for what it genuinely answers: a degree, or a
// technology no bullet names.
//
// One slot, not a fourth: k=3 was measured, and top-6 made the model markedly
// more generous (rho 0.522 → 0.468). Substituting keeps the rope the same
// length rather than handing it more.
func TopCandidates(q []float64, demo, catalogue []embed.Atom, k int) ([]embed.Scored, error) {
	out, err := embed.TopK(q, demo, k)
	if err != nil {
		return nil, err
	}
	cat, err := embed.TopK(q, catalogue, 1)
	if err != nil {
		return nil, err
	}
	if len(cat) == 0 {
		return out, nil
	}
	switch {
	case len(out) < k:
		out = append(out, cat[0])
	case cat[0].Sim > out[k-1].Sim:
		out[k-1] = cat[0]
	default:
		return out, nil
	}
	// Re-sorted because the list is presented to the model as a ranking, and a
	// substitution puts the new entry in the slot it displaced rather than the
	// one it earned.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sim > out[j].Sim })
	return out, nil
}

func (e *Evaluator) stage2(ctx context.Context, id int, reqs []Requirement, cvIndex []embed.Atom) ([]Row, error) {
	texts := make([]string, len(reqs))
	for i, r := range reqs {
		texts[i] = r.Text
	}
	vecs, err := e.OL.Embed(ctx, texts, ollama.ModelEmbed)
	if err != nil {
		return nil, fmt.Errorf("stage 2 (embedding requirements): %w", err)
	}

	var demo, catalogue []embed.Atom
	for _, a := range cvIndex {
		if fit.Catalogue(a.Source) {
			catalogue = append(catalogue, a)
		} else {
			demo = append(demo, a)
		}
	}

	candidates := make([][]embed.Scored, len(reqs))
	var b strings.Builder
	for i := range reqs {
		candidates[i], err = TopCandidates(vecs[i], demo, catalogue, topKCandidates)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		tag := "nice-to-have"
		if reqs[i].MustHave {
			tag = "MUST"
		}
		fmt.Fprintf(&b, "R%d (%s): %s", i+1, tag, reqs[i].Text)
		for j, c := range candidates[i] {
			fmt.Fprintf(&b, "\n   %c) %s", letters[j], clip(c.Text, 200))
		}
	}

	system, err := e.prompt("p2-evidence.md")
	if err != nil {
		return nil, err
	}
	var out struct {
		Matches []match `json:"matches"`
	}
	if err := e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: system, User: b.String(),
		Schema:  stage2Schema(),
		Options: ollama.Options{Temperature: 0, NumCtx: e.NumCtx, NumPredict: 1800},
		Timeout: e.Timeout, Label: "p2.evidence", ID: id,
	}, &out); err != nil {
		return nil, fmt.Errorf("stage 2 (evidence match): %w", err)
	}

	// Normalise to exactly one row per requirement, in order. The model can
	// skip, duplicate or renumber; the table downstream cannot.
	byReq := map[int]match{}
	for _, m := range out.Matches {
		if m.Req >= 1 && m.Req <= len(reqs) {
			if _, dup := byReq[m.Req]; !dup {
				byReq[m.Req] = m
			}
		}
	}

	rows := make([]Row, len(reqs))
	for i, r := range reqs {
		m, ok := byReq[i+1]
		if !ok {
			m = match{Pick: "none", SameTooling: "not_applicable", Note: "no grade returned"}
		}
		pickIdx := strings.Index(letters, m.Pick)
		if m.Pick == "" || m.Pick == "none" {
			pickIdx = -1
		}
		atomText, atomSource := "", ""
		if pickIdx >= 0 && pickIdx < len(candidates[i]) {
			atomText = candidates[i][pickIdx].Text
			atomSource = candidates[i][pickIdx].Source
		} else {
			pickIdx = -1
		}
		strength := fit.StrengthFrom(m.Pick, m.SameActivity, m.SameTooling, r.Text, atomText, atomSource)

		// A Gap has no evidence to show even when the model picked a line —
		// printing the rejected candidate would read as support for the
		// requirement.
		shown := pickIdx
		if strength == fit.Gap {
			shown = -1
		}
		row := Row{
			Requirement: r.Text, MustHave: r.MustHave, Strength: strength,
			SameActivity: m.SameActivity, SameTooling: m.SameTooling,
			Evidence: "—", Note: m.Note,
		}
		if shown >= 0 {
			row.Evidence = candidates[i][shown].Text
			row.Sim = round(candidates[i][shown].Sim, 3)
			row.HasSim = true
		}
		rows[i] = row
	}
	return rows, nil
}

// Coverage is the continuous fit metric derived from the evidence table. It is
// what turns three integer buckets into a real ranking.
type Coverage struct {
	Coverage  float64 `json:"coverage"`
	MustCount int     `json:"must_count"`
}

// transferableWeight is the "how much do I discount a stack I have not used?"
// dial — raise it to chase adjacent stacks, lower it to hold out for
// exact-stack roles.
const transferableWeight = 0.6

func coverageMetric(rows []Row) Coverage {
	if len(rows) == 0 {
		return Coverage{}
	}
	weight := func(s string) float64 {
		switch s {
		case fit.Strong:
			return 1
		case fit.Transferable:
			return transferableWeight
		}
		return 0
	}
	avg := func(pool []Row) float64 {
		var sum float64
		for _, r := range pool {
			sum += weight(r.Strength)
		}
		return sum / float64(len(pool))
	}
	var must, nice []Row
	for _, r := range rows {
		if r.MustHave {
			must = append(must, r)
		} else {
			nice = append(nice, r)
		}
	}
	var cov float64
	switch {
	// 75/25 must/nice split, so uncovered nice-to-haves temper a perfect must
	// score. Pure must-coverage saturated at 1.0 and pinned strong fits to 5.0.
	case len(must) >= 2 && len(nice) > 0:
		cov = 0.75*avg(must) + 0.25*avg(nice)
	case len(must) >= 2:
		cov = avg(must)
	default:
		cov = avg(rows) // the JD tagged nearly everything nice-to-have
	}
	return Coverage{Coverage: round(cov, 3), MustCount: len(must)}
}

// clip mirrors the Node implementation's .slice(0, n) — UTF-16 code units, not
// bytes. See text.Clip.
func clip(s string, n int) string { return text.Clip(s, n) }
