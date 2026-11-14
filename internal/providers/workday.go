package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Workday career sites are a single-page app over an undocumented JSON API, the
// one the page itself calls: POST /wday/cxs/<tenant>/<site>/jobs. It is the ATS
// the large UK employers run — 310 boards in data/registry.tsv, against 174 on
// every other API-backed provider here combined.
//
// portals.yml:
//
//   - name: Barclays
//     careers_url: https://barclays.wd3.myworkdayjobs.com/External_Career_Site_Barclays
//     country: United Kingdom   # optional: only that country's postings
//
// The API's limits, measured on twelve tenants in October 2026: a page is 20
// postings and anything larger is a 400, the total is capped at 2,000 — Airbus
// and JLL both report exactly that — and past the total the API does not stop:
// Lloyds, with 80 postings, answered every offset to 1,200 with a full page of
// repeats.
func init() { Register(workday{}) }

const (
	wdPageSize = 20
	// wdMaxPages is the API's own 2,000-posting ceiling. A country facet keeps
	// most boards far under it: JLL's UK slice is 290 of its 2,000.
	wdMaxPages = 100
	// wdMaxProbes bounds one scan's detail reads that place a location in a
	// country. Mastercard lists 165 offices; what is left over is read next scan.
	wdMaxProbes = 100
)

var (
	wdJobsRe = regexp.MustCompile(`(?i)^https://([a-z0-9-]+)\.(wd\d+)\.myworkdayjobs\.com/(?:[a-z]{2}-[a-z]{2}/)?([a-z0-9_-]+)`)
	wdSiteRe = regexp.MustCompile(`(?i)^https://(wd\d+)\.myworkdaysite\.com/(?:[a-z]{2}-[a-z]{2}/)?recruiting/([a-z0-9-]+)/([a-z0-9_-]+)`)
	wdMulti  = regexp.MustCompile(`(?i)^\d+\s+locations$`)
	wdPosted = regexp.MustCompile(`(?i)posted\s+(today|yesterday|(\d+)\+?\s+days?\s+ago)`)
)

// wdBoard is one careers site: where its API is, and the base its postings'
// public pages hang off.
type wdBoard struct{ api, public, tenant string }

// workdayBoard reads a careers URL in either of Workday's two shapes. The host
// is pinned by the pattern itself, which is the SSRF guard here: nothing that
// does not end in a Workday domain becomes a request.
func workdayBoard(careersURL string) (wdBoard, bool) {
	if m := wdJobsRe.FindStringSubmatch(careersURL); m != nil && !strings.EqualFold(m[3], "wday") {
		tenant := strings.ToLower(m[1])
		host := tenant + "." + strings.ToLower(m[2]) + ".myworkdayjobs.com"
		return wdBoard{"https://" + host + "/wday/cxs/" + tenant + "/" + m[3], "https://" + host + "/" + m[3], tenant}, true
	}
	if m := wdSiteRe.FindStringSubmatch(careersURL); m != nil {
		host, tenant := strings.ToLower(m[1])+".myworkdaysite.com", strings.ToLower(m[2])
		return wdBoard{"https://" + host + "/wday/cxs/" + tenant + "/" + m[3], "https://" + host + "/recruiting/" + tenant + "/" + m[3], tenant}, true
	}
	return wdBoard{}, false
}

type workday struct{}

func (workday) ID() string { return "workday" }

func (workday) Detect(e Entry) bool { _, ok := workdayBoard(e.CareersURL); return ok }

// wdFacet is a facet or one of its values; the API nests one inside the other
// (locationMainGroup holds locationCountry, which holds the countries).
type wdFacet struct {
	FacetParameter string    `json:"facetParameter"`
	Descriptor     string    `json:"descriptor"`
	ID             string    `json:"id"`
	Values         []wdFacet `json:"values"`
}

type wdPosting struct {
	Title         string `json:"title"`
	ExternalPath  string `json:"externalPath"`
	LocationsText string `json:"locationsText"`
	PostedOn      string `json:"postedOn"`
}

