package phase2

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/text"
)

// assembleReport writes the whole markdown report in code.
//
// The model never writes markdown. It answers schema-constrained questions and
// the shape is ours — which is what makes the report stable enough to diff
// between two runs and what keeps a verbose answer from restructuring the page.
func assembleReport(in reportInput) string {
	var md strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&md, format+"\n", a...) }

	pre := "N/A"
	if in.HasPreScore {
		pre = trimFloat(in.PreScore)
	}
	p("# Evaluation: %s — %s", in.Parsed.Company, in.Parsed.Role)
	p("")
	p("**Date:** %s", in.Today)
	p("**Archetype:** %s", in.J.Archetype)
	p("**Score:** %s/5", trimFloat(in.Score))
	// Phase 1's score is reported but was withheld from every prompt, so the
	// evaluation could not anchor on it.
	p("**Score pre-screening (local model):** %s/5", pre)
	// One line where the Comp & Demand block was: a posted salary still carries
	// 20% of the score, and a report that drops the reason drops the explanation.
	if in.HasComp {
		p("**Salary:** %s — comp %d/5 in the score", text.FormatSalary(in.Salary), in.CompDim)
	} else {
		p("**Salary:** not stated — score is fit only")
	}
	p("**URL:** %s", in.URL)
	p("**PDF:** %s", in.PDFNote)
	p("**Batch ID:** %d", in.ID)
	p("")
	p("---")
	p("")
	p("## Machine Summary")
	p("")
	p("%s", in.MachineSummary)
	p("")
	p("---")
	p("")

	p("## A) Role Summary")
	p("")
	p("| Field | Value |")
	p("|-------|-------|")
	p("| Archetype | %s |", in.J.Archetype)
	p("| Domain | %s |", in.Parsed.Domain)
	p("| Seniority | %s%s |", in.Parsed.SeniorityLevel, yearsSuffix(in.Parsed.YearsRequired))
	p("| Remote policy | %s |", in.Parsed.RemotePolicy)
	p("| Location | %s |", in.Parsed.Location)
	p("| TL;DR | %s |", in.J.Notes)
	p("")

	p("## B) CV Match")
	p("")
	p("_Evidence retrieved semantically from cv.md. **Strong** = same work, same tools · **Transferable** = same work, different tools · **Gap** = not demonstrated. Requirement coverage (75%% must / 25%% nice-to-have, Transferable counts 0.6): **%s%%**._", text.Fixed(in.Coverage.Coverage*100, 0))
	p("")
	p("| JD Requirement | Candidate evidence | Strength |")
	p("|----------------|-------------------|----------|")
	for _, e := range in.Rows {
		req := e.Requirement
		if e.MustHave {
			req = "**[must]** " + req
		}
		p("| %s | %s | %s |", pipeSafe(req), text.Clip(pipeSafe(e.Evidence), 160), e.Strength)
	}
	p("")
	p("**Gaps:** %s", orNone(strings.Join(in.J.SoftGaps, "; "), "none identified"))
	// Can legitimately be empty: unverifiable strengths are dropped, not kept.
	p("**Top strengths:** %s", orNone(strings.Join(in.J.TopStrengths, "; "), "none verifiable against cv.md"))
	p("")

	p("## C) Level & Strategy")
	p("")
	p("**JD seniority level:** %s", in.Parsed.SeniorityLevel)
	p("**Candidate natural level:** early-career engineer with production track record")
	p("")
	if len(in.J.Strategy) > 0 {
		p("**Strategy to position without overpromising:**")
		for _, s := range in.J.Strategy {
			p("- %s", s)
		}
		p("")
	}
	p("**If downlevelled:** accept if comp is fair; set a written 6-month review criteria.")
	p("")

	p("## D) Personalisation Plan")
	p("")
	p("Top CV changes for this specific role:")
	p("")
	p("| # | Section | Current | Proposed change | Why |")
	p("|---|---------|---------|-----------------|-----|")
	for i, x := range in.J.Personalisation {
		p("| %d | %s | %s | %s | %s |", i+1, pipeSafe(x.Section), pipeSafe(x.Current), pipeSafe(x.Proposed), pipeSafe(x.Why))
	}
	p("")

	if len(in.Calib) > 0 {
		p("## Similar Past Offers (calibration)")
		p("")
		for _, c := range in.Calib {
			label := ""
			if c.HasUserLabel {
				label = fmt.Sprintf(", user rating %g/5", c.UserLabel)
			}
			p("- %s — %s: %g/5, %s%s (sim %g)", c.Company, c.Role, c.Score, c.Decision, label, c.Sim)
		}
		p("")
	}

	p("---")
	p("")
	p("## Keywords")
	p("")
	// The Node builder ends with an empty array element, which join turns into
	// the final newline and nothing more. A trailing p("") here would add a
	// second one.
	p("%s", strings.Join(dedupe(append(append([]string{}, in.Parsed.Keywords...), in.Parsed.TechStack...), 20), ", "))
	return md.String()
}

type reportInput struct {
	ID             int
	URL            string
	Today          string
	Parsed         Parsed
	Rows           []Row
	Coverage       Coverage
	J              Judgment
	Salary         *text.Salary
	CompDim        int
	HasComp        bool
	Score          float64
	PreScore       float64
	HasPreScore    bool
	PDFNote        string
	MachineSummary string
	Calib          []Calibration
}

// pipeSafe substitutes a literal pipe. One in any cell corrupts the row and
// every column after it, and the report's own tables are parsed downstream.
func pipeSafe(s string) string { return strings.ReplaceAll(s, "|", "/") }

func yearsSuffix(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d+ yrs)", n)
}

func dedupe(in []string, cap int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == cap {
			break
		}
	}
	return out
}

// trimFloat renders a score the way JSON.stringify does: no trailing zeros.
func trimFloat(f float64) string { return fmt.Sprintf("%g", f) }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
