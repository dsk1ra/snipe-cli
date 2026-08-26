package tui

import (
	"os"
	"sync"
	"time"

	"charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// pollInterval is how often the TUI re-reads disk. The pipeline moves in tens
// of seconds, so this is about feeling live, not about resolution.
const pollInterval = time.Second

// frameInterval is how often the animation advances, which is NOT how often the
// disk is read. They used to be the same number, and a spinner stepping once a
// second does not read as motion — it reads as a glyph that keeps changing.
// Re-reading a few hundred artifacts ten times a second to fix that would be
// paying for the wrong thing, so the tick is fast and the poll rides every
// framesPerPoll of them.
const frameInterval = 100 * time.Millisecond

// framesPerPoll is how many fast ticks make one poll. Exact by construction:
// keep the two intervals a whole multiple apart.
const framesPerPoll = int(pollInterval / frameInterval)

// snapshot is everything the UI reads off disk, taken in one pass so no two
// panes can render inconsistent halves of the same tick.
type snapshot struct {
	rows    map[int]state.Row
	offers  map[int]state.Offer
	queue   []int
	session []int // ids to render, chronological (ids are sequential)
	// days is the day each offer first appeared, keyed by id. It is what the
	// list groups under, and it comes from the same scan that built session —
	// the mtimes were already read to find the ids.
	days     map[int]time.Time
	applied  state.Marks
	skipped  state.Marks
	declined state.Marks
	labels   map[int]string
	// scores is the eval score per offer. Absent means "no eval landed"; a real
	// score is 1–5, so a zero value cannot be mistaken for one.
	scores map[int]float64
	// paused is the work a stopped run left mid-stage. Read only when nothing
	// holds the lock: while a run is active a stage in flight is just a stage in
	// flight, and the scan is ~7 ms over a few hundred checkpoints — free on an
	// idle machine, not worth paying every second on a busy one.
	paused  []state.Work
	stats   state.Stats
	avg     float64
	avgOK   bool
	running bool
	pid     int
	err     error
}

type snapshotMsg snapshot

// watcher answers one question before a poll does any work: has anything the
// snapshot reads actually moved?
//
// Almost every poll is over a tree nobody has touched, and the answer costs nine
// stats rather than a few hundred lstats and two full parses — 0.01 ms against
// 5–7 ms, measured over ~650 offers.
//
// It works because of how this tree is written. Every state file is committed
// through WriteAtomic — a temp file and a rename INTO its directory — and a
// rename moves that directory's own mtime, so one stat covers every file in it.
// The exceptions are the append-only ones: batch-input.tsv and queue.txt are
// written with O_APPEND, which moves the file's mtime and not the directory's,
// so they are watched by name. The two tracker files are watched by name for the
// same reason — they are edited in place, by the runner and by hand.
type watcher struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	running bool
	primed  bool
}

func newWatcher() *watcher { return &watcher{seen: map[string]time.Time{}} }

// watched is everything a poll would read, named at the coarsest level that
// still catches every write to it.
func watched(l paths.Layout) []string {
	return []string{
		l.StateDir(), l.Evals(), l.JDs(), l.Scores(), l.Work(),
		l.Input(), l.Queue(), // appended in place: their directory does not move
		l.Tracker(), l.Followups(),
	}
}

// moved reports whether to rebuild, recording what it saw.
//
// running is passed in rather than probed here because the caller needs it
// either way — and it is the one input with no filesystem event behind it: a
// runner that is killed releases its flock without touching a single file.
func (w *watcher) moved(l paths.Layout, running bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	changed := !w.primed || running != w.running
	w.primed, w.running = true, running

	now := time.Now()
	for _, p := range watched(l) {
		var t time.Time
		if st, err := os.Stat(p); err == nil {
			t = st.ModTime()
		}
		if !t.Equal(w.seen[p]) {
			changed = true
		}
		// A filesystem clock is coarse: Linux stamps mtimes from a timer tick,
		// so a write landing microseconds after this stat can be given the mtime
		// that was just read and then never be seen again. Anything stamped
		// within the last couple of polls counts as moving. It costs nothing —
		// during a run everything is recent and the rebuild is wanted anyway.
		if !t.IsZero() && now.Sub(t) < 2*pollInterval {
			changed = true
		}
		w.seen[p] = t
	}
	return changed
}

// tickMsg drives the spinner and the message-expiry sweep between polls.
type tickMsg time.Time

func pollCmd(l paths.Layout, c *state.Cache, w *watcher) tea.Cmd {
	return func() tea.Msg {
		// The lock is probed first because the answer is needed either way, and
		// because it is what the gate cannot see: nothing on disk changes when a
		// runner dies and the kernel drops its flock.
		running := state.Active(l.Lock())
		if !w.moved(l, running) {
			// Nothing has moved, so the snapshot the tabs are holding is still
			// true. Sending no message at all is the whole saving.
			return nil
		}
		s := snapshot{
			applied:  state.LoadMarks(l.Applied()),
			skipped:  state.LoadMarks(l.Skipped()),
			declined: state.LoadMarks(l.Declined()),
			queue:    state.Queue(l.Queue()),
			labels:   map[int]string{},
			scores:   map[int]float64{},
			running:  running,
		}
		s.session, s.days = state.Timeline(l.JDs(), l.Evals(), l.Queue())
		if pid, ok := state.HolderPID(l.Lock()); ok {
			s.pid = pid
		}
		rows, err := state.Load(l.State())
		if err != nil {
			// A corrupt state file is worth saying out loud rather than
			// rendering as an empty queue, which reads as "nothing to do".
			s.err = err
			rows = map[int]state.Row{}
		}
		s.rows = rows
		offers, err := state.LoadOffers(l.Input())
		if err != nil && s.err == nil {
			s.err = err
		}
		s.offers = offers
		if !s.running {
			s.paused = state.Interrupted(l.Work())
		}
		s.stats = state.Lifetime(rows)
		s.avg, s.avgOK = c.LifetimeAvg(l.Evals())
		for _, id := range s.session {
			s.labels[id] = c.Label(l.Scores(), l.Evals(), id)
			// Same artifact the label came from — already read, already cached.
			if a := c.Eval(l.Evals(), id); a != nil && a.Score > 0 {
				s.scores[id] = a.Score
			}
		}
		return snapshotMsg(s)
	}
}

// tickCmd schedules the next frame. It runs at the animation rate only while
// something is actually animating: a screen with nothing moving on it does not
// need redrawing ten times a second, and an idle TUI left open all day is the
// normal case for this one.
func tickCmd(animating bool) tea.Cmd {
	d := pollInterval
	if animating {
		d = frameInterval
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}
