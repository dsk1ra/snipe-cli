package guards

import (
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/fill"
)

var parenGroup = regexp.MustCompile(`\(([^)]*)\)`)

// FilterSkillItems drops the skill items cv.md does not support — a CV that
// names AWS but never SQS must not claim SQS.
//
// Item-level, because a skills row is already a comma-separated list: dropping
// the offending item is the natural repair and keeps the true ones. A row left
// empty is removed rather than shipped as a bare heading. Parenthesised asides
// are pruned in place, so "AWS (Lambda, SQS)" becomes "AWS (Lambda)" — inside
// the parentheses first, so one bad sub-item does not cost the whole entry.
func FilterSkillItems(rows []fill.SkillRow, cvText string) []fill.SkillRow {
	clean := func(item string) string {
		withAside := parenGroup.ReplaceAllStringFunc(item, func(whole string) string {
			inner := whole[1 : len(whole)-1]
			var kept []string
			for _, x := range strings.Split(inner, ",") {
				x = strings.TrimSpace(x)
				if x != "" && len(ProductFab(x, cvText)) == 0 {
					kept = append(kept, x)
				}
			}
			if len(kept) == 0 {
				return ""
			}
			return "(" + strings.Join(kept, ", ") + ")"
		})
		withAside = strings.TrimSpace(spaceRun.ReplaceAllString(withAside, " "))
		if len(ProductFab(withAside, cvText)) > 0 {
			return ""
		}
		return withAside
	}

	out := []fill.SkillRow{}
	for _, r := range rows {
		var items []string
		for _, x := range strings.Split(r.Items, ",") {
			if c := clean(strings.TrimSpace(x)); c != "" {
				items = append(items, c)
			}
		}
		r.Items = strings.Join(items, ", ")
		if r.Items != "" {
			out = append(out, r)
		}
	}
	return out
}
