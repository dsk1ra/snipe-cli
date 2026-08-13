# CV generation — candidate improvements

Planning document, not results. Written 2026-08-13, after the summary rework
(`PHASE3-GENERATION-LEDGER.md` §11–§12) and the experience floor (§13). Nothing
here is implemented; each item states what it would cost to find out.

Read `PHASE3-NEXT.md` before picking anything — it holds the closed-idea table,
and one item below was already closed there.

| # | item | area | cost to answer |
|---|---|---|---|
| 1 | ~~dead pin in `profile.yml`~~ | selection | **code done, your string edit** |
| 2 | ~~blanket experience floor~~ | selection | **subsumed by item 3, quantified** |
| 3 | ~~section-level budget ratio~~ | selection | **shipped, `projMaxLines=14`** |
| 4 | ~~judge grade as a cut~~ | selection | **closed, −0.062 held out** |
| 5 | ~~near-duplicate suppression~~ | — | **closed, see below** |
| 6 | best-of-N summary | summary | one 32-offer arm |
| 7 | grade-ordered evidence | summary | one 32-offer arm |
| 8 | curated synonym alignment | ATS | offline, then one arm |
| 9 | ~~wider skills taxonomy~~ | ATS | **answered, your edit next** |
| 10 | ~~generic proper-noun detector~~ | fabrication | **closed, 0 catches / 16% false** |
| 11 | ~~`section_balance` metric~~ | measurement | **shipped, see below** |
| 12 | regenerate 240–243 | hygiene | ~20 min of machine time |

---

## 1. The pin in `config/profile.yml` has never fired — code done

`cv.pinned_projects` holds `"Zero Trust SIEM"`. The match is a case-insensitive
substring of the `### ` title, and the title in `cv.md` is `Zero Trust Security
Analytics Dashboard`. No substring, no pin. Every run since the feature landed
has selected as if the list were empty.

**Correction to the premise above: it was never silent.** `cv-select` has warned
since the feature's first commit and warned on every run —
`batch/logs/pdf-NNN-<id>.log` carries the line in 16 of 202 logs. That file is
opened only when an offer fails, so the warning was loud in the wrong room.

Fixed: the runner asks `unmatchedPins` once at preflight, beside the other config
validation, and prints a warning rather than exiting — a stale pin must not stop
a run. The per-offer warning stays for callers that are not the runner.

The string itself is still yours.

Worth knowing before you correct the string: a pin spends one of the three
project slots and moves the benchmark for every arm, so any comparison against a
run made before the pin fires needs saying out loud.

## 2. Blanket experience floor — SUBSUMED by item 3

The section cap answers this without a blanket floor, because it attacks the
same imbalance from the budget side. Per-entity, over the 128-offer corpus,
measured on the entity the complaint was actually about:

| config | Teaching Assistant at 1 bullet | **commercial role at 1 bullet** | Δ coverage held out |
|---|---|---|---|
| floor only (what shipped) | 53% | **18%** | — |
| **floor + cap 14 (ships now)** | 37% | **3%** | −0.011, CI contains 0 |
| blanket floor | 0% | 0% | −0.035, CI excludes 0 |
| blanket floor + cap 14 | 0% | 0% | −0.037, CI excludes 0 |

The cap removes **83% of the complaint for free** — the commercial role goes
from one bullet on 18% of postings to 3%. A blanket floor closes the last 3% and
charges 0.024 more coverage for it, and stacking it on top of the cap buys
nothing further (−0.037 against −0.035 alone).

Still your judgement rather than the corpus's, but it is now a much smaller
question: 3% of postings against 0.024 coverage. The original framing below
priced it at 0.037 for the whole gap, which was before the cap existed.

### The original proposal, for the record

The shipped floor lifts the highest-scoring experience entry to two bullets. On
the 128-offer corpus that entry is usually the teaching assistantship, which is
why "top-scoring only" priced at −0.018 `differentiator_coverage` against −0.055
for flooring every entry.

Your CV has two employers, so the general rule and your case pull apart. On
report 239 the floor lifted Edinburgh Napier and left UBWIS at one bullet — the
aggregate improved (offers with *every* employer at one bullet went 7/32 → 0/32)
while the specific complaint that started the work did not.

A blanket floor guarantees UBWIS ≥2 for roughly 0.037 more coverage than what
ships today. That is a judgement about what a reader of your CV should see,
which the corpus cannot make for you.

## 3. Section-level budget ratio

Reaches the same imbalance from the other side. Rather than flooring an entry,
cap what projects may take of the 24-line budget, so experience cannot be
squeezed to 2 lines of 10 before any floor has to intervene.

This is closer to how the page actually reads and it needs no targeting rule, so
the awkward question in item 2 — which entry deserves the floor — stops being
asked. It is also more disruptive: `allocateLines` currently spends one pool, and
a ratio splits it into two. `select-sweep.mjs` can simulate it for free, and the
simulator now agrees with reality to within 0.011 (§13), so an offline sweep is
worth trusting before spending an arm.

