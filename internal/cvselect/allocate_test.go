package cvselect

import "testing"

func entry(head string, bullets ...string) *Entry {
	e := &Entry{Head: []string{head}, Bullets: bullets}
	for i, b := range bullets {
		// Descending scores, so "first bullet" and "best bullet" coincide and a
		// test that gets the order wrong fails loudly.
		e.Scored = append(e.Scored, ScoredBullet{Text: b, Score: 1 - float64(i)*0.1})
	}
	return e
}

func TestTrimGuaranteesAMetricBullet(t *testing.T) {
	// The top-ranked bullets carry no digit; the swap pulls one in.
	e := entry("**Role**", "Led the team", "Owned the roadmap", "Cut latency by 40%")
	Trim(e, 2, false)
	if len(e.Bullets) != 2 {
		t.Fatalf("kept %d, want 2", len(e.Bullets))
	}
	if !digitRe.MatchString(e.Bullets[1]) {
		t.Errorf("no metric bullet was swapped in: %v", e.Bullets)
	}
	// The swap replaces the LOWEST-ranked kept bullet, not the highest.
	if e.Bullets[0] != "Led the team" {
		t.Errorf("the top bullet was displaced: %q", e.Bullets[0])
	}
}

func TestBudgetedTrimWillNotBuyALongerBullet(t *testing.T) {
	// Under a line budget the swap must not trade a one-line bullet for a
	// four-line one — that puts the page over with no ranker involved.
	long := "Cut latency by 40% " + str(400)
	e := entry("**Role**", "Led the team", "Owned it", long)
	Trim(e, 2, true)
	for _, b := range e.Bullets {
		if b == long {
			t.Error("a budgeted trim swapped in a longer bullet")
		}
	}
	// Unbudgeted, the same swap is allowed.
	e2 := entry("**Role**", "Led the team", "Owned it", long)
	Trim(e2, 2, false)
	if e2.Bullets[1] != long {
		t.Error("an unbudgeted trim refused the swap")
	}
}

func TestTrimKeepsExistingMetricBullet(t *testing.T) {
	e := entry("**Role**", "Cut latency by 40%", "Led the team", "Shipped 9 releases")
	Trim(e, 2, false)
	if e.Bullets[1] != "Led the team" {
		t.Errorf("swapped despite already having a metric bullet: %v", e.Bullets)
	}
}

func TestAllocateLinesGivesEveryEntryOneBullet(t *testing.T) {
	// A budget too small to go round starves nobody entirely — it stops early
	// and goes over, rather than rendering an entry as a bare heading.
	a := entry("**A**", str(300), str(300))
	b := entry("**B**", str(300), str(300))
	n := AllocateLines([]*Entry{a, b}, 1, nil, nil, 1, 0)
	if n[a] != 1 || n[b] != 1 {
		t.Errorf("got %d and %d, want one each even over budget", n[a], n[b])
	}
}

func TestAllocateLinesRanksByValueDensity(t *testing.T) {
	// The question a fixed page asks is not "which bullet is best" but "which is
	// the best use of the lines it costs". A marginally weaker one-line bullet
	// beats a marginally stronger three-line one.
	cheap := &Entry{Head: []string{"**Cheap**"}, Scored: []ScoredBullet{
		{Text: "top", Score: 1.0}, {Text: "short and good", Score: 0.80},
	}}
	dear := &Entry{Head: []string{"**Dear**"}, Scored: []ScoredBullet{
		{Text: "top", Score: 1.0}, {Text: str(300), Score: 0.85},
	}}
	// Room for both tops plus exactly one more one-line bullet.
	budget := BulletCost("top")*2 + BulletCost("short and good")
	n := AllocateLines([]*Entry{cheap, dear}, budget, nil, nil, 1, 0)
	if n[cheap] != 2 || n[dear] != 1 {
		t.Errorf("cheap=%d dear=%d; the higher-scoring but 3-line bullet should lose", n[cheap], n[dear])
	}
}

