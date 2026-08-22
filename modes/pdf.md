# Mode: pdf — ATS-Optimized PDF Generation

## Full pipeline

1. Read `cv.md` as the source of truth
2. Ask the user for the JD if it is not in context (text or URL)
3. Extract 15-20 keywords from the JD
4. Detect JD language → CV language (EN default)
5. Always A4. Letter used to be selectable and is not: it is 66px shorter,
   about four bullet lines on a CV fighting to stay on one page, and no ATS or
   recruiter screens on a 6mm width difference.
6. Detect role archetype → adapt framing
7. Select top 3-4 most relevant projects for the job
8. Reorder experience bullets by JD relevance
9. Inject keywords naturally into existing achievements (NEVER invent)
10. Generate the full LaTeX document from the template + personalized content
11. Read `name` from `config/profile.yml` and derive two forms: `{candidate}` = kebab-case lowercase for slugs (e.g. "John Doe" → "john-doe"); `{CandidateName}` = Title-Case hyphenated for the PDF filename (e.g. "John-Doe"). Also derive `{company}` and `{role}` as kebab-case slugs.
12. **One folder per application — keep the PDF and its source together.** Let `APP_DIR = output/{YYYY-MM-DD}_{company}_{role}`. Write the filled LaTeX to `{APP_DIR}/source.tex` (persisted, NOT /tmp — it is the editable source of the PDF).
13. Execute: `mkdir -p "{APP_DIR}" && node generate-pdf.mjs "{APP_DIR}/source.tex" "{APP_DIR}/{CandidateName}-CV.pdf"` (always A4)
14. Report: the folder path, the PDF path (`{CandidateName}-CV.pdf` — attach as-is), number of pages, keyword coverage %. Note that `source.tex` in the same folder is editable for manual tweaks + re-render.

## ATS Rules (clean parsing)

- Single-column layout (no sidebars, no parallel columns)
- Standard headers: "Technical Skills", "Work Experience", "Projects", "Education"
- No text in images/SVGs
- No critical info in PDF headers/footers (ATS ignores them)
- UTF-8, selectable text (not rasterized)
- No nested tables
- Distributed JD keywords: Summary (top 5), first bullet of each role, Skills section

## PDF Design

- **Font**: Latin Modern, the LaTeX default — nothing to self-host
- **Header**: name in `\Huge\scshape`, then the target role, then one contact row
- **Section headers**: `\large\scshape` over a full-width `\titlerule`
- **Body**: 11pt document, bullets set `\small` (10pt) on 12pt leading
- **Colour**: none. The palette was the least ATS-safe thing in the old template
- **Margins**: 0.45in, A4
- **Background**: pure white

## Section order (optimized "6-second recruiter scan")

1. Header (large name, target role, contact row)
2. Technical Skills (the one section a keyword scan reads first)
3. Work Experience (reverse chronological)
4. Projects (top 3-4 most relevant)
5. Education & Certifications

No Professional Summary and no Core Competencies. Every tag Competencies printed
also appeared in Skills; the summary is a section Jake's Resume does not have,
and `generate-pdf.mjs` holds every section but Skills to `cv.md`'s own order.

## Keyword injection strategy (ethical, truth-based)

Examples of legitimate reformulation:
- JD says "RAG pipelines" and CV says "LLM workflows with retrieval" → change to "RAG pipeline design and LLM orchestration workflows"
- JD says "MLOps" and CV says "observability, evals, error handling" → change to "MLOps and observability: evals, error handling, cost monitoring"
- JD says "stakeholder management" and CV says "collaborated with team" → change to "stakeholder management across engineering, operations, and business"

**NEVER add skills that the candidate does not have. Only reword real experience using the exact JD vocabulary.**

## Template LaTeX

