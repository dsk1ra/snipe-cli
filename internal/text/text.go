// Package text is the shared CV/JD cleaning and the deterministic salary
// parsing. Single source of truth for all three phases — the model never
// guesses compensation.
package text

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

var (
	dataImage   = regexp.MustCompile(`!\[\]\(data:image/[^)]*\)`)
	trailingWS  = regexp.MustCompile(`(?m)[ \t]+$`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
	scriptBlock = regexp.MustCompile(`(?is)<script.*?</script>`)
	styleBlock  = regexp.MustCompile(`(?is)<style.*?</style>`)
	blockClose  = regexp.MustCompile(`(?i)</(?:p|div|li|ul|ol|h\d|br|tr)>`)
	anyTag      = regexp.MustCompile(`<[^>]+>`)
	looksHTML   = regexp.MustCompile(`(?i)</(?:div|p|li|span|ul|h\d)>`)
	multiSpace  = regexp.MustCompile(`[ \t]{2,}`)
)

// CleanCV strips embedded base64 images, trailing whitespace and blank runs.
// They burn context tokens for zero evaluation value.
func CleanCV(md string) string {
	s := dataImage.ReplaceAllString(md, "")
	s = trailingWS.ReplaceAllString(s, "")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var entities = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
)

// boilerplate marks a trailing section worth cutting: benefits, EEO, company
// blurb. Only honoured past the midpoint — a posting that opens with its
// benefits still has its requirements below.
var boilerplate = regexp.MustCompile(`(?i)\n[^\n]{0,60}\b(equal opportunity|equal employment|we are an equal|EEO|diversity, equity|how to apply|benefits & perks|perks & benefits|our benefits|what we offer)\b`)

// DefaultJDCap is the character budget a JD gets in a prompt.
const DefaultJDCap = 4000

// CleanJD trims boilerplate and hard-caps length so the requirements and
// responsibilities survive within the context budget. Idempotent on plain text.
func CleanJD(jd string, cap int) string {
	if strings.TrimSpace(jd) == "" {
		return "(no JD available)"
	}
	if cap <= 0 {
		cap = DefaultJDCap
	}
	t := strings.ReplaceAll(jd, "\r", "")
	// A cached JD from a pre-fetch may be raw HTML.
	if looksHTML.MatchString(t) {
		t = scriptBlock.ReplaceAllString(t, "")
		t = styleBlock.ReplaceAllString(t, "")
		t = blockClose.ReplaceAllString(t, "\n")
		t = anyTag.ReplaceAllString(t, " ")
		t = entities.Replace(t)
		t = multiSpace.ReplaceAllString(t, " ")
	}
	t = strings.TrimSpace(blankRuns.ReplaceAllString(t, "\n\n"))

	if loc := boilerplate.FindStringIndex(t); loc != nil && float64(loc[0]) > float64(len(t))*0.45 {
		t = strings.TrimSpace(t[:loc[0]])
	}
	// Truncate by UTF-16 code unit — see Clip. On the real corpus this is the
	// difference between 88/351 and 351/351.
	if clipped := Clip(t, cap); clipped != t {
		t = strings.TrimSpace(clipped) + "\n[...]"
	}
	return t
}

// ── Salary ───────────────────────────────────────────────────────────────────

// A figure outside this band is not an annual salary — it is a headcount, a
// year, a share price or a typo.
const (
	plausibleMin = 18_000
	plausibleMax = 500_000
)

const numPat = `\d{1,3}(?:,\d{3})+|\d+(?:\.\d+)?[kK]|\d{5,6}`

var (
	rangeRe = regexp.MustCompile(
		// "£40,000 - £55,000" / "£40k to £55k"
		`([£$€])\s?(` + numPat + `)\s?(?:-|–|—|to)\s?(?:[£$€]\s?)?(` + numPat + `)` +
			// Adjacent double-currency "£325,000 £485,000" — what an HTML pay-range
			// widget (<span>lo</span><span>hi</span>) flattens to.
			`|([£$€])\s?(` + numPat + `)\s{1,4}[£$€]\s?(` + numPat + `)` +
			`|\b(GBP|USD|EUR)\s?(` + numPat + `)\s?(?:-|–|—|to)\s?(` + numPat + `)`)
	singleRe = regexp.MustCompile(
		`([£$€])\s?(` + numPat + `)|\b(GBP|USD|EUR)\s?(` + numPat + `)|\b(` + numPat + `)\s?(GBP|USD|EUR)\b`)
	// Day, hour, week and month rates are rejected: only annual salary is scored.
	rateRe = regexp.MustCompile(`(?i)^\s*(?:per|a|an|/)\s*(?:day|hour|hr|week|month)|^\s*(?:daily|hourly|weekly|monthly|p/?[dh])`)
)

var curWord = map[string]string{"GBP": "£", "USD": "$", "EUR": "€"}

type Salary struct {
	Currency string `json:"currency"`
	Min      int    `json:"min"`
	Max      int    `json:"max"`
	Raw      string `json:"raw"`
}

func parseMoney(s string) (int, bool) {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	k := strings.HasSuffix(t, "k")
	if k {
		t = t[:len(t)-1]
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	if k {
		n *= 1000
	}
	return int(math.Round(n)), true
}

func annualContext(text string, end int) bool {
	tail := text[end:]
	if len(tail) > 12 {
		tail = tail[:12]
	}
	return !rateRe.MatchString(tail)
}

// ExtractSalary deterministically pulls an annual salary out of JD text. Ranges
// win over single figures. Returns nil when the posting states none — which is
// the common case and must not be filled in by a model.
func ExtractSalary(text string) *Salary {
	// A cached JD may hold raw HTML; strip tags so a range split across <span>s
	// is seen as two adjacent numbers.
	t := multiSpace.ReplaceAllString(anyTag.ReplaceAllString(text, " "), " ")

	firstOf := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}

	for _, m := range rangeRe.FindAllStringSubmatchIndex(t, -1) {
		g := group(t, m)
		cur := firstOf(g[1], g[4])
		if cur == "" {
			cur = curWord[g[7]]
		}
		if cur == "" {
			cur = "£"
		}
		lo, okLo := parseMoney(firstOf(g[2], g[5], g[8]))
		hi, okHi := parseMoney(firstOf(g[3], g[6], g[9]))
		if !okLo || !okHi || lo > hi || lo < plausibleMin || hi > plausibleMax {
			continue
		}
		if !annualContext(t, m[1]) {
			continue
		}
		return &Salary{Currency: cur, Min: lo, Max: hi, Raw: strings.TrimSpace(g[0])}
	}

	for _, m := range singleRe.FindAllStringSubmatchIndex(t, -1) {
		g := group(t, m)
		cur := g[1]
		if cur == "" {
			cur = firstOf(curWord[g[3]], curWord[g[6]])
		}
		if cur == "" {
			cur = "£"
		}
		n, ok := parseMoney(firstOf(g[2], g[4], g[5]))
		if !ok || n < plausibleMin || n > plausibleMax {
			continue
		}
		if !annualContext(t, m[1]) {
			continue
		}
		return &Salary{Currency: cur, Min: n, Max: n, Raw: strings.TrimSpace(g[0])}
	}
	return nil
}

// group turns FindAllStringSubmatchIndex output into the submatch strings, with
// "" for a group that did not participate.
func group(s string, idx []int) []string {
	out := make([]string, len(idx)/2)
	for i := range out {
		if idx[2*i] >= 0 {
			out[i] = s[idx[2*i]:idx[2*i+1]]
		}
	}
	return out
}

// ── Compensation targets ─────────────────────────────────────────────────────

type CompTargets struct {
	Floor      int
	TargetLow  int
	TargetHigh int
}

var (
	targetRangeRe = regexp.MustCompile(`target_range:\s*["']?([^"'\n]+)`)
	minimumRe     = regexp.MustCompile(`minimum:\s*["']?([^"'\n]+)`)
	numRe         = regexp.MustCompile(numPat)
)

// ParseCompTargets reads the comp targets out of config/profile.yml as text.
// Text, not YAML: the values are written as human ranges ("£35k–£55k"), so the
// numbers have to be pulled out either way.
func ParseCompTargets(configText string) *CompTargets {
	rangeStr := ""
	if m := targetRangeRe.FindStringSubmatch(configText); m != nil {
		rangeStr = m[1]
	}
	nums := numRe.FindAllString(rangeStr, -1)
	if len(nums) < 2 {
		return nil
	}
	lo, okLo := parseMoney(nums[0])
	hi, okHi := parseMoney(nums[1])
	if !okLo || !okHi {
		return nil
	}
	floor := lo
	if m := minimumRe.FindStringSubmatch(configText); m != nil {
		if f := numRe.FindString(m[1]); f != "" {
			if v, ok := parseMoney(f); ok {
				floor = v
			}
		}
	}
	return &CompTargets{Floor: floor, TargetLow: lo, TargetHigh: hi}
}

// CompScore is the comp dimension (1–5) from a posted salary against the
// candidate's targets, computed in code. It replaces a model-guessed
// comp_inferred, which was hallucination-prone.
//
// Returns ok=false when the posting states no salary — that is the signal to
// drop the comp weight entirely, not to score it 0.
func CompScore(s *Salary, t *CompTargets) (int, bool) {
	if s == nil {
		return 0, false
	}
	if t == nil {
		return 3, true // salary posted but no targets configured: neutral
	}
	mid := float64(s.Min+s.Max) / 2
	switch {
	case mid < float64(t.Floor):
		return 1, true
	case mid < float64(t.TargetLow):
		return 2, true
	case mid < float64(t.TargetLow+t.TargetHigh)/2:
		return 3, true
	case mid < float64(t.TargetHigh):
		return 4, true
	}
	return 5, true
}

// FormatSalary renders a salary the way the report prints it.
func FormatSalary(s *Salary) string {
	if s == nil {
		return "not stated"
	}
	if s.Min == s.Max {
		return fmt.Sprintf("%s%s", s.Currency, thousands(s.Min))
	}
	return fmt.Sprintf("%s%s–%s%s", s.Currency, thousands(s.Min), s.Currency, thousands(s.Max))
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// Clip truncates to n UTF-16 code units, the unit String.prototype.slice counts.
//
// Not bytes and not runes. Every prompt in this pipeline mirrors a JS
// `.slice(0, n)`, and the corpus is full of em dashes, £ and – — three bytes
// each in UTF-8, one code unit in UTF-16. Slicing bytes both cuts multi-byte
// runes in half and produces a shorter string than the Node implementation, so
// the model sees a different prompt and answers differently. That is invisible
// downstream: same scores, different prose.
//
// Use this anywhere a prompt or a report mirrors a JS slice. Ordinary Go string
// handling should keep using bytes or runes.
func Clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

// Fixed formats a number the way JavaScript's Number.prototype.toFixed does:
// rounding halves away from zero.
//
// Go's %f verb rounds halves to even. They agree everywhere except an exact
// tie, and a tie is not rare here — a coverage of 0.225 renders as 23% in Node
// and 22% under %.0f. That one character sits in the stage 3 prompt, and it
// changed the model's entire judgment on a real offer: same dimensions from
// stages 1 and 2, a different score out of stage 3.
//
// Use this anywhere a prompt or a report mirrors a JS toFixed. math.Round is
// already half-away-from-zero, so the fix is to round before formatting rather
// than to let the verb do it.
func Fixed(f float64, places int) string {
	p := math.Pow(10, float64(places))
	return strconv.FormatFloat(math.Round(f*p)/p, 'f', places, 64)
}

// ── ATS normalisation ────────────────────────────────────────────────────────

// atsReplacer converts the Unicode an ATS parser or a PDF text extractor
// mangles. Arrows and middle dots consume the whitespace around them, so the
// replacement does not double-space.
//
// The yen sign is deliberately left alone: ¥ is ambiguous between JPY and CNY,
// and spelling it out would assert one.
var atsReplacer = strings.NewReplacer(
	"—", "-", "–", "-",
	"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
	"‘", "'", "’", "'", "‚", "'", "‛", "'",
	"…", "...",
	// Zero-width and BOM, written as escapes: a literal BOM in Go source is a
	// compile error, and the rest are invisible in a diff.
	"\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "",
	"\u00a0", " ", // non-breaking space
	"€", "EUR ", "£", "GBP ",
)

var (
	rightArrowRe  = regexp.MustCompile(`\s*→\s*`)
	leftArrowRe   = regexp.MustCompile(`\s*←\s*`)
	vertArrowRe   = regexp.MustCompile(`\s*[↑↓]\s*`)
	middotRe      = regexp.MustCompile(`\s*·\s*`)
	bulletGlyphRe = regexp.MustCompile(`\s*•\s*`)
)

// SanitizeATS normalises text for ATS compatibility.
//
// ATS parsers and legacy systems fail on em dashes, smart quotes, zero-width
// characters and non-breaking spaces — they cause mojibake or parsing errors.
// The arrow and dot glyphs are often stripped outright by PDF text extractors,
// so they become words or pipes rather than vanishing.
//
// Order matters: the regex passes run before the plain replacements, because
// they consume surrounding whitespace and the replacer would otherwise have
// already rewritten what sits next to them.
func SanitizeATS(s string) string {
	if s == "" {
		return s
	}
	s = rightArrowRe.ReplaceAllString(s, " to ")
	s = leftArrowRe.ReplaceAllString(s, " from ")
	s = vertArrowRe.ReplaceAllString(s, " ")
	s = middotRe.ReplaceAllString(s, " | ")
	s = bulletGlyphRe.ReplaceAllString(s, " | ")
	return atsReplacer.Replace(s)
}

// ── Keyword tokens ───────────────────────────────────────────────────────────

var tokenRe = regexp.MustCompile(`[a-z0-9+#.]{3,}`)

var stopwords = map[string]bool{
	"and": true, "the": true, "of": true, "for": true, "to": true, "in": true,
	"with": true, "on": true, "our": true, "you": true, "your": true, "we": true,
	"is": true, "are": true, "as": true, "at": true, "an": true, "or": true,
	"by": true, "be": true, "this": true, "that": true, "will": true,
	"have": true, "has": true, "from": true, "using": true, "use": true,
}

// Tokenize splits text into keyword tokens: runs of at least three letters,
// digits, +, # or . — so "c++", "c#" and "node.js" survive as one token — minus
// the stopwords. The three-character floor is what keeps "and" and "the" out
// even before the list.
func Tokenize(s string) []string {
	var out []string
	for _, w := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		if !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// TokenSet is Tokenize as a set.
func TokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, w := range Tokenize(s) {
		set[w] = true
	}
	return set
}

// sentenceEnd is ". ", "! " or "? " — the separator, not the sentence.
var sentenceEnd = regexp.MustCompile(`[.!?]\s+`)

// SplitSentences cuts after . ! or ? followed by whitespace, keeping the
// punctuation on the sentence it belongs to.
//
// RE2 has no lookbehind, so `split(/(?<=[.!?])\s+/)` is done by index:
// FindAllStringIndex gives the separator spans and the sentence ends at the
// punctuation, not before it.
func SplitSentences(s string) []string {
	var out []string
	prev := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(s, -1) {
		out = append(out, s[prev:loc[0]+1]) // +1 keeps the . ! or ?
		prev = loc[1]
	}
	return append(out, s[prev:])
}
