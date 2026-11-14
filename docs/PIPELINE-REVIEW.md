# Pipeline review: phases 1 to 3

Started 2026-11-11 on branch `pipeline-quality`. It maps what each phase does
and how long it takes, records a baseline for every gate, ranks the changes
worth trying, and logs what each one measured. Numbers come from a copy of the
live home, never the live home itself.

A second pass on 2026-11-14 ran the list section 6 held then. Its results
start at "Second pass" in section 4.

## 1. Map

This is the pipeline as the branch found it. Section 4 says what changed.

One offer goes through seven model calls and four kinds of embedding call.
Every chat call is schema-constrained and runs at temperature 0.

### Phase 1: pre-score (`internal/phase1`, `internal/extract`)

Input is the posting's URL from `batch/state/batch-input.tsv`. The text is
fetched once and cached in `batch/jds/<id>.txt`; every later phase reads that
file.

| call | prompt | model | num_ctx | reads | writes |
|---|---|---|---|---|---|
| `p1.score` | `ollama-score-prompt.md` (`.local.md` wins) | `snipe-screen`, Qwen3 4B q8_0, all on GPU | 12288 | cv.md, profile.md, profile.yml, posting cut to 10k chars | `cv_match`, `north_star`, `archetype` (enum from the profile), up to 5 gaps each with a posting quote, 3 strengths, confidence |
| `p1.extract` | `p1-extract.md` | `snipe-screen` | 8192 | the posting | attendance, human languages, years, stack, each with a quote; cached per posting text in `batch/cache/extract-snipe-screen.json` |
| `p1.cvstack` | `p1-cvstack.md` | `snipe-screen` | 8192 | the ledger's entity names | the programming languages cv.md demonstrates; once per ledger |

The score is computed in code: `cv_match × 0.625 + north_star × 0.375`, each
dimension first clamped by the stack, language, location and seniority caps
(`internal/fit`). A gap is kept only if its quote is in the posting, a strength
only if every technology it names is in cv.md. An offer under 2.5 never
reaches Phase 2.

### Phase 2: evaluate (`internal/phase2`)

| call | prompt | model | num_ctx / num_predict | reads | writes |
|---|---|---|---|---|---|
| embed | none | `snipe-embed`, Qwen3 Embedding 0.6B q8_0 | | cv.md atoms (cached in `cv-index.json`), then the posting cut to 2,000 chars | the 3 most similar past postings with their scores and outcomes |
| `p2.jdparse` | `p2-jdparse.md` | `snipe-eval`, Qwen3 30B-A3B Q4_K_M, 25% on GPU | 8192 / 1800 | posting cut to 8,000 chars | company, role, seniority, 3 to 12 requirements each with `must_have`, `kind`, `terms` and a quote, plus keywords |
| embed | none | `snipe-embed` | | the requirement texts | 3 candidate cv.md lines per requirement: the top 3 bullets, with one Skills or Education line allowed to take the third slot |
| `p2.evidence` | `p2-evidence.md` | `snipe-eval` | 8192 / 1800 | each requirement with its candidates A, B, C cut to 200 chars | per requirement: `pick`, `same_activity`, `same_tooling`, a note |
| `p2.judgment` | `p2-judgment.md` | `snipe-eval` | 12288 / 5120 | the graded table, coverage, the similar offers, profile.md, cv.md, 2,500 chars of posting | `cv_match`, archetype, gaps, strengths, strategy, the personalisation table, a decision |

Strength is derived in `fit.StrengthFrom`, not asked for. No pick, or a
different activity, is a Gap. Different tooling, a Skills or Education line, or
a requirement naming a technology its evidence lacks, is Transferable. The rest
is Strong. Coverage is `0.75 × must + 0.25 × nice` with Strong 1, Transferable
0.6 and Gap 0.

The score is `cv × 0.625 + north_star × 0.375`, or `cv × 0.50 + north_star ×
0.30 + comp × 0.20` when the posting states a salary. `cv` is `0.6 × cv_match +
0.4 × (1 + 4 × coverage)` under the same caps as Phase 1. `north_star` is the
archetype's `fit` tier from profile.yml. `comp` is parsed from the posting.
Under 3.0 the decision is Skip. The report goes to `reports/`, the artifact to
`batch/evals/<id>.json`.

### Phase 3: tailor (`internal/phase3`, `internal/cvselect`, `internal/fill`)

Runs at 3.0 or more. It ranks cv.md's 33 Experience and Projects bullets and
prints the winners as cv.md words them.

| call | prompt | model | num_ctx / num_predict | reads | writes |
|---|---|---|---|---|---|
| embed | none | `snipe-embed` | | the report's requirements and the 33 bullets | each bullet's best cosine against any requirement |
| embed | none | `snipe-embed` | | every requirement of every past report | each bullet's mean score over past postings, cached in `cv-spike.json` |
| `p3.judge` | `p3-judge.md` | `snipe-eval` | 12288 / 1536 | 2 worked postings from `judge-shots.json`, then the requirements, 2,500 chars of posting and the numbered bullets | a 0 to 3 grade per bullet |

A bullet's score is `cosine − 6/7 × its corpus mean + 0.10 × grade`.
`cvselect.Select` then spends 22 rendered lines: 3 projects (pinned ones
first), at most 13 lines of projects, at least 2 bullets for the top role, at
most 4 per entry. Skills are cut to the categories the posting names, in the
posting's spelling where `config/skills.yml` approves one. A density ladder
tightens the layout until `pdflatex` gives one page.

After the last offer the runner merges the tracker and rebuilds the skill
alias suggestions, which embeds about 800 terms.

### Where the time goes

Three offers run one at a time through `snipe run --only-id`, on a copy of
the live home, 2026-11-11. Seconds, with the model load inside each figure in
brackets.

