package registry

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/providers"
)

// ── detection ────────────────────────────────────────────────────────────────

// shape is one way an ATS shows up in a page: the URL pattern, and the board
// URL it implies. The (?P<slug>…) group is the tenant; a junk slug is the
// vendor's own site, not a customer's. An empty board means the pattern is a
// fingerprint — the platform serves the careers site on the employer's own
// domain — and the page itself is the board.
type shape struct {
	ats   string
	re    *regexp.Regexp
	board string
}

func sh(ats, re, board string) shape { return shape{ats, regexp.MustCompile(`(?i)` + re), board} }

// shapes is in preference order: on a tie in how often a page links each board,
// the earlier wins. The enterprise systems the large UK employers run come
// first; in an 80-employer sample Workday and SuccessFactors were 13 of the 23
// identified.
var shapes = []shape{
	// The cxs alternative comes first so ".../wday/cxs/acme/External/jobs" names
	// the site, not "wday".
	sh("workday", `\b(?P<slug>[a-z0-9-]+)\.(wd\d+)\.myworkdayjobs\.com/(?:wday/cxs/[a-z0-9_-]+/|[a-z]{2}-[a-z]{2}/)?([a-z0-9_-]+)`,
		"https://${slug}.${2}.myworkdayjobs.com/${3}"),
	sh("workday", `\b(wd\d+)\.myworkdaysite\.com/(?:[a-z]{2}-[a-z]{2}/)?recruiting/(?P<slug>[a-z0-9_-]+)/([a-z0-9_-]+)`,
		"https://${1}.myworkdaysite.com/recruiting/${slug}/${3}"),
	sh("successfactors", `\b(career\d*\.successfactors\.(?:com|eu))/(?:career|portalcareer)\?[^"'\s<>]*?\bcompany=(?P<slug>[a-z0-9_-]+)`,
		"https://${1}/career?company=${slug}"),
	sh("oracle", `\b(?P<slug>[a-z0-9-]+\.fa(?:\.[a-z0-9-]+)*\.oraclecloud\.com)/hcmui/candidateexperience/[a-z-]+/sites/([a-z0-9_-]+)`,
		"https://${slug}/hcmUI/CandidateExperience/en/sites/${2}"),
	sh("taleo", `\b(?P<slug>[a-z0-9-]+)\.taleo\.net/careersection/([a-z0-9_-]+)`,
		"https://${slug}.taleo.net/careersection/${2}/jobsearch.ftl"),
	sh("icims", `\b(?P<slug>[a-z0-9-]+)\.icims\.com\b`, "https://${slug}.icims.com/jobs"),
	sh("avature", `\b(?P<slug>[a-z0-9-]+)\.avature\.net\b`, "https://${slug}.avature.net/careers"),
	sh("eightfold", `\b(?P<slug>[a-z0-9-]+)\.eightfold\.ai/careers`, "https://${slug}.eightfold.ai/careers"),
	sh("oleeo", `\b(?P<slug>[a-z0-9-]+)\.tal\.net\b`, "https://${slug}.tal.net"),
	sh("greenhouse", `\b(?:boards|job-boards)(\.eu)?\.greenhouse\.io/(?:embed/job_board(?:/js)?\?for=)?(?P<slug>[a-z0-9_-]+)`,
		"https://job-boards${1}.greenhouse.io/${slug}"),
	sh("greenhouse", `\bboards-api(\.eu)?\.greenhouse\.io/v1/boards/(?P<slug>[a-z0-9_-]+)`,
		"https://job-boards${1}.greenhouse.io/${slug}"),
	sh("lever", `\bjobs\.(eu\.)?lever\.co/(?P<slug>[a-z0-9_-]+)`, "https://jobs.${1}lever.co/${slug}"),
	sh("ashby", `\bjobs\.ashbyhq\.com/(?P<slug>[a-z0-9_.-]+)`, "https://jobs.ashbyhq.com/${slug}"),
	sh("smartrecruiters", `\b(?:jobs|careers)\.smartrecruiters\.com/(?P<slug>[a-z0-9_-]+)`,
		"https://jobs.smartrecruiters.com/${slug}"),
	sh("workable", `\bapply\.workable\.com/(?P<slug>[a-z0-9_-]+)`, "https://apply.workable.com/${slug}"),
	sh("teamtailor", `\b(?P<slug>[a-z0-9-]+)\.teamtailor\.com\b`, "https://${slug}.teamtailor.com/jobs"),
	sh("recruitee", `\b(?P<slug>[a-z0-9-]+)\.recruitee\.com\b`, "https://${slug}.recruitee.com"),
	sh("pinpoint", `\b(?P<slug>[a-z0-9-]+)\.pinpointhq\.com\b`, "https://${slug}.pinpointhq.com"),
	sh("personio", `\b(?P<slug>[a-z0-9-]+)\.jobs\.personio\.(de|com)\b`, "https://${slug}.jobs.personio.${2}"),
	sh("bamboohr", `\b(?P<slug>[a-z0-9-]+)\.bamboohr\.com/(?:careers|jobs)`, "https://${slug}.bamboohr.com/careers"),
	sh("hibob", `\b(?P<slug>[a-z0-9-]+)\.careers\.hibob\.com\b`, "https://${slug}.careers.hibob.com"),
	sh("jobvite", `\bjobs\.jobvite\.com/(?P<slug>[a-z0-9_-]+)`, "https://jobs.jobvite.com/${slug}"),
	sh("breezy", `\b(?P<slug>[a-z0-9-]+)\.breezy\.hr\b`, "https://${slug}.breezy.hr"),
	// The UK mid-market systems, found by counting the third-party hosts on the
	// careers pages of the sites the first trial build could not place. iTrent
	// and PeopleHR are HR suites first, and a staff-login link to one is not a
	// careers site, so their shapes require the recruitment path.
	sh("dayforce", `\bjobs\.dayforcehcm\.com/(?:[a-z]{2}-[a-z]{2}/)?(?P<slug>[a-z0-9_-]+)/candidateportal`,
		"https://jobs.dayforcehcm.com/en-GB/${slug}/CANDIDATEPORTAL"),
	sh("dayforce", `\b[a-z0-9-]+\.dayforcehcm\.com/candidateportal/(?:[a-z]{2}-[a-z]{2}/)?(?P<slug>[a-z0-9_-]+)`,
		"https://jobs.dayforcehcm.com/en-GB/${slug}/CANDIDATEPORTAL"),
	sh("itrent", `\b(?P<slug>[a-z0-9-]+)\.webitrent\.com/[a-z0-9_]+_webrecruitment`, "https://${slug}.webitrent.com/${slug}_webrecruitment"),
	sh("peoplehr", `\b(?P<slug>[a-z0-9-]+)\.peoplehr\.net/pages/jobboard`, "https://${slug}.peoplehr.net"),
	sh("kallidus", `\b(?P<slug>[a-z0-9-]+)\.kallidusrecruit\.com\b`, "https://${slug}.kallidusrecruit.com"),
	sh("talos360", `\b(?P<slug>[a-z0-9-]+)\.talosats-careers\.com\b`, "https://${slug}.talosats-careers.com"),
	sh("corehr", `\bmy\.corehr\.com/pls/(?P<slug>[a-z0-9_]+)/erq_`, "https://my.corehr.com/pls/${slug}/erq_search_package.search_form"),
	// The public sector's national boards. Every employer on one shares the
	// board, which the scan fetches once.
	sh("civilservice", `\b(?P<slug>civilservicejobs)\.service\.gov\.uk\b`, "https://www.civilservicejobs.service.gov.uk"),
	sh("nhsjobs", `\b(?P<slug>jobs)\.nhs\.uk\b`, "https://www.jobs.nhs.uk"),
	sh("trac", `\b(?P<slug>trac)\.jobs\b`, "https://www.trac.jobs"),

	sh("successfactors", `\brmkcdn\.successfactors\.com\b`, ""),
	sh("phenom", `\bcdn\.phenompeople\.com\b`, ""),
	sh("teamtailor", `\bteamtailor-cdn\.com\b`, ""),
	sh("radancy", `\b(?:radancy\.net|tmpwebeng\.com)\b`, ""),
	sh("eploy", `\beploy\.co\.uk\b`, ""),
	sh("firefish", `\bfirefishsoftware\.com\b`, ""),
	sh("hireful", `\bhireful\.(?:com|co\.uk)\b`, ""),
	sh("current-vacancies", `\bcurrent-vacancies\.com\b`, ""),
}

