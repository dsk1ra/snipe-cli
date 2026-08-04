// @ts-check
/**
 * timing.mjs — one append-only TSV of every Ollama call the pipeline makes.
 *
 * There was no per-phase wall clock anywhere, so the "~5 min per JD" budget was
 * unfalsifiable and every latency claim was a guess. Ollama already returns
 * `load_duration` / `prompt_eval_*` / `eval_*` on every response, so recording
 * it needs no stopwatch of our own — the numbers are more accurate than one,
 * because they exclude our JSON parsing and the model's queue wait.
 *
 * `load_duration` is the load-event detector L2 needs: a call that found its
 * model resident reports single-digit ms, one that evicted another model
 * reports seconds. Counting the big ones counts the thrash.
 *
 * Off unless SNIPE_TIMING is set to a path, so production runs pay nothing and
 * benchmarks opt in. Appends are single `writeFileSync(..., {flag:'a'})` calls,
 * which are atomic enough for the pipeline's parallelism (one line each, well
 * under PIPE_BUF).
 */

import { appendFileSync } from 'fs';

/**
 * Every phase script is invoked with `--id N`, so the offer id is already on the
 * command line. Reading it here rather than threading a parameter through four
 * call sites keeps the instrumentation to one import per file.
 */
const ARGV_ID = (() => {
  const i = process.argv.indexOf('--id');
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : '-';
})();

const NS_PER_S = 1e9;
const round = (n, d = 2) => Math.round(n * 10 ** d) / 10 ** d;

/** Wall-clock seconds from a nanosecond duration field, 0 when absent. */
const secs = (ns) => round((Number(ns) || 0) / NS_PER_S, 3);

/**
 * Append one row for a completed Ollama call.
 *
 * @param {string} phase   e.g. 'p1-score', 'p2-judgment', 'p3-tailor', 'p3-judge'
 * @param {string} model
 * @param {any}    data    the parsed Ollama response (any endpoint)
 * @param {{id?: string|number, extra?: string}} [meta]
 */
export function logCall(phase, model, data, meta = {}) {
  const path = process.env.SNIPE_TIMING;
  if (!path) return;
  const d = data || {};
  const row = [
    new Date().toISOString(),
    meta.id ?? ARGV_ID,
    phase,
    model,
    secs(d.total_duration),
    secs(d.load_duration),
    Number(d.prompt_eval_count) || 0,
    secs(d.prompt_eval_duration),
    Number(d.eval_count) || 0,
    secs(d.eval_duration),
    d.done_reason || '-',
    meta.extra || '-',
  ].join('\t');
  try { appendFileSync(path, row + '\n', 'utf8'); } catch { /* telemetry never breaks a run */ }
}

/**
 * Wall-clock row for something that is not an Ollama call — a whole phase, a
 * PDF render. Ollama's own durations miss JD fetching, chromium and the shell,
 * and the 5-minute budget is measured on the wall, not on the GPU.
 * @param {string} phase
 * @param {number} seconds
 * @param {{id?: string|number, extra?: string}} [meta]
 */
export function logWall(phase, seconds, meta = {}) {
  logCall(phase, 'wall', { total_duration: seconds * NS_PER_S, done_reason: 'wall' }, meta);
}

export const TIMING_HEADER =
  'at\tid\tphase\tmodel\ttotal_s\tload_s\tprompt_tok\tprompt_s\tout_tok\tout_s\tdone\textra\n';

/**
 * Summarise a timings TSV: per-phase totals, plus the load events L2 targets.
 * A load over `loadThresholdS` means the model was not resident — that is a
 * reload the phase order caused, not a fixed cost.
 * @param {string} tsv raw file contents
 * @param {number} [loadThresholdS]
 */
