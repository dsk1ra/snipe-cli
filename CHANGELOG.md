# Changelog

Notable changes to snipe, newest first. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versions follow [semver](https://semver.org/spec/v2.0.0.html) — while snipe is 0.x, a minor bump is allowed to break things.

## [Unreleased]

### Added

- `snipe registry` builds `data/registry.tsv`, the scanner's company list from
  public data instead of portals.yml: every employer the gender pay gap service
  lists (all UK employers with 250+ staff, 11,231 in October 2026), and for each
  one with a website, the ATS behind its careers page and the board URL it
  answers on. It reads the home page, the careers link on it and the "search
  jobs" link on that, and recognises 36 ATSs and career-site platforms. A
  resolution is reused for 30 days, and a stopped build keeps what it resolved.
  `snipe scan` fetches every registry board a provider can read, alongside
  portals.yml's, whenever the registry exists.
- A Workday provider. `careers_url: https://<tenant>.wdN.myworkdayjobs.com/<site>`
  (or the `myworkdaysite.com/recruiting/` form) is detected like any other ATS,
  and an optional `country:` keeps only that country's postings. 169 of the 305
  reachable Workday boards in the registry have a country facet, which cuts
  their 66,202 postings to 5,515 in the UK before anything is fetched; for the
  other 136, each office's country is read off one posting and cached, and a
  "3 Locations" posting is placed only once it has passed the title filter. On
  the first full scan, 27 of a 60-posting sample of the new Workday matches were
  multi-location ones that had passed the location filter blank, Juniper's Ohio
  sales roles among them. Phase 1 reads Workday descriptions from the same API,
  since the posting page is a script shell and the HTML fallback got nothing
  from it.
- `config/skills.yml`, approved spellings of cv.md's skills. The tailored skills
  block prints the one the posting uses ("RESTful APIs" where cv.md says "REST
  APIs"), and a skill named only by an alias now counts as named. Rules and the
  traps (compound items, ordinary English words) are in
  `config/skills.example.yml`.
- `go run ./cmd/gate skills <home>` scores the skills block in the posting's own
  spelling over every cached JD, with and without aliases. With the first nine,
  48 alias-only mentions across 351 JDs went from 0 carried to all 48, and none
  of the 1,830 cv.md-spelled ones moved.
- Skill spellings are suggested from the postings and approved in the TUI.
  When a run ends, report keywords that name nothing in cv.md are matched to the
  nearest cv.md item by embedding; those in two or more postings scoring 0.68
  or better are queued (46 over 228 reports, in 8–10 s). The review shows the
  skills block the posting would get and the whole queue, this posting's terms
  first and quoted. Enter adds the spelling to `config/skills.yml` and `n`
  rejects it for good. Nothing applies itself, and an item with a spaced ` / `
  is refused until it is split in cv.md.

### Changed

- The binary builds to `./snipe` at the repo root, which the docs now say: it
  finds prompts/ and templates/ beside itself, so `./bin/snipe` only worked from
  the repo root. CI also runs on pushes to `develop`, not only on pull requests
  into it.
- `location_filter.regions` places postings with a UK gazetteer before the
  keyword tiers: `london`, `west-midlands` (the metropolitan county), `uk-wide`
  and `uk`. Substring matching had kept "Sydney, New South Wales" on "Wales" and
  "Birmingham, AL" on "Birmingham", and dropped a bare "Coventry" or "Solihull"
  for naming no keyword. Without `regions` the filter is unchanged. The scan's
  summary counts the new postings by place when it is on.
- `snipe scan` fetches eight boards at a time instead of one. The postings are
  still filtered in entry order once every fetch is in, so which copy of a
  duplicate is kept does not depend on which board answered first.
- Phase 2's `north_star` is read from the archetype's `fit` tier in
  `profile.yml` (primary 5, secondary 3, adjacent 2, unlisted 1) and then capped
  as before; the model no longer scores it. It had been doing the lookup anyway,
  scoring every primary archetype 5. Stage 3's `archetype` is now an enum of the
  profile's list, as Phase 1's already was, so the lookup gets an exact label. On
  the 18 labelled offers the lookup's own moves all went toward the labels.
- The location cap has a hybrid tier. A `search_locations` entry that allows
  hybrid but not on-site, such as `London — hybrid or remote`, makes a posting
  there acceptable unless it asks for full-time attendance (five days a week, or
  on-site only). A hybrid posting offering that city alongside another ("London
  or Manchester, hybrid") is read as the acceptable one, and a hybrid office near
  Liverpool Street no longer reads as Liverpool. With London set to hybrid, the
  capped postings among 351 cached JDs fell from 52 to 14, every one of them
  full-time on-site, and pair accuracy on the 18 labelled offers rose from 63.3%
  to 77.7%.
- The tailored skills block names a skill only when the posting contains one of
  its forms whole (the item itself, a side of a ` / `, an approved alias). One
  shared word used to be enough, so "engineering" named a category in 241 of 351
  cached postings and the category cap never bound. The limits are now 5
  categories, 7 items and a floor of 3, down from 6, 12 and 3. Over the 351
  postings the block falls from 37 items to about 22 and from an estimated 9.8
  lines to 5.3, and keeps 2,568 of 2,574 named mentions. `gate p3` stops
  comparing skills with the Node oracle, which keeps the old rule; `gate skills
  --sweep` measures the block instead.
- `v`, and a finished row's action (now "skills", was "report"), open that
  review instead of the report. Reports are still written to `reports/`, and
  the follow-ups board still opens them.

### Fixed

- `batch/bench-tools/rho.mjs` crashed on an undefined `dirs` whenever it was run
  from the command line.
- The three model gates all ran the oracle from paths that no longer resolve.
  `gate-phase1.sh` and `gate-p3.sh` invoked `batch/ollama-scorer.mjs` and
  `batch/local-pdf-offer.mjs`, both cut from the 0.6.0 keep-list by mistake;
  `gate-phase2.sh` copied `tracker/*.mjs`, which went with the Node tooling, and
  died at the copy under `set -e`. Only the four no-model gates were run when
  0.6.0 landed, which is why none of this showed.
- The restored oracle scripts read their prompts from `batch/`, where 0.6.0's
  deduplication removed them. They read `prompts/` now, the same file
  `internal/config.Prompt` reads.
- Four gate scripts defaulted their oracle root to `$HOME/projects/snipe`, which
  resolved only because this checkout sits at that path, and used it unresolved,
  so a relative argument produced a self-referential `node_modules` symlink.
- `gate-phase1.sh` ran the oracle in place, against the rule that a gate must
  not. `ollama-scorer.mjs` writes the JD it scored into its own `batch/jds/`.
  It runs from a fixture now, and `gate-p3.sh` pins `SNIPE_ADDITIONS` to scratch
  so a tracker row cannot depend on an argument.
- `TestLadderProducesAOnePagePDF`, the only check that the emitted `.tex`
  compiles, looked for one hardcoded past application and skipped itself
  everywhere else.
- The demo fixture wrote a real person's name into a PDF filename while seeding
  a profile that calls the candidate "A. Candidate".
- `TestApifyPostedAtAcceptsBothShapes` hardcoded 2026-08-26 as a date "this
  week" and had failed every day since. It compares against today.
- Only the first `search_locations` entry in `profile.yml` was ever read: the
  pattern ended at `$`, and under multi-line mode the lazy match stopped at the
  first line end. `batch/fit-rules.mjs` has the same bug, so `gate fit` now
  differs from the Node oracle wherever a later entry matters.
- Editing `profile.md` did not re-run a cached Phase 2 judgment. The judgment
  prompt includes the profile but its checkpoint key did not, so an offer
  re-run after a profile edit kept the judgment written under the old one. The
  key changed shape, so every cached judgment re-runs once on its next pass.
- Whole-phrase skill naming missed the posting's other grammatical number:
  "code reviews" never named Code Review (51 of 351 cached postings), nor
  "event-driven architectures" Event-Driven Architecture (21). Token overlap
  had caught both on a single shared word. A form now matches its singular or
  plural too, within limits that keep AWS, CSS and "reacts" out, and the block
  prints the number the posting used.
- A posting that names no tool by its full name got cv.md's first five skill
  categories in order, because an item not named whole scored nothing for
  ranking either. Whole-phrase naming still decides what may not be cut, and
  token overlap ranks again. On the live CV's 394 postings, printed items
  sharing a word with the posting rose from 27% to 43%, and 7 of 8 "Security
  Engineer" postings get a Security row, up from 5.
- Accepting an alias wrote `config/skills.yml` by renaming a temporary file
  over it, which replaced a symlinked `skills.yml` with a plain file. It now
  writes through the link to the file behind it.

### Removed

- Phase 2's report drops Comp & Demand, LinkedIn changes, Interview Prep and
  Posting Legitimacy, and the judgment call stops writing them. Nothing
  downstream read them, yet they were most of that call's output: on the 18
  labelled offers it fell from 3,022 to 982 tokens and from 158 s to 57 s, about
  95 s off every offer once the stage reload is counted. Pair accuracy moved by
  one offer. The report runs A–D now; a posted salary keeps one header line,
  since it still carries a fifth of the score.
- `scripts/import-data.sh`. It copied the user layer out of the Node checkout,
  and its own guard refuses when source and destination are the same tree.

## [0.6.0] - 2026-08-26

The Node implementation retires. snipe is a Go binary now.

### Added

- An Apify provider in Go. LinkedIn, Indeed and Glassdoor were three enabled
  entries in `portals.yml` that the Go scanner could not serve, and fixing that
  took the scan from 16 new postings to 53 on the same config. Built-in field
  maps for the three actors, per-key `field_map` overrides, and results cached
  on disk keyed on the actor and its input, because runs are billed.
- `Options.Method`, `Options.Body` and `Options.Timeout` on the provider
  transport. All three are zero-valued for a plain GET, so the nine existing
  providers were untouched.
- `.env` is read at startup, about twenty lines rather than a dotenv dependency.
  Nothing on the Go side read it before, so a configured `APIFY_API_KEY` was
  invisible.

### Changed

- **The Node implementation is gone.** The pipeline, TUI, runner, scanner, all
  twenty providers, the tracker tooling and the 1363-check test suite. Recover
  any of it from the `oracle/node-complete` tag.
- Two pieces stayed, and neither is on the path of a run: the oracle under
  `batch/` that `cmd/gate` diffs against, and `batch/bench-tools/`, the only way
  to re-derive a measured constant.
- The npm tree is one package. `ink`, `react`, `playwright`, `dotenv`, `c8`,
  `typescript` and `@types/node` all go, and setup no longer downloads a browser.
- CI runs one job. The Node job took `npm ci`, a Chromium install, `tsc` and `c8`
  over a suite that no longer exists.
- One copy of the prompts and Modelfiles. All seven under `batch/` were
  byte-identical duplicates of `prompts/`, and two copies meant an edit could
  reach the binary and not the harness, leaving a gate green against a stale
  prompt.

### Removed

- Cover letters. The feature was never finished, and removing it took the last
  consumer of Playwright and of the HTML render path with it.
- Liveness verification, which `internal/scan` had already documented as a
  deliberate omission.
- `docs/SCRIPTS.md` and `docs/local-parser-cookbook.md`, which described only
  deleted scripts.
- `PORT-PLAN.md`, the Node to Go transition plan, along with its history. The
  port is finished, so the plan described a transition that no longer has two
  sides. Three Phase 3 comments cited its section 2 for why the 7B tailor arm
  was not ported; each now states the reason instead of pointing at a file.
- `CLAUDE.md` is untracked and gitignored. It briefs a coding agent working in
  this checkout rather than describing the project. The data contract and the
  entry-point map were the parts other files cited, and neither is agent
  specific, so both moved to `docs/ARCHITECTURE.md`. `batch/CLAUDE.md` stays
  tracked: it is the authority for every measured constant.

### Security

- The Apify token is sent as a bearer header instead of a URL query parameter.
  As a query parameter it was printed in full by every transport error, which
  put it in terminal scrollback and any log capturing scan warnings. A test
  asserts it reaches neither the request URL nor any error text.

## [0.5.0] - 2026-08-26

The Go port lands. Both implementations now live in this repo, and the Go one
runs the whole pipeline end to end. Nothing is removed yet: the Node code is
untouched and still the one the `snipe` script drives, so this release adds a
second implementation rather than swapping one for the other.

### Added

- A Go implementation of the whole pipeline, one package per concern under
  `internal/`, with `cmd/snipe` as the binary and `cmd/gate` as the harness that
  compares it against the Node original. Build it with
  `go build -o bin/snipe ./cmd/snipe`.
- A Bubble Tea TUI covering the queue, the activity view, follow-ups, the report
  pane and the per-row actions, with a snapshot suite that pins the frame.
- `cmd/gate`, which diffs Go output against Node output over the real cached
  corpus, one subcommand per seam. Green at this release: `fit` 351 of 351,
  `salary` 351 of 351, `cleanjd` 351 of 351, `atoms` 68 of 68.
- The demo recording rig is now committed: the vhs tapes, the sandbox generator
  and the caption tool. Without them the mp4 and gif in the README were binaries
  nobody could reproduce. Everything the generator invents is fake and it reads
  no `cv.md`, no tracker and no model.

### Changed

- The Go binary builds to `bin/snipe` rather than `./snipe`, because the Node
  script of that name is still tracked at the repo root. It takes the plain name
  when that script retires.
- `CLAUDE.md` describes both implementations for as long as both are here. The
  Node data contract, entry points and conventions are unchanged.
- The JS gate harnesses resolve their repo path to an absolute path from any
  input and default to this repo root. A dynamic `import()` resolves against the
  module while `readFileSync` resolves against the process, so a relative
  argument quietly meant two different directories.

### Fixed

- A YAML scalar of any type now decodes as text in the Go config loader. An
  unquoted phone number is a YAML integer, and the failure would have taken the
  archetype list and the score threshold down with it.
- The README and `docs/ARCHITECTURE.md` carried claims that no longer matched the
  code. The architecture diagram still rendered the PDF from HTML through
  Puppeteer, which stopped being true in 0.4.0; scoring was documented as ten
  weighted dimensions when it is three; the PDF naming convention described a
  filename nothing has ever written; and archetypes were described as a fixed set
  of six rather than whatever `config/profile.yml` defines. The README claimed a
  4.0 recommendation threshold that exists nowhere in the code.

### Security

- Removed a real phone number that was serving as the worked example in a source
  comment, and the hardcoded local home directory that five gate harnesses used
  as their default repo path.

## [0.4.0] - 2026-08-13

The tailored-summary release. Pre-release: the location cap changes what the
pipeline recommends, and that wants real use before anyone calls it stable.

### Added

- A location cap. Phase 1 and Phase 2 hold a role to 2/2 when it asks for office
  attendance somewhere outside your commutable base, which lands the composite
  under both phases' gates. The base is read from `location.city` and
  `search_locations` in `config/profile.yml`, so the policy stays yours. Both
  signals have to appear within 160 characters of each other, because a posting
  names every city it keeps an office in.
- `SNIPE_PROJ_MAX_LINES` caps the lines Projects may take of the page budget,
  default 14. Set it to 0 for the old behaviour.
- The top experience entry is floored at two bullets, so a page crowded with
  projects cannot reduce your current job to a single line.
- The generation harness measures how the page divides between Experience and
  Projects, and whether a summary credits a figure to the entry that earned it.

### Changed

- The summary follows the standard CV template: a positioning line, the
  requirements your evidence answers in the posting's own wording, then one
  quantified achievement.
- The summary stage drafts twice and ships the better of the two. Summaries
  reaching the page with no quantified achievement at all became rare.
- Ecosystem detection reads experience, projects and education, and ignores the
  `## Skills` catalogue. A technology appearing only in the catalogue stops
  counting as coverage, so a posting built entirely on one no longer reads as a
  match.
- The runner warns at startup when `cv.pinned_projects` names a project that no
  longer exists in `cv.md`. That warning used to go to a log file which is
  opened only when an offer fails.

### Fixed

- A summary claiming something `cv.md` cannot support is thrown away and
  re-requested with the posting's requirements withheld. It used to be patched
  in place, which left the claim standing in a shorter sentence.
- A figure belonging to a different entry is rejected. Two numbers can both be
  real and the sentence still credit the wrong piece of work.
- `skill_coverage` looked for the CV's exact string, so it scored a fraction of
  the taxonomy and reported full marks. A spaced slash now reads as "either of
  these".
- The selection sweep simulated a funnel the pipeline stopped using on
  2026-08-08, so its numbers described code nobody runs.

## [0.3.0] - 2026-08-11

### Added

- `scan` verifies that a posting is still live by default, and degrades to an unverified scan when the browser won't launch. `--no-verify` skips the check.

### Changed

- Agent instructions split by directory, so `batch/`, `test/` and `tracker/` each own their rules.
- The test suite reaches the validators, the pipeline importer and the provider reject paths that were never exercised.

### Fixed

- The job role on the tailored CV has its own styling instead of inheriting the location's.
- `validate-portals.mjs` no longer runs its CLI when something imports it.

## [0.2.1] - 2026-08-10

### Changed

- README shows the TUI running, and drops the prose the screenshots made redundant.

## [0.2.0] - 2026-08-09

The one-page CV release.

### Added

- Phase 3 rations the page in lines rather than bullets, and the layout ladder targets a single A4 page.
- The skills block ships what the posting asked for.
- `cv.pinned_projects` in `config/profile.yml` forces a project past the relevance cut.
- `--cv-file` runs the tailor against a curated CV selection.
- Contact details and repository references render as real links.
- The P1 gate asks a question and waits for an answer instead of blocking silently.

### Changed

- Header is centred and the name scales with it; the achievement gets its own row.
- Three projects, 24 lines between them, and nothing slices the skills block.
- TUI footer messages clear themselves after five seconds.

### Fixed

- The bullet margin is charged against the budget, which was off by a line without it.
- The CV's location is indexed, so a posting's "based in" requirement can be graded.

## [0.1.0] - 2026-08-08

First tagged release. The three-phase local pipeline (pre-score, evaluate, tailor) driven from the snipe TUI, with portal scanning, the application tracker, PDF generation, the Phase 3 benchmark harness, and CI over the test suite and the JSDoc types.

[Unreleased]: https://github.com/dsk1ra/snipe-cli/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/dsk1ra/snipe-cli/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/dsk1ra/snipe-cli/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/dsk1ra/snipe-cli/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/dsk1ra/snipe-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/dsk1ra/snipe-cli/releases/tag/v0.1.0
