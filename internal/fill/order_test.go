package fill

import (
	"os"
	"strings"
	"testing"
)

const orderCV = `**Denys Skira**

## Summary
Some prose.

## Experience

### Engineer
- did a thing

## Projects

### Thing
- built it

## Education

### University
- studied

## Skills
**Languages:** Go
`

func tex(sections ...string) string {
	out := ""
	for _, s := range sections {
		out += "\\section{" + s + "}\n\\resumeSubHeadingListStart\n"
	}
	return out
}

func TestSectionOrderAcceptsTheShippedLayout(t *testing.T) {
	// Skills first is exactly what the template does, and it is allowed to:
	// that position is the template's to choose.
	doc := tex("Technical Skills", "Work Experience", "Projects", `Education \& Certifications`)
	if err := CheckSectionOrder(doc, orderCV); err != nil {
		t.Fatalf("the shipped order should pass: %v", err)
	}
}

func TestSectionOrderCatchesAReorder(t *testing.T) {
	doc := tex("Technical Skills", "Projects", "Work Experience", `Education \& Certifications`)
	err := CheckSectionOrder(doc, orderCV)
	if err == nil {
		t.Fatal("projects before experience contradicts cv.md and was accepted")
	}
	// The message has to name both orders — it is read by someone who has just
	// edited one of the two files and needs to know which.
	for _, want := range []string{"projects -> experience", "experience -> projects"} {
		if !contains(err.Error(), want) {
			t.Errorf("message %q does not carry %q", err, want)
		}
	}
}

// The whole reason for porting this: the Node original reads only `# ` headings.
func TestSectionOrderReadsBoldHeadings(t *testing.T) {
	bold := "**Denys Skira**\n\n**Experience**\n\n**Engineer**\n- x\n\n**Projects**\n\n**Thing**\n- y\n"
	if err := CheckSectionOrder(tex("Work Experience", "Projects"), bold); err != nil {
		t.Fatalf("bold headings should parse: %v", err)
	}
	if CheckSectionOrder(tex("Projects", "Work Experience"), bold) == nil {
		t.Error("a bold-heading CV silently checked nothing — which is the bug this replaces")
	}
}

func TestSectionOrderIgnoresWhatItCannotCompare(t *testing.T) {
	if err := CheckSectionOrder(tex("Work Experience"), orderCV); err != nil {
		t.Errorf("one comparable section cannot be out of order: %v", err)
	}
	if err := CheckSectionOrder(tex("Publications", "Awards"), orderCV); err != nil {
		t.Errorf("sections cv.md does not have are not ordered by it: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The shipped template against the real cv.md. Skipped where either is absent,
// which on a CI runner is always — cv.md is user data and is never committed.
func TestSectionOrderOnTheRealDocuments(t *testing.T) {
	tpl, err := os.ReadFile("../../templates/cv-template.tex")
	if err != nil {
		t.Skip("no template in this checkout")
	}
	cv, err := os.ReadFile("../../cv.md")
	if err != nil {
		t.Skip("no cv.md in this checkout")
	}
	// The four section headings render.go substitutes, with the certifications
	// spelling that only appears when cv.md has any.
	doc := strings.NewReplacer(
		"{{SECTION_EXPERIENCE}}", "Work Experience",
		"{{SECTION_PROJECTS}}", "Projects",
		"{{SECTION_EDUCATION}}", `Education \& Certifications`,
		"{{SECTION_SKILLS}}", "Technical Skills",
	).Replace(string(tpl))

	if err := CheckSectionOrder(doc, string(cv)); err != nil {
		t.Errorf("the shipped template and cv.md disagree: %v", err)
	}
}
