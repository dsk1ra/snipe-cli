# PRD: Adapt the CV's own lines to each posting (tailoring rework)

**Status: proposal, 2026-10-21. Nothing here is built.** Numbers marked
*measured* come from this tree or its ledgers. Numbers from outside sources are
cited. Everything else is an estimate and says so. The usual rule for planning
documents in `docs/` applies: Check the ledgers before trusting a number here.

---

## 0. The short version

Phase 3 picks bullets out of `cv.md` and prints them word for word. The only
part that adapts to the posting is the skills block, through per-term aliases
in `config/skills.yml`, and the uncommitted `snipe suggest` work extends the
same idea to swapping up to three words inside a bullet. Neither can change
which part of the work a line leads with, or how the work is framed.

This PRD proposes replacing term substitution with constrained line
adaptation:

1. A fact ledger, built once per `cv.md` by the local 30B and approved by you
   in the TUI, holds every claim the CV makes, entry by entry, plus the
   generalisations ("framings") you are willing to defend in an interview.
2. For each posting, code builds a rewrite plan for every selected bullet: The
   requirements it should evidence, the posting terms it may use, the figures
   and qualifiers it must keep, and the rendered lines it may cost. On an offer
   that scores high enough, posting tools the CV never mentions become proposed
   additions, and none of them prints until you approve it.
3. The 30B writes two drafts per bullet against that plan.
4. A verifier from another model family (Bespoke-MiniCheck-7B on Ollama)
   checks each draft in both directions. Everything the draft says must be
   supported by that entry's facts or by an inference you approved for that
   entry, and every must-keep fact must still be said. Code checks figures,
   entities and line cost before that.
5. Code picks the draft that evidences the requirements most plainly. The
   original line wins every tie, so the worst case is today's CV.

**Truth contract.** Per role, at least 80% of claims are supported by `cv.md`.
Up to 20% may be inferred from the posting itself: Tools and practices the
posting names and the CV never mentions, proposed only when the fit score makes
it likely you used them, and printed only after you approve each one. Any other
claim is dropped, and no employer, title, date, degree, certificate or figure
is ever added.

The metrics change with it. `grounding` and `ats_coverage` reward copying by
construction. They give way to attribution, preservation, supported-term
parity, and an ATS simulator modelled on what Workday and Greenhouse document
about their own matching. Phase 2's fit score moves onto the same ledger.

Hand-written vocabularies (176 product names, 12 ecosystem and 14 tool
patterns, 29 cities, 20 languages, the attendance and seniority regexes) are
replaced by model extraction that must quote its source, checked by code. Each
runs in shadow until the replacement agrees with it or beats it. Measured
constants stay where they are.

---

## 1. The task

**Given** one pasted posting, `cv.md`, `article-digest.md` and the profile,
**produce** a one-page PDF where:

- **T1 · The lines are yours, adapted.** Every experience and project bullet is
  a `cv.md` bullet, shipped verbatim or rewritten for this posting. A rewrite
  uses the posting's words for skills you have, moves the relevant part of the
  work to the front, and frames the outcome the way this employer measures
  things. It never copies the posting's sentence structure.
- **T2 · Truth per role.** For each employer and each project, at least 80% of
  the claims on the page are supported by that entry in `cv.md`. The rest may
  be inferred from the posting, and only after you approve them. Hard
  invariants: No new employer, title, date, degree, module, certificate or
  figure, and no claim moves from one entry to another.
- **T3 · Evidence for the requirements.** Every must-have the CV genuinely
  meets is evidenced by at least one line that a keyword search, a semantic
  matcher and an LLM grader would each recognise. Requirements you do not meet
  stay unmet on the page.
- **T4 · It reads like you.** Lines carry no keyword stuffing, no appended
  "directly applicable to…" clauses, and no vocabulary the rest of the CV would
  not use.
- **T5 · Budget.** At most 6 minutes per posting end to end on the RTX 3060
  Laptop (6 GB VRAM, 30 GB RAM), the budget `REWORK-PLAN.md` set. The pipeline
  measures roughly 2.7–5.4 minutes today, so Phase 3 has about two minutes to
  spend.

**Out of scope.** Cover letters, hidden text or anything else aimed at a parser
instead of a reader, education and certificates beyond the existing module
pick, the scoring weights (they need labels), and the scanner.

### 1.1 What "at least 80% true" means here

Confirmed 2026-10-21. At least 80% of the claims about each role must be
supported by the master CV. Up to 20% may be inferred from the posting, because
the CV leaves things out: When an offer scores high, a tool the posting lists
is likely one you have used and never wrote down. You approve each such
addition by hand.

| class | meaning | budget per role |
|---|---|---|
| **supported** | entailed by that entry's facts in `cv.md` or `article-digest.md`, including framings you approved ("10 services talking only through a Kafka log" ⇒ "event-driven microservices") | ≥ 80% of claims |
| **inferred** | taken from the posting, absent from the CV, proposed only on a high-scoring offer, and approved by you for this entry | ≤ 20% |
| **unapproved** | neither of the above | 0 |

A claim is one checkable proposition, the unit FActScore and Claimify both use.
An inference attaches to one entry ("Kubernetes at UBWIS"), so approving it
cannot spread the tool to every role on the page. Employers, titles, dates,
degrees, modules, certificates and figures are never inferred.

A posting term is proposed only when three conditions hold:

1. **The offer scores high enough.** `infer_min_score` in `profile.yml`,
   default 4.0 on Phase 2's scale. That default is a guess; E7 (§8.2) tunes it.
2. **The term is a tool or practice.** Stage 1's typed parse marks it `tool` or
   `capability` (§5.2). Domains, credentials and eligibility are never inferred.
3. **An entry does the same work.** Stage 2 graded the requirement same
   activity, different tooling (Transferable) against that entry, and the
   inference attaches there. "Kubernetes" then lands on a line about deploying
   a platform and never on one about billing. A term with no such entry can only
   go to the skills block, where the same 80/20 rule applies to its items.

Approvals are stored per fact in the ledger (§6.1) and reused on later postings
without asking again; each use still counts toward the 20%. An inference you
approve on several postings is a line `cv.md` is missing, and the review screen
says so.

Takano (2026) found plausible new claims were the category a human reviewer
missed 45% of the time (§3.4). Approving your own history is an easier job than
checking a stranger's resume, but the fatigue is the same, so §6.6 shows one
claim at a time with reject as the default.

---

## 2. The research goal

Set before any searching, so the findings answer one question:

