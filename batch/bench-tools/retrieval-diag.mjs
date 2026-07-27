#!/usr/bin/env node
// Is stage 2 failing to JUDGE, or failing to SEE? For each requirement of an
// offer, show what topK=3 actually surfaced and what a wider topK would have.
// #42 marked "distributed systems" and "Go/C++/Rust/Java/C#" as Gap while the
// CV plainly covers both — this says whether the right atom was ever offered.
import { readFileSync } from 'node:fs';
import { dirname, resolve as _resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadCvIndex, embed, topK } from '../embeddings.mjs';
// Resolved from this file so the tool works from any cwd.
const ROOT = _resolve(dirname(fileURLToPath(import.meta.url)), '../..');

const ids = process.argv.slice(2);
if (!ids.length) { console.error('usage: retrieval-diag.mjs <offer-id>...'); process.exit(1); }
const K = +(process.env.K || 8);
const cvIndex = await loadCvIndex({});

for (const id of ids) {
  // Requirements come from the report's evidence table (exact text the model saw).
  const dir = process.env.BENCH || 'batch/bench/t01a';
  const { readdirSync } = await import('node:fs');
  const rf = readdirSync(_resolve(ROOT, dir, 'reports')).find(f => f.startsWith(`${id}-`));
  if (!rf) { console.log(`#${id}: no report in ${dir}`); continue; }
  const md = readFileSync(_resolve(ROOT, dir, 'reports', rf), 'utf8');
  const reqs = [];
  for (const line of md.split('\n')) {
    const m = /^\|(.*)\|(.*)\|\s*(Strong|Transferable|Gap)\s*\|\s*$/.exec(line);
    if (m) reqs.push({
      text: m[1].replace(/\*\*\[(must|nice)[^\]]*\]\*\*/, '').trim(),
      shown: m[2].trim(), strength: m[3],
    });
  }
  if (!reqs.length) { console.log(`#${id}: no evidence rows`); continue; }

  console.log(`\n${'='.repeat(78)}\n#${id}  (${reqs.length} requirements, K=${K})\n${'='.repeat(78)}`);
  const vecs = await embed(reqs.map(r => r.text), {});
  for (let i = 0; i < reqs.length; i++) {
    const r = reqs[i];
    const cands = topK(vecs[i], cvIndex, K);
    console.log(`\n[${r.strength}] ${r.text.slice(0, 95)}`);
    if (r.strength === 'Gap') console.log(`   shown to model: (hidden — Gap)`);
    cands.forEach((c, j) => {
      const inTop3 = j < 3 ? ' ' : '*';
      console.log(`  ${inTop3}${j < 3 ? 'ABC'[j] : String(j + 1).padStart(1)}) ${c.sim.toFixed(3)} ${c.text.slice(0, 150).replace(/\n/g, ' ')}`);
    });
  }
}
console.log('\n  (* = beyond the topK=3 the model was actually shown)');
