package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-state.tsv")
	in := map[int]Row{
		2: {ID: 2, URL: "u2", P1Status: P1Scored, P1Score: "2.30", Archetype: "Outside targets",
			P2Status: P2Gated, P3Status: P3Skipped, Error: "p1-gated(threshold=2.5)"},
		1: {ID: 1, URL: "u1", P1Status: P1Scored, P1Score: "2.6", P2Status: P2Evaled, ReportNum: "078", P3Status: P3Completed, Retries: 3},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range in {
		if out[id] != want {
			t.Errorf("row %d: got %+v want %+v", id, out[id], want)
		}
	}
	// "2.30" must survive as text — reparsing through a float would ship 2.3.
	if out[2].P1Score != "2.30" {
		t.Errorf("score text mangled: %q", out[2].P1Score)
	}
	// Empty cells are written as "-", never elided, or every later column shifts.
	if !strings.Contains(string(mustRead(t, path)), "\t-\t") {
		t.Error("empty cells not written as -")
	}
}

func TestLoadRejectsShortRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.tsv")
	writeFile(t, path, Header+"\n1\thttp://x\tscored\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want an error on a 3-column row, got none")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	rows, err := Load(filepath.Join(t.TempDir(), "absent.tsv"))
	if err != nil || len(rows) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", rows, err)
	}
}

func TestFailedPhase(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  Row
		want string
	}{
		{"clean", Row{P1Status: P1Scored, P2Status: P2Evaled, ReportNum: "078", P3Status: P3Completed}, ""},
		{"score failed", Row{P1Status: P1Failed}, "p1"},
		{"unavailable", Row{P1Status: P1Unavailable}, "p1"},
		{"eval failed", Row{P1Status: P1Scored, P2Status: P2Failed}, "p2"},
		// The corruption class: a failed eval back-filled as a success. Without
		// this case it renders as "pending" forever with no way to act on it.
		{"evaled without report", Row{P1Status: P1Scored, P2Status: P2Evaled}, "p2"},
		{"pdf failed", Row{P1Status: P1Scored, P2Status: P2Evaled, ReportNum: "078", P3Status: P3Failed}, "p3"},
		{"gated is not failed", Row{P1Status: P1Scored, P2Status: P2Gated}, ""},
	} {
		if got := FailedPhase(tc.row); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestCommitIsAtomicAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.tsv")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(7, func(r *Row) { r.URL = "u7"; r.P1Status = P1Scored }); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(7, func(r *Row) { r.P2Status = P2Evaled; r.ReportNum = "012" }); err != nil {
		t.Fatal(err)
	}
	rows, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := rows[7]
	if got.URL != "u7" || got.P1Status != P1Scored || got.ReportNum != "012" {
		t.Fatalf("commit did not accumulate: %+v", got)
	}
}

func TestLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.pid")
	if Active(path) {
		t.Fatal("no file, yet reported active")
	}
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if !Active(path) {
		t.Error("held lock not reported active")
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Errorf("second acquire: got %v, want ErrLocked", err)
	}
	if pid, ok := HolderPID(path); !ok || pid <= 0 {
		t.Errorf("HolderPID: got %d, %v", pid, ok)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if Active(path) {
		t.Error("released lock still reported active")
	}
}

