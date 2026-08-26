package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/tracker"
)

// Phase durations, before DEMO_SPEED. Long enough that the phase a row is in is
// readable under the spinner before it changes — the run is the part of the
// demo worth watching — and split into the same stages the real pipeline
// checkpoints, so `p` (pause) can name the model call it dropped.
var stages = map[string][]struct {
	name string
	dur  time.Duration
}{
	"p1": {{"p1.fetch", 900 * time.Millisecond}, {"p1.score", 1900 * time.Millisecond}},
	"p2": {{"p2.jdparse", 1300 * time.Millisecond}, {"p2.evidence", 1500 * time.Millisecond}, {"p2.judgment", 2600 * time.Millisecond}},
	"p3": {{"p3.select", 1800 * time.Millisecond}, {"p3.render", 2300 * time.Millisecond}},
}

// speed scales every nap. hero.tape sets DEMO_SPEED=0.45 for a loop short
// enough to autoplay.
func speed() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("DEMO_SPEED"), 64); err == nil && v > 0 {
		return v
	}
	return 1
}

// plan is what the fixture decided each offer's outcome would be, before
// anything ran. It is what makes the tape's fixed sleeps line up with the
// phases — nothing here is random at record time.
type plan struct {
	Company string  `json:"company"`
	Role    string  `json:"role"`
	P1      float64 `json:"p1"`
	Score   float64 `json:"score"`
}

func planPath(l paths.Layout) string { return filepath.Join(l.Home, "plan.json") }

