#!/usr/bin/env node
// Offline sweep for Phase 3 *selection*.
//
// The retention ledger localised the whole remaining gap to selection: the
// pipeline ships 89% of the achievable grade mass but only 47% of the
// differentiators, and never because the page ran out of room. Testing a fix
// end-to-end costs a 66 s judge call per offer, which makes a weight sweep
// unaffordable — so this simulates the funnel over cached cosines and cached
// judge grades instead, and sweeps hundreds of configurations for free.
//
// The simulation is only worth anything if it reproduces the number the real
// pipeline measured, so `validate` checks exactly that before any variant is
// believed. See docs/PHASE3-RETENTION-LEDGER.md §4.
//
//   node batch/bench-tools/select-sweep.mjs prep       # cosines (local, free)
//   node batch/bench-tools/select-sweep.mjs grades     # 30B grades (slow, cached)
//   node batch/bench-tools/select-sweep.mjs validate
//   node batch/bench-tools/select-sweep.mjs sweep [--split train|test|all]
//
import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { embed, cosine } from '../embeddings.mjs';
import { extractBlockBRequirements, judgeGradesFull } from '../cv-select.mjs';
import { loadExemplars } from '../goldset.mjs';
import { loadLabels } from '../opus-metrics.mjs';
import { bootstrapCI, signTest } from '../stats.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..', '..');
const CACHE = resolve(PROJECT, 'batch/bench/opus/sweep-cache.json');

// The funnel the real pipeline applies, as measured off a bench run: selection
// keeps the top `EXP_KEEP` bullets of every experience entry and the top
// `PROJ_KEEP` project entries, then the verbatim writer renders `PROJ_BULLETS`
// bullets of each. `validate` is what says these are right.
const EXP_KEEP = 4, PROJ_KEEP = 4, PROJ_BULLETS = 2;

const load = () => (existsSync(CACHE) ? JSON.parse(readFileSync(CACHE, 'utf8')) : { offers: {}, grades: {} });
const save = (c) => { mkdirSync(dirname(CACHE), { recursive: true }); writeFileSync(CACHE, JSON.stringify(c), 'utf8'); };

/** Offers are split by id parity — stable, and independent of anything measured. */
const splitOf = (id) => (Number(id) % 2 === 0 ? 'train' : 'test');

function offersFor(labels, split) {
  return [...labels.values()]
    .filter(l => split === 'all' || splitOf(l.offer.id) === split)
    .sort((a, b) => Number(a.offer.id) - Number(b.offer.id));
}

/**
 * Offers to score, and whether the judge term can be used on them.
 *
 * An ungraded offer silently ranks as grade-0 everywhere, which is a different
 * ranker rather than a missing term — so it is dropped, not zeroed. Turning the
 * judge off for the whole split instead (the first version of this) meant one
 * report with no parseable Block B quietly demoted every run to cosine-only.
 */
function scorable(cache, labels, split) {
  const all = offersFor(labels, split).filter(l => cache.offers[String(l.offer.id)]);
  const graded = all.filter(l => cache.grades[String(l.offer.id)]);
  return graded.length >= all.length / 2
    ? { offers: graded, gradeW: 0.10, dropped: all.length - graded.length }
    : { offers: all, gradeW: 0, dropped: 0 };
}

// ── prep: every cosine the sweep needs, computed once ────────────────────────

async function prep() {
  const labels = loadLabels();
  const cache = load();
  const all = offersFor(labels, 'all');

  // Atoms are positional against cv.md and identical across label files; a
  // mismatch means cv.md moved under the corpus and every label is void.
  const atoms = all[0].atoms;
  for (const l of all) {
    if (l.atoms.length !== atoms.length || l.atoms.some((a, i) => a.text !== atoms[i].text))
      throw new Error(`atoms differ in offer ${l.offer.id} — cv.md changed, relabel before sweeping`);
  }

  const atomVecs = await embed(atoms.map(a => a.text));
  // Atom-to-atom similarity, for the redundancy penalty.
  cache.atomSim = atomVecs.map(u => atomVecs.map(v => cosine(u, v)));
  cache.atoms = atoms.map(a => ({ id: a.id, section: a.section, entity: a.entity }));

  let done = 0;
  for (const l of all) {
    const id = String(l.offer.id);
    if (cache.offers[id]) { done++; continue; }
    const reportPath = resolve(PROJECT, l.offer.report || '');
    if (!existsSync(reportPath)) continue;
    const reqs = extractBlockBRequirements(readFileSync(reportPath, 'utf8'));
    if (!reqs.length) continue;
    const qv = await embed(reqs);
    // Max over requirements is what selectCvForJd scores with.
    cache.offers[id] = { maxcos: atomVecs.map(v => Math.max(...qv.map(q => cosine(q, v)))), nreq: reqs.length };
    if (++done % 10 === 0) { save(cache); process.stderr.write(`  prep ${done}/${all.length}\n`); }
  }
  save(cache);
  console.log(`prepped ${Object.keys(cache.offers).length} offers, ${atoms.length} atoms`);
}