Use `templates/cv-template.tex` — [Jake's Resume](https://github.com/jakegut/resume),
compiled by `pdflatex`. Replace the `{{...}}` placeholders with personalized
content, all of it LaTeX-escaped (`% & _ # $ { } ~ ^` and the T1 traps `< > |`):

| Placeholder | Content |
|-------------|-----------|
| `{{NAME}}` | (from profile.yml) |
| `{{TARGET_ROLE}}` | The role applied for, under the name. Whole line including its `\\`, or empty |
| `{{CONTACT}}` | The whole header line: `$|$`-separated `\href` calls for phone, email, LinkedIn, GitHub, then location. Built as one token because every field is optional |
| `{{SECTION_SKILLS}}` | Technical Skills — renders directly under the header |
| `{{SKILLS}}` | An `itemize` of `\textbf{Category}{: items}` rows |
| `{{SECTION_EXPERIENCE}}` | Work Experience |
| `{{EXPERIENCE}}` | One `\resumeSubheading{Company}{Period}{Role}{Location}` per job, each wrapping its bullets in `\resumeItem` |
| `{{SECTION_PROJECTS}}` | Projects |
| `{{PROJECTS}}` | One `\resumeProjectHeading{\textbf{Name} $|$ \emph{Badge}}{Period}` per project, bullets as `\resumeItem`, stack and repo as a trailing `\resumeTechLine` |
| `{{SECTION_EDUCATION}}` | Education \& Certifications |
| `{{EDUCATION}}` | `\resumeSubheading{Degree}{Period}{School}{}`, with modules, achievements and certifications as `\resumeItem` rows |

There is no summary. Jake's has none, and the generation stage that used to
write one is disconnected in `batch/local-pdf-offer.mjs`. Core Competencies is
gone too: every tag it printed also appeared in Skills. Certifications has no
section of its own — it trails the last education entry.

**Never write a placeholder token inside a `%` comment** in the template: the
filler is a blind `replaceAll` and will substitute into the comment.

An empty `itemize` is a LaTeX error, not an empty list — an entry whose bullets
are all trimmed must render as a bare heading, with no `\resumeItemListStart`.

## Canva CV Generation (optional)

If `config/profile.yml` has `cv.canva_resume_design_id` set, offer the user a choice before generating:
- **"HTML/PDF (fast, ATS-optimized)"** — existing flow above
- **"Canva CV (visual, design-preserving)"** — new flow below

If the user has no `cv.canva_resume_design_id`, skip this prompt and use the HTML/PDF flow.

### Canva workflow

#### Step 1 — Duplicate the base design

a. `export-design` the base design (using `cv.canva_resume_design_id`) as PDF → get download URL
b. `import-design-from-url` using that download URL → creates a new editable design (the duplicate)
c. Note the new `design_id` for the duplicate

#### Step 2 — Read the design structure

a. `get-design-content` on the new design → returns all text elements (richtexts) with their content
b. Map text elements to CV sections by content matching:
   - Look for the candidate's name → header section
   - Look for "Summary" or "Professional Summary" → summary section
   - Look for company names from cv.md → experience sections
   - Look for degree/school names → education section
   - Look for skill keywords → skills section
c. If mapping fails, show the user what was found and ask for guidance

#### Step 3 — Generate tailored content

Same content generation as the LaTeX flow (Steps 1-9 above), plus a summary,
which the Canva design has a box for and the PDF template does not:
- Rewrite Professional Summary with JD keywords + exit narrative
- Reorder experience bullets by JD relevance
- Select top competencies from JD requirements
- Inject keywords naturally (NEVER invent)

**IMPORTANT — Character budget rule:** Each replacement text MUST be approximately the same length as the original text it replaces (within ±15% character count). If tailored content is longer, condense it. The Canva design has fixed-size text boxes — longer text causes overlapping with adjacent elements. Count the characters in each original element from Step 2 and enforce this budget when generating replacements.

#### Step 4 — Apply edits

a. `start-editing-transaction` on the duplicate design
b. `perform-editing-operations` with `find_and_replace_text` for each section:
   - Replace summary text with tailored summary
   - Replace each experience bullet with reordered/rewritten bullets
   - Replace competency/skills text with JD-matched terms
   - Replace project descriptions with top relevant projects
c. **Reflow layout after text replacement:**
   After applying all text replacements, the text boxes auto-resize but neighboring elements stay in place. This causes uneven spacing between work experience sections. Fix this:
   1. Read the updated element positions and dimensions from the `perform-editing-operations` response
   2. For each work experience section (top to bottom), calculate where the bullets text box ends: `end_y = top + height`
   3. The next section's header should start at `end_y + consistent_gap` (use the original gap from the template, typically ~30px)
   4. Use `position_element` to move the next section's date, company name, role title, and bullets elements to maintain even spacing
   5. Repeat for all work experience sections
d. **Verify layout before commit:**
   - `get-design-thumbnail` with the transaction_id and page_index=1
   - Visually inspect the thumbnail for: text overlapping, uneven spacing, text cut off, text too small
   - If issues remain, adjust with `position_element`, `resize_element`, or `format_text`
   - Repeat until layout is clean
e. Show the user the final preview and ask for approval
f. `commit-editing-transaction` to save (ONLY after user approval)

#### Step 5 — Export and download PDF

a. `export-design` the duplicate as PDF (format: a4 or letter based on JD location)
b. **IMMEDIATELY** download the PDF into the application folder using Bash (`APP_DIR = output/{YYYY-MM-DD}_{company}_{role}`):
   ```bash
   mkdir -p "{APP_DIR}" && curl -sL -o "{APP_DIR}/{CandidateName}-CV-canva.pdf" "{download_url}"
   ```
   The export URL is a pre-signed S3 link that expires in ~2 hours. Download it right away.
c. Verify the download:
   ```bash
   file "{APP_DIR}/{CandidateName}-CV-canva.pdf"
   ```
   Must show "PDF document". If it shows XML or HTML, the URL expired — re-export and retry.
d. Report: PDF path, file size, Canva design URL (for manual tweaking)

#### Error handling

- If `import-design-from-url` fails → fall back to HTML/PDF pipeline with message
- If text elements can't be mapped → warn user, show what was found, ask for manual mapping
- If `find_and_replace_text` finds no matches → try broader substring matching
- Always provide the Canva design URL so the user can edit manually if auto-edit fails

## Cover Letter Sub-flow

After generating the CV PDF, offer to generate a cover letter:

```text
CV PDF generated: output/{path}

Want a cover letter for this role too?
- Say "yes" or "cover letter" to generate one now
- Or run `/snipe cover {slug}` later
```

If the user says yes, run the full cover letter flow from `modes/cover.md` in slug mode:
1. Load the existing `## Cover Letter Draft` from the evaluation report as a starting point
2. Run company research (Step 3 of cover.md)
3. Present keyword list for confirmation (Step 4)
4. Surface any gaps (Step 5)
5. Ask the four prompts: why / problems / approach / tone (Step 6)
6. Draft in chat, wait for approval (Steps 7-8)
7. Generate cover letter PDF via `node generate-cover-letter.mjs` (Step 9)
8. Report both PDF paths

Do not auto-generate the cover letter PDF without going through the interactive steps above.

## Post-generation

Update tracker if the job is already registered: change PDF from N to Y.
