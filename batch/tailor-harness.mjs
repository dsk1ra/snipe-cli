// @ts-check
/**
 * tailor-harness.mjs — benchmark Phase 3 (CV tailoring).
 *
 * Phase 1/2 are scored, so `eval-harness.mjs` can rank-correlate them against a
 * reference. Phase 3 emits a JSON *document*, so there is nothing to correlate
 * and no gold set exists. Every metric here is therefore label-free and checked
 * against source text with no model in the loop — the same property that made
 * `fab_jd`/`fab_cv` the only trustworthy metrics in PHASE1-EXPERIMENT-LEDGER.md.
 *
 *   role_retention  roles in the output / roles cv.md has          want 1.0
 *   metric_fab      output numbers absent from cv.md               want 0
 *   example_copy    offers copying an 8-gram from the prompt's
 *                   own worked example                             want 0
 *   grounding       mean token overlap of each output bullet with
 *                   the best-matching cv.md bullet for that role   higher
 *
 * CLI:
 *   node batch/tailor-harness.mjs sample --n 24        write the fixed sample
 *   node batch/tailor-harness.mjs run <label> [--temperature 0]
 *   node batch/tailor-harness.mjs metrics <label>
 *   node batch/tailor-harness.mjs compare <a> <b>
 *
 * The sample is written once and reused by every variant — an A/B over
 * different offers measures the offers, not the change.
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { execFileSync } from 'child_process';
import { parseCvSections, parseEntries, entryCompany } from './cv-select.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
const BENCH = resolve(__dirname, 'bench/tailor');
const SAMPLE = resolve(BENCH, 'sample.tsv');

// ── sample selection ──────────────────────────────────────────────────────────

/**
 * Offers with an eval, a report on disk and a cached JD — Phase 3's real inputs.
 * Paths are injectable so the selection rules can be tested against a fixture
 * tree instead of whatever happens to be in the developer's batch/ dir.
 * @param {{stateFile?: string, reportsDir?: string, batchDir?: string}} [paths]
 */
export function eligible(paths = {}) {
  const {
    stateFile = resolve(__dirname, 'local-state.tsv'),
    reportsDir = resolve(PROJECT, 'reports'),
    batchDir = __dirname,
  } = paths;
  if (!existsSync(stateFile) || !existsSync(reportsDir)) return [];
  const state = readFileSync(stateFile, 'utf8').trim().split('\n');
  const reports = readdirSync(reportsDir);
  const out = [];
  for (const line of state.slice(1)) {
    const c = line.split('\t');
    const [id, , , , , p2status, reportNum] = c;
    if (p2status !== 'evaled' || !reportNum || reportNum === '-') continue;
    const report = reports.find(f => f.startsWith(`${reportNum}-`));
    const jd = resolve(batchDir, `jds/${id}.txt`);
    const ev = resolve(batchDir, `evals/${id}.json`);
    if (!report || !existsSync(jd) || !existsSync(ev)) continue;
    let e;
    try { e = JSON.parse(readFileSync(ev, 'utf8')); } catch { continue; }
    if (!e.company || !e.role || typeof e.score !== 'number') continue;
    out.push({ id, reportNum, report: `reports/${report}`, jd: `batch/jds/${id}.txt`,
               company: e.company, role: e.role, score: e.score });
  }
  return out;
}

/**
 * Stratified by eval score so the sample spans the range Phase 3 actually sees,
 * deterministic (sorted, fixed stride) so re-running `sample` reproduces it.
 */
function buildSample(n, paths = {}) {
  const all = eligible(paths).sort((a, b) => a.score - b.score || Number(a.id) - Number(b.id));
  if (all.length <= n) return all;
  const step = all.length / n;
  return Array.from({ length: n }, (_, i) => all[Math.floor(i * step)]);
}

export function readSample(samplePath = SAMPLE) {
  if (!existsSync(samplePath)) throw new Error(`no sample — run: node batch/tailor-harness.mjs sample --n 24`);
  return readFileSync(samplePath, 'utf8').trim().split('\n').slice(1).map(l => {
    const [id, reportNum, report, jd, company, role, score] = l.split('\t');
    return { id, reportNum, report, jd, company, role, score: parseFloat(score) };
  });
}

// ── running a variant ─────────────────────────────────────────────────────────

