// Package fit holds the deterministic fit rules shared by Phase 1 and Phase 2.
//
// These enforce judgments the local models are unreliable at: the pre-screen
// inflates cv_match when the domain and keywords look familiar even though the
// role's core language or platform is one the candidate does not have.
//
// The candidate's ecosystems are parsed from cv.md, so this stays in sync with
// the CV automatically — add a language to cv.md and the penalty stops firing
// for it.
package fit

import (
	"regexp"
	"strings"
)

// ecosystem is a language/platform family and how to spot it. Deliberately
// narrow, to avoid false positives — "go-live" must not count as Go.
type ecosystem struct {
	name string
	re   *regexp.Regexp
	// notAfter/notBefore emulate lookaround, which RE2 does not have. See
	// countMatches.
	notBefore *regexp.Regexp
	notAfter  *regexp.Regexp
}

// The c#/.net entry is the one that needs the emulation.
//
// `\b` cannot fence a token that starts or ends with punctuation: `\bc#\b`
// needs a word char right after the `#`, and `\b\.net` needs one right before
// the dot — so a naive pattern matches NEITHER "C#" nor ".NET" in any real
// spelling, only "dotnet"/"asp.net". The ecosystem was therefore invisible on
// both sides of every comparison: JDs demanding it, and a CV claiming it.
//
// `c#(?:\.net)?` comes first so "C#.Net" counts once, not twice, against
// minMentions — Go's regexp is leftmost-first like JS, so alternation order is
// preserved. The leading guard is what keeps a ".net" TLD ("example.net") from
// reading as the ecosystem.
var ecosystems = []ecosystem{
	{
		name:      "c#/.net",
		re:        regexp.MustCompile(`(?i)c#(?:\.net)?|asp\.net|jscript\.net|dotnet|\.net`),
		notBefore: regexp.MustCompile(`(?i)[a-z0-9]$`),
		notAfter:  regexp.MustCompile(`(?i)^[a-z0-9]`),
	},
	// The JS pattern carries a `(?!script)` after `\bjava\b`, which is dead:
	// there is no word boundary between "java" and "script" in "javascript", so
	// `\bjava\b` already cannot match inside it. Dropped rather than emulated.
	{name: "java", re: regexp.MustCompile(`(?i)\bjava\b`)},
	{name: "go", re: regexp.MustCompile(`(?i)\b(golang|go\s+(?:developer|engineer|programmer|programming)|written\s+in\s+go|microservices\s+in\s+go)\b`)},
	{name: "ruby", re: regexp.MustCompile(`(?i)\b(ruby on rails|\bruby\b|\brails\b)\b`)},
	{name: "php", re: regexp.MustCompile(`(?i)\b(php|laravel|symfony)\b`)},
	{name: "python", re: regexp.MustCompile(`(?i)\b(python|django|flask|fastapi)\b`)},
	{name: "javascript", re: regexp.MustCompile(`(?i)\b(javascript|typescript|node\.?js|react|angular|vue|next\.?js)\b`)},
	{name: "rust", re: regexp.MustCompile(`(?i)\brust\b`)},
	{name: "scala", re: regexp.MustCompile(`(?i)\bscala\b`)},
	{name: "elixir", re: regexp.MustCompile(`(?i)\belixir\b`)},
	{name: "kotlin", re: regexp.MustCompile(`(?i)\bkotlin\b`)},
	{name: "cpp", re: regexp.MustCompile(`(?i)c\+\+`)},
}