| step | #45 | #108 | #52 |
|---|---|---|---|
| `p1.score` | 14.9 (4.3) | 13.7 (4.1) | 11.4 (3.7) |
| `p1.cvstack`, once per ledger | 24.0 (3.6) | | |
| embed the posting | 3.6 (3.4) | 3.4 (3.2) | 3.3 (3.1) |
| `p2.jdparse` | 57.6 (13.9) | 33.9 (6.8) | 49.7 (6.4) |
| embed the requirements | 12.1 (11.9) | 13.2 (13.0) | 12.4 (12.2) |
| `p2.evidence` | 38.4 (6.4) | 24.9 (6.6) | 34.2 (6.6) |
| `p2.judgment` | 66.0 (6.8) | 53.0 (7.2) | 66.0 (6.8) |
| embed the spike background, 4,037 texts | 67.9 (26.4) | 66.2 (25.5) | 66.9 (25.5) |
| `p3.judge` | 50.4 (6.7) | 48.5 (6.4) | 49.8 (6.9) |
| embed the alias suggestions, 806 texts | 21.2 (14.8) | 20.8 (14.5) | 21.0 (14.8) |
| Phase 1 wall | 38.9 | 13.7 | 11.5 |
| Phase 2 wall | 178.0 | 128.5 | 165.7 |
| Phase 3 wall | 120.1 | 116.5 | 118.6 |
| whole run | 358.8 | 280.1 | 317.5 |

`p1.extract` was a cache hit on all three. On a posting it has not seen it
adds 11 s, 4 s of that a reload (median of 56 calls in the live timing log).
So a new posting costs about 5.4 minutes against the 7 minute budget.

Two things in that table are not model work.

Loading models takes 86 to 98 s of every run. Only one model stays resident in
6 GB, so each embedding call evicts the 30B (3 to 13 s to load the embedder)
and the next 30B call loads it back (6 to 7 s). The 30B is also reloaded when
`num_ctx` moves from 8192 to 12288 between `p2.evidence` and `p2.judgment`,
and the 4B when it moves between `p1.score` and `p1.extract`.

The spike background is rebuilt from nothing on every single-offer run. Its
cache key includes the list of report files, and Phase 2 has just written a
new one. That is 4,037 texts and 67 s to add one report's worth of numbers to
a mean over 404.

REWORK-PLAN.md projected 2.7 minutes per offer after the judgment was
slimmed. The judgment did shrink (64 s against 171 s in August), but stage 1
grew from 507 to 917 output tokens when the typed fields went in on
2026-10-22, the judge came back on 2026-11-09, and the two costs above were
never in the projection.

## 2. Baseline

`bin/base` is develop at 062c890 plus the two gates this branch adds, `gate
labels` and `gate goldset grade`. Every gate ran twice, A and B, each in its
own copy of the home and one model job at a time. The gap between A and B is
the noise a change has to beat.

| gate | what it measures | n | A | B |
|---|---|---|---|---|
| `goldset score` | stage-2 retrieval: is a ticked line among the 3 shown | 63 ticked requirements | hit@3 0.794, hit@1 0.381, p@3 0.333 | the same |
| `goldset grade` | stage 2 end to end: does the line it cites carry a tick | 63 ticked, 25 blank | cited a ticked line 35 (0.556), an unticked one 26, false gap 2, precision 0.574, blank graded Gap 9 of 25 | the same; 2 of 88 rows move between Strong and Transferable |
| `judge score` | Phase 3 ranking: how often a ticked bullet outranks an unticked one | 12 postings, 33 bullets | cosine 0.750, blend 0.847 | the same means; one posting differs by 0.005 |
| `tailor` | the shape of the page | 16 offers | one page 16 of 16, 51.06 lines, exp_starved 1.375, front_load 0.307, keep_rate 0.958 | 51.13 lines, front_load 0.302 |
| `ats` | must-haves the page gives evidence for, judged by gemma3 4B | 15 offers | 0.814, preferred 0.856, blank CV 0.030 | 0.831, preferred 0.826 |
| `skills` | posting terms the Skills block can carry | 3,668 terms | 99.0% | the same |
| `typed` | stage-1 requirements that carry their typed fields | 12 postings | 133 of 138 | 131 of 136 |
| `labels`, Phase 2 | a fresh Phase 2 score against the 18 rated offers | 83 pairs the ratings separate | pair accuracy 81.3%, rho 0.546, MAE 0.74 | the same, offer by offer |
| `labels`, Phase 1 | a fresh Phase 1 score against the same ratings, and against stored Phase 2 scores on 40 offers | 83 pairs; 40 offers | 56.6%; against Phase 2 pair 0.645, recall_good 15 of 18, drop_bad 4 of 8 | all 40 scores identical |

Phase 1 is deterministic: 40 offers, 40 identical scores. So were the 18
Phase 2 runs: score, cv_match and coverage are equal on every offer. Stage 1
is still not fixed in general: one posting in the `typed` sample came back
with 10 requirements instead of 12. The tailor and ATS differences between A and B
both come from offer 323, which selected different bullets on the second run.
The embedder is the likely cause. On the GPU it does not return the same
vector twice: the cached CV index and a fresh embedding of the same 57 lines
differ by up to 0.0024 per component.

### What each gate can be trusted for

The evidence sheet (`batch/bench/evidence-goldset.md`) was remapped to the
current cv.md on 2026-11-09. 45 of its ticks are unconfirmed carry-overs and
14 Zero Trust ticks were dropped. Its candidates were pooled from one
retriever, so a line no arm ever showed counts as a miss whether or not it is
evidence. It ranks two arms against each other. It does not say how good
either one is.

The judge sheet (`batch/bench/judge-2026-11-09.md`) matches the current cv.md.
A model ticked it, not the user.

The 18 ratings take three values. One rating off by one moves a single pair,
1.2 points of pair accuracy, so two arms within 2 or 3 points are tied.

`tailor`, `ats` and `skills` need no labels. The page-shape metrics are
saturated: one page 16 of 16, term parity 1.000. `ats` is the only one of the
three with room to move.

### Where stage 2 loses

Of the 61 citations on ticked requirements, 26 land on a line without a tick.

- In 13 the grader had a ticked line in front of it and chose another. 8 of
  those chose a Skills or Education line over a bullet.
- In 13 no ticked line was among the three shown. Retrieval lost these before
  the grader saw anything.

Two more requirements were graded Gap with a ticked line shown. 16 of the 25
requirements the sheet left blank were given evidence anyway.

### Where Phase 1 loses

Against the ratings Phase 1 orders 47 of 83 pairs correctly, where a coin
would get 41.5.
Three offers rated 4 fall under the 2.5 gate and never reach Phase 2: #34 and
#39 at 2.4, and #36 at 2.0 under the language cap (the posting asks for
Chinese).

## 3. Proposals

