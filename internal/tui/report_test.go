package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/dsk1ra/snipe-cli/internal/config"
	"github.com/dsk1ra/snipe-cli/internal/paths"
	"github.com/dsk1ra/snipe-cli/internal/state"
)

// One report carrying every construct the pipeline emits, plus the two shapes
// that broke the wrap: a tracking URL with no space in a thousand characters,
// and a hyphenated word landing on the boundary.
const sampleReport = "# Evaluation: Counter Terrorism Policing (CTP) — Software Developer - Technology -\n" + `
**Date:** 2026-08-22
**Score:** 4.8/5
**URL:** https://www.linkedin.com/jobs/search-results/?currentJobId=4457808322&eBP=CwEAAAGgKV1HLNRHRwdqGmIq5aWol_NxYJTR4rDnC5WS-21BxvVXE_oc0eOqcBxyeDFqtNBNH2LQSy_8ElV_LARi4NkifGii-7Q19_PswOolfuv4XIqGguzFd3J9GgoGHteRgIsL6XAftgx-pJSzVeEiuiGPZWdccyTTZfaw_4YUVnuD0f_E_5t5HOrFrI-tl5eJ1PCEtYXqWeL8Y_wsgz0xYU-oL-_f1x5LnzW8t48Oeq978ofefOjrU-TkCqtufFDygrJXruh5

---

## A) Role Summary

| Field | Value |
|-------|-------|
| Archetype | Backend / Distributed Systems Engineer |
| TL;DR | This role is a perfect fit: it matches the archetype, covers all MUST requirements with Strong or Transferable evidence, and aligns with the stated goals and location policy. |

## B) Evidence

| # | JD Requirement | Story | S | T | A | R |
|---|---|---|---|---|---|---|
| 1 | Experience developing software to meet user needs | I shipped a service like I did in Snipe when I retired a bad-tempered cache layer | The platform was growing rapidly | Automate it | Led it from 80 to 200 members | 99.97% uptime |

- a bullet
- another
`

// Nothing the reader lays out may be wider than the pane it is laid out for:
// the viewport cuts an overlong line rather than folding it, so one column over
// is a character silently gone.
//
// This is also the regression guard on ansi.Wrap, which lands a column or two
// past the limit when it breaks on a hyphen — see wrapText.
func TestReportFitsItsWidth(t *testing.T) {
	for _, w := range []int{20, 40, 60, 80, 102, 160} {
		for i, line := range strings.Split(renderReport(sampleReport, w), "\n") {
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("w=%d line %d is %d columns: %q", w, i, got, line)
			}
		}
	}
}

// A table whose content fits keeps its columns; one whose content does not is
// turned on its side, one record per block.
//
// The test is the content, not the column count. Seven columns share a hundred
// between them at fourteen characters each, and a paragraph of evidence down a
// fourteen-column ribbon runs forty lines with six empty ones beside it — but so
// does a three-column table whose middle cell is that same paragraph, which is
// what the CV-match table in every report looks like.
func TestWideTablesAreTransposed(t *testing.T) {
	out := renderReport(sampleReport, 102)

	// The narrow table keeps its rule; the wide one has none to keep.
	if !strings.Contains(out, "Archetype") || !strings.Contains(stripANSI(out), "│") {
		t.Errorf("the two-column table lost its columns:\n%s", out)
	}
	star := stripANSI(out[strings.Index(out, "B) Evidence"):])
	if strings.Contains(star, "│") {
		t.Errorf("the seven-column table was drawn as columns:\n%s", star)
	}
	for _, label := range []string{"JD Requirement", "Story", "S ", "T ", "A ", "R "} {
		if !strings.Contains(star, label) {
			t.Errorf("transposed block is missing the %q label:\n%s", label, star)
		}
	}
}

// Three columns are not by themselves too many: what decides is whether the
// widths the renderer will shrink toward still fit.
//
// The pair below differ only in the length of one cell. Judged by column count
// they are the same table, and the long one came out with its verdict column
// squeezed under its own content — "Transferable" broken across two lines on the
// table that appears in every report.
func TestAThreeColumnTableTurnsOnlyWhenItsContentDoesNotFit(t *testing.T) {
	const short = `| Req | Evidence | Strength |
|---|---|---|
| Go | Wrote a lot of it | Strong |`
	const long = `| Req | Evidence | Strength |
|---|---|---|
| Experience operating and troubleshooting services in production | Software Engineer: ran it on AWS from Terraform and GitHub Actions, instrumented with OpenTelemetry into Grafana, and set the SLOs the team ran on | Transferable |`

	if got := stripANSI(renderReport(short, 102)); !strings.Contains(got, "│") {
		t.Errorf("a table that fits lost its columns:\n%s", got)
	}
	got := stripANSI(renderReport(long, 102))
	if strings.Contains(got, "│") {
		t.Errorf("a table that does not fit kept its columns:\n%s", got)
	}
	if !strings.Contains(got, "Transferable") {
		t.Errorf("the verdict was broken across lines:\n%s", got)
	}
}

// The "**Date:** … **Score:** …" block stays one field per line.
//
// This is the reason the reader is not glamour. Those lines are separated by
// single newlines, so CommonMark folds them into one paragraph and a real
// markdown renderer runs them together — correct by the spec, and wrong for a
// document whose header is a list of fields.
func TestTheHeaderBlockStaysOnePerLine(t *testing.T) {
	out := renderReport(sampleReport, 102)
	if !strings.Contains(out, "**Date:** 2026-08-22\n**Score:** 4.8/5") {
		t.Errorf("the header block was run together:\n%s", out[:min(400, len(out))])
	}
}

// A resize re-lays the open report. The text was wrapped to the old width, so
// every line of it is wrong at the new one.
func TestTheReaderRewrapsOnResize(t *testing.T) {
	q := newQueueTab(paths.Layout{Home: t.TempDir(), Code: t.TempDir()}, state.NewCache(), config.DefaultGoals)
	q.setSize(100, 30)
	q.Update(detailMsg{title: "a report", body: sampleReport})

	wide := q.detail.GetContent()
	q.setSize(50, 30)
	narrow := q.detail.GetContent()

	if narrow == wide {
		t.Fatal("the reader kept the old layout across a resize")
	}
	for _, line := range strings.Split(narrow, "\n") {
		if got := ansi.StringWidth(line); got > 50 {
			t.Errorf("still laid out for the old width: %d columns in %q", got, line)
		}
	}
}
