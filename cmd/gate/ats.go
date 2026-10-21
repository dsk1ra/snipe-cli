package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/atsjudge"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// gate ats is the judge half of §8.1: the ATS simulator run on the rendered
// pages `gate tailor` left in e3-verbatim.json and e3-adapt.json, both arms,
// every offer present in both. The judge model is never the writer's family
// (atsjudge refuses it), so a gain here is not the writer grading itself.

const atsUsage = "usage: gate ats <home> [--model gemma3:4b-it-q8_0] [--allow-same-family] [--control]"

// blankCeiling is how far a blank CV may score before the judge is useless:
// the floor, with the name and contact line only, should be near 0.
const blankCeiling = 0.2

// atsItem is one offer: its qualifications and the page each arm rendered.
type atsItem struct {
	ID              string
	Quals           []atsjudge.Qual
	Verbatim, Adapt string
}

// atsRow is an item scored on both arms.
type atsRow struct {
	ID   string
	V, A atsjudge.Result
}

// qualsOf reads the qualifications off a report, with the parser the pipeline
// uses for the same table.
func qualsOf(report string) []atsjudge.Qual {
	return atsjudge.QualsFromRows(adapt.RowsFromReport(report))
}

// atsItems pairs the two arms' cached pages by offer. An offer that failed in
// either arm, or has no page, is left out: there is nothing to compare.
func atsItems(l paths.Layout, report func(id string) (string, error), w io.Writer) []atsItem {
	v, a := loadE3(e3Path(l, "verbatim")), loadE3(e3Path(l, "adapt"))
	var out []atsItem
	for _, rv := range v.Offers {
		ra, ok := a.get(rv.Offer)
		if !ok || rv.Err != "" || ra.Err != "" || rv.Page == "" || ra.Page == "" {
			continue
		}
		rep, err := report(rv.Offer)
		if err != nil {
			fmt.Fprintf(w, "  %s: %v\n", rv.Offer, err)
			continue
		}
		out = append(out, atsItem{ID: rv.Offer, Quals: qualsOf(rep), Verbatim: rv.Page, Adapt: ra.Page})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// reportOf is what tailorEnv.offer reads a report with; it needs the home's
// layout and nothing of the CV, so gate ats takes no cv argument.
func reportOf(l paths.Layout) func(id string) (string, error) {
	env := &tailorEnv{l: l}
	return func(id string) (string, error) {
		o, err := env.offer(id)
		return o.Text, err
	}
}

// pairStats is the paired comparison of one metric over the rows where both
// arms have it: the two means, their difference, and how many offers went up,
// down or stayed. Equal means within 1e-9, which no 8-qualification ratio
// reaches by accident.
type pairStats struct {
	N                         int
	MeanV, MeanA, Diff        float64
	Improved, Regressed, Tied int
}

func paired(rows []atsRow, get func(atsjudge.Result) (float64, bool)) pairStats {
	var p pairStats
	for _, r := range rows {
		x, okV := get(r.V)
		y, okA := get(r.A)
		if !okV || !okA {
			continue
		}
		p.N++
		p.MeanV += x
		p.MeanA += y
		switch d := y - x; {
		case d > 1e-9:
			p.Improved++
		case d < -1e-9:
			p.Regressed++
		default:
			p.Tied++
		}
	}
	if p.N > 0 {
		p.MeanV /= float64(p.N)
		p.MeanA /= float64(p.N)
		p.Diff = p.MeanA - p.MeanV
	}
	return p
}

func mustOf(r atsjudge.Result) (float64, bool) { return r.MustEvidence, r.HasMust }
func prefOf(r atsjudge.Result) (float64, bool) { return r.PrefEvidence, r.HasPref }

func ratioCell(v float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f", v)
}

// atsReport prints the per-offer table, then the means and the paired counts.
func atsReport(w io.Writer, model string, rows []atsRow) {
	fmt.Fprintf(w, "gate ats — judge %s, %d offer(s) on both arms\n\n", model, len(rows))
	fmt.Fprintf(w, "%-6s %9s %9s %9s %6s %7s %5s %7s\n", "offer", "mhe verb", "mhe adapt", "Δ", "grade", "grade", "pf", "pf")
	fmt.Fprintf(w, "%-6s %9s %9s %9s %6s %7s %5s %7s\n", "", "", "", "", "verb", "adapt", "verb", "adapt")
	pf, er := [2]int{}, [2]int{}
	grades := [2]map[string]int{{}, {}}
	for _, r := range rows {
		d := "-"
		if r.V.HasMust && r.A.HasMust {
			d = fmt.Sprintf("%+.2f", r.A.MustEvidence-r.V.MustEvidence)
		}
		fmt.Fprintf(w, "%-6s %9s %9s %9s %6s %7s %5d %7d\n", r.ID,
			ratioCell(r.V.MustEvidence, r.V.HasMust), ratioCell(r.A.MustEvidence, r.A.HasMust), d,
			r.V.Grade, r.A.Grade, r.V.PointerFailed, r.A.PointerFailed)
		pf[0] += r.V.PointerFailed
		pf[1] += r.A.PointerFailed
		er[0] += r.V.Errored
		er[1] += r.A.Errored
		grades[0][r.V.Grade]++
		grades[1][r.A.Grade]++
	}
	for _, m := range []struct {
		name string
		get  func(atsjudge.Result) (float64, bool)
	}{{"must_have_evidence", mustOf}, {"preferred_evidence", prefOf}} {
		p := paired(rows, m.get)
		if p.N == 0 {
			fmt.Fprintf(w, "\n%-20s no offer has it on both arms\n", m.name)
			continue
		}
		fmt.Fprintf(w, "\n%-20s verbatim %.3f  adapt %.3f  paired Δ %+.3f over %d offer(s): %d improved, %d regressed, %d tied\n",
			m.name, p.MeanV, p.MeanA, p.Diff, p.N, p.Improved, p.Regressed, p.Tied)
	}
	fmt.Fprintf(w, "grades               verbatim %s  adapt %s\n", gradeLine(grades[0]), gradeLine(grades[1]))
	fmt.Fprintf(w, "pointer_failed       verbatim %d  adapt %d (met or partly answers whose quote is not in the CV, counted unmet)\n", pf[0], pf[1])
	fmt.Fprintf(w, "no usable reply      verbatim %d  adapt %d (counted unmet)\n", er[0], er[1])
}

func gradeLine(m map[string]int) string {
	var parts []string
	for _, g := range []string{"A", "B", "C", "D"} {
		parts = append(parts, fmt.Sprintf("%s=%d", g, m[g]))
	}
	return strings.Join(parts, " ")
}

// atsControl judges the blank CV of each item and prints the mean
// must_have_evidence: the floor. It returns that mean and whether any offer had
// the metric.
func atsControl(ctx context.Context, w io.Writer, j *atsjudge.Judge, items []atsItem) (float64, bool, error) {
	var xs []float64
	for _, it := range items {
		r, err := j.Score(ctx, it.Quals, atsjudge.Blank(it.Verbatim))
		if err != nil {
			return 0, false, fmt.Errorf("control %s: %w", it.ID, err)
		}
		if r.HasMust {
			xs = append(xs, r.MustEvidence)
		}
	}
	if len(xs) == 0 {
		fmt.Fprintln(w, "\ncontrol (blank CV)   no offer has a supported must-have")
		return 0, false, nil
	}
	m := mean(xs)
	fmt.Fprintf(w, "\ncontrol (blank CV)   must_have_evidence %.3f over %d offer(s): the floor, a CV with no bullets\n", m, len(xs))
	if m > blankCeiling {
		fmt.Fprintf(w, "WARNING: the judge scores a blank CV at %.2f (> %.1f). It says met to anything, so its numbers on the real CVs mean nothing.\n", m, blankCeiling)
	}
	return m, true, nil
}

func gateATS(args []string) int {
	opt := atsjudge.Options{}
	control := false
	var pos []string
	for i := 0; i < len(args); i++ {
		if v, ok := optVal(args, &i, "--model"); ok {
			opt.Model = v
		} else if args[i] == "--allow-same-family" {
			opt.AllowSameFamily = true
		} else if args[i] == "--control" {
			control = true
		} else {
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, atsUsage)
		return 2
	}
	// Only a Qwen judge (reachable with --allow-same-family) reasons by default.
	opt.NoThink = opt.AllowSameFamily
	l := paths.Layout{Home: pos[0], Code: pos[0]}
	j, err := atsjudge.New(ollama.New("", nil), l.CacheDir(), opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	items := atsItems(l, reportOf(l), os.Stderr)
	if len(items) == 0 {
		fmt.Fprintf(os.Stderr, "no offer is in both %s and %s: run gate tailor --arm=verbatim and --arm=adapt on the same sample first\n",
			e3Path(l, "verbatim"), e3Path(l, "adapt"))
		return 2
	}

	ctx := context.Background()
	var rows []atsRow
	for _, it := range items {
		v, err := j.Score(ctx, it.Quals, it.Verbatim)
		if err == nil {
			var a atsjudge.Result
			if a, err = j.Score(ctx, it.Quals, it.Adapt); err == nil {
				rows = append(rows, atsRow{ID: it.ID, V: v, A: a})
				continue
			}
		}
		fmt.Fprintf(os.Stderr, "  %s: %v\n", it.ID, err)
		return 1
	}
	atsReport(os.Stdout, j.Model, rows)
	code := 0
	if control {
		if _, _, err := atsControl(ctx, os.Stdout, j, items); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	fmt.Printf("\nmodel calls this run: %d (the rest came from %s)\n", j.Calls, atsjudge.CachePath(l.CacheDir(), j.Model))
	return code
}

// withCachedATS fills must_have_evidence from a `gate ats` cache, reading only:
// it never calls a model. The row in the table stops saying "needs judge" only
// when the cache covers every offer of the arm being reported, since a mean
// over some of them would read as the arm's. base is filled best-effort for the
// paired column. The default judge model is the one read; --model on gate ats
// writes a different file, which this does not look for.
func withCachedATS(l paths.Layout, report func(id string) (string, error), rs, base []e3Result) {
	j, err := atsjudge.New(nil, l.CacheDir(), atsjudge.Options{})
	if err != nil {
		return
	}
	covered := fillATS(j, report, rs)
	fillATS(j, report, base)
	if !covered {
		return
	}
	for i := range suite {
		if suite[i].name == "must_have_evidence" {
			suite[i].na = ""
		}
	}
}

// fillATS sets must_have_evidence on each finished result whose judgments are
// all cached and reports whether every one was.
func fillATS(j *atsjudge.Judge, report func(id string) (string, error), rs []e3Result) bool {
	seen, all := false, true
	for _, r := range rs {
		if r.Err != "" {
			continue
		}
		seen = true
		rep, err := report(r.Offer)
		if err != nil || r.Page == "" {
			all = false
			continue
		}
		res, err := j.Score(context.Background(), qualsOf(rep), r.Page)
		if err != nil { // not cached: this offer is not covered
			all = false
			continue
		}
		if res.HasMust && r.Metrics != nil {
			r.Metrics["must_have_evidence"] = res.MustEvidence
		}
	}
	return seen && all
}
