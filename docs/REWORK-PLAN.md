# Rework plan — a lean report, the posting's vocabulary, supervised skills

A planning document, started 2026-10-19 on branch `feat/slim-judgment`. It holds
what was decided, why, what is built, and what is still to build. Numbers marked
*measured* come from this tree; the rest are estimates and say so. Check the
status table first, since the sections below it describe intent and some of it
has not shipped.

## Status

| # | step | status |
|---|---|---|
| 1 | Slim the Phase 2 judgment and report to what decides the CV | implemented, measured: −95 s/offer, no detectable quality change |
| 1b | `north_star` from the profile's `fit` tier, not the model | implemented, measured: its own moves all toward the labels |
| 2 | Measure the skills block in the posting's own spelling (`gate skills`) | done |
| 3 | Approved skill aliases (`config/skills.yml`) | done, measured |
| 3b | Whole-form skill naming and 5/7/3 block limits | done, measured |
| 4 | Embedding-suggested aliases and a review queue | not started: needs the GPU and a TUI decision |
| 5 | Constrained keyword edit inside bullets | not started, and possibly unnecessary (see §5) |
| 6 | Qwen3.5-35B-A3B as a Phase 2 benchmark arm | not started |

## The goals

- The report carries only what helps decide what to do with the tailored CV.
  Less output, less generation time.
- The CV uses the posting's vocabulary. A posting that says "RESTful" should find
  "RESTful", without the skills list carrying both REST and RESTful.
- The skills block is matched, grouped (3–5 categories of 5–7 items) and approved
  by the user rather than left to a model, which tends to pick at random or
  overclaim.
- Output quality first. Budget: **6 minutes per JD**, excluding user input. Local
  models only, on an RTX 3060 Laptop (6 GB VRAM), 30 GB RAM, Ryzen 7 6800H.

## Where the time goes (measured)

From `batch/timings.tsv`, runs since 2026-08-15:

| stage | mean | notes |
|---|---|---|
| Phase 1 | 18 s | |
| Phase 2 | 298 s | of which `p2-judgment` 177 s, ~2,970 output tokens at ~21 tok/s |
| Phase 3 | ~8 s | the verbatim writer, no generation call |
| **total** | **~5.4 min** | already close to the budget |

Model-written report sections, median characters over the last 80 reports:
Interview Prep 8,079 · Personalisation + LinkedIn 2,755 · Strategy 926 ·
Legitimacy 703. Interview Prep alone was about 60% of what the judgment call
wrote.

## 1. Slim the judgment and the report

**Decision.** Drop the five things the user listed: Comp & Demand, LinkedIn
changes, Interview Prep (STAR stories), hard questions, posting legitimacy.

**What changed.**
- `internal/phase2/judgment.go`: `linkedin`, `stories`, `hard_questions`,
  `legitimacy_tier`, `legitimacy_reason` gone from the struct and the schema;
  `storiesSchema` and the story-target plumbing deleted.
- `internal/phase2/report.go`: the report is A) Role Summary, B) CV Match,
  C) Level & Strategy, D) Personalisation Plan. Phase 3 finds Block B by its table
  header, not its letter, so re-lettering is safe. The header gains one `Salary:`
  line, because a posted salary still carries 20% of the score and the report
  should say why.
- `internal/phase2/phase2.go`: the legitimacy fields leave the eval JSON and the
  machine summary. The multi-posting gate (`is_single_posting` from stage 1 and
  `fit.LooksMultiPosting`) stays. It was always the part that protected anything;
  the model's tier was decoration on top.
- `prompts/p2-judgment.md`: the story and legitimacy instructions are gone. The
  no-new-technology and no-invented-numbers rules now apply to the
  personalisation table, which is the one remaining place the model proposes CV
  text.
- `num_ctx` stays 12288 and `num_predict` 5120. Changing `num_ctx` changed
  answers at temperature 0 (`PHASE3-GENERATION-LEDGER.md` §8), and `num_predict`
  is a ceiling, not a cost.

**Kept on purpose.** Strategy and the Personalisation table. Neither was on the
user's list. Phase 3 does not read the Personalisation table (it parses Block B
only), so it is the next candidate if more time is wanted, about 2,100 more
characters of output.

