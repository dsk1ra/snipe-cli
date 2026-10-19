package phase2

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/text"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

// coverageBlend is how much of cv_match comes from the continuous coverage
// metric rather than the model's integer. It is what turns three buckets into a
// real ranking.
//
// 0.4, not 0.5 — a measured constant, so do not round it to something tidier.
const coverageBlend = 0.4

type Evaluator struct {
	L     paths.Layout
	Cfg   *config.Config
	OL    *ollama.Client
	IX    *embed.Indexer
	Store *state.Store
	Model string
	// NumCtx is the window for stages 1 and 2. Stage 3 sets its own 12288: it
	// emits every content block at once and needs the room, while the two
	// earlier calls are small and a wider window is not free.
	NumCtx  int
	Timeout time.Duration
	RAG     bool

	// ReportNum is allocated by the caller before the run, so a report and its
	// tracker row cannot disagree about the number.
	ReportNum string
	Threshold float64

	calib []Calibration
}

func (e *Evaluator) prompt(name string) (string, error) { return config.PromptLines(e.L, name) }

// Eval is what lands in batch/evals/<id>.json.
type Eval struct {
	Status         string       `json:"status"`
	ID             string       `json:"id"`
	URL            string       `json:"url"`
	ReportNum      string       `json:"report_num"`
	ReportPath     string       `json:"report_path"`
	ReportFilename string       `json:"report_filename"`
	Company        string       `json:"company"`
	Role           string       `json:"role"`
	CVMatch        int          `json:"cv_match"`
	CVCoverage     float64      `json:"cv_coverage"`
	CVBlended      float64      `json:"cv_blended"`
	NorthStar      int          `json:"north_star"`
	CompInferred   *int         `json:"comp_inferred"`
	RedFlagsScore  int          `json:"red_flags_score"`
	Score          float64      `json:"score"`
	Archetype      string       `json:"archetype"`
	FinalDecision  string       `json:"final_decision"`
	HardStops      []string     `json:"hard_stops"`
	SoftGaps       []string     `json:"soft_gaps"`
	TopStrengths   []string     `json:"top_strengths"`
	PDFDecision    bool         `json:"pdf_decision"`
	Notes          string       `json:"notes"`
	SalaryPosted   *text.Salary `json:"salary_posted"`
	Evaluator      string       `json:"evaluator"`
	Error          *string      `json:"error"`
}

