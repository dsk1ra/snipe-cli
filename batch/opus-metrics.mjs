// @ts-check
/**
 * opus-metrics.mjs — the two questions the label-free harness cannot ask.
 *
 * Every metric in tailor-harness.mjs punishes falsity: fabricated numbers,
 * invented products, copied examples, dropped figures. That set has a structural
 * blind spot its own ledger names — an empty CV asserts nothing, so it scores
 * perfectly on all of them. It took reading twelve real CVs to notice the 7B was
 * truncating away 44 % of the figures its sources stated, because no metric
 * could see a deletion.
 *
 * `ats_coverage` and `selection_regret` are the existing gestures at the other
 * side, and both are weaker than they look:
 *
 *   ats_coverage      counts JD *terms*, so a CV that name-drops "Kafka" scores
 *                     the same as one that shows what was built with it.
 *   selection_regret  scores the selection against an oracle built from the same
 *                     embedding model the selector uses. It cannot report that
 *                     the embeddings were wrong, only that the selector
 *                     disagreed with them. Circular by construction.
 *
 * These metrics score against `batch/bench/opus/labels/*.json` — a frontier model
 * that is not in the pipeline, grading every CV atom for every posting. That
 * breaks the circularity, and it makes the user's actual requirement measurable
 * for the first time:
 *
 *   differentiator_coverage  of the atoms that make this candidate distinct FOR
 *                            THIS POSTING, how many reached the CV. Losing one is
 *                            the failure that matters and nothing else sees it.
 *   noise_rate               of what shipped, how much the reviewer called
 *                            padding. The other half of the same question.
 *   grade_yield              graded worth of what shipped, over the best possible
 *                            pick of the same size. selection_regret with real
 *                            labels instead of the selector's own embeddings.
 *
 * Self-check: node batch/opus-metrics.mjs
 */

