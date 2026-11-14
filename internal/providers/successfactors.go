package providers

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SAP SuccessFactors boards come in two kinds, 116 of them in data/registry.tsv.
//
// The classic career portal, career<N>.successfactors.eu/career?company=<id>,
// lists every posting as XML with no key (resultType=XML). An item has a title,
// a req id, labelled filters the employer chose (Location, Country, City...) and,
// on 18 of the 52 portals with postings measured in October 2026, a Posted-Date.
// The rest are undated, which the scan's age filter drops. Dates are dd/mm/yyyy
// or mm/dd/yyyy by portal, whatever locale is asked for, so the order is read
// from the dates themselves. Items carry no link; the posting's page is built
// from the req id.
//
// Recruiting Marketing (RMK) sites run on the employer's own domain
// (careers.coloplast.com) and are known only by the registry's fingerprint, so
// the entry must say `ats: successfactors`. Each serves RSS at
// /services/rss/job/: one request with rows=5000 returns the whole site (paging
// parameters are ignored), every item dated, its title ending in where it is,
// "(Warrington, GB, WA1 1AA)".
//
// portals.yml:
//
//   - name: Coloplast
//     careers_url: https://careers.coloplast.com/
//     ats: successfactors
//     country: United Kingdom   # optional: only that country's postings
func init() { Register(successfactors{}) }

// sfMaxRows bounds an RMK feed. ponytail: one request; HCLTech's feed fills it,
// so a site that large loses whatever sorts past 5,000. sfMaxBytes is room for
// the largest measured, Cargill's 1,365 classic postings and HCLTech's 5,000.
const (
	sfMaxRows  = 5000
	sfMaxBytes = 64 << 20
)

var (
	sfClassicHost = regexp.MustCompile(`(?i)^career\d*\.(?:successfactors|sapsf)\.(?:com|eu)$`)
	sfTitleLoc    = regexp.MustCompile(`\s*\(([^()]*)\)\s*$`)
	sfLocLabel    = regexp.MustCompile(`(?i)location|city|town|country|county|region|state|province|site`)
	sfDate        = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
	sfCode        = regexp.MustCompile(`^[A-Z]{2}$`)
	sfDescription = regexp.MustCompile(`(?s)<Job-Description>.*?</Job-Description>|<>[^<]*</>`)
)

// sfCountries names the country codes RMK titles use. ponytail: only the
// registry's country; add a code when an entry asks for another.
var sfCountries = map[string]string{"GB": "United Kingdom", "UK": "United Kingdom"}

type successfactors struct{}

func (successfactors) ID() string { return "successfactors" }

// sfBoard is a classic portal (company set) or an RMK site. A classic host is
// pinned by its pattern; an RMK site is the careers URL's own host, fetched with
// redirects refused.
type sfBoard struct{ host, company string }

func sfBoardOf(e Entry) (sfBoard, bool) {
	u, err := url.Parse(e.CareersURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return sfBoard{}, false
	}
	if sfClassicHost.MatchString(u.Hostname()) {
		c := u.Query().Get("company")
		return sfBoard{host: u.Host, company: c}, c != ""
	}
	return sfBoard{host: u.Host}, e.Str("ats") == "successfactors"
}

func (successfactors) Detect(e Entry) bool { _, ok := sfBoardOf(e); return ok }

func (successfactors) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	b, ok := sfBoardOf(e)
	if !ok {
		return nil, fmt.Errorf("successfactors: cannot derive a feed URL for %s", e.Name)
	}
	country := strings.TrimSpace(e.Str("country"))
	// RMK answers 406 to the default Accept. A whole board is one response,
	// descriptions and all, so it gets more time and room than the defaults.
	o := Options{NoRedirect: true, Timeout: 90 * time.Second, MaxBytes: sfMaxBytes,
		Headers: map[string]string{"Accept": "application/rss+xml,application/xml,text/xml"}}
	if b.company != "" {
		text, err := f.Text(ctx, "https://"+b.host+"/career?company="+url.QueryEscape(b.company)+
			"&career_ns=job_listing_summary&resultType=XML", o)
		if err != nil {
			return nil, err
		}
		return sfClassic(text, b, e.Name, country, time.Now())
	}
	text, err := f.Text(ctx, "https://"+b.host+"/services/rss/job/?keywords=&rows="+strconv.Itoa(sfMaxRows), o)
	if err != nil {
		return nil, err
	}
	return sfRMK(text, e.Name, country)
}

// sfDecode reads a feed leniently, for the odd bare ampersand or HTML entity.
func sfDecode(text string, v any) error {
	d := xml.NewDecoder(strings.NewReader(text))
	d.Strict, d.Entity = false, xml.HTMLEntity // no AutoClose: it empties RSS's <link>
	return d.Decode(v)
}

