// Package pdf renders the LaTeX CV and reports how much of the page it filled.
//
// One pdflatex pass, not two: the template sets `bookmarks=false`, so hyperref
// writes no .out file and has nothing to ask for a rerun about. Nothing else in
// the document cross-references, and the density ladder compiles this up to six
// times per offer.
package pdf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Result is what one compile says about the page.
type Result struct {
	Pages int
	// Fill is the fractional page count from the template's \AtEndDocument hook:
	// whole pages plus how full the last one is. 0 when the hook is absent — a
	// .tex that did not come from templates/cv-template.tex.
	Fill float64
	Size int
}

var (
	pageCountRe = regexp.MustCompile(`Output written on .*?\((\d+) pages?`)
	fillRe      = regexp.MustCompile(`SNIPE-FILL ([\d.]+)pt ([\d.]+)pt`)
	errLineRe   = regexp.MustCompile(`(?m)^!.*$`)
)

// ErrNoLaTeX is returned when pdflatex is not installed, so the caller can say
// so once rather than six times up the ladder.
var ErrNoLaTeX = errors.New("pdflatex not found on PATH — install TeX Live (texlive-latex-recommended, texlive-fonts-recommended)")

// Render compiles tex and writes the PDF to outPath.
//
// pdflatex insists on writing .aux/.log next to its output, so it renders into a
// scratch directory and the PDF is moved into place. -output-directory rather
// than a chdir, because the ladder runs several of these and the working
// directory is process-global.
func Render(ctx context.Context, tex, outPath string) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return Result{}, err
	}
	scratch, err := os.MkdirTemp("", "snipe-tex-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(scratch)

	texPath := filepath.Join(scratch, "cv.tex")
	if err := os.WriteFile(texPath, []byte(tex), 0o644); err != nil {
		return Result{}, err
	}

	cmd := exec.CommandContext(ctx, "pdflatex",
		"-interaction=nonstopmode", "-halt-on-error",
		"-output-directory="+scratch, texPath)
	// nonstopmode puts the real complaint on stdout, prefixed "! ".
	out, runErr := cmd.Output()
	log := string(out)
	if runErr != nil {
		if errors.Is(runErr, exec.ErrNotFound) {
			return Result{}, ErrNoLaTeX
		}
		return Result{}, fmt.Errorf("pdflatex failed: %s", complaint(log, 3))
	}

	// The page-count regex the HTML path used reads /Type /Page out of the raw
	// bytes; pdflatex compresses those into object streams, so it finds zero on
	// every document. pdflatex states the count itself, so read that rather than
	// adding a poppler dependency to get it back.
	m := pageCountRe.FindStringSubmatch(log)
	if m == nil {
		return Result{}, fmt.Errorf("pdflatex wrote no page count — %s", complaint(log, 2))
	}
	pages, _ := strconv.Atoi(m[1])

	res := Result{Pages: pages}
	if f := fillRe.FindStringSubmatch(log); f != nil {
		used, _ := strconv.ParseFloat(f[1], 64)
		total, _ := strconv.ParseFloat(f[2], 64)
		if total != 0 {
			res.Fill = float64(pages-1) + used/total
		}
	}

	blob, err := os.ReadFile(filepath.Join(scratch, "cv.pdf"))
	if err != nil {
		return Result{}, err
	}
	res.Size = len(blob)
	return res, os.WriteFile(outPath, blob, 0o644)
}

func complaint(log string, n int) string {
	lines := errLineRe.FindAllString(log, n)
	if len(lines) == 0 {
		return "no output"
	}
	return strings.Join(lines, "; ")
}
