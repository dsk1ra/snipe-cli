package providers

import (
	"context"
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// The aggregator boards: one feed each, every company on it. Unlike the ATS
// providers there is no per-company entry — one portals.yml row yields the whole
// board, and the title and location filters are what narrow it.

func init() {
	Register(remoteok{})
	Register(himalayas{})
	Register(hnHiring{})
}

// ── RemoteOK ─────────────────────────────────────────────────────────────────

const remoteOKAPI = "https://remoteok.com/api"

type remoteok struct{}

func (remoteok) ID() string { return "remoteok" }

func (remoteok) Detect(e Entry) bool {
	h, err := httpsHost(e.CareersURL)
	if err != nil {
		return false
	}
	return hostAllowed(strings.TrimPrefix(h, "www."), "remoteok.com", "remoteok.io")
}

func (remoteok) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	// The feed URL is fixed rather than taken from the entry: the entry only
	// says which board this is, and honouring an arbitrary careers_url here
	// would make portals.yml a way to point the scanner anywhere.
	var rows []struct {
		Position string `json:"position"`
		URL      string `json:"url"`
		ApplyURL string `json:"apply_url"`
		Company  string `json:"company"`
		Location string `json:"location"`
		Date     string `json:"date"`
		Epoch    int64  `json:"epoch"`
	}
	if err := f.JSON(ctx, remoteOKAPI, Options{NoRedirect: true}, &rows); err != nil {
		return nil, err
	}
	var out []Job
	for _, r := range rows {
		// The first element is a legal notice with no `position`.
		link := firstNonEmpty(r.URL, r.ApplyURL)
		if r.Position == "" || link == "" {
			continue
		}
		posted := epochMS(r.Date)
		if posted == 0 && r.Epoch > 0 {
			posted = r.Epoch * 1000
		}
		out = append(out, Job{
			Title: r.Position, URL: link,
			Company:  firstNonEmpty(r.Company, e.Name),
			Location: firstNonEmpty(r.Location, "Remote"),
			PostedAt: posted,
		})
	}
	return out, nil
}

// ── Himalayas ────────────────────────────────────────────────────────────────

const himalayasAPI = "https://himalayas.app/jobs/api"

type himalayas struct{}

func (himalayas) ID() string { return "himalayas" }

func (himalayas) Detect(e Entry) bool {
	h, err := httpsHost(e.CareersURL)
	return err == nil && hostAllowed(h, "himalayas.app")
}

func (himalayas) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	// pubDate is a unix epoch on some rows and an ISO string on others, and
	// locationRestrictions is a list or a bare string, so both are decoded late.
	type row struct {
		Title                string          `json:"title"`
		ApplicationLink      string          `json:"applicationLink"`
		GUID                 string          `json:"guid"`
		CompanyName          string          `json:"companyName"`
		LocationRestrictions json.RawMessage `json:"locationRestrictions"`
		PubDate              json.RawMessage `json:"pubDate"`
	}
	var resp struct {
		Jobs []row `json:"jobs"`
	}
	if err := f.JSON(ctx, himalayasAPI, Options{NoRedirect: true}, &resp); err != nil {
		return nil, err
	}
	var out []Job
	for _, r := range resp.Jobs {
		link := firstNonEmpty(r.ApplicationLink, r.GUID)
		if r.Title == "" || link == "" {
			continue
		}
		out = append(out, Job{
			Title: r.Title, URL: link,
			Company:  firstNonEmpty(r.CompanyName, e.Name),
			Location: firstNonEmpty(joinAny(r.LocationRestrictions), "Remote"),
			PostedAt: epochAny(r.PubDate),
		})
	}
	return out, nil
}

// joinAny renders a field that is a string on some rows and a list on others.
func joinAny(raw json.RawMessage) string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, ", ")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	return ""
}

// epochAny reads a date given as a number (seconds or milliseconds) or a string.
func epochAny(raw json.RawMessage) int64 {
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		if n > 1e12 {
			return int64(n)
		}
		return int64(n) * 1000
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return epochMS(s)
	}
	return 0
}

// ── Hacker News "Who is hiring?" ─────────────────────────────────────────────

const (
	hnSearchURL = "https://hn.algolia.com/api/v1/search_by_date?tags=story,author_whoishiring&query=hiring&hitsPerPage=10"
	hnItemURL   = "https://hn.algolia.com/api/v1/items/"
	hnMaxPosts  = 200
)

type hnHiring struct{}

func (hnHiring) ID() string { return "hn-hiring" }

func (hnHiring) Detect(e Entry) bool {
	h, err := httpsHost(e.CareersURL)
	return err == nil && hostAllowed(h, "hn.algolia.com")
}

