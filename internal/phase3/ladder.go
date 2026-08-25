// Package phase3 turns an evaluated offer into a PDF: it fills the LaTeX
// template from the model's tailored content and walks a density ladder until
// the result fits the page.
package phase3

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/pdf"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// Step is one rung: 0 means "no cap" for that dimension.
type Step struct {
	Skills      int
	Bullets     int
	Projects    int
	ProjBullets int
}

// Ladder is the relevance-preserving density ladder. It reduces experience
// bullet depth (fill caps per role, hitting the least-relevant backfilled roles
// too), skill breadth, and the weakest projects.
//
// The target is ONE page. It used to be two, which meant the ladder was
// perfectly satisfied by the output that prompted the whole exercise — it
// stopped at step 0 on every offer and never fired at all.
//
// Its role has changed with it. cv-select now spends a rendered-line budget, so
// selection already fits the page on 31 of 32 offers and this is a safety net
// rather than the mechanism: it exists for the residual selection cannot
// predict, since a bullet's rendered height depends on where LaTeX breaks its
// lines. Steps are therefore gentle at the top — one project bullet at a time —
// where they used to jump straight from 4 to 1 and throw away a project's worth
// of evidence to save two lines.
//
// Step 0 must not clip what the selectors allocated: its caps are the same 4/4
// bounds cv-select works to, and its skills cap is 0 because the writer stage
// has already cut the block to what the posting can use. It renders the
// selection untouched; only a step below it may take evidence away.
//
// Skills survive the first three steps intact. Capping them is a blunt slice off
// the end of a list ordered by cv.md, so it cannot tell a category the posting
// named from one it did not — it took MCP off an EPAM CV that asks for MCP four
// times, the same defect the category rule exists to prevent. A project bullet
// is the cheaper thing to spend first.
var Ladder = []Step{
	{Skills: 0, Bullets: 4, Projects: 4, ProjBullets: 4}, // as selected
	{Skills: 0, Bullets: 4, Projects: 4, ProjBullets: 3},
	{Skills: 0, Bullets: 3, Projects: 4, ProjBullets: 2},
	{Skills: 5, Bullets: 3, Projects: 3, ProjBullets: 2},
	{Skills: 5, Bullets: 2, Projects: 3, ProjBullets: 1},
	{Skills: 4, Bullets: 2, Projects: 2, ProjBullets: 1}, // tightest
}

// TrimProjects keeps the n projects most relevant to the JD, in their existing
// (chronological) order.
//
// Order matters twice over: the caller has already sorted by date, so a tail
// slice would drop the OLDEST project rather than the weakest — on one
// Java/Spring posting that silently cut the Java/Spring project, the one thing
// the CV most needed to show.
//
// One divergence from the Node, deliberate: a legacy project given as a bare
// string scores 0 there, because the scorer reads `p.name` off a string and gets
// undefined. Here it scores on its name like any other. The Node's behaviour is
// "keep the first n"; this keeps the n the posting actually asked about.
func TrimProjects(list []fill.ProjectPick, n int, jdTokens map[string]bool) []fill.ProjectPick {
	if n <= 0 || len(list) <= n {
		return list
	}
	score := func(p fill.ProjectPick) int {
		hits := 0
		for t := range text.TokenSet(p.Name + " " + p.Description) {
			if jdTokens[t] {
				hits++
			}
		}
		return hits
	}
	idx := make([]int, len(list))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return score(list[idx[a]]) > score(list[idx[b]]) })

	keep := map[int]bool{}
	for _, i := range idx[:n] {
		keep[i] = true
	}
	out := make([]fill.ProjectPick, 0, n)
	for i, p := range list {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// Fit is what the ladder settled on.
type Fit struct {
	Step  int
	Pages int
	Tex   string
	PDF   pdf.Result
}

// Params are everything the ladder needs that does not change between steps.
type Params struct {
	Template   string
	CV         fill.CV
	Content    fill.Content
	Candidate  config.Candidate
	CVText     string
	JDText     string
	TargetRole string
	OutPath    string
}

// Fill renders one rung. Exported because the gate drives it directly, without
// a LaTeX installation.
func Fill(p Params, s Step) string {
	c := p.Content
	// Trimmed from the FULL project list every step, never cumulatively from the
	// last step's output: the original loop reassigned the content in place, so
	// once a step had cut to three no later step could see the fourth again — and
	// the two-page fallback could not restore what the one-page attempts had shed.
	if picks, ok := c.ProjectPicks(); ok {
		c = c.WithProjects(TrimProjects(picks, s.Projects, text.TokenSet(p.JDText)))
	}
	return fill.Render(p.Template, p.CV, c, p.Candidate, fill.Opts{
		MaxSkills: s.Skills, MaxBullets: s.Bullets, MaxProjBullets: s.ProjBullets,
		TargetRole: p.TargetRole, CVText: p.CVText,
	})
}

// Run walks the ladder until the PDF fits, and returns the rung that did.
//
// One page is the target; two is the ceiling, not the goal. Every density step
// is tried at one page first, and only if none of them fits does the ladder
// accept two — and then at the FULLEST step, because a CV that has to run to two
// pages should carry the evidence it was going to carry rather than the
// stripped-down version that failed to save it.
func Run(ctx context.Context, p Params) (Fit, error) {
	var lastErr error
	var checked bool
	for _, maxPages := range []int{1, 2} {
		steps := len(Ladder)
		if maxPages == 2 {
			steps = 1
		}
		for step := 0; step < steps; step++ {
			if err := ctx.Err(); err != nil {
				return Fit{}, err
			}
			tex := Fill(p, Ladder[step])
			// Section order is a property of the template and cv.md, neither of
			// which the ladder touches, so one check answers for every rung —
			// and failing here is right: a CV whose sections contradict the
			// source document should not be compiled, let alone sent.
			if !checked {
				if err := fill.CheckSectionOrder(tex, p.CVText); err != nil {
					return Fit{}, err
				}
				checked = true
			}
			res, err := pdf.Render(ctx, tex, p.OutPath)
			if err != nil {
				// A missing TeX install is the same answer six times over.
				if errors.Is(err, pdf.ErrNoLaTeX) {
					return Fit{}, err
				}
				lastErr = err
				continue // a later, tighter step may still render
			}
			if res.Pages <= maxPages {
				return Fit{Step: step, Pages: res.Pages, Tex: tex, PDF: res}, nil
			}
		}
	}
	if lastErr != nil {
		return Fit{}, lastErr
	}
	return Fit{}, fmt.Errorf("PDF still over 2 pages after %d density steps", len(Ladder))
}
