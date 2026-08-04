#!/usr/bin/env node
// @ts-check
/**
 * retrieval-bench.mjs — A/B retrieval strategies for Phase 3 bullet selection.
 *
 * `goldset.mjs score` answers "does the shipped ranker agree with the human".
 * This answers "which ranker agrees most", which needs more care: a dozen
 * variants tried against twelve offers will find a winner whether or not one
 * exists. So nothing here reports a bare mean.
 *
 *   - Every variant is compared to the baseline **paired, per offer**. The same
 *     offers, the same labels; only the scoring function moves.
 *   - The headline is the bootstrap 95% CI of the mean paired delta, resampling
 *     offers (the unit of independence). A CI straddling 0 is not a result.
 *   - A sign test over offers runs alongside it, because one offer swinging 0.4
 *     can carry a mean that no other offer supports.
 *
 * Embeddings are cached on disk by (model fingerprint, text), so a sweep over
 * twenty variants embeds each bullet once.
 *
 *   node batch/retrieval-bench.mjs list
 *   node batch/retrieval-bench.mjs run [--only a,b,c] [--boot 5000]
 *   node batch/retrieval-bench.mjs selfcheck
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { createHash } from 'crypto';
import { cvAtoms, parseSheet } from './goldset.mjs';
import { eligible } from './tailor-harness.mjs';
import { extractBlockBRequirements } from './cv-select.mjs';
import { embed, cosine, modelFingerprint, EMBED_MODEL } from './embeddings.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
const BENCH = resolve(__dirname, 'bench');
// One cache file per model: the fingerprint check would otherwise discard the
// whole cache on every switch, and re-embedding 900 background requirements
// with a 4B model is not a thing to do twice.
let EMBED_MODEL_NAME = EMBED_MODEL;
const cachePath = () => resolve(BENCH, `embed-cache-${EMBED_MODEL_NAME.replace(/[^a-z0-9.-]/gi, '_')}.json`);
const SHEET = resolve(BENCH, 'goldset.md');

// ── embedding cache ───────────────────────────────────────────────────────────

/** @type {Record<string, number[]>} */
let cache = {};
let cacheDirty = false;
let fingerprint = '';

async function initCache() {
  fingerprint = await modelFingerprint({ model: EMBED_MODEL_NAME });
  const CACHE = cachePath();
  if (existsSync(CACHE)) {
    const d = JSON.parse(readFileSync(CACHE, 'utf8'));
    // A cache keyed by a different embedder is silently wrong, not merely stale.
    if (d.fingerprint === fingerprint) cache = d.vectors || {};
  }
}
function saveCache() {
  if (!cacheDirty) return;
  mkdirSync(BENCH, { recursive: true });
  writeFileSync(cachePath(), JSON.stringify({ fingerprint, vectors: cache }));
  cacheDirty = false;
}
const keyOf = (t) => createHash('sha1').update(t).digest('hex').slice(0, 20);

/** Embed with disk cache. Order-preserving. @param {string[]} texts */
export async function embedCached(texts) {
  const missing = [...new Set(texts.filter(t => !cache[keyOf(t)]))];
  for (let i = 0; i < missing.length; i += 64) {
    const chunk = missing.slice(i, i + 64);
    const vecs = await embed(chunk, { model: EMBED_MODEL_NAME });
    chunk.forEach((t, j) => { cache[keyOf(t)] = vecs[j]; });
    cacheDirty = true;
  }
  saveCache();
  return texts.map(t => cache[keyOf(t)]);
}

// ── gold data ─────────────────────────────────────────────────────────────────

/**
 * One labelled offer: the requirements, the JD, and the atom ids a human ticked.
 * @typedef {{id: string, company: string, role: string, want: Set<number>,
 *            reqs: string[], jd: string}} GoldOffer
 */

/** @returns {GoldOffer[]} */
export function loadGold(sheetPath = SHEET) {
  const picks = parseSheet(readFileSync(sheetPath, 'utf8'));
  const byId = new Map(eligible().map(o => [o.id, o]));
  const out = [];
  for (const [id, want] of picks) {
    if (!want.size) continue;
    const o = byId.get(id);
    if (!o) continue;
    const reqs = extractBlockBRequirements(readFileSync(resolve(PROJECT, o.report), 'utf8'));
    if (!reqs.length) continue;
    out.push({ id, company: o.company, role: o.role, want, reqs,
               jd: readFileSync(resolve(PROJECT, o.jd), 'utf8') });
  }
  return out;
}

