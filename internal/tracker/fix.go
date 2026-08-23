package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// rank orders the statuses by how far the application actually got, so a merge
// can keep the furthest one.
//
// applied outranks rejected: an open application is later in the pipeline than
// a closed one, and a rejection on a duplicate row must not overwrite the row
// that is still live.
var rank = map[string]int{
	StatusSkip: 0, StatusDiscarded: 0, StatusRejected: 1, StatusEvaluated: 2,
	StatusApplied: 3, StatusResponded: 4, StatusInterview: 5, StatusOffer: 6,
}

// advanced marks a status that has left evaluation. Losing one of these loses a
// real application, so the dedup will not merge two rows on a fuzzy title match
// alone once either has reached it.
func advanced(status string) bool { return rank[status] >= rank[StatusApplied] }

// FixResult is what one repair pass did, or would do.
type FixResult struct {
	Statuses  []string // "#42 Hold -> Evaluated"
	Removed   []string // "#310 Acme — Backend Engineer (kept #402)"
	Links     []string // "#88 report link rewritten"
	Protected []string // fuzzy pairs kept because one side has applied
	Unknown   []string // statuses nothing maps, left exactly as written
}

func (r FixResult) Changed() int { return len(r.Statuses) + len(r.Removed) + len(r.Links) }

type fixRow struct {
	line  int
	cells []string // the inner cells, without the leading and trailing empties
	app   Application
}

// Fix folds tracker/dedup-tracker.mjs, tracker/normalize-statuses.mjs and
// tracker-links into one pass: canonical statuses, one row per opening, and
// report links that resolve from the tracker's own directory.
//
// Rows it does not change are left byte-identical — the tracker is hand-edited,
// and a maintenance command that reflows every row makes its own diff unreadable.
// With apply false nothing is written and the result says what would happen.
func Fix(trackerPath, repoRoot string, apply bool) (FixResult, error) {
	var res FixResult
	b, err := os.ReadFile(trackerPath)
	if err != nil {
		return res, err
	}
	lines := strings.Split(string(b), "\n")

	var rows []*fixRow
	for i, line := range lines {
		p := splitRow(line)
		if len(p) < 9 {
			continue
		}
		num, err := strconv.Atoi(p[1])
		if err != nil {
			continue // header and separator
		}
		// A row without its closing pipe splits one cell short, and every index
		// below would be reading the wrong column. Leave it exactly as written
		// rather than repair it into something worse.
		inner := p[1 : len(p)-1]
		if len(inner) < 9 {
			res.Unknown = append(res.Unknown, fmt.Sprintf("#%d has %d columns, not 9 — left alone", num, len(inner)))
			continue
		}
		r := &fixRow{line: i, cells: inner, app: Application{
			Num: num, Date: p[2], Company: p[3], Role: p[4],
			Score: p[5], Status: NormalizeStatus(p[6]), PDF: p[7], Report: p[8],
		}}
		if len(p) > 9 {
			r.app.Notes = p[9]
		}
		rows = append(rows, r)
	}

	drop := map[int]bool{}
	dedup(rows, drop, &res)

	for _, r := range rows {
		if drop[r.line] {
			continue
		}
		normalizeCells(r, trackerPath, repoRoot, &res)
	}

	if res.Changed() == 0 || !apply {
		return res, nil
	}
	rewritten := make(map[int]string, len(rows))
	for _, r := range rows {
		if !drop[r.line] {
			rewritten[r.line] = "| " + strings.Join(r.cells, " | ") + " |"
		}
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		switch {
		case drop[i]:
		case rewritten[i] != "":
			out = append(out, rewritten[i])
		default:
			out = append(out, line)
		}
	}
	return res, writeAtomic(trackerPath, []byte(strings.Join(out, "\n")))
}

