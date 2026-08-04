#!/usr/bin/env node
/**
 * Gold set for Phase 3 selection — the only part of the calibration that needs
 * a human. `sheet` writes a markdown file of CV atoms per offer for the user to
 * mark; `score` checks the embedding proxy's ranking against those marks.
 *
 *   node batch/goldset.mjs sheet [--n 12] [--min-score 3.5]
 *   node batch/goldset.mjs score [--sheet batch/bench/goldset.md]
 *
 * The marks validate the proxy. They are never tuned against — if the proxy
 * disagrees with the human, the proxy is what changes.
 */
import { readFileSync, writeFileSync, existsSync } from 'fs';
import { createHash } from 'crypto';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { eligible, buildSample } from './tailor-harness.mjs';
import { parseCvSections, parseEntries, extractBlockBRequirements } from './cv-select.mjs';
import { embed, cosine } from './embeddings.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
const SHEET = resolve(__dirname, 'bench/goldset.md');
/** Exemplars for the Phase 3 reranker. In batch/, not batch/bench/, because
 *  production reads it and bench/ is gitignored scratch. */
const SHOTS = resolve(__dirname, 'judge-shots.json');

export const cvHash = (cvText) => createHash('sha256').update(cvText).digest('hex').slice(0, 16);

/**
 * Freeze N gold offers into a self-contained exemplar file for the production
 * reranker: requirements, a JD excerpt, and the ticked atoms **as text**.
 *
 * Text, not ids. Atom ids are positional in cv.md, so adding one bullet
 * renumbers everything after it and a baked id would silently point at the
 * wrong bullet. Text is matched back exactly at load time, and anything that
 * fails to match disables the exemplar rather than guessing.
 */
function exportShots(ids) {
  const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  const atoms = cvAtoms(cvText);
  const byId = new Map(atoms.map(a => [a.id, a]));
  const picks = parseSheet(readFileSync(SHEET, 'utf8'));
  const offers = new Map(eligible().map(o => [o.id, o]));
  const shots = [];
  for (const id of ids) {
    const want = picks.get(id);
    const o = offers.get(id);
    if (!want?.size || !o) throw new Error(`offer ${id} is not a ticked gold offer`);
    shots.push({
      id, company: o.company, role: o.role,
      reqs: extractBlockBRequirements(readFileSync(resolve(PROJECT, o.report), 'utf8')),
      jd: readFileSync(resolve(PROJECT, o.jd), 'utf8').slice(0, 2500),
      wantText: [...want].map(i => byId.get(i)?.text).filter(Boolean),
    });
  }
  writeFileSync(SHOTS, JSON.stringify({ cvHash: cvHash(cvText), shots }, null, 1));
  console.log(`wrote ${shots.length} exemplars to ${SHOTS}`);
}

/**
 * Load exemplars for the current cv.md, or [] if they cannot be trusted.
 *
 * `want` comes back as a Set of bullet **text**, not ids: the consumer is
 * cv-select, which works in bullet text, and keeping ids out of the contract
 * means cv-select never has to import this module. That matters — goldset
 * imports cv-select, so the reverse edge would be a cycle, and a dynamic import
 * across it deadlocks while cv-select is still evaluating.
 */
export function loadExemplars(cvText, shotsPath = SHOTS) {
  if (!existsSync(shotsPath)) return [];
  let d;
  try { d = JSON.parse(readFileSync(shotsPath, 'utf8')); } catch { return []; }
  const known = new Set(cvAtoms(cvText).map(a => a.text));
  const out = [];
  for (const s of d.shots || []) {
    const texts = s.wantText || [];
    // A bullet reworded since labelling makes that exemplar a lie about what
    // the human picked. Drop it; a missing exemplar is recoverable, a wrong one
    // teaches the judge the opposite of the intended taste.
    if (!texts.length || texts.some(t => !known.has(t))) continue;
    out.push({ ...s, want: new Set(texts) });
  }
  return out;
}

/**
 * Every selectable atom on the CV: experience bullets and whole projects.
 *
 * `parts` is what actually gets embedded, and it mirrors `selectCvForJd`'s
 * `ctx` exactly — one bare string per real CV bullet.
 * A project is scored by its best part, never by its bullets joined: the joined
 * blob is 100–375 words against a 24–41 word bullet, and a long text's embedding
 * drifts to a generic centroid, so cosine against a short requirement ranks by
 * length rather than relevance. Measured: it buried both Rust systems projects
 * at 13th and 14th for a C++ HFT role.
 */
