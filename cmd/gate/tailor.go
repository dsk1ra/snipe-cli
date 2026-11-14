package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/adapt"
	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// gate tailor is E0 of docs/PRD-TAILORING-REWORK.md: the §8.1 suite, in Go, on
// a fixed sample of offers, over the TEXT OF THE RENDERED PDF (benchmark rule
// 11 — the bench scores the document production prints, not the JSON that fed
// it). Paired, with an A/A floor so a later arm's delta can be read against the
// noise of the measurement itself.
//
// A metric is computed when na is empty; otherwise the table prints na in its
// place. Values live in e3Result.Metrics by name, and a metric with nothing to
// measure on an offer (no must-have the ledger supports, say) is absent, not 0.
// tailor_e3.go computes them and runs the E3 loop.
type metric struct {
	name string
	na   string // what the table says in place of a number
}

const needsJudge = "n/a (needs judge)"

var suite = []metric{
	{name: "one_page"},
	{name: "lines"},
	{name: "exp_starved"},
	{name: "front_load"},
	{name: "shape_rejects"},
	{name: "rewrite_rate"},
	{name: "supported_share"},
	{name: "inferred_share"},
	{name: "unapproved"},
	{name: "entity_violations"},
	{name: "keep_rate"},
	{name: "term_parity"},
	{name: "boolean_pass"},
	{name: "must_have_evidence", na: needsJudge},
	{name: "pairwise_pref", na: needsJudge},
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// offer is everything an arm may read about one posting.
type offer struct {
	ID      string // zero-padded report number
	Report  string
	JD      string
	Text    string // the report's text
	Role    string
	Score   float64 // Phase 2's
	Company string
	Reqs    []string // Block B, or the cleaned posting when the report has none
}

// armOut is what an arm hands back for one offer: the text of its PDF, and what
// the metrics need to read it against the ledger.
type armOut struct {
	Page      string
	Selected  []string         // the selected lines as cv.md has them, before any rewrite
	Truth     *adapt.TruthFile // nil for verbatim, and when adapt fell back to it
	Stats     *adapt.Stats     // the rewrite call's counts; nil when none ran
	AdaptWall time.Duration    // Tailor.Adapt alone: plan, rewrite, verify, select
	Fallback  bool             // adapt was asked for and shipped verbatim
}

// Arm renders one offer.
type Arm func(o offer) (armOut, error)

// arms are the registered arms, built once the CV and home are known.
// "adapt" is E3's arm: the same selection and render as verbatim, with the
// lines rewritten through phase3.Tailor.Adapt, the code --writer adapt runs.
var arms = map[string]func(*tailorEnv) Arm{
	"verbatim": (*tailorEnv).verbatim,
	"adapt":    (*tailorEnv).adapt,
	"grow":     (*tailorEnv).grow,
}

// ── pure: metrics from text ─────────────────────────────────────────────────

type pageMetrics struct {
	Pages      int
	Lines      int
	ExpStarved int
	FrontLoad  float64
	Bullets    int
}

// frontWords is how far into a bullet a requirement term still counts as
// leading it. An ATS snippet and a skimming recruiter both read the front.
const frontWords = 10

// measure scores the text pdftotext -layout printed. terms are the posting's
// requirement tokens; none means front_load is 0, not undefined.
func measure(pdfText string, terms map[string]bool) pageMetrics {
	var m pageMetrics
	var all []string
	for _, p := range strings.Split(pdfText, "\f") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		m.Pages++
		all = append(all, strings.Split(p, "\n")...)
	}
	for _, l := range all {
		if strings.TrimSpace(l) != "" {
			m.Lines++
		}
	}

	var hits int
	for name, lines := range sections(all) {
		isExp := expHeadRe.MatchString(name)
		if !isExp && !projHeadRe.MatchString(name) {
			continue
		}
		for _, e := range entries(lines) {
			if isExp && len(e) <= 1 {
				m.ExpStarved++
			}
			for _, b := range e {
				m.Bullets++
				if hasTerm(b, terms) {
					hits++
				}
			}
		}
	}
	if m.Bullets > 0 {
		m.FrontLoad = float64(hits) / float64(m.Bullets)
	}
	return m
}

var (
	expHeadRe  = regexp.MustCompile(`(?i)experience|employment`)
	projHeadRe = regexp.MustCompile(`(?i)project`)
)

func hasTerm(bullet string, terms map[string]bool) bool {
	w := strings.Fields(bullet)
	if len(w) > frontWords {
		w = w[:frontWords]
	}
	for _, t := range text.Tokenize(strings.Join(w, " ")) {
		if terms[t] {
			return true
		}
	}
	return false
}

// terms are the tokens of the posting's requirements.
func terms(reqs []string) map[string]bool { return text.TokenSet(strings.Join(reqs, "\n")) }