func TestStagedSkipsCachedAndInvalidatesOnInputChange(t *testing.T) {
	dir := t.TempDir()
	c := LoadCheckpoint(dir, 42)
	ctx := context.Background()

	calls := 0
	double := func(_ context.Context, n int) (int, error) { calls++; return n * 2, nil }

	if got, err := Staged(ctx, c, "p2.judge", 21, double); err != nil || got != 42 {
		t.Fatalf("got %d, %v", got, err)
	}
	if got, err := Staged(ctx, c, "p2.judge", 21, double); err != nil || got != 42 {
		t.Fatalf("got %d, %v", got, err)
	}
	if calls != 1 {
		t.Errorf("cached stage re-ran: %d calls", calls)
	}

	// A changed input must re-run — this is what makes editing a JD and
	// retrying redo exactly the affected stages rather than all of them.
	if got, err := Staged(ctx, c, "p2.judge", 50, double); err != nil || got != 100 {
		t.Fatalf("got %d, %v", got, err)
	}
	if calls != 2 {
		t.Errorf("changed input did not re-run: %d calls", calls)
	}

	// Survives a restart: a fresh handle over the same dir keeps the cache.
	fresh := LoadCheckpoint(dir, 42)
	if got, err := Staged(ctx, fresh, "p2.judge", 50, double); err != nil || got != 100 {
		t.Fatalf("got %d, %v", got, err)
	}
	if calls != 2 {
		t.Errorf("reload lost the cache: %d calls", calls)
	}

	if err := fresh.Discard(); err != nil {
		t.Fatal(err)
	}
	if _, err := Staged(ctx, fresh, "p2.judge", 50, double); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("discard did not clear: %d calls", calls)
	}
}

