# snipe-cli — local AI job search tool

Driven from the **snipe TUI** (`snipe-tui`).
Everything runs locally against Ollama — no cloud LLM calls in the pipeline.

## Data contract

**User layer — never auto-update, all personalization goes here:**
`cv.md`, `config/profile.yml`, `config/profile.md`, `article-digest.md`,
`portals.yml`, `data/*`, `reports/*`, `output/*`, `interview-prep/*`

**System layer — safe to edit as code:**
`modes/_shared.md` and other modes, `*.mjs`, `batch/*`, `templates/*`, this file.

**Rule:** customization (archetypes, narrative, comp targets, location policy,
scoring weights) goes in `config/profile.md` or `config/profile.yml`. Never
`modes/_shared.md`.

`config/profile.md` is a hard dependency — `batch/ollama-scorer.mjs` and
`batch/staged-evaluator.mjs` read it at runtime.

## Entry points

```
snipe-tui.mjs (ink/react)
  ├─ snipe ──────── batch/local-runner.sh   the 3-phase pipeline
  ├─ scan.mjs ────────── providers/*.mjs         portal scan
  ├─ batch/import-pipeline.mjs                   data/pipeline.md → batch input
  └─ tracker/followup-cadence.mjs --json         follow-up due list
```

`snipe --jd "<text>"` adds a pasted JD and runs it. A run already holding
`batch/local-runner.pid` causes queueing instead; `snipe --drain` processes
the queue.

## The 3-phase pipeline (`batch/local-runner.sh`)

| Phase | Script | Model | Output |
|-------|--------|-------|--------|
| 1 pre-score | `ollama-scorer.mjs` | `snipe-screen` (Qwen3 4B q8_0) | `batch/scores/<id>.json` |
| 2 evaluate | `staged-evaluator.mjs` | `snipe-eval` (Qwen3 30B-A3B Q4_K_M) | `reports/<NNN>-<slug>-<date>.md`, `batch/evals/<id>.json` |
| 3 tailor CV | `local-pdf-offer.mjs` | `snipe-cv` (Qwen2.5 7B Coder Q5_K_M) | `output/<date>_<slug>_<NNN>/` |

`snipe-embed` (Qwen3 Embedding 0.6B q8_0) backs Phase 2's evidence matching and
Phase 3's bullet selection — see `batch/embeddings.mjs`.

Then `tracker/merge-tracker.mjs` → `data/applications.md`, and `tracker/verify-pipeline.mjs`.

**Phase 1** fetches the JD (provider API or HTML), caches it to `batch/jds/<id>.txt`,
scores schema-constrained. `score = cv_match×0.625 + north_star×0.375`.
Offers below `--p1-threshold` (default 2.5) skip Phase 2.

**Phase 2** is staged by default — three schema-constrained calls (JD parse →
embedding evidence match → judgment) with the report assembled in code, so the
model never writes markdown. `--classic-eval` reverts to the monolithic
`ollama-evaluator.mjs`. Phase 1's score is deliberately withheld from the prompt
to avoid anchoring. Salary is parsed from the JD in code (`text-utils.mjs`),
never guessed; when present the weights become `cv×0.50 + ns×0.30 + comp×0.20`.
Seniority and stack-mismatch caps (`fit-rules.mjs`) are code-enforced in both phases.

**Phase 3** runs only at score ≥ `auto_pdf_score_threshold` (default 3.0).
`cv-select.mjs` ranks CV bullets against Block B requirements via embeddings
first, so the 7B only rewrites — it never selects. PDF is hard-capped at 2 pages.

Embedding indexes (`batch/cv-index.json`, `batch/jd-index.json`) rebuild with
`node batch/embeddings.mjs rebuild` (auto-invalidated by `cv.md` hash).

### Common runs

```bash
bash batch/local-runner.sh                          # all phases
bash batch/local-runner.sh --skip-phase3            # no PDFs
bash batch/local-runner.sh --only-id 42 --retry-failed
bash batch/local-runner.sh --dry-run
```