> **Find a way to tailor that changes how existing CV lines are worded and
> ordered for one posting, measurably improves how ATSs, AI screeners and
> human skimmers read the CV, keeps every claim provable from the source CV,
> and runs on a 6 GB GPU with as few hand-written rules as possible.**

| # | question | what it decides |
|---|---|---|
| RQ1 | How do current ATSs match a CV to a posting: keywords, synonyms, embeddings, LLM grading? | what "ATS-optimised" should optimise |
| RQ2 | Does wording change AI screeners' decisions when the facts are held fixed? | whether rewriting is worth its risk |
| RQ3 | How do LLM resume rewriters fabricate, and which defences measurably work? | the verifier design |
| RQ4 | What can check truth locally on 6 GB? | the verifier model |
| RQ5 | How should fit and ATS match be measured without labels that rot when `cv.md` changes? | the metric suite (the current labels have rotted, §4) |
| RQ6 | Can model extraction plus verification replace hand-written vocabularies? | the hardcoding plan |

---

## 3. Findings

### 3.1 How ATSs match (RQ1)

- **Greenhouse.** Recruiters search resumes with keywords marked Required
  (AND) or Preferred (OR), plus full text. Its newer Talent Matching scores
  candidates against a recruiter's calibration of skills, experience and job
  titles, each weighted. The support pages say "multiple terms can map to the
  same calibrated skill" and "a longer list of matched terms doesn't always
  mean a higher match score". Extraction runs on "a series of fine-tuned LLM
  models", and skills and titles are matched through an "embedding
  representation … so that we can run a semantic search on them", highlighted
  "either as an exact match or a semantically similar match".
- **Workday HiredScore.** Grades candidates A–D against the posting's basic and
  preferred qualifications. Since the release of 5 August 2026, an LLM
  "evaluates each screenable qualification individually against the
  candidate's parsed resume", giving an explanation and "pointers to relevant
  resume sections" for each conclusion. Meeting every basic qualification puts
  a candidate in A or B, and preferred matches separate A from B. Skills Cloud
  input to the grade is announced for a later release.
- **Automatic rejection is rare.** An Enhancv survey of 25 recruiters
  (vendor-sponsored, small) found 92% say their system does not auto-reject.
  The filter that matters is a person reading a ranked or searched list.

Both systems grade per requirement and look for evidence, with semantic
matching for synonyms, so repeating a term buys little in either. A line that
plainly demonstrates a requirement, in words close to the posting's, is what
earns the grade. Exact spelling still matters for a recruiter's boolean search,
so a skill you have should appear once in the posting's form. The rewrite
therefore has three goals, in order: Explicit evidence per requirement, then
the posting's term for skills you have, then everything else.

### 3.2 Wording moves AI screeners (RQ2)

- Chen & Xiao (2026) held competence fixed and varied wording, structure and
  polish. Llama-3.1-8B reversed 29.6% of matched pairwise decisions,
  Mistral-7B-v0.3 41.4%, and the most valid screener in the study was still
  unstable.
- Xu, Li & Jiang (2025) found LLM screeners prefer resumes written by the same
  LLM, 67–82% of the time against human-written ones in the arXiv abstract (the
  range differs between versions). Candidates whose resume came from the
  evaluator's own model were 23–60% more likely to be shortlisted.
- Castleman et al. (2026) found many LLMs fail to pick the more qualified of
  two directly comparable resumes consistently.

These results support the premise of the rework. AI screeners respond to
wording, so verbatim lines leave something on the table. The effects are
model-specific and unstable, which makes any one screener's taste a poor
target; the design aims instead at explicit evidence per requirement, the thing
Workday's documented grader checks (§3.1). For measurement, the judge is never
the model that wrote the text, and every pairwise judgement runs in both
orders.

### 3.3 Human skim and recruiters' view of AI text (RQ2)

- The Ladders eye-tracking study (2018) measured an initial screen of 7.4
  seconds and found layouts with bold titles and bulleted accomplishments,
  read in an F- or E-pattern, did well. Susan Gygax (ERE, 2020) points out
  that the report does not state the roles, the resume lengths or how many
  recruiters took part, and calls the figure "no more meaningful than a random
  number". The design relies only on the reading pattern, in which the start
  of each bullet is seen first.
- Hiring-manager surveys, all run by resume vendors with different samples and
  wording: Resume Now (925 US HR professionals, March 2025) found 62% say
  AI-generated resumes without customisation are likely to be rejected, and
  36% list generic content as a top reason for rejection. TopResume (600
  hiring managers, May 2025) found 19.6% would reject a candidate with an
  AI-generated resume. AIResumeBuilder (929 hiring managers, October 2025)
  found 61% say AI resumes often make candidates look more qualified than they
  are.

The complaints are about generic text and inflated qualifications. In
TopResume's survey, 52% accepted AI help with proofreading or drafting but
wanted a person to produce the final document, which is an argument for the
review screen in §6.6. The rewrite should front-load the requirement, keep the
CV's voice and never add a claim.

### 3.4 How rewriters fabricate (RQ3)

- **Takano (2026).** Resume rewriting against JDs with a frontier model: 96.7%
  of fully automated outputs carried at least one unsupported claim. The
  categories were invented identity, hits on a list of plausible-but-absent
  items (tools, certificates, domains), dropped qualifiers ("basic",
  "prototype"), altered scope or metrics, and new unsupported claims. A
  guardrail prompt cut findings by 86%, yet half of outputs still had one,
  mostly new unsupported claims. A human checkpoint removed every invented
  identity but only 54.5% of dropped qualifiers and new claims. Claim retention
  stayed above 99%. The authors ask for automated diff alignment against
  source claims and for qualifier-preservation checks.
- **Indukuri & Agrawal (2026).** Five layers for resume rewriting: temporal
  validation, deterministic contamination detection, structural invariants,
  prompt grounding and an evaluator agent. Undefended baselines produced
  2.48–5.36 detected hallucinations per resume. Prompt grounding alone reached
  zero at low temperature with a capable model; weaker models and higher
  temperatures needed the deterministic layers.
- **This repository (measured).** The 7B rewrite retyped 78% of bullets
  verbatim, and the 22% it changed held the fabrications. Only 44% of source
  figures survived its rewrites. Figures moved between entries
  (`PHASE3-GENERATION-LEDGER.md` §10). A summary shown the posting invented a
  domain, a language or a figure on 8 of 32 offers. A 9B and the 30B
  paraphrased more and lost token-overlap grounding on every offer they
  changed.

