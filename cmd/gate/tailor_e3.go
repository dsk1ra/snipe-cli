package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/ledger"
	"github.com/dsk1ra/snipe-cli/internal/minicheck"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/verify"
)

// E3 of docs/PRD-TAILORING-REWORK.md §8.2: the adapt arm against verbatim on a
// fixed sample, every metric of §8.1 that needs no judge model, the kill rule,
// and a results file the lead reads by hand before trusting any table
// (benchmark rule 7).

// adaptBudget is what is left of the 6-minute pipeline budget once Phase 1
// (18 s), Phase 2 (~135 s) and selection (~8 s) have had theirs (§6.8, §8.2).
// This run measures only the adapt step, so this is the bar for its max.
const adaptBudget = 200 * time.Second

// ── results ─────────────────────────────────────────────────────────────────

// e3Result is one offer under one arm. It is what the results file holds, and
// what a rerun reuses when the key still matches.
type e3Result struct {
	Offer   string  `json:"offer"`
	Arm     string  `json:"arm"`
	Ledger  string  `json:"ledger"` // hash of the ledger file and the CV the gate ranked against
	Prompt  string  `json:"prompt"` // hash of the rewrite prompt and the verifier model
	Company string  `json:"company,omitempty"`
	Score   float64 `json:"score"`
	Err     string  `json:"error,omitempty"`

	Metrics map[string]float64 `json:"metrics,omitempty"`
	B       map[string]float64 `json:"metrics_aa,omitempty"` // second run, with --aa

	WallS    float64 `json:"wall_s"`  // the arm, end to end
	AdaptS   float64 `json:"adapt_s"` // Tailor.Adapt alone
	Fallback bool    `json:"fallback,omitempty"`
	Selected int     `json:"selected"`

	Changed        []changedBullet `json:"changed,omitempty"`
	Kill           []string        `json:"kill,omitempty"` // why the kill rule fires, in words
	BooleanMissing []string        `json:"boolean_missing,omitempty"`
	Page           string          `json:"page,omitempty"` // pdftotext -layout, for reading by hand
}

// changedBullet is a shipped line that differs from cv.md's, with what
// licensed it (the truth.json record) and the gate's own recheck.
type changedBullet struct {
	adapt.BulletTruth
	Closure string `json:"closure,omitempty"` // verify.EntityClosure on shipped vs original; "" is clean
	Recheck string `json:"recheck,omitempty"` // verify.Code on the shipped line; "" is clean
}

type e3File struct {
	Arm     string     `json:"arm"`
	Updated string     `json:"updated"`
	Offers  []e3Result `json:"offers"`
}

func e3Path(l paths.Layout, arm string) string { return filepath.Join(l.CacheDir(), "e3-"+arm+".json") }

func loadE3(path string) e3File {
	var f e3File
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &f) // a torn file is a cold start, not a failure
	}
	return f
}

func (f *e3File) get(id string) (e3Result, bool) {
	for _, r := range f.Offers {
		if r.Offer == id {
			return r, true
		}
	}
	return e3Result{}, false
}

func (f *e3File) put(r e3Result) {
	for i := range f.Offers {
		if f.Offers[i].Offer == r.Offer {
			f.Offers[i] = r
			return
		}
	}
	f.Offers = append(f.Offers, r)
}

func (f *e3File) save(path string) error {
	f.Updated = time.Now().Format(time.RFC3339)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return state.WriteAtomic(path, append(b, '\n'))
}

func short(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:12]
}

// ── the sample ──────────────────────────────────────────────────────────────

type cand struct {
	ID      string // report number
	Score   float64
	Company string
}

// stratify picks n of cs, the same n for the same input. At least a quarter
// have score >= hi when that many exist; the rest are spread evenly over the
// score order of what is left, so the sample runs from the worst to the best
// posting and not from wherever the queue happened to be rich.
func stratify(cs []cand, n int, hi float64) []cand {
	cs = slices.Clone(cs)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Score != cs[j].Score {
			return cs[i].Score > cs[j].Score
		}
		return cs[i].ID < cs[j].ID
	})
	if n >= len(cs) {
		return byID(cs)
	}
	var high []cand
	for _, c := range cs {
		if c.Score >= hi {
			high = append(high, c)
		}
	}
	picked := spread(high, min(len(high), (n+3)/4))
	taken := map[string]bool{}
	for _, c := range picked {
		taken[c.ID] = true
	}
	var rest []cand
	for _, c := range cs {
		if !taken[c.ID] {
			rest = append(rest, c)
		}
	}
	return byID(append(picked, spread(rest, n-len(picked))...))
}

