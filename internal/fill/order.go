package fill

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
)

// sectionAliases folds the template's wording into cv.md's. The document says
// "Work Experience" because that is what an ATS keyword scan expects to read;
// cv.md says "Experience" because that is what a person writing it types.
var sectionAliases = map[string]string{
	"professional summary":       "summary",
	"core competencies":          "competencies",
	"work experience":            "experience",
	"professional experience":    "experience",
	"selected projects":          "projects",
	"personal projects":          "projects",
	"education & certifications": "education",
	"technical skills":           "skills",
}

// templateOrdered are sections the template places itself, so cv.md's ordering
// does not bind them.
//
// Skills renders directly under the header wherever cv.md happens to put it:
// that position is a measured layout decision, not a content one. It used to
// render dead last, below Certifications on page 2 — the worst spot on the
// document for the one section a keyword scan reads first.
var templateOrdered = map[string]bool{"skills": true}

var texSectionRe = regexp.MustCompile(`(?m)^\s*\\section\{([^{}]*)\}`)

// CheckSectionOrder refuses a rendered .tex whose sections run in a different
// order than cv.md's. Everything the template does not place itself has to
// follow the source document, which is what stops a template edit silently
// reordering someone's CV.
//
// Ported from generate-pdf.mjs with one change, and it is the point of porting
// it: the Node version reads only `# ` headings, so against a bold-heading
// cv.md it finds no sections, returns early, and has been checking nothing.
// cvselect.ParseSections reads both spellings.
func CheckSectionOrder(tex, cvMD string) error {
	rendered := renderedSections(tex)

	pos := map[string]int{}
	var sourceOrder []string
	for i, s := range cvselect.ParseSections(cvMD) {
		key := sectionKey(s.Name)
		if _, seen := pos[key]; seen {
			continue
		}
		pos[key] = i
		sourceOrder = append(sourceOrder, key)
	}
	if len(rendered) < 2 || len(pos) < 2 {
		return nil
	}

	// Only sections that exist on both sides and that cv.md actually orders can
	// disagree. A section the template invents has no source position to
	// contradict, and one cv.md holds but the template drops cannot move.
	var comparable []string
	for _, key := range rendered {
		if _, ok := pos[key]; ok && !templateOrdered[key] {
			comparable = append(comparable, key)
		}
	}
	if len(comparable) < 2 {
		return nil
	}
	for i := 1; i < len(comparable); i++ {
		if pos[comparable[i]] < pos[comparable[i-1]] {
			return fmt.Errorf(
				"CV section order diverges from cv.md: rendered %s; cv.md %s",
				strings.Join(comparable, " -> "), strings.Join(inOrder(sourceOrder, comparable), " -> "))
		}
	}
	return nil
}

func renderedSections(tex string) []string {
	var out []string
	for _, m := range texSectionRe.FindAllStringSubmatch(tex, -1) {
		// `\&` is how "Education & Certifications" reaches the .tex.
		if key := sectionKey(strings.ReplaceAll(m[1], `\&`, "&")); key != "" {
			out = append(out, key)
		}
	}
	return out
}

var titleNoise = strings.NewReplacer("*", "", "_", "", "`", "", "~", "")

func sectionKey(title string) string {
	t := strings.ToLower(strings.Join(strings.Fields(titleNoise.Replace(title)), " "))
	if alias, ok := sectionAliases[t]; ok {
		return alias
	}
	return t
}

// inOrder renders the cv.md side of the complaint: the same sections, in the
// order the source puts them, so the two lines can be read against each other.
func inOrder(source, want []string) []string {
	keep := map[string]bool{}
	for _, k := range want {
		keep[k] = true
	}
	var out []string
	for _, k := range source {
		if keep[k] {
			out = append(out, k)
		}
	}
	return out
}
