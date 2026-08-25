package guards

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/dsk1ra/snipe-cli/internal/fill"
)

// VerifyProjectFigures strips a clause claiming a figure this project's own CV
// entry does not state.
//
// Projects had no figure guard at all — only the product one, which knows names
// and not numbers. Observed on one CV: a blurb claiming "970%+ revenue growth"
// (absent from cv.md entirely) alongside another claiming "sub-500ms load times"
// (real, but a different project's). One entry-scoped check catches both,
// because neither figure is in the entry doing the claiming.
//
// Clause surgery rather than reversion: a blurb is synthesised from several
// source bullets, so reverting joins them into a run-on — which is exactly what
// shipped on 3 of 12 CVs as a 55-word paragraph next to 12-word siblings.
//
// A project whose name resolves to nothing is left alone rather than gutted.
func VerifyProjectFigures(projects []fill.ProjectPick, cvText string) []fill.ProjectPick {
	entries := section(cvText, "Projects")
	if entries == nil {
		return projects
	}
	byName := map[string]map[string]bool{}
	for _, e := range entries {
		byName[strings.ToLower(entryTitle(e))] = NumbersIn(entryText(e))
	}

	out := make([]fill.ProjectPick, len(projects))
	copy(out, projects)
	for i, p := range out {
		allowed, ok := byName[strings.ToLower(strings.TrimSpace(p.Name))]
		if !ok {
			continue
		}
		out[i].Description = StripUnsupportedClauses(p.Description, func(t string) bool {
			for n := range NumbersIn(t) {
				if !allowed[n] {
					return true
				}
			}
			return false
		})
	}
	return out
}

// splitClauses cuts on a full stop or semicolon at bracket depth zero.
//
// Whitespace must follow, so "0.4 ms" and "e.g." are not boundaries.
func splitClauses(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case (c == '.' || c == ';') && depth == 0 && followedBySpace(s, i+1):
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// followedBySpace treats the end of the string as whitespace, as `s[i] ?? ' '`
// does.
func followedBySpace(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	return unicode.IsSpace(rune(s[i]))
}

func wordCount(s string) int {
	return len(strings.Fields(strings.TrimSpace(s)))
}

var clauseTrimLead = regexp.MustCompile(`^[;\s]+`)
var clauseTrimTail = regexp.MustCompile(`[.;\s]+$`)
var dotTail = regexp.MustCompile(`[.\s]+$`)

// PadProjectDescriptions fills a short project blurb from that project's own CV
// bullets.
//
// The prompt asks for two sentences and 35-55 words; the model routinely returns
// twelve. A CV bullet is often one long semicolon-joined sentence, so splitting
// on clause boundaries is what keeps the pad from overshooting the band.
//
// Safe to run last, after the product and figure guards, because padded text is
// verbatim cv.md: it names no product the CV lacks and quotes no figure the
// project's own entry lacks, so it passes both by construction. Padding FIRST
// was the obvious placement and the wrong one — clause surgery could then take a
// clause back off and ship the blurb under the floor with nothing re-checking.
func PadProjectDescriptions(projects []fill.ProjectPick, cvText string, minWords, maxWords int) []fill.ProjectPick {
	entries := section(cvText, "Projects")
	if entries == nil {
		return projects
	}
	type real struct {
		name    string
		bullets []string
	}
	var reals []real
	for _, e := range entries {
		reals = append(reals, real{entryTitle(e), e.Bullets})
	}

	out := make([]fill.ProjectPick, len(projects))
	copy(out, projects)
	for i, p := range out {
		desc := strings.TrimSpace(p.Description)
		// "Built a high-performance." — the model sometimes stops mid-clause.
		// Padding such a description only welds good CV prose onto a broken
		// opening, which reads worse than not tailoring at all, so it is discarded
		// and the blurb is built from the CV.
		if cs := splitClauses(desc); len(cs) == 0 || wordCount(cs[0]) < 6 {
			desc = ""
		}
		if desc != "" && wordCount(desc) >= minWords {
			continue
		}
		pn := strings.ToLower(strings.TrimSpace(p.Name))
		var src *real
		for j := range reals {
			if strings.ToLower(reals[j].name) == pn {
				src = &reals[j]
				break
			}
		}
		if src == nil && pn != "" {
			for j := range reals {
				rn := strings.ToLower(reals[j].name)
				if strings.Contains(rn, pn) || strings.Contains(pn, rn) {
					src = &reals[j]
					break
				}
			}
		}
		if src == nil {
			continue
		}

		dt := Toks(desc)
		covered := func(c string) float64 {
			ct := Toks(c)
			if len(ct) == 0 {
				return 1
			}
			shared := 0
			for t := range ct {
				if dt[t] {
					shared++
				}
			}
			return float64(shared) / float64(len(ct))
		}

		type chunk struct {
			text string
			cov  float64
		}
		var chunks []chunk
		for _, b := range src.bullets {
			for _, c := range splitClauses(b) {
				c = strings.TrimSpace(clauseTrimTail.ReplaceAllString(clauseTrimLead.ReplaceAllString(c, ""), ""))
				if c != "" {
					chunks = append(chunks, chunk{c, covered(c)})
				}
			}
		}
		// Least-covered first, so the clause the model already rewrote is reached
		// last and usually never is. A stable sort leaves the untouched clauses —
		// nearly all of them, scoring 0 — in the selector's relevance order, so
		// this costs nothing but the redundancy it removes. Ranking beats a
		// skip-threshold: no ratio separates "already said" from "shares two
		// tokens with what was said".
		sort.SliceStable(chunks, func(a, b int) bool { return chunks[a].cov < chunks[b].cov })

		add := func(c string) {
			s := dotTail.ReplaceAllString(upperFirst(c), "")
			if desc == "" {
				desc = s + "."
			} else {
				desc = dotTail.ReplaceAllString(desc, "") + ". " + s + "."
			}
		}
		var spare []string
		for _, c := range chunks {
			if wordCount(desc) >= minWords {
				break
			}
			if c.cov > 0.6 {
				continue // a near-verbatim repeat is worse than being short
			}
			// Respect the ceiling while there is any other clause to try.
			// Overshooting is not free: a 75-word blurb costs four lines, and the
			// density ladder pays for it by dropping a whole project — which is
			// exactly the floor this defends.
			if wordCount(desc)+wordCount(c.text) > maxWords {
				spare = append(spare, c.text)
				continue
			}
			add(c.text)
		}
		// Nothing short enough was left. A project below the floor reads as an
		// afterthought, so the floor outranks the ceiling on the last clause.
		if wordCount(desc) < minWords && len(spare) > 0 {
			add(spare[0])
		}
		out[i].Description = desc
	}
	return out
}
