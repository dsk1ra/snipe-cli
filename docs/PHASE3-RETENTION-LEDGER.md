# Phase 3 retention ledger — what the tailored CV loses

Companion to `PHASE3-GENERATION-LEDGER.md` (what it invents) and
`PHASE3-RETRIEVAL-LEDGER.md` (what it ranks). This one is about the third
question, which had no measurement at all: **does the tailored CV still carry
the things that make this candidate worth interviewing?**

Everything here was run on `batch/bench/tailor/sample32.tsv` (32 offers, eval
score 3.5–5.0, mean 4.43) at `--temperature 0`, paired per offer, with a
bootstrap CI over offers and a two-sided sign test (`batch/stats.mjs`).

---

## 1. Why the existing suite could not see this

Every metric in `tailor-harness.mjs` punishes falsity: `metric_fab`,
`product_fab`, `summary_fab`, `example_copy_pct`, `grounding`, `num_retention`.
The generation ledger already names the consequence — *an empty CV asserts
nothing, so it scores perfectly on all of them.*

That is not a hypothetical failure mode. On the control run:

| metric | control |
|---|---|
| `metric_fab` | 0.000 |
| `product_fab` | 0.000 |
| `num_retention` | 1.000 |
| `grounding` | 0.971 |
| `role_retention` | 1.000 |

A perfect scorecard. And on the same 32 CVs, **the pipeline was dropping three
to six of every six bullets a reviewer called distinguishing.**

The two metrics that gesture at the other side are both weaker than they look:

- **`ats_coverage`** counts JD *terms*. A CV that name-drops "Kafka" in a skills
  list scores the same as one that shows what was built with it.
- **`selection_regret`** scores the selection against an oracle built from the
  same embedding model the selector uses. It can report that the selector
  disagreed with the embeddings; it cannot report that the embeddings were
  wrong. Circular by construction.

## 2. The label corpus

`batch/bench-tools/opus-label.mjs` → `batch/bench/opus/labels/*.json`.

128 offers, each labelled by a frontier model outside the pipeline (`claude -p`,
Opus, `--allowedTools ""` so nothing outside the prompt can influence a label).
125 new + 3 from the pilot. **$49.21.**

Per offer it produces `role_family`, `seniority`, `key_requirements`, a 0–3
grade with an ≤8-word rationale for **every** one of the 33 CV atoms, and two
things the grades cannot express:

- **`differentiators`** (≤6) — atoms that are relevant **and rare**. A bullet can
  be a legitimate 3 and still be generic: every applicant has "built REST APIs
  with Spring Boot". This is the field the whole exercise exists for.
- **`noise`** — atoms that would read as padding on this posting and cost
  credibility.

A label is rejected if it grades no atom 0. That is the exact failure the local
30B judge fell into 0-shot, where it scored 0.670 against plain cosine's 0.756 —
worse than not grading at all — and accepting it here would bake that refusal to
choose into the ground truth.

Why a per-grade rationale: the retrieval ledger measured that dropping the local
judge's per-item `id` field — pure bookkeeping, the order being fixed by the
prompt — cost **0.052 pair accuracy**. Making the model write something per item
is what keeps the grading deliberate.

### Corpus shape

```
role families   backend 51 · fullstack 36 · platform-infra 17 · ml-ai 11 · data 7 · security 5 · other 1
seniority       senior 52 · mid 43 · junior 23 · graduate 10
```

## 3. The new metrics (`batch/opus-metrics.mjs`)

| metric | want | definition |
|---|---|---|
| `differentiator_coverage` | 1.0 | of the atoms marked distinguishing for this posting, the fraction that reached the CV |
| `noise_rate` | 0 | of the atoms that shipped, the fraction the reviewer called padding |
| `grade_yield` | 1.0 | graded worth of what shipped over the best possible pick **of the same size** — `selection_regret` with real labels instead of the selector's own embeddings |
| `mean_grade` | higher | mean label grade of the shipped atoms |

Two design decisions that matter:

**Direction.** Retention is matched atom → output, not output → atom. The
existing `shippedAtomIndices` asks "what did this bullet come from", so a CV with
fewer bullets than atoms reports fewer matches whether or not the content
survived, and a merged or truncated bullet reads as a clean miss. Asking "did
this atom survive" instead counts a shortened rewrite as retained, which is what
Phase 3 does to every line.

**`undefined`, not 0.** An offer where the reviewer named no differentiator is
skipped rather than scored zero — averaging a 0 there would punish a CV for a
question that was never asked.

---

## 4. Results

All runs: n=32 offers, temperature 0, paired per offer, bootstrap CI95 over
offers + two-sided exact sign test. `*` = CI excludes 0. Selection is cached
(`SNIPE_SELECT_CACHE`), so every arm ranks bullets identically and the only
variable is what happens *after* selection.

### 4.1 The headline: deleting the generation call wins

`ctl32` (shipped) → `vbp2` (verbatim + 2 project bullets):

| metric | ctl32 | vbp2 | delta | w-l | p |
|---|---|---|---|---|---|
| `differentiator_coverage` | 0.311 | **0.468** | **+0.157** | 20-3 | <0.001 * |
| `grade_yield` | 0.622 | **0.689** | +0.067 | 25-5 | <0.001 * |
| `ats_coverage` | 0.636 | **0.697** | +0.061 | 25-4 | <0.001 * |
| `grounding` | 0.971 | **1.000** | +0.029 | 26-0 | <0.001 * |
| `noise_rate` | 0.288 | 0.286 | −0.002 | — | 1.000 |
| `metric_fab` / `product_fab` / `num_lost` | 0.000 | 0.000 | 0 | — | — |

Half the gain is the writer, half is the rendering, and they were measured
separately so neither claims the other's credit:

