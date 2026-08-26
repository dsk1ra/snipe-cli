package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase1"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
	"github.com/dsk1ra/snipe-cli/internal/providers"
	"github.com/dsk1ra/snipe-cli/internal/run"
	"github.com/dsk1ra/snipe-cli/internal/scan"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
	"github.com/dsk1ra/snipe-cli/internal/tui"
)

func cmdTUI() error {
	p := tea.NewProgram(tui.New(layout()))
	_, err := p.Run()
	return err
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	opt := run.DefaultOptions()
	fs.IntVar(&opt.OnlyID, "only-id", 0, "process just this offer")
	fs.IntVar(&opt.StartFrom, "start-from", 0, "skip offers below this id")
	fs.BoolVar(&opt.RetryFailed, "retry-failed", false, "re-run failed offers from scratch, ignoring the retry limit")
	fs.BoolVar(&opt.Drain, "drain", false, "work through the queue file")
	fs.BoolVar(&opt.SkipPhase1, "skip-phase1", false, "skip scoring")
	fs.BoolVar(&opt.SkipPhase2, "skip-phase2", false, "skip evaluation")
	fs.BoolVar(&opt.SkipPhase3, "skip-phase3", false, "skip CV tailoring")
	fs.BoolVar(&opt.DryRun, "dry-run", false, "show what would run")
	fs.Float64Var(&opt.P1Threshold, "p1-threshold", opt.P1Threshold, "Phase 1→2 gate; 0 disables it")
	fs.Float64Var(&opt.Threshold, "threshold", 0, "Phase 2→3 gate (default: from profile.yml, else 3.0)")
	fs.IntVar(&opt.ParallelScore, "parallel-score", opt.ParallelScore, "Phase 1 workers")
	fs.IntVar(&opt.ParallelEval, "parallel-eval", opt.ParallelEval, "Phase 2 workers")
	fs.IntVar(&opt.ParallelPDF, "parallel-pdf", opt.ParallelPDF, "Phase 3 workers (VRAM-shared — keep low)")
	fs.StringVar(&opt.OllamaURL, "ollama-url", ollama.DefaultURL, "Ollama base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}

	l := layout()
	cfg, err := config.Load(l)
	if err != nil {
		return err
	}
	if opt.Threshold == 0 {
		opt.Threshold = cfg.Thresholds.AutoPDFScore
	}

	// Timing is on by default: the per-JD budget is only enforceable if it is
	// measured, and an append per model call costs nothing. SNIPE_TIMING= mutes it.
	timingPath := l.Timings()
	if v, set := os.LookupEnv("SNIPE_TIMING"); set {
		timingPath = v
	}
	tl := ollama.NewTimingLog(timingPath)
	cl := ollama.New(opt.OllamaURL, tl)

	if !opt.DryRun {
		probe, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		models, err := cl.Up(probe)
		cancel()
		if err != nil {
			return fmt.Errorf("%w — start it with: ollama serve", err)
		}
		if err := requireModels(models, opt, cfg); err != nil {
			return err
		}
	}

	st, err := state.Open(l.State())
	if err != nil {
		return err
	}
	scorer := &phase1.Scorer{
		L: l, Cfg: cfg, OL: cl, Model: ollama.ModelScreen, Store: st,
		Timeout: 120 * time.Second,
	}
	cache := state.NewCache()

	ix := &embed.Indexer{
		OL: cl, CVPath: l.CVIndex(),
		JDPath: l.JDIndex(), JDsDir: l.JDs(),
	}
	evaluator := &phase2.Evaluator{
		L: l, Cfg: cfg, OL: cl, IX: ix, Store: st,
		Model: ollama.ModelEval, NumCtx: 8192,
		Timeout: 300 * time.Second, RAG: os.Getenv("SNIPE_NO_RAG") == "", Threshold: opt.Threshold,
	}

	ph := run.Phases{
		Score: scorer.Run,
		Eval: func(ctx context.Context, id int, ck *state.Checkpoint) error {
			// The report number is allocated per offer, against both the files
			// on disk and any number already recorded in state, so a report and
			// its tracker row cannot disagree about it.
			evaluator.ReportNum = phase2.NextReportNum(l.Reports(), st.Rows())
			return evaluator.Run(ctx, id, ck)
		},
		// EvalScore reads the authoritative, code-computed Phase 2 score back
		// off the eval artifact, for the Phase 3 gate.
		EvalScore: func(id int) (float64, bool) {
			a := cache.Eval(l.Evals(), id)
			if a == nil || a.Status != state.P2Evaled {
				return 0, false
			}
			return a.Score, true
		},
	}

	tailor := &phase3.Tailor{L: l, Cfg: cfg, OL: cl, Store: st, Cache: cache}
	ph.PDF = tailor.Run

	r := run.New(l, opt, ph, st, cl, tl)
	ctx, cleanup := signalCtx(r.RequestStop)
	defer cleanup()

	err = r.Run(ctx)
	if errors.Is(err, run.ErrStopped) {
		fmt.Println("stopped — `snipe run` resumes from the checkpoints")
		return nil
	}
	return err
}

// requireModels fails before the first offer rather than during it.
func requireModels(available []string, opt run.Options, cfg *config.Config) error {
	need := []string{}
	if !opt.SkipPhase1 {
		need = append(need, ollama.ModelScreen)
	}
	if !opt.SkipPhase2 {
		need = append(need, ollama.ModelEval, ollama.ModelEmbed)
	}
	// Phase 3 ranks the CV's bullets against the posting's requirements, so it
	// needs the embedder even though it makes no generation call.
	if !opt.SkipPhase3 {
		need = append(need, ollama.ModelEmbed)
	}
	for _, want := range need {
		found := false
		for _, got := range available {
			if got == want || strings.HasPrefix(got, want+":") {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("model %q not in Ollama (have: %s)\nbuild it: ollama create %s -f prompts/Modelfile.%s",
				want, strings.Join(available, ", "), want, want)
		}
	}
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	jd := fs.String("jd", "", "the job description text")
	file := fs.String("f", "", "read the job description from a file, or - for stdin")
	link := fs.String("link", "", "the posting URL, kept for tracking (never fetched)")
	queueOnly := fs.Bool("queue", false, "queue it without running")
	if err := fs.Parse(args); err != nil {
		return err
	}

	body := *jd
	switch {
	case *file == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = string(b)
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		body = string(b)
	}
	body = strings.TrimSpace(body)
	if len(body) < 100 {
		return fmt.Errorf("JD too short (%d chars) — paste the full description", len(body))
	}

	l := layout()
	offers, err := state.LoadOffers(l.Input())
	if err != nil {
		return err
	}
	if *link != "" {
		for _, o := range offers {
			if o.URL == *link {
				return fmt.Errorf("that link is already in the pipeline as #%d — rerun it with: snipe run --only-id %d --retry-failed", o.ID, o.ID)
			}
		}
	}
	id := state.NextOfferID(offers)
	url := *link
	if url == "" {
		url = "manual:snipe-" + strconv.Itoa(id)
	}
	if err := os.MkdirAll(l.JDs(), 0o755); err != nil {
		return err
	}
	// The JD text is what all three phases read; nothing is ever fetched for a
	// manual offer, and the link is kept only for tracking.
	if err := os.WriteFile(filepath.Join(l.JDs(), strconv.Itoa(id)+".txt"), []byte(body), 0o644); err != nil {
		return err
	}
	if err := state.AppendOffer(l.Input(), state.Offer{
		ID: id, URL: url, Source: "snipe",
		Notes: "manual JD " + time.Now().Format("2006-01-02"),
	}); err != nil {
		return err
	}
	fmt.Printf("added offer #%d (%d chars)\n", id, len(body))

	// A run already holding the lock means queue, not collide.
	if *queueOnly || state.Active(l.Lock()) {
		f, err := os.OpenFile(l.Queue(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "%d\n", id); err != nil {
			return err
		}
		if state.Active(l.Lock()) {
			fmt.Println("queued — the active run picks it up when it finishes")
		} else {
			fmt.Println("queued — process it with: snipe run --drain")
		}
		return nil
	}
	return cmdRun([]string{"--only-id", strconv.Itoa(id)})
}

func cmdDoctor() error {
	l := layout()
	fmt.Printf("data root: %s\ncode root: %s\n\n", l.Home, l.Code)

	var problems int
	check := func(path, what string, required bool) {
		if _, err := os.Stat(path); err != nil {
			if required {
				problems++
				fmt.Printf("  MISSING  %-28s %s\n", what, path)
			} else {
				fmt.Printf("  absent   %-28s %s\n", what, path)
			}
			return
		}
		fmt.Printf("  ok       %-28s\n", what)
	}
	check(l.CV(), "cv.md", true)
	// config/profile.md is a hard runtime dependency of Phases 1 and 2, not a
	// nicety: both read it at call time.
	check(l.ProfileMD(), "config/profile.md", true)
	check(l.ProfileYML(), "config/profile.yml", true)
	check(l.Prompt("ollama-score-prompt.md"), "prompts/", true)
	check(l.Template("cv-template.tex"), "templates/", true)
	check(l.Input(), "batch/state/batch-input.tsv", false)
	check(l.State(), "batch/state/local-state.tsv", false)

	// The user layer, checked for content rather than existence — cv-sync-check.mjs's
	// live half. Its other two checks (hardcoded metrics in modes/_shared.md,
	// article-digest.md freshness) target files that exist in neither tree, so
	// they are not ported: a check that can never fire is worse than absent,
	// because it reads as coverage.
	//
	// Warnings, not problems: a short CV or an unedited profile still runs, it
	// just will not run well, and failing `doctor` over it would block a first
	// setup at exactly the moment the message is most useful.
	for _, w := range userLayerWarnings(l) {
		fmt.Printf("  warn     %s\n", w)
	}

	if _, err := config.Load(l); err != nil {
		problems++
		fmt.Printf("\n  config: %v\n", err)
	}
	rows, err := state.Load(l.State())
	if err != nil {
		problems++
		fmt.Printf("\n  state: %v\n", err)
	} else {
		fmt.Printf("\n%d state rows\n", len(rows))
		// The corruption class worth naming: a row marked evaled whose report
		// was never written is unreachable by every other code path.
		cache := state.NewCache()
		for id, r := range rows {
			if r.P2Status != state.P2Evaled {
				continue
			}
			if _, ok := cache.ReportFile(l.Reports(), r.ReportNum); !ok {
				problems++
				fmt.Printf("  #%d evaled but report %q is not on disk — `snipe run` will reset it\n", id, r.ReportNum)
			}
		}
	}
	// The tracker's own integrity — verify-pipeline.mjs's live checks. Only the
	// ones that can actually fire: its states.yml cross-check and its
	// pending-TSV sweep are covered by tracker.Merge and by the additions dir
	// below, and a check that can never fail reads as coverage it is not.
	apps, err := tracker.LoadApplications(l.Tracker())
	if err != nil {
		problems++
		fmt.Printf("\n  tracker: %v\n", err)
	} else if len(apps) > 0 {
		fmt.Printf("\n%d tracker rows\n", len(apps))
		seen := map[int]bool{}
		for _, a := range apps {
			if seen[a.Num] {
				problems++
				fmt.Printf("  #%d appears twice in the tracker\n", a.Num)
			}
			seen[a.Num] = true
			// A link pointing at a report that is not on disk is the one
			// tracker defect that costs something: the row's evidence is gone.
			rel, ok := tracker.ReportPath(a.Report)
			if !ok {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(l.Tracker()), rel)); err != nil {
				problems++
				fmt.Printf("  #%d links %s, which is not on disk\n", a.Num, rel)
			}
		}
		// Repairs are a nudge, not a problem: none of them lose anything, and
		// `snipe tracker fix` is the thing that applies them.
		if fix, err := tracker.Fix(l.Tracker(), l.Home, false); err == nil {
			if n := fix.Changed(); n > 0 {
				fmt.Printf("  %d row(s) would change — run `snipe tracker fix --dry-run`\n", n)
			}
			for _, u := range fix.Unknown {
				fmt.Printf("  warn     status %s maps to nothing canonical\n", u)
			}
		}
	}
	if ents, err := os.ReadDir(l.Additions()); err == nil {
		pending := 0
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".tsv") {
				pending++
			}
		}
		if pending > 0 {
			fmt.Printf("  %d pending tracker addition(s) — the next `snipe run` merges them\n", pending)
		}
	}

	if problems == 0 {
		fmt.Println("\nno problems found")
		return nil
	}
	return fmt.Errorf("%d problem(s) found", problems)
}

