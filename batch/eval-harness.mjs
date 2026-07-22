#!/usr/bin/env node
// @ts-check
/**
 * eval-harness.mjs — measure the eval pipeline instead of vibing it.
 *
 * Subcommands:
 *   stats     [--evals DIR]                    Distribution stats for an eval dir
 *   backtest  --weights cv=0.5,ns=0.3,comp=0.2 [--evals DIR]
 *                                              Recompute scores from stored dims
 *                                              under alternative weights
 *   sample    [--n 12] [--out batch/bench/set.tsv]
 *                                              Stratified benchmark set (ids with
 *                                              cached JDs, spread across buckets)
 *   compare   --a DIR --b DIR [--labels FILE]  Side-by-side of two bench runs on
 *                                              the same ids (dims, spread, rank
 *                                              agreement, Block B grounding)
 *
 * A bench dir is what `ollama-evaluator.mjs --bench-dir DIR` produces:
 * DIR/evals/<id>.json + DIR/reports/<num>-<slug>-<date>.md
 *
 * Optional labels file (TSV: id<TAB>would_apply_1to5) enables Spearman vs your
 * own judgment — label a handful of offers once and every future change gets a
 * number.
 */

import { readFileSync, readdirSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { loadOutcomes } from './embeddings.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');

// ── CLI ───────────────────────────────────────────────────────────────────────

const [cmd, ...rest] = process.argv.slice(2);
const opts = {};
for (let i = 0; i < rest.length; i++) {
  if (rest[i].startsWith('--')) opts[rest[i].slice(2)] = rest[i + 1], i++;
}

// ── Loaders ───────────────────────────────────────────────────────────────────

function loadEvals(dir) {
  const out = new Map();
  if (!existsSync(dir)) return out;
  for (const f of readdirSync(dir)) {
    if (!f.endsWith('.json')) continue;
    try {
      const j = JSON.parse(readFileSync(join(dir, f), 'utf8'));
      if (j.status === 'evaled') out.set(String(j.id ?? f.replace('.json', '')), j);
    } catch {}
  }
  return out;
}

function loadLabels(file) {
  const out = new Map();
  if (!file || !existsSync(file)) return out;
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    const [id, v] = line.split('\t');
    const n = parseFloat(v);
    if (id && Number.isFinite(n)) out.set(id.trim(), n);
  }
  return out;
}

// ── Stats helpers ─────────────────────────────────────────────────────────────

function spearman(pairs) {
  // pairs: [[a,b], ...] — Spearman rank correlation
  if (pairs.length < 3) return null;
  const rank = (vals) => {
    const sorted = vals.map((v, i) => [v, i]).sort((x, y) => x[0] - y[0]);
    const r = new Array(vals.length);
    for (let i = 0; i < sorted.length; ) {
      let j = i;
      while (j + 1 < sorted.length && sorted[j + 1][0] === sorted[i][0]) j++;
      const avg = (i + j) / 2 + 1;
      for (let k = i; k <= j; k++) r[sorted[k][1]] = avg;
      i = j + 1;
    }
    return r;
  };
  const ra = rank(pairs.map(p => p[0]));
  const rb = rank(pairs.map(p => p[1]));
  const n = pairs.length;
  const mean = n => (n + 1) / 2;
  let num = 0, da = 0, db = 0;
  for (let i = 0; i < n; i++) {
    const xa = ra[i] - mean(n), xb = rb[i] - mean(n);
    num += xa * xb; da += xa * xa; db += xb * xb;
  }
  return da && db ? num / Math.sqrt(da * db) : null;
}

function dist(vals) {
  const n = vals.length;
  if (!n) return { n: 0 };
  const mean = vals.reduce((a, b) => a + b, 0) / n;
  const sd = Math.sqrt(vals.reduce((a, b) => a + (b - mean) ** 2, 0) / n);
  const uniq = new Set(vals).size;
  return { n, mean: +mean.toFixed(2), sd: +sd.toFixed(2), uniq, min: Math.min(...vals), max: Math.max(...vals) };
}

function histogram(vals, label) {
  const counts = {};
  for (const v of vals) counts[v] = (counts[v] || 0) + 1;
  const keys = Object.keys(counts).sort((a, b) => +a - +b);
  console.log(`  ${label}:`);
  const max = Math.max(...Object.values(counts));
  for (const k of keys) {
    const bar = '█'.repeat(Math.max(1, Math.round(counts[k] / max * 30)));
    console.log(`    ${String(k).padStart(4)} │${bar} ${counts[k]}`);
  }
}

