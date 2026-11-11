package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

// goldsetGrade runs the production stage 2 (retrieval and the 30B's grade)
// over the sheet's requirements and scores the line each row cites against the
// ticks. `goldset score` stops at retrieval; this asks whether the evidence
// the report prints is evidence a person agrees with.
//
// A requirement with a tick is one where the human found evidence in the pool,
// so a Gap there is a false gap, split by whether a ticked line was among the
// three the grader saw. A cited line the sheet never showed is counted apart:
// nobody labelled it, so it is neither right nor wrong.
//
// A requirement with no tick is "nothing here is evidence" or "not reviewed",
// and the sheet cannot say which, so those are reported and kept out of the
// headline.
func goldsetGrade(home, sheet string) int {
	// The prompt under test is the checkout's, as in gate typed.
	code := home
	if wd, err := os.Getwd(); err == nil {
		if _, err := os.Stat(filepath.Join(wd, "prompts", "p2-evidence.md")); err == nil {
			code = wd
		}
	}
	l := paths.Layout{Home: home, Code: code}
	cfg, err := config.Load(l)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ol := gateClient()
	ix := &embed.Indexer{OL: ol, CVPath: l.CVIndex(), JDPath: l.JDIndex(), JDsDir: l.JDs()}
	index, err := ix.LoadCV(context.Background(), embed.ExtractAtoms(cfg.CVRaw, embed.ReadFileOr(l.Digest())))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cases, err := parseGoldsheet(sheet, len(index))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	atomID := map[string]int{}
	for i, a := range index {
		atomID[a.Text] = i
	}
	ev := &phase2.Evaluator{L: l, Cfg: cfg, OL: ol, Model: ollama.ModelEval, NumCtx: 8192, Timeout: 5 * time.Minute}

	var ticked, shown, cited, wrong, unlabelled, gap, gapShown, blank, blankGap int
	// A wrong line is split two ways: whether the grader had a ticked line in
	// front of it, and whether what it cited instead was a Skills or Education
	// line, which the sheet's rules leave unticked wherever a bullet shows the work.
	var wrongShown, wrongShownCat, wrongUnshownCat int
	start := time.Now()
	for i := 0; i < len(cases); {
		j := i
		for j < len(cases) && cases[j].report == cases[i].report {
			j++
		}
		group := cases[i:j]
		i = j

		// The grader is told MUST or nice-to-have, and the sheet does not carry
		// it. The report the requirements came from does.
		must := map[string]bool{}
		if b, err := os.ReadFile(filepath.Join(l.Reports(), group[0].report)); err == nil {
			for _, r := range cvselect.ExtractBlockB(string(b)) {
				must[r.Requirement] = r.MustHave
			}
		}
		reqs := make([]phase2.Requirement, len(group))
		for k, c := range group {
			reqs[k] = phase2.Requirement{Text: c.req, MustHave: must[c.req]}
		}
		rows, err := ev.GradeRows(context.Background(), 0, reqs, index)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", group[0].report, err)
			return 1
		}
		fmt.Printf("\n%s\n", group[0].report)
		for k, c := range group {
			row := rows[k]
			inPool := map[int]bool{}
			for _, id := range c.pool {
				inPool[id] = true
			}
			sawTicked := false
			for _, t := range row.Shown {
				if id, ok := atomID[t]; ok && c.want[id] {
					sawTicked = true
				}
			}
			pick, picked := atomID[row.Picked]
			mark := ""
			switch {
			case len(c.want) == 0:
				blank++
				mark = "blank, cited"
				if row.Strength == fit.Gap {
					blankGap++
					mark = "blank, gap"
				}
			case row.Strength == fit.Gap:
				ticked++
				gap++
				mark = "FALSE GAP, not shown"
				if sawTicked {
					gapShown++
					mark = "FALSE GAP, shown"
				}
			case picked && c.want[pick]:
				ticked++
				cited++
				mark = "ok"
			case picked && !inPool[pick]:
				ticked++
				unlabelled++
				mark = "unlabelled line"
			default:
				ticked++
				wrong++
				cat := picked && fit.Catalogue(index[pick].Source)
				mark = "WRONG LINE"
				if cat {
					mark = "WRONG LINE, catalogue"
				}
				switch {
				case sawTicked && cat:
					wrongShown++
					wrongShownCat++
				case sawTicked:
					wrongShown++
				case cat:
					wrongUnshownCat++
				}
			}
			if len(c.want) > 0 && sawTicked {
				shown++
			}
			fmt.Printf("  R%d %-12s %-22s %s\n", k+1, row.Strength, mark, clipTo(c.req, 70))
			// A miss is only useful with the lines in front of you: what it
			// cited, and what the sheet wanted and whether the grader saw it.
			if mark != "ok" && mark != "blank, gap" {
				if row.Picked != "" {
					fmt.Printf("       cited   %s\n", clipTo(row.Picked, 110))
				}
				for id := range index {
					if c.want[id] {
						seen := "not shown"
						for _, t := range row.Shown {
							if t == index[id].Text {
								seen = "shown"
							}
						}
						fmt.Printf("       ticked  %s (%s)\n", clipTo(index[id].Text, 110), seen)
					}
				}
			}
		}
	}

	if ticked == 0 {
		fmt.Fprintln(os.Stderr, "sheet has no ticks yet")
		return 2
	}
	share := func(n int) string { return fmt.Sprintf("%3d  %.3f", n, float64(n)/float64(ticked)) }
	fmt.Printf("\ngate goldset grade: %s, %d ticked requirements, %d blank, %.0f s\n\n", filepath.Base(sheet), ticked, blank, time.Since(start).Seconds())
	fmt.Printf("  ticked line shown to the grader   %s\n", share(shown))
	fmt.Printf("  cited a ticked line               %s\n", share(cited))
	fmt.Printf("  cited an unticked line            %s\n", share(wrong))
	fmt.Printf("    with a ticked line shown        %3d         (%d cited a Skills or Education line)\n", wrongShown, wrongShownCat)
	fmt.Printf("    with none shown                 %3d         (%d cited a Skills or Education line)\n", wrong-wrongShown, wrongUnshownCat)
	fmt.Printf("  cited a line the sheet never had  %s\n", share(unlabelled))
	fmt.Printf("  false gap                         %s   (%d with a ticked line shown)\n", share(gap), gapShown)
	if cited+wrong > 0 {
		fmt.Printf("  precision of cited lines          %.3f  (%d/%d labelled)\n", float64(cited)/float64(cited+wrong), cited, cited+wrong)
	}
	if blank > 0 {
		fmt.Printf("  blank requirements graded Gap     %d/%d\n", blankGap, blank)
	}
	return 0
}

func clipTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
