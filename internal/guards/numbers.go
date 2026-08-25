package guards

import (
	"regexp"
	"strings"
)

// numericRe matches the figures worth attributing to the CV. Single digits are
// too noisy to track, so they are filtered out after the match.
//
// The original guards it with (?<![A-Za-z]): "L40 Engineer" (an internal job
// level) and "v4"/"H100"-style identifiers are names, not claims, and flagging
// the 40 in one made a job title read as an invented figure. Separators still
// count, so "AES-256" and "sub-500ms" are unaffected. RE2 has no lookbehind, so
// the guard is applied by index.
var (
	numericRe = regexp.MustCompile(`\d[\d,.]*\+?%?`)
	alphaByte = regexp.MustCompile(`[A-Za-z]`)
	trailPunc = regexp.MustCompile(`[.,]$`)
)

// NumbersIn is the set of figures a piece of text claims.
func NumbersIn(s string) map[string]bool {
	out := map[string]bool{}
	for _, loc := range numericRe.FindAllStringIndex(s, -1) {
		if loc[0] > 0 && alphaByte.MatchString(s[loc[0]-1:loc[0]]) {
			continue
		}
		n := trailPunc.ReplaceAllString(s[loc[0]:loc[1]], "")
		if len(n) > 1 {
			out[n] = true
		}
	}
	return out
}

var stopToks = map[string]bool{
	"and": true, "the": true, "for": true, "with": true, "system": true,
	"management": true, "a": true, "an": true, "of": true, "in": true,
}

var tokRe = regexp.MustCompile(`[a-z0-9+#.:-]{3,}`)

// Toks is the overlap vocabulary the reverter and the padder share. Its
// three-character floor is why "C#" and "CI/CD" never appear in it.
func Toks(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range tokRe.FindAllString(strings.ToLower(s), -1) {
		if !stopToks[w] {
			out[w] = true
		}
	}
	return out
}