// userLayerWarnings reports a user layer that is present but not yet filled in.
func userLayerWarnings(l paths.Layout) []string {
	var out []string
	if b, err := os.ReadFile(l.CV()); err == nil && len(strings.TrimSpace(string(b))) < 100 {
		out = append(out, "cv.md is under 100 characters — is it the whole CV?")
	}
	b, err := os.ReadFile(l.ProfileYML())
	if err != nil {
		return out
	}
	yml := string(b)
	for _, field := range []string{"full_name", "email", "location"} {
		if !strings.Contains(yml, field) {
			out = append(out, "config/profile.yml has no "+field)
		}
	}
	// The example file ships this name; finding it means the copy was never edited.
	if strings.Contains(yml, "Jane Smith") {
		out = append(out, "config/profile.yml still carries the example data")
	}
	return out
}

// cmdScan resolves every enabled portals.yml entry to a provider, fetches, and
// queues what survives the filters.
func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be queued, write nothing")
	company := fs.String("company", "", "only entries whose name contains this")
	if err := fs.Parse(args); err != nil {
		return err
	}
	l := layout()
	cfg, err := scan.LoadConfig(l.Portals())
	if err != nil {
		return fmt.Errorf("portals.yml: %w — run onboarding first", err)
	}
	if *company != "" {
		cfg.TrackedCompanies = filterByName(cfg.TrackedCompanies, *company)
		cfg.JobBoards = filterByName(cfg.JobBoards, *company)
	}
	now := time.Now()
	mem := scan.Memory{Input: l.Input(), History: l.ScanHistory(), Tracker: l.Tracker()}
	known, err := scan.Load(mem, cfg.RecheckAfterDays(), now)
	if err != nil {
		return err
	}
	fmt.Printf("scanning %d companies, %d boards — %d URLs, %d company+role pairs already known\n",
		len(cfg.TrackedCompanies), len(cfg.JobBoards), len(known.URLs), len(known.Roles))

	ctx, cleanup := signalCtx(func() {})
	defer cleanup()
	res := scan.Run(ctx, cfg, providers.NewHTTP(), known, now)

	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "  WARN: %v\n", e)
	}
	fmt.Printf("%d fetched | dropped %d title, %d location, %d age, %d duplicate, %d same role | %d new\n",
		res.Fetched, res.Filtered.Title, res.Filtered.Location, res.Filtered.Age,
		res.Filtered.Duplicate, res.Filtered.Role, len(res.Kept))

	if *dryRun {
		for _, j := range res.Kept {
			fmt.Printf("  %s — %s (%s)\n", j.Company, j.Title, j.URL)
		}
		return nil
	}
	day := now.Format("2006-01-02")
	n, err := scan.Append(l.Input(), res.Kept, "pipeline-scan-"+day)
	if err != nil {
		return err
	}
	// The history is written after the queue, and a failure to write it is
	// reported rather than fatal: the offers are already queued, and the cost
	// of the missing rows is that a later scan offers them again.
	if err := scan.AppendHistory(l.ScanHistory(), res.Kept, day, "added"); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: scan-history not updated: %v\n", err)
	}
	if n > 0 {
		fmt.Printf("queued %d — `snipe run` scores them\n", n)
	}
	return nil
}

