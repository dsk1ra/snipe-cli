package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// fixTracker writes a tracker whose body is the rows given, and returns the
// tracker path and the repo root the report links resolve against.
func fixTracker(t *testing.T, rows ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	data := paths.Layout{Home: root, Code: root}.Data()
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Applications Tracker\n\n" +
		"| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n" +
		"|---|------|---------|------|-------|--------|-----|--------|-------|\n" +
		strings.Join(rows, "\n") + "\n"
	p := filepath.Join(data, "applications.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, root
}

func TestFixNormalizesStatusesAndLeavesTheRestAlone(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Kafka Streaming Engineer | 3.0/5 | **Hold** | N | [12](../reports/012-a.md) | keep me |`,
		`| 2 | 2026-01-02 | Globex | Rust Compiler Engineer | 4.0/5 | sent | Y | [13](../reports/013-b.md) | untouched |`,
		`| 3 | 2026-01-03 | Initech | Kubernetes Platform Engineer | 4.5/5 | Applied | Y | [14](../reports/014-c.md) | already fine |`,
	)
	before := read(t, p)

	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Statuses) != 2 {
		t.Fatalf("normalised %v, want Hold and sent", res.Statuses)
	}
	got := read(t, p)
	if !strings.Contains(got, "| Evaluated |") || !strings.Contains(got, "| Applied | Y | [13]") {
		t.Errorf("statuses not canonical:\n%s", got)
	}
	// The row that was already right must come back byte-identical: this file is
	// hand-edited and a maintenance pass that reflows it hides its own diff.
	for _, line := range strings.Split(before, "\n") {
		if strings.Contains(line, "already fine") && !strings.Contains(got, line) {
			t.Errorf("an untouched row was rewritten:\n  was %q", line)
		}
	}
}

func TestFixReportsAStatusItCannotMap(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Kafka Engineer | 3.0/5 | Ghosted? | N | [12](../reports/012-a.md) | x |`,
	)
	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unknown) != 1 || !strings.Contains(res.Unknown[0], "Ghosted?") {
		t.Fatalf("unknown is %v, want the status named", res.Unknown)
	}
	// Named, not coerced — the cell is the only record of what was meant.
	if !strings.Contains(read(t, p), "| Ghosted? |") {
		t.Error("an unrecognised status was overwritten")
	}
}

func TestFixMergesTwoRowsForOneReport(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme Ltd. | Backend Engineer | 3.0/5 | Rejected | N | [12](../reports/012-a.md) | first pass |`,
		`| 2 | 2026-01-02 | acme ltd | Backend Engineer | 4.2/5 | Evaluated | Y | [12](../reports/012-a.md) | rescored |`,
	)
	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || !strings.Contains(res.Removed[0], "#1") {
		t.Fatalf("removed %v, want the lower-scored row", res.Removed)
	}
	got := read(t, p)
	if strings.Contains(got, "| 1 |") {
		t.Error("the duplicate row is still there")
	}
	// The kept row takes the better score, and inherits nothing it should not:
	// Rejected ranks below Evaluated, so the status stays Evaluated.
	if !strings.Contains(got, "4.2/5 | Evaluated") {
		t.Errorf("kept row is wrong:\n%s", got)
	}
	if !strings.Contains(got, "rescored — first pass") {
		t.Errorf("the dropped row's notes were lost:\n%s", got)
	}
}

func TestFixKeepsTheFurthestStatusOfAMergedPair(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Rust Engineer | 2.0/5 | Interview | N | [12](../reports/012-a.md) | phone screen |`,
		`| 2 | 2026-01-02 | Acme | Rust Engineer | 4.5/5 | Evaluated | Y | [12](../reports/012-a.md) | rescored |`,
	)
	if _, err := Fix(p, root, true); err != nil {
		t.Fatal(err)
	}
	// The higher score wins the row, but losing the Interview would lose the
	// only record that this application went somewhere.
	if got := read(t, p); !strings.Contains(got, "4.5/5 | Interview") {
		t.Errorf("the further status was dropped:\n%s", got)
	}
}

func TestFixRefusesToMergeAnAppliedRowOnATitleMatchAlone(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Kubernetes Platform Engineer | 2.0/5 | Applied | Y | [12](../reports/012-a.md) | applied here |`,
		`| 2 | 2026-01-02 | Acme | Kubernetes Platform Engineer | 4.5/5 | Evaluated | Y | [99](../reports/099-b.md) | different opening |`,
	)
	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("removed %v — an applied row must survive a mere title match", res.Removed)
	}
	if len(res.Protected) != 1 {
		t.Fatalf("protected %v, want the pair named so it can be checked by hand", res.Protected)
	}
	if got := read(t, p); !strings.Contains(got, "applied here") || !strings.Contains(got, "different opening") {
		t.Errorf("both rows should still be there:\n%s", got)
	}
}

// Two generic titles at one company are not evidence of anything: the fuzzy
// matcher needs a token that discriminates, and "software engineer" has none.
func TestFixLeavesGenericTitlesAlone(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Software Engineer | 2.0/5 | Evaluated | N | [12](../reports/012-a.md) | one |`,
		`| 2 | 2026-01-02 | Acme | Software Engineer | 4.5/5 | Evaluated | Y | [99](../reports/099-b.md) | two |`,
	)
	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() != 0 {
		t.Errorf("changed %d — two Software Engineer rows may be two real openings", res.Changed())
	}
}

func TestFixRewritesARootRelativeReportLink(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Kafka Engineer | 3.0/5 | Evaluated | N | [12](reports/012-a.md) | x |`,
	)
	res, err := Fix(p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Links) != 1 {
		t.Fatalf("links %v, want the root-relative one rewritten", res.Links)
	}
	if got := read(t, p); !strings.Contains(got, "(../reports/012-a.md)") {
		t.Errorf("link does not resolve from data/:\n%s", got)
	}
}

func TestFixDryRunWritesNothing(t *testing.T) {
	p, root := fixTracker(t,
		`| 1 | 2026-01-01 | Acme | Kafka Engineer | 3.0/5 | **Hold** | N | [12](../reports/012-a.md) | x |`,
	)
	before := read(t, p)
	res, err := Fix(p, root, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() == 0 {
		t.Fatal("a dry run still has to report what it would do")
	}
	if read(t, p) != before {
		t.Error("dry run wrote to the tracker")
	}
}