Both papers and the ledgers point the same way. A prompt reduces fabrication
without ending it, reviewers miss the subtle categories, and the defences that
held were checks comparing the output with its source. Dropped qualifiers need
a check of their own because a reviewer catches only about half of them, and
this CV has several that matter: "tested at 250M+ *simulated* authentication
events", "*Supervised* Project", "working knowledge".

### 3.5 Checking truth locally (RQ4)

| option | what it does | fit for this machine |
|---|---|---|
| **Bespoke-MiniCheck-7B** (MiniCheck, Tang et al. 2024) | document + one sentence → supported yes/no; state of the art on LLM-AggreFact per its card | on Ollama as `bespoke-minicheck` (q3_K_M 3.8 GB, q5_K_M 5.5 GB); talks to the existing client; CC BY-NC 4.0, fine for personal use and not to be bundled |
| Granite Guardian (IBM) | groundedness of an answer against documents | on Ollama; Apache 2.0; check which generation a tag pulls, and the yes/no polarity of its groundedness label |
| LettuceDetect (ModernBERT) | token-level unsupported spans | Python only, which would bring back a runtime the project removed when it moved to Go |
| the 30B itself | schema-constrained grading | no new model, but self-preference and same-family blind spots |
| Qwen3-Reranker | relevance ranking | llama.cpp only; Ollama has no documented rerank endpoint (request open as issue #4510) |

MiniCheck's card asks for multi-sentence claims to be split into sentences
first. A CV bullet is one long sentence with several clauses, so whether a
whole-bullet check catches one bad clause is an open measurement (E2, §8.2),
and claim decomposition is the fallback. RARR (Gao et al. 2023) names the two
directions this needs: *Attribution* (everything said is supported) and
*preservation* (the revision keeps what the original said).

A modest judge on this hardware should only get questions with a checkable
answer, with code combining the answers. Phase 2 already works this way. Its
two narrow evidence questions replaced a single Strong/Partial/Gap label after
that label let a Databricks role score 5/5 against a CV with no Databricks on it
(`fit.StrengthFrom`).

### 3.6 Vocabularies versus extraction (RQ6)

- A 2025 study of text encoders for labour-market analysis (arXiv 2505.24640)
  reports LLMs at the state of the art for extracting skills from postings and
  weaker at normalising them to a taxonomy. SkiLLMo (Malandri, Mercorio &
  Serino, 2025) reports 91% precision extracting, 80% standardising and 79% end
  to end. ESCO is free but weak on technology names, and Lightcast's free tier
  is restricted (`REWORK-PLAN.md`).
- Phase 1's `verifyGaps` already shows how to replace a regex list without
  trusting a model blindly. It keeps a model's gap only when the gap's verbatim
  quote is a substring of the posting. The model can copy a span but cannot
  reliably invent one that survives the check.

For a single candidate, the CV is the taxonomy that matters. A list of what the
CV claims, built once and approved, covers every product a posting might name.
`NamedProducts` (176 entries, seeded from nine real violations) covers only the
products someone thought to add.

### 3.7 Judges

Shi et al. ("Judging the Judges") measure position bias in pairwise LLM judges
as position consistency: Whether a judge picks the same winner after the two
answers swap places. Every pairwise judge here runs both orders and scores a
disagreement as a tie. This repository has measured that the 30B judge is
worthless alone and useful blended (+0.115 pair accuracy held out), that
dropping its per-item ids cost 0.052, and that 0-shot it is worse than no
judge. Every judge in this design feeds a score with a measured weight.

---

## 4. Why the current metrics mislead

The suite was built to catch a model that invents things, and it does. Its
blind spot is the one you suspected. Every metric that reads wording measures
resemblance to `cv.md`, so it cannot reward adaptation and always scores
verbatim as optimal.

| metric | what it rewards | why it misleads for this rework | proposal |
|---|---|---|---|
| `grounding` | token overlap between an output bullet and its source | verbatim scores 1.000 by construction; a faithful rewording loses; a fabrication that reuses the source's tokens passes | replace with attribution (§8) |
| `ats_coverage` | 3+ character tokens shared by the JD and `cv.md` that reach the page | counts generic English (top misses: *complex*, *location*, *fast*); its ceiling is `cv.md`'s own vocabulary, so rewording into the posting's term for a real skill cannot raise it | replace with supported-term parity over typed posting terms |
| `skill_coverage` | named skills in `cv.md`'s spelling that reach the page | blind to alias-only mentions (fixed for the skills block only, in `gate skills`) | keep, over ledger forms |
| `differentiator_coverage`, `grade_yield`, `noise_rate` | selection, against 128 Opus labels | labels key atoms by position, and 0 of the 33 label atoms match the current `cv.md` (checked 2026-10-21), so none of these can be computed on the CV that ships | re-key by fact hash, relabel only what changed |
| `product_fab`, `metric_fab` | products or figures absent from `cv.md` | an open list of 176 products; blind to domains, credentials, qualifiers, and any product spelled outside the list | replace with entity closure against the ledger |
| Phase 2 `cv_match` | the 30B's integer | saturates at 5 on 10 of 18 labelled offers (`REWORK-PLAN.md` §1), yet it is 60% of `cv_blended` | demote; band by must-haves (§5.2) |
| Phase 2 coverage | requirement → top-3 bullets by cosine → two-axis grade | whole bullets are coarse retrieval units. In report 343 (Palantir), "Contributions to open-source projects" matched the Zero Trust project as Transferable while the CV labels Snipe *Open Source*, and "Proficiency in Java, Rust, Scala, C++ or C" got the Skills catalogue line, capped at Transferable, though Java and Rust are demonstrated in two projects. Coverage came out at 57% on a near-complete must-have match | align against ledger facts |

Two more findings from reading the code:

- **The Phase 3 judge rerank is not in the Go port.** `cvselect.JudgeBlend`
  (`internal/cvselect/allocate.go:101`) is defined and never used, and Phase 3
  ranks by cosine minus the spike baseline. The README still says bullets are
  "reranked by `snipe-eval`", and the ledgers price the judge at +0.115 pair
  accuracy. Port it or correct the README. The selection changes below need
  that baseline settled first.
- **Approved aliases bypass the skills guard.** `Params.Approved` appends every
  alias form to the text grounding checks against
  (`internal/phase3/ladder.go:144`), so an alias counts as CV content. Several
  approvals in the live `config/skills.yml` would print what `cv.md` does not
  claim: `Node.js: [..., NestJS]` (another framework), `STRIDE threat
  modelling: [Threat Intelligence]` (another discipline), `AI-assisted
  development: [..., OpenAI]`, `end-to-end testing: [end-to-end development]`.
  Others would replace a specific tool with a generic phrase:
  `Docker: [Containerization]`, `RabbitMQ: [message brokers]`,
  `Resilience4j: [resilience]`. A context-free substitution cannot tell these
  cases apart, so §6.1 moves approval from terms to facts.

The old numbers still hold for unconstrained rewrites, which dropped figures,
blended entries and invented domains. The design keeps every invariant that
caught those (figure retention, entry-scoped figures, revert to source) and
replaces only the metrics that cannot tell a faithful rewrite from a bad one.

---

## 5. Phase by phase

Each phase gets three parts: What it does now, its problems for this goal, and
the proposed changes.

### 5.1 Phase 1, pre-score (`internal/phase1`, `snipe-screen` 4B)

**Now.** Fetches and caches the posting. One schema-constrained 4B call returns
`cv_match`, `north_star`, the archetype, gaps with verbatim quotes, and
strengths. Code computes `0.625·cv + 0.375·ns` and applies four caps (stack,
natural language, location, seniority) from regexes in `internal/fit`.
Strengths naming a tool absent from `cv.md` are dropped against the `fit` tool
patterns. Gate at 2.5. *Measured* 18 s.

**Problems.** It is a cost gate, so its errors are cheap. The caps, though,
live in 59 `regexp.MustCompile` sites and four word lists in `fit.go`, and
several of them carry a comment naming the posting that broke the previous
version (Liverpool Street, "6 days a month", `C#.Net`). North star is still the
4B's guess here, while Phase 2 looks it up.

**Changes.**
- Ask the 4B for the facts the caps need, each with a verbatim quote: Office
  attendance (days per week, place), required natural languages, years
  demanded, seniority in the title. Code checks every quote by substring, as
  `verifyGaps` does, and applies `profile.yml`'s policy to the structured
  values. A quote that fails means the fact is unknown and the cap does not
  fire, which errs in the same direction the regexes do.
- Take north star from the profile's fit tier, as Phase 2 already does.
- Keep the regex caps as a shadow for one release. Log every disagreement,
  read them, then delete the lists.
- **Gate:** `gate fit` over the cached JDs, with every disagreement against
  today's decisions read by hand. The location cap fired on 49 of 310 cached
  postings when it shipped, a small enough set to read.

### 5.2 Phase 2, evaluate (`internal/phase2`, `snipe-eval` 30B-A3B)

**Now.** Stage 1 parses the posting into requirements with must-have flags, a
tech stack and 8–20 keywords. Stage 2 embeds each requirement, retrieves the
top three demonstrations plus one catalogue line, and asks the 30B two
questions per requirement (same activity? same tooling?); code derives the
strength. Stage 3 writes the judgment: `cv_match`, archetype, gaps, strategy
and a personalisation table. Code blends `0.6·cv_match + 0.4·(1 + 4·coverage)`,
applies the caps and computes the score. About 135 s after the slimming.

**Problems.**
1. Retrieval runs over whole bullets and catalogue lines, and §4 shows what it
   missed on report 343.
2. Stage 1's keywords are untyped. Nothing downstream can tell a tool from a
   capability from a soft skill, so the skills selector re-derives all of it
   with token rules.
3. The personalisation table is the only place the pipeline proposes adapted
   lines, and nothing reads it. Its proposals also break T4. Report 343's table
   appends "— directly applicable to query planning and execution optimization
   in data engines" to a real bullet.
4. `cv_match` saturates, so most of `cv_blended` carries little ranking
   information.

**Changes.**
- **Stage 1, typed.** Each requirement gets a `kind` (tool, capability, domain,
  credential, eligibility, soft), the posting's exact surface terms, and a
  verbatim quote checked by substring. Add a short list of emphasis signals
  (what the posting stresses: scale, ownership, a regulated domain, delivery
  speed), each quoted. Same call with a wider schema; estimated +10–15 s of
  output.
- **Stage 2, over the ledger.** Retrieve facts instead of bullets (shorter
  units, each tied to its entry), and keep the two-axis grading and the
  catalogue pool rule. Its output is the requirement ↔ fact alignment Phase 3
  needs anyway, so it is computed once. `topKCandidates` was measured on
  bullets and has to be re-checked on facts against
  `batch/bench/evidence-goldset.md`.
- **Banding the way the ATSs band.** Every must-have Strong or Transferable
  puts the offer in band A/B, and preferred coverage separates A from B;
  otherwise C/D. Map the bands onto the 1–5 scale and keep the caps. Report the
  banding beside today's blend against the 18 labels (pair accuracy, benchmark
  rule 3), and switch only when it wins.