// ── grades: the 30B judge, cached so the sweep costs nothing ─────────────────

async function grades({ withDistinct = false } = {}) {
  const labels = loadLabels();
  const cache = load();
  // Two separate caches on purpose. The one-field schema is what production
  // runs, so it is the only fair input to "is the judge still worth 110 s";
  // the two-field schema can move `grade` itself, so mixing them would compare
  // a ranker against a differently-graded version of itself.
  const key = withDistinct ? 'grades2' : 'grades';
  cache[key] = cache[key] || {};
  const all = offersFor(labels, 'all');
  // 0-shot the judge is worse than no judge, so it refuses without exemplars —
  // which silently cached nothing on the first attempt.
  const judgeShots = loadExemplars(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  if (!judgeShots.length) throw new Error('no usable exemplars — regenerate with goldset.mjs export-shots');
  let n = 0;
  for (const l of all) {
    const id = String(l.offer.id);
    if (cache[key][id]) continue;
    const reportPath = resolve(PROJECT, l.offer.report || '');
    const jdPath = resolve(PROJECT, 'batch/jds', `${id}.txt`);
    if (!existsSync(reportPath) || !existsSync(jdPath)) continue;
    const reqs = extractBlockBRequirements(readFileSync(reportPath, 'utf8'));
    if (!reqs.length) continue;
    const items = l.atoms.map(a => ({ text: a.text }));
    const g = await judgeGradesFull(items, reqs, readFileSync(jdPath, 'utf8'), { judgeShots, withDistinct });
    if (!g) continue;             // no exemplars or model failure: leave uncached
    // Re-read: a concurrent pass writes the same file, and an in-memory copy
    // taken minutes ago would silently drop everything it wrote since.
    const fresh = load();
    fresh[key] = fresh[key] || {};
    fresh[key][id] = Object.fromEntries(l.atoms.map(a => {
      const v = g.get(a.text);
      return [a.id, withDistinct ? { g: v?.grade ?? 0, d: v?.distinct ?? 0 } : (v?.grade ?? 0)];
    }));
    fresh.atomSim = fresh.atomSim || cache.atomSim;
    save(fresh);
    process.stderr.write(`  graded ${id} (${++n})\n`);
  }
  console.log(`${key} cached for ${Object.keys(load()[key] || {}).length} offers`);
}

// ── the simulated funnel ─────────────────────────────────────────────────────

/**
 * Greedy selection under the funnel's structural quotas. lambda=0 and spikeW=0
 * reduce it to the shipped ranker; the redundancy penalty and the corpus-relative
 * specificity term are the two things being tested.
 * @returns {number[]} shipped atom ids
 */
export function simulate(cache, id, { gradeW = 0.10, spikeW = 0, lambda = 0, reserve = 0, distinctW = 0 } = {}) {
  const off = cache.offers[id];
  if (!off) return [];
  const g = cache.grades[id] || {};
  const g2 = cache.grades2?.[id] || {};
  const A = cache.atoms;

  const score = A.map((a, i) =>
    off.maxcos[i]
    // Under the two-field schema `grade` lives in .g; the one-field cache is a
    // bare number. distinctW>0 means the caller wants the two-field pass, so its
    // relevance grade must come from there too or the two terms disagree.
    + gradeW * (distinctW ? (g2[a.id]?.g ?? 0) : (g[a.id] ?? 0))
    + distinctW * (g2[a.id]?.d ?? 0)
    + spikeW * (cache.spike?.[id]?.[i] ?? 0));

  // Which project entries are eligible at all: the funnel keeps the top
  // PROJ_KEEP of them, ranked by their best bullet, before any bullet is picked.
  const projEntries = [...new Set(A.filter(a => a.section === 'Projects').map(a => a.entity))];
  const bestOf = e => Math.max(...A.map((a, i) => (a.entity === e ? score[i] : -Infinity)));
  const keptProjects = new Set(projEntries.sort((x, y) => bestOf(y) - bestOf(x)).slice(0, PROJ_KEEP));

  const quota = new Map();
  const cap = a => (a.section === 'Projects' ? PROJ_BULLETS : EXP_KEEP);
  const eligible = A.map((a, i) => ({ a, i }))
    .filter(({ a }) => a.section !== 'Projects' || keptProjects.has(a.entity));

  const picked = [];
  const pickedIdx = [];
  // `reserve` slots go to the highest specificity rather than the highest score,
  // which is the blunt version of the same idea — kept here so the sweep can say
  // whether the graded blend is doing the work or the reservation is.
  const order = (pool) => {
    if (reserve && picked.length < reserve && cache.spike?.[id]) {
      return pool.slice().sort((p, q) => cache.spike[id][q.i] - cache.spike[id][p.i]);
    }
    return pool.slice().sort((p, q) => {
      const pen = x => lambda * Math.max(0, ...pickedIdx.map(j => cache.atomSim[x][j]));
      return (score[q.i] - pen(q.i)) - (score[p.i] - pen(p.i));
    });
  };

  while (true) {
    const pool = eligible.filter(({ a, i }) =>
      !pickedIdx.includes(i) && (quota.get(a.entity) ?? 0) < cap(a));
    if (!pool.length) break;
    const next = order(pool)[0];
    picked.push(next.a.id);
    pickedIdx.push(next.i);
    quota.set(next.a.entity, (quota.get(next.a.entity) ?? 0) + 1);
  }
  return picked;
}

/** Fraction of this offer's flagged differentiators that reached the page. */
function coverage(label, shipped) {
  const d = label.differentiators || [];
  if (!d.length) return null;
  const s = new Set(shipped);
  return d.filter(x => s.has(x)).length / d.length;
}

function yieldOf(label, shipped) {
  const total = label.grades.reduce((a, x) => a + x.grade, 0);
  if (!total) return null;
  const s = new Set(shipped);
  return label.grades.filter(x => s.has(x.id)).reduce((a, x) => a + x.grade, 0) / total;
}

/**
 * Corpus-relative specificity: how much *this* offer likes an atom, over how
 * much every offer does. An atom that scores the same against every posting is
 * filler however high that score is; one that spikes here is what differentiates.
 *
 * Two backgrounds, because they cost very different things at runtime. The
 * default subtracts the mean over the labelled offers' requirement sets — exact,
 * but a fresh offer has no such corpus. SPIKE_BG=jd subtracts the mean cosine
 * against `batch/jd-index.json`, which the pipeline already maintains and
 * auto-invalidates, so it needs no new file and no new invalidation rule. If the
 * cheap one measures the same, the cheap one ships.
 */
function addSpike(cache) {
  const ids = Object.keys(cache.offers);
  const n = cache.atoms.length;
  let mean;
  if (process.env.SPIKE_BG === 'jd') {
    mean = JSON.parse(readFileSync(resolve(PROJECT, 'batch/bench/opus/spike-jd.json'), 'utf8')).mean;
    if (mean.length !== n) throw new Error('spike-jd.json is stale — recompute it against the current cv.md');
  } else {
    mean = Array.from({ length: n }, (_, i) =>
      ids.reduce((a, id) => a + cache.offers[id].maxcos[i], 0) / ids.length);
  }
  cache.spike = {};
  for (const id of ids) cache.spike[id] = cache.offers[id].maxcos.map((c, i) => c - mean[i]);
  return cache;
}

function evalCfg(cache, labels, offers, cfg) {
  const rows = [];
  for (const l of offers) {
    const id = String(l.offer.id);
    if (!cache.offers[id]) continue;
    const shipped = simulate(cache, id, cfg);
    const cov = coverage(l, shipped), yld = yieldOf(l, shipped);
    if (cov === null) continue;
    rows.push({ id, cov, yld: yld ?? 0 });
  }
  const m = k => rows.reduce((a, r) => a + r[k], 0) / (rows.length || 1);
  return { n: rows.length, cov: m('cov'), yld: m('yld'), rows };
}

// ── commands ─────────────────────────────────────────────────────────────────

function validate() {
  const labels = loadLabels();
  const cache = addSpike(load());
  // The 32 offers the real generation runs used.
  const sample = readFileSync(resolve(PROJECT, 'batch/bench/tailor/sample32.tsv'), 'utf8')
    .trim().split('\n').map(l => l.split('\t')[0]);
  const offers = [...labels.values()].filter(l => sample.includes(String(l.offer.id)));
  const base = evalCfg(cache, labels, offers, {});
  console.log(`simulator on the 32-offer sample: n=${base.n}`);
  console.log(`  differentiator_coverage  ${base.cov.toFixed(3)}   (vbp2 measured 0.468)`);
  console.log(`  grade_yield              ${base.yld.toFixed(3)}   (vbp2 measured 0.689)`);
  const off = Math.abs(base.cov - 0.468);
  console.log(off < 0.05
    ? `\nOK — within ${off.toFixed(3)} of the real run. Deltas from this simulator are worth reading.`
    : `\nOFF by ${off.toFixed(3)}. Fix the funnel constants before believing any sweep.`);
}

function sweep(split = 'train') {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW, dropped } = scorable(cache, labels, split);
  const allGraded = gradeW > 0;
  const base = evalCfg(cache, labels, offers, { gradeW });
  console.log(`split=${split} n=${base.n} offers · judge term ${allGraded ? 'ON (0.10)' : 'OFF — cosine-only'}` +
    `${dropped ? ` · ${dropped} ungraded offer(s) dropped` : ''}`);
  console.log(`baseline: cov=${base.cov.toFixed(3)} yield=${base.yld.toFixed(3)}\n`);

  const results = [];
  // gradeW is swept, not fixed: the judge costs 66-110 s per offer and is by far
  // the most expensive thing left in Phase 3. If spike subsumes it, the right
  // move is to delete it — the same question that already paid off twice.
  const gradeWs = allGraded ? [0, 0.05, 0.10, 0.20] : [0];
  for (const gw of gradeWs)
    for (const spikeW of [0, 0.5, 1, 2, 3, 4, 6, 8])
      for (const lambda of [0, 0.1, 0.2, 0.3, 0.5, 0.8])
        for (const reserve of [0, 2, 4]) {
          const r = evalCfg(cache, labels, offers, { gradeW: gw, spikeW, lambda, reserve });
          results.push({ gradeW: gw, spikeW, lambda, reserve, ...r });
        }
  results.sort((a, b) => b.cov - a.cov);
  console.log('gradeW  spikeW  lambda  reserve   cov     d(cov)   yield   d(yield)');
  for (const r of results.slice(0, 12))
    console.log(`${String(r.gradeW).padEnd(7)} ${String(r.spikeW).padEnd(7)} ${String(r.lambda).padEnd(7)} ${String(r.reserve).padEnd(9)} ` +
      `${r.cov.toFixed(3)}  ${(r.cov - base.cov >= 0 ? '+' : '')}${(r.cov - base.cov).toFixed(3)}   ` +
      `${r.yld.toFixed(3)}  ${(r.yld - base.yld >= 0 ? '+' : '')}${(r.yld - base.yld).toFixed(3)}`);

  // A mean over 61 offers gets the same paired treatment as every other claim
  // here — a config that wins big on three offers and ties elsewhere is not a win.
  const top = results[0];
  const byId = new Map(base.rows.map(r => [r.id, r.cov]));
  const deltas = top.rows.map(r => r.cov - (byId.get(r.id) ?? 0));
  const ci = bootstrapCI(deltas);
  const st = signTest(deltas);
  console.log(`\nbest: spikeW=${top.spikeW} lambda=${top.lambda} reserve=${top.reserve}`);
  console.log(`paired vs baseline: mean ${ci.mean >= 0 ? '+' : ''}${ci.mean.toFixed(3)} ` +
    `CI95 [${ci.lo.toFixed(3)}, ${ci.hi.toFixed(3)}] · ${st.pos}-${st.neg} · p=${st.p.toFixed(4)}` +
    `${ci.lo > 0 || ci.hi < 0 ? ' *' : ''}`);
}

