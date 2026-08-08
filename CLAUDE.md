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

The footer hint line names **only what the focused element does** (`hintFor()`),
plus ` · ? keys`. The full reference is `HELP_ROWS`, shown by `?` as an overlay
that replaces the body and is closed by the next keypress (Ctrl-C excepted — a
help screen must not trap a quit). One place, not two: the window is routinely a
quarter of a screen wide, and a footer that spells every key out just truncates.
`HELP_ROWS` is ordered most-used first, since a short terminal clips the tail.
Two navigation tests used to assert on that footer string and so only proved the
hint line; they now anchor on each tab's own body.

A P1-gated row renders as `· Company — Role  P1 2.0 | proceed?  link` — the
same yellow action, because the `--p1-threshold` gate is a cost heuristic over a
4B pre-screen score, not a verdict. The number is `p1_score` straight from
`local-state.tsv`, drawn through the same `ScoreText` colour bands as an eval
score, because "how close was it" is the only input to the decision the row is
asking for. Rows whose `p1_score` is `-` fall back to the bare `P1-gated` label.

`proceed?` is a **question**, so it takes an answer as well as Enter — but only
while it is the focused stop, so no new global hotkeys and `q` still quits:

- **y** / **Enter** — proceed (below).
- **n** / **Backspace** / **Delete** — dismiss. Nothing on the pipeline changes;
  the row was already finished. The offer retires and the row renders as
  `· Company — Role  P1 2.0  link`. The answer is written to
  `batch/p1-declined.tsv` (`readMarkMap`/`writeMarkMap`, same id→ISO shape as
  `applied.tsv`/`skipped.tsv`) because the row list is rebuilt from disk every
  second and from scratch on every launch — an in-memory "no" would come back.
  Undo is deleting the line. Deliberately **not** the `x` skip mark: `toggleMark`
  requires an eval and drives the tracker to SKIP, and a gated offer has neither
  an eval nor a report number to `syncTracker` against.

- **proceed?** — `local-runner.sh --only-id N --p1-threshold 0`. Nothing is
  re-scored: the Phase 1 offer list skips anything already `scored` (`:918`), so
  the cached score and JD are reused and the run starts at Phase 2 — reusing
  Phase 1's work is what the flag already does, so there is no second code path
  for it. No `--retry-failed`: the row is `p1-gated`, not failed, and Phase 2's
  own guard (`p2_status != evaled`) already lets it through. Phase 3 follows on
  its own if the eval clears `--threshold`. The tracker row written by
  `write_tracker_p1_skip` is updated in place by the merge's company+role dedup —
  but only if the eval scores *higher* than the P1 pre-score; below it, the merge
  keeps the older row and the stale "no report — P1-gated" note with it.

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
first. PDF is hard-capped at 2 pages.

Ranking is `cos − α·corpus_mean + 0.10 × judge_grade`, `α = w/(1+w)` at
`spikeWeight = 6`. That middle term — **corpus-relative specificity** — is what
makes the ranker prefer *distinctive* evidence over merely relevant evidence:
a bullet scoring 0.6 against every past posting is filler, one scoring 0.6 here
and 0.31 elsewhere is a differentiator. Worth **+0.060 differentiator coverage
held out** against the full production ranker (n=66, CI [0.017, 0.103], 25-9,
p=0.009) with `grade_yield` flat, and **+0.080 in a full Phase 3 run** (n=32,
11-2, p=0.022) with ATS coverage and every falsity metric unmoved.
All three Phase 3 changes together: **0.311 → 0.548 (+0.237, 24-2, p<0.001)**.
The corpus mean is built from past reports' Block B requirement sets and cached
in `batch/cv-spike.json`; under 20 usable reports it returns null and ranking
falls back to plain cosine. **The background cannot come from `jd-index.json`** —
whole-JD cosine is a different scale from max-cosine-against-requirements, and
measured −0.025. Only the real embedder may write the cache (`caching =
_embed === embed`), or a test stub would poison production ranking.