// Run evaluates one offer through the three stages.
//
// Each stage is checkpointed on a hash of its input, so a stop between them
// resumes at the next one. Stage 3 is the expensive call; losing it to a Ctrl-C
// was the single biggest cost of the bash runner.
func (e *Evaluator) Run(ctx context.Context, id int, ck *state.Checkpoint) error {
	row, _ := e.Store.Get(id)
	jdPath := filepath.Join(e.L.JDs(), strconv.Itoa(id)+".txt")
	jdBytes, err := os.ReadFile(jdPath)
	if err != nil {
		return fmt.Errorf("JD not cached for #%d (run Phase 1 first — the scorer writes batch/jds/<id>.txt)", id)
	}
	jd := strings.TrimSpace(string(jdBytes))
	if jd == "" {
		return fmt.Errorf("JD for #%d is empty", id)
	}

	// The embedder block runs before the 30B is ever loaded.
	//
	// Only one model stays resident in 6 GB, so every switch between the 18.5 GB
	// 30B and the 0.6 GB embedder costs a reload — measured at 4 reloads across
	// 5 calls on Phase 3. Everything the embedder can do without stage 1's
	// output therefore happens here, not between the two 30B calls.
	atoms := embed.ExtractAtoms(e.Cfg.CVRaw, embed.ReadFileOr(e.L.Digest()))
	cvIndex, err := e.IX.LoadCV(ctx, atoms)
	if err != nil {
		return fmt.Errorf("loading the CV index: %w", err)
	}
	var calib []Calibration
	if e.RAG {
		calib, err = e.similarPastOffers(ctx, jd, id, 3)
		e.calib = calib
		if err != nil {
			// Calibration is an anchor, not a dependency: losing it costs
			// accuracy, not the evaluation.
			fmt.Fprintf(os.Stderr, "[phase2] calibration RAG failed for #%d: %v\n", id, err)
			calib, e.calib = nil, nil
		}
	}

	cvHash := e.Cfg.Hash()

	parsed, err := state.Staged(ctx, ck, "p2.jdparse", jd,
		func(ctx context.Context, jd string) (Parsed, error) { return e.stage1(ctx, id, jd) })
	if err != nil {
		return err
	}

	type evidenceInput struct {
		Reqs   []Requirement `json:"reqs"`
		CVHash string        `json:"cv_hash"`
	}
	rows, err := state.Staged(ctx, ck, "p2.evidence",
		evidenceInput{Reqs: parsed.Requirements, CVHash: cvHash},
		func(ctx context.Context, in evidenceInput) ([]Row, error) {
			return e.stage2(ctx, id, in.Reqs, cvIndex)
		})
	if err != nil {
		return err
	}
	coverage := coverageMetric(rows)

	// Deterministic comp, computed in code. The model is never asked to guess a
	// salary, and a posting that states none must not get one invented.
	salary := text.ExtractSalary(jd)
	compDim, hasComp := text.CompScore(salary, e.Cfg.Comp)

	judgment, err := state.Staged(ctx, ck, "p2.judgment",
		stage3Input{Parsed: parsed, Rows: rows, Coverage: coverage, Calib: calib, Salary: salary, JD: jd, CVHash: cvHash},
		func(ctx context.Context, in stage3Input) (Judgment, error) { return e.stage3(ctx, id, in) })
	if err != nil {
		return err
	}

	// top_strengths is the one field asserting something on the candidate's
	// behalf, and the one that reaches an application. Phase 1 has filtered it
	// for a long time; the staged Phase 2 — the default path — never did, so a
	// strength naming a technology absent from cv.md shipped straight into the
	// report. Measured over the stored evals: about 1% of strengths, every one a
	// neighbour swap.
	kept, dropped := fit.VerifyAgainstCv(judgment.TopStrengths, e.Cfg.CVRaw)
	judgment.TopStrengths = kept
	if len(dropped) > 0 {
		fmt.Fprintf(os.Stderr, "[phase2] #%d dropped %d unverifiable strength(s): %s\n",
			id, len(dropped), strings.Join(dropped, " | "))
	}

	res := e.compose(id, row.URL, jd, parsed, rows, coverage, judgment, salary, compDim, hasComp)
	return e.commit(ctx, id, res)
}

// composed carries everything the report and the artifact need.
type composed struct {
	eval   Eval
	report string
}