Ranked by what each one should return for the GPU time it costs to test. The
result of each is in section 4, the ones that lost in section 5.

### Speed

| # | change | expected | how it is checked |
|---|---|---|---|
| T1 | Keep the spike background as one row per report, so a new report costs one row | 41 s of embedding per offer, and with T2 the 26 s swap that goes with it | rows from an incremental update equal the rows of a full rebuild; `judge` and `tailor` unchanged; three timed runs |
| T2 | Run the embedder on the CPU for small jobs, so it stops evicting the 30B | 40 to 50 s of model swaps per offer | CPU and GPU vectors agree and pick the same top 3; `goldset score` and `judge` unchanged; three timed runs |
| T3 | Give `p1.extract` and `p1.cvstack` the 12288 window `p1.score` uses | 3.7 s per new posting, twice that in a batch | 40 fresh Phase 1 scores and extractions identical to baseline |
| T4 | One window for all three Phase 2 stages | 7 s | not run: it changes what stages 1 and 2 are asked, a 50-minute gate for 2% of the run. The August ledger reverted the same change unvalidated |

### Quality

| # | change | targets | how it is checked |
|---|---|---|---|
| Q1a | Show the grader each candidate line in full. 14 of the 57 lines run past the 200-character cut, and what is cut is usually the technology | the 13 wrong picks with a ticked line shown | `goldset grade`, then Phase 2 on the 18 rated offers |
| Q1b | Order the grader's reply so the note comes before the pick, or between the pick and the two grades | the same 13 | `goldset grade` |
| Q1c | Put the Skills or Education line last instead of where its cosine ranks it | the 8 of those 13 that chose a Skills or Education line over a bullet | `goldset grade` |
| Q1d | Rank on the bullet without its "Project name:" prefix; use an instruction prefix on the query; show more than 3 | the 13 with no ticked line shown | screened on embeddings alone, the survivors through `goldset grade` |
| Q2 | Phase 1 `north_star` from the archetype's `fit` tier, as Phase 2 already does, not from the 4B | offers rated 4 that Phase 1 gates | fresh Phase 1 on the 40 offers plus the 68 applied or skipped ones; both policies are computed from the same run |
| Q3 | Tell Phase 3 which bullets Phase 2 cited | the judge gate, the must-have evidence on the page | needs a fresh Phase 2 on the judge postings: every stored report quotes cv.md as it was worded in August |

## 4. Results

Each kept change is one commit, with these numbers in its message.

### T1. Spike background per report (`01a6218`)

The cache holds one row per report, keyed on the bullets and the embedder. A
new report adds a row.

| | before | after |
|---|---|---|
| #45, whole run | 358.8 s | 298.3 s |
| #108, whole run | 280.1 s | 220.9 s |
| #52, whole run | 317.5 s | 259.5 s |
| Phase 3 wall | 117 to 120 s | 62 to 64 s |
| `judge`, blend | 0.847 | 0.846 |
| `tailor`, 16 offers | | equal to baseline B on every metric |

After three incremental updates the background mean is within 0.00007 per
bullet of a full rebuild.

### T3. One context window in Phase 1 (`396b0d0`)

| | before | after |
|---|---|---|
| `p1.extract`, mean of 39 calls | 9.59 s | 6.46 s |
| of which load | 3.70 s | 0.16 s |
| 40 Phase 1 runs | 1,507 s | 1,390 s |
| scores equal | | 40 of 40 |

12 of the 39 extraction replies differ in a detail (a stack entry, a quote,
the days in the office). No score moved. One cap did: offer 51 lost its
location hard stop and stayed at 1.0.

A and B agree to the last digit because they make the same calls in the same
order. This change altered what was loaded when a call arrived, and a 4B
answer at temperature 0 came out differently. More on that below, under "How
far a gate moves on its own".

### T5. Alias term vectors kept between runs (`6cf53f4`)

The skill-alias suggestions are rebuilt after every run and embedded all 806
terms each time. They are now kept by text.

| suggestions step | before | after |
|---|---|---|
| #45, no new term | 21.2 s | 0 s |
| #108, 2 new terms | 21.1 s | 12.6 s |
| #52, 1 new term | 20.9 s | 12.0 s |

`skill-suggestions.tsv` is byte for byte the same. The 12 s that remain are
the embedder loading on the GPU for one or two texts, which T2 removes.

### T2. The embedder on the CPU (`26d9e5a`)

Only one model fits in 6 GB of VRAM, so every embedding call evicted the 30B
and the next chat call loaded it back. The embedder now runs on the CPU, where
it loads beside the 30B in 1.2 s.

The first version sent small jobs to the CPU and large ones to the GPU. It
lost (section 5): the two devices return different vectors for the same text,
and comparing across them is worse than either alone. So it is one device for
everything, and the device is part of the fingerprint every vector cache is
keyed on.

Three offers, seconds. "Before" is T1, T3 and T5 on the GPU embedder.

| | before | after |
|---|---|---|
| #45, whole run | 250.4 | 214.5 |
| #108, whole run | 218.6 | 173.2 |
| #52, whole run | 253.8 | 207.5 |
| Phase 2 wall | 171, 127, 167 | 146, 106, 142 |
| Phase 3 wall | 64, 64, 63 | 53, 52, 52 |
| model loading in the run | 59 to 73 | 24 |

What it costs:

- The first run after the change rebuilds every vector cache on the CPU: the
  posting index, 453 postings in 499 s; the spike background, 4,037 texts in
  about 190 s; the alias terms, 807 in 22 s. Once.
- Editing a bullet in cv.md rebuilds the spike background, as before. That is
  now about 190 s on the CPU where it was 67 s on the GPU.

What it buys besides the time: the CPU returns the same vector for the same
text every time. The GPU does not, and that was the reason baseline A and B
selected different bullets for offer 323.

The vectors are not the GPU's, so stage 2 retrieves differently: the two
devices pick the same top 3 for 273 of 320 requirements. `goldset grade` at
three and at four candidates, GPU against CPU. Each CPU cell holds two runs,
with the gate's prompt cache warm and then cold (see "How far a gate moves on
its own" below).