/** One config, one split — the held-out check. Never tune with this. */
function check(split, cfg) {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW: defaultGradeW, dropped } = scorable(cache, labels, split);
  // The baseline is always the shipped ranker; only the variant may move gradeW,
  // so "delete the judge" is measured against what production actually does.
  const gradeW = cfg.gradeW ?? defaultGradeW;
  const base = evalCfg(cache, labels, offers, { gradeW: defaultGradeW });
  const got = evalCfg(cache, labels, offers, { ...cfg, gradeW });
  const byId = new Map(base.rows.map(r => [r.id, r]));
  const dCov = got.rows.map(r => r.cov - byId.get(r.id).cov);
  const dYld = got.rows.map(r => r.yld - byId.get(r.id).yld);
  console.log(`split=${split} n=${got.n} · judge term ${gradeW ? 'ON (0.10)' : 'OFF (cosine-only)'}` +
    `${dropped ? ` · ${dropped} ungraded offer(s) dropped` : ''}\n` +
    `variant: gradeW=${gradeW} spikeW=${cfg.spikeW ?? 0} lambda=${cfg.lambda ?? 0} ` +
    `reserve=${cfg.reserve ?? 0} distinctW=${cfg.distinctW ?? 0}\n`);
  for (const [name, before, after, d] of [
    ['differentiator_coverage', base.cov, got.cov, dCov],
    ['grade_yield', base.yld, got.yld, dYld]]) {
    const ci = bootstrapCI(d), st = signTest(d);
    console.log(`${name.padEnd(24)} ${before.toFixed(3)} -> ${after.toFixed(3)}  ` +
      `${ci.mean >= 0 ? '+' : ''}${ci.mean.toFixed(3)}  CI95 [${ci.lo.toFixed(3)}, ${ci.hi.toFixed(3)}]  ` +
      `${st.pos}-${st.neg}  p=${st.p.toFixed(4)}${ci.lo > 0 || ci.hi < 0 ? ' *' : ''}`);
  }
}