// compose computes the score in code and applies every cap.
func (e *Evaluator) compose(id int, url, jd string, parsed Parsed, rows []Row, coverage Coverage,
	j Judgment, salary *text.Salary, compDim int, hasComp bool) composed {

	modelCV := clampDim(j.CVMatch, 1)
	ns := e.Cfg.NorthStar(j.Archetype)
	rf := clampDim(j.RedFlagsScore, 5)

	// Blend the continuous coverage metric into cv_match.
	coverage5 := 1 + 4*coverage.Coverage
	cvBlend := float64(modelCV)*(1-coverageBlend) + coverage5*coverageBlend

	sen := fit.SeniorityCaps(parsed.Role, jd, parsed.YearsRequired, parsed.SeniorityLevel)
	cvBlend = math.Min(cvBlend, float64(sen.CV))
	ns = minInt(ns, sen.NS)

	cvBlend = math.Min(cvBlend, float64(fit.StackMismatchCap(jd, e.Cfg.CVRaw, 0, 0).Cap))

	// A required natural language the candidate lacks is a hard blocker: one Gap
	// row barely moves the coverage average, so it has to be gated in code.
	if lang, missing := fit.LanguageMismatchCap(jd, e.Cfg.CVRaw); missing != "" {
		cvBlend = math.Min(cvBlend, float64(lang.CV))
		ns = minInt(ns, lang.NS)
		j.HardStops = prepend(j.HardStops, "Requires "+missing+" fluency — not in CV languages")
	}
	// Office attendance somewhere the profile will not commute to. The same gate,
	// and it has to be one: hard_stops never reaches final_decision, so Phase 2
	// was printing this exact deal-breaker on offers it then told the user to
	// apply to, with a tailored PDF.
	if loc, city := fit.LocationMismatchCap(jd, e.Cfg.ProfileYML); city != "" {
		cvBlend = math.Min(cvBlend, float64(loc.CV))
		ns = minInt(ns, loc.NS)
		j.HardStops = prepend(j.HardStops, "On-site/hybrid in "+city+" — outside the commutable base")
	}
	// A hiring thread, blog page or homepage is not an evaluable posting, and a
	// capable model happily "matches" the candidate against garbage — an HN
	// thread scored 4.0 before this cap. Either signal trips it: the model's own
	// flag, or the JD advertising several jobs in plain text, which the model
	// missed on one offer six times out of six.
	if !parsed.IsSinglePosting || fit.LooksMultiPosting(jd) {
		cvBlend = math.Min(cvBlend, 2)
		ns = minInt(ns, 2)
		j.HardStops = prepend(j.HardStops, "Not a single job posting — verify the URL manually")
	}
	cvBlend = round(cvBlend, 2)

	var score float64
	if hasComp {
		score = round(cvBlend*0.50+float64(ns)*0.30+float64(compDim)*0.20, 1)
	} else {
		score = round(cvBlend*0.625+float64(ns)*0.375, 1)
	}

	final := orNone(j.FinalDecision, "Consider")
	if score < 3 {
		final = "Skip"
	} else if score < 3.5 && final == "Apply" {
		final = "Consider"
	}

	var comp *int
	if hasComp {
		comp = &compDim
	}
	ev := Eval{
		Status: state.P2Evaled, ID: strconv.Itoa(id), URL: url,
		Company: parsed.Company, Role: parsed.Role,
		CVMatch: modelCV, CVCoverage: coverage.Coverage, CVBlended: cvBlend,
		NorthStar: ns, CompInferred: comp, RedFlagsScore: rf, Score: score,
		Archetype: j.Archetype, FinalDecision: final,
		HardStops: nonNil(j.HardStops), SoftGaps: nonNil(j.SoftGaps), TopStrengths: nonNil(j.TopStrengths),
		Notes: j.Notes, SalaryPosted: salary, Evaluator: "staged",
	}
	today := time.Now().Format("2006-01-02")
	slug := slugify(orNone(parsed.Company, "unknown"))
	ev.ReportFilename = fmt.Sprintf("%s-%s-%s.md", e.ReportNum, slug, today)
	ev.ReportPath = filepath.Join(e.L.Reports(), ev.ReportFilename)
	ev.ReportNum = e.ReportNum
	ev.PDFDecision = score >= e.Threshold

	pdfNote := "to be generated in Phase 3"
	if !ev.PDFDecision {
		// Names the command, not the reason: the reader wants to know what to do
		// next, and the score is three lines above.
		pdfNote = "not generated — run /snipe pdf " + slug + " to create on demand"
	}

	// The machine summary is the report's own header block, and it is what the
	// tracker and Phase 3 read back rather than re-parsing prose.
	machine := mustJSONCompact(machineSummary{
		Company: ev.Company, Role: ev.Role, CVMatch: ev.CVMatch, CVCoverage: ev.CVCoverage,
		CVBlended: ev.CVBlended, NorthStar: ev.NorthStar, CompInferred: ev.CompInferred,
		RedFlagsScore: ev.RedFlagsScore, Score: ev.Score, Archetype: ev.Archetype,
		FinalDecision: ev.FinalDecision, HardStops: ev.HardStops, SoftGaps: ev.SoftGaps,
		TopStrengths: ev.TopStrengths, PDFDecision: ev.PDFDecision, Notes: ev.Notes,
	})

	pre, hasPre := 0.0, false
	if a := state.NewCache().Score(e.L.Scores(), id); a != nil && a.Status == state.P1Scored {
		pre, hasPre = a.Score, true
	}

	report := assembleReport(reportInput{
		ID: id, URL: url, Today: today, Parsed: parsed, Rows: rows, Coverage: coverage,
		J: j, Salary: salary, CompDim: compDim, HasComp: hasComp,
		Score: score, PreScore: pre, HasPreScore: hasPre, PDFNote: pdfNote,
		MachineSummary: machine, Calib: e.calib,
	})
	return composed{eval: ev, report: report}
}

