// Package cvselect ranks CV bullets against a posting's requirements and
// rations them against the page.
//
// Every constant here is measured rather than chosen. The comments say what was
// measured and how, because the numbers look arbitrary and are not: changing
// one without re-measuring silently changes what reaches the page.
package cvselect

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf16"
)

// ── Page geometry ────────────────────────────────────────────────────────────

// charsPerLine is how many characters of a bullet fit on one rendered line.
//
// Measured, not read off the template: all 33 real cv.md bullets were measured
// with \settototalheight in the real list nesting, and 108 was exact on 33 of
// 33. It fell from 124 with the move to Jake's Resume, whose nested itemize sets
// bullets \small in a 497.5pt column — narrower than the old HTML template's
// 708px box, so each bullet costs about 15% more lines.
const charsPerLine = 108

// bulletOverhead is what a \resumeItem costs on top of its line height, in
// line-heights.
//
// Also measured: a list of 4 versus 12 identical one-line bullets differs by
// 14.00pt each, and the same list of two-line bullets by 26.00pt — so a text
// line is exactly 12pt and the fixed cost per bullet is exactly 2pt.
//
// Counting only line height understates a nine-bullet page by more than a line
// and a thirteen-bullet page by nearly two, which is how the first knapsack run
// overran on 6 of 32 offers by one to two lines apiece while a line-only model
// insisted every one fitted.
const bulletOverhead = 2.0 / 12.0

// BulletLines is how many rendered lines a bullet takes.
//
// Length is counted in UTF-16 code units, matching String.prototype.length: the
// corpus is full of em dashes and £, three bytes each in UTF-8 and one unit
// here, so counting bytes would inflate every bullet containing one and ration
// the page against a document nobody is rendering.
func BulletLines(text string) int {
	n := len(utf16.Encode([]rune(text)))
	return max(1, int(math.Ceil(float64(n)/charsPerLine)))
}

// BulletCost is what a bullet costs the page, in line-heights, margin included.
// The budget is measured in the same unit, so cost and budget agree only if the
// overhead is charged here.
func BulletCost(text string) float64 {
	return float64(BulletLines(text)) + bulletOverhead
}

// ── CV parsing ───────────────────────────────────────────────────────────────

