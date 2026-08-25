package providers

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func init() {
	Register(greenhouse{})
	Register(lever{})
	Register(ashby{})
	Register(recruitee{})
	Register(workable{})
	Register(smartrecruiters{})
}

// ── Greenhouse ───────────────────────────────────────────────────────────────

var greenhouseHosts = []string{
	"boards-api.greenhouse.io", "boards.greenhouse.io",
	"job-boards.greenhouse.io", "job-boards.eu.greenhouse.io",
}

var ghBoardRe = regexp.MustCompile(`job-boards(?:\.eu)?\.greenhouse\.io/([^/?#]+)`)

type greenhouse struct{}

func (greenhouse) ID() string { return "greenhouse" }

func (g greenhouse) api(e Entry) (string, error) {
	if e.API != "" {
		if err := assertHost("greenhouse", e.API, greenhouseHosts...); err != nil {
			return "", err
		}
		return e.API, nil
	}
	if m := ghBoardRe.FindStringSubmatch(e.CareersURL); m != nil {
		return "https://boards-api.greenhouse.io/v1/boards/" + m[1] + "/jobs", nil
	}
	return "", fmt.Errorf("greenhouse: cannot derive an API URL for %s", e.Name)
}

func (g greenhouse) Detect(e Entry) bool {
	_, err := g.api(e)
	return err == nil
}

func (g greenhouse) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	api, err := g.api(e)
	if err != nil {
		return nil, err
	}
	if err := assertHost("greenhouse", api, greenhouseHosts...); err != nil {
		return nil, err
	}
	var resp struct {
		Jobs []struct {
			Title       string `json:"title"`
			AbsoluteURL string `json:"absolute_url"`
			Location    struct {
				Name string `json:"name"`
			} `json:"location"`
			FirstPublished string `json:"first_published"`
		} `json:"jobs"`
	}
	if err := f.JSON(ctx, api, Options{NoRedirect: true}, &resp); err != nil {
		return nil, err
	}
	var out []Job
	for _, j := range resp.Jobs {
		if j.AbsoluteURL == "" {
			continue
		}
		out = append(out, Job{
			Title: j.Title, URL: j.AbsoluteURL, Company: e.Name,
			Location: j.Location.Name, PostedAt: epochMS(j.FirstPublished),
		})
	}
	return out, nil
}

// ── Lever ────────────────────────────────────────────────────────────────────

var leverCoRe = regexp.MustCompile(`jobs\.lever\.co/([^/?#]+)`)

type lever struct{}

func (lever) ID() string { return "lever" }

func (l lever) api(e Entry) (string, bool) {
	if m := leverCoRe.FindStringSubmatch(e.CareersURL); m != nil {
		return "https://api.lever.co/v0/postings/" + m[1] + "?mode=json", true
	}
	return "", false
}

func (l lever) Detect(e Entry) bool { _, ok := l.api(e); return ok }

func (l lever) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	api, ok := l.api(e)
	if !ok {
		return nil, fmt.Errorf("lever: cannot derive an API URL for %s", e.Name)
	}
	var resp []struct {
		Text       string `json:"text"`
		HostedURL  string `json:"hostedUrl"`
		CreatedAt  int64  `json:"createdAt"`
		Categories struct {
			Location string `json:"location"`
		} `json:"categories"`
	}
	if err := f.JSON(ctx, api, Options{}, &resp); err != nil {
		return nil, err
	}
	var out []Job
	for _, j := range resp {
		out = append(out, Job{
			Title: j.Text, URL: j.HostedURL, Company: e.Name,
			Location: j.Categories.Location, PostedAt: j.CreatedAt,
		})
	}
	return out, nil
}

// ── Ashby ────────────────────────────────────────────────────────────────────

var ashbyCoRe = regexp.MustCompile(`jobs\.ashbyhq\.com/([^/?#]+)`)

// Ashby rate-limits a bulk scan, so its fetch backs off rather than giving up.
const (
	ashbyRetries = 2
	ashbyTimeout = 20 * time.Second
)

type ashby struct{}

func (ashby) ID() string { return "ashby" }

