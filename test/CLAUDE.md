# test/ — the seams

`node test-all.mjs` must stay green; see the root `CLAUDE.md` for the suite
overview and the typecheck/coverage gates.

## Testing things that need a model, a TTY, or the network

Three seams carry most of the suite, and reaching for the right one is usually
the whole problem:

- **`test/fake-ollama.mjs`** — an HTTP stand-in for Ollama. `/api/chat` and
  `/api/generate` read the JSON Schema the caller passes as `format` and
  synthesise a conforming answer, so it needs no per-stage knowledge and does not
  drift when a schema changes; `onChat` overrides it wholesale (the classic
  evaluator wants `<REPORT>`/`<SUMMARY>` prose, not JSON). Every phase script
  takes `--ollama-url`, so pointing it at the fake runs the real code end to end.
  Spawn the script — c8 works through `NODE_V8_COVERAGE`, so a subprocess counts.
  Use `runNodeAsync` from the harness, never `run()`: `execSync` blocks the event
  loop and the fake server never answers.
- **`test/tui-driver.mjs`** — fakes both TTYs, feeds key bytes, and captures the
  frames. It sets `FORCE_COLOR` because chalk's level is decided off a pipe as 0,
  which would erase the `inverse` that marks the focused row.
- **`SNIPE_PORTALS` + a temp cwd** — `scan.mjs` resolves its portal list from that
  env var and writes `data/` relative to the cwd, so a scan can run fully
  sandboxed. A `local-parser` portal makes it offline and deterministic.
- **`SNIPE_BENCH_DIR`** — redirects the bench root that `retrieval-bench.mjs` and
  `tailor-harness.mjs` resolve `BENCH` from, so a fixture run cannot touch the real
  one. That matters more than sandboxing usually does: `batch/bench/` holds a 28 MB
  embedding cache and `bench/tailor/` a 5 MB one, both keyed by model fingerprint,
  so a run with a fake embedder overwrites them and the undo is a full re-embed.
  `BENCH` is a module-level const, so the env var must be set **before the import**
  — and `units.test.mjs` imports `tailor-harness.mjs` first without it, which is why
  `test/bench.test.mjs` imports that one under a `?bench-root=` query to force a
  fresh instance. Coverage still attributes to the same file.
- **`SNIPE_HOME`** — `snipe-tui.mjs` splits its *data* root from its *code* root:
  state, queue, applied/skipped, `batch-input.tsv`, `reports/`, `output/` and the
  tracker all resolve from `SNIPE_HOME`, while the scripts it shells out to
  (`local-runner.sh`, `scan.mjs`, `import-pipeline.mjs`) stay on the repo. The TUI
  test builds its whole fixture in a temp home, so a run killed mid-test cannot
  leave `#99000x` rows in the real queue — which is exactly what it used to do.
- **`SNIPE_TRACKER`** — `tracker/paths.mjs` reads it before either default
  layout, so every tracker script can be pointed at a fixture `applications.md`
  in a temp dir instead of the real one. `snipe-tui.mjs` honours it too, so the
  TUI and the tracker scripts it spawns cannot disagree about which file is the
  tracker. `SNIPE_ADDITIONS` is its sibling for `batch/tracker-additions/`, read
  by both `merge-tracker.mjs` and `local-pdf-offer.mjs` — a fixture TSV left in
  the real dir would otherwise be merged into the tracker by the next run.
- **An injected `ctx`** — providers never call `fetch` themselves; they take
  `{ fetchJson, fetchText }`. A stub returning canned payloads reaches every
  parse and normalisation path with no server at all (`test/providers-http.mjs`).
  Only `euremotejobs` and `apify` bypass it, and both are driven by swapping
  `globalThis.fetch` and restoring it in a `finally`.

Prefer a temp root over `preserve()` where a seam exists: `preserve()` only
restores if the `finally` actually runs, and a `SIGKILL`ed or crashed suite
leaves whatever it wrote. What still writes into the working tree (the embedding
indexes, Phase 1/2 JD and score artifacts) snapshots what it touches with
`preserve()` and restores in a `finally`;
`ensureUserLayer()` stands up a minimal `cv.md` / `config/profile.*` when the
gitignored real ones are absent, and removes only what it created.

Anything the TUI shells out to (`xdg-open`, `notify-send`, and the runner's
`bash` for retry) is stubbed onto `PATH` for the driven process — otherwise a
test run throws the developer's editor at them and a "retry" assertion starts a
real three-phase pipeline run.