func loadPlans(l paths.Layout) map[string]plan {
	out := map[string]plan{}
	b, err := os.ReadFile(planPath(l))
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func savePlan(l paths.Layout, id int, p plan) error {
	all := loadPlans(l)
	all[strconv.Itoa(id)] = p
	b, _ := json.MarshalIndent(all, "", "  ")
	return state.WriteAtomic(planPath(l), append(b, '\n'))
}

// firstLineRe pulls "<Role> at <Company>" out of a pasted job description, so a
// row queued from the TUI carries the company that was actually pasted rather
// than a lookup-table name.
var firstLineRe = regexp.MustCompile(`^\s*(.+?)\s+(?:at|@)\s+([A-Z][\w.& -]+)`)

func planFor(l paths.Layout, id int) plan {
	if p, ok := loadPlans(l)[strconv.Itoa(id)]; ok {
		return p
	}
	// No plan: a JD pasted into the TUI moments ago. Read its first line for the
	// name, and give it the score the demo wants to show off — a 5.0 renders in
	// animated gold.
	p := plan{Company: "Pasted Offer", Role: "Software Engineer", P1: 4.3, Score: 5.0}
	if b, err := os.ReadFile(filepath.Join(l.JDs(), strconv.Itoa(id)+".txt")); err == nil {
		if m := firstLineRe.FindStringSubmatch(string(b)); m != nil {
			p.Role = strings.TrimSpace(m[1])
			p.Company = strings.TrimRight(strings.TrimSpace(m[2]), ".,—- ")
		}
	}
	_ = savePlan(l, id, p)
	return p
}

// ── the runner ───────────────────────────────────────────────────────────────

// sim is `snipe run`: the same flags the TUI passes, the same files written, on
// a timer instead of against a model.
func sim(l paths.Layout, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	onlyID := fs.Int("only-id", 0, "")
	retry := fs.Bool("retry-failed", false, "")
	drain := fs.Bool("drain", false, "")
	p1Threshold := fs.Float64("p1-threshold", 2.5, "")
	// The real command takes a dozen more flags; anything the demo does not act
	// on is accepted and ignored rather than made a parse error.
	for _, ignored := range []string{"start-from", "parallel-score", "parallel-eval", "parallel-pdf"} {
		fs.Int(ignored, 0, "")
	}
	fs.Bool("skip-phase1", false, "")
	fs.Bool("skip-phase2", false, "")
	fs.Bool("skip-phase3", false, "")
	fs.Bool("dry-run", false, "")
	fs.Float64("threshold", 0, "")
	fs.String("ollama-url", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = drain // the queue is drained whenever no single id was named

	lock, err := state.Acquire(l.Lock())
	if err != nil {
		return err
	}
	defer lock.Release()

	ctx, stopping := signals()

	st, err := state.Open(l.State())
	if err != nil {
		return err
	}

	// Three ways in, the same three the real runner has: one named offer, the
	// queue, or everything the checkpoints say is unfinished — which is what ▶
	// resumes after a pause.
	switch {
	case *onlyID != 0:
		dropFromQueue(l, *onlyID)
		run1(ctx, l, st, *onlyID, *p1Threshold, *retry)
	case len(state.Queue(l.Queue())) > 0:
		// One id at a time, put back if a stop lands first. A queue emptied up
		// front takes every waiting row off the screen at once, and the rows are
		// the thing being watched.
		for {
			id, ok := popQueue(l)
			if !ok {
				break
			}
			if stopping() || ctx.Err() != nil {
				pushQueueFront(l, id)
				break
			}
			if !run1(ctx, l, st, id, *p1Threshold, *retry) {
				break
			}
		}
	default:
		for _, id := range pending(l) {
			if stopping() || !run1(ctx, l, st, id, *p1Threshold, *retry) {
				break
			}
		}
	}

	// The real runner folds the pending rows into the tracker once, at the end.
	res, err := tracker.Merge(l.Tracker(), l.Additions())
	if err != nil {
		return err
	}
	if err := tracker.File(l.Additions(), res.Files); err != nil {
		return err
	}
	fmt.Printf("tracker: %d added, %d updated\n", res.Added, res.Updated)
	return nil
}

// run1 is one offer, reporting whether the run should carry on. A cancelled
// stage stays marked in the checkpoint, so a resume redoes that one and no more.
func run1(ctx context.Context, l paths.Layout, st *state.Store, id int, threshold float64, retry bool) bool {
	if err := runOne(ctx, l, st, id, threshold, retry); err != nil {
		fmt.Fprintf(os.Stderr, "#%d: %v\n", id, err)
		return false
	}
	return true
}

// pending is everything the fixture has left unfinished: no verdict, not gated,
// not failed. It is what ▶ picks up when the queue is empty.
func pending(l paths.Layout) []int {
	rows, _ := state.Load(l.State())
	ids, _ := state.Timeline(l.JDs(), l.Evals(), l.Queue())
	var out []int
	for _, id := range ids {
		r := rows[id]
		if state.FailedPhase(r) != "" || state.Gated(r) {
			continue
		}
		if r.P3Status == state.P3Completed || r.P3Status == state.P3Skipped {
			continue
		}
		out = append(out, id)
	}
	return out
}

// signals wires the two ways a run ends early, exactly as the real one does:
// SIGUSR1 cancels the stage in flight (pause), SIGTERM lets it finish (stop).
func signals() (context.Context, func() bool) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := false
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1)
	go func() {
		if <-ch == syscall.SIGUSR1 {
			cancel()
			return
		}
		stop = true
	}()
	return ctx, func() bool { return stop }
}

