package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
)

// gateSkills measures the skills block in the posting's own spelling, over every
// cached JD. Not an oracle diff — the Node implementation had no aliases.
//
// A skill is asked when the posting names it in any spelling cv.md or
// config/skills.yml accepts, and carried when the block prints a spelling the
// posting used. The Node harness's skill_coverage cannot see this: it only knows
// cv.md's own strings, so "RESTful" against a CV that says "REST APIs" is not a
// miss there, it is absent from the denominator.
//
//	go run ./cmd/gate skills <home> [--sweep]
//
// --sweep scores a grid of block sizes instead (categories × items per category
// × the per-category floor), all with aliases on.
func gateSkills(home string, sweep bool) int {
	cfg, err := config.Load(paths.Layout{Home: home, Code: home})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	files, _ := filepath.Glob(filepath.Join(home, "batch", "jds", "*.txt"))
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no cached JDs under", filepath.Join(home, "batch", "jds"))
		return 2
	}

	var items []string
	seen := map[string]bool{}
	for _, c := range phase3.ParseSkillCategories(cfg.CVRaw) {
		for _, i := range c.Items {
			if !seen[i] {
				seen[i] = true
				items = append(items, i)
			}
		}
	}
	// A key that matches no item is a typo or a renamed skill, and the
	// selector ignores it without a word. This is where that gets said.
	known := map[string]bool{}
	for _, i := range items {
		known[strings.ToLower(i)] = true
	}
	for k := range cfg.Aliases {
		if !known[k] {
			fmt.Printf("  unmatched alias key %q — no cv.md skill item is spelled that way\n", k)
		}
	}
	if sweep {
		return sweepSkills(cfg, files, items)
	}

	// Scored twice per posting, paired: the block as selected without the
	// aliases, and with them. The definition of asked is the same for both.
	type tally struct{ asked, carried, aliasAsked, aliasCarried int }
	var before, after tally
	misses := map[string]int{}
	pageOf := func(jd string, aliases map[string][]string) string {
		var page []string
		for _, p := range phase3.SelectSkills(cfg.CVRaw, jd, phase3.SkillCats, phase3.SkillItems, phase3.SkillFloor, aliases) {
			page = append(page, p.Items)
		}
		return phase3.NormPhrase(strings.Join(page, ", "))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		jd := string(b)
		jdNorm := phase3.NormPhrase(jd)
		pages := []string{pageOf(jd, nil), pageOf(jd, cfg.Aliases)}

		for _, item := range items {
			var used []string
			for _, form := range phase3.SkillForms(item, cfg.Aliases) {
				if strings.Contains(jdNorm, phase3.NormPhrase(form)) {
					used = append(used, form)
				}
			}
			if len(used) == 0 {
				continue
			}
			isAlias := map[string]bool{}
			for _, a := range cfg.Aliases[strings.ToLower(item)] {
				isAlias[a] = true
			}
			byAlias := true
			for _, f := range used {
				byAlias = byAlias && isAlias[f]
			}
			for i, t := range []*tally{&before, &after} {
				hit := false
				for _, form := range used {
					if strings.Contains(pages[i], phase3.NormPhrase(form)) {
						hit = true
						break
					}
				}
				t.asked++
				if byAlias {
					t.aliasAsked++
				}
				if hit {
					t.carried++
					if byAlias {
						t.aliasCarried++
					}
				} else if t == &after {
					misses[fmt.Sprintf("%s as %q", item, used[0])]++
				}
			}
		}
	}

	pct := func(a, b int) string {
		if b == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
	}
	fmt.Printf("jds %d  aliases %d  skills asked %d\n", len(files), len(cfg.Aliases), after.asked)
	for _, r := range []struct {
		name string
		t    tally
	}{{"without aliases", before}, {"with aliases", after}} {
		fmt.Printf("  %-16s carried in the posting's spelling %d (%s); named only by an alias %d, carried %d (%s)\n",
			r.name, r.t.carried, pct(r.t.carried, r.t.asked), r.t.aliasAsked, r.t.aliasCarried, pct(r.t.aliasCarried, r.t.aliasAsked))
	}

	type miss struct {
		what string
		n    int
	}
	var ms []miss
	for w, n := range misses {
		ms = append(ms, miss{w, n})
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].n != ms[j].n {
			return ms[i].n > ms[j].n
		}
		return ms[i].what < ms[j].what
	})
	if len(ms) > 15 {
		ms = ms[:15]
	}
	for _, m := range ms {
		fmt.Printf("  still missed ×%-3d %s\n", m.n, m.what)
	}
	return 0
}

