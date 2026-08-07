#!/usr/bin/env node
// @ts-check
/**
 * opus-label.mjs — a frontier-model gold standard for Phase 3, at scale.
 *
 * Every label the Phase 3 work has run on came from one of two places: a human
 * ticking a 12-offer sheet, or the local 30B grading itself. The first is
 * accurate and tiny — the retrieval ledger's own power table says +0.03 needs
 * 137 offers and the sheets have 12 — and the second was pre-registered at
 * >=0.764 agreement, scored 0.739, and was rejected as an oracle for exactly
 * that reason.
 *
 * This asks a model that is not in the pipeline. It runs `claude -p` once per
 * offer and writes one JSON file of labels, so the whole corpus is produced once
 * and every later benchmark reads it for free.
 *
 * What it labels, and why each field exists:
 *
 *   grades          0-3 per CV atom — the same scale cv-select's judge uses, so
 *                   the two are directly comparable on pair accuracy.
 *   differentiators the point of the whole exercise. A bullet can be a genuine
 *                   3 and still be generic ("built REST APIs"): every applicant
 *                   for the posting has one. A differentiator is relevant AND
 *                   distinguishing, and losing one is the failure the user
 *                   actually cares about. Nothing in the existing harness can
 *                   see it — every shipped metric punishes falsity, so an empty
 *                   CV scores perfectly on all of them.
 *   noise           the other half: CV content that would read as padding on
 *                   THIS posting. Gives `noise_rate` a ground truth instead of
 *                   a heuristic.
 *
 * The per-grade `why` is not decoration. The retrieval ledger measured that
 * dropping the local judge's per-item `id` field — pure bookkeeping, the order
 * being fixed by the prompt — cost 0.052 pair accuracy. Making the model write
 * something per item is what keeps the grading deliberate.
 *
 *   node batch/bench-tools/opus-label.mjs --sample sample128.tsv [--concurrency 5]
 *   node batch/bench-tools/opus-label.mjs --sample sample128.tsv --limit 4 --dry-run
 *
 * Resumable: an offer whose label file already parses is skipped, so a killed
 * run costs only what it had not finished.
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { execFile } from 'child_process';
import { selectableAtoms } from '../tailor-harness.mjs';
import { cleanJd } from '../text-utils.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '../..');
const OUT_DIR = resolve(PROJECT, 'batch/bench/opus/labels');

const arg = (name, def) => {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 ? process.argv[i + 1] : def;
};
const has = (name) => process.argv.includes(`--${name}`);

// ── the prompt ────────────────────────────────────────────────────────────────

const SYSTEM = `You are a hiring-side technical reviewer building ground truth for a CV-tailoring benchmark. You output JSON only — no prose, no markdown fences, no commentary.

You are given one job posting and the complete master CV of one candidate, broken into numbered atoms (individual bullets from their jobs and projects). A tailoring pipeline must choose which atoms to put on a two-page CV for this posting. Your labels are the reference that pipeline is scored against, so they must reflect what a competent hiring manager for THIS posting would actually want to read.

Grade every atom 0-3:
  3 - directly evidences a stated requirement; the CV is materially worse without it
  2 - clearly relevant; earns a slot if there is room
  1 - tangential; only if nothing better competes
  0 - irrelevant to this posting

Grade on what the atom DEMONSTRATES, not keyword overlap. A Rust project evidences systems engineering for a C++ posting. A payments integration evidences third-party API work for a fintech posting. Conversely, matching a keyword inside an unrelated accomplishment is not evidence.

Use the full range. A grading where most atoms are 2 or 3 is not a grading — it is a refusal to choose. Most CVs contain genuinely irrelevant material for any given posting.

Then answer two questions the grades alone cannot:

DIFFERENTIATORS (at most 6, may be fewer, may be empty). Which atoms would make a hiring manager pick THIS candidate over another applicant who has the same job title and a superficially similar CV? A differentiator is relevant AND rare. "Built REST APIs with Spring Boot" can be a legitimate 3 and still not differentiate, because every applicant has it. "Eliminated an 8 MB-per-frame copy with a lock-free ring and atomic CAS slot ownership" differentiates, because almost nobody has it. Judge rarity against the realistic applicant pool for this specific posting and seniority — a technique that is exotic for a graduate scheme is unremarkable for a systems role.

NOISE. Which atoms, if they appeared on the CV sent to this posting, would read as padding and cost the candidate credibility? These are usually the 0s, but not always: a 1 that eats a slot on a crowded page is noise, and an impressive-but-off-topic item can actively hurt by suggesting the candidate did not read the posting.

Judge only against what the posting asks for and the CV states. Never reward or invent content the CV does not contain.`;

/**
 * The atom list, numbered, with the entry each belongs to so the model can see
 * that six bullets are one project rather than six unrelated claims.
 * @param {{text: string, section: string, entity: string}[]} atoms
 */
