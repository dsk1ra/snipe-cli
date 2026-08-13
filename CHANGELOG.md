# Changelog

Notable changes to snipe, newest first. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versions follow [semver](https://semver.org/spec/v2.0.0.html) — while snipe is 0.x, a minor bump is allowed to break things.

## [Unreleased]

_Nothing yet._

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
