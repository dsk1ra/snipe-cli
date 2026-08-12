# batch/ — the 3-phase pipeline and its benchmarks

Rules for everything under `batch/`. The repo-wide data contract, conventions
and ethics rules are in the root `CLAUDE.md` and still apply.

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

**Skills are selected, not dumped** (`selectSkills` in `cv-writers.mjs`). The
block used to ship 52 items — near enough the whole taxonomy, merely reordered —
of which 12.9 shared a term with the posting. The other 75% bought nothing an ATS
could read and cost 4.4 rendered lines on a page whose entire evidence budget is
21. Items now ship in three tiers and everything below them is dropped:

1. **named** — the posting's own terms, matched as whole phrases
2. **related** — what `cv.md` writes on the same line as a named item, so a Java
   posting still gets Spring Boot. The Skills section is excluded from that
   evidence: its lines list a category's items together by definition, so
   counting them would promote whole categories on one match
3. **floor** — `minItems` (3) in `cv.md`'s own order, so a block never shrinks to
   a mirror of the posting. Without it the CV reads as pandering and loses the
   distinctive evidence, which is `docs/PHASE3-RETENTION-LEDGER.md` §1 all over

`maxCats` (6) budgets only the categories the posting did *not* name; one it did
is never cut, which costs a seventh row on 2 of 32 offers and is why LADDER step 0
now passes no `--max-skills` at all. **Worth `skill_coverage` 0.932 → 1.000**
(+0.068, CI [0.032, 0.106], 9-0, p=0.004) and `ats_coverage` +0.014 (13-0,
p<0.001) against a 0.000 A/A floor, with 52 → 30 items, 979 → 958 px, and every
falsity metric and the one-page rate unmoved.

Three bugs were in the way, each of which silently deleted a real skill:

- **`parseSkillCategories` dropped every parenthesis**, so `Message Queues
  (RabbitMQ, Kafka)` shipped as "Message Queues". Kafka, AES-256-GCM, EC2/Lambda/
  S3/IAM, Jest, Jenkins, Ollama and MCP were deleted at parse time from every
  tailored PDF ever generated. Parts are now judged individually, so a name is
  promoted to an item and prose ("working knowledge") is still dropped.
- **`tokenize` has a 3-character floor**, so `C#` and `CI/CD` produce no tokens
  and scored 0 however loudly a posting asked. Ranking hid it — they shipped
  last; filtering exposed it. Selection now also tests whole-phrase presence.
- **A `.` survives phrase normalisation** for `Next.js`, which welded the period
  in "…experience with Kafka." onto the term. `normPhrase` is exported from
  `cv-writers.mjs` and used by the harness too, so the metric scores what the
  selector selected rather than drifting from it.

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

**The budget is 24 lines over 3 projects** (`SNIPE_LINE_BUDGET`,
`SNIPE_MAX_PROJECTS`), not 21 over 4. Four projects spent ~38px each on a title,
a badge and a tech line to deliver one bullet apiece — 43% of the largest section
on the page was chrome, and three of the four said one thing and stopped. Trading
a project's chrome, plus the 69px the page was simply not using, for bullets is
worth **+0.116 differentiator coverage** (n=32, 21-5, CI [0.051, 0.176], p=0.002)
with `noise_rate` −0.063, `grade_yield` +0.071, `mean_grade` +0.182 and
`mean_bullets` 2.69 → 3.44, against a 0.000 A/A floor. `skill_coverage` holds at
1.000, `ats_coverage` +0.016, grounding and every fabrication metric unmoved.
One page still holds for all 32, with the ladder firing on 3 and none past step 2.
**`projectBulletBudget` is dead on this path** — `allocateLines` spends
`lineBudget` and never reads it (`cv-select.mjs:579`), so setting
`SNIPE_PROJ_BUDGET` changes nothing unless `SNIPE_LINE_BUDGET=0`.

**Nothing may slice the skills block down to a fixed count.** `selectSkills`
keeps a category past the sixth precisely when the posting named something inside
it, so a blunt `slice(0, 6)` anywhere downstream cannot tell that category from
filler and silently deletes a claimed skill. Two places did: `clampContent`,
whose skill clamp now applies only to `--writer model` (it exists to contain a
model that ignores its schema, and cv-writers is not a model), and LADDER steps 1
and 2, which now leave skills untouched and pay for the overrun in project
bullets instead. Both took MCP off an EPAM CV that asks for MCP four times.
Because no offer needs a step past 2, `skill_coverage` is 1.000 **as shipped**,
not merely as selected.

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

The **summary** is a separate call (`batch/summary-stage.mjs`) on `snipe-eval`,
fed the selected bullets *and* Block B, at temperature 0. It follows the standard
CV template — positioning, the requirements the evidence answers in the posting's
own wording, one quantified achievement — and **the posting supplies the wording
while the evidence supplies the facts**.

