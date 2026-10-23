package verify

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/guards"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
)

// The code checks of docs/PRD-TAILORING-REWORK.md §6.4. Each returns "" when
// the draft passes and the reason when it does not. They are exact and free, so
// they run before any model call, and none of them reads the posting: a draft
// is judged against its source bullet and the ledger alone.

// Figures fails a draft that adds a figure or loses one. Same test as
// suggest.keepsFigures, which is unexported and says only yes/no; a reason is
// what the review screen needs.
func Figures(source, draft string) string {
	had, has := guards.NumbersIn(source), guards.NumbersIn(draft)
	var added, lost []string
	for n := range has {
		if !had[n] {
			added = append(added, n)
		}
	}
	for n := range had {
		if !has[n] {
			lost = append(lost, n)
		}
	}
	sort.Strings(added)
	sort.Strings(lost)
	switch {
	case len(added) > 0:
		return "adds figure " + strings.Join(added, ", ")
	case len(lost) > 0:
		return "drops figure " + strings.Join(lost, ", ")
	}
	return ""
}

var (
	// A dot inside a longer word is a name (Node.js, ASP.NET); "e.g" is not.
	dotName  = regexp.MustCompile(`\p{L}{2,}\.\p{L}`)
	trimEdge = ".,;:!?()[]{}\"'`“”‘’"
)

// words splits on spaces and strips the punctuation that sits around a word,
// not the symbols inside it: "C++" and "CI/CD" must survive intact.
func words(s string) []string {
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, trimEdge)
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

// needsEntity reports whether a new part is the kind of token that can name a
// thing the CV does not claim: capitalised, an acronym, or carrying a digit or
// symbol. first is the draft's opening word, capitalised by grammar alone.
func needsEntity(p string, first bool) bool {
	if strings.IndexFunc(p, unicode.IsDigit) >= 0 || strings.ContainsAny(p, "+#/@&_") || dotName.MatchString(p) {
		return true
	}
	letters, upper := 0, 0
	for _, r := range p {
		if unicode.IsLetter(r) {
			letters++
			if unicode.IsUpper(r) {
				upper++
			}
		}
	}
	if letters >= 2 && upper == letters {
		return true // acronym
	}
	return !first && upper > 0 && unicode.IsUpper([]rune(p)[0])
}

// EntityClosure fails a draft that introduces a name its source did not use and
// the entry has no record of. known is lowercased, from the entry's facts, the
// ledger's surface forms for them, and the caller's allowed terms; see forms.
//
// This replaces the product and cased-product lists: a new word is judged by
// its shape, so there is no vocabulary of tools to keep current.
func EntityClosure(source, draft string, known map[string]bool) string {
	seen := map[string]bool{}
	for _, w := range words(source) {
		seen[strings.ToLower(w)] = true
		for _, p := range strings.Split(w, "-") {
			seen[strings.ToLower(p)] = true
		}
	}
	var bad []string
	for i, w := range words(draft) {
		if seen[strings.ToLower(w)] || known[strings.ToLower(w)] {
			continue
		}
		for j, p := range strings.Split(w, "-") {
			lp := strings.ToLower(p)
			if p == "" || seen[lp] || known[lp] {
				continue
			}
			if needsEntity(p, i == 0 && j == 0) {
				bad = append(bad, p)
			}
		}
	}
	if len(bad) > 0 {
		return "unrecorded term " + strings.Join(bad, ", ")
	}
	return ""
}

// LineCost fails a draft that prints on more lines than its source. The page
// is a line budget; a longer line spends another bullet's room.
//
// BulletLines divides by a fixed 108 characters, and a real line breaks on
// glyph widths and hyphens. The E3 run that swapped "by owning its platform end
// to end" for "with end-to-end ownership of its platform" matched the estimate
// and still rendered one line more, because the source sat just under a
// boundary. So a draft longer than its source must also leave a tenth of its
// last line free; one no longer than the source cannot be worse.
func LineCost(source, draft string) string {
	d, s := cvselect.BulletLines(draft), cvselect.BulletLines(source)
	if d > s {
		return fmt.Sprintf("takes %d lines, the source %d", d, s)
	}
	n, sn := len(utf16.Encode([]rune(draft))), len(utf16.Encode([]rune(source)))
	if n > sn && float64(n) > 0.9*float64(s*cvselect.CharsPerLine) {
		return fmt.Sprintf("%d characters is too close to the line end for a draft longer than its source", n)
	}
	return ""
}

