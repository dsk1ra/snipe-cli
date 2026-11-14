package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
)

// gate judge measures the Phase 3 judge blend against plain cosine on a sheet
// of postings × cv.md bullets, ticked with the bullets worth keeping. It stands
// in for the Node retrieval-bench.mjs that produced the 0.815 → 0.930 result,
// at bullet granularity: the old sheets ticked project titles.
//
// The sheet's EXEMPLAR postings become the judge's few-shot examples, written
// beside the sheet in judge-shots.json's format and loaded back through
// phase3.LoadShots, so they pass the check the live judge applies. The rest are
// scored. Nothing here writes the live batch/cache/judge-shots.json.

func gateJudge(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gate judge sheet <home> [postings=14] [exemplars=2] > sheet.md\n"+
			"       gate judge score <home> <sheet.md>")
		return 2
	}
	var err error
	switch home := args[1]; args[0] {
	case "sheet":
		n, k := 14, 2
		if len(args) > 2 {
			fmt.Sscan(args[2], &n)
		}
		if len(args) > 3 {
			fmt.Sscan(args[3], &k)
		}
		err = judgeSheet(home, n, k)
	case "score":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gate judge score <home> <sheet.md>")
			return 2
		}
		err = judgeScore(home, args[2])
	default:
		err = fmt.Errorf("unknown judge mode %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate judge:", err)
		return 1
	}
	return 0
}

type cvEntry struct {
	Name    string
	Bullets []string
}

var boldMark = regexp.MustCompile(`\*\*`)

// cvEntries walks cv.md the way phase3's cvBullets does: Experience, then
// Projects.
func cvEntries(cv string) []cvEntry {
	var out []cvEntry
	for _, name := range []string{"Experience", "Projects"} {
		for _, s := range cvselect.ParseSections(cv) {
			if s.Name != name {
				continue
			}
			for _, e := range cvselect.ParseEntries(s.Lines).Entries {
				var head []string
				for _, h := range e.Head {
					head = append(head, strings.TrimSpace(boldMark.ReplaceAllString(h, "")))
				}
				out = append(out, cvEntry{strings.Join(head, " / "), e.Bullets})
			}
		}
	}
	return out
}

func judgeSheet(home string, n, k int) error {
	env, err := newTailorEnv(home, paths.Layout{Home: home}.CV())
	if err != nil {
		return err
	}
	defer os.RemoveAll(env.tmp)
	all, err := env.eligible()
	if err != nil {
		return err
	}
	// The band Phase 3 runs on, one posting per company, best first so spread
	// covers the band from top to bottom.
	floor := env.cfg.Thresholds.AutoPDFScore
	seen := map[string]bool{}
	var band []cand
	for _, c := range all {
		if c.Score >= floor && !seen[c.Company] {
			seen[c.Company] = true
			band = append(band, c)
		}
	}
	sort.SliceStable(band, func(i, j int) bool { return band[i].Score > band[j].Score })
	if len(band) < n {
		return fmt.Errorf("%d postings at score >= %.1f, want %d", len(band), floor, n)
	}
	picked := spread(band, n)
	ex := map[string]bool{}
	for _, c := range spread(picked, k) {
		ex[c.ID] = true
	}

	entries := cvEntries(env.cfg.CVRaw)
	var b strings.Builder
	fmt.Fprintf(&b, "# gate judge sheet\n\ncv.md %s, %s. %d postings at score >= %.1f, one per company; "+
		"the %d marked EXEMPLAR are examples, the rest are scored.\n\n", env.cfg.Hash(), time.Now().Format("2006-01-02"), n, floor, k)
	b.WriteString("For each posting, change `- [ ]` to `- [x]` on every CV bullet you would keep on a one-page CV " +
		"tailored to that posting. Tick 3 to 8 per posting. Change nothing else.\n")
	for _, c := range picked {
		o, err := env.offer(c.ID)
		if err != nil {
			return err
		}
		tag := ""
		if ex[c.ID] {
			tag = " EXEMPLAR"
		}
		fmt.Fprintf(&b, "\n---\n\n## %s — %s — %s (score %.1f)%s\n\n**Requirements**\n\n", o.ID, o.Company, o.Role, o.Score, tag)
		for _, r := range o.Reqs {
			b.WriteString("- " + strings.Join(strings.Fields(r), " ") + "\n")
		}
		// What the judge itself is shown of the posting.
		jd := o.JD
		if r := []rune(jd); len(r) > 2500 {
			jd = string(r[:2500])
		}
		b.WriteString("\n**Posting (excerpt)**\n\n")
		for _, l := range strings.Split(strings.TrimSpace(jd), "\n") {
			b.WriteString("> " + l + "\n")
		}
		b.WriteString("\n**CV bullets**\n")
		for _, e := range entries {
			fmt.Fprintf(&b, "\n*%s*\n\n", e.Name)
			for _, bl := range e.Bullets {
				b.WriteString("- [ ] " + bl + "\n")
			}
		}
	}
	fmt.Print(b.String())
	return nil
}

