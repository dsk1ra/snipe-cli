package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// gateAtoms compares the CV atom split, which decides what retrieval can even
// see. An atom the splitter drops can never be matched to a requirement, and
// that reads as a Gap rather than as a bug.
func gateAtoms(root, jsFile string) int {
	l := paths.Layout{Home: root, Code: root}
	cv, err := os.ReadFile(l.CV())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	digest := embed.ReadFileOr(l.Digest())

	wantB, err := os.ReadFile(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	want := strings.Split(strings.TrimRight(string(wantB), "\n"), "\n")

	got := []string{}
	for _, a := range embed.ExtractAtoms(string(cv), digest) {
		got = append(got, a.Source+"\t"+a.Text)
	}

	diffs := 0
	for i := 0; i < len(want) || i < len(got); i++ {
		w, g := "", ""
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g {
			diffs++
			if diffs <= 10 {
				fmt.Printf("atom %d\n  go %q\n  js %q\n", i, g, w)
			}
		}
	}
	fmt.Printf("\natoms: %d go / %d js, %d differ", len(got), len(want), diffs)
	if diffs > 0 {
		fmt.Println(" — DIFFER")
		return 1
	}
	fmt.Println(" — GREEN")
	return 0
}
