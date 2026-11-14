package tui

import (
	"strings"
	"testing"
)

func TestWordDiffMarksOnlyWhatChanged(t *testing.T) {
	old := "Built retail operations as 10 Java services covering orders"
	new := "Designed 10 event-driven Java services covering orders"
	was, now := wordDiff(old, new)
	// Stripped of styling each side reads as its own line.
	if stripANSI(was) != old || stripANSI(now) != new {
		t.Fatalf("text changed:\n%q\n%q", stripANSI(was), stripANSI(now))
	}
	// Words both lines share are not drawn as changes: the plain run "10 Java services" survives intact in both.
	for _, s := range []string{was, now} {
		if !strings.Contains(s, "services covering orders") {
			t.Errorf("a shared run was split: %q", s)
		}
	}
	if w, n := wordDiff("same line", "same line"); stripANSI(w) != "same line" || w != n {
		t.Errorf("identical lines: %q %q", w, n)
	}
}
