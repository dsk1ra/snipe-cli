// Package paths resolves every file the pipeline reads or writes.
//
// Two roots: Home is the *data* root (state, queue, reports, output, tracker)
// and Code is where the prompts and templates live. SNIPE_HOME splits them so a
// test can build a whole fixture tree in a temp dir.
//
// batch/ was a flat drop of everything the pipeline touched, inherited from the
// Node implementation so both could read one tree during the port. It is split
// now that they no longer have to: batch/state/ is the record, batch/cache/ is
// derived, and the per-offer directories are their own thing.
package paths

import (
	"os"
	"path/filepath"
)

type Layout struct {
	Home string // data root
	Code string // repo root (prompts, templates)
}

// Default resolves the layout from the environment. code is the directory the
// binary considers its repo root; when empty it falls back to the working dir.
func Default(code string) Layout {
	if code == "" {
		code, _ = os.Getwd()
	}
	home := code
	if v := os.Getenv("SNIPE_HOME"); v != "" {
		if abs, err := filepath.Abs(v); err == nil {
			home = abs
		}
	}
	return Layout{Home: home, Code: code}
}

func (l Layout) Batch() string { return filepath.Join(l.Home, "batch") }

// StateDir and CacheDir split batch/ on the only line that matters at three in
// the morning: what you lose if it is gone.
//
// Everything in StateDir is the record — which offers exist, what happened to
// them, what you decided. Nothing rebuilds it. Everything in CacheDir is
// derived and can be deleted to force a rebuild, which is a real operation
// (a changed embedding model invalidates both indexes at once) and one that
// should not require picking files out of a directory by name.
func (l Layout) StateDir() string { return filepath.Join(l.Batch(), "state") }
func (l Layout) CacheDir() string { return filepath.Join(l.Batch(), "cache") }
func (l Layout) Reports() string  { return filepath.Join(l.Home, "reports") }
func (l Layout) Output() string   { return filepath.Join(l.Home, "output") }
func (l Layout) Data() string     { return filepath.Join(l.Home, "data") }

// The record.
func (l Layout) State() string    { return filepath.Join(l.StateDir(), "local-state.tsv") }
func (l Layout) Input() string    { return filepath.Join(l.StateDir(), "batch-input.tsv") }
func (l Layout) Queue() string    { return filepath.Join(l.StateDir(), "queue.txt") }
func (l Layout) Applied() string  { return filepath.Join(l.StateDir(), "applied.tsv") }
func (l Layout) Skipped() string  { return filepath.Join(l.StateDir(), "skipped.tsv") }
func (l Layout) Declined() string { return filepath.Join(l.StateDir(), "declined.tsv") }

// Lock lives with the state it guards. It is the only file in there that does
// not survive the process, which is the point: a lock found in a tree copied
// off another machine is a lie, and one found beside the state file is at least
// looking at the state it claims to hold.
func (l Layout) Lock() string { return filepath.Join(l.StateDir(), "runner.pid") }

// CVBank is in the state directory rather than the cache despite being derived
// from cv.md. Rebuilding it is six Opus calls, one per CV entry — cheap enough
// to redo deliberately, expensive enough that it must not be swept up by a
// "delete the caches" that is otherwise free.
func (l Layout) CVBank() string { return filepath.Join(l.StateDir(), "cv-bank.json") }

// Derived, and cheap enough to throw away.
func (l Layout) CVIndex() string    { return filepath.Join(l.CacheDir(), "cv-index.json") }
func (l Layout) JDIndex() string    { return filepath.Join(l.CacheDir(), "jd-index.json") }
func (l Layout) Spike() string      { return filepath.Join(l.CacheDir(), "cv-spike.json") }
func (l Layout) JudgeShots() string { return filepath.Join(l.CacheDir(), "judge-shots.json") }
func (l Layout) Timings() string    { return filepath.Join(l.CacheDir(), "timings.tsv") }

// Per-offer artifacts, one file per offer id.
func (l Layout) Scores() string     { return filepath.Join(l.Batch(), "scores") }
func (l Layout) Evals() string      { return filepath.Join(l.Batch(), "evals") }
func (l Layout) Logs() string       { return filepath.Join(l.Batch(), "logs") }
func (l Layout) Errors() string     { return filepath.Join(l.Batch(), "errors") }
func (l Layout) JDs() string        { return filepath.Join(l.Batch(), "jds") }
func (l Layout) Work() string       { return filepath.Join(l.Batch(), "work") }
func (l Layout) CV() string         { return filepath.Join(l.Home, "cv.md") }
func (l Layout) Digest() string     { return filepath.Join(l.Home, "article-digest.md") }
func (l Layout) ProfileYML() string { return filepath.Join(l.Home, "config", "profile.yml") }
func (l Layout) ProfileMD() string  { return filepath.Join(l.Home, "config", "profile.md") }
func (l Layout) Followups() string  { return filepath.Join(l.Data(), "follow-ups.md") }

// ScanHistory is the scanner's long memory: every URL it has ever kept, which
// outlives that URL's row in batch-input.tsv. It sits with the data, not the
// cache — losing it means re-offering hundreds of postings already judged.
func (l Layout) ScanHistory() string { return filepath.Join(l.Data(), "scan-history.tsv") }

// Additions is batch/tracker-additions/, overridable so a fixture TSV cannot be
// merged into the real tracker by the next run.
func (l Layout) Additions() string {
	if v := os.Getenv("SNIPE_ADDITIONS"); v != "" {
		return v
	}
	return filepath.Join(l.Batch(), "tracker-additions")
}

// Tracker honours SNIPE_TRACKER so the TUI and everything it shells out to
// cannot disagree about which file is the tracker.
func (l Layout) Tracker() string {
	if v := os.Getenv("SNIPE_TRACKER"); v != "" {
		return v
	}
	return filepath.Join(l.Data(), "applications.md")
}

// Portals is the scanner's config. It lives with the data, not the code: it is
// the user's list of companies and boards, edited far more often than the
// prompts beside it.
func (l Layout) Portals() string { return filepath.Join(l.Home, "portals.yml") }

// Prompts and templates live with the code, never with the data.
func (l Layout) Prompt(name string) string   { return filepath.Join(l.Code, "prompts", name) }
func (l Layout) Template(name string) string { return filepath.Join(l.Code, "templates", name) }
