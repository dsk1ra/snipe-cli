# Batch Processing (Local)

Process multiple job offers in a 3-phase local pipeline — all LLM work runs on Ollama, no cloud calls. Phase 1 pre-scores, Phase 2 writes the full A-G report, Phase 3 tailors the CV and generates the PDF for offers above threshold.

## Quick Start

1. **Add offers** to `batch-input.tsv` (tab-separated: `id`, `url`, `source`, `notes`) — or bridge them from `data/pipeline.md`:

   ```bash
   node batch/import-pipeline.mjs
   ```

2. **Dry run** to preview what will be processed:

   ```bash
   bash batch/local-runner.sh --dry-run
   ```

3. **Run the batch**:

   ```bash
   bash batch/local-runner.sh
   ```

4. **Results** are automatically merged into `data/applications.md` and verified with `tracker/verify-pipeline.mjs` at the end of the run.

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--parallel-score N` | `1` | Concurrent Phase 1 scorers |
| `--parallel-eval N` | `1` | Concurrent Phase 2 evaluators (VRAM-bound — keep at 1 on 6 GB) |
| `--parallel-pdf N` | `2` | Concurrent Phase 3 tailoring workers |
| `--threshold N` | profile.yml | Phase 2→3 PDF gate (`auto_pdf_score_threshold`, else 3.0) |
| `--p1-threshold N` | off | Phase 1→2 gate: skip Phase 2 below this pre-score |
| `--dry-run` | off | Preview pending offers without processing |
| `--skip-phase3` | off | Score + evaluate only, no PDFs |
| `--skip-phase2 --skip-phase3` | off | Score only |
| `--retry-failed` | off | Only retry offers marked failed |
| `--only-id N` | off | Process a single offer |
| `--start-from N` | `0` | Skip offers with ID below N |
| `--max-retries N` | `2` | Max retry attempts per offer before giving up |
| `--classic-eval` | off | Monolithic Phase 2 evaluator instead of the staged default |
| `--ollama-model` / `--phase2-model` / `--phase3-model` | see CLAUDE.md | Per-phase Ollama model overrides |

## Directory Layout

```
batch/
  local-runner.sh          # Orchestrator script (all 3 phases)
  Modelfile.snipe-*        # Ollama model definitions (screen/eval/cv/embed)
  ollama-score-prompt.md   # Phase 1 system prompt
  ollama-eval-prompt.md    # Phase 2 system prompt (classic evaluator)
  local-tailor-prompt.md   # Phase 3 system prompt
  batch-input.tsv          # Input offers (you create this / import-pipeline.mjs)
  local-state.tsv          # Processing state (auto-managed, resumable)
  cv-index.json            # Embedding indexes (snipe-embed), rebuilt from cv.md +
  jd-index.json            #   past JDs; back Phase 2 evidence and Phase 3 selection
  jds/                     # Persistent JD text cache — phases never re-fetch
  scores/                  # Phase 1 output ({id}.json)
  evals/                   # Phase 2 output ({id}.json)
  logs/                    # Per-offer worker logs
  tracker-additions/       # TSV lines produced by workers
    merged/                # TSVs already merged into applications.md
```

Each `*-prompt.md` ships as a generic default. To override one with your own
wording, drop a `*.local.md` sibling (e.g. `local-tailor-prompt.local.md`) — it's
gitignored and loaded in preference to the shipped file.

## How It Works

1. **local-runner.sh** reads `batch-input.tsv` and `local-state.tsv` to determine which offers need processing.
2. **Phase 1** (`ollama-scorer.mjs`): reads the JD from `batch/jds/<id>.txt` (or fetches it once if missing), pre-scores it, writes `scores/<id>.json`.
3. **Phase 2** (`staged-evaluator.mjs`): full A-G evaluation from the stored JD, writes `reports/<NNN>-<slug>-<date>.md` + `evals/<id>.json`.
4. **Phase 3** (`local-pdf-offer.mjs`): for offers ≥ threshold, tailors the CV on the `snipe-cv` coder model, generates the PDF via `generate-pdf.mjs`, writes the tracker TSV. Below-threshold offers get a tracker TSV directly (no LLM).
5. After all phases, the runner calls `tracker/merge-tracker.mjs` and `tracker/verify-pipeline.mjs`.

## Tracker Merge

Workers write one TSV per offer to `batch/tracker-additions/`. The merge script (`npm run merge`) handles:

- Deduplication by company + role fuzzy match and report number
- Column order conversion (TSV has status before score; applications.md has score before status)
- In-place updates when a re-evaluation scores higher than the existing entry
- Moving processed TSVs to `tracker-additions/merged/`

Run `npm run merge` manually if you need to merge outside of a batch run.

## Resumability

`local-state.tsv` tracks per-phase status for every offer (10 columns). If the batch is interrupted, re-running `local-runner.sh` picks up where it left off — completed phases are skipped per offer. A PID-based lock file (`local-runner.pid`) prevents concurrent batch runs; stale locks from crashed runs are detected and removed automatically.

## Prerequisites

- Ollama running with the custom models built (see "One-time setup" in CLAUDE.md — `snipe-screen`, `snipe-eval`, `snipe-cv`, `snipe-embed`)
- Node.js >= 18 (>= 22.5 for the optional SQLite tracker index), Playwright chromium installed
- `batch-input.tsv` with at least one offer
