package cvselect

import (
	"math"
	"regexp"
	"sort"
)

// Options are the selector's knobs. Every default is measured; the comments say
// what by.
type Options struct {
	MaxProjects          int
	MaxBulletsPerProject int
	MaxBulletsPerRole    int

	// MinTopExpBullets floors the highest-scoring experience entry.
	//
	// It applies to ONE entry, and that restriction is the whole result. Swept
	// over the 128-offer corpus, a floor on *every* experience entry costs 0.055
	// differentiator coverage held out — the entry the budget actually starves
	// is the teaching assistantship (1.39 bullets, at the floor in 71% of
	// offers) rather than the commercial role (2.41, 25%), so a blanket floor
	// spends the page on the least differentiating evidence on the CV.
	// Restricted to the top entry it costs 0.018 held out.
	//
	// That 0.018 is a real cost, not noise — Phase 3's A/A floor is 0.000. It
	// buys something the metric cannot see: the one employer a reader checks
	// first never renders as a single line. Differentiator coverage scores an
	// Experience section of two one-line entries perfectly if the projects
	// carry the differentiators.
	//
	// 1 restores the pre-floor allocation exactly, which is what makes this
	// measurable as one change.
	MinTopExpBullets int

	// ProjMaxLines is how much of LineBudget the Projects section may take.
	//
	// One pool lets a strongly-matching posting spend the page on projects and
	// leave both employers at a single line — which is what the 24-line budget
	// did the day it landed, invisibly, because no metric read section balance
	// until all_exp_starved_pct existed. This bounds the contest rather than
	// flooring an entry inside it, so it needs no rule about which entry
	// deserves protection.
	//
	// 14 of 24 was the swept optimum; 13 of 22 carries that same 58% share
	// through the LaTeX template's smaller budget. The share is carried over,
	// not re-swept: re-sweeping costs a judge call per offer and nothing about
	// the trade it encodes changed. 0 disables it.
	ProjMaxLines float64

	// LineBudget is the total rendered bullet-lines the page can carry, across
	// experience AND projects. 0 keeps the older count-based behaviour.
	//
	// The page is rationed in lines; this used to ration bullets. A four-line
	// bullet and a one-line bullet cost the same against a count budget and four
	// times as much of the page, so the ranker had never seen the page at all.
	//
	// Production passes the MINIMUM of the measured capacity range, not the
	// median: a budget that fits the typical offer overruns the third of them
	// carrying the heaviest chrome, and the gate is "32 of 32 fit", not "most
	// fit". All 220 cv-content.json in output/ re-filled and compiled gave p10
	// 22.9, p25 23.9, median 25.9 — hence 22.
	LineBudget float64

	// ProjectBulletBudget is the count-based fallback, used when LineBudget is 0.
	// Exactly what 4 projects × 2 bullets already rendered, so it redistributes
	// the page rather than buying more of it.
	ProjectBulletBudget int

	// PinnedProjects survive the MaxProjects cut whatever the posting scores
	// them, matched case-insensitively as a substring of the entry title.
	//
	// Relevance ranking is the right default and the wrong answer for entries
	// whose value is not what they are about: an Honours dissertation is the
	// thing a reader of a graduate CV looks for, and it lost the cut on postings
	// with no use for its subject. A deliberate override of the ranker, so it
	// belongs in the user layer.
	PinnedProjects []string

	// SpikeWeight is the weight on corpus-relative specificity. 6 is the
	// measured optimum and the curve is broad (4 → +0.075, 6 → +0.084, 8 →
	// +0.079), so it is a plateau, not a knife edge. 0 disables it.
	SpikeWeight float64
}

func DefaultOptions() Options {
	return Options{
		MaxProjects: 4, MaxBulletsPerProject: 4, MaxBulletsPerRole: 4,
		MinTopExpBullets: 2, ProjMaxLines: 13, LineBudget: 22,
		ProjectBulletBudget: 8, SpikeWeight: 6,
	}
}

