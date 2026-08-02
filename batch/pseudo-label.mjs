#!/usr/bin/env node
// @ts-check
/**
 * pseudo-label.mjs — expand the selection ground truth with the 30B judge.
 *
 * The human gold set is 12 offers. At that size the bootstrap CI on a paired
 * delta is roughly +/-0.05, so a variant worth +0.02 is invisible. More human
 * labels cost 25 minutes per twelve offers, which does not scale to the
 * hundred-odd needed.
 *
 * snipe-eval already reads a JD and matches it against CV evidence in Phase 2,
 * so it is not being asked to do anything new here — just to do it over the
 * fixed atom list and return ids. Two rules keep this honest:
 *
 *   1. Pseudo-labels are VALIDATED against the human set before use. `agree`
 *      reports how well the judge reproduces the human's picks; if that is not
 *      well above the retrieval baseline it is not a usable oracle.
 *   2. The human 12 stay out of the pseudo set. They are the held-out check
 *      that a variant tuned on pseudo-labels did not just learn the judge.
 *
 *   node batch/pseudo-label.mjs agree                validate against the human 12
 *   node batch/pseudo-label.mjs run [--n 80]         label offers, skipping the human 12
 *   node batch/pseudo-label.mjs stats
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { cvAtoms, parseSheet } from './goldset.mjs';
import { eligible, buildSample } from './tailor-harness.mjs';
import { extractBlockBRequirements } from './cv-select.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
const BENCH = resolve(__dirname, 'bench');
const SHEET = resolve(BENCH, 'goldset.md');
const LABELS = resolve(BENCH, 'pseudo-labels.json');

const SCHEMA = {
  type: 'object',
  properties: {
    selected: { type: 'array', items: { type: 'integer' } },
    reasoning: { type: 'string' },
  },
  required: ['selected'],
};

/**
 * Graded schema. A set answer is coarse: every selected atom ties with every
 * other selected one, and pair accuracy scores ties at 0.5, so a set can only
 * reach the human's ranking by getting the boundary exactly right. Asking for
 * 0-3 per atom yields an ordering instead, which is also what the retrieval
 * variants produce and therefore the like-for-like comparison.
 */
const GRADED_SCHEMA = {
  type: 'object',
  properties: {
    grades: {
      type: 'array',
      items: {
        type: 'object',
        properties: { id: { type: 'integer' }, grade: { type: 'integer' } },
        required: ['id', 'grade'],
      },
    },
  },
  required: ['grades'],
};

const SYSTEM = `You are a recruiter reviewing which items from a candidate's master CV belong on a CV tailored to one specific job posting.

You are given the posting's requirements and a numbered list of CV items. Return the ids of the items that earn their place on the tailored CV.

Rules:
- The CV is two pages. An item earns its place by displacing another item.
- Judge on what the item DEMONSTRATES, not on keyword overlap. A project in a different language still counts if it shows the engineering the posting asks for.
- Do not select an item merely because it is impressive. It must be relevant to THIS posting.
- Do not select everything. Typical answers select 5 to 8 of the items.
- Return ids only, in the "selected" array.`;

const GRADED_SYSTEM = `You are a recruiter deciding which items from a candidate's master CV to keep on a CV tailored to one specific job posting.

Grade EVERY item from 0 to 3:
  3 - directly evidences a requirement of this posting; must appear
  2 - clearly relevant; earns its place if there is room
  1 - tangential; only if nothing better
  0 - not relevant to this posting

Judge on what the item DEMONSTRATES, not keyword overlap: a project in a
different language still grades high if it shows the engineering this posting
asks for. Being impressive is not relevance. Use the full range — a grading
where most items are 2 or 3 is not a grading.

Return one entry per item id.`;