func runOne(ctx context.Context, l paths.Layout, st *state.Store, id int, threshold float64, retry bool) error {
	t := planFor(l, id)
	if retry {
		_ = os.Remove(filepath.Join(l.Work(), strconv.Itoa(id)+".json"))
	}
	row, _ := st.Get(id)

	// Phase 1. An already-scored offer reuses its score, exactly like the real
	// phase-1 offer list — which is what makes "proceed anyway" cost nothing.
	if row.P1Status != state.P1Scored || retry {
		// Before the nap, not after: the queue dates a row by its JD file, and a
		// row that vanishes for the length of the fetch reads as a bug.
		writeJD(l, id, t)
		if err := phase(ctx, l, id, "p1"); err != nil {
			return err
		}
		writeArtifact(filepath.Join(l.Scores(), strconv.Itoa(id)+".json"), state.Artifact{
			Status: state.P1Scored, Score: t.P1, Company: t.Company, Role: t.Role, Archetype: t.Role,
		})
		if err := st.Commit(id, func(r *state.Row) {
			r.URL = urlFor(l, id)
			r.P1Status, r.P1Score, r.Archetype = state.P1Scored, fmt.Sprintf("%.1f", t.P1), t.Role
		}); err != nil {
			return err
		}
		fmt.Printf("#%d scored %.1f\n", id, t.P1)
	}
	row, _ = st.Get(id)
	if p1, ok := row.Score(); ok && p1 < threshold {
		if err := st.Commit(id, func(r *state.Row) {
			r.P2Status, r.P3Status = state.P2Gated, state.P3Skipped
			r.Error = fmt.Sprintf("p1-gated(threshold=%g)", threshold)
		}); err != nil {
			return err
		}
		fmt.Printf("#%d gated at %.1f\n", id, t.P1)
		return nil
	}

	// Phase 2.
	if !row.HasReport() || retry {
		if err := phase(ctx, l, id, "p2"); err != nil {
			return err
		}
		num := nextReportNum(l)
		date := time.Now().Format("2006-01-02")
		file := fmt.Sprintf("%s-%s-%s.md", num, slug(t.Company), date)
		if err := os.MkdirAll(l.Reports(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(l.Reports(), file), []byte(reportBody(t, num, date, urlFor(l, id))), 0o644); err != nil {
			return err
		}
		writeArtifact(filepath.Join(l.Evals(), strconv.Itoa(id)+".json"), state.Artifact{
			Status: state.P2Evaled, Score: t.Score, Company: t.Company, Role: t.Role,
			Archetype: t.Role, ReportNum: num,
		})
		if err := st.Commit(id, func(r *state.Row) {
			r.P2Status, r.ReportNum = state.P2Evaled, num
		}); err != nil {
			return err
		}
		addition(l, id, t, num, file, date)
		fmt.Printf("#%d evaluated %.1f → report %s\n", id, t.Score, num)
	}

	// Phase 3. Below the auto-PDF threshold the offer is finished, not
	// unfinished: it earned an eval and a report, and deliberately no CV.
	if t.Score < 3 {
		return st.Commit(id, func(r *state.Row) { r.P3Status = state.P3Skipped })
	}
	if err := phase(ctx, l, id, "p3"); err != nil {
		return err
	}
	row, _ = st.Get(id)
	dir := filepath.Join(l.Output(), fmt.Sprintf("%s_%s_%s",
		time.Now().Format("2006-01-02"), slug(t.Company), row.ReportNum))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_ = os.WriteFile(filepath.Join(dir, "Denys-Skira-CV.pdf"), []byte("%PDF-1.4\n% demo placeholder — no CV was tailored\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cv-content.json"), []byte(fmt.Sprintf(
		"{\n  \"demo\": true,\n  \"company\": %q,\n  \"role\": %q,\n  \"pages\": 1\n}\n", t.Company, t.Role)), 0o644)
	fmt.Printf("#%d CV written to %s\n", id, filepath.Base(dir))
	return st.Commit(id, func(r *state.Row) { r.P3Status = state.P3Completed })
}

// phase naps through one phase's stages, marking each in the offer's checkpoint
// before the "call" and clearing it after — so a pause leaves exactly one stage
// named, and a resume redoes that one and no more.
func phase(ctx context.Context, l paths.Layout, id int, name string) error {
	for _, s := range stages[name] {
		c := loadCk(l, id)
		if c.Stages[s.name] {
			continue
		}
		c.Running = s.name
		if err := saveCk(l, id, c); err != nil {
			return err
		}
		select {
		case <-time.After(time.Duration(float64(s.dur) * speed())):
		case <-ctx.Done():
			return fmt.Errorf("paused in %s", s.name)
		}
		c.Stages[s.name], c.Running = true, ""
		if err := saveCk(l, id, c); err != nil {
			return err
		}
	}
	return nil
}

// checkpoint is state.Checkpoint's file, written by hand: the demo has no stage
// outputs to cache, only the record of which stages landed.
type checkpoint struct {
	Stages  map[string]bool   `json:"stages"`
	Inputs  map[string]string `json:"inputs"`
	Running string            `json:"running,omitempty"`
}

func ckPath(l paths.Layout, id int) string {
	return filepath.Join(l.Work(), strconv.Itoa(id)+".json")
}

func loadCk(l paths.Layout, id int) checkpoint {
	c := checkpoint{Stages: map[string]bool{}, Inputs: map[string]string{}}
	if b, err := os.ReadFile(ckPath(l, id)); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.Stages == nil {
		c.Stages = map[string]bool{}
	}
	return c
}

func saveCk(l paths.Layout, id int, c checkpoint) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return state.WriteAtomic(ckPath(l, id), b)
}

