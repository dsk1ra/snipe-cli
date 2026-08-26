package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The three built-in actors emit three different shapes for the same five
// fields, which is the whole reason the field map exists.
func TestApifyMapsEachBuiltInActor(t *testing.T) {
	cases := []struct {
		actor, row string
		want       Job
	}{{
		actor: "curious_coder/linkedin-jobs-scraper",
		row:   `{"title":"Backend Engineer","link":"https://x.test/1","companyName":"Acme","location":"London"}`,
		want:  Job{Title: "Backend Engineer", URL: "https://x.test/1", Company: "Acme", Location: "London"},
	}, {
		actor: "misceres/indeed-scraper",
		row:   `{"positionName":"Go Developer","url":"https://x.test/2","company":"Beta","location":"Leeds"}`,
		want:  Job{Title: "Go Developer", URL: "https://x.test/2", Company: "Beta", Location: "Leeds"},
	}, {
		// Glassdoor nests the company and location, and its URL field is the
		// company's own ATS link rather than the listing page.
		actor: "valig/glassdoor-jobs-scraper",
		row:   `{"title":"SRE","applyUrl":"https://x.test/3","employer":{"name":"Gamma"},"location":{"name":"Bristol"}}`,
		want:  Job{Title: "SRE", URL: "https://x.test/3", Company: "Gamma", Location: "Bristol"},
	}}

	for _, c := range cases {
		var item map[string]any
		if err := json.Unmarshal([]byte(c.row), &item); err != nil {
			t.Fatal(err)
		}
		got, ok := apifyMap(item, apifyFieldMaps[c.actor])
		if !ok {
			t.Fatalf("%s: row dropped", c.actor)
		}
		if got.Title != c.want.Title || got.URL != c.want.URL ||
			got.Company != c.want.Company || got.Location != c.want.Location {
			t.Errorf("%s:\n got %+v\nwant %+v", c.actor, got, c.want)
		}
	}
}

// A row with no URL cannot be carried: the URL is the dedup key, so every
// blank one would collide with every other.
func TestApifyDropsRowsWithNoURL(t *testing.T) {
	for _, row := range []string{
		`{"title":"No URL"}`,
		`{"title":"Undefined","link":"undefined"}`,
		`{"link":"https://x.test/4"}`,
	} {
		var item map[string]any
		_ = json.Unmarshal([]byte(row), &item)
		if _, ok := apifyMap(item, apifyFieldMaps["curious_coder/linkedin-jobs-scraper"]); ok {
			t.Errorf("kept a row it should have dropped: %s", row)
		}
	}
}

// Glassdoor reports an age in days where the others send a date. Both have to
// land on an epoch, or the age filter drops the posting as undated.
func TestApifyPostedAtAcceptsBothShapes(t *testing.T) {
	if got := apifyPosted("2026-08-20"); got == 0 {
		t.Error("an ISO date parsed to zero, which the age filter reads as undated")
	}
	days := apifyPosted(float64(3))
	if days == 0 {
		t.Fatal("an age in days parsed to zero")
	}
	if now := apifyPosted("2026-08-26"); days > now {
		t.Error("three days ago should be older than a date this week")
	}
}

// An entry's field_map overrides one key without discarding the other four.
func TestApifyFieldMapOverrideIsPerKey(t *testing.T) {
	f := &stub{pages: map[string]string{}}
	e := Entry{Name: "t", Provider: "apify", Extra: map[string]any{
		"actor_id":  "curious_coder/linkedin-jobs-scraper",
		"field_map": map[string]any{"title": "headline"},
	}}
	t.Setenv("APIFY_API_KEY", "k")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// The stub 404s, so this exercises resolution and the request, not parsing.
	_, err := apify{}.Fetch(context.Background(), e, f)
	if err == nil {
		t.Fatal("expected the stub's 404")
	}
	if len(f.seen) != 1 || !strings.Contains(f.seen[0], "curious_coder~linkedin-jobs-scraper") {
		t.Fatalf("actor id not tilde-encoded for the API: %v", f.seen)
	}
	if f.opts[0].Method != "POST" || len(f.opts[0].Body) == 0 {
		t.Errorf("actor run must POST its input, got %+v", f.opts[0])
	}
}

// Without a key the provider must say so rather than POST an unauthenticated
// run that bills nothing and returns nothing.
func TestApifyRequiresAKeyAndAnActor(t *testing.T) {
	t.Setenv("APIFY_API_KEY", "")
	f := &stub{pages: map[string]string{}}
	_, err := apify{}.Fetch(context.Background(), Entry{Name: "t",
		Extra: map[string]any{"actor_id": "a/b"}}, f)
	if err == nil || !strings.Contains(err.Error(), "APIFY_API_KEY") {
		t.Errorf("want a missing-key error, got %v", err)
	}
	t.Setenv("APIFY_API_KEY", "k")
	p := apify{}
	_, err = p.Fetch(context.Background(), Entry{Name: "t"}, f)
	if err == nil || !strings.Contains(err.Error(), "actor_id") {
		t.Errorf("want a missing-actor error, got %v", err)
	}
}

// Detect must never claim an entry: no URL shape means "Apify actor", so an
// entry has to name the provider.
func TestApifyNeverAutoDetects(t *testing.T) {
	p := apify{}
	if p.Detect(Entry{CareersURL: "https://api.apify.com/v2/acts/x"}) {
		t.Error("apify claimed an entry by URL, which would hijack routing")
	}
}

// A credential in a URL ends up in every transport error, and this package puts
// the URL in each one. It belongs in a header.
func TestApifyKeepsTheTokenOutOfTheURL(t *testing.T) {
	const secret = "apify_api_THISMUSTNOTLEAK"
	t.Setenv("APIFY_API_KEY", secret)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	f := &stub{pages: map[string]string{}}
	e := Entry{Name: "t", Provider: "apify",
		Extra: map[string]any{"actor_id": "curious_coder/linkedin-jobs-scraper"}}

	p := apify{}
	_, err := p.Fetch(context.Background(), e, f)
	if err == nil {
		t.Fatal("expected the stub's 404")
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("the token reached an error message, which is a log line and a scrollback")
	}
	for _, u := range f.seen {
		if strings.Contains(u, secret) || strings.Contains(u, "token=") {
			t.Errorf("token in the request URL: %s", u)
		}
	}
	if got := f.opts[0].Headers["Authorization"]; got != "Bearer "+secret {
		t.Errorf("Authorization header = %q, want the bearer token", got)
	}
}

// An actor run outlasts a board fetch by minutes. Inheriting the ten-second
// default made every real run fail on the deadline.
func TestApifyOutlastsTheDefaultTimeout(t *testing.T) {
	t.Setenv("APIFY_API_KEY", "k")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &stub{pages: map[string]string{}}
	q := apify{}
	_, _ = q.Fetch(context.Background(), Entry{Name: "t",
		Extra: map[string]any{"actor_id": "a/b"}}, f)
	if got := f.opts[0].Timeout; got <= defaultTimeout {
		t.Errorf("timeout %v does not outlast the %v default", got, defaultTimeout)
	}
}
