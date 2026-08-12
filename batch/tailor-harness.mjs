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
 *                   — EXPERIENCE BULLETS ONLY. It read a clean 0
 *                   through every run in which a summary claimed
 *                   a tenure, a figure and a university tier the
 *                   CV never states; see summary_fab_* below
 *   summary_fab_pct offers whose SHIPPED summary still carries a
 *                   fabrication — a guard regression, not a
 *                   model result                                   want 0
 *   summary_fab_raw offers whose summary carried one BEFORE the
 *      _pct         guards repaired it. This is the model's
 *                   actual rate and the number to move; it is
 *                   read from `_summary_pre_guard`, because
 *                   scoring the repaired text would report 0 by
 *                   construction (rule 5). `summary_fab_raw_n`
 *                   is how many offers could answer at all        want 0
 *   example_copy    offers copying an 8-gram from the prompt's
 *                   own worked example                             want 0
 *   summary_shape   shape defects per summary, and the fraction of
 *      _defects     offers carrying any. The only metric here that
 *      _pct         is not about falsity: a summary can be entirely
 *                   true and still ship as a 75-word run-on or a
 *                   list of bullets with the bullets removed, and
 *                   every other metric scores those clean. See
 *                   `summaryShape`                                 want 0
 *   grounding       mean token overlap of each output bullet with
 *                   the best-matching cv.md bullet for that role   higher
 *   num_retention   figures in that source bullet that survived
 *                   into the rewrite (`num_lost` = how many did
 *                   not)                                           want 1.0
 *
 * `metric_fab` and `num_retention` are the two halves of the same question and
 * neither implies the other: a rewrite that drops every quantified outcome
 * invents nothing, so it scores a clean 0 fab and reads as healthy. It took a
 * batch of 12 real CVs at 44 % retention to notice.
 *
 * CLI:
 *   node batch/tailor-harness.mjs sample --n 24        write the fixed sample
 *   node batch/tailor-harness.mjs run <label> [--temperature 0]
 *   node batch/tailor-harness.mjs metrics <label>   (--rows prints
 *                   `sfab=<shipped>/<raw>(classes)` per offer)
 *   node batch/tailor-harness.mjs compare <a> <b>
 *
 * The sample is written once and reused by every variant — an A/B over
 * different offers measures the offers, not the change.
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync, mkdtempSync, rmSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { tmpdir } from 'os';
import { fileURLToPath, pathToFileURL } from 'url';
import { execFileSync } from 'child_process';
import { createHash } from 'crypto';
import { parseCvSections, parseEntries, entryCompany,
         extractBlockBRequirements } from './cv-select.mjs';
import { embed, cosine } from './embeddings.mjs';
import { summaryUnsupported as summaryFab, productFab,
         summaryShape } from './summary-stage.mjs';
import { parseSkillCategories, normPhrase } from './cv-writers.mjs';
import { loadLabels, scoreOffer } from './opus-metrics.mjs';

/**
 * Fabrication classes present in a summary, named.
 *
 * Defined by asking the shipped guards whether they would change the text, so
 * the metric cannot drift away from what is actually enforced — a hand-rolled
 * second detector would answer a different question the first time either side
 * was edited.
 *
 * Read it against `_summary_pre_guard`, not the shipped summary: run against the
 * repaired text it is 0 by construction and says nothing about the model. Both
 * are reported, because they answer different questions — the raw count is how
 * often the model fabricates, the shipped count is whether anything is leaking
 * past the guards, and only the second is allowed to be non-zero-worthy news.
 *
 * @param {string} summary
 * @param {string} cvText
 * @returns {string[]}
 */
// Re-exported rather than reimplemented. This function used to have its own copy
// of the kind list, and the copy drifted: it grew `tenure` and `figure` while the
// generation gate in summary-stage.mjs kept checking only products. The stage
// therefore believed it had rejected every fabrication while eight of 32 offers
// shipped one. The gate and the metric now cannot disagree, because they are the
// same function.
export { summaryFab };

