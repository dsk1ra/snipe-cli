# One page — the measurement, and the plan

Everything below is measured against the shipped NatWest CV
(`output/2026-08-07_natwest-group_194/`), rendered in headless Chromium at print
width. No estimates.

---

## 1. Confirmed: it is too long, and it rendered on the wrong paper

| | |
|---|---|
| Pages | **2** |
| Paper | **US Letter (612×792pt)** — for a UK bank |
| Content height | 1729px |
| A4 budget at today's 0.6in margins | 1007px |
| Overflow | **722px — 1.72 pages** |

### The paper is a bug, not a preference

`local-pdf-offer.mjs:129`:

```js
if (/\b(united states|usa|\bus\b|canada|...)\b/.test(combined)) return 'letter';
```

The NatWest JD opens `Join us as a Software Engineer, Engineering Platforms Team`.
`\bus\b` matches the pronoun. Every JD that says "join us", "contact us", "tell us
about yourself" renders on Letter.

Letter is **66px shorter** than A4 at the same margins, so the bug costs about four
bullet lines on exactly the CV that needed them. Fix: drop the bare `\bus\b`
alternative, keep `united states|usa|u\.s\.`, and prefer the location field the eval
already parsed over a regex over the whole JD.

---

## 2. Where the 1729px actually goes

Per-section, at A4 print width:

| section | px | what it is |
|---|---|---|
| Header | 64 | |
| Professional Summary | 96 | 4 lines of prose |
| **Core Competencies** | **45** | 7 keywords, all of which repeat verbatim in Skills |
| Work Experience | 537 | 2 roles, 8 bullets |
| Projects | 515 | 4 projects, 8 bullets |
| Education | 94 | |
| Certifications | 70 | 2 certs, own section, own rule |
| Skills | 192 | 6 categories, ~50 items, 3 of them wrap |

Unit costs, measured:

- one rendered bullet line = **15.6px**; a bullet costs `lines × 15.6 + 4`
- **not one bullet on the page is a single line.** Bullets run 2–4 lines
  (49/32/49/65px in Experience, 32–49px in Projects)
- a job's chrome (company / role / location on three separate lines) = **51px**
- a project's chrome (title + tech line) = **33px**
- a section header (rule + margins) = **42px**. Seven sections = **294px of pure
  chrome, 29% of an A4 page, before a word of content.**

`cv.md` bullet lengths, n=38: median 223 chars ≈ 2.5 rendered lines; only 5 are one
line; 8 are four or five. The master CV is written long, on purpose. **That is the
whole problem.**

---

## 3. The structural finding

I prototyped the proposed layout and measured the budget it leaves.

```
                                   chrome floor   room for bullets
today, 0.6in margins, Letter            770px      171px  ≈ 11 lines
today, 0.6in margins, A4                728px      279px  ≈ 18 lines
proposed layout, A4, 0.45in margins     594px      442px  ≈ 28 lines
```

Today's CV ships **40 bullet-lines**.

Two things follow, and they are the plan:

**(a) Layout is worth 10 bullet-lines and nothing more.** I swept it: A4 + 0.45in
margins + dropping Core Competencies + tightened spacing + 3 projects + certs folded
into Education takes 1729px → 1173px. Still **1.13 pages**. Layout alone cannot do
this. Anyone who tells you a template fixes it is wrong.

**(b) The page is rationed in LINES; `cv-select.mjs` rations COUNTS.** This is the
actual defect. `allocate()` (`cv-select.mjs:445`) spends a budget of 8 project
*bullets*; experience gets a flat `maxBulletsPerRole = 4` with no budget at all.
A 4-line bullet and a 1-line bullet cost the same against that budget and 4× the
page. The ranker has never seen the page.

The `LADDER` in `local-pdf-offer.mjs:939` cannot rescue it either — it only cuts
counts, and it stops at `≤ 2 pages`, so it is *satisfied* by the output you are
unhappy with.

---

## 4. Verdict on your recommendations

| your point | verdict | measured |
|---|---|---|
| Target role in the header | **Yes** | costs 16px; `args.role` is already in `local-pdf-offer.mjs` |
| Summary 2–4 lines | already 4 — **cap it at 3** | −17px |
| Skills immediately after Summary | **Yes** | free; today it is dead last on page 2, the worst position on the document |
| 4–6 categories, 12–20 curated skills | **Yes, strongly** | ~50 items today, 3 categories wrap; curating to ~20 takes Skills 192px → 102px (**−90px ≈ 6 bullet lines**) |
| `Company, Location \| Title \| Dates` on one line | **Yes** | 51px → 34px per role, **−34px** |
| 2–3 projects when tailored | **Yes** | 4th project costs 40px chrome + its bullets |
| GitHub link per project | **Yes** | free — `cv.md` already carries them (lines 42, 53, 62, 71), the template just never renders them |
| Education: 3–6 modules + achievement | already correct | keep |
| Compact certifications | **Fold into Education** | its own section costs 42px of chrome for 2 lines of content: **−45px** |

