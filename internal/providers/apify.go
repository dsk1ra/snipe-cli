package providers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Apify runs a hosted actor and maps its output onto Job. It exists for the
// platforms that refuse a plain HTTP client: LinkedIn, Indeed and Glassdoor.
//
// Unlike every other provider here it is a POST, and it costs money per run,
// which is why the results are cached on disk. A scan that reruns inside the
// TTL pays nothing and hits the cache instead.
//
// portals.yml:
//
//   - name: LinkedIn — Software Engineer
//     provider: apify
//     actor_id: curious_coder/linkedin-jobs-scraper
//     actor_input: { urls: ["https://…"], count: 100 }
//     cache_ttl_hours: 12      # optional, default 6
//     field_map: { title: positionName }   # optional, overrides the built-in
func init() { Register(apify{}) }

const (
	apifyBase       = "https://api.apify.com/v2"
	apifyTTL        = 6 * time.Hour
	apifyRunTimeout = 270 // seconds; the server-side ceiling is 300
	apifyMemoryMB   = 256
)

// apifyFieldMaps are the built-in mappings for the three actors this was
// written against. Dot notation resolves a nested field. An entry's field_map
// overrides these key by key rather than replacing the whole map, so naming one
// field does not silently drop the other four.
var apifyFieldMaps = map[string]map[string]string{
	"curious_coder/linkedin-jobs-scraper": {
		"title": "title", "url": "link", "company": "companyName",
		"location": "location", "postedAt": "postedAt",
	},
	"misceres/indeed-scraper": {
		"title": "positionName", "url": "url", "company": "company",
		"location": "location", "postedAt": "postingDateParsed",
	},
	"valig/glassdoor-jobs-scraper": {
		// applyUrl is the company's own ATS link, not the Glassdoor listing.
		"title": "title", "url": "applyUrl", "company": "employer.name",
		"location": "location.name", "postedAt": "ageInDays",
	},
}

type apify struct{}

func (apify) ID() string { return "apify" }

// Detect never claims an entry. There is no URL shape that means "this is an
// Apify actor", so an entry has to name the provider explicitly.
func (apify) Detect(Entry) bool { return false }

func (a apify) Fetch(ctx context.Context, e Entry, f Fetcher) ([]Job, error) {
	key := os.Getenv("APIFY_API_KEY")
	if key == "" {
		key = e.Str("api_key")
	}
	if key == "" {
		return nil, fmt.Errorf("%s: APIFY_API_KEY not set, and no api_key in portals.yml", e.Name)
	}
	actor := e.Str("actor_id")
	if actor == "" {
		return nil, fmt.Errorf("%s: actor_id is required", e.Name)
	}

	input, _ := e.Extra["actor_input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("%s: actor_input is not encodable: %w", e.Name, err)
	}

	ttl := apifyTTL
	if h, ok := toFloat(e.Extra["cache_ttl_hours"]); ok && h > 0 {
		ttl = time.Duration(h * float64(time.Hour))
	}
	cache := apifyCachePath(actor, body)
	if jobs, ok := readApifyCache(cache, ttl); ok {
		return jobs, nil
	}

	// The API takes the actor id with a tilde where the slug has a slash.
	//
	// The token goes in a header, never the query string. Apify documents both,
	// but a URL is echoed by every transport error this package raises, and a
	// credential in a warning line is a credential in a log file and a terminal
	// scrollback.
	endpoint := fmt.Sprintf("%s/acts/%s/run-sync-get-dataset-items?timeout=%d&memory=%d",
		apifyBase, strings.ReplaceAll(actor, "/", "~"), apifyRunTimeout, apifyMemoryMB)

	var raw []map[string]any
	if err := f.JSON(ctx, endpoint, Options{
		Method: "POST",
		Body:   body,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + key,
		},
		// An actor run is a job, not a request. The server-side ceiling is 300s
		// and the run asks for 270, so the client has to outlast both.
		Timeout: (apifyRunTimeout + 40) * time.Second,
	}, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", e.Name, err)
	}

	fm := map[string]string{}
	for k, v := range apifyFieldMaps[actor] {
		fm[k] = v
	}
	if over, ok := e.Extra["field_map"].(map[string]any); ok {
		for k, v := range over {
			if s, ok := v.(string); ok {
				fm[k] = s
			}
		}
	}

	var jobs []Job
	for _, item := range raw {
		if j, ok := apifyMap(item, fm); ok {
			jobs = append(jobs, j)
		}
	}
	writeApifyCache(cache, jobs)
	return jobs, nil
}

// apifyMap pulls one posting out of an actor's row. A row with no title or no
// URL is dropped rather than carried as a blank: the URL is the dedup key, so a
// missing one would collide with every other missing one.
func apifyMap(item map[string]any, fm map[string]string) (Job, bool) {
	pick := func(field, fallback string) string {
		path := fm[field]
		if path == "" {
			path = fallback
		}
		s, _ := apifyDig(item, path).(string)
		return strings.TrimSpace(s)
	}
	title := pick("title", "title")
	url := pick("url", "url")
	if title == "" || url == "" || url == "undefined" {
		return Job{}, false
	}
	return Job{
		Title:    title,
		URL:      url,
		Company:  pick("company", "company"),
		Location: pick("location", "location"),
		PostedAt: apifyPosted(apifyDig(item, orStr(fm["postedAt"], "postedAt"))),
	}, true
}

// apifyDig resolves a dot-notation path such as "employer.name".
func apifyDig(m map[string]any, path string) any {
	var cur any = m
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[part]
	}
	return cur
}

// apifyPosted normalises the three date shapes these actors emit: an ISO
// string, an epoch, and Glassdoor's age in days.
func apifyPosted(v any) int64 {
	switch t := v.(type) {
	case string:
		return epochMS(t)
	case float64:
		// A bare number this small is an age in days, not an epoch.
		if t > 0 && t < 10000 {
			return time.Now().Add(-time.Duration(t) * 24 * time.Hour).UnixMilli()
		}
		return int64(t)
	}
	return 0
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case uint64:
		return float64(t), true
	}
	return 0, false
}

func orStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ── cache ────────────────────────────────────────────────────────────────────
//
// Keyed on the actor and its input together, because the same actor with two
// different searches is two different result sets.

type apifyCache struct {
	SavedAt int64 `json:"saved_at"`
	Jobs    []Job `json:"jobs"`
}

func apifyCachePath(actor string, input []byte) string {
	sum := sha1.Sum(append([]byte(actor), input...))
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	slug := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(actor))
	return filepath.Join(base, "snipe-apify", slug+"-"+hex.EncodeToString(sum[:])[:16]+".json")
}

func readApifyCache(path string, ttl time.Duration) ([]Job, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var c apifyCache
	if json.Unmarshal(b, &c) != nil {
		return nil, false
	}
	if time.Since(time.UnixMilli(c.SavedAt)) > ttl {
		return nil, false
	}
	return c.Jobs, true
}

// writeApifyCache is best effort. A cache that cannot be written costs another
// actor run next scan, which is a bill, not a failure, so it must not abort one.
func writeApifyCache(path string, jobs []Job) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(apifyCache{SavedAt: time.Now().UnixMilli(), Jobs: jobs})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}
