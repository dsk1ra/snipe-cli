package phase3

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

func picks(names ...string) []fill.ProjectPick {
	out := make([]fill.ProjectPick, len(names))
	for i, n := range names {
		out[i] = fill.ProjectPick{Name: n}
	}
	return out
}

func names(ps []fill.ProjectPick) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Name)
	}
	return strings.Join(s, ",")
}

func TestTrimProjectsKeepsChronologicalOrder(t *testing.T) {
	list := picks("Rust QUIC relay", "Java Spring store", "React dashboard", "Go pipeline")
	jd := map[string]bool{"java": true, "spring": true, "react": true}
	// Java and React score; the two that survive must stay in their original
	// order, not come back ranked.
	if got := names(TrimProjects(list, 2, jd)); got != "Java Spring store,React dashboard" {
		t.Fatalf("got %q", got)
	}
}

func TestTrimProjectsIsANoOpWhenItFits(t *testing.T) {
	list := picks("a", "b")
	if got := TrimProjects(list, 4, map[string]bool{}); len(got) != 2 {
		t.Fatalf("trimmed a list that already fit: %d", len(got))
	}
}

// The ladder trims from the FULL list every step. The bug this replaced trimmed
// cumulatively, so once a step had cut to three no later step — including the
// two-page fallback at step 0 — could see the fourth again.
func TestEveryStepTrimsFromTheFullList(t *testing.T) {
	c := fill.Content{Projects: []byte(`[{"name":"a"},{"name":"b"},{"name":"c"},{"name":"d"}]`)}
	p := Params{Template: "{{PROJECTS}}", Content: c,
		CV: fill.CV{Projects: []fill.Project{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}}}

	tightest := Fill(p, Ladder[len(Ladder)-1])
	if strings.Count(tightest, `\resumeProjectHeading`) != 2 {
		t.Fatalf("tightest step should render 2 projects:\n%s", tightest)
	}
	// Step 0 after the tightest still sees all four.
	if n := strings.Count(Fill(p, Ladder[0]), `\resumeProjectHeading`); n != 4 {
		t.Fatalf("step 0 rendered %d projects after the tightest step ran, want 4", n)
	}
}

// End to end against the real cv.md and template, when both a checkout and
// pdflatex are present. It is the only check that the .tex this package emits
// actually compiles.
func TestLadderProducesAOnePagePDF(t *testing.T) {
	repo := os.Getenv("SNIPE_NODE_REPO")
	if repo == "" {
		// go test runs in the package directory, so the checkout is two up. It
		// used to be $HOME/projects/snipe, which only resolved on one machine.
		repo, _ = filepath.Abs(filepath.Join("..", ".."))
	}
	if _, err := os.Stat(filepath.Join(repo, "cv.md")); err != nil {
		t.Skip("no CV checkout to render")
	}
	if _, err := exec.LookPath("pdflatex"); err != nil {
		t.Skip("pdflatex not installed")
	}
	l := paths.Default(repo)
	cfg, err := config.Load(l)
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := os.ReadFile(l.Template("cv-template.tex"))
	if err != nil {
		t.Fatal(err)
	}
	// Any cached offer renders. Naming one pinned this to a single past
	// application, so the only check that the emitted .tex compiles skipped
	// itself on every machine but the one that produced that folder.
	cached, _ := filepath.Glob(filepath.Join(repo, "output", "*", "cv-content.json"))
	if len(cached) == 0 {
		t.Skip("no cached content to render")
	}
	raw, err := os.ReadFile(cached[0])
	if err != nil {
		t.Skip("no cached content to render")
	}
	content, err := fill.ParseContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	fit, err := Run(context.Background(), Params{
		Template: string(tmpl), CV: fill.ParseCV(cfg.CVRaw), Content: content,
		Candidate: cfg.Candidate, CVText: cfg.CVRaw, JDText: "go backend distributed systems",
		OutPath: filepath.Join(t.TempDir(), "cv.pdf"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fit.Pages != 1 {
		t.Errorf("settled at %d pages (step %d)", fit.Pages, fit.Step)
	}
	if fit.PDF.Fill <= 0 || fit.PDF.Fill > 2 {
		t.Errorf("page fill %.3f is not a sane fraction", fit.PDF.Fill)
	}
	t.Logf("step %d, %d page(s), fill %.3f, %d KB", fit.Step, fit.Pages, fit.PDF.Fill, fit.PDF.Size/1024)
}