func TestStagedDoesNotCacheFailures(t *testing.T) {
	c := LoadCheckpoint(t.TempDir(), 1)
	boom := errors.New("ollama down")
	_, err := Staged(context.Background(), c, "p1.score", "jd", func(context.Context, string) (int, error) {
		return 0, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the underlying error", err)
	}
	if len(c.Done()) != 0 {
		t.Errorf("a failed stage was cached: %v", c.Done())
	}
}

// Pause, end to end: the stage in flight is dropped, every stage behind it is
// kept, and the resume runs exactly the one that was dropped.
//
// This is the whole value of the feature. A Phase 2 offer paused in judgment
// has already paid for the parse and the evidence, and redoing those is two
// model calls spent to reach the same place.
func TestAPausedStageResumesWithoutRedoingTheOnesBehindIt(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())

	calls := map[string]int{}
	stage := func(name string) func(context.Context, string) (string, error) {
		return func(ctx context.Context, in string) (string, error) {
			calls[name]++
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return in + "|" + name, nil
		}
	}

	// The run: parse, evidence, then a pause lands mid-judgment.
	c := LoadCheckpoint(dir, 214)
	out, err := Staged(ctx, c, "p2.jdparse", "jd", stage("p2.jdparse"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err = Staged(ctx, c, "p2.evidence", out, stage("p2.evidence")); err != nil {
		t.Fatal(err)
	}
	cancel() // the pause: SIGUSR1 cancels the context under the in-flight call
	if _, err := Staged(ctx, c, "p2.judgment", out, stage("p2.judgment")); err == nil {
		t.Fatal("the cancelled stage returned a result")
	}

	// What the pause left on disk. Read through a fresh handle, because a
	// resume is a new process.
	paused := LoadCheckpoint(dir, 214)
	if got := paused.Interrupted(); got != "p2.judgment" {
		t.Errorf("interrupted at %q, want p2.judgment", got)
	}
	if got := paused.Done(); len(got) != 2 || got[0] != "p2.evidence" || got[1] != "p2.jdparse" {
		t.Errorf("kept %v, want the two stages that landed", got)
	}
	if w := Interrupted(dir); len(w) != 1 || w[0].ID != 214 || w[0].Stage != "p2.judgment" {
		t.Errorf("Interrupted(dir) = %+v, want one entry naming #214 at p2.judgment", w)
	}

	// The resume.
	fresh := context.Background()
	out, err = Staged(fresh, paused, "p2.jdparse", "jd", stage("p2.jdparse"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err = Staged(fresh, paused, "p2.evidence", out, stage("p2.evidence")); err != nil {
		t.Fatal(err)
	}
	if _, err = Staged(fresh, paused, "p2.judgment", out, stage("p2.judgment")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"p2.jdparse", "p2.evidence"} {
		if calls[name] != 1 {
			t.Errorf("%s ran %d times — a resume redid work the pause had paid for", name, calls[name])
		}
	}
	if calls["p2.judgment"] != 2 {
		t.Errorf("p2.judgment ran %d times, want 2 — once dropped, once on resume", calls["p2.judgment"])
	}
	// Nothing is in flight once it lands, or every later run reports a pause
	// that already finished.
	if got := paused.Interrupted(); got != "" {
		t.Errorf("still marked %q after the stage landed", got)
	}
	if w := Interrupted(dir); len(w) != 0 {
		t.Errorf("Interrupted(dir) = %+v after a clean finish, want none", w)
	}
}

// A stage marks itself before the call, not after. Without that the pause has
// nothing to name: the dropped stage writes no output, which is the point.
func TestAStageIsMarkedWhileItRuns(t *testing.T) {
	dir := t.TempDir()
	c := LoadCheckpoint(dir, 7)
	_, err := Staged(context.Background(), c, "p1.score", "jd", func(context.Context, string) (int, error) {
		// Mid-call, read from disk the way another process would.
		if got := LoadCheckpoint(dir, 7).Interrupted(); got != "p1.score" {
			t.Errorf("mid-call the checkpoint says %q, want p1.score", got)
		}
		return 4, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := LoadCheckpoint(dir, 7).Interrupted(); got != "" {
		t.Errorf("after the call the checkpoint still says %q", got)
	}
}

// Timeline dates a row by when it FIRST appeared, and returns every offer the
// tree holds rather than a recent slice of them.
//
// Earliest-wins is what keeps the list groupable: ids are handed out in order,
// so a first-touch day runs monotonically down the list and each date needs one
// heading. Dating by last touch would file a re-evaluated offer under today
// while its row stayed put in the middle of the list, and the day it belongs to
// would then be printed twice.
func TestTimelineDatesAnOfferByItsFirstTouch(t *testing.T) {
	dir := t.TempDir()
	jds, evals := filepath.Join(dir, "jds"), filepath.Join(dir, "evals")
	for _, d := range []string{jds, evals} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	at := func(dir, name string, daysAgo int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().AddDate(0, 0, -daysAgo)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	// #1 was fetched a fortnight ago and re-evaluated today: well outside any
	// window this used to apply, and it belongs to the older day.
	at(jds, "1.txt", 14)
	at(evals, "1.json", 0)
	at(jds, "2.txt", 3)
	// #3 was queued with #4 but nothing fetched it until today, so its only
	// mtime is today's. It cannot have appeared after #4 did.
	at(jds, "3.txt", 0)
	at(jds, "4.txt", 2)
	// #9 is queued and has nothing on disk yet.
	queue := filepath.Join(dir, "queue.txt")
	if err := os.WriteFile(queue, []byte("9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, days := Timeline(jds, evals, queue)
	if want := []int{1, 2, 3, 4, 9}; !slices.Equal(ids, want) {
		t.Fatalf("ids %v, want %v in order", ids, want)
	}
	// The whole point of the dates: one heading per day, never a day reopened
	// further down the list.
	for i := 1; i < len(ids); i++ {
		if days[ids[i]].Before(days[ids[i-1]]) {
			t.Errorf("#%d is dated %s, before #%d at %s — the list would print that day twice",
				ids[i], DayKey(days[ids[i]]), ids[i-1], DayKey(days[ids[i-1]]))
		}
	}
	if got, want := DayKey(days[3]), DayKey(time.Now().AddDate(0, 0, -2)); got != want {
		t.Errorf("#3 is dated %s, want %s — it cannot have landed after #4", got, want)
	}
	if got, want := DayKey(days[1]), DayKey(time.Now().AddDate(0, 0, -14)); got != want {
		t.Errorf("#1 is dated %s, want %s — the later eval won", got, want)
	}
	if got, want := DayKey(days[2]), DayKey(time.Now().AddDate(0, 0, -3)); got != want {
		t.Errorf("#2 is dated %s, want %s", got, want)
	}
	if got, want := DayKey(days[9]), DayKey(time.Now()); got != want {
		t.Errorf("#9 is dated %s, want %s — a queued offer is being worked on now", got, want)
	}
}