- **Drop the personalisation table** once Phase 3 adapts lines under
  verification. That is about 2,100 characters of 30B output (`REWORK-PLAN.md`
  estimate), roughly 20 s at the measured 25.6 tok/s.

### 5.3 Phase 3, tailor (`internal/phase3`, `cvselect`, `fill`, `guards`)

**Now.** Embeds the Block B requirements and every bullet, and scores each
bullet by its best cosine minus its spike corpus mean. Spends a 22-line budget
across three projects and the experience entries (a floor of two on the top
employer, projects capped at 13 lines, pins honoured). Renders the selected
bullets verbatim. Picks skills deterministically (whole-form naming, 5/7/3,
posting spellings through aliases) and five modules by token overlap. Walks
the density ladder to one page. About 8 s, with no generation call.

**What stays.** The allocator, spike, pins, line budget and ladder are measured,
and they act on selection, which no rewrite can fix (the retention ledger's
loss buckets). The skills selector keeps its structure, and module selection
stays.

**What changes.**

| part | now | proposed |
|---|---|---|
| bullet text | verbatim | plan → two drafts → verify → select, with the original as fallback and tie-breaker (§6) |
| what bullets rank against | Block B requirement strings | the same, plus stage 2's alignment: a bonus for a bullet carrying a fact aligned to an otherwise uncovered must-have. An experiment, since reserved slots failed for differentiators (E4) |
| skill spellings | per-term aliases, context-free | ledger surface forms: the posting's spelling prints only when the ledger records it as the same entity, and a generic phrase never replaces a named tool |
| guards | 176-name product list, a cased list, numeric checks | entity closure against the ledger, numeric checks kept, MiniCheck for the rest |
| judge rerank | missing in Go | decide in M0: port it (it is measured) or document its absence |
| order of work | per offer: embed, then render | stage-major across the run: every selection (embedder), then every rewrite (30B), then every verification (MiniCheck), then every render. Each model loads once per run instead of once per offer, the lesson of the generation ledger's reload count |