| | GPU, 3 | CPU, 3 | GPU, 4 | CPU, 4 |
|---|---|---|---|---|
| ticked line shown to the grader | 50 | 51 | 57 | 53 |
| cited a ticked line | 35 | 36, 37 | 38 | 33, 34 |
| cited an unticked line | 26 | 20, 20 | 24 | 23, 22 |
| cited a line the sheet never had | 0 | 1, 1 | 0 | 3, 4 |
| false gap | 2 | 6, 5 | 1 | 4, 3 |
| precision of cited lines | 0.574 | 0.643, 0.649 | 0.613 | 0.589, 0.607 |
| blank requirements graded Gap | 9 | 9, 9 | 10 | 6, 6 |

At three candidates the CPU cites 6 fewer unticked lines and makes 3 or 4
more false gaps. At four it loses what made four worth having: the fourth
slot holds a ticked line for 2 more requirements where on the GPU it held 7.
On the CPU's vectors the fourth slot is more often a line the sheet never
offered (16 of 252 slots against 2 at three), because the sheet's candidates
were pooled from the GPU retriever.

`judge`: cosine 0.750 on both devices, the blend 0.846 on the GPU and 0.839
and 0.836 on the CPU, warm and cold. The judge alone reads no vectors, and it
moves as far between the two CPU runs (0.754, 0.745) as between the devices
(0.758).

### Spike background reuses the bullets' vectors (`3faeeab`)

`scoreBullets` embeds the 33 bullets, and the spike background embedded them
again whenever a report was missing from its cache, which after T1 is once
an offer. It now takes the vectors it was handed.

| Phase 3 embed job | before | after |
|---|---|---|
| #45 | 90 texts, 9.0 s | 57 texts, 6.3 s |
| #108 | 78 texts, 8.7 s | 45 texts, 5.6 s |
| #52 | 86 texts, 8.8 s | 53 texts, 5.7 s |

The rows are the same numbers. Two run homes rebuilt `cv-spike.json` from
nothing on the CPU, one with this change and one without. Of the reports both
hold with the same text, 404 of 404 have bit-identical rows.

### Q1. Stage 2 arms on `goldset grade`

63 ticked requirements, 25 blank. Baseline A and B give the first row twice.

| arm | ticked line shown | cited a ticked line | cited an unticked line | false gap | precision | blank graded Gap |
|---|---|---|---|---|---|---|
| baseline | 50 | 35 | 26 | 2 | 0.574 | 9 |
| candidates in full, cut at 400 | 50 | 34 | 26 | 3 | 0.567 | 7 |
| note before the pick | 50 | 35 | 26 | 2 | 0.574 | 8 |
| note between pick and grades | 50 | 35 | 26 | 2 | 0.574 | 9 |
| rank on the bare bullet | 53 | 35 | 22, and 3 the sheet never had | 3 | 0.614 | 8 |
| prompt rule: prefer a bullet to a Skills line | 50 | 37 | 23 | 3 | 0.617 | 6 |
| Skills or Education line last | 50 | 39 | 21 | 3 | 0.650 | 5 |
| 4 candidates | 57 | 38 | 24 | 1 | 0.613 | 10 |

How to read the table. A change of schema order moves 3 of the 88 rows. The
arms that change what the grader sees move 14 to 30, some rows for the better
and some for the worse, and the totals above are what is left over. So a
difference of two or three citations is inside what any substantive edit to
this call does, and the sheet cannot rank arms that close.

Four candidates is the one arm that is better on every column. It puts a
ticked line in front of the grader for 7 more requirements, which is
retrieval and not a matter of the model's mood, and it does not make the
grader more generous: 10 blank requirements are graded Gap against 9. Among
the ticked rows that moved, 8 went from wrong to right and 5 the other way.

Putting the Skills line last scores the highest precision and gives evidence
to 4 more of the blank requirements, the generosity the August top-6
experiment was reverted for. On top of four candidates it adds nothing (38
cited, 24 unticked, 1 false gap) and leaves only 4 blank requirements graded
Gap. Dropped, with the prompt rule that aims at the same misses.

Four candidates then ran through Phase 2 on the 18 rated offers, with stage 1
reused from baseline A so that only the grader's input differs.

| `labels`, 18 offers, 83 pairs | 3 candidates | 4 candidates |
|---|---|---|
| Phase 2 score, pair accuracy | 81.3% (67.5) | 81.3% (67.5) |
| Phase 2 score, rho | 0.546 | 0.548 |
| cv_blended, pair accuracy | 80.7% (67) | 81.9% (68) |
| cv_match, pair accuracy | 66.3% (55) | 70.5% (58.5) |
| coverage, pair accuracy | 75.9% (63) | 75.9% (63) |

Eleven of the 18 offers changed coverage and four changed score by 0.3 or
more: #32 from 3.1 to 3.4 and #39 from 4.4 to 4.8, both rated 4; #42 from
4.6 to 3.8, also rated 4; #75 from 3.1 to 3.5, rated 3. The ordering of the
18 comes out level. Kept (`31f7752`): better on its own gate, level on the
other, and free.

That was on the GPU's vectors. T2 came after it, and on the CPU's vectors the
sheet no longer prefers four (see the table under T2): three cites 36 or 37
ticked lines against 33 or 34, and four makes 2 fewer false gaps. Phase 2
then ran in full on the 18 rated offers at three and at four, both on the
CPU embedder:

| `labels`, 18 offers, 83 pairs | 3 candidates | 4 candidates |
|---|---|---|
| Phase 2 score, pair accuracy | 80.7% (67) | 81.9% (68) |
| Phase 2 score, rho | 0.540 | 0.559 |
| cv_blended, pair accuracy | 77.7% (64.5) | 84.3% (70) |
| coverage, pair accuracy | 74.7% (62) | 81.9% (68) |
| cv_match, pair accuracy | 70.5% (58.5) | 72.3% (60) |

Four stays. The sheet's 3 citations are inside its own churn, and it
cannot score the 3 or 4 lines four cites that its GPU-pooled candidates
never offered. The ratings separate coverage by 6 pairs, beyond the 2 or 3
that one wrong label explains.

### Q2. Phase 1 north_star from the archetype's tier (`2f7a82b`)

Phase 1 asks the 4B for `north_star`. Phase 2 looks it up from the
archetype's `fit` tier in profile.yml. Both policies can be computed from one
run, because the model's reply holds the archetype either way.

108 fresh Phase 1 scores: the 40-offer subset and the 68 other offers the
user applied to or skipped.

