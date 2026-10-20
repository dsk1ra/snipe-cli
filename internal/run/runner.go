// Package run is the orchestrator: which offers enter which phase, how many at
// once, what happens on a stop, and where a restart picks up.
//
// It replaces batch/local-runner.sh. Three things it can do that bash could not:
//
//  1. Cancellation reaches the work. The context threads into every Ollama
//     request and every pdflatex exec, so a stop tears down an in-flight call
//     instead of orphaning a child that still holds the GPU.
//  2. A stop is graceful by default: no new offers are admitted, the in-flight
//     stage finishes and checkpoints, and the process exits clean.
//  3. A restart resumes at the next *stage*, not the top of the phase. Phase 2
//     is three model calls and Phase 3 opens with a 66-second rerank; losing
//     those to a Ctrl-C was the single most expensive thing about the old
//     runner.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/dsk1ra/snipe-cli/internal/aliases"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

// ErrStopped is returned when a run ended because a stop was requested. It is
// not a failure: everything finished is committed and a restart continues.
var ErrStopped = errors.New("stopped on request")

type Options struct {
	OnlyID      int
	StartFrom   int
	RetryFailed bool
	Drain       bool
	SkipPhase1  bool
	SkipPhase2  bool
	SkipPhase3  bool
	DryRun      bool

	// P1Threshold gates Phase 1 → 2. Benchmarked over 115 offers: recall on
	// good offers (P2 ≥ 4.0) stays at 1.000 for every gate from 1.5 to 3.0,
	// while the share of genuine mismatches dropped nearly doubles at 2.5
	// (0.389 → 0.722). 2.5 is the safe end of that plateau. 0 disables it.
	P1Threshold float64
	// Threshold gates Phase 2 → 3, on the Phase 2 eval score.
	Threshold float64

	ParallelScore int
	ParallelEval  int
	ParallelPDF   int // VRAM-shared — keep low

	MaxRetries int
	OllamaURL  string
}

func DefaultOptions() Options {
	return Options{
		P1Threshold:   2.5,
		Threshold:     3.0,
		ParallelScore: 1,
		ParallelEval:  1,
		ParallelPDF:   2,
		MaxRetries:    2,
	}
}

// Phase is one unit of work on one offer. It gets a checkpoint so it can skip
// the stages it already completed.
type Phase func(ctx context.Context, id int, ck *state.Checkpoint) error

// Phases is how the runner gets its work. Concrete functions, injected by the
// command — which also makes the orchestration testable without a model.
type Phases struct {
	Score Phase
	Eval  Phase
	PDF   Phase

	// EvalScore reads back the authoritative Phase 2 score for the Phase 3
	// gate. It is the code-computed value from the eval artifact, not the
	// Phase 1 pre-screen.
	EvalScore func(id int) (float64, bool)
}

type Runner struct {
	L      paths.Layout
	Opt    Options
	Ph     Phases
	Ollama *ollama.Client
	Log    *ollama.TimingLog

	st       *state.Store
	stopping atomic.Bool
	out      func(string, ...any)
}

