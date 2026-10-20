// Package scan turns portals.yml into new rows in batch-input.tsv.
//
// It replaces scan.mjs's pipeline: resolve a provider per entry, fetch, filter
// on title, location and age, dedup against what the tree already holds, and
// append. The parts of scan.mjs that are not here are deliberate — liveness
// verification (--verify) is its own step, and the WebSearch handoff is an
// agent-level affordance with no code behind it.
package scan

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	"golang.org/x/sync/errgroup"

	"github.com/dsk1ra/snipe-cli/internal/providers"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// Config is portals.yml, in the shape the scan reads it.
//
// salary_filter is not read: no shipped config sets one, and an unread key is
// honest about that where a no-op implementation would not be.
type Config struct {
	MaxAgeHours *int `yaml:"max_age_hours"`
	TitleFilter struct {
		Positive []string `yaml:"positive"`
		Negative []string `yaml:"negative"`
	} `yaml:"title_filter"`
	LocationFilter *struct {
		AlwaysAllow []string `yaml:"always_allow"`
		Allow       []string `yaml:"allow"`
		Block       []string `yaml:"block"`
	} `yaml:"location_filter"`
	ScanHistory *struct {
		RecheckAfterDays *int `yaml:"recheck_after_days"`
	} `yaml:"scan_history"`
	TrackedCompanies []providers.Entry `yaml:"tracked_companies"`
	JobBoards        []providers.Entry `yaml:"job_boards"`
}

