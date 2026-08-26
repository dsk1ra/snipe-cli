package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// The gate rests on one property of how this tree is written: a commit is a
// rename INTO a directory, and a rename moves that directory's own mtime. Where
// that is not true — the two files appended in place — the file is watched by
// name instead. Both halves are asserted here, because if the first ever stops
// holding the TUI goes blind, and if the second ever starts holding two watches
// can be deleted.
func TestWatcherSeesEveryWayTheTreeMoves(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	for _, d := range []string{l.StateDir(), l.Evals(), l.JDs(), l.Scores(), l.Work(), l.Data()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(l.Tracker(), []byte("# Applications Tracker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newWatcher()
	// settle backdates every watched path out of the recency window, so what the
	// next call answers is the change under test and not the clock.
	settle := func() {
		old := time.Now().Add(-time.Hour)
		for _, p := range watched(l) {
			_ = os.Chtimes(p, old, old)
		}
		w.moved(l, false)
		if w.moved(l, false) {
			t.Fatal("a settled tree still reads as moved")
		}
	}

	if !w.moved(l, false) {
		t.Fatal("the first poll has nothing to compare against and must rebuild")
	}
	settle()

	t.Run("a commit renames into its directory, which moves the directory", func(t *testing.T) {
		before := mtime(t, l.StateDir())
		if err := state.Save(l.State(), map[int]state.Row{1: {ID: 1, URL: "u"}}); err != nil {
			t.Fatal(err)
		}
		if mtime(t, l.StateDir()).Equal(before) {
			t.Fatal("the state directory did not move — one stat no longer covers the files in it")
		}
		if !w.moved(l, false) {
			t.Error("the watcher missed a committed state file")
		}
		settle()
	})

	t.Run("an append moves the file and not its directory", func(t *testing.T) {
		// The FIRST append creates the file, and a new directory entry does move
		// the directory. It is the second one — a write into a name that already
		// exists — that the directory cannot see, and that is the ordinary case:
		// batch-input.tsv is appended to for the life of the tree.
		if err := state.AppendOffer(l.Input(), state.Offer{ID: 2, URL: "https://x.test/j"}); err != nil {
			t.Fatal(err)
		}
		settle()

		before := mtime(t, l.StateDir())
		if err := state.AppendOffer(l.Input(), state.Offer{ID: 3, URL: "https://y.test/j"}); err != nil {
			t.Fatal(err)
		}
		if !mtime(t, l.StateDir()).Equal(before) {
			t.Error("O_APPEND moved the directory after all — batch-input.tsv need not be watched by name")
		}
		if !w.moved(l, false) {
			t.Error("the watcher missed an appended offer — this is why the file is watched by name")
		}
		settle()
	})

	t.Run("the tracker is edited in place, by us and by hand", func(t *testing.T) {
		if err := os.WriteFile(l.Tracker(), []byte("# Applications Tracker\n\n| 1 |\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !w.moved(l, false) {
			t.Error("the watcher missed a tracker edit — the follow-up board would not have reloaded")
		}
		settle()
	})

	t.Run("a run that dies touches nothing, so the lock is asked separately", func(t *testing.T) {
		// A killed runner has its flock dropped by the kernel. No file changes,
		// so nothing the gate stats can tell — which is why `running` is an
		// argument rather than something moved() works out for itself.
		if !w.moved(l, true) {
			t.Error("the watcher ignored a run starting")
		}
		w.moved(l, true)
		if !w.moved(l, false) {
			t.Error("the watcher ignored a run ending")
		}
	})
}

// A write is invisible to the gate if the filesystem gives it an mtime that was
// already read. The recency window is what covers that, so it has to actually
// fire.
func TestWatcherDistrustsAFreshMtime(t *testing.T) {
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: home}
	if err := os.MkdirAll(l.StateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	w := newWatcher()
	w.moved(l, false)
	if !w.moved(l, false) {
		t.Error("a directory written a moment ago must be re-read: its mtime may be the one already seen")
	}
}

func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", filepath.Base(path), err)
	}
	return st.ModTime()
}
