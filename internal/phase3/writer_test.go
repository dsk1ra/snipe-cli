package phase3

import (
	"os"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/fill"
)

// The fixture cv-writers.mjs checks itself against.
const writerCV = `# X

## Experience

### Engineer
**Acme** — London | Jan 2024 – Present

- Shipped a thing serving 170 users
- Cut latency by 80%

## Projects

### Widget
**Personal** | Rust | 2025

- Built a Rust widget with AES-256-GCM encryption and a lock-free ring buffer
- Benchmarked it across 7 schemes and 5 payload sizes with bootstrap CIs

## Skills

**Languages:** Rust, Java, Python
**Security:** AES-256-GCM, OAuth 2.0 (working knowledge, self-study), JWT
**Cloud:** AWS, Docker
`

// A parenthesis holds either the keywords themselves or a caveat about depth,
// and the old rule dropped both — deleting AES-256-GCM, Jenkins, Ollama and MCP
// from every tailored PDF at parse time.
func TestExpandSkillItemKeepsNamesAndDropsProse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"OAuth 2.0 (working knowledge, self-study)", []string{"OAuth 2.0"}},
		{"Message Queues (RabbitMQ, Kafka)", []string{"Message Queues", "RabbitMQ", "Kafka"}},
		{"AWS (EC2, Lambda, S3, IAM)", []string{"AWS", "EC2", "Lambda", "S3", "IAM"}},
		{"Kubernetes", []string{"Kubernetes"}},
	} {
		got := expandSkillItem(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Splitting on every comma ships "Message Queues (RabbitMQ" as a skill.
func TestSkillItemsSplitOnTopLevelCommasOnly(t *testing.T) {
	cats := ParseSkillCategories(writerCV)
	if len(cats) != 3 {
		t.Fatalf("got %d categories, want 3: %+v", len(cats), cats)
	}
	var sec SkillCat
	for _, c := range cats {
		if c.Category == "Security" {
			sec = c
		}
	}
	// The parenthesised aside must not split into a bogus item.
	if want := "AES-256-GCM|OAuth 2.0|JWT"; strings.Join(sec.Items, "|") != want {
		t.Errorf("Security items %q, want %q", sec.Items, want)
	}
}

// A posting that ends a sentence on a technology name still matches it.
func TestASentenceFinalDotDoesNotHideAMatch(t *testing.T) {
	if got := NormPhrase("We need Kafka."); !strings.Contains(got, " kafka ") {
		t.Errorf("normalised to %q, want kafka as a whole phrase", got)
	}
	if got := tokenize("experience with Kafka."); got[len(got)-1] != "kafka" {
		t.Errorf("tokenised to %q, want a bare kafka", got)
	}
	// The dot inside a name survives, which is the reason for the trap.
	if got := NormPhrase("Next.js"); !strings.Contains(got, "next.js") {
		t.Errorf("normalised to %q, want next.js whole", got)
	}
}

// Categories are ranked by the posting and then restored to the CV's order, and
// one the posting named is never cut for budget.
func TestSelectSkillsKeepsWhatThePostingNamed(t *testing.T) {
	jd := "We need Rust and AES-256-GCM encryption experience."
	got := SelectSkills(writerCV, jd, 6, 12, 3, nil)

	if len(got) != 3 {
		t.Fatalf("got %d categories, want all 3 within budget: %+v", len(got), got)
	}
	// CV order survives the cut, even though Security outranks Cloud here.
	for i, want := range []string{"Languages", "Security", "Cloud"} {
		if got[i].Category != want {
			t.Errorf("category %d is %q, want %q (cv.md's order)", i, got[i].Category, want)
		}
	}
	// Within a category the posting's own terms come first.
	if !strings.HasPrefix(got[0].Items, "Rust") {
		t.Errorf("Languages reads %q, want Rust first", got[0].Items)
	}
	if !strings.HasPrefix(got[1].Items, "AES-256-GCM") {
		t.Errorf("Security reads %q, want AES-256-GCM first", got[1].Items)
	}

	// A tight budget drops what the posting did not name, and only that.
	tight := SelectSkills(writerCV, jd, 2, 12, 3, nil)
	for _, c := range tight {
		if c.Category == "Cloud" {
			t.Errorf("dropped a named category before an unnamed one: %+v", tight)
		}
	}
}

// A word an item shares with the posting does not name it; a whole form does.
// Token overlap had "engineering" naming a category in 241 of 351 postings.
func TestSelectSkillsNamesWholeFormsOnly(t *testing.T) {
	cv := strings.Replace(writerCV, "**Cloud:** AWS, Docker",
		"**Cloud:** AWS, Docker\n**Practice:** System Design, TypeScript / JavaScript", 1)
	cats := func(jd string) string {
		var out []string
		for _, c := range SelectSkills(cv, jd, 0, 12, 1, nil) {
			out = append(out, c.Category)
		}
		return strings.Join(out, ",")
	}
	// With no budget for unnamed categories, only named ones survive.
	if got := cats("A strong design and engineering culture."); got != "" {
		t.Errorf("a shared word named %q, want nothing named", got)
	}
	if got := cats("You will own system design reviews."); got != "Practice" {
		t.Errorf("named %q, want Practice by its whole phrase", got)
	}
	if got := cats("TypeScript on the frontend."); got != "Practice" {
		t.Errorf("named %q, want Practice by one side of the slash", got)
	}
	if got := cats("You will lead system designs."); got != "Practice" {
		t.Errorf("named %q, want Practice by the plural", got)
	}
}

// A shared word does not name a category, but it still ranks one: the slot
// for an unnamed category goes to the one the posting is about.
func TestSelectSkillsRanksUnnamedByOverlap(t *testing.T) {
	cv := "## Skills\n\n**Data:** PostgreSQL, MySQL\n**Security:** supply chain security, threat modelling\n"
	got := SelectSkills(cv, "Help us keep the company's security strong.", 1, 7, 1, nil)
	if len(got) != 1 || got[0].Category != "Security" {
		t.Errorf("picked %+v, want Security over Data, which only comes first in cv.md", got)
	}
}

func TestOtherNumber(t *testing.T) {
	for in, want := range map[string]string{
		"Code Review": "Code Reviews",
		"WebSockets":  "WebSocket",
		"AWS":         "", // not a plural
		"CSS":         "",
		"React":       "", // one word: "reacts" is a verb
		"Serverless":  "",
	} {
		if got := otherNumber(in); got != want {
			t.Errorf("otherNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

// The real cv.md, so the parse is exercised against the file that actually ships.
func TestSelectSkillsAgainstTheRealCV(t *testing.T) {
	b, err := os.ReadFile("../../cv.md")
	if err != nil {
		t.Skip(err)
	}
	cats := ParseSkillCategories(string(b))
	if len(cats) < 4 {
		t.Fatalf("parsed %d skill categories out of cv.md, want the real block", len(cats))
	}
	got := SelectSkills(string(b), "Backend engineer: Java, Spring Boot, Kafka, PostgreSQL, Kubernetes on AWS.", SkillCats, SkillItems, SkillFloor, nil)
	if len(got) == 0 || len(got) > SkillCats {
		t.Fatalf("selected %d categories, want 1..%d", len(got), SkillCats)
	}
	t.Logf("%d categories from %d:", len(got), len(cats))
	for _, c := range got {
		t.Logf("  %-28s %s", c.Category, c.Items)
	}
}

// An approved alias makes a skill count as named and prints it the way the
// posting spells it — the one thing an ATS keyword search can actually see.
func TestSelectSkillsPrintsThePostingsSpelling(t *testing.T) {
	aliases := map[string][]string{"java": {"JVM", "JVM languages"}}
	items := func(jd string, aliases map[string][]string) string {
		for _, c := range SelectSkills(writerCV, jd, 6, 12, 1, aliases) {
			if c.Category == "Languages" {
				return c.Items
			}
		}
		return ""
	}

	// Named only by an alias: kept and printed as the posting wrote it, the
	// longest matching form winning.
	if got := items("Strong background in JVM languages.", aliases); got != "JVM languages" {
		t.Errorf("Languages reads %q, want the posting's longest spelling", got)
	}
	// The posting also uses cv.md's own spelling, so that one stays.
	if got := items("Java on the JVM.", aliases); got != "Java" {
		t.Errorf("Languages reads %q, want cv.md's Java kept", got)
	}
	// Without aliases nothing is named, so the floor prints cv.md's strings.
	if got := items("Strong background in JVM languages.", nil); strings.Contains(got, "JVM") {
		t.Errorf("Languages reads %q with no aliases, want cv.md's strings only", got)
	}
}

// Aliases are approved per item, so two items could print as one skill: Coder's
// posting says both "AI tools" and "AI tooling", one an alias of each item.
func TestSelectSkillsPrintsEachSkillOnce(t *testing.T) {
	cv := "# X\n\n## Skills\n\n**AI Engineering:** agentic workflows, AI-assisted development, RAG\n"
	aliases := map[string][]string{
		"agentic workflows":       {"AI tools"},
		"ai-assisted development": {"AI tooling"},
	}
	jd := "Coder embraces the use of AI tools. Using AI tooling for take-home assessments is permitted."
	got := SelectSkills(cv, jd, 6, 12, 1, aliases)
	if len(got) != 1 || got[0].Items != "AI tools, AI-assisted development" {
		t.Errorf("block reads %+v, want AI tools once and the second item in cv.md's spelling", got)
	}

	for _, p := range [][2]string{{"AI tools", "AI tooling"}, {"unit tests", "unit testing"}, {"event-driven", "Event driven"}} {
		if skillKey(p[0]) != skillKey(p[1]) {
			t.Errorf("%q and %q read as two skills", p[0], p[1])
		}
	}
	for _, p := range [][2]string{{"Spring", "Spr"}, {"AWS", "AW"}, {"CSS", "CS"}} {
		if skillKey(p[0]) == skillKey(p[1]) {
			t.Errorf("%q and %q read as one skill", p[0], p[1])
		}
	}
}

// A posting that asks for English gets the human languages row, which lives
// outside the Skills section. One that does not keeps the line.
func TestSelectSkillsAddsLanguagesThePostingNames(t *testing.T) {
	cv := writerCV + "\n## Languages\n\n- English: Advanced (C1 certified)\n- Welsh: Native\n"
	has := func(jd string) string {
		for _, p := range SelectSkills(cv, jd, SkillCats, SkillItems, SkillFloor, nil) {
			if p.Category == fill.HumanLanguages {
				return p.Items
			}
		}
		return ""
	}
	if got := has("Rust engineer. Fluent in English."); got != "English: Advanced (C1 certified), Welsh: Native" {
		t.Errorf("posting names English, row = %q", got)
	}
	if got := has("Rust engineer on AWS."); got != "" {
		t.Errorf("posting names no language, row = %q", got)
	}
	// End to end: the pick has to survive ResolveSkills onto the page.
	c := VerbatimContent(cv, cv, "Rust engineer. Fluent in English.", 0, nil)
	rows := fill.ResolveSkills(fill.ParseCV(cv).Skills, c, 0, cv)
	if last := rows[len(rows)-1]; last.Category != fill.HumanLanguages || !strings.Contains(last.Items, "C1") {
		t.Errorf("page's last skills row = %+v", last)
	}
}
