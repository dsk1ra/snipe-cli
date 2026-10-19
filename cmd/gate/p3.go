package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/cvselect"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/ollama"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/phase3"
	"github.com/dsk1ra/snipe-cli/internal/text"
)

// gateP3 writes the tailored content for one offer, the same shape
// local-pdf-offer.mjs --bench-dir does, so the two can be diffed.
//
// It stops where the bench does: cv-content.json is the model's — here the
// selector's — whole output, and everything past it is deterministic
// post-processing that the fill gate already covers.
func gateP3(home, cvPath, reportPath, jdPath, outPath string) int {
	l := paths.Layout{Home: home, Code: home}
	cfg, err := config.Load(l)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// The gate CV replaces cv.md wholesale: both implementations must rank the
	// same document, and the JS parser reads only ##-spelled headings.
	cv, err := os.ReadFile(cvPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg.CVRaw = string(cv)

	report, err := os.ReadFile(reportPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	jd, err := os.ReadFile(jdPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	reqs := cvselect.ExtractBlockBRequirements(string(report))
	if len(reqs) == 0 {
		reqs = []string{text.CleanJD(string(jd), 1500)}
	}

	t := &phase3.Tailor{L: l, Cfg: cfg, OL: ollama.New("", nil)}
	score, err := t.ScoreBullets(context.Background(), reqs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	c := t.Build(score, string(jd))

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	b, err := json.MarshalIndent(comparable(c), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.WriteFile(outPath, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

// comparable is the subset of the content that reaches the page.
//
// `competencies` is excluded deliberately: the JS still derives it and the
// LaTeX template has no slot for it, so it is dead on both sides and diffing it
// would fail the gate over output neither implementation prints. `summary` is
// excluded for the same reason — Jake's Resume has no summary section.
//
// `skills` left the comparison when the Go selector moved to whole-phrase naming
// and 5/7/3 limits; the oracle still names by token overlap at 6/12/3, so the
// two now differ on purpose. `gate skills` measures the block instead.
type comparableContent struct {
	Experience []fill.ContentExp  `json:"experience"`
	Projects   []fill.ProjectPick `json:"projects"`
	Modules    []string           `json:"education_modules"`
}

func comparable(c fill.Content) comparableContent {
	out := comparableContent{Experience: c.Experience, Modules: c.EducationModules}
	if out.Experience == nil {
		out.Experience = []fill.ContentExp{}
	}
	if out.Modules == nil {
		out.Modules = []string{}
	}
	_ = json.Unmarshal(c.Projects, &out.Projects)
	if out.Projects == nil {
		out.Projects = []fill.ProjectPick{}
	}
	return out
}

// NormalizeJS reduces a cv-content.json written by local-pdf-offer.mjs to the
// same shape, so the two files differ only where the implementations do.
func normalizeJS(path, outPath string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	var c fill.Content
	if err := json.Unmarshal(b, &c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	out, err := json.MarshalIndent(comparable(c), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}
