// @ts-check
/**
 * cv-bank.mjs — pick a pre-written phrasing instead of generating one.
 *
 * `batch/cv-bank.json` holds, for every cv.md bullet, the source text plus
 * several alternative phrasings that a frontier model wrote offline and
 * `gen-cv-bank.mjs` verified: same figures, no name the source lacks, no new
 * claim, ≥60 % of the source's tokens. Choosing between them is a cosine, so
 * Phase 3's writer stops being a model call.
 *
 * The property that makes this worth doing is not speed. It is that the bank
 * cannot fabricate. Every guard in local-pdf-offer.mjs — the figure reverts, the
 * product strips, the clause surgery — is a runtime detector for damage a 7B does
 * to a bullet it was handed. A variant that would trip any of them was rejected
 * at authoring time, by a check a human can read the output of, once, instead of
 * on every offer forever.
 *
 * The source is always variant 0 and always eligible, so the bank's floor is
 * exactly the verbatim writer: it can only deviate where a variant scores higher
 * against this posting's requirements.
 *
 * Self-check: node batch/cv-bank.mjs
 */

import { readFileSync, existsSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { createHash } from 'crypto';
import { embed, cosine } from './embeddings.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const BANK_PATH = resolve(__dirname, 'cv-bank.json');

/**
 * Load the bank, or null.
 *
 * A bank authored against a different cv.md is refused outright rather than used
 * partially. Its variants are phrasings of bullets that may no longer exist, and
 * a half-stale bank fails silently — the same reasoning that makes cv-select's
 * judge return null on a missing exemplar file instead of running 0-shot, where
 * it scored worse than no rerank at all.
 *
 * @param {string} cvText
 * @param {string} [path]
 * @returns {{atoms: any[], cv_sha1: string}|null}
 */
export function loadBank(cvText, path = BANK_PATH) {
  if (!existsSync(path)) return null;
  try {
    const bank = JSON.parse(readFileSync(path, 'utf8'));
    if (!Array.isArray(bank.atoms) || !bank.atoms.length) return null;
    const hash = createHash('sha1').update(cvText).digest('hex');
    if (bank.cv_sha1 && bank.cv_sha1 !== hash) {
      process.stderr.write('cv-bank: built against a different cv.md — ignoring it\n');
      return null;
    }
    return bank;
  } catch { return null; }
}

const norm = (s) => String(s || '').replace(/\s+/g, ' ').trim();

/**
 * Choose one phrasing per bullet, for the bullets this offer actually selected.
 *
 * `margin` is the source's head start. The whole codebase has learned this once
 * already: the summary stage ships its challenger only by a clear margin,
 * because a bare `>` handed the slot away on a rounding difference. Default 0
 * keeps it a pure argmax so the knob can be swept rather than assumed.
 *
 * @param {string[]} bulletTexts the bullets that will appear, as cv.md words them
 * @param {string[]} requirements Block B requirements for this posting
 * @param {{atoms: any[]}} bank
 * @param {{ollamaUrl?: string, margin?: number, _embed?: Function}} [opts]
 * @returns {Promise<Map<string, {text: string, angle: string, gain: number}>>}
 */
export async function chooseVariants(bulletTexts, requirements, bank, opts = {}) {
  const { margin = 0, _embed = embed } = opts;
  const picks = new Map();
  if (!requirements?.length || !bulletTexts?.length) return picks;

  const bySource = new Map(bank.atoms.map(a => [norm(a.source), a]));
  const wanted = bulletTexts
    .map(b => ({ bullet: b, atom: bySource.get(norm(b)) }))
    .filter(x => x.atom && x.atom.variants.length > 1);
  if (!wanted.length) return picks;

  // One embed call for everything: the requirements and every candidate phrasing
  // of every selected bullet.
  const texts = [...requirements];
  const offsets = [];
  for (const w of wanted) {
    offsets.push({ ...w, at: texts.length });
    for (const v of w.atom.variants) texts.push(v.text);
  }
  const vecs = await _embed(texts, opts);
  const qv = vecs.slice(0, requirements.length);
  const best = (v) => Math.max(...qv.map(q => cosine(q, v)));

  for (const w of offsets) {
    const scores = w.atom.variants.map((v, i) => ({ v, s: best(vecs[w.at + i]) }));
    const source = scores[0];
    let win = source;
    for (const c of scores.slice(1)) if (c.s > win.s && c.s > source.s + margin) win = c;
    if (win !== source) {
      picks.set(w.bullet, { text: win.v.text, angle: win.v.angle, gain: +(win.s - source.s).toFixed(4) });
    }
  }
  return picks;
}

/**
 * Apply the chosen phrasings to a content object's experience and projects.
 *
 * Projects are matched by clause rather than whole blurb: `padProjectDescriptions`
 * builds a description by joining clauses from several bullets, so a project's
 * text contains a bullet's words without ever equalling it.
 *
 * @param {any} content
 * @param {Map<string, {text: string}>} picks
 */
export function applyPicks(content, picks) {
  for (const e of (content.experience || [])) {
    e.bullets = (e.bullets || []).map(b => picks.get(b)?.text ?? b);
  }
  return content;
}

// ── self-check ────────────────────────────────────────────────────────────────

if (process.argv[1] && process.argv[1].endsWith('cv-bank.mjs')) {
  const assert = (await import('assert')).default;

  const bank = { atoms: [
    { id: 'a', source: 'Built a Rust service handling 63,000 benchmark runs across 7 schemes',
      variants: [
        { angle: 'source', text: 'Built a Rust service handling 63,000 benchmark runs across 7 schemes' },
        { angle: 'security', text: 'Built a Rust cryptographic service running 63,000 signature benchmarks across 7 schemes' },
      ] },
    { id: 'b', source: 'A bullet with no variants at all',
      variants: [{ angle: 'source', text: 'A bullet with no variants at all' }] },
  ] };

  // A stub embedder: vectors are keyed off whether the text mentions cryptography,
  // so the security variant is the argmax for a cryptography requirement.
  const fake = async (texts) => texts.map(t => /crypto|signature/i.test(t) ? [1, 0] : [0, 1]);

  const picks = await chooseVariants(
    ['Built a Rust service handling 63,000 benchmark runs across 7 schemes', 'A bullet with no variants at all'],
    ['cryptographic signature work'], bank, { _embed: fake });

  assert.equal(picks.size, 1, 'only the atom with a real alternative is re-phrased');
  assert.match(picks.get('Built a Rust service handling 63,000 benchmark runs across 7 schemes').text, /cryptographic/);

  // The source must hold when nothing scores better — the bank's floor is verbatim.
  const flat = async (texts) => texts.map(() => [1, 0]);
  const none = await chooseVariants(['Built a Rust service handling 63,000 benchmark runs across 7 schemes'],
    ['anything'], bank, { _embed: flat });
  assert.equal(none.size, 0, 'a tie leaves the source in place');

  // ...and a margin must be able to hold it against a small win.
  const held = await chooseVariants(['Built a Rust service handling 63,000 benchmark runs across 7 schemes'],
    ['cryptographic signature work'], bank, { _embed: fake, margin: 5 });
  assert.equal(held.size, 0, 'margin keeps the source');

  const content = applyPicks({ experience: [{ bullets: ['Built a Rust service handling 63,000 benchmark runs across 7 schemes'] }] }, picks);
  assert.match(content.experience[0].bullets[0], /cryptographic/, 'picks are applied');

  // A bank built against a different cv.md must disable itself, not part-apply.
  const tmp = resolve((await import('os')).tmpdir(), `bank-${process.pid}.json`);
  (await import('fs')).writeFileSync(tmp, JSON.stringify({ atoms: bank.atoms, cv_sha1: 'deadbeef' }));
  assert.equal(loadBank('some other cv', tmp), null, 'stale bank refused');

  console.log('cv-bank self-check OK');
}