type judgeCase struct {
	ID       string
	Exemplar bool
	Want     map[string]bool
	Listed   int
}

var (
	judgeHeadRe = regexp.MustCompile(`^## (\d+) — `)
	judgeTickRe = regexp.MustCompile(`^- \[([ xX])\] (.+)$`)
)

// parseJudgeSheet reads the ticks. A listed bullet that is not in bullets
// means cv.md changed after the sheet was written: the ticks no longer name
// what the labeller read, so that is an error rather than a skip.
func parseJudgeSheet(sheet string, bullets []string) ([]judgeCase, error) {
	known := map[string]bool{}
	for _, b := range bullets {
		known[b] = true
	}
	var out []judgeCase
	inBullets := false // requirement lines above the bullets are list items too
	for _, l := range strings.Split(sheet, "\n") {
		if m := judgeHeadRe.FindStringSubmatch(l); m != nil {
			out = append(out, judgeCase{ID: m[1], Exemplar: strings.HasSuffix(strings.TrimSpace(l), "EXEMPLAR"), Want: map[string]bool{}})
			inBullets = false
			continue
		}
		if strings.TrimSpace(l) == "**CV bullets**" {
			inBullets = len(out) > 0
			continue
		}
		m := judgeTickRe.FindStringSubmatch(strings.TrimRight(l, " "))
		if m == nil || !inBullets {
			continue
		}
		c := &out[len(out)-1]
		if !known[m[2]] {
			return nil, fmt.Errorf("posting %s lists a bullet cv.md no longer has: %.60q", c.ID, m[2])
		}
		c.Listed++
		if m[1] != " " {
			c.Want[m[2]] = true
		}
	}
	for _, c := range out {
		if c.Listed != len(bullets) {
			return nil, fmt.Errorf("posting %s lists %d bullets, cv.md has %d", c.ID, c.Listed, len(bullets))
		}
		if len(c.Want) == 0 || len(c.Want) == len(bullets) {
			return nil, fmt.Errorf("posting %s has %d ticks: nothing to rank", c.ID, len(c.Want))
		}
	}
	return out, nil
}

// pairAcc is how often score puts a wanted bullet above an unwanted one, a tie
// counting half. Chance is 0.5, and it does not move with how many were ticked.
func pairAcc(score map[string]float64, bullets []string, want map[string]bool) float64 {
	var n, w float64
	for _, a := range bullets {
		if !want[a] {
			continue
		}
		for _, b := range bullets {
			if want[b] {
				continue
			}
			n++
			switch {
			case score[a] > score[b]:
				w++
			case score[a] == score[b]:
				w += 0.5
			}
		}
	}
	return w / n
}

// bootCI is the 95% interval of the mean of d, resampling postings, seeded so
// a rerun prints the same interval.
func bootCI(d []float64) (lo, hi float64) {
	r := rand.New(rand.NewPCG(1, 2))
	means := make([]float64, 10000)
	for i := range means {
		var s float64
		for range d {
			s += d[r.IntN(len(d))]
		}
		means[i] = s / float64(len(d))
	}
	slices.Sort(means)
	return means[249], means[9749]
}