type wdPage struct {
	Total       int         `json:"total"`
	JobPostings []wdPosting `json:"jobPostings"`
	Facets      []wdFacet   `json:"facets"`
}

func (b wdBoard) page(ctx context.Context, f Fetcher, facets map[string][]string, offset int) (wdPage, error) {
	body, err := json.Marshal(map[string]any{
		"appliedFacets": facets, "limit": wdPageSize, "offset": offset, "searchText": "",
	})
	if err != nil {
		return wdPage{}, err
	}
	var p wdPage
	err = f.JSON(ctx, b.api+"/jobs", Options{
		Method: "POST", Body: body, NoRedirect: true,
		Headers: map[string]string{"Content-Type": "application/json"},
	}, &p)
	return p, err
}

// Fetch pages through the board, filtered to the entry's country where the
// site offers a country facet.
//
// Every posting's location ends in its country when the country is known,
// because the list says "Filton" or "Canary Wharf, 1 Churchill Place" and the
// scan's location filter has to see "United Kingdom" to keep it. Where the site
// has no country facet — 136 of the 305 sites in the registry, Barclays, Lloyds
// and Mastercard among them — the country of each distinct location is read
// off one posting's detail record and kept, since an office does not move.
func (workday) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	b, ok := workdayBoard(e.CareersURL)
	if !ok {
		return nil, fmt.Errorf("workday: cannot derive an API URL for %s", e.Name)
	}
	country := strings.TrimSpace(e.Str("country"))
	facets := map[string][]string{}
	first, err := b.page(ctx, f, facets, 0)
	if err != nil {
		return nil, err
	}
	filtered := false
	if param, id, ok := wdCountry(first.Facets, country); country != "" && ok {
		facets[param], filtered = []string{id}, true
		if first, err = b.page(ctx, f, facets, 0); err != nil {
			return nil, err
		}
	}
	var all []wdPosting
	seen := map[string]bool{}
	page, total := first.JobPostings, first.Total
	for n := 1; ; n++ {
		fresh := 0
		for _, p := range page {
			if p.ExternalPath != "" && !seen[p.ExternalPath] {
				seen[p.ExternalPath] = true
				all = append(all, p)
				fresh++
			}
		}
		// The total ends the paging; a page with nothing new is the backstop
		// for a site whose first page gives none.
		if len(page) < wdPageSize || fresh == 0 || total > 0 && n*wdPageSize >= total || n == wdMaxPages {
			break
		}
		next, err := b.page(ctx, f, facets, n*wdPageSize)
		if err != nil {
			return nil, err
		}
		page = next.JobPostings
	}

	var places map[string]string // location → country, for a site with no facet
	cache := wdPlacesPath(b.tenant)
	if country != "" && !filtered {
		places = readPlaces(cache)
	}
	tried := map[string]bool{}
	now := time.Now()
	var out []Job
	for _, p := range all {
		loc := p.LocationsText
		multi := wdMulti.MatchString(loc)
		if multi {
			loc = "" // "3 Locations": the list does not say which
		}
		var locate func(context.Context) string
		switch {
		case filtered:
			loc = withCountry(loc, country)
		case places != nil && loc != "":
			if _, known := places[loc]; !known && !tried[loc] && len(tried) < wdMaxProbes {
				tried[loc] = true
				if c := b.country(ctx, f, p.ExternalPath); c != "" {
					places[loc] = c
				}
			}
			loc = withCountry(loc, places[loc])
		case places != nil && multi:
			// Reading every multi-location posting would be thousands of
			// requests a scan; the scan asks for the few its title filter keeps.
			path := p.ExternalPath
			locate = func(ctx context.Context) string { return b.locate(ctx, f, path, country, places) }
		}
		out = append(out, Job{
			Title: p.Title, URL: b.public + p.ExternalPath, Company: e.Name,
			Location: loc, PostedAt: workdayPosted(p.PostedOn, now), Locate: locate,
		})
	}
	if len(tried) > 0 {
		writePlaces(cache, places)
	}
	return out, nil
}

