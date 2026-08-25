package phase3

import (
	"os"
	"strings"
	"testing"
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
	got := SelectSkills(writerCV, jd, 6, 12, 3)

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
	tight := SelectSkills(writerCV, jd, 2, 12, 3)
	for _, c := range tight {
		if c.Category == "Cloud" {
			t.Errorf("dropped a named category before an unnamed one: %+v", tight)
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
	got := SelectSkills(string(b), "Backend engineer: Java, Spring Boot, Kafka, PostgreSQL, Kubernetes on AWS.", 6, 12, 3)
	if len(got) == 0 || len(got) > 6 {
		t.Fatalf("selected %d categories, want 1..6", len(got))
	}
	t.Logf("%d categories from %d:", len(got), len(cats))
	for _, c := range got {
		t.Logf("  %-28s %s", c.Category, c.Items)
	}
}
