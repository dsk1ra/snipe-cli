package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Merging batch/tracker-additions/*.tsv into data/applications.md, the way
// tracker/merge-tracker.mjs did at the end of every bash run.
//
// The 792-line original is mostly machinery this does not need: a hashed lock
// directory, a ready-IPC handshake and a column migration, all of which existed
// because several bash subshells could reach the file at once. The Go runner
// holds one lock for the whole run and calls this in-process, so what is left is
// the merge itself.

// display is the cased spelling each canonical status is written back as. The
// table is read by a human; NormalizeStatus lowercases for comparison only.
var display = map[string]string{
	StatusEvaluated: "Evaluated", StatusApplied: "Applied", StatusResponded: "Responded",
	StatusInterview: "Interview", StatusOffer: "Offer", StatusRejected: "Rejected",
	StatusDiscarded: "Discarded", StatusSkip: "SKIP",
}

// Addition is one pending tracker row, as Phase 3 writes it.
type Addition struct {
	Num                                     int
	Date, Company, Role, Status, Score, PDF string
	Report, Notes                           string
}

// Result is what one merge did, for the run summary.
type Result struct {
	Added, Updated, Skipped int
	Files                   []string // merged files, ready to be filed away
}

var (
	scoreCellRe  = regexp.MustCompile(`^\d+\.?\d*/5$`)
	statusCellRe = regexp.MustCompile(`(?i)^(evaluated|applied|responded|interview|offer|rejected|discarded|skip|dup|repost|hold|monitor|sent)`)
	reportNumRe  = regexp.MustCompile(`\[(\d+)\]`)
	linkPathRe   = regexp.MustCompile(`\]\(([^)]+)\)`)
	reportRelRe  = regexp.MustCompile(`^(?:\.\./)*(reports/.+)$`)
	nonAlnum     = regexp.MustCompile(`[^a-z0-9]`)
	sepRowRe     = regexp.MustCompile(`^\|[\s|:-]+\|$`)
)

// ParseAddition reads one addition file's contents.
//
// The column order is detected rather than assumed. Both spellings exist on
// disk — the TSV convention is (status, score) and some hand-written rows are
// (score, status) — and guessing wrong writes "Applied" into the score column
// of the user's real tracker, which is not a failure anyone notices quickly.
func ParseAddition(content string) (Addition, error) {
	f := strings.Split(strings.TrimSpace(content), "\t")
	if len(f) < 8 {
		return Addition{}, fmt.Errorf("want at least 8 fields, got %d", len(f))
	}
	num, _ := strconv.Atoi(strings.TrimSpace(f[0]))

	c4, c5 := strings.TrimSpace(f[4]), strings.TrimSpace(f[5])
	statusCol, scoreCol := c4, c5
	switch {
	case statusCellRe.MatchString(c4) && !looksLikeScore(c4):
	case looksLikeScore(c4) && statusCellRe.MatchString(c5):
		statusCol, scoreCol = c5, c4
	}

	a := Addition{
		Num: num, Date: f[1], Company: f[2], Role: f[3],
		Status: validateStatus(statusCol), Score: scoreCol, PDF: f[6], Report: f[7],
	}
	if len(f) > 8 {
		a.Notes = f[8]
	}
	return a, nil
}

func looksLikeScore(s string) bool {
	return scoreCellRe.MatchString(s) || s == "N/A" || s == "DUP"
}

// validateStatus maps whatever the writer emitted onto a canonical state.
func validateStatus(raw string) string {
	if d, ok := display[NormalizeStatus(raw)]; ok {
		return d
	}
	return "Evaluated"
}

// NormalizeCompany is the same-company key: lowercase alphanumerics only, so
// "Re:Link Ltd." and "relink ltd" compare equal before roles are considered.
func NormalizeCompany(name string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(name), "")
}

func reportNum(cell string) (int, bool) {
	m := reportNumRe.FindStringSubmatch(cell)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// NormalizeReportLink rewrites a report link so it resolves from the tracker
// file's own directory. Additions carry root-relative `reports/...`; a tracker
// at data/applications.md needs `../reports/...`. Idempotent, so re-running
// never double-prefixes.
func NormalizeReportLink(field, trackerDir, repoRoot string) string {
	return linkPathRe.ReplaceAllStringFunc(field, func(whole string) string {
		m := linkPathRe.FindStringSubmatch(whole)
		r := reportRelRe.FindStringSubmatch(m[1])
		if r == nil {
			return whole // not a report path — leave it alone
		}
		rel, err := filepath.Rel(trackerDir, filepath.Join(repoRoot, r[1]))
		if err != nil {
			return whole
		}
		return "](" + filepath.ToSlash(rel) + ")"
	})
}

// parseScore reads a score cell for the upgrade comparison. Presentation only:
// bold markers and the /5 suffix carry no information.
func parseScore(cell string) float64 {
	s := strings.TrimSpace(strings.ReplaceAll(cell, "**", ""))
	s = strings.TrimSuffix(s, "/5")
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// cell substitutes a literal pipe rather than escaping it: both parsers split on
// a raw |, so `\|` would still split. Lossy by design — the table is a display
// view and batch/evals/*.json keeps the verbatim role.
func cell(s string) string { return strings.ReplaceAll(s, "|", "/") }

func buildRow(a Addition) string {
	return fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %s | %s | %s |",
		a.Num, a.Date, cell(a.Company), cell(a.Role), a.Score, a.Status, a.PDF, a.Report, cell(a.Notes))
}

// Merge folds every pending addition into the tracker and returns what it did.
//
// New rows go directly under the header separator, because the table reads
// newest-first. A duplicate is only overwritten by a HIGHER score: a re-run that
// scored the same offer lower is a worse measurement of the same thing, not
// news.
func Merge(trackerPath, additionsDir string) (Result, error) {
	var res Result
	ents, err := os.ReadDir(additionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, err
	}
	var files []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tsv") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return res, nil
	}
	sort.Strings(files)

	b, err := os.ReadFile(trackerPath)
	if err != nil {
		return res, err
	}
	lines := strings.Split(string(b), "\n")

	apps, err := LoadApplications(trackerPath)
	if err != nil {
		return res, err
	}
	max := 0
	for _, a := range apps {
		if a.Num > max {
			max = a.Num
		}
	}

	trackerDir := filepath.Dir(trackerPath)
	// reports/ sits at the repo root, which is the tracker's parent under the
	// data/ layout and the tracker's own directory at root layout.
	repoRoot := trackerDir
	if filepath.Base(trackerDir) == "data" {
		repoRoot = filepath.Dir(trackerDir)
	}

	var fresh []string
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(additionsDir, name))
		if err != nil {
			return res, err
		}
		add, err := ParseAddition(string(raw))
		if err != nil {
			fmt.Printf("  WARN: skipping %s: %v\n", name, err)
			res.Skipped++
			continue
		}
		add.Report = NormalizeReportLink(add.Report, trackerDir, repoRoot)

		dup, idx := findDuplicate(apps, add)
		if dup != nil {
			oldScore, newScore := parseScore(dup.Score), parseScore(add.Score)
			if newScore <= oldScore {
				res.Skipped++
				res.Files = append(res.Files, name)
				continue
			}
			upd := Addition{
				Num: dup.Num, Date: add.Date, Company: add.Company, Role: add.Role,
				Score: add.Score, Status: display[dup.Status], PDF: dup.PDF, Report: add.Report,
				Notes: fmt.Sprintf("Re-eval %s (%g→%g). %s", add.Date, oldScore, newScore, add.Notes),
			}
			if upd.Status == "" {
				upd.Status = add.Status
			}
			if li := lineOf(lines, dup.Num); li >= 0 {
				lines[li] = buildRow(upd)
				apps[idx].Score = add.Score
				res.Updated++
			}
			res.Files = append(res.Files, name)
			continue
		}

		// The addition's own number is only trusted when it is ahead of the
		// tracker: report numbering and row numbering drift, and reusing a
		// number that is already taken writes a second row with one id.
		if add.Num > max {
			max = add.Num
		} else {
			max++
			add.Num = max
		}
		fresh = append(fresh, buildRow(add))
		apps = append(apps, Application{Num: add.Num, Company: add.Company, Role: add.Role,
			Score: add.Score, Report: add.Report})
		res.Added++
		res.Files = append(res.Files, name)
	}

	if len(fresh) > 0 {
		at := insertPoint(lines)
		// The whole block goes in at once, in the order the additions were
		// read — the same splice the Node did. Reversing it here to put the
		// highest number on top looks tidier and disagrees with the oracle.
		out := append([]string{}, lines[:at]...)
		out = append(out, fresh...)
		lines = append(out, lines[at:]...)
	}
	if res.Added+res.Updated > 0 {
		if err := writeAtomic(trackerPath, []byte(strings.Join(lines, "\n"))); err != nil {
			return res, err
		}
	}
	return res, nil
}

// findDuplicate applies the three signals in order of confidence: the same
// report number, the same row id at the same company, then a fuzzy role match
// at the same company.
func findDuplicate(apps []Application, add Addition) (*Application, int) {
	if n, ok := reportNum(add.Report); ok {
		for i := range apps {
			if m, ok := reportNum(apps[i].Report); ok && m == n {
				return &apps[i], i
			}
		}
	}
	norm := NormalizeCompany(add.Company)
	for i := range apps {
		if apps[i].Num == add.Num && NormalizeCompany(apps[i].Company) == norm {
			return &apps[i], i
		}
	}
	for i := range apps {
		if NormalizeCompany(apps[i].Company) == norm && RoleFuzzyMatch(add.Role, apps[i].Role) {
			return &apps[i], i
		}
	}
	return nil, -1
}

func lineOf(lines []string, num int) int {
	want := "| " + strconv.Itoa(num) + " |"
	for i, l := range lines {
		if strings.HasPrefix(l, want) {
			return i
		}
	}
	return -1
}

// insertPoint is the line after the header separator, or after the last line
// that looks like a heading when the table has no separator yet.
func insertPoint(lines []string) int {
	for i, l := range lines {
		if strings.HasPrefix(l, "|") && sepRowRe.MatchString(strings.TrimSpace(l)) {
			return i + 1
		}
	}
	for i, l := range lines {
		if strings.HasPrefix(l, "|") {
			return i
		}
	}
	return len(lines)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// File moves the merged additions into additions/merged/ so the next run does
// not reconsider them.
func File(additionsDir string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	merged := filepath.Join(additionsDir, "merged")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		return err
	}
	for _, n := range names {
		if err := os.Rename(filepath.Join(additionsDir, n), filepath.Join(merged, n)); err != nil {
			return err
		}
	}
	return nil
}
