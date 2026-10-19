package phase3

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
)

// The deterministic writer: the tailored content is lifted verbatim out of
// cv.md, with no generation call anywhere in Phase 3.
//
// cv-writers.mjs defaults to `--writer verbatim` and the 7B tailor arm survives
// only as a benchmark control, so it was never ported. This is the whole
// writer: grounded by construction, because every string it emits came out of
// the CV. The fabrication guards downstream exist to catch what a model invents
// and have nothing to do here.

// SkillPick is one row of the tailored skills block — a category and its items
// comma-joined, the shape cv-content.json carries.
type SkillPick struct {
	Category string `json:"category"`
	Items    string `json:"items"`
}

// writerStopwords is cv-writers.mjs's list. Kept here rather than shared with
// text.Tokenize, which is gated byte-identical against a different Node function
// and must not drift to suit this one.
var writerStopwords = map[string]bool{
	"and": true, "the": true, "of": true, "for": true, "to": true, "in": true,
	"with": true, "on": true, "our": true, "you": true, "your": true, "we": true,
	"is": true, "are": true, "as": true, "at": true, "an": true, "or": true,
	"by": true, "be": true, "this": true, "that": true, "will": true,
	"have": true, "has": true, "from": true, "using": true, "use": true,
}

var writerTokenRe = regexp.MustCompile(`[a-z0-9+#.]{3,}`)

