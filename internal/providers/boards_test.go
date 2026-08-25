package providers

import "testing"

func TestRemoteOKSkipsTheNoticeRow(t *testing.T) {
	e := Entry{Name: "RemoteOK", CareersURL: "https://remoteok.com/api"}
	if !(remoteok{}).Detect(e) {
		t.Fatal("did not claim the remoteok feed")
	}
	jobs, s := fetch(t, remoteok{}, e, map[string]string{
		remoteOKAPI: `[
		  {"legal":"see remoteok.com/terms"},
		  {"position":"Backend Engineer","url":"https://remoteok.com/l/1","company":"Acme","location":"","epoch":1700000000},
		  {"position":"No Link"},
		  {"position":"SRE","apply_url":"https://remoteok.com/l/2","date":"2026-08-01T00:00:00Z"}
		]`,
	})
	if len(jobs) != 2 {
		t.Fatalf("kept %d jobs, want the two with links: %+v", len(jobs), jobs)
	}
	if jobs[0].Location != "Remote" {
		t.Errorf("blank location is %q, want the board's default", jobs[0].Location)
	}
	if jobs[0].PostedAt != 1700000000*1000 {
		t.Errorf("epoch seconds not scaled to ms: %d", jobs[0].PostedAt)
	}
	if jobs[1].URL != "https://remoteok.com/l/2" {
		t.Errorf("apply_url not used as the fallback link: %q", jobs[1].URL)
	}
	if jobs[1].PostedAt == 0 {
		t.Error("ISO date not parsed — the age filter is fail-closed, so this drops the row")
	}
	// The feed URL is fixed, never taken from the entry.
	if len(s.seen) != 1 || s.seen[0] != remoteOKAPI || !s.opts[0].NoRedirect {
		t.Errorf("fetched %v with %+v, want the fixed API and no redirects", s.seen, s.opts)
	}
}

// pubDate and locationRestrictions each arrive in two shapes on this feed.
func TestHimalayasAcceptsBothFieldShapes(t *testing.T) {
	e := Entry{Name: "Himalayas", CareersURL: "https://himalayas.app/jobs/api"}
	if !(himalayas{}).Detect(e) {
		t.Fatal("did not claim the himalayas feed")
	}
	jobs, _ := fetch(t, himalayas{}, e, map[string]string{
		himalayasAPI: `{"jobs":[
		  {"title":"Go Engineer","applicationLink":"https://x/1","companyName":"Acme",
		   "locationRestrictions":["UK","Ireland"],"pubDate":1700000000},
		  {"title":"Rust Engineer","guid":"https://x/2",
		   "locationRestrictions":"Worldwide","pubDate":"2026-08-01T00:00:00Z"},
		  {"title":"No Link"}
		]}`,
	})
	if len(jobs) != 2 {
		t.Fatalf("kept %d jobs, want 2: %+v", len(jobs), jobs)
	}
	if jobs[0].Location != "UK, Ireland" || jobs[0].PostedAt != 1700000000*1000 {
		t.Errorf("list/epoch shape mishandled: %+v", jobs[0])
	}
	if jobs[1].Location != "Worldwide" || jobs[1].PostedAt == 0 {
		t.Errorf("string/ISO shape mishandled: %+v", jobs[1])
	}
	if jobs[1].URL != "https://x/2" {
		t.Errorf("guid not used as the fallback link: %q", jobs[1].URL)
	}
}

func TestHNHiringParsesThePipeConvention(t *testing.T) {
	e := Entry{Name: "HN", CareersURL: "https://hn.algolia.com/whoishiring"}
	if !(hnHiring{}).Detect(e) {
		t.Fatal("did not claim the HN sentinel URL")
	}
	jobs, _ := fetch(t, hnHiring{}, e, map[string]string{
		hnSearchURL: `{"hits":[
		  {"title":"Ask HN: Who wants to be hired?","objectID":"1"},
		  {"title":"Ask HN: Who is hiring? (August 2026)","objectID":"42"}
		]}`,
		hnItemURL + "42": `{"children":[
		  {"text":"<p>Acme | Backend Engineer | London | REMOTE | <a href=\"https://acme.com/jobs\">apply</a></p>","author":"alice","created_at_i":1700000000},
		  {"text":"<p>No link here at all</p>","author":"bob","created_at_i":1700000001},
		  {"text":"<p>Hi HN, we are hiring | Platform Engineer | https://x.com/j</p>","author":"carol","created_at_i":1700000002}
		]}`,
	})
	if len(jobs) != 2 {
		t.Fatalf("kept %d posts, want the two carrying a URL: %+v", len(jobs), jobs)
	}
	if jobs[0].Company != "Acme" || jobs[0].Title != "Backend Engineer" {
		t.Errorf("first line not split on the pipe convention: %+v", jobs[0])
	}
	if jobs[0].URL != "https://acme.com/jobs" {
		t.Errorf("anchor href not kept through the strip: %q", jobs[0].URL)
	}
	if jobs[0].Location != "Remote" {
		t.Errorf("REMOTE not detected: %q", jobs[0].Location)
	}
	// Prose in the first segment is not a company name; fall back to the poster.
	if jobs[1].Company != "carol" {
		t.Errorf("company is %q, want the author when the segment reads as prose", jobs[1].Company)
	}
}

func TestHNHiringIgnoresTheWantsToBeHiredThread(t *testing.T) {
	e := Entry{Name: "HN", CareersURL: "https://hn.algolia.com/whoishiring"}
	jobs, _ := fetch(t, hnHiring{}, e, map[string]string{
		hnSearchURL: `{"hits":[{"title":"Ask HN: Who wants to be hired? (August 2026)","objectID":"1"}]}`,
	})
	if len(jobs) != 0 {
		t.Errorf("got %d jobs from the wrong thread", len(jobs))
	}
}

// A board provider must refuse an entry pointed at another host, or portals.yml
// becomes a way to aim the scanner anywhere.
func TestBoardsRefuseForeignHosts(t *testing.T) {
	for _, p := range []Provider{remoteok{}, himalayas{}, hnHiring{}} {
		if p.Detect(Entry{CareersURL: "https://evil.example.com/api"}) {
			t.Errorf("%s claimed a foreign host", p.ID())
		}
		if p.Detect(Entry{CareersURL: "http://remoteok.com/api"}) {
			t.Errorf("%s claimed a plaintext URL", p.ID())
		}
	}
}
