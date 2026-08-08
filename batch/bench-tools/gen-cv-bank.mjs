#!/usr/bin/env node
// @ts-check
/**
 * gen-cv-bank.mjs — author `batch/cv-bank.json`, the pre-written variant bank.
 *
 * Phase 3's writer has always been a 7B rewriting every bullet at runtime, with
 * eighteen guard functions downstream undoing the damage. Measured on the control
 * run, 78 % of shipped experience bullets end up byte-identical to cv.md anyway:
 * the guards revert three rewrites in four, so the pipeline pays for a generation
 * call whose output is mostly discarded, and the 22 % that survives is exactly the
 * part every fabrication guard exists to police.
 *
 * The bank moves that work offline. A frontier model writes several phrasings of
 * each CV bullet ONCE, each verified here against its source, and the runtime
 * only picks between them. Fabrication stops being a thing to detect and repair
 * on every offer and becomes a thing to reject once, at authoring time, where a
 * human can read the rejects.
 *
 * The verifier is the load-bearing half. Nothing a model returns is trusted:
 *
 *   figures   every number the source states must survive verbatim. This is the
 *             44 %-truncation defect that no metric could see, made structurally
 *             impossible instead of detected after the fact.
 *   vocabulary  no proper noun that is not in the source bullet or its entry
 *             head. That is `stripFabricatedProducts` moved to authoring time.
 *   overlap   at least 60 % of the source's tokens. Partly a claim guard — a
 *             variant sharing less than that is writing something new — and
 *             partly a measurement one: every metric traces output back to a
 *             cv.md atom by token overlap, so a variant that drifts too far
 *             would read as a *lost* atom and score the bank as worse than it is.
 *   length    18-42 words, the band that renders as one or two lines.
 *
 *   node batch/bench-tools/gen-cv-bank.mjs [--out batch/cv-bank.json] [--entry N]
 *   node batch/bench-tools/gen-cv-bank.mjs --verify-only
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { execFile } from 'child_process';
import { parseCvSections, parseEntries } from '../cv-select.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '../..');

const arg = (name, def) => {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 ? process.argv[i + 1] : def;
};
const has = (name) => process.argv.includes(`--${name}`);

// ── the CV, as entries ────────────────────────────────────────────────────────

/** Experience and project entries, with the head lines that name their stack. */
export function cvEntries(cvText) {
  const out = [];
  for (const sec of parseCvSections(cvText)) {
    if (sec.name !== 'Experience' && sec.name !== 'Projects') continue;
    for (const e of parseEntries(sec.lines).entries) {
      out.push({
        section: sec.name,
        name: e.head[0].replace(/^###\s+/, '').trim(),
        head: e.head.slice(1).join(' ').trim(),
        bullets: e.bullets,
      });
    }
  }
  return out;
}

// ── the verifier ──────────────────────────────────────────────────────────────

const NUM = /\d[\d,.]*\+?%?/g;
/** Figures as normalised strings; "170" and "170+" stay distinguishable. */
export function figuresOf(s) {
  return new Set((String(s).match(NUM) || []).map(x => x.replace(/[.,]$/, '')).filter(x => x.length > 1));
}

const WORD = /[a-z0-9+#.]{3,}/g;
const toks = (s) => new Set((String(s).toLowerCase().match(WORD) || []));

/**
 * Capitalised words and acronyms — the surface a fabricated product shows up on.
 * Sentence-initial words are excluded: every bullet starts with a capital, and
 * "Delivered" is not a proper noun.
 */
export function properNouns(s) {
  const words = String(s).split(/\s+/);
  const out = new Set();
  words.forEach((w, i) => {
    // The trailing class keeps `.` so that `Next.js` and `Node.js` survive, which
    // also welds a sentence-final period on: "across UK." reads as the name `uk.`
    // and matches the source's `uk` nowhere. Strip the edge dots separately.
    const c = w.replace(/^[^A-Za-z0-9]+|[^A-Za-z0-9+#.]+$/g, '').replace(/^\.+|\.+$/g, '');
    if (!c || c.length < 2) return;
    if (i === 0 && !/[A-Z]{2,}/.test(c)) return;       // sentence-initial, not an acronym
    if (/^[A-Z]/.test(c) || /^[A-Z0-9.+-]{2,}$/.test(c)) out.add(c.toLowerCase());
  });
  return out;
}

/**
 * Reject a variant, or return []. `allowed` is the source bullet plus its entry
 * head — the head is where a project's stack is listed, so a variant naming
 * "Axum" for a bullet whose head says `Rust, Flutter, WebRTC, Axum` is grounded.
 *
 * @param {string} variant
 * @param {string} source
 * @param {string} allowedContext
 */
export function verifyVariant(variant, source, allowedContext) {
  const errs = [];
  const v = String(variant || '').trim();
  if (!v) return ['empty'];

  // Relative to the source, not absolute. This CV's bullets run 18 to 68 words,
  // and a fixed 18-42 band rejected every variant of the six longest ones — the
  // detailed, mechanism-carrying bullets that are the most worth re-angling. The
  // floor stops a variant compressing a bullet down to its topic sentence, which
  // is the 7B's characteristic failure and the reason the bank exists.
  const srcWords = String(source).trim().split(/\s+/).filter(Boolean).length;
  const lo = Math.max(15, Math.round(srcWords * 0.65));
  const hi = Math.min(70, Math.round(srcWords * 1.15));
  const words = v.split(/\s+/).filter(Boolean).length;
  if (words < lo || words > hi) errs.push(`${words} words (want ${lo}-${hi} for a ${srcWords}-word source)`);

  const src = figuresOf(source), got = figuresOf(v);
  const lost = [...src].filter(f => !got.has(f));
  if (lost.length) errs.push(`dropped figures: ${lost.join(', ')}`);
  const invented = [...got].filter(f => !src.has(f));
  if (invented.length) errs.push(`invented figures: ${invented.join(', ')}`);

  const allow = properNouns(`${source} ${allowedContext}`);
  const bad = [...properNouns(v)].filter(n => !allow.has(n));
  if (bad.length) errs.push(`ungrounded names: ${bad.join(', ')}`);

  const st = toks(source);
  let shared = 0;
  for (const t of st) if (toks(v).has(t)) shared++;
  const ov = st.size ? shared / st.size : 1;
  if (ov < 0.6) errs.push(`only ${(ov * 100).toFixed(0)}% of source tokens (want >=60%)`);

  return errs;
}

// ── authoring ─────────────────────────────────────────────────────────────────

const SYSTEM = `You rewrite CV bullets. You output JSON only — no prose, no markdown fences.

You are given one entry from a candidate's CV (a job or a project) and its bullets. For each bullet you produce alternative phrasings. The candidate applies to many different postings; a phrasing that leads with the distributed-systems angle serves a platform role, and the same facts led by the cryptography angle serve a security role. At runtime one phrasing is chosen per posting. Your job is to make sure a good one exists for each angle the bullet can honestly serve.

ABSOLUTE RULES — a variant breaking any of these is discarded:

1. Every number in the source must appear in every variant, unchanged. "800+" stays "800+", not "800" or "many". "2+ hours to 30 minutes" keeps both halves. Dropping a quantified outcome is the single most damaging thing you can do here.
2. Never name a technology, product, company, standard or tool that is not already named in the source bullet or in the entry's header line. You may drop names; you may never add one.
3. Never add a claim. No new outcomes, no new scale, no new responsibilities, no adjectives asserting quality that the source does not assert. You are re-emphasising facts, not selling them.
4. Keep at least 60% of the source's distinctive words. A variant is a re-angling, not a fresh sentence.
5. Stay close to the source's length — between 65% and 115% of its word count. A long, detailed bullet stays long; its detail is the value. Do not compress a bullet down to its topic sentence.

WHAT MAKES A GOOD VARIANT:

Lead with what the target reader cares about. The source often buries the strongest fact mid-sentence; a variant may promote it to the front. Prefer a concrete verb over "Responsible for" or "Worked on". Keep the mechanism — "a lock-free, pre-allocated frame ring with atomic CAS slot ownership" is why the bullet is impressive, and compressing it to "optimised the video path" destroys the bullet even though it is technically true.

Angles, use the ones this bullet can honestly serve: backend, distributed-systems, security, performance, infrastructure, data, ml-ai, frontend, leadership, product.

Produce 3 variants per bullet, each a different angle. If a bullet can only honestly serve one or two angles, produce fewer rather than inventing an angle it does not support.`;

const userPrompt = (entry) => `# CV entry

Section: ${entry.section}
Name: ${entry.name}
Header: ${entry.head}

# Bullets

${entry.bullets.map((b, i) => `${i + 1}. ${b}`).join('\n\n')}

# Output

{
  "bullets": [
    {
      "id": 1,
      "variants": [
        {"angle": "one of the listed angles", "text": "the rephrased bullet"}
      ]
    }
  ]
}

One object per bullet, ids 1-${entry.bullets.length}, in order.`;

function callClaude(prompt, model) {
  return new Promise((res, rej) => {
    const child = execFile('claude', [
      '-p', prompt, '--output-format', 'json',
      '--system-prompt', SYSTEM, '--allowedTools', '', '--model', model,
    ], { maxBuffer: 32 * 1024 * 1024, timeout: 900_000 }, (err, stdout) => {
      if (err && !stdout) return rej(err);
      try {
        const env = JSON.parse(stdout);
        if (env.is_error) return rej(new Error(String(env.result).slice(0, 300)));
        let t = String(env.result || '').trim();
        const fence = t.match(/```(?:json)?\s*([\s\S]*?)```/);
        if (fence) t = fence[1].trim();
        const a = t.indexOf('{'), b = t.lastIndexOf('}');
        res({ data: JSON.parse(t.slice(a, b + 1)), cost: env.total_cost_usd || 0 });
      } catch (e) { rej(new Error(String(e.message).slice(0, 200))); }
    });
    child.stdin?.end();
  });
}

// ── main ──────────────────────────────────────────────────────────────────────

// The verifier is the reusable half of this file, so importing it must not spend
// money — without this guard `import { verifyVariant }` starts authoring the bank.
const isMain = process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));
if (!isMain) {
  // imported for the verifier: run nothing
} else {

const outPath = resolve(PROJECT, String(arg('out', 'batch/cv-bank.json')));
const model = String(arg('model', 'opus'));
const cvText = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
const entries = cvEntries(cvText);

if (has('verify-only')) {
  const bank = JSON.parse(readFileSync(outPath, 'utf8'));
  let bad = 0, tot = 0;
  for (const a of bank.atoms) for (const v of a.variants) {
    tot++;
    const errs = verifyVariant(v.text, a.source, a.context);
    if (errs.length) { bad++; console.log(`REJECT ${a.id} [${v.angle}]: ${errs.join('; ')}\n  ${v.text}`); }
  }
  console.log(`\n${tot - bad}/${tot} variants pass`);
  process.exit(bad ? 1 : 0);
}

const only = arg('entry', '');
let spent = 0;
const atoms = [];
let kept = 0, rejected = 0;

for (const [ei, entry] of entries.entries()) {
  if (only && String(ei) !== only) continue;
  process.stderr.write(`\n[${ei + 1}/${entries.length}] ${entry.name} (${entry.bullets.length} bullets)\n`);
  let data;
  try {
    const r = await callClaude(userPrompt(entry), model);
    data = r.data; spent += r.cost;
  } catch (e) {
    process.stderr.write(`  FAILED: ${String(e.message).slice(0, 200)}\n`);
    continue;
  }

  for (const [bi, source] of entry.bullets.entries()) {
    const got = (data.bullets || []).find(b => Number(b.id) === bi + 1);
    const context = entry.head;
    // The source is always variant 0 and is never subject to the verifier: it is
    // cv.md itself, so "does it invent a figure" is not a question that can have
    // a yes. This is also what guarantees the bank can never be worse than
    // verbatim — the shipped text is always available to pick.
    const variants = [{ angle: 'source', text: source }];
    for (const v of (got?.variants || [])) {
      const errs = verifyVariant(v.text, source, context);
      if (errs.length) {
        rejected++;
        process.stderr.write(`  reject [${v.angle}] ${errs.join('; ')}\n`);
        continue;
      }
      kept++;
      variants.push({ angle: String(v.angle || 'unknown'), text: String(v.text).trim() });
    }
    atoms.push({
      id: `${entry.section === 'Experience' ? 'exp' : 'proj'}/${entry.name}/${bi + 1}`,
      section: entry.section, entity: entry.name, context, source, variants,
    });
    process.stderr.write(`  ${bi + 1}. ${variants.length - 1} variants: ${variants.slice(1).map(v => v.angle).join(', ') || '(none)'}\n`);
  }
}

mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, JSON.stringify({
  version: 1, model, at: new Date().toISOString(),
  // cv.md is the source of truth and the bank is derived from it; a bank built
  // against a different cv.md is stale, and the runtime refuses it rather than
  // shipping variants of bullets the CV no longer contains.
  cv_sha1: (await import('crypto')).createHash('sha1').update(cvText).digest('hex'),
  atoms,
}, null, 2), 'utf8');

process.stderr.write(`\nwrote ${atoms.length} atoms, ${kept} variants kept, ${rejected} rejected, $${spent.toFixed(2)}\n`);

}