const readSafe = (p) => { try { return p && existsSync(p) ? readFileSync(p, 'utf8') : ''; } catch { return ''; } };

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');
// Same override as retrieval-bench.mjs, for the same reason: BENCH holds a real
// embedding cache, and a fixture run that overwrote it would cost a full
// re-embed to undo. Unset in production, where it resolves as it always did.
const BENCH = resolve(process.env.SNIPE_BENCH_DIR || resolve(__dirname, 'bench'), 'tailor');
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
function buildSample(n, paths = {}, minScore = 0) {
  // The same posting gets re-scanned across runs; two copies of one JD is a
  // wasted sample slot, not a second data point. Keep the latest scan of each.
  const seen = new Map();
  for (const o of eligible(paths)) {
    if (o.score < minScore) continue;
    const k = `${o.company} ${o.role}`.toLowerCase();
    if (!seen.has(k) || Number(o.id) > Number(seen.get(k).id)) seen.set(k, o);
  }
  const all = [...seen.values()].sort((a, b) => a.score - b.score || Number(a.id) - Number(b.id));
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

/**
 * The commit the tree is on, or '' outside a repo.
 *
 * Recorded at the start of a run and re-checked at the end. A 32-offer run is
 * 40 minutes of spawning `local-pdf-offer.mjs`, which re-imports its modules
 * every time, so a checkout mid-run silently splits it: offers before the switch
 * ran one version, offers after ran another, and the mean is of neither. That
 * happened — a branch switch 34 minutes into an E2 arm — and nothing noticed
 * until the numbers were being read. Benchmark rule 4, enforced rather than
 * remembered.
 */
function headSha() {
  try {
    return execFileSync('git', ['rev-parse', 'HEAD'], { cwd: PROJECT, encoding: 'utf8' }).trim();
  } catch { return ''; }
}

function runVariant(label, { temperature = 0, ollamaUrl = 'http://localhost:11434', model = 'snipe-cv', summaryModel = 'snipe-eval', limit = 0, writer = 'model', samplePath = SAMPLE, resume = false } = {}) {
  // `limit` takes a PREFIX of the sample, never a random subset: the sample is
  // sorted by eval score, so the same prefix is the same offers every time and
  // two limited runs stay paired. A limited run is only comparable to another
  // run over the same prefix — noted in meta.json so a later reader cannot
  // mistake it for a full one.
  const sample = limit ? readSample(samplePath).slice(0, limit) : readSample(samplePath);
  const dir = resolve(BENCH, label);
  mkdirSync(dir, { recursive: true });
  const sha0 = headSha();
  const t0 = Date.now();
  let ok = 0, failed = 0, skipped = 0;
  for (const [i, s] of sample.entries()) {
    // --resume keeps offers that already produced parseable content and runs
    // only the gaps, so a run interrupted at offer 28 costs four offers to
    // finish rather than thirty-two. An unparseable or absent file is a gap.
    // Matched on the id prefix rather than by rebuilding the slug: the naming
    // rule lives in local-pdf-offer.mjs, and a second copy here would silently
    // stop matching the first time either changed.
    if (resume) {
      let done = false;
      try {
        const d = readdirSync(dir).find(x => x.startsWith(`${s.id}_`));
        done = !!(d && JSON.parse(readFileSync(resolve(dir, d, 'cv-content.json'), 'utf8')));
      } catch { done = false; }
      if (done) { skipped++; continue; }
    }
    process.stderr.write(`[${i + 1}/${sample.length}] #${s.id} ${s.company} — ${s.role}\n`);
    try {
      execFileSync(process.execPath, [
        resolve(__dirname, 'local-pdf-offer.mjs'),
        '--id', s.id, '--report-path', resolve(PROJECT, s.report), '--report-num', s.reportNum,
        '--jd-file', resolve(PROJECT, s.jd), '--eval-score', String(s.score),
        '--company', s.company, '--role', s.role, '--date', '2026-01-01',
        '--model', model, '--summary-model', summaryModel, '--ollama-url', ollamaUrl,
        '--threshold', '0', '--temperature', String(temperature), '--bench-dir', dir,
        '--writer', writer,
      ], { stdio: ['ignore', 'pipe', 'pipe'], cwd: PROJECT, timeout: 900_000 });
      ok++;
    } catch (err) {
      failed++;
      process.stderr.write(`  FAILED: ${String(err.stderr || err.message).slice(0, 200)}\n`);
    }
  }
  // Env-gated behaviour is invisible in the output dir, so a run that forgot the
  // flag is indistinguishable from one that had it. Record which arm this was.
  const flags = Object.fromEntries(Object.entries(process.env)
    .filter(([k]) => k.startsWith('SNIPE_') && k !== 'SNIPE_TIMING'));
  const sha1 = headSha();
  const split = !!(sha0 && sha1 && sha0 !== sha1);
  const meta = { label, temperature, model, summaryModel, writer, sample: samplePath, n: sample.length,
                 limit: limit || null, ok, failed, skipped: resume ? skipped : null,
                 commit: sha0, commit_end: sha1,
                 // Loud, and in the artifact rather than only on a terminal
                 // nobody was watching: a split run's mean is of neither version.
                 split_run: split || null,
                 flags, minutes: +((Date.now() - t0) / 60000).toFixed(1), at: new Date().toISOString() };
  writeFileSync(resolve(dir, 'meta.json'), JSON.stringify(meta, null, 2), 'utf8');
  if (split) {
    process.stderr.write(`\n*** HEAD MOVED DURING THIS RUN: ${sha0.slice(0, 8)} -> ${sha1.slice(0, 8)}\n`
      + `*** local-pdf-offer.mjs re-imports per offer, so offers before and after ran\n`
      + `*** different code. This run is a mongrel — do not score it. (rule 4)\n\n`);
  }
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

/**
 * Snapshot of the worked-example bullets as they stood when copying was first
 * measured at 29 %.
 *
 * Without this the metric is self-defeating: `example_copy_pct` is defined
 * against whatever example the prompt currently holds, so *deleting* the example
 * (which is exactly what G2 does) drops the metric to 0 by construction — the
 * detector goes blind and the change scores a perfect win it did not earn.
 * Unioning a fixed snapshot in means the post-G2 number still answers the real
 * question: does the model still emit that text?
 *
 * The committed snapshot is generic, because the worked example in a personal
 * `local-tailor-prompt.local.md` is lifted from a real CV and this file is the
 * one thing under `batch/bench/` that ships. A `.local.json` beside it wins when
 * present, the same override `local-pdf-offer.mjs:36` applies to the prompt
 * itself — so the shipped repo carries placeholder text while a real run still
 * measures copying against the example it actually shows the model.
 */
function snapshotShingles() {
  const out = new Set();
  const local = resolve(__dirname, 'bench/example-bullets.local.json');
  const path  = existsSync(local) ? local : resolve(__dirname, 'bench/example-bullets.json');
  try {
    for (const b of JSON.parse(readFileSync(path, 'utf8'))) {
      for (const sh of shingles(b)) out.add(sh);
    }
  } catch { /* no snapshot — fall back to the live prompt alone */ }
  return out;
}

function exampleShingles() {
  const p = readFileSync(activePromptPath(), 'utf8');
  const out = snapshotShingles();
  for (const m of p.matchAll(/"bullets"\s*:\s*\[([^\]]*)\]/g)) {
    for (const b of m[1].split(/",\s*"/)) for (const sh of shingles(b)) out.add(sh);
  }
  return out;
}

// ── Named-product fabrication ─────────────────────────────────────────────────
// The vocabulary and the detector live in summary-stage.mjs, because the
// pipeline *enforces* the rule at generation time and the bench only measures
// it. Two copies would drift, and the copy that mattered would be the one the
// benchmark was not using.

// ── ATS keyword coverage ──────────────────────────────────────────────────────

// Beyond the 3-char floor: words a JD and a CV both contain regardless of
// subject, which would inflate coverage without measuring anything.
const ATS_STOP = new Set([
  'and','the','for','with','you','our','your','are','will','have','has','from',
  'this','that','they','their','its','not','but','can','all','any','who','how',
  'work','working','team','teams','role','roles','job','jobs','company','years',
  'year','experience','experienced','skills','skill','strong','good','great',
  'ability','able','including','include','includes','across','within','using',
  'use','used','new','well','also','more','most','other','such','into','while',
  'about','over','than','then','been','were','was','would','should','could',
  'must','may','need','needs','required','requirements','requirement','looking',
  'candidate','candidates','apply','please','join','help','make','build',
  'building','built','develop','developing','support','ensure','deliver','high',
  'level','based','part','time','full','world','best','like','want','one','two',
]);

/**
 * Of the JD terms `cv.md` can *legitimately* support, the fraction that reach
 * the tailored output.
 *
 * Scoring against the supportable subset rather than the whole JD is the point:
 * a CV cannot be rewarded for covering a term it has no business claiming, so
 * the metric cannot be gamed by inventing. It is capped at the CV's honest
 * ceiling by construction.
 *
 * @param {string} jdText
 * @param {string} cvText
 * @param {string} outputText
 */
export function atsCoverage(jdText, cvText, outputText) {
  const content = (s) => new Set([...toks(s)].filter(t => !ATS_STOP.has(t) && !/^\d+$/.test(t)));
  const jd = content(jdText), cv = content(cvText), out = content(outputText);
  const supportable = [...jd].filter(t => cv.has(t));
  if (!supportable.length) return { coverage: 0, supportable: 0, covered: 0, missed: [] };
  const covered = supportable.filter(t => out.has(t));
  return {
    coverage: covered.length / supportable.length,
    supportable: supportable.length,
    covered: covered.length,
    missed: supportable.filter(t => !out.has(t)).slice(0, 20),
  };
}

/**
 * Of the skills this posting **names** and `cv.md` genuinely claims, the fraction
 * that reach the page.
 *
 * `ats_coverage` is the blunt version of this question and answers a different
 * one. It counts every ≥3-char token a JD and `cv.md` share, so on this corpus
 * its 202 distinct misses are led by `complex`, `location`, `fast`, `where` and
 * `never` — 185 of them generic English rather than anything a recruiter searches
 * for. It cannot reach 1.0 by any legitimate means, and driving it up rewards
 * padding. Keep it as a breadth signal; do not target it.
 *
 * This one is matched as **phrases against cv.md's own skill taxonomy**, so
 * there is no stoplist to tune and "NAT Traversal (STUN/TURN)" cannot contribute
 * a spurious `turn`. It is honest in both directions: bounded above by what the
 * CV actually claims, so it cannot be gamed by inventing, and it goes down when a
 * real skill is cut — which is exactly what the skills block used to do silently.
 *
 * Returns `null` coverage when a posting names no skill at all, rather than 0 —
 * a posting with nothing to match is not a failure to match it.
 *
 * @param {string} jdText
 * @param {string} cvText
 * @param {string} outputText
 */
export function skillCoverage(jdText, cvText, outputText) {
  const norm = normPhrase;
  const items = [...new Set(parseSkillCategories(cvText).flatMap(c => c.items))];
  const jd = norm(jdText), out = norm(outputText);
  const asked = items.filter(s => jd.includes(norm(s)));
  const missed = asked.filter(s => !out.includes(norm(s)));
  return {
    coverage: asked.length ? (asked.length - missed.length) / asked.length : null,
    asked: asked.length,
    missed,
  };
}

/**
 * @param {string} label
 * @param {{benchRoot?: string, cvPath?: string, keep?: Set<string>|null}} [paths]
 *   injectable for tests; `keep` restricts to a set of run directories so two
 *   runs of different sizes can be compared over the offers they share.
 */
function metricsFor(label, paths = {}) {
  const { benchRoot = BENCH, cvPath = resolve(PROJECT, 'cv.md'), keep = null } = paths;
  const dir = resolve(benchRoot, label);
  if (!existsSync(dir)) throw new Error(`no run at ${dir}`);
  const cvExp = cvExperience(cvPath);
  const expectedRoles = cvExp.length;
  const cvText = readFileSync(cvPath, 'utf8');
  const cvNums = numsOf(cvText);
  const ex = exampleShingles();
  // Offer id → that offer's Phase 2 report. Keyed on the id rather than the
  // whole `<id>_<slug>` dir name so a change to the slug rule cannot silently
  // empty every requirement list and read as selection_regret improving.
  const reportById = new Map();
  // The role each offer was tailored for. Needed only so the rendered page
  // carries the same target-role line a real run would print — it is one line,
  // but measuring a page without it measures a page nobody ships.
  const roleById = new Map();
  // Which sample this run used, as recorded by runVariant. A run over a second
  // sample file scored against sample.tsv matches no offer, so every requirement
  // list comes back empty and selection_regret reads null — the same
  // wrong-sheet failure retrieval-bench hit as a clean 0.000 CI (rule 6).
  let ranSample = SAMPLE;
  try { ranSample = JSON.parse(readFileSync(resolve(dir, 'meta.json'), 'utf8')).sample || SAMPLE; } catch {}
  try {
    for (const s of readSample(ranSample)) {
      reportById.set(String(s.id), resolve(PROJECT, s.report));
      roleById.set(String(s.id), s.role || '');
    }
  } catch { /* no sample (unit tests use a fixture tree) — reqs stay empty */ }
  const reportFor = (d) => reportById.get(d.split('_')[0]) || '';

  const rows = [];
  for (const d of readdirSync(dir)) {
    if (keep && !keep.has(d)) continue;
    const f = join(dir, d, 'cv-content.json');
    if (!existsSync(f)) continue;
    /** @type {any} */
    let c;
    try { c = JSON.parse(readFileSync(f, 'utf8')); } catch { continue; }
    const exp = Array.isArray(c.experience) ? c.experience : [];

    const fabNums = [];
    let groundSum = 0, groundN = 0, copied = 0, unmatched = 0, numKept = 0, numLost = 0;
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
          // Which cv.md bullet was this rewritten from — argmax of the same
          // overlap `grounding` already trusts. `overlap` is asymmetric
          // (|b ∩ cb| / |b|), so a truncated bullet still scores near 1 against
          // its own source, which is exactly the case being measured.
          let src = '', bestOv = 0;
          for (const cb of match.bullets) {
            const o = overlap(b, cb);
            if (o > bestOv) { bestOv = o; src = cb; }
          }
          groundSum += bestOv;
          groundN++;
          // metric_fab counts numbers the model invented. Nothing counted the
          // ones it deleted, and truncating to the first clause drops the
          // quantified outcome — "cutting configuration time from 2+ hours to
          // 30 minutes" becomes "authored documentation" and every guard passes.
          const has = numsOf(b);
          for (const num of numsOf(src)) (has.has(num) ? numKept++ : numLost++);
        }
      }
    }
    // Everything that reaches the PDF, as one blob. Modules and skills are
    // code-derived (Tier 3), but they ship, so a fabricated product there counts
    // exactly as much as one in a bullet.
    //
    // Two corrections, and they very nearly cancelled — which is why neither was
    // visible. Core Competencies was deleted from the template during the
    // one-page work but kept being scored, inflating every number by 0.009; and
    // project *bullets*, the field batch/CLAUDE.md calls the one that carries the
    // differentiators, were never read at all, deflating them by 0.008. Offsetting
    // errors are luck, not correctness: the phantom section is now gone for good,
    // so the inflation would not have offset anything again.
    const outputText = [
      c.summary || '',
      (c.projects || []).map(p =>
        `${p.name || ''} ${p.description || ''} ${(p.bullets || []).join(' ')} ${p.tech || ''} ${p.url || ''}`).join(' '),
      (c.education_modules || []).join(' '),
      (c.skills || []).map(s => `${s.category || ''} ${s.items || ''}`).join(' '),
      exp.map(e => (e.bullets || []).join(' ')).join(' '),
    ].join('\n');
    const jdText = readSafe(join(dir, d, 'job-description.txt'));
    const fabProducts = productFab(outputText, cvText);
    const ats = atsCoverage(jdText, cvText, outputText);
    const skill = skillCoverage(jdText, cvText, outputText);
    // Block B is what cv-select ranked against, so selection_regret has to be
    // scored against the same requirements the selector actually saw. The bench
    // dir is named `<id>_<slug>`, which is enough to find the offer's report.
    const reqs = extractBlockBRequirements(readSafe(reportFor(d)));

    rows.push({
      dir: d,
      role: roleById.get(d.split('_')[0]) || '',
      roles: exp.length,
      products: fabProducts,
      product_fab: fabProducts.length,
      ats_coverage: ats.coverage,
      ats_supportable: ats.supportable,
      skill_coverage: skill.coverage,
      skills_asked: skill.asked,
      skills_missed: skill.missed,
      summary: c.summary || '',
      // Shipped vs as-written. `_summary_pre_guard` is only present on runs made
      // after it was added; older runs fall back to the shipped summary, which
      // understates their raw count rather than inventing one.
      summary_fab: summaryFab(c.summary || '', cvText).length,
      summary_fab_raw: summaryFab(c._summary_pre_guard ?? c.summary ?? '', cvText).length,
      summary_fab_kinds: summaryFab(c._summary_pre_guard ?? c.summary ?? '', cvText).join('+'),
      has_pre_guard: typeof c._summary_pre_guard === 'string',
      // Shape, on the same shipped/raw split as fabrication and for the same
      // reason (rule 5): nothing repairs shape today, so the two agree — but the
      // guard that will is the point of measuring this, and a shipped-only
      // metric would read 0 the moment it lands.
      summary_shape: summaryShape(c.summary || '').length,
      summary_shape_raw: summaryShape(c._summary_pre_guard ?? c.summary ?? '').length,
      summary_shape_kinds: summaryShape(c._summary_pre_guard ?? c.summary ?? '').join('+'),
      reqs,
      // Structured, not just folded into outputText: the label metrics have to
      // ask which project blurb an atom survived into, not merely whether its
      // words appear somewhere on the page.
      projects: Array.isArray(c.projects) ? c.projects : [],
      outBullets: exp.flatMap(e => e.bullets || []),
      jdText,
      outputText,
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
      // ponytail: a CV whose source bullets carry no figures scores 1 — nothing
      // to lose. num_lost is the absolute counterpart, immune to that.
      num_retention: numKept + numLost ? numKept / (numKept + numLost) : 1,
      num_lost: numLost,
    });
  }

  const n = rows.length || 1;
  const mean = k => rows.reduce((a, r) => a + r[k], 0) / n;
  // Skips the offers a metric could not score, rather than averaging their nulls
  // in as zeros — a posting that names no skill has no coverage to report and
  // must not drag the mean down as though it were a miss.
  const meanOf = (k) => {
    const got = rows.filter(r => typeof r[k] === 'number');
    return got.length ? got.reduce((a, r) => a + r[k], 0) / got.length : null;
  };
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
    num_retention: +mean('num_retention').toFixed(3),
    num_lost: +mean('num_lost').toFixed(2),
    product_fab: +mean('product_fab').toFixed(3),
    product_fab_pct: +(rows.filter(r => r.product_fab > 0).length / n).toFixed(3),
    // Leaked past the guards — any non-zero is a guard regression, not a model
    // result. metric_fab reads only experience bullets and sat at a clean 0
    // through every run in which a summary claimed Russell Group membership.
    summary_fab_pct: +(rows.filter(r => r.summary_fab > 0).length / n).toFixed(3),
    // How often the model fabricates, before repair. This is the one to move.
    summary_fab_raw_pct: +(rows.filter(r => r.summary_fab_raw > 0).length / n).toFixed(3),
    // Runs predating _summary_pre_guard cannot answer the raw question; say so
    // rather than letting a 0 read as good news.
    summary_fab_raw_n: rows.filter(r => r.has_pre_guard).length,
    // Shape defects per offer, and how many offers carry any. The mean is the
    // one to move: an offer can fail on run_on and off_band at once, and fixing
    // only the band would leave `_pct` flat while the summary is still a
    // run-on.
    summary_shape_defects: +mean('summary_shape_raw').toFixed(3),
    summary_shape_pct: +(rows.filter(r => r.summary_shape_raw > 0).length / n).toFixed(3),
    // Leaked past the guards, once there are any. Non-zero is a guard
    // regression, exactly as with summary_fab_pct.
    summary_shape_shipped_pct: +(rows.filter(r => r.summary_shape > 0).length / n).toFixed(3),
    ats_coverage: +mean('ats_coverage').toFixed(3),
    // The one to target. See `skillCoverage` for why `ats_coverage` is not.
    skill_coverage: meanOf('skill_coverage') === null ? null : +meanOf('skill_coverage').toFixed(3),
    skill_coverage_n: rows.filter(r => typeof r.skill_coverage === 'number').length,
    skills_asked: +mean('skills_asked').toFixed(1),
    mean_bullets: +mean('bullets').toFixed(2),
    rows,
  };
}

