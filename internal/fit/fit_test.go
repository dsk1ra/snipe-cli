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

func TestLanguageMismatchCap(t *testing.T) {
	for _, tc := range []struct {
		name, jd, want string
		cap            int
	}{
		{"german-speaking role capped", "Software Engineer, Agent (German speaking). Professional fluency in both German and English.", "german", 2},
		{"a language the candidate has is fine", "Fluency in Spanish required for client calls", "", 5},
		{"nice-to-have is not a requirement", "German is a plus but not required. Fluent German would be a bonus.", "", 5},
		{"naming a market is not demanding a language", "We serve the German market from our Berlin office.", "", 5},
	} {
		caps, missing := LanguageMismatchCap(tc.jd, langCV)
		if missing != tc.want || caps.CV != tc.cap {
			t.Errorf("%s: got %q cap %d, want %q cap %d", tc.name, missing, caps.CV, tc.want, tc.cap)
		}
	}
}

const profileYML = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe / Mereside — on-site, hybrid or remote"
    - "United Kingdom — fully remote only"
    - "London — fully remote only"
`

func TestCommutableTerms(t *testing.T) {
	got := strings.Join(CommutableTerms(profileYML), ",")
	if got != "ashcombe,mereside" {
		t.Errorf("got %q, want \"ashcombe,mereside\" — a remote-only search location is not a commutable base", got)
	}
}

func TestLocationMismatchCap(t *testing.T) {
	for _, tc := range []struct {
		name, jd, wantCity string
	}{
		{"a hybrid role in a city we will not commute to", "Location: London (Hybrid Working). Rust as a main language.", "london"},
		{"weekly commute", "Location & Work Model: London, UK | Hybrid (3 days per week in the office)", "london"},
		{"any non-commutable city, not just London", "Location: Glasgow. Work Setup: Hybrid - 3 days in the office", "glasgow"},
		{"the commutable base is never capped", "Backend Engineer, Ashcombe. Hybrid, 3 days a week in the office.", ""},
		{"fully remote naming a city is not capped", "Fully remote (UK). The team meets in London twice a year.", ""},
		{"a monthly cadence is inside the travel policy", "Locations: London, Essex | Hybrid: 6 days a month travel to office", ""},
		{"an on-site signal naming no city", "Backend Engineer. Hybrid working available.", ""},
		{"near-adjacent signals still cap", "Location: London. Hybrid working, 2 days a week in the office.", "london"},
	} {
		caps, city := LocationMismatchCap(tc.jd, profileYML)
		if city != tc.wantCity {
			t.Errorf("%s: got city %q, want %q", tc.name, city, tc.wantCity)
		}
		wantCap := 5
		if tc.wantCity != "" {
			wantCap = 2
		}
		if caps.CV != wantCap || caps.NS != wantCap {
			t.Errorf("%s: caps %d/%d, want %d/%d", tc.name, caps.CV, caps.NS, wantCap, wantCap)
		}
	}
}

// A place the candidate will commute to part of the week. Hybrid there is fine;
// full-time attendance is still capped.
func TestLocationHybridTier(t *testing.T) {
	const prof = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe / Mereside — on-site, hybrid or remote"
    - "London — hybrid or remote"
    - "United Kingdom — fully remote only"
`
	full, hybrid := commuteTiers(prof)
	if got := strings.Join(hybrid, ","); got != "london" {
		t.Errorf("hybrid terms %q, want \"london\"", got)
	}
	if got := strings.Join(full, ","); got != "ashcombe,mereside" {
		t.Errorf("commutable terms %q — a hybrid-only place is not a full-time base", got)
	}
	for _, tc := range []struct {
		name, jd, wantCity string
	}{
		{"hybrid in a hybrid-tier city", "Location: London (Hybrid Working). Rust as a main language.", ""},
		{"three days a week is hybrid", "Location & Work Model: London, UK | 3 days per week in the office", ""},
		{"words between days and week", "Location: London, UK (2 Days Onsite Per Week)", ""},
		{"five days on-site is not", "Location: Gloucester, Manchester or London (5 days onsite)", "london"},
		{"on-site with no hybrid word", "Location: London. This role is on-site.", "london"},
		{"hybrid elsewhere is still capped", "Location: Glasgow. Work Setup: Hybrid - 3 days in the office", "glasgow"},
		{"an acceptable option among others", "Location: London or Manchester – Hybrid working offered", ""},
		{"a London station is not Liverpool", "Our team is based in central London, near Liverpool Street station. Hybrid working model.", ""},
	} {
		if _, city := LocationMismatchCap(tc.jd, prof); city != tc.wantCity {
			t.Errorf("%s: got city %q, want %q", tc.name, city, tc.wantCity)
		}
	}
}

// The proximity rule. One vendor posting says it has no in-office requirement
// and names London 1.7k characters away in a list of global offices; testing
// the two signals independently capped it at 2.0 from a score of 5.0.
func TestLocationProximityRequired(t *testing.T) {
	jd := "There is no minimum in-office qualification requirement. " + strings.Repeat("x", 400) +
		" Headquartered overseas with key offices in London, New York and Paris."
	if _, city := LocationMismatchCap(jd, profileYML); city != "" {
		t.Errorf("capped on %q — the city and the on-site signal must be near each other", city)
	}
}

func TestLocationNoPolicyNoGate(t *testing.T) {
	if _, city := LocationMismatchCap("Location: London (Hybrid)", ""); city != "" {
		t.Error("capped with no stated policy")
	}
}

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

func TestSeniorityCaps(t *testing.T) {
	if s := SeniorityCaps("Senior Security Engineering Manager", "", 0, ""); s.CV != 2 || s.NS != 3 {
		t.Errorf("manager title: %d/%d, want 2/3 — a management role is a track mismatch, not a stretch", s.CV, s.NS)
	}
	if s := SeniorityCaps("Software Engineer I", "some jd text", 0, ""); s.CV != 5 {
		t.Errorf("junior role capped at %d, want 5", s.CV)
	}
	if s := SeniorityCaps("Software Engineer", "10+ years of experience", 0, ""); s.CV != 2 {
		t.Errorf("10+ years: %d, want 2", s.CV)
	}
	if s := SeniorityCaps("Software Engineer", "", 6, ""); s.CV != 3 {
		t.Errorf("6 structured years: %d, want 3", s.CV)
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
