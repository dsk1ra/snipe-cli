package phase1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// maxJD is how much of a posting is kept. Past this it is boilerplate.
const maxJD = 16_000

// ErrUnavailable means the posting is definitively gone or blocked. It is not
// retried: the runner marks the row unavailable and never fetches it again.
var ErrUnavailable = errors.New("posting unavailable")

const userAgent = "Mozilla/5.0 (compatible; snipe/1.0)"

func httpGet(ctx context.Context, target string, asJSON bool, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	if asJSON {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// 404 is expired, 403 is blocked. Neither recovers, so neither is retried.
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusForbidden {
			return "", fmt.Errorf("%w: HTTP %d from %s — posting expired or access blocked",
				ErrUnavailable, res.StatusCode, target)
		}
		return "", fmt.Errorf("HTTP %d from %s", res.StatusCode, target)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if asJSON {
		return "", json.Unmarshal(body, out)
	}
	return string(body), nil
}

var (
	scriptRe  = regexp.MustCompile(`(?is)<script.*?</script>`)
	styleRe   = regexp.MustCompile(`(?is)<style.*?</style>`)
	tagRe     = regexp.MustCompile(`<[^>]+>`)
	numEntRe  = regexp.MustCompile(`&#\d+;`)
	nameEntRe = regexp.MustCompile(`&[a-z]{2,6};`)
	wsRe      = regexp.MustCompile(`\s{2,}`)
)

var htmlEntities = strings.NewReplacer(
	"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ",
)

// stripHTML is the fallback for boards with no API. Deliberately the same
// sequence as the Node version, entity by entity: it is what every cached JD in
// batch/jds was produced with, and the equivalence gate reads those.
func stripHTML(html string) string {
	s := scriptRe.ReplaceAllString(html, "")
	s = styleRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, " ")
	s = htmlEntities.Replace(s)
	s = numEntRe.ReplaceAllString(s, " ")
	s = nameEntRe.ReplaceAllString(s, " ")
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var (
	ghDirectRe = regexp.MustCompile(`greenhouse\.io/([^/?#]+)/jobs/(\d+)`)
	ashbyRe    = regexp.MustCompile(`jobs\.ashbyhq\.com/([^/?#]+)/([a-fA-F0-9-]{36})`)
	leverRe    = regexp.MustCompile(`jobs\.lever\.co/([^/?#]+)/([a-fA-F0-9-]{36})`)
)

// fromProvider hits the board's own API where one exists, which avoids
// JS-rendered pages entirely. Returns "" when no provider matches, and the
// caller falls back to HTML.
func fromProvider(ctx context.Context, raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", nil
	}

	if m := ghDirectRe.FindStringSubmatch(raw); m != nil {
		return greenhouse(ctx, m[1], m[2])
	}
	// Greenhouse embedded in a company's own careers page via ?gh_jid=.
	if jid := parsed.Query().Get("gh_jid"); jid != "" {
		board := parsed.Query().Get("board")
		if board == "" {
			board = strings.Split(strings.TrimPrefix(parsed.Hostname(), "www."), ".")[0]
		}
		return greenhouse(ctx, board, jid)
	}

	if m := ashbyRe.FindStringSubmatch(raw); m != nil {
		return ashby(ctx, m[1], m[2])
	}
	if m := leverRe.FindStringSubmatch(raw); m != nil {
		return lever(ctx, m[1], m[2])
	}
	return "", nil
}

func greenhouse(ctx context.Context, board, jobID string) (string, error) {
	var j struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	api := fmt.Sprintf("https://boards-api.greenhouse.io/v1/boards/%s/jobs/%s", board, jobID)
	if _, err := httpGet(ctx, api, true, &j); err != nil {
		return "", err
	}
	return clip(j.Title+"\n\n"+stripHTML(j.Content), maxJD), nil
}

func ashby(ctx context.Context, company, postingID string) (string, error) {
	var j struct {
		Jobs        []ashbyPost `json:"jobs"`
		JobPostings []ashbyPost `json:"jobPostings"`
	}
	api := fmt.Sprintf("https://api.ashbyhq.com/posting-api/job-board/%s?includeCompensation=true", company)
	if _, err := httpGet(ctx, api, true, &j); err != nil {
		return "", err
	}
	all := append(j.Jobs, j.JobPostings...)
	for _, p := range all {
		if !strings.EqualFold(p.ID, postingID) {
			continue
		}
		desc := p.DescriptionPlain
		if desc == "" {
			desc = stripHTML(firstNonEmpty(p.DescriptionHTML, p.DescriptionSafeHTML))
		}
		loc := ""
		if p.LocationName != "" {
			loc = "Location: " + p.LocationName
		}
		return clip(p.Title+"\n"+loc+"\n\n"+desc, maxJD), nil
	}
	// Not in the board listing means definitively closed, not a transient error.
	return "", fmt.Errorf("%w: Ashby posting %s no longer listed for %s — likely closed",
		ErrUnavailable, postingID, company)
}

type ashbyPost struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	LocationName        string `json:"locationName"`
	DescriptionPlain    string `json:"descriptionPlain"`
	DescriptionHTML     string `json:"descriptionHtml"`
	DescriptionSafeHTML string `json:"descriptionSafeHtml"`
}

func lever(ctx context.Context, company, postingID string) (string, error) {
	var j struct {
		Text             string          `json:"text"`
		DescriptionPlain string          `json:"descriptionPlain"`
		Description      string          `json:"description"`
		Categories       json.RawMessage `json:"categories"`
		Lists            []struct {
			Text    string `json:"text"`
			Content string `json:"content"`
		} `json:"lists"`
	}
	api := fmt.Sprintf("https://api.lever.co/v0/postings/%s/%s", company, postingID)
	if _, err := httpGet(ctx, api, true, &j); err != nil {
		return "", err
	}
	desc := j.DescriptionPlain
	if desc == "" {
		desc = stripHTML(j.Description)
	}
	var lists []string
	for _, l := range j.Lists {
		lists = append(lists, l.Text+":\n"+l.Content)
	}
	return clip(j.Text+"\n"+string(j.Categories)+"\n\n"+desc+"\n\n"+strings.Join(lists, "\n\n"), maxJD), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// FetchJD returns the posting text, preferring a cached copy.
//
// The cache is what makes the whole pipeline re-runnable offline and what a
// pasted JD writes into — nothing is ever fetched for a manual offer.
func FetchJD(ctx context.Context, rawURL, cachePath string, timeout time.Duration) (string, error) {
	if b, err := os.ReadFile(cachePath); err == nil {
		if s := strings.TrimSpace(string(b)); len(s) > 50 {
			return s, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Provider APIs first. An ErrUnavailable from one is final — do not fall
	// back to scraping a page the board has already said is gone.
	text, err := fromProvider(ctx, rawURL)
	if errors.Is(err, ErrUnavailable) {
		return "", err
	}
	if err == nil && len(text) > 100 {
		return text, nil
	}

	html, err := httpGet(ctx, rawURL, false, nil)
	if err != nil {
		return "", err
	}
	stripped := stripHTML(html)
	if len(stripped) < 100 {
		return "", fmt.Errorf("JD too short after stripping HTML (%d chars) — the page is probably JS-rendered and no provider handler matched", len(stripped))
	}
	return clip(stripped, maxJD), nil
}
