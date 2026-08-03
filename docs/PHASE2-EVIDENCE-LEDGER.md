# Phase 2 evidence ledger

Block B is the report's central claim: *this CV line proves that JD requirement,
at this strength*. It also feeds Phase 3 — `cv-select.mjs` ranks against Block B
requirements — so a wrong row propagates into the tailored CV. This file records
what has been measured about it. Append-only.

---

## 1. The abstain floor. Measured, and it cannot work.

`stage2Evidence` calls `topK(reqVec, cvIndex, 3)`, which has **no similarity
floor**: it returns the three nearest CV atoms no matter how far away they are.
The model then picks one or answers `none`. The obvious fix is to withhold
candidates below some cosine, so a requirement with no real evidence can only
come back a Gap.

Top-1 cosine for 573 requirement rows across 60 shipped reports, grouped by the
strength that shipped:

```
strength        n    p05    p25    med    p75    p95
Strong        336  0.528  0.600  0.645  0.679  0.732
Transferable  146  0.586  0.623  0.653  0.698  0.772
Gap            91  0.521  0.598  0.628  0.662  0.705
```

**The three classes are indistinguishable.** Gap sits at median 0.628 against
Strong's 0.645, and the p05–p95 ranges overlap almost entirely. `snipe-embed`
maps all software-engineering prose into a 0.5–0.77 band, so the cosine encodes
"both of these are technical text", not "this line evidences this requirement".

What a floor would actually cut:

```
  <0.40     0 rows
  <0.45     1 row   (already Gap)
  <0.50     5 rows  (Strong   1, Transferable  1, already Gap 3)
  <0.55    45 rows  (Strong  33, Transferable  4, already Gap 8)
```

Below 0.50 it is a no-op; at 0.55 it demotes 33 Strong rows to catch 8 Gaps,
which is cutting at random. **Not shipped.** A floor here would have looked
principled in the diff and done nothing — the failure mode this repository keeps
finding, and worth leaving written down so it is not re-proposed.

## 2. A catalogue line has never once produced a Gap

The same 573 rows, by what kind of CV atom the evidence came from:

```
rows citing a Skills or Education catalogue line: 184/573 (32%)
  Strong 138 · Transferable 46 · Gap 0
```

Zero. Not "few" — none, in 184 opportunities. The reason is structural: a
catalogue lexically contains every technology it lists, so it matches any
requirement naming one and then "proves" it. The surface cannot fail.

Observed shape, from report 168 (Java/Kafka role, scored 5.0):

| Requirement | Evidence | Shipped |
|---|---|---|
| AWS cloud platform knowledge | `Skills — Cloud & Infrastructure: AWS (EC2, Lambda, S3, IAM), …` | **Strong** |
| Familiarity with DevOps and CI/CD pipelines | `Skills — Development Practices: Agile / Scrum, Technical Leadership, …` | **Strong** |

AWS appears nowhere in that CV's Experience or Projects — only in the catalogue
line that "proved" it. The second row's evidence does not contain CI/CD at all.
Together with two more, they produced `cv_coverage: 1.0` and "Gaps: none
identified".

The stage-2 prompt has carried an exemplar against exactly this since it was
written — *Req "Kubernetes internals" vs evidence "Skills — Kubernetes (working
knowledge)" → same_activity FALSE* — and lost 138 times. Ledger V4's rule: when
the prompt-side fix measures zero, the repair belongs in code.

### The fix

`strengthFrom` takes the atom's `source` and caps `skills` / `education` at
Transferable. Not Gap — the tool genuinely is on the CV — but nothing in that
row shows it being used.

### Measured: two arms, 18 human-labelled offers

`SNIPE_SKILLS_CAP=0` runs the pre-cap arm without editing the file mid-run.

```
                          ctl      cap
mean score                4.16     4.06
distinct scores             12       13
distinct cv/ns combos        6        7
Block B grounded            97%      97%
A↔B Spearman                    0.980
catalogue-backed Strong      36        0     ← the mechanism
total Strong                 91       57

vs 18 human labels        ctl      cap     delta
Spearman                  0.412    0.441   +0.029
pair accuracy             0.735    0.753   +0.018  CI [-0.018, 0.054]
                                            better 5, worse 1, tied 77 of 83 pairs
```

**The ranking effect is unmeasurable and is not the justification.** Benchmark
rule 3 puts the resolution limit here at ~0.30 of Spearman per single label flip
across 18 labels; +0.029 is far inside it, and the pair-accuracy CI includes
zero. What the numbers establish is that the change **costs** nothing: grounding
flat, discrimination marginally up, rank order essentially preserved (0.980).

Shipped on **correctness** grounds instead. A row reading *"Strong — same work,
same tools"* whose evidence is a list of nouns is a false statement in a document
the user acts on, whether or not fixing it improves rank correlation against 18
labels. Phase 3 consumes Strong rows for bullet selection, so the same bogus row
propagates into the tailored CV.

### Consequence: scores shift down ~0.1

Mean 4.16 → 4.06 on the labelled set; 11 of 18 offers moved, the largest single
change −0.5. **Evaluations produced before this change are no longer comparable
to ones produced after**, which is benchmark rule 1 arriving in production rather
than in a bench directory. The 4.0 apply threshold now sits slightly higher in
real terms than it did.

### An offline replay that had to be thrown away

The first attempt measured this without model calls, by recomputing coverage
from the shipped reports — the cap is a pure function of (strength, atom source),
so it looked free. It reported pair accuracy 0.487 → 0.539.

Validating the replay against the reports' own stated values killed it: coverage
reproduced on 117/167 reports, **score on only 81/167**. Seniority and stack caps
are `Math.min` gates absent from the reconstruction, and the earliest reports came
from the classic evaluator. Both arms were distorted, so the delta meant nothing.
Discarded, and the real arms run instead. The lesson is benchmark rule 6's: a
cheap measurement has to be validated against something it did not compute
itself, or it is a confident number with no plumbing behind it.