// spread takes m evenly spaced items of a sorted list, the middle of each of m
// equal slices of it.
func spread(xs []cand, m int) []cand {
	var out []cand
	for k := 0; k < m; k++ {
		out = append(out, xs[(2*k+1)*len(xs)/(2*m)])
	}
	return out
}

func byID(cs []cand) []cand {
	slices.SortFunc(cs, func(a, b cand) int { return strings.Compare(a.ID, b.ID) })
	return cs
}

// eligible is every offer the arms can run on: a state row with a report, a
// posting in batch/jds and a finished Phase 2 artifact. Like offer(), a report
// number resolves to the lowest state id that carries it.
func (e *tailorEnv) eligible() ([]cand, error) {
	rows, err := state.Load(e.l.State())
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	cache := state.NewCache()
	seen := map[string]bool{}
	var out []cand
	for _, id := range ids {
		row := rows[id]
		if !row.HasReport() || seen[row.ReportNum] {
			continue
		}
		seen[row.ReportNum] = true
		ev := cache.Eval(e.l.Evals(), row.ID)
		if ev == nil || ev.Status != state.P2Evaled {
			continue
		}
		if _, ok := cache.ReportFile(e.l.Reports(), row.ReportNum); !ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.l.JDs(), strconv.Itoa(row.ID)+".txt")); err != nil {
			continue
		}
		out = append(out, cand{row.ReportNum, ev.Score, ev.Company})
	}
	return out, nil
}

func samplePath(e *tailorEnv) string { return filepath.Join(e.l.CacheDir(), "e3-sample.txt") }

// sample returns the ids to run: the saved list, or a fresh draw saved for the
// next run. w gets the chosen ids with score and company.
func (e *tailorEnv) sample(w io.Writer, n int, resample bool) ([]string, error) {
	all, err := e.eligible()
	if err != nil {
		return nil, err
	}
	hi := e.cfg.Thresholds.InferMinScore
	path := samplePath(e)
	var picked []cand
	if b, err := os.ReadFile(path); err == nil && !resample {
		for _, id := range parseOfferIDs(strings.Split(string(b), "\n")) {
			c := cand{ID: id}
			if i := slices.IndexFunc(all, func(c cand) bool { return c.ID == id }); i >= 0 {
				c = all[i]
			}
			picked = append(picked, c)
		}
		fmt.Fprintf(w, "sample: reusing %s (%d offers; --resample to redraw)\n", path, len(picked))
	}
	if picked == nil {
		picked = stratify(all, n, hi)
		var b strings.Builder
		fmt.Fprintf(&b, "# gate tailor E3 sample: %d of %d eligible, stratified by Phase 2 score, infer_min_score %.1f\n", len(picked), len(all), hi)
		for _, c := range picked {
			fmt.Fprintf(&b, "%s  # %.1f %s\n", c.ID, c.Score, c.Company)
		}
		if err := state.WriteAtomic(path, []byte(b.String())); err != nil {
			return nil, err
		}
		fmt.Fprintf(w, "sample: %d of %d eligible, written to %s\n", len(picked), len(all), path)
	}
	nhi := 0
	var ids []string
	for _, c := range picked {
		if c.Score >= hi {
			nhi++
		}
		ids = append(ids, c.ID)
		fmt.Fprintf(w, "  %s  %4.1f  %s\n", c.ID, c.Score, c.Company)
	}
	fmt.Fprintf(w, "  %d with score >= %.1f\n\n", nhi, hi)
	return ids, nil
}

// ── metrics ─────────────────────────────────────────────────────────────────

