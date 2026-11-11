// Command gate measures the pipeline over the real cached corpus: evidence
// selection against hand labels (goldset), the skills block (skills), typed
// stage 1 (typed), the tailored page (tailor, ats) and the fact checker (e2).
// Each needs a home with cached JDs and a real cv.md, so none of them run in CI.
//
//	go run ./cmd/gate skills <home> [--sweep]
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gate <goldset|skills|typed|tailor|ats|judge> <home> ...\n       gate e2 <home> [--checker minicheck|eval] [--split whole|clause] [--n 150] [--out report.json]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "goldset":
		os.Exit(gateGoldset(os.Args[2:]))
	case "typed":
		os.Exit(gateTyped(os.Args[2:]))
	case "tailor":
		os.Exit(gateTailor(os.Args[2:]))
	case "judge":
		os.Exit(gateJudge(os.Args[2:]))
	case "ats":
		os.Exit(gateATS(os.Args[2:]))
	case "e2":
		os.Exit(gateE2(os.Args[2:]))
	case "skills":
		os.Exit(gateSkills(os.Args[2], len(os.Args) > 3 && os.Args[3] == "--sweep"))
	default:
		fmt.Fprintf(os.Stderr, "unknown gate %q\n", os.Args[1])
		os.Exit(2)
	}
}