// ── Block B quality proxies (grounding + duplicates) ──────────────────────────

function tokenize(s) {
  return (String(s).toLowerCase().match(/[a-z0-9+#.]{4,}/g) || []);
}

function cvLines() {
  const cv = readFileSync(resolve(PROJECT, 'cv.md'), 'utf8');
  return cv.split('\n').map(l => new Set(tokenize(l))).filter(s => s.size >= 4);
}

function blockBQuality(reportText, cvLineSets) {
  const m = reportText.match(/^## B\)[^\n]*\n([\s\S]*?)(?=^## )/m);
  if (!m) return null;
  const rows = m[1].split('\n')
    .filter(l => l.trim().startsWith('|'))
    .slice(2) // header + separator
    .map(l => l.split('|').map(c => c.trim()).filter(Boolean))
    .filter(c => c.length >= 2);
  if (!rows.length) return { rows: 0, dupes: 0, grounded: 0 };
  const seen = new Set();
  let dupes = 0, grounded = 0, gradeable = 0;
  for (const cells of rows) {
    const key = cells.join('|').toLowerCase();
    if (seen.has(key)) dupes++;
    seen.add(key);
    const evidence = tokenize(cells[1] || '');
    // Rows with no evidence claim (declared Gaps, "—") can't be grounded and
    // shouldn't count against the report — honesty isn't a grounding failure.
    if (evidence.length < 3) continue;
    gradeable++;
    const best = Math.max(0, ...cvLineSets.map(ls => {
      let hit = 0;
      for (const t of evidence) if (ls.has(t)) hit++;
      return hit / evidence.length;
    }));
    if (best >= 0.5) grounded++;
  }
  return { rows: rows.length, dupes, grounded, gradeable };
}

function benchReports(dir) {
  const rdir = join(dir, 'reports');
  const out = new Map(); // id → report text (id parsed from eval json report_path)
  const evals = loadEvals(join(dir, 'evals'));
  for (const [id, ev] of evals) {
    const p = ev.report_filename ? join(rdir, ev.report_filename) : null;
    if (p && existsSync(p)) out.set(id, readFileSync(p, 'utf8'));
  }
  return out;
}

// ── Subcommands ───────────────────────────────────────────────────────────────

function cmdStats() {
  const dir = resolve(PROJECT, opts.evals || 'batch/evals');
  const evals = loadEvals(dir);
  console.log(`\n=== stats: ${dir} (${evals.size} evals) ===\n`);
  const scores = [], combos = {}, decisions = {};
  let withDims = 0, withSalary = 0;
  for (const ev of evals.values()) {
    if (typeof ev.score === 'number') scores.push(ev.score);
    if (ev.cv_match != null && ev.north_star != null) {
      withDims++;
      const k = `cv${ev.cv_match}/ns${ev.north_star}`;
      combos[k] = (combos[k] || 0) + 1;
    }
    if (ev.salary_posted) withSalary++;
    decisions[ev.final_decision || '?'] = (decisions[ev.final_decision || '?'] || 0) + 1;
  }
  console.log('  score:', JSON.stringify(dist(scores)));
  histogram(scores, 'score histogram');
  console.log(`  dim combos (${withDims} evals with dims):`);
  for (const [k, v] of Object.entries(combos).sort((a, b) => b[1] - a[1])) {
    console.log(`    ${k.padEnd(10)} ${v}`);
  }
  console.log('  decisions:', JSON.stringify(decisions));
  console.log(`  salary_posted present: ${withSalary}/${evals.size}`);

  // Phase 1 vs Phase 2 rank agreement, where both exist
  const scoresDir = resolve(PROJECT, 'batch/scores');
  const pairs = [];
  for (const [id, ev] of evals) {
    const sf = join(scoresDir, `${id}.json`);
    if (!existsSync(sf)) continue;
    try {
      const p1 = JSON.parse(readFileSync(sf, 'utf8'));
      if (typeof p1.score === 'number' && typeof ev.score === 'number') pairs.push([p1.score, ev.score]);
    } catch {}
  }
  const rho = spearman(pairs);
  if (rho !== null) console.log(`  P1↔P2 Spearman: ${rho.toFixed(3)} (${pairs.length} offers)`);

  // Outcome separation: does the score distinguish offers that went somewhere
  // (interview/offer) from ones that died (rejected)? Meaningful once the
  // tracker accumulates real statuses beyond Evaluated/Applied.
  const outcomes = loadOutcomes();
  const byOutcome = {};
  for (const ev of evals.values()) {
    const o = outcomes.get(Number(ev.report_num));
    if (!o || typeof ev.score !== 'number') continue;
    (byOutcome[o] ||= []).push(ev.score);
  }
  if (Object.keys(byOutcome).length) {
    console.log('  score by real outcome:');
    for (const [o, s] of Object.entries(byOutcome).sort((a, b) => b[1].length - a[1].length)) {
      const mean = s.reduce((a, b) => a + b, 0) / s.length;
      console.log(`    ${o.padEnd(26)} n=${String(s.length).padEnd(3)} mean ${mean.toFixed(2)}`);
    }
  } else {
    console.log('  score by real outcome: (no tracker outcomes yet)');
  }
}

function cmdBacktest() {
  const dir = resolve(PROJECT, opts.evals || 'batch/evals');
  const evals = loadEvals(dir);
  const w = { cv: 0.625, ns: 0.375, comp: 0 };
  for (const part of (opts.weights || '').split(',')) {
    const [k, v] = part.split('=');
    if (k && v !== undefined) w[k.trim()] = parseFloat(v);
  }
  console.log(`\n=== backtest: weights ${JSON.stringify(w)} on ${dir} ===\n`);
  const oldScores = [], newScores = [], deltas = [];
  for (const [id, ev] of evals) {
    if (ev.cv_match == null || ev.north_star == null) continue;
    const comp = ev.comp_inferred ?? 3;
    const ns2 = Math.round((ev.cv_match * w.cv + ev.north_star * w.ns + comp * w.comp) * 10) / 10;
    oldScores.push(ev.score);
    newScores.push(ns2);
    deltas.push({ id, company: ev.company, old: ev.score, nu: ns2, d: +(ns2 - ev.score).toFixed(1) });
  }
  console.log('  old:', JSON.stringify(dist(oldScores)));
  console.log('  new:', JSON.stringify(dist(newScores)));
  histogram(newScores, 'new score histogram');
  const moved = deltas.filter(d => Math.abs(d.d) >= 0.3).sort((a, b) => Math.abs(b.d) - Math.abs(a.d));
  console.log(`  biggest moves (${moved.length} ≥ 0.3):`);
  for (const m of moved.slice(0, 10)) console.log(`    #${m.id} ${m.company}: ${m.old} → ${m.nu} (${m.d > 0 ? '+' : ''}${m.d})`);
}

function cmdSample() {
  const n = parseInt(opts.n || '12', 10);
  const outFile = resolve(PROJECT, opts.out || 'batch/bench/set.tsv');
  const evals = loadEvals(resolve(PROJECT, 'batch/evals'));
  const jdsDir = resolve(PROJECT, 'batch/jds');
  const candidates = [...evals.values()]
    .filter(ev => typeof ev.score === 'number' && existsSync(join(jdsDir, `${ev.id}.txt`)))
    .sort((a, b) => a.score - b.score);
  if (candidates.length < n) {
    console.error(`Only ${candidates.length} candidates with cached JDs — sampling all.`);
  }
  // Stratify: split sorted-by-score list into n equal slices, take the middle of each
  const picked = [];
  const k = Math.min(n, candidates.length);
  for (let i = 0; i < k; i++) {
    const idx = Math.floor((i + 0.5) * candidates.length / k);
    picked.push(candidates[idx]);
  }
  mkdirSync(dirname(outFile), { recursive: true });
  const lines = picked.map(ev => `${ev.id}\t${ev.url}\t${ev.score}\t${ev.company}`);
  writeFileSync(outFile, lines.join('\n') + '\n', 'utf8');
  console.log(`\n=== sample: ${picked.length} offers → ${outFile} ===\n`);
  for (const ev of picked) console.log(`  #${ev.id}  old-score ${ev.score}  ${ev.company} — ${ev.role}`);
}

function cmdCompare() {
  if (!opts.a || !opts.b) { console.error('compare requires --a DIR --b DIR'); process.exit(1); }
  const dirA = resolve(PROJECT, opts.a), dirB = resolve(PROJECT, opts.b);
  const a = loadEvals(join(dirA, 'evals'));
  const b = loadEvals(join(dirB, 'evals'));
  const labels = loadLabels(opts.labels && resolve(PROJECT, opts.labels));
  const ids = [...a.keys()].filter(id => b.has(id));
  console.log(`\n=== compare: A=${opts.a} B=${opts.b} (${ids.length} common offers) ===\n`);
  if (!ids.length) return;

  console.log('  id      | A cv/ns/comp → score | B cv/ns/comp → score | Δ');
  console.log('  --------|----------------------|----------------------|-----');
  const pairs = [], aScores = [], bScores = [];
  for (const id of ids) {
    const ea = a.get(id), eb = b.get(id);
    const fmt = e => `${e.cv_match}/${e.north_star}/${e.comp_inferred ?? '-'} → ${e.score}`;
    const d = +(eb.score - ea.score).toFixed(1);
    console.log(`  ${id.padEnd(7)} | ${fmt(ea).padEnd(20)} | ${fmt(eb).padEnd(20)} | ${d > 0 ? '+' : ''}${d}  ${ea.company}`);
    pairs.push([ea.score, eb.score]);
    aScores.push(ea.score); bScores.push(eb.score);
  }
  console.log('');
  console.log('  A score:', JSON.stringify(dist(aScores)));
  console.log('  B score:', JSON.stringify(dist(bScores)));
  const rho = spearman(pairs);
  if (rho !== null) console.log(`  A↔B Spearman: ${rho.toFixed(3)}`);

  // Discrimination: unique (cv,ns) combos per model on the same offers
  const uc = m => new Set(ids.map(id => `${m.get(id).cv_match}/${m.get(id).north_star}`)).size;
  console.log(`  unique cv/ns combos: A=${uc(a)} B=${uc(b)} (higher = more discriminating)`);

  // Block B grounding
  const cvSets = cvLines();
  for (const [name, dir] of [['A', dirA], ['B', dirB]]) {
    const reports = benchReports(dir);
    let rows = 0, dupes = 0, grounded = 0, gradeable = 0, n = 0;
    for (const [, text] of reports) {
      const q = blockBQuality(text, cvSets);
      if (!q) continue;
      rows += q.rows; dupes += q.dupes; grounded += q.grounded; gradeable += q.gradeable; n++;
    }
    if (n) console.log(`  Block B (${name}): ${rows} rows across ${n} reports | ${dupes} duplicate rows | ${grounded}/${gradeable} evidence claims grounded in cv.md (${gradeable ? (grounded / gradeable * 100).toFixed(0) : 0}%)`);
  }

  // Labels
  if (labels.size) {
    for (const [name, m] of [['A', a], ['B', b]]) {
      const lp = ids.filter(id => labels.has(id)).map(id => [m.get(id).score, labels.get(id)]);
      const r = spearman(lp);
      if (r !== null) console.log(`  ${name}↔labels Spearman: ${r.toFixed(3)} (${lp.length} labelled)`);
    }
  }
}

// Lists evaluated offers with no entry yet in labels.tsv — read-only nudge so
// labeling stays a matter of "here's what's missing" rather than manual digging.
function cmdUnlabeled() {
  const evals = loadEvals(resolve(PROJECT, 'batch/evals'));
  const labels = loadLabels(resolve(PROJECT, opts.labels || 'batch/labels.tsv'));
  const missing = [...evals.values()]
    .filter(ev => !labels.has(String(ev.id)))
    .sort((a, b) => a.id - b.id);
  console.log(`\n=== ${missing.length} evaluated offer(s) without a labels.tsv entry ===\n`);
  for (const ev of missing) {
    console.log(`  #${ev.id}\tscore ${ev.score}\t${ev.company} — ${ev.role}`);
  }
  if (missing.length) console.log(`\nAppend to labels.tsv: <id>\\t<1-5 would-apply>\\t<company>`);
}

// ── Main ──────────────────────────────────────────────────────────────────────

switch (cmd) {
  case 'stats':      cmdStats(); break;
  case 'backtest':   cmdBacktest(); break;
  case 'sample':     cmdSample(); break;
  case 'compare':    cmdCompare(); break;
  case 'unlabeled':  cmdUnlabeled(); break;
  default:
    console.log('Usage: eval-harness.mjs <stats|backtest|sample|compare|unlabeled> [options]');
    process.exit(cmd ? 1 : 0);
}
