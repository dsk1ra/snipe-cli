# Architecture

## System Overview

```
      ┌────────────────────────┐   ┌─────────────────────────────────┐
      │       snipe TUI        │   │       AI Coding CLI Agent       │
      │       cmd/snipe        │   │      (reads modes/*.md)         │
      └───────────┬────────────┘   └──────────────┬──────────────────┘
                  └───────────────┬───────────────┘
                                  │
            ┌──────────────────┼──────────────────────┐
            │                  │                       │
     ┌──────▼──────┐   ┌──────▼──────┐   ┌───────────▼────────┐
     │ Single Eval  │   │ Portal Scan │   │   Batch Process    │
     │ (snipe add)  │   │ (snipe scan)│   │    (snipe run)     │
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
     │  │ (A-D eval)│  │  pdflatex)   │  │ (merge-tracker)  │  │
     │  └──────────┘  └────────────┘  └───────────────────┘  │
     └────────────────────────────────────────────────────────┘
                               │
                    ┌──────────▼──────────┐
                    │  data/applications.md │
                    │  (canonical tracker)  │
                    └──────────────────────┘
```

## What is here

One Go binary. `internal/` is a package per concern, `cmd/snipe` is the binary,
`cmd/gate` the oracle harness.

| concern | package |
|---|---|
| orchestration | `internal/run` |
| phases | `internal/phase1`, `internal/phase2`, `internal/phase3` |
| CV selection | `internal/cvselect` |
| render | `internal/latex`, `internal/fill`, `internal/pdf` |
| providers | `internal/providers`, `internal/scan`, `internal/registry` |
| tracker | `internal/tracker` |
| TUI | `internal/tui` |

Every path lives in `internal/paths` and nothing else joins one. The data root
splits by what you lose if it is gone: `batch/state/` is the record,
`batch/cache/` is derived and safe to delete, and the per-offer directories are
their own thing.

Two pieces of the Node implementation this replaced are still in the tree, and
neither is on the path of a run:

- `batch/*.mjs`, the oracle `cmd/gate` diffs against. It is what proved the port
  and what would catch a regression in the ported logic.
- `batch/bench-tools/`, the sweeps that measured the tuned constants.

Everything else is at the `oracle/node-complete` tag.

`cmd/gate` agreeing with the oracle is not the same as being right: both can
share a defect and agree perfectly. `gate goldset` asks a human instead.

## Data contract

**User layer — never auto-update, all personalization goes here:**
`cv.md`, `config/profile.yml`, `config/profile.md`, `article-digest.md`,
`portals.yml`, `data/*`, `reports/*`, `output/*`, `interview-prep/*`

**System layer — safe to edit as code:**
`internal/*`, `cmd/*`, `prompts/*`, `templates/*`, `modes/*`, `scripts/*`, and
the Node oracle under `batch/` that the gates compare against.

**Rule:** customization (archetypes, narrative, comp targets, location policy,
scoring weights) goes in `config/profile.md` or `config/profile.yml`. Never
`modes/_shared.md`.

`config/profile.md` is a hard dependency: `internal/phase1` and
`internal/phase2` read it at runtime.

`cv.pinned_projects` in `config/profile.yml` forces a project past the
`maxProjects` cut whatever the posting scores it, matched case-insensitively as a
substring of the `### ` title. It is the escape hatch for an entry whose value is
not its subject — an Honours dissertation loses the cut on any posting with no
use for its topic, and is the first thing a reader of a graduate CV looks for.
Each pin spends one of the three slots, so it is a project the ranker no longer
chooses. **It also moves the benchmark**: the sweeps under `batch/bench-tools/`
read this file, so a pin changes selection for every arm. Note it when comparing
against a run made before the pin existed.

## Entry points

```
bin/snipe                    the TUI (default)
  ├─ snipe run               the three-phase pipeline
  ├─ snipe scan              portals.yml + data/registry.tsv → internal/providers → the queue
  ├─ snipe registry          pay gap data → careers-page crawl → data/registry.tsv
  ├─ snipe add               one pasted JD, queued or run
  └─ snipe tracker fix       canonical statuses, one row per opening
```

`snipe add -jd "<text>"` adds a pasted JD and runs it. A run already holding
`batch/state/runner.pid` queues instead; `snipe run` drains the queue.

Deeper TUI behaviour — failed-row retry/debug semantics, the footer message and
hint rules, P1-gated rows and `proceed?` — is in [`TUI.md`](TUI.md).

Every `SNIPE_*` env seam is load-bearing for the Go test suite. Keep them. Tests
needing Ollama, pdflatex or a real `cv.md` skip themselves, because CI has none
of the three and `cv.md` is user data that is never committed.

## Evaluation Flow (Single Offer)

1. **Input**: User pastes JD text or URL
2. **Extract**: the JD text is supplied by the caller. `snipe add -link` keeps the URL for tracking and never fetches it
3. **Classify**: Detect archetype, from the list you define in `config/profile.yml`
4. **Evaluate**: 4 blocks (A-D), only what decides the tailored CV:
   - A: Role summary
   - B: CV match (evidence table, gaps, strengths)
   - C: Level strategy
   - D: CV personalisation plan
5. **Score**: Weighted blend of three dimensions (1-5).
   `cv_match × 0.50 + north_star × 0.30 + comp × 0.20`, or
   `cv_match × 0.625 + north_star × 0.375` when the posting states no salary.
   `north_star` is not judged: it is the archetype's `fit` tier in
   `profile.yml` (primary 5, secondary 3, adjacent 2, outside 1), then capped
   for seniority in code. `comp` is computed from the posted salary.
   Below 3.0 the verdict is Skip; an Apply under 3.5 becomes Consider
6. **Report**: Save as `reports/{num}-{company}-{date}.md`
7. **PDF**: Generate ATS-optimised CV through `internal/pdf`, compiling `templates/cv-template.tex`
8. **Track**: Write TSV to `batch/tracker-additions/`, auto-merged

## Batch Processing

The batch system processes multiple offers in a fully local 3-phase pipeline (Ollama only — no cloud LLM calls):

```
batch-input.tsv    →  internal/run  →  Phase 1 score → Phase 2 eval → Phase 3 CV/PDF
(id, url, source)     (orchestrator)    (all local Ollama workers)
                           │
              batch/state/local-state.tsv
                 (per-phase progress)
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
data/registry.tsv        →  UK employers (250+ staff) and the ATS behind each careers page
templates/states.yml     →  Canonical status values
templates/cv-template.tex  → PDF generation template (Jake's Resume, pdflatex)
```

## File Naming Conventions

- Reports: `{###}-{company-slug}-{YYYY-MM-DD}.md` (3-digit zero-padded)
- PDFs: `output/{YYYY-MM-DD}_{company-slug}_{###}/{CandidateName}-CV.pdf`
- Tracker TSVs: `batch/tracker-additions/{id}.tsv`

## Pipeline Integrity

Scripts maintain data consistency:

| Command | Purpose |
|---------|---------|
| `snipe doctor` | Setup, state and tracker integrity in one pass |
| `snipe tracker fix` | Canonical statuses, one row per opening, working report links |

Both live in `internal/tracker`. What used to be four scripts and a setup
checker is two subcommands, because the checks always ran together and a
half-fixed tracker was never a state anyone wanted.

