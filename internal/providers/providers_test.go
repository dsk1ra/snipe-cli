package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// stub is the injected fetcher the whole provider design exists for: every
// parse and normalisation path is reachable with canned payloads and no server.
type stub struct {
	pages map[string]string
	seen  []string
	opts  []Options
}

func (s *stub) record(url string, o Options) (string, error) {
	s.seen = append(s.seen, url)
	s.opts = append(s.opts, o)
	body, ok := s.pages[url]
	if !ok {
		return "", &StatusError{Status: 404, URL: url}
	}
	return body, nil
}

func (s *stub) JSON(_ context.Context, url string, o Options, out any) error {
	body, err := s.record(url, o)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), out)
}

func (s *stub) Text(_ context.Context, url string, o Options) (string, error) {
	return s.record(url, o)
}

func fetch(t *testing.T, p Provider, e Entry, pages map[string]string) ([]Job, *stub) {
	t.Helper()
	s := &stub{pages: pages}
	jobs, err := p.Fetch(context.Background(), e, s)
	if err != nil {
		t.Fatalf("%s: %v", p.ID(), err)
	}
	return jobs, s
}

func TestGreenhouse(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://job-boards.greenhouse.io/acme/jobs/1"}
	if !(greenhouse{}).Detect(e) {
		t.Fatal("did not claim a greenhouse board URL")
	}
	jobs, s := fetch(t, greenhouse{}, e, map[string]string{
		"https://boards-api.greenhouse.io/v1/boards/acme/jobs": `{"jobs":[
			{"title":"Backend Engineer","absolute_url":"https://job-boards.greenhouse.io/acme/jobs/1",
			 "location":{"name":"London"},"first_published":"2026-08-01T10:00:00Z"},
			{"title":"No URL","absolute_url":"","location":{"name":"Remote"}}]}`,
	})
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1 — a posting with no URL cannot be deduped or opened", len(jobs))
	}
	if jobs[0].Title != "Backend Engineer" || jobs[0].Location != "London" || jobs[0].Company != "Acme" {
		t.Errorf("bad mapping: %+v", jobs[0])
	}
	if jobs[0].PostedAt == 0 {
		t.Error("first_published did not parse — the age filter is fail-closed, so this drops the posting")
	}
	if !s.opts[0].NoRedirect {
		t.Error("redirects were not refused: the hostname check only guards the URL we send")
	}
}

func TestGreenhouseRejectsAnUntrustedAPIHost(t *testing.T) {
	e := Entry{Name: "Evil", API: "https://attacker.test/v1/boards/x/jobs"}
	if (greenhouse{}).Detect(e) {
		t.Error("claimed an entry pointing off-domain")
	}
	if _, err := (greenhouse{}).Fetch(context.Background(), e, &stub{}); err == nil {
		t.Error("fetched an untrusted hostname")
	}
}

func TestGreenhouseRejectsPlainHTTP(t *testing.T) {
	e := Entry{Name: "Acme", API: "http://boards-api.greenhouse.io/v1/boards/acme/jobs"}
	if _, err := (greenhouse{}).Fetch(context.Background(), e, &stub{}); err == nil {
		t.Error("accepted plain HTTP — a misconfigured entry should fail, not silently downgrade")
	}
}

func TestLever(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://jobs.lever.co/acme"}
	if !(lever{}).Detect(e) {
		t.Fatal("did not claim a lever URL")
	}
	jobs, _ := fetch(t, lever{}, e, map[string]string{
		"https://api.lever.co/v0/postings/acme?mode=json": `[
			{"text":"Platform Engineer","hostedUrl":"https://jobs.lever.co/acme/abc",
			 "createdAt":1754006400000,"categories":{"location":"Remote (UK)"}}]`,
	})
	if len(jobs) != 1 || jobs[0].Title != "Platform Engineer" || jobs[0].Location != "Remote (UK)" {
		t.Fatalf("bad mapping: %+v", jobs)
	}
	if jobs[0].PostedAt != 1754006400000 {
		t.Errorf("PostedAt %d — lever gives epoch ms directly", jobs[0].PostedAt)
	}
}