// ── Embedding-backed metrics ──────────────────────────────────────────────────
// These three need vectors, so unlike the text metrics they cannot run offline.
// They stay deterministic (greedy embedder, cached by content hash), and the
// cache means re-scoring a run costs nothing after the first pass.

const EMBED_CACHE = resolve(BENCH, 'metric-embeds.json');

/** Content-hashed embedding cache. Keyed by text alone — the embedder is pinned. */
async function embedCached(texts, ollamaUrl) {
  let cache = {};
  try { cache = JSON.parse(readFileSync(EMBED_CACHE, 'utf8')); } catch {}
  const key = t => createHash('sha1').update(t).digest('hex').slice(0, 16);
  const missing = [...new Set(texts.filter(t => t && !cache[key(t)]))];
  if (missing.length) {
    // Batched, but in chunks — one 200-item embed request is a context risk on
    // a 0.6B model and a single failure loses the whole batch.
    for (let i = 0; i < missing.length; i += 32) {
      const chunk = missing.slice(i, i + 32);
      const vecs = await embed(chunk, { ollamaUrl });
      chunk.forEach((t, j) => { cache[key(t)] = vecs[j]; });
    }
    mkdirSync(dirname(EMBED_CACHE), { recursive: true });
    writeFileSync(EMBED_CACHE, JSON.stringify(cache), 'utf8');
  }
  return texts.map(t => (t ? cache[key(t)] : null));
}