// wdPlacesPath is where a tenant's location countries are kept, beside the
// Apify cache: losing it costs detail reads, not data.
func wdPlacesPath(tenant string) string {
	return filepath.Join(cacheDir("snipe-workday"), tenant+".json")
}

func readPlaces(path string) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &m) == nil && m != nil {
		return m
	}
	return map[string]string{}
}

// writePlaces is best effort, as writeApifyCache is: a cache that cannot be
// written costs the same reads again next scan.
func writePlaces(path string, m map[string]string) {
	b, err := json.Marshal(m)
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

// wdCountry finds a country's value in the site's country facet. Sites name the
// facet differently — locationCountry under locationMainGroup, Location_Country
// at the top level — but the value IDs are Workday's own: the United Kingdom
// was 29247e57… on all seven sample sites that had the facet.
func wdCountry(fs []wdFacet, country string) (param, id string, ok bool) {
	for _, f := range fs {
		if strings.Contains(strings.ToLower(f.FacetParameter), "country") {
			for _, v := range f.Values {
				if v.ID != "" && strings.EqualFold(v.Descriptor, country) {
					return f.FacetParameter, v.ID, true
				}
			}
		}
		if param, id, ok = wdCountry(f.Values, country); ok {
			return param, id, ok
		}
	}
	return "", "", false
}

// country reads one posting's country off its detail record, or "" when it
// cannot, in which case the location stays as the list gave it.
func (b wdBoard) country(ctx context.Context, f Fetcher, path string) string {
	var d struct {
		Info struct {
			Country struct {
				Descriptor string `json:"descriptor"`
			} `json:"country"`
		} `json:"jobPostingInfo"`
	}
	if f.JSON(ctx, b.api+path, Options{NoRedirect: true}, &d) != nil {
		return ""
	}
	return d.Info.Country.Descriptor
}

// locate reads a multi-location posting's detail record and returns all its
// locations, ending in the wanted country when any of them is in it. The extra
// locations are office names, placed through what the scan has learned about
// this site's offices; one it has never seen on a single-location posting
// counts as elsewhere.
//
// places is only read here. Fetch has finished writing it before the scan
// calls this, from the goroutine that ran the Fetch.
func (b wdBoard) locate(ctx context.Context, f Fetcher, path, country string, places map[string]string) string {
	var d struct {
		Info struct {
			Location   string   `json:"location"`
			Additional []string `json:"additionalLocations"`
			Country    struct {
				Descriptor string `json:"descriptor"`
			} `json:"country"`
		} `json:"jobPostingInfo"`
	}
	if f.JSON(ctx, b.api+path, Options{NoRedirect: true}, &d) != nil {
		return "" // unreadable: a blank passes, as it would have without this
	}
	all := strings.Join(append([]string{d.Info.Location}, d.Info.Additional...), "; ")
	if strings.EqualFold(d.Info.Country.Descriptor, country) {
		return withCountry(all, country)
	}
	for _, l := range d.Info.Additional {
		if strings.EqualFold(places[l], country) {
			return withCountry(all, country)
		}
	}
	return withCountry(all, d.Info.Country.Descriptor)
}

func withCountry(loc, country string) string {
	switch {
	case country == "":
		return loc
	case loc == "":
		return country
	case strings.Contains(strings.ToLower(loc), strings.ToLower(country)):
		return loc
	}
	return loc + ", " + country
}

// workdayPosted reads "Posted Today", "Posted Yesterday", "Posted 3 Days Ago"
// and "Posted 30+ Days Ago". The list gives nothing finer than a day, and "30+"
// is a floor, which the age filter needs no more than.
func workdayPosted(s string, now time.Time) int64 {
	m := wdPosted.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	days := 0
	switch strings.ToLower(m[1]) {
	case "today":
	case "yesterday":
		days = 1
	default:
		days, _ = strconv.Atoi(m[2])
	}
	return now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
}