// New takes the state store rather than opening one, because the phases need
// the same handle. Two handles over one file is not a cache-coherence problem to
// paper over — each keeps its own in-memory map and flushes the whole file, so
// the last writer silently discards the other's rows.
func New(l paths.Layout, opt Options, ph Phases, st *state.Store, cl *ollama.Client, tl *ollama.TimingLog) *Runner {
	return &Runner{
		L: l, Opt: opt, Ph: ph, st: st, Ollama: cl, Log: tl,
		out: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
}

// RequestStop asks for a graceful stop: finish what is in flight, admit nothing
// new. Idempotent, so a second SIGTERM handled elsewhere as a hard cancel does
// not have to coordinate with this.
func (r *Runner) RequestStop() { r.stopping.Store(true) }

func (r *Runner) Stopping() bool { return r.stopping.Load() }

// Run executes the pipeline. The lock is held for its whole duration; a second
// runner refuses rather than interleaving.
func (r *Runner) Run(ctx context.Context) error {
	if !r.Opt.DryRun {
		lock, err := state.Acquire(r.L.Lock())
		if err != nil {
			return err
		}
		defer lock.Release()
	}

	for _, d := range []string{r.L.StateDir(), r.L.CacheDir(), r.L.Scores(), r.L.Evals(),
		r.L.Logs(), r.L.JDs(), r.L.Work(), r.L.Reports(), r.L.Errors()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	offers, err := state.LoadOffers(r.L.Input())
	if err != nil {
		return err
	}
	if len(offers) == 0 {
		r.out("No offers in %s.", r.L.Input())
		return nil
	}

	// Drain works through the queue file, one id at a time, so a JD pasted
	// while a run is active is picked up rather than colliding with it.
	if r.Opt.Drain {
		err = r.drain(ctx, offers)
	} else {
		err = r.pipeline(ctx, offers)
	}
	if err == nil && !r.Opt.DryRun {
		r.suggestAliases(ctx)
	}
	return err
}

// suggestAliases rebuilds the skills review queue once the run is over, when
// the 30B has nothing left to do and the embedder can have the GPU. It is a
// courtesy, not a phase: a failure is reported and the run still succeeds.
// 8–10 s over 228 reports.
func (r *Runner) suggestAliases(ctx context.Context) {
	if r.Ollama == nil {
		return
	}
	cfg, err := config.Load(r.L)
	if err != nil {
		r.out("skills: no suggestions (%v)", err)
		return
	}
	n, err := aliases.Suggest(ctx, r.L, cfg, r.Ollama)
	if err != nil {
		r.out("skills: no suggestions (%v)", err)
		return
	}
	if n > 0 {
		r.out("skills: %d spellings to review — v on any finished row", n)
	}
}

func (r *Runner) drain(ctx context.Context, offers map[int]state.Offer) error {
	for {
		id, ok := popQueue(r.L.Queue())
		if !ok {
			return nil
		}
		if r.stopping.Load() || ctx.Err() != nil {
			// Put it back: a stop must not silently swallow queued work.
			pushQueueFront(r.L.Queue(), id)
			return ErrStopped
		}
		r.out("── picked up queued offer #%d ──", id)
		sub := r.Opt
		sub.OnlyID = id
		saved := r.Opt
		r.Opt = sub
		err := r.pipeline(ctx, offers)
		r.Opt = saved
		if err != nil && !errors.Is(err, ErrStopped) {
			return err
		}
		if errors.Is(err, ErrStopped) {
			return err
		}
	}
}

func (r *Runner) pipeline(ctx context.Context, offers map[int]state.Offer) error {
	ids := sortedIDs(offers)

	if r.Opt.DryRun {
		p1 := r.phase1Candidates(ids, offers)
		r.out("=== DRY RUN ===")
		r.out("Phase 1 (to score):    %d offers", len(p1))
		r.out("Phase 2 (to evaluate): %d offers", len(r.phase2Candidates(ids, offers)))
		r.out("Phase 3 (to tailor):   %d offers", len(r.phase3Candidates(ids, offers)))
		return nil
	}

	r.out("=== snipe run ===")
	r.out("P1 gate: %.1f | P3 threshold: %.1f | parallel %d/%d/%d",
		r.Opt.P1Threshold, r.Opt.Threshold, r.Opt.ParallelScore, r.Opt.ParallelEval, r.Opt.ParallelPDF)

	if !r.Opt.SkipPhase1 {
		if err := r.runPhase(ctx, "phase1", r.Opt.ParallelScore, r.phase1Candidates(ids, offers), r.scoreOne); err != nil {
			return err
		}
	}

	// An `evaled` row whose report is missing is a corrupted row: a previous
	// run assigned a number and wrote the status but never wrote the file, or a
	// failed eval was back-filled as a success. Left alone it is unreachable —
	// Phase 3 has no report to find, a retry filter sees no failure status, and
	// the UI renders it pending forever. Reset it so it re-evaluates.
	r.reconcileOrphans()

	if !r.Opt.SkipPhase2 {
		if err := r.runPhase(ctx, "phase2", r.Opt.ParallelEval, r.phase2Candidates(ids, offers), r.evalOne); err != nil {
			return err
		}
	}
	if !r.Opt.SkipPhase3 {
		if err := r.runPhase(ctx, "phase3", r.Opt.ParallelPDF, r.phase3Candidates(ids, offers), r.pdfOne); err != nil {
			return err
		}
	}

	r.mergeTracker()

	r.summary()
	if r.stopping.Load() {
		return ErrStopped
	}
	return ctx.Err()
}

// runPhase fans work out across workers and stops admitting new offers the
// moment a stop is requested. errgroup.SetLimit is the whole worker pool; the
// bash version tracked a pids array and waited on it by hand.
func (r *Runner) runPhase(ctx context.Context, name string, workers int, ids []int, fn func(context.Context, int) error) error {
	if len(ids) == 0 {
		r.out("=== %s: nothing to do ===", name)
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	r.out("=== %s: %d offers, %d at a time ===", name, len(ids), workers)
	start := time.Now()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	var done, skipped atomic.Int64

	for _, id := range ids {
		if r.stopping.Load() || gctx.Err() != nil {
			skipped.Add(1)
			continue
		}
		g.Go(func() error {
			// Re-check inside the worker: the stop may have arrived while this
			// one was queued behind the limit.
			if r.stopping.Load() || gctx.Err() != nil {
				skipped.Add(1)
				return nil
			}
			if err := fn(gctx, id); err != nil {
				// One offer failing is not the run failing. The failure is
				// already recorded on its row; the rest keep going.
				r.out("  #%d %s: %v", id, name, err)
			}
			done.Add(1)
			return nil
		})
	}
	err := g.Wait()
	r.Log.Wall(name+"-wall", 0, time.Since(start))
	r.out("  ⏱  %s: %s (%d done, %d not started)", name, time.Since(start).Round(time.Second), done.Load(), skipped.Load())
	if err != nil {
		return err
	}
	if skipped.Load() > 0 && r.stopping.Load() {
		return ErrStopped
	}
	return nil
}

// ── offer selection ──────────────────────────────────────────────────────────

func sortedIDs(offers map[int]state.Offer) []int {
	ids := make([]int, 0, len(offers))
	for id := range offers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// eligible applies the filters every phase shares.
func (r *Runner) eligible(id int) (state.Row, bool) {
	if r.Opt.OnlyID != 0 && id != r.Opt.OnlyID {
		return state.Row{}, false
	}
	if id < r.Opt.StartFrom {
		return state.Row{}, false
	}
	row, _ := r.st.Get(id)
	// Never retry an unavailable posting: expired or blocked, it will not
	// recover, and re-fetching it every run is pure cost.
	if row.P1Status == state.P1Unavailable {
		return state.Row{}, false
	}
	if r.Opt.RetryFailed && state.FailedPhase(row) == "" {
		return state.Row{}, false
	}
	return row, true
}

func (r *Runner) phase1Candidates(ids []int, offers map[int]state.Offer) []int {
	var out []int
	for _, id := range ids {
		row, ok := r.eligible(id)
		if !ok {
			continue
		}
		if row.P1Status == state.P1Scored && !r.Opt.RetryFailed {
			continue
		}
		// A score that has exhausted its retries stays failed until someone
		// asks for it by name with --retry-failed.
		if row.P1Status == state.P1Failed && !r.Opt.RetryFailed && row.Retries >= r.Opt.MaxRetries {
			r.out("  -  #%d skipped (score_failed, %d/%d retries used)", id, row.Retries, r.Opt.MaxRetries)
			continue
		}
		out = append(out, id)
	}
	return out
}

func (r *Runner) phase2Candidates(ids []int, offers map[int]state.Offer) []int {
	var out []int
	for _, id := range ids {
		row, ok := r.eligible(id)
		if !ok || row.P1Status != state.P1Scored {
			continue
		}
		if row.P2Status == state.P2Evaled && !r.Opt.RetryFailed {
			continue
		}
		// The Phase 1 → 2 cost gate. Below it the offer never reaches the 30B,
		// and a tracker row is written directly — no model call for a decision
		// the pre-screen already made.
		if r.Opt.P1Threshold > 0 {
			if score, has := row.Score(); has && score < r.Opt.P1Threshold {
				r.gateOne(id, row, score)
				continue
			}
		}
		out = append(out, id)
	}
	return out
}

func (r *Runner) phase3Candidates(ids []int, offers map[int]state.Offer) []int {
	var out []int
	for _, id := range ids {
		row, ok := r.eligible(id)
		if !ok || row.P1Status != state.P1Scored || row.P2Status != state.P2Evaled {
			continue
		}
		if row.P3Status == state.P3Completed && !r.Opt.RetryFailed {
			continue
		}
		// Gate on the Phase 2 eval score — the authoritative, code-computed
		// value — falling back to the pre-screen only when the eval artifact
		// has no score.
		gate, ok := r.Ph.EvalScore(id)
		if !ok {
			gate, ok = row.Score()
		}
		if !ok {
			continue
		}
		if gate < r.Opt.Threshold {
			r.belowThreshold(id, row, gate)
			continue
		}
		out = append(out, id)
	}
	return out
}

func (r *Runner) gateOne(id int, row state.Row, score float64) {
	if row.P2Status == state.P2Gated {
		return // already recorded; do not rewrite the row every run
	}
	_ = r.st.Commit(id, func(x *state.Row) {
		x.P2Status = state.P2Gated
		x.P3Status = state.P3Skipped
		x.Error = fmt.Sprintf("p1-gated(threshold=%g)", r.Opt.P1Threshold)
	})
	r.out("  #%d skipped Phase 2 (P1 %.1f < %.1f)", id, score, r.Opt.P1Threshold)
}

func (r *Runner) belowThreshold(id int, row state.Row, score float64) {
	if row.P3Status == state.P3Skipped {
		return
	}
	_ = r.st.Commit(id, func(x *state.Row) {
		x.P3Status = state.P3Skipped
		x.Error = fmt.Sprintf("below-threshold(%g)", r.Opt.Threshold)
	})
	r.out("  #%d skipped Phase 3 (eval %.1f < %.1f)", id, score, r.Opt.Threshold)
}

// mergeTracker folds the rows Phase 3 wrote into data/applications.md, where
// local-runner.sh called merge-tracker.mjs: at the end of the run, unconditional.
//
// Unconditional because additions outlive the run that wrote them — an
// interrupted run leaves its rows pending, and only the next merge picks them
// up. A failure here is reported and not fatal: the CVs are on disk and the
// additions are still queued, so the next run merges them.
func (r *Runner) mergeTracker() {
	res, err := tracker.Merge(r.L.Tracker(), r.L.Additions())
	if err != nil {
		r.out("  WARN: tracker merge failed: %v", err)
		return
	}
	if res.Added+res.Updated+res.Skipped == 0 {
		return
	}
	r.out("")
	r.out("=== tracker: %d added, %d updated, %d skipped ===", res.Added, res.Updated, res.Skipped)
	if err := tracker.File(r.L.Additions(), res.Files); err != nil {
		r.out("  WARN: filing merged additions: %v", err)
	}
}

// reconcileOrphans resets rows marked evaled whose report is not on disk.
func (r *Runner) reconcileOrphans() {
	cache := state.NewCache()
	for id, row := range r.st.Rows() {
		if row.P2Status != state.P2Evaled {
			continue
		}
		if row.HasReport() {
			if _, ok := cache.ReportFile(r.L.Reports(), row.ReportNum); ok {
				continue
			}
		}
		why := "report missing after eval"
		if !row.HasReport() {
			why = state.NoReport
		}
		r.out("  WARN: #%d %s — will re-evaluate", id, why)
		_ = r.st.Commit(id, func(x *state.Row) {
			x.P2Status = state.P2Failed
			x.P3Status = ""
			x.Error = why
		})
	}
}

// ── per-offer phase bodies ───────────────────────────────────────────────────

func (r *Runner) checkpoint(id int) *state.Checkpoint {
	ck := state.LoadCheckpoint(r.L.Work(), id)
	// A deliberate retry starts clean: --retry-failed means "this attempt was
	// wrong", not "resume it".
	if r.Opt.RetryFailed {
		_ = ck.Discard()
	}
	return ck
}

// announce says where an offer picks up.
//
// "resuming at X" is the stage a pause dropped, about to run again; the stages
// named after it are the ones that do not. That distinction is the whole reason
// the checkpoint records a stage in flight — without it a paused offer and one
// that simply got that far read identically.
func (r *Runner) announce(phase string, id int, ck *state.Checkpoint) {
	done := ck.Done()
	switch {
	case ck.Interrupted() != "" && len(done) > 0:
		r.out("  [%s] #%d resuming at %s, keeping %s", phase, id, ck.Interrupted(), strings.Join(done, ", "))
	case ck.Interrupted() != "":
		r.out("  [%s] #%d resuming at %s", phase, id, ck.Interrupted())
	case len(done) > 0:
		r.out("  [%s] #%d resuming past %s", phase, id, strings.Join(done, ", "))
	default:
		r.out("  [%s] #%d", phase, id)
	}
}

func (r *Runner) scoreOne(ctx context.Context, id int) error {
	ck := r.checkpoint(id)
	r.announce("score", id, ck)
	if err := r.Ph.Score(ctx, id, ck); err != nil {
		return r.fail(id, "p1", err)
	}
	return nil
}

func (r *Runner) evalOne(ctx context.Context, id int) error {
	ck := r.checkpoint(id)
	r.announce("eval", id, ck)
	// The row is marked in flight BEFORE the call, not after it. An eval is a
	// minutes-long model call, and until this the row on disk still read exactly
	// as it had before the run started — so overruling the Phase 1 cost gate
	// left the queue wearing "?" and offering "proceed?" for the whole
	// evaluation, on an offer that was already being evaluated. P2Evaluating
	// was declared and read by the UI from the start; nothing ever wrote it.
	//
	// Read and written in one closure so the swap happens under the store's
	// lock, which is also the cheapest way to learn what the status was.
	var was string
	_ = r.st.Commit(id, func(x *state.Row) {
		was, x.P2Status = x.P2Status, state.P2Evaluating
	})
	if err := r.Ph.Eval(ctx, id, ck); err != nil {
		// A cancelled eval is a pause, not a failure — fail() says so by
		// recording nothing. Then this has to put the status back itself, or a
		// stopped override would leave a gated row stuck on a stage no process
		// is working on, with the question it was asking gone.
		if errors.Is(err, context.Canceled) {
			_ = r.st.Commit(id, func(x *state.Row) { x.P2Status = was })
		}
		return r.fail(id, "p2", err)
	}
	return nil
}

func (r *Runner) pdfOne(ctx context.Context, id int) error {
	ck := r.checkpoint(id)
	r.announce("pdf", id, ck)
	if err := r.Ph.PDF(ctx, id, ck); err != nil {
		return r.fail(id, "p3", err)
	}
	return nil
}

// fail records the failure on the row. A cancelled call is not a failure — it
// is a stop, and the checkpoint already holds everything that completed.
func (r *Runner) fail(id int, phase string, err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] // the column is read as a fallback; the artifact holds the whole thing
	}
	_ = r.st.Commit(id, func(x *state.Row) {
		switch phase {
		case "p1":
			x.P1Status = state.P1Failed
			x.Retries++
		case "p2":
			x.P2Status = state.P2Failed
		case "p3":
			x.P3Status = state.P3Failed
		}
		x.Error = msg
	})
	return err
}

func (r *Runner) summary() {
	rows := r.st.Rows()
	s := state.Lifetime(rows)
	var scored, unavailable, evaled int
	for _, row := range rows {
		switch row.P1Status {
		case state.P1Scored:
			scored++
		case state.P1Unavailable:
			unavailable++
		}
		if row.P2Status == state.P2Evaled {
			evaled++
		}
	}
	r.out("")
	r.out("=== summary ===")
	r.out("Phase 1: %d scored | %d unavailable", scored, unavailable)
	r.out("Phase 2: %d evaluated | %d gated", evaled, s.Gated)
	r.out("Phase 3: %d CVs | %d below threshold | %d failed", s.CV, s.Completed-s.CV, s.Failed)
}

// ── queue file ───────────────────────────────────────────────────────────────

// popQueue removes and returns the first queued id. Rewritten in place so
// anyone holding the file keeps a valid handle.
func popQueue(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i, line := range lines {
		id, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		rest := append(append([]string{}, lines[:i]...), lines[i+1:]...)
		out := strings.TrimLeft(strings.Join(rest, "\n"), "\n")
		if out != "" {
			out += "\n"
		}
		_ = os.WriteFile(path, []byte(out), 0o644)
		return id, true
	}
	return 0, false
}

func pushQueueFront(path string, id int) {
	b, _ := os.ReadFile(path)
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s", id, string(b))), 0o644)
}