var junk = map[string]bool{
	"www": true, "app": true, "api": true, "embed": true, "cdn": true, "static": true,
	"assets": true, "scripts": true, "support": true, "help": true, "status": true,
	"career": true, "careers": true, "developer": true, "developers": true,
	"community": true, "j": true, "login": true, "signin": true, "blog": true,
	"message": true,
}

// Detect names the ATS a page points at and its board, or returns empty
// strings. page is the URL the page was served from, and counts as a mention:
// a careers link that redirects straight to the ATS lands on a page that may
// never repeat its own address. Where a page links several boards — a group
// site listing its subsidiaries — the one it links most often wins.
func Detect(body, page string) (ats, board string) {
	body = page + "\n" + strings.ReplaceAll(body, `\/`, "/") // URLs inside inline JSON
	best := 0
	for _, s := range shapes {
		if s.board == "" {
			continue
		}
		slug := s.re.SubexpIndex("slug")
		counts := map[string]int{}
		for _, m := range s.re.FindAllStringSubmatchIndex(body, -1) {
			if slug > 0 && m[2*slug] >= 0 && junk[strings.ToLower(body[m[2*slug]:m[2*slug+1]])] {
				continue
			}
			counts[string(s.re.ExpandString(nil, s.board, body, m))]++
		}
		for b, n := range counts {
			if n > best || n == best && s.ats == ats && b < board {
				ats, board, best = s.ats, b, n
			}
		}
	}
	if ats != "" {
		return ats, board
	}
	for _, s := range shapes {
		if s.board == "" && s.re.MatchString(body) {
			return s.ats, page
		}
	}
	return "", ""
}

