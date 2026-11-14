package providers

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const wdAPI = "https://acme.wd3.myworkdayjobs.com/wday/cxs/acme/External"

type wdPost struct{ loc, posted string }

// wdStub answers the Workday API from the request body, which is where the
// paging and the facets go: the unfiltered board is `all`, the country-filtered
// one `uk`, and a detail read finds the posting's country in `detail`.
//
// Past the end it repeats its last page, as Lloyds does, and it sends the total
// on the first page unless noTotal.
type wdStub struct {
	all, uk []wdPost
	facets  string
	detail  map[string]string // externalPath → country
	records map[string]string // externalPath → a whole jobPostingInfo, for multi-location posts
	noTotal bool
	bodies  []string
	gets    []string
}

func (s *wdStub) JSON(_ context.Context, url string, o Options, out any) error {
	if o.Method != "POST" {
		s.gets = append(s.gets, url)
		if r, ok := s.records[strings.TrimPrefix(url, wdAPI)]; ok {
			return json.Unmarshal([]byte(`{"jobPostingInfo":`+r+`}`), out)
		}
		c, ok := s.detail[strings.TrimPrefix(url, wdAPI)]
		if !ok {
			return &StatusError{Status: 404, URL: url}
		}
		return json.Unmarshal([]byte(`{"jobPostingInfo":{"country":{"descriptor":"`+c+`"}}}`), out)
	}
	if url != wdAPI+"/jobs" || o.Headers["Content-Type"] != "application/json" || !o.NoRedirect {
		return fmt.Errorf("unexpected request %s %+v", url, o)
	}
	s.bodies = append(s.bodies, string(o.Body))
	var req struct {
		AppliedFacets map[string][]string
		Limit, Offset int
	}
	if err := json.Unmarshal(o.Body, &req); err != nil {
		return err
	}
	board := s.all
	if len(req.AppliedFacets["locationCountry"]) > 0 {
		board = s.uk
	}
	if req.Offset >= len(board) && len(board) >= req.Limit {
		req.Offset = len(board) - req.Limit
	}
	total := ""
	if req.Offset == 0 && !s.noTotal {
		total = fmt.Sprintf(`"total":%d,`, len(board))
	}
	var posts []string
	for i := req.Offset; i < len(board) && i < req.Offset+req.Limit; i++ {
		posts = append(posts, fmt.Sprintf(`{"title":"Engineer %d","externalPath":"/job/x/Engineer_JR%d","locationsText":%q,"postedOn":%q}`,
			i, i, board[i].loc, board[i].posted))
	}
	return json.Unmarshal([]byte(`{`+total+`"jobPostings":[`+strings.Join(posts, ",")+`],"facets":`+cmp.Or(s.facets, "[]")+`}`), out)
}

func (s *wdStub) Text(context.Context, string, Options) (string, error) {
	return "", fmt.Errorf("workday never asks for text")
}

func repeat(n int, p wdPost) []wdPost {
	out := make([]wdPost, n)
	for i := range out {
		out[i] = p
	}
	return out
}

