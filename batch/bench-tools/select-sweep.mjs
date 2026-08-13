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
import { extractBlockBRequirements, judgeGradesFull, bulletCost,
         parseCvSections, parseEntries } from '../cv-select.mjs';
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
//
// **This is the pre-line-budget funnel** and production stopped running it on
// 2026-08-08. It is kept because every ablation recorded in the ledgers was
// measured on it, and re-pointing those numbers at a different funnel would make
// them incomparable. `lineBudget` selects the shipped one — see `allocateLines`
// below — and `validate` now checks whichever funnel the caller asked for
// against the arm that actually ran it.
const EXP_KEEP = 4, PROJ_KEEP = 4, PROJ_BULLETS = 2;

// Shipped config: SNIPE_LINE_BUDGET=24 over SNIPE_MAX_PROJECTS=3, caps of 4.
const LINE_BUDGET = 24, LB_PROJ_KEEP = 3, LB_ROLE_CAP = 4, LB_PROJ_CAP = 4;

/**
 * What each atom costs in rendered lines.
 *
 * The cache stores `{id, section, entity}` and no text, because atoms are
 * positional against `cv.md` — the same contract the 128 labels rely on, and the
 * same reason editing `cv.md` invalidates them. So the text is recovered by
 * walking `cv.md` in the identical order and priced with production's own
 * `bulletCost`, rather than a second copy of the line-height arithmetic.
 *
 * The entity names are asserted to line up rather than assumed. A silent
 * off-by-one here would price every bullet as its neighbour and the simulator
 * would still run, which is the failure mode that produces confident wrong
 * numbers.
 */
