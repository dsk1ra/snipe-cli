// Package registry is the scanner's company list, built from data the UK
// government already publishes rather than curated by hand in portals.yml.
//
// The seed is the gender pay gap service. Every UK employer with 250 or more
// staff has to report each year, and the report carries a company number, the
// registered postcode, a size band, SIC codes and, for about two thirds of
// employers, a link to their own website. Resolve turns that website into the
// applicant tracking system behind its careers page.
//
// The registry is deliberately not filtered by region. The postcode is the
// registered office, not where the jobs are: on the 2025 data only 49
// West Midlands-registered employers carry a tech SIC code, and a filter on it
// drops every Birmingham hub of a London-registered bank. Location is a property
// of the posting and is filtered there.
package registry

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/dsk1ra/snipe-cli/internal/providers"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// Employer is one registry row.
type Employer struct {
	ID            string // the pay gap service's EmployerId; the row key
	Name          string
	CompanyNumber string // empty for public-sector employers
	PostCode      string // registered office
	Size          string // the service's band, e.g. "1000 to 4999"
	SIC           string // comma-separated
	Site          string // host of the employer's website; empty when unknown
	ATS           string // provider id, NoATS or Unreachable; empty until resolved
	Board         string // the URL the ATS answers on; for NoATS, the careers page if one was found
	Checked       string // YYYY-MM-DD the site was last resolved
}

// The two outcomes of a resolution that found no board. They are kept apart
// because they age differently: a site with no recognisable ATS stays that way
// for a month, an unreachable one is worth trying again on the next build.
const (
	NoATS       = "none"
	Unreachable = "unreachable"
)

const gpgURL = "https://gender-pay-gap.service.gov.uk/viewing/download-data/%d"

// Download fetches the reporting years that matter and merges them.
//
// A year's file is named for the year its snapshot date falls in and opens
// once the first employer files, so in October 2026 "2026" holds a few hundred
// early filers and "2025" is the complete year. The employers are the union of
// the newest two files, which is everyone currently reporting whatever the
// month. Two more years are read for links only, which in October 2026 took the
// current employers with a link from 5,989 of 11,231 to 7,747: a link left blank
// this year was often given the year before.
func Download(ctx context.Context, g Getter, now time.Time) ([]Employer, error) {
	var years [][]Employer
	for y := now.Year(); y > now.Year()-5 && len(years) < 4; y-- {
		yctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_, body, err := g.Get(yctx, fmt.Sprintf(gpgURL, y))
		cancel()
		var se *providers.StatusError
		if errors.As(err, &se) && se.Status == 404 {
			continue // a year that has not opened yet
		}
		if err != nil {
			return nil, err
		}
		es, err := ParseGPG(strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("pay gap data for %d: %w", y, err)
		}
		if len(es) > 0 { // a year opened but not yet filed into is a missing one
			years = append(years, es)
		}
	}
	if len(years) == 0 {
		return nil, errors.New("no pay gap data downloaded")
	}
	return Merge(years), nil
}

// ParseGPG reads one reporting year's CSV.
func ParseGPG(r io.Reader) ([]Employer, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimPrefix(h, "\ufeff")] = i
	}
	for _, need := range []string{"EmployerName", "EmployerId", "PostCode", "CompanyNumber", "SicCodes", "CompanyLinkToGPGInfo", "EmployerSize"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("no %s column", need)
		}
	}
	var out []Employer
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		get := func(k string) string {
			if i := col[k]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		name := get("EmployerName")
		if _, ok := col["CurrentName"]; ok && get("CurrentName") != "" {
			name = get("CurrentName")
		}
		out = append(out, Employer{
			ID: get("EmployerId"), Name: name,
			CompanyNumber: get("CompanyNumber"), PostCode: get("PostCode"),
			Size: get("EmployerSize"),
			SIC:  strings.Join(strings.Fields(strings.ReplaceAll(get("SicCodes"), ",", " ")), ","),
			Site: Site(get("CompanyLinkToGPGInfo")),
		})
	}
}