Block B was withheld for a long time because handing a 7B the posting's
vocabulary produced straight parroting. Giving it back is worth `ats_coverage`
+0.029 (n=32, 21-3, CI [0.018, 0.041], p<0.001) and shape defects 0.844 → 0.063
per summary, but **it reopens every fabrication path at once** — 8 of 32 offers
invented a domain, a language or a figure the first time it landed, against 1 of
32 JD-blind. `docs/PHASE3-GENERATION-LEDGER.md` §11 has the full account; the
three things that hold it shut:

1. **Rejection, not repair.** A draft that claims anything `cv.md` cannot support
   is thrown away and re-requested *with the requirements withheld*. That sibling
   is clean by construction rather than by inspection — it never saw the posting.
   Fires on 3 of 32, so the common case still costs one call.
2. **`summaryUnsupported` is the gate and the metric.** One function, six classes
   (tenure, figure, credential, product, domain, cased_product). They were two
   functions and drifted, and the stage shipped 8 fabrications believing it had
   rejected everything.
3. **The gate knows about guards that run after it.** `stripJdProperNouns` runs
   downstream in `local-pdf-offer.mjs`; candidates it would gut are rejected up
   front, or the summary falls under the 50-word floor and picks up the generic
   "Targeting … end-to-end" closer.

**A figure the evidence does not literally contain is a fabrication even when the
arithmetic is right** — "93.0%" for 0.930, "14.1%" for 0.815 → 0.930, "87.5%"
where `cv.md` says 90%. This was the largest class, 6 of 8.

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

**`ats_coverage` is a breadth signal, not a target.** It counts every ≥3-char
token a JD and `cv.md` share, so most of what it calls a miss is generic English:
of 202 distinct missed terms over 32 offers, the leaders are `complex`,
`location`, `fast`, `where` and `never`, and only 17 are technologies. It cannot
reach 1.0 by any honest means and rewards padding on the way up. **`skill_coverage`
is the one to target** — of the skills a posting *names* and `cv.md` genuinely
claims, the fraction reaching the page, matched as phrases against the CV's own
taxonomy so there is no stoplist to tune and `NAT Traversal (STUN/TURN)` cannot
contribute a spurious `turn`. It is bounded above by what the CV claims, so
inventing cannot raise it, and it falls when a real skill is cut. It is `null`,
never 0, for a posting that names no skill at all. It sits at **1.000**.

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
   **Phase 3 under `--writer verbatim` — the production default — has a floor of
   exactly 0.000.** 32-offer A/A run 2026-08-08 (`aa1`/`aa2`, fresh selection in
   both, no `SNIPE_SELECT_CACHE`): all 32 `cv-content.json` byte-identical, every
   metric 0.000 with CI [0.000, 0.000], across 191 real model calls. Verified
   against rule 6 rather than assumed — see `docs/CV-ONE-PAGE-EXPERIMENTS.md` §1.
   Any nonzero delta is signal. Caveat: measured back-to-back on an idle machine
   with warm models, so re-check if a result rests on <0.01 under GPU contention.

   **The floors below are for `--writer model` only.** Deleting the 7B tailor call
   deleted the nondeterminism with it — what remains is a short summary and a
   schema-constrained list of small integers, with far less room to diverge than a
   page of rewritten bullets. `--writer model` survives as the benchmark control,
   and there the 7B tailor call **is not byte-identical at temp 0** — GPU
   batch/split variation flips a token regardless of temperature.
   `PHASE3-EXPERIMENT-LEDGER.md` measured this directly (24-offer A/A: 12.5% of
   offers vary; `grounding` ±0.020, `example_copy_pct` ±0.042, `mean_bullets`
   ±0.120) and **is the authority** for that arm. A later
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
   **This rule paid for itself.** `sum-new` read as a clean win — shape 0.844 →
   0.125, `ats_coverage` +0.028, `product_fab` a flat 0 — while fabricating on 8
   of 32 offers. The tell was `summary_jd_fit` +0.151 sitting next to
   `summary_cv_fit` −0.087, and nothing but reading five summaries confirmed it.
   `summaryUnsupported` now covers the domain gap this rule predicted, so the
   *specific* hole is shut; the habit is what generalises.
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
11. **The bench must render the document production renders, and score the
    document it rendered.** Three ways that broke, all found in one sitting:
    `withPageMetrics` passed `--max-skills 6` while LADDER step 0 had stopped
    capping, so it measured a 6-row page against an 8-row PDF; `outputText` still
    scored Core Competencies months after the template deleted it (+0.009); and it
    never read project *bullets* at all (−0.008). The last two nearly cancelled,
    which is why neither showed. Offsetting errors are luck — when the phantom
    section finally went, nothing would have offset the omission. After any
    template or ladder change, diff what the metric concatenates against what the
    page prints.