**There is no bullet-generation call.** `--writer verbatim` is the default: the
selected bullets render as `cv.md` already words them, and projects render 2
bullets each rather than a prose paragraph. Deleting the 7B rewrite and adding
those bullets measured **+0.157 differentiator coverage** (20-3, p<0.001),
+0.061 ATS coverage, grounding to a flat 1.000 — 78% of what the 7B emitted was
byte-identical to its source line anyway, and the 22% it changed is where the
fabrication lived. Scaling the writer does not fix it: a 9B and the 30B both buy
~0.05 coverage and lose grounding in *every* offer they change (0-32, 0-11).
`--writer model` survives as the benchmark control only. Full numbers and the
two production bugs found on the way: `docs/PHASE3-RETENTION-LEDGER.md`.

Project bullets are **allocated, not capped**. `cv-select.mjs` spends one total
budget (`maxProjects × 2` = 8, exactly what a flat 2-per-project rendered) across
the projects that survived the top-4 cut: one bullet each, then the rest to the
highest-scoring bullets anywhere, ≤ `maxBulletsPerProject` (4) per project. So a
posting mostly about one project renders 4/2/1/1 and a flat field still renders
2/2/2/2. Worth **+0.101 differentiator coverage** end to end (n=32, 15-4,
CI [0.039, 0.161], p=0.019) with `grade_yield` +0.030, `mean_bullets` identically
8.000, and every falsity metric unmoved — it is redistribution, not expansion.
Costs `ats_coverage` −0.015 (CI [−0.028, −0.002], 7× its A/A floor, so real).
Attacks the one loss bucket no ranker or rewrite can reach: a third flagged
differentiator inside a two-bullet project.

`SNIPE_PROJECT_BULLETS` is now the per-project **ceiling**, not the count
(default 4) — it only has to avoid clipping the allocation, since cv-select
already spent the budget. The same is true of the density ladder's step 0 in
`local-pdf-offer.mjs`; every tighter step still drops project bullets before
experience bullets. All 32 bench offers render at 2 pages on step 0.

**`trim()`'s metric-bullet guarantee overrides the ranker at keep=1**, which the
allocation now reaches: all 57 single-slot project bullets across the 32 offers
carry a digit against a 72% base rate, and the swap fires on 42% of them. It cost
one differentiator in 32 offers, so it is not eating the gain — but it is the
first suspect for that ATS dip.

That ranking is then reranked by `snipe-eval`, which grades each bullet 0–3 for
the posting and is blended in at `cos + 0.10 × grade` (+0.10 pair accuracy
against the human gold set, holds under a disjoint exemplar pair — see
`docs/PHASE3-RETRIEVAL-LEDGER.md`). It is few-shot from two hand-labelled
offers in `batch/judge-shots.json`; **0-shot it is worse than no rerank at
all**, so a missing or unmatched exemplar file disables it rather than
degrading it, as does any model failure. Still worth its 66 s: deleting it costs
−0.021 differentiator coverage held out even with spike on. (42 s was the plan's
estimate and is not a measurement — it survived in two places until a wall-clock
check on a fresh select cache put a verbatim-writer offer at 67 s end to end,
which is the judge plus the summary stage and little else.)

