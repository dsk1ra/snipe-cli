package phase3

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/guards"
)

// VerbatimContent renders the selection as cv.md already words it — the shipped
// writer, and the whole of it. cv-writers.mjs defaults to `--writer verbatim`
// and the 7B tailor arm was never ported, so there is no generation call in
// Phase 3 at all.
//
// It parses the SELECTED cv rather than re-deriving entries from the full one:
// fill.ParseCV already yields companies and project names in the exact shapes
// fill.Render matches against, so this is a change of shape and not a second
// parser that could drift from the first.
//
// Grounded by construction — every string it emits came out of the CV — which
// is why the fabrication guards are not on this path. They exist to catch what
// a model invents, and nothing here invents anything.
func VerbatimContent(selectedCV, cvText, jdText string, projectBullets int, aliases map[string][]string) fill.Content {
	sel := fill.ParseCV(selectedCV)

	exp := make([]fill.ContentExp, 0, len(sel.Exp))
	for _, e := range sel.Exp {
		exp = append(exp, fill.ContentExp{Company: e.Company, Bullets: e.Bullets})
	}

	// An empty description makes PadProjectDescriptions build the whole blurb
	// from the project's own CV clauses, least-covered first — which is exactly
	// the verbatim rendering wanted here, and is already the repair path for a
	// description a model truncated. Reusing it beats a second clause-joiner
	// that would drift from it.
	picks := make([]fill.ProjectPick, 0, len(sel.Projects))
	for _, p := range sel.Projects {
		picks = append(picks, fill.ProjectPick{Name: p.Name})
	}
	picks = guards.PadProjectDescriptions(picks, selectedCV, 35, 55)
	// Bullets alongside the blurb, not instead of it: the template picks the
	// list when it is there and falls back to the paragraph otherwise. This is
	// the field that carries the differentiators — the paragraph form was
	// discarding all but the first clause or two of them.
	for i := range picks {
		if projectBullets > 0 && i < len(sel.Projects) {
			b := sel.Projects[i].Bullets
			if len(b) > projectBullets {
				b = b[:projectBullets]
			}
			picks[i].Bullets = b
		}
	}

	c := fill.Content{Experience: exp}
	c.Projects, _ = json.Marshal(picks)
	c.Skills, _ = json.Marshal(SelectSkills(cvText, jdText, SkillCats, SkillItems, SkillFloor, aliases))
	return c
}

var modulesRe = regexp.MustCompile(`(?i)\*\*Key Modules:\*\*\s*([^\n]+)`)

// EducationModules ranks the CV's "Key Modules:" list by token overlap with the
// posting and returns the top n names. Ties keep CV order; zero overlap falls
// back to the CV's first n, because an arbitrary five is still better than an
// empty line where the module list goes.
func EducationModules(cvText, jdText string, n int) []string {
	m := modulesRe.FindStringSubmatch(cvText)
	if m == nil {
		return nil
	}
	var mods []string
	for _, s := range strings.Split(m[1], ",") {
		if s = strings.TrimSpace(s); s != "" {
			mods = append(mods, s)
		}
	}
	if len(mods) == 0 {
		return nil
	}
	jdTok := map[string]bool{}
	for _, t := range moduleTokens(jdText) {
		jdTok[t] = true
	}
	idx := make([]int, len(mods))
	score := make([]int, len(mods))
	for i, mod := range mods {
		idx[i] = i
		seen := map[string]bool{}
		for _, t := range moduleTokens(mod) {
			if !seen[t] && jdTok[t] {
				seen[t] = true
				score[i]++
			}
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return score[idx[a]] > score[idx[b]] })
	if n > len(idx) {
		n = len(idx)
	}
	top := idx[:n]
	best := 0
	for _, i := range top {
		best += score[i]
	}
	if best == 0 {
		return mods[:n]
	}
	out := make([]string, 0, n)
	for _, i := range top {
		out = append(out, mods[i])
	}
	return out
}

// moduleTokens is local-pdf-offer.mjs's own tokenizer, which is NOT the writer's.
//
// The two differ in exactly one place: the writer trims leading and trailing
// dots and re-applies the three-character floor, so a posting ending a sentence
// on "Kafka." yields "kafka". This one does not, so it yields "kafka.". That is
// worse — and it is what ranks the modules today, so it is what is ported.
// Sharing the writer's tokenizer here silently re-ranks the module list
// (a posting saying "secure software development." stopped matching the module
// that names it), which is a quality change wearing a refactor's clothes.
// Change it deliberately, with a measurement, or not at all.
func moduleTokens(s string) []string {
	var out []string
	for _, w := range writerTokenRe.FindAllString(strings.ToLower(s), -1) {
		if !writerStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}
