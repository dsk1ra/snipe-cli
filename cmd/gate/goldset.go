package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/embed"
	"github.com/dsk1ra/snipe-cli/internal/fit"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase2"
)

// The Phase 2 evidence gold set.
//
// Every other gate in this directory asks "does Go agree with the Node oracle".
// That question cannot see a retrieval defect both implementations share, and
// they shared one: a Skills or Education catalogue line was rank-1 for 44% of
// real requirements, took two of the three candidate slots on 17.8% of them and
// all three on 3.2% — evidence fit.Catalogue caps at Transferable before the
// grader reads it. Both implementations agreed perfectly while doing it.
//
// So this gate asks the other question — does retrieval agree with a human —
// and it is the only one here that needs one. It is the Node `batch/goldset.mjs`
// idea moved to the space the defect lives in: that sheet ranked whole CV atoms
// per OFFER for Phase 3 selection, this one ranks them per REQUIREMENT for
// Phase 2 evidence.
//
//	go run ./cmd/gate goldset sheet <home> [offers] > batch/bench/evidence-goldset.md
//	<tick the boxes>
//	go run ./cmd/gate goldset score <home> batch/bench/evidence-goldset.md
//
// Pooled labelling: the sheet offers the union of what the old and new rankers
// return, not the whole 65-atom index, which is what keeps the job to minutes
// rather than an evening. The cost is that neither ranker can be credited for
// an atom nobody was ever shown — fine for an A/B between two rankers over the
// same pool, wrong for an absolute recall number, so this prints neither.
//
// The ticks are the record. If the ranker disagrees with them, the ranker is
// what changes.
func gateGoldset(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gate goldset sheet <home> [offers] [previous-sheet.md]\n"+
			"       gate goldset score <home> <sheet.md>")
		return 2
	}
	home := args[1]
	switch args[0] {
	case "sheet":
		n := 5
		if len(args) > 2 {
			n, _ = strconv.Atoi(args[2])
		}
		prior := ""
		if len(args) > 3 {
			prior = args[3]
		}
		return goldsetSheet(home, n, prior)
	case "score":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: gate goldset score <home> <sheet.md>")
			return 2
		}
		return goldsetScore(home, args[2])
	}
	fmt.Fprintf(os.Stderr, "unknown goldset mode %q\n", args[0])
	return 2
}

// poolK is how deep each ranker contributes to the pool. One past the three
// slots either ranker fills, so a near-miss is on the sheet and can be seen to
// be a near-miss rather than being invisible.
const poolK = 4

type goldCase struct {
	report string
	req    string
	pool   []int // atom indices, ascending
	want   map[int]bool
}

// corpus is everything both modes need: the atoms, their vectors, and the
// requirements to rank them against.
func goldsetCorpus(home string) (paths.Layout, []embed.Atom, error) {
	l := paths.Layout{Home: home, Code: home}
	cfg, err := config.Load(l)
	if err != nil {
		return l, nil, err
	}
	atoms := embed.ExtractAtoms(cfg.CVRaw, embed.ReadFileOr(filepath.Join(l.Home, "article-digest.md")))
	if len(atoms) == 0 {
		return l, nil, fmt.Errorf("no atoms extracted from %s", l.CV())
	}
	ix := &embed.Indexer{OL: ollama.New("", nil), CVPath: l.CVIndex(), JDPath: l.JDIndex(), JDsDir: l.JDs()}
	indexed, err := ix.LoadCV(context.Background(), atoms)
	if err != nil {
		return l, nil, err
	}
	return l, indexed, nil
}

// recentReports lists the newest report files that carry a parseable Block B,
// newest first.
func recentReports(l paths.Layout, n int) ([]string, error) {
	ents, err := os.ReadDir(l.Reports())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	var out []string
	for _, name := range names {
		if len(out) == n {
			break
		}
		b, err := os.ReadFile(filepath.Join(l.Reports(), name))
		if err != nil {
			continue
		}
		if len(cvselect.ExtractBlockBRequirements(string(b))) > 0 {
			out = append(out, name)
		}
	}
	return out, nil
}

// rank returns both rankers' picks for one query, as atom indices into index.
func rank(q []float64, index []embed.Atom, k int) (before, after []int, err error) {
	byText := map[string]int{}
	for i, a := range index {
		byText[a.Text] = i
	}
	idx := func(s []embed.Scored) []int {
		out := make([]int, 0, len(s))
		for _, c := range s {
			out = append(out, byText[c.Text])
		}
		return out
	}

	old, err := embed.TopK(q, index, k)
	if err != nil {
		return nil, nil, err
	}
	var demo, catalogue []embed.Atom
	for _, a := range index {
		if fit.Catalogue(a.Source) {
			catalogue = append(catalogue, a)
		} else {
			demo = append(demo, a)
		}
	}
	// The production ranker, called directly. A gate pass is a statement about
	// what runs, or it is a statement about nothing.
	new, err := phase2.TopCandidates(q, demo, catalogue, k)
	if err != nil {
		return nil, nil, err
	}
	return idx(old), idx(new), nil
}

