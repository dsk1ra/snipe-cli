package phase3

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/guards"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Tailor is the Phase 3 worker: one evaluated offer in, one tailored PDF out.
//
// It replaces batch/local-pdf-offer.mjs on the shipped path only — the
// `--writer verbatim` one, with no model generation call. The 7B tailor arm
// survives in the old repo as a benchmark control (PORT-PLAN.md §2) and is not
// ported.
type Tailor struct {
	L     paths.Layout
	Cfg   *config.Config
	OL    *ollama.Client
	Store *state.Store
	Cache *state.Cache

	// spikeMu serialises the spike rebuild across Phase 3 workers. The runner
	// runs two by default and the background is the same for both, so without it
	// they rebuild it simultaneously — twice the GPU, and the loser's write
	// lands on a file the winner had already made correct.
	spikeMu sync.Mutex
}

// Run tailors one offer. It is a run.Phase.
func (t *Tailor) Run(ctx context.Context, id int, ck *state.Checkpoint) error {
	row, ok := t.Store.Get(id)
	if !ok {
		return fmt.Errorf("no state row")
	}
	ev := t.Cache.Eval(t.L.Evals(), id)
	if ev == nil {
		return fmt.Errorf("no eval artifact")
	}
	reportPath, ok := t.Cache.ReportFile(t.L.Reports(), row.ReportNum)
	if !ok {
		return fmt.Errorf("no report for %q", row.ReportNum)
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	jd, err := os.ReadFile(filepath.Join(t.L.JDs(), strconv.Itoa(id)+".txt"))
	if err != nil {
		return fmt.Errorf("no job description: %w", err)
	}
	tpl, err := os.ReadFile(t.L.Template("cv-template.tex"))
	if err != nil {
		return err
	}

	// What the selection ranks against. Block B when the report has one, the
	// cleaned posting when it does not — ranking against the whole JD is a
	// blunter query but a far better one than ranking against nothing, which is
	// what an empty query list would mean.
	reqs := cvselect.ExtractBlockBRequirements(string(report))
	if len(reqs) == 0 {
		reqs = []string{text.CleanJD(string(jd), 1500)}
		fmt.Printf("  #%d: report has no Block B — ranking against the posting itself\n", id)
	}

	// The CV hash is part of the stage input for the same reason p2.evidence
	// carries it: the cached value is a map keyed by bullet TEXT. Edit cv.md
	// between a stop and the resume and every reworded bullet misses the map,
	// scores 0, sorts last and gets cut — silently, on the offer the pause was
	// protecting.
	score, err := state.Staged(ctx, ck, "p3.select",
		selectInput{Reqs: reqs, CVHash: t.Cfg.Hash()},
		func(ctx context.Context, in selectInput) (map[string]float64, error) {
			return t.ScoreBullets(ctx, in.Reqs)
		})
	if err != nil {
		return err
	}

	content := t.Build(score, string(jd))

	date := time.Now().Format("2006-01-02")
	dir := filepath.Join(t.L.Output(), fmt.Sprintf("%s_%s_%s", date, slugify(orUnknown(ev.Company)), row.ReportNum))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return err
	}
	if err := state.WriteAtomic(filepath.Join(dir, "cv-content.json"), body); err != nil {
		return err
	}
	if err := state.WriteAtomic(filepath.Join(dir, "job-description.txt"), jd); err != nil {
		return err
	}

	name := strings.ReplaceAll(strings.TrimSpace(t.Cfg.Candidate.FullName), " ", "-")
	if name == "" {
		name = "Candidate"
	}
	pdfPath := filepath.Join(dir, name+"-CV.pdf")
	fit, err := Run(ctx, Params{
		Template: string(tpl), CV: fill.ParseCV(t.Cfg.CVRaw), Content: content,
		Candidate: t.Cfg.Candidate, CVText: t.Cfg.CVRaw, JDText: string(jd),
		TargetRole: ev.Role, OutPath: pdfPath,
	})
	if err != nil {
		return err
	}
	if err := state.WriteAtomic(filepath.Join(dir, "source.tex"), []byte(fit.Tex)); err != nil {
		return err
	}

	rel, _ := filepath.Rel(t.L.Home, pdfPath)
	t.backfillReport(reportPath, string(report), rel)
	t.writeTracker(id, ev, row, date, filepath.Base(reportPath), rel)

	fmt.Printf("  #%d → %s (%d page(s), density step %d)\n", id, rel, fit.Pages, fit.Step)
	return t.Store.Commit(id, func(r *state.Row) {
		r.P3Status = state.P3Completed
		r.Error = ""
	})
}

