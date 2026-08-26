package cvselect

import (
	"sort"
	"strings"
)

// Selected is the outcome of a selection: the trimmed CV text, plus what the
// funnel decided, for a caller that wants to inspect rather than render.
type Selected struct {
	CV       string
	ExpKept  map[string]int // entry title → bullets kept
	ProjKept map[string]int
}

// Scorer supplies a relevance score for one bullet against the posting. It is a
// function rather than an embedder so the allocation can be exercised — and
// gated — without a model in the loop.
type Scorer func(bullet string) float64

// Select rations the CV against the page.
//
// The order is deliberate and each step depends on the last: which projects make
// the cut is settled before any budget is spent (spending on a project that gets
// dropped wastes it), then the budget is allocated across everything the page
// carries, then ordering is applied. Relevance decides what survives, chronology
// decides how it reads, and the two must not be the same sort.
//
// Returns the CV unchanged when there is nothing to rank against, which is also
// what a CV with no Experience and no Projects section gets. Those two cases
// being indistinguishable is why ParseSections has to accept both heading
// spellings rather than guess.
func Select(cv string, score Scorer, opt Options) Selected {
	sections := ParseSections(cv)
	var exp, proj *Section
	for i := range sections {
		switch sections[i].Name {
		case "Experience":
			exp = &sections[i]
		case "Projects":
			proj = &sections[i]
		}
	}
	if exp == nil && proj == nil {
		return Selected{CV: cv}
	}

	var expParsed, projParsed *Parsed
	if exp != nil {
		p := ParseEntries(exp.Lines)
		expParsed = &p
	}
	if proj != nil {
		p := ParseEntries(proj.Lines)
		projParsed = &p
	}

	for _, p := range []*Parsed{expParsed, projParsed} {
		if p == nil {
			continue
		}
		for _, e := range p.Entries {
			e.Scored = e.Scored[:0]
			for _, b := range e.Bullets {
				e.Scored = append(e.Scored, ScoredBullet{Text: b, Score: score(b)})
			}
		}
	}

	// Which projects make the cut, before any budget is spent.
	if projParsed != nil {
		for _, e := range projParsed.Entries {
			Trim(e, opt.MaxBulletsPerProject, false)
		}
		sort.SliceStable(projParsed.Entries, func(i, j int) bool {
			return projParsed.Entries[i].Score > projParsed.Entries[j].Score
		})
		if len(opt.PinnedProjects) > 0 {
			projParsed.Entries = applyPins(projParsed.Entries, opt.PinnedProjects)
		}
		if len(projParsed.Entries) > opt.MaxProjects {
			projParsed.Entries = projParsed.Entries[:opt.MaxProjects]
		}
	}

	if opt.LineBudget > 0 {
		caps := map[*Entry]int{}
		expSet := map[*Entry]bool{}
		var all []*Entry
		if expParsed != nil {
			for _, e := range expParsed.Entries {
				caps[e] = opt.MaxBulletsPerRole
				expSet[e] = true
				all = append(all, e)
			}
		}
		if projParsed != nil {
			for _, e := range projParsed.Entries {
				caps[e] = opt.MaxBulletsPerProject
				all = append(all, e)
			}
		}
		for e, k := range AllocateLines(all, opt.LineBudget, caps, expSet, opt.MinTopExpBullets, opt.ProjMaxLines) {
			Trim(e, k, true)
		}
	} else {
		if expParsed != nil {
			for _, e := range expParsed.Entries {
				Trim(e, opt.MaxBulletsPerRole, false)
			}
		}
		if projParsed != nil {
			for e, k := range Allocate(projParsed.Entries, opt.ProjectBulletBudget, opt.MaxBulletsPerProject) {
				Trim(e, k, false)
			}
		}
	}

	out := Selected{ExpKept: map[string]int{}, ProjKept: map[string]int{}}
	// UK CV convention: reverse-chronological, never reordered by relevance.
	if expParsed != nil {
		sort.SliceStable(expParsed.Entries, func(i, j int) bool {
			return EntryEndDate(expParsed.Entries[i]) > EntryEndDate(expParsed.Entries[j])
		})
		for _, e := range expParsed.Entries {
			out.ExpKept[entryTitle(e)] = len(e.Bullets)
		}
		exp.Lines = Render(*expParsed)
	}
	if projParsed != nil {
		sort.SliceStable(projParsed.Entries, func(i, j int) bool {
			return EntryEndDate(projParsed.Entries[i]) > EntryEndDate(projParsed.Entries[j])
		})
		for _, e := range projParsed.Entries {
			out.ProjKept[entryTitle(e)] = len(e.Bullets)
		}
		proj.Lines = Render(*projParsed)
	}

	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(s.Lines, "\n"))
	}
	out.CV = blankRuns.ReplaceAllString(b.String(), "\n\n")
	return out
}

// applyPins moves pinned projects to the front, keeping relevance order inside
// each group. Pinning more than MaxProjects still cannot overflow the page —
// the truncation afterwards is the same one, applied to a reordered list.
func applyPins(entries []*Entry, pins []string) []*Entry {
	isPinned := func(e *Entry) bool {
		t := strings.ToLower(entryTitle(e))
		for _, p := range pins {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" && strings.Contains(t, p) {
				return true
			}
		}
		return false
	}
	var pinned, rest []*Entry
	for _, e := range entries {
		if isPinned(e) {
			pinned = append(pinned, e)
		} else {
			rest = append(rest, e)
		}
	}
	return append(pinned, rest...)
}

func entryTitle(e *Entry) string {
	if len(e.Head) == 0 {
		return ""
	}
	t := entryRe.ReplaceAllString(e.Head[0], "")
	t = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(t), "**"), "**")
	return strings.TrimSpace(t)
}
