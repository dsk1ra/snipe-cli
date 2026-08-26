# Architecture

## System Overview

```
      ┌────────────────────────┐   ┌─────────────────────────────────┐
      │       snipe TUI        │   │       AI Coding CLI Agent       │
      │  Go   cmd/snipe        │   │  (reads CLAUDE.md + modes/*.md) │
      │  Node snipe-tui.mjs    │   └──────────────┬──────────────────┘
      └───────────┬────────────┘                  │
                  └───────────────┬───────────────┘
                                  │
            ┌──────────────────┼──────────────────────┐
            │                  │                       │
     ┌──────▼──────┐   ┌──────▼──────┐   ┌───────────▼────────┐
     │ Single Eval  │   │ Portal Scan │   │   Batch Process    │
     │ (auto-pipe)  │   │  (scan.md)  │   │   (local-runner)   │
     └──────┬──────┘   └──────┬──────┘   └───────────┬────────┘
            │                  │                       │
            │           ┌──────▼──────┐          ┌────▼─────┐
            │           │ pipeline.md │          │ N workers│
            │           │ (URL inbox) │          │ (headless)
            │           └─────────────┘          └────┬─────┘
            │                                          │
     ┌──────▼──────────────────────────────────────────▼──────┐
     │                    Output Pipeline                      │
     │  ┌──────────┐  ┌────────────┐  ┌───────────────────┐  │
     │  │ Report.md│  │ PDF (.tex → │  │ Tracker TSV       │  │
     │  │ (A-F eval)│  │  pdflatex)   │  │ (merge-tracker)  │  │
     │  └──────────┘  └────────────┘  └───────────────────┘  │
     └────────────────────────────────────────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │  data/applications.md │
                    │  (canonical tracker)  │
                    └──────────────────────┘
```

## Implementations

Two live in this repo. The Node one shipped through v0.4.0; the Go one landed
complete in v0.5.0 and neither has been removed.

| concern | Node | Go |
|---|---|---|
| entry point | `snipe-tui.mjs`, `./snipe` | `cmd/snipe`, built to `bin/snipe` |
| orchestration | `batch/local-runner.sh` | `internal/run` |
| phases | `batch/ollama-scorer.mjs`, `batch/staged-evaluator.mjs`, `batch/local-pdf-offer.mjs` | `internal/phase1`, `internal/phase2`, `internal/phase3` |
| CV selection | `batch/cv-select.mjs` | `internal/cvselect` |
| render | `generate-pdf.mjs` | `internal/latex`, `internal/fill`, `internal/pdf` |
| providers | `providers/*.mjs` | `internal/providers`, `internal/scan` |
| tracker | `tracker/*.mjs` | `internal/tracker` |

The Go side keeps every path in `internal/paths` and nothing else joins one. It
splits the data root by what you lose if it is gone: `batch/state/` is the
record, `batch/cache/` is derived and safe to delete, and the per-offer
directories are their own thing.

`cmd/gate` is the oracle harness. It diffs Go output against Node output over the
real cached corpus, one subcommand per seam, which is what proves the port rather
than the Go unit tests. `gate goldset` is the exception: it asks whether
retrieval agrees with a human, because both implementations can share a defect
and agree perfectly while doing the wrong thing.

## Evaluation Flow (Single Offer)

1. **Input**: User pastes JD text or URL
2. **Extract**: Playwright/WebFetch extracts JD from URL
3. **Classify**: Detect archetype, from the list you define in `config/profile.yml`
4. **Evaluate**: 6 blocks (A-F):
   - A: Role summary
   - B: CV match (gaps + mitigation)
   - C: Level strategy
   - D: Comp research (WebSearch)
   - E: CV personalization plan
   - F: Interview prep (STAR stories)
5. **Score**: Weighted blend of three integer dimensions (1-5).
   `cv_match × 0.50 + north_star × 0.30 + comp × 0.20`, or
   `cv_match × 0.625 + north_star × 0.375` when the posting states no salary.
   Below 3.0 the verdict is Skip; an Apply under 3.5 becomes Consider
6. **Report**: Save as `reports/{num}-{company}-{date}.md`
7. **PDF**: Generate ATS-optimised CV. Node renders through `generate-pdf.mjs`, Go through `internal/pdf`; both compile the same `templates/cv-template.tex`
8. **Track**: Write TSV to `batch/tracker-additions/`, auto-merged

## Batch Processing

The batch system processes multiple offers in a fully local 3-phase pipeline (Ollama only — no cloud LLM calls):

```
batch-input.tsv    →  local-runner.sh  →  Phase 1 score → Phase 2 eval → Phase 3 CV/PDF
(id, url, source)     (orchestrator)       (all local Ollama workers)
                           │
              local-state.tsv  (per-phase progress)
              batch/ under Node, batch/state/ under Go
```

Workers produce:
- Report .md
- PDF
- Tracker TSV line

The orchestrator manages parallelism, state, retries, and resume.

## Data Flow

```
cv.md                    →  Evaluation context
article-digest.md        →  Proof points for matching
config/profile.yml       →  Candidate identity
portals.yml              →  Scanner configuration
templates/states.yml     →  Canonical status values
templates/cv-template.tex  → PDF generation template (Jake's Resume, pdflatex)
```

## File Naming Conventions

- Reports: `{###}-{company-slug}-{YYYY-MM-DD}.md` (3-digit zero-padded)
- PDFs: `output/{YYYY-MM-DD}_{company-slug}_{###}/{CandidateName}-CV.pdf`
- Tracker TSVs: `batch/tracker-additions/{id}.tsv`

## Pipeline Integrity

Scripts maintain data consistency:

| Script | Purpose |
|--------|---------|
| `tracker/merge-tracker.mjs` | Merges batch TSV additions into applications.md |
| `tracker/verify-pipeline.mjs` | Health check: statuses, duplicates, links |
| `tracker/dedup-tracker.mjs` | Removes duplicate entries by company+role |
| `tracker/normalize-statuses.mjs` | Maps status aliases to canonical values |
| `cv-sync-check.mjs` | Validates setup consistency |