// goldsetSheet cuts a new sheet, carrying over the ticks of a previous one.
//
// Carrying them is not a convenience. A sheet is hand-labelled, the labels are
// the most expensive thing in this directory, and widening the corpus without
// this means re-ticking everything already answered — which is how the Node
// goldset's 174 ticks were lost. parseGoldsheet's atom-count check is what makes
// it safe: if cv.md has moved, the old ids point at different atoms and the
// carry is refused rather than silently wrong.
func goldsetSheet(home string, n int, prior string) int {
	l, index, err := goldsetCorpus(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// Ticks are carried by the atom's TEXT, not its id.
	//
	// Ids are positions in cv.md, so adding one line renumbers everything below
	// it and every id-keyed tick then points at a different atom. That is why
	// scoring refuses a sheet whose atom count has moved — but it would also
	// mean editing the CV throws away every label, which makes the sheet a
	// reason not to improve the CV. Text survives renumbering; a tick whose text
	// is genuinely gone is reported rather than dropped in silence.
	ticked, keep, err := priorTicks(prior)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot carry ticks from %s: %v\n", prior, err)
		return 2
	}
	byText := map[string]bool{}
	for _, a := range index {
		byText[a.Text] = true
	}
	for key, texts := range ticked {
		for t := range texts {
			if !byText[t] {
				fmt.Fprintf(os.Stderr, "  %s: ticked atom is no longer in cv.md, tick dropped: %.70s\n",
					strings.ReplaceAll(key, "\x00", " "), t)
			}
		}
	}
	carried, fresh, stranded := 0, 0, 0
	// With a prior sheet, `n` widens rather than replaces: its reports are kept
	// and the newest n it does not already hold are added.
	//
	// Without this the sheet is a sliding window over the most recent reports,
	// and every tick silently stops applying as soon as n more offers land —
	// which had already happened once, seven reports later, with nothing saying
	// so. cv.md moving is guarded; the corpus moving underneath was not.
	recent, err := recentReports(l, n+len(keep))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var reports []string
	held := map[string]bool{}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(l.Reports(), name)); err != nil {
			fmt.Fprintf(os.Stderr, "prior sheet's %s is no longer on disk — its ticks are dropped\n", name)
			continue
		}
		held[name] = true
		reports = append(reports, name)
	}
	for _, name := range recent {
		if len(reports) >= n+len(held) {
			break
		}
		if !held[name] {
			held[name] = true
			reports = append(reports, name)
		}
	}
	if len(reports) == 0 {
		fmt.Fprintln(os.Stderr, "no reports with a Block B")
		return 2
	}

	ol := ollama.New("", nil)
	var b strings.Builder
	fmt.Fprintf(&b, "# Phase 2 evidence gold set\n\n")
	fmt.Fprintf(&b, "cv-atoms: %d\n\n", len(index))
	b.WriteString("Tick `[x]` every line that is *genuine evidence* for the requirement above it:\n")
	b.WriteString("the work it describes would let you answer that requirement in an interview.\n")
	b.WriteString("Leave the rest. No ranking, no partial credit — evidence or not.\n\n")
	b.WriteString("A Skills or Education line is a claim, not a demonstration. Tick it only when\n")
	b.WriteString("nothing on the CV demonstrates the requirement and the claim is all there is.\n\n")
	b.WriteString("Then: `go run ./cmd/gate goldset score <home> <this file>`\n\n")

	for _, name := range reports {
		raw, err := os.ReadFile(filepath.Join(l.Reports(), name))
		if err != nil {
			continue
		}
		reqs := cvselect.ExtractBlockBRequirements(string(raw))
		if len(reqs) > 6 {
			reqs = reqs[:6] // six is enough per offer, and the sheet has to stay tickable
		}
		vecs, err := embed.Batch(context.Background(), ol, reqs, ollama.ModelEmbed)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintf(&b, "---\n\n## %s\n\n", name)
		for i, r := range reqs {
			before, after, err := rank(vecs[i], index, poolK)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			seen := map[int]bool{}
			var pool []int
			for _, id := range append(append([]int{}, before...), after...) {
				if !seen[id] {
					seen[id] = true
					pool = append(pool, id)
				}
			}
			// Ascending, never by score: listing the pool in either ranker's
			// order anchors the ticks on the answer the sheet exists to check.
			sort.Ints(pool)
			was, seenBefore := ticked[name+"\x00"+r]
			if seenBefore {
				carried++
			} else {
				fresh++
			}
			// The pool is ranked live, so a tick can only be carried if its atom
			// is still in it. One that is not would otherwise just not appear —
			// a hand label lost with nothing said, which is the whole failure
			// this carry exists to prevent.
			inPool := map[string]bool{}
			for _, id := range pool {
				inPool[index[id].Text] = true
			}
			for t := range was {
				if byText[t] && !inPool[t] {
					fmt.Fprintf(os.Stderr, "  %s R%d: ticked atom no longer ranks, tick dropped: %.60s\n",
						name, i+1, t)
					stranded++
				}
			}
			fmt.Fprintf(&b, "### R%d %s\n\n", i+1, r)
			for _, id := range pool {
				mark := " "
				if m := was[index[id].Text]; m != "" {
					mark = m
				}
				fmt.Fprintf(&b, "- [%s] **%d** *(%s)* %s\n", mark, id+1, index[id].Source, index[id].Text)
			}
			b.WriteString("\n")
		}
	}
	if prior != "" {
		fmt.Fprintf(os.Stderr, "%d report(s): %d carried from %s, %d new\n",
			len(reports), len(keep), filepath.Base(prior), len(reports)-len(keep))
		fmt.Fprintf(os.Stderr, "ticks carried for %d requirement(s); %d still need labelling\n", carried, fresh)
		if stranded > 0 {
			fmt.Fprintf(os.Stderr, "%d tick(s) could not be carried — see above\n", stranded)
		}
	}
	fmt.Print(b.String())
	return 0
}

