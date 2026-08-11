# Tailoring quality campaign — execution plan

**Budget:** 8 hours wall clock, planning + execution + documentation.
**Constraint:** RTX 3060 6 GB + 30 GB RAM, ≤50 GB of Ollama models on disk,
≤5 min per JD end-to-end (Phase 1 → tailored PDF).
**Status:** executed 2026-08-03. Results in `PHASE3-GENERATION-LEDGER.md`.
Shipped: F1-F4, L2 (partial), G2, G3, G4, T2. Measured and not shipped: G1, L3.
Dropped after measurement made them pointless: R1, R3, R4, R5, R6, M3.

---

## 0. The measured starting position

Everything in this section was measured today, not assumed. It is the reason the
plan is ordered the way it is.

### 0.1 Hardware cost model

| Model | Role | Cold load (disk) | Reload (page cache) | Prefill | Generate |
|---|---|---|---|---|---|
| `snipe-eval` (30B-A3B q4) | Phase 2, judge | **40.0 s** | **9.0 s** | 428 tok/s | **25.6 tok/s** |
| `snipe-cv` (7B-Coder q5) | Phase 3 writer | 7.1 s | ~2 s | 2053 tok/s | 55.2 tok/s |
| `snipe-screen` (4B q8) | Phase 1 | 7.0 s | ~2 s | — | — |
| `snipe-embed` (0.6B q8) | retrieval | 3.5 s | — | — | — |

Three facts follow, and they drive most of the plan:

1. **Only one model stays resident.** 6 GB VRAM. Loading `snipe-cv` evicts
   `snipe-eval` — verified: a 30B call immediately after a 7B call paid 9.0 s.
   `snipe-embed` alone claims 2.15 GB. The pipeline's model order therefore has
   a real, uncounted cost.
2. **On the 30B, generated tokens cost 16.7× prefill tokens** (25.6 vs 428
   tok/s). A 1000-token JD costs 2.3 s to read and 39 s to echo. Every
   optimisation that shortens 30B *output* is worth ~16 prompt-side ones.
3. **The 40 s cold load is a page-cache artefact, not a disk-speed one.** With
   30 GB RAM and a 18.5 GB model, anything else that touches memory evicts it.
   Keeping the 30B hot is a scheduling problem, not a hardware limit.

**Not yet measured, needed in the first 20 minutes:** the actual end-to-end
wall clock per JD. There is no timing instrumentation anywhere in
`local-runner.sh` or the phase scripts. The "~5 min" figure is an estimate, and
the whole latency budget below is unfalsifiable until it is a number. This is
task **F2**.

### 0.2 Disk budget — currently 41 % over