import { readFileSync, existsSync, readdirSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
export const LABELS_DIR = resolve(PROJECT, 'batch/bench/opus/labels');

const WORD = /[a-z0-9+#.]{3,}/g;
const toks = (s) => new Set((String(s).toLowerCase().match(WORD) || []));

/**
 * Asymmetric overlap, |a ∩ b| / |a| — the same measure `grounding` and
 * `shippedAtomIndices` already trust, so an atom is traced to output text the
 * same way everywhere and the metrics stay talking about one mapping.
 * @param {string} a @param {string} b
 */
export function overlap(a, b) {
  const A = toks(a), B = toks(b);
  if (!A.size) return 0;
  let n = 0;
  for (const t of A) if (B.has(t)) n++;
  return n / A.size;
}

/**
 * Load every label file, keyed by offer id.
 * @param {string} [dir]
 * @returns {Map<string, any>}
 */
export function loadLabels(dir = LABELS_DIR) {
  const out = new Map();
  if (!existsSync(dir)) return out;
  for (const f of readdirSync(dir)) {
    if (!f.endsWith('.json')) continue;
    try {
      const j = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      if (j?.offer?.id && Array.isArray(j.grades)) out.set(String(j.offer.id), j);
    } catch { /* a half-written label is not a label */ }
  }
  return out;
}

/**
 * Which labelled atoms a CV's output text covers.
 *
 * Direction matters and is the opposite of `shippedAtomIndices`: that asks "what
 * did this bullet come from", walking output → atom, and a CV with fewer bullets
 * than atoms therefore reports fewer matches whether or not the content survived.
 * This asks "did this atom survive", walking atom → output, so an atom merged
 * into another bullet or folded into a project blurb still counts. Measuring
 * retention with the first direction would score a truncation as a clean miss and
 * a merge as a loss.
 *
 * @param {{id: number, text: string}[]} atoms
 * @param {string[]} chunks every distinct piece of prose the CV shipped
 * @param {number} floor
 * @returns {Set<number>} atom ids present
 */
export function coveredAtoms(atoms, chunks, floor = 0.5) {
  const present = new Set();
  for (const a of atoms) {
    for (const c of chunks) {
      if (overlap(a.text, c) >= floor) { present.add(a.id); break; }
    }
  }
  return present;
}

/**
 * Every piece of prose a tailored CV shipped, as separate chunks.
 *
 * Project blurbs are included because 24 of this CV's 33 atoms are project
 * bullets: scoring only `experience[].bullets` would report that every project
 * differentiator was lost on every run.
 * @param {any} content a cv-content.json object
 */
export function outputChunks(content) {
  const out = [];
  for (const e of (content.experience || [])) for (const b of (e.bullets || [])) out.push(String(b));
  for (const p of (content.projects || [])) {
    out.push(`${p.name || ''} ${p.description || ''}`);
    // A project may ship as bullets rather than one blurb. Read both for every
    // run, so the metric asks the same question of an arm that has no bullets
    // (where this loop is a no-op) as of one that does.
    for (const b of (p.bullets || [])) out.push(String(b));
  }
  if (content.summary) out.push(String(content.summary));
  return out.filter(s => s.trim());
}

/**
 * Label-scored metrics for one offer.
 *
 * @param {any} label   one labels/<id>.json
 * @param {any} content that offer's cv-content.json
 */
export function scoreOffer(label, content) {
  const atoms = label.atoms;
  const gradeOf = new Map(label.grades.map(g => [Number(g.id), Number(g.grade)]));
  const chunks = outputChunks(content);
  const present = coveredAtoms(atoms, chunks);

  const diffs = (label.differentiators || []).map(Number);
  const noise = new Set((label.noise || []).map(Number));

  const keptDiffs = diffs.filter(id => present.has(id));
  const shipped = [...present];

  // Graded worth of what shipped, against the best possible pick of that size.
  // Capped at the same k so a CV is not punished for being short — that is
  // mean_bullets' job, and conflating the two would let "ship everything" win.
  const k = shipped.length;
  const all = [...gradeOf.values()].sort((a, b) => b - a);
  const oracle = all.slice(0, k).reduce((a, b) => a + b, 0);
  const got = shipped.reduce((a, id) => a + (gradeOf.get(id) ?? 0), 0);

  return {
    // The headline. undefined, not 0, when the reviewer named no differentiator
    // for this posting — averaging a 0 there would punish a CV for a question
    // that was never asked.
    differentiator_coverage: diffs.length ? keptDiffs.length / diffs.length : undefined,
    differentiators_lost: diffs.length - keptDiffs.length,
    lost_ids: diffs.filter(id => !present.has(id)),
    noise_rate: k ? shipped.filter(id => noise.has(id)).length / k : 0,
    grade_yield: oracle > 0 ? got / oracle : undefined,
    mean_grade: k ? got / k : 0,
    atoms_shipped: k,
  };
}

/**
 * Mean the per-offer scores, skipping undefined rather than counting them as 0.
 * @param {any[]} rows
 * @param {string} key
 */
export function meanOf(rows, key) {
  const vals = rows.map(r => r[key]).filter(v => typeof v === 'number' && Number.isFinite(v));
  return vals.length ? +(vals.reduce((a, b) => a + b, 0) / vals.length).toFixed(3) : null;
}

/**
 * Score a whole run directory against the labels.
 * @param {string} runDir  batch/bench/tailor/<label>
 * @param {Map<string, any>} labels
 */
export function scoreRun(runDir, labels) {
  const rows = [];
  for (const d of readdirSync(runDir)) {
    const f = join(runDir, d, 'cv-content.json');
    if (!existsSync(f)) continue;
    const id = d.split('_')[0];
    const label = labels.get(id);
    if (!label) continue;
    let content;
    try { content = JSON.parse(readFileSync(f, 'utf8')); } catch { continue; }
    rows.push({ dir: d, id, company: label.offer.company, ...scoreOffer(label, content) });
  }
  return {
    n: rows.length,
    differentiator_coverage: meanOf(rows, 'differentiator_coverage'),
    noise_rate: meanOf(rows, 'noise_rate'),
    grade_yield: meanOf(rows, 'grade_yield'),
    mean_grade: meanOf(rows, 'mean_grade'),
    atoms_shipped: meanOf(rows, 'atoms_shipped'),
    rows,
  };
}

// ── self-check ────────────────────────────────────────────────────────────────

if (process.argv[1] && process.argv[1].endsWith('opus-metrics.mjs')) {
  const assert = (await import('assert')).default;

  const label = {
    offer: { id: '1', company: 'Acme' },
    atoms: [
      { id: 1, text: 'Eliminated an 8 MB-per-frame copy with a lock-free ring and atomic CAS ownership' },
      { id: 2, text: 'Built REST endpoints with Spring Boot and PostgreSQL persistence' },
      { id: 3, text: 'Coordinated session cover and triage with the teaching assistant team' },
      { id: 4, text: 'Benchmarked post-quantum signatures across seven schemes with bootstrap intervals' },
    ],
    grades: [{ id: 1, grade: 3 }, { id: 2, grade: 2 }, { id: 3, grade: 0 }, { id: 4, grade: 3 }],
    differentiators: [1, 4],
    noise: [3],
  };

  // A CV that keeps both differentiators and no noise.
  const good = scoreOffer(label, { experience: [{ bullets: [label.atoms[0].text, label.atoms[1].text] }],
                                   projects: [{ name: 'PQC', description: label.atoms[3].text }] });
  assert.equal(good.differentiator_coverage, 1, 'both differentiators retained');
  assert.equal(good.noise_rate, 0, 'no noise shipped');
  assert.equal(good.grade_yield, 1, '3+2+3 is the best possible pick of size 3');

  // A CV that drops the rare one and ships the padding instead.
  const bad = scoreOffer(label, { experience: [{ bullets: [label.atoms[1].text, label.atoms[2].text] }], projects: [] });
  assert.equal(bad.differentiator_coverage, 0, 'both differentiators lost');
  assert.deepEqual(bad.lost_ids, [1, 4]);
  assert.equal(bad.noise_rate, 0.5, 'one of two shipped atoms was called padding');
  assert.ok(Math.abs(bad.grade_yield - 2 / 6) < 1e-9, 'got 2 of a possible 6');

  // Retention must survive a rewrite that shortens the bullet, since that is what
  // Phase 3 does to every line — atom → output, not output → atom.
  const trimmed = scoreOffer(label, {
    experience: [{ bullets: ['Eliminated an 8 MB-per-frame copy with a lock-free ring'] }],
    projects: [{ name: 'PQC', description: 'Benchmarked post-quantum signatures across seven schemes' }],
  });
  assert.equal(trimmed.differentiator_coverage, 1, 'a shortened rewrite still counts as retained');

  // A posting with no named differentiator must not drag the mean down.
  const none = scoreOffer({ ...label, differentiators: [] }, { experience: [], projects: [] });
  assert.equal(none.differentiator_coverage, undefined, 'no question asked, no score');
  assert.equal(meanOf([{ x: 1 }, { x: undefined }, { x: 0 }], 'x'), 0.5, 'undefined skipped, not zeroed');

  console.log('opus-metrics self-check OK');
}