// ── what the phases write ────────────────────────────────────────────────────

func writeArtifact(path string, a state.Artifact) {
	b, _ := json.MarshalIndent(a, "", "  ")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = state.WriteAtomic(path, append(b, '\n'))
}

// writeJD stands in for p1.fetch: a scanned offer has no JD on disk until the
// run fetches one, and the queue dates a row by that file.
func writeJD(l paths.Layout, id int, t plan) {
	path := filepath.Join(l.JDs(), strconv.Itoa(id)+".txt")
	if _, err := os.Stat(path); err == nil {
		return // pasted by hand; that text is the real input
	}
	_ = os.MkdirAll(l.JDs(), 0o755)
	_ = os.WriteFile(path, []byte(fmt.Sprintf(
		"%s at %s\n\nSample posting, fetched by the demo pipeline. No page was requested.\n",
		t.Role, t.Company)), 0o644)
}

func addition(l paths.Layout, id int, t plan, num, file, date string) {
	line := strings.Join([]string{
		strconv.Itoa(nextTrackerNum(l)), date, t.Company, t.Role, "Evaluated",
		fmt.Sprintf("%.1f/5", t.Score), map[bool]string{true: "Y", false: "N"}[t.Score >= 3],
		fmt.Sprintf("[%s](reports/%s)", num, file),
		fmt.Sprintf("Auto-evaluated — score %.1f/5", t.Score),
	}, "\t")
	_ = os.MkdirAll(l.Additions(), 0o755)
	_ = state.WriteAtomic(filepath.Join(l.Additions(), strconv.Itoa(id)+".tsv"), []byte(line+"\n"))
}

var trackerNumRe = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`)

func nextTrackerNum(l paths.Layout) int {
	b, err := os.ReadFile(l.Tracker())
	if err != nil {
		return 1
	}
	max := 0
	for _, m := range trackerNumRe.FindAllStringSubmatch(string(b), -1) {
		if n, _ := strconv.Atoi(m[1]); n > max {
			max = n
		}
	}
	return max + 1
}

var reportNumRe = regexp.MustCompile(`^(\d{3})-`)

func nextReportNum(l paths.Layout) string {
	max := 0
	ents, _ := os.ReadDir(l.Reports())
	for _, e := range ents {
		if m := reportNumRe.FindStringSubmatch(e.Name()); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > max {
				max = n
			}
		}
	}
	return fmt.Sprintf("%03d", max+1)
}

func urlFor(l paths.Layout, id int) string {
	offers, _ := state.LoadOffers(l.Input())
	if o, ok := offers[id]; ok {
		return o.URL
	}
	return "manual:snipe-" + strconv.Itoa(id)
}

// reportBody is the shape the reader pane (v) is built for: a header block, then
// tables carrying the reasoning.
func reportBody(t plan, num, date, url string) string {
	verdict := "Below the apply threshold — no CV was tailored."
	switch {
	case t.Score >= 4.5:
		verdict = "Strong fit. Apply today."
	case t.Score >= 3:
		verdict = "Reasonable fit — worth an application."
	}
	return fmt.Sprintf(`# Evaluation: %s — %s

**Date:** %s
**Archetype:** %s
**Score:** %.1f/5
**Score pre-screening (local model):** %.1f/5
**Legitimacy:** High Confidence
**URL:** %s
**PDF:** output/%s_%s_%s/Denys-Skira-CV.pdf

> Demo report. Written by demo/sandbox with invented data — no job description
> was read, no model ran, and no CV was scored.

## A) Role Summary

| Field | Value |
|-------|-------|
| Archetype | %s |
| Seniority | Mid |
| Remote policy | Remote (EU) |
| Location | Remote |
| TL;DR | %s |

## B) CV Match

_Evidence retrieved semantically from cv.md. **Strong** = same work, same tools · **Transferable** = same work, different tools · **Gap** = not demonstrated._