// machineSummary is the compact block the report header carries and everything
// downstream reads instead of re-parsing the prose.
type machineSummary struct {
	Company       string   `json:"company"`
	Role          string   `json:"role"`
	CVMatch       int      `json:"cv_match"`
	CVCoverage    float64  `json:"cv_coverage"`
	CVBlended     float64  `json:"cv_blended"`
	NorthStar     int      `json:"north_star"`
	CompInferred  *int     `json:"comp_inferred"`
	RedFlagsScore int      `json:"red_flags_score"`
	Score         float64  `json:"score"`
	Archetype     string   `json:"archetype"`
	FinalDecision string   `json:"final_decision"`
	HardStops     []string `json:"hard_stops"`
	SoftGaps      []string `json:"soft_gaps"`
	TopStrengths  []string `json:"top_strengths"`
	PDFDecision   bool     `json:"pdf_decision"`
	Notes         string   `json:"notes"`
}

// commit writes the report and the artifact, then the state row — in that
// order, so a crash can leave an orphaned report but never a row claiming a
// report that is not there. `snipe run` resets the reverse case on sight, and
// the whole point of the ordering is that it never has to.
func (e *Evaluator) commit(ctx context.Context, id int, c composed) error {
	if err := os.MkdirAll(e.L.Reports(), 0o755); err != nil {
		return err
	}
	if err := state.WriteAtomic(c.eval.ReportPath, []byte(c.report)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.eval, "", "  ")
	if err != nil {
		return err
	}
	if err := state.WriteAtomic(filepath.Join(e.L.Evals(), strconv.Itoa(id)+".json"), append(b, 0x0a)); err != nil {
		return err
	}
	return e.Store.Commit(id, func(r *state.Row) {
		r.P2Status = state.P2Evaled
		r.ReportNum = c.eval.ReportNum
		r.Error = ""
		// A fresh eval supersedes whatever Phase 3 verdict the row carried, for
		// the same reason the error is cleared: both describe the run before
		// this one. The Phase 1 cost gate writes `skipped` when it turns an
		// offer back, so overruling that gate with `proceed?` left the refusal
		// behind — and a row that then evaled 4.1 read as "made no CV, the
		// score did not clear the gate", quoting a gate it had just cleared.
		r.P3Status = ""
	})
}

func minInt(a, b int) int {
	if b < a {
		return b
	}
	return a
}

func clampDim(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	if v < 1 {
		return 1
	}
	if v > 5 {
		return 5
	}
	return v
}