// RecheckAfterDays is how long an `added` history row keeps suppressing its URL.
// -1 — no stated window, which is every shipped config — means forever: without
// being told otherwise the scanner does not re-offer what it has offered once.
func (c *Config) RecheckAfterDays() int {
	if c.ScanHistory == nil || c.ScanHistory.RecheckAfterDays == nil || *c.ScanHistory.RecheckAfterDays < 0 {
		return -1
	}
	return *c.ScanHistory.RecheckAfterDays
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// lower normalises a keyword list. A blank keyword would match every string via
// substring containment and silently bypass every other tier, so it is dropped
// rather than kept.
func lower(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func containsAny(s string, keys []string) bool {
	for _, k := range keys {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// TitleOK is the title gate: at least one positive (or none configured) and no
// negative.
func (c *Config) TitleOK(title string) bool {
	pos, neg := lower(c.TitleFilter.Positive), lower(c.TitleFilter.Negative)
	t := strings.ToLower(title)
	if len(pos) > 0 && !containsAny(t, pos) {
		return false
	}
	return !containsAny(t, neg)
}

// LocationOK is the location gate, in tier order.
//
// A blank location passes: providers expose location inconsistently at list
// level, and penalising a missing field would drop real hits. always_allow
// beats block so a multi-location posting ("Remote, Belgium or France") passes
// on the home region even though another of its options is blocked.
func (c *Config) LocationOK(loc string) bool {
	if c.LocationFilter == nil || strings.TrimSpace(loc) == "" {
		return true
	}
	l := strings.ToLower(loc)
	always, allow, block := lower(c.LocationFilter.AlwaysAllow), lower(c.LocationFilter.Allow), lower(c.LocationFilter.Block)
	if containsAny(l, always) {
		return true
	}
	if containsAny(l, block) {
		return false
	}
	return len(allow) == 0 || containsAny(l, allow)
}

// AgeOK is fail-closed: a posting with no parseable date is dropped.
//
// That is the measured behaviour, not caution for its own sake — on a full scan
// 0 postings were undated and 201 were genuinely dated (median 67 days), so the
// rule costs nothing and stops an undated feed flooding the queue. A per-entry
// max_age_hours of 0 disables the filter for that entry.
func (c *Config) AgeOK(e providers.Entry, postedAt int64, now time.Time) bool {
	hours := 0
	if c.MaxAgeHours != nil {
		hours = *c.MaxAgeHours
	}
	if e.MaxAgeHours != nil {
		hours = *e.MaxAgeHours
	}
	if hours <= 0 {
		return true
	}
	if postedAt <= 0 {
		return false
	}
	return now.Sub(time.UnixMilli(postedAt)) <= time.Duration(hours)*time.Hour
}

// Result is one scan's outcome.
type Result struct {
	Fetched  int
	Kept     []providers.Job
	Filtered struct{ Title, Location, Age, Duplicate, Role int }
	Errors   []error
}

// AddBoards appends boards from outside portals.yml — the registry's — to the
// tracked companies, skipping any whose careers_url an entry already has, and
// reports how many it added.
func (c *Config) AddBoards(es []providers.Entry) int {
	norm := func(u string) string { return strings.TrimRight(strings.ToLower(strings.TrimSpace(u)), "/") }
	have := map[string]bool{}
	for _, e := range c.TrackedCompanies {
		have[norm(e.CareersURL)] = true
	}
	n := 0
	for _, e := range es {
		if u := norm(e.CareersURL); !have[u] {
			have[u] = true
			c.TrackedCompanies = append(c.TrackedCompanies, e)
			n++
		}
	}
	return n
}

// Workers is how many entries Run fetches at once. The Workday boards the
// registry adds answer at about a second a page, and one at a time that is
// half an hour of scan.
//
// ponytail: one limit for every host. Ashby is the only board known to
// rate-limit a bulk scan, and it backs off on its own; per-host limits are the
// upgrade if another starts refusing.
const Workers = 8

// Run resolves and fetches every enabled entry, then filters and dedups.
//
// Fetching is concurrent and filtering is not: the postings are filtered in
// entry order once every fetch is in, so which of two copies of a posting is
// kept does not depend on which board answered first.
//
// A provider that fails is recorded and skipped: one board being down is not a
// reason to lose the other 116 companies' postings.
func Run(ctx context.Context, c *Config, f providers.Fetcher, k Known, now time.Time) Result {
	type slot struct {
		e    providers.Entry
		jobs []providers.Job
		err  error
	}
	var slots []*slot
	g := errgroup.Group{}
	g.SetLimit(Workers)
	for _, group := range [][]providers.Entry{c.TrackedCompanies, c.JobBoards} {
		for _, e := range group {
			if !e.On() || strings.TrimSpace(e.Name) == "" {
				continue
			}
			p, err := providers.Resolve(e)
			if err != nil {
				// A careers_url-only entry that nothing detects is not an
				// error. An entry that names its provider and does not get
				// one is: portals.yml says fetch this board, and the run
				// would otherwise report "1 board, 0 fetched" and stop there.
				if e.Provider != "" {
					slots = append(slots, &slot{e: e, err: err})
				}
				continue
			}
			s := &slot{e: e}
			slots = append(slots, s)
			g.Go(func() error {
				if s.jobs, s.err = p.Fetch(ctx, e, f); s.err != nil {
					s.err = fmt.Errorf("%s (%s): %w", e.Name, p.ID(), s.err)
				}
				// A posting the list could not place is placed here, by the
				// worker that fetched it, and only if the title keeps it: a
				// few hundred requests a scan, which in the filter loop below
				// ran one at a time.
				for i, j := range s.jobs {
					if j.Locate != nil && c.TitleOK(j.Title) {
						s.jobs[i].Location = j.Locate(ctx)
					}
				}
				return nil
			})
		}
	}
	_ = g.Wait() // a failed fetch is recorded on its slot, never returned

	var res Result
	kept := map[string]bool{}
	keptRole := map[string]bool{}
	for _, s := range slots {
		e := s.e
		if s.err != nil {
			res.Errors = append(res.Errors, s.err)
			continue
		}
		res.Fetched += len(s.jobs)
		for _, j := range s.jobs {
			// Resolve the company before the dedup, not after: it is half
			// of the company+role key, and a board that cannot name the
			// employer at list level would otherwise key on the empty
			// string for every posting it returns.
			if j.Company == "" {
				j.Company = e.Name
			}
			j.Source = e.Name
			role, hasRole := roleKey(j.Company, j.Title)
			switch {
			case !c.TitleOK(j.Title):
				res.Filtered.Title++
			case !c.LocationOK(j.Location):
				res.Filtered.Location++
			case !c.AgeOK(e, j.PostedAt, now):
				res.Filtered.Age++
			case k.URLs[j.URL] || kept[j.URL]:
				res.Filtered.Duplicate++
			case hasRole && (k.Roles[role] || keptRole[role]):
				res.Filtered.Role++
			default:
				kept[j.URL] = true
				keptRole[role] = true
				res.Kept = append(res.Kept, j)
			}
		}
	}
	sort.SliceStable(res.Kept, func(i, j int) bool { return res.Kept[i].URL < res.Kept[j].URL })
	return res
}

// Append writes the kept postings to batch-input.tsv, continuing the id
// sequence. It rewrites the whole file atomically rather than appending in
// place: a torn append leaves a half-row that LoadOffers silently skips, and
// the row it skips is a job the user never sees.
func Append(inputPath string, jobs []providers.Job, source string) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}
	b, err := os.ReadFile(inputPath)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	body := strings.TrimRight(string(b), "\n")
	if body == "" {
		body = "id\turl\tsource\tnotes"
	}
	offers, err := state.LoadOffers(inputPath)
	if err != nil {
		return 0, err
	}
	next := 1
	for id := range offers {
		if id >= next {
			next = id + 1
		}
	}
	var sb strings.Builder
	sb.WriteString(body)
	for _, j := range jobs {
		note := strings.TrimSpace(j.Company + " | " + j.Title)
		// A tab in a cell shifts every column after it, exactly as a pipe does
		// in the tracker table. Same fix: substitute, do not escape.
		sb.WriteString(fmt.Sprintf("\n%d\t%s\t%s\t%s", next, clean(j.URL), clean(source), clean(note)))
		next++
	}
	sb.WriteString("\n")
	return len(jobs), state.WriteAtomic(inputPath, []byte(sb.String()))
}

func clean(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}