## 4. Judge grade as a cut — CLOSED, do not re-open

**−0.062 differentiator coverage held out, 0 wins 18 losses, p<0.0001.** Answered
offline for nothing; the grades were already cached. Ledger §16 has the account.

The mechanism is the part that matters, because it also closes the neighbouring
ideas. The judge's grades are binary, so every threshold is the same cut. Its
zeros are **22.8% precise** against the Opus corpus — cutting them deletes 368
flagged differentiators. And `gradeW` cannot be the control rule 4 demands
because it is **already saturated at the shipped 0.10**: with grades in {0, 3},
raising it cannot reorder anything.

The judge is a precise positive signal (0.950) and a near-worthless negative one
(0.228). Weighting uses the positives; cutting uses the negatives. That is the
whole result.

### The original proposal, for the record

`noise_rate` is 0.178 — close to a fifth of what ships gets called padding by the
same judge that graded it. The rank is `cos − α·corpus_mean + 0.10 × grade`, so a
bullet the judge grades 0 is only nudged down; a strong cosine still carries it
onto the page.

Dropping atoms below a grade threshold is a different lever and, as far as
`PHASE3-NEXT.md` records, untried. Read the adjacent closed result first: the
distinctiveness rating lost because it could not beat simply raising `gradeW`,
so any threshold has to be measured against a raised weight rather than against
today's 0.10.

Watch the interaction with item 2. A floor promotes bullets by position and a cut
removes them by grade, and on a two-employer CV they can fight over the same
line.

## 5. Near-duplicate suppression — closed, do not re-open

Measured as the MMR redundancy penalty: **+0.002 on top of spike, subsumed.**
The spike term already discounts a bullet that reads relevant to everything,
which is most of what redundancy suppression would have caught. Listed here only
so it stops being re-proposed; see `PHASE3-NEXT.md`.

## 6. Best-of-N summary

`generateSummary` returns the first usable draft. `scoreSummary` exists, is
tested, and currently only breaks ties on the repair path — which fires rarely
now that rejection replaced repair.

Three drafts at temperature 0.3 with the score choosing costs roughly 8 seconds
per offer and reuses machinery already built. The reason to expect anything: the
guards prove a draft is *clean*, and clean is a floor rather than a ranking. Two
clean drafts can differ a lot in how well they answer the posting, and nothing
currently distinguishes them.

Against it — temperature 0 is a determinism choice for production, and sampling
gives that up. A single run stops being a valid A/B on this stack, so the arm
needs repeats where every summary arm so far has needed one.

## 7. Order the evidence lines by judge grade

The summary prompt hands the model the shipped bullets in selection order and
asks for one achievement from one entry. The judge has already graded every one
of those bullets and the summary never sees it.

Sorting the evidence by grade points the model at the strongest claim rather than
the first plausible one. No extra model call — the grades are already in memory
at that point in the run.

## 8. Curated synonym alignment for ATS terms

`ats_coverage` sits at 0.659 and is structurally capped. `--writer verbatim`
means every bullet is literal `cv.md` text, so a posting asking for "RDBMS" finds
nothing when the CV says "PostgreSQL", and a posting asking for "CI/CD" finds
nothing when the CV says "GitHub Actions".

The proposal is deliberately narrow: a hand-written equivalence map, applied only
to exact known pairs, surfacing the posting's term where the CV already proves
the thing. No generative rewrite, so no new fabrication surface — which matters,
because `PHASE3-NEXT.md` records that a bigger writer model loses grounding in
every offer it changes.

Distinct from Experiment B (`PHASE3-NEXT.md`), which rewrites `cv.md` itself to
lower a bullet's corpus mean. That one attacks ranking; this one attacks the
keyword surface, and they can ship independently.

## 9. Widen the skills taxonomy — ANSWERED, your edit next

The taxonomy lives in `cv.md`'s `## Skills` block, not `profile.yml` —
`selectSkills(cvText, …)` parses the CV. Both are the user layer, so it does not
change whose edit it is, but it changes which file to open.

Report: `node batch/bench-tools/skills-gap.mjs --min 4 [--shaped]`. Two passes,
because they fail in opposite directions — a curated-vocabulary pass that is
precise and blind to anything nobody wrote down, and a shape-only pass that is
complete and noisy. The shape pass is what found the two entries at the top of
this table, so it earned its noise.

**The top two are notation, not capability.** `cv.md` already proves both, and
the taxonomy simply has no string that matches what postings write. Since
`skillForms` now reads a spaced slash as "either of these", each is a
one-character edit:

| write it as | instead of | offers naming it |
|---|---|---|
| `AI / LLM application development` | `LLM application development` | **84 (66%)** |
| `REST / RESTful APIs` | `RESTful APIs` | **21 (16%)** |

