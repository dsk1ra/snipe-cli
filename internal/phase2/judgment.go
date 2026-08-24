package phase2

import (
	"context"
	"fmt"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Judgment is stage 3's output: the dimensions and every content block, in one
// call.
//
// Scoring and the content blocks stay together deliberately. Splitting them
// (dimensions first, prose second) was measured over 18 hand-labelled offers
// and collapsed rank agreement from 0.578 to 0.101 — and to 0.018 when the CV
// was also withheld. The dimensions decode before any story token exists, so
// this is not the prose leaking into the scores; committing to justify the
// score with specific CV lines and real projects appears to be what keeps the
// score honest. Do not split it again without re-running the eval harness
// against the labels.
type Judgment struct {
	CVMatch          int           `json:"cv_match"`
	NorthStar        int           `json:"north_star"`
	RedFlagsScore    int           `json:"red_flags_score"`
	Archetype        string        `json:"archetype"`
	HardStops        []string      `json:"hard_stops"`
	SoftGaps         []string      `json:"soft_gaps"`
	TopStrengths     []string      `json:"top_strengths"`
	Strategy         []string      `json:"strategy"`
	Personalisation  []Personalise `json:"personalisation"`
	LinkedIn         []string      `json:"linkedin"`
	Stories          []Story       `json:"stories"`
	HardQuestions    []QA          `json:"hard_questions"`
	LegitimacyTier   string        `json:"legitimacy_tier"`
	LegitimacyReason string        `json:"legitimacy_reason"`
	FinalDecision    string        `json:"final_decision"`
	Notes            string        `json:"notes"`
}

type Personalise struct {
	Section  string `json:"section"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
	Why      string `json:"why"`
}

type Story struct {
	Req         int    `json:"req"`
	Requirement string `json:"requirement,omitempty"`
	Story       string `json:"story"`
	Situation   string `json:"situation"`
	Task        string `json:"task"`
	Action      string `json:"action"`
	Result      string `json:"result"`
}

type QA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// capped is a string property with a length limit.
//
// maxItems alone does not bound the output: the grammar happily decodes ONE
// string forever, and did — two offers failed by looping inside a single
// top_strengths entry, comma-appending CV skill fragments for 7128 of 21241
// characters until num_predict ran out mid-string. The truncated JSON then
// failed to parse and the whole evaluation was lost. maxLength makes the
// sampler close the quote at the cap instead, so a verbose answer degrades to a
// clipped sentence rather than a lost evaluation.
//
// Limits are about 1.5× the p95 measured over the stored evals, so normal
// output is untouched and only the runaway case is cut.
//
// ponytail: keep every cap at or under 1500. llama.cpp expands maxLength into
// that many nested optional char rules and the grammar stops compiling
// somewhere between 1800 and 2000 — the call then dies with HTTP 400 "failed to
// parse grammar", which is a worse failure than the one this fixes. The limit
// is per field, not cumulative.
func capped(n int) ollama.Schema {
	return ollama.Obj(ollama.F("type", "string"), ollama.F("maxLength", n))
}

func cappedArr(maxLen, maxItems int, minItems ...int) ollama.Schema {
	s := ollama.Obj(ollama.F("type", "array"))
	if len(minItems) > 0 {
		s = append(s, ollama.F("minItems", minItems[0]))
	}
	return append(s, ollama.F("maxItems", maxItems), ollama.F("items", capped(maxLen)))
}

// storiesSchema constrains STAR targets to rows that actually have evidence.
//
// The targets are chosen by CODE, not by the model: req is an index into the
// eligible list, so a story about a requirement the CV does not cover is not
// merely discouraged — it cannot be encoded.
//
// The old schema asked for free-text `requirement` with minItems 3 while the
// prompt pointed at "the hardest requirements", which are exactly the ones
// graded Gap. That made fabrication the only grammatically valid output, and it
// duly produced stories about technologies the CV had none of. When nothing is
// covered, an empty Block F is the honest answer.
func storiesSchema(eligible []int) ollama.Schema {
	if len(eligible) == 0 {
		return ollama.Obj(
			ollama.F("type", "array"),
			ollama.F("maxItems", 0),
			ollama.F("items", ollama.Obj(ollama.F("type", "object"))),
		)
	}
	return ollama.Obj(
		ollama.F("type", "array"),
		ollama.F("minItems", min(3, len(eligible))),
		ollama.F("maxItems", min(5, len(eligible))),
		ollama.F("items", ollama.Object(ollama.Obj(
			// An enum of the allowed evidence-table row numbers. The same
			// grammar-level guarantee as a bounded integer, but it indexes the
			// table the prompt already contains, so the targets need not be
			// re-listed.
			ollama.F("req", ollama.Obj(ollama.F("type", "integer"), ollama.F("enum", eligible))),
			// Caps from 417 stored STAR rows (p95 / observed max):
			// story 857/1648 · situation 239/515 · task 162/209 · action 811/1316.
			ollama.F("story", capped(1200)),
			ollama.F("situation", capped(400)),
			ollama.F("task", capped(300)),
			ollama.F("action", capped(1000)),
			ollama.F("result", capped(800)),
		))),
	)
}

func stage3Schema(eligible []int) ollama.Schema {
	dim := ollama.Int(1, 5)
	// `stories` comes LAST in properties, and mid-list in required.
	//
	// That asymmetry is not a mistake to tidy up. The Node schema spreads the
	// stories property onto the object literal, which appends it at the end,
	// while its `required` array lists it between linkedin and hard_questions.
	// Ollama compiles `format` into a decoding grammar that emits properties in
	// *properties* order, so moving stories earlier changes the context every
	// later field is generated against — measured: identical dimensions,
	// visibly different notes, soft_gaps and top_strengths.
	props := ollama.Obj(
		ollama.F("cv_match", dim),
		ollama.F("north_star", dim),
		ollama.F("red_flags_score", dim),
		ollama.F("archetype", capped(80)),
		ollama.F("hard_stops", cappedArr(200, 6)),
		ollama.F("soft_gaps", cappedArr(240, 5)),
		ollama.F("top_strengths", cappedArr(160, 3)),
		ollama.F("strategy", cappedArr(400, 4, 2)),
		ollama.F("personalisation", ollama.Obj(
			ollama.F("type", "array"),
			ollama.F("minItems", 3),
			ollama.F("maxItems", 5),
			ollama.F("items", ollama.Object(ollama.Obj(
				ollama.F("section", capped(80)),
				ollama.F("current", capped(400)),
				ollama.F("proposed", capped(400)),
				ollama.F("why", capped(300)),
			))),
		)),
		ollama.F("linkedin", cappedArr(300, 3, 2)),
		ollama.F("hard_questions", ollama.Obj(
			ollama.F("type", "array"),
			ollama.F("minItems", 2),
			ollama.F("maxItems", 3),
			ollama.F("items", ollama.Object(ollama.Obj(
				ollama.F("q", capped(200)),
				ollama.F("a", capped(800)),
			))),
		)),
		ollama.F("legitimacy_tier", ollama.Str("High Confidence", "Proceed with Caution", "Suspicious")),
		ollama.F("legitimacy_reason", capped(400)),
		ollama.F("final_decision", ollama.Str("Apply", "Research first", "Consider", "Skip")),
		ollama.F("notes", capped(1500)),
		ollama.F("stories", storiesSchema(eligible)),
	)
	return ollama.Object(props,
		"cv_match", "north_star", "red_flags_score", "archetype", "hard_stops",
		"soft_gaps", "top_strengths", "strategy", "personalisation", "linkedin",
		"stories", "hard_questions", "legitimacy_tier", "legitimacy_reason",
		"final_decision", "notes")
}

// stageContext builds the shared prompt fragments. Both the evidence table and
// the story targets are numbered against the same list, so the targets are row
// numbers rather than a second copy of the table — re-listing each target with
// its evidence cost 1500–2000 characters of pure duplication, and on the
// longest JD that squeezed the output budget until the eval failed.
func stageContext(rows []Row, calib []Calibration) (evidenceTable string, eligible []int, targetLines, calibLines string) {
	var b strings.Builder
	for i, e := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		must := ""
		if e.MustHave {
			must = ", MUST"
		}
		fmt.Fprintf(&b, "%d. [%s%s] %s\n   evidence: %s", i+1, e.Strength, must, e.Requirement, clip(e.Evidence, 180))
		if e.Note != "" {
			fmt.Fprintf(&b, "\n   note: %s", e.Note)
		}
	}
	evidenceTable = b.String()

	// Only requirements with real retrieved evidence can host a STAR story.
	for i, e := range rows {
		if e.Strength != "Gap" {
			eligible = append(eligible, i+1)
		}
	}
	if len(eligible) > 0 {
		targetLines = fmt.Sprintf("Requirements %s (numbered as in the evidence table above). No others have supporting CV evidence.",
			joinInts(eligible, ", "))
	} else {
		targetLines = "(none — no requirement has supporting CV evidence, so write no stories)"
	}

	if len(calib) == 0 {
		calibLines = "(none available)"
		return
	}
	var cb strings.Builder
	for i, c := range calib {
		if i > 0 {
			cb.WriteString("\n")
		}
		fmt.Fprintf(&cb, "- %s — %s: scored %g/5, decision %s", c.Company, c.Role, c.Score, c.Decision)
		if c.HasUserLabel {
			fmt.Fprintf(&cb, ", user's own rating %g/5", c.UserLabel)
		}
		if c.Outcome != "" {
			fmt.Fprintf(&cb, ", real outcome: %s", c.Outcome)
		}
		fmt.Fprintf(&cb, " (similarity %g)", c.Sim)
	}
	calibLines = cb.String()
	return
}

// Calibration is one similar past offer, used as a few-shot anchor.
type Calibration struct {
	ID           string  `json:"id"`
	Company      string  `json:"company"`
	Role         string  `json:"role"`
	Score        float64 `json:"score"`
	Decision     string  `json:"decision"`
	UserLabel    float64 `json:"user_label,omitempty"`
	HasUserLabel bool    `json:"-"`
	Outcome      string  `json:"outcome,omitempty"`
	Sim          float64 `json:"sim"`
}

type stage3Input struct {
	Parsed   Parsed        `json:"parsed"`
	Rows     []Row         `json:"rows"`
	Coverage Coverage      `json:"coverage"`
	Calib    []Calibration `json:"calibration"`
	Salary   *text.Salary  `json:"salary"`
	JD       string        `json:"jd"`
	CVHash   string        `json:"cv_hash"`
}

func (e *Evaluator) stage3(ctx context.Context, id int, in stage3Input) (Judgment, error) {
	evidenceTable, eligible, targetLines, calibLines := stageContext(in.Rows, in.Calib)

	salaryLine := "Not stated. Do NOT guess one."
	if in.Salary != nil {
		salaryLine = fmt.Sprintf("%s (parsed from JD; comp is scored by the system, not you)",
			text.FormatSalary(in.Salary))
	}
	years := ""
	if in.Parsed.YearsRequired > 0 {
		years = fmt.Sprintf(" (%d+ years demanded)", in.Parsed.YearsRequired)
	}

	user := strings.Join([]string{
		"## Role (parsed)",
		in.Parsed.Company + " — " + in.Parsed.Role,
		fmt.Sprintf("Seniority: %s%s | Remote: %s | Location: %s | Domain: %s",
			in.Parsed.SeniorityLevel, years, in.Parsed.RemotePolicy, in.Parsed.Location, in.Parsed.Domain),
		"",
		"## Posted salary", salaryLine, "",
		"## Evidence table (requirement → graded CV evidence)", evidenceTable, "",
		fmt.Sprintf("Coverage of must-have requirements: %s%%", text.Fixed(in.Coverage.Coverage*100, 0)),
		"",
		"## Story targets", targetLines, "",
		"## Similar past offers (calibration anchors)", calibLines, "",
		"## Candidate profile (archetypes, framing, deal-breakers)", orNone(e.Cfg.ProfileMD, "(no profile)"), "",
		"## Candidate CV (cite real lines only)", e.Cfg.CV, "",
		"## JD excerpt (context)", text.CleanJD(in.JD, 2500),
	}, "\n")

	system, err := e.prompt("p2-judgment.md")
	if err != nil {
		return Judgment{}, err
	}

	var out Judgment
	// Stage 3 emits every content block at once and used to run right at a
	// 3000-token ceiling, where one offer truncated twice and lost the eval.
	// 5120 leaves real headroom; num_ctx 12288 keeps prompt plus output inside
	// the window, which q8_0 KV cache makes fit.
	if err := e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: system, User: user,
		Schema:  stage3Schema(eligible),
		Options: ollama.Options{Temperature: 0, NumCtx: 12288, NumPredict: 5120},
		Timeout: e.Timeout, Label: "p2.judgment", ID: id,
	}, &out); err != nil {
		return Judgment{}, fmt.Errorf("stage 3 (judgment): %w", err)
	}

	// Resolve target numbers back to requirement text. Out-of-range indices are
	// dropped rather than rendered.
	allowed := map[int]bool{}
	for _, n := range eligible {
		allowed[n] = true
	}
	var kept []Story
	for _, s := range out.Stories {
		if allowed[s.Req] {
			s.Requirement = in.Rows[s.Req-1].Requirement
			kept = append(kept, s)
		}
	}
	out.Stories = kept
	return out, nil
}

func joinInts(ns []int, sep string) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, sep)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func orNone(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
