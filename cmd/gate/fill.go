package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/fill"
	"github.com/dsk1ra/snipe-cli/internal/paths"
)

// gateFill renders one source.tex the way fill-cv-template.mjs would, so the
// caller can diff the two byte for byte.
//
//	gate fill <node-repo> <content.json> <out.tex> <maxSkills> <maxBullets> <maxProjBullets> [role]
//
// The knobs are positional and 0 means "no cap", matching the Node's null.
func gateFill(args []string) int {
	if len(args) < 6 {
		fmt.Fprintln(os.Stderr, "usage: gate fill <node-repo> <content.json> <out.tex> <maxSkills> <maxBullets> <maxProjBullets> [role]")
		return 2
	}
	repo, contentPath, outPath := args[0], args[1], args[2]
	num := func(i int) int { n, _ := strconv.Atoi(args[i]); return n }
	role := ""
	if len(args) > 6 {
		role = args[6]
	}

	l := paths.Default(repo)
	cfg, err := config.Load(l)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	tmpl, err := os.ReadFile(l.Template("cv-template.tex"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	raw, err := os.ReadFile(contentPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	content, err := fill.ParseContent(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// cv.md verbatim: the parser reads its structure, and CleanCV strips exactly
	// the markdown it needs.
	cv := fill.ParseCV(cfg.CVRaw)
	out := fill.Render(string(tmpl), cv, content, cfg.Candidate, fill.Opts{
		MaxSkills: num(3), MaxBullets: num(4), MaxProjBullets: num(5),
		TargetRole: role, CVText: cfg.CVRaw,
	})
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}
