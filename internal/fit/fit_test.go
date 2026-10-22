package fit

import (
	"strings"
	"testing"
)

// These are the Node self-check's assertions, carried over. The corpus gate
// proves the rules agree on real JDs; these pin the edge cases the corpus does
// not happen to contain — each one is a bug that shipped.
//
// All fixtures are fictional: the assertions need only "a language the
// candidate has" and "one they do not".

const langCV = "**Languages**\n- English: Advanced (B2 certified)\n- Spanish: Native\n- Portuguese: Native\n"

const profileYML = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe / Mereside — on-site, hybrid or remote"
    - "United Kingdom — fully remote only"
    - "London — fully remote only"
`

// candidateEcosystems reads work, not the skills catalogue: a technology named
// only in the taxonomy has no bullet behind it anywhere on the CV.
func TestEcosystemsIgnoreTheSkillsCatalogue(t *testing.T) {
	cv := "## Experience\n- Built services in Java and Rust\n\n" +
		"## Skills\n**Languages:** Java, Rust, C#\n\n## Education\n- BEng\n"
	eco := CandidateEcosystems(cv)
	if !eco["java"] {
		t.Error("an ecosystem with a bullet behind it should count")
	}
	if eco["c#/.net"] {
		t.Error("a skills-only claim should not count as evidence")
	}
	if got := StackMismatchCap("C# developer wanted. Strong C# and .NET Core throughout.", cv, 0, 0); got.Cap != 3 {
		t.Errorf("cap %d, want 3 — a C#-only posting against a CV that only catalogues C#", got.Cap)
	}
}

// `\b` cannot fence `c#` or `.net`, so a naive pattern matches neither in any
// spelling a JD actually uses. This is what the lookaround emulation is for.
func TestCSharpDotNetDetection(t *testing.T) {
	cv := "Skills: Ruby, Rails, Elasticsearch, GCP, Docker"
	has := func(jd string) bool {
		for _, m := range StackMismatchCap(jd, cv, 0, 0).Missing {
			if m == "c#/.net" {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name, jd string
		want     bool
	}{
		{"bare C#", "C#/OO software engineer. Strong C# required.", true},
		{"bare .NET", "We build in .NET 8 and ship .NET services daily.", true},
		{"asp.net and dotnet", "ASP.NET Core and dotnet tooling throughout.", true},
		// Counted once, not twice — otherwise a single "C#.Net" trips minMentions alone.
		{"one C#.Net mention is below minMentions", "C#.Net is used here.", false},
		{"a .net TLD is not the ecosystem", "Apply at example.net or careers.foo.net today.", false},
		{"C++ is not C#", "We write C++ and Rust.", false},
	} {
		if got := has(tc.jd); got != tc.want {
			t.Errorf("%s: got %t, want %t", tc.name, got, tc.want)
		}
	}
}

// Adjacent occurrences must both count: encoding the trailing guard as a
// consumed character would make ".NET .NET" one match, because the delimiter
// ending the first is the one starting the second.
func TestAdjacentOccurrencesBothCount(t *testing.T) {
	cv := "Skills: Ruby, Rails"
	got := StackMismatchCap(".NET .NET", cv, 0, 0)
	if len(got.Missing) != 1 || got.Missing[0] != "c#/.net" {
		t.Errorf("missing=%v, want [c#/.net] — two adjacent mentions should reach minMentions", got.Missing)
	}
}

func TestStrengthFrom(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		pick                           string
		same                           bool
		tooling, req, evidence, source string
		want                           string
	}{
		{"same work, same tools", "A", true, "same", "", "", "", Strong},
		{"same work, another tool", "A", true, "different", "", "", "", Transferable},
		{"no technology named is a full match, not a partial one", "A", true, "not_applicable", "", "", "", Strong},
		{"different work is a Gap", "A", false, "different", "", "", "", Gap},
		{"a tooling match cannot rescue a different activity", "A", false, "same", "", "", "", Gap},
		{"no evidence picked outranks both axes", "none", true, "same", "", "", "", Gap},

		// A catalogue line cannot lose: it holds every technology it lists.
		// Over 573 rows from 60 reports, 184 cited one — 138 Strong, 46
		// Transferable, 0 Gap. A surface that never fails is not grading.
		{"a skills catalogue caps at Transferable", "A", true, "same", "AWS knowledge", "Skills — Cloud: AWS, Docker", "skills", Transferable},
		{"an education catalogue caps the same way", "A", true, "not_applicable", "A degree", "Education — Modules: ...", "education", Transferable},
		{"a work bullet still reaches Strong", "A", true, "same", "AWS knowledge", "Deployed services to AWS EC2", "experience", Strong},
		{"the cap never rescues a row already called a Gap", "A", false, "same", "", "", "skills", Gap},

		// The grounding guard: same_tooling is the model's own opinion and it is
		// optimistic — measured at ~7% of Strong rows citing evidence that never
		// mentions the technology the requirement names.
		{"a Java requirement is demoted when the evidence never mentions Java", "A", true, "same",
			"5+ years of Java full stack", "Awarded a departmental prize for the final-year project", "", Transferable},
		{"grounded row stays Strong", "A", true, "same",
			"Strong AWS and Linux expertise", "Skills — Cloud: AWS (Lambda, S3); OS: Linux server administration", "", Strong},
		{"a requirement naming nothing checkable is not taxed", "A", true, "same",
			"Excellent written and verbal communication", "Ran the weekly design review for the team", "", Strong},
		{"an alternatives list is a disjunction, so one match is enough", "A", true, "same",
			"Cloud platforms such as AWS, Azure or GCP", "Deployed the API on AWS Lambda", "", Strong},
		{"the guard never rescues", "none", true, "same", "Java", "Java everywhere", "", Gap},
	} {
		if got := StrengthFrom(tc.pick, tc.same, tc.tooling, tc.req, tc.evidence, tc.source); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestVerifyAgainstCv(t *testing.T) {
	cv := "Skills: Ruby, Rails, Elasticsearch, GCP, Docker"
	kept, dropped := VerifyAgainstCv([]string{
		"Ruby on Rails services backed by Elasticsearch",       // both present → keep
		"Strong communication with non-technical stakeholders", // names nothing → keep
		"Built and tuned Spark batch jobs",                     // Spark absent → drop
		"Deployed containers to Azure App Service",             // Azure absent → drop
	}, cv)
	if len(kept) != 2 || len(dropped) != 2 {
		t.Fatalf("kept %d, dropped %d; want 2 and 2\nkept=%v\ndropped=%v", len(kept), len(dropped), kept, dropped)
	}
	found := false
	for _, k := range kept {
		if strings.Contains(k, "non-technical") {
			found = true
		}
	}
	if !found {
		t.Error("a claim naming no technology should be left alone")
	}
	if k, _ := VerifyAgainstCv(nil, cv); len(k) != 0 {
		t.Error("empty input should be safe")
	}
}

func TestLooksMultiPosting(t *testing.T) {
	for _, tc := range []struct {
		jd   string
		want bool
	}{
		{"40+ top trading firms seeking exceptional engineers. Multiple immediate openings.", true},
		{"Ask HN: Who's hiring? (July 2026)", true},
		{"We have several openings across the platform team", true},
		{"Senior Rust Engineer. You will join a team of 12 engineers building trading systems.", false},
		{"Software Engineer — 5+ years experience with distributed systems required.", false},
		// Both are real JDs an earlier employer-scale pattern wrongly flagged. A
		// false positive caps a real offer at 2/2 and brands it Suspicious.
		{"Acme is Europe's leading freelance marketplace, connecting over 1,000,000 talented freelancers with 100,000+ companies.", false},
		{"You will own the AWS business within a portfolio of over 1000 clients.", false},
		{"", false},
	} {
		if got := LooksMultiPosting(tc.jd); got != tc.want {
			t.Errorf("%t for %q, want %t", got, truncate(tc.jd, 60), tc.want)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestStrengthFromTerms(t *testing.T) {
	for _, tc := range []struct {
		name, evidence, source string
		terms                  []string
		pick                   string
		want                   string
	}{
		{"a named tool the evidence never mentions", "Awarded a prize for the final-year project", "", []string{"Java"}, "A", Transferable},
		{"grounded", "Deployed services to AWS EC2", "", []string{"AWS"}, "A", Strong},
		{"one of the alternatives is enough", "Deployed the API on AWS Lambda", "", []string{"AWS", "Azure", "GCP"}, "A", Strong},
		{"symbols are part of the name", "Wrote the codec in C++", "", []string{"C++"}, "A", Strong},
		{"a name is a whole word", "Rewrote the Gopher client", "", []string{"Go"}, "A", Transferable},
		{"no terms is not taxed", "Ran the weekly review", "", nil, "A", Strong},
		{"a catalogue still caps", "Skills: AWS", "skills", []string{"AWS"}, "A", Transferable},
		{"never rescues a gap", "Java everywhere", "", []string{"Java"}, "none", Gap},
	} {
		if got := StrengthFromTerms(tc.pick, true, "same", tc.evidence, tc.source, tc.terms); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestCandidateEcosystemsIgnoresABoldSkillsCatalogue(t *testing.T) {
	cv := "**Experience**\n- Built the API in Java and Spring Boot\n\n**Skills**\n**Languages:** Java, C#, Rust\n**Data:** PostgreSQL\n\n**Education**\n- BEng\n"
	got := CandidateEcosystems(cv)
	if !got["java"] || got["c#/.net"] || got["rust"] {
		t.Errorf("only what the experience shows should count: %v", got)
	}
	// The old heading style still works, and a section after the catalogue is kept.
	md := "## Experience\n- Wrote Go services\n\n## Skills\nPython, Ruby\n\n## Projects\n- Built a Rust parser\n"
	if got := CandidateEcosystems(md); !got["rust"] || got["python"] || got["ruby"] {
		t.Errorf("## style: %v", got)
	}
}

func TestSeniorityCaps(t *testing.T) {
	if s := SeniorityCaps("Senior Security Engineering Manager", 0, ""); s.CV != 2 || s.NS != 3 {
		t.Errorf("manager title: %d/%d, want 2/3 — a management role is a track mismatch, not a stretch", s.CV, s.NS)
	}
	if s := SeniorityCaps("Software Engineer I", 0, ""); s.CV != 5 {
		t.Errorf("junior role capped at %d, want 5", s.CV)
	}
	for _, tc := range []struct{ years, want int }{{3, 5}, {4, 5}, {5, 3}, {7, 3}, {8, 2}, {10, 2}} {
		if s := SeniorityCaps("Software Engineer", tc.years, ""); s.CV != tc.want {
			t.Errorf("%d years: %d, want %d", tc.years, s.CV, tc.want)
		}
	}
	if s := SeniorityCaps("Software Engineer", 0, "Senior"); s.CV != 3 {
		t.Errorf("a parsed Senior level: %d, want 3", s.CV)
	}
}