// sections groups the lines under their headings. The template sets a heading
// flush left and everything else at least one space in, so a line starting in
// column 0 is a heading; the name and contact block above the first heading are
// indented and fall under "".
func sections(lines []string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, l := range lines {
		if l != "" && l[0] != ' ' && l[0] != '\t' {
			cur = strings.TrimSpace(l)
			continue
		}
		out[cur] = append(out[cur], l)
	}
	return out
}

func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

// entries splits one section into its entries, each a list of bullet texts. An
// entry's own lines (company, role, project title) sit at the section's
// shallowest indent; a bullet is a deeper line opening with a bullet glyph, and
// a deeper line without one continues the bullet above it.
//
// ponytail: the tech line under a project is indistinguishable from a wrapped
// bullet in text, so it is glued to the last bullet's tail. It only matters to
// a bullet shorter than frontWords.
func entries(lines []string) [][]string {
	lo := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if i := indent(l); lo < 0 || i < lo {
			lo = i
		}
	}
	var out [][]string
	headLine := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if indent(l) == lo {
			if !headLine {
				out = append(out, nil)
			}
			headLine = true
			continue
		}
		headLine = false
		if len(out) == 0 {
			continue
		}
		e := &out[len(out)-1]
		if rest, ok := strings.CutPrefix(t, "•"); ok {
			*e = append(*e, strings.TrimSpace(rest))
		} else if len(*e) > 0 {
			(*e)[len(*e)-1] += " " + t
		}
	}
	return out
}

// ── pure: offers and the A/A floor ──────────────────────────────────────────

var offerIDRe = regexp.MustCompile(`(?:^|[_-])(\d{3})(?:[_.-]|$)`)

