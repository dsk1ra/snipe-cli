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

Paste a job description and snipe scores it against your CV, writes a full fit
report, and tailors a 2-page PDF for the roles worth applying to — all from a
terminal cockpit, the **snipe TUI**.

**Everything runs locally against [Ollama](https://ollama.com). No cloud LLM
calls in the pipeline.** Your CV, applications, and reports never leave the machine.

snipe drafts and fills applications — it never submits them. You send them.

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
# add your cv.md at the project root
```

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

It's a pure consumer of on-disk state — it reads the queue, scores, evals, and
output every second, so you can watch a run progress live while you keep adding
jobs. Kick off a run and the pipeline keeps churning in the background.

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
   **Enter** to run the queue. Jobs flow through all three phases; results land
   in `reports/` and `output/` and the dashboard counters tick up live.

Queueing is automatic: if a run is already active, new jobs wait and get picked
up when it finishes — nothing is lost.

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
| **→** | Hop from the JD box to **▶**; on a list row with a link, focus the link |
| **Tab / Shift-Tab** | Cycle input ↔ ▶ ↔ list |
| **Enter** | Advance the JD → URL → Add form (enqueues); on a focused link, open it in the browser |
| **o** | Open the result folder / report |
| **a** | Mark the selected row **applied ✉** |
| **x** | Mark the selected row **skip ⊘** (mutually exclusive with applied) |
| **/** | Start a slash command |
| **Esc** | Clear the field / step out |
| **q** | Quit (when not inside an input field) |

Run `node snipe-tui.mjs --stats` for a no-TTY self-check that prints the current
pipeline stats and exits — handy in scripts or over SSH without a terminal.

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

Developed on an RTX 3060 6 GB + 30 GB RAM. Phases 1 and 3 fit fully on GPU; the
30B MoE evaluator auto-splits between GPU and RAM. Smaller or CPU-only setups
work with smaller models — override with `--phase2-model` and friends.

## Tests

```bash
node test-all.mjs   # 269 checks, must stay green
```

## Data & privacy

`cv.md`, `config/profile.*`, `portals.yml`, `data/`, `reports/`, `output/`, and
`interview-prep/` hold your personal data and are gitignored. Only the system
layer (scripts, modes, templates) is tracked. Full architecture and the data
contract live in [`CLAUDE.md`](CLAUDE.md) and [`docs/`](docs/).