// tools are named technologies that are not programming languages, so the
// ecosystem table does not cover them. Used only to check claims about the
// candidate against cv.md — keep it to things a CV would name explicitly.
var tools = []ecosystem{
	{name: "azure", re: regexp.MustCompile(`(?i)\bazure\b`)},
	{name: "gcp", re: regexp.MustCompile(`(?i)\b(gcp|google cloud)\b`)},
	{name: "aws", re: regexp.MustCompile(`(?i)\baws\b`)},
	{name: "kubernetes", re: regexp.MustCompile(`(?i)\b(kubernetes|k8s)\b`)},
	{name: "terraform", re: regexp.MustCompile(`(?i)\bterraform\b`)},
	{name: "flask", re: regexp.MustCompile(`(?i)\bflask\b`)},
	{name: "pytorch", re: regexp.MustCompile(`(?i)\bpytorch\b`)},
	{name: "tensorflow", re: regexp.MustCompile(`(?i)\btensorflow\b`)},
	{name: "spark", re: regexp.MustCompile(`(?i)\b(apache\s+)?spark\b`)},
	{name: "databricks", re: regexp.MustCompile(`(?i)\bdatabricks\b`)},
	{name: "kafka", re: regexp.MustCompile(`(?i)\bkafka\b`)},
	{name: "elasticsearch", re: regexp.MustCompile(`(?i)\belasticsearch\b`)},
	{name: "snowflake", re: regexp.MustCompile(`(?i)\bsnowflake\b`)},
	{name: "airflow", re: regexp.MustCompile(`(?i)\bairflow\b`)},
}

// countMatches counts non-overlapping matches, applying the lookaround guards
// by inspecting the characters either side of each candidate match.
//
// Doing it by index rather than folding the guards into the pattern is what
// keeps adjacent occurrences countable: encoding a trailing guard as a consumed
// character would make ".net .net" a single match, because the delimiter that
// ends the first is the one that starts the second.
func (e ecosystem) countMatches(text string) int {
	n := 0
	for _, loc := range e.re.FindAllStringIndex(text, -1) {
		if e.notBefore != nil && e.notBefore.MatchString(text[:loc[0]]) {
			continue
		}
		if e.notAfter != nil && e.notAfter.MatchString(text[loc[1]:]) {
			continue
		}
		n++
	}
	return n
}

// skillsSection is the skills catalogue under either heading style: "## Skills"
// or a bold-alone "**Skills**" line. It ends at the next heading of either style.
// The CV moved from the first to the second, and the first alone stopped matching,
// so the languages line counted as evidence.
var skillsSection = regexp.MustCompile(`(?is)\n(?:##\s*|\*\*)Skills\b.*?(\n##\s|\n\*\*[^*:\n]+\*\*[ \t]*(?:\n|$)|$)`)

// EvidenceText is cv.md minus its `## Skills` taxonomy.
//
// Same doctrine the evidence grader applies: a catalogue line is a claim, not a
// demonstration. An ecosystem named only in the skills block has no bullet
// behind it anywhere on the CV, so StackMismatchCap must not read it as
// coverage.
func EvidenceText(cv string) string {
	return skillsSection.ReplaceAllString(cv, "\n")
}

// CandidateEcosystems are the ecosystems the candidate clearly has, parsed from
// cv.md — experience, projects and education only, never the skills catalogue.
//
// Measured on the reference CV: every other claimed ecosystem is named 3–6
// times outside the skills block and c#/.net exactly 0, so the whole effect of
// the exclusion is that five C#-only postings stop reading as covered — three
// of which had scored above the Phase 3 threshold and generated a tailored CV
// for a stack with no project behind it.
func CandidateEcosystems(cv string) map[string]bool {
	have := map[string]bool{}
	ev := EvidenceText(cv)
	for _, e := range ecosystems {
		if e.countMatches(ev) > 0 {
			have[e.name] = true
		}
	}
	return have
}

// StackCap is the result of the stack-mismatch rule. Cap 5 means no penalty.
type StackCap struct {
	Cap     int
	JDStack []string
	Missing []string
}