// sweepSkills scores block sizes against the cost the user cares about: named
// skills lost, items printed that the posting never asked for, and lines.
//
// Categories the posting named are never cut (batch/CLAUDE.md, "nothing may
// slice the skills block"), so a category cap is a target and not a bound;
// `over` counts the postings that name more categories than the cap allows.
func sweepSkills(cfg *config.Config, files []string, items []string) int {
	type jd struct {
		text string
		used map[string][]string // item → the spellings the posting used
	}
	var jds []jd
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		j := jd{text: string(b), used: map[string][]string{}}
		norm := phase3.NormPhrase(j.text)
		for _, item := range items {
			for _, form := range phase3.SkillForms(item, cfg.Aliases) {
				if strings.Contains(norm, phase3.NormPhrase(form)) {
					j.used[item] = append(j.used[item], form)
				}
			}
		}
		jds = append(jds, j)
	}

	// ponytail: lines are estimated, not rendered — one line per lineChars of
	// "Category: items". Good for comparing rows; check one real PDF before
	// trusting an absolute figure.
	const lineChars = 115
	fmt.Printf("%-10s %9s %8s %10s %9s %10s %8s\n",
		"cats/items/min", "named kept", "cats", "over cap", "items", "unasked", "lines")
	for _, g := range [][3]int{
		{6, 12, 3},                                 // the limits before strict naming
		{5, 7, 5}, {5, 7, 3}, {5, 6, 5}, {5, 5, 5}, // 5/7/3 ships
		{4, 7, 5}, {4, 7, 3}, {4, 5, 5},
		{3, 7, 5}, {3, 7, 3}, {3, 5, 5},
	} {
		var asked, kept, cats, maxCats, over, nItems, unasked, lines int
		for _, j := range jds {
			block := phase3.SelectSkills(cfg.CVRaw, j.text, g[0], g[1], g[2], cfg.Aliases)
			cats += len(block)
			if len(block) > maxCats {
				maxCats = len(block)
			}
			if len(block) > g[0] {
				over++
			}
			var page []string
			for _, c := range block {
				page = append(page, c.Items)
				lines += (len(c.Category) + 2 + len(c.Items) + lineChars - 1) / lineChars
				for _, it := range strings.Split(c.Items, ", ") {
					nItems++
					_, hit := j.used[it]
					for _, forms := range j.used {
						for _, f := range forms {
							if strings.EqualFold(f, it) {
								hit = true
							}
						}
					}
					if !hit {
						unasked++
					}
				}
			}
			pageNorm := phase3.NormPhrase(strings.Join(page, ", "))
			for _, forms := range j.used {
				asked++
				for _, f := range forms {
					if strings.Contains(pageNorm, phase3.NormPhrase(f)) {
						kept++
						break
					}
				}
			}
		}
		n := float64(len(jds))
		fmt.Printf("%2d/%2d/%d    %6.1f%%   %4.1f≤%-2d %5d (%2.0f%%) %7.1f %6.1f (%2.0f%%) %6.1f\n",
			g[0], g[1], g[2], 100*float64(kept)/float64(asked),
			float64(cats)/n, maxCats, over, 100*float64(over)/n,
			float64(nItems)/n, float64(unasked)/n, 100*float64(unasked)/float64(nItems),
			float64(lines)/n)
	}
	return 0
}
