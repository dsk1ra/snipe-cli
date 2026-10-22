package phase1

import (
	"strings"
	"sync"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/extract"
)

const testProfile = `location:
  city: "Ashcombe"
  search_locations:
    - "Ashcombe — on-site, hybrid or remote"
    - "United Kingdom — fully remote only"
`

func TestValidateLetsExtractedFactsDecideAndReportsTheLists(t *testing.T) {
	s := &Scorer{Cfg: &config.Config{CVRaw: "Ada\n", ProfileYML: testProfile}}
	m := modelReply{Role: "Backend Engineer", CVMatch: 5, NorthStar: 5}
	// The word lists do not know West Drayton and read "10 years" as a demand.
	jd := "Backend Engineer\nWest Drayton (4 days onsite)\nVodafone has served customers for 10 years.\n"
	facts := &extract.Facts{Attendance: extract.Attendance{Mode: extract.Onsite, Place: "West Drayton", DaysPerWeek: 4}}

	if got := s.validate(m, jd, "u", 1, nil, nil); len(got.CapShadow) != 0 || got.CVCap != 2 {
		t.Fatalf("no facts must keep the list caps (the age as a seniority demand) and report nothing: %+v", got)
	}
	got := s.validate(m, jd, "u", 1, facts, nil)
	if got.CVCap != 2 || len(got.HardStops) != 1 || !strings.Contains(got.HardStops[0], "west drayton") {
		t.Errorf("location fact did not cap: %+v", got)
	}
	if len(got.CapShadow) < 1 || !strings.HasPrefix(got.CapShadow[0], "location:") {
		t.Errorf("disagreement not reported: %v", got.CapShadow)
	}
	if len(got.CapShadow) != 2 || !strings.HasPrefix(got.CapShadow[1], "seniority: lists cap 2/3") {
		t.Errorf("the list's company-age cap should show up as a shadow disagreement: %v", got.CapShadow)
	}
	if len(got.SoftGaps) != 0 {
		t.Errorf("seniority cap applied from the posting's age: %v", got.SoftGaps)
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