/**
 * The `cv.md` atoms Phase 3 selects *from* — experience bullets and project
 * entries. Deliberately the same corpus the gold set labels, so selection_regret
 * and the gold-set pair accuracy are talking about the same 14 things.
 * @param {string} cvText
 */
export function selectableAtoms(cvText) {
  const out = [];
  for (const sec of parseCvSections(cvText)) {
    if (sec.name !== 'Experience' && sec.name !== 'Projects') continue;
    for (const e of parseEntries(sec.lines).entries) {
      const name = e.head[0].replace(/^###\s+/, '').trim();
      for (const b of e.bullets) out.push({ text: b, section: sec.name, entity: name });
    }
  }
  return out;
}

/**
 * How much worse the shipped selection is than the best possible pick of the
 * same size, against this offer's Block B requirements. 0 = optimal.
 *
 * Normalised by the oracle's total so offers with weak requirement overlap do
 * not dominate the mean — the question is "did it pick well *here*", not "is
 * this a good offer".
 *
 * @param {{text:string}[]} atoms         every selectable cv.md atom
 * @param {number[][]} atomVecs
 * @param {number[][]} reqVecs            Block B requirement vectors
 * @param {Set<number>} shippedIdx        indices of the atoms that reached the CV
 */
export function selectionRegret(atoms, atomVecs, reqVecs, shippedIdx) {
  if (!reqVecs.length || !shippedIdx.size) return null;
  // An atom's worth is its best match against any single requirement — a bullet
  // that nails one requirement earns its slot even if it is irrelevant to the rest.
  const score = atomVecs.map(v => (v ? Math.max(...reqVecs.map(r => cosine(v, r))) : 0));
  const k = shippedIdx.size;
  const oracle = [...score].sort((a, b) => b - a).slice(0, k).reduce((a, b) => a + b, 0);
  const got = [...shippedIdx].reduce((a, i) => a + score[i], 0);
  if (oracle <= 0) return null;
  return Math.max(0, (oracle - got) / oracle);
}

/**
 * Which `cv.md` atoms a set of output bullets came from, by token overlap.
 * Phase 3 rewrites wording, so the mapping is fuzzy by nature; a floor keeps a
 * fully-invented bullet from being credited to whichever atom it least resembles.
 */
export function shippedAtomIndices(outputBullets, atoms, floor = 0.25) {
  const idx = new Set();
  for (const b of outputBullets) {
    let best = -1, bestScore = floor;
    atoms.forEach((a, i) => {
      const s = overlap(b, a.text);
      if (s > bestScore) { bestScore = s; best = i; }
    });
    if (best >= 0) idx.add(best);
  }
  return idx;
}

/**
 * Attach selection_regret and the two summary alignments to a metrics object.
 * Split from metricsFor so the text-only metrics stay runnable with no Ollama.
 */
async function withEmbedMetrics(m, { ollamaUrl = 'http://localhost:11434', cvPath = resolve(PROJECT, 'cv.md'), reportsFor = null } = {}) {
  const cvText = readFileSync(cvPath, 'utf8');
  const atoms = selectableAtoms(cvText);
  // cv_fit is measured against the CV's *evidence*, not the whole file — the
  // contact header and education boilerplate would drag every offer's cosine
  // toward the same constant and flatten the metric.
  const cvBody = atoms.map(a => a.text).join('\n');

  const texts = [cvBody, ...atoms.map(a => a.text)];
  for (const r of m.rows) { texts.push(r.summary, r.jdText); for (const q of (r.reqs || [])) texts.push(q); }
  const vecs = await embedCached(texts.filter(Boolean), ollamaUrl);
  const byText = new Map();
  texts.filter(Boolean).forEach((t, i) => byText.set(t, vecs[i]));
  const V = t => (t ? byText.get(t) : null);

  const atomVecs = atoms.map(a => V(a.text));
  const cvVec = V(cvBody);

  for (const r of m.rows) {
    const sv = V(r.summary), jv = V(r.jdText);
    r.summary_jd_fit = sv && jv ? cosine(sv, jv) : 0;
    r.summary_cv_fit = sv && cvVec ? cosine(sv, cvVec) : 0;
    const reqVecs = (r.reqs || []).map(q => V(q)).filter(Boolean);
    r.selection_regret = selectionRegret(atoms, atomVecs, reqVecs, shippedAtomIndices(r.outBullets || [], atoms));
  }

  const n = m.rows.length || 1;
  const mean = k => m.rows.reduce((a, r) => a + (r[k] || 0), 0) / n;
  const scored = m.rows.filter(r => r.selection_regret !== null && r.selection_regret !== undefined);
  return {
    ...m,
    summary_jd_fit: +mean('summary_jd_fit').toFixed(3),
    summary_cv_fit: +mean('summary_cv_fit').toFixed(3),
    selection_regret: scored.length
      ? +(scored.reduce((a, r) => a + r.selection_regret, 0) / scored.length).toFixed(3)
      : null,
    selection_regret_n: scored.length,
  };
}

/**
 * Attach page geometry: how tall each offer's CV actually renders, and whether
 * it fits on one page.
 *
 * Every other metric in this file scores `cv-content.json`, which
 * `local-pdf-offer.mjs` writes and then exits on in bench mode — deliberately,
 * so the density ladder cannot flatter a generation change. The consequence is
 * that the whole suite was blind to the page: an arm that shipped twice as much
 * content scored identically to one that fit, and a one-page experiment would
 * have read as a clean null across every arm (benchmark rule 5 — ask what the
 * metric reads if the change lands).
 *
 * Measured, not simulated. The content JSON goes through the real
 * `fill-cv-template.mjs` and is laid out by the real browser at the real content
 * box, so the number cannot drift from what `generate-pdf.mjs` would print. One
 * browser for the whole run, not one per offer.
 *
 * `pages` is fractional on purpose. A boolean "fits" cannot tell an arm that
 * overran by two lines from one that overran by a page, and the difference is
 * the whole question during the one-page work.
 */
// `maxSkills` defaults to null to match LADDER step 0, the step production
// renders at unless the page overruns. A 6 here measured a document the pipeline
// does not produce: cv-writers keeps a category the posting named even past the
// sixth, so the bench would have rendered 6 rows while the PDF carried 8.
async function withPageMetrics(m, { benchRoot = BENCH, label = '', maxSkills = null } = {}) {
  if (!m.rows.length) return m;
  let chromium;
  try { ({ chromium } = await import('playwright')); }
  catch { return { ...m, pages: null, page_note: 'playwright unavailable' }; }
  const { CONTENT_BOX } = await import(pathToFileURL(resolve(PROJECT, 'generate-pdf.mjs')).href);

  const tmp = mkdtempSync(join(tmpdir(), 'snipe-pages-'));
  const fill = resolve(PROJECT, 'batch/fill-cv-template.mjs');
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage();
    await page.setViewportSize({ width: CONTENT_BOX.width, height: 1200 });
    for (const r of m.rows) {
      const src = join(benchRoot, label, r.dir, 'cv-content.json');
      const html = join(tmp, `${r.dir}.html`);
      try {
        const a = [fill, '--content', src, '--output', html];
        if (maxSkills) a.push('--max-skills', String(maxSkills));
        if (r.role) a.push('--role', r.role);
        execFileSync(process.execPath, a, { stdio: 'ignore', cwd: PROJECT });
        await page.goto(pathToFileURL(html).href, { waitUntil: 'load' });
        r.page_px = await page.evaluate(() =>
          Math.round(document.querySelector('.page').getBoundingClientRect().height));
        r.pages = +(r.page_px / CONTENT_BOX.height).toFixed(3);
        r.fits_one_page = r.pages <= 1 ? 1 : 0;
      } catch { r.page_px = null; r.pages = null; r.fits_one_page = null; }
    }
  } finally {
    await browser.close();
    rmSync(tmp, { recursive: true, force: true });
  }

  const got = m.rows.filter(r => typeof r.pages === 'number');
  if (!got.length) return { ...m, pages: null, page_note: 'no offer rendered' };
  const avg = k => +(got.reduce((a, r) => a + r[k], 0) / got.length).toFixed(3);
  return {
    ...m,
    pages: avg('pages'),
    page_px: Math.round(got.reduce((a, r) => a + r.page_px, 0) / got.length),
    // The gate for the one-page work. A mean page count says nothing on its own:
    // 30 offers fitting and 2 overrunning is not "1.05 pages", it is a failure.
    one_page_rate: +(got.filter(r => r.fits_one_page).length / got.length).toFixed(3),
    one_page_n: got.filter(r => r.fits_one_page).length,
    pages_n: got.length,
  };
}

