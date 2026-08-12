# snipe-cli — local AI job search tool

Driven from the **snipe TUI** (`snipe-tui`).
Everything runs locally against Ollama — no cloud LLM calls in the pipeline.

## Data contract

**User layer — never auto-update, all personalization goes here:**
`cv.md`, `config/profile.yml`, `config/profile.md`, `article-digest.md`,
`portals.yml`, `data/*`, `reports/*`, `output/*`, `interview-prep/*`

**System layer — safe to edit as code:**
`modes/_shared.md` and other modes, `*.mjs`, `batch/*`, `templates/*`, this file.

**Rule:** customization (archetypes, narrative, comp targets, location policy,
scoring weights) goes in `config/profile.md` or `config/profile.yml`. Never
`modes/_shared.md`.

`config/profile.md` is a hard dependency — `batch/ollama-scorer.mjs` and
`batch/staged-evaluator.mjs` read it at runtime.

`cv.pinned_projects` in `config/profile.yml` forces a project past the
`maxProjects` cut whatever the posting scores it, matched case-insensitively as a
substring of the `### ` title. It is the escape hatch for an entry whose value is
not its subject — an Honours dissertation loses the cut on any posting with no
use for its topic, and is the first thing a reader of a graduate CV looks for.
Each pin spends one of the three slots, so it is a project the ranker no longer
chooses. **It also moves the benchmark**: the bench runs `local-pdf-offer.mjs`,
which reads this file, so a pin changes selection for every arm. Note it when
comparing against a run made before the pin existed.

## Entry points

```
snipe-tui.mjs (ink/react)
  ├─ snipe ──────── batch/local-runner.sh   the 3-phase pipeline
  ├─ scan.mjs ────────── providers/*.mjs         portal scan
  ├─ batch/import-pipeline.mjs                   data/pipeline.md → batch input
  └─ tracker/followup-cadence.mjs --json         follow-up due list
```

`snipe --jd "<text>"` adds a pasted JD and runs it. A run already holding
`batch/local-runner.pid` causes queueing instead; `snipe --drain` processes
the queue.

Deeper TUI behaviour — failed-row retry/debug semantics, the footer message and
hint rules, P1-gated rows and `proceed?` — is in `docs/TUI.md`.

## Where the rest of the rules live

Read the file for the directory you are working in. Each is authoritative for
its own area; this file holds only what applies everywhere.

| file | covers |
|---|---|
| `batch/CLAUDE.md` | the 3-phase pipeline, models, hardware, benchmarking and its rules |
| `test/CLAUDE.md` | the test seams — fake Ollama, TUI driver, the `SNIPE_*` env vars |
| `tracker/CLAUDE.md` | tracker rules and the TSV contract |
| `docs/README.md` | index of the measurement ledgers — which one answers which question |
| `docs/TUI.md` | `snipe-tui.mjs` interaction detail |

## Tests

`node test-all.mjs` — 1326 checks, must stay green. It's a launcher over
`test/*.test.mjs` (shared `test/harness.mjs`); run one suite in isolation with
`node test/<name>.test.mjs`.

`npm run typecheck` — `tsc --noEmit` over the JSDoc types, also green, also in CI.
`npm run coverage` — the same suite under c8 (~90% lines, ~77% branches); CI uploads
`coverage/lcov.info` to Codecov. `all: true` in `.c8rc.json` counts never-loaded
files, so the number stays honest rather than flattered by exclusions.
`checkJs` is off in `tsconfig.json`: a file opts in by starting with `// @ts-check`
(24 do today), so adding a new one is a per-file decision rather than a repo-wide
gate. `providers/_types.js` is the shared type catalog.

## Conventions

Node `.mjs` ESM, YAML config, markdown data. Reports numbered sequentially,
3-digit zero-padded, max existing + 1. Output in `output/` (gitignored), JDs in
`batch/jds/`. Never hardcode CV metrics — read them from `cv.md` /
`article-digest.md` at evaluation time.

## Ethics

Quality over quantity. Below 4.0/5, recommend against applying. **Never submit
an application** — draft, fill, generate, then stop and let the user send it.