func filterByName(in []providers.Entry, want string) []providers.Entry {
	want = strings.ToLower(want)
	var out []providers.Entry
	for _, e := range in {
		if strings.Contains(strings.ToLower(e.Name), want) {
			out = append(out, e)
		}
	}
	return out
}

// cmdTracker is the tracker's maintenance corner: the three one-shot Node
// scripts (dedup-tracker, normalize-statuses, tracker-links) folded into one
// pass over applications.md.
func cmdTracker(args []string) error {
	if len(args) == 0 || args[0] != "fix" {
		return fmt.Errorf("usage: snipe tracker fix [--dry-run]")
	}
	fs := flag.NewFlagSet("tracker fix", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "report what would change, write nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	l := layout()
	res, err := tracker.Fix(l.Tracker(), l.Home, !*dryRun)
	if err != nil {
		return err
	}
	for _, group := range []struct {
		label string
		lines []string
	}{
		{"status", res.Statuses},
		{"removed", res.Removed},
		{"link", res.Links},
		{"kept", res.Protected},
		{"unknown", res.Unknown},
	} {
		for _, line := range group.lines {
			fmt.Printf("  %-8s %s\n", group.label, line)
		}
	}
	if res.Changed() == 0 {
		fmt.Println("tracker is clean")
		return nil
	}
	if *dryRun {
		fmt.Printf("\n%d change(s) — re-run without --dry-run to apply\n", res.Changed())
		return nil
	}
	fmt.Printf("\n%d change(s) written to %s\n", res.Changed(), l.Tracker())
	return nil
}