/** Each term alone, so a plateau cannot hide which one is doing the work. */
/**
 * Why the remaining differentiators are lost, and which CV atoms lose them.
 *
 * The headline gap is a single number and a single number cannot be acted on.
 * This splits every miss into the four things that can cause one, because the
 * fix is different for each: a bullet beaten by its own siblings is a wording
 * or ranking problem, a third differentiator inside a two-bullet project is a
 * *budget allocation* problem that no wording can touch.
 */
function attribute(split = 'all') {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW } = scorable(cache, labels, split);
  const cfg = { gradeW, spikeW: 6 };
  const byId = new Map(cache.atoms.map(a => [a.id, a]));
  const n = cache.atoms.length;
  const atomText = [...labels.values()][0].atoms;

  const cause = { ranker: 0, projectDropped: 0, capped: 0, experience: 0 };
  const per = Array.from({ length: n }, () => ({ diff: 0, miss: 0, shipped: 0, seen: 0 }));

  for (const l of offers) {
    const id = String(l.offer.id);
    const ship = new Set(simulate(cache, id, cfg));
    const shippedEntities = new Set([...ship].map(a => byId.get(a).entity));
    const diffsByEntity = {};
    for (let i = 0; i < n; i++) {
      per[i].seen++;
      if (ship.has(cache.atoms[i].id)) per[i].shipped++;
    }
    for (const d of l.differentiators || []) {
      const idx = cache.atoms.findIndex(a => a.id === d);
      if (idx >= 0) { per[idx].diff++; if (!ship.has(d)) per[idx].miss++; }
      const a = byId.get(d);
      if (!a) continue;
      if (a.section === 'Projects') (diffsByEntity[a.entity] = diffsByEntity[a.entity] || []).push(d);
      else if (!ship.has(d)) cause.experience++;
    }
    for (const [entity, ds] of Object.entries(diffsByEntity)) {
      const missed = ds.filter(d => !ship.has(d)).length;
      if (!shippedEntities.has(entity)) { cause.projectDropped += missed; continue; }
      // Only PROJ_BULLETS of a project can ever ship, so anything beyond that
      // many differentiators in one project is lost by arithmetic, not ranking.
      const impossible = Math.max(0, ds.length - PROJ_BULLETS);
      cause.capped += Math.min(missed, impossible);
      cause.ranker += Math.max(0, missed - impossible);
    }
  }

  const total = Object.values(cause).reduce((a, b) => a + b, 0);
  const pct = v => `${((100 * v) / (total || 1)).toFixed(0)}%`.padStart(4);
  console.log(`split=${split} · n=${offers.length} offers · ${total} missed differentiators\n`);
  console.log('cause                                        count  share  fixable by');
  console.log(`  beaten by its own project siblings         ${String(cause.ranker).padStart(5)}  ${pct(cause.ranker)}  wording / ranker`);
  console.log(`  project never made the cut                 ${String(cause.projectDropped).padStart(5)}  ${pct(cause.projectDropped)}  project scoring`);
  console.log(`  >${PROJ_BULLETS} differentiators in one project        ${String(cause.capped).padStart(5)}  ${pct(cause.capped)}  ALLOCATION ONLY`);
  console.log(`  experience bullet lost                     ${String(cause.experience).padStart(5)}  ${pct(cause.experience)}  wording / ranker`);

  const mean = cache.atoms.map((_, i) =>
    Object.keys(cache.offers).reduce((a, id) => a + cache.offers[id].maxcos[i], 0) / Object.keys(cache.offers).length);
  const rows = per.map((s, i) => ({ id: cache.atoms[i].id, ...s, generic: mean[i],
    where: `${cache.atoms[i].section}/${cache.atoms[i].entity}`.slice(0, 34),
    text: atomText[i].text.slice(0, 60) }))
    .filter(r => r.diff).sort((a, b) => b.miss - a.miss);
  console.log('\nWORST ATOMS — flagged a differentiator, did not reach the page');
  console.log('  A high `generic` is a wording problem: spike discounts a bullet every');
  console.log('  posting likes moderately. A low one that still misses is crowding.\n');
  console.log('id   miss/diff  ship%  generic  where');
  for (const r of rows.slice(0, 10))
    console.log(`${String(r.id).padEnd(4)} ${(r.miss + '/' + r.diff).padEnd(10)} ` +
      `${((100 * r.shipped) / r.seen).toFixed(0).padStart(4)}%  ${r.generic.toFixed(3).padStart(7)}  ${r.where}`);
}

