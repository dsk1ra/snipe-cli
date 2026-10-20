package scan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/providers"
)

type stub struct{ pages map[string]string }

func (s stub) JSON(_ context.Context, url string, _ providers.Options, out any) error {
	body, ok := s.pages[url]
	if !ok {
		return &providers.StatusError{Status: 404, URL: url}
	}
	return json.Unmarshal([]byte(body), out)
}

func (s stub) Text(_ context.Context, url string, _ providers.Options) (string, error) {
	return s.pages[url], nil
}

func cfg(t *testing.T, body string) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "portals.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTitleFilter(t *testing.T) {
	c := cfg(t, "title_filter:\n  positive: [engineer]\n  negative: [senior, \"  \"]\n")
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Backend Engineer", true},
		{"Senior Engineer", false}, // negative beats positive
		{"Designer", false},        // no positive hit
	} {
		if got := c.TitleOK(tc.title); got != tc.want {
			t.Errorf("TitleOK(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
	// No positives configured means every title clears that tier.
	open := cfg(t, "title_filter:\n  negative: [intern]\n")
	if !open.TitleOK("Designer") || open.TitleOK("Intern") {
		t.Error("an empty positive list should pass everything the negatives allow")
	}
}

func TestLocationFilterTierOrder(t *testing.T) {
	c := cfg(t, "location_filter:\n  always_allow: [london]\n  allow: [united kingdom]\n  block: [france]\n")
	for _, tc := range []struct {
		loc  string
		want bool
	}{
		{"", true},                        // missing data is not a reason to drop
		{"   ", true},                     //
		{"Remote, London or Paris", true}, // always_allow beats block
		{"Paris, France", false},          // blocked
		{"Manchester, United Kingdom", true},
		{"Berlin", false}, // allow non-empty and unmatched
	} {
		if got := c.LocationOK(tc.loc); got != tc.want {
			t.Errorf("LocationOK(%q) = %v, want %v", tc.loc, got, tc.want)
		}
	}
	// No location_filter at all: everything passes.
	if !cfg(t, "max_age_hours: 1\n").LocationOK("Anywhere") {
		t.Error("an absent location_filter should pass everything")
	}
}

// Fail-closed: an undated posting is dropped rather than assumed fresh.
func TestAgeFilterIsFailClosed(t *testing.T) {
	c := cfg(t, "max_age_hours: 48\n")
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).UnixMilli()
	stale := now.Add(-100 * time.Hour).UnixMilli()

	if !c.AgeOK(providers.Entry{}, fresh, now) {
		t.Error("a 1-hour-old posting was dropped")
	}
	if c.AgeOK(providers.Entry{}, stale, now) {
		t.Error("a 100-hour-old posting passed a 48-hour filter")
	}
	if c.AgeOK(providers.Entry{}, 0, now) {
		t.Error("an undated posting passed — the filter must fail closed")
	}
	// A per-entry 0 disables it, for a source that exposes no dates at all.
	zero := 0
	if !c.AgeOK(providers.Entry{MaxAgeHours: &zero}, 0, now) {
		t.Error("max_age_hours: 0 on the entry should disable the filter")
	}
}

func TestRunFiltersAndDedups(t *testing.T) {
	c := cfg(t, `
max_age_hours: 0
title_filter:
  positive: [engineer]
location_filter:
  block: [france]
job_boards:
  - name: RemoteOK
    careers_url: https://remoteok.com/api
`)
	f := stub{pages: map[string]string{
		"https://remoteok.com/api": `[
		  {"position":"Backend Engineer","url":"https://x/1","company":"Acme","location":"London"},
		  {"position":"Backend Engineer","url":"https://x/1","company":"Acme","location":"London"},
		  {"position":"Designer","url":"https://x/2","location":"London"},
		  {"position":"Data Engineer","url":"https://x/3","location":"Paris, France"},
		  {"position":"Platform Engineer","url":"https://x/4","location":"Leeds"}
		]`,
	}}
	known := Known{URLs: map[string]bool{"https://x/4": true}}
	res := Run(context.Background(), c, f, known, time.Now())

	if len(res.Kept) != 1 || res.Kept[0].URL != "https://x/1" {
		t.Fatalf("kept %+v, want only the London engineer", res.Kept)
	}
	if res.Filtered.Title != 1 || res.Filtered.Location != 1 || res.Filtered.Duplicate != 2 {
		t.Errorf("counts are %+v, want 1 title, 1 location, 2 duplicate (one in-feed, one already seen)", res.Filtered)
	}
}

func TestAppendContinuesTheIDSequence(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "batch-input.tsv")
	if err := os.WriteFile(in, []byte("id\turl\tsource\tnotes\n1\thttps://a\tx\tn\n7\thttps://b\tx\tn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Append(in, []providers.Job{
		{Title: "Backend\tEngineer", URL: "https://c", Company: "Acme"},
	}, "pipeline-scan-2026-08-23")
	if err != nil || n != 1 {
		t.Fatalf("Append = %d, %v", n, err)
	}
	body, _ := os.ReadFile(in)
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "8\thttps://c\t") {
		t.Errorf("last row is %q, want id 8 continuing max+1", last)
	}
	if strings.Count(last, "\t") != 3 {
		t.Errorf("row has %d tabs, want exactly 3 — a tab in a cell shifts every column after it: %q",
			strings.Count(last, "\t"), last)
	}
	// Appending nothing must not touch the file.
	before, _ := os.ReadFile(in)
	if _, err := Append(in, nil, "x"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(in)
	if string(before) != string(after) {
		t.Error("an empty append rewrote the file")
	}
}

