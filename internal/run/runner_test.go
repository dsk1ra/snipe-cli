package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// fixture builds a home with n offers already in batch-input.tsv.
func fixture(t *testing.T, n int) paths.Layout {
	t.Helper()
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	if err := os.MkdirAll(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := state.AppendOffer(l.Input(), state.Offer{
			ID: i, URL: "https://example.test/" + string(rune('a'+i)), Source: "test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func opts() Options {
	o := DefaultOptions()
	o.SkipPhase2 = true
	o.SkipPhase3 = true
	return o
}

// silence swaps the runner's printer for the test log.
func silence(r *Runner, t *testing.T) *Runner {
	r.out = func(f string, a ...any) { t.Logf(f, a...) }
	return r
}

func TestScoresEveryOffer(t *testing.T) {
	l := fixture(t, 3)
	var scored atomic.Int64
	ph := Phases{
		Score: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			scored.Add(1)
			_, err := state.Staged(ctx, ck, "p1.score", id, func(context.Context, int) (int, error) { return id, nil })
			return err
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}
	r := silence(newT(t, l, opts(), ph), t)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scored.Load() != 3 {
		t.Errorf("scored %d, want 3", scored.Load())
	}
}

// The core claim of the redesign: a stop lets the in-flight offer finish, does
// not start the rest, and a restart picks up where it left off.
func TestGracefulStopThenResume(t *testing.T) {
	l := fixture(t, 5)
	var started atomic.Int64
	var r *Runner

	ph := Phases{
		Score: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			started.Add(1)
			// The first offer asks for a stop mid-flight, then finishes.
			if started.Load() == 1 {
				r.RequestStop()
			}
			_, err := state.Staged(ctx, ck, "p1.score", id, func(context.Context, int) (int, error) { return id * 10, nil })
			return err
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}
	o := opts()
	o.ParallelScore = 1
	r = silence(newT(t, l, o, ph), t)

	err := r.Run(context.Background())
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("got %v, want ErrStopped", err)
	}
	if started.Load() != 1 {
		t.Fatalf("started %d offers after a stop, want 1", started.Load())
	}
	// The one that ran got to finish and checkpoint.
	if got := len(state.LoadCheckpoint(l.Work(), 1).Done()); got != 1 {
		t.Errorf("offer 1 checkpointed %d stages, want 1", got)
	}

	// Restart: the finished offer is not redone, the other four run.
	started.Store(0)
	var redone atomic.Int64
	ph.Score = func(ctx context.Context, id int, ck *state.Checkpoint) error {
		started.Add(1)
		_, err := state.Staged(ctx, ck, "p1.score", id, func(context.Context, int) (int, error) {
			redone.Add(1)
			return id * 10, nil
		})
		return err
	}
	r2 := silence(newT(t, l, o, ph), t)
	if err := r2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if started.Load() != 5 {
		t.Errorf("resumed run visited %d offers, want 5", started.Load())
	}
	// Offer 1's stage was cached, so only four did real work.
	if redone.Load() != 4 {
		t.Errorf("resumed run recomputed %d stages, want 4 (offer 1 was checkpointed)", redone.Load())
	}
}

// A hard stop cancels the context. Stages already committed survive; the one in
// flight does not, and its failure is not recorded as a failure.
func TestHardCancelKeepsCompletedStages(t *testing.T) {
	l := fixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	ph := Phases{
		Score: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			if _, err := state.Staged(ctx, ck, "p1.fetch", id, func(context.Context, int) (string, error) {
				return "jd text", nil
			}); err != nil {
				return err
			}
			cancel() // the model call is interrupted
			_, err := state.Staged(ctx, ck, "p1.score", id, func(c context.Context, _ int) (int, error) {
				return 0, c.Err()
			})
			return err
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}
	r := silence(newT(t, l, opts(), ph), t)
	_ = r.Run(ctx)

	ck := state.LoadCheckpoint(l.Work(), 1)
	done := ck.Done()
	if len(done) != 1 || done[0] != "p1.fetch" {
		t.Fatalf("checkpoint holds %v, want just p1.fetch", done)
	}
	// And it names the stage that was dropped, which is what makes the resume
	// line say where it picks up instead of leaving it to be inferred from
	// what did not happen.
	if got := ck.Interrupted(); got != "p1.score" {
		t.Errorf("interrupted at %q, want p1.score", got)
	}
	if w := state.Interrupted(l.Work()); len(w) != 1 || w[0].ID != 1 || w[0].Stage != "p1.score" {
		t.Errorf("state.Interrupted = %+v, want one entry naming #1 at p1.score", w)
	}
	// A cancellation is a stop, not a failure: the row must not be marked
	// score_failed, or the next run would need --retry-failed to touch it.
	rows, err := state.Load(l.State())
	if err != nil {
		t.Fatal(err)
	}
	if rows[1].P1Status == state.P1Failed {
		t.Error("a cancelled offer was recorded as failed")
	}
}

func TestUnavailableIsNeverRetried(t *testing.T) {
	l := fixture(t, 2)
	st, err := state.Open(l.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Commit(1, func(r *state.Row) { r.P1Status = state.P1Unavailable }); err != nil {
		t.Fatal(err)
	}
	var seen []int
	ph := Phases{
		Score: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			seen = append(seen, id)
			return nil
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}
	r := silence(newT(t, l, opts(), ph), t)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != 2 {
		t.Errorf("scored %v, want only [2] — an expired posting must not be refetched", seen)
	}
}

func TestSecondRunnerRefuses(t *testing.T) {
	l := fixture(t, 1)
	if err := os.MkdirAll(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := state.Acquire(l.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	r := silence(newT(t, l, opts(), Phases{
		Score:     func(context.Context, int, *state.Checkpoint) error { return nil },
		EvalScore: func(int) (float64, bool) { return 0, false },
	}), t)
	if err := r.Run(context.Background()); !errors.Is(err, state.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
}

func TestP1GateSkipsPhase2AndRecordsIt(t *testing.T) {
	l := fixture(t, 2)
	st, _ := state.Open(l.State())
	// One under the gate, one over.
	_ = st.Commit(1, func(r *state.Row) { r.P1Status = state.P1Scored; r.P1Score = "2.0" })
	_ = st.Commit(2, func(r *state.Row) { r.P1Status = state.P1Scored; r.P1Score = "4.0" })

	var evaled []int
	o := DefaultOptions()
	o.SkipPhase1, o.SkipPhase3 = true, true
	r := silence(newT(t, l, o, Phases{
		Eval: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			evaled = append(evaled, id)
			return nil
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}), t)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(evaled) != 1 || evaled[0] != 2 {
		t.Errorf("evaluated %v, want only [2]", evaled)
	}
	rows, _ := state.Load(l.State())
	if rows[1].P2Status != state.P2Gated {
		t.Errorf("gated row records %q, want %q", rows[1].P2Status, state.P2Gated)
	}
	if rows[1].P3Status != state.P3Skipped {
		t.Errorf("gated row's Phase 3 is %q, want skipped", rows[1].P3Status)
	}
}

func TestQueueDrainPutsBackOnStop(t *testing.T) {
	l := fixture(t, 2)
	if err := os.WriteFile(l.Queue(), []byte("1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var r *Runner
	o := opts()
	o.Drain = true
	r = silence(newT(t, l, o, Phases{
		Score: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			r.RequestStop()
			return nil
		},
		EvalScore: func(int) (float64, bool) { return 0, false },
	}), t)

	if err := r.Run(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("got %v, want ErrStopped", err)
	}
	// Offer 1 was popped and run; the stop lands before 2, which must still be
	// queued — a stop that swallowed queued work would lose it silently.
	b, err := os.ReadFile(l.Queue())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "2\n" {
		t.Errorf("queue is %q, want \"2\\n\"", got)
	}
}

func TestOrphanedEvalIsReset(t *testing.T) {
	l := fixture(t, 1)
	st, _ := state.Open(l.State())
	// evaled, with a report number whose file was never written.
	_ = st.Commit(1, func(r *state.Row) {
		r.P1Status = state.P1Scored
		r.P2Status = state.P2Evaled
		r.ReportNum = "042"
	})
	if err := os.MkdirAll(l.Reports(), 0o755); err != nil {
		t.Fatal(err)
	}

	o := opts()
	o.SkipPhase1 = true
	r := silence(newT(t, l, o, Phases{
		Eval:      func(context.Context, int, *state.Checkpoint) error { return nil },
		EvalScore: func(int) (float64, bool) { return 0, false },
	}), t)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := state.Load(l.State())
	if rows[1].P2Status != state.P2Failed {
		t.Errorf("orphaned row is %q, want eval_failed so it re-evaluates", rows[1].P2Status)
	}
}

func TestPopQueue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q.txt")
	os.WriteFile(p, []byte("7\n8\n9\n"), 0o644)
	for _, want := range []int{7, 8, 9} {
		got, ok := popQueue(p)
		if !ok || got != want {
			t.Fatalf("got %d, %v; want %d", got, ok, want)
		}
	}
	if _, ok := popQueue(p); ok {
		t.Error("empty queue still popped")
	}
	pushQueueFront(p, 3)
	if got, _ := popQueue(p); got != 3 {
		t.Errorf("push-front then pop gave %d, want 3", got)
	}
}

// newT builds a runner over a fresh store for the fixture home.
func newT(t *testing.T, l paths.Layout, o Options, ph Phases) *Runner {
	t.Helper()
	st, err := state.Open(l.State())
	if err != nil {
		t.Fatal(err)
	}
	return New(l, o, ph, st, nil, nil)
}

// Overruling the Phase 1 gate has to move the row off `p1-gated` while the eval
// runs, not only once it lands.
//
// Nothing wrote P2Evaluating before, so for the whole minutes-long call the row
// still read exactly as it had before the override — and the queue went on
// showing "?" and offering "proceed?" on an offer already being evaluated. A
// cancelled eval puts the gate back, so the question returns rather than the row
// sitting on a stage no process is working on.
func TestAnOverruledGateIsMarkedInFlight(t *testing.T) {
	seen := func(t *testing.T, evalErr error) (during, after string) {
		t.Helper()
		l := fixture(t, 1)
		st, err := state.Open(l.State())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Commit(1, func(x *state.Row) {
			x.P1Status, x.P1Score = state.P1Scored, "1.9"
			x.P2Status, x.P3Status = state.P2Gated, state.P3Skipped
		}); err != nil {
			t.Fatal(err)
		}

		o := opts()
		o.SkipPhase1, o.SkipPhase2, o.SkipPhase3 = true, false, true
		o.OnlyID, o.P1Threshold = 1, 0 // what `proceed?` spawns
		r := silence(New(l, o, Phases{
			Eval: func(context.Context, int, *state.Checkpoint) error {
				during = st.Rows()[1].P2Status
				return evalErr
			},
			EvalScore: func(int) (float64, bool) { return 0, false },
		}, st, nil, nil), t)
		_ = r.Run(context.Background())
		return during, st.Rows()[1].P2Status
	}

	t.Run("the row stops reading as gated while the eval runs", func(t *testing.T) {
		during, _ := seen(t, nil)
		if during != state.P2Evaluating {
			t.Errorf("mid-eval the row reads %q, want %q", during, state.P2Evaluating)
		}
	})

	t.Run("a cancelled eval puts the gate back", func(t *testing.T) {
		_, after := seen(t, context.Canceled)
		if after != state.P2Gated {
			t.Errorf("after a stop the row reads %q, want the gate back so it asks again", after)
		}
	})
}