async function judgeGraded(reqs, jd, atoms, { ollamaUrl, model, timeoutMs = 300_000, shots = [] }) {
  const list = atoms.map(a =>
    `${a.id}. [${a.kind}] ${a.text}${a.kind === 'project' ? ` — ${(a.body || '').slice(0, 220)}` : ''}`
  ).join('\n');
  const messages = [{ role: 'system', content: GRADED_SYSTEM }];
  // Few-shot from human labels, leave-one-out at the call site. This is the
  // point of the exercise: the judge is meant to imitate THIS human's taste,
  // and a worked example transfers that far better than an adjective can.
  for (const s of shots) {
    messages.push({ role: 'user', content: shotUser(s.reqs, s.jd, list) });
    messages.push({ role: 'assistant', content: JSON.stringify({
      grades: atoms.map(a => ({ id: a.id, grade: s.want.has(a.id) ? 3 : 0 })) }) });
  }
  messages.push({ role: 'user', content: shotUser(reqs, jd, list) });
  const res = await fetch(`${ollamaUrl}/api/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model, messages, stream: false, format: GRADED_SCHEMA,
      options: { temperature: 0, num_ctx: 12288, num_predict: 1536 },
    }),
    signal: AbortSignal.timeout(timeoutMs),
  });
  if (!res.ok) throw new Error(`Ollama HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const data = await res.json();
  const parsed = JSON.parse(data?.message?.content || '{}');
  const g = new Map(atoms.map(a => [a.id, 0]));
  for (const e of parsed.grades || []) if (g.has(e.id)) g.set(e.id, Number(e.grade) || 0);
  return g;
}

function shotUser(reqs, jd, list) {
  const jdPart = jd ? `\n\n## Posting (excerpt)\n\n${String(jd).slice(0, 2500)}` : '';
  return `## Requirements\n\n${reqs.map(r => `- ${r}`).join('\n')}${jdPart}\n\n## Candidate CV items\n\n${list}\n\nGrade every item 0-3 for this posting.`;
}

async function judge(reqs, atoms, { ollamaUrl, model, timeoutMs = 300_000 }) {
  const list = atoms.map(a =>
    `${a.id}. [${a.kind}] ${a.text}${a.kind === 'project' ? ` — ${(a.body || '').slice(0, 220)}` : ''}`
  ).join('\n');
  const user = `## Job posting requirements\n\n${reqs.map(r => `- ${r}`).join('\n')}\n\n## Candidate CV items\n\n${list}\n\nWhich item ids belong on the CV tailored to this posting?`;
  const res = await fetch(`${ollamaUrl}/api/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model,
      messages: [{ role: 'system', content: SYSTEM }, { role: 'user', content: user }],
      stream: false,
      format: SCHEMA,
      // Temperature 0: greedy decoding is byte-identical on this stack, so a
      // re-run of the labeller reproduces the labels exactly.
      options: { temperature: 0, num_ctx: 8192, num_predict: 1024 },
    }),
    signal: AbortSignal.timeout(timeoutMs),
  });
  if (!res.ok) throw new Error(`Ollama HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const data = await res.json();
  const parsed = JSON.parse(data?.message?.content || '{}');
  const valid = new Set(atoms.map(a => a.id));
  return [...new Set((parsed.selected || []).filter(n => valid.has(n)))].sort((a, b) => a - b);
}

/** Jaccard and per-offer pair accuracy of one label set against another. */
function compare(a, b, atoms) {
  const inter = [...a].filter(x => b.has(x)).length;
  const union = new Set([...a, ...b]).size;
  return {
    jaccard: union ? inter / union : 1,
    // The judge's set treated as a ranking: selected above unselected. This is
    // directly comparable to what the retrieval variants score.
    pair: (() => {
      let ok = 0, n = 0;
      for (const w of b) for (const at of atoms) {
        if (b.has(at.id)) continue;
        n++;
        const sw = a.has(w) ? 1 : 0, su = a.has(at.id) ? 1 : 0;
        if (sw === su) ok += 0.5; else if (sw > su) ok++;
      }
      return n ? ok / n : 0;
    })(),
  };
}

function loadLabels() {
  return existsSync(LABELS) ? JSON.parse(readFileSync(LABELS, 'utf8')) : { model: null, offers: {} };
}

const reqsOf = (o) => extractBlockBRequirements(readFileSync(resolve(PROJECT, o.report), 'utf8'));

async function cmdAgree(opts) {
  const atoms = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  const human = parseSheet(readFileSync(SHEET, 'utf8'));
  const byId = new Map(eligible().map(o => [o.id, o]));
  const rows = [];
  for (const [id, want] of human) {
    if (!want.size) continue;
    const o = byId.get(id);
    if (!o) continue;
    const reqs = reqsOf(o);
    if (!reqs.length) continue;
    process.stderr.write(`judging #${id} ${o.company}\n`);
    const sel = new Set(await judge(reqs, atoms, opts));
    const c = compare(sel, want, atoms);
    rows.push({ offer: `${o.company}`.slice(0, 28), human: want.size, judge: sel.size,
                jaccard: c.jaccard.toFixed(2), pair: c.pair.toFixed(2) });
  }
  console.table(rows);
  const mp = rows.reduce((a, r) => a + parseFloat(r.pair), 0) / rows.length;
  const mj = rows.reduce((a, r) => a + parseFloat(r.jaccard), 0) / rows.length;
  console.log(`\nmean jaccard ${mj.toFixed(3)}   mean pair accuracy ${mp.toFixed(3)}   n=${rows.length}`);
  console.log(`baseline retrieval scores 0.764 pair on the same offers.`);
  console.log(mp >= 0.85
    ? 'USABLE — the judge reproduces the human well enough to label at scale.'
    : 'NOT USABLE as-is — tuning retrieval toward this would chase the judge, not the human.');
}

/**
 * Graded judge vs the human, optionally with `nshot` worked examples drawn
 * leave-one-out from the human's own labels.
 */
async function cmdAgree2(opts, nshot) {
  const atoms = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  const human = parseSheet(readFileSync(SHEET, 'utf8'));
  const byId = new Map(eligible().map(o => [o.id, o]));
  const items = [];
  for (const [id, want] of human) {
    if (!want.size) continue;
    const o = byId.get(id);
    if (!o) continue;
    const reqs = reqsOf(o);
    if (!reqs.length) continue;
    items.push({ id, o, want, reqs, jd: readFileSync(resolve(PROJECT, o.jd), 'utf8') });
  }
  const rows = [];
  for (const it of items) {
    // Shots come from other offers only. Sharing even one would let the judge
    // read back the answer it is being scored on.
    const shots = items.filter(x => x.id !== it.id).slice(0, nshot);
    process.stderr.write(`grading #${it.id} ${it.o.company} (${shots.length} shots)\n`);
    let g;
    try { g = await judgeGraded(it.reqs, it.jd, atoms, { ...opts, shots }); }
    catch (e) { process.stderr.write(`  skip: ${String(e.message).slice(0, 100)}\n`); continue; }
    const ranked = atoms.map(a => ({ id: a.id, s: g.get(a.id) }));
    const { pairAccuracy, precisionAtK } = await import('./retrieval-bench.mjs');
    rows.push({ offer: String(it.o.company).slice(0, 26), human: it.want.size,
                spread: [...new Set(g.values())].sort().join(''),
                'p@k': precisionAtK(ranked, it.want).toFixed(2),
                pair: pairAccuracy(ranked, it.want).toFixed(2) });
  }
  console.table(rows);
  const m = k => (rows.reduce((a, r) => a + parseFloat(r[k]), 0) / rows.length).toFixed(3);
  console.log(`\ngraded judge, ${nshot}-shot:  mean pair ${m('pair')}   mean p@k ${m('p@k')}   n=${rows.length}`);
  console.log('retrieval baseline on the same offers: pair 0.764, p@k 0.635');
  return rows;
}

const GRADES = resolve(BENCH, 'judge-grades.json');

/**
 * Cache the graded judge's output for every gold offer, using FIXED exemplars.
 *
 * agree2 uses leave-one-out shots, which is right for asking "how well can this
 * judge imitate the human". It is wrong for building a production feature: in
 * production the exemplars are fixed, so a benchmark whose shots change per
 * offer measures something that cannot be deployed. Here the first two offers
 * (by id) are the permanent exemplars and are marked so the bench can drop
 * them from evaluation — they are training data.
 */
async function cmdGrades(opts, nshot) {
  const atoms = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  const human = parseSheet(readFileSync(SHEET, 'utf8'));
  const byId = new Map(eligible().map(o => [o.id, o]));
  const items = [];
  for (const [id, want] of human) {
    if (!want.size) continue;
    const o = byId.get(id);
    if (!o) continue;
    const reqs = reqsOf(o);
    if (!reqs.length) continue;
    items.push({ id, o, want, reqs, jd: readFileSync(resolve(PROJECT, o.jd), 'utf8') });
  }
  items.sort((a, b) => Number(a.id) - Number(b.id));
  // --shot-ids picks the exemplars explicitly. Swapping them is the robustness
  // check that matters: if the gain only holds for one lucky pair, the feature
  // is fitted to those two offers rather than to the human's taste.
  const pick = String(arg('--shot-ids', '')).split(',').filter(Boolean);
  const shots = pick.length ? items.filter(i => pick.includes(i.id)) : items.slice(0, nshot);
  const shotIds = shots.map(s => s.id);
  const store = { model: opts.model, nshot, shotIds, offers: {} };
  for (const it of items) {
    process.stderr.write(`grading #${it.id} ${it.o.company}\n`);
    try {
      const g = await judgeGraded(it.reqs, it.jd, atoms, { ...opts, shots: shots.filter(s => s.id !== it.id) });
      store.offers[it.id] = Object.fromEntries(g);
    } catch (e) { process.stderr.write(`  skip: ${String(e.message).slice(0, 100)}\n`); }
  }
  mkdirSync(BENCH, { recursive: true });
  const outPath = String(arg('--out', GRADES));
  writeFileSync(outPath, JSON.stringify(store, null, 1));
  console.log(`wrote ${Object.keys(store.offers).length} graded offers to ${outPath}`);
  console.log(`exemplars (excluded from evaluation): ${shotIds.join(', ')}`);
}

async function cmdRun(n, opts) {
  const atoms = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  const human = new Set([...parseSheet(readFileSync(SHEET, 'utf8'))].filter(([, s]) => s.size).map(([id]) => id));
  const store = loadLabels();
  if (store.model && store.model !== opts.model) throw new Error(`labels were made by ${store.model}, refusing to mix with ${opts.model}`);
  store.model = opts.model;
  // Widest apply-worthy pool; the human 12 are excluded so they stay held out.
  const pool = buildSample(n + human.size, {}, 3.0).filter(o => !human.has(o.id));
  let done = 0;
  for (const o of pool.slice(0, n)) {
    if (store.offers[o.id]) { done++; continue; }
    const reqs = reqsOf(o);
    if (!reqs.length) continue;
    try {
      const sel = await judge(reqs, atoms, opts);
      store.offers[o.id] = { selected: sel, company: o.company, role: o.role, score: o.score };
      done++;
      process.stderr.write(`[${done}/${Math.min(n, pool.length)}] #${o.id} ${o.company} → ${sel.length} atoms\n`);
      mkdirSync(BENCH, { recursive: true });
      writeFileSync(LABELS, JSON.stringify(store, null, 1));
    } catch (e) {
      process.stderr.write(`[skip] #${o.id}: ${String(e.message).slice(0, 120)}\n`);
    }
  }
  console.log(`${Object.keys(store.offers).length} pseudo-labelled offers in ${LABELS}`);
}

function cmdStats() {
  const store = loadLabels();
  const ids = Object.keys(store.offers);
  if (!ids.length) return console.log('no labels yet');
  const sizes = ids.map(i => store.offers[i].selected.length);
  const freq = new Map();
  for (const i of ids) for (const a of store.offers[i].selected) freq.set(a, (freq.get(a) || 0) + 1);
  console.log(`model ${store.model} · ${ids.length} offers · selected/offer mean ${(sizes.reduce((a, b) => a + b, 0) / sizes.length).toFixed(1)} min ${Math.min(...sizes)} max ${Math.max(...sizes)}`);
  console.log('atom pick rate: ' + [...freq].sort((a, b) => a[0] - b[0]).map(([a, c]) => `${a}:${(c / ids.length).toFixed(2)}`).join(' '));
}

const arg = (f, d) => { const i = process.argv.indexOf(f); return i === -1 ? d : process.argv[i + 1]; };
const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
const opts = { ollamaUrl: String(arg('--ollama-url', 'http://localhost:11434')), model: String(arg('--model', 'snipe-eval')) };
const cmd = isMain ? process.argv[2] : null;
if (!isMain) { /* imported */ }
else if (cmd === 'agree') await cmdAgree(opts);
else if (cmd === 'agree2') await cmdAgree2(opts, Number(arg('--shots', 0)));
else if (cmd === 'grades') await cmdGrades(opts, Number(arg('--shots', 2)));
else if (cmd === 'run') await cmdRun(Number(arg('--n', 80)), opts);
else if (cmd === 'stats') cmdStats();
else if (cmd === 'selfcheck') {
  const assert = (c, m) => { if (!c) { console.error(`✗ ${m}`); process.exit(1); } };
  const atoms = [{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }];
  const perfect = compare(new Set([1, 2]), new Set([1, 2]), atoms);
  assert(perfect.jaccard === 1 && perfect.pair === 1, 'identical sets agree perfectly');
  const wrong = compare(new Set([3, 4]), new Set([1, 2]), atoms);
  assert(wrong.jaccard === 0 && wrong.pair === 0, 'disjoint sets agree not at all');
  assert(compare(new Set([1, 2, 3, 4]), new Set([1, 2]), atoms).pair === 0.5, 'selecting everything is chance');
  console.log('pseudo-label selfcheck ok');
} else console.log('usage: agree | agree2 [--shots N] | run [--n 80] | stats | selfcheck');

export { compare, judge, judgeGraded, LABELS };