// scoreOffer computes every metric that needs no judge, from the rendered
// page, the arm's truth.json and the ledger. The ledger metrics are recomputed
// here from the page and the shipped text; none of them trusts the pipeline's
// own verdict, except supported/inferred/unapproved, which are its accounting.
func scoreOffer(o offer, out armOut, led *ledger.Ledger, inferMin float64) e3Result {
	pm := measure(out.Page, terms(o.Reqs))
	m := map[string]float64{
		"one_page":    b2f(pm.Pages <= 1),
		"lines":       float64(pm.Lines),
		"exp_starved": float64(pm.ExpStarved),
		"front_load":  pm.FrontLoad,
	}
	r := e3Result{Offer: o.ID, Company: o.Company, Score: o.Score, Metrics: m, Selected: len(out.Selected), Page: out.Page}
	if out.Stats != nil {
		m["shape_rejects"] = float64(out.Stats.ShapeRejects())
	}

	post := adapt.Posting{
		ID: o.ID, Score: o.Score, InferMinScore: inferMin,
		Rows: adapt.RowsFromReport(o.Text), Terms: phase3.PostingTerms(nil, o.Text),
	}
	plans := adapt.PlanAll(led, out.Selected, post)
	page := strings.Join(strings.Fields(out.Page), " ")

	// Truth: nothing changed means every claim is cv.md's own.
	sup, inf, unapproved, violations, changed := 1.0, 0.0, 0, 0, 0
	if tf := out.Truth; tf != nil {
		for _, e := range tf.Entries {
			sup, inf = math.Min(sup, e.SupportedShare()), math.Max(inf, e.InferredShare())
			unapproved += e.Unapproved
		}
		named := false
		for _, b := range tf.Bullets {
			if b.Class != string(verify.Supported) && b.Class != string(verify.Inferred) {
				named = true
				r.Kill = append(r.Kill, fmt.Sprintf("unapproved: [%s] %q (class %q)", b.Entry, b.Shipped, b.Class))
			}
			if b.Shipped == b.Original {
				continue
			}
			changed++
			cb := changedBullet{BulletTruth: b}
			i := slices.IndexFunc(plans, func(p adapt.Plan) bool { return p.Bullet.Text == b.Original })
			if i >= 0 && plans[i].EntryID != "" {
				p := plans[i]
				if ei := slices.IndexFunc(led.Entries, func(e ledger.Entry) bool { return e.ID == p.EntryID }); ei >= 0 {
					e := led.Entries[ei]
					known := verify.Known(e, led.Entities, p.AllowedTerms)
					if why := verify.EntityClosure(b.Original, b.Shipped, known); why != "" {
						cb.Closure = why
						violations += len(strings.Split(strings.TrimPrefix(why, "unrecorded term "), ", "))
						r.Kill = append(r.Kill, fmt.Sprintf("entity: %s\n        shipped:  %s\n        original: %s", why, b.Shipped, b.Original))
					}
					cb.Recheck = verify.Code(e, p.Bullet, b.Shipped, p.AllowedTerms, verify.Options{Entities: led.Entities})
				}
			}
			r.Changed = append(r.Changed, cb)
		}
		if unapproved > 0 && !named {
			r.Kill = append(r.Kill, fmt.Sprintf("unapproved: %d claim(s), the bullets are not identified in truth.json", unapproved))
		}
	}
	m["supported_share"], m["inferred_share"] = sup, inf
	m["unapproved"], m["entity_violations"] = float64(unapproved), float64(violations)
	if len(plans) > 0 {
		m["rewrite_rate"] = float64(changed) / float64(len(plans))
	}

	// keep_rate: the figures and hedge words of the selected lines' must-keep
	// facts, found on the page as whole words.
	var kept, total int
	for _, p := range plans {
		for _, f := range p.Bullet.Facts {
			if !f.MustKeep || f.Status == "rejected" || f.Source == "inferred" {
				continue
			}
			items := slices.Clone(f.Figures)
			for _, h := range ledger.Hedges {
				if strings.Contains(strings.ToLower(f.Text), h) {
					items = append(items, h)
				}
			}
			for _, it := range items {
				total++
				if pageHas(page, it) {
					kept++
				}
			}
		}
	}
	if total > 0 {
		m["keep_rate"] = float64(kept) / float64(total)
	}

	// term_parity: of the posting terms the ledger supports and the page names
	// in some form, the share spelt as the posting spells them. boolean_pass:
	// the supported terms of the must-have requirements, all spelt that way.
	var named, spelt int
	var need []string
	for _, t := range post.Terms {
		if !ledgerSupports(led, t.Text) {
			continue
		}
		if slices.ContainsFunc(formsOf(led, t.Text), func(f string) bool { return pageHas(page, f) }) {
			named++
			if pageHas(page, t.Text) {
				spelt++
			}
		}
		if slices.ContainsFunc(post.Rows, func(row phase2.Row) bool { return row.MustHave && pageHas(row.Requirement, t.Text) }) &&
			!slices.ContainsFunc(need, func(s string) bool { return strings.EqualFold(s, t.Text) }) {
			need = append(need, t.Text)
		}
	}
	if named > 0 {
		m["term_parity"] = float64(spelt) / float64(named)
	}
	if len(need) > 0 {
		m["boolean_pass"] = 1
		for _, t := range need {
			if !pageHas(page, t) {
				m["boolean_pass"] = 0
				r.BooleanMissing = append(r.BooleanMissing, t)
			}
		}
	}
	return r
}