// ── metrics ───────────────────────────────────────────────────────────────────

/**
 * Fraction of (wanted, unwanted) atom pairs the ranking orders correctly.
 * Chance is 0.5 and it does not move with how many atoms the human ticked,
 * which precision@k does — that matters when k ranges 3..8 across offers.
 * @param {{id: number, s: number}[]} ranked  scored atoms, any order
 * @param {Set<number>} want
 */
export function pairAccuracy(ranked, want) {
  const sorted = [...ranked].sort((a, b) => b.s - a.s);
  const rank = new Map(sorted.map((r, i) => [r.id, i]));
  let ok = 0, n = 0;
  for (const w of want) {
    for (const r of ranked) {
      if (want.has(r.id)) continue;
      n++;
      // Ties are half credit; without this a variant that scores everything
      // equally reads as 1.0 or 0.0 depending on sort stability.
      const rw = /** @type {number} */ (rank.get(w)), ru = /** @type {number} */ (rank.get(r.id));
      const sw = sorted[rw].s, su = sorted[ru].s;
      if (sw === su) ok += 0.5; else if (rw < ru) ok++;
    }
  }
  return n ? ok / n : 0;
}

/** @param {{id: number, s: number}[]} ranked @param {Set<number>} want */
export function precisionAtK(ranked, want) {
  const top = [...ranked].sort((a, b) => b.s - a.s).slice(0, want.size);
  return top.filter(r => want.has(r.id)).length / want.size;
}

/** Binary-gain NDCG over the full ranking — rewards getting picks near the top. */
export function ndcg(ranked, want) {
  const sorted = [...ranked].sort((a, b) => b.s - a.s);
  let dcg = 0;
  sorted.forEach((r, i) => { if (want.has(r.id)) dcg += 1 / Math.log2(i + 2); });
  let idcg = 0;
  for (let i = 0; i < want.size; i++) idcg += 1 / Math.log2(i + 2);
  return idcg ? dcg / idcg : 0;
}

// ── statistics ────────────────────────────────────────────────────────────────