export function summarise(tsv, loadThresholdS = 1) {
  const rows = tsv.trim().split('\n')
    .filter(l => l && !l.startsWith('at\t'))
    .map(l => {
      const c = l.split('\t');
      return { id: c[1], phase: c[2], model: c[3], total: +c[4], load: +c[5],
               promptTok: +c[6], promptS: +c[7], outTok: +c[8], outS: +c[9] };
    });
  const byPhase = new Map();
  for (const r of rows) {
    const p = byPhase.get(r.phase) || { phase: r.phase, model: r.model, calls: 0, total: 0, load: 0,
                                        loads: 0, promptTok: 0, outTok: 0, outS: 0 };
    // The model has to survive aggregation or the `model === 'wall'` half of the
    // wall test below reads undefined and never fires — only the `-wall` naming
    // convention would be enforcing it. `time_phase pdf-render` in the runner
    // would then have its wall time added to model time, double-counting every
    // call inside it.
    p.calls++;
    p.total += r.total;
    p.load += r.load;
    if (r.load > loadThresholdS) p.loads++;
    p.promptTok += r.promptTok;
    p.outTok += r.outTok;
    p.outS += r.outS;
    byPhase.set(r.phase, p);
  }
  const offers = new Set(rows.map(r => r.id).filter(i => i !== '-')).size;
  const phases = [...byPhase.values()].map(p => ({
    ...p,
    total: round(p.total), load: round(p.load),
    tok_per_s: p.outS ? round(p.outTok / p.outS, 1) : 0,
  })).sort((a, b) => b.total - a.total);

  // Wall rows (logWall) ENCLOSE the model calls made inside that phase, so
  // summing both double-counts every call. They are reported separately: the
  // wall rows answer "how long did a JD take", the model rows answer "where did
  // that time go". Adding them answers nothing.
  const isWall = p => p.model === 'wall' || p.phase.endsWith('-wall');
  const modelPhases = phases.filter(p => !isWall(p));
  const wallPhases = phases.filter(isWall);
  const modelTotal = round(modelPhases.reduce((a, p) => a + p.total, 0));
  // Mean of each phase's mean, not total/runs: a JD that stopped at the Phase 1
  // gate never entered Phase 2, so dividing by the Phase 1 count understates it.
  const perJd = round(wallPhases.reduce((a, p) => a + p.total / p.calls, 0));

  return {
    offers,
    calls: rows.length,
    model_s: modelTotal,
    load_s: round(modelPhases.reduce((a, p) => a + p.load, 0)),
    reloads: modelPhases.reduce((a, p) => a + p.loads, 0),
    per_jd_s: wallPhases.length ? perJd : (offers ? round(modelTotal / offers) : modelTotal),
    per_jd_from: wallPhases.length ? 'phase wall clock' : 'model time / offers',
    phases,
  };
}

// ── CLI ───────────────────────────────────────────────────────────────────────
// `node batch/timing.mjs report [file]` — the per-phase breakdown F2 gates on.

if (process.argv[1] && process.argv[1].endsWith('timing.mjs') && process.argv[2] === 'report') {
  const { readFileSync } = await import('fs');
  const file = process.argv[3] || process.env.SNIPE_TIMING;
  if (!file) { console.error('usage: timing.mjs report <timings.tsv>'); process.exit(1); }
  const s = summarise(readFileSync(file, 'utf8'));
  const pad = (v, w) => String(v).padEnd(w);
  const isWall = p => p.model === 'wall' || p.phase.endsWith('-wall');
  const wall = s.phases.filter(isWall);

  console.log(`offers ${s.offers}  calls ${s.calls}`);
  console.log(`per JD ${s.per_jd_s}s = ${(s.per_jd_s / 60).toFixed(2)} min  (${s.per_jd_from})`);
  console.log(`model time ${s.model_s}s, of which ${s.load_s}s (${Math.round(100 * s.load_s / (s.model_s || 1))}%) is loading models — ${s.reloads} reloads\n`);

  if (wall.length) {
    console.log(`${pad('phase', 14)}${pad('runs', 7)}${pad('total_s', 10)}mean_s`);
    console.log('-'.repeat(40));
    for (const p of wall) console.log(`${pad(p.phase, 14)}${pad(p.calls, 7)}${pad(p.total, 10)}${(p.total / p.calls).toFixed(1)}`);
    console.log('');
  }

  console.log(`${pad('phase', 14)}${pad('calls', 7)}${pad('total_s', 10)}${pad('load_s', 9)}${pad('reloads', 9)}${pad('out_tok', 9)}tok/s`);
  console.log('-'.repeat(66));
  for (const p of s.phases.filter(x => !isWall(x))) {
    console.log(`${pad(p.phase, 14)}${pad(p.calls, 7)}${pad(p.total, 10)}${pad(p.load, 9)}${pad(p.loads, 9)}${pad(p.outTok, 9)}${p.tok_per_s}`);
  }
}
