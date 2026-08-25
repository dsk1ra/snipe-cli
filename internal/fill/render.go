package fill

import (
	"regexp"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/latex"
)

var nonDialRe = regexp.MustCompile(`[^\d+]`)

// contact builds the header's run of `$|$`-separated \href calls.
//
// One token rather than ten, because every field is optional and threading that
// through the template would need `\href{mailto:{{EMAIL}}}{\underline{{{EMAIL}}}}`
// — four levels of brace that break the moment a field is blank.
func contact(c config.Candidate) string {
	href := func(target, label string) string {
		return `\href{` + latex.EscapeURL(target) + `}{\underline{` + latex.Escape(label) + `}}`
	}
	var parts []string
	if p := c.Phone.String(); p != "" {
		parts = append(parts, `\href{tel:`+latex.EscapeURL(nonDialRe.ReplaceAllString(p, ""))+`}{`+latex.Escape(p)+`}`)
	}
	if e := c.Email.String(); e != "" {
		parts = append(parts, href("mailto:"+e, e))
	}
	for _, u := range []string{c.LinkedIn.String(), c.GitHub.String()} {
		if u != "" {
			parts = append(parts, href(absoluteURL(u), u))
		}
	}
	if c.Location != "" {
		parts = append(parts, latex.Escape(c.Location))
	}
	return strings.Join(parts, ` $|$ `)
}

// Render merges the model's content and cv.md into the template.
func Render(template string, cv CV, c Content, cand config.Candidate, opt Opts) string {
	targetRole := ""
	if r := strings.TrimSpace(opt.TargetRole); r != "" {
		targetRole = `\small{\textit{` + latex.Escape(r) + `}} \\ \vspace{1pt}`
	}
	eduHeading := "Education"
	if len(cv.Certs) > 0 {
		eduHeading = `Education \& Certifications`
	}
	return strings.NewReplacer(
		"{{NAME}}", latex.Escape(cand.FullName),
		"{{TARGET_ROLE}}", targetRole,
		"{{CONTACT}}", contact(cand),
		"{{SECTION_EXPERIENCE}}", "Work Experience",
		"{{EXPERIENCE}}", BuildExperience(cv.Exp, c.Experience, opt.MaxBullets),
		"{{SECTION_PROJECTS}}", "Projects",
		"{{PROJECTS}}", BuildProjects(cv.Projects, c.projects(), opt.MaxProjBullets),
		"{{SECTION_EDUCATION}}", eduHeading,
		"{{EDUCATION}}", BuildEducation(cv.Edu, c.EducationModules, cv.Certs),
		"{{SECTION_SKILLS}}", "Technical Skills",
		"{{SKILLS}}", BuildSkills(ResolveSkills(cv.Skills, c, opt.MaxSkills, opt.CVText)),
	).Replace(template)
}
