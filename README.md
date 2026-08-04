```
 ███████╗███╗   ██╗██╗██████╗ ███████╗
 ██╔════╝████╗  ██║██║██╔══██╗██╔════╝
 ███████╗██╔██╗ ██║██║██████╔╝█████╗
 ╚════██║██║╚██╗██║██║██╔═══╝ ██╔══╝
 ███████║██║ ╚████║██║██║     ███████╗
 ╚══════╝╚═╝  ╚═══╝╚═╝╚═╝     ╚══════╝
local AI job search · driven from your terminal
```

# snipe-cli

[![CI](https://github.com/dsk1ra/snipe-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/dsk1ra/snipe-cli/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/dsk1ra/snipe-cli/graph/badge.svg)](https://codecov.io/gh/dsk1ra/snipe-cli)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Node](https://img.shields.io/badge/node-%E2%89%A518-brightgreen)

Paste a job description. snipe scores it against your CV, writes a full fit
report, and tailors a 2-page PDF for the roles worth your time. You drive all of
it from the **snipe TUI**, a dashboard that lives in your terminal.

Every model call goes to a local [Ollama](https://ollama.com). Your CV, your
applications and your reports never leave the machine.

snipe drafts and fills applications. It never submits them; that part stays
yours.

---

## The pipeline in one picture

```
  paste JD ─┐
  /scan  ───┤        Phase 1            Phase 2              Phase 3
  scan.mjs ─┘   ┌──────────────┐  ┌──────────────┐   ┌──────────────────┐
     queue ────►│  pre-score   │─►│   evaluate   │──►│   tailor CV +    │──► output/
                │ snipe-screen │  │  snipe-eval  │   │   snipe-cv → PDF │    *.pdf
                └──────────────┘  └──────────────┘   └──────────────────┘
                   score ≥ 2.5?      full report        score ≥ 3.0?
                                  reports/NNN-*.md
```

| Phase | Model | Output |
|-------|-------|--------|
| 1 · pre-score | `snipe-screen` (Qwen3 4B) | `batch/scores/<id>.json` |
| 2 · evaluate | `snipe-eval` (Qwen3 30B-A3B) | `reports/<NNN>-<slug>-<date>.md` |
| 3 · tailor CV | `snipe-cv` (Qwen2.5 7B Coder) | `output/<date>_<slug>_<NNN>/` (PDF) |

`snipe-embed` (Qwen3 Embedding 0.6B) backs Phase 2's evidence matching and
Phase 3's bullet selection. Scores are 0–5; snipe recommends against applying
below 4.0.

---

## Requirements

- **Node.js ≥ 18** (≥ 22.5 for the optional SQLite tracker index)
- **[Ollama](https://ollama.com)** running locally
- **Playwright** Chromium (`npx playwright install chromium`) for PDF rendering
- A GPU helps but isn't required — see [Hardware](#hardware)

## Setup

```bash
npm install
cp config/profile.example.yml config/profile.yml   # your comp/location/scoring policy
cp config/profile.template.md  config/profile.md    # your archetypes + narrative
cp templates/portals.example.yml portals.yml        # portals to scan (optional)
cp .env.example .env                                # API keys (optional)
# add your cv.md at the project root (article-digest.md too, if you have one)
```

`.env` only matters for portal scanning. `APIFY_API_KEY` unlocks the Apify-backed
providers: LinkedIn, Indeed, Glassdoor. Without a key those three are skipped and
everything else, the whole pipeline included, runs as normal.

Build the four Ollama models once from the Modelfiles:

```bash
ollama pull qwen3:4b-instruct-2507-q8_0
ollama pull qwen3:30b-a3b-instruct-2507-q4_K_M
ollama pull qwen2.5-coder:7b-instruct-q5_K_M
ollama pull qwen3-embedding:0.6b-q8_0

ollama create snipe-screen -f batch/Modelfile.snipe-screen
ollama create snipe-eval   -f batch/Modelfile.snipe-eval
ollama create snipe-cv     -f batch/Modelfile.snipe-cv
ollama create snipe-embed  -f batch/Modelfile.snipe-embed
```

Start the Ollama server with a q8_0 KV cache so the 30B model's context fits:

```bash
OLLAMA_KV_CACHE_TYPE=q8_0 ollama serve
```

---

## Using the TUI

The TUI is the front door. Launch it with:

```bash
node snipe-tui.mjs        # or: npm run snipe-tui
```

Once a second it re-reads what's on disk: the queue, the scores, the evals, the
output. So you can start a run, watch it move, and keep adding jobs while it
works. The TUI never edits pipeline state itself. The only files it writes are
its own sidecars: `batch/applied.tsv`, `batch/skipped.tsv` and
`batch/errors/<id>.txt`.

### The three tabs

Switch tabs with **←/→** or the number keys **1 · 2 · 3**.

```
        1 QUEUE          2 ACTIVITY        3 FOLLOW-UPS (n)
```

**1 · QUEUE** — the home screen. A live dashboard (queue depth, active run,
completed, CV count, average score, hit rate, P1-gated, follow-ups due) sits
above the input area where you add jobs:

```
  ┌ Paste the Job Description — or type /scan ──────────┐ ┌─────┐
  │ ▏                                                   │ │  ▶  │
  └─────────────────────────────────────────────────────┘ └─────┘
  URL (optional): ____________________________
  Add to queue
```

**2 · ACTIVITY** — a grid of everything that's moved recently. Toggle the
window with **y / m / d** (year / month / day view), step through periods with
**‹ ›**, and cycle the row type with **j / k**. Rows with a report or PDF link
can be opened.

**3 · FOLLOW-UPS** — applications that are due for a nudge, from the follow-up
cadence tracker. Press **↓** to enter the list, **Enter** to mark one nudged,
**u** to undo, **o** to open its report.

### Adding and running jobs

1. On the **QUEUE** tab, paste a job description into the box (type **/** to jump
   straight into it from anywhere on the tab).
2. Press **Enter** to walk the mini-form: **JD → URL → Add to queue**. Each Enter
   advances a step; "Add to queue" enqueues the job.
3. Move focus to **▶** (with **→** from the JD box, or **Tab**) and press
   **Enter** to run the queue. Jobs flow through all three phases, results land
   in `reports/` and `output/`, and the dashboard counters tick up as they land.

Queueing is automatic. If a run is already active, new jobs wait their turn and
get picked up when it finishes. Nothing is lost.

### When a job fails

A failed row carries its own actions — **→** focuses each, **Enter** fires it:

```
  ✗ Company — Role  see error  retry | debug
```

| Action | Does |
|--------|------|
| **see error** | Opens `batch/errors/<id>.txt` — the full, untruncated failure text (also a clickable `file://` link in terminals that support them) |
| **retry** | Re-runs the offer through all three phases, overwriting the last attempt |
| **debug** | Opens the *input* that phase read (the fetched JD, or the Phase 2 report) so you can fix it before retrying |

The usual loop is **see error → debug → edit → retry**. Expired and blocked
postings show no **retry** at all, because re-running cannot recover them.

### Slash commands

Type a command in the JD box (or just press **/** anywhere on the tab):

| Command | Does |
|---------|------|
| `/scan` | Runs the zero-token portal scanner (`scan.mjs`) and queues whatever new roles it finds |

### Keybindings

| Key | Action |
|-----|--------|
| **←/→** or **1/2/3** | Switch tabs |
| **↑/↓** | Walk every element top-to-bottom (tab → list → JD → URL → Add); ↑ past the top returns to the tab bar |
| **→** | Hop from the JD box to **▶**; on a list row, walk its inline actions (the link, or **see error · retry · debug** on a failed row) |
| **Tab / Shift-Tab** | Cycle input ↔ ▶ ↔ list |
| **Enter** | Advance the JD → URL → Add form (enqueues); on a focused row action, fire it |
| **o** | Open the result folder / report |
| **a** | Mark the selected row **applied `>`** |
| **x** | Mark the selected row **skip `-`** (mutually exclusive with applied) |
| **/** | Start a slash command |
| **Esc** | Clear the field / step out |
| **q** | Quit (when not inside an input field) |

`node snipe-tui.mjs --stats` prints the current pipeline stats as JSON and exits,
no terminal required. Useful in scripts, or over an SSH connection that has no
TTY to render into.

---

## Command line

You don't need the TUI. The `snipe` launcher and the runner work standalone:

```bash
./snipe --jd "<paste JD text>"          # add one JD and run it through the pipeline
./snipe --jdf job.txt --link <url>      # same, JD read from a file
./snipe --jd-q "<text>"                 # queue only — don't run yet
./snipe --drain                         # process everything queued
node scan.mjs                           # scan configured portals for new roles
```

Run the pipeline directly for batches:

```bash
bash batch/local-runner.sh                # all phases
bash batch/local-runner.sh --skip-phase3  # score + evaluate, no PDFs
bash batch/local-runner.sh --dry-run      # preview what would run
bash batch/local-runner.sh --only-id 42 --retry-failed      # retry failed job 42
```

See [`batch/README.md`](batch/README.md) for every flag.

---

## Hardware

Developed on an RTX 3060 6 GB with 30 GB of RAM. Phases 1 and 3 fit entirely on
the GPU; the 30B MoE evaluator auto-splits between GPU and RAM. Smaller and
CPU-only setups work too — point the phases at lighter models with
`--phase2-model` and friends.

## Tracker

`data/applications.md` is the source of truth for everything you've evaluated,
applied to, or skipped. Runs merge into it automatically; the FOLLOW-UPS tab
reads its cadence from there.

```bash
node tracker/verify-pipeline.mjs    # health check — reports, links, statuses
```

Never hand-add rows. Drop a TSV in `batch/tracker-additions/` and let
`tracker/merge-tracker.mjs` merge it. Editing an existing row's status or note is
fine. `tracker/tracker.mjs` keeps an optional SQLite index (Node ≥ 22.5) that is
safe to delete; it regenerates on the next sync.

## Tests

```bash
node test-all.mjs      # full suite, must stay green
npm run typecheck      # tsc over the JSDoc types, also in CI
npm run coverage       # same suite under c8 → coverage/lcov.info
```

Coverage counts every `.mjs` file, including the ones the suite never loads. No
exclusions to flatter the number.

None of it needs a GPU, a terminal or the network. The Ollama-driven phases run
for real against a stand-in model server. The TUI is driven headlessly through a
fake TTY. `scan.mjs` scans a sandboxed fixture portal, and the job-board
providers get a stub transport instead of a socket.

## Data & privacy

Everything personal stays on your machine and is gitignored. Only the system
layer — scripts, modes, templates — is tracked:

- **What you wrote** — `cv.md`, `article-digest.md`, `config/profile.*`,
  `portals.yml`, `.env`
- **What the pipeline produced** — `data/`, `reports/`, `output/`,
  `interview-prep/`
- **Working artifacts**, easy to overlook but just as personal: `batch/jds/`
  (full text of every JD fetched), `batch/scores/`, `batch/evals/`,
  `batch/errors/`, `batch/logs/`, and `batch/local-state.tsv`

Outbound traffic is limited to the job boards and APIs you configure yourself.
Every model call goes to Ollama on localhost.

## License

MIT — see [`LICENSE`](LICENSE).

---

Deeper reading: [`CLAUDE.md`](CLAUDE.md) for the data contract ·
[`docs/SETUP.md`](docs/SETUP.md) · [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) ·
[`docs/CUSTOMIZATION.md`](docs/CUSTOMIZATION.md) · [`docs/SCRIPTS.md`](docs/SCRIPTS.md)
