// Package fill merges the model's tailored JSON with cv.md into source.tex.
//
// cv.md is the source of truth for the *structure* — which employers exist, in
// what order, with what dates. The model supplies bullets per employer and may
// narrow the skills; it never adds an employer, reorders them, or invents a
// skill. Everything here enforces that asymmetry.
package fill

import (
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
)

type Exp struct {
	Role, Company, Location, Period string
	Bullets                         []string
}

type Project struct {
	Name, Badge, Tech, Period, URL string
	Bullets                        []string
}

type Edu struct {
	School, Degree, Period string
	Extra                  []string
}

type Cert struct{ Name, Org, Year string }

type SkillRow struct{ Category, Items string }

// CV is cv.md, parsed.
type CV struct {
	Exp      []Exp
	Projects []Project
	Edu      []Edu
	Certs    []Cert
	Skills   []SkillRow
}

// body returns a section's lines with its heading line dropped.
//
// cvselect.ParseSections keeps the heading in Lines[0], which the selector
// wants and this does not: "**Experience**" is shaped exactly like a role title
// and parseExp would open a bogus entry on it.
func body(secs []cvselect.Section, name string) []string {
	for _, s := range secs {
		if s.Name == name && len(s.Lines) > 0 {
			return s.Lines[1:]
		}
	}
	return nil
}

// ParseCV splits cv.md and parses every section the template needs.
func ParseCV(md string) CV {
	secs := cvselect.ParseSections(md)

	// Human languages live under their own heading but belong to the skills
	// block, so the marker line is re-inserted for parseSkills to switch on. The
	// Node implementation gets this for free by leaving Languages out of its
	// known-sections list, so the heading falls through as Skills content.
	skillLines := body(secs, "Skills")
	if lang := body(secs, "Languages"); len(lang) > 0 {
		skillLines = append(append(append([]string{}, skillLines...), "**Languages**"), lang...)
	}

	return CV{
		Exp:      parseExp(body(secs, "Experience")),
		Projects: parseProjects(body(secs, "Projects")),
		Edu:      parseEdu(body(secs, "Education")),
		Certs:    parseCerts(body(secs, "Certifications")),
		Skills:   parseSkills(skillLines),
	}
}

var (
	// The dash may be an em-dash, an en-dash or a plain ASCII hyphen. cv.md is a
	// user-layer file edited by hand, and matching only the typographic ones
	// fails SILENTLY: the line falls through to the role-title branch, which
	// does not match either, so company, location and period all render empty
	// and the CV ships with no employer names or dates on it. The empty company
	// then makes every JSON entry match the first role (see matchCompany), so
	// every employer gets the same bullets.
	companyLineRe = regexp.MustCompile(`^\*\*[^*]+\*\*\s*(?:—|–|-)\s`)
	companyRe     = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*(?:—|–|-)\s*([^|]+)\|\s*(.+)$`)
	boldAlone     = regexp.MustCompile(`^\*\*([^*]+)\*\*$`)
	hashHead      = regexp.MustCompile(`^###\s+(.+)$`)
	pipeHead      = regexp.MustCompile(`^\*\*[^*]+\*\*\s*\|`)
	projMetaRe    = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*\|\s*(.+?)\s*\|\s*(.+)$`)
	urlLineRe     = regexp.MustCompile(`^(https?://|github\.com/)`)
	degreeRe      = regexp.MustCompile(`^\*\*([^*]+)\*\*\s*\|\s*(.+)$`)
	kvLineRe      = regexp.MustCompile(`^\*\*[^*]+:\*\*|^\*\*[^*]+\*\*:`)
	skillRowRe    = regexp.MustCompile(`^\*\*([^*]+)\*\*:\s*(.+)$`)
	skillRowAltRe = regexp.MustCompile(`^\*\*([^*]+:\*\*)\s*(.*)$`)
	langHeadRe    = regexp.MustCompile(`^(\*\*Languages\*\*|##\s+Languages)$`)
	starsRe       = regexp.MustCompile(`\*\*`)
)

// head matches an entry title: "**Title**" alone, or "### Title".
func head(s string) (string, bool) {
	if m := boldAlone.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	if m := hashHead.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]), true
	}
	return "", false
}