// JudgeBlend is how much each judge grade is worth against cosine.
//
// Grades are 0–3 and the term is flat. Measured on the gold set at +0.10 pair
// accuracy over cosine alone (CI [0.027, 0.190], 7 offers better, 0 worse), and
// the gain held when the exemplar pair was swapped, so it is not fitted to two
// lucky offers. Alone it is worth nothing — 0.801 against cosine's 0.815; only
// the blend pays.
const JudgeBlend = 0.10

// SpikeAlpha converts the spike weight into the coefficient actually applied.
//
// The sweep scored cos + w·(cos − mean). Dividing by (1+w) is a positive scale
// factor, so it ranks identically — but it keeps cosine on its original scale,
// which matters because the judge term is a flat +0.10 per grade benchmarked
// against unscaled cosine. Applied raw, w=6 would inflate the cosine part
// sevenfold and quietly delete the rerank.
func SpikeAlpha(w float64) float64 { return w / (1 + w) }

var digitRe = regexp.MustCompile(`\d`)

// Trim keeps an entry's top `keep` bullets in relevance order, guaranteeing at
// least one carrying a digit.
//
// budgeted makes that guarantee pay for itself. The swap replaces the
// lowest-ranked kept bullet with the best digit-carrying one, which under a line
// budget can trade a one-line bullet for a four-line one and put the page three
// lines over with no ranker involved. It fires on 42% of single-slot project
// bullets, so it is not a corner case — under a budget the swap only happens
// when the replacement is no longer than what it displaces.
func Trim(e *Entry, keep int, budgeted bool) {
	ranked := append([]ScoredBullet{}, e.Scored...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })

	if keep > len(ranked) {
		keep = len(ranked)
	}
	kept := append([]ScoredBullet{}, ranked[:max(0, keep)]...)

	if len(kept) > 0 && !anyDigit(kept) {
		out := kept[len(kept)-1]
		for _, b := range ranked {
			if !digitRe.MatchString(b.Text) {
				continue
			}
			if budgeted && BulletLines(b.Text) > BulletLines(out.Text) {
				continue
			}
			kept[len(kept)-1] = b
			break
		}
	}
	e.Bullets = e.Bullets[:0]
	for _, b := range kept {
		e.Bullets = append(e.Bullets, b.Text)
	}
	if len(ranked) > 0 {
		e.Score = ranked[0].Score
	} else {
		e.Score = 0
	}
}

func anyDigit(bs []ScoredBullet) bool {
	for _, b := range bs {
		if digitRe.MatchString(b.Text) {
			return true
		}
	}
	return false
}

