package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/providers"
)

// stub serves canned pages; redirect maps a URL to the one it lands on.
type stub struct {
	pages    map[string]string
	redirect map[string]string
	seen     []string
}

func (s *stub) Get(_ context.Context, u string) (string, string, error) {
	s.seen = append(s.seen, u)
	if to, ok := s.redirect[u]; ok {
		u = to
	}
	body, ok := s.pages[u]
	if !ok {
		return "", "", &providers.StatusError{Status: 404, URL: u}
	}
	return u, body, nil
}

const gpgHead = `"EmployerName","EmployerId","Address","PostCode","CompanyNumber","SicCodes","CompanyLinkToGPGInfo","EmployerSize","CurrentName"` + "\n"

func TestDownloadMerges(t *testing.T) {
	year := func(y string) string { return "https://gender-pay-gap.service.gov.uk/viewing/download-data/" + y }
	s := &stub{pages: map[string]string{
		// 2026 is absent: a year that has not opened yet is skipped, not fatal.
		year("2025"): gpgHead +
			`"ACME LIMITED","1","x","B1 1AA","0001","62012,` + "\n" + `62020","","1000 to 4999","ACME GROUP LIMITED"` + "\n" +
			`"BETA PLC","2","x","EC1A 1BB","0002","64191","https://assets.ctfassets.net/gpg.pdf","500 to 999",""` + "\n",
		year("2024"): gpgHead +
			`"ACME LIMITED","1","x","B1 1AA","0001","62012","https://www.Acme.co.uk/gpg-2024.pdf","1000 to 4999",""` + "\n" +
			`"GAMMA LTD","3","x","CV1 1CC","0003","29100","https://gamma.com","250 to 499",""` + "\n",
		year("2023"): gpgHead +
			`"BETA PLC","2","x","EC1A 1BB","0002","64191","beta.com/gpg","500 to 999",""` + "\n" +
			`"DELTA LTD","4","x","WV1 1DD","0004","62012","https://delta.com","250 to 499",""` + "\n",
		year("2022"): gpgHead,
	}}
	es, err := Download(context.Background(), s, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Employer{}
	for _, e := range es {
		got[e.ID] = e
	}
	if len(got) != 3 || got["4"].ID != "" {
		t.Fatalf("want the employers of the newest two years only, got %+v", es)
	}
	if a := got["1"]; a.Name != "ACME GROUP LIMITED" || a.SIC != "62012,62020" || a.Site != "www.acme.co.uk" {
		t.Errorf("acme: current name, flattened SIC and last year's link expected, got %+v", a)
	}
	if b := got["2"]; b.Site != "beta.com" {
		t.Errorf("beta: a file-host link should fall back to an older year's site, got %q", b.Site)
	}
}

func TestSite(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.Acme.com/files/gpg.pdf":           "www.acme.com",
		"acme.co.uk/gpg":                               "acme.co.uk",
		`https:\\thompsonandcapper.com`:                "thompsonandcapper.com",
		"https://acme.sharepoint.com/sites/x":          "",
		"https://www.gov.uk/government/publications/x": "",
		"https://localhost/x":                          "",
		"":                                             "",
	} {
		if got := Site(in); got != want {
			t.Errorf("Site(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParent(t *testing.T) {
	for in, want := range map[string]string{
		"careers.firstgroup.co.uk": "firstgroup.co.uk",
		"static-wbd-cdn.wbd.com":   "wbd.com",
		"acme.co.uk":               "",
		"acme.uk.com":              "",
		"www.acme.com":             "",
		"wbd.com":                  "",
	} {
		if got := parent(in); got != want {
			t.Errorf("parent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct{ name, page, body, ats, board string }{
		{"workday in inline JSON, with a locale", "https://acme.com/", `{"url":"https:\/\/acme.wd3.myworkdayjobs.com\/en-GB\/External\/job\/1"}`,
			"workday", "https://acme.wd3.myworkdayjobs.com/External"},
		{"workday cxs path names the site", "https://acme.com/", `fetch("https://acme.wd103.myworkdayjobs.com/wday/cxs/acme/Careers/jobs")`,
			"workday", "https://acme.wd103.myworkdayjobs.com/Careers"},
		{"the page's own URL counts", "https://acme.wd5.myworkdayjobs.com/Acme_Careers", `<div id="root"></div>`,
			"workday", "https://acme.wd5.myworkdayjobs.com/Acme_Careers"},
		{"successfactors through an escaped ampersand", "https://acme.com/", `<a href="https://career5.successfactors.eu/career?career_ns=job_listing&amp;company=AcmeP">`,
			"successfactors", "https://career5.successfactors.eu/career?company=AcmeP"},
		{"greenhouse embed", "https://acme.com/", `<script src="https://boards.greenhouse.io/embed/job_board/js?for=acme"></script>`,
			"greenhouse", "https://job-boards.greenhouse.io/acme"},
		{"most-linked board wins", "https://group.com/", `jobs.lever.co/subsidiary https://acme.teamtailor.com https://acme.teamtailor.com/jobs/1`,
			"teamtailor", "https://acme.teamtailor.com/jobs"},
		{"a vendor's own site is not a board", "https://acme.com/", `<a href="https://career.teamtailor.com">Powered by Teamtailor</a> apply.workable.com/api/v1`,
			"", ""},
		{"a fingerprint makes the page the board", "https://careers.acme.com/gb/en", `<script src="https://cdn.phenompeople.com/CareerConnectResources/x.js">`,
			"phenom", "https://careers.acme.com/gb/en"},
		{"a board beats a fingerprint", "https://careers.acme.com/", `cdn.phenompeople.com acme.wd1.myworkdayjobs.com/Ext`,
			"workday", "https://acme.wd1.myworkdayjobs.com/Ext"},
		{"dayforce, old portal path", "https://acme.com/", `<a href="https://globaleur242.dayforcehcm.com/CandidatePortal/en-GB/tgguk/">`,
			"dayforce", "https://jobs.dayforcehcm.com/en-GB/tgguk/CANDIDATEPORTAL"},
		{"an HR suite's staff login is not a careers site", "https://acme.com/", `<a href="https://acme.peoplehr.net/Pages/Login.aspx">Staff</a>`, "", ""},
		{"oleeo", "https://acme.com/", `<a href="https://acmegrads.tal.net/vx/candidate">`, "oleeo", "https://acmegrads.tal.net"},
		{"nothing", "https://acme.com/", `<a href="/about">About</a>`, "", ""},
	}
	for _, c := range cases {
		if ats, board := Detect(c.body, c.page); ats != c.ats || board != c.board {
			t.Errorf("%s: got %q %q, want %q %q", c.name, ats, board, c.ats, c.board)
		}
	}
}

func TestResolveFollowsCareersLinks(t *testing.T) {
	s := &stub{
		pages: map[string]string{
			"https://www.acme.co.uk/": `<a href="/privacy">Privacy</a><a href="https://www.linkedin.com/company/acme/jobs">LinkedIn</a>
				<a class="nav" href="/working-here"><span>Work with us</span></a>`,
			"https://www.acme.co.uk/working-here":           `<a href="/about">About</a><a href="https://jobs.acme.co.uk/search">Search all jobs</a>`,
			"https://acme.wd3.myworkdayjobs.com/en-GB/Acme": `<div id="root"></div>`,
		},
		redirect: map[string]string{"https://jobs.acme.co.uk/search": "https://acme.wd3.myworkdayjobs.com/en-GB/Acme"},
	}
	ats, board := Resolver{G: s}.Resolve(context.Background(), "www.acme.co.uk")
	if ats != "workday" || board != "https://acme.wd3.myworkdayjobs.com/Acme" {
		t.Fatalf("got %q %q", ats, board)
	}
	for _, u := range s.seen {
		if strings.Contains(u, "linkedin") || strings.Contains(u, "privacy") {
			t.Errorf("followed %s, which is not a careers link", u)
		}
	}
}

func TestResolveOutcomes(t *testing.T) {
	s := &stub{pages: map[string]string{
		// No careers link on the subdomain, so the main site is tried.
		"https://investors.beta.com/": `<a href="/results">Results</a>`,
		"https://beta.com/":           `<a href="https://apply.workable.com/beta/">Careers</a>`,
		"https://gamma.com/":          `<p>rendered by script</p>`,
		"https://delta.com/":          `<a href="/about">About</a><a href="/careers/">Work for us</a>`,
		"https://delta.com/careers/":  `<a href="/careers/vacancies/123">Engineer</a>`,
	}}
	r := Resolver{G: s}
	for site, want := range map[string][2]string{
		"investors.beta.com": {"workable", "https://apply.workable.com/beta"},
		"gamma.com":          {NoATS, ""},
		"delta.com":          {NoATS, "https://delta.com/careers/"}, // an ATS without a shape: the careers page is kept
		"gone.com":           {Unreachable, ""},
	} {
		if ats, board := r.Resolve(context.Background(), site); ats != want[0] || board != want[1] {
			t.Errorf("%s: got %q %q, want %q %q", site, ats, board, want[0], want[1])
		}
	}
	// The script-rendered home page fell back to the two common paths.
	if !strings.Contains(strings.Join(s.seen, " "), "https://gamma.com/careers") {
		t.Errorf("no fallback to /careers: %v", s.seen)
	}
}

func TestCarry(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	prev := []Employer{
		{Site: "fresh.com", ATS: "workday", Board: "b", Checked: "2026-10-01"},
		{Site: "stale.com", ATS: "workday", Board: "b", Checked: "2026-08-01"},
		{Site: "down.com", ATS: Unreachable, Checked: "2026-10-18"},
	}
	fresh := []Employer{{ID: "1", Site: "fresh.com"}, {ID: "2", Site: "stale.com"}, {ID: "3", Site: "down.com"},
		{ID: "4", Site: "stale.com"}, {ID: "5"}}
	todo := Carry(fresh, prev, now, 30)
	if strings.Join(todo, ",") != "stale.com,down.com" {
		t.Errorf("todo = %v", todo)
	}
	if fresh[0].ATS != "workday" || fresh[0].Checked != "2026-10-01" {
		t.Errorf("a fresh resolution was not carried: %+v", fresh[0])
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.tsv")
	in := []Employer{{ID: "1", Name: "ACME\tLTD", Site: "acme.com", ATS: "workday", Board: "https://x", Checked: "2026-10-19"}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil || len(out) != 1 || out[0].Name != "ACME LTD" || out[0].Board != "https://x" {
		t.Fatalf("round trip: %+v %v", out, err)
	}
}

func TestWebRefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	if _, _, err := NewWeb().Get(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("a loopback fetch should be refused, got %v", err)
	}
}

func TestEntries(t *testing.T) {
	es := []Employer{
		{Name: "LOOKERS LEASING LIMITED", Size: "250 to 499", Board: "https://job-boards.eu.greenhouse.io/lookers"},
		{Name: "LOOKERS MOTOR GROUP LIMITED", Size: "5000 to 19,999", Board: "https://job-boards.eu.greenhouse.io/lookers"},
		{Name: "BARCLAYS BANK UK PLC", Size: "20,000 or more", Board: "https://barclays.wd3.myworkdayjobs.com/External_Career_Site_Barclays"},
		{Name: "MOTORPOINT LIMITED", Board: "https://careers.motorpoint.co.uk/"}, // Teamtailor: nothing reads it yet
		{Name: "NO SITE LTD"},
	}
	got := Entries(es)
	if len(got) != 2 {
		t.Fatalf("want one entry per readable board, got %+v", got)
	}
	if got[0].Name != "Lookers Motor Group" || got[1].Name != "Barclays Bank UK" {
		t.Errorf("names %q, %q: a shared board takes the largest employer's", got[0].Name, got[1].Name)
	}
	if got[1].Str("country") != Country {
		t.Errorf("country %q", got[1].Str("country"))
	}
}

func TestDisplay(t *testing.T) {
	for in, want := range map[string]string{
		"BARCLAYS BANK UK PLC":         "Barclays Bank UK",
		"HSBC UK BANK PLC":             "HSBC UK Bank",
		"3M UNITED KINGDOM PUBLIC LTD": "3M United Kingdom Public",
		"JONES LANG LASALLE LIMITED":   "Jones Lang Lasalle",
		"Aqa":                          "Aqa",
		"ACME (UK) LIMITED":            "Acme",
		"BANK OF AMERICA EUROPE":       "Bank of America Europe",
	} {
		if got := Display(in); got != want {
			t.Errorf("Display(%q) = %q, want %q", in, got, want)
		}
	}
}