| step | change | `differentiator_coverage` | w-l | p |
|---|---|---|---|---|
| `ctl32`→`vb32` | delete the 7B rewrite | +0.078 | 15-5 | 0.041 * |
| `vb32`→`vbp2` | render project bullets | +0.079 | 24-0 | <0.001 * |

`vb32`→`vbp32` (3 project bullets) scored `+0.221` / 24-0, but 3 bullets renders
a 3-page PDF and the cap is 2. The shippable version is 2.

**Why the rewrite was never buying anything:** 78% of the bullets the 7B emitted
were already byte-identical to their `cv.md` source line. It was paid a full
generation call to retype the input, and the 22% it did change is where the
grounding loss lived.

### 4.2 A bigger writer does not fix the writer

The user's budget explicitly allowed 8 min/JD, so G1 was re-opened with two
larger models. Both were run against `vbp2` — same selection, same rendering,
the *only* variable is the generation call coming back.

| metric | `vbp2` | `q9b` | `m30b` |
|---|---|---|---|
| `differentiator_coverage` | 0.468 | 0.514 * | 0.524 * |
| `grade_yield` | 0.689 | 0.728 * | 0.714 * |
| `ats_coverage` | **0.697** | 0.631 (2-27, <0.001 *) | 0.650 (7-25, 0.002 *) |
| `grounding` | **1.000** | 0.945 (0-32, <0.001 *) | 0.980 (0-11, <0.001 *) |
| `mean_grade` | 1.630 | 1.646 (ns) | 1.624 (ns) |
| wall clock | ~0 | ~25 min/32 | ~43 min/32 |

A bigger model recovers ~0.05 of coverage by *paraphrasing more aggressively*,
and pays for it in the two metrics that encode the user's actual requirement:
ATS keyword coverage drops (2-27 and 7-25 against it) and grounding leaves 1.000
in **every single offer that changed** — 0-32 and 0-11, not one win. That is the
"don't add irrelevant stuff" failure, measured. Scaling the writer moves the
failure, it does not remove it.

### 4.3 The variant bank: a clean negative

`vbp2`→`bank2` — 33 CV atoms × ~3 Opus-written angle variants (94 verified),
picked per offer by embedding argmax against the Block B requirements:

| metric | delta | p |
|---|---|---|
| `differentiator_coverage` | 0.000 | 1.000 |
| `grade_yield` | 0.000 | 1.000 |
| `ats_coverage` | +0.002 | 0.58 |
| `grounding` | −0.037 | * against |

Blind pairwise preference (Opus, order flipped per offer by sha1 of the dir
name, ties allowed): **22–8–2 for `vbp2`, p=0.016 against the bank.**

Rephrasing an atom cannot recover a differentiator that was never selected, and
the bank's angles read as thesaurus work to a human judge. Cost $2.27 to author.
The code stays on disk unwired (`batch/cv-bank.mjs`, `batch/cv-bank.json`,
`batch/bench-tools/gen-cv-bank.mjs`) as the record; nothing in the pipeline
imports it.

### 4.4 Where the loss actually was

Architectural, not model quality. `cv.md`'s Projects section holds **24 of the
33 labelled differentiator atoms**, and Phase 3 rendered every project as a
single prose paragraph — so three quarters of the evidence had one sentence to
fit through. The 7B was then blamed for losing what the template never gave it
room to print. Rendering project bullets is worth more than any writer change
measured here, and costs no model call.

### 4.5 Two production bugs found on the way

- **Reasoning models return `""`.** `qwen3.5:9b` failed 32/32 with an empty
  `response` — hybrid-reasoning models route output to `thinking` unless
  `think: false` is set. Now set in `callOllama`, verified harmless on
  `snipe-cv` and `snipe-eval`.
- **JD proper nouns leaked into the summary.** A summary named a product from
  the *posting* (observed: "Joybuy"), which `product_fab` could not catch — it
  only knows products absent from `cv.md`, and this one was absent from `cv.md`
  *and* present in the JD, which reads as grounded. `stripJdProperNouns` now
  removes them.

### 4.6 Shipped

`--writer verbatim` and 2 project bullets are the defaults as of this branch.
The 7B generation call is off the shipping path; `--writer model` remains as the
benchmark control. Phase 3 loses its 7B call entirely — the summary stage
(`batch/summary-stage.mjs`) is now the only generation in the phase.

### 4.7 What was deliberately not done

`cv.md` was backed up (`cv.md.backup-20260807`) but **not rewritten**. Dead
weight is real — several atoms never get selected by any of the 128 offers — but
every one of the 128 labels is positional against the current `cv.md`, and the
bank keys off its sha1. Rewriting it invalidates $49 of labels. Do the rewrite
and the relabel together, or not at all.

## 5. Runs

| label | writer | project rendering | model |
|---|---|---|---|
| `ctl32` | shipped JSON rewrite | paragraph | `snipe-cv` (Qwen2.5-Coder 7B) |
| `vb32` | verbatim, no generation call | paragraph | — |
| `vbp2` | verbatim | 2 bullets | — |
| `bank2` | pre-written variant bank | 2 bullets | — |
| `q9b` | JSON rewrite | 2 bullets | `qwen3.5:9b-q4_K_M` |
| `m30b` | JSON rewrite | 2 bullets | `snipe-eval` (Qwen3 30B-A3B) |

**Confound, stated rather than hidden:** `ctl32`/`vb32`/`vbp2`/`bank2` ran before
`stripJdProperNouns` and `think: false` landed; `q9b`/`m30b` ran after. Both fixes
touch generation only, so they can only have *helped* the two model arms — which
lost anyway. The §4.1 comparison is unaffected (all four arms are pre-fix), and
§4.2 is if anything generous to the writers.