func TestAshbyRetriesThenSucceeds(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://jobs.ashbyhq.com/acme"}
	api := "https://api.ashbyhq.com/posting-api/job-board/acme?includeCompensation=true"
	// Fails twice, then answers — the shape Ashby's rate limiter produces.
	fails := 2
	f := &flaky{n: &fails, ok: `{"jobs":[{"title":"SRE","jobUrl":"https://jobs.ashbyhq.com/acme/x",
		"location":"London","publishedAt":"2026-08-02T09:00:00Z"}]}`, url: api}
	jobs, err := (ashby{}).Fetch(context.Background(), e, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Title != "SRE" {
		t.Fatalf("bad mapping: %+v", jobs)
	}
	if f.calls != 3 {
		t.Errorf("%d attempts, want 3 (two failures plus the success)", f.calls)
	}
}

func TestAshbyGivesUpAfterItsRetries(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://jobs.ashbyhq.com/acme"}
	fails := 99
	f := &flaky{n: &fails, url: "x"}
	if _, err := (ashby{}).Fetch(context.Background(), e, f); err == nil {
		t.Error("succeeded despite every attempt failing")
	}
	if f.calls != ashbyRetries+1 {
		t.Errorf("%d attempts, want %d", f.calls, ashbyRetries+1)
	}
}

type flaky struct {
	n     *int
	ok    string
	url   string
	calls int
}

func (f *flaky) JSON(_ context.Context, url string, _ Options, out any) error {
	f.calls++
	if *f.n > 0 {
		*f.n--
		return errors.New("429 slow down")
	}
	return json.Unmarshal([]byte(f.ok), out)
}
func (f *flaky) Text(context.Context, string, Options) (string, error) { return "", nil }

func TestRecruiteeDropsOffDomainLinks(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://acme.recruitee.com/"}
	if !(recruitee{}).Detect(e) {
		t.Fatal("did not claim a recruitee subdomain")
	}
	jobs, _ := fetch(t, recruitee{}, e, map[string]string{
		"https://acme.recruitee.com/api/offers/": `{"offers":[
			{"title":"Good","careers_url":"https://acme.recruitee.com/o/good","city":"Leeds","country":"UK"},
			{"title":"Elsewhere","careers_url":"https://attacker.test/o/bad","city":"Leeds"},
			{"title":"Remote","url":"https://acme.recruitee.com/o/r","city":"Leeds","country":"UK","remote":true},
			{"title":"Explicit","careers_url":"https://acme.recruitee.com/o/e","location":"Anywhere","city":"Ignored"}]}`,
	})
	if len(jobs) != 4 {
		t.Fatalf("got %d rows, want 4", len(jobs))
	}
	// The URL is the dedup key and everything downstream trusts it, so an
	// off-domain link is emptied rather than carried.
	if jobs[1].URL != "" {
		t.Errorf("kept an off-domain link: %q", jobs[1].URL)
	}
	if jobs[0].Location != "Leeds, UK" {
		t.Errorf("location %q, want \"Leeds, UK\"", jobs[0].Location)
	}
	if jobs[2].Location != "Leeds, UK, Remote" {
		t.Errorf("remote location %q", jobs[2].Location)
	}
	if jobs[3].Location != "Anywhere" {
		t.Errorf("an explicit location should win: %q", jobs[3].Location)
	}
}

func TestWorkableMarkdown(t *testing.T) {
	feed := `# Jobs

| Title | Department | Location | Type | Salary | Posted | Details |
|---|---|---|---|---|---|---|
| Backend Engineer | Eng | London | Full-time | — | 2026-08-01 | [View](https://apply.workable.com/acme/jobs/view/1.md) |
| Off Domain | Eng | London | Full-time | — | 2026-08-01 | [View](https://attacker.test/x.md) |
| No Link | Eng | Remote | Full-time | — | 2026-08-01 | — |
`
	jobs := ParseWorkableMarkdown(feed, "Acme")
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1 — off-domain and link-less rows are skipped, not emitted", len(jobs))
	}
	if jobs[0].Title != "Backend Engineer" || jobs[0].Location != "London" || jobs[0].Company != "Acme" {
		t.Errorf("bad mapping: %+v", jobs[0])
	}
}

