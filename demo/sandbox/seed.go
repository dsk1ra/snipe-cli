package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// seed builds demo/.sandbox: a self-contained fake snipe install for the VHS
// tapes. Everything in it is invented — companies, roles, scores, dates,
// applications, follow-ups. Nothing reads the real cv.md, the real tracker, or
// any model.
//
// It is deterministic: the same -seed builds the same year of history, so a
// tape's fixed sleeps line up with the same screen every time.

// Names the live part of the demo uses — today's queue, the scan finds, the
// pasted JD. Kept out of the generated history so a company is never both
// "evaluated four minutes ago" and "applied to in March".
var reserved = map[string]bool{
	"Northwind Labs": true, "Kestrel Data": true, "Orbital Freight": true,
	"Lumen Analytics": true, "Ironbark Security": true, "Bluefin Cloud": true,
	"Pallas Networks": true, "Tessera Retail": true, "Helix Systems": true,
}

var companies = []string{
	"Ardent Robotics", "Verdant Health", "Quanta Payments", "Meridian Trading", "Solstice AI",
	"Calyx Bio", "Driftwood Games", "Ember Logistics", "Vantage Mobility", "Wildcard Media",
	"Zephyr Telecom", "Cobalt Grid", "Nimbus Foundry", "Harbinger Fintech", "Silverpine Energy",
	"Auralite Systems", "Fernwood Travel", "Granite Ledger", "Halcyon Maps", "Juniper Works",
	"Larkspur Media", "Modulus Robotics", "Peregrine Labs", "Rookery Analytics", "Saltmarsh Bank",
	"Thistledown Care", "Umbra Vision", "Windlass Marine", "Yarrow Foods", "Zircon Devices",
}

var roles = []string{
	"Backend Engineer", "Senior Backend Engineer", "Platform Engineer", "Go Developer",
	"Java Developer", "Site Reliability Engineer", "Full-Stack Engineer (Node/React)",
	"Distributed Systems Engineer", "Data Platform Engineer", "Cloud Infrastructure Engineer",
	"DevOps Engineer", "Software Engineer (C#/.NET)", "ML Platform Engineer", "Security Engineer",
}

var channels = []string{"Email", "LinkedIn", "Portal"}

// today's queue: one of every row shape the list can draw.
var todayRows = []struct {
	company, role string
	p1, score     float64
	kind          string // done | below | gated | failed
	applied       bool
}{
	{"Northwind Labs", "Platform Engineer", 3.8, 4.6, "done", false},
	{"Kestrel Data", "Site Reliability Engineer", 3.5, 4.1, "done", true},
	{"Lumen Analytics", "Full-Stack Engineer (Node/React)", 3.0, 2.9, "below", false},
	{"Ironbark Security", "Security Engineer", 1.9, 3.9, "gated", false},
	{"Orbital Freight", "Backend Engineer", 3.4, 0, "failed", false},
	{"Bluefin Cloud", "Go Developer", 4.0, 4.8, "done", false},
}

type gen struct {
	l    paths.Layout
	r    *rand.Rand
	now  time.Time
	rows map[int]state.Row
	// input is batch-input.tsv, applied the marks file, plans the sim's
	// pre-decided outcomes for the rows the demo runs live.
	input   []string
	applied state.Marks
	plans   map[string]plan
	tracker []*hist
	id      int
	report  int
}

// hist is one generated offer that got far enough to carry a tracker row.
type hist struct {
	id                 int
	company, role, url string
	score              float64
	rnum, appliedOn    string
	status             string
	nudges, num        int
	reportFile         string
}

