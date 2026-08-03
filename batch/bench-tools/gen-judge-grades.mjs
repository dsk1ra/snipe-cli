#!/usr/bin/env node
// @ts-check
/**
 * gen-judge-grades.mjs — produce a judge-grades file for a gold sheet.
 *
 * Runs the *production* reranker path (goldset.loadExemplars → cv-select's
 * judgeGrades) over every offer in a sheet, so the grades the bench evaluates
 * are the grades the pipeline would actually compute. Writing them once and
 * caching means a variant sweep costs no 30B calls at all.
 *
 *   node batch/bench-tools/gen-judge-grades.mjs \
 *     --sheet batch/bench/goldset-2.md --out batch/bench/judge-grades-2.json
 */

import { readFileSync, writeFileSync, existsSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { loadGold } from '../retrieval-bench.mjs';
import { cvAtoms, loadExemplars } from '../goldset.mjs';
import { judgeGrades } from '../cv-select.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '../..');

const arg = (name, def) => {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 ? process.argv[i + 1] : def;
};

const sheet = resolve(PROJECT, String(arg('sheet', 'batch/bench/goldset-2.md')));
const out = resolve(PROJECT, String(arg('out', 'batch/bench/judge-grades-2.json')));
const ollamaUrl = String(arg('ollama-url', 'http://localhost:11434'));

if (!existsSync(sheet)) throw new Error(`no sheet at ${sheet}`);

const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
const atoms = cvAtoms(cvText);
const gold = loadGold(sheet);
const judgeShots = loadExemplars(cvText);
if (!judgeShots.length) throw new Error('no exemplars — the judge refuses to run 0-shot, and so does this');

// The bench keys grades by atom id; the judge keys them by bullet text. Each
// atom's FIRST part is the text cv-select passes as that atom's item, so the
// mapping is exact rather than a fuzzy match.
const items = atoms.map(a => ({ text: a.parts[0], id: a.id }));

const offers = {};
for (const [i, g] of gold.entries()) {
  process.stderr.write(`[${i + 1}/${gold.length}] #${g.id} ${g.company}\n`);
  const grades = await judgeGrades(items, g.reqs, g.jd, { ollamaUrl, judgeShots });
  if (!grades) { process.stderr.write('  judge returned nothing — skipped\n'); continue; }
  const byId = {};
  for (const it of items) byId[it.id] = grades.get(it.text) ?? 0;
  offers[g.id] = byId;
}

writeFileSync(out, JSON.stringify({
  model: 'snipe-eval', nshot: judgeShots.length,
  shotIds: [],  // sheet 2 shares no offers with the exemplars, so none are training data
  sheet: sheet.replace(PROJECT + '/', ''),
  offers,
}, null, 2), 'utf8');
console.log(`wrote ${Object.keys(offers).length} offers to ${out}`);
