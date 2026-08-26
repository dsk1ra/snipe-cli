# Templates

System-layer template files used by the binary and the modes. Put user customizations in the user-layer files instead (see the data contract in docs/ARCHITECTURE.md).

## Files

| File | Used By | Purpose |
|------|---------|---------|
| `cv-template.tex` | `internal/fill`, `internal/pdf` | LaTeX template (Jake's Resume) for ATS-optimized CV PDFs |
| `portals.example.yml` | Onboarding | Example portal scanner configuration (copy to `portals.yml` to activate) |
| `states.yml` | `internal/tracker` | Canonical application states and their aliases |

### cv-template.tex

[Jake's Resume](https://github.com/jakegut/resume) (MIT), compiled by `pdflatex`.
`internal/fill` substitutes the `{{...}}` placeholder tokens and writes
`source.tex` next to the PDF, so a manual tweak plus a re-render is still one
command.

**Design:** Latin Modern, single-column, ruled small-caps section headings.
`\input{glyphtounicode}` + `\pdfgentounicode=1` is what makes the PDF's text
layer extract cleanly — that is the ATS guarantee, and it is why the template
keeps them.

**Sections:** Technical Skills, Work Experience, Projects, Education. There is
no summary: Jake's has none, and the pipeline's summary stage is disconnected
in `internal/phase3`. Skills sits directly under the header rather than last,
which is a deliberate departure from upstream — `internal/fill` treats it as
template-ordered and holds every other section to `cv.md`'s order.

**Two departures from upstream:** `a4paper` rather than `letterpaper` (Letter is
66px shorter, about four bullet lines on a CV fighting to stay on one page), and
`geometry` at 0.45in rather than the Letter-tuned `\addtolength` margin
arithmetic.

**Requires:** a TeX Live install with `titlesec`, `enumitem`, `tabularx`,
`marvosym`, `fancyhdr` and `glyphtounicode`.

**Customization:** edit this file for spacing or section order; a token name must
never appear in a comment, because the filler is a blind whole-template
replace (`strings.NewReplacer`) and will substitute into one.

### portals.example.yml

Pre-configured portal scanner with 45+ tracked companies and search queries. Contains title filters, company career page URLs, Greenhouse API endpoints, and WebSearch queries.

**To activate:** Copy to project root as `portals.yml` and customize `title_filter.positive` keywords for your target roles. Add or remove companies as needed.

### states.yml

Defines the 8 canonical application states (`Evaluated`, `Applied`, `Responded`, `Interview`, `Offer`, `Rejected`, `Discarded`, `SKIP`) with aliases for common variants. `internal/tracker` validates statuses against this file.

**Do not rename states**: the tracker depends on these exact IDs. You can add aliases if you encounter new variants that should map to an existing state.