// parseOfferIDs reads report numbers from file names (`001-keyrock-….md`,
// `2026-07-30_keyrock_001`) or bare numbers, sorted and unique.
func parseOfferIDs(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n, _, _ = strings.Cut(n, "#") // e3-sample.txt carries "061  # 4.3 Acme"
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		id := ""
		if m := offerIDRe.FindStringSubmatch(n); m != nil {
			id = m[1]
		} else if k, err := strconv.Atoi(n); err == nil {
			id = fmt.Sprintf("%03d", k)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// floor is the A/A noise on one metric: how far two runs of the same arm
// disagree, paired by offer.
type floor struct{ Mean, Max float64 }

func aaFloor(a, b []float64) floor {
	var f floor
	if len(a) == 0 {
		return f
	}
	for i := range a {
		d := math.Abs(a[i] - b[i])
		f.Mean += d
		f.Max = math.Max(f.Max, d)
	}
	f.Mean /= float64(len(a))
	return f
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// ── IO ──────────────────────────────────────────────────────────────────────

type tailorEnv struct {
	l   paths.Layout
	cfg *config.Config
	tpl string
	t   *phase3.Tailor
	tmp string
}

func readOfferIDs(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var names []string
	if st.IsDir() {
		ents, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			names = append(names, e.Name())
		}
	} else {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		names = strings.Split(string(b), "\n")
	}
	return parseOfferIDs(names), nil
}

// gateClient is the client for a gate's model calls. A gate repeats one call
// across postings and the model reuses the prefix they share. SNIPE_GATE_COLD=1
// unloads the model before every call, so each prompt is read from the start
// by a server that remembers nothing (see ollama.Client.Cold). Run
// a gate both ways: the difference is noise that rerunning the same binary
// cannot show, because a rerun repeats the same call states.
func gateClient() *ollama.Client {
	c := ollama.New("", nil)
	c.Cold = os.Getenv("SNIPE_GATE_COLD") != ""
	return c
}

// newTailorEnv reads the CV the gate is told to use, not the home's cv.md: two
// runs have to rank the same document.
func newTailorEnv(home, cvPath string) (*tailorEnv, error) {
	// SNIPE_CODE points the prompts and templates at a dev tree when the home is
	// the live install, whose pinned checkout may predate the prompt being tested.
	code := home
	if v := os.Getenv("SNIPE_CODE"); v != "" {
		code = v
	}
	l := paths.Layout{Home: home, Code: code}
	cfg, err := config.Load(l)
	if err != nil {
		return nil, err
	}
	cv, err := os.ReadFile(cvPath)
	if err != nil {
		return nil, err
	}
	cfg.CVRaw = string(cv)
	tpl, err := os.ReadFile(l.Template("cv-template.tex"))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "snipe-gate-tailor-")
	if err != nil {
		return nil, err
	}
	return &tailorEnv{
		l: l, cfg: cfg, tpl: string(tpl), tmp: tmp,
		t: &phase3.Tailor{L: l, Cfg: cfg, OL: gateClient()},
	}, nil
}

var reportNameRe = regexp.MustCompile(`^(\d{3})-.*\.md$`)

// offer finds a report number's report, its posting and its role. The posting
// is filed under the state row's id, which is not the report number.
func (e *tailorEnv) offer(id string) (offer, error) {
	ents, err := os.ReadDir(e.l.Reports())
	if err != nil {
		return offer{}, err
	}
	o := offer{ID: id}
	for _, f := range ents {
		if m := reportNameRe.FindStringSubmatch(f.Name()); m != nil && m[1] == id {
			o.Report = filepath.Join(e.l.Reports(), f.Name())
			break
		}
	}
	if o.Report == "" {
		return offer{}, fmt.Errorf("no report %s", id)
	}
	rep, err := os.ReadFile(o.Report)
	if err != nil {
		return offer{}, err
	}
	rows, err := state.Load(e.l.State())
	if err != nil {
		return offer{}, err
	}
	row, ok := findRow(rows, id)
	if !ok {
		return offer{}, fmt.Errorf("report %s has no state row", id)
	}
	jd, err := os.ReadFile(filepath.Join(e.l.JDs(), strconv.Itoa(row.ID)+".txt"))
	if err != nil {
		return offer{}, fmt.Errorf("no job description: %w", err)
	}
	o.JD, o.Text = string(jd), string(rep)
	if ev := state.NewCache().Eval(e.l.Evals(), row.ID); ev != nil {
		o.Role, o.Score, o.Company = ev.Role, ev.Score, ev.Company
	}
	o.Reqs = cvselect.ExtractBlockBRequirements(string(rep))
	if len(o.Reqs) == 0 {
		o.Reqs = []string{text.CleanJD(o.JD, 1500)}
	}
	return o, nil
}

func findRow(rows map[int]state.Row, reportNum string) (state.Row, bool) {
	ids := make([]int, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if rows[id].ReportNum == reportNum {
			return rows[id], true
		}
	}
	return state.Row{}, false
}

// verbatim is the shipped Phase 3 path: score, select, render through the
// density ladder, then read the PDF back. Same calls as Tailor.compile — that
// one is unexported and also writes into the output folder.
func (e *tailorEnv) verbatim() Arm { return e.arm(false) }

// adapt is verbatim with the selected lines rewritten between select and render.
// Without a checkpoint nothing is cached, and without Queue the home's ledger
// is not written to.
func (e *tailorEnv) adapt() Arm { return e.arm(true) }

// grow is adapt with Tailor.FramingGrow: a line may take one more rendered line
// to add an approved framing a must-have asks for, and the density ladder pays
// for it elsewhere on the page.
func (e *tailorEnv) grow() Arm {
	e.t.FramingGrow = true
	return e.arm(true)
}

func (e *tailorEnv) arm(adapted bool) Arm {
	return func(o offer) (armOut, error) {
		ctx := context.Background()
		score, err := e.t.ScoreBullets(ctx, o.Reqs)
		if err != nil {
			return armOut{}, err
		}
		content := e.t.Build(score, o.JD)
		out := armOut{Selected: phase3.ContentBullets(content)}
		if adapted {
			// A fallback to verbatim prints its reason, and the metrics then read
			// as verbatim's; the table cannot tell, so Fallback and the log have to.
			var st adapt.Stats
			t0 := time.Now()
			content, out.Truth, err = e.t.Adapt(ctx, nil, phase3.AdaptJob{
				Report: o.ID, Score: o.Score, JD: o.JD, Content: content,
				Rows: adapt.RowsFromReport(o.Text), Terms: phase3.PostingTerms(nil, o.Text),
				Stats: &st,
			})
			out.AdaptWall = time.Since(t0)
			if err != nil {
				return armOut{}, err
			}
			out.Fallback = out.Truth == nil
			if st.Rejects != nil {
				out.Stats = &st
			}
		}
		var approved []string
		for _, forms := range e.cfg.Aliases {
			approved = append(approved, forms...)
		}
		pdf := filepath.Join(e.tmp, o.ID+".pdf")
		_, err = phase3.Run(ctx, phase3.Params{
			Template: e.tpl, CV: fill.ParseCV(e.cfg.CVRaw), Content: content,
			Candidate: e.cfg.Candidate, CVText: e.cfg.CVRaw, JDText: o.JD,
			TargetRole: o.Role, OutPath: pdf, Approved: approved,
		})
		if err != nil {
			return armOut{}, err
		}
		out.Page, err = pdfText(ctx, pdf)
		return out, err
	}
}

// pdfText is what a reader, or an ATS, gets out of the file. -layout keeps the
// indentation that tells headings, entries and bullets apart.
func pdfText(ctx context.Context, path string) (string, error) {
	b, err := exec.CommandContext(ctx, "pdftotext", "-layout", path, "-").Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext: %w", err)
	}
	return string(b), nil
}