### 5.4 Aliases and suggestions (`internal/aliases`, `internal/suggest`, uncommitted)

**Now.** Embeddings propose posting-term → CV-item aliases, you approve each
term, and the skills block prints the posting's spelling. `snipe suggest` asks
the 30B for word swaps (at most three new words, at most an eighth longer,
figures kept, one posting word added), vetted in code and reviewed in the TUI.

**Assessment.** Both work on the wrong unit. A term has no context, so one
approval applies everywhere, including where it is false (§4). A three-word
swap cannot move emphasis or framing, and the growth cap rules out
restructuring. The vetting rules are good, and most of them survive into §6.4
as code invariants. The TUI tailor screen is the right shape for reviewing a
rewrite against its original.

**Changes.** Keep the tailor screen and `snipe rebuild` and point them at the
adapted lines. Migrate the approved aliases into ledger surface forms, minus
the ones §4 lists. Retire `p3-suggest.md` and the alias-suggestion rebuild once
framings cover them; the suggestion queue becomes the framing queue (§6.1).

### 5.5 Measurement (`batch/*.mjs`, `cmd/gate`)

**Now.** `tailor-harness.mjs` and `opus-metrics.mjs` carry most of Phase 3's
measurement. `cmd/gate` covers parity with Node, `skills` and `goldset`. The
Opus labels are stale (§4).

**Change.** A Go `gate tailor` subcommand runs the suite in §8 on a fixed
sample, paired, with an A/A floor, over the text of the rendered PDF. It reads
the current `cv.md` and ledger, so editing a bullet does not void it.

---

## 6. The new tailoring design

### 6.1 The fact ledger

One file, `batch/state/cv-ledger.json`. It lives under `state/` because it
carries your approvals and is not safe to delete. It is the only place the
pipeline learns what you can claim.

```json
{
  "cv_hash": "…",
  "entries": [{
    "id": "de-store",
    "kind": "project",
    "title": "Distributed Store Management Platform",
    "qualifiers": ["Supervised Project"],
    "bullets": [{
      "hash": "sha1 of the bullet text",
      "text": "Built retail operations as 10 Java and Spring Boot services …",
      "facts": [
        {"id": "de-store/3a", "text": "Built 10 Java and Spring Boot services for retail operations",
         "entities": ["Java", "Spring Boot"], "figures": ["10"], "must_keep": true},
        {"id": "de-store/3b", "text": "Each service owns its own database"},
        {"id": "de-store/3c", "text": "The services talk to each other only through a Kafka log",
         "entities": ["Kafka"]}
      ],
      "framings": [
        {"phrase": "event-driven microservices", "from": ["de-store/3a", "de-store/3c"], "approved": true},
        {"phrase": "database-per-service", "from": ["de-store/3b"], "approved": true}
      ]
    }]
  }],
  "entities": {
    "Apache Kafka": ["Kafka", "Apache Kafka"],
    "PostgreSQL": ["PostgreSQL", "Postgres"]
  }
}
```

- **Built** by the 30B when `cv.md` changes, never per posting. One call per
  entry (six today) decomposes the bullets into facts and marks the figures,
  entities and qualifiers that must survive. A second pass proposes framings
  from the phrases employers use. For each bullet it takes the
  closest requirement phrases from past reports' Block B and stage 1 keywords
  (394 cached postings live), by embedding, and the 30B says which of them the
  facts genuinely evidence. Estimated five minutes of 30B time per rebuild.
- **Reviewed** in the TUI the way aliases are now: Approve, edit or reject each
  fact, framing and surface form. Rejections persist. The cost is per fact, and
  facts only change when `cv.md` does.
- **Keyed by bullet hash.** Editing one bullet invalidates that bullet's facts
  and framings and nothing else, so labels and approvals built on the ledger
  survive edits elsewhere. The 128 Opus labels were keyed by position, and none
  of their 33 atoms match the current text.
- **`article-digest.md`** lines enter as facts flagged `render: false`. They can
  serve as evidence and as support for framings, and never print as a line of
  their own, which is what that file already promises.
- **Approval queue.** One queue, replacing `skill-suggestions.tsv`, holds two
  kinds of item. A *framing* is a posting phrase that generalises facts the CV
  already states: "this posting says *event sourcing*; fact de-store/8b says
  *CQRS views that rebuild from the event log*; approve?". An *inference* is a
  posting tool the CV never mentions, proposed under §1.1's three conditions:
  "this posting asks for *Kubernetes*; UBWIS does the same deployment work with
  Docker and Terraform; did you use Kubernetes there?". An approved inference
  is stored as a fact on that entry, marked `"source": "inferred"` with the
  posting and date it was approved on.
- **Entity forms and framings stay separate.** Same-entity spellings
  (Postgres, K8s, Golang) are entity forms and may print in the skills block.
  Capability phrases (message brokers, containerisation) are framings. They may
  appear in a bullet whose facts support them and never replace a tool's name.
  The alias file mixes the two, which is how `Docker` came to print as
  `Containerization` on any posting that used only that word.

### 6.2 The rewrite plan (code)

For each selected bullet, built from the ledger and stage 2's alignment:

| field | source |
|---|---|
| `targets` | requirements this bullet's facts align to, must-haves first |
| `lead` | which fact opens the line: the one closest to the top target and to the posting's emphasis signals, when the source does not already open with it |
| `allowed_terms` | posting terms reachable from this bullet's facts: entity forms, approved framings, and inferences you approved for this entry |
| `proposed` | posting tools that meet §1.1's three conditions for this entry and are not yet approved; sent to the queue and never written into a draft |
| `forbidden` | posting terms with no path from these facts and no approved inference (Spark and Iceberg on report 343) |
| `must_keep` | figures, qualifiers and the outcome fact of the source bullet |
| `max_lines` | the source bullet's rendered line cost from `cvselect.BulletLines`; a rewrite may not add a line |

Code makes the decisions. The model gets a narrow brief and none of the
choices the ledgers show a 7B or a 30B getting wrong, such as which entry a
figure belongs to, which tool to claim and how long to be. Tone comes from the
same plan. The fact that leads and the framing used decide how a line turns
toward what this employer wants to hear. A tone change that alters meaning
("members" → "customers", "built" → "led") is a framing and needs your approval
once.

### 6.3 The rewrite (30B)

One call per offer on `snipe-eval`, which is already resident at the end of
Phase 2. Input: Each selected bullet's plan, its source text, its entry's
facts, and a ~1,000-character excerpt of the posting for register. Output,
schema-constrained, two drafts per bullet:

