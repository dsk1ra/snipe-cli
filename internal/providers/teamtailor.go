package providers

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Teamtailor boards publish every open posting as RSS at /jobs.rss, with no key.
// 139 boards in data/registry.tsv, most on the employer's own domain
// (careers.spacenk.com), which no URL pattern can tell apart from any other
// careers site. So a board is Teamtailor's when its host is <slug>.teamtailor.com
// or its entry says so: the registry passes the ATS it fingerprinted as `ats`.
//
// portals.yml:
//
//   - name: Space NK
//     careers_url: https://careers.spacenk.com/
//     ats: teamtailor
//     country: United Kingdom   # optional: only that country's postings
//
// Measured on 30 boards in October 2026: a page is 100 items and ?offset= pages
// on (SD Worx, 142 postings, answers 100 then 42); every item has a pubDate and
// all but 4 of 523 a location; links may sit on another host than the feed.
func init() { Register(teamtailor{}) }

const (
	ttPageSize = 100
	ttMaxPages = 20
)

type teamtailor struct{}

func (teamtailor) ID() string { return "teamtailor" }

// feed is the board's RSS URL. The host is the careers URL's own and redirects
// are refused, so the request goes only where the registry or portals.yml said.
func (teamtailor) feed(e Entry) (string, bool) {
	u, err := url.Parse(e.CareersURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), ".teamtailor.com") && e.Str("ats") != "teamtailor" {
		return "", false
	}
	return "https://" + u.Host + "/jobs.rss", true
}

func (t teamtailor) Detect(e Entry) bool { _, ok := t.feed(e); return ok }

type ttItem struct {
	Title     string `xml:"title"`
	Link      string `xml:"link"`
	PubDate   string `xml:"pubDate"`
	Remote    string `xml:"remoteStatus"`
	Locations []struct {
		Name    string `xml:"name"`
		City    string `xml:"city"`
		Country string `xml:"country"`
	} `xml:"locations>location"`
}

func (t teamtailor) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	feed, ok := t.feed(e)
	if !ok {
		return nil, fmt.Errorf("teamtailor: cannot derive a feed URL for %s", e.Name)
	}
	country := strings.TrimSpace(e.Str("country"))
	var out []Job
	for page := 0; page < ttMaxPages; page++ {
		u := feed
		if page > 0 {
			u += "?offset=" + strconv.Itoa(page*ttPageSize)
		}
		text, err := f.Text(ctx, u, Options{NoRedirect: true})
		if err != nil {
			return nil, err
		}
		var rss struct {
			Items []ttItem `xml:"channel>item"`
		}
		if err := xml.Unmarshal([]byte(text), &rss); err != nil {
			return nil, fmt.Errorf("teamtailor: %s: %w", u, err)
		}
		for _, it := range rss.Items {
			if j, ok := ttJob(it, e.Name, country); ok {
				out = append(out, j)
			}
		}
		if len(rss.Items) < ttPageSize {
			break
		}
	}
	return out, nil
}

// ttJob is one item as a posting, or false when it has no HTTPS link or, with a
// country asked for, lists locations none of which are in it. A posting with no
// location is kept for the scan's keyword tiers to place. Its location names
// only the asked-for country's places, each marked remote when the post is.
func ttJob(it ttItem, company, country string) (Job, bool) {
	if _, err := httpsHost(it.Link); err != nil {
		return Job{}, false
	}
	var locs []string
	for _, l := range it.Locations {
		if country != "" && !strings.EqualFold(strings.TrimSpace(l.Country), country) {
			continue
		}
		s := withCountry(firstNonEmpty(strings.TrimSpace(l.City), strings.TrimSpace(l.Name)), strings.TrimSpace(l.Country))
		if it.Remote == "fully" {
			s += " (Remote)"
		}
		locs = append(locs, s)
	}
	if len(locs) == 0 && len(it.Locations) > 0 {
		return Job{}, false
	}
	var posted int64
	if ts, err := time.Parse(time.RFC1123Z, strings.TrimSpace(it.PubDate)); err == nil {
		posted = ts.UnixMilli()
	}
	return Job{
		Title: strings.TrimSpace(it.Title), URL: strings.TrimSpace(it.Link), Company: company,
		Location: strings.Join(locs, "; "), PostedAt: posted,
	}, true
}