| | model's number | lookup |
|---|---|---|
| pairs ordered as the 18 ratings order them | 47 of 83 | 49 of 83 |
| offers Phase 2 scores 4 or more that pass the gate | 63 of 67 | 64 of 67 |
| offers Phase 2 scores under 3 that the gate stops | 5 of 18 | 5 of 18 |
| offers rated 4 that the gate stops | #34, #36, #39 | #36 |
| applied offers the gate stops | #325, #409 | #409 |
| skipped offers the gate stops | #237, #415 | #237, #415 |
| offers gated | 15 of 108 | 12 of 108 |

23 scores change. Twenty are AI/Platform roles, a secondary archetype, moving
from 5.0 to 4.3, from 4.4 to 3.6 or from 3.4 to 3.0, all still above the gate. Three cross
it, all upwards: #34 and #39, which the user rated 4, and #325, which the
user applied to and where the 4B gave a primary archetype a 1. No offer
crosses it downwards, so the cost is three more Phase 2 runs in 108.

### `gate ats --ceiling` reads unlabelled verdicts (`8478847`)

Asked about the whole of cv.md, gemma3 answers "met" and a quote on two bare
lines, with no labels, and the parser counted all 151 replies as unusable. A
first line that is exactly met, partly or unmet now parses. The 16 tailored
pages score as before, 0.814. What the ceiling then showed is in section 5.

### How far a gate moves on its own (`951295b`)

Baseline A and B agree to the digit, so section 2 reads as though the gates
had no noise. They repeat the same calls in the same order. T3 was the first
sign that this understates it, and T2 the plain one: with the embedder off
the GPU the 30B stays loaded between a gate's calls, keeps the previous
prompt and re-reads only what differs. At temperature 0 that changes some
answers. The judge graded 5 of its 12 postings differently from a
byte-identical prompt.

`SNIPE_GATE_COLD=1` sent a throwaway one-token call before each of a gate's
30B calls, meant to make each prompt read from the start. It did not do
that, and on 14 Nov the flag was changed to unload the model instead: see
"The same gate on another day". The table is from the first version, so its
columns are two call states inside one session and neither is a first read.
One binary and one set of caches:

| | warm | cold |
|---|---|---|
| `judge`, blend | 0.839 | 0.836 |
| `judge`, judge alone | 0.754 | 0.745 |
| `goldset grade`, cited a ticked line | 33 | 34 |
| `goldset grade`, false gap | 4 | 3 |
| `goldset grade`, precision | 0.589 | 0.607 |
| `tailor`, exp_starved | 1.562 | 1.438 |
| `tailor`, front_load | 0.311 | 0.301 |
| `ats`, must-have evidence | 0.779 | 0.817 |
| `ats`, preferred evidence | 0.826 | 0.795 |

`ats` itself is not cold. It grades the pages `tailor` wrote, and the cold
`tailor` chose different bullets. One judge posting moved 0.145 on its own.

This is the noise floor inside one session: about 0.01 on the judge blend,
one to two citations and false gaps on `goldset grade`, 0.04 on `ats`. A
rerun cannot show it, since a rerun repeats the same call states. Between
sessions the judge blend has moved by 0.026.
The flag is off by default, so numbers already in the ledgers stay
comparable.

### All together

Baseline against the branch's last commit. Where the last binary has two
numbers they are the warm and the cold run.

| gate | baseline | now |
|---|---|---|
| `labels`, Phase 2 score, pair accuracy | 81.3% (67.5 of 83) | 81.9% (68) |
| `labels`, Phase 2 score, rho | 0.546 | 0.559 |
| `labels`, cv_blended | 80.7% (67) | 84.3% (70) |
| `labels`, coverage | 75.9% (63) | 81.9% (68) |
| `labels`, cv_match | 66.3% (55) | 72.3% (60) |
| `labels`, Phase 1 score | 56.6% (47) | 62.7% (52) |
| Phase 1 against stored Phase 2, 40 offers | pair 0.645, recall_good 15 of 18, drop_bad 4 of 8 | pair 0.674, recall_good 16 of 18, drop_bad 4 of 8 |
| `goldset score`, ticked line among those shown | 50 of 63 (3 shown) | 53 of 63 (4 shown) |
| `goldset grade`, cited a ticked line | 35 | 33, 34 |
| `goldset grade`, cited an unticked line | 26 | 23, 22 |
| `goldset grade`, false gap | 2 | 4, 3 |
| `goldset grade`, precision | 0.574 | 0.589, 0.607 |
| `goldset grade`, blank graded Gap | 9 of 25 | 6, 6 |
| `judge`, blend | 0.847 | 0.839, 0.836 |
| `tailor`, one page | 16 of 16 | 16 of 16 |
| `tailor`, lines | 51.06 | 50.81 |
| `tailor`, exp_starved | 1.375 | 1.562, 1.438 |
| `tailor`, front_load | 0.307 | 0.311, 0.301 |
| `ats`, must-have evidence | 0.814, 0.831 (A, B) | 0.779, 0.817 |
| `ats`, preferred evidence | 0.856, 0.826 (A, B) | 0.826, 0.795 |
| `skills` | 99.0% | 99.0% |
| `typed` | 133 of 138 | 133 of 138 |
| time per offer, mean of 3 | 311 s like for like | 207 s |

Phase 1's 52 of 83: 49 come from the lookup (Q2). The other 3 come from a
changed numerical path. With the embedder off the GPU (T2), nothing evicts
the 4B between offers, so `p1.score` reuses the 7,765-token prompt prefix it
already holds: 0.3 s of load and 0.5 s of prompt where it was 3.8 and 2.8,
15.1 s a call down to 9.2 over the 40 offers. Three cv_match values moved
from 4 to 5 with it (offers 37, 108 and 215).

Against the ratings, coverage and cv_match each order 5 more pairs and
cv_blended 3 more. Phase 1 orders 5 more. The Phase 2 score itself is level,
half a pair up. The Phase 3 gates are level within the warm-against-cold
spread above. The evidence sheet reads slightly worse: one or two fewer ticked
citations, one or two more false gaps, and 3 more blank requirements given
evidence. The reasons are under T2 and Q1.

### Where the time goes now

The same three offers on the branch's last binary, seconds, load in
brackets.

