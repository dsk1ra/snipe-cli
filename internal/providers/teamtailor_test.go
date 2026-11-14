package providers

import (
	"fmt"
	"strings"
	"testing"
)

func ttFeed(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:tt="https://teamtailor.com/locations"><channel><title>Acme</title><link>https://careers.acme.co.uk/jobs</link>` +
		strings.Join(items, "") + `</channel></rss>`
}

func ttItemXML(title, link, remote string, locs ...[2]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<item><title>%s</title><description>&lt;p&gt;x&lt;/p&gt;</description><pubDate>Thu, 22 Oct 2026 12:20:11 +0100</pubDate><link>%s</link><remoteStatus>%s</remoteStatus><tt:locations>`, title, link, remote)
	for _, l := range locs {
		fmt.Fprintf(&b, `<tt:location><tt:name>%s office</tt:name><tt:city>%s</tt:city><tt:country>%s</tt:country></tt:location>`, l[0], l[0], l[1])
	}
	b.WriteString(`</tt:locations></item>`)
	return b.String()
}

func TestTeamtailorDetect(t *testing.T) {
	for _, tc := range []struct {
		e    Entry
		want bool
	}{
		{Entry{CareersURL: "https://acme.teamtailor.com/jobs"}, true},
		{Entry{CareersURL: "https://careers.acme.co.uk/", Extra: map[string]any{"ats": "teamtailor"}}, true},
		{Entry{CareersURL: "https://careers.acme.co.uk/"}, false},
		{Entry{CareersURL: "http://acme.teamtailor.com/jobs"}, false},
	} {
		if got := (teamtailor{}).Detect(tc.e); got != tc.want {
			t.Errorf("Detect(%s, %v) = %v", tc.e.CareersURL, tc.e.Extra, got)
		}
	}
}

func TestTeamtailorPagesAndFiltersByCountry(t *testing.T) {
	var full []string
	for i := 0; i < ttPageSize; i++ {
		full = append(full, ttItemXML(fmt.Sprintf("Role %d", i), fmt.Sprintf("https://careers.acme.co.uk/jobs/%d", i), "none", [2]string{"Leeds", "United Kingdom"}))
	}
	e := Entry{Name: "Acme", CareersURL: "https://careers.acme.co.uk/", Extra: map[string]any{"ats": "teamtailor", "country": "United Kingdom"}}
	jobs, s := fetch(t, teamtailor{}, e, map[string]string{
		"https://careers.acme.co.uk/jobs.rss": ttFeed(full...),
		"https://careers.acme.co.uk/jobs.rss?offset=100": ttFeed(
			ttItemXML("Go Engineer", "https://jobs.acme.com/jobs/1-go", "fully", [2]string{"London", "United Kingdom"}, [2]string{"Amsterdam", "Netherlands"}),
			ttItemXML("Dutch only", "https://jobs.acme.com/jobs/2", "none", [2]string{"Amsterdam", "Netherlands"}),
			ttItemXML("Nowhere said", "https://jobs.acme.com/jobs/3", "none"),
			ttItemXML("Bad link", "http://jobs.acme.com/jobs/4", "none", [2]string{"Leeds", "United Kingdom"}),
		),
	})
	if len(s.seen) != 2 || !s.opts[0].NoRedirect {
		t.Fatalf("want two pages without redirects, got %v %+v", s.seen, s.opts)
	}
	if len(jobs) != ttPageSize+2 {
		t.Fatalf("want %d jobs, got %d", ttPageSize+2, len(jobs))
	}
	g := jobs[ttPageSize]
	if g.Title != "Go Engineer" || g.Location != "London, United Kingdom (Remote)" || g.Company != "Acme" || g.URL != "https://jobs.acme.com/jobs/1-go" {
		t.Errorf("remote UK posting: %+v", g)
	}
	if g.PostedAt != 1792668011000 {
		t.Errorf("PostedAt %d", g.PostedAt)
	}
	if n := jobs[ttPageSize+1]; n.Title != "Nowhere said" || n.Location != "" {
		t.Errorf("a posting with no location is kept unplaced: %+v", n)
	}
	if jobs[0].Location != "Leeds, United Kingdom" {
		t.Errorf("location %q", jobs[0].Location)
	}
}