export function cvAtoms(cvText) {
  const out = [];
  for (const sec of parseCvSections(cvText)) {
    if (sec.name !== 'Experience' && sec.name !== 'Projects') continue;
    for (const e of parseEntries(sec.lines).entries) {
      const name = e.head[0].replace(/^###\s+/, '').trim();
      const bullets = e.bullets.map(b => b.replace(/^-\s*/, ''));
      if (sec.name === 'Experience') {
        const co = (e.head[1] || '').replace(/\*\*/g, '').split('—')[0].trim();
        for (const b of bullets) out.push({ kind: 'bullet', group: co, text: b, parts: [b] });
      } else {
        out.push({ kind: 'project', group: 'Projects', text: name, body: bullets.join(' '),
                   parts: bullets.slice() });
      }
    }
  }
  return out.map((a, i) => ({ ...a, id: i + 1 }));
}

function writeSheet(n, minScore, force, sheetPath = SHEET, excludePath = null) {
  // The ticks are hand-labelled and cost ~25 minutes to reproduce; regenerating
  // the sheet over them is unrecoverable. batch/bench/ is gitignored, so there
  // is no working copy to fall back on either.
  if (!force && existsSync(sheetPath) && [...parseSheet(readFileSync(sheetPath, 'utf8')).values()].some(s => s.size)) {
    throw new Error(`${sheetPath} already has ticks — pass --force to discard them`);
  }
  const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  const atoms = cvAtoms(cvText);
  // A second sheet must not re-ask about offers the first one already covers,
  // or the extra labels buy no extra power.
  const seen = excludePath && existsSync(excludePath)
    ? new Set([...parseSheet(readFileSync(excludePath, 'utf8'))].filter(([, v]) => v.size).map(([k]) => k))
    : new Set();
  const offers = buildSample(n + seen.size, {}, minScore).filter(o => !seen.has(o.id)).slice(0, n);
  const L = [
    '# Phase 3 selection gold set',
    '',
    `${offers.length} offers · ${atoms.length} CV atoms each · mark \`[x]\` on the atoms that`,
    'belong on the tailored CV for that offer. Leave the rest unticked. No ranking,',
    'no partial credit — belongs or does not.',
    '',
    'Then: `node batch/goldset.mjs score`',
    '',
    '## The atoms',
    '',
  ];
  for (const a of atoms) {
    L.push(`**${a.id}.** *(${a.group})* ${a.kind === 'project' ? `**${a.text}** — ${a.body.slice(0, 160)}…` : a.text}`);
    L.push('');
  }
  for (const o of offers) {
    const report = readFileSync(resolve(PROJECT, o.report), 'utf8');
    const reqs = extractBlockBRequirements(report).slice(0, 8);
    L.push('---', '', `## ${o.id} · ${o.company} — ${o.role}  *(eval ${o.score})*`, '',
      `JD: \`${o.jd}\` · report: \`${o.report}\``, '', 'What it asks for:', '');
    for (const r of reqs) L.push(`- ${r}`);
    L.push('');
    for (const a of atoms) {
      const label = a.kind === 'project' ? a.text : a.text.slice(0, 90) + (a.text.length > 90 ? '…' : '');
      L.push(`- [ ] **${a.id}** ${label}`);
    }
    L.push('');
  }
  writeFileSync(sheetPath, L.join('\n'));
  console.log(`wrote ${sheetPath} — ${offers.length} offers × ${atoms.length} atoms`);
}

/** Parse the marked sheet back into { offerId → Set(atomId) }. */
export function parseSheet(md) {
  const picks = new Map();
  let cur = null;
  for (const line of md.split('\n')) {
    const h = line.match(/^##\s+(\d+)\s+·/);
    if (h) { cur = h[1]; picks.set(cur, new Set()); continue; }
    const m = line.match(/^-\s*\[([ xX])\]\s*\*\*(\d+)\*\*/);
    if (m && cur && m[1] !== ' ') picks.get(cur).add(Number(m[2]));
  }
  return picks;
}

async function scoreSheet(sheetPath = SHEET) {
  if (!existsSync(sheetPath)) throw new Error(`no sheet — run: node batch/goldset.mjs sheet`);
  const picks = parseSheet(readFileSync(sheetPath, 'utf8'));
  const marked = [...picks].filter(([, s]) => s.size);
  if (!marked.length) throw new Error('sheet has no ticks yet');
  const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  const atoms = cvAtoms(cvText);
  const flat = atoms.flatMap(a => a.parts);
  const flatVecs = await embed(flat);
  const span = [];
  for (let i = 0, c = 0; i < atoms.length; i++) { span.push([c, c += atoms[i].parts.length]); }
  const byId = new Map(eligible().map(o => [o.id, o]));

  let totAcc = 0, totP = 0, rows = [];
  for (const [oid, want] of marked) {
    const o = byId.get(oid);
    if (!o) continue;
    const reqs = extractBlockBRequirements(readFileSync(resolve(PROJECT, o.report), 'utf8'));
    if (!reqs.length) continue;
    const reqVecs = await embed(reqs);
    // Best part × best requirement — `selectCvForJd`'s ranking function exactly,
    // so a gate pass is a statement about production and not about this file.
    const ranked = atoms
      .map((a, i) => {
        const [lo, hi] = span[i];
        let best = -1;
        for (let j = lo; j < hi; j++) for (const r of reqVecs) best = Math.max(best, cosine(r, flatVecs[j]));
        return { id: a.id, s: best };
      })
      .sort((a, b) => b.s - a.s);
    const k = want.size;
    const top = new Set(ranked.slice(0, k).map(r => r.id));
    const hit = [...top].filter(id => want.has(id)).length;
    // Pair accuracy: over wanted/unwanted pairs, how often the proxy ranks the
    // wanted one higher. Insensitive to k, unlike precision@k.
    const rank = new Map(ranked.map((r, i) => [r.id, i]));
    let ok = 0, n = 0;
    for (const w of want) for (const a of atoms) if (!want.has(a.id)) { n++; if (rank.get(w) < rank.get(a.id)) ok++; }
    totAcc += hit / k; totP += ok / n;
    rows.push({ offer: `${o.company} — ${o.role}`.slice(0, 44), k, [`p@k`]: (hit / k).toFixed(2), pair: (ok / n).toFixed(2) });
  }
  console.table(rows);
  console.log(`\nmean precision@k ${(totAcc / rows.length).toFixed(3)}   mean pair accuracy ${(totP / rows.length).toFixed(3)}   n=${rows.length}`);
  console.log(rows.length && totP / rows.length >= 0.75
    ? 'GATE PASS — proxy agrees with the gold set; selection_regret is safe to tune against.'
    : 'GATE FAIL — proxy disagrees with the human. Fix the proxy, not the labels.');
}

const arg = (f, d) => { const i = process.argv.indexOf(f); return i === -1 ? d : process.argv[i + 1]; };
// Guarded: importing this module must not run its CLI. Without it, importing
// goldset from another script made that script's argv[2] dispatch here too —
// `retrieval-bench.mjs selfcheck` silently ran the goldset selfcheck as well.
const isMain = process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url);
const cmd = isMain ? process.argv[2] : null;
if (!isMain) { /* imported: expose helpers, run nothing */ }
else if (cmd === 'export-shots') exportShots(String(arg('--ids', '5,50')).split(','));
else if (cmd === 'sheet') writeSheet(Number(arg('--n', 12)), Number(arg('--min-score', 3.5)), process.argv.includes('--force'), String(arg('--out', SHEET)), arg('--exclude', null));
else if (cmd === 'score') await scoreSheet(String(arg('--sheet', SHEET)));
else if (cmd === 'selfcheck') {
  const assert = (c, m) => { if (!c) { console.error(`✗ ${m}`); process.exit(1); } };
  const a = cvAtoms(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));
  assert(a.length > 10, 'atoms extracted');
  assert(a.some(x => x.kind === 'project') && a.some(x => x.kind === 'bullet'), 'both kinds');
  assert(JSON.stringify(a.map(x => x.id)) === JSON.stringify(a.map((_, i) => i + 1)), 'ids are 1..n');
  const p = parseSheet('## 42 · X — Y\n- [x] **3** a\n- [ ] **4** b\n## 43 · Z — W\n- [X] **1** c');
  assert(JSON.stringify([...p.get('42')]) === JSON.stringify([3]), 'ticked only');
  assert(JSON.stringify([...p.get('43')]) === JSON.stringify([1]), 'second offer separate');
  let clobbered = false;
  try { writeSheet(2, 3.5, false, SHEET); clobbered = true; } catch (e) { assert(/already has ticks/.test(e.message), 'refuses for the right reason'); }
  assert(!clobbered, 'refuses to clobber labels');
  // Exemplars are keyed by atom text, so a reworded bullet must drop the
  // exemplar rather than silently point the judge at a different bullet.
  const cvT = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  const real = cvAtoms(cvT);
  const tmp = resolve(PROJECT, 'batch/bench/.shots-selfcheck.json');
  writeFileSync(tmp, JSON.stringify({ cvHash: 'x', shots: [{ reqs: ['r'], jd: 'j', wantText: [real[0].text] }] }));
  assert(loadExemplars(cvT, tmp).length === 1, 'exemplar resolves by text');
  assert(loadExemplars(cvT, tmp)[0].want.has(real[0].text), 'want is bullet text, not ids');
  writeFileSync(tmp, JSON.stringify({ cvHash: 'x', shots: [{ reqs: ['r'], jd: 'j', wantText: ['a bullet that is not on the CV'] }] }));
  assert(loadExemplars(cvT, tmp).length === 0, 'unmatched atom text drops the exemplar');
  assert(loadExemplars(cvT, resolve(PROJECT, 'batch/bench/.nope.json')).length === 0, 'missing file is empty, not fatal');
  console.log('goldset selfcheck ok');
} else console.log('usage: sheet [--n 12] [--min-score 3.5] | score | export-shots [--ids 5,50] | selfcheck');