| step | #45 | #108 | #52 |
|---|---|---|---|
| `p1.score` | 15.8 (5.2) | 14.4 (4.8) | 12.8 (5.0) |
| embed the posting | 3.1 (1.7) | 2.3 (1.2) | 2.6 (1.2) |
| `p2.jdparse` | 51.5 (7.6) | 35.1 (7.4) | 50.7 (7.6) |
| embed the requirements | 2.1 (1.4) | 1.5 (1.2) | 1.6 (1.2) |
| `p2.evidence` | 36.3 (0.2) | 17.7 (0.2) | 32.6 (0.2) |
| `p2.judgment` | 71.5 (9.2) | 56.3 (8.7) | 57.7 (9.3) |
| Phase 3 embeds | 6.3 | 5.6 | 5.8 |
| `p3.judge` | 44.8 (0.2) | 43.3 (0.2) | 45.7 (0.2) |
| alias suggestions | 0.7 | 0.6 | 0.8 |
| Phase 1 wall | 15.9 | 14.4 | 12.8 |
| Phase 2 wall | 164.7 | 113.2 | 145.4 |
| Phase 3 wall | 51.6 | 49.4 | 51.9 |
| whole run | 232.9 | 177.6 | 210.9 |
| baseline | 358.8 | 280.1 | 317.5 |

The baseline #45 includes `p1.cvstack`, 24 s once per ledger, which on this
run fell to the offer before. Like for like the mean is 311 s before and
207 s after, 5.2 minutes to 3.5.

Model loading is down from 86 to 98 s a run to about 26: the 4B for
`p1.score`, the 30B back after it, and the 30B again for `p2.judgment`'s
larger window. The 30B's four calls take 152 to 204 s of the run, loads
included, and `p2.judgment` is the largest of them.

### Second pass, 14 November

Section 6 listed six things to try, and this pass ran them. Two are on the
branch and described next. Two were measured and dropped, and one passed its
gates and was left out: section 5 has the three. The last, the evidence
sheet, needs four lines ticked by hand and is what section 6 now holds.

The measuring turned up two faults, and both fixes are on the branch. The
gates' cold flag did not make a call cold, and Phase 2 scored an offer by
whether Phase 1 had run before it. They follow the two changes.

Both arms of every comparison below ran in the same session. "The same gate
on another day" says why.

### A Languages row when the posting names one (`9fdd6d2`)

cv.md keeps English, Ukrainian and Russian under their own heading, outside
Skills, so `SelectSkills` never saw them and no tailored page said English.
It now adds the row when the posting names one of those languages as a whole
word. 28 of the 452 cached postings do, most of them as a must-have. The
other 94% keep the line for a bullet.

`gate tailor`, cold, on 38 reports: the usual 16 and the 22 other evaluated
postings that name English. The head binary against this one, then
`gate ats` on both sets of pages.

| | before | after |
|---|---|---|
| pages with the row | 0 | 24 of 38 |
| rest of the page changed | | 0 of 38 |
| one page | 38 of 38 | 38 of 38 |
| lines | 50.9 | 51.5 |
| `ats`, must-have evidence | 0.850 | 0.866 |
| the 19 English must-haves: unmet, partly, met | 9, 4, 6 | 0, 4, 15 |

Paired by offer, 9 went up, 3 went down and 25 tied. The 3 that went down
lost other qualifications, not English: gemma3 re-reads a page that differs
by one line and changes 15 of the 346 other verdicts, 5 up and 10 down.

Of the usual 16 pages, 2 gain the row, reports 004 and 056, so their mean
goes from 50.81 lines to 50.94.

The match is on the word, so a posting that only says its policies are
available in English prints the row too. The row is headed "Human
Languages", the name the template parser already had for it.

### The spike background keeps the requirement vectors (`017d516`)

The spike background is keyed on the bullets. Editing one bullet in cv.md
threw every row away, and the next run embedded the requirements of every
past report again, though none of them had changed: 67 s on the GPU, about
190 s since T2 moved the embedder to the CPU.

A rebuild now goes through `ledger.CachedEmbed`, the text-to-vector file T5
uses for alias terms, in `batch/cache/spike-embed.json`. The next rebuild
embeds only the reports written since. A run that adds one report does not
open the file, which is 43 MB to read for about ten texts.

Offer 45, Phase 3 alone, 404 reports, the cache key broken between runs to
force a rebuild:

| | texts embedded | embedding | Phase 3 wall |
|---|---|---|---|
| nothing kept | 3,441 | 186.6 s | 231 s |
| vectors kept | 44 | 3.8 s | 32 s |
| vectors kept, again | 44 | 3.9 s | 32 s |

The 44 are the offer's own requirements and the 33 bullets. All 404 rows
are bit-identical across the three runs.

### The same gate on another day (`7d14676`)

The warm-against-cold spread is the noise inside one session. Between
sessions it is larger. `gate judge` on the same judge code, the same home
copy and the same Ollama, 0.31.1, three days and several restarts apart:

| | 11 Nov | 14 Nov |
|---|---|---|
| blend, warm | 0.839 | 0.858 |
| blend, cold | 0.836 | 0.862 |
| judge alone, cold | 0.745 | 0.784 |
| cosine | 0.750 | 0.750 |

The cosine arm is equal to the digit, so the vectors and the candidates are
the same and the 30B alone answered differently, on 6 of the 12 postings.
Within 14 Nov, warm and cold differ on one posting.

Ollama's log has the cause. The server keeps finished prompts in memory, up
to 8 GB of them, and gives each new call the stored prompt that shares its
longest prefix. The judge's calls share the system prompt and two
exemplars, about 4,100 of 6,000 tokens. So only the first judge call of a
session read that prefix. Every later call restored it and read the 1,700
to 1,850 tokens of its own posting. The throwaway call did not stop this:
the log shows "found better prompt with f_keep = 0.63" before each real call
on both days.

Warm and cold in one session therefore reuse one prefix state and agree, and
two sessions do not, because the prefix was first read in different
company. On 11 Nov it was read after a model load. On 14 Nov it was read
straight after a Phase 2 judgment in the same window, into a slot that kept
that call's first six tokens.

`SNIPE_GATE_COLD=1` now unloads the model before each call, with the request
Ollama documents for it: no messages and `keep_alive` 0. In the log every
call then reads its whole prompt, 5,821 to 6,001 tokens, and nothing is
restored. Two runs of `gate judge`, the first after Phase 2 calls and the
second after the first:

