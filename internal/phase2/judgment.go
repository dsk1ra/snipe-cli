package phase2

import (
	"context"
	"fmt"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Judgment is stage 3's output: the dimensions and the CV-facing content
// blocks, in one call.
//
// The report carries only what decides the tailored CV. STAR stories, hard
// questions, LinkedIn tweaks and the posting-legitimacy verdict were cut, and
// nothing downstream read them. Measured over the 18 labelled offers: output
// 3,022 → 982 tokens, this call 158 s → 57 s, pair accuracy within one offer's
// flip of the baseline (docs/REWORK-PLAN.md §1). The multi-posting check that
// legitimacy stood in for is a code gate in compose and stays.
//
// The old note here — that splitting scoring from the content blocks collapsed
// rank agreement 0.578 → 0.101 — measured against the July evals archive, which
// batch/labels/LEDGER.md records as an invalid control. Re-run the eval
// harness against the labels before reading anything into a change here.
type Judgment struct {
	CVMatch         int           `json:"cv_match"`
	RedFlagsScore   int           `json:"red_flags_score"`
	Archetype       string        `json:"archetype"`
	HardStops       []string      `json:"hard_stops"`
	SoftGaps        []string      `json:"soft_gaps"`
	TopStrengths    []string      `json:"top_strengths"`
	Strategy        []string      `json:"strategy"`
	Personalisation []Personalise `json:"personalisation"`
	FinalDecision   string        `json:"final_decision"`
	Notes           string        `json:"notes"`
}

type Personalise struct {
	Section  string `json:"section"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
	Why      string `json:"why"`
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

// stage3Schema takes the profile's archetype enum because north_star is now
// looked up from the archetype in code (config.NorthStar), and a lookup needs
// the exact label. Phase 1 constrains the same field the same way. With no
// profile list it stays free text and north_star falls back to neutral.
func stage3Schema(archetypes []string) ollama.Schema {
	dim := ollama.Int(1, 5)
	archetype := capped(80)
	if len(archetypes) > 0 {
		archetype = ollama.Str(archetypes...)
	}
	// Ollama compiles `format` into a decoding grammar that emits properties in
	// *properties* order, so reordering them changes the context every later
	// field is generated against — measured: identical dimensions, visibly
	// different notes, soft_gaps and top_strengths.
	props := ollama.Obj(
		ollama.F("cv_match", dim),
		ollama.F("red_flags_score", dim),
		ollama.F("archetype", archetype),
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
		ollama.F("final_decision", ollama.Str("Apply", "Research first", "Consider", "Skip")),
		ollama.F("notes", capped(1500)),
	)
	return ollama.Object(props,
		"cv_match", "red_flags_score", "archetype", "hard_stops",
		"soft_gaps", "top_strengths", "strategy", "personalisation",
		"final_decision", "notes")
}

// stageContext builds the shared prompt fragments: the numbered evidence table
// and the calibration anchors.
func stageContext(rows []Row, calib []Calibration) (evidenceTable, calibLines string) {
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
	evidenceTable, calibLines := stageContext(in.Rows, in.Calib)

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
	// num_ctx stays at 12288 although the output shrank: Ollama keys a loaded
	// model on its context size, and changing it was measured to change the
	// answers at temperature 0 (PHASE3-GENERATION-LEDGER §8). NumPredict is a
	// ceiling, not a cost.
	if err := e.OL.Chat(ctx, ollama.Request{
		Model: e.Model, System: system, User: user,
		Schema:  stage3Schema(e.Cfg.ArchetypeEnum()),
		Options: ollama.Options{Temperature: 0, NumCtx: 12288, NumPredict: 5120},
		Timeout: e.Timeout, Label: "p2.judgment", ID: id,
	}, &out); err != nil {
		return Judgment{}, fmt.Errorf("stage 3 (judgment): %w", err)
	}
	return out, nil
}

func orNone(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