// tokenize differs from text.Tokenize in one place, and it matters: it strips
// leading and trailing dots and re-applies the three-character floor afterwards.
// A posting that ends a sentence on "Kafka." otherwise yields the token "kafka."
// — which matches "kafka" nowhere — and postings end sentences on technology
// names constantly.
func tokenize(s string) []string {
	var out []string
	for _, w := range writerTokenRe.FindAllString(strings.ToLower(s), -1) {
		w = strings.Trim(w, ".")
		if len(w) >= 3 && !writerStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

var parenRe = regexp.MustCompile(`\(([^)]*)\)`)
var runsOfSpace = regexp.MustCompile(`\s{2,}`)

// expandSkillItem splits one skill item from its parenthesis, keeping the names
// inside and dropping the prose.
//
// A parenthesis holds one of two very different things, and the old rule — drop
// every parenthesis before splitting — treated them alike:
//
//	Message Queues (RabbitMQ, Kafka)   the keywords ARE the parenthesis
//	Kubernetes (working knowledge, …)  a caveat about depth
//
// Dropping both shipped "Message Queues" to a posting asking for Kafka, and did
// the same to AES-256-GCM, EC2/Lambda/S3/IAM, Jest, Jenkins, Ollama and MCP —
// the CV's most searchable terms, deleted at parse time from every tailored PDF.
//
// Each comma-separated part is judged alone, so a mixed parenthesis keeps the
// names and drops the prose: a name starts with a capital or a digit, or carries
// a digit; "working knowledge" and "daily driver" do neither.
func expandSkillItem(raw string) []string {
	var promoted []string
	base := parenRe.ReplaceAllStringFunc(raw, func(m string) string {
		inner := m[1 : len(m)-1]
		for _, part := range strings.Split(inner, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			first := []rune(part)[0]
			if unicode.IsUpper(first) || unicode.IsDigit(first) || strings.ContainsAny(part, "0123456789") {
				promoted = append(promoted, part)
			}
		}
		return " "
	})
	base = strings.TrimSpace(runsOfSpace.ReplaceAllString(base, " "))

	out := make([]string, 0, 1+len(promoted))
	for _, s := range append([]string{base}, promoted...) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

var skillLineRe = regexp.MustCompile(`(?m)^\*\*([^*]+):\*\*[ \t]*(.+)$`)

// SkillCat is a `**Category:** a, b, c` line out of cv.md's Skills section.
type SkillCat struct {
	Category string
	Items    []string
}

// ParseSkillCategories reads the Skills section into categories and items.
//
// Sectioned through cvselect rather than on Node's `/^##\s+/m`: the real cv.md
// writes its headings as **Skills** on a line of its own and has no `##` in it
// at all, so the literal port found nothing on the only file that matters.
func ParseSkillCategories(cvText string) []SkillCat {
	sec := ""
	for _, s := range cvselect.ParseSections(cvText) {
		if strings.EqualFold(s.Name, "Skills") {
			sec = strings.Join(s.Lines, "\n")
			break
		}
	}
	var out []SkillCat
	for _, m := range skillLineRe.FindAllStringSubmatch(sec, -1) {
		c := SkillCat{Category: strings.TrimSpace(m[1])}
		for _, item := range splitTopLevel(m[2]) {
			if item = strings.TrimSpace(item); item != "" {
				c.Items = append(c.Items, expandSkillItem(item)...)
			}
		}
		out = append(out, c)
	}
	return out
}

// splitTopLevel splits on commas outside parentheses. Several items carry a
// comma inside one — "Message Queues (RabbitMQ, Kafka)" — and splitting naively
// ships "Message Queues (RabbitMQ" as a skill.
//
// A depth counter rather than the Node's lookahead, which Go's regexp does not
// have; on well-formed input the two agree, and this one also survives a nested
// or unclosed bracket instead of falling back to splitting everything.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

var nonPhraseRe = regexp.MustCompile(`[^a-z0-9+#.]+`)
var trailingDotRe = regexp.MustCompile(`\.(\s|$)`)

// NormPhrase is a phrase space-wrapped for whole-phrase containment tests.
//
// The dot survives the character class so "Next.js" and "node.js" stay whole,
// and that welds a sentence-final period onto the last word: a JD reading
// "…experience with Kafka." yields "kafka.", which contains "kafka" nowhere as a
// phrase. Postings end sentences on technology names constantly, so this
// silently dropped real matches.
func NormPhrase(s string) string {
	t := nonPhraseRe.ReplaceAllString(strings.ToLower(s), " ")
	t = trailingDotRe.ReplaceAllString(t, "$1")
	return " " + strings.TrimSpace(t) + " "
}

// relatedToJd is what cv.md ties to the terms the posting used: an item the
// posting never named, but which shares a line of evidence with one it did.
// Java naming Spring Boot, on the CV's own say-so.
func relatedToJd(cvText string, allItems []string, hits func(string) int) map[string]bool {
	var present []string
	for _, i := range allItems {
		if hits(i) > 0 {
			present = append(present, NormPhrase(i))
		}
	}
	out := map[string]bool{}
	if len(present) == 0 {
		return out
	}
	// Every section but Skills: the block cannot vouch for itself.
	var evidence []string
	for _, s := range cvselect.ParseSections(cvText) {
		if !strings.EqualFold(s.Name, "Skills") {
			evidence = append(evidence, strings.Join(s.Lines, "\n"))
		}
	}
	for _, line := range strings.Split(strings.Join(evidence, "\n"), "\n") {
		n := NormPhrase(line)
		found := false
		for _, p := range present {
			if strings.Contains(n, p) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		for _, item := range allItems {
			if hits(item) == 0 && strings.Contains(n, NormPhrase(item)) {
				out[item] = true
			}
		}
	}
	return out
}

// SkillForms are the spellings that count as a posting naming an item: the item
// itself, each side of a spaced slash ("TypeScript / JavaScript" lists two real
// skills, and a posting asking for TypeScript has named it), and its approved
// aliases. It mirrors cv-writers.mjs's skillForms, plus the aliases.
func SkillForms(item string, aliases map[string][]string) []string {
	forms := []string{item}
	if parts := strings.Split(item, " / "); len(parts) > 1 {
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				forms = append(forms, p)
			}
		}
	}
	return append(forms, aliases[strings.ToLower(item)]...)
}

// The skills block as it ships: up to SkillCats categories the posting did not
// name, up to SkillItems items in each, and a SkillFloor of cv.md's own. Chosen
// with `gate skills <home> --sweep` over 351 cached postings: 99.8% of named
// skills kept, 5.2 categories and ~22 items, against 37 items at the old 6/12/3.
const (
	SkillCats  = 5
	SkillItems = 7
	SkillFloor = 3
)

// SelectSkills ranks cv.md's skill categories by what the posting asked for, and
// the items inside each the same way.
//
// Items are kept in three tiers and everything below them is DROPPED, not merely
// ranked last. On the 32-offer bench the block shipped 52 items of which 12.9
// shared a term with the posting: 75% of it bought nothing an ATS could read and
// cost 4.4 rendered lines, on a page whose whole evidence budget is 21.
//
//  1. the posting's own terms — one of SkillForms, as a whole phrase
//  2. what cv.md ties to them — relatedToJd, so Java brings Spring Boot
//  3. a floor in cv.md's order — minItems, so nothing reads as pandering
//
// Tier 3 is the guard against optimising the metric into a lie: a block holding
// only what the posting asked for drops the distinctive evidence, Rust vanishing
// off a Java posting.
//
// aliases are config/skills.yml's approved spellings, keyed by item lowercased.
// An item the posting names only by an alias counts as named, and prints in the
// posting's spelling: "REST APIs" ships as "RESTful APIs" to a posting that says
// so. Nil keeps cv.md's own strings.
func SelectSkills(cvText, jdText string, maxCats, maxItems, minItems int, aliases map[string][]string) []SkillPick {
	jd := map[string]bool{}
	for _, t := range tokenize(jdText) {
		jd[t] = true
	}
	jdNorm := NormPhrase(jdText)
	// tokenize has a three-character floor, so "C#" and "CI/CD" yield no tokens
	// and score zero however loudly a posting asks for them. Ranking hid that —
	// they shipped last — and filtering exposed it: C# was dropped from five
	// postings that named it. Whole-phrase presence is the honest test, and it
	// is stricter than token overlap rather than looser.
	hits := func(s string) int {
		n := 0
		for _, t := range tokenize(s) {
			if jd[t] {
				n++
			}
		}
		if strings.Contains(jdNorm, NormPhrase(s)) {
			n++
		}
		return n
	}
	// spelling is the form the posting uses: the item itself when the posting
	// writes it that way or not at all, else the longest approved alias it does
	// write — so "RESTful APIs" beats "RESTful" on a posting that says the former.
	spelling := func(item string) string {
		if strings.Contains(jdNorm, NormPhrase(item)) {
			return item
		}
		best := ""
		for _, a := range aliases[strings.ToLower(item)] {
			if len(a) > len(best) && strings.Contains(jdNorm, NormPhrase(a)) {
				best = a
			}
		}
		if best == "" {
			return item
		}
		return best
	}
	// named was token overlap, and one shared word was enough: "engineering"
	// named "prompt and context engineering" in 241 of 351 postings, "design"
	// named System Design. Nearly every category read as named and named ones
	// are never cut, so the caps never bound. Now a whole form must appear;
	// hits only ranks within the tier, floored at 1 because "AI" is under
	// tokenize's three characters and would otherwise score nothing.
	named := func(item string) int {
		for _, f := range SkillForms(item, aliases) {
			if strings.Contains(jdNorm, NormPhrase(f)) {
				return max(1, hits(spelling(item)))
			}
		}
		return 0
	}

	cats := ParseSkillCategories(cvText)
	var all []string
	for _, c := range cats {
		all = append(all, c.Items...)
	}
	related := relatedToJd(cvText, all, named)

	type ranked struct {
		item  string // as the block prints it
		i     int
		score int
		tier  int
	}
	type cat struct {
		category string
		items    []ranked
		idx      int
		score    int
		named    bool
	}

	scored := make([]cat, 0, len(cats))
	for idx, c := range cats {
		rows := make([]ranked, 0, len(c.Items))
		for i, item := range c.Items {
			h := named(item)
			t := 0
			switch {
			case h > 0:
				t = 2
			case related[item]:
				t = 1
			}
			rows = append(rows, ranked{item: spelling(item), i: i, score: h, tier: t})
		}
		// Tier first, so a related item outranks a floor item but never
		// displaces one the posting actually named. Stable on the CV's order.
		sortStable(rows, func(a, b ranked) bool {
			if a.tier != b.tier {
				return a.tier > b.tier
			}
			if a.score != b.score {
				return a.score > b.score
			}
			return a.i < b.i
		})
		kept := rows[:0:0]
		for _, r := range rows {
			if r.tier > 0 {
				kept = append(kept, r)
			}
		}
		if len(kept) < minItems {
			kept = rows[:min(minItems, len(rows))]
		}
		if len(kept) > maxItems {
			kept = kept[:maxItems]
		}

		out := cat{category: c.Category, items: kept, idx: idx}
		for _, r := range kept {
			out.score += r.score
			// Whether the posting named anything in here at all. Summed score
			// alone let a category the posting asked for lose its slot to one
			// scoring higher on many weak matches: "Databases & Caching" placed
			// seventh of six on two postings, which is how PostgreSQL and MySQL
			// went missing from CVs that claim both.
			if r.tier == 2 {
				out.named = true
			}
		}
		scored = append(scored, out)
	}
	sortStable(scored, func(a, b cat) bool {
		if a.score != b.score {
			return a.score > b.score
		}
		return a.idx < b.idx
	})

	// maxCats budgets the categories the posting did NOT name. One it did is
	// never cut: dropping it silently deletes a skill the CV claims and the
	// posting explicitly asked for, which no saved space is worth.
	var out []cat
	for _, c := range scored {
		if c.named {
			out = append(out, c)
		}
	}
	room := maxCats - len(out)
	for _, c := range scored {
		if !c.named && room > 0 {
			out = append(out, c)
			room--
		}
	}
	// Back to CV order once the cut is made. The categories read as a list, and
	// reordering them by relevance makes the same CV look different to a human
	// holding two of them side by side, for no gain.
	sortStable(out, func(a, b cat) bool { return a.idx < b.idx })

	picks := make([]SkillPick, 0, len(out))
	for _, c := range out {
		names := make([]string, 0, len(c.items))
		for _, r := range c.items {
			names = append(names, r.item)
		}
		picks = append(picks, SkillPick{Category: c.category, Items: strings.Join(names, ", ")})
	}
	return picks
}

// sortStable is sort.SliceStable with the element type kept, so the comparators
// above read as comparisons rather than as index arithmetic.
func sortStable[T any](s []T, less func(a, b T) bool) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}