func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	out := fs.String("o", "demo/.sandbox", "directory to build the sandbox in")
	seedN := fs.Uint64("seed", 20260823, "PRNG seed — same seed, same year of history")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	l := paths.Layout{Home: dir, Code: dir}
	for _, d := range []string{
		l.Scores(), l.Evals(), l.JDs(), l.Logs(), l.Errors(), l.Work(), l.StateDir(),
		l.CacheDir(), l.Additions(), l.Data(), l.Reports(), l.Output(),
		filepath.Join(dir, "config"), filepath.Join(dir, "bin"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	g := &gen{
		l: l, r: rand.New(rand.NewPCG(*seedN, *seedN>>2)), now: time.Now(),
		rows: map[int]state.Row{}, applied: state.Marks{}, plans: map[string]plan{},
	}
	if err := g.fixedFiles(); err != nil {
		return err
	}
	g.history()
	g.board()
	if err := g.today(); err != nil {
		return err
	}
	if err := g.write(); err != nil {
		return err
	}
	fmt.Printf("sandbox: %s\n", dir)
	fmt.Printf("  %d offers · %d tracker rows · %d applied · %d evals\n",
		len(g.rows), len(g.tracker), len(g.applied), g.report)
	return nil
}

// fixedFiles is everything that does not depend on the generated history: the
// user layer the TUI reads, and the two desktop stubs.
func (g *gen) fixedFiles() error {
	files := map[string]string{
		// cv.md only has to exist and be non-empty: config.Load refuses to
		// answer without one, and the goals in the side pane come through it.
		g.l.CV(): "# Demo CV\n\nInvented. The demo pipeline never reads this file.\n",
		g.l.ProfileYML(): `# Demo profile — invented, for the recording only.
candidate:
  full_name: "A. Candidate"
  location: "Remote (EU)"

auto_pdf_score_threshold: 3.0

goals:
  daily_scans: 10
  daily_applications: 5
  weekly_applications: 25

followup_cadence:
  applied_first_days: 7
  applied_subsequent_days: 7
  applied_max_followups: 2
  responded_initial_days: 1
  responded_subsequent_days: 3
  interview_thankyou_days: 1
`,
		g.l.Portals(): "tracked_companies: []\njob_boards: []\n",
	}
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
	}
	// The TUI opens folders, links and the debug target with xdg-open. In a
	// recording that throws a file manager over the terminal, so PATH gets a
	// stub that logs and does nothing.
	for _, name := range []string{"xdg-open", "notify-send"} {
		body := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' \"%s\" \"$*\" >> \"$(dirname \"$0\")/../batch/logs/opened.log\"\n", name)
		if err := os.WriteFile(filepath.Join(g.l.Home, "bin", name), []byte(body), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (g *gen) pick(a []string) string { return a[g.r.IntN(len(a))] }
func (g *gen) between(lo, hi int) int { return lo + g.r.IntN(hi-lo+1) }
func round1(f float64) float64        { return float64(int(f*10+0.5)) / 10 }

// history is a year of scanning: quiet days, working days, and the odd burst.
//
// The per-day counts are decided before anything is generated, because the
// heatmap paints its busiest day gold and two gold cells read as a rendering bug
// rather than a record. Every generated day is capped below the champion.
func (g *gen) history() {
	const scanCap, champScans = 6, 11
	champ := 96
	for {
		wd := g.now.AddDate(0, 0, -champ).Weekday()
		if wd != time.Saturday && wd != time.Sunday {
			break
		}
		champ--
	}
	daily := map[int]int{}
	for d := 365; d >= 1; d-- {
		wd := g.now.AddDate(0, 0, -d).Weekday()
		weekend := wd == time.Saturday || wd == time.Sunday
		r := g.r.Float64()
		var n int
		switch {
		case weekend:
			if r >= 0.72 {
				n = g.between(1, 3)
			}
		case r < 0.35:
			n = 0
		case r < 0.85:
			n = g.between(1, 3)
		default:
			n = g.between(4, 9)
		}
		// The month view opens on the current month, which is only a few days
		// old: keep the last fortnight busy so it is not four squares and a lot
		// of grey.
		if d <= 14 && n == 0 {
			n = g.between(1, 3)
		}
		if d == champ {
			n = champScans
		} else {
			n = min(n, scanCap)
		}
		daily[d] = n
	}

	for d := 365; d >= 1; d-- {
		day := g.now.AddDate(0, 0, -d)
		for range daily[d] {
			at := time.Date(day.Year(), day.Month(), day.Day(),
				g.between(9, 23), g.between(0, 59), 0, 0, day.Location())
			h := g.offer(at, d == champ)
			// Evening-heavy, like a real search: a good score gets applied to
			// within a day or two.
			if h != nil && h.score >= 3.6 && g.r.Float64() < 0.55 {
				appliedAt := at.Add(time.Duration(g.between(1, 40)) * time.Hour)
				g.applied[h.id] = appliedAt
				h.appliedOn = appliedAt.Format("2006-01-02")
				g.tracker = append(g.tracker, h)
			}
		}
	}
	g.breakApplicationTies()
}

// offer generates one historical offer and everything it left on disk. It
// returns nil for the ones that never earned a tracker row — a score the Phase 1
// gate turned back.
// force is the champion day: every one of its offers lands an eval, so the day
// the heatmap paints gold is the day that actually had the most scans. Without
// it some of the champion's offers hit the gate — which writes no eval — and the
// crown lands on whichever ordinary day lost the fewest.
// ponytail: no historical failures. Today's Orbital Freight row is the only one,
// so the Failed counter reads 1 rather than a year's worth.
func (g *gen) offer(at time.Time, force bool) *hist {
	var company string
	for company == "" || reserved[company] {
		company = g.pick(companies)
	}
	role := g.pick(roles)
	g.id++
	id := g.id
	url := fmt.Sprintf("https://jobs.example.com/%s/%d", slug(company), id)
	g.input = append(g.input, fmt.Sprintf("%d\t%s\tportal-scan\tscan %s", id, url, at.Format("2006-01-02")))

	p1 := round1(2.2 + g.r.Float64()*2.8)
	if p1 < 2.5 && !force { // gated before Phase 2 — no eval, no score
		g.rows[id] = state.Row{
			ID: id, URL: url, P1Status: state.P1Scored, P1Score: fmt.Sprintf("%.1f", p1),
			Archetype: role, P2Status: state.P2Gated, P3Status: state.P3Skipped,
			Error: "p1-gated(threshold=2.5)",
		}
		return nil
	}

	score := round1(min(5, 1.8+g.r.Float64()*3.4))
	g.report++
	rnum := fmt.Sprintf("%03d", g.report)
	date := at.Format("2006-01-02")
	h := &hist{
		id: id, company: company, role: role, url: url, score: score, rnum: rnum,
		appliedOn: date, reportFile: fmt.Sprintf("%s-%s-%s.md", rnum, slug(company), date),
	}
	p3 := state.P3Skipped
	if score >= 3 {
		p3 = state.P3Completed
	}
	g.rows[id] = state.Row{
		ID: id, URL: url, P1Status: state.P1Scored, P1Score: fmt.Sprintf("%.1f", p1),
		Archetype: role, P2Status: state.P2Evaled, ReportNum: rnum, P3Status: p3,
	}
	g.writeEval(h, at)
	g.writeReport(h, date, p1)
	if score >= 3 {
		g.writeOutput(h, date)
	}
	return h
}

// writeEval dates the artifact by hand: the activity heatmap buckets a scan by
// its eval's mtime, which is the only date the fixture can move.
func (g *gen) writeEval(h *hist, at time.Time) {
	path := filepath.Join(g.l.Evals(), strconv.Itoa(h.id)+".json")
	writeArtifact(path, state.Artifact{
		Status: state.P2Evaled, Score: h.score, Company: h.company, Role: h.role,
		Archetype: h.role, ReportNum: h.rnum,
	})
	_ = os.Chtimes(path, at, at)
}

func (g *gen) writeReport(h *hist, date string, p1 float64) {
	body := reportBody(plan{Company: h.company, Role: h.role, P1: p1, Score: h.score}, h.rnum, date, h.url)
	_ = os.WriteFile(filepath.Join(g.l.Reports(), h.reportFile), []byte(body), 0o644)
}

func (g *gen) writeOutput(h *hist, date string) {
	dir := filepath.Join(g.l.Output(), fmt.Sprintf("%s_%s_%s", date, slug(h.company), h.rnum))
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "Denys-Skira-CV.pdf"), []byte("%PDF-1.4\n% demo placeholder\n"), 0o644)
}

// breakApplicationTies leaves the applications heatmap one busiest day too.
// These counts are a by-product of the scan counts rather than a plan, so the
// ties are broken after the fact: one application dropped off each rival day.
func (g *gen) breakApplicationTies() {
	byDay := map[string][]int{}
	for id, ts := range g.applied {
		k := state.DayKey(ts)
		byDay[k] = append(byDay[k], id)
	}
	max, days := 0, []string{}
	for k, v := range byDay {
		if len(v) > max {
			max = len(v)
		}
		days = append(days, k)
	}
	sort.Strings(days) // map order is random; the crown must not be
	crowned := false
	for _, k := range days {
		if len(byDay[k]) < max {
			continue
		}
		if !crowned {
			crowned = true
			continue
		}
		sort.Ints(byDay[k])
		drop := byDay[k][0]
		delete(g.applied, drop)
		for i, h := range g.tracker {
			if h.id == drop {
				g.tracker = append(g.tracker[:i], g.tracker[i+1:]...)
				break
			}
		}
	}
}

// board assigns tracker statuses by age, so the cadence maths produces one of
// every rung on the follow-ups tab: urgent (a reply landed today), overdue
// (applied a week or more ago with no nudge), waiting (applied this week), cold
// (two nudges already sent).
func (g *gen) board() {
	plans := []struct {
		status string
		age    int
		nudges int
	}{
		{"Responded", 0, 0}, {"Responded", 1, 0}, {"Interview", 3, 0},
		{"Applied", 19, 0}, {"Applied", 14, 0}, {"Applied", 11, 0}, {"Applied", 9, 0},
		{"Applied", 5, 0}, {"Applied", 3, 0}, {"Applied", 2, 0},
		{"Applied", 26, 2}, {"Applied", 31, 2},
	}
	// The most recent applications, newest first.
	recent := g.tracker
	if len(recent) > 14 {
		recent = recent[len(recent)-14:]
	}
	live := map[*hist]bool{}
	for i := len(recent) - 1; i >= 0 && len(live) < len(plans); i-- {
		p := plans[len(live)]
		h := recent[i]
		h.status, h.nudges = p.status, p.nudges
		h.appliedOn = g.now.AddDate(0, 0, -p.age).Format("2006-01-02")
		live[h] = true
	}
	for _, h := range g.tracker {
		if h.status == "" {
			h.status = g.pick([]string{"Rejected", "Rejected", "Rejected", "Discarded", "Applied"})
		}
		// An old "Applied" with no reply is noise on the board — retire it.
		if h.status == "Applied" && !live[h] {
			h.status = "Rejected"
		}
	}
	for i, h := range g.tracker {
		h.num = i + 1
	}
}

// today is what the TUI shows at launch: six rows dated now, one of every shape
// the list can draw — finished with a CV, finished below the CV threshold,
// stopped at the Phase 1 gate, and failed mid-evaluation.
func (g *gen) today() error {
	g.id = ((g.id + 20) / 10) * 10 // clear of the generated history's ids
	for _, t := range todayRows {
		g.id++
		id := g.id
		url := fmt.Sprintf("https://jobs.example.com/%s/%d", slug(t.company), id)
		at := g.now.Add(-time.Duration(g.between(1, 6)) * time.Hour)
		g.input = append(g.input, fmt.Sprintf("%d\t%s\tportal-scan\tscan %s", id, url, at.Format("2006-01-02")))
		g.plans[strconv.Itoa(id)] = plan{Company: t.company, Role: t.role, P1: t.p1, Score: t.score}

		jd := filepath.Join(g.l.JDs(), strconv.Itoa(id)+".txt")
		if err := os.WriteFile(jd, []byte(fmt.Sprintf("%s at %s\n\nSample posting. Invented for the demo.\n", t.role, t.company)), 0o644); err != nil {
			return err
		}
		if err := os.Chtimes(jd, at, at); err != nil {
			return err
		}
		writeArtifact(filepath.Join(g.l.Scores(), strconv.Itoa(id)+".json"), state.Artifact{
			Status: state.P1Scored, Score: t.p1, Company: t.company, Role: t.role, Archetype: t.role,
		})

		row := state.Row{
			ID: id, URL: url, P1Status: state.P1Scored,
			P1Score: fmt.Sprintf("%.1f", t.p1), Archetype: t.role,
		}
		switch t.kind {
		case "gated":
			row.P2Status, row.P3Status = state.P2Gated, state.P3Skipped
			row.Error = "p1-gated(threshold=2.5)"
		case "failed":
			row.P2Status, row.Retries = state.P2Failed, 1
			row.Error = "stage3 (judgment) failed: model returned no JSON after 2 attempts"
		default:
			g.report++
			date := g.now.Format("2006-01-02")
			h := &hist{
				id: id, company: t.company, role: t.role, url: url, score: t.score,
				rnum: fmt.Sprintf("%03d", g.report), appliedOn: date,
			}
			h.reportFile = fmt.Sprintf("%s-%s-%s.md", h.rnum, slug(t.company), date)
			row.P2Status, row.ReportNum = state.P2Evaled, h.rnum
			row.P3Status = state.P3Skipped
			if t.kind == "done" {
				row.P3Status = state.P3Completed
				g.writeOutput(h, date)
			}
			g.writeEval(h, at)
			g.writeReport(h, date, t.p1)
			if t.applied {
				g.applied[id] = at
				h.status, h.num = "Applied", len(g.tracker)+1
				g.tracker = append(g.tracker, h)
			}
		}
		g.rows[id] = row
		// The error text pane reads the phase's own payload before the state
		// row's truncated column, so the failed row gets a real one.
		if t.kind == "failed" {
			writeArtifact(filepath.Join(g.l.Evals(), strconv.Itoa(id)+".json"), state.Artifact{
				Status: state.P2Failed, Company: t.company, Role: t.role,
				Error: "stage3 (judgment) failed after 2 attempts\n\n" +
					"  model:    snipe-eval (14B, q4_K_M)\n" +
					"  context:  8192 tokens\n" +
					"  last reply ended mid-object:\n\n" +
					"    {\"company\":\"Orbital Freight\",\"role\":\"Backend Engineer\",\"cv_match\":4,\n" +
					"     \"north_star\":4,\"red_flags_score\":5,\"score\":\n\n" +
					"  The judgment stage is checkpointed, so a retry re-runs this call and\n" +
					"  keeps the JD parse and the evidence table behind it.",
			})
			// Dated with the row so the failure sits in today's group.
			_ = os.Chtimes(filepath.Join(g.l.Evals(), strconv.Itoa(id)+".json"), at, at)
		}
	}
	return nil
}

func (g *gen) write() error {
	if err := state.Save(g.l.State(), g.rows); err != nil {
		return err
	}
	if err := os.WriteFile(g.l.Input(),
		[]byte("id\turl\tsource\tnotes\n"+strings.Join(g.input, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := g.applied.Save(g.l.Applied()); err != nil {
		return err
	}
	for _, f := range []string{g.l.Queue(), g.l.Skipped(), g.l.Declined()} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			return err
		}
	}
	b, _ := jsonIndent(g.plans)
	if err := os.WriteFile(planPath(g.l), b, 0o644); err != nil {
		return err
	}

	// The tracker reads newest-first, which is the order the merge keeps.
	var rows, fus []string
	fuNum := 0
	for i := len(g.tracker) - 1; i >= 0; i-- {
		h := g.tracker[i]
		pdf := "N"
		if h.score >= 3 {
			pdf = "Y"
		}
		rows = append(rows, fmt.Sprintf("| %d | %s | %s | %s | %.1f/5 | %s | %s | [%s](../reports/%s) | Auto-evaluated — score %.1f/5 |",
			h.num, h.appliedOn, h.company, h.role, h.score, h.status, pdf, h.rnum, h.reportFile, h.score))
	}
	for _, h := range g.tracker {
		for i := range h.nudges {
			fuNum++
			fus = append(fus, fmt.Sprintf("| %d | %d | %s | %s | %s | %s | — | Nudge %d sent |",
				fuNum, h.num, g.now.AddDate(0, 0, -(20-i*7)).Format("2006-01-02"),
				h.company, h.role, g.pick(channels), i+1))
		}
	}
	if err := os.WriteFile(g.l.Tracker(), []byte(
		"# Applications Tracker\n\n> Sample data — generated by demo/sandbox for the TUI demo.\n\n"+
			"| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n"+
			"|---|------|---------|------|-------|--------|-----|--------|-------|\n"+
			strings.Join(rows, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(g.l.Followups(), []byte(
		"# Follow-ups Log\n\n"+
			"| # | App | Date | Company | Role | Channel | Contact | Notes |\n"+
			"|---|-----|------|---------|------|---------|---------|-------|\n"+
			strings.Join(fus, "\n")+"\n"), 0o644)
}
