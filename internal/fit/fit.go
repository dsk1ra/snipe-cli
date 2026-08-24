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

var skillsSection = regexp.MustCompile(`(?is)\n##\s*Skills\b.*?(\n##\s|$)`)

// evidenceText is cv.md minus its `## Skills` taxonomy.
//
// Same doctrine the evidence grader applies: a catalogue line is a claim, not a
// demonstration. An ecosystem named only in the skills block has no bullet
// behind it anywhere on the CV, so StackMismatchCap must not read it as
// coverage.
func evidenceText(cv string) string {
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
	ev := evidenceText(cv)
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
	highYearsRe = regexp.MustCompile(`\b(8|9|10|11|12|15)\+?\s*years`)
	midYearsRe  = regexp.MustCompile(`\b(5|6|7)\+?\s*years`)
	fwdRe       = regexp.MustCompile(`\bforward[\s-]?deployed\b|\bfde\b`)
	architectRe = regexp.MustCompile(`\barchitect\b`)
	// People-management titles count as Staff+ for an early-career IC: an
	// engineering-manager role is a track mismatch, not a stretch.
	staffishRe  = regexp.MustCompile(`\b(staff|principal|distinguished|director|vp|vice president|head of|manager)\b`)
	seniorishRe = regexp.MustCompile(`\b(senior|sr\.?|lead)\b`)
)

// SeniorityCaps applies the ceilings an early-career candidate faces. stage1
// carries the structured fields when the staged evaluator has them; Phase 1
// passes zero values and works off the title and the text.
func SeniorityCaps(roleTitle, jdText string, yearsRequired int, seniorityLevel string) Caps {
	title := strings.ToLower(roleTitle)
	jd := strings.ToLower(jdText)
	level := strings.ToLower(seniorityLevel)

	highYears := yearsRequired >= 8 || highYearsRe.MatchString(jd)
	midYears := (yearsRequired >= 5 && yearsRequired < 8) || midYearsRe.MatchString(jd)
	staffish := staffishRe.MatchString(title) || strings.Contains(level, "staff") || strings.Contains(level, "principal")
	seniorish := seniorishRe.MatchString(title) || strings.Contains(level, "senior")

	switch {
	case staffish || architectRe.MatchString(title) || highYears:
		return Caps{CV: 2, NS: 3, Reason: "Staff/Principal/8+ yrs demand"}
	case seniorish || fwdRe.MatchString(title) || midYears:
		return Caps{CV: 3, NS: 4, Reason: "Senior/5+ yrs demand"}
	}
	return Caps{CV: 5, NS: 5}
}

// ── Natural language ─────────────────────────────────────────────────────────

// English is never a mismatch: the CV is in English.
var humanLanguages = []string{
	"german", "french", "spanish", "italian", "dutch", "portuguese",
	"japanese", "korean", "mandarin", "chinese", "arabic", "polish", "swedish",
	"norwegian", "danish", "finnish", "turkish", "czech", "ukrainian", "russian",
}

