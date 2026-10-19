package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// Goals come out of profile.yml, and an unset one keeps its default rather than
// dropping to zero — a zero target renders as no bar at all, so one goal in the
// file would silently blank the other two.
func TestGoalsFallBackPerField(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cv.md", "# CV\n\nSome experience.\n")
	write("config/profile.yml", "goals:\n  daily_scans: 15\n")

	c, err := Load(paths.Layout{Home: dir, Code: dir})
	if err != nil {
		t.Fatal(err)
	}
	if c.Goals.DailyScans != 15 {
		t.Errorf("daily_scans is %d, want the profile's 15", c.Goals.DailyScans)
	}
	if c.Goals.DailyApplied != DefaultGoals.DailyApplied {
		t.Errorf("daily_applications is %d, want the default %d",
			c.Goals.DailyApplied, DefaultGoals.DailyApplied)
	}
	if c.Goals.WeeklyApplied != DefaultGoals.WeeklyApplied {
		t.Errorf("weekly_applications is %d, want the default %d",
			c.Goals.WeeklyApplied, DefaultGoals.WeeklyApplied)
	}
}

// CVRaw is cv.md and nothing else.
//
// This is the invariant the whole retrieval-only seam rests on. article-digest.md
// exists so a fact can reach Phase 2's evidence index without appearing on a
// tailored CV, and that works only because the render path — ParseCV, Render,
// pdflatex — is fed CVRaw. Folding the digest in here to "have one CV string"
// would print a private eligibility note on every PDF sent to an employer, and
// nothing downstream would object.
func TestCVRawIsCvMdAlone(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cv.md":             "# CV\n\nSome experience.\n",
		"article-digest.md": "- a retrieval-only fact that must never be rendered on a CV\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(paths.Layout{Home: dir, Code: dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.CVRaw, "retrieval-only") || strings.Contains(c.CV, "retrieval-only") {
		t.Fatal("article-digest.md reached the CV string the template renders from")
	}
	if c.CVRaw != "# CV\n\nSome experience." {
		t.Errorf("CVRaw is %q, want cv.md trimmed and otherwise verbatim", c.CVRaw)
	}
}

// north_star is the profile's fit tier, so a mis-read tier silently re-ranks
// every offer of that archetype.
func TestNorthStarFollowsFitTier(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cv.md": "# CV\n",
		"config/profile.yml": `target_roles:
  archetypes:
    - name: "Backend"
      fit: "primary"
    - name: "Full-Stack"
      fit: "Secondary"
    - name: "SRE"
      fit: "adjacent"
    - "Security"
`,
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(paths.Layout{Home: dir, Code: dir})
	if err != nil {
		t.Fatal(err)
	}
	for arch, want := range map[string]int{
		"Backend": 5, "Full-Stack": 3, "SRE": 2, "Security": 5,
		OutsideTargets: 1, "Data Engineer": 1,
	} {
		if got := c.NorthStar(arch); got != want {
			t.Errorf("NorthStar(%q) = %d, want %d", arch, got, want)
		}
	}
	if got := (&Config{}).NorthStar("Backend"); got != 3 {
		t.Errorf("with no archetype list NorthStar = %d, want neutral 3", got)
	}
}