// StackMismatchCap caps cv_match when the JD's required ecosystems — those
// mentioned at least minMentions times — have no overlap with the candidate's.
// A strong engineer can ramp on a new language, but it is a real gap.
func StackMismatchCap(jd, cv string, cap, minMentions int) StackCap {
	if cap == 0 {
		cap = 3
	}
	if minMentions == 0 {
		minMentions = 2
	}
	have := CandidateEcosystems(cv)
	var present []string
	for _, e := range ecosystems {
		if e.countMatches(jd) >= minMentions {
			present = append(present, e.name)
		}
	}
	if len(present) == 0 {
		return StackCap{Cap: 5} // language-agnostic JD
	}
	for _, p := range present {
		if have[p] {
			return StackCap{Cap: 5, JDStack: present} // candidate covers at least one
		}
	}
	return StackCap{Cap: cap, JDStack: present, Missing: present}
}

// VerifyAgainstCv drops claims about the candidate that name a technology
// cv.md never mentions.
//
// top_strengths is the one field where the pipeline asserts something on the
// candidate's *behalf*, and it fabricates: measured over 115 offers, 3.7% of
// the strengths naming a technology named one the CV does not contain — usually
// a neighbour of something it does. Unlike a wrong gap, a wrong strength can
// reach an application, so this is a hard filter rather than a prompt
// instruction. A claim naming nothing checkable is left alone.
func VerifyAgainstCv(claims []string, cv string) (kept, dropped []string) {
	all := append(append([]ecosystem{}, ecosystems...), tools...)
	for _, claim := range claims {
		fabricated := false
		for _, e := range all {
			if e.countMatches(claim) > 0 && e.countMatches(cv) == 0 {
				fabricated = true
				break
			}
		}
		if fabricated {
			dropped = append(dropped, claim)
		} else {
			kept = append(kept, claim)
		}
	}
	return kept, dropped
}

// ── Seniority ────────────────────────────────────────────────────────────────

// Caps is a pair of ceilings on the two scored dimensions.
type Caps struct {
	CV     int
	NS     int
	Reason string
}

var (
	fwdRe       = regexp.MustCompile(`\bforward[\s-]?deployed\b|\bfde\b`)
	architectRe = regexp.MustCompile(`\barchitect\b`)
	// People-management titles count as Staff+ for an early-career IC: an
	// engineering-manager role is a track mismatch, not a stretch.
	staffishRe  = regexp.MustCompile(`\b(staff|principal|distinguished|director|vp|vice president|head of|manager)\b`)
	seniorishRe = regexp.MustCompile(`\b(senior|sr\.?|lead)\b`)
)

// SeniorityCaps applies the ceilings an early-career candidate faces. The title
// is read here, by the words that carry a level. The years demanded come from the
// caller: the extracted minimum, or stage 1's parse. They are not searched for in
// the posting, since "15 years" there is as likely the company's age.
func SeniorityCaps(roleTitle string, yearsRequired int, seniorityLevel string) Caps {
	title := strings.ToLower(roleTitle)
	level := strings.ToLower(seniorityLevel)

	staffish := staffishRe.MatchString(title) || strings.Contains(level, "staff") || strings.Contains(level, "principal")
	seniorish := seniorishRe.MatchString(title) || strings.Contains(level, "senior")

	switch {
	case staffish || architectRe.MatchString(title) || yearsRequired >= 8:
		return Caps{CV: 2, NS: 3, Reason: "Staff/Principal/8+ yrs demand"}
	case seniorish || fwdRe.MatchString(title) || (yearsRequired >= 5 && yearsRequired < 8):
		return Caps{CV: 3, NS: 4, Reason: "Senior/5+ yrs demand"}
	}
	return Caps{CV: 5, NS: 5}
}

// ── Natural language ─────────────────────────────────────────────────────────

var (
	langSectionRe = regexp.MustCompile(`(?is)\*\*Languages\*\*\s*\n(.*?)(\n\s*\*\*|$)`)
	langItemRe    = regexp.MustCompile(`^\s*-\s*([A-Za-z]+)`)
)

// CandidateLanguages are the languages listed under the CV's Languages section.
func CandidateLanguages(cv string) map[string]bool {
	have := map[string]bool{"english": true}
	m := langSectionRe.FindStringSubmatch(cv)
	if m == nil {
		return have
	}
	for _, line := range strings.Split(m[1], "\n") {
		if item := langItemRe.FindStringSubmatch(line); item != nil {
			have[strings.ToLower(item[1])] = true
		}
	}
	return have
}

