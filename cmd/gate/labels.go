package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/run"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// gate labels scores Phase 1 and Phase 2 against what the user said about the
// postings. It stands in for the Node rho.mjs, which left with the oracle.
//
//	gate labels <home> [--evals dir] [--scores dir]
//
// --evals and --scores point at another run's artifacts, so an arm made in its
// own home is scored against the same labels.
//
// There are three references and none is complete:
//
//   - batch/labels/labels-rev.tsv, 18 offers rated 1-5. The ratings take three
//     values, so one label off by one moves rho about 0.30
//     (batch/labels/LEDGER.md). Read pair accuracy.
//   - batch/state/{applied,skipped}.tsv. The user applied after seeing the score,
//     so these favour whichever scorer was live. A guard, not a target.
//   - Phase 2 as the reference for Phase 1, over every offer both scored. Phase 2
//     has its own false gaps (batch/PHASE1-EXPERIMENT-LEDGER.md), so recall_good
//     is the half to trust.
func gateLabels(args []string) int {
	var pos []string
	evalsDir, scoresDir := "", ""
	for i := 0; i < len(args); i++ {
		if v, ok := optVal(args, &i, "--evals"); ok {
			evalsDir = v
		} else if v, ok := optVal(args, &i, "--scores"); ok {
			scoresDir = v
		} else {
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gate labels <home> [--evals dir] [--scores dir]")
		return 2
	}
	l := paths.Layout{Home: pos[0], Code: pos[0]}
	if evalsDir == "" {
		evalsDir = l.Evals()
	}
	if scoresDir == "" {
		scoresDir = l.Scores()
	}
	labels, order, err := readLabels(filepath.Join(l.Batch(), "labels", "labels-rev.tsv"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	p2 := readArtifacts(evalsDir, state.P2Evaled)
	p1 := readArtifacts(scoresDir, state.P1Scored)

	fmt.Printf("gate labels: %d labelled offers\n\n", len(labels))
	fmt.Printf("  %-24s %3s  %6s  %-16s %5s\n", "", "n", "rho", "pair accuracy", "MAE")
	field := func(name string, arts map[int]artifact, get func(artifact) float64) {
		pred := map[int]float64{}
		for id := range labels {
			if a, ok := arts[id]; ok {
				pred[id] = get(a)
			}
		}
		c, n := pairAccuracy(pred, labels)
		fmt.Printf("  %-24s %3d  %6.3f  %5.1f%% (%g/%g)  %5.2f\n", name, len(pred), spearman(pred, labels), 100*c/n, c, n, mae(pred, labels))
	}
	field("phase 2 score", p2, func(a artifact) float64 { return a.Score })
	field("  cv_blended", p2, func(a artifact) float64 { return a.CVBlended })
	field("  cv_coverage", p2, func(a artifact) float64 { return a.CVCoverage })
	field("  cv_match", p2, func(a artifact) float64 { return float64(a.CVMatch) })
	field("phase 1 score", p1, func(a artifact) float64 { return a.Score })

	fmt.Printf("\n  %4s %5s  %4s  %4s  %8s %8s\n", "id", "label", "p1", "p2", "cv_match", "coverage")
	for _, id := range order {
		fmt.Printf("  %4d %5g  %4s  %4s  %8d %8.3f\n", id, labels[id], scoreOr(p1, id), scoreOr(p2, id), p2[id].CVMatch, p2[id].CVCoverage)
	}

	// Phase 1 is a cost gate, so what matters is which side of it an offer lands.
	gate := run.DefaultOptions().P1Threshold
	a, b := map[int]float64{}, map[int]float64{}
	good, goodKept, bad, badCut := 0, 0, 0, 0
	for id, s := range p1 {
		e, ok := p2[id]
		if !ok {
			continue
		}
		a[id], b[id] = s.Score, e.Score
		if e.Score >= 4 {
			good++
			if s.Score >= gate {
				goodKept++
			}
		}
		if e.Score < 3 {
			bad++
			if s.Score < gate {
				badCut++
			}
		}
	}
	if len(a) > 0 {
		c, n := pairAccuracy(a, b)
		fmt.Printf("\nphase 1 against phase 2, %d offers scored by both, gate %.1f\n", len(a), gate)
		fmt.Printf("  pair accuracy %.3f   recall_good %d/%d (%.3f)   drop_bad %d/%d (%.3f)\n",
			c/n, goodKept, good, ratio(goodKept, good), badCut, bad, ratio(badCut, bad))
	}

	applied, skipped := state.LoadMarks(l.Applied()), state.LoadMarks(l.Skipped())
	if len(applied) > 0 {
		var gated []string
		var pos, neg []float64
		for id := range applied {
			if s, ok := p1[id]; ok && s.Score < gate {
				gated = append(gated, "#"+strconv.Itoa(id))
			}
			if e, ok := p2[id]; ok {
				pos = append(pos, e.Score)
			}
		}
		for id := range skipped {
			if e, ok := p2[id]; ok {
				neg = append(neg, e.Score)
			}
		}
		sort.Strings(gated)
		fmt.Printf("\ndecisions: %d applied, %d skipped\n", len(applied), len(skipped))
		fmt.Printf("  applied offers phase 1 would gate: %d %s\n", len(gated), strings.Join(gated, " "))
		if len(pos) > 0 && len(neg) > 0 {
			fmt.Printf("  applied above skipped by phase 2 score: AUC %.3f (%d v %d)\n", auc(pos, neg), len(pos), len(neg))
		}
	}
	return 0
}

type artifact struct {
	Status     string  `json:"status"`
	ID         string  `json:"id"`
	Score      float64 `json:"score"`
	CVMatch    int     `json:"cv_match"`
	CVCoverage float64 `json:"cv_coverage"`
	CVBlended  float64 `json:"cv_blended"`
}

func readArtifacts(dir, status string) map[int]artifact {
	out := map[int]artifact{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		var a artifact
		b, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(b, &a) != nil || a.Status != status {
			continue
		}
		if id, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json")); err == nil {
			out[id] = a
		}
	}
	return out
}

func readLabels(path string) (map[int]float64, []int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	labels := map[int]float64{}
	var order []int
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		id, err1 := strconv.Atoi(f[0])
		v, err2 := strconv.ParseFloat(f[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		labels[id] = v
		order = append(order, id)
	}
	if len(labels) == 0 {
		return nil, nil, fmt.Errorf("no labels in %s", path)
	}
	return labels, order, nil
}

func scoreOr(m map[int]artifact, id int) string {
	if a, ok := m[id]; ok {
		return strconv.FormatFloat(a.Score, 'g', -1, 64)
	}
	return "-"
}

// pairAccuracy is the share of pairs the truth separates that pred orders the
// same way. A pred tie counts half.
func pairAccuracy(pred, truth map[int]float64) (correct, n float64) {
	ids := make([]int, 0, len(pred))
	for id := range pred {
		if _, ok := truth[id]; ok {
			ids = append(ids, id)
		}
	}
	for i, x := range ids {
		for _, y := range ids[i+1:] {
			if truth[x] == truth[y] {
				continue
			}
			n++
			switch d := (pred[x] - pred[y]) * (truth[x] - truth[y]); {
			case d > 0:
				correct++
			case d == 0:
				correct += 0.5
			}
		}
	}
	return correct, n
}

func mae(pred, truth map[int]float64) float64 {
	var sum, n float64
	for id, p := range pred {
		if t, ok := truth[id]; ok {
			sum += math.Abs(p - t)
			n++
		}
	}
	return sum / n
}

// spearman is Pearson over average ranks, which is what ties need.
func spearman(pred, truth map[int]float64) float64 {
	var xs, ys []float64
	for id, p := range pred {
		if t, ok := truth[id]; ok {
			xs, ys = append(xs, p), append(ys, t)
		}
	}
	rx, ry := ranks(xs), ranks(ys)
	var mx, my float64
	for i := range rx {
		mx += rx[i]
		my += ry[i]
	}
	n := float64(len(rx))
	mx, my = mx/n, my/n
	var sxy, sxx, syy float64
	for i := range rx {
		sxy += (rx[i] - mx) * (ry[i] - my)
		sxx += (rx[i] - mx) * (rx[i] - mx)
		syy += (ry[i] - my) * (ry[i] - my)
	}
	return sxy / math.Sqrt(sxx*syy)
}

func ranks(v []float64) []float64 {
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, len(v))
	for i := 0; i < len(idx); {
		j := i
		for j < len(idx) && v[idx[j]] == v[idx[i]] {
			j++
		}
		for k := i; k < j; k++ {
			out[idx[k]] = float64(i+j+1) / 2
		}
		i = j
	}
	return out
}

// auc is the chance a positive outscores a negative, ties counting half.
func auc(pos, neg []float64) float64 {
	var c float64
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				c++
			case p == n:
				c += 0.5
			}
		}
	}
	return c / float64(len(pos)*len(neg))
}