/**
 * Attach the label-scored metrics, for the offers that have a label.
 *
 * Kept separate from the label-free suite rather than merged into it: those
 * metrics are checkable against source text with no judgement in the loop, and
 * that property is what makes them trustworthy. These are only as good as
 * `batch/bench/opus/labels/` and say so — `labelled_n` is reported alongside so a
 * run scored against six labels cannot be read as one scored against thirty-two.
 */
function withLabelMetrics(m) {
  const labels = loadLabels();
  if (!labels.size) return m;
  let scored = 0;
  for (const r of m.rows) {
    const label = labels.get(String(r.dir).split('_')[0]);
    if (!label) continue;
    Object.assign(r, scoreOffer(label, {
      experience: (r.outBullets || []).length ? [{ bullets: r.outBullets }] : [],
      // metricsFor keeps the joined output text but not the structured
      // projects, and 24 of this CV's 33 atoms are project bullets — scoring
      // without them would report every project differentiator as lost.
      projects: r.projects || [],
      summary: r.summary,
    }));
    scored++;
  }
  const mean = (k) => {
    const v = m.rows.map(r => r[k]).filter(x => typeof x === 'number' && Number.isFinite(x));
    return v.length ? +(v.reduce((a, b) => a + b, 0) / v.length).toFixed(3) : null;
  };
  return {
    ...m,
    labelled_n: scored,
    differentiator_coverage: mean('differentiator_coverage'),
    differentiators_lost: mean('differentiators_lost'),
    noise_rate: mean('noise_rate'),
    grade_yield: mean('grade_yield'),
    mean_grade: mean('mean_grade'),
  };
}