// ashbyHosts is what an explicit `api:` must point at to be an Ashby board.
//
// Without the check this claimed every entry that set `api:` at all, whatever it
// pointed at — and because providers are tried in id order, "ashby" sorts ahead
// of "greenhouse" and took all 48 tracked companies whose api: is a Greenhouse
// board with it. They failed on the payload shape, one WARN each, which reads as
// 48 boards being down rather than as one line of detection.
var ashbyHosts = []string{"api.ashbyhq.com"}

func (a ashby) api(e Entry) (string, bool) {
	if e.API != "" {
		return e.API, assertHost("ashby", e.API, ashbyHosts...) == nil
	}
	if m := ashbyCoRe.FindStringSubmatch(e.CareersURL); m != nil {
		return "https://api.ashbyhq.com/posting-api/job-board/" + m[1] + "?includeCompensation=true", true
	}
	return "", false
}

func (a ashby) Detect(e Entry) bool { _, ok := a.api(e); return ok }

func (a ashby) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	api, ok := a.api(e)
	if !ok {
		return nil, fmt.Errorf("ashby: cannot derive an API URL for %s", e.Name)
	}
	// Belt and braces, as greenhouse does: the allowlist that decides detection
	// also has to hold at the moment of the request.
	if err := assertHost("ashby", api, ashbyHosts...); err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= ashbyRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter. The jitter matters as much as the
			// backoff: a bulk scan retrying on a fixed cadence re-collides with
			// itself.
			wait := time.Duration(1<<(attempt-1))*time.Second + time.Duration(rand.Intn(500))*time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		var resp struct {
			Jobs []struct {
				Title       string `json:"title"`
				JobURL      string `json:"jobUrl"`
				Location    string `json:"location"`
				PublishedAt string `json:"publishedAt"`
			} `json:"jobs"`
		}
		if err := f.JSON(ctx, api, Options{}, &resp); err != nil {
			lastErr = err
			continue
		}
		var out []Job
		for _, j := range resp.Jobs {
			out = append(out, Job{
				Title: j.Title, URL: j.JobURL, Company: e.Name,
				Location: j.Location, PostedAt: epochMS(j.PublishedAt),
			})
		}
		return out, nil
	}
	return nil, fmt.Errorf("ashby: %s after %d retries: %w", e.Name, ashbyRetries, lastErr)
}

// ── Recruitee ────────────────────────────────────────────────────────────────

var recruiteeSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type recruitee struct{}

func (recruitee) ID() string { return "recruitee" }

func (r recruitee) slug(e Entry) (string, bool) {
	host, err := httpsHost(e.CareersURL)
	if err != nil {
		return "", false
	}
	slug, rest, found := strings.Cut(host, ".")
	if !found || rest != "recruitee.com" || !recruiteeSlugRe.MatchString(slug) {
		return "", false
	}
	return slug, true
}

func (r recruitee) Detect(e Entry) bool { _, ok := r.slug(e); return ok }

func (r recruitee) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	slug, ok := r.slug(e)
	if !ok {
		return nil, fmt.Errorf("recruitee: cannot derive an API URL for %s", e.Name)
	}
	api := fmt.Sprintf("https://%s.recruitee.com/api/offers/", slug)
	var resp struct {
		Offers []struct {
			Title       string `json:"title"`
			CareersURL  string `json:"careers_url"`
			URL         string `json:"url"`
			City        string `json:"city"`
			Country     string `json:"country"`
			Location    string `json:"location"`
			Remote      bool   `json:"remote"`
			PublishedAt string `json:"published_at"`
		} `json:"offers"`
	}
	if err := f.JSON(ctx, api, Options{NoRedirect: true}, &resp); err != nil {
		return nil, err
	}
	want := slug + ".recruitee.com"
	var out []Job
	for _, o := range resp.Offers {
		link := o.CareersURL
		if link == "" {
			link = o.URL
		}
		// An off-domain link is dropped rather than emitted: the URL is the
		// dedup key and everything downstream treats it as trustworthy.
		if host, err := httpsHost(link); err != nil || host != want {
			link = ""
		}
		out = append(out, Job{
			Title: o.Title, URL: link, Company: e.Name,
			Location: recruiteeLocation(o.Location, o.City, o.Country, o.Remote),
			PostedAt: epochMS(o.PublishedAt),
		})
	}
	return out, nil
}