func prepend(list []string, head string) []string {
	out := []string{head}
	seen := map[string]bool{head: true}
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func round(f float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(f*p) / p
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify is capped at 40 characters — the report filename is a path component
// and a company that writes its full legal name into the JD produces a very long
// one. ASCII by this point, so bytes and code units agree.
func slugify(s string) string {
	out := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// NextReportNum is max(existing)+1, zero-padded to three digits. Reports are
// numbered sequentially and the number is the tracker's join key, so it must be
// allocated against both the files on disk and any number already recorded in
// state.
func NextReportNum(reportsDir string, rows map[int]state.Row) string {
	max := 0
	if ents, err := os.ReadDir(reportsDir); err == nil {
		for _, e := range ents {
			name := e.Name()
			if len(name) < 3 {
				continue
			}
			if n, err := strconv.Atoi(name[:3]); err == nil && n > max {
				max = n
			}
		}
	}
	for _, r := range rows {
		if n, err := strconv.Atoi(r.ReportNum); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("%03d", max+1)
}

// outcomeByStatus turns a tracker status into what actually happened.
//
// Joined live at query time rather than baked into the index, so it is always
// current. Keyed by report number, which is the tracker row's own #.
// "Evaluated" means no outcome yet and is deliberately absent.
var outcomeByStatus = map[string]string{
	tracker.StatusApplied:   "applied, no response yet",
	tracker.StatusResponded: "company responded",
	tracker.StatusInterview: "reached interview",
	tracker.StatusOffer:     "received offer",
	tracker.StatusRejected:  "rejected",
	tracker.StatusDiscarded: "user chose not to apply",
	tracker.StatusSkip:      "user chose not to apply",
}

func (e *Evaluator) outcomes() map[int]string {
	out := map[int]string{}
	apps, err := tracker.LoadApplications(e.L.Tracker())
	if err != nil {
		return out
	}
	for _, a := range apps {
		if o, ok := outcomeByStatus[a.Status]; ok {
			out[a.Num] = o
		}
	}
	return out
}

// calibMaxSim excludes an anchor that is the same posting under a different id.
//
// Boards repost, and the scan gives the repost a new id, so its vector comes
// back at cosine 1.000 against the offer being evaluated. The anchor is then
// not a prior drawn from history but this pipeline's own previous score for
// this exact job, fed back in as evidence for what to score it now — and it
// takes a slot from the two or three genuine neighbours the block is for. Over
// the 927 anchors in the report corpus one offer spent two of its three slots
// on itself, at 3/5 and 3.4/5.
//
// 0.97 is where the histogram turns: the tail thins normally through 0.88–0.96
// (1–7 anchors a bucket) and then climbs — 6, 10, 13, 29 — which is a
// duplicate population, not a similarity tail. It drops 58 of 927 anchors.
const calibMaxSim = 0.97

// similarPastOffers is the calibration RAG: the k most similar past postings
// with their own outcomes, as few-shot anchors drawn from the user's history.
//
// A real outcome outranks the past model score in the prompt — a similar offer
// that was rejected argues for scoring lower, one that reached interview
// validates the fit.
func (e *Evaluator) similarPastOffers(ctx context.Context, jd string, selfID, k int) ([]Calibration, error) {
	entries, err := e.IX.LoadJD(ctx)
	if err != nil {
		return nil, err
	}
	vecs, err := e.OL.Embed(ctx, []string{text.CleanJD(jd, 2000)}, ollama.ModelEmbed)
	if err != nil || len(vecs) == 0 {
		return nil, err
	}
	self := strconv.Itoa(selfID)

	type scored struct {
		id  string
		sim float64
	}
	var ranked []scored
	for _, en := range entries {
		if en.ID == self {
			continue
		}
		sim, err := embed.Cosine(vecs[0], en.Vec)
		if err != nil {
			return nil, err
		}
		if sim >= calibMaxSim {
			continue
		}
		ranked = append(ranked, scored{en.ID, sim})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })

	outcomes := e.outcomes()
	var out []Calibration
	for _, r := range ranked {
		if len(out) == k {
			break
		}
		n, err := strconv.Atoi(r.id)
		if err != nil {
			continue
		}
		var ev Eval
		b, err := os.ReadFile(filepath.Join(e.L.Evals(), r.id+".json"))
		if err != nil || json.Unmarshal(b, &ev) != nil || ev.Status != state.P2Evaled {
			continue // only a completed evaluation is an anchor
		}
		_ = n
		c := Calibration{
			ID: r.id, Company: ev.Company, Role: ev.Role,
			Score: ev.Score, Decision: ev.FinalDecision, Sim: round(r.sim, 3),
		}
		if rn, err := strconv.Atoi(ev.ReportNum); err == nil {
			c.Outcome = outcomes[rn]
		}
		out = append(out, c)
	}
	return out, nil
}

func mustJSONCompact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