let _costs = null;
export function atomCosts(atoms) {
  if (_costs) return _costs;
  const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  const flat = [];
  for (const name of ['Experience', 'Projects']) {
    for (const sec of parseCvSections(cvText)) {
      if (sec.name !== name) continue;
      for (const e of parseEntries(sec.lines).entries) {
        const entity = e.head[0].replace(/^###\s+/, '').trim();
        for (const b of e.bullets) flat.push({ entity, section: name, text: b });
      }
    }
  }
  if (flat.length !== atoms.length) {
    throw new Error(`atom/cv.md mismatch: cache has ${atoms.length} atoms, cv.md has ${flat.length} bullets — the cache is stale against cv.md`);
  }
  flat.forEach((f, i) => {
    if (f.entity !== atoms[i].entity || f.section !== atoms[i].section) {
      throw new Error(`atom ${i} is "${atoms[i].entity}" in the cache and "${f.entity}" in cv.md — positions have drifted`);
    }
  });
  _costs = flat.map(f => bulletCost(f.text));
  return _costs;
}

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
/**
 * `cv-select.mjs`'s `allocateLines`, over cached cosines.
 *
 * Ported rather than approximated, because the whole question this tool is being
 * asked — how much of the page experience gets — only exists in this funnel.
 * The pre-line-budget simulator gives experience its own `EXP_KEEP` quota, so
 * experience and projects never compete and the effect is invisible by
 * construction.
 *
 * Three properties are load-bearing and each mirrors production:
 *
 *  - **A bullet costs what it occupies.** Ranking is score *per line*, so a
 *    marginally weaker one-line bullet beats a marginally stronger three-line
 *    one. Against a count budget they price identically and one is three times
 *    the page.
 *  - **Counts, then top-k.** `allocateLines` returns how many bullets each entry
 *    gets and `trim(e, k, true)` then keeps that entry's k highest-scoring. The
 *    bullet the density loop happened to pick is not necessarily the one that
 *    ships, so picking counts and re-selecting by score is the faithful order.
 *  - **Every entry keeps its top bullet before anything else is spent**, so a
 *    budget too small to go round starves nobody to a bare heading.
 *
 * `minExp` is the knob under test: the floor on experience entries, 1 in
 * production. It is paid out of the same budget, immediately after the top-bullet
 * pass and before the density loop, so it takes lines from projects exactly as a
 * production floor would.
 */
function allocateLinesSim(A, score, keptProjects, budget, minExp, floorMode = 'all', cut = null,
  projMaxLines = 0) {
  const costs = atomCosts(A);
  const idx = A.map((a, i) => ({ a, i }))
    .filter(({ a }) => a.section !== 'Projects' || keptProjects.has(a.entity));
  const byEntity = new Map();
  for (const x of idx) {
    if (!byEntity.has(x.a.entity)) byEntity.set(x.a.entity, []);
    byEntity.get(x.a.entity).push(x);
  }
  for (const v of byEntity.values()) v.sort((x, y) => score[y.i] - score[x.i]);

  const isProj = (e) => keptProjects.has(e);
  const capOf = (e) => (isProj(e) ? LB_PROJ_CAP : LB_ROLE_CAP);
  const n = new Map();
  let spent = 0;
  // Lines projects have taken, tracked separately for `projMaxLines`. The
  // top-bullet pass counts toward it but is never blocked by it — a project that
  // reached the page must render at least one bullet or it ships as a bare
  // title, which is a worse page than an unbalanced one.
  let projSpent = 0;
  for (const [e, v] of byEntity) {
    if (!v.length) continue;
    n.set(e, 1);
    spent += costs[v[0].i];
    if (isProj(e)) projSpent += costs[v[0].i];
  }
  // The floor, before the open contest. `floorMode` decides who gets it: every
  // experience entry, or only the one this posting scores highest. The
  // distinction is the whole question — across 128 offers the entry actually
  // starved is Teaching Assistant (1.39 bullets, at the floor in 71% of offers),
  // not the commercial role (2.41, 25%). A blanket floor therefore buys mostly
  // teaching-assistant bullets, which is the least differentiating evidence on
  // the CV and the reason it costs coverage so steeply.
  const expEntities = [...byEntity.keys()].filter(e => !isProj(e));
  const topExp = expEntities.slice().sort((x, y) =>
    (score[byEntity.get(y)[0].i] ?? -Infinity) - (score[byEntity.get(x)[0].i] ?? -Infinity))[0];
  for (const [e, v] of byEntity) {
    if (isProj(e)) continue;
    if (floorMode === 'top' && e !== topExp) continue;
    while ((n.get(e) ?? 0) < Math.min(minExp, capOf(e), v.length)) {
      const k = n.get(e) ?? 0;
      spent += costs[v[k].i];
      n.set(e, k + 1);
    }
  }
  // The grade cut, applied to the surplus only. Everything above ran first on
  // purpose: an entry keeps its top bullet and the floor is paid before the open
  // contest, so a cut can never leave an entry as a bare heading. That ordering
  // is the whole design — the judge grades 72% of atoms 0, so a cut applied
  // before those passes would empty most entries outright.
  const rest = [...byEntity].flatMap(([e, v]) => v.slice(n.get(e) ?? 0)
    .filter(x => !cut || !cut(x.i))
    .map(x => ({ e, i: x.i, cost: costs[x.i], score: score[x.i] })));
  for (const b of rest.sort((x, y) => (y.score / y.cost) - (x.score / x.cost))) {
    if (spent + b.cost > budget) continue;          // a cheaper bullet may still fit
    if ((n.get(b.e) ?? 0) >= capOf(b.e)) continue;
    // The section budget. One pool lets a strongly-matching project take the
    // page down to two one-line employers, which is what §14 measured: the
    // starvation appeared the day experience and projects started competing.
    // This bounds the contest instead of flooring an entry inside it.
    if (projMaxLines && isProj(b.e) && projSpent + b.cost > projMaxLines) continue;
    n.set(b.e, (n.get(b.e) ?? 0) + 1);
    spent += b.cost;
    if (isProj(b.e)) projSpent += b.cost;
  }
  return [...byEntity].flatMap(([e, v]) => v.slice(0, n.get(e) ?? 0).map(x => x.a.id));
}

export function simulate(cache, id, { gradeW = 0.10, spikeW = 0, lambda = 0, reserve = 0, distinctW = 0,
  projCap = PROJ_BULLETS, projBudget = PROJ_KEEP * PROJ_BULLETS, gateK = 1,
  projKeep = PROJ_KEEP, lineBudget = 0, minExp = 1, floorMode = 'all',
  gradeCut = 0, projMaxLines = 0 } = {}) {
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
  // PROJ_KEEP of them before any bullet is picked.
  //
  // `gateK` is how many of a project's bullets that gate reads. 1 is the shipped
  // rule — max over the project, so a project with three matching bullets and
  // one with a single matching bullet gate identically. Allocation did not touch
  // this bucket (71 differentiators lost to it before and after), because it
  // only spends the budget on projects the gate already admitted.
  const projEntries = [...new Set(A.filter(a => a.section === 'Projects').map(a => a.entity))];
  const massOf = e => A.map((a, i) => (a.entity === e ? score[i] : null))
    .filter(s => s !== null).sort((x, y) => y - x).slice(0, gateK)
    .reduce((s, v) => s + v, 0);
  // `projKeep` is how many survive. The CV has 5 projects and ships 4, so this
  // gate chooses which ONE to drop — re-scoring that choice is near zero-sum,
  // which is why gateK measured nothing. Keeping all 5 on the same bullet budget
  // is the version of the idea with something to win.
  const keptProjects = new Set(projEntries.sort((x, y) => massOf(y) - massOf(x))
    .slice(0, lineBudget ? LB_PROJ_KEEP : projKeep));

  // Drop an atom the judge graded below `gradeCut` from the open contest. There
  // is effectively one setting: the grades are binary — 3026 zeros, 30 mid, 1135
  // threes over 127 offers x 33 atoms — so any threshold in (0, 3] is "drop the
  // zeros" and thresholds above 0 differ only in whether they also take the 30.
  const cut = gradeCut
    ? (i) => ((distinctW ? (g2[A[i].id]?.g ?? 0) : (g[A[i].id] ?? 0)) < gradeCut)
    : null;

  if (lineBudget) return allocateLinesSim(A, score, keptProjects, lineBudget, minExp, floorMode, cut,
    projMaxLines);

  const quota = new Map();
  // Project bullets are drawn from one shared budget rather than a flat cap per
  // project: a posting that is mostly about what one project did should spend
  // more of the page on that project. `projCap` bounds how lopsided that gets,
  // and one slot per kept project is reserved so none ships as a bare title.
  // projCap = PROJ_BULLETS reproduces the flat allocation exactly, which is what
  // makes this a null-safe generalisation rather than a new ranker.
  let spent = 0;
  const unfed = () => [...keptProjects].filter(e => !quota.get(e)).length;
  const admits = (a) => {
    const q = quota.get(a.entity) ?? 0;
    if (a.section !== 'Projects') return q < EXP_KEEP;
    if (q >= projCap || spent >= projBudget) return false;
    return q === 0 || projBudget - spent > unfed();
  };
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
    const pool = eligible.filter(({ a, i }) => !pickedIdx.includes(i) && admits(a));
    if (!pool.length) break;
    const next = order(pool)[0];
    picked.push(next.a.id);
    pickedIdx.push(next.i);
    quota.set(next.a.entity, (quota.get(next.a.entity) ?? 0) + 1);
    if (next.a.section === 'Projects') spent++;
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
export function addSpike(cache) {
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

/**
 * Experience entries this selection leaves at one bullet, and whether that is
 * all of them.
 *
 * The sweep optimised coverage alone, and generation ledger §14 is the case that
 * coverage cannot see a starved Experience section: a page of project bullets
 * over two one-line employers scores perfectly. A cap or a floor is a trade —
 * coverage for balance — and a tool that measures only one side of a trade will
 * always report the trade as a loss.
 */
function starvation(cache, shipped) {
  const byId = new Map(cache.atoms.map(a => [a.id, a]));
  const perEntity = new Map();
  for (const id of shipped) {
    const a = byId.get(id);
    if (!a || a.section === 'Projects') continue;
    perEntity.set(a.entity, (perEntity.get(a.entity) ?? 0) + 1);
  }
  const expEntities = [...new Set(cache.atoms.filter(a => a.section !== 'Projects').map(a => a.entity))];
  const starved = expEntities.filter(e => (perEntity.get(e) ?? 0) <= 1).length;
  return { starved, all: expEntities.length && starved === expEntities.length ? 1 : 0 };
}

function evalCfg(cache, labels, offers, cfg) {
  const rows = [];
  for (const l of offers) {
    const id = String(l.offer.id);
    if (!cache.offers[id]) continue;
    const shipped = simulate(cache, id, cfg);
    const cov = coverage(l, shipped), yld = yieldOf(l, shipped);
    if (cov === null) continue;
    const s = starvation(cache, shipped);
    rows.push({ id, cov, yld: yld ?? 0, starved: s.starved, allStarved: s.all });
  }
  const m = k => rows.reduce((a, r) => a + r[k], 0) / (rows.length || 1);
  return { n: rows.length, cov: m('cov'), yld: m('yld'),
           starved: m('starved'), allStarved: m('allStarved'), rows };
}

// ── commands ─────────────────────────────────────────────────────────────────

/**
 * The real arm each funnel is validated against.
 *
 * These were hardcoded to `vbp2`'s 0.468/0.689 — an arm from 2026-08-07 run with
 * `SNIPE_PROJECT_BULLETS=2` and no line budget. Production moved to the 24-line
 * budget the next day and now measures 0.564, so `validate` was reporting "within
 * 0.011" of a number the pipeline had stopped producing, and would have gone on
 * reporting it indefinitely. Benchmark rule 1, in a tool built to enforce rule 1.
 *
 * A reference is a *measurement*, so it names the arm, the date and the flags it
 * was taken under. When production's funnel changes again this has to change with
 * it, and the label is what makes that obvious rather than silent.
 */
/**
 * The shipped ranker, in this simulator's parameterisation — which is **not**
 * `cv-select.mjs`'s.
 *
 * Production scores `cos + 0.10·grade`, then subtracts `α·mean` with
 * `α = w/(1+w)` at `w = 6`. This simulator scores
 * `cos + gradeW·grade + spikeW·(cos − mean)`, which expands to
 * `(1+spikeW)·cos − spikeW·mean + gradeW·grade`. Divide through by `(1+spikeW)`
 * and the ranking is identical to production's **except** that the judge term has
 * been shrunk by that same factor. So `--spike 6 --grade 0.10` is not the shipped
 * ranker: it is the shipped ranker with the judge at 0.10/7 ≈ 0.014, and the
 * judge is worth +0.115 pair accuracy.
 *
 * Scaling by `(1+w)` is what restores it, and it is what closes the gap: at
 * `spikeW=6, gradeW=0.10` the simulator lands 0.046 from the real arm, and at
 * `gradeW=0.70` it lands 0.011 — the same tolerance the legacy funnel validates
 * at. Any earlier sweep run at `spikeW > 0` was under-weighting the judge.
 */
const SHIPPED_SPIKE = 6;
// Every field production runs, including the floor. It was omitted when the
// floor shipped, so `validate` went on describing the pre-floor pipeline against
// the pre-floor arm — self-consistent, and one commit stale, which is the same
// failure §13 was written about. Anything added to the shipped funnel belongs
// here on the way in, not after the next tool discovers it.
const shippedCfg = (gradeW = 0.10) => ({
  spikeW: SHIPPED_SPIKE,
  gradeW: gradeW * (1 + SHIPPED_SPIKE),
  lineBudget: LINE_BUDGET,
  minExp: 2,
  floorMode: 'top',
});

const REFERENCE = {
  legacy: { cov: 0.468, yld: 0.689, arm: 'vbp2', at: '2026-08-07',
            flags: 'SNIPE_PROJECT_BULLETS=2, no line budget' },
  linebudget: { cov: 0.552, yld: 0.767, arm: 'floor2', at: '2026-08-13',
                flags: 'SNIPE_LINE_BUDGET=24 SNIPE_MAX_PROJECTS=3, minTopExpBullets=2' },
};

function validate() {
  const labels = loadLabels();
  const cache = addSpike(load());
  // The 32 offers the real generation runs used.
  const sample = readFileSync(resolve(PROJECT, 'batch/bench/tailor/sample32.tsv'), 'utf8')
    .trim().split('\n').map(l => l.split('\t')[0]);
  const offers = [...labels.values()].filter(l => sample.includes(String(l.offer.id)));
  // Default to the funnel production actually runs. `--legacy` still checks the
  // old one, because every ablation in the ledgers was measured on it.
  const legacy = process.argv.includes('--legacy');
  const cfg = legacy ? {} : shippedCfg();
  const ref = legacy ? REFERENCE.legacy : REFERENCE.linebudget;
  const base = evalCfg(cache, labels, offers, cfg);
  console.log(`simulator on the 32-offer sample: n=${base.n}  funnel=${legacy ? 'legacy' : 'line-budget'}`);
  console.log(`  reference: ${ref.arm} (${ref.at}, ${ref.flags})`);
  console.log(`  differentiator_coverage  ${base.cov.toFixed(3)}   (${ref.arm} measured ${ref.cov})`);
  console.log(`  grade_yield              ${base.yld.toFixed(3)}   (${ref.arm} measured ${ref.yld})`);
  // Coverage is the gate; yield is printed and not trusted. It has never
  // reproduced on this simulator — 0.518 against vbp2's 0.689 on the legacy
  // funnel, 0.424 against sum-v5's 0.777 here — and the gap is far too large to
  // be the handful of atoms coverage disagrees about. Read yield deltas as
  // direction at most, never as magnitude.
  console.log('  (grade_yield does not reproduce on this simulator — coverage is the gate)');
  const off = Math.abs(base.cov - ref.cov);
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

/**
 * One config, one split — the held-out check. Never tune with this.
 *
 * `base` is what the baseline arm runs, and it is not cosmetic: measured against
 * a baseline that lacks a shipped term, a variant reports its own gain *plus*
 * that term's and reads as roughly twice what it is worth — allocation read
 * +0.132 against spike 0 and +0.077 against the spike 6 that actually ships.
 * Every field defaults to the pre-change value, because every number already in
 * the ledger was taken that way; pass --base-spike / --base-cap / --base-gate-k
 * to measure the next change against what is shipped by then.
 */
function check(split, cfg, base = {}, shipped = false) {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW: defaultGradeW, dropped } = scorable(cache, labels, split);
  // The baseline is always the shipped ranker; only the variant may move gradeW,
  // so "delete the judge" is measured against what production actually does.
  const gradeW = cfg.gradeW ?? defaultGradeW;
  // `--shipped` puts BOTH arms on the funnel production runs. It has to be both:
  // a legacy baseline against a line-budget variant measures the funnel change
  // and calls it the variant's gain. Without the flag this stays on the legacy
  // funnel, because every number already in the ledgers was taken there and
  // silently re-pointing them at a different funnel makes them incomparable —
  // the same trap from the other side.
  const funnel = shipped ? shippedCfg(cfg.gradeW ?? 0.10) : {};
  const baseCfg = { gradeW: defaultGradeW, spikeW: 0, projCap: PROJ_BULLETS, gateK: 1, projKeep: PROJ_KEEP,
                    ...funnel, ...base };
  const varCfg = { ...funnel, ...cfg, gradeW: cfg.gradeW ?? funnel.gradeW ?? gradeW };
  const baseline = evalCfg(cache, labels, offers, baseCfg);
  const got = evalCfg(cache, labels, offers, varCfg);
  const byId = new Map(baseline.rows.map(r => [r.id, r]));
  const dCov = got.rows.map(r => r.cov - byId.get(r.id).cov);
  const dYld = got.rows.map(r => r.yld - byId.get(r.id).yld);
  console.log(`split=${split} n=${got.n} · judge term ${gradeW ? 'ON (0.10)' : 'OFF (cosine-only)'}` +
    `${dropped ? ` · ${dropped} ungraded offer(s) dropped` : ''}\n` +
    `funnel: ${shipped ? `SHIPPED (lineBudget=${LINE_BUDGET} minExp=2 floorMode=top)` : 'legacy (pre-2026-08-08)'}\n` +
    `baseline: spikeW=${baseCfg.spikeW} projCap=${baseCfg.projCap} gateK=${baseCfg.gateK} gradeCut=${baseCfg.gradeCut ?? 0}\n` +
    // Printed off the MERGED config, not the flags. Printing `cfg` showed
    // spikeW=0 while the run used the funnel's 6, which is precisely how the two
    // previous bugs in this file stayed invisible: the display described the
    // request rather than the run.
    `variant: gradeW=${varCfg.gradeW} spikeW=${varCfg.spikeW ?? 0} lambda=${varCfg.lambda ?? 0} ` +
    `reserve=${varCfg.reserve ?? 0} distinctW=${varCfg.distinctW ?? 0} projCap=${varCfg.projCap ?? PROJ_BULLETS} `+
    `gateK=${varCfg.gateK ?? 1} projKeep=${varCfg.projKeep ?? PROJ_KEEP} gradeCut=${varCfg.gradeCut ?? 0}\n`);
  const dStarved = got.rows.map(r => r.starved - byId.get(r.id).starved);
  const dAll = got.rows.map(r => r.allStarved - byId.get(r.id).allStarved);
  for (const [name, before, after, d] of [
    ['differentiator_coverage', baseline.cov, got.cov, dCov],
    ['grade_yield', baseline.yld, got.yld, dYld],
    // The other side of the trade. Ledger §14: coverage scores a page of project
    // bullets over two one-line employers perfectly, so a balance change looks
    // like a pure loss unless what it buys is measured next to what it costs.
    ['exp_starved', baseline.starved, got.starved, dStarved],
    ['all_exp_starved', baseline.allStarved, got.allStarved, dAll]]) {
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
function attribute(split = 'all', projCap = PROJ_BULLETS, gateK = 1) {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW } = scorable(cache, labels, split);
  const cfg = { gradeW, spikeW: 6, projCap, gateK };
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
      // Only as many bullets as this project was allocated can ever ship, so
      // anything beyond that many differentiators in it is lost by arithmetic,
      // not by ranking. Counted off the shipped slots rather than a constant,
      // because the allocation is no longer flat — under the flat cap the two
      // are the same number for every kept project.
      const slots = [...ship].filter(a => byId.get(a).entity === entity).length;
      const impossible = Math.max(0, ds.length - slots);
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
  console.log(`  more differentiators than the project's slots ${String(cause.capped).padStart(3)}  ${pct(cause.capped)}  ALLOCATION ONLY`);
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

function ablate(split = 'train', shipped = false) {
  const labels = loadLabels();
  const cache = addSpike(load());
  const { offers, gradeW, dropped } = scorable(cache, labels, split);
  // Same rule as `check`: the funnel is part of the baseline or every delta
  // below is the funnel's and not the variant's.
  const funnel = shipped ? shippedCfg() : {};
  const base = evalCfg(cache, labels, offers, { gradeW, ...funnel });
  console.log(`split=${split} n=${base.n}  judge ${gradeW ? 'ON' : 'OFF'}` +
    `${dropped ? ` (${dropped} ungraded dropped)` : ''}  baseline cov=${base.cov.toFixed(3)}` +
    `  funnel=${shipped ? 'SHIPPED' : 'legacy'}\n`);
  console.log('config                        cov     delta   yield   starved  all@1');
  const show = (name, cfg) => {
    const r = evalCfg(cache, labels, offers, { gradeW, ...funnel, ...cfg });
    console.log(`${name.padEnd(29)} ${r.cov.toFixed(3)}  ${r.cov - base.cov >= 0 ? '+' : ''}${(r.cov - base.cov).toFixed(3)}  ` +
      `${r.yld.toFixed(3)}   ${r.starved.toFixed(2)}     ${(100 * r.allStarved).toFixed(0)}%`);
  };
  // The grade cut, on the shipped funnel only — it acts on the surplus the
  // line budget leaves, which the legacy quotas do not have.
  if (shipped) {
    for (const c of [1, 2, 3]) show(`gradeCut ${c}`, { gradeCut: c });
    // Rule 4: the cheap explanation for anything the cut wins is simply
    // weighting the judge higher, which is what beat the distinctiveness rating.
    for (const w of [0.10, 0.20, 0.40, 0.70, 1.0]) show(`gradeW ${w} (control)`, { gradeW: w * (1 + SHIPPED_SPIKE) });
    for (const c of [1, 3]) for (const w of [0.40, 1.0])
      show(`gradeCut ${c} + gradeW ${w}`, { gradeCut: c, gradeW: w * (1 + SHIPPED_SPIKE) });
    // Backlog item 3: bound what projects may take of the 24 lines, rather than
    // flooring an entry inside a single contest. 24 is the whole budget, so it
    // must print delta 0.000 — the null-safety check that says the cap is a
    // generalisation and not a different allocator.
    for (const L of [24, 20, 18, 16, 15, 14, 12, 10]) show(`projMaxLines ${L}`, { projMaxLines: L });
    // Against the floor rather than beside it: the cap should make the floor
    // redundant if it is doing the same work from the other side.
    for (const L of [16, 14, 12]) show(`projMaxLines ${L}, no floor`, { projMaxLines: L, minExp: 1 });
    return;
  }
  for (const w of [1, 2, 3, 4, 6, 8, 12]) show(`spike ${w} only`, { spikeW: w });
  for (const l of [0.1, 0.2, 0.3, 0.5, 0.8]) show(`mmr ${l} only`, { lambda: l });
  for (const r of [2, 4, 6]) show(`reserve ${r} only`, { reserve: r });
  show('spike 4 + mmr 0.3', { spikeW: 4, lambda: 0.3 });
  // Adaptive allocation: same total project-bullet budget, distributed by score.
  // projCap 2 IS the shipped flat allocation, so it must print delta 0.000 —
  // anything else means the generalisation changed the baseline it generalises.
  for (const c of [2, 3, 4, 6]) show(`projCap ${c} only`, { projCap: c });
  for (const c of [3, 4, 6]) show(`spike 6 + projCap ${c}`, { spikeW: 6, projCap: c });
  // The project gate: how many bullets decide which projects make the cut.
  // gateK 1 is shipped and must print delta 0.000. Every project in this CV has
  // at least 4 bullets, so up to gateK 4 the sum compares equal-length lists and
  // cannot be won by simply having more bullets to add up.
  for (const k of [1, 2, 3, 4]) show(`gateK ${k}, shipped alloc`, { spikeW: 6, projCap: 4, gateK: k });
  // Same budget of 8, spread over all 5 projects instead of 4. Costs a project
  // header and blurb on the page, which the simulator cannot see -- render before
  // believing it.
  show('projKeep 5, shipped alloc', { spikeW: 6, projCap: 4, projKeep: 5 });
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
else if (cmd === 'ablate') ablate(arg('--split', 'train'), process.argv.includes('--shipped'));
else if (cmd === 'attribute') attribute(arg('--split', 'all'), parseInt(arg('--proj-cap', String(PROJ_BULLETS)), 10),
  parseInt(arg('--gate-k', '1'), 10));
else if (cmd === 'check') {
  // Only flags actually passed may override the funnel. Building these from
  // `arg(f, default)` gave every field a value whether or not it was typed, so
  // `--shipped` set spikeW=6 and the argv default immediately set it back to 0 —
  // both arms then ran the shipped line budget with the spike term switched off,
  // and the run still printed a clean paired result. An absent flag has to mean
  // absent, not zero.
  const has = (f) => process.argv.includes(f);
  const opt = (f, key, parse) => (has(f) ? { [key]: parse(arg(f, '0')) } : {});
  const num = (x) => parseFloat(x), int = (x) => parseInt(x, 10);
  check(arg('--split', 'test'),
    { ...opt('--spike', 'spikeW', num), ...opt('--lambda', 'lambda', num),
      ...opt('--reserve', 'reserve', int), ...opt('--distinct', 'distinctW', num),
      ...opt('--proj-cap', 'projCap', int), ...opt('--gate-k', 'gateK', int),
      ...opt('--proj-keep', 'projKeep', int), ...opt('--grade-cut', 'gradeCut', int),
      ...opt('--proj-max-lines', 'projMaxLines', int), ...opt('--min-exp', 'minExp', int),
      ...opt('--grade', 'gradeW', num) },
    { ...opt('--base-spike', 'spikeW', num), ...opt('--base-cap', 'projCap', int),
      ...opt('--base-gate-k', 'gateK', int), ...opt('--base-proj-keep', 'projKeep', int) },
    has('--shipped'));
}
else console.log('usage: select-sweep.mjs prep|grades|validate|sweep|ablate|attribute|check [--split train|test|all]\n' +
  '       grades [--distinct]   check [--spike W] [--lambda L] [--reserve N] [--grade W] [--distinct W]\n' +
  '                                   [--proj-cap N] [--gate-k N] [--proj-keep N] [--grade-cut N]\n' +
  '                                   [--proj-max-lines N] [--min-exp N]\n' +
  '                                   [--base-spike W] [--base-cap N] [--base-gate-k N]\n' +
  '       --shipped   run BOTH arms on the funnel production runs (line budget +\n' +
  '                   experience floor). Without it, sweep/ablate/check simulate the\n' +
  '                   pre-2026-08-08 funnel, which is where every ledger number was taken.');