function runVariant(label, { temperature = 0, ollamaUrl = 'http://localhost:11434', model = 'snipe-cv' } = {}) {
  const sample = readSample();
  const dir = resolve(BENCH, label);
  mkdirSync(dir, { recursive: true });
  const t0 = Date.now();
  let ok = 0, failed = 0;
  for (const [i, s] of sample.entries()) {
    process.stderr.write(`[${i + 1}/${sample.length}] #${s.id} ${s.company} — ${s.role}\n`);
    try {
      execFileSync(process.execPath, [
        resolve(__dirname, 'local-pdf-offer.mjs'),
        '--id', s.id, '--report-path', resolve(PROJECT, s.report), '--report-num', s.reportNum,
        '--jd-file', resolve(PROJECT, s.jd), '--eval-score', String(s.score),
        '--company', s.company, '--role', s.role, '--date', '2026-01-01',
        '--model', model, '--ollama-url', ollamaUrl,
        '--threshold', '0', '--temperature', String(temperature), '--bench-dir', dir,
      ], { stdio: ['ignore', 'pipe', 'pipe'], cwd: PROJECT, timeout: 600_000 });
      ok++;
    } catch (err) {
      failed++;
      process.stderr.write(`  FAILED: ${String(err.stderr || err.message).slice(0, 200)}\n`);
    }
  }
  const meta = { label, temperature, model, n: sample.length, ok, failed,
                 minutes: +((Date.now() - t0) / 60000).toFixed(1), at: new Date().toISOString() };
  writeFileSync(resolve(dir, 'meta.json'), JSON.stringify(meta, null, 2), 'utf8');
  return meta;
}

// ── metrics ───────────────────────────────────────────────────────────────────

/**
 * Experience entries in cv.md, as { company, role, bullets[] }.
 * @param {string} [cvPath] injectable so tests use a fixture CV, not the user's
 */
function cvExperience(cvPath = resolve(PROJECT, 'cv.md')) {
  const sec = parseCvSections(readFileSync(cvPath, 'utf8')).find(s => s.name === 'Experience');
  if (!sec) return [];
  return parseEntries(sec.lines).entries.map(e => ({
    company: entryCompany(e),
    role: e.head[0].replace(/^###\s+/, '').trim(),
    bullets: e.bullets,
  }));
}

const NUM = /\d[\d,.]*\+?%?/g;
/** Numbers as normalised strings; "170" and "170+" and "1,700" stay distinguishable. */
function numsOf(s) {
  return new Set((String(s).match(NUM) || []).map(x => x.replace(/[.,]$/, '')).filter(x => x.length > 1));
}

const WORD = /[a-z0-9+#.]{3,}/g;
const toks = s => new Set((String(s).toLowerCase().match(WORD) || []));
function overlap(a, b) {
  const A = toks(a), B = toks(b);
  if (!A.size) return 0;
  let n = 0;
  for (const t of A) if (B.has(t)) n++;
  return n / A.size;
}

/** 8-gram shingles, for detecting text lifted from the prompt's worked example. */
function shingles(s, k = 8) {
  const w = String(s).toLowerCase().match(WORD) || [];
  const out = new Set();
  for (let i = 0; i + k <= w.length; i++) out.add(w.slice(i, i + k).join(' '));
  return out;
}

/**
 * Bullets in the worked example of the prompt that **actually runs**.
 * local-pdf-offer.mjs:28 prefers the gitignored .local.md override when it
 * exists, so resolving the shipped file here measured a prompt the pipeline
 * never opened.
 */
export function activePromptPath() {
  const local = resolve(__dirname, 'local-tailor-prompt.local.md');
  return existsSync(local) ? local : resolve(__dirname, 'local-tailor-prompt.md');
}

function exampleShingles() {
  const p = readFileSync(activePromptPath(), 'utf8');
  const out = new Set();
  for (const m of p.matchAll(/"bullets"\s*:\s*\[([^\]]*)\]/g)) {
    for (const b of m[1].split(/",\s*"/)) for (const sh of shingles(b)) out.add(sh);
  }
  return out;
}

/**
 * @param {string} label
 * @param {{benchRoot?: string, cvPath?: string}} [paths] injectable for tests
 */
function metricsFor(label, paths = {}) {
  const { benchRoot = BENCH, cvPath = resolve(PROJECT, 'cv.md') } = paths;
  const dir = resolve(benchRoot, label);
  if (!existsSync(dir)) throw new Error(`no run at ${dir}`);
  const cvExp = cvExperience(cvPath);
  const expectedRoles = cvExp.length;
  const cvNums = numsOf(readFileSync(cvPath, 'utf8'));
  const ex = exampleShingles();

  const rows = [];
  for (const d of readdirSync(dir)) {
    const f = join(dir, d, 'cv-content.json');
    if (!existsSync(f)) continue;
    /** @type {any} */
    let c;
    try { c = JSON.parse(readFileSync(f, 'utf8')); } catch { continue; }
    const exp = Array.isArray(c.experience) ? c.experience : [];

    const fabNums = [];
    let groundSum = 0, groundN = 0, copied = 0, unmatched = 0;
    const matchedRoles = new Set();
    for (const e of exp) {
      // A schema floor can be satisfied by padding, so an entry only counts as a
      // retained role if it names something cv.md actually contains — by company
      // or by role title, since the model uses either.
      const key = String(e.company || '').toLowerCase().slice(0, 8);
      const match = key && cvExp.find(r =>
        r.company.toLowerCase().includes(key) || r.role.toLowerCase().includes(key));
      // A second entry for an employer already claimed is not a role, it is
      // junk — count it with the invented ones rather than as a match, or the
      // `Acme|Acme` failure shape reads as two healthy roles.
      if (match && !matchedRoles.has(match.company)) matchedRoles.add(match.company);
      else unmatched++;
      for (const b of (e.bullets || [])) {
        for (const n of numsOf(b)) if (!cvNums.has(n)) fabNums.push(n);
        for (const sh of shingles(b)) if (ex.has(sh)) { copied++; break; }
        if (match) {
          groundSum += Math.max(0, ...match.bullets.map(cb => overlap(b, cb)));
          groundN++;
        }
      }
    }
    rows.push({
      dir: d,
      roles: exp.length,
      // Retention counts roles traceable to cv.md, not array length — otherwise
      // a padded or invented entry reads as a fix.
      retention: expectedRoles ? matchedRoles.size / expectedRoles : 1,
      unmatched,
      companies: exp.map(e => e.company).join('|'),
      fab: fabNums.length,
      fabList: [...new Set(fabNums)],
      copied,
      grounding: groundN ? groundSum / groundN : 0,
      bullets: groundN,
    });
  }

  const n = rows.length || 1;
  const mean = k => rows.reduce((a, r) => a + r[k], 0) / n;
  let meta = {};
  try { meta = JSON.parse(readFileSync(resolve(dir, 'meta.json'), 'utf8')); } catch {}
  return {
    label, n: rows.length, meta,
    role_retention: +mean('retention').toFixed(3),
    all_roles_pct: +(rows.filter(r => r.retention >= 1).length / n).toFixed(3),
    invented_roles: +mean('unmatched').toFixed(3),
    metric_fab: +mean('fab').toFixed(3),
    fab_offers_pct: +(rows.filter(r => r.fab > 0).length / n).toFixed(3),
    example_copy_pct: +(rows.filter(r => r.copied > 0).length / n).toFixed(3),
    grounding: +mean('grounding').toFixed(3),
    mean_bullets: +mean('bullets').toFixed(2),
    rows,
  };
}

// ── CLI ───────────────────────────────────────────────────────────────────────

const [cmd, ...rest] = process.argv.slice(2);
const flag = (name, def) => {
  const i = rest.indexOf(`--${name}`);
  return i >= 0 ? rest[i + 1] : def;
};
const positional = rest.filter((a, i) => !a.startsWith('--') && !(i > 0 && rest[i - 1].startsWith('--')));

// Only run the CLI when invoked directly — importing the metrics from a test or
// an ad-hoc script must not execute a command.
const isMain = process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url));