// normalizeCells rewrites the status and the report link in place, recording
// each change.
func normalizeCells(r *fixRow, trackerPath, repoRoot string, res *FixResult) {
	if want, ok := display[r.app.Status]; ok {
		if r.cells[5] != want {
			res.Statuses = append(res.Statuses, fmt.Sprintf("#%d %q -> %s", r.app.Num, r.cells[5], want))
			r.cells[5] = want
		}
	} else if strings.TrimSpace(r.cells[5]) != "" {
		// Coercing an unrecognised status would destroy the only record of what
		// the person meant. Name it and leave it.
		res.Unknown = append(res.Unknown, fmt.Sprintf("#%d %q", r.app.Num, r.cells[5]))
	}
	if link := NormalizeReportLink(r.cells[7], filepath.Dir(trackerPath), repoRoot); link != r.cells[7] {
		res.Links = append(res.Links, fmt.Sprintf("#%d report link rewritten", r.app.Num))
		r.cells[7] = link
	}
}

var reportRef = regexp.MustCompile(`\[(\d+)\]`)

// sameReport reports whether two rows are the same record rather than two
// similar ones. Stronger than a fuzzy title match, and the only evidence strong
// enough to merge a row that has already been applied to.
func sameReport(a, b Application) bool {
	if a.Num == b.Num {
		return true
	}
	ma, mb := reportRef.FindStringSubmatch(a.Report), reportRef.FindStringSubmatch(b.Report)
	return ma != nil && mb != nil && ma[1] == mb[1]
}

// dedup clusters same-company rows whose roles match, keeps the best-scored one
// of each cluster and folds the others into it.
func dedup(rows []*fixRow, drop map[int]bool, res *FixResult) {
	byCompany := map[string][]*fixRow{}
	var order []string
	for _, r := range rows {
		key := NormalizeCompany(r.app.Company)
		if key == "" {
			continue
		}
		if _, seen := byCompany[key]; !seen {
			order = append(order, key)
		}
		byCompany[key] = append(byCompany[key], r)
	}
	warned := map[string]bool{}
	for _, key := range order {
		group := byCompany[key]
		for i, a := range group {
			if drop[a.line] {
				continue
			}
			for _, b := range group[i+1:] {
				if drop[b.line] {
					continue
				}
				if !mergeable(a.app, b.app, warned, res) {
					continue
				}
				keep, lose := a, b
				if parseScore(b.app.Score) > parseScore(a.app.Score) {
					keep, lose = b, a
				}
				fold(keep, lose.app)
				drop[lose.line] = true
				res.Removed = append(res.Removed, fmt.Sprintf("#%d %s — %s (kept #%d)",
					lose.app.Num, lose.app.Company, lose.app.Role, keep.app.Num))
				if lose == a {
					break // a is gone; move to the next row
				}
			}
		}
	}
}

func mergeable(a, b Application, warned map[string]bool, res *FixResult) bool {
	if sameReport(a, b) {
		return true
	}
	if !RoleFuzzyMatch(a.Role, b.Role) {
		return false
	}
	// A fuzzy title match is not enough once either row has been applied to: the
	// two may be sibling openings, and dropping one would lose its status, its
	// report and its notes.
	if advanced(a.Status) || advanced(b.Status) {
		lo, hi := a.Num, b.Num
		if lo > hi {
			lo, hi = hi, lo
		}
		key := fmt.Sprintf("%d:%d", lo, hi)
		if !warned[key] {
			warned[key] = true
			res.Protected = append(res.Protected,
				fmt.Sprintf("#%d and #%d: similar roles but one has been applied to — kept both", lo, hi))
		}
		return false
	}
	return true
}

// fold carries the furthest status and any notes the losing row had onto the
// one being kept.
func fold(keep *fixRow, lose Application) {
	if rank[lose.Status] > rank[keep.app.Status] {
		keep.app.Status = lose.Status
		if d, ok := display[lose.Status]; ok {
			keep.cells[5] = d
		}
	}
	note := strings.TrimSpace(lose.Notes)
	if note == "" || strings.Contains(keep.cells[8], note) {
		return
	}
	merged := strings.TrimSpace(keep.cells[8])
	if merged == "" {
		keep.cells[8] = note
		return
	}
	keep.cells[8] = merged + " — " + note
}
