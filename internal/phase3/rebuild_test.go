package phase3

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
	"github.com/dsk1ra/snipe-cli/internal/suggest"
)

const rebuildCV = `# Sam Lee

## Experience

### Backend Engineer
**Acme Ltd** — London, UK | Jan 2022 – Present
- Built REST services handling 40k req/s on Postgres

## Projects

### Tiny Tool
- Wrote a small tool

## Education

**Napier University**
BSc Computer Science | 2018 – 2022

## Skills

**Data:** PostgreSQL, Redis
`

// Rebuild prints the accepted suggestions and nothing else, leaves
// cv-content.json as Run wrote it, and a rejected one gets the original back.
func TestRebuildPrintsOnlyWhatWasAccepted(t *testing.T) {
	if _, err := exec.LookPath("pdflatex"); err != nil {
		t.Skip("pdflatex not installed")
	}
	repo, _ := filepath.Abs(filepath.Join("..", ".."))
	home := t.TempDir()
	l := paths.Layout{Home: home, Code: repo}

	dir := filepath.Join(l.Output(), "2026-01-01_acme_007")
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(l.Evals(), 0o755)
	os.WriteFile(filepath.Join(l.Evals(), "7.json"), []byte(`{"status":"evaled","company":"Acme","role":"Engineer"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "job-description.txt"), []byte("RESTful APIs on Postgres"), 0o644)
	content := fill.Content{Experience: []fill.ContentExp{{Company: "Acme Ltd", Bullets: []string{"Built REST services handling 40k req/s on Postgres"}}}}
	content.Skills = []byte(`[{"category":"Data","items":"PostgreSQL, Redis"}]`)
	body, _ := json.Marshal(content)
	os.WriteFile(filepath.Join(dir, "cv-content.json"), body, 0o644)

	st, err := state.Open(l.State())
	if err != nil {
		t.Fatal(err)
	}
	st.Commit(7, func(r *state.Row) { r.ReportNum = "007" })
	tailor := &Tailor{L: l, Cfg: &config.Config{CVRaw: rebuildCV}, Store: st, Cache: state.NewCache()}

	items := []suggest.Item{
		{ID: "B1", Kind: suggest.Bullet, Old: "Built REST services handling 40k req/s on Postgres",
			New: "Built RESTful API services handling 40k req/s on Postgres", State: suggest.Accepted},
		{ID: "K1", Kind: suggest.Skill, Old: "PostgreSQL", New: "Postgres", State: suggest.Accepted},
	}
	suggest.Save(dir, items)

	tex := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "source.tex"))
		return string(b)
	}
	n, _, err := tailor.Rebuild(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := tex(); n != 2 || !strings.Contains(got, "RESTful API services") || !strings.Contains(got, "Postgres, Redis") {
		t.Errorf("accepted changes missing (applied %d):\n%s", n, got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cv-content.json")); string(b) != string(body) {
		t.Error("Rebuild rewrote cv-content.json")
	}

	suggest.Mark(dir, "B1", suggest.Rejected)
	if _, _, err := tailor.Rebuild(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if got := tex(); strings.Contains(got, "RESTful API services") || !strings.Contains(got, "Built REST services") {
		t.Errorf("a rejected bullet did not go back:\n%s", got)
	}
}