- **Draft A, minimal edit.** The source sentence with the posting's terms
  swapped in where `allowed_terms` allows. This is the successor to `suggest`,
  with its length cap relaxed to the line cost.
- **Draft B, re-led.** The same facts reordered so the target requirement opens
  the line, in the posting's terms where allowed, with the outcome kept.

The prompt carries no worked example of output (generation ledger §7). Both
drafts use temperature 0 so an arm is reproducible, with `think: false` and a
fixed `num_ctx` (generation ledger §8). *Estimated* cost: ~3,500 prompt tokens
(9 s at the measured 428 tok/s prefill) and ~1,100 output tokens for ten
bullets × two drafts (43 s at 25.6 tok/s), about 50 s in all.

### 6.4 Verification (code first, then MiniCheck)

Code checks run first because they are cheap and exact. Any failure drops the
draft.

1. **Figures.** Exactly the source bullet's figures, none added
   (`guards.NumbersIn`, the check `suggest.keepsFigures` already makes).
2. **Entity closure.** Every token new relative to the source that is
   capitalised, an acronym, or carries a digit or symbol must be an entity or
   entity form recorded for this entry, or an inference you approved for it.
   So must every posting term in stage 1's typed list. On this surface that replaces `NamedProducts`,
   `CasedProducts` and the `fit` tool patterns, with no list to maintain.
3. **Line cost.** Rendered lines at most the source's.
4. **Shape.** One sentence, no clause whose only content is the posting's own
   wording, and the function-word-ratio test the summary stage used
   (`looksLikeProse`, Node).

Then MiniCheck, in both directions:

5. **Attribution.** `MiniCheck(document = the entry's facts and approved
   framings, claim = draft)`. On failure, retry with the entry's approved
   inferences appended. A draft that passes the first check is *supported*,
   one that passes only the second uses an *inference*, and one that fails both
   is dropped.
6. **Preservation.** `MiniCheck(document = draft, claim = fact)` for each
   `must_keep` fact. Any failure drops the draft. Dropping "simulated" would
   inflate the claim; it is Takano's qualifier-drop category.

The 80/20 count runs in code. A shipped bullet's claims are its source facts
plus each approved inference whose entity appears in it, so the ratio needs no
further model call.

*Estimated* 60–80 checks per offer at 0.3–0.5 s each once loaded, plus one ~6 s
load per run under stage-major ordering. E2 decides between whole-bullet and
per-clause checks before anything ships. E2 also runs the 30B as verifier on
the same seeded set, and if it matches MiniCheck there, no model is added.

### 6.5 Selection (code)

Among the drafts that passed, plus the original:

- **Evidence.** Targets whose posting term or approved framing now appears in
  the first ~10 words, weighted 2 for a must-have and 1 for a preferred.
- **Parity.** Posting spellings gained for entities the bullet already names.
- **Budget.** A choice that would take a role past 20% inferred claims is
  skipped.
- **Ties.** The original wins.

The truth score per role is then recomputed on the page as rendered, and the
offer's folder gets a `truth.json` (claims per role by class, every framing
used) that the TUI reads.

### 6.6 Review (TUI)

The existing tailor screen, pointed at the adapted lines. Per bullet it shows
the original against what shipped with the difference marked, and flags each
claim resting on a framing or an inference with what licensed it. `enter` keeps, `n` reverts that
bullet to the original, `r` rebuilds.

A run's first PDF carries supported claims only. Proposed inferences for the
offer wait in the queue, one claim per row with reject as the default, and
approving any of them rebuilds the PDF with the drafts that use them. No
inferred claim reaches a PDF before you have approved it once. For supported
claims the verifier is the guarantee, since Takano's reviewer caught 54.5% of
subtle edits; the screen is for preference and spot checks.

### 6.7 A worked example

From the live `cv.md`, against a posting that asks for "experience designing
event-driven microservices on Kafka":

> **source** Built retail operations as 10 Java and Spring Boot services
> covering orders, stock, pricing, delivery, loyalty and finance, each owning
> its own database and talking to the others only through a Kafka log

> **draft B, re-led** Designed 10 event-driven Java and Spring Boot
> microservices for retail operations (orders, stock, pricing, delivery,
> loyalty, finance), each owning its own database and talking to the others
> only through Kafka

Draft B keeps `10`, and Java, Spring Boot and Kafka all belong to this entry.
"Event-driven microservices" is an approved framing of facts 3a and 3c, so
those claims count as supported through a framing. "Designed" rests on the same entry's saga and ADR
bullets. If MiniCheck does not accept it against this bullet's facts, B fails
and draft A ("Built 10 event-driven Java and Spring Boot microservices …") is
next. The line cost is equal. B or A ships, and the original ships only if both
fail.

On report 343, a Snipe bullet re-led with "Open-source" is supported, because the
entry's header says Open Source. Spark, DataFusion and Iceberg have no path from
any fact, so no line may name them and they stay gaps, as T3 requires. Stage 2
graded them different activity, so they fail §1.1's third condition; at 4.2 the
offer clears the score bar, which means they can still be proposed for the
skills block, and they reach the page only if you approve them.