| | run 1 | run 2 |
|---|---|---|
| blend | 0.848 | 0.848 |
| judge alone | 0.758 | 0.758 |
| postings graded differently | | 0 of 12 |

It costs a model load per call. The judge gate takes 615 s where it took
394. Whether the number survives a restart is not tested yet: both runs were
on 14 Nov.

`goldset grade` under the new flag, one run, against the old flag's cold run
of 11 Nov:

| | old flag | new flag |
|---|---|---|
| ticked line shown to the grader | 53 of 63 | 53 |
| cited a ticked line | 34 | 33 |
| cited an unticked line | 22 | 23 |
| cited a line the sheet never had | 4 | 3 |
| false gap | 3 | 4 |
| precision of cited lines | 0.607 | 0.589 |
| blank requirements graded Gap | 6 of 25 | 6 |
| time | 262 s | 405 s |

The totals are a citation apart, and 8 of the 88 requirements get another
grade. The right-hand column is the one to pair a stage 2 change against.

`gate tailor` under the new flag, twice, on the usual 16 reports, then
`gate ats` on the pages:

| | run 1 | run 2 |
|---|---|---|
| one page | 16 of 16 | 16 of 16 |
| lines | 51.19 | 51.19 |
| exp_starved | 1.375 | 1.375 |
| front_load | 0.313 | 0.313 |
| keep_rate | 0.958 | 0.958 |
| pages that differ | | 0 of 16 |
| time | 855 s | 862 s |
| `ats`, must-have evidence | 0.808 | |
| `ats`, preferred evidence | 0.856 | |

The old flag's cold run took 522 s.

The five judge blends on record for this code are 0.836, 0.839, 0.848, 0.858
and 0.862. A difference under 0.03 on that gate is not a result.

Phase 2 through `snipe run` did not have the problem. Each offer is a new
process and the same sequence of loads, so its calls meet the same states
every time. Between 11 and 14 Nov, stage 1 and stage 2 wrote identical
output on all 18 rated offers, stage 3 on the 14 whose earlier checkpoints
were kept, and `labels` orders 68 of 83 pairs on both days.

### Phase 2 asks for the CV's language list (`c5c8bc0`)

One offer scored differently between the two days for a reason that is not
the model. Offer 42 was 4.2 on 11 Nov and is 3.8 on 14 Nov, on the same three
stage outputs.

The stack cap compares the languages a posting wants with the languages the
CV has work in. The second list comes from one 4B call in Phase 1, cached
under a key of the ledger's entities. Phase 2 only read that cache. A home
where Phase 1 has not run since the entities last changed has no list, and
the cap fell back to the word lists without saying so. Offer 42 wants Go:
the word lists cap it at 3 and the extracted facts do not.

The live home has no list today and holds offer 42 at 3.8. So did the copy
every 14 Nov arm ran on, which keeps those arms paired like for like.

Phase 2 now asks for the list before its first 30B call. Offers 42 and 86
through `snipe run --skip-phase1` on a copy with no list:

| | before | after |
|---|---|---|
| offer 42, score | 3.8 | 4.2 |
| offer 42, cv_blended | 3 | 3.73 |
| offer 86, score | 3.4 | 3.4 |
| `p1.cvstack` calls | 0 | 1, on offer 42, 25.6 s |
| Phase 2 wall, offer 42 | 138.6 s | 157.5 s |
| Phase 2 wall, offer 86 | 125.4 s | 124.7 s |

Every other field of the two eval files is equal. The call is made once per
ledger and read from the cache after that.

Of the 391 evaluations in the home, 4 record a cap the two sources disagree
on. The word lists cap offers 42, 414 and 439 at 3 where the facts do not,
and the facts cap offer 401 for Kotlin where the word lists do not.

## 5. Negatives

Measured and dropped. None of these should come back without a new reason.

| change | gate | result | why it lost |
|---|---|---|---|
| Show the grader each candidate in full, cut at 400 characters not 200 | `goldset grade` | cited a ticked line 35 -> 34, false gap 2 -> 3, blank graded Gap 9 -> 7 | Nothing gained, and the grader finds evidence for two more requirements that have none |
| Grader writes its note before the pick | `goldset grade` | every column equal, blank graded Gap 9 -> 8 | No effect. The note is not where the decision is made |
| Grader writes its note between the pick and the two grades | `goldset grade` | every column equal | No effect |
| Rank candidates on the bullet without its "Project name:" prefix | `goldset grade` | ticked line shown 50 -> 53, cited a ticked line 35 -> 35, false gap 2 -> 3 | Three more ticked lines reach the grader and it cites none of them. 3 citations land on lines the sheet never offered, so they cannot be scored either way |
| Prompt rule: prefer a bullet to a Skills line | `goldset grade` | cited a ticked line 35 -> 37, false gap 2 -> 3, blank graded Gap 9 -> 6 | More generous on requirements with no evidence |
| Skills or Education line always last | `goldset grade` | cited a ticked line 35 -> 39, precision 0.574 -> 0.650, false gap 2 -> 3, blank graded Gap 9 -> 5 | Best precision of any arm and the most generous. Adds nothing on top of four candidates |
| Instruction prefix on the requirement before embedding it | embeddings only | hit@3 47 to 51 of 63 against 50 | Not decidable. 53 to 92 of the 189 slots hold lines the sheet never offered. The sheet was pooled from the current retriever, so it cannot score a retriever that looks elsewhere |
| Embedder on the CPU for small jobs and the GPU for large ones | `goldset score`, `goldset grade`, `judge` | ticked line shown 50 -> 48, cited a ticked line 35 -> 33, false gap 2 -> 5, blend 0.847 -> 0.840 | The CPU and the GPU return different vectors for one text: the same top 3 for 273 of 320 requirements. Queries from one device against an index from the other are worse than either alone (48 shown, against 50 all-GPU and 51 all-CPU) |
| Stage 1 without `kind`, `terms` and `quote` | `labels`, a fresh Phase 2 on the 18 rated offers | Phase 2 score 68 -> 62 of 83 pairs, rho 0.559 -> 0.431; Phase 2 132 -> 118 s an offer | Nothing shipped reads the three fields, but the model writes its requirements differently without them: 128 must-haves where there were 138, and stage 2 then grades 54 requirements Strong where it graded 70 and calls the tooling different on 68 rows where it did on 43 |
| Stage 1 without `quote` alone | the same | Phase 2 score 68 -> 65 pairs, rho 0.559 -> 0.499; Phase 2 132 -> 128 s | `quote` is 23% of what stage 1 writes and the other two fields 6%, so this was the cheap one to lose. It costs 3 pairs for 4 s: Strong 70 -> 62 |
| Bonus in Phase 3 for a bullet Phase 2 cited (Q3) | `judge`, with a fresh Phase 2 on its 12 postings | blend 0.858 -> 0.860 and 0.862 -> 0.864 on the two runs of 14 Nov at a bonus of 0.05, nothing or a loss from 0.10 up | Inside the noise. Phase 2 cites 50 bullets and 26 of them are ticked, 52% against 24% of all bullets, so the bonus lifts 24 unticked bullets with them. Strong citations only, or must-haves only, do the same |