func judgeScore(home, sheetPath string) error {
	env, err := newTailorEnv(home, paths.Layout{Home: home}.CV())
	if err != nil {
		return err
	}
	defer os.RemoveAll(env.tmp)
	var bullets []string
	for _, e := range cvEntries(env.cfg.CVRaw) {
		bullets = append(bullets, e.Bullets...)
	}
	raw, err := os.ReadFile(sheetPath)
	if err != nil {
		return err
	}
	cases, err := parseJudgeSheet(string(raw), bullets)
	if err != nil {
		return err
	}

	type shot struct {
		Reqs     []string `json:"reqs"`
		JD       string   `json:"jd"`
		WantText []string `json:"wantText"`
	}
	var shots []shot
	var test []judgeCase
	for _, c := range cases {
		if !c.Exemplar {
			test = append(test, c)
			continue
		}
		o, err := env.offer(c.ID)
		if err != nil {
			return err
		}
		s := shot{Reqs: o.Reqs, JD: o.JD}
		for _, b := range bullets { // cv.md order, so the file is stable
			if c.Want[b] {
				s.WantText = append(s.WantText, b)
			}
		}
		shots = append(shots, s)
	}
	shotsPath := strings.TrimSuffix(sheetPath, ".md") + ".shots.json"
	js, _ := json.MarshalIndent(map[string]any{"cvHash": env.cfg.Hash(), "graded": false, "shots": shots}, "", "  ")
	if err := os.WriteFile(shotsPath, append(js, '\n'), 0o644); err != nil {
		return err
	}
	loaded := phase3.LoadShots(shotsPath, env.cfg.CVRaw)
	if len(loaded) != len(shots) || len(loaded) == 0 {
		return fmt.Errorf("%s: %d of %d exemplars load against cv.md", shotsPath, len(loaded), len(shots))
	}
	fmt.Printf("gate judge — %d exemplar(s) in %s, %d posting(s) scored, %d bullets\n\n", len(loaded), shotsPath, len(test), len(bullets))

	ctx := context.Background()
	var base, blend, alone []float64
	failed := 0
	fmt.Printf("%-6s %5s %7s %7s %7s %6s\n", "offer", "ticks", "cosine", "blend", "judge", "secs")
	for _, c := range test {
		o, err := env.offer(c.ID)
		if err != nil {
			return err
		}
		t0 := time.Now()
		b, g, err := env.t.BenchScores(ctx, o.Reqs, o.JD, loaded)
		if b == nil {
			return fmt.Errorf("posting %s: %w", c.ID, err)
		}
		if err != nil {
			// Live falls back to cosine when the judge fails, so the blend does too.
			failed++
			fmt.Printf("  posting %s: judge failed, blend = cosine: %v\n", c.ID, err)
		}
		bl := map[string]float64{}
		for k, v := range b {
			bl[k] = v + cvselect.JudgeBlend*g[k]
		}
		base = append(base, pairAcc(b, bullets, c.Want))
		blend = append(blend, pairAcc(bl, bullets, c.Want))
		alone = append(alone, pairAcc(g, bullets, c.Want))
		fmt.Printf("%-6s %5d %7.3f %7.3f %7.3f %6.0f\n", c.ID, len(c.Want), base[len(base)-1], blend[len(blend)-1], alone[len(alone)-1], time.Since(t0).Seconds())
	}

	fmt.Printf("\npair accuracy over %d posting(s), chance 0.5\n\n%-24s %6s %8s %18s %6s\n", len(test), "arm", "pair", "Δ cosine", "CI95", "w/l")
	fmt.Printf("%-24s %6.3f\n", "cosine (live today)", mean(base))
	for _, arm := range []struct {
		name string
		v    []float64
	}{{fmt.Sprintf("cosine+judge %.2f", cvselect.JudgeBlend), blend}, {"judge alone", alone}} {
		d := make([]float64, len(base))
		w, l := 0, 0
		for i := range base {
			d[i] = arm.v[i] - base[i]
			if d[i] > 0 {
				w++
			} else if d[i] < 0 {
				l++
			}
		}
		lo, hi := bootCI(d)
		sig := ""
		if lo > 0 {
			sig = "  better"
		} else if hi < 0 {
			sig = "  worse"
		}
		fmt.Printf("%-24s %6.3f %+8.3f   [%+.3f, %+.3f] %3d/%d%s\n", arm.name, mean(arm.v), mean(d), lo, hi, w, l, sig)
	}
	if failed > 0 {
		fmt.Printf("\n%d judge call(s) failed and scored as cosine\n", failed)
	}
	return nil
}