| JD Requirement | Candidate evidence | Strength |
|----------------|-------------------|----------|
| **[must]** Distributed backend systems at scale | Rebuilt a job-search pipeline to run fully locally — three model phases, checkpointed per stage, resumable after a kill | Strong |
| **[must]** Go or JVM in production | Ported 4,700 lines of Node and bash to one Go binary; one lock, one process, one state file | Strong |
| **[must]** Postgres and schema design under load | Designed the append-only TSV record the runner and the UI both read once a second | Transferable |
| gRPC and REST API design | Built the HTTP client and the timing log the model calls run through | Transferable |
| Terraform, CI/CD, observability | Ran services on AWS from Terraform with OpenTelemetry into Grafana, and carried the SLOs | Strong |
| Payments or clearing domain | Not demonstrated | Gap |

## C) Verdict

| Field | Value |
|-------|-------|
| Score | %.1f/5 |
| Decision | %s |
| Confidence | High |

%s
`, t.Company, t.Role, date, t.Role, t.Score, t.P1, url, date, slug(t.Company), num,
		t.Role, verdict, t.Score, map[bool]string{true: "Apply", false: "Pass"}[t.Score >= 3], verdict)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// ── the scanner ──────────────────────────────────────────────────────────────

// scanFinds is what `snipe scan` "turns up". Fixed rather than random so the
// tape's sleeps hold, and one of the two is below the Phase 1 gate — a scan that
// only ever finds winners is not what a scan looks like.
var scanFinds = []struct {
	company, role, url string
	p1, score          float64
}{
	{"Pallas Networks", "Distributed Systems Engineer", "https://jobs.example.com/pallas/dse-4471", 3.6, 4.2},
	{"Tessera Retail", "Software Engineer (C#/.NET)", "https://careers.example.org/tessera/se-dotnet", 2.1, 3.3},
}

// fakeScan is `snipe scan`: the boards, the filter counts, and the ids it
// queued. It writes what the real one writes — offers in batch-input.tsv and
// their ids in the queue — and fetches nothing.
func fakeScan(l paths.Layout) error {
	boards := []string{"remoteok", "weworkremotely", "euremotejobs", "himalayas", "jobicy", "remotive"}
	offers, err := state.LoadOffers(l.Input())
	if err != nil {
		return err
	}
	fmt.Printf("scanning 12 companies, %d boards — %d URLs already known\n", len(boards), len(offers))
	for _, b := range boards {
		time.Sleep(time.Duration(450 * float64(time.Millisecond) * speed()))
		fmt.Printf("  %-16s ok\n", b)
	}
	fmt.Printf("%d fetched | dropped %d title, %d location, %d age, %d duplicate | %d new\n",
		214, 121, 47, 31, 13, len(scanFinds))

	id := state.NextOfferID(offers)
	for _, f := range scanFinds {
		if err := state.AppendOffer(l.Input(), state.Offer{
			ID: id, URL: f.url, Source: "portal-scan",
			Notes: "scan " + time.Now().Format("2006-01-02"),
		}); err != nil {
			return err
		}
		if err := savePlan(l, id, plan{Company: f.company, Role: f.role, P1: f.p1, Score: f.score}); err != nil {
			return err
		}
		if err := appendQueue(l, id); err != nil {
			return err
		}
		id++
	}
	fmt.Printf("queued %d — `snipe run` scores them\n", len(scanFinds))
	return nil
}

func appendQueue(l paths.Layout, id int) error {
	if err := os.MkdirAll(filepath.Dir(l.Queue()), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Queue(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d\n", id)
	return err
}

// jsonIndent is the one-liner the seed uses for plan.json.
func jsonIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}

// popQueue takes the first id off the queue file, rewriting the rest.
func popQueue(l paths.Layout) (int, bool) {
	ids := state.Queue(l.Queue())
	if len(ids) == 0 {
		return 0, false
	}
	writeQueue(l, ids[1:])
	return ids[0], true
}

// pushQueueFront puts an id back at the head, for a stop that landed between
// offers: a stop must not silently swallow queued work.
func pushQueueFront(l paths.Layout, id int) {
	writeQueue(l, append([]int{id}, state.Queue(l.Queue())...))
}

func dropFromQueue(l paths.Layout, id int) {
	var rest []int
	for _, q := range state.Queue(l.Queue()) {
		if q != id {
			rest = append(rest, q)
		}
	}
	writeQueue(l, rest)
}

func writeQueue(l paths.Layout, ids []int) {
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "%d\n", id)
	}
	_ = state.WriteAtomic(l.Queue(), []byte(b.String()))
}