One thing passed its gates and is left out.

One window for all three Phase 2 stages (T4) removes the 8 s reload before
`p2.judgment` and costs nothing else: the 30B writes 25 tokens a second in
either window. That is the saving on an offer run whole, where Phase 1's 4B
evicts the 30B in between. On the 18 rated offers run one after another the
30B stays loaded from offer to offer and both of Phase 2's loads go, 15 s.
The wall falls by 10 s, because the three stages write 2,350 tokens an offer
where they wrote 2,210.

| | two windows | one window |
|---|---|---|
| `labels`, Phase 2 score | 68 of 83 pairs, rho 0.559 | 71.5, rho 0.635 |
| `labels`, cv_blended | 68.5 | 69.5 |
| `labels`, coverage | 68 | 70 |
| `labels`, cv_match | 60 | 63.5 |
| requirements, must-haves among them | 160, 138 | 163, 147 |
| graded Strong, Transferable, Gap | 70, 68, 22 | 63, 67, 33 |
| Phase 2 wall, mean of the 18 | 132.2 s | 121.9 s |
| `goldset grade`, cited a ticked line | 33 of 63 | 33 |
| `goldset grade`, false gap | 4 | 1 |
| `goldset grade`, cited a line the sheet never had | 3 | 7 |
| `goldset grade`, blank graded Gap | 6 of 25 | 7 |

The `goldset grade` rows are stage 2 alone, cold, one run each: the same
requirements in an 8192 window and a 12288 one. The window changes what the
model writes with nothing else different. 17 of the 88 requirements get
another grade and 12 another line. The count of ticked lines cited does not
move. Three false gaps become citations of unticked lines, and four other
citations move to lines the sheet never offered, so they cannot be scored.

It is not worse, and 3.5 pairs is at the edge of what 18 ratings can tell
apart. It stays out for what it does to the measuring. Each change of window
restarts the server's runner, which is why Phase 2 gives the same answer on
any day: every offer starts from nothing. With one window the runner lives
on, and the server hands each call the prefix an earlier offer left. On this
run `p2.jdparse` read its prompt in 2.3 s where it took 3.8. An offer's score
would then depend on the offers before it, and `labels` would stop being the
one gate that repeats. That is a poor trade for 4 to 8% of a run. If the
speed is wanted more, it is `NumCtx: 12288` on one line of
`cmd/snipe/cmds.go`.

### The ATS ceiling is not a measurement

`gate ats --ceiling` judges the whole of cv.md against each posting, to
separate must-haves the page lost from must-haves the CV never had. It
reports 92 of 114 met on the page, 17 met only by cv.md and 5 by neither,
which reads as 15% lost to selection. It is not. On the long CV gemma3 4B
answers "met" and quotes a line that has nothing to do with the
qualification: for "English fluency", "Experience with PostgreSQL" and
"Effective communication skills in Chinese and English" it quotes the same
Rust remote-desktop bullet. The quote is in the CV, so the pointer check
passes it. The page score has the opposite fault in at least one place:
offer 056's page lists Postgres and names PostgreSQL in a project header,
and the judge calls "Experience with PostgreSQL database" unmet.

So `must_have_evidence` is usable as a paired comparison between two pages
and as nothing else.

One thing the listing did show. The tailored page had no Languages section,
so a posting whose must-have is English fluency got no evidence for it,
though cv.md says C1 certified. `9fdd6d2` prints the row (section 4).

## 6. What to try next

The six things this section listed on 11 November are done. Section 4's
second pass has the two that are on the branch, and section 5 the three
that are not.

What is left is to re-tick the evidence sheet for the CPU retriever. Its
candidates were pooled from the GPU's top 3, so on CPU vectors
`goldset grade` cites lines the sheet never offered and cannot score them.
The two cold runs at the shipped window cite four between them:

| report | requirement | line cited |
|---|---|---|
| 320, R4 | Experience with cloud, APIs, microservices, or distributed systems | Distributed Store Management Platform: Built 10 Spring Boot microservices... |
| 329, R4 | Strong problem-solving skills to break down complex technical or logistical problems | Software Engineer: Built the React and Next.js admin console... |
| 327, R2 | Fresh graduate or 1+ years of professional Java development experience | the summary line that starts "Built a 10-service event-driven platform in Java" |
| 326, R3 | Skills in Object-Oriented Design, Design Patterns, Architecture, and Application Integration | the header line of the Distributed Store Management Platform project |

Each needs a yes or a no by the sheet's own rule: would the work on that
line let you answer the requirement in an interview? With the four on the
sheet, ticked or blank, `goldset grade` is exact for those runs. Another
binary can cite other lines, so read its count of "unlabelled line" before
its grade.

Two smaller things are open.

The unloading cold flag has run on one day. Run `gate judge` with it after
`sudo systemctl restart ollama`. 0.848 again makes that the judge's
baseline, and any other number means the server keeps more between calls
than its prompt cache.

One window for Phase 2 is a choice between 8 s an offer and a `labels` gate
that repeats. Section 5 has both sides.

For anything new, run both arms in one session and count only a difference
larger than the gate's own spread, which on the judge blend is 0.03 between
sessions. `SNIPE_GATE_COLD=1` makes every 30B call read its whole prompt, at
a model load a call, and is the setting to quote a number from.