`ollama list` shows 70.6 GB of unique weights against a 50 GB ceiling.
Deduplicated (derived models share their base's blobs):

| Keep | GB | Why |
|---|---|---|
| `qwen3:30b-a3b-instruct-2507-q4_K_M` → `snipe-eval` | 18.56 | Phase 2 + judge |
| `qwen2.5-coder:7b-instruct-q5_K_M` → `snipe-cv` | 5.44 | Phase 3 writer (**under review, G1**) |
| `qwen3:4b-instruct-2507-q8_0` → `snipe-screen` | 4.28 | Phase 1 |
| `qwen3-embedding:0.6b-q8_0` → `snipe-embed` | 0.64 | retrieval |
| | **28.92** | |

| Remove | GB | Why |
|---|---|---|
| `qwen3-coder:30b-a3b-q4_K_M` + `qwen3-coder-32k` | 18.56 | one blob, unused by the pipeline |
| `gemma2:9b` | 5.44 | unused |
| `qwen2.5:7b-instruct-q5_K_M` | 5.44 | unused |
| `qwen2.5:7b` | 4.68 | unused |
| `qwen2.5:7b-instruct-q4_0` | 4.43 | unused |
| `qwen3-embedding:4b` | 2.50 | **measured worse** than the 0.6B (0.715 vs 0.756 pair accuracy) |
| | **41.05** | |

After cleanup: **28.9 GB used, 21.1 GB free** under the ceiling. That headroom
is the pull budget for G1 (writer), R2 (cross-encoder) and M4 (oracle) — and
they cannot all have it. Priority if contended: **G1 > M4 > R2**.

There is also a stale `~/.ollama/models` (5 GB, one orphan blob and a 4.3 GB
`-partial`) left over from a user-scoped install; the live store is the
`ollama` service user's. Delete the orphan, it is not in the accounting above.

### 0.3 Quality baseline

Retrieval (gold set, 10 evaluable offers, pair accuracy — chance 0.5):

```
shipped: cos + 0.10 × judge_grade    0.851
         cosine alone                0.756
         (pre-fix, entry-prefixed)   0.689
```

Generation (`batch/tailor-harness.mjs`, label-free, n=24 fixed sample):

```
role_retention 1.0    metric_fab 0    grounding 0.90    example_copy_pct 0.292
```

Summary, measured ad-hoc over all 133 shipped CVs — **not yet in any harness**:

```
mean summary_jd_fit 0.549    mean summary_cv_fit 0.586    product_fab 9/133 = 7%
```

The 7 % is 9 CVs naming a product absent from `cv.md`: gcp ×3, kotlin, golang,
dbt, terraform, azure, angular.

### 0.4 Where the headroom actually is

The power analysis in `PHASE3-RETRIEVAL-LEDGER.md` is the single most important
input to this plan:

> +0.05 pair accuracy needs 50 labelled offers to detect. +0.03 needs 137.
> +0.02 needs 308. We have 10.

**Retrieval tuning is out of measurement resolution, not out of ideas.** The
shipped +0.095 was detectable only because it was unusually large. Every
remaining retrieval candidate is in the 0.02–0.05 band, i.e. invisible at n=10.
Spending the 8 hours on retrieval variants would produce numbers that cannot be
distinguished from label noise — which is not even estimated yet.

Generation is the opposite. Its metrics are **label-free and computable on 133
artifacts already on disk**. A change can be measured to convergence without
spending any of the user's labelling time. `example_copy_pct 0.292` and
`product_fab 7 %` are large, unambiguous defects with obvious causes.

So the plan spends its hours roughly: **45 % generation, 20 % latency (which
buys budget for generation), 15 % retrieval, 20 % measurement infrastructure.**

---

## 1. Already disproven — do not re-run

From `PHASE3-RETRIEVAL-LEDGER.md`. Listed so the catalogue below is not
re-litigating settled results.

| Approach | Result | Verdict |
|---|---|---|
| CSLS / hubness correction | 0.686 vs 0.756 | Hubs here are the genuinely good bullets |
| Qwen3-Embedding instruct prefix | 0.633 (0.6B), 0.673 (4B) | Worse on both |
| HyDE | 0.651 | Worse |
| 4B embedder | 0.715 | Quantisation, not the Modelfile — raw base scores identically |
| 30B-A3B as *label oracle* | 0.693 / 0.670 / 0.739 | Failed the pre-registered ≥0.764 bar |
| JD sentences as query source | 0.757 ≈ base | Block B is not lossy |
| 4 exemplars vs 2 | ≈ equal | Not the lever |
| Judge weight sweep | plateau 0.10–0.50 | 0.10 is fine, not a tuning target |
| 0-shot judge | 0.670 | **Worse than no rerank.** Few-shot framing is load-bearing |

That last row is a live constraint on L1 below: any change that moves the judge
must carry its exemplars, or it will silently degrade below cosine-alone.

---

## 2. Approach catalogue

Each entry: hypothesis → method → cost → gate. **Tier** is MUST / SHOULD /
STRETCH; STRETCH items are the first dropped when the clock runs out.

### 2.A Foundation (measurement + hygiene)

Nothing else in this plan is falsifiable without these. They come first.

**F1 — Disk cleanup. MUST. 10 min.**
Remove the 41.05 GB in §0.2. Delete the orphan `~/.ollama` blobs. Compliance is
a precondition for pulling anything.
*Gate:* `ollama list` unique total ≤ 50 GB; `node test-all.mjs` still green.

**F2 — Timing instrumentation. MUST. 20 min.**
No phase records its own wall clock. Add per-phase and per-call timing to
`local-runner.sh` and the three phase scripts, written to the state row or a
sidecar TSV. Without it the 5-minute constraint cannot be enforced and every
latency claim below is a guess.
*Gate:* one real end-to-end run emits a per-phase breakdown.

**F3 — The four missing metrics. MUST. 45 min.**
`ats_coverage`, `selection_regret`, `summary_jd_fit`/`summary_cv_fit`,
`product_fab` into `batch/tailor-harness.mjs`. Two exist as throwaway `node -e`
scripts and must become real, tested metrics. All label-free, all checked
against source text with no model in the loop — the property that made the
existing four trustworthy.
*Gate:* re-running the harness on the fixed sample reproduces the §0.3 numbers.

**F4 — Re-baseline. MUST. 25 min (mostly unattended).**
Run the current code against the fixed sample at `--temperature 0` and record
all eight metrics. Production runs at 0.15 (`local-pdf-offer.mjs:51`); benchmarks
already override to 0, where this stack is byte-identical. Every later A/B is
paired against *this* run, not against history.
*Gate:* a committed baseline file.

### 2.B Latency — buys the budget for everything else

**L1 — Fold bullet grading into Phase 2. SHOULD. 40 min.**
*Hypothesis:* the Phase 3 judge call (44 s, one 30B load + prefill) is
redundant. Phase 2 already has the 30B resident and the JD parsed. Adding 14
integers to its judgment schema costs ~30 output tokens ≈ 1.2 s.
*Method:* extend the staged evaluator's judgment schema with `bullet_grades`;
carry the two `judge-shots.json` exemplars into that prompt; persist grades to
`batch/evals/<id>.json`; `cv-select` reads them instead of calling.
*Cost:* −43 s per JD.
*Risk:* **high.** The 0-shot result (0.670, worse than no rerank) proves the
few-shot framing is load-bearing. Embedding the grading task inside a larger
prompt may degrade it the same way.
*Gate:* pair accuracy on the gold set within noise of 0.851. **If it drops
below 0.80, revert and keep the separate call** — 43 s is not worth 0.05.

**L2 — Model residency scheduling. MUST. 25 min.**
*Hypothesis:* the phase order thrashes the single resident slot. Current
sequence is 4B → embed → 30B → embed → 30B → 7B; each embed call between 30B
calls evicts an 18.5 GB model for a 0.6 GB one.
*Method:* batch all embedding work into one block before the 30B block; set
`keep_alive` explicitly rather than relying on the 5-minute default; order
Phase 3 so the 7B loads only after the last 30B call.
*Cost:* saves 9–40 s per avoided reload. Pure code, no quality effect.
*Gate:* F2's breakdown shows fewer load events. Quality metrics unchanged
(they must be — this changes no prompt).

**L3 — 30B output-token audit. SHOULD. 20 min.**
*Hypothesis:* `num_predict: 5120` on the Phase 2 judgment call. At 25.6 tok/s a
2000-token report costs 78 s. Some of that is schema verbosity, not content.
*Method:* log actual `eval_count` per call across the sample; trim the schema's
most verbose fields; move anything assemblable in code out of the model's mouth
(the staged evaluator's whole premise).
*Gate:* wall clock down, report content unchanged on a diff.

**L4 — Prompt-prefix ordering. STRETCH. 15 min.**
Put the static system prompt + `cv.md` first and the variable JD last, so the
three staged calls share a cached prefix. Saves prefill only (~2–6 s).
*Gate:* measurable in F2's breakdown, else revert as noise.

**L5 — Parallel embed + generate. REJECTED.**
Two models cannot be co-resident in 6 GB without thrashing. The hardware
forecloses it.

### 2.C Generation — the largest expected gain

**G1 — Replace the writer model. MUST. 60 min.**
*Hypothesis:* **`snipe-cv` is Qwen2.5-Coder-7B — a code-tuned model writing
English CV prose.** This is a tool mismatch, and it is the most likely single
cause of the generation defects (`example_copy_pct 0.292`, generic summaries).
*Method:* A/B the writer on the fixed sample, all eight metrics, paired per
offer:
  - (a) `snipe-eval` 30B-A3B — already on disk, **already resident from Phase 2**
    (so L2 makes it nearly free on load), ~50 s for a 1300-token generation
  - (b) `qwen3:14b` q4 ≈ 9 GB — fits the headroom, ~20 tok/s expected
  - (c) `mistral-small:24b` q4 ≈ 14 GB — strong writer, slower
  - (d) current 7B-Coder as control
*Cost:* (a) is +40 s generation but −7 s load; (b)/(c) cost a pull.
*Gate:* `grounding` up or flat, `example_copy_pct` down, `metric_fab` stays 0,
`role_retention` stays 1.0, end-to-end still under 5 min. **Try (a) first — it
costs no disk and no pull.**

**G2 — Drop the worked example. MUST. 20 min.**
*Hypothesis:* `example_copy_pct 0.292` — 29 % of offers plagiarise an 8-gram
from the prompt's own worked example. The example exists to convey JSON shape,
but `TAILOR_SCHEMA` already guarantees shape via constrained decoding. The
example is therefore pure downside.
*Method:* delete it; rely on the schema.
*Cost:* one harness run.
*Gate:* `example_copy_pct` → near 0 with no drop in `grounding` or
`role_retention`. This is the laziest high-value change in the plan.

**G3 — Fix the Tier-4 repair path. MUST. 15 min.**
*Hypothesis:* `local-pdf-offer.mjs:463` builds its rewrite prompt from the JD
and the profile narrative **and never sees the selected CV bullets**:
```js
const user = `Target role (keyword context):\n${(jd||'').slice(0,1000)}
              \n\nCandidate highlights:\n${narrative||'(none)'}...`;
```
It is structurally guaranteed to pull the summary toward the posting. This is
the parroting failure mode, built into the repair path.
*Method:* pass the selected bullets; make them the primary evidence and the JD
secondary.
*Gate:* `summary_cv_fit` up from 0.586, `summary_jd_fit` not collapsing.

**G4 — Summary as its own stage. MUST. 45 min.**
*Hypothesis:* the summary is one field in a large JSON blob competing with five
other sections for the model's attention. As its own small call fed by the
*selected* evidence, it describes what the CV actually shows.
*Method:* separate call after selection; input = selected bullets + projects +
the JD's top requirements; output = one 50–70 word string.
*Cost:* +1 call. On the 7B ~3 s; on the 30B ~15 s.
*Gate:* the strategy doc's own gate — **both** alignment numbers up, nothing
else regresses.

**G5 — Best-of-N summary with a code-side selector. SHOULD. 40 min.**
*Hypothesis:* a summary is short enough to sample repeatedly and pick by
measurement, converting a generation problem into a selection problem — which
is code, and therefore reliable.
*Method:* N=4 at temperature 0.7, score each with
`0.5·cv_fit + 0.3·jd_fit − product_fab_penalty − length_penalty`, keep the best.
*Cost:* 4 × 70 tokens ≈ 5 s on the 7B, ~11 s on the 30B.
*Gate:* both alignment numbers above G4's. **Reproducibility caveat:** temp 0.7
breaks the byte-identical property, so this variant must be A/B'd with a fixed
seed or over repeated runs — the noise floor is no longer 0.

**G6 — Per-section generation. STRETCH. 45 min.**
Split the one large JSON call into focused per-section calls. Smaller prompts,
less cross-section interference, no worked example needed anywhere.
*Cost:* more calls, each cheap on the 7B; likely net-neutral latency.
*Gate:* `example_copy_pct` and `grounding` both better than G2 alone. Only
worth it if G2 leaves residual copying.

**G7 — Provenance-constrained rewriting. SHOULD. 30 min.**
Every output bullet must exceed a cosine threshold against its source atom;
regenerate the ones that fail. Extends the existing `grounding` metric from a
measurement into an enforcement.
*Gate:* `grounding` up from 0.90; no drop in `ats_coverage`.

**G8 — Few-shot from best historical CVs. STRETCH.**
Only if G2/G6 leave a shape problem. Reintroduces copying risk by construction —
that is what G2 is removing. Low priority.

### 2.D Retrieval — capped effort, because it cannot be measured

Note the corpus is **14 atoms** (9 experience bullets + 5 projects). This is a
tiny ranking problem; sophisticated IR machinery has little room to work, which
is consistent with everything in §1 having failed.

**R1 — doc2query atom expansion. SHOULD. 40 min.**
*Hypothesis:* the strongest untried retrieval idea. Vocabulary mismatch is the
residual error — `cv.md` says "lock-free pre-allocated video frame ring", the JD
says "low-latency C++". Expanding each atom offline with the job-posting phrasings
it would satisfy closes that gap directly.
*Method:* for each of 14 atoms, generate ~10 requirement-style phrases **once**,
cache keyed by the `cv.md` hash (the invalidation mechanism already exists),
embed the expansion, score against both original and expansion.
*Cost:* **zero at runtime.** One offline 30B pass over 14 atoms.
*Gate:* pair accuracy up on the gold set. Given the resolution limit, treat
anything under +0.05 as unproven and ship only if it is also non-negative on
the second gold set (M2).

**R2 — Cross-encoder reranker. SHOULD (feasibility-gated). 45 min.**
*Hypothesis:* `bge-reranker-v2-m3` (~1.2 GB) is purpose-built for query-document
relevance and should beat bi-encoder cosine outright — at ~1 s for 14×8 pairs
versus the 30B judge's 44 s.
*Method:* **check servability first** — Ollama's reranking support is limited
and this may need llama.cpp directly. Timebox the feasibility check to 15 min;
abandon if it is not servable in that window.
*Gate:* pair accuracy ≥ 0.851 (the shipped judge) at a fraction of the cost. If
it matches, it replaces the judge and L1 becomes unnecessary.

**R3 — Requirement→atom assignment. SHOULD. 35 min.**
*Hypothesis:* attacks a *measured* defect — "one CV atom is reused across many
requirements". Independent top-k scoring lets one hub atom win everything.
*Method:* min-cost assignment (Hungarian) with per-atom capacity, so coverage
spreads across requirements. Pure math, no model, no latency.
*Lazy version first:* MMR — `λ·relevance − (1−λ)·max_sim_to_selected`. If MMR
captures the gain, skip the assignment solver entirely.
*Gate:* `selection_regret` (F3) down; pair accuracy not down.

**R4 — Alternative embedder families. STRETCH. 30 min.**
Qwen3-emb-4B failed, but that is one family. `bge-m3` (2.2 GB),
`mxbai-embed-large` (0.67 GB), `nomic-embed-text-v1.5`. The bench harness and
the embedding cache make each variant nearly free to test.
*Gate:* pair accuracy > 0.851. Low expectation given §1.

**R5 — Learned feature blend. STRETCH — gated behind M2/M4.**
Logistic regression over `[cos, bm25, judge_grade, doc2query_cos, section_prior]`.
At n=10 offers × 14 atoms this **will** overfit; leave-one-offer-out is mandatory
and any gain under 0.05 is noise. **Do not attempt before the label count rises.**

**R6 — Per-atom z-normalisation over the 186-JD background. STRETCH. 15 min.**
Different from CSLS (which failed), but same family of idea. One cheap run.

### 2.E Truth and balance

**T1 — Two-tier vocabulary. SHOULD. 40 min.**
The strategy doc's Phase 3, sequenced last there and here for the same reason:
capability phrases free, named products grounded. Loosening keyword filtering is
only safe once `ats_coverage` and `product_fab` can both be watched.
*Gate:* `ats_coverage` up toward the 50 % band; `product_fab` stays 0.

**T2 — `product_fab` as a hard gate. MUST. 20 min.**
Baseline is 7 %, and the two-tier rule is aspirational until violations are
*rejected* rather than counted. Detect a fabricated named product → regenerate
that field, then strip if it persists.
*Gate:* `product_fab` → 0 on the fixed sample, no `ats_coverage` collapse.

### 2.F Measurement — breaking the resolution ceiling

**M1 — Second gold set. SHOULD (needs ~25 min of user time).**
`batch/bench/goldset-2.md` is already generated and unlabelled: 12 fresh offers
with no overlap with sheet 1. It doubles resolution and gives a genuinely
held-out check on `cos + 0.10 × judge` — offers that played no part in selecting
it.

**M2 — Label-noise estimate. SHOULD. Free, rides on M1.**
Re-tick 2 of sheet 1's offers inside sheet 2. **Label noise is currently
unmeasured, so the true ceiling of the 0.851 is unknown** — it may already be at
it. This is the cheapest genuinely informative measurement available.

**M3 — Offline oracle pseudo-labels. STRETCH. 60 min, mostly unattended.**
*Hypothesis:* the only route past n=10 that does not cost user time. The prior
oracle attempt used `snipe-eval` itself — the *same* 30B-A3B as the runtime
judge, with 3B active parameters. That is circular and underpowered. A dense
32B, run offline where slowness is free, is a different proposition.
*Method:* pull `qwen3:32b` q4 (~20 GB — consumes essentially all headroom, so
this contends with G1(b)/(c) and R2; see §0.2 priority order). Label 50–100
offers in the background. Validate against the 10 human-labelled offers **before
trusting any of it.**
*Gate:* **pre-registered** — agreement with human labels ≥ 0.764 pair accuracy,
the same bar the previous oracle attempt failed. Below that, discard the labels
entirely rather than dilute the gold set.

---

## 3. Schedule

Two things run unattended in the background from early on: the oracle labelling
(M3) and the long harness runs. The wall clock below assumes that overlap.

| Slot | Work | Tier |
|---|---|---|
| **0:00–0:40** | F1 disk cleanup · F2 timing instrumentation · first real end-to-end measurement | MUST |
| **0:40–1:25** | F3 four metrics into the harness · F4 re-baseline (unattended run) | MUST |
| **1:25–2:05** | L2 residency scheduling · L3 output-token audit | MUST/SHOULD |
| **2:05–3:05** | **G1 writer swap** — (a) 30B first, then (b) if it fails the latency gate | MUST |
| **3:05–3:40** | G2 drop worked example · G3 Tier-4 fix | MUST |
| **3:40–4:25** | G4 summary as its own stage | MUST |
| **4:25–5:05** | G5 best-of-N · T2 `product_fab` hard gate | SHOULD |
| **5:05–5:50** | R1 doc2query · R3 MMR (assignment only if MMR shows signal) | SHOULD |
| **5:50–6:30** | L1 fold grading into Phase 2 · R2 cross-encoder feasibility (15 min timebox) | SHOULD |
| **6:30–7:10** | T1 two-tier vocabulary | SHOULD |
| **7:10–7:40** | Full regression: all eight metrics on the fixed sample, gold-set check, one real end-to-end calibration run | MUST |
| **7:40–8:00** | Ledger, commits, cleanup | MUST |

**Background from 1:25:** M3 oracle labelling, if the disk contention resolves
in its favour.
**Needs user time, request early:** M1 + M2 gold-set labelling (~25 min).

**First to be dropped when the clock slips:** G6, G8, R4, R5, R6, L4, M3.
**Never dropped:** F1–F4 and the 7:10 regression. A change that ships unmeasured
is worse than one that does not ship.

---

## 4. Gates, kill criteria, rollback

**Benchmark discipline** (from `batch/CLAUDE.md`, not relitigated):
temperature 0 where possible — this stack is byte-identical greedy, so the noise
floor is 0 and one run is a valid A/B. Compare two runs made *now*; historical
artifacts are not a control. Paired per offer, bootstrap CI over offers, sign
test.

**Kill criteria, decided in advance:**

- Any retrieval variant gaining **< 0.05** pair accuracy at n=10 is **not
  shipped**, however good the point estimate looks. That is the resolution
  limit, and shipping inside it is how a benchmark becomes decoration.
- L1 dropping the gold set below **0.80** reverts. 43 s does not buy 0.05.
- M3's oracle labels are discarded outright below **0.764** agreement.
- Any change that breaks `role_retention 1.0` or `metric_fab 0` reverts
  immediately, regardless of what else improved. Those are truth invariants,
  not metrics to trade against.
- End-to-end over **5 min** on the calibration run reverts the most recent
  latency-costing change.

**Rollback:** one commit per change, each with its measured deltas in the
message. `PHASE3-RETRIEVAL-LEDGER.md` stays append-only; a second ledger section
covers generation. Nothing is squashed — a variant that loses is as valuable a
record as one that wins, and §1 exists because the losses were written down.

---

## 5. Named risks

- **G1 is the plan's biggest bet on an untested premise.** "Coder model writes
  bad prose" is highly plausible and completely unverified. If the 30B writer
  shows no metric gain, ~60 min is spent and the plan's largest expected gain
  evaporates. Mitigated by running it early enough to re-plan around.
- **Latency and quality trade directly.** G1(a), G4, G5 and G7 each add model
  calls. The 5-minute ceiling is a real constraint, and L1–L3 must land first or
  the generation work has no room to fit.
- **The 0.851 may already be at the label-noise ceiling.** M2 is what would
  reveal it. If it is, no retrieval work in this plan can succeed and the
  retrieval slots should be reallocated to generation on the spot.
- **`summary_jd_fit` and `summary_cv_fit` can be gamed together** by a summary
  that quotes both documents verbatim. `product_fab` and `grounding` hold that
  down, but the pairing needs eyes on actual output, not just numbers.
- **Determinism loss.** G5's temperature 0.7 forfeits the byte-identical
  property that makes single-run A/B valid. It needs its own noise floor
  established before its numbers mean anything.
- **Disk contention is real.** G1(b/c), M3 and R2 cannot all be pulled. Priority
  is G1 > M3 > R2, and G1(a) sidesteps it entirely by reusing a model already
  on disk.

## 6. Out of scope

Education and certifications (static, untailored). Phases 1–2 scoring quality.
The `hard_stops` schema bug — real, flagged five times, and a Phase 2 evaluator
defect rather than a tailoring one. Anything touching the user layer
(`cv.md`, `config/profile.*`, `reports/*`, `output/*`).
