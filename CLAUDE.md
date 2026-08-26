# snipe-cli — local AI job search tool

Everything runs locally against Ollama. No cloud LLM calls in the pipeline.

## What is Go and what is not

snipe is a Go binary. `internal/` is one package per concern, `cmd/snipe` is the
binary and `cmd/gate` the oracle harness. Build with
`go build -o bin/snipe ./cmd/snipe`.

The Node implementation it replaced is gone as of v0.5.0, except two things that
never had a Go equivalent and are not on the path of a run:

| what | where | why it stayed |
|---|---|---|
| the gate oracle | `batch/*.mjs` (14 files) | `cmd/gate` diffs Go against it. Delete it and nothing can check the port again |
| the benchmark harness | `batch/bench-tools/` | the only way to re-derive a measured constant |

Everything else is recoverable from the `oracle/node-complete` tag:

```bash
git worktree add /tmp/node-oracle oracle/node-complete
```

Deliberately not ported and now simply gone: cover letters, liveness
verification, and the Playwright dependency both needed. The CV renders from
LaTeX, so no HTML path survives.

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
`internal/*`, `cmd/*`, `prompts/*`, `templates/*`, `modes/*`, `scripts/*`, and
the Node oracle under `batch/` that the gates compare against, plus this file.

**Rule:** customization (archetypes, narrative, comp targets, location policy,
scoring weights) goes in `config/profile.md` or `config/profile.yml`. Never
`modes/_shared.md`.

`config/profile.md` is a hard dependency: `internal/phase1` and
`internal/phase2` read it at runtime.

`cv.pinned_projects` in `config/profile.yml` forces a project past the
`maxProjects` cut whatever the posting scores it, matched case-insensitively as a
substring of the `### ` title. It is the escape hatch for an entry whose value is
not its subject — an Honours dissertation loses the cut on any posting with no
use for its topic, and is the first thing a reader of a graduate CV looks for.
Each pin spends one of the three slots, so it is a project the ranker no longer
chooses. **It also moves the benchmark**: the sweeps under `batch/bench-tools/`
read this file, so a pin changes selection for every arm. Note it when
comparing against a run made before the pin existed.

## Entry points

```
bin/snipe                    the TUI (default)
  ├─ snipe run               the three-phase pipeline
  ├─ snipe scan              portals.yml → internal/providers → the queue
  ├─ snipe add               one pasted JD, queued or run
  └─ snipe tracker fix       canonical statuses, one row per opening
```

`snipe add -jd "<text>"` adds a pasted JD and runs it. A run already holding
`batch/state/runner.pid` queues instead; `snipe run` drains the queue.

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
| `batch/CLAUDE.md` | the measured constants, the models, the hardware, and the benchmark rules |
| `docs/README.md` | index of the measurement ledgers — which one answers which question |
| `docs/ARCHITECTURE.md` | how the pieces fit together |

`test/CLAUDE.md` and `tracker/CLAUDE.md` went with the code they documented.
The Go equivalents live in package doc comments, which is where a Go reader
looks first.

## Tests

`go test ./...` must stay green. The tests needing Ollama, pdflatex or a real
`cv.md` skip themselves, which is what lets CI run the suite with none of the
three.

The gates are the other half and they are not in CI, because they need the
cached JD corpus and a real `cv.md`. Run them locally:

```bash
npm install          # js-yaml, nothing else
npm run gate:fit     # 351/351   npm run gate:cleanjd  # 351/351
npm run gate:salary  # 351/351   npm run gate:atoms    # 68/68
```

Every `SNIPE_*` env seam is load-bearing for the suite. Keep them.

## Conventions

Node `.mjs` ESM, YAML config, markdown data. Reports numbered sequentially,
3-digit zero-padded, max existing + 1. Output in `output/` (gitignored), JDs in
`batch/jds/`. Never hardcode CV metrics — read them from `cv.md` /
`article-digest.md` at evaluation time.

## Ethics

Quality over quantity. Below 4.0/5, recommend against applying. **Never submit
an application** — draft, fill, generate, then stop and let the user send it.
