package phase1

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/extract"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

const testProfile = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe — on-site, hybrid or remote"
    - "United Kingdom — fully remote only"
`

func TestValidateCapsFromExtractedFactsAndFailsOpenWithoutThem(t *testing.T) {
	s := &Scorer{Cfg: &config.Config{CVRaw: "Ada\n", ProfileYML: testProfile}}
	m := modelReply{Role: "Backend Engineer", CVMatch: 5, NorthStar: 5}
	// No word list knows West Drayton, and "10 years" here is the company's age.
	jd := "Backend Engineer\nWest Drayton (4 days onsite)\nVodafone has served customers for 10 years.\n"
	facts := &extract.Facts{Attendance: extract.Attendance{Mode: extract.Onsite, Place: "West Drayton", DaysPerWeek: 4}}

	if got := s.validate(m, jd, "u", 1, nil, nil); got.CVCap != 5 || len(got.HardStops) != 0 {
		t.Fatalf("no facts must cap nothing: %+v", got)
	}
	got := s.validate(m, jd, "u", 1, facts, nil)
	if got.CVCap != 2 || len(got.HardStops) != 1 || !strings.Contains(got.HardStops[0], "west drayton") {
		t.Errorf("location fact did not cap: %+v", got)
	}
	if len(got.SoftGaps) != 0 {
		t.Errorf("seniority cap applied from the posting's age: %v", got.SoftGaps)
	}
	// The title still caps without any facts.
	if got := s.validate(modelReply{Role: "Senior Engineer", CVMatch: 5, NorthStar: 5}, jd, "u", 1, nil, nil); got.CVCap != 3 {
		t.Errorf("a Senior title should cap at 3 with no facts: %d", got.CVCap)
	}
	// Years come from the facts, not from the text.
	y := &extract.Facts{Years: extract.Years{MinYears: 8, QuoteOK: true}}
	if got := s.validate(m, jd, "u", 1, y, nil); got.CVCap != 2 {
		t.Errorf("8 extracted years should cap at 2: %d", got.CVCap)
	}
}

func TestExtractIsSafeAcrossWorkers(t *testing.T) {
	ex := extract.New(nil, t.TempDir(), "m", "sys")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = ex.Extract(t.Context(), "posting") }()
	}
	wg.Wait() // fails under -race if the cache is unguarded
}

func TestValidateStackCapFromExtractedLanguages(t *testing.T) {
	s := &Scorer{Cfg: &config.Config{CVRaw: "Ada\n", ProfileYML: testProfile}}
	m := modelReply{Role: "Engineer", CVMatch: 5, NorthStar: 5}
	// The table has no Swift, so only the facts see this posting's stack.
	jd := "Engineer\nYou have experience developing iOS applications using Swift.\n"
	facts := &extract.Facts{Stack: []extract.StackItem{{Term: "Swift", Language: "swift", Requirement: extract.Required}}}

	if got := s.validate(m, jd, "u", 1, facts, nil); got.CVCap != 5 || len(got.CapShadow) != 0 {
		t.Errorf("without CV languages the table decides: %+v", got)
	}
	got := s.validate(m, jd, "u", 1, facts, []string{"go"})
	if got.CVCap != 3 || len(got.CapShadow) != 1 || !strings.HasPrefix(got.CapShadow[0], "stack: lists cap 5/5") {
		t.Errorf("swift against a go CV should cap 3: %+v %v", got.CVCap, got.CapShadow)
	}
	if got := s.validate(m, jd, "u", 1, facts, []string{"go", "swift"}); got.CVCap != 5 {
		t.Errorf("a covered language caps nothing: %+v", got.CVCap)
	}
}

// north_star comes from the archetype's tier, whatever number the model wrote.
func TestValidateLooksNorthStarUpFromTheArchetype(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	os.MkdirAll(filepath.Dir(l.ProfileYML()), 0o755)
	os.WriteFile(l.CV(), []byte("Ada\n"), 0o644)
	os.WriteFile(l.ProfileYML(), []byte(`target_roles:
  archetypes:
    - name: "Backend Engineer"
      fit: "primary"
    - name: "Platform Engineer"
      fit: "secondary"
`), 0o644)
	cfg, err := config.Load(l)
	if err != nil {
		t.Fatal(err)
	}
	s := &Scorer{Cfg: cfg}
	for _, c := range []struct {
		archetype string
		model     int
		want      int
	}{
		{"Backend Engineer", 1, 5},  // the model undersold a primary target
		{"Platform Engineer", 5, 3}, // and oversold a secondary one
		{config.OutsideTargets, 4, 1},
	} {
		got := s.validate(modelReply{Role: "Engineer", Archetype: c.archetype, CVMatch: 4, NorthStar: c.model}, "Engineer\n", "u", 1, nil, nil)
		if got.NorthStar != c.want {
			t.Errorf("%s with the model's %d: north_star %d, want %d", c.archetype, c.model, got.NorthStar, c.want)
		}
	}
}