function ablate(split = 'train') {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW, dropped } = scorable(cache, labels, split);
  const base = evalCfg(cache, labels, offers, { gradeW });
  console.log(`split=${split} n=${base.n}  judge ${gradeW ? 'ON' : 'OFF'}` +
    `${dropped ? ` (${dropped} ungraded dropped)` : ''}  baseline cov=${base.cov.toFixed(3)}\n`);
  console.log('config                        cov     delta   yield');
  const show = (name, cfg) => {
    const r = evalCfg(cache, labels, offers, { gradeW, ...cfg });
    console.log(`${name.padEnd(29)} ${r.cov.toFixed(3)}  ${r.cov - base.cov >= 0 ? '+' : ''}${(r.cov - base.cov).toFixed(3)}  ${r.yld.toFixed(3)}`);
  };
  for (const w of [1, 2, 3, 4, 6, 8, 12]) show(`spike ${w} only`, { spikeW: w });
  for (const l of [0.1, 0.2, 0.3, 0.5, 0.8]) show(`mmr ${l} only`, { lambda: l });
  for (const r of [2, 4, 6]) show(`reserve ${r} only`, { reserve: r });
  show('spike 4 + mmr 0.3', { spikeW: 4, lambda: 0.3 });
  // The judge's own answer to the question spike answers arithmetically.
  if (Object.keys(cache.grades2 || {}).length) {
    for (const d of [0.05, 0.10, 0.20, 0.40]) show(`distinct ${d} only`, { distinctW: d });
    for (const d of [0.10, 0.20, 0.40]) show(`spike 6 + distinct ${d}`, { spikeW: 6, distinctW: d });
    show('distinct 0.20, no cosine judge', { gradeW: 0, distinctW: 0.20 });
  }
}

const cmd = process.argv[2];
const arg = (f, d) => { const i = process.argv.indexOf(f); return i > 0 ? process.argv[i + 1] : d; };
if (cmd === 'prep') await prep();
else if (cmd === 'grades') await grades({ withDistinct: process.argv.includes('--distinct') });
else if (cmd === 'validate') validate();
else if (cmd === 'sweep') sweep(arg('--split', 'train'));
else if (cmd === 'ablate') ablate(arg('--split', 'train'));
else if (cmd === 'attribute') attribute(arg('--split', 'all'));
else if (cmd === 'check') check(arg('--split', 'test'), {
  spikeW: parseFloat(arg('--spike', '0')),
  lambda: parseFloat(arg('--lambda', '0')),
  reserve: parseInt(arg('--reserve', '0'), 10),
  distinctW: parseFloat(arg('--distinct', '0')),
  ...(process.argv.includes('--grade') ? { gradeW: parseFloat(arg('--grade', '0.10')) } : {}) });
else console.log('usage: select-sweep.mjs prep|grades|validate|sweep|ablate|attribute|check [--split train|test|all]\n' +
  '       grades [--distinct]   check [--spike W] [--lambda L] [--reserve N] [--grade W] [--distinct W]');
