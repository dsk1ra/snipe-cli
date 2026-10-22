package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// gateFit compares every deterministic cap against the Node implementation over
// the whole cached JD corpus. These are the hard-won constants the port must not
// drift on, and they have no unit test on either side.
func gateFit(root, jsFile string) int {
	l := paths.Layout{Home: root, Code: root}
	cv, err := os.ReadFile(l.CV())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	eco := fit.CandidateEcosystems(string(cv))
	names := make([]string, 0, len(eco))
	for n := range eco {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "ecosystems="+strings.Join(names, ","))

	want, err := readTSV(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// Seniority, language and location moved off word lists to extracted facts
	// and are no longer compared with the Node output; the stack table and the
	// multi-posting rule still are.
	for f, line := range want {
		var keep []string
		for _, col := range strings.Split(line, "\t") {
			if strings.HasPrefix(col, "stack=") || strings.HasPrefix(col, "multi=") {
				keep = append(keep, col)
			}
		}
		want[f] = strings.Join(keep, "\t")
	}
	files := sortedKeys(want)

	var diffs int
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(l.JDs(), f))
		if err != nil {
			continue
		}
		jd := string(b)

		sm := fit.StackMismatchCap(jd, string(cv), 0, 0)
		got := strings.Join([]string{
			fmt.Sprintf("stack=%d|%s|%s", sm.Cap, strings.Join(sm.JDStack, "+"), strings.Join(sm.Missing, "+")),
			fmt.Sprintf("multi=%t", fit.LooksMultiPosting(jd)),
		}, "\t")

		if got != want[f] {
			diffs++
			if diffs <= 15 {
				fmt.Printf("%-9s\n  go %s\n  js %s\n", f, got, want[f])
			}
		}
	}
	fmt.Printf("\nfit: %d/%d identical", len(files)-diffs, len(files))
	if diffs > 0 {
		fmt.Printf(" — %d DIFFER\n", diffs)
		return 1
	}
	fmt.Println(" — GREEN")
	return 0
}