**Known defect — the judge grades binary.** Its exemplars are built as
`want.has(text) ? 3 : 0` from binary human ticks, so every demonstration is a 0
or a 3, and demonstrations beat the system prompt's "use the full range": **30 of
4191 gradings across 128 offers use the middle of the scale.** The retrieval
ledger already priced binarisation at 0.03 pair accuracy, so the judge has been
paying that all along. The fix is graded exemplars, not a prompt edit — see
`docs/PHASE3-RETENTION-LEDGER.md` §4.10. Not attempted yet. Costs one 30B call, **measured 66 s**
— 80% of Phase 3's wall clock, and validated held-out on `goldset-2.md` at
**+0.115 pair accuracy, 11 wins 0 losses, CI [0.074, 0.159]**. Alone it is worth
nothing (0.801 vs cosine's 0.815); only the blend pays. Do not binarise the
grades to shrink the output — that costs 0.03.
Regenerate exemplars after editing cv.md:
`node batch/goldset.mjs export-shots --ids 5,50`.

The **summary** is a separate call (`batch/summary-stage.mjs`), fed the bullets
that will actually appear on the CV. It is deliberately *not* given the JD's
requirements: `cv-select` has already ranked the evidence against them, and
handing the posting's vocabulary to a 7B produced straight parroting. Candidates
must pass a prose gate before being scored, and the main JSON call's summary is
the incumbent — the stage only displaces it by a clear margin.

Named products absent from `cv.md` are **rejected, not counted**: experience
bullets revert to their CV source line, summaries and project blurbs get clause
surgery (`stripFabricatedProducts`).

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

Phase 3 *selection* has its own ground truth and harness, because a tailored CV
is a document with nothing to rank-correlate:

```bash
node batch/goldset.mjs sheet          # 12 offers x 14 CV atoms, for a human to tick
node batch/goldset.mjs score          # does the shipped ranker agree with the human
node batch/retrieval-bench.mjs run    # A/B every retrieval variant, paired, with CIs
node batch/retrieval-bench.mjs run --sheet batch/bench/goldset-2.md \
  --grades batch/bench/judge-grades-2.json      # the held-out set
```

`batch/bench/goldset.md` is hand-labelled ground truth force-added past
.gitignore; `sheet` refuses to overwrite ticks without `--force`. Variants are
compared **paired per offer** with a bootstrap CI over offers and a sign test —
a dozen variants against twelve offers will otherwise always find a "winner".

`goldset-2.md` is a second, disjoint sheet used as a **held-out** check.
`bench-tools/gen-judge-grades.mjs` caches the 30B's grades for a sheet so a
variant sweep costs no model calls. Everything in `batch/bench/` is gitignored,
so a new fixture the code depends on must be `git add -f`ed or it exists only on
one disk.

Phase 3 *generation* has its own harness — eight label-free metrics, no model in
the loop for six of them:

```bash
node batch/tailor-harness.mjs run <label> --temperature 0 [--model M] [--limit N]
                             [--sample F.tsv] [--writer verbatim|model] [--out DIR]
node batch/tailor-harness.mjs metrics <label> [--rows] [--no-embed]
node batch/tailor-harness.mjs compare <a> <b>
node batch/tailor-harness.mjs paired  <a> <b> [--no-embed]
```

`paired` is the one to trust: per-offer deltas with a bootstrap CI95 over offers
and a two-sided sign test, so a metric that moves on 2 of 32 offers cannot read
as a win. It drops offers where either run returned null rather than zeroing
them. `compare` pairs on the offers **both** runs produced and says how many. Timing
comes free: `SNIPE_TIMING=path` makes every Ollama call append its own
`load_duration` / `eval_count` (`batch/timing.mjs report`), which is how a model
reload gets counted rather than guessed.

Those eight metrics all punish **falsity**, and an empty CV scores perfectly on
every one of them — so none of them can see a CV that dropped the thing that
differentiates you. `batch/opus-metrics.mjs` closes that hole against a label
corpus in `batch/bench/opus/labels/` (128 offers, each CV atom graded 0–3 for
that posting, and the differentiators marked):

| metric | what it catches |
|---|---|
| `differentiator_coverage` | fraction of the offer's differentiator atoms that reached the page |
| `differentiators_lost` / `lost_ids` | which ones did not, by id |
| `noise_rate` | fraction of shipped content the labeller graded 0 for this posting |
| `grade_yield` | shipped grade mass ÷ available grade mass |

Coverage is measured **atom→output** (does this atom appear anywhere), the
opposite direction from `shippedAtomIndices` — an atom split across two bullets
still counts. Relabelling is `batch/bench-tools/opus-label.mjs`; the labels are
positional against `cv.md`, so **editing `cv.md` invalidates all 128**.
`SNIPE_SELECT_CACHE` freezes selection across arms, so a generation A/B costs no
66 s judge call and every arm ranks identically — which also means **a selection
change must not reuse an existing select cache**, since the key is over the CV
and requirements, not the ranker.

Selection changes get swept offline instead, because testing one end-to-end costs
a judge call per offer:

```bash
node batch/bench-tools/select-sweep.mjs prep      # cosines, local and free
node batch/bench-tools/select-sweep.mjs grades    # 30B grades, ~110 s/offer, cached
node batch/bench-tools/select-sweep.mjs validate  # does the sim match the real run?
node batch/bench-tools/select-sweep.mjs ablate --split train
node batch/bench-tools/select-sweep.mjs check  --split test --spike 6
```

`attribute` says *why* the remaining differentiators are lost — beaten by their
own project siblings, project never made the cut, or more than `PROJ_BULLETS`
differentiators inside one project, which no ranker or rewrite can recover.
The fix differs per bucket, so read it before proposing one.
**`docs/PHASE3-NEXT.md` holds the current attribution, the two candidate
experiments, and the ideas already closed** — read it before re-opening any of them.

`validate` is the load-bearing command: the simulator reproduces the funnel over
cached cosines, and its deltas mean nothing until it reproduces the number a real
run measured (it lands within 0.039). Offers split train/test by id parity —
`sweep`/`ablate` tune on train, `check` scores one config on held-out and is
never used to choose one. A half-populated grade cache silently ranks the
ungraded offers as all-zero, which is a different ranker rather than a missing
term, so the tools drop the judge term entirely unless every offer in the split
has grades.

### Benchmark rules (learned the hard way)

1. **Compare two runs made now. `batch/evals/` is NOT a control.** It is a
   historical archive whose calibration corpus grew underneath it (21→37→64→65
   offers over Jul 18–21), so it scores ~0.5 higher than the same code does today
   and silently invalidates any comparison against it.
2. **Run at temperature 0** — but the noise floor is 0 only where it was measured.
   Greedy decoding is byte-identical for **Phase 2**: verified across 4 offers × 2
   runs, evals *and* report prose. At temp 0.1 that floor was 0.091 and individual
   offers swung up to 2.1 points between identical runs.
   **Phase 3's 7B tailor call is not byte-identical at temp 0** and never was in
   that check — GPU batch/split variation flips a token regardless of temperature.
   `PHASE3-EXPERIMENT-LEDGER.md` measured this directly (24-offer A/A: 12.5% of
   offers vary; `grounding` ±0.020, `example_copy_pct` ±0.042, `mean_bullets`
   ±0.120) and **is the authority** — this rule used to contradict it. A later
   12-offer A/A adds floors for the metrics the ledger never floored:

   | metric | floor | from |
   |---|---|---|
   | `ats_coverage` | ±0.002 | 12-offer A/A |
   | `selection_regret` | ±0.001 | 12-offer A/A |
   | `summary_cv_fit` | ±0.004 | 12-offer A/A |
   | `summary_jd_fit` | ±0.016 | 12-offer A/A |
   | `grounding` | **±0.020** | 24-offer ledger — use this, not the 12-offer 0.009 |
   | `mean_bullets` | **±0.120** | 24-offer ledger |

   Take the wider floor when two measurements disagree. This is not academic: the
   content-floor change scored `grounding +0.021`, which reads as a win against the
   12-offer floor and is **inside** the 24-offer one, so it is not claimable —
   while `ats_coverage +0.145` clears its floor by 70×. Run the A/A whenever a
   Phase 3 result rests on a small delta; two runs of the *same* label cost what
   one A/B costs and are the only thing that says which deltas exist.
3. **Know the resolution limit before believing a delta.** With 18 labels spanning
   3 distinct values, one label being off by ±1 moves Spearman rho by ~0.30. Any
   improvement smaller than that is indistinguishable from label noise, no matter
   how many runs you average. Report **pair accuracy** (fraction of differently-
   labelled offer pairs ordered correctly) alongside rho — it is far less tied.
4. **Never edit a phase's files while a benchmark of that phase is running.**
   `local-pdf-offer.mjs` is spawned per offer and re-imports its modules each
   time, so an edit mid-run silently splits the run: offers before the edit ran
   one version, offers after ran another, and the mean is of neither. Kill and
   restart instead of measuring the mongrel.
5. **A metric defined against a thing you are about to delete is not a metric.**
   `example_copy_pct` read the worked example out of the live prompt, so
   deleting the example — the fix being tested — would have driven it to 0 by
   construction and scored a perfect win. It now unions a committed snapshot
   (`batch/bench/example-bullets.json`). Ask of any metric: what does this read
   if the change lands?
6. **A zero-width confidence interval is a plumbing failure, not a null result.**
   `retrieval-bench.mjs` had no `--sheet` flag and always loaded sheet 1, so
   sheet 2's grades matched no offer and the run reported delta 0.000, CI
   [0.000, 0.000] — which reads like a clean negative and is not.
7. **The summary metrics can both be satisfied by output that is wrong, and were.**
   A summary parroting the posting raises `summary_jd_fit`; `product_fab` does
   not know a *domain* ("HFT", "financial markets") from a product, and
   `metric_fab` does not know "over a decade" is a number. Rewarding evidence
   overlap then produced a slash-separated keyword dump that scored well and was
   not prose. Read the actual output of the first few offers of any generation
   change before trusting its table.
8. **rho is corruptible here: the labels skew high, so generosity buys rho without
   buying truth.** Judge changes on row-level grounding too — how often a graded
   row cites evidence that does not contain the technology the requirement names,
   how often one CV atom is reused across many requirements, and whether any STAR
   story names a technology absent from `cv.md` (must stay zero).
9. **Ask what the metric suite scores an empty output, before trusting a win.**
   All eight generation metrics punish falsity, so the null CV is perfect on all
   of them and "lost the thing that makes me hireable" was invisible for the
   whole life of the harness. A suite that only measures one direction is not a
   suite. See `docs/PHASE3-RETENTION-LEDGER.md` §1.
10. **Try deleting the model call before scaling it.** The 7B tailor was retyping
    78% of its input verbatim and fabricating in the rest; removing it beat it,
    and a 9B and the 30B both lost to *no writer at all*. Rendering — projects as
    bullets rather than a paragraph — was worth more than any model swap and cost
    nothing. Check what the template can physically print before blaming a model
    for not printing it.

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

`node test-all.mjs` — 1121 checks, must stay green. It's a launcher over
`test/*.test.mjs` (shared `test/harness.mjs`); run one suite in isolation with
`node test/<name>.test.mjs`.

`npm run typecheck` — `tsc --noEmit` over the JSDoc types, also green, also in CI.
`npm run coverage` — the same suite under c8 (~90% lines, ~77% branches); CI uploads
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
- **`SNIPE_BENCH_DIR`** — redirects the bench root that `retrieval-bench.mjs` and
  `tailor-harness.mjs` resolve `BENCH` from, so a fixture run cannot touch the real
  one. That matters more than sandboxing usually does: `batch/bench/` holds a 28 MB
  embedding cache and `bench/tailor/` a 5 MB one, both keyed by model fingerprint,
  so a run with a fake embedder overwrites them and the undo is a full re-embed.
  `BENCH` is a module-level const, so the env var must be set **before the import**
  — and `units.test.mjs` imports `tailor-harness.mjs` first without it, which is why
  `test/bench.test.mjs` imports that one under a `?bench-root=` query to force a
  fresh instance. Coverage still attributes to the same file.
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
