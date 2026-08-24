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
	p("**Legitimacy:** %s", in.J.LegitimacyTier)
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

	md.WriteString(compBlock(in.Salary, in.CompDim, in.HasComp, in.CompTargets))

	p("## E) Personalisation Plan")
	p("")
	p("Top CV changes for this specific role:")
	p("")
	p("| # | Section | Current | Proposed change | Why |")
	p("|---|---------|---------|-----------------|-----|")
	for i, x := range in.J.Personalisation {
		p("| %d | %s | %s | %s | %s |", i+1, pipeSafe(x.Section), pipeSafe(x.Current), pipeSafe(x.Proposed), pipeSafe(x.Why))
	}
	p("")
	p("Top LinkedIn changes:")
	for i, l := range in.J.LinkedIn {
		p("%d. %s", i+1, l)
	}
	p("")

	p("## F) Interview Prep")
	p("")
	if len(in.J.Stories) == 0 {
		p("_No STAR stories: no JD requirement has supporting evidence in cv.md. Inventing one here would put experience you do not have in front of an interviewer._")
		p("")
	} else {
		p("STAR stories mapped to JD requirements:")
		p("")
		p("| # | JD Requirement | Story | S | T | A | R |")
		p("|---|----------------|-------|---|---|---|---|")
		for i, s := range in.J.Stories {
			p("| %d | %s | %s | %s | %s | %s | %s |", i+1,
				pipeSafe(s.Requirement), pipeSafe(s.Story), pipeSafe(s.Situation),
				pipeSafe(s.Task), pipeSafe(s.Action), pipeSafe(s.Result))
		}
	}
	if len(in.J.HardQuestions) > 0 {
		p("")
		p("**Likely hard questions:**")
		for i, q := range in.J.HardQuestions {
			p("%d. Q: %q → A: %s", i+1, q.Q, q.A)
		}
	}
	p("")

	p("## G) Posting Legitimacy")
	p("")
	p("**Verification:** unconfirmed (batch mode — Playwright unavailable)")
	p("")
	p("| Signal | Assessment |")
	p("|--------|------------|")
	p("| Salary transparency | %s |", ifElse(in.Salary != nil, "Disclosed", "Not disclosed"))
	p("| Requirements | %d parsed, %d must-have |", len(in.Rows), in.Coverage.MustCount)
	p("| Reposting | check scan-history.tsv manually |")
	p("")
	p("**Tier:** %s", in.J.LegitimacyTier)
	p("")
	p("**Reason:** %s", in.J.LegitimacyReason)
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
	CompTargets    *text.CompTargets
	Score          float64
	PreScore       float64
	HasPreScore    bool
	PDFNote        string
	MachineSummary string
	Calib          []Calibration
}

func compBlock(s *text.Salary, dim int, hasComp bool, t *text.CompTargets) string {
	var b strings.Builder
	b.WriteString("## D) Comp & Demand\n\n")
	b.WriteString("| Source | Salary range | Confidence |\n")
	b.WriteString("|--------|-------------|------------|\n")
	if s == nil {
		b.WriteString("| Job posting | Not stated | — |\n\n")
		b.WriteString("**Comp score:** excluded from the composite — no salary stated in the posting (score = cv_match × 0.625 + north_star × 0.375).\n")
		b.WriteString("\n")
		return b.String()
	}
	fmt.Fprintf(&b, "| Job posting (parsed) | %s | High |\n\n", text.FormatSalary(s))
	note := "— no comp targets configured, scored neutral"
	if t != nil {
		note = fmt.Sprintf("vs target %s%s–%s%s (floor %s%s)",
			s.Currency, commas(t.TargetLow), s.Currency, commas(t.TargetHigh), s.Currency, commas(t.Floor))
	}
	fmt.Fprintf(&b, "**Comp score (1–5):** %d — posted salary %s.\n\n", dim, note)
	return b.String()
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

func ifElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
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

func commas(n int) string {
	s := fmt.Sprint(n)
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