// ── Location policy ──────────────────────────────────────────────────────────

var (
	profileCityRe = regexp.MustCompile(`(?m)^\s*city:\s*["']?([^"'\n]+)`)
	// \z, not $: under (?m) the lazy group stopped at the first end of line,
	// so only the first search_locations entry was ever read.
	searchLocsRe    = regexp.MustCompile(`(?ms)^\s*search_locations:\s*\n(.*?)(\n\s*\w+:|\z)`)
	remoteOnlyRe    = regexp.MustCompile(`(?i)remote\s+only`)
	onsiteOKRe      = regexp.MustCompile(`(?i)on-?site`)
	hybridOKRe      = regexp.MustCompile(`(?i)\bhybrid\b`)
	listItemRe      = regexp.MustCompile(`^\s*-\s`)
	listItemStripRe = regexp.MustCompile(`^\s*-\s*["']?`)
)

// commuteTiers splits the places the candidate will attend: full are full-time
// bases, hybrid are search_locations entries that allow hybrid and not on-site
// ("London — hybrid or remote"), commuted to part of the week only.
func commuteTiers(profileYML string) (full, hybrid []string) {
	seen := map[string]bool{}
	add := func(to *[]string, s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			*to = append(*to, s)
		}
	}
	if m := profileCityRe.FindStringSubmatch(profileYML); m != nil {
		add(&full, m[1])
	}
	if m := searchLocsRe.FindStringSubmatch(profileYML); m != nil {
		for _, line := range strings.Split(m[1], "\n") {
			if !listItemRe.MatchString(line) || remoteOnlyRe.MatchString(line) {
				continue
			}
			to := &full
			if hybridOKRe.MatchString(line) && !onsiteOKRe.MatchString(line) {
				to = &hybrid
			}
			place := listItemStripRe.ReplaceAllString(line, "")
			place = strings.FieldsFunc(place, func(r rune) bool {
				return r == '—' || r == '–' || r == '"' || r == '\''
			})[0]
			for _, part := range strings.Split(place, "/") {
				add(to, part)
			}
		}
	}
	return full, hybrid
}

// ── Evidence strength ────────────────────────────────────────────────────────

// Strength levels for one requirement's evidence row.
const (
	Strong       = "Strong"
	Transferable = "Transferable"
	Gap          = "Gap"
)

// Catalogue reports whether an atom is a *claim* rather than a demonstration.
//
// A Skills or Education catalogue line cannot lose: it lexically contains every
// technology it lists, so it matches any requirement naming one and proves it.
// Over 573 requirement rows from 60 reports, 184 cited a catalogue line — 138
// Strong, 46 Transferable, 0 Gap. A surface that has never once failed is not
// grading.
//
// Exported because retrieval has to know it too. Phase 2 ranks catalogue lines
// in their own pool so one can take at most a single candidate slot, and that
// rule and this demotion have to name the same set of sources or the grader
// spends its slots on evidence it has already decided cannot score Strong.
func Catalogue(source string) bool { return source == "skills" || source == "education" }

