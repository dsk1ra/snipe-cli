package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// gateFit compares every deterministic cap against the Node implementation over
// the whole cached JD corpus. These are the hard-won constants the port must not
// drift on, and they have no unit test on either side.
func gateFit(root, jsFile string) int {
	cv, err := os.ReadFile(filepath.Join(root, "cv.md"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	prof, err := os.ReadFile(filepath.Join(root, "config", "profile.yml"))
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
	fmt.Fprintln(os.Stderr, "commutable="+strings.Join(fit.CommutableTerms(string(prof)), ","))

	want, err := readTSV(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cache := state.NewCache()
	files := sortedKeys(want)

	var diffs int
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(root, "batch", "jds", f))
		if err != nil {
			continue
		}
		jd := string(b)

		title := ""
		id := strings.TrimSuffix(f, ".txt")
		if n := atoi(id); n > 0 {
			if a := cache.Score(filepath.Join(root, "batch", "scores"), n); a != nil {
				title = a.Role
			}
		}

		sm := fit.StackMismatchCap(jd, string(cv), 0, 0)
		sc := fit.SeniorityCaps(title, jd, 0, "")
		lm, missing := fit.LanguageMismatchCap(jd, string(cv))
		lo, city := fit.LocationMismatchCap(jd, string(prof))

		got := strings.Join([]string{
			fmt.Sprintf("stack=%d|%s|%s", sm.Cap, strings.Join(sm.JDStack, "+"), strings.Join(sm.Missing, "+")),
			fmt.Sprintf("sen=%d/%d|%s", sc.CV, sc.NS, sc.Reason),
			fmt.Sprintf("lang=%d/%d|%s", lm.CV, lm.NS, missing),
			fmt.Sprintf("loc=%d/%d|%s", lo.CV, lo.NS, city),
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
