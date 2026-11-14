package phase2

import (
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/extract"
)

const shadowProfile = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe — on-site, hybrid or remote"
    - "United Kingdom — fully remote only"
`

func TestComposeCapsFromExtractedFactsAndFailsOpenWithoutThem(t *testing.T) {
	e := &Evaluator{Cfg: &config.Config{CVRaw: "Ada\n", ProfileYML: shadowProfile}}
	jd := "Backend Engineer\nWest Drayton (4 days onsite)\nVodafone has served customers for 10 years.\n"
	parsed := Parsed{Role: "Backend Engineer", IsSinglePosting: true}
	j := Judgment{CVMatch: 5}
	facts := &extract.Facts{Attendance: extract.Attendance{Mode: extract.Onsite, Place: "West Drayton", DaysPerWeek: 4}}

	old := e.compose(1, "u", jd, parsed, nil, Coverage{Coverage: 1}, j, nil, 0, false, nil, nil).eval
	if len(old.HardStops) != 0 || old.CVBlended != 5 {
		t.Fatalf("no facts must cap nothing: %+v", old)
	}
	got := e.compose(1, "u", jd, parsed, nil, Coverage{Coverage: 1}, j, nil, 0, false, facts, nil).eval
	if len(got.HardStops) != 1 || !strings.Contains(got.HardStops[0], "west drayton") || got.CVBlended > 2 {
		t.Errorf("location fact did not cap: %+v", got)
	}
	// Stage 1's years are used when there are no facts.
	p8 := Parsed{Role: "Backend Engineer", IsSinglePosting: true, YearsRequired: 8}
	if g := e.compose(1, "u", jd, p8, nil, Coverage{Coverage: 1}, j, nil, 0, false, nil, nil).eval; g.CVBlended > 2 {
		t.Errorf("stage 1's 8 years should cap: %+v", g.CVBlended)
	}
}

// Phase 1 extracts the fetched text and Phase 2 reads it back trimmed from the
// cache file; both must hit the same cache entry.
func TestPhasesShareOneCacheKey(t *testing.T) {
	jd := "\n  Backend Engineer\n\nLondon, hybrid.\n\n"
	if extract.Posting(jd) != extract.Posting(strings.TrimSpace(jd)) {
		t.Errorf("%q != %q", extract.Posting(jd), extract.Posting(strings.TrimSpace(jd)))
	}
}
