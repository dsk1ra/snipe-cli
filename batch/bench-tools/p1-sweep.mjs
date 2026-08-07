#!/usr/bin/env node
// p1-sweep.mjs — A/B different base models for Phase 1 (the pre-score gate).
//
// Phase 1 has no harness of its own because it produces a number, not a
// document. But it does not need one: ollama-scorer.mjs writes its payload to
// *stdout* (batch/scores/<id>.json is local-runner's shell redirect), so the
// scorer can be driven directly over cached JDs without touching any pipeline
// state. Only side effect is rewriting batch/jds/<id>.txt with the bytes it
// just read from there.
//
// Two references, because neither alone is enough:
//   human  — batch/labels/labels-rev.tsv, 18 offers, real ground truth, but
//            3 distinct values so rho carries a ~0.30 noise band (CLAUDE.md #3).
//   p2     — the 30B's stored eval score over ~180 offers. Drift-affected, so
//            it is NOT a control in the absolute sense (CLAUDE.md #1) — but it
//            is the *same* fixed reference for every candidate, so relative
//            ordering between candidates is fair. Big n, weak truth; the human
//            set is small n, strong truth. Agreement across both is the signal.
//
// What Phase 1 is actually for is the gate, so the gate confusion at
// --p1-threshold matters more than rho: an offer the 30B would score >=3.0 that
// Phase 1 drops below 2.5 is never evaluated at all, and that is the only
// Phase 1 error that costs a real opportunity.
//
//   run <label> --model M [--n 60] [--ids 1,2,3]
//   compare <label>...          first label is the baseline for deltas
//
// ponytail: sequential, one offer at a time — there is one 6 GB GPU, so
// parallelism would only make the timings meaningless.

