package providers

import (
	"testing"
	"time"
)

func TestSuccessFactorsDetect(t *testing.T) {
	sf := map[string]any{"ats": "successfactors"}
	for _, tc := range []struct {
		e    Entry
		want bool
	}{
		{Entry{CareersURL: "https://career2.successfactors.eu/career?company=CAA"}, true},
		{Entry{CareersURL: "https://career55.sapsf.eu/career?career_company=universi38&company=universi38"}, true},
		{Entry{CareersURL: "https://career2.successfactors.eu/career?lang=en_GB"}, false},
		{Entry{CareersURL: "https://careers.coloplast.com/", Extra: sf}, true},
		{Entry{CareersURL: "https://careers.coloplast.com/"}, false},
		{Entry{CareersURL: "http://careers.coloplast.com/", Extra: sf}, false},
	} {
		if got := (successfactors{}).Detect(tc.e); got != tc.want {
			t.Errorf("Detect(%s, %v) = %v", tc.e.CareersURL, tc.e.Extra, got)
		}
	}
}

// The shape British Sugar's and CAA's portals answer with, nameless field and
// bare "<" in a description included.
const sfClassicXML = `<?xml version="1.0" encoding="UTF-8"?>
<Job-Listing>
<Job>
<JobTitle><![CDATA[Software Engineer]]></JobTitle>
<Job-Description><![CDATA[<p>Experience: < 5 years & counting<br></p>]]></Job-Description>
<ReqId>5622</ReqId>
<>266</>
<Posted-Date>22/09/2026</Posted-Date>
<filter1><label>Location</label><value>London</value></filter1>
<filter2><label>Country</label><value>GB</value></filter2>
<filter3><label>Business Area</label><value>Safety</value></filter3>
</Job>
<Job>
<JobTitle><![CDATA[Ingenieur]]></JobTitle>
<ReqId>5623</ReqId>
<Posted-Date>08/10/2026</Posted-Date>
<filter1><label>City</label><value>Munich</value></filter1>
<filter2><label>Country</label><value>Germany</value></filter2>
</Job>
<Job>
<JobTitle><![CDATA[Bid Writer]]></JobTitle>
<ReqId>5624</ReqId>
</Job>
</Job-Listing>`

func TestSuccessFactorsClassic(t *testing.T) {
	e := Entry{Name: "CAA", CareersURL: "https://career2.successfactors.eu/career?company=CAA", Extra: map[string]any{"country": "United Kingdom"}}
	jobs, s := fetch(t, successfactors{}, e, map[string]string{
		"https://career2.successfactors.eu/career?company=CAA&career_ns=job_listing_summary&resultType=XML": sfClassicXML,
	})
	if o := s.opts[0]; !o.NoRedirect || o.MaxBytes != sfMaxBytes || o.Headers["Accept"] == "" {
		t.Errorf("request options %+v", o)
	}
	if len(jobs) != 2 {
		t.Fatalf("want the UK posting and the unlocated one, got %+v", jobs)
	}
	j := jobs[0]
	if j.Title != "Software Engineer" || j.Location != "London, United Kingdom" || j.Company != "CAA" ||
		j.URL != "https://career2.successfactors.eu/career?career_ns=job_listing&company=CAA&career_job_req_id=5622" {
		t.Errorf("posting %+v", j)
	}
	if want := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC).UnixMilli(); j.PostedAt != want {
		t.Errorf("22/09/2026 read as %v", time.UnixMilli(j.PostedAt).UTC())
	}
	if u := jobs[1]; u.Title != "Bid Writer" || u.Location != "" || u.PostedAt != 0 {
		t.Errorf("unlocated, undated posting %+v", u)
	}
}

func TestSuccessFactorsDateOrder(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		dates []string
		want  bool
	}{
		{[]string{"06/05/2026", "22/09/2026"}, true},
		{[]string{"06/05/2026", "06/30/2026"}, false},
		// 10 May or 5 October: a live board's newest posting is the recent one.
		{[]string{"05/10/2026"}, true},
		{[]string{"10/05/2026"}, false},
		// 12 October or 10 December: December has not happened yet.
		{[]string{"12/10/2026"}, true},
	} {
		if got := sfDayFirst(tc.dates, now); got != tc.want {
			t.Errorf("sfDayFirst(%v) = %v", tc.dates, got)
		}
	}
}

const sfRMKFeed = `<?xml version="1.0" encoding="UTF-8" ?><rss version='2.0' xmlns:atom='http://www.w3.org/2005/Atom'><channel><atom:link href='https://careers.acme.com/feeds//' rel='self' type='application/rss+xml' /><title>Acme - Custom Search </title><link>https://careers.acme.com/feeds//</link>
<item><title><![CDATA[Backend Engineer (m/w/d) (Warrington, GB, WA1 1AA)]]></title><description>&lt;p&gt;x&lt;/p&gt;</description><pubDate>Fri, 23 Oct 2026 2:00:00 GMT</pubDate><link>https://careers.acme.com/job/Warrington-Backend-Engineer/1440958233/?feedId=null&amp;utm_source=J2WRSS</link></item>
<item><title><![CDATA[Baker (FT) (Ottawa, ON, CA, K1V 9B4)]]></title><pubDate>Fri, 23 Oct 2026 2:00:00 GMT</pubDate><link>https://careers.acme.com/job/Ottawa-Baker/1/</link></item>
<item><title><![CDATA[Field Engineer (Field-Based, GB)]]></title><pubDate>Thu, 22 Oct 2026 14:30:00 GMT</pubDate><link>https://careers.acme.com/job/Field/2/</link></item>
</channel></rss>`

func TestSuccessFactorsRMK(t *testing.T) {
	e := Entry{Name: "Acme", CareersURL: "https://careers.acme.com/go/UK/123/", Extra: map[string]any{"ats": "successfactors", "country": "United Kingdom"}}
	jobs, _ := fetch(t, successfactors{}, e, map[string]string{
		"https://careers.acme.com/services/rss/job/?keywords=&rows=5000": sfRMKFeed,
	})
	if len(jobs) != 2 {
		t.Fatalf("want the two GB postings, got %+v", jobs)
	}
	j := jobs[0]
	if j.Title != "Backend Engineer (m/w/d)" || j.Location != "Warrington, United Kingdom, WA1 1AA" ||
		j.URL != "https://careers.acme.com/job/Warrington-Backend-Engineer/1440958233/" {
		t.Errorf("posting %+v", j)
	}
	if want := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC).UnixMilli(); j.PostedAt != want {
		t.Errorf("pubDate read as %v", time.UnixMilli(j.PostedAt).UTC())
	}
	if jobs[1].Location != "Field-Based, United Kingdom" {
		t.Errorf("location %q", jobs[1].Location)
	}
}
