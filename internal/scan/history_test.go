package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/providers"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The regression this whole file exists for: a URL that was queued and has
// since left batch-input.tsv is still known, because the history kept it.
func TestLoadRemembersURLsThatHaveLeftTheQueue(t *testing.T) {
	dir := t.TempDir()
	m := Memory{
		Input: write(t, dir, "batch-input.tsv", "id\turl\tsource\tnotes\n1\thttps://queued\tx\tn\n"),
		History: write(t, dir, "scan-history.tsv", HistoryHeader+"\n"+
			"https://queued\t2026-01-01\tgreenhouse\tEngineer\tAcme\tadded\tLondon\n"+
			"https://gated\t2026-01-02\tgreenhouse\tEngineer\tAcme\tadded\tLondon\n"),
		Tracker: write(t, dir, "applications.md",
			"| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n"+
				"|---|---|---|---|---|---|---|---|---|\n"+
				"| 1 | 2026-01-03 | Globex | Backend Engineer | 4.0/5 | Applied | Y | [1](../reports/1.md) | https://applied |\n"),
	}
	k, err := Load(m, -1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://queued", "https://gated", "https://applied"} {
		if !k.URLs[u] {
			t.Errorf("%s is not remembered", u)
		}
	}
	if !k.Roles["globex::backend engineer"] {
		t.Errorf("roles are %v, want the tracker's company+role pair", k.Roles)
	}
}

func TestLoadSurvivesAMissingHistoryAndTracker(t *testing.T) {
	dir := t.TempDir()
	m := Memory{Input: write(t, dir, "batch-input.tsv", "id\turl\tsource\tnotes\n")}
	if _, err := Load(m, -1, time.Now()); err != nil {
		t.Fatalf("a fresh tree has neither file yet: %v", err)
	}
}

func TestDedupHistoryRowOnlyAgesOutPlainSightings(t *testing.T) {
	now := time.Date(2026, 8, 25, 23, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name              string
		firstSeen, status string
		recheck           int
		want              bool
	}{
		{"no window configured, so forever", "2020-01-01", "added", -1, true},
		{"inside the window", "2026-08-20", "added", 30, true},
		{"outside the window", "2026-06-01", "added", 30, false},
		{"exactly the window is out", "2026-07-26", "added", 30, false},
		// A judgement is not a sighting: it does not age out at all.
		{"blocked host ignores the window", "2020-01-01", "skipped_blocked_host", 30, true},
		{"expired ignores the window", "2020-01-01", "skipped_expired", 30, true},
		// An unreadable date must suppress, never re-offer: the safe direction
		// is one missed posting, not a hundred re-scored ones.
		{"unparseable date", "not-a-date", "added", 30, true},
		{"impossible date", "2026-02-31", "added", 30, true},
	} {
		if got := dedupHistoryRow(tc.firstSeen, tc.status, tc.recheck, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRoleKeyRefusesPlaceholderCompanies(t *testing.T) {
	if _, ok := roleKey("unknown", "Software Engineer"); ok {
		t.Error("`unknown` as a key would suppress that title from every board at once")
	}
	if _, ok := roleKey("  ", "Software Engineer"); ok {
		t.Error("a blank company is not a key")
	}
	k, ok := roleKey("  Acme Corp ", "Backend Engineer")
	if !ok || k != "acme corp::backend engineer" {
		t.Errorf("got %q %v, want a trimmed lowercase pair", k, ok)
	}
}

// Two boards carrying the same opening is one opening. The tracker already
// holding it is the same thing a day later.
func TestRunDropsARoleTheTrackerAlreadyHas(t *testing.T) {
	c := cfg(t, `
max_age_hours: 0
job_boards:
  - name: RemoteOK
    careers_url: https://remoteok.com/api
`)
	f := stub{pages: map[string]string{
		"https://remoteok.com/api": `[
		  {"position":"Backend Engineer","url":"https://x/1","company":"Globex","location":"London"},
		  {"position":"backend engineer","url":"https://x/2","company":"GLOBEX","location":"London"},
		  {"position":"Backend Engineer","url":"https://x/3","company":"Acme","location":"London"}
		]`,
	}}
	k := Known{URLs: map[string]bool{}, Roles: map[string]bool{"globex::backend engineer": true}}
	res := Run(context.Background(), c, f, k, time.Now())

	if len(res.Kept) != 1 || res.Kept[0].Company != "Acme" {
		t.Fatalf("kept %+v, want only the company the tracker has never seen", res.Kept)
	}
	if res.Filtered.Role != 2 {
		t.Errorf("dropped %d on role, want both Globex spellings", res.Filtered.Role)
	}
}

func TestAppendHistoryRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "scan-history.tsv")
	jobs := []providers.Job{{
		URL: "https://x/1", Title: "Backend\tEngineer", Company: "Acme",
		Location: "London", Source: "RemoteOK",
	}}
	if err := AppendHistory(p, jobs, "2026-08-25", "added"); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(p, []providers.Job{{URL: "https://x/2"}}, "2026-08-25", "added"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 3 || lines[0] != HistoryHeader {
		t.Fatalf("file is %q, want a header and two rows", string(b))
	}
	// A tab inside a cell would shift every column after it — including the
	// status the dedup reads.
	if strings.Count(lines[1], "\t") != 6 {
		t.Errorf("row %q does not have seven columns", lines[1])
	}
	k, err := Load(Memory{History: p}, -1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !k.URLs["https://x/1"] || !k.URLs["https://x/2"] {
		t.Errorf("read back %v, want both rows", k.URLs)
	}
}
