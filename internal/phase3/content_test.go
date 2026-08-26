package phase3

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/fill"
)

const testCV = `# X

**Experience**

**Engineer**
**Acme** - London | Jan 2024 – Present

- Shipped a thing serving 170 users
- Cut latency by 80%

**Projects**

**Widget**
**Personal** | Rust | 2025

- Built a Rust widget with AES-256-GCM encryption and a lock-free ring buffer
- Benchmarked it across 7 schemes and 5 payload sizes with bootstrap CIs

**Education**

**Napier**
BEng | 2022 - 2026

**Key Modules:** Concurrent Systems, Web Technologies, Secure Software Development, Databases, Algorithms

**Skills**

**Languages:** Rust, Java, Python
**Security:** AES-256-GCM, OAuth 2.0, JWT
`

// The writer is verbatim: every string it emits has to have come out of the CV,
// because that grounding is why Phase 3 runs no fabrication guards.
func TestVerbatimContentIsLiftedFromTheCV(t *testing.T) {
	c := VerbatimContent(testCV, testCV, "We need Rust and AES-256-GCM encryption.", 4)

	if len(c.Experience) != 1 || c.Experience[0].Company != "Acme" {
		t.Fatalf("experience is %+v, want one entry for Acme", c.Experience)
	}
	want := []string{"Shipped a thing serving 170 users", "Cut latency by 80%"}
	if strings.Join(c.Experience[0].Bullets, "|") != strings.Join(want, "|") {
		t.Errorf("bullets are %q, want them verbatim: %q", c.Experience[0].Bullets, want)
	}

	var projects []fill.ProjectPick
	if err := json.Unmarshal(c.Projects, &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "Widget" {
		t.Fatalf("projects are %+v, want one named Widget", projects)
	}
	if len(projects[0].Bullets) != 2 {
		t.Errorf("project kept %d bullets, want both", len(projects[0].Bullets))
	}
	// Padded from the project's own CV clauses rather than left blank, so the
	// template has a paragraph to fall back on.
	if !strings.Contains(projects[0].Description, "Rust widget") {
		t.Errorf("description is %q, want it built from the CV's own clauses", projects[0].Description)
	}

	var skills []SkillPick
	if err := json.Unmarshal(c.Skills, &skills); err != nil {
		t.Fatal(err)
	}
	if len(skills) == 0 {
		t.Error("no skills selected")
	}
}

// projectBullets is a ceiling, not a count — cv-select has already allocated
// the budget across the projects it kept.
func TestVerbatimContentCapsProjectBullets(t *testing.T) {
	c := VerbatimContent(testCV, testCV, "Rust.", 1)
	var projects []fill.ProjectPick
	if err := json.Unmarshal(c.Projects, &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects[0].Bullets) != 1 {
		t.Errorf("kept %d bullets under a ceiling of 1", len(projects[0].Bullets))
	}
}

func TestEducationModulesRankAgainstThePosting(t *testing.T) {
	got := EducationModules(testCV, "You will work on secure software development and databases at scale.", 2)
	if len(got) != 2 || got[0] != "Secure Software Development" || got[1] != "Databases" {
		t.Errorf("got %q, want the two the posting names, in CV order on ties", got)
	}
	// The wart, asserted so a refactor cannot quietly remove it: this ranker uses
	// local-pdf-offer.mjs's tokenizer, which does not trim trailing dots, so a
	// term the posting happens to end a sentence on scores nothing. The writer's
	// tokenizer does trim them — sharing it here would silently re-rank the
	// module list. See moduleTokens.
	got = EducationModules(testCV, "You will work on databases.", 1)
	if len(got) != 1 || got[0] != "Concurrent Systems" {
		t.Errorf("got %q — a sentence-final term is expected NOT to match, "+
			"falling back to the CV's first module", got)
	}
	// Nothing in common: the CV's own first n, because an arbitrary list still
	// beats an empty line where the modules go.
	got = EducationModules(testCV, "We sell artisanal cheese.", 2)
	if len(got) != 2 || got[0] != "Concurrent Systems" {
		t.Errorf("got %q, want the CV's first two", got)
	}
	if EducationModules("no modules here", "anything", 3) != nil {
		t.Error("a CV with no module line should yield nothing, not an empty label")
	}
}