var (
	sectionRe = regexp.MustCompile(`^##\s+(.+)$`)
	entryRe   = regexp.MustCompile(`^###\s+`)
	bulletRe  = regexp.MustCompile(`^-\s+`)
	titleRe   = regexp.MustCompile(`(?m)^###\s+(.+)$`)
	// A bold-alone line. cv.md is a hand-written document and both spellings
	// occur; see sectionNames.
	boldAloneRe = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*$`)
)

// sectionNames is what tells a bold section heading from a bold entry heading.
//
// The two are the same shape — "**Experience**" and "**Teaching Assistant**" —
// so the only way to disambiguate is a known list, which is exactly what the
// atom splitter already does. A name in this list is a section; any other
// bold-alone line is an entry.
var sectionNames = map[string]bool{
	"Summary": true, "Experience": true, "Projects": true, "Education": true,
	"Certifications": true, "Skills": true, "Languages": true,
}

// Section is one `## ` block of cv.md, with its heading line included in Lines.
type Section struct {
	Name  string
	Lines []string
}

// ParseSections splits cv.md into its sections. The first has an empty name and
// holds everything before the first heading — the contact block.
//
// Both heading spellings are accepted: "## Experience" and "**Experience**".
//
// This is a deliberate divergence from the Node implementation, which matches
// only "## ". cv.md moved to the bold spelling on 2026-08-18 and the Node
// selector has silently returned the CV unchanged on every run since — no
// ranking, no spike, no judge rerank, no line budget. Two entirely different
// postings produced tailored CVs differing in four lines: the job title and the
// Key Modules list. Everything measured about Phase 3 selection was inert.
//
// The failure is silent because returning the input unchanged is also the
// correct response to "this CV has no Experience or Projects section". A parser
// cannot tell "absent" from "spelled differently", so it must accept both
// spellings rather than guess.
func ParseSections(cv string) []Section {
	var out []Section
	cur := Section{}
	flush := func(name, line string) {
		out = append(out, cur)
		cur = Section{Name: name, Lines: []string{line}}
	}
	for _, l := range strings.Split(cv, "\n") {
		if m := sectionRe.FindStringSubmatch(l); m != nil {
			flush(strings.TrimSpace(m[1]), l)
			continue
		}
		if m := boldAloneRe.FindStringSubmatch(l); m != nil {
			if name := strings.TrimSpace(m[1]); sectionNames[name] {
				flush(name, l)
				continue
			}
		}
		cur.Lines = append(cur.Lines, l)
	}
	return append(out, cur)
}

// isEntryHead reports whether a line opens an entry: "### Role" or a bold-alone
// line that is not a section name.
func isEntryHead(l string) bool {
	if entryRe.MatchString(l) {
		return true
	}
	m := boldAloneRe.FindStringSubmatch(l)
	return m != nil && !sectionNames[strings.TrimSpace(m[1])]
}

// Entry is one `### ` entry: a role or a project.
type Entry struct {
	Head    []string
	Bullets []string
	// Scored is filled in by the ranker; declared here so the shape is complete
	// at construction rather than grown by assignment.
	Scored []ScoredBullet
	Score  float64
}

type ScoredBullet struct {
	Text  string
	Score float64
}

// Parsed is a section split into its entries, keeping the preamble.
type Parsed struct {
	Head    []string
	Entries []*Entry
}

// ParseEntries splits a section's lines into entries and their bullets.
//
// A line after the first bullet that is not itself a bullet is a wrapped
// continuation and is appended to the previous one — cv.md wraps long bullets
// in the source, and treating each wrapped line as its own bullet would both
// multiply the count and make every fragment score separately.
func ParseEntries(lines []string) Parsed {
	if len(lines) == 0 {
		return Parsed{}
	}
	p := Parsed{Head: []string{lines[0]}}
	var cur *Entry
	for _, l := range lines[1:] {
		switch {
		case isEntryHead(l):
			cur = &Entry{Head: []string{l}}
			p.Entries = append(p.Entries, cur)
		case cur == nil:
			p.Head = append(p.Head, l)
		case bulletRe.MatchString(l):
			cur.Bullets = append(cur.Bullets, strings.TrimSpace(bulletRe.ReplaceAllString(l, "")))
		case len(cur.Bullets) == 0:
			if strings.TrimSpace(l) != "" {
				cur.Head = append(cur.Head, l)
			}
		default:
			if s := strings.TrimSpace(l); s != "" {
				cur.Bullets[len(cur.Bullets)-1] += " " + s
			}
		}
	}
	return p
}

// Render turns a parsed section back into markdown lines.
func Render(p Parsed) []string {
	out := append(append([]string{}, p.Head...), "")
	for _, e := range p.Entries {
		out = append(out, e.Head...)
		out = append(out, "")
		for _, b := range e.Bullets {
			out = append(out, "- "+b)
		}
		out = append(out, "")
	}
	return out
}

// ── Reverse-chronological ordering ───────────────────────────────────────────

var months = map[string]int{
	"jan": 0, "feb": 1, "mar": 2, "apr": 3, "may": 4, "jun": 5,
	"jul": 6, "aug": 7, "sep": 8, "oct": 9, "nov": 10, "dec": 11,
}

var (
	presentRe   = regexp.MustCompile(`(?i)present`)
	monthYearRe = regexp.MustCompile(`([A-Za-z]{3,9})\s+(\d{4})`)
	yearRe      = regexp.MustCompile(`(\d{4})`)
	rangeRe     = regexp.MustCompile(`([A-Za-z0-9 ]+?)\s*[–-]\s*([A-Za-z0-9 ]+)\s*$`)
	digitYearRe = regexp.MustCompile(`\d{4}`)
)

// dateToken converts a date fragment to a sortable month count. "Present" sorts
// above every real date; an unparseable token sorts below every one.
func dateToken(token string) float64 {
	token = strings.TrimSpace(token)
	if presentRe.MatchString(token) {
		return math.Inf(1)
	}
	if m := monthYearRe.FindStringSubmatch(token); m != nil {
		year := atoi(m[2])
		return float64(year*12 + months[strings.ToLower(m[1][:3])])
	}
	if m := yearRe.FindStringSubmatch(token); m != nil {
		return float64(atoi(m[1]) * 12)
	}
	return math.Inf(-1)
}

// EntryEndDate reads the end of an entry's date range. The head's second line
// carries "… | <start> – <end>".
func EntryEndDate(e *Entry) float64 {
	line := ""
	for _, l := range e.Head {
		if digitYearRe.MatchString(l) {
			line = l
			break
		}
	}
	parts := strings.Split(line, "|")
	last := parts[len(parts)-1]
	if m := rangeRe.FindStringSubmatch(last); m != nil {
		return dateToken(m[2])
	}
	return dateToken(last)
}

// UnmatchedPins reports pins that match no `### ` title.
//
// A pin is a user-layer override of the ranker and cv.md is a user-layer file,
// so a project can be renamed in one without the other knowing — and the
// failure mode is the worst kind, a feature that appears to work and quietly
// does nothing. One real pin sat against a renamed title for five days and 16
// runs. The warning existed from the first commit and fired every time, into a
// per-offer log opened only when an offer fails: loud in the wrong room. This is
// exported so the runner can ask once at startup, where a config error belongs.
func UnmatchedPins(cv string, pins []string) []string {
	var titles []string
	for _, m := range titleRe.FindAllStringSubmatch(cv, -1) {
		titles = append(titles, strings.ToLower(strings.TrimSpace(m[1])))
	}
	// Bold-spelled entry titles count too, or every pin reads as unmatched on a
	// CV written that way.
	for _, l := range strings.Split(cv, "\n") {
		if m := boldAloneRe.FindStringSubmatch(l); m != nil {
			if name := strings.TrimSpace(m[1]); !sectionNames[name] {
				titles = append(titles, strings.ToLower(name))
			}
		}
	}
	var missed []string
	for _, p := range pins {
		want := strings.ToLower(strings.TrimSpace(p))
		if want == "" {
			continue
		}
		found := false
		for _, t := range titles {
			if strings.Contains(t, want) {
				found = true
				break
			}
		}
		if !found {
			missed = append(missed, want)
		}
	}
	return missed
}

// ── Block B ──────────────────────────────────────────────────────────────────

var blockBHeaderRe = regexp.MustCompile(`(?i)^\|\s*JD Requirement\s*\|\s*Candidate evidence\s*\|`)
var mustMarkerRe = regexp.MustCompile(`(?i)\*\*\[must\]\*\*`)

// ExtractBlockBRequirements pulls the requirement column out of a Phase 2
// report's evidence table. Requirements of eight characters or fewer are
// dropped as table artifacts rather than demands.
func ExtractBlockBRequirements(report string) []string {
	lines := strings.Split(report, "\n")
	start := -1
	for i, l := range lines {
		if blockBHeaderRe.MatchString(l) {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}
	var out []string
	for j := start + 2; j < len(lines) && strings.HasPrefix(lines[j], "|"); j++ {
		cells := strings.Split(lines[j], "|")
		if len(cells) < 2 {
			continue
		}
		req := strings.TrimSpace(mustMarkerRe.ReplaceAllString(cells[1], ""))
		if len(req) > 8 {
			out = append(out, req)
		}
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// PeriodEnd reads the end of a "Oct 2024 – Sep 2025" / "Sep 2025 – Present"
// range. The em-dash is deliberately not a separator here: cv.md uses it between
// a degree and its classification, never inside a date range.
var periodSplitRe = regexp.MustCompile(`[–-]`)

func PeriodEnd(period string) float64 {
	parts := periodSplitRe.Split(period, -1)
	return dateToken(parts[len(parts)-1])
}