type selectInput struct {
	Reqs   []string `json:"reqs"`
	CVHash string   `json:"cv_hash"`
}

// Build turns a scored CV into the tailored content the template is filled
// from. Separated from Run so the P3 gate can drive selection and the writer
// without a LaTeX installation or a state file — which is the only way to
// compare it against the oracle.
func (t *Tailor) Build(score map[string]float64, jd string) fill.Content {
	opt := cvselect.DefaultOptions()
	opt.PinnedProjects = t.Cfg.PinnedProjects
	// 3 projects, not the library default's 4. Four spent ~38px each on a title,
	// a badge and a tech line to deliver one bullet apiece: 43% of the largest
	// section on the page was chrome. Dropping one and spending its chrome on
	// bullets measured +0.116 differentiator coverage (21-5, p=0.002), -0.063
	// noise, +0.071 grade yield, with mean bullets 2.69 -> 3.44. It is what
	// local-pdf-offer.mjs passes in production (SNIPE_MAX_PROJECTS), and leaving
	// it at 4 here made the density ladder fire to step 3 on the first offer.
	opt.MaxProjects = 3
	sel := cvselect.Select(t.Cfg.CVRaw, func(b string) float64 { return score[b] }, opt)

	c := VerbatimContent(sel.CV, t.Cfg.CVRaw, jd, 4)

	// The length floor is asserted a second time, against the FULL cv.md, which
	// is where local-pdf-offer.mjs asserts it — after the guards, not inside the
	// writer. It matters: the writer only sees the SELECTED CV, so a project the
	// selector cut to one bullet can only pad to that one bullet's length and
	// ships under the floor. Topping up from the whole CV is safe because padded
	// text is verbatim cv.md — it names no product the CV lacks and quotes no
	// figure the project's own entry lacks, so it passes both fabrication guards
	// by construction rather than by inspection.
	if picks, ok := c.ProjectPicks(); ok {
		c = c.WithProjects(guards.PadProjectDescriptions(picks, t.Cfg.CVRaw, 35, 55))
	}
	c.EducationModules = EducationModules(t.Cfg.CVRaw, jd, 5)
	return c
}

// ScoreBullets is the production Scorer: each bullet's best cosine against the
// posting's requirements, less its corpus baseline.
//
// One batched embed for the queries and every bullet together, exactly as
// selectCvForJd does it — the Node made the same arithmetic over one call, and
// splitting it would change nothing except the number of round trips.
//
// The bullet is embedded bare. Prefixing it with its entry name injects a
// constant into every vector of that entry, which pulls the whole entry toward
// one point and flattens the ranking: pair accuracy 0.689 prefixed against
// 0.757 bare, better on 11 of 12 offers.
func (t *Tailor) ScoreBullets(ctx context.Context, reqs []string) (map[string]float64, error) {
	bullets := cvBullets(t.Cfg.CVRaw)
	out := make(map[string]float64, len(bullets))
	if len(bullets) == 0 || len(reqs) == 0 {
		return out, nil
	}
	vecs, err := t.embed(ctx, append(append([]string{}, reqs...), bullets...))
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(reqs)+len(bullets) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(reqs)+len(bullets))
	}
	qv, bv := vecs[:len(reqs)], vecs[len(reqs):]
	scores := make([]float64, len(bullets))
	for i := range bullets {
		scores[i] = maxCosine(qv, bv[i])
	}

	// Subtract each bullet's corpus mean, so the score reads "more than postings
	// usually like this" rather than "matches this posting". Dividing by (1+w)
	// keeps cosine on its original scale — applied raw, w=6 would inflate the
	// cosine part sevenfold, which matters the moment a judge term calibrated
	// against unscaled cosine is added back.
	if w := cvselect.DefaultOptions().SpikeWeight; w > 0 {
		if bg := t.spikeBackground(ctx, bullets, t.fingerprint(ctx)); bg != nil {
			alpha := cvselect.SpikeAlpha(w)
			for i := range scores {
				scores[i] -= alpha * bg[i]
			}
		}
	}
	for i, b := range bullets {
		out[b] = scores[i]
	}
	return out, nil
}

