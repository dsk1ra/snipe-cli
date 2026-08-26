// Package latex escapes text for the CV template and bolds its metrics.
//
// The whole of this package's difficulty is order: ATS normalisation runs
// before escaping, and metric matching runs before both. Getting either the
// wrong way round produces a document that compiles and reads wrong.
package latex

import (
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/text"
)

// escapes maps the characters TeX eats.
//
// The tilde is deliberately not \textasciitilde. cv.md's tildes all mean
// "approximately" (~200 TB, ~0.4 ms); the faithful glyph typesets as a raised
// squiggle that reads as a macron at this size, while $\sim$ is what the text
// means.
var escapes = strings.NewReplacer(
	`\`, `\textbackslash{}`,
	`{`, `\{`, `}`, `\}`,
	`$`, `\$`, `&`, `\&`, `#`, `\#`, `_`, `\_`, `%`, `\%`,
	`~`, `$\sim$`, `^`, `\textasciicircum{}`,
	`<`, `\textless{}`, `>`, `\textgreater{}`, `|`, `\textbar{}`,
)

// Escape makes text safe for LaTeX.
//
// ATS normalisation runs BEFORE escaping, not on the finished .tex. "•" maps to
// " | ", and a pipe introduced after the escaper has run reaches LaTeX bare —
// where T1 typesets it as an em dash.
func Escape(s string) string {
	return escapes.Replace(text.SanitizeATS(s))
}

// EscapeURL escapes only what TeX still eats inside \href's first argument.
// hyperref reads the target verbatim, so everything else in a URL — including ~
// and & — is safe there and must not be touched.
var urlEscape = regexp.MustCompile(`([%#\\])`)

func EscapeURL(s string) string { return urlEscape.ReplaceAllString(s, `\$1`) }

// metricRe matches measurable metrics: counts, percentages, latencies,
// currency, units and durations. It deliberately leaves bare years (2025),
// version numbers (2.0) and "AES-256" alone, since they carry no separator,
// sign or unit.
//
// The `\d[\d,]*(?:\.\d+)?[kKMB]\+?` alternative needs a negative lookbehind in
// the original, to keep the "2B" inside "B2B" from matching. RE2 has none, so
// the guard is applied by index in Bold.
var metricRe = regexp.MustCompile(strings.Join([]string{
	`[£$€]\s?\d[\d,]*(?:\.\d+)?\+?[kKmMbB]?`,           // £3,500, $120k
	`sub-\d[\d,]*\s?ms`,                                // sub-500ms
	`\d[\d,]*(?:\.\d+)?%\+?`,                           // 80%, 95%+
	`\d[\d,]*(?:\.\d+)?\s?(?:ms|x|×|GB|MB|TB|bn)\b\+?`, // 250ms, 10x
	`\d[\d,]*(?:\.\d+)?[kKMB]\+?`,                      // 3M+, 11K — guarded below
	`\d[\d,]*\+`,                                       // 100+, 5+
	`\d{1,3}(?:,\d{3})+`,                               // 50,000
	`\d+\s?(?:weeks?|days?|hours?|minutes?|months?|years?)\b`,
}, "|"))

var (
	alphaRe      = regexp.MustCompile(`[A-Za-z]`)
	trailAlphaRe = regexp.MustCompile(`^[a-zA-Z]`)
	kmbTailRe    = regexp.MustCompile(`^\d[\d,]*(?:\.\d+)?[kKMB]\+?$`)
)

// Bold escapes for LaTeX and wraps the metrics in \textbf, in one pass over the
// RAW string.
//
// The HTML template could run its metric regex over already-escaped text,
// because escaping HTML leaves digits, %, £ and $ alone. LaTeX escaping does
// not: "95%" becomes "95\%" and "$120k" becomes "\$120k", so a regex tuned to
// the source text stops matching the moment it is escaped. Matching first and
// escaping the pieces keeps the pattern reading the text it was calibrated on.
func Bold(raw string) string {
	var out strings.Builder
	last := 0
	for _, loc := range metricRe.FindAllStringIndex(raw, -1) {
		m := raw[loc[0]:loc[1]]
		// The lookaround the original expresses as (?<![A-Za-z]) … (?![a-zA-Z]),
		// applied here because RE2 cannot: it keeps the "2B" in "B2B" and the
		// "3M" in "3Mb" from reading as metrics.
		if kmbTailRe.MatchString(m) {
			before := raw[:loc[0]]
			after := raw[loc[1]:]
			if before != "" && alphaRe.MatchString(before[len(before)-1:]) {
				continue
			}
			if after != "" && trailAlphaRe.MatchString(after) {
				continue
			}
		}
		out.WriteString(Escape(raw[last:loc[0]]))
		out.WriteString(`\textbf{`)
		out.WriteString(Escape(m))
		out.WriteString(`}`)
		last = loc[1]
	}
	out.WriteString(Escape(raw[last:]))
	return out.String()
}
