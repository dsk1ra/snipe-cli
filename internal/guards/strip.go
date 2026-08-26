package guards

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/dsk1ra/snipe-cli/internal/text"
)

var (
	clauseSplit  = regexp.MustCompile(`,\s*`)
	trailingAnd  = regexp.MustCompile(`(?i)\s+and\s*$`)
	trailingComa = regexp.MustCompile(`,\s*$`)
	endsSentence = regexp.MustCompile(`[.!?]$`)
)

// StripUnsupportedClauses drops the clauses of text that fail isBad, sentence by
// sentence.
//
// The repair for surfaces with no single source line to revert to — a summary or
// a project blurb, both synthesised from several bullets. A sentence is kept
// whole if it passes; otherwise its clean clauses are rejoined, and the rebuild
// is accepted only if it actually cleared the problem, since a claim sitting in
// a sentence's only clause survives the join.
//
// ponytail: comma-splitting treats a comma-separated adjective list as two
// clauses, so dropping one can orphan the other ("building secure," when the
// clause carrying the noun goes). Deflating an inflated figure instead of
// cutting its clause removed every observed instance — one summary in twelve
// reaches this path at all, and none of the current corpus orphans. Upgrade path
// if that changes: drop the whole sentence when the survivor ends on a bare
// adjective, rather than splitting smarter.
func StripUnsupportedClauses(s string, isBad func(string) bool) string {
	if s == "" || !isBad(s) {
		return s
	}
	var kept []string
	for _, sentence := range text.SplitSentences(s) {
		if !isBad(sentence) {
			kept = append(kept, sentence)
			continue
		}
		clauses := clauseSplit.Split(sentence, -1)
		var clean []string
		for _, c := range clauses {
			if !isBad(c) {
				clean = append(clean, c)
			}
		}
		if len(clean) == 0 || len(clean) >= len(clauses) {
			continue
		}
		rebuilt := strings.Join(clean, ", ")
		rebuilt = trailingComa.ReplaceAllString(trailingAnd.ReplaceAllString(rebuilt, ""), "")
		rebuilt = strings.TrimSpace(rebuilt)
		// Dropping the leading clause promotes a mid-sentence one to sentence
		// start, which shipped "…across Python, React, and Next.js. strong
		// fundamentals,…".
		rebuilt = upperFirst(rebuilt)
		if rebuilt == "" || isBad(rebuilt) {
			continue
		}
		if !endsSentence.MatchString(rebuilt) {
			rebuilt += "."
		}
		kept = append(kept, rebuilt)
	}
	return strings.TrimSpace(spaceRun.ReplaceAllString(strings.Join(kept, " "), " "))
}

// upperFirst mirrors JS `s.charAt(0).toUpperCase() + s.slice(1)`, which operates
// on the first UTF-16 code unit. Every string reaching it here starts with an
// ASCII letter, but the rune-aware form is the one that cannot corrupt a
// multi-byte opener.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// StripFabricatedProducts drops the clauses that name a product cv.md never
// mentions.
//
// Sentence-level, then comma-clause-level: "Built services in Rust, Kotlin and
// Go" should lose Kotlin, not the whole sentence. It falls back to deleting the
// sentence only when the clause surgery cannot clear it, because shipping a
// shorter true blurb beats shipping a longer false one.
func StripFabricatedProducts(s, cvText string) string {
	return StripUnsupportedClauses(s, func(t string) bool { return len(ProductFab(t, cvText)) > 0 })
}
