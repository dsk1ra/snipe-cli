package guards

import (
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fill"
)

var hashPrefix = regexp.MustCompile(`^###\s+`)
var boldLead = regexp.MustCompile(`^\*\*(.+?)\*\*`)

// entryCompany is the employer an Experience entry belongs to: the bold name on
// the entry's second line, falling back to its title.
func entryCompany(e *cvselect.Entry) string {
	title := ""
	if len(e.Head) > 0 {
		title = strings.TrimSpace(hashPrefix.ReplaceAllString(e.Head[0], ""))
	}
	if len(e.Head) > 1 {
		if m := boldLead.FindStringSubmatch(e.Head[1]); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return title
}

// section returns one named section's entries, or nil.
func section(cvText, name string) []*cvselect.Entry {
	for _, s := range cvselect.ParseSections(cvText) {
		if s.Name == name {
			return cvselect.ParseEntries(s.Lines).Entries
		}
	}
	return nil
}

// entryTitle is a Projects entry's name, as the model is asked to spell it.
func entryTitle(e *cvselect.Entry) string {
	if len(e.Head) == 0 {
		return ""
	}
	return strings.TrimSpace(hashPrefix.ReplaceAllString(e.Head[0], ""))
}

func entryText(e *cvselect.Entry) string {
	return strings.Join(e.Head, " ") + " " + strings.Join(e.Bullets, " ")
}

// RevertUnsupportedBullets replaces every failing bullet with the cv.md line it
// most resembles, rather than dropping it, so the role keeps its depth.
//
// isUnsupported is given the bullet, the CV bullet it was most likely rewritten
// from, and the whole text of that employer's entry. The entry text includes the
// head: the dates and tech-stack line carry figures a bullet may legitimately
// cite.
func RevertUnsupportedBullets(items []fill.ContentExp, cvText string,
	isUnsupported func(bullet, src, entryText string) bool) []fill.ContentExp {

	byCompany := map[string]*cvselect.Entry{}
	for _, e := range section(cvText, "Experience") {
		byCompany[strings.ToLower(entryCompany(e))] = e
	}

	out := make([]fill.ContentExp, len(items))
	for i, entry := range items {
		out[i] = entry
		src := byCompany[strings.ToLower(strings.TrimSpace(entry.Company))]
		et := ""
		if src != nil {
			et = entryText(src)
		}
		// Always a list, never null: the Node rebuilds the entry unconditionally,
		// and an entry that arrived with no `bullets` key leaves with an empty
		// array. The difference is visible the moment this is serialised back.
		bullets := []string{}
		seen := map[string]bool{}
		for _, b := range entry.Bullets {
			if src == nil || len(src.Bullets) == 0 {
				// Nothing to revert to — but the dedupe still applies.
				if !seen[b] {
					seen[b] = true
					bullets = append(bullets, b)
				}
				continue
			}
			// Prefer the CV bullet this rewrite came from; token overlap picks it
			// out.
			bt := Toks(b)
			best, bestN := src.Bullets[0], -1
			for _, cb := range src.Bullets {
				n := 0
				for t := range Toks(cb) {
					if bt[t] {
						n++
					}
				}
				if n > bestN {
					bestN, best = n, cb
				}
			}
			kept := b
			if isUnsupported(b, best, et) {
				kept = best
			}
			// Reverting can collide two bullets onto the same CV line.
			if !seen[kept] {
				seen[kept] = true
				bullets = append(bullets, kept)
			}
		}
		out[i].Bullets = bullets
	}
	return out
}

// VerifyBulletProducts reverts every experience bullet naming a product cv.md
// never mentions.
//
// Bullets are reverted rather than clause-stripped because each has a known
// source line in cv.md — the untailored truth is right there and it fills the
// slot. Summaries and project blurbs have no single source line, so those get
// StripFabricatedProducts instead.
func VerifyBulletProducts(items []fill.ContentExp, cvText string) []fill.ContentExp {
	return RevertUnsupportedBullets(items, cvText, func(b, _, _ string) bool {
		return len(ProductFab(b, cvText)) > 0
	})
}

// VerifyBulletNumbers reverts a bullet claiming a figure its own employer's
// entry does not state.
//
// Scoped to the entry, not the whole CV. A CV-global allow-set answers "does
// this figure exist somewhere in the document", which is the wrong question: it
// lets one employer's bullet claim another project's sub-500ms load times,
// because the number is real — just not theirs.
func VerifyBulletNumbers(items []fill.ContentExp, cvText string) []fill.ContentExp {
	return RevertUnsupportedBullets(items, cvText, func(b, _, et string) bool {
		allowed := NumbersIn(et)
		for n := range NumbersIn(b) {
			if !allowed[n] {
				return true
			}
		}
		return false
	})
}