func recruiteeLocation(explicit, city, country string, remote bool) string {
	if explicit != "" {
		return explicit
	}
	var parts []string
	for _, p := range []string{city, country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if remote {
		parts = append(parts, "Remote")
	}
	return strings.Join(parts, ", ")
}

// ── Workable ─────────────────────────────────────────────────────────────────

var workableSlugRe = regexp.MustCompile(`apply\.workable\.com/([^/?#]+)`)

type workable struct{}

func (workable) ID() string { return "workable" }

func (w workable) feed(e Entry) (string, bool) {
	if m := workableSlugRe.FindStringSubmatch(e.CareersURL); m != nil {
		return "https://apply.workable.com/" + m[1] + "/?format=md", true
	}
	return "", false
}

func (w workable) Detect(e Entry) bool { _, ok := w.feed(e); return ok }

func (w workable) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	feed, ok := w.feed(e)
	if !ok {
		return nil, fmt.Errorf("workable: cannot derive a feed URL for %s", e.Name)
	}
	if err := assertHost("workable", feed, "apply.workable.com"); err != nil {
		return nil, err
	}
	text, err := f.Text(ctx, feed, Options{NoRedirect: true})
	if err != nil {
		return nil, err
	}
	return ParseWorkableMarkdown(text, e.Name), nil
}

var mdLinkRe = regexp.MustCompile(`\[View\]\(([^)]+)\)`)

// ParseWorkableMarkdown reads Workable's public markdown feed:
//
//	| Title | Department | Location | Type | Salary | Posted | Details |
//
// where Details holds a markdown link to the posting. Exported so the parse is
// testable without a transport.
func ParseWorkableMarkdown(text, company string) []Job {
	var out []Job
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := strings.Split(line, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) < 8 {
			continue
		}
		m := mdLinkRe.FindStringSubmatch(cells[7])
		if m == nil {
			continue
		}
		// Off-domain or non-HTTPS View links are skipped, not emitted.
		if host, err := httpsHost(m[1]); err != nil || host != "apply.workable.com" {
			continue
		}
		out = append(out, Job{Title: cells[1], URL: m[1], Company: company, Location: cells[3]})
	}
	return out
}

// ── SmartRecruiters ──────────────────────────────────────────────────────────

const (
	srPageSize = 100
	srMaxPages = 10
)

var srSlugRe = regexp.MustCompile(`smartrecruiters\.com/([^/?#]+)`)

type smartrecruiters struct{}

func (smartrecruiters) ID() string { return "smartrecruiters" }

func (s smartrecruiters) slug(e Entry) (string, bool) {
	if m := srSlugRe.FindStringSubmatch(e.CareersURL); m != nil {
		return m[1], true
	}
	return "", false
}

func (s smartrecruiters) Detect(e Entry) bool { _, ok := s.slug(e); return ok }

func (s smartrecruiters) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	slug, ok := s.slug(e)
	if !ok {
		return nil, fmt.Errorf("smartrecruiters: cannot derive an API URL for %s", e.Name)
	}
	var all []Job
	for page := 0; page < srMaxPages; page++ {
		api := fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/%s/postings?limit=%d&offset=%d",
			url.PathEscape(slug), srPageSize, page*srPageSize)
		if err := assertHost("smartrecruiters", api, "api.smartrecruiters.com"); err != nil {
			return nil, err
		}
		var resp struct {
			Content []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Ref      string `json:"ref"`
				Released string `json:"releasedDate"`
				Location struct {
					FullLocation string `json:"fullLocation"`
					City         string `json:"city"`
					Region       string `json:"region"`
					Country      string `json:"country"`
					Remote       bool   `json:"remote"`
				} `json:"location"`
			} `json:"content"`
		}
		if err := f.JSON(ctx, api, Options{NoRedirect: true}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Content) == 0 {
			break
		}
		for _, p := range resp.Content {
			link := p.Ref
			if link == "" {
				link = fmt.Sprintf("https://jobs.smartrecruiters.com/%s/%s", slug, p.ID)
			}
			all = append(all, Job{
				Title: p.Name, URL: link, Company: e.Name,
				Location: recruiteeLocation(p.Location.FullLocation, p.Location.City, p.Location.Country, p.Location.Remote),
				PostedAt: epochMS(p.Released),
			})
		}
		if len(resp.Content) < srPageSize {
			break // a short page is the last one
		}
	}
	return all, nil
}