func TestAllocateLinesSectionCapBoundsProjects(t *testing.T) {
	// One pool lets a strongly-matching posting spend the page on projects and
	// leave both employers at a single line.
	exp := entry("**Employer**", "e1", "e2", "e3")
	p1 := entry("**Proj**", "p1", "p2", "p3")
	expSet := map[*Entry]bool{exp: true}
	caps := map[*Entry]int{exp: 4, p1: 4}

	wide := AllocateLines([]*Entry{exp, p1}, 22, caps, expSet, 1, 0)
	capped := AllocateLines([]*Entry{exp, p1}, 22, caps, expSet, 1, BulletCost("p1"))
	if capped[p1] >= wide[p1] {
		t.Errorf("the section cap did not bind: %d vs %d", capped[p1], wide[p1])
	}
	if capped[p1] < 1 {
		t.Error("the cap starved a project to a bare title — the first bullet is never blocked by it")
	}
}

func TestFloorAppliesToOneEntryOnly(t *testing.T) {
	// A blanket floor spends the page on the least differentiating evidence.
	// Restricted to the top-scoring entry, it protects the employer a reader
	// checks first without that cost.
	top := &Entry{Head: []string{"**Top**"}, Scored: []ScoredBullet{
		{Text: "a", Score: 0.9}, {Text: "b", Score: 0.85},
	}}
	weak := &Entry{Head: []string{"**Weak**"}, Scored: []ScoredBullet{
		{Text: "c", Score: 0.2}, {Text: "d", Score: 0.1},
	}}
	expSet := map[*Entry]bool{top: true, weak: true}
	caps := map[*Entry]int{top: 4, weak: 4}
	// A budget that fits the floor and nothing else.
	budget := BulletCost("a")*2 + BulletCost("c")
	n := AllocateLines([]*Entry{top, weak}, budget, caps, expSet, 2, 0)
	if n[top] != 2 {
		t.Errorf("the top entry got %d, want the floor of 2", n[top])
	}
	if n[weak] != 1 {
		t.Errorf("the weak entry got %d, want 1 — the floor is not blanket", n[weak])
	}
}

func TestAllocateLinesSkipsRatherThanStops(t *testing.T) {
	// A bullet that does not fit must not end the walk: a cheaper one later in
	// the list may still fit.
	e := &Entry{Head: []string{"**E**"}, Scored: []ScoredBullet{
		{Text: "top", Score: 1.0},
		{Text: str(400), Score: 0.9}, // 4 lines — will not fit
		{Text: "cheap", Score: 0.5},  // 1 line — should still be taken
	}}
	budget := BulletCost("top") + BulletCost("cheap")
	n := AllocateLines([]*Entry{e}, budget, map[*Entry]int{e: 4}, nil, 1, 0)
	if n[e] != 2 {
		t.Errorf("kept %d, want 2 — the oversized bullet should be skipped, not stop the walk", n[e])
	}
}

func TestSpikeAlphaKeepsCosineOnScale(t *testing.T) {
	// Applied raw, w=6 would inflate the cosine part sevenfold and quietly
	// delete the judge rerank, which is a flat +0.10 per grade benchmarked
	// against unscaled cosine.
	if got := SpikeAlpha(6); got < 0.857 || got > 0.858 {
		t.Errorf("SpikeAlpha(6) = %v, want 6/7", got)
	}
	if SpikeAlpha(0) != 0 {
		t.Error("weight 0 should disable the spike entirely")
	}
}

func TestSelectReturnsTheCVWhenThereIsNothingToRank(t *testing.T) {
	cv := "**Summary**\nJust prose, no Experience and no Projects.\n"
	got := Select(cv, func(string) float64 { return 1 }, DefaultOptions())
	if got.CV != cv {
		t.Error("a CV with nothing to rank should come back untouched")
	}
}

func TestSelectPinsSurviveTheCut(t *testing.T) {
	cv := `**Projects**

**Alpha**
- a1

**Beta**
- b1

**Gamma**
- g1
`
	opt := DefaultOptions()
	opt.MaxProjects = 1
	opt.LineBudget = 0
	// Gamma scores worst, so only a pin can save it.
	score := func(b string) float64 {
		switch b {
		case "a1":
			return 0.9
		case "b1":
			return 0.5
		}
		return 0.1
	}
	if got := Select(cv, score, opt); len(got.ProjKept) != 1 || got.ProjKept["Alpha"] == 0 {
		t.Errorf("unpinned: kept %v, want Alpha", got.ProjKept)
	}
	opt.PinnedProjects = []string{"gamma"}
	if got := Select(cv, score, opt); got.ProjKept["Gamma"] == 0 {
		t.Errorf("pinned: kept %v, want Gamma to survive the cut", got.ProjKept)
	}
}