func TestSmartRecruitersPaginates(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://careers.smartrecruiters.com/Acme"}
	if !(smartrecruiters{}).Detect(e) {
		t.Fatal("did not claim a smartrecruiters URL")
	}
	page := func(n int, ids ...string) string {
		var rows []string
		for _, id := range ids {
			rows = append(rows, fmt.Sprintf(`{"id":%q,"name":"Role %s","ref":"https://jobs.smartrecruiters.com/Acme/%s","location":{"city":"London","country":"UK"}}`, id, id, id))
		}
		return fmt.Sprintf(`{"content":[%s]}`, join(rows))
	}
	full := make([]string, srPageSize)
	for i := range full {
		full[i] = fmt.Sprint(i)
	}
	pages := map[string]string{
		fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/Acme/postings?limit=%d&offset=0", srPageSize):              page(0, full...),
		fmt.Sprintf("https://api.smartrecruiters.com/v1/companies/Acme/postings?limit=%d&offset=%d", srPageSize, srPageSize): page(1, "x"),
	}
	jobs, s := fetch(t, smartrecruiters{}, e, pages)
	if len(jobs) != srPageSize+1 {
		t.Fatalf("got %d jobs, want %d — a short page ends the walk", len(jobs), srPageSize+1)
	}
	if len(s.seen) != 2 {
		t.Errorf("made %d requests, want 2", len(s.seen))
	}
}

func join(rows []string) string {
	out := ""
	for i, r := range rows {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}

func TestResolvePrefersAnExplicitProvider(t *testing.T) {
	// An explicit provider: bypasses detection, which is the escape hatch for a
	// board whose URL shape another provider would claim.
	e := Entry{Name: "Acme", Provider: "lever", CareersURL: "https://job-boards.greenhouse.io/acme"}
	p, err := Resolve(e)
	if err != nil || p.ID() != "lever" {
		t.Fatalf("got %v, %v; want lever", p, err)
	}
	if _, err := Resolve(Entry{Name: "Acme", Provider: "nope"}); err == nil {
		t.Error("resolved an unknown provider id")
	}
	if _, err := Resolve(Entry{Name: "Acme", CareersURL: "https://example.test/jobs"}); err == nil {
		t.Error("resolved an entry no provider claims")
	}
}

func TestAllIsOrdered(t *testing.T) {
	// Detection priority must not depend on map iteration order, or two machines
	// route the same entry to different providers.
	first := All()
	for i := 0; i < 5; i++ {
		got := All()
		for j := range got {
			if got[j].ID() != first[j].ID() {
				t.Fatalf("provider order is not stable: %s vs %s", got[j].ID(), first[j].ID())
			}
		}
	}
}

func TestEntryEnabledDefaultsTrue(t *testing.T) {
	off := false
	if !(Entry{}).On() {
		t.Error("an entry with no `enabled:` key should be on")
	}
	if (Entry{Enabled: &off}).On() {
		t.Error("enabled: false should be off")
	}
}

// Detection runs in id order, so "ashby" is asked before "greenhouse". It used
// to say yes to any entry carrying an `api:` at all, which handed it all 48
// tracked companies whose api: is a Greenhouse board — each then failing on a
// payload it could not parse.
func TestAshbyDoesNotClaimAnotherATSsAPI(t *testing.T) {
	gh := Entry{
		Name:       "Anthropic",
		CareersURL: "https://job-boards.greenhouse.io/anthropic",
		API:        "https://boards-api.greenhouse.io/v1/boards/anthropic/jobs",
	}
	if (ashby{}).Detect(gh) {
		t.Error("ashby claims a greenhouse api: URL")
	}
	p, err := Resolve(gh)
	if err != nil || p.ID() != "greenhouse" {
		t.Fatalf("Resolve picked %v (%v), want greenhouse", p, err)
	}
	// The derivation from careers_url still works, and so does an api: that
	// really is Ashby's.
	if !(ashby{}).Detect(Entry{Name: "X", CareersURL: "https://jobs.ashbyhq.com/elevenlabs"}) {
		t.Error("ashby no longer detects its own board URL")
	}
	if !(ashby{}).Detect(Entry{Name: "X", API: "https://api.ashbyhq.com/posting-api/job-board/x"}) {
		t.Error("ashby refuses its own api: host")
	}
}
