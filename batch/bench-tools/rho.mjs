#!/usr/bin/env node
// Spearman rho of a bench run's scores against the user's 18 labels, plus the
// per-offer error table so a win/loss can be attributed rather than just noted.
// usage: rho.mjs <bench-dir>...
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { dirname, resolve as _resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
// Repo root, resolved from this file so the tools work from any cwd and survive
// being moved. Labels live in batch/labels/ which is gitignored — the tooling is
// tracked, the personal data is not.
const ROOT = _resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const LABELS = _resolve(ROOT, 'batch/labels/labels-rev.tsv');


const labels = new Map(
  readFileSync(LABELS, 'utf8')
    .trim().split('\n').map(l => { const [id, v] = l.split('\t'); return [id.trim(), +v]; })
);

export function spearman(pairs) {
  const rank = vals => {
    const idx = vals.map((v, i) => [v, i]).sort((a, b) => a[0] - b[0]);
    const r = new Array(vals.length);
    for (let i = 0; i < idx.length;) {
      let j = i; while (j + 1 < idx.length && idx[j + 1][0] === idx[i][0]) j++;
      const avgRank = (i + j) / 2 + 1;
      for (let k = i; k <= j; k++) r[idx[k][1]] = avgRank;
      i = j + 1;
    }
    return r;
  };
  const rx = rank(pairs.map(p => p[0])), ry = rank(pairs.map(p => p[1]));
  const n = pairs.length;
  const mx = rx.reduce((a, b) => a + b) / n, my = ry.reduce((a, b) => a + b) / n;
  let num = 0, dx = 0, dy = 0;
  for (let i = 0; i < n; i++) { num += (rx[i] - mx) * (ry[i] - my); dx += (rx[i] - mx) ** 2; dy += (ry[i] - my) ** 2; }
  return dx && dy ? num / Math.sqrt(dx * dy) : 0;
}

// The labels take only 3 distinct values (2/3/4), so rho is heavily tied and
// coarsely quantized — several different weightings return the identical rho.
// Pair accuracy is the honest companion metric: of all offer pairs the user
// actually ranked differently, what fraction does the pipeline order correctly?
// Ties in the prediction count as half credit. Reported with its raw pair count
// so a 12-pair sample is never mistaken for a 100-pair one.
export function pairAccuracy(pairs) {
  let n = 0, correct = 0;
  for (let i = 0; i < pairs.length; i++) {
    for (let j = i + 1; j < pairs.length; j++) {
      const [si, li] = pairs[i], [sj, lj] = pairs[j];
      if (li === lj) continue;          // user did not rank these — skip
      n++;
      if (si === sj) correct += 0.5;    // prediction tied: half credit
      else if ((si - sj) * (li - lj) > 0) correct += 1;
    }
  }
  return { acc: n ? correct / n : 0, n, correct };
}

export function loadRun(dir) {
  const out = [];
  const evalDir = join(dir, 'evals');
  if (!existsSync(evalDir)) return out;
  for (const f of readdirSync(evalDir).filter(f => f.endsWith('.json'))) {
    const e = JSON.parse(readFileSync(join(evalDir, f), 'utf8'));
    if (!labels.has(String(e.id))) continue;
    if (e.status !== 'evaled') continue;
    out.push({ id: String(e.id), label: labels.get(String(e.id)), ...e });
  }
  return out.sort((a, b) => +a.id - +b.id);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const dirs = process.argv.slice(2);
  const runs = dirs.map(d => ({ dir: d, offers: loadRun(d) }));
  for (const { dir, offers } of runs) {
    if (!offers.length) { console.log(`\n=== ${dir}: no completed evals ===`); continue; }
    const rho = spearman(offers.map(o => [o.score, o.label]));
    const pa = pairAccuracy(offers.map(o => [o.score, o.label]));
    const mae = offers.reduce((a, o) => a + Math.abs(o.score - o.label), 0) / offers.length;
    console.log(`\n=== ${dir}  n=${offers.length}  rho=${rho.toFixed(3)}  pairAcc=${(100 * pa.acc).toFixed(1)}% (${pa.correct}/${pa.n})  MAE=${mae.toFixed(2)} ===`);
    console.log('   id  lbl  score   err   cv_m  cov    ns   decision');
    for (const o of offers.slice().sort((a, b) => (b.score - b.label) - (a.score - a.label))) {
      const err = o.score - o.label;
      console.log(`  ${o.id.padStart(3)}  ${o.label}    ${String(o.score).padEnd(5)} ${(err > 0 ? '+' : '') + err.toFixed(1).padEnd(5)} ${String(o.cv_match).padEnd(4)}  ${String(o.cv_coverage).padEnd(5)}  ${String(o.north_star).padEnd(3)}  ${o.final_decision}`);
    }
  }
  if (runs.length > 1 && runs.every(r => r.offers.length)) {
    console.log('\n=== deltas vs first ===');
    const base = new Map(runs[0].offers.map(o => [o.id, o.score]));
    for (const r of runs.slice(1)) {
      const rho0 = spearman(runs[0].offers.map(o => [o.score, o.label]));
      const rho1 = spearman(r.offers.map(o => [o.score, o.label]));
      console.log(`  ${r.dir}: rho ${rho0.toFixed(3)} -> ${rho1.toFixed(3)}  (${(rho1 - rho0 >= 0 ? '+' : '') + (rho1 - rho0).toFixed(3)})`);
      const moved = r.offers.filter(o => base.has(o.id) && Math.abs(o.score - base.get(o.id)) > 0.05);
      for (const o of moved) console.log(`      #${o.id} lbl ${o.label}: ${base.get(o.id)} -> ${o.score}`);
    }
  }
}