function atomBlock(atoms) {
  const lines = [];
  let last = '';
  atoms.forEach((a, i) => {
    if (a.entity !== last) { lines.push(`\n[${a.section}] ${a.entity}`); last = a.entity; }
    lines.push(`  ${i + 1}. ${a.text}`);
  });
  return lines.join('\n').trim();
}

const userPrompt = (offer, jd, atoms, cvText) => `# Job posting

Company: ${offer.company}
Role: ${offer.role}

${jd}

# Candidate master CV

${cvText}

# The ${atoms.length} selectable atoms, numbered

${atomBlock(atoms)}

# Output

Return exactly this JSON shape and nothing else:

{
  "role_family": "one of: backend | security | fullstack | platform-infra | data | embedded | ml-ai | other",
  "seniority": "one of: intern | graduate | junior | mid | senior",
  "key_requirements": ["5 to 8 short phrases naming what this posting actually needs"],
  "grades": [{"id": 1, "grade": 0, "why": "at most 8 words"}, ... one entry for EVERY atom 1-${atoms.length}],
  "differentiators": [ids of at most 6 atoms that are relevant AND rare for this posting],
  "noise": [ids of atoms that would read as padding on this posting],
  "summary_angle": "one sentence: what the tailored CV's summary should lead with, given this posting and this candidate"
}`;

// ── running claude -p ─────────────────────────────────────────────────────────

/**
 * One label call. Returns parsed JSON or throws.
 *
 * `--allowedTools ""` matters beyond tidiness: with tools available the model
 * may go and read the repo, and a label that depended on files outside the
 * prompt is not reproducible from the prompt.
 * @param {string} prompt
 */
function callClaude(prompt, model) {
  return new Promise((res, rej) => {
    const child = execFile('claude', [
      '-p', prompt,
      '--output-format', 'json',
      '--system-prompt', SYSTEM,
      '--allowedTools', '',
      '--model', model,
    ], { maxBuffer: 32 * 1024 * 1024, timeout: 600_000 }, (err, stdout) => {
      if (err && !stdout) return rej(err);
      try {
        const env = JSON.parse(stdout);
        if (env.is_error) return rej(new Error(String(env.result).slice(0, 300)));
        let t = String(env.result || '').trim();
        const fence = t.match(/```(?:json)?\s*([\s\S]*?)```/);
        if (fence) t = fence[1].trim();
        const a = t.indexOf('{'), b = t.lastIndexOf('}');
        if (a === -1 || b === -1) return rej(new Error(`no JSON in result: ${t.slice(0, 200)}`));
        res({ label: JSON.parse(t.slice(a, b + 1)), cost: env.total_cost_usd || 0 });
      } catch (e) {
        rej(new Error(`unparseable envelope: ${String(e.message).slice(0, 200)}`));
      }
    });
    child.stdin?.end();
  });
}

/**
 * Reject a label that is malformed or degenerate.
 *
 * The "most atoms are 2 or 3" failure is the one the local judge fell into
 * 0-shot, where it scored 0.670 against plain cosine's 0.756 — worse than not
 * grading at all. A label set with no 0s has not chosen anything, and silently
 * accepting it would put that same failure into the ground truth.
 */