// formsOf is the term and every spelling the ledger records for the same entity.
func formsOf(l *ledger.Ledger, term string) []string {
	forms := []string{term}
	for canon, group := range l.Entities {
		all := append([]string{canon}, group...)
		if slices.ContainsFunc(all, func(s string) bool { return strings.EqualFold(s, term) }) {
			for _, s := range all {
				if !slices.ContainsFunc(forms, func(f string) bool { return strings.EqualFold(f, s) }) {
					forms = append(forms, s)
				}
			}
		}
	}
	return forms
}

// ledgerSupports: some fact of the CV names the term, in any recorded spelling,
// or an approved framing is the term. A rejected fact never counts and an
// inference only once approved; a cv.md fact still awaiting review stands.
func ledgerSupports(l *ledger.Ledger, term string) bool {
	forms := formsOf(l, term)
	for _, e := range l.Entries {
		for _, b := range e.Bullets {
			for _, fr := range b.Framings {
				if fr.Approved && !fr.Rejected && strings.EqualFold(fr.Phrase, term) {
					return true
				}
			}
			for _, f := range b.Facts {
				if f.Status == "rejected" || (f.Source == "inferred" && f.Status != "approved") {
					continue
				}
				for _, form := range forms {
					if pageHas(f.Text, form) || slices.ContainsFunc(f.Entities, func(x string) bool { return strings.EqualFold(x, form) }) {
						return true
					}
				}
			}
		}
	}
	return false
}

// pageHas is a whole-word match, so "Go" is not found in "Google" and "C++"
// still matches. Case-insensitive, except for terms of three characters or
// fewer, where "go" is a word and "Go" a language (the rule adapt.contains uses).
func pageHas(s, term string) bool {
	if term == "" {
		return false
	}
	if utf8.RuneCountInString(term) > 3 {
		s, term = strings.ToLower(s), strings.ToLower(term)
	}
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], term)
		if i < 0 {
			return false
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(term):])
		if !isWord(before) && !isWord(after) {
			return true
		}
		from = i + 1
	}
	return false
}

// ── the run ─────────────────────────────────────────────────────────────────

type e3Runner struct {
	arm      string
	run      Arm
	offer    func(id string) (offer, error)
	led      *ledger.Ledger
	inferMin float64
	keyL     string
	keyP     string
	aa       bool
	path     string // the results file
	log      io.Writer
}

func (r *e3Runner) fresh(c e3Result) bool {
	return c.Err == "" && c.Arm == r.arm && c.Ledger == r.keyL && c.Prompt == r.keyP && (!r.aa || c.B != nil)
}

// do runs the ids in order and returns one result each. The results file is
// rewritten after every offer, and an offer whose cached result is still valid
// is not run again, so an interrupted run resumes where it stopped.
func (r *e3Runner) do(ids []string) []e3Result {
	f := loadE3(r.path)
	var out []e3Result
	for _, id := range ids {
		if c, ok := f.get(id); ok && r.fresh(c) {
			fmt.Fprintf(r.log, "  %s: cached\n", id)
			out = append(out, c)
			continue
		}
		res := r.one(id)
		if res.Err != "" {
			fmt.Fprintf(r.log, "  %s: %s\n", id, res.Err)
		}
		f.Arm = r.arm
		f.put(res)
		if err := f.save(r.path); err != nil {
			fmt.Fprintf(r.log, "  %s: results file: %v\n", id, err)
		}
		out = append(out, res)
	}
	return out
}