// Fetch parses the monthly thread's top-level comments. Each is freeform prose
// loosely following "Company | Role | Location | REMOTE | https://apply…".
//
// Best-effort by design: a post with no URL is skipped, because there is nothing
// to evaluate or dedup on. This is a discovery aid, not a clean API.
func (hnHiring) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	var search struct {
		Hits []struct {
			Title    string `json:"title"`
			ObjectID string `json:"objectID"`
		} `json:"hits"`
	}
	if err := f.JSON(ctx, hnSearchURL, Options{NoRedirect: true}, &search); err != nil {
		return nil, err
	}
	var id string
	for _, h := range search.Hits {
		if hnWhoIsHiring.MatchString(h.Title) && !hnNotHiring.MatchString(h.Title) {
			id = h.ObjectID
			break
		}
	}
	if id == "" {
		return nil, nil
	}

	var item struct {
		Children []struct {
			Text      string `json:"text"`
			Author    string `json:"author"`
			CreatedAt int64  `json:"created_at_i"`
		} `json:"children"`
	}
	if err := f.JSON(ctx, hnItemURL+url.PathEscape(id), Options{NoRedirect: true}, &item); err != nil {
		return nil, err
	}

	var out []Job
	for _, c := range item.Children {
		if c.Text == "" || len(out) >= hnMaxPosts {
			continue
		}
		text := hnStripHTML(c.Text)
		link := hnFirstURL(text)
		if link == "" {
			continue
		}
		first := ""
		for _, l := range strings.Split(text, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				first = l
				break
			}
		}
		var parts []string
		for _, s := range strings.Split(first, "|") {
			if s = strings.TrimSpace(s); s != "" {
				parts = append(parts, s)
			}
		}
		title := first
		if len(parts) > 1 {
			title = parts[1]
		} else if len(parts) == 1 {
			title = parts[0]
		}
		if len(title) > 120 {
			title = title[:120]
		}
		loc := ""
		if hnRemote.MatchString(text) {
			loc = "Remote"
		}
		company := e.Name
		if len(parts) > 0 {
			company = hnCleanCompany(parts[0], firstNonEmpty(c.Author, "Unknown"))
		}
		out = append(out, Job{
			Title: title, URL: link, Company: company, Location: loc,
			PostedAt: c.CreatedAt * 1000,
		})
	}
	return out, nil
}

var (
	hnWhoIsHiring = regexp.MustCompile(`(?i)who\s+is\s+hiring\??`)
	hnNotHiring   = regexp.MustCompile(`(?i)wants to be hired|freelancer`)
	hnRemote      = regexp.MustCompile(`(?i)\bremote\b`)
	hnAnchor      = regexp.MustCompile(`(?is)<a\b[^>]*\bhref="([^"]+)"[^>]*>.*?</a>`)
	hnCloseP      = regexp.MustCompile(`(?i)</p>`)
	hnTag         = regexp.MustCompile(`<[^>]+>`)
	hnSpaces      = regexp.MustCompile(`[ \t]{2,}`)
	hnURL         = regexp.MustCompile(`(?i)https?://[^\s)<>"']+`)
	hnGoogleWrap  = regexp.MustCompile(`(?i)^https?://(?:www\.)?google\.com/url\?q=([^&]+)`)
	hnHiringNoise = regexp.MustCompile(`(?i)\b(hi|hey|hello)\s+hn\b|\bis hiring\b`)
)

// hnStripHTML keeps link targets — the apply URL is usually inside an anchor,
// so dropping the tag wholesale would drop the only thing that makes a post
// usable.
func hnStripHTML(s string) string {
	s = hnAnchor.ReplaceAllString(s, " $1 ")
	s = hnCloseP.ReplaceAllString(s, "\n")
	s = hnTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(hnSpaces.ReplaceAllString(s, " "))
}

func hnFirstURL(text string) string {
	m := hnURL.FindString(text)
	if m == "" {
		return ""
	}
	m = strings.TrimRight(m, ".,);")
	if w := hnGoogleWrap.FindStringSubmatch(m); w != nil {
		if dec, err := url.QueryUnescape(w[1]); err == nil {
			m = dec
		}
	}
	return m
}

// hnCleanCompany accepts a first segment only if it reads like a name: short,
// not prose, not a URL. Everything else falls back to the poster's handle.
func hnCleanCompany(seg, fallback string) string {
	s := strings.TrimSpace(seg)
	if s == "" || len(s) > 45 || hnHiringNoise.MatchString(s) || strings.Contains(s, "http") {
		return fallback
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
