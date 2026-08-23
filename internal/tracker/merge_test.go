package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const header = `# Applications Tracker

| # | Date | Company | Role | Score | Status | PDF | Report | Notes |
|---|------|---------|------|-------|--------|-----|--------|-------|
| 7 | 2026-01-01 | Acme | Backend Engineer | 3.0/5 | Evaluated | N | [12](../reports/012-acme-2026-01-01.md) | first pass |
`

// fixture builds a tracker at data/applications.md with the additions given as
// raw TSV lines, and returns its directory.
func fixture(t *testing.T, additions ...string) (string, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	add := filepath.Join(root, "batch", "tracker-additions")
	for _, d := range []string{data, add, filepath.Join(root, "reports")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tracker := filepath.Join(data, "applications.md")
	if err := os.WriteFile(tracker, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, line := range additions {
		name := filepath.Join(add, string(rune('a'+i))+".tsv")
		if err := os.WriteFile(name, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tracker, add
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A new offer lands under the separator, and the root-relative report link is
// rewritten to resolve from data/.
func TestMergeAddsANewRow(t *testing.T) {
	tr, add := fixture(t, "99\t2026-02-02\tZeta\tRust Engineer\tEvaluated\t4.5/5\tY\t[20](reports/020-zeta-2026-02-02.md)\tPDF generated")
	res, err := Merge(tr, add)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Updated != 0 || res.Skipped != 0 {
		t.Fatalf("got %+v, want one add", res)
	}
	body := read(t, tr)
	if !strings.Contains(body, "| 99 | 2026-02-02 | Zeta | Rust Engineer | 4.5/5 | Evaluated | Y |") {
		t.Errorf("row not written as score-then-status:\n%s", body)
	}
	if !strings.Contains(body, "](../reports/020-zeta-2026-02-02.md)") {
		t.Errorf("report link not made relative to the tracker:\n%s", body)
	}
	lines := strings.Split(body, "\n")
	if !strings.HasPrefix(lines[4], "| 99 ") {
		t.Errorf("new row is at line %d, want directly under the separator", 4)
	}
}

// The same report coming back with a lower score is a worse measurement of the
// same thing, not news.
func TestMergeKeepsTheBetterScore(t *testing.T) {
	tr, add := fixture(t, "7\t2026-03-03\tAcme\tBackend Engineer\tEvaluated\t2.0/5\tN\t[12](reports/012-acme-2026-01-01.md)\tre-run")
	res, err := Merge(tr, add)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Updated != 0 || res.Added != 0 {
		t.Fatalf("got %+v, want one skip", res)
	}
	if !strings.Contains(read(t, tr), "| 3.0/5 |") {
		t.Error("the better score was overwritten by the worse one")
	}
}

func TestMergeUpgradesOnAHigherScore(t *testing.T) {
	tr, add := fixture(t, "7\t2026-03-03\tAcme\tBackend Engineer\tEvaluated\t4.4/5\tY\t[12](reports/012-acme-2026-01-01.md)\tre-run")
	res, err := Merge(tr, add)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("got %+v, want one update", res)
	}
	body := read(t, tr)
	if !strings.Contains(body, "| 4.4/5 |") || strings.Contains(body, "| 3.0/5 |") {
		t.Errorf("score not upgraded in place:\n%s", body)
	}
	if !strings.Contains(body, "Re-eval 2026-03-03 (3→4.4)") {
		t.Errorf("the upgrade is not recorded in the notes:\n%s", body)
	}
	if strings.Count(body, "| 7 | ") != 1 {
		t.Error("the upgrade added a second row instead of replacing one")
	}
}

// Both column spellings exist on disk. Guessing wrong writes a status into the
// score column of the real tracker, which nobody notices quickly.
func TestParseAdditionDetectsColumnOrder(t *testing.T) {
	std, err := ParseAddition("1\t2026-01-01\tA\tB\tApplied\t4.0/5\tY\t[1](reports/001-a.md)\tn")
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := ParseAddition("1\t2026-01-01\tA\tB\t4.0/5\tApplied\tY\t[1](reports/001-a.md)\tn")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Addition{std, swapped} {
		if a.Status != "Applied" || a.Score != "4.0/5" {
			t.Errorf("got status=%q score=%q, want Applied and 4.0/5", a.Status, a.Score)
		}
	}
}

// A number already taken is drift between report and row numbering, not an
// instruction to write a second row with one id.
func TestMergeRenumbersWhenTheIDIsTaken(t *testing.T) {
	tr, add := fixture(t, "7\t2026-02-02\tOther Co\tData Engineer\tEvaluated\t4.0/5\tY\t[21](reports/021-other-2026-02-02.md)\tx")
	if _, err := Merge(tr, add); err != nil {
		t.Fatal(err)
	}
	body := read(t, tr)
	if !strings.Contains(body, "| 8 | 2026-02-02 | Other Co |") {
		t.Errorf("row kept a taken id instead of taking the next one:\n%s", body)
	}
}

func TestNormalizeReportLinkIsIdempotent(t *testing.T) {
	once := NormalizeReportLink("[20](reports/020-z.md)", "/r/data", "/r")
	twice := NormalizeReportLink(once, "/r/data", "/r")
	if once != "[20](../reports/020-z.md)" || twice != once {
		t.Errorf("got %q then %q", once, twice)
	}
}

func TestFileMovesMergedAdditions(t *testing.T) {
	tr, add := fixture(t, "99\t2026-02-02\tZeta\tRust Engineer\tEvaluated\t4.5/5\tY\t[20](reports/020-z.md)\tx")
	res, err := Merge(tr, add)
	if err != nil {
		t.Fatal(err)
	}
	if err := File(add, res.Files); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(add, "merged", "a.tsv")); err != nil {
		t.Errorf("merged addition was not filed away: %v", err)
	}
	if _, err := os.Stat(filepath.Join(add, "a.tsv")); !os.IsNotExist(err) {
		t.Error("addition is still pending after the merge")
	}
}

func TestByReport(t *testing.T) {
	apps := []Application{
		{Num: 12, Report: "[073](../reports/073-x.md)", Status: StatusEvaluated},
		{Num: 40, Report: "no report — P1-gated", Status: StatusApplied},
	}
	if a, ok := ByReport(apps, "073"); !ok || a.Num != 12 {
		t.Fatalf("padded lookup: got %+v ok=%v", a, ok)
	}
	if a, ok := ByReport(apps, "73"); !ok || a.Num != 12 {
		t.Fatalf("unpadded lookup: got %+v ok=%v", a, ok)
	}
	// A row's own # is not its report number, and a row with no report must
	// never be matched by one.
	if _, ok := ByReport(apps, "40"); ok {
		t.Fatal("matched a row that carries no report number")
	}
	if _, ok := ByReport(apps, "-"); ok {
		t.Fatal("matched on a non-numeric report cell")
	}
}