One thing you did not list, and it is the cheapest win on the page:

**Delete Core Competencies.** `Software Engineer · Agile · CI/CD · Java · Python ·
Artificial Intelligence · Stakeholder Management` — every one of those seven strings
also appears in Skills. It is 45px to say the same thing twice, and it is the section
an ATS parser is most likely to double-count.

---

## 5. Plan

Ordered by px-per-unit-of-risk. Stop when it fits.

### Phase A — free wins, no selection change, no model call

| # | change | file | Δ |
|---|---|---|---|
| A1 | `detectFormat`: drop `\bus\b`; prefer the parsed location | `local-pdf-offer.mjs:127` | **+66px** budget |
| A2 | Margins 0.6in → 0.45in | `generate-pdf.mjs:352` | **+29px** budget, +29px width (less wrapping) |
| A3 | Delete Core Competencies section + `{{COMPETENCIES}}` | `cv-template.html:428`, `fill-cv-template.mjs:531` | −45px |
| A4 | Fold Certifications into Education as one `edu-desc` line | both | −45px |
| A5 | Job header to one line: `Company · Location \| Title \| Dates` | `fill-cv-template.mjs:313` | −34px |
| A6 | Tighten section/job/project margins and `line-height` 1.35 → 1.30 | `cv-template.html` | −119px |
| A7 | Move Skills between Summary and Experience | `cv-template.html:460` | 0px, large signal gain |
| A8 | Target role under the name, from `args.role` | template + fill | +16px |
| A9 | Render each project's `cv.md` GitHub link | `fill-cv-template.mjs` | ~0px (fits the tech line) |

Net: chrome **728px → 594px**, budget **279 → 442px for bullets (28 lines)**.

Font size stays at 12px and the Arial stack stays. Shrinking type to fit is the one
move that trades a real ATS/readability cost for page space, and at 28 lines we do
not need it.

### Phase B — the actual fix: budget lines, not bullets

`cv-select.mjs`. `allocate()` is already a greedy spender over a budget; make the
budget lines and charge each bullet what it costs.

```js
// est. rendered lines at ~95 chars/line on the A4 content width
const cost = t => Math.max(1, Math.ceil(t.length / 95));
```

- one shared line budget across Experience **and** Projects (today Experience has no
  budget at all and eats 537px unchallenged)
- greedy stays greedy, but ranks on **score per line**, not score
- every entry still keeps one bullet, as today
- `trim()`'s metric-bullet guarantee must charge for the swap. batch/CLAUDE.md already
  flags it as the first suspect for the ATS dip; under a line budget it can silently
  blow the page by swapping a 1-line bullet for a 4-line one

This is a selection change, so per batch/CLAUDE.md: sweep it offline with
`select-sweep.mjs`, and **do not reuse an existing `SNIPE_SELECT_CACHE`** — the key
is over CV and requirements, not the ranker.

The honest framing for the A/B: coverage will go **down**, because fewer bullets fit
on one page than on two. That is the price you are choosing to pay. The question the
benchmark answers is not "did coverage improve" but **"at a fixed one-page budget,
does the line-aware knapsack keep more differentiators than naively cutting counts
until it fits"** — arm A being the `LADDER` retargeted at 1 page, arm B the knapsack.
Run `opus-metrics.mjs` for `differentiator_coverage` and `grade_yield`, paired, and
remember the A/A floors in batch/CLAUDE.md before claiming anything under ±0.02.

### Phase C — retarget the ladder

`LADDER` in `local-pdf-offer.mjs:939` and `--max-pages=2` both encode the old goal.
Change the target to 1 page, keep 2 as the hard failure ceiling, and let the ladder
handle the tail cases Phase B cannot predict (an unusually verbose JD, a role with
five entries). Its steps should trim lines now, not counts.

### Phase D — the ceiling nobody can code around

At 28 lines and a median 2.5-line source bullet, one page holds **~11 bullets**.
Today's ships 16. If, after B and C, the differentiators still do not fit, the
remaining lever is `cv.md` itself: the four- and five-line bullets are master-CV
bullets, and no ranker can shorten them without a model rewrite — which
`PHASE3-RETENTION-LEDGER.md` already measured as a grounding loss (0-32, 0-11).

That is a **user-layer edit and therefore yours**, not the pipeline's. Note the cost
before starting: the 128 labels in `batch/bench/opus/labels/` are positional against
`cv.md`, so editing it invalidates all of them.

---

## 6. Explicitly not doing

- **Shrinking the font or the margins below 0.4in.** Buys space, costs ATS
  reliability and readability, and is the first thing a recruiter notices.
- **A model call to compress bullets.** The retention ledger settled this: a 9B and
  the 30B both lost to no writer at all, and every offer they changed lost grounding.
  `--writer verbatim` is why the output is grounded now.
- **Two columns.** ATS parsers linearise columns unpredictably, and the gain is
  Skills-only, which the curation in A already delivers.
- **Auto-editing `cv.md`.** It is the user layer.
