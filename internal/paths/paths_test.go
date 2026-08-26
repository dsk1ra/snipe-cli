package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

// The split exists so that "delete the derived caches" is one directory rather
// than a list of filenames to get right. That is only safe if nothing the
// pipeline cannot rebuild lives under it.
func TestNothingIrreplaceableLivesInTheCache(t *testing.T) {
	l := Layout{Home: "/home", Code: "/code"}

	record := map[string]string{
		"state":    l.State(),
		"input":    l.Input(),
		"queue":    l.Queue(),
		"applied":  l.Applied(),
		"skipped":  l.Skipped(),
		"declined": l.Declined(),
		"lock":     l.Lock(),
		// Derived from cv.md, but at six Opus calls a rebuild. Cheap enough to
		// redo deliberately, expensive enough that it must not be swept up by a
		// cache clear that is otherwise free.
		"cv-bank": l.CVBank(),
	}
	for name, p := range record {
		if strings.HasPrefix(p, l.CacheDir()+string(filepath.Separator)) {
			t.Errorf("%s is in the cache directory: %s", name, p)
		}
		if !strings.HasPrefix(p, l.StateDir()+string(filepath.Separator)) {
			t.Errorf("%s is not in the state directory: %s", name, p)
		}
	}

	derived := map[string]string{
		"cv-index":    l.CVIndex(),
		"jd-index":    l.JDIndex(),
		"spike":       l.Spike(),
		"judge-shots": l.JudgeShots(),
		"timings":     l.Timings(),
	}
	for name, p := range derived {
		if !strings.HasPrefix(p, l.CacheDir()+string(filepath.Separator)) {
			t.Errorf("%s is not in the cache directory: %s", name, p)
		}
	}
}

// SNIPE_HOME splits the data root off the code root; the prompts and templates
// must not follow it, or a fixture tree in /tmp takes the prompts with it.
func TestCodePathsIgnoreTheDataRoot(t *testing.T) {
	l := Layout{Home: "/data", Code: "/code"}
	for _, p := range []string{l.Prompt("x.md"), l.Template("y.tex")} {
		if !strings.HasPrefix(p, "/code/") {
			t.Errorf("%s is not under the code root", p)
		}
	}
	for _, p := range []string{l.State(), l.CV(), l.Reports(), l.Output()} {
		if !strings.HasPrefix(p, "/data/") {
			t.Errorf("%s is not under the data root", p)
		}
	}
}