/**
 * All metrics. `--no-embed` keeps the text-only ones runnable with Ollama down;
 * `--no-pages` skips the browser, which is the only part that needs one.
 */
async function allMetrics(label, noEmbed = false, keep = null, noPages = false) {
  const m = metricsFor(label, keep ? { keep } : {});
  // A split run is not a weak result, it is two runs averaged together. Refuse
  // rather than report, because the number looks entirely normal.
  if (m.meta?.split_run) {
    throw new Error(`run "${label}" is a mongrel: HEAD moved from `
      + `${String(m.meta.commit).slice(0, 8)} to ${String(m.meta.commit_end).slice(0, 8)} during it. `
      + `Re-run the affected offers (run <label> --resume after deleting them) rather than scoring this.`);
  }
  const withPages = noPages ? m : await withPageMetrics(m, { label });
  return withLabelMetrics(noEmbed ? withPages : await withEmbedMetrics(withPages));
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
  // Tailoring only ever runs above the Phase 3 threshold, so a sample spanning
  // 1.0–5.0 spent most of its budget on offers that would never be applied to.
  const s = buildSample(n, {}, parseFloat(String(flag('min-score', '3.5'))));
  const SAMPLE = resolve(BENCH, String(flag('out', 'sample.tsv')));
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
    summaryModel: String(flag('summary-model', 'snipe-eval')),
    limit: parseInt(String(flag('limit', '0')), 10),
    writer: String(flag('writer', 'model')),
    // rule 6: retrieval-bench reported a clean-looking null result for a whole
    // sheet because it had no --sheet flag and silently loaded the wrong one.
    // A second sample file gets a flag from the start.
    samplePath: resolve(BENCH, String(flag('sample', 'sample.tsv'))),
    resume: rest.includes('--resume'),
  }), null, 2));
} else if (cmd === 'metrics') {
  const m = await allMetrics(positional[0], rest.includes('--no-embed'), null, rest.includes('--no-pages'));
  const { rows, ...summary } = m;
  console.log(JSON.stringify(summary, null, 2));
  const pct = (x) => (typeof x === 'number' ? x.toFixed(2) : '-');
  // A bare dash when the offer has no label — "-(--)" reads as a number that
  // went wrong rather than as a question that was never asked.
  const diffCol = (r) => (typeof r.differentiator_coverage === 'number'
    ? `${r.differentiator_coverage.toFixed(2)}(-${r.differentiators_lost})` : '-');
  // Pages first: during the one-page work it is the gate, and a row that does not
  // fit is not improved by whatever its other columns say.
  const pgCol = (r) => (typeof r.pages === 'number' ? `${r.pages.toFixed(2)}${r.fits_one_page ? '' : '!'}` : '-');
  if (rest.includes('--rows')) for (const r of rows) console.log(`  ${r.dir}  pg=${pgCol(r)} diff=${diffCol(r)} noise=${pct(r.noise_rate)} yield=${pct(r.grade_yield)} roles=${r.roles} fab=${r.fab} copied=${r.copied} g=${r.grounding.toFixed(2)} num=${r.num_retention.toFixed(2)}(-${r.num_lost}) pfab=${r.product_fab} skill=${r.skill_coverage == null ? '-' : r.skill_coverage.toFixed(2)} ats=${r.ats_coverage.toFixed(2)} reg=${r.selection_regret == null ? '-' : r.selection_regret.toFixed(2)} sfab=${r.summary_fab}/${r.summary_fab_raw}${r.summary_fab_kinds ? `(${r.summary_fab_kinds})` : ''} shape=${r.summary_shape_kinds || '-'}  ${r.products.join(',') || ''}`);
} else if (cmd === 'paired') {
  // `compare` prints two means and their difference, which is exactly the shape
  // of evidence the retrieval work had to stop trusting: a dozen variants against
  // a dozen offers will always produce a winner. These runs are paired by
  // construction — same offers, same selection, one changed component — so the
  // per-offer delta is available and there is no excuse for reporting only its
  // mean. bootstrapCI and signTest are retrieval-bench's, unchanged, so both
  // benchmarks answer "is this real" the same way.
  const [a, b] = positional;
  const { bootstrapCI, signTest } = await import('./stats.mjs');
  const noPages = rest.includes('--no-pages');
  let A = await allMetrics(a, rest.includes('--no-embed'), null, noPages);
  let B = await allMetrics(b, rest.includes('--no-embed'), null, noPages);
  const common = A.rows.map(r => r.dir).filter(d => B.rows.some(r => r.dir === d));
  const byDir = (m) => new Map(m.rows.map(r => [r.dir, r]));
  const [ra, rb] = [byDir(A), byDir(B)];

  // Display name → the key the per-offer row actually carries. metricsFor names
  // several of them differently in the row than in the summary (`fab` becomes
  // `metric_fab`, `bullets` becomes `mean_bullets` once averaged), and reading
  // the summary name off a row silently yields undefined — which this reported
  // as "no paired data" rather than as a wrong number, but reported all the same.
  const keys = [
    ['differentiator_coverage', 'differentiator_coverage'], ['noise_rate', 'noise_rate'],
    ['grade_yield', 'grade_yield'], ['mean_grade', 'mean_grade'],
    ['skill_coverage', 'skill_coverage'],
    ['ats_coverage', 'ats_coverage'], ['grounding', 'grounding'],
    ['num_retention', 'num_retention'], ['num_lost', 'num_lost'],
    ['metric_fab', 'fab'], ['product_fab', 'product_fab'],
    ['summary_cv_fit', 'summary_cv_fit'], ['summary_jd_fit', 'summary_jd_fit'],
    ['selection_regret', 'selection_regret'], ['mean_bullets', 'bullets'],
  ];
  console.log(`paired on ${common.length} offers · ${a} → ${b}\n`);
  console.log(`${'metric'.padEnd(24)}${a.padEnd(10)}${b.padEnd(10)}${'delta'.padEnd(9)}${'CI95'.padEnd(20)}w-l    p`);
  console.log('-'.repeat(24 + 10 + 10 + 9 + 20 + 12));
  for (const [label, k] of keys) {
    // Only offers where BOTH runs produced the metric. A null on one side is not
    // a zero, and pairing it against a number would invent a delta.
    const deltas = [], av = [], bv = [];
    for (const d of common) {
      const x = ra.get(d)?.[k], y = rb.get(d)?.[k];
      if (typeof x !== 'number' || typeof y !== 'number' || !Number.isFinite(x) || !Number.isFinite(y)) continue;
      av.push(x); bv.push(y); deltas.push(y - x);
    }
    if (!deltas.length) { console.log(`${label.padEnd(24)}${'-'.padEnd(49)}no paired data`); continue; }
    const mean = (v) => v.reduce((p, q) => p + q, 0) / v.length;
    const ci = bootstrapCI(deltas);
    const { pos, neg, p } = signTest(deltas);
    const sig = ci.lo > 0 || ci.hi < 0 ? ' *' : '';
    console.log(`${label.padEnd(24)}${mean(av).toFixed(3).padEnd(10)}${mean(bv).toFixed(3).padEnd(10)}`
      + `${(ci.mean >= 0 ? '+' : '') + ci.mean.toFixed(3)}`.padEnd(9)
      + `[${ci.lo.toFixed(3)}, ${ci.hi.toFixed(3)}]`.padEnd(20)
      + `${pos}-${neg}`.padEnd(7) + `${p < 0.001 ? '<0.001' : p.toFixed(3)}${sig}`);
  }
  console.log(`\n  n varies per metric — offers where either run returned null are dropped, not zeroed.`);
  console.log(`  * = bootstrap CI95 excludes 0.`);
} else if (cmd === 'compare') {
  const [a, b] = positional;
  const noEmbed = rest.includes('--no-embed');
  const noPages = rest.includes('--no-pages');
  let A = await allMetrics(a, noEmbed, null, noPages), B = await allMetrics(b, noEmbed, null, noPages);
  // Compare only the offers BOTH runs produced. A --limit run holds a prefix of
  // the sample, so without this the means are taken over different offer sets
  // and the "delta" is mostly the difference between those sets, not the change.
  const common = new Set(A.rows.map(r => r.dir).filter(d => B.rows.some(r => r.dir === d)));
  if (common.size !== A.rows.length || common.size !== B.rows.length) {
    console.log(`paired on ${common.size} offers common to both runs `
      + `(${a}: ${A.rows.length}, ${b}: ${B.rows.length})\n`);
    A = await allMetrics(a, noEmbed, common, noPages);
    B = await allMetrics(b, noEmbed, common, noPages);
  }
  const keys = ['n', 'role_retention', 'all_roles_pct', 'invented_roles', 'metric_fab', 'fab_offers_pct',
                'example_copy_pct', 'grounding', 'num_retention', 'num_lost',
                'product_fab', 'product_fab_pct', 'skill_coverage', 'skills_asked', 'ats_coverage',
                'summary_fab_pct', 'summary_fab_raw_pct', 'summary_fab_raw_n',
                'summary_jd_fit', 'summary_cv_fit', 'selection_regret', 'mean_bullets',
                'labelled_n', 'differentiator_coverage', 'differentiators_lost',
                'noise_rate', 'grade_yield', 'mean_grade'];
  const pad = (s, w) => String(s).padEnd(w);
  const w = Math.max(14, a.length + 2, b.length + 2);
  console.log(`${pad('metric', 18)}${pad(a, w)}${pad(b, w)}delta`);
  console.log('-'.repeat(18 + 2 * w + 7));
  for (const k of keys) {
    const d = typeof A[k] === 'number' && typeof B[k] === 'number' ? (B[k] - A[k]).toFixed(3) : '';
    console.log(`${pad(k, 18)}${pad(A[k], w)}${pad(B[k], w)}${d}`);
  }
} else {
  console.log('usage: sample --n 24 | run <label> [--temperature 0] [--model M] [--summary-model M] [--limit N] [--resume] | metrics <label> [--rows] [--no-embed] [--no-pages] | compare <a> <b> [--no-embed]');
}

export { buildSample, cvExperience, metricsFor, numsOf, shingles, exampleShingles, withEmbedMetrics, allMetrics };