var (
	sentenceBreak = regexp.MustCompile(`[.!?]["')\]]*\s+\p{Lu}`)

	// The same function words summary-stage.mjs counted. A keyword dump has few
	// of them, and it scored well on overlap while reading as nothing.
	functionWords = map[string]bool{
		"the": true, "and": true, "a": true, "an": true, "of": true, "in": true,
		"to": true, "for": true, "with": true, "on": true, "at": true, "by": true,
		"from": true, "as": true, "that": true, "this": true, "it": true, "its": true,
		"into": true, "over": true, "across": true, "where": true, "which": true,
		"while": true, "their": true, "has": true, "have": true, "was": true,
		"were": true, "been": true, "is": true, "are": true,
	}
)

func fnRatio(s string) (ratio float64, n int) {
	ws := strings.Fields(s)
	fn := 0
	for _, w := range ws {
		if functionWords[strings.ToLower(strings.Trim(w, trimEdge))] {
			fn++
		}
	}
	if len(ws) == 0 {
		return 0, 0
	}
	return float64(fn) / float64(len(ws)), len(ws)
}

// Shape fails a draft that is empty, spans lines or sentences, or reads as a
// tag list. The prose tests are relative to the source: cv.md is hand-written,
// and a terse original must still be able to pass as itself.
//
// Left out: "no clause whose only content is the posting's own wording". That
// needs the posting, which this package does not take; the figure, entity and
// attribution checks already stop the part of it that carries a claim.
func Shape(source, draft string) string {
	d := strings.TrimSpace(draft)
	switch {
	case d == "":
		return "empty"
	case strings.ContainsAny(d, "\r\n"):
		return "spans lines"
	case sentenceBreak.MatchString(d):
		return "more than one sentence"
	case strings.Contains(d, "—") && !strings.Contains(source, "—"):
		// The CV has none, and a dash in a list is how a rewrite reads as generated.
		return "uses an em dash the CV does not"
	}
	if strings.Count(d, "/") >= 3 && strings.Count(d, "/") > strings.Count(source, "/") {
		return "reads as a tag list"
	}
	dr, n := fnRatio(d)
	sr, _ := fnRatio(source)
	if n >= 8 && dr < 0.12 && dr < sr {
		return "reads as a keyword list"
	}
	return ""
}

// Qualifiers fails a draft that drops a hedge the source bullet carries
// ("simulated", "prototype", "working knowledge"). E2 measured MiniCheck
// passing "tested at 250M+ authentication events" against a fact that says
// "simulated": one word in a long claim does not move its verdict, and it is
// Takano's dropped-qualifier category, the one reviewers also miss. A word
// list in code is exact where the model is not.
func Qualifiers(source, draft string) string {
	src, dr := strings.ToLower(source), strings.ToLower(draft)
	for _, h := range ledger.Hedges {
		if strings.Contains(src, h) && !strings.Contains(dr, h) {
			return fmt.Sprintf("drops the qualifier %q", h)
		}
	}
	return ""
}

// strong are the verbs that claim more ownership than "built" or "designed".
// E2 had MiniCheck pass "Designed the key exchange" rewritten as "Led the key
// exchange". Such a verb is allowed only when the source already uses it or an
// allowed term (an approved framing) does: PRD §11 Q3 makes each one a framing.
var strong = []string{"led", "lead", "leading", "architected", "managed", "directed", "headed", "spearheaded", "owned", "pioneered", "oversaw"}

func Scope(source, draft string, allowed []string) string {
	known := map[string]bool{}
	for _, w := range words(source) {
		known[strings.ToLower(w)] = true
	}
	for _, a := range allowed {
		for _, w := range words(a) {
			known[strings.ToLower(w)] = true
		}
	}
	for _, w := range words(draft) {
		w = strings.ToLower(w)
		if slices.Contains(strong, w) && !known[w] {
			return fmt.Sprintf("claims more ownership with %q", w)
		}
	}
	return ""
}

// Stuffing fails a draft that hangs an allowed term on the end of the line:
// "...from 8 minutes to 28 seconds via a Redis-backed worker pool, eliminating
// blocking and overnight-only scheduling for asynchronous workloads." The third
// E3 run shipped that. The term is licensed, but nothing in the sentence needs
// it; it is the posting's word parked where a keyword scan finds it, which T4
// rules out ("no appended 'directly applicable to...' clauses"). A term the
// source already ended on is fine, and so is one used mid-sentence. An entity
// spelling (PostgreSQL for Postgres) is a swap and never stuffing; the check is
// for capability phrases.
func Stuffing(source, draft string, allowed []string, entities map[string][]string) string {
	tail := func(s string) string {
		w := words(s)
		return strings.ToLower(strings.Join(w[max(0, len(w)-5):], " "))
	}
	dt, st := tail(draft), tail(source)
	dw := words(draft)
	for _, term := range allowed {
		t := strings.ToLower(term)
		if !strings.Contains(dt, t) || strings.Contains(st, t) || isEntity(entities, term) {
			continue
		}
		// A term in the tail is stuffing only when a connective introduces it.
		for i := max(0, len(dw)-6); i < len(dw); i++ {
			if slices.Contains(connectives, strings.ToLower(dw[i])) &&
				strings.Contains(strings.ToLower(strings.Join(dw[i+1:], " ")), t) {
				return fmt.Sprintf("ends by tacking on %q", term)
			}
		}
	}
	return ""
}