func (r *e3Runner) one(id string) (res e3Result) {
	defer func() {
		if p := recover(); p != nil {
			res.Err = fmt.Sprint("panic: ", p)
			res.Metrics, res.B = nil, nil
		}
	}()
	res = e3Result{Offer: id, Arm: r.arm, Ledger: r.keyL, Prompt: r.keyP}
	o, err := r.offer(id)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	t0 := time.Now()
	out, err := r.run(o)
	if err != nil {
		res.Err, res.Company, res.Score = err.Error(), o.Company, o.Score
		return res
	}
	wall := time.Since(t0)
	s := scoreOffer(o, out, r.led, r.inferMin)
	s.Arm, s.Ledger, s.Prompt = r.arm, r.keyL, r.keyP
	s.WallS, s.AdaptS, s.Fallback = wall.Seconds(), out.AdaptWall.Seconds(), out.Fallback
	if r.aa {
		out2, err := r.run(o)
		if err != nil {
			s.Err = "second run: " + err.Error()
			return s
		}
		s.B = scoreOffer(o, out2, r.led, r.inferMin).Metrics
	}
	return s
}

// ── report ──────────────────────────────────────────────────────────────────

func cell(m map[string]float64, name string, w int) string {
	if v, ok := m[name]; ok {
		return fmt.Sprintf(" %*.2f", w, v)
	}
	return fmt.Sprintf(" %*s", w, "-")
}

func meanOf(rs []e3Result, name string) (float64, int) {
	var xs []float64
	for _, r := range rs {
		if v, ok := r.Metrics[name]; ok {
			xs = append(xs, v)
		}
	}
	return mean(xs), len(xs)
}

// floorOf is the A/A floor of a metric over the offers run twice.
func floorOf(rs []e3Result, name string) (floor, bool) {
	var a, b []float64
	for _, r := range rs {
		x, okA := r.Metrics[name]
		y, okB := r.B[name]
		if okA && okB {
			a, b = append(a, x), append(b, y)
		}
	}
	return aaFloor(a, b), len(a) > 0
}

