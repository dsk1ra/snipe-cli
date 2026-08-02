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
    const p = byPhase.get(r.phase) || { phase: r.phase, calls: 0, total: 0, load: 0,
                                        loads: 0, promptTok: 0, outTok: 0, outS: 0 };
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
  const grand = round(rows.reduce((a, r) => a + r.total, 0));
  return {
    offers,
    calls: rows.length,
    total_s: grand,
    per_offer_s: offers ? round(grand / offers) : grand,
    reload_s: round(phases.reduce((a, p) => a + p.load, 0)),
    reloads: phases.reduce((a, p) => a + p.loads, 0),
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
  console.log(`offers ${s.offers}  calls ${s.calls}  total ${s.total_s}s  per-offer ${s.per_offer_s}s`);
  console.log(`reloads ${s.reloads} costing ${s.reload_s}s\n`);
  const pad = (v, w) => String(v).padEnd(w);
  console.log(`${pad('phase', 14)}${pad('calls', 7)}${pad('total_s', 10)}${pad('load_s', 9)}${pad('reloads', 9)}${pad('out_tok', 9)}tok/s`);
  console.log('-'.repeat(66));
  for (const p of s.phases) {
    console.log(`${pad(p.phase, 14)}${pad(p.calls, 7)}${pad(p.total, 10)}${pad(p.load, 9)}${pad(p.loads, 9)}${pad(p.outTok, 9)}${p.tok_per_s}`);
  }
}