Then a genuine gap that is safe to claim: **Git** (10 offers, 8%) — the CV lists
GitHub Actions and GitLab CI but never Git itself.

Everything below is a capability question the corpus cannot answer. Nothing here
carries fabrication risk either way: `filterSkillItems` grounds every rendered
tag against `cv.md`, so an unsupported item is dropped before it renders.

| term | offers | term | offers |
|---|---|---|---|
| Azure | 22 (17%) | Golang | 8 |
| GCP / Google Cloud | 17 (13%) | Copilot | 8 |
| Terraform | 11 (9%) | Angular | 6 |
| SRE | 9 (7%) | Flask, Databricks | 4 each |

Tail below 3 offers: Elasticsearch, Datadog, Snowflake, PHP, Kotlin, Scala,
Ansible, Cassandra, Airflow, Hibernate, Pytest.

**What this cost to find, and what it turned up on the way:** `skill_coverage`
was scoring 3.5 skills a posting out of a 105-item taxonomy, because it matched
the CV's exact string and `cv.md` writes several items as alternatives. Fixed —
see the ledger. Also measured and **rejected as marginal**: bare `Express` /
`Node` against the CV's `Express.js` / `Node.js`, which is real but 3 offers of
128, and the bare forms of `Next.js` and `.NET` collide with ordinary English
("next steps", "net salary"), so an alias rule would invent more than it fixes.

## 10. Generic proper-noun detector — CLOSED, measured against the arm that fabricated

Run as proposed — "a capitalised token appearing in neither `cv.md` nor the
posting" — over the pre-guard summaries of `sum-new`, the arm that fabricated on
8 of 32 offers, and of `sum-v4`:

| | `sum-new` | `sum-v4` |
|---|---|---|
| caught by `summaryUnsupported` only | **9** | 1 |
| caught by both | 1 | 0 |
| caught by the generic rule only | 5 | 2 |
| …of those, real fabrications | **0** | **0** |

Every generic-only hit is a compound or hyphenated form of a term `cv.md`
genuinely claims: `C#-Python`, `React-based`, `Rust-based`, `Kafka-based`,
`P2P`, `Python-based`, `Kafka/RabbitMQ`. A 16% false-positive rate on `sum-new`
for nothing.

**And it cannot catch the miss that motivated it.** §12's example is `sum-v4`
claiming *"clinical AI agents"* on a clinical-AI posting. The rule keys on
capitalisation; "clinical" is lowercase. The one documented gap in the hand
lists is invisible to the proposed replacement for them.

Fixing the false positives means morphological normalisation — splitting
hyphens, stripping `-based`, matching compounds — which is real code for a
measured yield of zero. The hand-maintained lists stay. Their weakness is real
and this is not the fix for it; a lowercase-domain rule would be, and that is
what `NAMED_DOMAINS` already is.

### The original proposal, for the record

`NAMED_DOMAINS` and `CASED_PRODUCTS` are hand-maintained word lists, and every
entry in them was added *after* a fabrication got through. The clinical-AI miss
in §11 is the pattern: the detector was honest about what it covered and I read
its silence as absence.

A rule of "capitalised token appearing in neither `cv.md` nor the posting" covers
the classes nobody has thought of yet. It will need an allow-list for ordinary
sentence-initial words and for the role title itself, so it is not free — but it
converts a vocabulary problem into a much smaller exceptions problem.

## 11. A `section_balance` row in the harness — SHIPPED

Four fields: `exp_bullets`, `proj_bullets`, `section_balance` (the share) and
`all_exp_starved_pct` (the gate). Validated by reproducing §13's hand count
exactly, 7/32 → 0/32, and the share alone would have missed it — 0.355 → 0.387
reads as noise. Items 2, 3 and 4 can no longer regress balance silently.

Rescoring the committed arms was free and turned up the reason the problem
exists: **the starvation arrived with the line budget on 2026-08-08.** Every
count-based funnel starves nobody; `LINE_BUDGET=21` starved every employer on 16
of 32 offers, 24 on 7 of 32. The +0.116 coverage that budget bought was paid for
partly in Experience section and nothing could read the invoice at the time.
Full account and the two comparability traps in
`PHASE3-GENERATION-LEDGER.md` §14.

## 12. Regenerate reports 240–243

They were rebuilt against the pre-floor selection and are stale. Report 239 has
been redone; these have not.

---

## Standing constraints that apply to all of the above

- A selection change must not reuse a select cache (items 2, 3, 4 all move
  selection).
- `validate` must pass before any sweep result is believed, and the reference is
  now named in `select-sweep.mjs` rather than hardcoded to a date.
- No historical control. Items compared against §13's numbers need their own
  control run made at the same time.
- Read the output. Every summary defect so far passed the metric table first.
