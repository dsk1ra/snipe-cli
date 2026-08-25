package fill

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/latex"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Opts are the density knobs the page-fit ladder turns. 0 means "no cap".
type Opts struct {
	MaxSkills      int
	MaxBullets     int // per role
	MaxProjBullets int // per project
	TargetRole     string
	CVText         string // cv.md, for grounding curated skill items
}

func clamp[T any](xs []T, n int) []T {
	if n > 0 && len(xs) > n {
		return xs[:n]
	}
	return xs
}

// ── matching ─────────────────────────────────────────────────────────────────

var nonWord = regexp.MustCompile(`[^a-z0-9 ]`)

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " ")) {
		out[w] = true
	}
	return out
}

func jaccard(a, b string) float64 {
	sa, sb := wordSet(a), wordSet(b)
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa)
	for w := range sb {
		if !sa[w] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func matchProject(jsonName, cvName string) bool {
	j, c := strings.ToLower(jsonName), strings.ToLower(cvName)
	if strings.Contains(c, text.Clip(j, 20)) {
		return true
	}
	return jaccard(j, c) >= 0.35
}

// matchCompany refuses an empty side.
//
// `”.includes(”)` and `x.includes(”)` are both true, so a cv.md entry whose
// company failed to parse used to match the FIRST model entry — and every
// employer after it rendered the same bullets.
func matchCompany(jsonCompany, cvCompany string) bool {
	j, c := lower(jsonCompany), lower(cvCompany)
	if j == "" || c == "" {
		return false
	}
	return j == c || strings.Contains(c, j) || strings.Contains(j, c)
}

// ── list plumbing ────────────────────────────────────────────────────────────

// itemList wraps items in the template's list macros, or renders nothing.
//
// `\begin{itemize}\end{itemize}` with no \item is a LaTeX error ("Something's
// wrong--perhaps a missing \item"), not the harmless empty <ul> the HTML
// template used to emit. Every list the density ladder can empty goes through
// here, so an entry whose bullets are all trimmed away renders as a bare heading
// rather than failing the whole compile.
func itemList(items []string) string {
	var kept []string
	for _, it := range items {
		if it != "" {
			kept = append(kept, it)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	const indent = "      "
	return "\n" + indent + "\\resumeItemListStart\n" + strings.Join(kept, "\n") + "\n" + indent + "\\resumeItemListEnd"
}

func joinPipe(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ` $|$ `)
}

// ── Experience ───────────────────────────────────────────────────────────────

// BuildExperience renders ALL companies, reverse-chronological.
//
// UK CV convention, and the CV itself is the source of truth for the company
// list and its order — never reordered or silently dropped by the model. The
// model only supplies (or fails to supply, in which case the CV's own stand)
// bullets per company.
func BuildExperience(cvExp []Exp, jsonExp []ContentExp, maxBullets int) string {
	ordered := append([]Exp{}, cvExp...)
	// Compared rather than subtracted: two "Present" entries are +Inf on both
	// sides, and Infinity - Infinity is NaN, which V8 treats as "equal" and so
	// leaves in place. A stable sort on `>` does the same without the NaN.
	sort.SliceStable(ordered, func(i, j int) bool {
		return cvselect.PeriodEnd(ordered[i].Period) > cvselect.PeriodEnd(ordered[j].Period)
	})

	var out []string
	for _, e := range ordered {
		bullets := e.Bullets
		for _, je := range jsonExp {
			if matchCompany(je.Company, e.Company) {
				bullets = je.Bullets
				break
			}
		}
		// Capped per role — applies to model-provided AND cv-backfilled entries —
		// so the page-fit ladder reduces depth uniformly without gutting any one
		// role. Bullets are already ordered most-relevant-first.
		var items []string
		for _, b := range clamp(bullets, maxBullets) {
			items = append(items, "        \\resumeItem{"+latex.Bold(b)+"}")
		}
		// Company/period on the bold line, role/location italic underneath — the
		// template's own \resumeSubheading shape. The HTML template welded all
		// four onto one line because three stacked <div>s cost 51px a role; LaTeX
		// stacks two at 0.97\textwidth for far less, so the two-line form stands.
		out = append(out, fmt.Sprintf("    \\resumeSubheading\n      {%s}{%s}\n      {%s}{%s}%s",
			latex.Escape(e.Company), latex.Escape(e.Period),
			latex.Escape(e.Role), latex.Escape(e.Location), itemList(items)))
	}
	return strings.Join(out, "\n")
}

// ── Projects ─────────────────────────────────────────────────────────────────

var (
	schemeRe  = regexp.MustCompile(`(?i)^https?://`)
	stripHost = regexp.MustCompile(`(?i)^https?://|^www\.`)
	ghRe      = regexp.MustCompile(`(?i)^github\.com/(.+)$`)
	dotRunRe  = regexp.MustCompile(`\.\.\s`)
)

// absoluteURL: cv.md writes repo URLs bare ("github.com/dsk1ra/snipe-cli"). An
// href without a scheme resolves against the local file and points nowhere.
func absoluteURL(url string) string {
	s := strings.TrimSpace(url)
	if schemeRe.MatchString(s) {
		return s
	}
	return "https://" + s
}

// shortRepo drops the host for github.com.
//
// `github.com/Drakon4ik-Coder/okta-dashboard-back` is the longest string on the
// page, and the header already carries the GitHub profile. Any other host keeps
// its name, because there the domain is the information.
func shortRepo(url string) string {
	s := stripHost.ReplaceAllString(url, "")
	s = stripHost.ReplaceAllString(s, "")
	if m := ghRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSuffix(m[1], "/")
	}
	return s
}

// BuildProjects renders the selected projects, keeping the CV's own metadata.
//
// The model supplies which projects and (optionally) tailored bullets or a
// description; the tech line, period, badge and repo URL always come from cv.md.
func BuildProjects(cvProjects []Project, sel []ProjectPick, maxProjBullets int) string {
	type chosen struct {
		cv          Project
		description string
		bullets     []string
	}
	var picks []chosen
	if len(sel) > 0 {
		taken := map[int]bool{}
		for _, s := range sel {
			if strings.TrimSpace(s.Name) == "" {
				continue
			}
			for i, p := range cvProjects {
				if matchProject(s.Name, p.Name) {
					if !taken[i] {
						taken[i] = true
						picks = append(picks, chosen{p, s.Description, clamp(s.Bullets, maxProjBullets)})
					}
					break
				}
			}
		}
	} else {
		for _, p := range clamp(cvProjects, 4) {
			picks = append(picks, chosen{cv: p})
		}
	}

	var out []string
	for _, c := range picks {
		// The repo is a link, not text. A reviewer who wants to check a claim
		// should not have to retype a URL off a PDF — and the tech line is the
		// only place the CV offers one per project.
		repoTex := ""
		if c.cv.URL != "" {
			repoTex = fmt.Sprintf(`\href{%s}{\underline{%s}}`,
				latex.EscapeURL(absoluteURL(c.cv.URL)), latex.Escape(shortRepo(c.cv.URL)))
		}
		techLine := joinPipe(latexOrEmpty(c.cv.Tech), repoTex)

		// Bullets when the writer supplies them, one paragraph otherwise.
		//
		// The paragraph was the only shape for a long time, and it is where the
		// candidate's most distinctive evidence was going to die. Projects hold 24
		// of this CV's 33 selectable atoms and most of what a reviewer called a
		// differentiator, and squashing five selected bullets into a 35-55 word
		// blurb physically cannot carry them: measured against the Opus labels,
		// every differentiator lost on a sampled offer was a project bullet, and
		// differentiator_coverage sat at 0.17-0.50 while every falsity metric read
		// perfect. A list costs vertical space, which is what the density ladder is
		// for; losing the evidence costs the application.
		var body []string
		if len(c.bullets) > 0 {
			for _, b := range c.bullets {
				body = append(body, "        \\resumeItem{"+latex.Bold(b)+"}")
			}
		} else {
			text := strings.TrimSpace(c.description)
			if text == "" {
				// Joining two bullets with a bare space runs two sentences together
				// with no separator.
				text = dotRunRe.ReplaceAllString(strings.Join(clamp(c.cv.Bullets, 2), ". "), ". ")
			}
			body = append(body, "        \\resumeItem{"+latex.Bold(text)+"}")
		}
		if techLine != "" {
			body = append(body, "        \\resumeTechLine{"+techLine+"}")
		}

		head := joinPipe(`\textbf{`+latex.Escape(c.cv.Name)+`}`, emphOrEmpty(c.cv.Badge))
		out = append(out, fmt.Sprintf("    \\resumeProjectHeading\n      {%s}{%s}%s",
			head, latex.Escape(c.cv.Period), itemList(body)))
	}
	return strings.Join(out, "\n")
}

func latexOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return latex.Escape(s)
}

func emphOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return `\emph{` + latex.Escape(s) + `}`
}

// ── Education ────────────────────────────────────────────────────────────────

// labelled bolds up to the colon so the label reads as a heading for its row —
// "Achievement: 2nd place out of 24 teams". Bounded at 20 characters so a line
// that merely contains a colon mid-sentence is not carved up at it.
var labelRe = regexp.MustCompile(`^([^:]{1,20}):\s*([\s\S]*)$`)

func labelled(line string) string {
	if m := labelRe.FindStringSubmatch(line); m != nil {
		return `\textbf{` + latex.Escape(m[1]) + `:} ` + latex.Escape(m[2])
	}
	return latex.Escape(line)
}

var (
	emDashRe     = regexp.MustCompile(`\s*—\s*`)
	keyModulesRe = regexp.MustCompile(`(?i)^key modules\s*:`)
)

// BuildEducation renders the degrees, rewriting "Key Modules:" to the model's
// JD-relevant subset when it supplied one. Without the field the CV's own
// modules pass through verbatim.
func BuildEducation(edu []Edu, selectedModules []string, certs []Cert) string {
	var sel []string
	if len(selectedModules) > 0 {
		sel = selectedModules
	}
	// Certifications ride the last education entry as one more line. Their own
	// section cost a heading and a rule to carry two lines of content. Name and
	// year only — the awarding body repeats what the name already says.
	certLine := ""
	if len(certs) > 0 {
		var parts []string
		for _, c := range certs {
			parts = append(parts, joinNonEmpty(", ", c.Name, c.Year))
		}
		certLine = "Certifications: " + strings.Join(parts, " • ")
	}

	var out []string
	for i, e := range edu {
		// The degree line is "<Degree> — <Classification>"; splitting on the dash
		// and keeping [0] threw the classification away, so "First Class Honours
		// (80+ average)" — the strongest single credential a recent graduate has —
		// never reached the PDF.
		degreeMain := strings.TrimSpace(emDashRe.ReplaceAllString(e.Degree, " — "))

		var lines []string
		for _, line := range e.Extra {
			if sel != nil && keyModulesRe.MatchString(line) {
				line = "Key Modules: " + strings.Join(sel, ", ")
			}
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		if i == len(edu)-1 && certLine != "" {
			lines = append(lines, certLine)
		}

		var items []string
		for _, line := range lines {
			items = append(items, "        \\resumeItem{"+labelled(line)+"}")
		}
		// One row per line, not all of them joined with " • ". A CTF placing that
		// trails five module names behind a bullet separator reads as a footnote to
		// the module list; on its own row with its label in bold it reads as a
		// result.
		out = append(out, fmt.Sprintf("    \\resumeSubheading\n      {%s}{%s}\n      {%s}{}%s",
			latex.Escape(degreeMain), latex.Escape(e.Period), latex.Escape(e.School), itemList(items)))
	}
	return strings.Join(out, "\n")
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// ── Skills ───────────────────────────────────────────────────────────────────

var langCatRe = regexp.MustCompile(`(?i)^languages`)

// ResolveSkills decides which skill rows render.
//
// Anything the model picks is matched back to a real CV category, so the
// canonical name is used and invented categories are dropped. Curated items are
// grounded against cv.md; an empty items string falls back to the category's
// full CV items, so a bare category name still renders correctly.
func ResolveSkills(allRows []SkillRow, c Content, maxCount int, cvText string) []SkillRow {
	var resolved []SkillRow
	has := func(cat string) bool {
		for _, r := range resolved {
			if r.Category == cat {
				return true
			}
		}
		return false
	}
	// The model's category names are fuzzy; the CV's are canonical. First word,
	// case-insensitive, as a substring of the CV's category.
	findRow := func(name string) *SkillRow {
		key := strings.Split(lower(name), " ")[0]
		for i := range allRows {
			if strings.Contains(strings.ToLower(allRows[i].Category), key) {
				return &allRows[i]
			}
		}
		return nil
	}

	if picks, isArray := c.skills(); isArray {
		for _, s := range picks {
			name := strings.TrimSpace(s.Category)
			if name == "" {
				continue
			}
			cvRow := findRow(name)
			if cvRow == nil {
				continue // drop categories not in the CV
			}
			items := cvRow.Items
			if strings.TrimSpace(s.Items) != "" {
				items = GroundSkillItems(strings.TrimSpace(s.Items), cvRow.Items, cvText)
			}
			if !has(cvRow.Category) {
				resolved = append(resolved, SkillRow{cvRow.Category, items})
			}
		}
	} else {
		for _, name := range c.SelectedSkills {
			if r := findRow(name); r != nil {
				resolved = append(resolved, *r)
			}
		}
	}

	// Languages is not the model's to drop. It omitted it from the Neo4j CV whose
	// first requirement was "strong TypeScript / JavaScript" — leaving the shipped
	// skills block with no language row at all, the one row every ATS scans.
	for i := range allRows {
		if langCatRe.MatchString(allRows[i].Category) {
			if !has(allRows[i].Category) {
				resolved = append([]SkillRow{allRows[i]}, resolved...)
			}
			break
		}
	}

	// Keep the model's curation as priority. When it under-delivers, PAD with the
	// remaining CV categories verbatim up to the target — never dump every
	// category, which is the "everything stuffed in" symptom this replaced.
	target := maxCount
	if target == 0 {
		target = 6
	}
	if len(resolved) == 0 {
		resolved = clamp(allRows, target)
	} else if len(resolved) < min(4, target) {
		for _, r := range allRows {
			if len(resolved) >= target {
				break
			}
			if !has(r.Category) {
				resolved = append(resolved, r)
			}
		}
	}
	return clamp(resolved, maxCount)
}

func BuildSkills(rows []SkillRow) string {
	if len(rows) == 0 {
		return ""
	}
	var body []string
	for _, r := range rows {
		body = append(body, `     \textbf{`+latex.Escape(r.Category)+`}{: `+latex.Escape(r.Items)+`}`)
	}
	return " \\begin{itemize}[leftmargin=0.15in, label={}]\n    \\small{\\item{\n" +
		strings.Join(body, " \\\\\n") + "\n    }}\n \\end{itemize}"
}
