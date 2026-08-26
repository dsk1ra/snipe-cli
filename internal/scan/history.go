package scan

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/providers"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

// Memory is every file the scan remembers a URL from.
//
// batch-input.tsv on its own is not the memory, which is what this used to
// assume. An offer that is queued and then gated, archived or hand-removed
// leaves the input file, and the next scan of the same board offers it again at
// one fetch and one Phase 1 call apiece — 351 of the 552 URLs in the history
// were already missing from the input file when this landed.
type Memory struct {
	Input   string // batch/state/batch-input.tsv — what is queued now
	History string // data/scan-history.tsv — every URL a scan has ever kept
	Tracker string // data/applications.md — what was actually applied to
}

// Known is what a scan already has: URLs from every memory file, and the
// company+role pairs the tracker has a row for.
type Known struct {
	URLs  map[string]bool
	Roles map[string]bool
}

// HistoryHeader is scan-history.tsv's first line. The location column was added
// last and rows written before it still parse: nothing past field 5 is read.
const HistoryHeader = "url\tfirst_seen\tportal\ttitle\tcompany\tstatus\tlocation"

// permanentStatus marks a row that no recheck window may reopen — the URL was
// not merely seen, it was judged unusable.
var permanentStatus = map[string]bool{
	"skipped_invalid_url":  true,
	"skipped_blocked_host": true,
}

// trackerURL matches a posting link anywhere in a tracker row: the report link
// is markdown and the notes carry the raw URL, so both spellings end in `)` or
// `|` or whitespace.
var trackerURL = regexp.MustCompile(`https?://[^\s|)]+`)

// Load reads every memory file. A missing history or tracker is not an error —
// on a fresh tree neither exists yet — but an unreadable input file is, because
// scanning without it would renumber the queue.
func Load(m Memory, recheckAfterDays int, now time.Time) (Known, error) {
	k := Known{URLs: map[string]bool{}, Roles: map[string]bool{}}

	offers, err := state.LoadOffers(m.Input)
	if err != nil && !os.IsNotExist(err) {
		return k, err
	}
	for _, o := range offers {
		k.URLs[o.URL] = true
	}

	for _, r := range readHistory(m.History) {
		if dedupHistoryRow(r.firstSeen, r.status, recheckAfterDays, now) {
			k.URLs[r.url] = true
		}
	}

	if b, err := os.ReadFile(m.Tracker); err == nil {
		for _, u := range trackerURL.FindAllString(string(b), -1) {
			k.URLs[u] = true
		}
	}
	apps, err := tracker.LoadApplications(m.Tracker)
	if err == nil {
		for _, a := range apps {
			if key, ok := roleKey(a.Company, a.Role); ok {
				k.Roles[key] = true
			}
		}
	}
	return k, nil
}

type historyRow struct{ url, firstSeen, status string }

func readHistory(path string) []historyRow {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []historyRow
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[0] == "" || f[0] == "url" {
			continue
		}
		r := historyRow{url: f[0], firstSeen: f[1], status: "added"}
		if len(f) > 5 && strings.TrimSpace(f[5]) != "" {
			r.status = strings.TrimSpace(f[5])
		}
		out = append(out, r)
	}
	return out
}

// dedupHistoryRow reports whether a history row still suppresses its URL.
//
// Only a plain `added` row can ever age out, and only when portals.yml states a
// window. Everything else — a skip, a verify outcome, an unparseable date — is
// permanent, because those rows record a judgement, not a sighting.
func dedupHistoryRow(firstSeen, status string, recheckAfterDays int, now time.Time) bool {
	if status != "added" || permanentStatus[status] {
		return true
	}
	if recheckAfterDays < 0 {
		return true
	}
	days, ok := daysSince(firstSeen, now)
	if !ok {
		return true
	}
	return days < recheckAfterDays
}

// daysSince counts whole days between two UTC dates, both truncated to midnight
// so a scan at 23:00 and one at 01:00 agree on the age.
func daysSince(day string, now time.Time) (int, bool) {
	d, err := time.ParseInLocation("2006-01-02", day, time.UTC)
	if err != nil {
		return 0, false
	}
	u := now.UTC()
	today := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return int(today.Sub(d).Hours() / 24), true
}

// roleKey is the company+role dedup key: the same opening reached through two
// boards is one opening, and the tracker already holds the first one.
//
// A placeholder company is refused. The tracker carries rows whose company the
// provider could not expose, and "unknown" + "Software Engineer" as a key would
// suppress every future posting with that title from every board at once.
func roleKey(company, role string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(company))
	r := strings.ToLower(strings.TrimSpace(role))
	if c == "" || r == "" || c == "unknown" || c == "company" {
		return "", false
	}
	return c + "::" + r, true
}

// AppendHistory records what a scan kept, so the URL is still remembered once
// it has left batch-input.tsv.
//
// It rewrites the file rather than appending in place, for the reason Append
// gives: a torn line is a URL the next scan offers again.
func AppendHistory(path string, jobs []providers.Job, date, status string) error {
	if len(jobs) == 0 {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	body := strings.TrimRight(string(b), "\n")
	if body == "" {
		body = HistoryHeader
	}
	var sb strings.Builder
	sb.WriteString(body)
	for _, j := range jobs {
		sb.WriteString("\n" + strings.Join([]string{
			clean(j.URL), date, clean(j.Source), clean(j.Title),
			clean(j.Company), status, clean(j.Location),
		}, "\t"))
	}
	sb.WriteString("\n")
	return state.WriteAtomic(path, []byte(sb.String()))
}
