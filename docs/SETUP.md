# Setup Guide

## Prerequisites

- [Go](https://go.dev) 1.25+
- [Ollama](https://ollama.com) with a GPU (see Hardware in `batch/CLAUDE.md`)
- TeX Live with `pdflatex`, for the PDFs (packages in `docs/CUSTOMIZATION.md`)
- ~23 GB free disk for the three models

## Install

```bash
git clone https://github.com/dsk1ra/snipe-cli.git
cd snipe-cli
go build -o snipe ./cmd/snipe
```

## Models

Start the server with a quantized KV cache — the 30B will not fit 12k context
without it:

```bash
OLLAMA_KV_CACHE_TYPE=q8_0 ollama serve
```

Pull the bases, then build the three `snipe-*` models:

```bash
ollama pull qwen3:4b-instruct-2507-q8_0
ollama pull qwen3:30b-a3b-instruct-2507-q4_K_M
ollama pull qwen3-embedding:0.6b-q8_0

ollama create snipe-screen -f prompts/Modelfile.snipe-screen
ollama create snipe-eval   -f prompts/Modelfile.snipe-eval
ollama create snipe-embed  -f prompts/Modelfile.snipe-embed
```

| Model | Role | Base |
|-------|------|------|
| `snipe-screen` | Phase 1 cheap pre-score | Qwen3 4B q8_0 |
| `snipe-eval` | Phase 2 deep evaluation | Qwen3 30B-A3B Q4_K_M |
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
./snipe                 # the TUI: paste a JD, run the queue, review
./snipe add              # add a pasted JD and run it
./snipe run --drain      # work through the queue
./snipe build            # rebuild from this checkout, then open the TUI
```

## Verify

```bash
./snipe doctor           # setup, user layer and tracker checks
go test ./...            # the suite CI runs
```
