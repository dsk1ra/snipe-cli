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
//	go run ./cmd/gate skills <home>
func gateSkills(home string) int {
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

	// Scored twice per posting, paired: the block as selected without the
	// aliases, and with them. The definition of asked is the same for both.
	type tally struct{ asked, carried, aliasAsked, aliasCarried int }
	var before, after tally
	misses := map[string]int{}
	pageOf := func(jd string, aliases map[string][]string) string {
		var page []string
		for _, p := range phase3.SelectSkills(cfg.CVRaw, jd, 6, 12, 3, aliases) {
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
			for _, form := range append([]string{item}, cfg.Aliases[strings.ToLower(item)]...) {
				if strings.Contains(jdNorm, phase3.NormPhrase(form)) {
					used = append(used, form)
				}
			}
			if len(used) == 0 {
				continue
			}
			byAlias := used[0] != item
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
