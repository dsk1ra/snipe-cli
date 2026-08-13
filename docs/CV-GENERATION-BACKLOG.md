# CV generation — candidate improvements

Planning document, not results. Written 2026-08-13, after the summary rework
(`PHASE3-GENERATION-LEDGER.md` §11–§12) and the experience floor (§13). Nothing
here is implemented; each item states what it would cost to find out.

Read `PHASE3-NEXT.md` before picking anything — it holds the closed-idea table,
and one item below was already closed there.

| # | item | area | cost to answer |
|---|---|---|---|
| 1 | dead pin in `profile.yml` | selection | minutes |
| 2 | blanket experience floor | selection | one 45-min arm |
| 3 | section-level budget ratio | selection | offline sweep, then one arm |
| 4 | judge grade as a cut | selection | offline sweep, then one arm |
| 5 | ~~near-duplicate suppression~~ | — | **closed, see below** |
| 6 | best-of-N summary | summary | one 32-offer arm |
| 7 | grade-ordered evidence | summary | one 32-offer arm |
| 8 | curated synonym alignment | ATS | offline, then one arm |
| 9 | wider skills taxonomy | ATS | offline report, then your edit |
| 10 | generic proper-noun detector | fabrication | offline against 32 shipped |
| 11 | ~~`section_balance` metric~~ | measurement | **shipped, see below** |
| 12 | regenerate 240–243 | hygiene | ~20 min of machine time |

---

## 1. The pin in `config/profile.yml` has never fired

`cv.pinned_projects` holds `"Zero Trust SIEM"`. The match is a case-insensitive
substring of the `### ` title, and the title in `cv.md` is `Zero Trust Security
Analytics Dashboard`. No substring, no pin. Every run since the feature landed
has selected as if the list were empty.

Two separate fixes, and only one of them is mine to make. The string lives in the
user layer. The silence does not: a pin that matches nothing should fail loudly
at load, because the failure mode here is a feature that appears to work and
quietly does nothing for months.

Worth knowing before you correct the string: a pin spends one of the three
project slots and moves the benchmark for every arm, so any comparison against a
run made before the pin fires needs saying out loud.

## 2. Blanket experience floor instead of top-entry only

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

## 4. Use the judge grade as a cut, not only as a weight

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

## 9. Widen the skills taxonomy in `profile.yml`

The cheapest ATS lever available. `filterSkillItems` grounds every rendered tag
against `cv.md`, so adding source vocabulary carries no fabrication risk by
construction — anything unsupported is dropped before it renders.

What I can produce without touching your layer: the list of JD terms across the
128-offer corpus that currently match nothing, ranked by how often they appear.
You decide which of them describe things you can actually do.

## 10. A generic proper-noun fabrication detector

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