// report prints the per-offer table, the summary against the baseline arm's
// results (paired by offer) and the A/A floor, and the timing check.
func report(w io.Writer, arm string, rs, base []e3Result) {
	var ok, bad []e3Result
	for _, r := range rs {
		if r.Err != "" {
			bad = append(bad, r)
		} else {
			ok = append(ok, r)
		}
	}
	fmt.Fprintf(w, "gate tailor — arm %s, %d offer(s)", arm, len(ok))
	if len(bad) > 0 {
		fmt.Fprintf(w, ", %d FAILED", len(bad))
	}
	if n := countWhere(ok, func(r e3Result) bool { return r.Fallback }); n > 0 {
		fmt.Fprintf(w, ", %d fell back to verbatim (their metrics are verbatim's)", n)
	}
	fmt.Fprint(w, "\n\n")

	var cols []metric
	for _, m := range suite {
		if m.na == "" {
			cols = append(cols, m)
		}
	}
	fmt.Fprintf(w, "%-6s %5s %7s %7s", "offer", "score", "wall_s", "adapt_s")
	for _, c := range cols {
		fmt.Fprintf(w, " %*s", len(c.name), c.name)
	}
	fmt.Fprintln(w)
	for _, r := range ok {
		fmt.Fprintf(w, "%-6s %5.1f %7.1f %7.1f", r.Offer, r.Score, r.WallS, r.AdaptS)
		for _, c := range cols {
			fmt.Fprint(w, cell(r.Metrics, c.name, len(c.name)))
		}
		fmt.Fprintln(w)
	}
	for _, r := range bad {
		fmt.Fprintf(w, "%-6s FAILED: %s\n", r.Offer, r.Err)
	}

	baseBy := map[string]e3Result{}
	for _, r := range base {
		if r.Err == "" {
			baseBy[r.Offer] = r
		}
	}
	fmt.Fprintf(w, "\n%-20s %9s", "metric", "mean")
	if arm != "verbatim" && len(baseBy) > 0 {
		fmt.Fprintf(w, " %9s %9s", "verbatim", "paired Δ")
	}
	fmt.Fprintf(w, "  %s\n", "A/A floor (mean |Δ|, max)")
	for _, m := range suite {
		if m.na != "" {
			fmt.Fprintf(w, "%-20s %s\n", m.name, m.na)
			continue
		}
		mu, n := meanOf(ok, m.name)
		if n == 0 {
			fmt.Fprintf(w, "%-20s %9s\n", m.name, "-")
			continue
		}
		fmt.Fprintf(w, "%-20s %9.3f", m.name, mu)
		fl, haveFloor := floorOf(ok, m.name)
		if !haveFloor {
			fl, haveFloor = floorOf(base, m.name)
		}
		var delta float64
		paired := false
		if arm != "verbatim" && len(baseBy) > 0 {
			var xs, ys []float64
			for _, r := range ok {
				x, okX := r.Metrics[m.name]
				y, okY := baseBy[r.Offer].Metrics[m.name]
				if okX && okY {
					xs, ys = append(xs, x), append(ys, y)
				}
			}
			if paired = len(xs) > 0; paired {
				delta = mean(xs) - mean(ys)
				fmt.Fprintf(w, " %9.3f %+9.3f", mean(ys), delta)
			} else {
				fmt.Fprintf(w, " %9s %9s", "-", "-")
			}
		}
		switch {
		case !haveFloor:
			fmt.Fprint(w, "  (run with --aa)")
		default:
			fmt.Fprintf(w, "  ± %.3f, max %.3f", fl.Mean, fl.Max)
			if paired && delta != 0 {
				if math.Abs(delta) > fl.Max {
					fmt.Fprint(w, "  beyond floor")
				} else {
					fmt.Fprint(w, "  within floor")
				}
			}
		}
		fmt.Fprintln(w)
	}
	if arm != "verbatim" && len(baseBy) == 0 {
		fmt.Fprintln(w, "\n(no e3-verbatim.json in the cache dir: run --arm=verbatim on the same sample for the paired column)")
	}

	// Budget: the PRD's 6 minutes is the whole pipeline, which this run is not.
	var adapts []float64
	for _, r := range ok {
		if r.AdaptS > 0 {
			adapts = append(adapts, r.AdaptS)
		}
	}
	if len(adapts) > 0 {
		mx := slices.Max(adapts)
		verdict := "within"
		if mx > adaptBudget.Seconds() {
			verdict = "OVER"
		}
		fmt.Fprintf(w, "\nPhase 3 adapt wall: mean %.0f s, max %.0f s over %d offer(s). %s the %.0f s left of the 6 min pipeline\n"+
			"budget once Phase 1 (18 s), Phase 2 (~135 s) and selection (~8 s) are paid (PRD §6.8). The first offer carries\n"+
			"the cold model loads; this run does not measure the whole pipeline.\n", mean(adapts), mx, len(adapts), verdict, adaptBudget.Seconds())
	}
}

func countWhere(rs []e3Result, f func(e3Result) bool) (n int) {
	for _, r := range rs {
		if f(r) {
			n++
		}
	}
	return n
}

// killRule is §8.2's: one offer with an unapproved claim or an entity
// violation stops the arm. It names the offer and the bullets and returns the
// exit code.
func killRule(w io.Writer, rs []e3Result) int {
	bad := 0
	for _, r := range rs {
		if r.Err != "" || (r.Metrics["unapproved"] == 0 && r.Metrics["entity_violations"] == 0) {
			continue
		}
		bad++
		fmt.Fprintf(w, "KILL: offer %s (%s): unapproved %.0f, entity_violations %.0f\n",
			r.Offer, r.Company, r.Metrics["unapproved"], r.Metrics["entity_violations"])
		for _, k := range r.Kill {
			fmt.Fprintf(w, "    %s\n", k)
		}
	}
	if bad > 0 {
		fmt.Fprintf(w, "KILL: %d offer(s) breach the kill rule; the arm stops until the cause is found\n", bad)
		return 1
	}
	fmt.Fprintln(w, "kill rule: clear")
	return 0
}

