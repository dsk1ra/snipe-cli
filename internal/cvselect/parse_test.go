package cvselect

import "testing"

// cv.md is a hand-written document and has used both heading spellings. The
// Node parser accepts only "## ", which is why Phase 3 selection went silently
// inert when the file moved to bold on 2026-08-18.
const hashCV = `## Summary
Prose.

## Experience

### Teaching Assistant
**Napier** - Edinburgh | Sep 2025 – Present
- Taught 800+ students
- Wrote the setup guides

## Projects

### Re:Link
**Honours** | Rust | 2025 - 2026
- Built a P2P tool
`

const boldCV = `**Summary**
Prose.

**Experience**

**Teaching Assistant**
**Napier** - Edinburgh | Sep 2025 – Present
- Taught 800+ students
- Wrote the setup guides

**Projects**

**Re:Link**
**Honours** | Rust | 2025 - 2026
- Built a P2P tool
`

func TestBothHeadingSpellingsParseTheSame(t *testing.T) {
	for name, cv := range map[string]string{"hash": hashCV, "bold": boldCV} {
		secs := ParseSections(cv)
		var exp, proj *Section
		for i := range secs {
			switch secs[i].Name {
			case "Experience":
				exp = &secs[i]
			case "Projects":
				proj = &secs[i]
			}
		}
		if exp == nil || proj == nil {
			t.Fatalf("%s: found sections %v, want Experience and Projects", name, sectionNamesOf(secs))
		}
		e := ParseEntries(exp.Lines)
		if len(e.Entries) != 1 || len(e.Entries[0].Bullets) != 2 {
			t.Errorf("%s: %d entries, %d bullets; want 1 and 2", name, len(e.Entries), bulletCount(e))
		}
		p := ParseEntries(proj.Lines)
		if len(p.Entries) != 1 || len(p.Entries[0].Bullets) != 1 {
			t.Errorf("%s: projects %d entries, %d bullets; want 1 and 1", name, len(p.Entries), bulletCount(p))
		}
		// The company line is a bold-alone line too, but it is not alone on its
		// line — it carries the location and dates — so it must stay in the head.
		if len(e.Entries[0].Head) != 2 {
			t.Errorf("%s: head is %v, want the title and the company line", name, e.Entries[0].Head)
		}
	}
}

func sectionNamesOf(secs []Section) []string {
	var out []string
	for _, s := range secs {
		if s.Name != "" {
			out = append(out, s.Name)
		}
	}
	return out
}

func bulletCount(p Parsed) int {
	n := 0
	for _, e := range p.Entries {
		n += len(e.Bullets)
	}
	return n
}

func TestWrappedBulletsJoin(t *testing.T) {
	// cv.md wraps long bullets in the source. Treating each wrapped line as its
	// own bullet would both multiply the count and score every fragment apart.
	p := ParseEntries([]string{
		"**Experience**",
		"**Engineer**",
		"- First bullet that continues",
		"  onto a second line",
		"- Second bullet",
	})
	if len(p.Entries) != 1 || len(p.Entries[0].Bullets) != 2 {
		t.Fatalf("got %d bullets, want 2", bulletCount(p))
	}
	if got := p.Entries[0].Bullets[0]; got != "First bullet that continues onto a second line" {
		t.Errorf("wrapped bullet joined as %q", got)
	}
}

func TestBulletGeometry(t *testing.T) {
	// Both constants are measured. 108 was exact on all 33 real bullets; the
	// 2/12 overhead is what a \resumeItem costs beyond its line height.
	for _, tc := range []struct {
		name  string
		text  string
		lines int
	}{
		{"empty is still one line", "", 1},
		{"short", "Shipped it", 1},
		{"exactly at the boundary", str(108), 1},
		{"one over", str(109), 2},
		{"three lines", str(300), 3},
		// An em dash is one UTF-16 unit and three UTF-8 bytes. Counting bytes
		// would ration the page against a document nobody is rendering.
		{"em dashes cost one unit each", str(100) + "—————", 1},
	} {
		if got := BulletLines(tc.text); got != tc.lines {
			t.Errorf("%s: %d lines, want %d", tc.name, got, tc.lines)
		}
	}
	if got := BulletCost("short"); got < 1.16 || got > 1.17 {
		t.Errorf("cost %v, want 1 line + 2/12", got)
	}
}

func str(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestEntryEndDateOrders(t *testing.T) {
	mk := func(head string) *Entry { return &Entry{Head: []string{"**Role**", head}} }
	present := EntryEndDate(mk("**Co** - Edinburgh | Sep 2025 – Present"))
	past := EntryEndDate(mk("**Co** - Edinburgh | Oct 2024 – Sep 2025"))
	older := EntryEndDate(mk("**Co** | Rust | 2023 - 2024"))
	if !(present > past && past > older) {
		t.Errorf("ordering wrong: present=%v past=%v older=%v", present, past, older)
	}
	// An unparseable head sorts below everything rather than above it: an entry
	// with no date should not lead a reverse-chronological CV.
	if EntryEndDate(mk("no dates here")) >= older {
		t.Error("an undated entry did not sort last")
	}
}

func TestUnmatchedPinsSeesBoldTitles(t *testing.T) {
	missed := UnmatchedPins(boldCV, []string{"Re:Link", "Zero Trust SIEM"})
	if len(missed) != 1 || missed[0] != "zero trust siem" {
		t.Errorf("got %v, want just the renamed pin", missed)
	}
}

func TestExtractBlockBRequirements(t *testing.T) {
	report := `## B) CV Match

| JD Requirement | Candidate evidence | Strength |
|----------------|-------------------|----------|
| **[must]** Core Java proficiency | Built services | Strong |
| Kafka experience | — | Gap |
| tiny | x | Gap |

**Gaps:** none`
	got := ExtractBlockBRequirements(report)
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 — a requirement of 8 characters or fewer is a table artifact", got)
	}
	if got[0] != "Core Java proficiency" {
		t.Errorf("the must marker was not stripped: %q", got[0])
	}
}