if (!isMain) {
  // imported: expose the helpers, run nothing
} else if (cmd === 'sample') {
  const n = parseInt(String(flag('n', '24')), 10);
  const s = buildSample(n);
  mkdirSync(BENCH, { recursive: true });
  writeFileSync(SAMPLE,
    'id\treport_num\treport\tjd\tcompany\trole\tscore\n' +
    s.map(r => [r.id, r.reportNum, r.report, r.jd, r.company, r.role, r.score].join('\t')).join('\n') + '\n',
    'utf8');
  const scores = s.map(r => r.score);
  console.log(`wrote ${s.length} offers to ${SAMPLE}`);
  console.log(`score range ${Math.min(...scores)}–${Math.max(...scores)}, mean ${(scores.reduce((a, b) => a + b, 0) / s.length).toFixed(2)}`);
} else if (cmd === 'run') {
  const label = positional[0];
  if (!label) throw new Error('usage: run <label> [--temperature 0]');
  console.log(JSON.stringify(runVariant(label, {
    temperature: parseFloat(String(flag('temperature', '0'))),
    ollamaUrl: String(flag('ollama-url', 'http://localhost:11434')),
    model: String(flag('model', 'snipe-cv')),
  }), null, 2));
} else if (cmd === 'metrics') {
  const m = metricsFor(positional[0]);
  const { rows, ...summary } = m;
  console.log(JSON.stringify(summary, null, 2));
  if (rest.includes('--rows')) for (const r of rows) console.log(`  ${r.dir}  roles=${r.roles} fab=${r.fab} copied=${r.copied} g=${r.grounding.toFixed(2)}  ${r.companies}`);
} else if (cmd === 'compare') {
  const [a, b] = positional;
  const A = metricsFor(a), B = metricsFor(b);
  const keys = ['n', 'role_retention', 'all_roles_pct', 'invented_roles', 'metric_fab', 'fab_offers_pct', 'example_copy_pct', 'grounding', 'mean_bullets'];
  const pad = (s, w) => String(s).padEnd(w);
  console.log(`${pad('metric', 18)}${pad(a, 14)}${pad(b, 14)}delta`);
  console.log('-'.repeat(56));
  for (const k of keys) {
    const d = typeof A[k] === 'number' && typeof B[k] === 'number' ? (B[k] - A[k]).toFixed(3) : '';
    console.log(`${pad(k, 18)}${pad(A[k], 14)}${pad(B[k], 14)}${d}`);
  }
} else {
  console.log('usage: sample --n 24 | run <label> [--temperature 0] | metrics <label> [--rows] | compare <a> <b>');
}

export { buildSample, cvExperience, metricsFor, numsOf, shingles, exampleShingles };