// firstChanged prints up to n changed lines, original above shipped, so the
// first outputs get read by a person (benchmark rule 7).
func firstChanged(w io.Writer, rs []e3Result, n int) {
	shown := 0
	for _, r := range rs {
		for _, c := range r.Changed {
			if shown == 0 {
				fmt.Fprintf(w, "\nfirst %d changed line(s):\n", n)
			}
			shown++
			fmt.Fprintf(w, "\n  offer %s [%s] %s/%s, %s — %s\n    was: %s\n    now: %s\n",
				r.Offer, c.Entry, c.Kind, c.Class, c.Source, c.Why, c.Original, c.Shipped)
			if shown == n {
				return
			}
		}
	}
	if shown == 0 {
		fmt.Fprintln(w, "\nno line was changed on any offer")
	}
}

// ── entry point ─────────────────────────────────────────────────────────────

const tailorUsage = "usage: gate tailor <home> <cv> [offers-dir-or-list] [--arm=verbatim|adapt] [--aa] [--sample N] [--resample]\n" +
	"  without a list, N offers (default 32) are drawn from the home's state and saved in batch/cache/e3-sample.txt"

func optVal(args []string, i *int, name string) (string, bool) {
	if v, ok := strings.CutPrefix(args[*i], name+"="); ok {
		return v, true
	}
	if args[*i] == name && *i+1 < len(args) {
		*i++
		return args[*i], true
	}
	return "", false
}

func gateTailor(args []string) int {
	aa, resample := false, false
	armName, n := "verbatim", 32
	var pos []string
	for i := 0; i < len(args); i++ {
		if v, ok := optVal(args, &i, "--arm"); ok {
			armName = v
		} else if v, ok := optVal(args, &i, "--sample"); ok {
			k, err := strconv.Atoi(v)
			if err != nil || k < 1 {
				fmt.Fprintln(os.Stderr, "--sample wants a positive number")
				return 2
			}
			n = k
		} else if args[i] == "--aa" {
			aa = true
		} else if args[i] == "--resample" {
			resample = true
		} else {
			pos = append(pos, args[i])
		}
	}
	mk, ok := arms[armName]
	if !ok || len(pos) < 2 || len(pos) > 3 {
		fmt.Fprintln(os.Stderr, tailorUsage)
		return 2
	}
	env, err := newTailorEnv(pos[0], pos[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer os.RemoveAll(env.tmp)

	var ids []string
	if len(pos) == 3 {
		ids, err = readOfferIDs(pos[2])
	} else {
		ids, err = env.sample(os.Stdout, n, resample)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(ids) == 0 {
		fmt.Fprintln(os.Stderr, "no offers to run")
		return 2
	}

	led, err := ledger.Load(env.l.CVLedger())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	lb, _ := os.ReadFile(env.l.CVLedger())
	keyP := ""
	if armName == "adapt" {
		pb, _ := os.ReadFile(env.l.Prompt("p3-rewrite.md"))
		keyP = short(string(pb), minicheck.Model)
	}
	path := e3Path(env.l, armName)
	r := &e3Runner{
		arm: armName, run: mk(env), offer: env.offer, led: led,
		inferMin: env.cfg.Thresholds.InferMinScore,
		keyL:     short(string(lb), env.cfg.CVRaw), keyP: keyP,
		aa: aa, path: path, log: os.Stderr,
	}
	rs := r.do(ids)

	base := loadE3(e3Path(env.l, "verbatim")).Offers
	withCachedATS(env.l, func(id string) (string, error) { o, err := env.offer(id); return o.Text, err }, rs, base)
	report(os.Stdout, armName, rs, base)
	fmt.Println()
	code := killRule(os.Stdout, rs)
	firstChanged(os.Stdout, rs, 5)
	fmt.Printf("\nresults: %s\n", path)
	if code == 0 && countWhere(rs, func(r e3Result) bool { return r.Err != "" }) > 0 {
		return 1
	}
	return code
}
