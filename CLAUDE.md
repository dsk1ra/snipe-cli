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

A failed queue row renders as `✗ Company — Role  see error  retry | debug`
(→ focuses each, Enter fires it). It ends at `debug` and drops the job link every
other row carries — `see error` is already a link, and the posting is not what you
act on next; `o` and the tracker still reach it:

- **see error** — a `file://` hyperlink to `batch/errors/<id>.txt`, written by the
  TUI's poll from the fullest source available. That is the `fatal()` JSON in
  `batch/scores/<id>.json` / `batch/evals/<id>.json`, **not** the log: both
  scripts write `fatal()` to stdout and only stderr to `batch/logs/`, so every
  score and eval log is 0 bytes. Phase 3 is the reverse — its log is the whole
  artifact. The state row's `error` column is truncated at 200 chars, so it is
  only the fallback.
- **retry** — re-runs the offer through all three phases with
  `local-runner.sh --only-id N --retry-failed`, overwriting the last attempt.
  Not a plain re-queue: `drain_queue` runs `--only-id N` with no flags, and the
  three phase guards (`:899`, `:976`, `:1021`) all key off the stored status, so
  a row that is `scored` + `evaled` with no report skips Phases 1–2 and fails
  Phase 3 forever. `--retry-failed` overrides all three and clears the
  `MAX_RETRIES` gate. Costs a fresh report number per attempt, orphaning the old
  one. `unavailable` rows get no retry — the runner refuses expired postings.
- **debug** — opens the *input* that phase read, to be edited in place before the
  retry reads it back: the fetched JD (`batch/jds/<id>.txt`) for Phases 1–2, the
  Phase 2 report for Phase 3. Falls back to the `score`/`eval` payload when the
  input never landed, which is itself the diagnosis. The file, not its folder —
  `batch/jds/` holds every JD ever fetched.

Mapping check: `node snipe-tui.mjs --retry-plan <p1s> <p2s> <p3s> <rnum> <id>`.

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

### Benchmark rules (learned the hard way)

1. **Compare two runs made now. `batch/evals/` is NOT a control.** It is a
   historical archive whose calibration corpus grew underneath it (21→37→64→65
   offers over Jul 18–21), so it scores ~0.5 higher than the same code does today
   and silently invalidates any comparison against it.
2. **Run at temperature 0.** Greedy decoding is byte-identical on this stack —
   verified across 4 offers × 2 runs, evals *and* report prose. That makes the
   noise floor 0 and a single run a valid A/B. At temp 0.1 the floor was 0.091 and
   individual offers swung up to 2.1 points between identical runs.
3. **Know the resolution limit before believing a delta.** With 18 labels spanning
   3 distinct values, one label being off by ±1 moves Spearman rho by ~0.30. Any
   improvement smaller than that is indistinguishable from label noise, no matter
   how many runs you average. Report **pair accuracy** (fraction of differently-
   labelled offer pairs ordered correctly) alongside rho — it is far less tied.