var (
	langSectionRe = regexp.MustCompile(`(?is)\*\*Languages\*\*\s*\n(.*?)(\n\s*\*\*|$)`)
	langItemRe    = regexp.MustCompile(`^\s*-\s*([A-Za-z]+)`)
	softMentionRe = regexp.MustCompile(`(?i)\bplus\b|\bbonus\b|nice[\s-]to[\s-]have|preferred|advantage|desirable`)
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

// LanguageMismatchCap caps hard when the JD requires fluency in a natural
// language the candidate does not have.
//
// One missed requirement barely moves the coverage average, so a "(German
// speaking)" role scores a 4.5 unless this is a code-level gate. Nice-to-have
// mentions do not trigger it.
func LanguageMismatchCap(jdText, cv string) (Caps, string) {
	jd := strings.ToLower(jdText)
	have := CandidateLanguages(cv)
	for _, lang := range humanLanguages {
		if have[lang] {
			continue
		}
		re := regexp.MustCompile(`(?i)\b` + lang + `[\s-]*(speaking|fluen\w*|proficien\w*|native)|` +
			`\b(fluen\w*|proficien\w*|native)[^.\n]{0,30}\b` + lang + `\b`)
		loc := re.FindStringIndex(jd)
		if loc == nil {
			continue
		}
		if softMentionRe.MatchString(window(jd, loc[0], loc[1], 60)) {
			continue
		}
		return Caps{CV: 2, NS: 2}, lang
	}
	return Caps{CV: 5, NS: 5}, ""
}

// ── Location policy ──────────────────────────────────────────────────────────

// A named city list, not "any capitalised word": a JD names a dozen places it
// is not hiring in — the HQ, the customers, the last funding round's office —
// and every one of them would cap an otherwise remote role.
var ukCities = []string{
	"london", "manchester", "leeds", "liverpool", "sheffield", "nottingham",
	"leicester", "bristol", "cardiff", "newcastle", "york", "cambridge", "oxford",
	"reading", "brighton", "southampton", "portsmouth", "edinburgh", "glasgow",
	"aberdeen", "dundee", "belfast", "dublin", "milton keynes", "swindon",
	"derby", "norwich", "exeter", "bath",
}

var (
	onsiteSignal    = regexp.MustCompile(`(?i)\bon-?site\b|\bhybrid\b|\bin[- ]office\b|\boffice[- ]based\b|\d\s*days?\s*(?:a|per)\s*week\s*(?:in|at|from)\b`)
	fullyRemote     = regexp.MustCompile(`(?i)\b(?:fully|100%|entirely|permanently)\s+remote\b|\bremote[- ]first\b|\bwork\s+from\s+anywhere\b`)
	monthlyCadence  = regexp.MustCompile(`(?i)\d+\s*days?\s*(?:a|per)\s*month|\bmonthly\s+(?:travel|visit|on-?site)`)
	weeklyCadence   = regexp.MustCompile(`(?i)\d+\s*days?\s*(?:a|per)\s*week|\bweekly\s+(?:travel|visit|on-?site)`)
	profileCityRe   = regexp.MustCompile(`(?m)^\s*city:\s*["']?([^"'\n]+)`)
	searchLocsRe    = regexp.MustCompile(`(?ms)^\s*search_locations:\s*\n(.*?)(\n\s*\w+:|$)`)
	remoteOnlyRe    = regexp.MustCompile(`(?i)remote\s+only`)
	listItemRe      = regexp.MustCompile(`^\s*-\s`)
	listItemStripRe = regexp.MustCompile(`^\s*-\s*["']?`)
)

// proximity is how far either side of a city name the on-site signal must sit.
const proximity = 160

// CommutableTerms are the places the candidate will physically attend an
// office, from config/profile.yml: location.city plus any search_locations
// entry that is not remote-only.
//
// That is the whole policy — one commutable base, everywhere else remote — so
// it is read from the user layer rather than hardcoded here.
func CommutableTerms(profileYML string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			terms = append(terms, s)
		}
	}
	if m := profileCityRe.FindStringSubmatch(profileYML); m != nil {
		add(m[1])
	}
	if m := searchLocsRe.FindStringSubmatch(profileYML); m != nil {
		for _, line := range strings.Split(m[1], "\n") {
			if !listItemRe.MatchString(line) || remoteOnlyRe.MatchString(line) {
				continue
			}
			place := listItemStripRe.ReplaceAllString(line, "")
			place = strings.FieldsFunc(place, func(r rune) bool {
				return r == '—' || r == '–' || r == '"' || r == '\''
			})[0]
			for _, part := range strings.Split(place, "/") {
				add(part)
			}
		}
	}
	return terms
}

// LocationMismatchCap caps hard when the JD requires office presence somewhere
// the candidate will not commute to.
//
// It exists because a detected hard stop was decoration: hard_stops never fed
// final_decision, so Phase 2 printed "hybrid or on-site outside the commutable
// base" verbatim on offers it then scored 4.9 and told the user to Apply to,
// with a tailored PDF. Capping both dimensions at 2 lands the composite at 2.0
// — under Phase 1's gate and under Phase 2's `< 3 ⇒ Skip`.
//
// Deliberately conservative: a false positive deletes a good offer, while a
// false negative only leaves noise the human discards. Both signals must be
// NEAR each other, which is how these ads are actually written. Testing them
// independently capped a posting that says it has *no* in-office requirement
// and lists London among its global offices, 1.7k characters apart.
//
// ponytail: a JD saying both "hybrid" and "fully remote" is left uncapped.
// Telling the two apart needs more than proximity, and no such offer has shown
// up yet.
func LocationMismatchCap(jdText, profileYML string) (Caps, string) {
	none := Caps{CV: 5, NS: 5}
	jd := strings.ToLower(jdText)
	commutable := CommutableTerms(profileYML)

	if len(commutable) == 0 {
		return none, "" // no stated policy, no gate
	}
	if !onsiteSignal.MatchString(jd) {
		return none, "" // nothing demands attendance
	}
	for _, t := range commutable {
		if strings.Contains(jd, t) {
			return none, ""
		}
	}
	if fullyRemote.MatchString(jd) {
		return none, ""
	}
	for _, city := range ukCities {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(city) + `\b`)
		for _, loc := range re.FindAllStringIndex(jd, -1) {
			w := window(jd, loc[0], loc[1], proximity)
			if !onsiteSignal.MatchString(w) {
				continue
			}
			// The policy allows a monthly cadence and refuses a weekly one, so
			// "6 days a month travel to office" is a yes and must not be capped
			// for saying "hybrid" in the same breath.
			if monthlyCadence.MatchString(w) && !weeklyCadence.MatchString(w) {
				continue
			}
			return Caps{CV: 2, NS: 2}, city
		}
	}
	return none, ""
}

// window is the text around a match, clamped to the string.
func window(s string, start, end, pad int) string {
	lo := start - pad
	if lo < 0 {
		lo = 0
	}
	hi := end + pad
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
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
	if pick == "none" || !sameActivity {
		return Gap
	}
	if tooling == "different" {
		return Transferable
	}
	if Catalogue(source) {
		return Transferable
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