var (
	goldReportRe = regexp.MustCompile(`^##\s+(\S.*?)\s*$`)
	goldReqRe    = regexp.MustCompile(`^###\s+R\d+\s+(.*\S)\s*$`)
	goldTickRe   = regexp.MustCompile(`^-\s*\[([ xX])\]\s*\*\*(\d+)\*\*`)
	goldAtomsRe  = regexp.MustCompile(`^cv-atoms:\s*(\d+)`)
)

// parseGoldsheet reads the ticked sheet back.
//
// Atom ids are positional in cv.md, so a bullet added above one renumbers
// everything after it and every tick then points at the wrong atom. The atom
// count is the cheapest thing that changes when that happens, and it is checked
// rather than merely recorded — the Node exemplar file wrote a cvHash it never
// compared, which is how it went stale in silence.
func parseGoldsheet(path string, atoms int) ([]goldCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []goldCase
	var report string
	var cur *goldCase
	declared := -1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := goldAtomsRe.FindStringSubmatch(line); m != nil {
			declared, _ = strconv.Atoi(m[1])
			continue
		}
		if m := goldReqRe.FindStringSubmatch(line); m != nil {
			out = append(out, goldCase{report: report, req: m[1], want: map[int]bool{}})
			cur = &out[len(out)-1]
			continue
		}
		if m := goldReportRe.FindStringSubmatch(line); m != nil {
			report, cur = m[1], nil
			continue
		}
		if m := goldTickRe.FindStringSubmatch(line); m != nil && cur != nil {
			id, _ := strconv.Atoi(m[2])
			cur.pool = append(cur.pool, id-1)
			if m[1] != " " {
				cur.want[id-1] = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if declared != atoms {
		return nil, fmt.Errorf("sheet was written against %d CV atoms, cv.md now yields %d — the ids have moved, re-cut the sheet", declared, atoms)
	}
	return out, nil
}

func goldsetScore(home, sheet string) int {
	_, index, err := goldsetCorpus(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cases, err := parseGoldsheet(sheet, len(index))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var marked, blank []goldCase
	for _, c := range cases {
		if len(c.want) > 0 {
			marked = append(marked, c)
		} else {
			blank = append(blank, c)
		}
	}
	if len(marked) == 0 {
		fmt.Fprintln(os.Stderr, "sheet has no ticks yet")
		return 2
	}

	ol := ollama.New("", nil)
	texts := make([]string, len(marked))
	for i, c := range marked {
		texts[i] = c.req
	}
	vecs, err := embed.Batch(context.Background(), ol, texts, ollama.ModelEmbed)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	type tally struct{ hits, anyHit, catSlots, slots int }
	score := func(t *tally, picks []int, want map[int]bool) {
		any := false
		for _, id := range picks {
			t.slots++
			if want[id] {
				t.hits++
				any = true
			}
			if fit.Catalogue(index[id].Source) {
				t.catSlots++
			}
		}
		if any {
			t.anyHit++
		}
	}

	// Scored at every k up to the production one, because the two numbers point
	// opposite ways and the choice of k is the trade between them: p@k falls as
	// slots are added that are not evidence, hit@k rises as a wider net catches
	// the requirement's one real match. Ranked separately at each k rather than
	// sliced from the k=3 result — the two-pool ranker allocates its slots
	// between the pools, so its top 1 of 3 is not what it would return for 1.
	const prodK = 3
	at := make([]struct{ before, after tally }, prodK+1)
	for k := 1; k <= prodK; k++ {
		for i, c := range marked {
			b, a, err := rank(vecs[i], index, k)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			score(&at[k].before, b, c.want)
			score(&at[k].after, a, c.want)
		}
	}
	before, after := at[prodK].before, at[prodK].after

	n := float64(len(marked))
	row := func(name string, pick func(int) tally) {
		fmt.Printf("  %-22s", name)
		for k := 1; k <= prodK; k++ {
			t := pick(k)
			fmt.Printf("  %.3f", float64(t.hits)/float64(t.slots))
		}
		for k := 1; k <= prodK; k++ {
			t := pick(k)
			fmt.Printf("  %.3f", float64(t.anyHit)/n)
		}
		fmt.Printf("   %.2f\n", float64(pick(prodK).catSlots)/n)
	}
	fmt.Printf("%s — %d labelled requirements over %d atoms\n\n", filepath.Base(sheet), len(marked), len(index))
	fmt.Printf("  %-22s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s   %s\n",
		"", "p@1", "p@2", "p@3", "hit@1", "hit@2", "hit@3", "cat/req")
	row("single pool (before)", func(k int) tally { return at[k].before })
	row("two pool (after)", func(k int) tally { return at[k].after })
	fmt.Println()

	// p@k counts a slot that holds no evidence as a miss, and a requirement
	// nothing on the CV demonstrates offers prodK of them. Those requirements
	// are excluded above because a sheet cannot tell "reviewed, none of these"
	// from "not reviewed" — but if they were reviewed, this is the real number,
	// and it is the case where a confident-looking table does the most harm.
	if len(blank) > 0 {
		all := float64(after.slots + len(blank)*prodK)
		fmt.Printf("  %d requirement(s) carry no tick. If that means \"nothing here is evidence\"\n"+
			"  rather than \"not reviewed yet\", p@%d over all %d requirements is %.3f, not %.3f.\n\n",
			len(blank), prodK, len(marked)+len(blank), float64(after.hits)/all,
			float64(after.hits)/float64(after.slots))
	}

	// An A/B over one pool, so the bar is the ranker it replaced. Equal is a
	// pass: the change exists to stop a catalogue line taking every slot, and
	// holding precision while doing that is the win.
	if after.hits >= before.hits {
		fmt.Printf("GATE PASS — two-pool retrieval agrees with the labels at least as often (%d/%d vs %d/%d picks).\n",
			after.hits, after.slots, before.hits, before.slots)
		return 0
	}
	fmt.Printf("GATE FAIL — two-pool retrieval lost %d correct picks. Fix the ranker, not the labels.\n", before.hits-after.hits)
	return 1
}

// goldLenientTickRe reads a sheet line without needing the ids to still be
// valid: the atom's text is what the carry matches on, and the mark character
// is kept so a hand tick stays distinguishable from a machine one across a
// re-cut.
var goldLenientTickRe = regexp.MustCompile(`^-\s*\[([ xX])\]\s*\*\*\d+\*\*\s*\*\([^)]*\)\*\s*(.*\S)\s*$`)

// priorTicks reads a previous sheet for its ticks and its report order.
//
// Deliberately lenient where parseGoldsheet is strict. Scoring must refuse a
// sheet whose ids have moved, because it indexes atoms by id. Carrying must
// not: it matches on text, which is exactly what survives the renumbering.
func priorTicks(path string) (map[string]map[string]string, []string, error) {
	ticks := map[string]map[string]string{}
	if path == "" {
		return ticks, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var reports []string
	seen := map[string]bool{}
	report, key := "", ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := goldReqRe.FindStringSubmatch(line); m != nil {
			key = report + "\x00" + m[1]
			continue
		}
		if m := goldReportRe.FindStringSubmatch(line); m != nil {
			report, key = m[1], ""
			if !seen[report] {
				seen[report] = true
				reports = append(reports, report)
			}
			continue
		}
		m := goldLenientTickRe.FindStringSubmatch(line)
		if m == nil || key == "" || m[1] == " " {
			continue
		}
		if ticks[key] == nil {
			ticks[key] = map[string]string{}
		}
		ticks[key][m[2]] = m[1]
	}
	return ticks, reports, sc.Err()
}