// cvBullets is every bullet the selector will score, in the order Select walks
// them — Experience first, then Projects.
func cvBullets(cv string) []string {
	var out []string
	for _, name := range []string{"Experience", "Projects"} {
		for _, s := range cvselect.ParseSections(cv) {
			if s.Name != name {
				continue
			}
			for _, e := range cvselect.ParseEntries(s.Lines).Entries {
				out = append(out, e.Bullets...)
			}
		}
	}
	return out
}

func (t *Tailor) embed(ctx context.Context, texts []string) ([][]float64, error) {
	return embed.Batch(ctx, t.OL, texts, ollama.ModelEmbed)
}

// fingerprint identifies what actually produced the vectors, so a rebuilt
// embedding model invalidates the spike cache instead of silently keeping it.
func (t *Tailor) fingerprint(ctx context.Context) string {
	var show struct {
		Details struct {
			ParentModel   string `json:"parent_model"`
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
		} `json:"details"`
	}
	if err := t.OL.Show(ctx, ollama.ModelEmbed, &show); err != nil {
		return ollama.ModelEmbed
	}
	parent := show.Details.ParentModel
	if parent == "" {
		parent = ollama.ModelEmbed
	}
	return strings.Join([]string{ollama.ModelEmbed, parent, show.Details.ParameterSize, show.Details.Quantization}, "|")
}

func maxCosine(qs [][]float64, v []float64) float64 {
	best := math.Inf(-1)
	for _, q := range qs {
		if s, err := embed.Cosine(q, v); err == nil && s > best {
			best = s
		}
	}
	if math.IsInf(best, -1) {
		return 0
	}
	return best
}

var pdfLineRe = regexp.MustCompile(`\*\*PDF:\*\*[^\n]*`)

// backfillReport points the report's **PDF:** line at the file that now exists.
// Best effort: the CV is on disk either way, and a report that still says "to be
// generated" is worth less than failing the offer over.
func (t *Tailor) backfillReport(path, body, rel string) {
	updated := pdfLineRe.ReplaceAllLiteralString(body, "**PDF:** "+rel)
	if updated != body {
		_ = state.WriteAtomic(path, []byte(updated))
	}
}

// writeTracker drops the offer's row in batch/tracker-additions/, which the
// merge step folds into data/applications.md. Same nine columns, same order.
func (t *Tailor) writeTracker(id int, ev *state.Artifact, row state.Row, date, reportFile, rel string) {
	num := nextTrackerNum(t.L.Tracker())
	line := strings.Join([]string{
		strconv.Itoa(num), date, orUnknown(ev.Company), orUnknown(ev.Role),
		"Evaluated", fmt.Sprintf("%g/5", ev.Score), "Y",
		fmt.Sprintf("[%s](reports/%s)", row.ReportNum, reportFile),
		fmt.Sprintf("PDF generated — score %g/5 — %s", ev.Score, row.URL),
	}, "\t")
	_ = os.MkdirAll(t.L.Additions(), 0o755)
	_ = state.WriteAtomic(filepath.Join(t.L.Additions(), strconv.Itoa(id)+".tsv"), []byte(line+"\n"))
}

var trackerNumRe = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`)

func nextTrackerNum(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 1
	}
	max := 0
	for _, m := range trackerNumRe.FindAllStringSubmatch(string(b), -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