func parseExp(lines []string) []Exp {
	var out []Exp
	var cur *Exp
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if companyLineRe.MatchString(s) {
			if m := companyRe.FindStringSubmatch(s); m != nil && cur != nil {
				cur.Company = strings.TrimSpace(m[1])
				cur.Location = strings.TrimSpace(m[2])
				cur.Period = strings.TrimSpace(m[3])
			}
			continue
		}
		if strings.HasPrefix(s, "- ") {
			if cur != nil {
				cur.Bullets = append(cur.Bullets, s[2:])
			}
			continue
		}
		if t, ok := head(s); ok {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Exp{Role: t}
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func parseProjects(lines []string) []Project {
	var out []Project
	var cur *Project
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if pipeHead.MatchString(s) {
			if m := projMetaRe.FindStringSubmatch(s); m != nil && cur != nil {
				cur.Badge = strings.TrimSpace(m[1])
				cur.Tech = strings.TrimSpace(m[2])
				cur.Period = strings.TrimSpace(m[3])
			}
			continue
		}
		if urlLineRe.MatchString(s) {
			if cur != nil {
				cur.URL = s
			}
			continue
		}
		if strings.HasPrefix(s, "- ") {
			if cur != nil {
				cur.Bullets = append(cur.Bullets, s[2:])
			}
			continue
		}
		if t, ok := head(s); ok {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Project{Name: t}
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func parseEdu(lines []string) []Edu {
	var out []Edu
	var cur *Edu
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if pipeHead.MatchString(s) {
			if m := degreeRe.FindStringSubmatch(s); m != nil && cur != nil {
				cur.Degree = strings.TrimSpace(m[1])
				cur.Period = strings.TrimSpace(m[2])
			}
			continue
		}
		if kvLineRe.MatchString(s) {
			if cur != nil {
				cur.Extra = append(cur.Extra, strings.TrimSpace(starsRe.ReplaceAllString(s, "")))
			}
			continue
		}
		if boldAlone.MatchString(s) {
			if cur != nil {
				out = append(out, *cur)
			}
			// s[2:len(s)-2], as the Node does — the regex already proved the shape.
			cur = &Edu{School: s[2 : len(s)-2]}
			continue
		}
		if cur != nil {
			cur.Extra = append(cur.Extra, starsRe.ReplaceAllString(s, ""))
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

func parseCerts(lines []string) []Cert {
	var out []Cert
	sep := regexp.MustCompile(`\s*—\s*`)
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if !strings.HasPrefix(s, "- ") {
			continue
		}
		p := sep.Split(s[2:], -1)
		at := func(i int) string {
			if i < len(p) {
				return strings.TrimSpace(p[i])
			}
			return ""
		}
		out = append(out, Cert{Name: at(0), Org: at(1), Year: at(2)})
	}
	return out
}

func parseSkills(lines []string) []SkillRow {
	var rows []SkillRow
	var humanLangs []string
	humanLangMode := false

	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if langHeadRe.MatchString(s) {
			humanLangMode = true
			continue
		}
		if humanLangMode && strings.HasPrefix(s, "- ") {
			humanLangs = append(humanLangs, s[2:])
			continue
		}
		if strings.HasPrefix(s, "![](data:image") {
			continue
		}
		if m := skillRowRe.FindStringSubmatch(s); m != nil {
			rows = append(rows, SkillRow{strings.TrimSpace(m[1]), strings.TrimSpace(m[2])})
			continue
		}
		if m := skillRowAltRe.FindStringSubmatch(s); m != nil {
			cat := strings.TrimSuffix(starsRe.ReplaceAllString(m[1], ""), ":")
			rows = append(rows, SkillRow{strings.TrimSpace(cat), strings.TrimSpace(m[2])})
		}
	}
	if len(humanLangs) > 0 {
		rows = append(rows, SkillRow{"Human Languages", strings.Join(humanLangs, ", ")})
	}
	return rows
}