func TestWorkdayFiltersByCountryFacet(t *testing.T) {
	uk := repeat(23, wdPost{"Filton", "Posted 30+ Days Ago"})
	uk[0], uk[1] = wdPost{"2 Locations", "Posted Today"}, wdPost{"London, United Kingdom", "Posted Yesterday"}
	s := &wdStub{
		all: repeat(60, wdPost{"Toulouse", "Posted Today"}), uk: uk,
		// Nested, as most sites send it.
		facets: `[{"facetParameter":"timeType","values":[{"descriptor":"Full time","id":"ft"}]},
			{"facetParameter":"locationMainGroup","values":[{"facetParameter":"locationCountry","values":[
				{"descriptor":"France","id":"fr"},{"descriptor":"United Kingdom","id":"gb"}]}]}]`,
	}
	e := Entry{Name: "Acme", CareersURL: "https://acme.wd3.myworkdayjobs.com/en-GB/External", Extra: map[string]any{"country": "United Kingdom"}}
	jobs, err := workday{}.Fetch(context.Background(), e, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 23 || len(s.bodies) != 3 || !strings.Contains(s.bodies[2], `"offset":20`) ||
		!strings.Contains(s.bodies[1], `"appliedFacets":{"locationCountry":["gb"]}`) {
		t.Fatalf("want the unfiltered probe then two filtered pages; got %d jobs from %v", len(jobs), s.bodies)
	}
	for i, want := range map[int]string{0: "United Kingdom", 1: "London, United Kingdom", 2: "Filton, United Kingdom"} {
		if jobs[i].Location != want {
			t.Errorf("job %d location %q, want %q", i, jobs[i].Location, want)
		}
	}
	if jobs[2].URL != "https://acme.wd3.myworkdayjobs.com/External/job/x/Engineer_JR2" || jobs[2].Company != "Acme" {
		t.Errorf("job URL %q", jobs[2].URL)
	}
	if age := time.Since(time.UnixMilli(jobs[1].PostedAt)); age < 23*time.Hour || age > 25*time.Hour {
		t.Errorf("Posted Yesterday read as %v ago", age)
	}
}

func TestWorkdayStopsAtTheTotal(t *testing.T) {
	for _, noTotal := range []bool{false, true} {
		s := &wdStub{all: repeat(40, wdPost{"London", "Posted Today"}), noTotal: noTotal}
		jobs, err := workday{}.Fetch(context.Background(), Entry{Name: "Acme", CareersURL: "https://acme.wd3.myworkdayjobs.com/External"}, s)
		if err != nil {
			t.Fatal(err)
		}
		// Without a total, the third page is all repeats and ends it.
		if want := map[bool]int{false: 2, true: 3}[noTotal]; len(jobs) != 40 || len(s.bodies) != want {
			t.Errorf("noTotal=%v: %d jobs from %d pages, want 40 from %d", noTotal, len(jobs), len(s.bodies), want)
		}
	}
}

func TestWorkdayPlacesLocationsWithoutAFacet(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := &wdStub{
		all: []wdPost{
			{"Canary Wharf, 1 Churchill Place", "Posted Today"},
			{"Canary Wharf, 1 Churchill Place", "Posted Today"},
			{"Working From Home, CALIFORNIA", "Posted Today"},
			{"3 Locations", "Posted Today"},
		},
		facets: `[{"facetParameter":"locationMainGroup","values":[{"facetParameter":"locations","values":[{"descriptor":"Canary Wharf, 1 Churchill Place","id":"cw"}]}]}]`,
		detail: map[string]string{"/job/x/Engineer_JR0": "United Kingdom", "/job/x/Engineer_JR2": "United States of America"},
	}
	e := Entry{Name: "Acme", CareersURL: "https://acme.wd3.myworkdayjobs.com/External", Extra: map[string]any{"country": "United Kingdom"}}
	jobs, err := workday{}.Fetch(context.Background(), e, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Canary Wharf, 1 Churchill Place, United Kingdom", "Canary Wharf, 1 Churchill Place, United Kingdom",
		"Working From Home, CALIFORNIA, United States of America", "",
	}
	for i, j := range jobs {
		if j.Location != want[i] {
			t.Errorf("job %d location %q, want %q", i, j.Location, want[i])
		}
	}
	// One detail read per distinct location, none for the "3 Locations" one.
	if len(s.gets) != 2 || len(s.bodies) != 1 {
		t.Errorf("detail reads %v, list pages %d", s.gets, len(s.bodies))
	}
	// An office does not move: the next scan reads none.
	s.gets = nil
	again, _ := workday{}.Fetch(context.Background(), e, s)
	if len(s.gets) != 0 || again[0].Location != want[0] {
		t.Errorf("second scan made detail reads %v, placed %q", s.gets, again[0].Location)
	}
}

func TestWorkdayLocatesMultiLocationPostsOnDemand(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s := &wdStub{
		all:    []wdPost{{"Glasgow Campus", "Posted Today"}, {"2 Locations", "Posted Today"}, {"2 Locations", "Posted Today"}},
		detail: map[string]string{"/job/x/Engineer_JR0": "United Kingdom"},
		records: map[string]string{
			"/job/x/Engineer_JR1": `{"location":"Pune","additionalLocations":["Glasgow Campus"],"country":{"descriptor":"India"}}`,
			"/job/x/Engineer_JR2": `{"location":"Ohio","additionalLocations":["Oregon"],"country":{"descriptor":"United States of America"}}`,
		},
	}
	e := Entry{Name: "Acme", CareersURL: "https://acme.wd3.myworkdayjobs.com/External", Extra: map[string]any{"country": "United Kingdom"}}
	jobs, err := workday{}.Fetch(context.Background(), e, s)
	if err != nil {
		t.Fatal(err)
	}
	// Fetch reads no multi-location record; it leaves the read to the scan.
	if len(s.gets) != 1 || jobs[0].Locate != nil || jobs[1].Locate == nil || jobs[1].Location != "" {
		t.Fatalf("after Fetch: reads %v, jobs %+v", s.gets, jobs)
	}
	// An extra location the site's offices place in the UK counts.
	if got := jobs[1].Locate(context.Background()); got != "Pune; Glasgow Campus, United Kingdom" {
		t.Errorf("Pune+Glasgow placed as %q", got)
	}
	if got := jobs[2].Locate(context.Background()); got != "Ohio; Oregon, United States of America" {
		t.Errorf("Ohio+Oregon placed as %q", got)
	}
}

func TestWorkdayBoardURLs(t *testing.T) {
	for in, want := range map[string]wdBoard{
		"https://barclays.wd3.myworkdayjobs.com/en-GB/External_Career_Site_Barclays/job/1": {
			"https://barclays.wd3.myworkdayjobs.com/wday/cxs/barclays/External_Career_Site_Barclays",
			"https://barclays.wd3.myworkdayjobs.com/External_Career_Site_Barclays", "barclays"},
		"https://wd3.myworkdaysite.com/recruiting/bsigroup/BSI_Careers": {
			"https://wd3.myworkdaysite.com/wday/cxs/bsigroup/BSI_Careers",
			"https://wd3.myworkdaysite.com/recruiting/bsigroup/BSI_Careers", "bsigroup"},
		"http://acme.wd3.myworkdayjobs.com/External":                     {},
		"https://acme.wd3.myworkdayjobs.com/wday/cxs/acme/External/jobs": {},
		"https://careers.acme.com/External":                              {},
		"https://acme.wd3.myworkdayjobs.com.evil.com/External":           {},
	} {
		got, ok := workdayBoard(in)
		if got != want || ok != (want != wdBoard{}) {
			t.Errorf("workdayBoard(%q) = %+v %v, want %+v", in, got, ok, want)
		}
	}
}

func TestWorkdayPosted(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for in, days := range map[string]int{
		"Posted Today": 0, "Posted Yesterday": 1, "Posted 3 Days Ago": 3, "Posted 30+ Days Ago": 30,
	} {
		if got := workdayPosted(in, now); got != now.AddDate(0, 0, -days).UnixMilli() {
			t.Errorf("%q read as %v", in, time.UnixMilli(got).UTC())
		}
	}
	if workdayPosted("Publié aujourd'hui", now) != 0 {
		t.Error("an unreadable date must be zero, which the age filter drops")
	}
}