Suppose instead a posting that scores 4.3 asks for Kubernetes. UBWIS's
deployment bullet ("Ran the platform on AWS from Terraform and GitHub Actions
…") does the same work with other tools, so Kubernetes goes to the queue as an
inference for UBWIS. If you approve it, a draft may name it on that line, and
it counts toward UBWIS's 20%. If you reject it, the term stays forbidden for
that entry.

### 6.8 Time budget

| step | model | time |
|---|---|---|
| Phase 1 | 4B | 18 s (measured) |
| Phase 2 | 30B | ~135 s (judgment measured, the rest estimated); +10–15 s for typed stage 1, −20 s without the personalisation table |
| selection | embedder | ~8 s (measured, verbatim path) |
| rewrite | 30B, resident | ~50 s (estimated) |
| verification | MiniCheck | ~25–40 s plus one load (estimated) |
| render | pdflatex | unchanged |
| **total** | | **about 4–4.5 minutes**, inside the 6-minute budget |

Every Phase 3 figure here is an estimate until E3 records real timings.

---

## 7. Hardcoded parts and the plan for them

Three kinds of hardcoding, each treated differently:

- **Measured constants** (`LineBudget` 22, `SpikeWeight` 6, `coverageBlend`
  0.4, `topKCandidates` 3, the 5/7/3 skills limits, and the rest). These are
  fitted parameters with provenance in `batch/CLAUDE.md`, and they stay.
- **Policy** (commutable places, seniority tolerance, the comp floor). These
  are your preferences. They belong in `profile.yml`, applied to facts a model
  extracted and quoted.
- **Vocabularies and special cases.** This is the part to remove. Each list
  grows when an incident is found and says nothing about the next unfamiliar
  posting.

| item | where | size | replaced by | milestone |
|---|---|---|---|---|
| `NamedProducts`, `CasedProducts` | `guards/vocab.go` | 176 + 4 | ledger entity closure | M3 |
| `ecosystems`, `tools` | `fit/fit.go` | 12 + 14 patterns | ledger entities (demonstrated or catalogue-only) and stage 1 typed tech terms | M5 |
| `ukCities`, attendance patterns, 160-character proximity | `fit/fit.go` | 29 + ~12 | quoted attendance facts and the profile's places | M5 |
| `humanLanguages`, fluency pattern | `fit/fit.go` | 20 | quoted language requirement against the CV's languages | M5 |
| seniority patterns | `fit/fit.go` | 6 | quoted years and title level; the caps stay in code | M5 |
| alias file | `config/skills.yml` | 29 keys | ledger entity forms and framings, approved per fact | M3 |
| `suggest` vetting rules | `internal/suggest` | 5 rules | the §6.4 invariants (most carry over) | M3 |
| writer stopwords, `moduleTokens` | `internal/phase3` | n/a | kept; module ranking could move to embeddings later, for little gain | none |
| HTML, LaTeX and salary parsing | `text`, `fill`, `latex` | n/a | kept, since deterministic parsing suits formats | none |

Every list being replaced runs in shadow first. Both decisions are logged and
the disagreements read, and the list is deleted once the new path is right on
each disagreement or the disagreement turns out to be a bug in the list. The
Node → Go gates used the same discipline to check parity; here it checks
quality.

---

## 8. Measurement

### 8.1 The suite

Everything is computed on the text of the rendered PDF (benchmark rule 11), per
offer, paired, with an A/A floor.

| family | metric | definition | want |
|---|---|---|---|
| truth | `supported_share` | supported claims ÷ all claims per role, minimum over roles | ≥ 0.80 |
| truth | `inferred_share` | inferred claims ÷ all claims per role, maximum over roles | ≤ 0.20 |
| truth | `unapproved` | claims failing attribution against facts, framings and approved inferences | 0 |
| truth | `entity_violations` | a new employer, title, date, degree, module, certificate or figure, or a tool neither in the entry nor approved for it | 0 |
| truth | `keep_rate` | must-keep facts (figures, qualifiers, outcomes) still entailed by the page | 1.0 |
| ATS | `term_parity` | posting spellings present for entities the ledger supports | higher |
| ATS | `must_have_evidence` | must-haves the ATS simulator marks met, over must-haves the ledger supports | higher |
| ATS | `boolean_pass` | the posting's required terms as an AND query, Greenhouse-style, restricted to supported terms | 1.0 |
| ATS | `front_load` | selected bullets with a target term in the first ~10 words | higher |
| quality | `pairwise_pref` | blind pairwise against verbatim, both orders, a tie when the orders disagree | ≥ 0.5 |
| quality | `shape_rejects` | drafts the shape gate rejected | lower |
| selection | `differentiator_coverage` | as now, on labels re-keyed by fact hash | no regression |
| page | `one_page`, `lines`, `exp_starved` | as now | no regression |

The ATS simulator follows Workday's documented method. For each basic and
preferred qualification from stage 1, a judge receives the qualification and
the rendered CV text and answers met, partly or unmet, with a quoted pointer
checked by substring. The A–D grade follows Workday's rule (every basic
qualification met ⇒ A or B). The judge is never the writer. It comes from
another model family (Granite or a 7–9B general model, chosen during E2) and
runs once on the verbatim CV and once on the adapted one for every offer.

### 8.2 Experiments, with gates set in advance

| # | what | gate |
|---|---|---|
| E0 | `gate tailor` in Go; a 32-offer A/A; settle the judge rerank question | an A/A floor recorded for every metric |
| E1 | build the ledger; you review it once | ≥ 95% of facts accepted unedited, or the decomposition prompt changes |
| E2 | verifier calibration on 100–150 seeded rewrites: an added tool, a dropped qualifier, inflated scope, a figure moved between entries, faithful paraphrases. The corruptions are false by construction, so only the paraphrases need your tick | MiniCheck recall on corruptions ≥ 0.95 at precision ≥ 0.80, or switch to per-clause checks and re-run. Run the 30B on the same set |
| E3 | rewrite arm against verbatim, 32 offers, paired | `unapproved` and `entity_violations` both 0 on every offer; `must_have_evidence` and `term_parity` up beyond the A/A floor; `pairwise_pref` ≥ 0.5; under 6 minutes |
| E4 | alignment bonus in selection | `differentiator_coverage` not down, `must_have_evidence` up |
| E5 | Phase 2 banding over ledger alignment; stage 2 on facts | pair accuracy on the 18 labels not below today's; evidence gold set not below today's |
| E6 | your blind read of 12 offers, verbatim against adapted | you prefer adapted on a majority and flag no false claim |
| E7 | inference proposals on the 32-offer sample at the default `infer_min_score` | you approve at least half of what is proposed; otherwise raise the threshold or require every must-have met (band A/B) |

**Kill rule.** One offer with an unapproved claim or an entity violation stops
the arm until the cause is found, the status `metric_fab 0` and
`role_retention 1.0` have today. The benchmark rules in `batch/CLAUDE.md` all
carry over. Rule 7 (read the first few outputs before trusting a table) applies
to every rewrite change.

---

## 9. Rollout

| milestone | contents | behind |
|---|---|---|
| M0 | E0; the README corrected or the judge ported | no flag |
| M1 | ledger build and TUI review; aliases migrated; E1 | `snipe ledger` |
| M2 | MiniCheck client and the verifier; E2 | no flag |
| M3 | plan, rewrite, select; `truth.json`; guards on entity closure; E3 | `--writer adapt` (verbatim stays the default) |
| M4 | TUI diff review; the framing queue replaces alias suggestions | no flag |
| M5 | Phase 1 and 2 quoted extraction, banding, vocabularies in shadow; E5 | a flag per cap |
| M6 | `adapt` becomes the default; superseded lists, `suggest` and the alias rebuild deleted | after E3 and E6 pass |

Nothing adds a runtime. Every model runs behind Ollama and every new piece is
Go.

---

## 10. Risks