// Merge combines reporting years, newest first: employers from the first two,
// each row's fields from its newest year, and the site from the newest year
// that gave a usable one.
func Merge(years [][]Employer) []Employer {
	byID := map[string]*Employer{}
	var order []string
	for i, year := range years {
		for _, e := range year {
			if e.ID == "" {
				continue
			}
			cur, ok := byID[e.ID]
			if !ok {
				if i > 1 {
					continue // has not reported for two years
				}
				cp := e
				byID[e.ID] = &cp
				order = append(order, e.ID)
				continue
			}
			if cur.Site == "" {
				cur.Site = e.Site
			}
		}
	}
	out := make([]Employer, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// hosting is where an employer's link points when it is a file rather than a
// site: CDNs, document viewers, cloud drives, the service itself. Its host says
// nothing about the employer, so the link is dropped and an older year's tried.
// The list is the hosts shared by the most unrelated employers on 2023–2025.
var hosting = []string{
	"www.gov.uk", "service.gov.uk", "amazonaws.com", "google.com", "sharepoint.com",
	"cloudfront.net", "ctfassets.net", "live.com", "adobe.com", "office.net",
	"website-files.com", "hubspotusercontent-na1.net", "hubspotusercontent-eu1.net",
	"hubspot.net", "digitaloceanspaces.com", "windows.net", "shopify.com",
	"squarespace.com", "widen.net", "sanity.io", "q4cdn.com", "finalsite.net",
	"azureedge.net", "bigcontent.io", "filesusr.com", "b-cdn.net", "prismic.io",
	"storyblok.com", "cdn-website.com", "issuu.com", "pagetiger.com", "dropbox.com",
	"box.com", "wixstatic.com", "1drv.ms", "bit.ly", "linkedin.com",
	// Platforms, found on the first full build: one label up from a customer's
	// file is the platform's own site, and its careers page is not the employer's.
	"bigcommerce.com", "ecloud.co.uk",
}

func onHost(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// Site is the host of an employer's pay gap link, or empty when the link is
// missing, malformed or points at a file host.
func Site(link string) string {
	link = strings.ReplaceAll(strings.TrimSpace(link), `\`, "/")
	if link == "" {
		return ""
	}
	if !strings.Contains(link, "://") {
		link = "https://" + link
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if !strings.Contains(h, ".") {
		return ""
	}
	for _, d := range hosting {
		if onHost(h, d) {
			return ""
		}
	}
	return h
}

// ── registry.tsv ─────────────────────────────────────────────────────────────

const header = "employer_id\tname\tcompany_number\tpostcode\tsize\tsic\tsite\tats\tboard\tchecked"

// Load reads the registry. A missing file is an empty registry.
func Load(path string) ([]Employer, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Employer
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 10 || f[0] == "employer_id" {
			continue
		}
		out = append(out, Employer{f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7], f[8], f[9]})
	}
	return out, nil
}

// Save writes the registry atomically, for the reason scan.Append gives: a
// torn file silently loses the rows past the tear.
func Save(path string, es []Employer) error {
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var sb strings.Builder
	sb.WriteString(header + "\n")
	for _, e := range es {
		f := []string{e.ID, e.Name, e.CompanyNumber, e.PostCode, e.Size, e.SIC, e.Site, e.ATS, e.Board, e.Checked}
		for i := range f {
			f[i] = clean(f[i])
		}
		sb.WriteString(strings.Join(f, "\t") + "\n")
	}
	return state.WriteAtomic(path, []byte(sb.String()))
}

// Carry copies each site's previous resolution onto a fresh download and
// returns the sites that still need resolving. A resolution is reused for
// maxAgeDays; an Unreachable one never is, because the commonest cause is a
// timeout that will not recur.
func Carry(fresh, prev []Employer, now time.Time, maxAgeDays int) []string {
	type res struct{ ats, board, checked string }
	known := map[string]res{}
	for _, p := range prev {
		if p.Site == "" || p.ATS == "" || p.ATS == Unreachable {
			continue
		}
		d, err := time.Parse("2006-01-02", p.Checked)
		if err != nil || now.Sub(d) > time.Duration(maxAgeDays)*24*time.Hour {
			continue
		}
		known[p.Site] = res{p.ATS, p.Board, p.Checked}
	}
	var todo []string
	queued := map[string]bool{}
	for i := range fresh {
		e := &fresh[i]
		if e.Site == "" {
			continue
		}
		if r, ok := known[e.Site]; ok {
			e.ATS, e.Board, e.Checked = r.ats, r.board, r.checked
			continue
		}
		if !queued[e.Site] {
			queued[e.Site] = true
			todo = append(todo, e.Site)
		}
	}
	return todo
}

// ── as scan entries ──────────────────────────────────────────────────────────

// Country is the one the registry describes: the pay gap service is the UK's.
const Country = "United Kingdom"

// sizeRank orders the service's size bands, so a board shared by a group's
// entities is named for the largest of them.
var sizeRank = map[string]int{
	"250 to 499": 1, "500 to 999": 2, "1000 to 4999": 3, "5000 to 19,999": 4, "20,000 or more": 5,
}

// Entries is the registry as scan entries: one per board some provider can
// read, named for the largest employer on it, asking for UK postings. A board
// no provider claims is left out rather than failed, which means a new
// provider widens the scan without touching this.
func Entries(es []Employer) []providers.Entry {
	best := map[string]Employer{}
	var order []string
	for _, e := range es {
		if e.Board == "" {
			continue
		}
		cur, ok := best[e.Board]
		if !ok {
			order = append(order, e.Board)
		}
		if !ok || sizeRank[e.Size] > sizeRank[cur.Size] ||
			sizeRank[e.Size] == sizeRank[cur.Size] && len(e.Name) < len(cur.Name) {
			best[e.Board] = e
		}
	}
	var out []providers.Entry
	for _, b := range order {
		en := providers.Entry{Name: Display(best[b].Name), CareersURL: b, Extra: map[string]any{"country": Country}}
		if _, err := providers.Resolve(en); err == nil {
			out = append(out, en)
		}
	}
	return out
}

var small = map[string]bool{"OF": true, "AT": true, "IN": true, "ON": true, "BY": true, "TO": true}

var legalSuffix = regexp.MustCompile(`(?i)[\s,.]+(?:limited|ltd\.?|plc|llp|lp|\(uk\))$`)

// Display is a legal name as people say it: "BARCLAYS BANK UK PLC" becomes
// "Barclays Bank UK". The name only labels the queue row and keys the
// company+role dedup — Phase 1 reads the company off the posting itself.
//
// ponytail: a word of two letters or none of them vowels stays upper case (UK,
// EY, HSBC, JLL), which gets "BAE" and "IBM" wrong; a curated alias list is the
// upgrade if that matters.
func Display(name string) string {
	n := strings.TrimSpace(name)
	for m := legalSuffix.ReplaceAllString(n, ""); m != n; m = legalSuffix.ReplaceAllString(n, "") {
		n = m
	}
	if n != strings.ToUpper(n) {
		return n // already cased by whoever filed it
	}
	words := strings.Fields(n)
	for i, w := range words {
		if small[w] {
			words[i] = strings.ToLower(w)
		} else if len(w) > 2 && strings.ContainsAny(w, "AEIOU") && !strings.ContainsAny(w, "0123456789&") {
			r := []rune(strings.ToLower(w))
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
	}
	return strings.Join(words, " ")
}
