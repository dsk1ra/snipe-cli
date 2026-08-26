package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dsk1ra/snipe-cli/internal/latex"
)

// gateTex compares LaTeX escaping and metric bolding over every real CV bullet
// plus a battery of edge cases. It is the highest-risk transcription in the
// template filler: an escaping slip produces a document that compiles and reads
// wrong, and a metric-regex slip silently stops bolding the numbers the page
// exists to show.
func gateTex(jsFile string) int {
	f, err := os.Open(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n, diffs := 0, 0
	for sc.Scan() {
		var row []string
		if json.Unmarshal(sc.Bytes(), &row) != nil || len(row) < 2 {
			continue
		}
		n++
		if row[0] == "URL" {
			if got := latex.EscapeURL(`https://x.test/a%b#c\d?e=1&f=~g`); got != row[1] {
				diffs++
				fmt.Printf("  URL\n    go %q\n    js %q\n", got, row[1])
			}
			continue
		}
		if got := latex.Bold(row[0]); got != row[1] {
			diffs++
			if diffs <= 10 {
				fmt.Printf("  Bold(%q)\n    go %q\n    js %q\n", trim(row[0], 60), got, row[1])
			}
		}
		if len(row) > 2 {
			if got := latex.Escape(row[0]); got != row[2] {
				diffs++
				if diffs <= 10 {
					fmt.Printf("  Escape(%q)\n    go %q\n    js %q\n", trim(row[0], 60), got, row[2])
				}
			}
		}
	}
	fmt.Printf("\ntex: %d cases, %d differ", n, diffs)
	if diffs > 0 {
		fmt.Println(" — DIFFER")
		return 1
	}
	fmt.Println(" — GREEN")
	return 0
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