4. **rho is corruptible here: the labels skew high, so generosity buys rho without
   buying truth.** Judge changes on row-level grounding too — how often a graded
   row cites evidence that does not contain the technology the requirement names,
   how often one CV atom is reused across many requirements, and whether any STAR
   story names a technology absent from `cv.md` (must stay zero).

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
{num}\t{date}\t{company}\t{role}\t{status}\t{score}/5\t{Y|N}\t[{num}](reports/{num}-{slug}-{date}.md)\t{note}
```

The report link is always written root-relative; `tracker/merge-tracker.mjs` rewrites it
relative to the tracker's own directory (idempotent; `--migrate` fixes old rows).

Health: `node tracker/verify-pipeline.mjs` · normalize: `tracker/normalize-statuses.mjs` ·
dedup: `tracker/dedup-tracker.mjs`

## Tests

`node test-all.mjs` — 795 checks, must stay green. It's a launcher over
`test/*.test.mjs` (shared `test/harness.mjs`); run one suite in isolation with
`node test/<name>.test.mjs`.

`npm run typecheck` — `tsc --noEmit` over the JSDoc types, also green, also in CI.
`npm run coverage` — the same suite under c8 (~89% lines, ~75% branches); CI uploads
`coverage/lcov.info` to Codecov. `all: true` in `.c8rc.json` counts never-loaded
files, so the number stays honest rather than flattered by exclusions.
`checkJs` is off in `tsconfig.json`: a file opts in by starting with `// @ts-check`
(24 do today), so adding a new one is a per-file decision rather than a repo-wide
gate. `providers/_types.js` is the shared type catalog.

### Testing things that need a model, a TTY, or the network

Three seams carry most of the suite, and reaching for the right one is usually
the whole problem:

- **`test/fake-ollama.mjs`** — an HTTP stand-in for Ollama. `/api/chat` and
  `/api/generate` read the JSON Schema the caller passes as `format` and
  synthesise a conforming answer, so it needs no per-stage knowledge and does not
  drift when a schema changes; `onChat` overrides it wholesale (the classic
  evaluator wants `<REPORT>`/`<SUMMARY>` prose, not JSON). Every phase script
  takes `--ollama-url`, so pointing it at the fake runs the real code end to end.
  Spawn the script — c8 works through `NODE_V8_COVERAGE`, so a subprocess counts.
  Use `runNodeAsync` from the harness, never `run()`: `execSync` blocks the event
  loop and the fake server never answers.
- **`test/tui-driver.mjs`** — fakes both TTYs, feeds key bytes, and captures the
  frames. It sets `FORCE_COLOR` because chalk's level is decided off a pipe as 0,
  which would erase the `inverse` that marks the focused row.
- **`SNIPE_PORTALS` + a temp cwd** — `scan.mjs` resolves its portal list from that
  env var and writes `data/` relative to the cwd, so a scan can run fully
  sandboxed. A `local-parser` portal makes it offline and deterministic.
- **`SNIPE_HOME`** — `snipe-tui.mjs` splits its *data* root from its *code* root:
  state, queue, applied/skipped, `batch-input.tsv`, `reports/`, `output/` and the
  tracker all resolve from `SNIPE_HOME`, while the scripts it shells out to
  (`local-runner.sh`, `scan.mjs`, `import-pipeline.mjs`) stay on the repo. The TUI
  test builds its whole fixture in a temp home, so a run killed mid-test cannot
  leave `#99000x` rows in the real queue — which is exactly what it used to do.
- **`SNIPE_TRACKER`** — `tracker/paths.mjs` reads it before either default
  layout, so every tracker script can be pointed at a fixture `applications.md`
  in a temp dir instead of the real one. `snipe-tui.mjs` honours it too, so the
  TUI and the tracker scripts it spawns cannot disagree about which file is the
  tracker. `SNIPE_ADDITIONS` is its sibling for `batch/tracker-additions/`, read
  by both `merge-tracker.mjs` and `local-pdf-offer.mjs` — a fixture TSV left in
  the real dir would otherwise be merged into the tracker by the next run.
- **An injected `ctx`** — providers never call `fetch` themselves; they take
  `{ fetchJson, fetchText }`. A stub returning canned payloads reaches every
  parse and normalisation path with no server at all (`test/providers-http.mjs`).
  Only `euremotejobs` and `apify` bypass it, and both are driven by swapping
  `globalThis.fetch` and restoring it in a `finally`.

Prefer a temp root over `preserve()` where a seam exists: `preserve()` only
restores if the `finally` actually runs, and a `SIGKILL`ed or crashed suite
leaves whatever it wrote. What still writes into the working tree (the embedding
indexes, Phase 1/2 JD and score artifacts) snapshots what it touches with
`preserve()` and restores in a `finally`;
`ensureUserLayer()` stands up a minimal `cv.md` / `config/profile.*` when the
gitignored real ones are absent, and removes only what it created.

Anything the TUI shells out to (`xdg-open`, `notify-send`, and the runner's
`bash` for retry) is stubbed onto `PATH` for the driven process — otherwise a
test run throws the developer's editor at them and a "retry" assertion starts a
real three-phase pipeline run.

## Conventions

Node `.mjs` ESM, YAML config, markdown data. Reports numbered sequentially,
3-digit zero-padded, max existing + 1. Output in `output/` (gitignored), JDs in
`batch/jds/`. Never hardcode CV metrics — read them from `cv.md` /
`article-digest.md` at evaluation time.

## Ethics

Quality over quantity. Below 4.0/5, recommend against applying. **Never submit
an application** — draft, fill, generate, then stop and let the user send it.