/** Deterministic PRNG so a reported CI is reproducible. */
function mulberry32(a) {
  return function () {
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * Bootstrap CI of the mean of `xs`, resampling with replacement.
 * `xs` are per-offer paired deltas, so the resampling unit is the offer.
 * @param {number[]} xs
 */
export function bootstrapCI(xs, draws = 5000, seed = 42, alpha = 0.05) {
  if (!xs.length) return { lo: 0, hi: 0, mean: 0 };
  const rnd = mulberry32(seed);
  const means = [];
  for (let d = 0; d < draws; d++) {
    let s = 0;
    for (let i = 0; i < xs.length; i++) s += xs[Math.floor(rnd() * xs.length)];
    means.push(s / xs.length);
  }
  means.sort((a, b) => a - b);
  return {
    mean: xs.reduce((a, b) => a + b, 0) / xs.length,
    lo: means[Math.floor(draws * alpha / 2)],
    hi: means[Math.floor(draws * (1 - alpha / 2))],
  };
}

/** Two-sided exact sign test on paired deltas; ties dropped, as is conventional. */
export function signTest(xs, eps = 1e-9) {
  const pos = xs.filter(x => x > eps).length;
  const neg = xs.filter(x => x < -eps).length;
  const n = pos + neg;
  if (!n) return { pos, neg, p: 1 };
  const C = (n, k) => { let r = 1; for (let i = 0; i < k; i++) r = r * (n - i) / (i + 1); return r; };
  let tail = 0;
  const k = Math.min(pos, neg);
  for (let i = 0; i <= k; i++) tail += C(n, i);
  return { pos, neg, p: Math.min(1, 2 * tail / 2 ** n) };
}

// ── query-side rewrites ───────────────────────────────────────────────────────

/**
 * Qwen3-Embedding is trained with an instruction prefix on the query side only.
 * Retrieval quality is reported to move a point or two with it, and it costs
 * one string concatenation, so it is worth knowing whether it helps here.
 */
const INSTRUCT = 'Given a job requirement, retrieve the CV achievement bullet that evidences it';
const withInstruct = (q) => `Instruct: ${INSTRUCT}\nQuery:${q}`;

/**
 * HyDE. A requirement ("Strong commercial C++ experience, C++17/20 preferred")
 * and a CV bullet ("Built a Rust microservice testbed comparing NIST post-
 * quantum signatures...") are the same subject in two different registers: one
 * is a demand, the other a past achievement with metrics. Cosine between
 * registers measures style as much as topic.
 *
 * So the requirement is first rewritten into the register it is searching —
 * one invented CV bullet that would satisfy it — and that hypothetical is
 * embedded instead. The hypothetical is never shown to anyone and never
 * reaches the CV; it exists only as a query vector.
 *
 * Generated with snipe-cv (7B) at temperature 0 and cached on disk, so the
 * whole sweep pays for it once.
 */
const HYDE_CACHE = resolve(BENCH, 'hyde-cache.json');
const HYDE_SYSTEM = 'Write ONE CV bullet point, 20-30 words, that a strong candidate would have on their CV to evidence the given job requirement. Past tense, concrete, include a plausible technology and a metric. Output only the bullet, no preamble, no leading dash.';

async function hydeFor(reqs, { ollamaUrl = 'http://localhost:11434', model = 'snipe-cv' } = {}) {
  let store = existsSync(HYDE_CACHE) ? JSON.parse(readFileSync(HYDE_CACHE, 'utf8')) : {};
  let dirty = false;
  const out = [];
  for (const r of reqs) {
    const k = keyOf(r);
    if (!store[k]) {
      const res = await fetch(`${ollamaUrl}/api/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model,
          messages: [{ role: 'system', content: HYDE_SYSTEM }, { role: 'user', content: r }],
          stream: false,
          options: { temperature: 0, num_ctx: 2048, num_predict: 96 },
        }),
        signal: AbortSignal.timeout(120_000),
      });
      if (!res.ok) throw new Error(`hyde HTTP ${res.status}`);
      const txt = String((await res.json())?.message?.content || '').trim().replace(/^[-*]\s*/, '').split('\n')[0];
      store[k] = txt || r;
      dirty = true;
    }
    out.push(store[k]);
  }
  if (dirty) { mkdirSync(BENCH, { recursive: true }); writeFileSync(HYDE_CACHE, JSON.stringify(store)); }
  return out;
}

// ── scoring context ───────────────────────────────────────────────────────────

/**
 * Everything a variant needs, precomputed once: the atoms, their per-part
 * vectors, and per-offer requirement vectors. A variant is a pure function of
 * this, which is what keeps the comparison paired.
 */
export async function buildContext(gold, atoms, { withHyde = true, gradesFile = null } = {}) {
  await initCache();
  // A sheet's ticks are atom ids frozen at labelling time. Edit cv.md and the
  // deleted atoms' ids survive in `want` but not in `atoms`, so every metric
  // indexes a rank that is not there — pairAccuracy died on `sorted[undefined].s`.
  // Intersect once here, where all three metrics route, and say so loudly: the
  // run is still valid (variants are compared paired on the same reduced set)
  // but it is no longer the same gold set the published number came from.
  const liveIds = new Set(atoms.map(a => a.id));
  let dropped = 0;
  for (const o of gold) {
    for (const w of o.want) if (!liveIds.has(w)) { o.want.delete(w); dropped++; }
  }
  if (dropped) {
    console.warn(`WARN: ${dropped} tick(s) reference atoms no longer in cv.md — dropped. ` +
      `Numbers are not comparable to runs made before the CV changed; re-sheet to restore full power.`);
  }
  const flat = atoms.flatMap(a => a.parts);
  const span = [];
  for (let i = 0, c = 0; i < atoms.length; i++) span.push([c, c += atoms[i].parts.length]);
  const partVecs = await embedCached(flat);
  const idf = jdIdf();
  const partToks = flat.map(tokens);

  // Background requirements for the hubness estimate, taken from reports that
  // are NOT under test. Using the test offers' own requirements would let each
  // atom's correction absorb the very signal being measured.
  const gradesPath = gradesFile || resolve(BENCH, 'judge-grades.json');
  const graded = existsSync(gradesPath) ? JSON.parse(readFileSync(gradesPath, 'utf8')) : { offers: {}, shotIds: [] };
  const goldIds = new Set(gold.map(g => g.id));
  const bg = [];
  for (const o of eligible()) {
    if (goldIds.has(o.id) || bg.length > 900) continue;
    try {
      for (const r of extractBlockBRequirements(readFileSync(resolve(PROJECT, o.report), 'utf8'))) bg.push(r);
    } catch { /* report vanished since the state file was written */ }
  }
  const bgVecs = await embedCached(bg.slice(0, 900));

  // CSLS uses the mean of the top-k background similarities, not the global
  // mean: a hub is a vector that is *strongly* close to many queries, and the
  // global mean is dominated by the long tail of unrelated ones.
  const K = 20;
  const hubness = partVecs.map(v => {
    const sims = bgVecs.map(b => cosine(b, v)).sort((a, b) => b - a).slice(0, K);
    return sims.reduce((a, b) => a + b, 0) / (sims.length || 1);
  });

  const offers = [];
  for (const g of gold) {
    const reqVecs = await embedCached(g.reqs);
    const reqStats = reqVecs.map(q => {
      const sims = partVecs.map(v => cosine(q, v));
      const mu = sims.reduce((a, b) => a + b, 0) / sims.length;
      const sd = Math.sqrt(sims.reduce((a, b) => a + (b - mu) ** 2, 0) / sims.length);
      return { mu, sd };
    });
    const instructVecs = await embedCached(g.reqs.map(withInstruct));
    // Requirement-looking sentences straight from the posting. Block B is the
    // 30B's parse of the same text, so anything it dropped is invisible to
    // every requirement-based variant; this is the check on that.
    const jdSents = String(g.jd).split(/\n|(?<=[.;])\s+/)
      .map(x => x.replace(/^[-*\u2022\s]+/, '').trim())
      .filter(x => x.length > 25 && x.length < 300 && /[a-z]/.test(x))
      .slice(0, 60);
    const jdSentVecs = jdSents.length ? await embedCached(jdSents) : null;
    let hydeVecs = null;
    if (withHyde) {
      const hyp = await hydeFor(g.reqs);
      hydeVecs = await embedCached(hyp);
    }
    offers.push({ ...g, reqVecs, reqStats, instructVecs, hydeVecs, jdSentVecs, reqToks: g.reqs.map(tokens),
                  grades: graded.offers[g.id] || null, isExemplar: (graded.shotIds || []).includes(g.id) });
  }
  return { atoms, flat, span, partVecs, partToks, idf, hubness, offers, bgCount: bgVecs.length,
           exemplars: (graded.shotIds || []).filter(id => goldIds.has(id)) };
}

// ── lexical machinery ─────────────────────────────────────────────────────────

/**
 * IDF over every cached JD, not over the 14 CV atoms. A 14-document corpus
 * gives "Rust" and "experience" nearly the same weight; 186 real postings put
 * three orders of magnitude between them, which is the entire point of IDF here.
 */
let idfCache = null;
export function jdIdf(jdDir = resolve(__dirname, 'jds')) {
  if (idfCache) return idfCache;
  const df = new Map();
  let n = 0;
  for (const f of readdirSync(jdDir).filter(f => f.endsWith('.txt'))) {
    const toks = new Set(tokens(readFileSync(resolve(jdDir, f), 'utf8')));
    n++;
    for (const t of toks) df.set(t, (df.get(t) || 0) + 1);
  }
  idfCache = new Map();
  for (const [t, d] of df) idfCache.set(t, Math.log(1 + (n - d + 0.5) / (d + 0.5)));
  idfCache.set('__default__', Math.log(1 + (n + 0.5) / 0.5));
  return idfCache;
}

const STOP = new Set(('a an the and or of in to for with on at by as is are be been being this that these those you your we our will '
  + 'have has had from strong good excellent experience experienced work working works ability able using use used across within '
  + 'role team teams year years including such etc via per new').split(' '));
/** @param {string} s */
export function tokens(s) {
  return (String(s).toLowerCase().match(/[a-z0-9][a-z0-9+#._-]{1,}/g) || [])
    .map(t => t.replace(/^[._-]+|[._-]+$/g, ''))
    .filter(t => t.length > 1 && !STOP.has(t));
}

/** BM25 of one query against one document, with IDF from the JD corpus. */
export function bm25(qToks, dToks, idf, avgdl = 28, k1 = 1.2, b = 0.75) {
  const tf = new Map();
  for (const t of dToks) tf.set(t, (tf.get(t) || 0) + 1);
  const dl = dToks.length || 1;
  let s = 0;
  for (const q of new Set(qToks)) {
    const f = tf.get(q);
    if (!f) continue;
    const w = idf.get(q) ?? idf.get('__default__');
    s += w * (f * (k1 + 1)) / (f + k1 * (1 - b + b * dl / avgdl));
  }
  return s;
}

// ── variants ──────────────────────────────────────────────────────────────────

/**
 * A variant scores every atom for one offer. It returns `{id, s}` per atom;
 * only the order matters, so scales need not be comparable between variants.
 * @typedef {(ctx: any, offer: any) => {id: number, s: number}[]} Variant
 */

/** max over an atom's parts of max over the offer's requirements. */
function maxCos(ctx, offer, atomIdx) {
  const [lo, hi] = ctx.span[atomIdx];
  let best = -Infinity;
  for (let j = lo; j < hi; j++) for (const q of offer.reqVecs) best = Math.max(best, cosine(q, ctx.partVecs[j]));
  return best;
}

/**
 * Hubness correction (CSLS). Some CV bullets are *hubs*: they sit near the
 * centre of the embedding space and rank high for every posting regardless of
 * topic — "Built the admin console" led both a Rust systems role and a trading
 * analytics role. Subtracting each atom's mean similarity to a large background
 * of unrelated requirements removes the part of the score that is "this text is
 * generically similar to job requirements" and leaves the part that is "this
 * text is similar to *these* requirements".
 *
 * The background is drawn from other offers' reports, never from the JD under
 * test, so it carries no label information.
 */
function csls(ctx, offer, atomIdx, k = 20) {
  const [lo, hi] = ctx.span[atomIdx];
  let best = -Infinity;
  for (let j = lo; j < hi; j++) {
    for (const q of offer.reqVecs) best = Math.max(best, cosine(q, ctx.partVecs[j]) - ctx.hubness[j]);
  }
  return best;
}

/** Per-requirement z-score: stops one broadly-phrased requirement dominating. */
function zMax(ctx, offer, atomIdx) {
  const [lo, hi] = ctx.span[atomIdx];
  let best = -Infinity;
  for (let qi = 0; qi < offer.reqVecs.length; qi++) {
    const { mu, sd } = offer.reqStats[qi];
    for (let j = lo; j < hi; j++) best = Math.max(best, (cosine(offer.reqVecs[qi], ctx.partVecs[j]) - mu) / (sd || 1e-9));
  }
  return best;
}

function bm25Score(ctx, offer, atomIdx) {
  const [lo, hi] = ctx.span[atomIdx];
  let best = 0;
  for (let j = lo; j < hi; j++) for (const qt of offer.reqToks) best = Math.max(best, bm25(qt, ctx.partToks[j], ctx.idf));
  return best;
}

/** Reciprocal rank fusion — scale-free, so no weight to tune. */
function rrf(ctx, offer, lists, k = 60) {
  const acc = new Map(ctx.atoms.map(a => [a.id, 0]));
  for (const list of lists) {
    const sorted = [...list].sort((a, b) => b.s - a.s);
    sorted.forEach((r, i) => acc.set(r.id, acc.get(r.id) + 1 / (k + i + 1)));
  }
  return ctx.atoms.map(a => ({ id: a.id, s: acc.get(a.id) }));
}

const perAtom = (fn) => (ctx, offer) => ctx.atoms.map((a, i) => ({ id: a.id, s: fn(ctx, offer, i) }));

/**
 * How often each atom is ticked across the OTHER offers — a JD-blind prior.
 * Leave-one-out is not optional: computed over all offers it reads the answer
 * for the offer being scored and every variant using it scores near 1.0.
 *
 * This is the control that says whether the problem is retrieval at all. If a
 * prior that never looks at the posting matches an embedding that does, then
 * the human's picks are mostly "these bullets are good" and only marginally
 * "these bullets suit this job" — and no amount of retrieval work moves it.
 */
function looPrior(ctx, offer) {
  const count = new Map(ctx.atoms.map(a => [a.id, 0]));
  let n = 0;
  for (const o of ctx.offers) {
    if (o.id === offer.id) continue;
    n++;
    for (const id of o.want) count.set(id, count.get(id) + 1);
  }
  return ctx.atoms.map(a => ({ id: a.id, s: count.get(a.id) / (n || 1) }));
}

/**
 * Mean of the top-n requirement similarities instead of the max. A single
 * requirement can match by accident; agreeing with two or three at once is
 * harder to do by chance.
 */
function topNCos(ctx, offer, atomIdx, n) {
  const [lo, hi] = ctx.span[atomIdx];
  const per = offer.reqVecs.map(q => {
    let best = -Infinity;
    for (let j = lo; j < hi; j++) best = Math.max(best, cosine(q, ctx.partVecs[j]));
    return best;
  }).sort((a, b) => b - a).slice(0, n);
  return per.reduce((a, b) => a + b, 0) / (per.length || 1);
}

/** Queries embedded through the alternate view (instruct-prefixed, or HyDE). */
function altCos(ctx, offer, atomIdx, key) {
  const alt = offer[key];
  if (!alt) return -Infinity;
  const [lo, hi] = ctx.span[atomIdx];
  let best = -Infinity;
  for (let j = lo; j < hi; j++) for (const q of alt) best = Math.max(best, cosine(q, ctx.partVecs[j]));
  return best;
}

/** @type {Record<string, Variant>} */
export const VARIANTS = {
  // production today
  'base-cos':        perAtom(maxCos),
  // hubness
  'csls':            perAtom((c, o, i) => csls(c, o, i)),
  'zscore':          perAtom(zMax),
  // lexical
  'bm25':            perAtom(bm25Score),
  // hybrids: cosine is ~[0.3,0.7], bm25 is unbounded, so bm25 is squashed first
  'hyb-0.10':        perAtom((c, o, i) => maxCos(c, o, i) + 0.10 * Math.tanh(bm25Score(c, o, i) / 4)),
  'hyb-0.20':        perAtom((c, o, i) => maxCos(c, o, i) + 0.20 * Math.tanh(bm25Score(c, o, i) / 4)),
  'hyb-0.35':        perAtom((c, o, i) => maxCos(c, o, i) + 0.35 * Math.tanh(bm25Score(c, o, i) / 4)),
  'csls+bm25-0.20':  perAtom((c, o, i) => csls(c, o, i) + 0.20 * Math.tanh(bm25Score(c, o, i) / 4)),
  'rrf-cos-bm25':    (c, o) => rrf(c, o, [perAtom(maxCos)(c, o), perAtom(bm25Score)(c, o)]),
  // aggregation over requirements
  'top2-cos':        perAtom((c, o, i) => topNCos(c, o, i, 2)),
  'top3-cos':        perAtom((c, o, i) => topNCos(c, o, i, 3)),
  'max+top3':        perAtom((c, o, i) => maxCos(c, o, i) + topNCos(c, o, i, 3)),
  // query-side rewrites
  'instruct':        perAtom((c, o, i) => altCos(c, o, i, 'instructVecs')),
  'hyde':            perAtom((c, o, i) => altCos(c, o, i, 'hydeVecs')),
  'hyde+cos':        perAtom((c, o, i) => maxCos(c, o, i) + altCos(c, o, i, 'hydeVecs')),
  'hyde+cos+bm25':   perAtom((c, o, i) => maxCos(c, o, i) + altCos(c, o, i, 'hydeVecs') + 0.2 * Math.tanh(bm25Score(c, o, i) / 4)),
  'rrf-cos-hyde':    (c, o) => rrf(c, o, [perAtom(maxCos)(c, o), perAtom((x, y, i) => altCos(x, y, i, 'hydeVecs'))(c, o)]),
  'rrf-cos-hyde-bm25': (c, o) => rrf(c, o, [perAtom(maxCos)(c, o), perAtom((x, y, i) => altCos(x, y, i, 'hydeVecs'))(c, o), perAtom(bm25Score)(c, o)]),
  // LLM reranker as a feature, not as a label source. Grades are 0-3 from
  // snipe-eval with two fixed exemplars; the exemplar offers are dropped from
  // evaluation by the runner because they are training data.
  'judge':           (c, o) => c.atoms.map(a => ({ id: a.id, s: (o.grades || {})[a.id] ?? 0 })),
  'cos+judge-0.02':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.02 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.05':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.05 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.10':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.10 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.15':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.15 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.25':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.25 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.50':  (c, o) => perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.50 * ((o.grades || {})[r.id] ?? 0) })),
  'cos+judge-0.10+bm25': (c, o) => perAtom((x, y, i) => maxCos(x, y, i) + 0.10 * ((y.grades || {})[x.atoms[i].id] ?? 0) + 0.10 * Math.tanh(bm25Score(x, y, i) / 4))(c, o),
  // query source: Block B is the 30B's parse of the posting and may drop things
  'jdsent':          perAtom((c, o, i) => altCos(c, o, i, 'jdSentVecs')),
  'cos+jdsent':      perAtom((c, o, i) => maxCos(c, o, i) + altCos(c, o, i, 'jdSentVecs')),
  'cos+jdsent+judge': (c, o) => perAtom((x, y, i) => maxCos(x, y, i) + altCos(x, y, i, 'jdSentVecs') + 0.20 * ((y.grades || {})[x.atoms[i].id] ?? 0))(c, o),
  'rrf-cos-judge':   (c, o) => rrf(c, o, [perAtom(maxCos)(c, o), c.atoms.map(a => ({ id: a.id, s: (o.grades || {})[a.id] ?? 0 }))]),
  'rrf-cos-judge-bm25': (c, o) => rrf(c, o, [perAtom(maxCos)(c, o), c.atoms.map(a => ({ id: a.id, s: (o.grades || {})[a.id] ?? 0 })), perAtom(bm25Score)(c, o)]),
  // controls
  'prior-only':      looPrior,
  'random':          (c, o) => c.atoms.map((a, i) => ({ id: a.id, s: Math.sin(i * 12.9898 + Number(o.id)) })),
  'cos+prior-0.05':  (c, o) => { const p = new Map(looPrior(c, o).map(r => [r.id, r.s])); return perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.05 * p.get(r.id) })); },
  'cos+prior-0.15':  (c, o) => { const p = new Map(looPrior(c, o).map(r => [r.id, r.s])); return perAtom(maxCos)(c, o).map(r => ({ id: r.id, s: r.s + 0.15 * p.get(r.id) })); },
  'rrf-csls-bm25':   (c, o) => rrf(c, o, [perAtom((x, y, i) => csls(x, y, i))(c, o), perAtom(bm25Score)(c, o)]),
};


// ── runner ────────────────────────────────────────────────────────────────────

const METRICS = { pair: pairAccuracy, 'p@k': precisionAtK, ndcg };

export function evaluate(ctx, variant) {
  const per = { pair: [], 'p@k': [], ndcg: [] };
  for (const offer of ctx.offers) {
    const ranked = variant(ctx, offer);
    for (const [m, fn] of Object.entries(METRICS)) per[m].push(fn(ranked, offer.want));
  }
  return per;
}

const mean = xs => xs.reduce((a, b) => a + b, 0) / xs.length;

async function run({ only, boot, dropExemplars, gradesFile, embedModel, sheet }) {
  if (embedModel) EMBED_MODEL_NAME = embedModel;
  const atoms = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  // A second sheet is the only way to check a variant on offers that played no
  // part in choosing it. Without this flag the runner silently scored sheet 1
  // against sheet 2's grades — every id missed, so the judge read as chance and
  // cos+judge read as identical to cosine.
  const gold = loadGold(sheet ? resolve(PROJECT, sheet) : undefined);
  const ctx = await buildContext(gold, atoms, { gradesFile });
  // Offers used as judge exemplars are training data for any judge-based
  // variant. Evaluating on them would flatter those variants and only those,
  // so they come out for every variant or none — otherwise the comparison is
  // no longer paired over the same offers.
  if (dropExemplars && ctx.exemplars.length) {
    ctx.offers = ctx.offers.filter(o => !o.isExemplar);
    console.log(`dropped ${ctx.exemplars.length} judge exemplars: ${ctx.exemplars.join(', ')}`);
  }
  console.log(`embedder ${EMBED_MODEL_NAME}`);
  console.log(`${ctx.offers.length} labelled offers · ${atoms.length} atoms · ${ctx.flat.length} parts · ${ctx.bgCount} background requirements\n`);

  const names = only ? only.split(',') : Object.keys(VARIANTS);
  const baseName = 'base-cos';
  const basePer = evaluate(ctx, VARIANTS[baseName]);

  const rows = [];
  for (const name of names) {
    const v = VARIANTS[name];
    if (!v) { console.error(`unknown variant: ${name}`); continue; }
    const per = name === baseName ? basePer : evaluate(ctx, v);
    const deltas = per.pair.map((x, i) => x - basePer.pair[i]);
    const ci = bootstrapCI(deltas, boot);
    const st = signTest(deltas);
    rows.push({
      variant: name,
      pair: mean(per.pair).toFixed(3),
      'p@k': mean(per['p@k']).toFixed(3),
      ndcg: mean(per.ndcg).toFixed(3),
      'Δpair': (name === baseName ? 0 : ci.mean).toFixed(3),
      'CI95': name === baseName ? '—' : `[${ci.lo.toFixed(3)}, ${ci.hi.toFixed(3)}]`,
      'w/l': name === baseName ? '—' : `${st.pos}/${st.neg}`,
      sig: name === baseName ? '—' : (ci.lo > 0 ? 'YES' : ci.hi < 0 ? 'WORSE' : 'no'),
    });
  }
  rows.sort((a, b) => parseFloat(b.pair) - parseFloat(a.pair));
  console.table(rows);
  console.log('sig = bootstrap 95% CI of the paired per-offer delta excludes 0.');
  return rows;
}

const arg = (f, d) => { const i = process.argv.indexOf(f); return i === -1 ? d : process.argv[i + 1]; };
const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
const cmd = isMain ? process.argv[2] : null;
if (!isMain) { /* imported: expose helpers, run nothing */ }
else if (cmd === 'list') console.log(Object.keys(VARIANTS).join('\n'));
else if (cmd === 'run') await run({ only: arg('--only', null), boot: Number(arg('--boot', 5000)), dropExemplars: !process.argv.includes('--keep-exemplars'), gradesFile: arg('--grades', null), embedModel: arg('--embed-model', null), sheet: arg('--sheet', null) });
else if (cmd === 'selfcheck') {
  // Same shape as cv-select's self-check: a plain predicate, not node:assert,
  // whose assertion signatures tsc will not accept through a dynamic import.
  const assert = (c, m) => { if (!c) { console.error(`✗ ${m}`); process.exit(1); } };
  const perfect = [{ id: 1, s: 9 }, { id: 2, s: 8 }, { id: 3, s: 1 }];
  assert(pairAccuracy(perfect, new Set([1, 2])) === 1, 'perfect ranking');
  assert(pairAccuracy(perfect, new Set([3])) === 0, 'inverted ranking');
  assert(pairAccuracy([{ id: 1, s: 5 }, { id: 2, s: 5 }], new Set([1])) === 0.5, 'ties are half credit');
  assert(precisionAtK(perfect, new Set([1, 2])) === 1, 'p@k');
  assert(Math.abs(ndcg(perfect, new Set([1, 2])) - 1) < 1e-9, 'ndcg perfect');
  const ci = bootstrapCI([0.1, 0.1, 0.1, 0.1]);
  assert(Math.abs(ci.mean - 0.1) < 1e-9 && ci.lo > 0, 'constant positive delta is significant');
  assert(bootstrapCI([-0.3, 0.3, -0.2, 0.2]).lo < 0, 'symmetric noise is not significant');
  assert(signTest([1, 1, 1, 1, 1, 1]).p < 0.05, 'six wins, no losses');
  assert(signTest([1, -1, 1, -1]).p > 0.5, 'even split');
  const idf = jdIdf();
  assert(idf.get('rust') > idf.get('kubernetes'), 'rarer term scores higher idf');
  assert(bm25(tokens('Rust systems'), tokens('built a Rust microservice'), idf) > 0, 'bm25 matches');
  assert(bm25(tokens('Rust'), tokens('a Java servlet'), idf) === 0, 'bm25 no match is 0');
  console.log('retrieval-bench selfcheck ok');
} else console.log('usage: list | run [--only a,b] [--boot 5000] | selfcheck');

export { SHEET, BENCH };