// A draft that gains a posting term may change at most this much else: the
// term may replace up to maxEditCut source words, and up to maxEditGlue
// function words may come with it ("by tuning" -> "through performance tuning
// of"). Set on the 64-offer E3 run of 2026-10-23, where every line that kept
// within them read as its source with the term in, and every line that did not
// was stuffed or said something else.
const (
	maxEditCut  = 3
	maxEditGlue = 2
)

// editGlue is the function words a gained term may bring with it.
var editGlue = func() map[string]bool {
	m := map[string]bool{"through": true, "via": true, "using": true}
	for w := range functionWords {
		m[w] = true
	}
	return m
}()

// MinimalEdit fails a draft that gains a posting term and changes the rest of
// the line. MiniCheck cannot see a moved cause or a changed measure: on E3 it
// passed "retired 2 metrics ... scoring every change as neutral" rewritten as
// "retired 2 metrics ..., resulting in neutral scores for every change", in
// both directions, and "Sustained 12,000 orders/second at p99 under 200ms" as
// "Achieved query performance at p99 under 200ms". So a line that takes the
// posting's words must otherwise stay word for word, which code can check
// exactly. gained are the allowed terms the draft says and the source does not;
// with none, the check does not apply.
func MinimalEdit(source, draft string, gained []string) string {
	if len(gained) == 0 {
		return ""
	}
	cut, added := wordDiff(lowerWords(source), lowerWords(draft))
	for _, g := range gained {
		for _, w := range lowerWords(g) {
			if i := slices.Index(added, w); i >= 0 {
				added = slices.Delete(added, i, i+1)
			}
		}
	}
	for _, w := range added {
		if !editGlue[w] {
			return fmt.Sprintf("adds %q besides %s", w, strings.Join(gained, ", "))
		}
	}
	switch {
	case len(cut) > maxEditCut:
		return fmt.Sprintf("changes %d source words (%s) to fit %s", len(cut), strings.Join(cut, " "), strings.Join(gained, ", "))
	case len(added) > maxEditGlue:
		return fmt.Sprintf("adds %d words (%s) besides %s", len(added), strings.Join(added, " "), strings.Join(gained, ", "))
	}
	return ""
}

// Gained is the allowed terms the draft says and the source does not.
func Gained(source, draft string, allowed []string) []string {
	src, dr := lowerWords(source), lowerWords(draft)
	var out []string
	for _, a := range allowed {
		if t := lowerWords(a); len(t) > 0 && hasRun(dr, t) && !hasRun(src, t) {
			out = append(out, a)
		}
	}
	return out
}

func lowerWords(s string) []string {
	w := words(s)
	for i := range w {
		w[i] = strings.ToLower(w[i])
	}
	return w
}

// hasRun is true when t occurs in ws as consecutive words.
func hasRun(ws, t []string) bool {
	for i := 0; i+len(t) <= len(ws); i++ {
		if slices.Equal(ws[i:i+len(t)], t) {
			return true
		}
	}
	return false
}

// wordDiff is what an LCS alignment leaves over on each side: the source words
// the draft does not keep in order, and the draft words that are new.
func wordDiff(a, b []string) (cut, added []string) {
	n, m := len(a), len(b)
	l := make([][]int, n+1)
	for i := range l {
		l[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i, j = i+1, j+1
		case l[i+1][j] >= l[i][j+1]:
			cut, i = append(cut, a[i]), i+1
		default:
			added, j = append(added, b[j]), j+1
		}
	}
	return append(cut, a[i:]...), append(added, b[j:]...)
}

var connectives = []string{"for", "in", "to", "with", "across", "via", "using", "by", "of", "on"}

func isEntity(entities map[string][]string, term string) bool {
	for name, forms := range entities {
		if strings.EqualFold(name, term) || slices.ContainsFunc(forms, func(f string) bool { return strings.EqualFold(f, term) }) {
			return true
		}
	}
	return false
}