- **The verifier passes a false line.** E2 measures recall before anything
  ships. Entity closure and the figure check do not depend on the verifier,
  and the per-role budget bounds the damage.
- **The verifier rejects true lines.** That costs only adaptation, since the
  original ships. E3 reports the rewrite rate so it stays visible.
- **Optimising for the simulator.** The simulator is one judge family,
  `pairwise_pref` uses another, and E6 is you. A gain on the simulator alone
  does not count.
- **Self-preference.** The writer never judges its own output (§3.2).
- **Rubber-stamped inferences.** An approved tool is a checkable claim, and an
  interviewer will ask about it. The queue shows one claim at a time with
  reject as the default, the 20% cap limits how much of a role can rest on
  approvals, and E7 shows how often the proposals are right.
- **Ledger review turns into a chore.** It is one-time per bullet, and an edit
  only invalidates the bullets it touched. If E1 takes much more than 45
  minutes, the decomposition is too fine.
- **Licence.** Bespoke-MiniCheck is CC BY-NC 4.0. You pull it; the repository
  never ships it. Granite Guardian (Apache 2.0) is the fallback should that
  ever matter.
- **Similar postings get similar CVs.** Deterministic plans make similar
  rewrites for similar postings. That is correct behaviour, noted here so
  nobody files it as a bug.

---

## 11. Open questions for you

1. Should an approved inference carry over to later postings (proposed), or be
   asked again on every posting?
2. Is 4.0 the right default for `infer_min_score`, or should inferences also
   need every must-have met (band A/B)?
3. Which verbs are fair when the CV says "built": "designed", "architected",
   "led"? Each becomes a framing you approve once, per entry.
4. Adapt project bullets too, or experience only to begin with?
5. The Node judge rerank measured +0.115 and is missing in Go. Port it in M0,
   or accept cosine plus spike as the new baseline?

---

## 12. Sources

ATS and screening
- Greenhouse, [Talent Matching: Data Processing FAQ](https://support.greenhouse.io/hc/en-us/articles/41131616864283-Talent-Matching-Data-Processing-FAQ) and [Talent Matching overview](https://support.greenhouse.io/hc/en-us/articles/41131886674075)
- Greenhouse, [Search resumes for keywords](https://support.greenhouse.io/hc/en-us/articles/115004600186-Search-resumes-for-keywords)
- Workday, [HiredScore grades](https://doc.workday.com/hiredscore/en-us/workday-hiredscore/recruiter-productivity-/concept--hiredscore-grades.html) and [release notes, 5 August 2026](https://doc.workday.com/hiredscore/en-us/workday-hiredscore/hiredscore-release-notes/2026/august/wednesday--5th-august.html)
- [Enhancv recruiter study, IT Brief UK coverage](https://itbrief.co.uk/story/study-reveals-ats-rarely-auto-rejects-cvs-debunks-75-myth)
- Chen & Xiao 2026, [Competence-Preserving Resume Perturbations Expose Presentation Sensitivity in LLM Screening](https://arxiv.org/abs/2609.16517)
- Xu, Li & Jiang 2025, [AI Self-preferencing in Algorithmic Hiring](https://arxiv.org/abs/2509.00462)
- Castleman et al. 2026, [Measuring Validity in LLM-based Resume Screening](https://arxiv.org/abs/2602.18550)
- The Ladders 2018, [eye-tracking study, HR Dive coverage](https://www.hrdive.com/news/eye-tracking-study-shows-recruiters-look-at-resumes-for-7-seconds/541582/); Susan Gygax, [Is the 6-second resume scan a myth?](https://www.ere.net/articles/is-the-6-second-resume-scan-a-myth) (ERE, 2020)
- Resume Now survey, [Staffing Industry Analysts coverage](https://www.staffingindustry.com/news/global-daily-news/hr-pros-say-ai-cant-replace-the-human-touch-survey-says); [TopResume hiring-manager survey 2025](https://topresume.com/career-advice/ai-in-hiring-survey); [AIResumeBuilder survey coverage](https://www.goskagit.com/airesumebuilder-com-survey-1-in-5-employers-may-adopt-pay-to-apply-model-as-ai/article_90c6c61b-de74-58fa-a260-310f0b0d2a65.html)

Fabrication in rewriting
- Takano 2026, [Mitigating Fabrication in Multi-Stage LLM Pipelines for Hiring](https://arxiv.org/abs/2608.26171)
- Indukuri & Agrawal 2026, [Grounded Optimization](https://arxiv.org/abs/2607.01457)
- [Career-Aware Resume Tailoring via Multi-Source RAG with Provenance Tracking](https://arxiv.org/abs/2605.05257) (a one-candidate pilot; useful for its architecture, too small for its results)

Verification and judges
- Tang, Laban & Durrett 2024, [MiniCheck](https://arxiv.org/abs/2404.10774); [Bespoke-MiniCheck-7B model card](https://huggingface.co/bespokelabs/Bespoke-MiniCheck-7B); [Ollama library](https://ollama.com/library/bespoke-minicheck)
- Gao et al. 2023, [RARR: Researching and Revising What Language Models Say](https://arxiv.org/abs/2210.08726)
- Min et al. 2023, [FActScore](https://arxiv.org/abs/2305.14251); Metropolitansky & Larson 2025, [Claimify](https://arxiv.org/abs/2502.10855)
- [LettuceDetect](https://arxiv.org/abs/2502.17125); [Granite Guardian](https://www.ibm.com/granite/docs/models/guardian)
- Shi et al., [Judging the Judges: position bias in LLM-as-a-judge](https://arxiv.org/abs/2406.07791)
- [Qwen3-Reranker GGUF for llama.cpp](https://huggingface.co/ggml-org/Qwen3-Reranker-0.6B-Q8_0-GGUF); [Ollama rerank request, issue #4510](https://github.com/ollama/ollama/issues/4510)

Skills and matching
- [Efficient Text Encoders for Labor Market Analysis](https://arxiv.org/abs/2505.24640) (2025)
- Malandri, Mercorio & Serino 2025, [SkiLLMo](https://boa.unimib.it/handle/10281/555064)
- [Resume2Vec, MDPI Electronics 14(4):794](https://www.mdpi.com/2079-9292/14/4/794); [ConFit v2](https://arxiv.org/abs/2502.12361)

Inside this repository
- `docs/PHASE3-GENERATION-LEDGER.md`, `docs/PHASE3-RETENTION-LEDGER.md`,
  `docs/PHASE3-RETRIEVAL-LEDGER.md`, `docs/REWORK-PLAN.md`, `batch/CLAUDE.md`