// An entry naming a provider the binary does not carry — `provider: apify` —
// used to fall through the same branch as a careers_url-only company and vanish
// without a word, which reads as "the board had nothing new".
func TestRunReportsAnEntryWhoseNamedProviderIsMissing(t *testing.T) {
	c := cfg(t, `
job_boards:
  - name: LinkedIn — Software Engineer
    provider: apify
  - name: Some Company
    careers_url: https://example.com/careers
`)
	res := Run(context.Background(), c, stub{}, Known{}, time.Now())
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "apify") {
		t.Fatalf("errors are %v, want one naming apify and none for the careers_url-only entry", res.Errors)
	}
}

// lazy is a provider whose postings can only be placed by a further call.
type lazy struct{ calls *int }

func (lazy) ID() string                  { return "test-lazy" }
func (lazy) Detect(providers.Entry) bool { return false }
func (l lazy) Fetch(context.Context, providers.Entry, providers.Fetcher) ([]providers.Job, error) {
	at := func(loc string) func(context.Context) string {
		return func(context.Context) string { *l.calls++; return loc }
	}
	return []providers.Job{
		{Title: "Backend Engineer", URL: "https://x/1", Locate: at("Paris, France")},
		{Title: "Backend Engineer", URL: "https://x/2", Locate: at("London, United Kingdom")},
		{Title: "Designer", URL: "https://x/3", Locate: at("London, United Kingdom")},
	}, nil
}

var lazyCalls int

func init() { providers.Register(lazy{&lazyCalls}) }

func TestRunLocatesOnlyWhatTheTitleKeeps(t *testing.T) {
	c := cfg(t, `
max_age_hours: 0
title_filter:
  positive: [engineer]
location_filter:
  allow: [united kingdom]
tracked_companies:
  - name: Acme
    provider: test-lazy
`)
	lazyCalls = 0
	res := Run(context.Background(), c, stub{}, Known{}, time.Now())
	if lazyCalls != 2 || len(res.Kept) != 1 || res.Kept[0].URL != "https://x/2" || res.Filtered.Location != 1 {
		t.Errorf("%d locate calls, kept %+v, filtered %+v: want 2 calls, the London engineer kept, Paris dropped", lazyCalls, res.Kept, res.Filtered)
	}
}

func TestAddBoardsSkipsWhatPortalsAlreadyTracks(t *testing.T) {
	c := cfg(t, "tracked_companies:\n  - name: Intercom\n    careers_url: https://job-boards.greenhouse.io/intercom\n")
	n := c.AddBoards([]providers.Entry{
		{Name: "Intercom R&D", CareersURL: "https://job-boards.greenhouse.io/Intercom/"},
		{Name: "Barclays", CareersURL: "https://barclays.wd3.myworkdayjobs.com/External"},
		{Name: "Barclays again", CareersURL: "https://barclays.wd3.myworkdayjobs.com/External"},
	})
	if n != 1 || len(c.TrackedCompanies) != 2 || c.TrackedCompanies[1].Name != "Barclays" {
		t.Errorf("added %d: %+v", n, c.TrackedCompanies)
	}
}
