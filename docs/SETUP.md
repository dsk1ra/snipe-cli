# Setup Guide

## Prerequisites

- [Node.js](https://nodejs.org) 22.5+ — `tracker/tracker.mjs` uses the built-in
  `node:sqlite`, which lands in 22.5
- [Ollama](https://ollama.com) with a GPU (see Hardware in `CLAUDE.md`)
- ~28 GB free disk for the four models

## Install

```bash
git clone https://github.com/dsk1ra/snipe-cli.git
cd snipe-cli
npm install
npx playwright install chromium   # Phase 3 renders PDFs headless
```

## Models

Start the server with a quantized KV cache — the 30B will not fit 12k context
without it:

```bash
OLLAMA_KV_CACHE_TYPE=q8_0 ollama serve
```

Pull the bases, then build the four `snipe-*` models:

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

| Model | Role | Base |
|-------|------|------|
| `snipe-screen` | Phase 1 cheap pre-score | Qwen3 4B q8_0 |
| `snipe-eval` | Phase 2 deep evaluation | Qwen3 30B-A3B Q4_K_M |
| `snipe-cv` | Phase 3 CV tailoring | Qwen2.5 7B Coder Q5_K_M |
| `snipe-embed` | Evidence match + bullet selection | Qwen3 Embedding 0.6B q8_0 |

## Personalize

These are the user layer — nothing overwrites them. Each ships with a template;
copy it, then fill in your own details:

| Your file | Copy from | What it holds |
|-----------|-----------|---------------|
| `cv.md` | `examples/cv-example.md` | your CV in markdown |
| `article-digest.md` | `examples/article-digest-example.md` | proof points the pipeline pulls from (optional) |
| `config/profile.yml` | `config/profile.example.yml` | name, target roles, comp targets, thresholds |
| `config/profile.md` | `config/profile.template.md` | archetypes, narrative, location policy (read at runtime by the scorer and evaluator) |
| `portals.yml` | `templates/portals.example.yml` | companies for the zero-token scanner |
| `.env` | `.env.example` | API keys (only needed for Apify-backed scans) |

```bash
cp examples/cv-example.md cv.md
cp config/profile.example.yml config/profile.yml
cp config/profile.template.md config/profile.md
cp templates/portals.example.yml portals.yml
cp .env.example .env            # optional; only for Apify scans
```

## Run

```bash
npm run snipe-tui        # the cockpit: paste a JD, run the queue, review
snipe --jd "<text>"      # one-off from the shell
snipe --drain            # process anything queued
```

## Verify

```bash
node cv-sync-check.mjs             # config consistency
node tracker/verify-pipeline.mjs   # tracker integrity
node test-all.mjs                  # full suite
```
