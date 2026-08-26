package fill

import (
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/latex"
)

// The three dashes cv.md is written with. Matching only the typographic ones
// fails silently: the line falls through to the role-title branch, which does
// not match either, so the CV ships with no employer names or dates on it.
func TestCompanyLineParsesWithEveryDash(t *testing.T) {
	for _, dash := range []string{"—", "–", "-"} {
		got := parseExp([]string{
			"### Teaching Assistant",
			"**Napier University** " + dash + " Edinburgh, UK | Sep 2025 – Present",
			"- Taught 800+ undergraduates",
		})
		if len(got) != 1 {
			t.Fatalf("%q: %d entries", dash, len(got))
		}
		e := got[0]
		if e.Company != "Napier University" || e.Location != "Edinburgh, UK" ||
			e.Period != "Sep 2025 – Present" || len(e.Bullets) != 1 {
			t.Errorf("%q parsed as %+v", dash, e)
		}
	}
}

// An entry that failed to parse must match nothing, or it takes the first model
// entry's bullets and every employer renders the same ones.
func TestEmptyCompanyMatchesNothing(t *testing.T) {
	cases := []struct {
		j, c string
		want bool
	}{
		{"Acme", "Acme", true},
		{"Acme Ltd", "Acme", true},
		{"Acme", "", false},
		{"", "Acme", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := matchCompany(c.j, c.c); got != c.want {
			t.Errorf("matchCompany(%q, %q) = %v", c.j, c.c, got)
		}
	}
}

// An empty itemize is a LaTeX error, not an empty list.
func TestItemListRendersNothingWhenEmpty(t *testing.T) {
	if got := itemList(nil); got != "" {
		t.Errorf("empty list rendered %q", got)
	}
	if got := itemList([]string{"", ""}); got != "" {
		t.Errorf("all-blank list rendered %q", got)
	}
	if got := itemList([]string{`  \resumeItem{x}`}); !strings.Contains(got, `\resumeItemListStart`) {
		t.Errorf("items rendered no list: %q", got)
	}
}

// The model may narrow the CV's items; it may never add to them.
func TestGroundSkillItems(t *testing.T) {
	const cv = "**Cloud & Infrastructure:** AWS (EC2, Lambda, S3, IAM), Docker, Kubernetes\n" +
		"**Backend:** Event-Driven Architecture, Saga Pattern\n"
	const row = "AWS (EC2, Lambda, S3, IAM), Docker, Kubernetes"
	const langCV = cv + "**Languages:** Rust, Java, C/C++, C#\n"
	const langRow = "Rust, Java, C/C++, C#"

	cases := []struct{ curated, row, cv, want, why string }{
		{"Docker, Terraform, Kubernetes", row, cv, "Docker, Kubernetes", "invented tool dropped"},
		{"AWS (EC2, Lambda), Docker", row, cv, "AWS (EC2, Lambda), Docker", "commas inside parens do not split"},
		{"Event-Driven Architecture", row, cv, "Event-Driven Architecture", "hyphenated item is not an unknown word"},
		{"Terraform, Genesys", row, cv, row, "an entirely invented row falls back to the CV"},
		{"Rust, R", langRow, langCV, "Rust", "single-letter invention is not waved through on length"},
		{"C, C#", langRow, langCV, "C, C#", "a real single-letter language grounds on its own token"},
		{"Docker, ()", row, cv, "Docker", "an item with no head is junk"},
	}
	for _, c := range cases {
		if got := GroundSkillItems(c.curated, c.row, c.cv); got != c.want {
			t.Errorf("%s: GroundSkillItems(%q) = %q, want %q", c.why, c.curated, got, c.want)
		}
	}
}

// The metric bolding and the ATS pass both have to survive the escaper, and the
// bolding has to happen before it.
func TestEscapingAndBolding(t *testing.T) {
	cases := []struct{ in, want, why string }{
		{"100% & <C++>", `100\% \& \textless{}C++\textgreater{}`, "specials escape"},
		{"a — b", "a - b", "em-dash normalised before escaping"},
		{"CEH • 2025", `CEH \textbar{} 2025`, "a bullet becomes an escaped pipe, not a bare one"},
	}
	for _, c := range cases {
		if got := latex.Escape(c.in); got != c.want {
			t.Errorf("%s: %q → %q, want %q", c.why, c.in, got, c.want)
		}
	}
}

// The model's category is fuzzy; the CV's is canonical. An invented one is
// dropped, and Languages is put back whatever the model said.
func TestResolveSkillsDropsInventedAndRestoresLanguages(t *testing.T) {
	rows := []SkillRow{
		{"Languages", "Go, Rust"},
		{"Security", "OAuth 2.0"},
		{"Databases", "PostgreSQL"},
	}
	c := Content{Skills: []byte(`[{"category":"Security stuff","items":""},{"category":"Astrology","items":"x"}]`)}
	got := ResolveSkills(rows, c, 0, "Go, Rust, OAuth 2.0, PostgreSQL")

	if len(got) == 0 || got[0].Category != "Languages" {
		t.Fatalf("Languages is not first: %+v", got)
	}
	for _, r := range got {
		if r.Category == "Astrology" {
			t.Error("an invented category reached the CV")
		}
	}
}