import { readFileSync, writeFileSync, readdirSync, existsSync, mkdirSync } from 'node:fs';
import { join, dirname, resolve as _resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { spearman, pairAccuracy } from './rho.mjs';

const ROOT = _resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const OUT = _resolve(ROOT, 'batch/bench/p1');
const LABELS = _resolve(ROOT, 'batch/labels/labels-rev.tsv');

const human = new Map(
  readFileSync(LABELS, 'utf8').trim().split('\n')
    .map(l => { const [id, v] = l.split('\t'); return [id.trim(), +v]; })
);

/** Stored 30B eval score per id, for ids that also have a cached JD. */
function p2Reference() {
  const out = new Map();
  const evalDir = join(ROOT, 'batch/evals');
  if (!existsSync(evalDir)) return out;
  for (const f of readdirSync(evalDir)) {
    if (!f.endsWith('.json')) continue;
    const id = f.replace('.json', '');
    if (!existsSync(join(ROOT, 'batch/jds', `${id}.txt`))) continue;
    try {
      const j = JSON.parse(readFileSync(join(evalDir, f), 'utf8'));
      if (j.status === 'evaled' && typeof j.score === 'number') out.set(id, j);
    } catch {}
  }
  return out;
}

/** URL for an id — the prompt includes it, so a bench run must use the real one. */
function urlFor(id) {
  for (const p of [`batch/evals/${id}.json`, `batch/scores/${id}.json`]) {
    const f = join(ROOT, p);
    if (!existsSync(f)) continue;
    try { const u = JSON.parse(readFileSync(f, 'utf8')).url; if (u) return u; } catch {}
  }
  return `https://local/${id}`;
}

/**
 * The 18 human-labelled offers always, then a stratified fill from the rest by
 * P2 bucket so the big reference set spans the score range rather than piling
 * into the 3s (91 of 180 sit there).
 */
function idSet(n) {
  const p2 = p2Reference();
  const picked = [...human.keys()].filter(id => existsSync(join(ROOT, 'batch/jds', `${id}.txt`)));
  const seen = new Set(picked);
  const buckets = new Map();
  for (const [id, j] of p2) {
    if (seen.has(id)) continue;
    const b = Math.floor(j.score);
    if (!buckets.has(b)) buckets.set(b, []);
    buckets.get(b).push(id);
  }
  for (const arr of buckets.values()) arr.sort((a, b) => +a - +b);
  const keys = [...buckets.keys()].sort();
  let progress = true;
  while (picked.length < n && progress) {
    progress = false;
    for (const k of keys) {
      if (picked.length >= n) break;
      const arr = buckets.get(k);
      if (arr.length) { picked.push(arr.shift()); progress = true; }
    }
  }
  return picked.sort((a, b) => +a - +b);
}

function runSweep(label, model, ids, timeout) {
  mkdirSync(OUT, { recursive: true });
  const rows = [];
  const t0 = Date.now();
  for (let i = 0; i < ids.length; i++) {
    const id = ids[i];
    const started = Date.now();
    const r = spawnSync('node', [
      join(ROOT, 'batch/ollama-scorer.mjs'),
      '--id', id,
      '--url', urlFor(id),
      '--jd-file', join(ROOT, 'batch/jds', `${id}.txt`),
      '--model', model,
      // A thinking model on a 6 GB card blows the scorer's 120s default, and a
      // timeout measures the ceiling rather than the model. Raise it for the
      // bench so the real cost is what gets recorded.
      '--timeout', String(timeout),
    ], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
    const secs = (Date.now() - started) / 1000;
    let j = null;
    try { j = JSON.parse(r.stdout); } catch {}
    const row = j && j.status === 'scored'
      ? {
          id, secs, status: 'scored', score: j.score,
          cv_match: j.cv_match, north_star: j.north_star,
          hard_stops: (j.hard_stops || []).length,
          decision: j.recommendation ?? j.final_decision ?? null,
        }
      : { id, secs, status: (j && j.status) || 'failed', score: null,
          error: (j && j.error) || (r.stderr || '').slice(0, 200) };
    rows.push(row);
    process.stdout.write(
      `[${i + 1}/${ids.length}] #${id} ${row.status} ${row.score ?? '-'} ${secs.toFixed(1)}s\n`);
  }
  const payload = {
    label, model, timeout, at: new Date().toISOString(),
    minutes: +((Date.now() - t0) / 60000).toFixed(1),
    n: rows.length, ok: rows.filter(r => r.status === 'scored').length,
    rows,
  };
  writeFileSync(join(OUT, `${label}.json`), JSON.stringify(payload, null, 2));
  console.log(`\n${JSON.stringify({ ...payload, rows: undefined }, null, 2)}`);
}

// ── comparison ───────────────────────────────────────────────────────────────

const load = (label) => JSON.parse(readFileSync(join(OUT, `${label}.json`), 'utf8'));

/** Paired bootstrap over offers, per CLAUDE.md — a dozen variants will
 *  otherwise always find a "winner". */
function bootstrapDelta(idsA, fnA, fnB, iters = 2000) {
  const deltas = [];
  for (let it = 0; it < iters; it++) {
    const samp = [];
    for (let i = 0; i < idsA.length; i++) samp.push(idsA[(Math.random() * idsA.length) | 0]);
    deltas.push(fnB(samp) - fnA(samp));
  }
  deltas.sort((a, b) => a - b);
  return [deltas[(0.025 * iters) | 0], deltas[(0.975 * iters) | 0]];
}

function metrics(run) {
  const p2 = p2Reference();
  const byId = new Map(run.rows.filter(r => r.status === 'scored').map(r => [r.id, r]));
  const hPairs = [...byId.values()].filter(r => human.has(r.id)).map(r => [r.score, human.get(r.id)]);
  const pPairs = [...byId.values()].filter(r => p2.has(r.id)).map(r => [r.score, p2.get(r.id).score]);
  // Gate confusion at the shipped default. A "missed" offer is the expensive
  // error: gated below 2.5 while the 30B would have scored it >= 3.0.
  let missed = 0, wasted = 0, gateN = 0;
  for (const r of byId.values()) {
    if (!p2.has(r.id)) continue;
    gateN++;
    const t = p2.get(r.id).score;
    if (r.score < 2.5 && t >= 3.0) missed++;
    if (r.score >= 2.5 && t < 2.5) wasted++;
  }
  const okRows = [...byId.values()];
  return {
    label: run.label, model: run.model, n: run.n, ok: run.ok, minutes: run.minutes,
    secs: okRows.reduce((a, r) => a + r.secs, 0) / (okRows.length || 1),
    humanN: hPairs.length,
    humanRho: hPairs.length >= 3 ? spearman(hPairs) : NaN,
    humanPA: pairAccuracy(hPairs),
    p2N: pPairs.length,
    p2Rho: pPairs.length >= 3 ? spearman(pPairs) : NaN,
    p2PA: pairAccuracy(pPairs),
    missed, wasted, gateN,
    meanScore: okRows.reduce((a, r) => a + r.score, 0) / (okRows.length || 1),
    hardStops: okRows.reduce((a, r) => a + (r.hard_stops || 0), 0),
    byId,
  };
}

function compare(labels) {
  const p2 = p2Reference();
  const runs = labels.map(l => metrics(load(l)));
  const pad = (s, n) => String(s).padEnd(n);
  console.log('\nlabel            model                     ok    s/off  meanS  rho_h  PA_h    rho_p2  PA_p2   missed wasted');
  for (const m of runs) {
    console.log(
      `${pad(m.label, 16)} ${pad(m.model, 25)} ${pad(`${m.ok}/${m.n}`, 5)} ` +
      `${pad(m.secs.toFixed(1), 6)} ${pad(m.meanScore.toFixed(2), 6)} ` +
      `${pad(Number.isNaN(m.humanRho) ? '-' : m.humanRho.toFixed(3), 6)} ` +
      `${pad((100 * m.humanPA.acc).toFixed(1) + '%', 7)} ` +
      `${pad(Number.isNaN(m.p2Rho) ? '-' : m.p2Rho.toFixed(3), 7)} ` +
      `${pad((100 * m.p2PA.acc).toFixed(1) + '%', 7)} ` +
      `${pad(`${m.missed}/${m.gateN}`, 6)} ${m.wasted}/${m.gateN}`);
  }

  const base = runs[0];
  for (const m of runs.slice(1)) {
    const shared = [...m.byId.keys()].filter(id => base.byId.has(id));
    const hIds = shared.filter(id => human.has(id));
    const pIds = shared.filter(id => p2.has(id));
    const paOf = (run, ref) => (ids) =>
      pairAccuracy(ids.map(id => [run.byId.get(id).score, ref(id)])).acc;
    const hA = paOf(base, id => human.get(id)), hB = paOf(m, id => human.get(id));
    const pA = paOf(base, id => p2.get(id).score), pB = paOf(m, id => p2.get(id).score);
    const hCI = hIds.length >= 3 ? bootstrapDelta(hIds, hA, hB) : [NaN, NaN];
    const pCI = pIds.length >= 3 ? bootstrapDelta(pIds, pA, pB) : [NaN, NaN];
    // Sign test on the shared offers: how often does each land closer to the
    // reference, ties excluded.
    let win = 0, loss = 0;
    for (const id of pIds) {
      const t = p2.get(id).score;
      const dA = Math.abs(base.byId.get(id).score - t), dB = Math.abs(m.byId.get(id).score - t);
      if (dB < dA) win++; else if (dB > dA) loss++;
    }
    console.log(
      `\n${m.label} vs ${base.label}  (paired on ${shared.length} offers)\n` +
      `  PA_human  ${(100 * (hB(hIds) - hA(hIds))).toFixed(1)} pts  CI [${(100 * hCI[0]).toFixed(1)}, ${(100 * hCI[1]).toFixed(1)}]  n=${hIds.length}\n` +
      `  PA_p2     ${(100 * (pB(pIds) - pA(pIds))).toFixed(1)} pts  CI [${(100 * pCI[0]).toFixed(1)}, ${(100 * pCI[1]).toFixed(1)}]  n=${pIds.length}\n` +
      `  closer to p2: ${win} wins / ${loss} losses\n` +
      `  speed:    ${(base.secs / m.secs).toFixed(2)}x  (${base.secs.toFixed(1)}s → ${m.secs.toFixed(1)}s per offer)`);
  }
}

// ── CLI ──────────────────────────────────────────────────────────────────────

const [cmd, ...rest] = process.argv.slice(2);
const flag = (name, dflt) => {
  const i = rest.indexOf(`--${name}`);
  return i === -1 ? dflt : rest[i + 1];
};

if (cmd === 'run') {
  const label = rest[0];
  const model = flag('model', 'snipe-screen');
  const ids = flag('ids') ? flag('ids').split(',') : idSet(+flag('n', 60));
  if (!label || label.startsWith('--')) { console.error('usage: run <label> --model M [--n 60]'); process.exit(1); }
  const timeout = +flag('timeout', 600);
  console.log(`p1-sweep run ${label}  model=${model}  offers=${ids.length}  timeout=${timeout}s`);
  runSweep(label, model, ids, timeout);
} else if (cmd === 'compare') {
  compare(rest.filter(a => !a.startsWith('--')));
} else if (cmd === 'ids') {
  console.log(idSet(+flag('n', 60)).join(','));
} else {
  console.error('usage: p1-sweep.mjs run <label> --model M [--n 60] | compare <label>... | ids [--n 60]');
  process.exit(1);
}