State lives in `batch/local-state.tsv`; logs in `batch/logs/{score,eval,pdf}-*.log`.

### Hardware

RTX 3060 6 GB + 30 GB RAM. Phase 1 and 3 fit fully on GPU (`num_gpu 99`);
the 30B MoE **auto-splits** GPU/RAM — do not force `num_gpu` on it. Start the
server with q8_0 KV cache or 12k context will not fit:

```bash
OLLAMA_KV_CACHE_TYPE=q8_0 ollama serve
```

Models are built once from the Modelfiles:

```bash
ollama pull qwen3:4b-instruct-2507-q8_0          # bases, once
ollama pull qwen3:30b-a3b-instruct-2507-q4_K_M
ollama pull qwen2.5-coder:7b-instruct-q5_K_M
ollama pull qwen3-embedding:0.6b-q8_0

ollama create snipe-screen -f batch/Modelfile.snipe-screen
ollama create snipe-eval   -f batch/Modelfile.snipe-eval
ollama create snipe-cv     -f batch/Modelfile.snipe-cv
ollama create snipe-embed  -f batch/Modelfile.snipe-embed
```

## Quality is measured, not vibed

Model or prompt changes get benchmarked before they land:

```bash
node batch/eval-harness.mjs stats
node batch/eval-harness.mjs sample --n 12
node batch/eval-harness.mjs compare --a batch/bench/7b --b batch/bench/30b [--labels batch/labels.tsv]
```

`--bench-dir` keeps benchmark runs out of real `reports/` and `evals/`.

## Tracker rules

`data/applications.md` is the source of truth. `tracker/tracker.mjs` maintains an
optional SQLite index derived from it (safe to delete, regenerates on sync).

1. **Never hand-add rows.** Write a TSV to `batch/tracker-additions/` and let
   `tracker/merge-tracker.mjs` merge it. Editing status/notes of *existing* rows is fine.
2. Never create a second row for a company+role that already exists.
3. Statuses must be canonical (`templates/states.yml`): `Evaluated`, `Applied`,
   `Responded`, `Interview`, `Offer`, `Rejected`, `Discarded`, `SKIP`. No bold,
   no dates, no extra prose in the status cell.
4. A literal `|` in any cell corrupts the row and every column after it — both
   parsers split on raw pipes (`tracker/merge-tracker.mjs`,
   `tracker/verify-pipeline.mjs`). `buildRow()` substitutes it with `/`.
5. Reports need `**URL:**` and `**Legitimacy:**` in the header.

TSV format — 9 tab-separated columns, **status before score** (merge swaps them
to match the tracker's score-before-status layout):

```
{num}\t{date}\t{company}\t{role}\t{status}\t{score}/5\t{✅|❌}\t[{num}](reports/{num}-{slug}-{date}.md)\t{note}
```

The report link is always written root-relative; `tracker/merge-tracker.mjs` rewrites it
relative to the tracker's own directory (idempotent; `--migrate` fixes old rows).

Health: `node tracker/verify-pipeline.mjs` · normalize: `tracker/normalize-statuses.mjs` ·
dedup: `tracker/dedup-tracker.mjs`

## Tests

`node test-all.mjs` — 269 checks, must stay green. It's a launcher over
`test/*.test.mjs` (shared `test/harness.mjs`); run one suite in isolation with
`node test/<name>.test.mjs`.

## Conventions

Node `.mjs` ESM, YAML config, markdown data. Reports numbered sequentially,
3-digit zero-padded, max existing + 1. Output in `output/` (gitignored), JDs in
`batch/jds/`. Never hardcode CV metrics — read them from `cv.md` /
`article-digest.md` at evaluation time.

## Ethics

Quality over quantity. Below 4.0/5, recommend against applying. **Never submit
an application** — draft, fill, generate, then stop and let the user send it.
