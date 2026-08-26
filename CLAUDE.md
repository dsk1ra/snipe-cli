# snipe-cli — local AI job search tool

Everything runs locally against Ollama. No cloud LLM calls in the pipeline.

## Two implementations live here

The Node implementation is the one that shipped through v0.4.0. The Go one is
replacing it component by component, and both are in this tree until the swap
finishes. `PORT-PLAN.md` is the running record of what has moved.

| | Node | Go |
|---|---|---|
| entry point | `./snipe`, `snipe-tui.mjs` | `bin/snipe` (`go build -o bin/snipe ./cmd/snipe`) |
| pipeline | `batch/*.mjs` | `internal/phase{1,2,3}` |
| providers | `providers/*.mjs` | `internal/providers` |
| tracker | `tracker/*.mjs` | `internal/tracker` |
| tests | `node test-all.mjs` | `go test ./...` |

The Go binary builds to `bin/` rather than `./snipe`, because the Node script of
that name is still tracked at the root. It claims the plain name when that
script retires.

Both read the same data root, so the tree under `batch/` and `data/` is shared
and either implementation can run against it.

## The one rule that matters

`batch/CLAUDE.md` remains the authority for every measured constant.
`spikeWeight = 6`, `coverageBlend = 0.4`, `lineBudget = 22`, the 160 character
proximity window on the location cap, the two mentions ecosystem threshold. Each
was measured over a real corpus and the justification lives there. Read why a
number is that number before changing it.

The same goes for `prompts/` and `templates/`. The Go `prompts/` is a byte for
byte copy of the Node originals under `batch/`. Never retype one, never reflow
one. A transcription slip changes output quality with nothing to notice it.

## The gates

The tests prove the Go code does what the Go code says. The gates prove it does
what the Node implementation did, over the real cached corpus. A component is
not ported until its gate is green, however the unit tests look.

```bash
# No model needed. Green as of the port.
node scripts/js-fit-harness.mjs   > /tmp/js-fit.tsv
go run ./cmd/gate fit     . /tmp/js-fit.tsv          # 351/351
node scripts/js-text-harness.mjs  . salary  > /tmp/js-salary.tsv
go run ./cmd/gate salary  batch/jds /tmp/js-salary.tsv   # 351/351
node scripts/js-text-harness.mjs  . cleanjd > /tmp/js-cleanjd.tsv
go run ./cmd/gate cleanjd batch/jds /tmp/js-cleanjd.tsv  # 351/351
node scripts/js-atoms-harness.mjs . > /tmp/js-atoms.tsv
go run ./cmd/gate atoms   . /tmp/js-atoms.tsv        # 68/68

# Needs Ollama, slow.
scripts/gate-phase1.sh . 16
bash scripts/gate-p3.sh .
```

Two things to know about the rest:

- A gate must not run the Node implementation in place. `staged-evaluator.mjs`
  resolves its project root as `resolve(__dirname, '..')` and writes `reports/`,
  `batch/evals/` and `batch/jds/` under it. `gate-phase2.sh` copies the Node code
  into a fixture so that root is the fixture. Running it against the real
  checkout once overwrote a real evaluation artifact.
- A gate agrees with Node, which is not the same as being right. Both can share
  a defect and agree perfectly while doing the wrong thing. Phase 2 retrieval had
  two of these. `go run ./cmd/gate goldset` is the one check that asks a human.

`SNIPE_DUMP=<dir>` writes every `/api/chat` body to a file per stage. When two
implementations produce different prose from the same model at temperature 0,
the difference is in the request.

## The retrieval-only seam

`article-digest.md` (optional, gitignored) is read by `embed.ExtractAtoms` and by
nothing in the render path. A `- ` line over 30 characters in it becomes an atom
Phase 2 can cite as evidence, and it never appears on a tailored CV. The template
is fed `config.CVRaw`, which is cv.md alone.

`config.TestCVRawIsCvMdAlone` pins it. Folding the digest into `CVRaw` to have
one CV string would print private notes on every generated PDF, and nothing
downstream would object.

Word those lines the way a job description would, not the way the candidate
would. Retrieval is cosine against the requirement text.

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

## Go layout

`internal/` is one package per concern, `cmd/snipe` is the binary and `cmd/gate`
the oracle harness. Data lives under the data root (`SNIPE_HOME`, default the
checkout), code-adjacent assets under the code root. `internal/paths` owns every
path and nothing else joins one.

```
batch/state/   local-state.tsv, batch-input.tsv, queue.txt, runner.pid, cv-bank.json
batch/cache/   cv-index.json, jd-index.json, cv-spike.json, timings.tsv
batch/{jds,scores,evals,errors,logs,work}/   one file per offer id
data/          applications.md, follow-ups.md, scan-history.tsv
```

The split is on one line: what you lose if it is gone. `rm -rf batch/cache` is
meant to be a safe thing to type, which is why `cv-bank.json`, six Opus calls to
rebuild, sits in `state/` instead. `internal/paths` has the test that pins it.

Every `SNIPE_*` env seam is load-bearing for the Go test suite. Keep them. Tests
needing Ollama, pdflatex or a real `cv.md` skip themselves, because CI has none
of the three and `cv.md` is user data that is never committed.

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

`go test ./...` covers the Go side and must stay green. The Ollama,
pdflatex and cv.md tests skip themselves.

`node test-all.mjs` — 1335 checks, must stay green. It's a launcher over
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