**The risk, and how big it is.** The old comment in `judgment.go` said splitting
scoring from the content blocks dropped rank agreement 0.578 → 0.101. That 0.578
was the July evals archive, which `batch/labels/LEDGER.md` records as an invalid
control, and "content generation dilutes scoring" is in its disproved list. The
evidence that removing these sections hurts is therefore weak, but it is still a
change to the context the scores are decoded in, so it gets benchmarked.

**Measured saving.** Judgment output 3,022 → 982 tokens (−68%), generation
135 s → 42 s, the call 158 s → 57 s. With the ~6 s reload that production pays
between stages and the bench arms did not, about **95 s per offer**: Phase 2
~230 s → ~135 s on this sample.

**Gate.** Pair accuracy on the 18 labels (`batch/labels/labels-rev.tsv`) must not
drop materially against a baseline run made now. rho is reported, but with three
label values a ±1 on one label moves it ~0.30, so it cannot tell small changes
apart (benchmark rule 3).

**If pair accuracy drops:** add a short `rationale` field *before* the score
fields (2–3 sentences citing evidence-table rows, ~100 tokens), which keeps
"justify the score with real CV lines" at a fraction of the old cost. Evidence on
field order is mixed: one write-up found it matters, another found almost no
effect. Treat it as an A/B, not a fix.

**Open: comp in the score.** Proposal: keep the salary floor as a hard gate,
drop comp's 20% weight. It changes the ranking, so it needs its own benchmark
arm. Not done.

**Results** (2026-10-19, 18 labelled offers, temperature 0, no RAG; stages 1–2
shared from the baseline, so only the judgment differs):

| arm | rho | pair accuracy | MAE | judgment out tokens | judgment call |
|---|---|---|---|---|---|
| baseline (HEAD c07361c) | 0.342 | 69.3% (57.5/83) | 1.14 | 3,022 | 158 s |
| slim | 0.300 | 66.9% (55.5/83) | 1.12 | 982 | 57 s |
| slim + north_star in code | 0.239 | 63.3% (52.5/83) | 1.01 | 885 | 53 s |

Without the five London-hybrid offers (see "Label conflict" below):

| arm | rho | pair accuracy | MAE |
|---|---|---|---|
| baseline | 0.686 | 87.2% (41/47) | 0.98 |
| slim | 0.610 | 83.0% (39/47) | 0.95 |
| slim + north_star in code | 0.554 | 79.8% (37.5/47) | 0.79 |

**Reading it.** Every move is a single integer on a single offer:
- slim: #39 (label 4) `cv_match` 5 → 4, score 5.0 → 4.6. It now sits under #35
  and #40 (label 3, both 4.7), which is the whole −2 pairs. Nothing else moved.
- north_star in code: the moves the lookup itself made all went toward the
  labels — #32 (4) 3.0 → 3.4, #51 (3) 1.4 → 2.0, #86 (3) 3.7 → 3.4. The loss is
  #36 (label 4) `cv_match` 5 → 4 (4.8 → 4.5), a flip from the changed schema
  rather than from north_star, plus #51 now tying the London offers at 2.0.
- MAE improves at every step, because both flips move a label-4 offer from 5.0
  or 4.8 toward 4.

At n=18, 2–5 pairs out of 83 is one or two offers changing places at the top of
the scale, where `cv_match` is 5 on 10 of 18 offers and two label-3 offers sit at
4.7. The ranking there is decided by coin flips among 4.5–4.8 scores, so this
sample cannot tell these arms apart. It can rule out a collapse, and there was
none. The report is 55% smaller (332 KB → 149 KB over the 18).

**Label conflict.** Offers 34, 37, 42, 43 and 56 are London hybrid. The location
cap caps them at ~2.0 because `profile.yml` says a weekly London commute is a
hard no, but the labels predate that policy and rate four of them 4. Every arm
pays the same penalty, so arm comparisons are fair, but absolute rho is
understated: without them today's baseline (0.686, 41/47) is within one pair of
August's (0.733, 42/47). Relabel them, or relax the cap if London hybrid is
acceptable after all.

