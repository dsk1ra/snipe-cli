package fill

import (
	"regexp"
	"strings"
)

var (
	skillNormRe  = regexp.MustCompile(`[^a-z0-9+#.]+`)
	parenTailRe  = regexp.MustCompile(`\(.*$`)
	headSplitRe  = regexp.MustCompile(`[\s/-]+`)
	insideParens = regexp.MustCompile(`^[^(]*\)`)
)

func skillNorm(s string) string {
	return " " + strings.TrimSpace(skillNormRe.ReplaceAllString(strings.ToLower(s), " ")) + " "
}

// splitItems splits a skills row on commas that are not inside parentheses.
//
// The original is `/,(?![^(]*\))/` — a comma is a separator unless a `)` can be
// reached without crossing a `(`. RE2 has no lookahead, so the same predicate is
// applied by index, which also keeps the quirk: an unbalanced `)` later in the
// row suppresses every comma before it, in both implementations.
func splitItems(s string) []string {
	var out []string
	last := 0
	for i, c := range s {
		if c != ',' || insideParens.MatchString(s[i+1:]) {
			continue
		}
		out = append(out, s[last:i])
		last = i + 1
	}
	return append(out, s[last:])
}

// GroundSkillItems drops curated skill items that do not appear in cv.md. The
// model may narrow the CV's items; it may never add to them.
//
// There is no length exemption. A single-character head used to skip the check
// entirely, so an invented "R" or "D" shipped as grounded — while a real
// single-letter language still passes on its own token, since "C/C++"
// normalises to a bare " c ". An empty head is junk ("(", "-"), not an item.
func GroundSkillItems(curated, cvRowItems, cvText string) string {
	cvNorm := skillNorm(cvText)
	var kept []string
	for _, raw := range splitItems(curated) {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		head := strings.ToLower(headSplitRe.Split(strings.TrimSpace(parenTailRe.ReplaceAllString(item, "")), -1)[0])
		if head != "" && strings.Contains(cvNorm, " "+head+" ") {
			kept = append(kept, item)
		}
	}
	if len(kept) == 0 {
		return cvRowItems
	}
	return strings.Join(kept, ", ")
}