func sortedScored(e *Entry) []ScoredBullet {
	out := append([]ScoredBullet{}, e.Scored...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Allocate is the count-based split: one bullet apiece, then the rest to the
// highest-scoring bullets anywhere, capped per entry.
//
// Greedy over the pooled bullets rather than a proportional split of the scores,
// because the scores are cosines in a narrow band — a proportional rule off 0.58
// against 0.55 is noise, while "whose next bullet is best" is the question the
// page is actually asking.
func Allocate(entries []*Entry, budget, cap int) map[*Entry]int {
	n := map[*Entry]int{}
	for _, e := range entries {
		n[e] = 1
	}
	spent := len(entries)

	type cand struct {
		e     *Entry
		score float64
	}
	var rest []cand
	for _, e := range entries {
		for _, b := range sortedScored(e)[min(1, len(e.Scored)):] {
			rest = append(rest, cand{e, b.Score})
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].score > rest[j].score })

	for _, b := range rest {
		if spent >= budget {
			break
		}
		if n[b.e] >= cap {
			continue
		}
		n[b.e]++
		spent++
	}
	return n
}

// AllocateLines is the same allocation, spending rendered LINES instead of
// bullet slots, over every entry on the page at once rather than projects alone.
//
// Two changes from Allocate, and they are the whole of it:
//
//  1. A bullet costs what it occupies. Against a count budget a four-line
//     bullet and a one-line bullet are the same price and four times the page.
//  2. Experience and projects draw on one budget. Experience had none at all —
//     a flat per-role cap — so two roles took whatever they took and projects
//     were trimmed around them.
//
// Ranking is by score per line, not score. That is the question a fixed page
// actually asks: not "which bullet is best" but "which bullet is the best use of
// the three lines it costs". A marginally weaker one-line bullet beats a
// marginally stronger three-line one, and beating it is the point.
//
// Every entry keeps its top bullet before any of this, so a budget too small to
// go round starves nobody entirely — it stops early and the result is over
// budget rather than an entry rendered as a bare heading.
func AllocateLines(entries []*Entry, budget float64, caps map[*Entry]int,
	expSet map[*Entry]bool, floorMin int, projMax float64) map[*Entry]int {

	n := map[*Entry]int{}
	isProj := func(e *Entry) bool { return expSet == nil || !expSet[e] }

	var spent, projSpent float64
	for _, e := range entries {
		n[e] = 1
		if s := sortedScored(e); len(s) > 0 {
			spent += BulletCost(s[0].Text)
			if isProj(e) {
				// The one-bullet-each pass counts toward the section cap but is
				// never blocked by it — a project on the page has to render a
				// bullet or it ships as a bare title.
				projSpent += BulletCost(s[0].Text)
			}
		}
	}

	// The floor, paid before the open contest and out of the same budget, so it
	// takes lines from projects exactly as it appears to.
	if expSet != nil && floorMin > 1 {
		var best *Entry
		var bestTop float64
		for _, e := range entries {
			if !expSet[e] || len(e.Scored) == 0 {
				continue
			}
			top := sortedScored(e)[0].Score
			if best == nil || top > bestTop {
				best, bestTop = e, top
			}
		}
		if best != nil {
			ranked := sortedScored(best)
			want := min(floorMin, len(ranked))
			if c, ok := caps[best]; ok {
				want = min(want, c)
			}
			for n[best] < want {
				spent += BulletCost(ranked[n[best]].Text)
				n[best]++
			}
		}
	}

	type cand struct {
		e     *Entry
		cost  float64
		score float64
	}
	var rest []cand
	for _, e := range entries {
		for _, b := range sortedScored(e)[min(n[e], len(e.Scored)):] {
			rest = append(rest, cand{e, BulletCost(b.Text), b.Score})
		}
	}
	// Sorted once by value density. Re-sorting after each pick would be a true
	// greedy knapsack, but the costs here are 1–4 and the scores are cosines in
	// a narrow band, so the order does not change — and the sort is over every
	// unpicked bullet on the page, which is not free.
	sort.SliceStable(rest, func(i, j int) bool {
		return rest[i].score/rest[i].cost > rest[j].score/rest[j].cost
	})

	for _, b := range rest {
		// `continue`, not `break`: a cheaper bullet later in the list may still
		// fit where this one does not.
		if spent+b.cost > budget {
			continue
		}
		if c, ok := caps[b.e]; ok && n[b.e] >= c {
			continue
		}
		if projMax > 0 && isProj(b.e) && projSpent+b.cost > projMax {
			continue
		}
		n[b.e]++
		spent += b.cost
		if isProj(b.e) {
			projSpent += b.cost
		}
	}
	return n
}

// TotalLines is what an allocation costs the page, for the caller that wants to
// check its own arithmetic.
func TotalLines(entries []*Entry, n map[*Entry]int) float64 {
	var total float64
	for _, e := range entries {
		ranked := sortedScored(e)
		for i := 0; i < n[e] && i < len(ranked); i++ {
			total += BulletCost(ranked[i].Text)
		}
	}
	return math.Round(total*1000) / 1000
}
