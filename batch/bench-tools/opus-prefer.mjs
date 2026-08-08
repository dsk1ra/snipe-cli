#!/usr/bin/env node
// @ts-check
/**
 * opus-prefer.mjs — blind pairwise preference between two Phase 3 runs.
 *
 * Every other metric in this repo is label-free or label-scored, and both kinds
 * answer questions about *content*: which atoms shipped, whether a figure
 * survived, how much of the posting's vocabulary appears. None of them can see
 * whether a CV reads well, and for one change that turned out to be the whole
 * question.
 *
 * The variant bank (`cv-bank.json`) re-phrases bullets under a verifier that
 * forbids introducing any word the source lacks. Measured against the label
 * metrics it is exactly a null: differentiator_coverage, noise_rate, grade_yield
 * and mean_grade are identical to three decimals, because the atoms that ship are
 * the same atoms — only their wording changed. ats_coverage moves +0.002
 * (p=0.58), which is what a token-overlap metric must report about a change that
 * cannot add tokens. So either the bank is worthless, or the metrics cannot see
 * its value. Nothing already in the harness can tell those apart.
 *
 * This does, by asking a reviewer to choose. Blind (the runs are labelled A and
 * B), order-randomised per offer against a fixed seed so a position bias shows up
 * as a draw rather than as a win, and ties are allowed so the judge is not forced
 * to invent a preference.
 *
 *   node batch/bench-tools/opus-prefer.mjs --a vbp2 --b bank2 [--limit 16]
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { execFile } from 'child_process';
import { createHash } from 'crypto';
import { cleanJd } from '../text-utils.mjs';
import { signTest } from '../stats.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '../..');
const BENCH = resolve(PROJECT, 'batch/bench/tailor');
const OUT = resolve(PROJECT, 'batch/bench/opus/prefer');

const arg = (n, d) => { const i = process.argv.indexOf(`--${n}`); return i >= 0 ? process.argv[i + 1] : d; };

const SYSTEM = `You are a hiring manager choosing between two versions of the same candidate's CV for one specific job posting. Both contain the same underlying facts about the same person; they differ only in how bullets are worded and ordered.

You output JSON only.

Judge on what would actually make you more likely to interview this person for THIS posting:
- Does the bullet lead with the thing that matters for this role, or bury it?
- Is the technical mechanism legible, or compressed into a vague claim?
- Does it read like a person who did the work, or like keyword padding?

Both versions are truthful. Do not reward one for containing a fact the other lacks unless it genuinely does. Do not reward length. Do not reward jargon density.

If they are genuinely equivalent, say "tie" — a forced preference between two near-identical documents is noise, and noise is what this measurement is trying to avoid.`;

/** Render a cv-content.json as the plain text a reader would see. */
function render(c) {
  const out = [`SUMMARY\n${c.summary || ''}`];
  out.push('\nEXPERIENCE');
  for (const e of (c.experience || [])) {
    out.push(`\n${e.company}`);
    for (const b of (e.bullets || [])) out.push(`  - ${b}`);
  }
  out.push('\nPROJECTS');
  for (const p of (c.projects || [])) {
    out.push(`\n${p.name}`);
    if (p.bullets?.length) for (const b of p.bullets) out.push(`  - ${b}`);
    else out.push(`  ${p.description || ''}`);
  }
  return out.join('\n');
}

function callClaude(prompt, model) {
  return new Promise((res, rej) => {
    const child = execFile('claude', ['-p', prompt, '--output-format', 'json',
      '--system-prompt', SYSTEM, '--allowedTools', '', '--model', model],
      { maxBuffer: 32 * 1024 * 1024, timeout: 600_000 }, (err, stdout) => {
        if (err && !stdout) return rej(err);
        try {
          const env = JSON.parse(stdout);
          if (env.is_error) return rej(new Error(String(env.result).slice(0, 200)));
          let t = String(env.result || '').trim();
          const f = t.match(/```(?:json)?\s*([\s\S]*?)```/); if (f) t = f[1].trim();
          res({ v: JSON.parse(t.slice(t.indexOf('{'), t.lastIndexOf('}') + 1)), cost: env.total_cost_usd || 0 });
        } catch (e) { rej(new Error(String(e.message).slice(0, 200))); }
      });
    child.stdin?.end();
  });
}

const A = String(arg('a', 'vbp2')), B = String(arg('b', 'bank2'));
const model = String(arg('model', 'opus'));
const limit = parseInt(String(arg('limit', '0')), 10);
const conc = parseInt(String(arg('concurrency', '4')), 10);

const dirs = readdirSync(resolve(BENCH, A))
  .filter(d => existsSync(join(BENCH, A, d, 'cv-content.json'))
            && existsSync(join(BENCH, B, d, 'cv-content.json')));
const work = limit ? dirs.slice(0, limit) : dirs;
mkdirSync(OUT, { recursive: true });

console.error(`${work.length} offers · ${A} vs ${B} · model=${model}`);

let cursor = 0, spent = 0;
/** @type {{dir: string, winner: string, why: string}[]} */
const results = [];

async function worker() {
  while (cursor < work.length) {
    const d = work[cursor++];
    // Deterministic per-offer coin flip: the same offer always presents the same
    // way, so a rerun is comparable, but neither run is always "first".
    const flip = parseInt(createHash('sha1').update(d).digest('hex').slice(0, 8), 16) % 2 === 1;
    const [firstLabel, secondLabel] = flip ? [B, A] : [A, B];
    const first = JSON.parse(readFileSync(join(BENCH, firstLabel, d, 'cv-content.json'), 'utf8'));
    const second = JSON.parse(readFileSync(join(BENCH, secondLabel, d, 'cv-content.json'), 'utf8'));
    const jd = cleanJd(readFileSync(join(BENCH, A, d, 'job-description.txt'), 'utf8'), 5000);

    const prompt = `# The posting\n\n${jd}\n\n# Version A\n\n${render(first)}\n\n# Version B\n\n${render(second)}\n\n# Output\n\n{"winner": "A" | "B" | "tie", "why": "one sentence naming the concrete difference that decided it"}`;
    try {
      const { v, cost } = await callClaude(prompt, model);
      spent += cost;
      const w = v.winner === 'A' ? firstLabel : v.winner === 'B' ? secondLabel : 'tie';
      results.push({ dir: d, winner: w, why: String(v.why || '') });
      console.error(`[${results.length}/${work.length}] ${d.slice(0, 30)} → ${w} · $${spent.toFixed(2)}`);
    } catch (e) {
      console.error(`  ${d} FAILED: ${String(e.message).slice(0, 120)}`);
    }
  }
}
await Promise.all(Array.from({ length: Math.min(conc, work.length) }, worker));

const aWins = results.filter(r => r.winner === A).length;
const bWins = results.filter(r => r.winner === B).length;
const ties = results.filter(r => r.winner === 'tie').length;
// +1 per B win, -1 per A win, ties dropped as the sign test conventionally does.
const { p } = signTest([...Array(bWins).fill(1), ...Array(aWins).fill(-1)]);

writeFileSync(resolve(OUT, `${A}-vs-${B}.json`), JSON.stringify({ a: A, b: B, model, aWins, bWins, ties, p, results }, null, 2), 'utf8');
console.error(`\n${A}: ${aWins}   ${B}: ${bWins}   tie: ${ties}   sign-test p=${p.toFixed(3)}   $${spent.toFixed(2)}`);