func sfClassic(text string, b sfBoard, company, country string, now time.Time) ([]Job, error) {
	var feed struct {
		Jobs []struct {
			Title   string `xml:"JobTitle"`
			ReqID   string `xml:"ReqId"`
			Posted  string `xml:"Posted-Date"`
			Filters []struct {
				Label string `xml:"label"`
				Value string `xml:"value"`
			} `xml:",any"`
		} `xml:"Job"`
	}
	// Out before decoding: the descriptions, which nothing here reads, and the
	// nameless field some portals emit (British Sugar's "<>266</>"), which no XML
	// decoder gets past.
	if err := sfDecode(sfDescription.ReplaceAllString(text, ""), &feed); err != nil {
		return nil, fmt.Errorf("successfactors: %s: %w", b.company, err)
	}
	var dates []string
	for _, j := range feed.Jobs {
		dates = append(dates, strings.TrimSpace(j.Posted))
	}
	dayFirst := sfDayFirst(dates, now)
	var out []Job
	for _, j := range feed.Jobs {
		id, title := strings.TrimSpace(j.ReqID), strings.TrimSpace(j.Title)
		if id == "" || title == "" {
			continue
		}
		var parts []string
		stated := ""
		for _, fl := range j.Filters {
			v := strings.TrimSpace(fl.Value)
			if v == "" || !sfLocLabel.MatchString(fl.Label) {
				continue
			}
			if n, ok := sfCountries[strings.ToUpper(v)]; ok {
				v = n
			}
			if strings.Contains(strings.ToLower(fl.Label), "country") {
				stated = v
			}
			if !containsFold(parts, v) {
				parts = append(parts, v)
			}
		}
		if country != "" && stated != "" && !strings.Contains(strings.ToLower(stated), strings.ToLower(country)) {
			continue
		}
		out = append(out, Job{
			Title: title, Company: company, Location: strings.Join(parts, ", "),
			URL: "https://" + b.host + "/career?career_ns=job_listing&company=" + url.QueryEscape(b.company) +
				"&career_job_req_id=" + url.QueryEscape(id),
			PostedAt: sfPosted(strings.TrimSpace(j.Posted), dayFirst),
		})
	}
	return out, nil
}

// sfDayFirst reads a portal's date order from its dates: a first field over 12
// is a day, a second over 12 a day in the other order. With every field 12 or
// under, the order that puts the newest posting latest without passing now
// wins, since a live board has recent postings.
func sfDayFirst(dates []string, now time.Time) bool {
	var newestDM, newestMD int64
	for _, d := range dates {
		m := sfDate.FindStringSubmatch(d)
		if m == nil {
			continue
		}
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		switch {
		case a > 12:
			return true
		case b > 12:
			return false
		}
		if t := sfPosted(d, true); t <= now.UnixMilli() && t > newestDM {
			newestDM = t
		}
		if t := sfPosted(d, false); t <= now.UnixMilli() && t > newestMD {
			newestMD = t
		}
	}
	return newestDM >= newestMD
}

func sfPosted(d string, dayFirst bool) int64 {
	m := sfDate.FindStringSubmatch(d)
	if m == nil {
		return 0
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	y, _ := strconv.Atoi(m[3])
	day, month := a, b
	if !dayFirst {
		day, month = b, a
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0
	}
	return time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()
}

func sfRMK(text, company, country string) ([]Job, error) {
	var rss struct {
		Items []struct {
			Title   string `xml:"title"`
			Link    string `xml:"link"`
			PubDate string `xml:"pubDate"`
		} `xml:"channel>item"`
	}
	if err := sfDecode(text, &rss); err != nil {
		return nil, fmt.Errorf("successfactors: %w", err)
	}
	var out []Job
	for _, it := range rss.Items {
		u, err := url.Parse(strings.TrimSpace(it.Link))
		if err != nil || u.Scheme != "https" {
			continue
		}
		u.RawQuery, u.Fragment = "", "" // utm_* and feedId: the same posting every scan
		title, loc, stated := strings.TrimSpace(it.Title), "", ""
		if m := sfTitleLoc.FindStringSubmatchIndex(title); m != nil {
			parts := strings.Split(title[m[2]:m[3]], ",")
			for i, p := range parts {
				p = strings.TrimSpace(p)
				if sfCode.MatchString(p) { // the last one: "City, ON, CA, K1V 9B4" is Canada
					stated = p
					if n, ok := sfCountries[p]; ok {
						p, stated = n, n
					}
				}
				parts[i] = p
			}
			title, loc = strings.TrimSpace(title[:m[0]]), strings.Join(parts, ", ")
		}
		if country != "" && stated != "" && !strings.EqualFold(stated, country) {
			continue
		}
		var posted int64
		for _, layout := range []string{time.RFC1123, time.RFC1123Z} {
			if t, err := time.Parse(layout, strings.TrimSpace(it.PubDate)); err == nil {
				posted = t.UnixMilli()
				break
			}
		}
		out = append(out, Job{Title: title, URL: u.String(), Company: company, Location: loc, PostedAt: posted})
	}
	return out, nil
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