// ── following the careers link ───────────────────────────────────────────────

var (
	anchorRe   = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*["']([^"'#]+)[^"']*["'][^>]*>(.*?)</a>`)
	tagRe      = regexp.MustCompile(`<[^>]+>`)
	careerWord = regexp.MustCompile(`(?i)career|\bjobs?\b|vacanc|join[ -]?(?:us|our|the)\b|work[ -](?:with|for|at)[ -]us\b|recruit`)
	fileExt    = regexp.MustCompile(`(?i)\.(?:pdf|docx?|jpe?g|png|gif|svg|zip)$`)
)

// Links into a job board or a social network are never the way to an
// employer's own ATS; following one is a wasted fetch.
var offsite = []string{
	"linkedin.com", "indeed.com", "indeed.co.uk", "glassdoor.com", "glassdoor.co.uk",
	"facebook.com", "twitter.com", "x.com", "instagram.com", "youtube.com", "tiktok.com",
}

// careerLinks returns up to n links that look like the way to a page's jobs,
// those whose URL says so ahead of those where only the anchor text does. The
// host only counts when the link leaves the site: on careers.example.com every
// link has "career" in its host, the privacy policy included.
func careerLinks(body, page string, n int) []string {
	base, err := url.Parse(page)
	if err != nil {
		return nil
	}
	var strong, weak []string
	seen := map[string]bool{page: true}
	for _, m := range anchorRe.FindAllStringSubmatch(body, -1) {
		u, err := base.Parse(html.UnescapeString(strings.TrimSpace(m[1])))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || fileExt.MatchString(u.Path) {
			continue
		}
		inHref := careerWord.MatchString(u.Path+"?"+u.RawQuery) ||
			!strings.EqualFold(u.Host, base.Host) && careerWord.MatchString(u.Host)
		if !inHref && !careerWord.MatchString(tagRe.ReplaceAllString(m[2], " ")) {
			continue
		}
		skip := false
		for _, d := range offsite {
			skip = skip || onHost(strings.ToLower(u.Hostname()), d)
		}
		if s := u.String(); !skip && !seen[s] {
			seen[s] = true
			if inHref {
				strong = append(strong, s)
			} else {
				weak = append(weak, s)
			}
		}
	}
	out := append(strong, weak...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Getter fetches a page and reports the URL it ended on after redirects, which
// is what makes a careers link that redirects to the ATS detectable.
type Getter interface {
	Get(ctx context.Context, url string) (final, body string, err error)
}

// Resolver finds a site's ATS by reading it the way a person would: the home
// page, the careers link on it, the "search jobs" link on that.
//
// ponytail: no JavaScript. A careers page rendered by script shows no links and
// resolves as NoATS; a headless browser is the upgrade if that share is large.
type Resolver struct{ G Getter }

const pageTimeout = 15 * time.Second

// Resolve returns a site's ATS and board, NoATS when the site was read and none
// was found, or Unreachable when it could not be read at all. With NoATS the
// board is the careers page, when the crawl found one: an ATS nobody has a
// shape for still serves its jobs there.
//
// A site that yields nothing is tried again one label up. The pay gap link is
// often to a host that is not the main site — investors.example.com,
// static-cdn.example.com — and that host's home page has no careers link.
func (r Resolver) Resolve(ctx context.Context, site string) (ats, board string) {
	ats = Unreachable
	for _, host := range []string{site, parent(site)} {
		if host == "" {
			continue
		}
		switch a, b := r.crawl(ctx, "https://"+host+"/"); a {
		case Unreachable:
		case NoATS:
			ats, board = NoATS, cmp.Or(board, b)
		default:
			return a, b
		}
	}
	return ats, board
}

// crawl reads three career links off the start page and two off each of
// those: ten fetches at most. Failing an ATS, it returns the first page whose
// URL reads as a careers page.
func (r Resolver) crawl(ctx context.Context, start string) (string, string) {
	final, body, err := r.get(ctx, start)
	if err != nil {
		return Unreachable, ""
	}
	if a, b := Detect(body, final); a != "" {
		return a, b
	}
	seen := map[string]bool{start: true, final: true}
	careers := ""
	next := careerLinks(body, final, 3)
	if len(next) == 0 {
		// A home page rendered by script has no links to follow. These are the
		// two paths most careers sites answer on.
		next = []string{start + "careers", start + "jobs"}
	}
	for depth := 1; depth <= 2; depth++ {
		var after []string
		for _, u := range next {
			if seen[u] {
				continue
			}
			seen[u] = true
			final, body, err := r.get(ctx, u)
			if err != nil {
				continue
			}
			seen[final] = true
			if a, b := Detect(body, final); a != "" {
				return a, b
			}
			if careers == "" && careerWord.MatchString(final) {
				careers = final
			}
			if depth == 1 {
				after = append(after, careerLinks(body, final, 2)...)
			}
		}
		next = after
	}
	return NoATS, careers
}

func (r Resolver) get(ctx context.Context, u string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	return r.G.Get(ctx, u)
}

// secondLevel are the suffixes under which one label up from a three-label
// host is no longer anyone's site.
var secondLevel = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "nhs.uk": true,
	"ltd.uk": true, "plc.uk": true, "uk.com": true, "com.au": true,
}

// parent is a host one label up, or empty where that would leave a bare suffix
// or where it is the same site: www.example.com's parent is example.com, which
// the first crawl already reached through its redirect.
func parent(host string) string {
	_, up, ok := strings.Cut(host, ".")
	if !ok || !strings.Contains(up, ".") || secondLevel[up] || strings.HasPrefix(host, "www.") {
		return ""
	}
	return up
}

// ── transport ────────────────────────────────────────────────────────────────

const (
	userAgent = "Mozilla/5.0 (compatible; snipe/1.3)"
	maxBody   = 16 << 20
)

// Web is the real Getter.
type Web struct{ Client *http.Client }

// NewWeb builds a client that will only connect to public addresses. The crawl
// follows links off pages nobody here wrote, and a router's admin page or a
// cloud metadata address is one href away from any of them. The check runs at
// dial time, after DNS, so a public name that resolves privately is caught too;
// and no proxy, because a proxy is the one hop the check cannot see past.
func NewWeb() *Web {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}
	// A fresh transport rather than a clone of the default: HTTP/1.1 only, since
	// a crawl gains nothing from HTTP/2 and across a few thousand servers its
	// client logs a protocol complaint a minute. A clone cannot be talked out of
	// it — it carries the default's TLS config, which still offers h2 by ALPN,
	// and a server that accepts gets HTTP/1.1 spoken at it.
	t := &http.Transport{
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Web{Client: &http.Client{Transport: t}}
}

func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return fmt.Errorf("refusing non-public address %s", host)
	}
	return nil
}

func (w *Web) Get(ctx context.Context, target string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/csv;q=0.9,*/*;q=0.8")
	res, err := w.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	// One byte over the limit is read so a cut-off body is an error, not a
	// short CSV that parses cleanly with the last employers missing.
	b, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return "", "", err
	}
	if len(b) > maxBody {
		return "", "", fmt.Errorf("%s: response over %d bytes", target, maxBody)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", "", &providers.StatusError{Status: res.StatusCode, URL: target}
	}
	return res.Request.URL.String(), string(b), nil
}