**Next problem this exposed:** saturation at the top of the scale. Fixing it
(the model's `cv_match` barely discriminates; coverage does the spreading) is
worth more than anything in this table.

## 1b. north_star from the profile, not the model

The user's premise was that north star needs web search. It does not:
`p2-judgment.md` defined it as fit to the target archetypes in the profile. The
web-search references are in `modes/_shared.md`, for comp and legitimacy in the
interactive modes, not the batch pipeline.

It was still a weak dimension, for a different reason: the model was doing a
lookup. Per the `profile.yml` comment, over 167 enum-era scores every `primary`
archetype scored 5 and `secondary` scored 3 in 11 of 16.

**What changed.**
- `internal/config`: `profile.yml`'s `fit` is now parsed. `Config.NorthStar`
  maps primary 5, secondary 3, adjacent 2, anything not listed (including
  "Outside targets") 1. An archetype with no tier counts as primary. With no
  archetype list at all it returns a neutral 3. Tested in `config_test.go`.
- Stage 3's `archetype` is now an enum of the profile's archetypes, the same way
  Phase 1 constrains it, because a lookup needs the exact label. `north_star` is
  no longer in the schema. The seniority, language, location and multi-posting
  caps still apply after the lookup.
- The prompt tells the model to pick the archetype by the work the role does, and
  to keep "Outside targets" for genuinely different work.

**Follow-up.** Phase 1 still asks its 4B model for `north_star`. Moving it to the
same lookup needs its own Phase 1 benchmark (`batch/PHASE1-EXPERIMENT-LEDGER.md`).

## 2. Measuring the skills block in the posting's spelling

`go run ./cmd/gate skills <home>` scores, over every cached JD in
`<home>/batch/jds/`, the cv.md skill items a posting names in any approved
spelling, and how many the skills block prints in a spelling the posting used. It
scores each posting twice, with and without aliases, so the comparison is
paired. It also lists any alias key that matches no cv.md item.

Why a new measure: the Node harness's `skill_coverage` only knows cv.md's own
strings. "RESTful" against a CV that says "REST APIs" is not a miss there; it is
absent from the denominator (`CV-GENERATION-BACKLOG.md` §8 hit the same blind
spot).

## 3. Approved skill aliases

**Format** (`config/skills.yml`, gitignored like `profile.yml`; rules in
`config/skills.example.yml`):

```yaml
aliases:
  REST APIs: [RESTful APIs, RESTful, REST API]
  PostgreSQL: [Postgres]
  Go: [Golang]
```

**Behaviour** (`phase3.SelectSkills`): an item the posting names only by an alias
counts as named (tier 1, so it is kept and ranked) and prints as the posting
wrote it. The longest matching form wins. If the posting also uses cv.md's own
spelling, that one stays. With nil aliases the selector is unchanged.
`gate p3` no longer compares the skills block at all (§3b).

**Measured** over 351 cached JDs with the first nine aliases:

| | without aliases | with aliases |
|---|---|---|
| named in cv.md's spelling, carried | 1,830 / 1,830 | 1,830 / 1,830 |
| named only by an alias, carried | **0 / 48** | **48 / 48** |

46 of 351 skills blocks change. All 46 were read: Golang 10, Postgres 10,
RESTful/RESTful APIs 16, NodeJS 4, REST API 2, OAuth2 2, NextJS 2, K8s 1,
SpringBoot 1. Extra items such as PKCE/DPoP after OAuth2 come from the existing
"related" tier (cv.md writes them on the same line), which is intended.

Prevalence in the cached JDs, for choosing what to alias next (case-sensitive
counts of postings): RESTful 20, Postgres 10, Golang 13, IaC 9, JS 11, GenAI 10.

**Rules that matter:**
- An alias replaces the whole item. Do not alias a compound item:
  `Terraform / Infrastructure as Code` → `IaC` would drop Terraform. Split it in
  cv.md first. That is the only thing blocking `IaC` today.
- No ordinary English words as aliases. Bare `REST` matches "the rest of the
  team"; `Next` matches "next steps".
- The ` / ` notation in cv.md cannot double as alias syntax: `TypeScript /
  JavaScript`, `HTML / CSS` and `SAST / DAST` are two real skills each.

Bullets were deliberately left alone. A recruiter's full-text search, which is
the filter the research points at, finds the term in the skills block. Changing
bullet text costs grounding for little search value; see §5.

## 3b. Whole-form naming and the block limits

The limits did nothing on their own. `SelectSkills` counted an item as named when
it shared one token with the posting: "engineering" named "prompt and context
engineering" in 241 of 351 cached postings, "design" named System Design. Named
categories are never cut, so nearly every category survived any cap.

An item is now named only when the posting contains one of its `SkillForms`
whole: the item, either side of a spaced ` / `, or an approved alias. Token hits
still rank items inside the tier, floored at 1 so "AI" (under the tokenizer's
three characters) is not lost. Measured with `gate skills . --sweep` over the 351
cached postings, aliases on:

| naming, cats/items/floor | named kept | categories | items | lines (est.) |
|---|---|---|---|---|
| token overlap, 6/12/3 (before) | 98.5% | 7.3 | 37 | 9.8 |
| whole form, 6/12/3 | 100% | 6.0 | 26 | 6.4 |
| **whole form, 5/7/3 (ships)** | **99.8%** | **5.2** | **22** | **5.3** |
| whole form, 5/7/5 | 99.8% | 5.2 | 28 | 6.2 |
| whole form, 4/7/3 | 99.8% | 4.6 | 20 | 4.7 |

The floor of 3 was chosen over 5: the goal's "5–7 items" read literally adds
about six items per CV that the posting never mentions. 5/7/3 drops 6 of 2,574
named mentions, all DevOps items on postings that name eight or more from one
category. 16% of postings name more than five categories and keep them all.

The Node selector still names by token overlap at 6/12/3, so `gate p3` stopped
comparing `skills`; experience, projects and modules are still diffed.

**Singular and plural.** The sweep defined "asked" by the forms cv.md already
knows, so it could not see what whole-phrase naming lost to spelling. Step 4's
first probe did: "code reviews" no longer named Code Review in 51 postings,
"event-driven architectures" missed Event-Driven Architecture in 21. Each form
now also matches its other number (`otherNumber`: a lowercase final s dropped,
an s added only to phrases of two words or more), and the page prints the
number the posting used. That names 82 more mentions, all real, with no change
in block size. Differently worded matches (Kafka for Apache Kafka, mentorship
for Mentoring) are left to step 4.

## 4. Embedding-suggested aliases (next)

Hand-writing aliases does not scale. The plan is to let embeddings propose them
and the user approve them once, so the approval cost is per pair, not per JD.

**Measured probe** (`snipe-embed`, 12 true alias pairs vs 12 close-but-different
pairs):

| pair | raw | with Qwen3 instruction prefix |
|---|---|---|
| REST APIs ~ RESTful (alias) | 0.832 | 0.911 |
| Go ~ Golang (alias) | 0.880 | 0.812 |
| AWS ~ Azure (different) | 0.875 | 0.725 |
| PostgreSQL ~ MySQL (different) | 0.861 | 0.609 |
| React ~ React Native (different) | 0.756 | 0.867 |
| Java ~ JavaScript (different) | 0.721 | 0.739 |
| different pairs above the weakest alias | **10 / 12** | **2 / 12** |

Prefix used: `Instruct: Given a technical skill, retrieve the same skill written
differently\nQuery: `. Without it no threshold separates aliases from different
tools. With it most do separate, but React ~ React Native still beats several real
aliases. **So embeddings propose, and the user accepts.** Nothing auto-applies.

**Design.**
- Source of posting terms: the stage-1 `keywords` and `tech_stack` (already
  extracted; reports carry them under `## Keywords`). No new LLM call.
- For each posting term that is not a cv.md item or an approved alias, find the
  nearest cv.md item by cosine, with the prefix on the query side only (per the
  Qwen3 docs). Above a threshold set from the probe, it becomes a proposal.
- Search only over the user's own items. A wrong neighbour then cannot invent
  anything; the worst case is printing a true skill that does not help. The
  dangerous decision is "is Azure an alias of AWS", which is exactly what the
  user approves.
- Proposals go to a review queue. An approval appends to `config/skills.yml`; a
  rejection is remembered so it is not proposed again.
- Bootstrap over the ~350 cached JDs once, then run incrementally.
- No vector DB: ~150 items is microseconds of brute force, and `internal/embed`
  already has `Cosine`, `TopK` and a JSON cache.

**Constraints.** Embedding calls time out while the 30B is benchmarking (GPU
contention, `batch/labels/LEDGER.md`), so run it when nothing else is.

**Open: the review UX.** It must stay a TUI. Proposed shape: Phases 1–2 stay
headless; the review queue is a TUI screen worked through when convenient; Enter
accepts. `charm.land/bubbles/v2` (list) is already a dependency. To be discussed
before building.

**Separate experiment, not part of this:** the pipeline sends no instruction
prefix anywhere, including Phase 2 evidence retrieval and Phase 3 bullet ranking.
Adding it there would move measured constants (`topKCandidates`, the spike
background), so it needs its own retrieval benchmark.

## 5. Keyword edits inside bullets (maybe never)

The user's original idea was to replace the bullet bank with detailed project
descriptions and have the model write bullets in the posting's words. The
evidence is against free generation:

- In this tree (`PHASE3-RETENTION-LEDGER.md` §4.1–4.3): deleting the 7B rewrite
  *raised* keyword coverage (+0.061) and took grounding to 1.000; 78% of its
  bullets were the source line retyped. A 9B and the 30B gained ~0.05 on
  differentiators but lost keyword coverage (the 9B lost on 27 offers and won on
  2, the 30B lost 25–7) and lost grounding on every offer they changed. The Opus
  variant bank gained nothing and lost a blind preference 22–8.
- Outside (Takano 2026, arXiv 2608.26171): 96.7% of fully automated resume
  outputs carried an unsupported claim. Prompt guardrails cut findings 86% but
  half of outputs still had one. A human review step caught every obvious
  fabrication but only 54.5% of subtle ones. A person picking from generated
  options cannot replace the deterministic guards.

**If it is built,** it is a constrained edit, not generation:
- Only the ~8–10 selected bullets; at most 1–2 posting terms added per bullet.
- The allowed terms are those the bullet's own project evidences. Detailed
  project descriptions supply that evidence. They belong in `article-digest.md`,
  which `embed.ExtractAtoms` already reads and which never reaches the PDF.
- Code checks the diff: every added word must be in the allowed set, and figures
  and product names must be unchanged (`internal/guards`).
- The TUI shows original against edited; original is the default.
- Estimated ~30 s per offer on the 30B.

**Measure before building.** Extend `gate skills` to read the selected bullets
too. If the skills block already carries the posting's terms, bullet edits buy
little search value and cost grounding.

## 6. A model arm: Qwen3.5-35B-A3B

Released Feb 2026; hybrid Gated DeltaNet + attention MoE, 35B total / 3B active;
vendor card IFEval 91.9, IFBench 70.2. 22 GB in Ollama against 18 GB for the
current 30B. That fits in 30 GB RAM + 6 GB VRAM, but tightly.

- It thinks by default. The Go Ollama client sends no `think` flag. The Node
  client learned this when `qwen3.5:9b` returned 32 of 32 empty responses
  (`PHASE3-RETENTION-LEDGER.md` §4.5). Add `think: false` first.
- Run it as a Phase 2 arm against the labels, never as a blind swap. Config
  changes alone have moved answers here.

## Time budget, projected

| | before (measured) | after steps 1–3 |
|---|---|---|
| Phase 1 | 18 s | 18 s |
| Phase 2 | 230–298 s | ~135 s (judgment measured, the rest unchanged) |
| Phase 3 | ~8 s | ~8 s (+~30 s if §5 is built) |
| **total** | **~4.3–5.4 min** | **~2.7 min** |

The 230 s is today's 18-offer bench, the 298 s the August pipeline average;
they differ by sample.

## Not building, and why

- **Web search for north star.** It was never a search question (§1b), and it
  would add 30–90 s for something that does not affect the CV.
- **A vector database.** ~150 items; brute force is microseconds.
- **LLM-chosen skill categories.** `SelectSkills` is deterministic and already
  at `skill_coverage` 1.000. The limits live in code over fixed categories
  (§3b). A model brings back the random-or-overconfident behaviour the user
  wanted supervised.
- **Free bullet generation from project descriptions.** §5.
- **More bank variants.** Measured negative (`PHASE3-RETENTION-LEDGER.md` §4.3).

## Found on the way

- `internal/providers/apify_test.go:75` hardcoded 2026-08-26 as "this week" and
  had failed since that date passed. It compares against today now.
- `batch/bench-tools/rho.mjs` referenced an undefined `dirs` and crashed from the
  command line. Fixed.
- `scripts/gate-phase2.sh` now expects `p2.judgment`, the eval and the report to
  differ from the Node oracle. `p2.jdparse` and `p2.evidence` requests must still
  match.

## How the Phase 2 arms are benchmarked

Each arm gets its own fixture: a code root holding a frozen binary plus copies of
`prompts/` and `templates/` (the binary resolves prompts from its own directory,
so editing the repo mid-run cannot leak in), and a `SNIPE_HOME` with `cv.md`,
`config/`, the 18 labelled JDs, and `batch/state/` rows marked `scored`.

```bash
SNIPE_HOME=<arm>/home SNIPE_NO_RAG=1 <arm>/code/snipe run \
  --skip-phase1 --skip-phase3 --p1-threshold 0
node batch/bench-tools/rho.mjs <baseline>/home/batch <arm>/home/batch
```

To rerun only the judgment, seed an arm's `batch/work/<id>.json` from the
baseline's checkpoints with the `p2.judgment` entry removed from `stages` and
`inputs`. Stages 1–2 are byte-identical at temperature 0, so that saves ~80 s per
offer without changing anything measured. Timings land in
`<arm>/home/batch/cache/timings.tsv`.

## Research notes

- **ATS matching.** Vendors publish little, and most "ATS secrets" articles come
  from resume-tool vendors. Documented: Greenhouse recruiters search with
  keywords marked Required (AND) or Preferred (OR), plus full-text search.
  Workday's Skills Cloud groups synonyms (20+ per common skill). An Enhancv study
  of 25 recruiters (vendor-sponsored, small) found 92% say their system does not
  auto-reject. The realistic filter is a person searching for the posting's own
  words, so print the posting's form and spell out an acronym with its long form
  once.
- **LLM judges.** Surveys support splitting a judgement into concrete checks.
  Stage 2 already does (`same_activity` / `same_tooling`). The idea that several
  scores in one prompt contaminate each other is plausible but has no
  peer-reviewed support found.
- **Prompt format.** He et al. 2024 found format sensitivity of up to 40% for
  weaker models. When a subset of `skills.yml` is ever shown to a model, keep the
  format the benchmark used.
- **Taxonomies.** Lightcast's free tier is limited to non-profit and public-good
  use with 50 extractions a month. ESCO is free but weak on technology spellings.
  A personal alias file is the better fit at this scale.

### Sources

- [Enhancv recruiter study coverage (IT Brief UK)](https://itbrief.co.uk/story/study-reveals-ats-rarely-auto-rejects-cvs-debunks-75-myth)
- [Greenhouse Talent Filtering keyword search](https://www.greenhouse.com/product-features/greenhouse-talent-filtering-keyword-search)
- [Greenhouse support: search resumes for keywords](https://support.greenhouse.io/hc/en-us/articles/115004600186-Search-resumes-for-keywords)
- [Workday Skills Cloud foundation](https://blog.workday.com/en-us/2020/foundation-workday-skills-cloud.html)
- [eWeek on Workday Skills Cloud synonyms](https://www.eweek.com/cloud/workday-debuts-a-universal-skills-ontology-called-skills-cloud/)
- [Takano 2026, Mitigating Fabrication in Multi-Stage LLM Pipelines for Hiring](https://arxiv.org/abs/2608.26171)
- [Indukuri & Agrawal 2026, Grounded Optimization](https://arxiv.org/abs/2607.01457)
- [A Survey on LLM-as-a-Judge](https://arxiv.org/pdf/2411.15594)
- [Castillo: structured output field order](https://dylancastillo.co/posts/llm-pydantic-order-matters.html)
- [CO/AI: field order may not improve reasoning](https://getcoai.com/news/why-field-order-may-not-improve-model-reasoning)
- [He et al. 2024, Does Prompt Formatting Have Any Impact on LLM Performance?](https://arxiv.org/html/2411.10541v1)
- [Qwen3 Embedding technical report](https://arxiv.org/html/2506.05176v1)
- [Qwen3.5-35B-A3B model card](https://huggingface.co/Qwen/Qwen3.5-35B-A3B)
- [Lightcast free API access](https://docs.lightcast.io/lightcast-api/docs/free-api-access)
- [Skill Extraction from Job Postings using Weak Supervision](https://arxiv.org/pdf/2209.08071)
