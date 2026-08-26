package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
)

// The Node harness injects a fake embedder and lets selectCvForJd do the rest,
// so a bullet's score is the max COSINE between its vector and the queries' —
// not the hash. Scoring the hash directly ranks differently and the gate reads
// as a funnel difference when it is a harness difference.
//
// gateQueries must match the Node harness exactly.
var gateQueries = []string{"Java experience", "Kafka streaming", "security"}

func hashUnit(text string) float64 {
	sum := sha256.Sum256([]byte(text))
	n, _ := strconv.ParseInt(hex.EncodeToString(sum[:])[:8], 16, 64)
	return float64(n%100000) / 100000
}

// fakeVec is the injected embedder: [h, 1-h].
func fakeVec(text string) [2]float64 {
	h := hashUnit(text)
	return [2]float64{h, 1 - h}
}

func cos2(a, b [2]float64) float64 {
	dot := a[0]*b[0] + a[1]*b[1]
	na := math.Sqrt(a[0]*a[0] + a[1]*a[1])
	nb := math.Sqrt(b[0]*b[0] + b[1]*b[1])
	if na*nb == 0 {
		return 0
	}
	return dot / (na * nb)
}

func fixedScore(text string) float64 {
	v := fakeVec(text)
	best := math.Inf(-1)
	for _, q := range gateQueries {
		if s := cos2(fakeVec(q), v); s > best {
			best = s
		}
	}
	return best
}

// gateSelect compares the selection funnel — trim, pins, the line budget, the
// section cap, the floor and the chronological re-sort — against the Node
// implementation on a CV it can read.
//
// It must be a `##` CV: the Node parser matches only that spelling, so on a
// bold-heading CV it returns the input unchanged and there is nothing to gate.
func gateSelect(cvPath, jsFile string) int {
	cv, err := os.ReadFile(cvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	wantB, err := os.ReadFile(jsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	opt := cvselect.DefaultOptions()
	opt.SpikeWeight = 0 // the harness drives fixed scores; no background to subtract
	sel := cvselect.Select(string(cv), fixedScore, opt)

	var got []string
	emit := func(section string, kept map[string]int, order []string) {
		for _, title := range order {
			got = append(got, fmt.Sprintf("%s\t%s\t%d", section, title, kept[title]))
		}
	}
	// Compare the multiset of (section, title, count) rather than the rendered
	// order: the Node harness prints in its own render order and the point here
	// is what survived, not where it landed.
	emit("Experience", sel.ExpKept, sortedTitles(sel.ExpKept))
	emit("Projects", sel.ProjKept, sortedTitles(sel.ProjKept))

	var want []string
	for _, line := range strings.Split(strings.TrimRight(string(wantB), "\n"), "\n") {
		if strings.HasPrefix(line, "  BULLET") || line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		title := strings.TrimSpace(strings.TrimPrefix(f[1], "###"))
		want = append(want, fmt.Sprintf("%s\t%s\t%s", f[0], title, f[2]))
	}
	sort.Strings(want)
	sort.Strings(got)

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
			fmt.Printf("  go %q\n  js %q\n", g, w)
		}
	}
	fmt.Printf("\nselect: %d/%d entries identical", len(want)-diffs, len(want))
	if diffs > 0 {
		fmt.Println(" — DIFFER")
		return 1
	}
	fmt.Println(" — GREEN")
	return 0
}

func sortedTitles(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