function validate(label, n) {
  const errs = [];
  const g = Array.isArray(label?.grades) ? label.grades : [];
  const ids = new Set(g.map(x => Number(x.id)));
  if (g.length !== n || ids.size !== n) errs.push(`expected ${n} unique grades, got ${ids.size}`);
  for (const x of g) if (!(Number(x.grade) >= 0 && Number(x.grade) <= 3)) errs.push(`grade out of range for id ${x.id}`);
  const vals = g.map(x => Number(x.grade));
  if (vals.length && !vals.some(v => v === 0)) errs.push('no atom graded 0 — the full range was not used');
  if (!Array.isArray(label?.differentiators)) errs.push('differentiators missing');
  else if (label.differentiators.length > 6) errs.push('more than 6 differentiators');
  if (!Array.isArray(label?.noise)) errs.push('noise missing');
  if (!Array.isArray(label?.key_requirements) || label.key_requirements.length < 3) errs.push('key_requirements too short');
  return errs.slice(0, 4);
}

// ── main ──────────────────────────────────────────────────────────────────────

const samplePath = resolve(PROJECT, 'batch/bench/tailor', String(arg('sample', 'sample128.tsv')));
const concurrency = parseInt(String(arg('concurrency', '5')), 10);
const limit = parseInt(String(arg('limit', '0')), 10);
const model = String(arg('model', 'opus'));
const dryRun = has('dry-run');

const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
const atoms = selectableAtoms(cvText);

const rows = readFileSync(samplePath, 'utf8').trim().split('\n').slice(1).map(l => {
  const [id, reportNum, report, jd, company, role, score] = l.split('\t');
  return { id, reportNum, report, jd, company, role, score: parseFloat(score) };
});
const work = (limit ? rows.slice(0, limit) : rows);

mkdirSync(OUT_DIR, { recursive: true });
const done = new Set(readdirSync(OUT_DIR).filter(f => f.endsWith('.json')).map(f => f.replace('.json', '')));
const todo = work.filter(o => !done.has(o.id));

console.error(`${atoms.length} atoms · ${work.length} offers · ${done.size} already labelled · ${todo.length} to do · model=${model}`);

if (dryRun) {
  const o = todo[0] || work[0];
  const jd = cleanJd(readFileSync(resolve(PROJECT, o.jd), 'utf8'), 6000);
  const p = userPrompt(o, jd, atoms, cvText);
  console.log(`--- SYSTEM (${SYSTEM.length} chars) ---\n${SYSTEM}\n`);
  console.log(`--- USER (${p.length} chars, ~${Math.round(p.length / 4)} tokens) ---\n${p.slice(0, 3000)}\n[...]`);
  process.exit(0);
}

let spent = 0, ok = 0, bad = 0;
let cursor = 0;

async function worker(w) {
  while (cursor < todo.length) {
    const o = todo[cursor++];
    const n = cursor;
    try {
      const jd = cleanJd(readFileSync(resolve(PROJECT, o.jd), 'utf8'), 6000);
      const { label, cost } = await callClaude(userPrompt(o, jd, atoms, cvText), model);
      spent += cost;
      const errs = validate(label, atoms.length);
      if (errs.length) {
        bad++;
        console.error(`[${n}/${todo.length}] w${w} #${o.id} REJECTED: ${errs.join('; ')}`);
        continue;
      }
      // The atom text is stored beside the labels on purpose. The ids are
      // positional, so a later edit to cv.md silently repoints every one of
      // them; keeping the text is what lets a reader detect that instead of
      // scoring against labels for bullets that no longer exist.
      writeFileSync(resolve(OUT_DIR, `${o.id}.json`), JSON.stringify({
        offer: { id: o.id, company: o.company, role: o.role, score: o.score, report: o.report },
        atoms: atoms.map((a, i) => ({ id: i + 1, section: a.section, entity: a.entity, text: a.text })),
        model, at: new Date().toISOString(), cost,
        ...label,
      }, null, 2), 'utf8');
      ok++;
      const d = label.grades.filter(x => Number(x.grade) === 3).length;
      console.error(`[${n}/${todo.length}] w${w} #${o.id} ${o.company} — ${d}x3, ${label.differentiators.length} diff, ${label.noise.length} noise · $${spent.toFixed(2)}`);
    } catch (e) {
      bad++;
      console.error(`[${n}/${todo.length}] w${w} #${o.id} FAILED: ${String(e.message).slice(0, 200)}`);
    }
  }
}

await Promise.all(Array.from({ length: Math.min(concurrency, todo.length) }, (_, i) => worker(i + 1)));
console.error(`\ndone: ${ok} labelled, ${bad} failed, $${spent.toFixed(2)} spent`);