// StrengthFrom derives evidence strength from the model's two axes rather than
// asking for it directly. A single Strong/Partial/Gap enum made the model fold
// two independent calls into one label, and Partial conflated "same work,
// different tool" with "vaguely adjacent" — which is how a Databricks role
// scored 5/5 against a CV with no Databricks on it.
//
// Different activity is a Gap however well the tooling lines up: listing
// Kubernetes as a tool does not cover a requirement for Kubernetes internals.
//
// tooling is three-way because most requirements name no technology. As a
// boolean it collapsed "no tooling involved" into "wrong tooling" and quietly
// taxed every degree, communication and mentoring requirement.
func StrengthFrom(pick string, sameActivity bool, tooling, requirement, evidence, source string) string {
	if s, done := strengthBase(pick, sameActivity, tooling, source); done {
		return s
	}
	// Same catalogue as VerifyAgainstCv but the opposite quantifier: a *claim*
	// naming two technologies asserts both, a *requirement* listing them ("AWS,
	// Azure or GCP") accepts any one. Demote rather than Gap — the activity
	// matched, only the proof of the named technology is missing.
	if requirement != "" {
		all := append(append([]ecosystem{}, ecosystems...), tools...)
		var named []ecosystem
		for _, e := range all {
			if e.countMatches(requirement) > 0 {
				named = append(named, e)
			}
		}
		if len(named) > 0 {
			grounded := false
			for _, e := range named {
				if e.countMatches(evidence) > 0 {
					grounded = true
					break
				}
			}
			if !grounded {
				return Transferable
			}
		}
	}
	return Strong
}

// NamedTech lists the ecosystems and tools the patterns find in text. It exists
// so `gate typed` can compare the patterns with the parsed terms.
func NamedTech(text string) []string {
	var out []string
	for _, e := range append(append([]ecosystem{}, ecosystems...), tools...) {
		if e.countMatches(text) > 0 {
			out = append(out, e.name)
		}
	}
	return out
}

// StrengthFromTerms is StrengthFrom with the technologies the requirement names
// read by the JD parse (quoted and checked) instead of found by the ecosystem
// and tool patterns. The rule is the same: a row whose requirement names
// technologies and whose evidence names none of them is demoted.
func StrengthFromTerms(pick string, sameActivity bool, tooling, evidence, source string, terms []string) string {
	if s, done := strengthBase(pick, sameActivity, tooling, source); done {
		return s
	}
	if len(terms) > 0 {
		ev := phraseSpace(evidence)
		for _, t := range terms {
			if strings.Contains(ev, phraseSpace(t)) {
				return Strong
			}
		}
		return Transferable
	}
	return Strong
}

// strengthBase is the part of the grade that does not look at technology names.
func strengthBase(pick string, sameActivity bool, tooling, source string) (string, bool) {
	if pick == "none" || !sameActivity {
		return Gap, true
	}
	if tooling == "different" || Catalogue(source) {
		return Transferable, true
	}
	return "", false
}

var phraseNoise = regexp.MustCompile(`[^a-z0-9+#.&-]+`)

// phraseSpace lower-cases and pads with spaces so a phrase match cannot straddle
// a word boundary, and keeps the symbols that are part of names (C++, Node.js).
func phraseSpace(s string) string {
	return " " + strings.TrimSpace(phraseNoise.ReplaceAllString(strings.ToLower(s), " ")) + " "
}

// ── Multi-posting ────────────────────────────────────────────────────────────

// Every pattern must name the OPENINGS, never the employer's scale. A count of
// firms or clients reads as an aggregator ("40+ top trading firms seeking
// engineers") but far more often it is a company bragging about its customers —
// "100,000+ companies" and "1000 clients" in real postings both tripped an
// earlier rule. A false positive caps a real offer at 2/2 and brands it
// Suspicious, so the bar is deliberately high.
var multiPosting = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bmultiple\s+(immediate\s+)?(openings|positions|roles|vacancies)\b`),
	regexp.MustCompile(`(?i)\bwho['’]?s\s+hiring\b`),
	regexp.MustCompile(`(?i)\bhiring\s+thread\b`),
	regexp.MustCompile(`(?i)\bseveral\s+(openings|positions|roles|vacancies)\b`),
	regexp.MustCompile(`(?i)\bvarious\s+(openings|positions|roles|vacancies)\b`),
}

// LooksMultiPosting is true when the JD advertises more than one job. OR this
// with the model's is_single_posting so either signal trips the cap — the model
// is not reliable here: on one aggregator advert it answered "single posting"
// six times out of six, and the offer scored 4.8 with "Apply".
func LooksMultiPosting(jd string) bool {
	for _, re := range multiPosting {
		if re.MatchString(jd) {
			return true
		}
	}
	return false
}
