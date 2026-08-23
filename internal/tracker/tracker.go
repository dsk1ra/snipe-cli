// Package tracker reads data/applications.md and data/follow-ups.md.
//
// The markdown table is the source of truth — there is no database. The Node
// implementation kept an optional SQLite index derived from this file and
// documented it as safe to delete; at a few hundred rows a full parse costs
// less than opening the connection would.
package tracker

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Canonical statuses (templates/states.yml). Anything else is an alias or junk.
const (
	StatusEvaluated = "evaluated"
	StatusApplied   = "applied"
	StatusResponded = "responded"
	StatusInterview = "interview"
	StatusOffer     = "offer"
	StatusRejected  = "rejected"
	StatusDiscarded = "discarded"
	StatusSkip      = "skip"
)

var aliases = map[string]string{
	"hold":        StatusEvaluated,
	"sent":        StatusApplied,
	"monitor":     StatusSkip,
	"geo blocker": StatusSkip,
}

var trailingDate = regexp.MustCompile(`\s+\d{4}-\d{2}-\d{2}.*$`)

// NormalizeStatus strips the bold markers, the trailing date and the case that
// hand-edited rows accumulate. Statuses must be canonical; this is what makes a
// row written by hand still readable.
func NormalizeStatus(raw string) string {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, "**", "")))
	s = strings.TrimSpace(trailingDate.ReplaceAllString(s, ""))
	if a, ok := aliases[s]; ok {
		return a
	}
	return s
}

// Application is one row of the tracker.
type Application struct {
	Num     int
	Date    string // YYYY-MM-DD as written; the file is the record
	Company string
	Role    string
	Score   string
	Status  string // canonical, via NormalizeStatus
	PDF     string
	Report  string // raw cell — its [n] is the *report* number, not Num
	Notes   string
}

// Followup is one row of follow-ups.md.
type Followup struct {
	Num     int
	AppNum  int
	Date    string
	Company string
	Role    string
	Channel string
	Contact string
	Notes   string
}

// splitRow splits a markdown table row into trimmed cells. A literal | in any
// cell corrupts the row and every column after it — both the Node parsers split
// on raw pipes too, and the writer substitutes / for it. Matching that exactly
// matters more than being cleverer than the writer.
func splitRow(line string) []string {
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func LoadApplications(path string) ([]Application, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Application
	for _, line := range strings.Split(string(b), "\n") {
		p := splitRow(line)
		if len(p) < 9 {
			continue
		}
		num, err := strconv.Atoi(p[1])
		if err != nil {
			continue // the header and the separator row
		}
		a := Application{
			Num: num, Date: p[2], Company: p[3], Role: p[4],
			Score: p[5], Status: NormalizeStatus(p[6]), PDF: p[7], Report: p[8],
		}
		if len(p) > 9 {
			a.Notes = p[9]
		}
		out = append(out, a)
	}
	return out, nil
}

func LoadFollowups(path string) ([]Followup, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Followup
	for _, line := range strings.Split(string(b), "\n") {
		p := splitRow(line)
		if len(p) < 8 {
			continue
		}
		num, err := strconv.Atoi(p[1])
		if err != nil {
			continue
		}
		appNum, _ := strconv.Atoi(p[2])
		f := Followup{
			Num: num, AppNum: appNum, Date: p[3], Company: p[4],
			Role: p[5], Channel: p[6], Contact: p[7],
		}
		if len(p) > 8 {
			f.Notes = p[8]
		}
		out = append(out, f)
	}
	return out, nil
}

var reportLink = regexp.MustCompile(`\]\(([^)]+)\)`)

// ReportPath pulls the path out of the report cell's markdown link. The cell is
// free text for a row with no report ("no report — P1-gated"), so a miss is
// normal, not an error.
func ReportPath(cell string) (string, bool) {
	m := reportLink.FindStringSubmatch(cell)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func ParseDate(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	return t, err == nil
}

func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours() / 24)
}

func itoa(n int) string { return strconv.Itoa(n) }

func atoiStrict(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

// SetStatus rewrites one application's status cell in place. Editing an
// existing row's status is the one hand-edit the tracker rules allow; adding a
// row is not (that goes through batch/tracker-additions and the merge).
func SetStatus(path string, num int, status string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		p := splitRow(line)
		if len(p) < 9 {
			continue
		}
		n, err := strconv.Atoi(p[1])
		if err != nil || n != num {
			continue
		}
		// Rebuild from the original cells so nothing else shifts.
		cells := p[1 : len(p)-1]
		cells[5] = status
		lines[i] = "| " + strings.Join(cells, " | ") + " |"
		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
	}
	return nil
}

// ByReport finds the row whose Report cell carries this report number. Offer
// ids and tracker row numbers are separate sequences that drift apart; the
// report number is the only id the queue and the tracker share.
func ByReport(apps []Application, report string) (Application, bool) {
	want, err := strconv.Atoi(strings.TrimSpace(report))
	if err != nil {
		return Application{}, false
	}
	for _, a := range apps {
		if n, ok := reportNum(a.Report); ok && n == want {
			return a, true
		}
	}
	return Application{}, false
}
