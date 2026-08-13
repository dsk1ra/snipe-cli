#!/usr/bin/env node
// @ts-check
/**
 * staged-evaluator.mjs — Phase 2 as three small, schema-constrained calls
 * instead of one monolithic report generation.
 *
 *   Stage 1  JD parse        → company/role/seniority/requirements/keywords (JSON)
 *   Stage 2  evidence match  → each requirement vs top-3 embedded CV atoms; the
 *                              model answers same_activity/same_tooling (JSON)
 *                              and code derives Strong/Transferable/Gap plus the
 *                              coverage metric
 *   Stage 3  judgment        → dims + strategy + personalisation + STAR stories
 *                              + legitimacy (JSON), grounded in the stage-2
 *                              evidence table + calibration from similar past
 *                              offers (embedding RAG over batch/jds)
 *
 * The full A–G report is assembled IN CODE from the structured outputs — the
 * model never writes free-form markdown, so template echo is impossible and
 * every field is grammar-guaranteed.
 *
 * Drop-in replacement for ollama-evaluator.mjs (same CLI, same output JSON,
 * same report layout). Select it in local-runner.sh with --staged-eval (default).
 *
 * Usage:
 *   node batch/staged-evaluator.mjs --id N --url URL --report-num NNN
 *     [--model snipe-screen] [--ollama-url URL] [--threshold 3.0]
 *     [--bench-dir DIR] [--no-rag]
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { stackMismatchCap, seniorityCaps, languageMismatchCap, locationMismatchCap, looksMultiPosting, strengthFrom, verifyAgainstCv } from './fit-rules.mjs';
import {
  cleanCvForPrompt, cleanJd, extractSalary, parseCompTargets,
  compScoreFromSalary, buildCompBlock,
} from './text-utils.mjs';
import { loadCvIndex, embed, topK, similarPastOffers } from './embeddings.mjs';
import { logCall } from './timing.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT_DIR = resolve(__dirname, '..');
let REPORTS_DIR = resolve(PROJECT_DIR, 'reports');
let EVALS_DIR = resolve(__dirname, 'evals');

// How much the deterministic coverage metric contributes to the cv dimension.
// coverage is continuous, so this is what breaks the integer-bucket collapse.
const COVERAGE_BLEND = 0.4;

// ── Args ──────────────────────────────────────────────────────────────────────

function parseArgs(argv) {
  const args = {
    id: null, url: null, reportNum: null,
    model: 'snipe-screen', ollamaUrl: 'http://localhost:11434',
    timeout: 300_000, threshold: 3.0, rag: true, benchDir: null,
  };
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--id':          args.id = argv[++i]; break;
      case '--url':         args.url = argv[++i]; break;
      case '--report-num':  args.reportNum = argv[++i]; break;
      case '--model':       args.model = argv[++i]; break;
      case '--ollama-url':  args.ollamaUrl = argv[++i]; break;
      case '--timeout':     args.timeout = parseInt(argv[++i], 10) * 1000; break;
      case '--threshold':   args.threshold = parseFloat(argv[++i]); break;
      case '--bench-dir':   args.benchDir = argv[++i]; break;
      case '--no-rag':      args.rag = false; break;
    }
  }
  if (!args.id)        fatal('--id is required');
  if (!args.url)       fatal('--url is required');
  if (!args.reportNum) fatal('--report-num is required');
  if (args.benchDir) {
    REPORTS_DIR = resolve(args.benchDir, 'reports');
    EVALS_DIR = resolve(args.benchDir, 'evals');
  }
  return args;
}

function fatal(msg) {
  process.stdout.write(JSON.stringify({ status: 'eval_failed', error: msg }) + '\n');
  process.exit(1);
}

function readSafe(p) {
  try { return existsSync(p) ? readFileSync(p, 'utf8').trim() : ''; } catch { return ''; }
}

function slugify(str) {
  return str.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40);
}

const clampDim = (v) => {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(5, Math.max(1, n)) : null;
};

// ── Ollama (schema-constrained chat) ──────────────────────────────────────────

async function ollamaJson({ baseUrl, model, system, user, schema, numPredict, timeoutMs, temperature = 0, numCtx = 8192, label = 'p2' }) {
  // One retry. Two different failure modes need two different retries:
  //   done_reason=stop   → a bad sample; re-roll at a slightly higher temperature.
  //   done_reason=length → the answer did not FIT; re-rolling the same budget just
  //                        truncates again (observed: offer #38 hit 3000/3000 twice
  //                        and failed the eval), so give it more room instead.
  let lastErr, truncated = false, promptTokens = 0;
  for (let attempt = 0; attempt < 2; attempt++) {
    // On a length retry, spend everything the context actually has left rather
    // than a guessed multiple — attempt 0 reports the real prompt size, so the
    // budget can be exact instead of risking a context overflow.
    const budget = truncated
      ? Math.max(numPredict, Math.min(Math.round(numPredict * 1.5), numCtx - promptTokens - 256))
      : numPredict;
    const res = await fetch(`${baseUrl}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model,
        messages: [{ role: 'system', content: system }, { role: 'user', content: user }],
        stream: false,
        format: schema,
        options: {
          temperature: attempt === 0 || truncated ? temperature : temperature + 0.15,
          num_ctx: numCtx,
          num_predict: budget,
        },
      }),
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!res.ok) throw new Error(`Ollama HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
    const data = await res.json();
    logCall(label, model, data, { extra: `attempt=${attempt}` });
    const content = data?.message?.content || '';
    // Context-budget telemetry: prompt + output must fit num_ctx, and a prompt
    // that grows past it silently truncates the answer instead of erroring.
    if (process.env.SNIPE_DEBUG) {
      process.stderr.write(`[ctx] prompt=${data?.prompt_eval_count} out=${data?.eval_count}/${budget} done=${data?.done_reason} attempt=${attempt}\n`);
    }
    try { return JSON.parse(content); }
    catch (e) {
      truncated = data?.done_reason === 'length';
      promptTokens = Number(data?.prompt_eval_count) || 0;
      lastErr = new Error(`invalid JSON (${content.length} chars, done_reason=${data?.done_reason}, budget=${budget}): ${content.slice(-80)}`);
    }
  }
  throw lastErr;
}

// ── Stage 1: JD parse ─────────────────────────────────────────────────────────

const STAGE1_SCHEMA = {
  type: 'object',
  properties: {
    is_single_posting: { type: 'boolean' },
    company:         { type: 'string' },
    role:            { type: 'string' },
    seniority_level: { type: 'string', enum: ['Junior', 'Mid', 'Senior', 'Staff+', 'Unspecified'] },
    years_required:  { type: 'integer', minimum: 0, maximum: 30 },
    remote_policy:   { type: 'string' },
    location:        { type: 'string' },
    domain:          { type: 'string' },
    requirements:    { type: 'array', minItems: 3, maxItems: 12, items: {
      type: 'object',
      properties: { text: { type: 'string' }, must_have: { type: 'boolean' } },
      required: ['text', 'must_have'],
    } },
    tech_stack:      { type: 'array', items: { type: 'string' }, maxItems: 15 },
    keywords:        { type: 'array', items: { type: 'string' }, minItems: 8, maxItems: 20 },
  },
  required: ['is_single_posting', 'company', 'role', 'seniority_level', 'years_required',
             'remote_policy', 'location', 'domain', 'requirements', 'tech_stack', 'keywords'],
};

async function stage1JdParse(jd, args) {
  const system = [
    'You are a precise job-description parser. Extract structured facts from the JD.',
    'Rules:',
    '- `is_single_posting`: true ONLY if this text is ONE specific job posting from ONE company. False for: hiring threads/aggregator lists with multiple jobs, blog posts, company homepages, news articles, or pages with no actual job description. When false, still fill the other fields as best you can from whatever is there.',
    '- `requirements`: the distinct skills/qualifications the JD actually asks for, one per entry, each a short self-contained phrase. Each must be a CONCRETE, testable demand (a technology, skill, domain, or experience — e.g. "Python + Django for billing systems", "VAT/invoicing domain knowledge", "mentoring mid-level engineers"). Never output section headings, company values, or vague labels like "Backend Excellence". `must_have: true` only for explicitly required items; nice-to-haves get false.',
    '- `years_required`: the explicit years-of-experience demand; 0 if none stated.',
    '- `seniority_level`: from the title and demands; "Unspecified" if genuinely unclear.',
    '- `keywords`: 8-20 ATS keywords present in the JD.',
    '- Copy company/role from the JD text; never invent. Use "unknown" if absent.',
  ].join('\n');
  return ollamaJson({
    baseUrl: args.ollamaUrl, model: args.model, system,
    user: `## Job Description\n${cleanJd(jd, 8000)}`,
    schema: STAGE1_SCHEMA, numPredict: 900, timeoutMs: args.timeout, label: 'p2-parse',
  });
}

// ── Stage 2: evidence matching (embeddings + LLM verify) ──────────────────────

const STAGE2_SCHEMA = {
  type: 'object',
  properties: {
    matches: { type: 'array', items: {
      type: 'object',
      properties: {
        req:      { type: 'integer', minimum: 1 },
        // `pick` first so the model commits to a piece of evidence before it
        // judges it — the axes are then about a fixed line, not a moving target.
        pick:          { type: 'string', enum: ['A', 'B', 'C', 'none'] },
        same_activity: { type: 'boolean' },
        // Three-way, not boolean: plenty of requirements name no technology at
        // all (a degree, communication, mentoring). Forcing a yes/no there made
        // the model answer "no" and silently cost a full match 40% of its weight.
        same_tooling:  { type: 'string', enum: ['same', 'different', 'not_applicable'] },
        note:          { type: 'string' },
      },
      required: ['req', 'pick', 'same_activity', 'same_tooling', 'note'],
    } },
  },
  required: ['matches'],
};

/**
 * @param {any[]} requirements
 * @param {any} args
 * @param {any} [preloadedIndex] the CV index loaded in the embedder block above;
 *   falls back to loading it here so the function stays callable on its own.
 */
async function stage2Evidence(requirements, args, preloadedIndex = null) {
  const cvIndex = preloadedIndex || await loadCvIndex({ ollamaUrl: args.ollamaUrl });
  const reqTexts = requirements.map(r => r.text);
  const reqVecs = await embed(reqTexts, { ollamaUrl: args.ollamaUrl });

  // Measured: topK 6 / 320-char slice cut retrieval starvation hard (pick:"none"
  // 33 -> 21 rows, Gap-despite-tech-in-CV 7 -> 3) but made the model MORE GENEROUS
  // overall — rho 0.522 -> 0.468, pairAcc 78.9% -> 75.7%, mis-grounded 6.3% -> 7.6%,
  // one atom answering 5 of 12 requirements on #34, and #53 overflowed stage 3.
  // More candidates gave it more rope to find a plausible-looking line for every
  // requirement. Kept at 3 deliberately. See batch/bench/retr6.
  const candidates = reqVecs.map(v => topK(v, cvIndex, 3));
  const LETTERS = 'ABC';

  const lines = requirements.map((r, i) => {
    const cands = candidates[i]
      .map((c, j) => `   ${LETTERS[j]}) ${c.text.slice(0, 200)}`)
      .join('\n');
    return `R${i + 1} (${r.must_have ? 'MUST' : 'nice-to-have'}): ${r.text}\n${cands}`;
  }).join('\n\n');

  const system = [
    'You grade how well a candidate\'s CV evidence covers each job requirement.',
    'For each requirement R1..Rn, the top candidate CV lines (A/B/C, retrieved by semantic similarity) are shown.',
    'You answer TWO independent questions per requirement. You do NOT rate strength — the system computes that from your answers.',
    'Rules:',
    '- `pick`: the single best evidence line (A/B/C), or "none" if not one of them is about this requirement at all.',
    '- `same_activity`: is the picked line the SAME KIND OF WORK as the requirement — would you reach for the same mental model and the same debugging steps? Sharing a buzzword ("performance", "scale", "pipeline", "distributed") is NOT enough. Different problem shape or different domain = false.',
    '- `same_tooling`: "same" if the picked line uses the specific technology the requirement names; "different" if it is another tool for the same job; "not_applicable" if the requirement names no technology at all (a degree, communication skills, mentoring, stakeholder management, ways of working).',
    '- Only "different" costs the candidate credit. Do NOT answer "different" just because a requirement has no technology in it — that is what "not_applicable" is for.',
    '- The two fields are independent. Same work with another tool is the normal, useful case: same_activity true, same_tooling "different".',
    '',
    'Worked examples:',
    '- Req "Build REST APIs in Go" vs evidence "built REST APIs in Node.js" → same_activity TRUE, same_tooling "different". (Same work, another language.)',
    '- Req "Strong written and verbal communication skills" vs evidence "Spanish: Fluent (B2 certified)" → same_activity TRUE, same_tooling "not_applicable". (No technology is named; this is a full match, not a partial one.)',
    '- Req "Degree in Computer Science or related discipline" vs evidence "BSc Computer Science" → same_activity TRUE, same_tooling "not_applicable".',
    '- Req "PySpark tuning: data skew, Catalyst optimizer" vs evidence "lock-free ring buffer, zero-copy frames" → same_activity FALSE. (Distributed shuffle optimisation vs single-process memory layout share the word "performance" and nothing else.)',
    '- Req "Kubernetes internals" vs evidence "Skills — Kubernetes (working knowledge)" → same_activity FALSE. (Listing a tool is not working on its internals.)',
    '- Req "Delta Lake / Iceberg lakehouse migration" vs evidence "CQRS with denormalised read views" → same_activity FALSE. (Architectural pattern in common, storage-layer migration work absent.)',
    '',
    '- Similarity retrieval surfaces superficially-similar lines on purpose — it is your job to reject them. When the candidates are all off-topic, "none" is the correct answer, not the closest one.',
    '- `note`: one short clause (max 15 words) justifying the grade.',
    '- Output exactly one entry per requirement, req numbered from 1.',
  ].join('\n');

  const out = await ollamaJson({
    baseUrl: args.ollamaUrl, model: args.model, system,
    user: lines, schema: STAGE2_SCHEMA, numPredict: 1800, timeoutMs: args.timeout, label: 'p2-evidence',
  });

  // Normalize: exactly one entry per requirement, in order.
  const byReq = new Map();
  for (const m of out.matches || []) {
    if (m.req >= 1 && m.req <= requirements.length && !byReq.has(m.req)) byReq.set(m.req, m);
  }
  return requirements.map((r, i) => {
    const m = byReq.get(i + 1) || { pick: 'none', same_activity: false, same_tooling: 'not_applicable', note: 'no grade returned' };
    const pickIdx = LETTERS.indexOf(m.pick);
    const atomText = pickIdx >= 0 ? candidates[i][pickIdx].text : '';
    // SNIPE_SKILLS_CAP=0 runs the pre-cap arm without editing this file
    // mid-benchmark, which would split the run (benchmark rule 4).
    const atomSource = pickIdx >= 0 && process.env.SNIPE_SKILLS_CAP !== '0'
      ? (candidates[i][pickIdx].source || '') : '';
    const strength = strengthFrom(m.pick, m.same_activity === true, m.same_tooling, r.text, atomText, atomSource);
    // A Gap has no evidence to show even when the model picked a line — showing
    // the rejected candidate would read as support for the requirement.
    const shownIdx = strength === 'Gap' ? -1 : pickIdx;
    const atom = shownIdx >= 0 ? candidates[i][shownIdx] : null;
    return {
      requirement: r.text,
      must_have: r.must_have,
      strength,
      same_activity: m.same_activity === true,
      same_tooling: m.same_tooling,
      evidence: atom ? atom.text : '—',
      sim: atom ? +atom.sim.toFixed(3) : null,
      note: m.note || '',
    };
  });
}

function coverageMetric(evidence) {
  // Transferable = same work, different tool. 0.6 is the "how much do I discount
  // a stack I haven't used?" dial — raise it to chase adjacent stacks, lower it
  // to hold out for exact-stack roles. `?? 0` keeps a legacy grade from an older
  // eval turning the whole average into NaN.
  const w = { Strong: 1, Transferable: 0.6, Gap: 0 };
  const avg = pool => pool.reduce((a, e) => a + (w[e.strength] ?? 0), 0) / pool.length;
  const must = evidence.filter(e => e.must_have);
  const nice = evidence.filter(e => !e.must_have);
  if (!evidence.length) return { coverage: 0, mustCount: 0 };
  // 75/25 must/nice split so uncovered nice-to-haves temper a perfect must score
  // (pure must-coverage saturated at 1.0 and pinned strong fits to 5.0).
  const coverage = must.length >= 2
    ? (nice.length ? 0.75 * avg(must) + 0.25 * avg(nice) : avg(must))
    : avg(evidence); // fall back if JD tagged (nearly) everything nice-to-have
  return { coverage: +coverage.toFixed(3), mustCount: must.length };
}

// ── Stage 3: judgment ─────────────────────────────────────────────────────────

// Every free-text string is length-capped. `maxItems` alone does not bound the
// output: the grammar happily decodes ONE string forever, and did — two offers
// failed by looping inside a single `top_strengths` entry, comma-appending CV
// skill fragments for 7128 of 21241 chars until num_predict ran out mid-string.
// The truncated JSON then failed to parse and the whole evaluation was lost.
// maxLength makes the sampler close the quote at the cap instead (verified: a
// capped call returns done_reason=stop and valid JSON where the uncapped one
// returns done_reason=length and a dangling string), so a verbose answer
// degrades to a clipped sentence rather than a lost evaluation.
//
// Limits are ~1.5x the p95 measured over the stored evals and STAR rows, so
// normal output is untouched; only the runaway case is cut. top_strengths is the
// exception — its cap sits just above p95 and deliberately below the observed
// max, because the prompt asks for "short factual phrases" there.
//
// ponytail: keep every cap <= 1500. llama.cpp expands maxLength into that many
// nested optional char rules, and the grammar stops compiling somewhere between
// 1800 and 2000 — the whole call then dies with HTTP 400 "failed to parse
// grammar", which is a worse failure than the one this fixes. The limit is
// per-field, not cumulative (a schema of 16 capped fields compiles fine).
const STAGE3_SCHEMA = {
  type: 'object',
  properties: {
    cv_match:        { type: 'integer', minimum: 1, maximum: 5 },
    north_star:      { type: 'integer', minimum: 1, maximum: 5 },
    red_flags_score: { type: 'integer', minimum: 1, maximum: 5 },
    archetype:       { type: 'string', maxLength: 80 },
    hard_stops:      { type: 'array', items: { type: 'string', maxLength: 200 }, maxItems: 6 },
    soft_gaps:       { type: 'array', items: { type: 'string', maxLength: 240 }, maxItems: 5 },
    top_strengths:   { type: 'array', items: { type: 'string', maxLength: 160 }, maxItems: 3 },
    strategy:        { type: 'array', items: { type: 'string', maxLength: 400 }, minItems: 2, maxItems: 4 },
    personalisation: { type: 'array', minItems: 3, maxItems: 5, items: {
      type: 'object',
      properties: {
        section:  { type: 'string', maxLength: 80 },
        current:  { type: 'string', maxLength: 400 },
        proposed: { type: 'string', maxLength: 400 },
        why:      { type: 'string', maxLength: 300 },
      },
      required: ['section', 'current', 'proposed', 'why'],
    } },
    linkedin:        { type: 'array', items: { type: 'string', maxLength: 300 }, minItems: 2, maxItems: 3 },
    hard_questions:  { type: 'array', minItems: 2, maxItems: 3, items: {
      type: 'object',
      properties: { q: { type: 'string', maxLength: 200 }, a: { type: 'string', maxLength: 800 } },
      required: ['q', 'a'],
    } },
    legitimacy_tier:   { type: 'string', enum: ['High Confidence', 'Proceed with Caution', 'Suspicious'] },
    legitimacy_reason: { type: 'string', maxLength: 400 },
    final_decision:    { type: 'string', enum: ['Apply', 'Research first', 'Consider', 'Skip'] },
    notes:             { type: 'string', maxLength: 1500 },
    // `stories` is injected per offer by storiesSchema().
  },
  required: ['cv_match', 'north_star', 'red_flags_score', 'archetype', 'hard_stops',
             'soft_gaps', 'top_strengths', 'strategy', 'personalisation', 'linkedin',
             'stories', 'hard_questions', 'legitimacy_tier', 'legitimacy_reason',
             'final_decision', 'notes'],
};

/**
 * STAR targets are chosen by CODE, not by the model: `req` is an index into the
 * eligible list, so a story about a requirement the CV does not cover is not
 * merely discouraged — it cannot be encoded.
 *
 * The old schema asked for a free-text `requirement` with minItems 3, while the
 * prompt pointed at "the hardest requirements" — which are exactly the ones
 * graded Gap. That made fabrication the only grammatically valid output, and it
 * duly produced Databricks/PySpark/Delta Lake stories for a CV with none of them.
 * When nothing is covered, an empty Block F is the honest answer.
 */
function storiesSchema(eligibleRows) {
  if (!eligibleRows.length) return { type: 'array', maxItems: 0, items: { type: 'object' } };
  return {
    type: 'array',
    minItems: Math.min(3, eligibleRows.length),
    maxItems: Math.min(5, eligibleRows.length),
    items: {
      type: 'object',
      properties: {
        // An enum of the allowed evidence-table row numbers. Same grammar-level
        // guarantee as a bounded integer, but it indexes the table the prompt
        // already contains — so the targets need not be re-listed.
        req:       { type: 'integer', enum: eligibleRows },
        // Caps from the 417 stored STAR rows (p95 / observed max):
        // story 857/1648 · situation 239/515 · task 162/209 · action 811/1316.
        story:     { type: 'string', maxLength: 1200 },
        situation: { type: 'string', maxLength: 400 },
        task:      { type: 'string', maxLength: 300 },
        action:    { type: 'string', maxLength: 1000 },
        result:    { type: 'string', maxLength: 800 },
      },
      required: ['req', 'story', 'situation', 'task', 'action', 'result'],
    },
  };
}

/** Shared prompt fragments — both calls read the same evidence table. */
function stageContext({ evidence, calibration }) {
  const evidenceTable = evidence.map((e, i) =>
    `${i + 1}. [${e.strength}${e.must_have ? ', MUST' : ''}] ${e.requirement}\n   evidence: ${e.evidence.slice(0, 180)}${e.note ? `\n   note: ${e.note}` : ''}`
  ).join('\n');

  // Only requirements with real retrieved evidence can host a STAR story.
  // Row numbers into the evidence table above, not a second copy of it. Re-listing
  // each target with its evidence cost ~1500-2000 chars of pure duplication, and
  // on the longest JD (#53, prompt 6757) that squeezed the output budget until the
  // eval failed.
  const eligibleRows = evidence.map((e, i) => (e.strength === 'Gap' ? 0 : i + 1)).filter(Boolean);
  const targetLines = eligibleRows.length
    ? `Requirements ${eligibleRows.join(', ')} (numbered as in the evidence table above). No others have supporting CV evidence.`
    : '(none — no requirement has supporting CV evidence, so write no stories)';

  const calibLines = calibration.length
    ? calibration.map(c =>
        `- ${c.company} — ${c.role}: scored ${c.score}/5, decision ${c.decision}${c.user_label != null ? `, user's own rating ${c.user_label}/5` : ''}${c.outcome ? `, real outcome: ${c.outcome}` : ''} (similarity ${c.sim})`
      ).join('\n')
    : '(none available)';

  return { evidenceTable, eligibleRows, targetLines, calibLines };
}

// ── Stage 3: judgment ─────────────────────────────────────────────────────────

// Scoring and the content blocks stay in ONE call. Splitting them (dims first,
// prose second) was measured over 18 hand-labelled offers and collapsed rank
// agreement from 0.578 to 0.101 — and 0.018 when the CV was also withheld. The
// dims decode before any story token exists, so this is not the prose leaking
// into the scores; committing to justify the score with specific CV lines and
// real projects appears to be what keeps the score honest. Do not split it again
// without re-running batch/eval-harness against the labels.
async function stage3Judgment({ jd, parsed, evidence, coverage, calibration, salary, cv, profile, args }) {
  const { evidenceTable, eligibleRows, targetLines, calibLines } = stageContext({ evidence, calibration });

  const system = [
    'You are a job-offer evaluator for a software engineering candidate. A pre-verified evidence table (each JD requirement graded against real CV lines) is provided — treat it as ground truth for what the candidate does and does not have. Never invent experience.',
    '',
    'Scoring (integers 1-5, commit — no hedging toward 3):',
    '- The evidence table grades each requirement Strong (same work, same tools), Transferable (same work, different tools — real but partial credit) or Gap (the candidate does not have this).',
    '- cv_match: 5 = nearly every requirement Strong; 4 = most Strong, 1-2 minor gaps; 3 = about half covered, or broad Transferable coverage with few Strong; 2 = a minority covered; 1 = different stack/domain. Must be CONSISTENT with the evidence table — do not score 4-5 while MUST requirements sit at Gap, and never describe a Gap or Transferable requirement as experience the candidate has.',
    '- north_star: 5 = squarely a primary archetype from the profile at a reachable seniority; 3 = adjacent archetype or seniority stretch; 1 = outside all targets.',
    '- red_flags_score: start at 5, subtract 1 per deal-breaker from the profile that ACTUALLY applies (informational — not scored).',
    '- The SYSTEM computes the composite and applies seniority caps in code; your job is honest dimensions.',
    '- Similar past offers (with the candidate\'s own scores/ratings) are calibration anchors: a materially better fit than a past 3.3 should score above 3.3, a similar one should land nearby. Where a real outcome is given it outranks the past model score: similar offers that were rejected argue for scoring lower; ones that reached interview validate the fit.',
    '',
    '- archetype / hard_stops / soft_gaps / top_strengths: short factual phrases drawn from the evidence table, not prose.',
    '',
    'Content blocks: strategy (positioning without overpromising), personalisation (specific CV changes for THIS role, referencing real CV content), linkedin (headline/summary tweaks), hard_questions (tough interview questions with grounded answers — `a` must be an ANSWER the candidate can give, never another question), legitimacy (is this POSTING genuine and well-specified — description quality, realistic requirements, transparency? It is NOT about candidate fit; a poor fit at a legitimate company is still "High Confidence". Judge ONLY from the JD itself — never invent hiring freezes or company signals).',
    '',
    'STAR stories — the strictest rule here:',
    '- `req` is the NUMBER of a target from the "Story targets" list. That list is the complete set of requirements you may write a story about. Requirements missing from it have no supporting CV evidence.',
    '- Build each story from the evidence shown for that target and from the CV. Do NOT introduce a technology, platform or tool that is absent from the CV — not in situation, task, action or result, and not as something the candidate "would apply".',
    '- Do NOT invent numbers. Percentages, counts and outcomes must appear in the CV; if none is given, describe the result without inventing a metric.',
    '- If the JD demands a technology the candidate lacks, that belongs in soft_gaps or hard_stops. It must never appear inside a story as work they did.',
  ].join('\n');

  const user = [
    `## Role (parsed)`,
    `${parsed.company} — ${parsed.role}`,
    `Seniority: ${parsed.seniority_level}${parsed.years_required ? ` (${parsed.years_required}+ years demanded)` : ''} | Remote: ${parsed.remote_policy} | Location: ${parsed.location} | Domain: ${parsed.domain}`,
    '',
    `## Posted salary`,
    salary ? `${salary.currency}${salary.min.toLocaleString()}–${salary.currency}${salary.max.toLocaleString()} (parsed from JD; comp is scored by the system, not you)` : 'Not stated. Do NOT guess one.',
    '',
    `## Evidence table (requirement → graded CV evidence)`,
    evidenceTable,
    '',
    `Coverage of must-have requirements: ${(coverage.coverage * 100).toFixed(0)}%`,
    '',
    `## Story targets`,
    targetLines,
    '',
    `## Similar past offers (calibration anchors)`,
    calibLines,
    '',
    `## Candidate profile (archetypes, framing, deal-breakers)`,
    profile || '(no profile)',
    '',
    `## Candidate CV (cite real lines only)`,
    cv,
    '',
    `## JD excerpt (context)`,
    cleanJd(jd, 2500),
  ].join('\n');

  const schema = {
    ...STAGE3_SCHEMA,
    properties: { ...STAGE3_SCHEMA.properties, stories: storiesSchema(eligibleRows) },
  };

  // Stage 3 emits every content block at once and was running right at the old
  // 3000-token ceiling (#39 landed at 2633, #38 truncated at 3000 twice and lost
  // the eval). 4096 leaves real headroom; num_ctx 12288 keeps prompt+output
  // inside the window — the pipeline already runs 12k elsewhere, and q8_0 KV
  // makes the extra ~200 MiB fit.
  const out = await ollamaJson({
    baseUrl: args.ollamaUrl, model: args.model, system, user,
    // 0.1 to match stages 1 and 2 — the 0.2 here was inherited, and this call's
    // primary output is a 1-5 judgment, not prose. Lower temperature also shrinks
    // run-to-run variance, which is what makes an 18-offer benchmark readable.
    schema, numPredict: 5120, numCtx: 12288, timeoutMs: args.timeout, temperature: 0, label: 'p2-judgment',
  });

  // Resolve target numbers back to requirement text so report assembly is
  // unchanged. Out-of-range indices are dropped rather than rendered.
  out.stories = (out.stories || [])
    .filter(s => eligibleRows.includes(s.req))
    .map(s => ({ ...s, requirement: evidence[s.req - 1].requirement }));
  return out;
}

// ── Report assembly (all markdown written by code) ────────────────────────────

function assembleReport({ args, today, parsed, evidence, coverage, judgment, salary, compDim, compTargets, score, preScore, pdfNote, machineSummary, calibration }) {
  const md = [];
  md.push(`# Evaluation: ${parsed.company} — ${parsed.role}`, '');
  md.push(`**Date:** ${today}`);
  md.push(`**Archetype:** ${judgment.archetype}`);
  md.push(`**Score:** ${score}/5`);
  md.push(`**Score pre-screening (local model):** ${preScore ?? 'N/A'}/5`);
  md.push(`**Legitimacy:** ${judgment.legitimacy_tier}`);
  md.push(`**URL:** ${args.url}`);
  md.push(`**PDF:** ${pdfNote}`);
  md.push(`**Batch ID:** ${args.id}`);
  md.push('', '---', '');
  md.push('## Machine Summary', '', JSON.stringify(machineSummary), '', '---', '');

  md.push('## A) Role Summary', '');
  md.push('| Field | Value |', '|-------|-------|');
  md.push(`| Archetype | ${judgment.archetype} |`);
  md.push(`| Domain | ${parsed.domain} |`);
  md.push(`| Seniority | ${parsed.seniority_level}${parsed.years_required ? ` (${parsed.years_required}+ yrs)` : ''} |`);
  md.push(`| Remote policy | ${parsed.remote_policy} |`);
  md.push(`| Location | ${parsed.location} |`);
  md.push(`| TL;DR | ${judgment.notes} |`);
  md.push('');

  md.push('## B) CV Match', '');
  md.push(`_Evidence retrieved semantically from cv.md. **Strong** = same work, same tools · **Transferable** = same work, different tools · **Gap** = not demonstrated. Requirement coverage (75% must / 25% nice-to-have, Transferable counts 0.6): **${(coverage.coverage * 100).toFixed(0)}%**._`, '');
  md.push('| JD Requirement | Candidate evidence | Strength |');
  md.push('|----------------|-------------------|----------|');
  for (const e of evidence) {
    const req = `${e.must_have ? '**[must]** ' : ''}${e.requirement}`;
    md.push(`| ${req.replace(/\|/g, '/')} | ${e.evidence.replace(/\|/g, '/').slice(0, 160)} | ${e.strength} |`);
  }
  md.push('');
  md.push(`**Gaps:** ${judgment.soft_gaps.join('; ') || 'none identified'}`);
  // Can now be empty: verifyAgainstCv drops strengths naming absent technology.
  md.push(`**Top strengths:** ${judgment.top_strengths.join('; ') || 'none verifiable against cv.md'}`);
  md.push('');

  md.push('## C) Level & Strategy', '');
  md.push(`**JD seniority level:** ${parsed.seniority_level}`);
  md.push(`**Candidate natural level:** early-career engineer with production track record`, '');
  if (judgment.strategy.length) {
    md.push('**Strategy to position without overpromising:**');
    for (const s of judgment.strategy) md.push(`- ${s}`);
    md.push('');
  }
  md.push('**If downlevelled:** accept if comp is fair; set a written 6-month review criteria.', '');

  md.push(buildCompBlock(salary, compDim, compTargets));

  md.push('## E) Personalisation Plan', '');
  {
    md.push('Top CV changes for this specific role:', '');
    md.push('| # | Section | Current | Proposed change | Why |');
    md.push('|---|---------|---------|-----------------|-----|');
    judgment.personalisation.forEach((p, i) => {
      const c = s => String(s).replace(/\|/g, '/');
      md.push(`| ${i + 1} | ${c(p.section)} | ${c(p.current)} | ${c(p.proposed)} | ${c(p.why)} |`);
    });
    md.push('', 'Top LinkedIn changes:');
    judgment.linkedin.forEach((l, i) => md.push(`${i + 1}. ${l}`));
    md.push('');
  }

  md.push('## F) Interview Prep', '');
  if (!judgment.stories.length) {
    md.push('_No STAR stories: no JD requirement has supporting evidence in cv.md. Inventing one here would put experience you do not have in front of an interviewer._', '');
  } else {
    md.push('STAR stories mapped to JD requirements:', '');
    md.push('| # | JD Requirement | Story | S | T | A | R |');
    md.push('|---|----------------|-------|---|---|---|---|');
    judgment.stories.forEach((s, i) => {
      const c = v => String(v).replace(/\|/g, '/');
      md.push(`| ${i + 1} | ${c(s.requirement)} | ${c(s.story)} | ${c(s.situation)} | ${c(s.task)} | ${c(s.action)} | ${c(s.result)} |`);
    });
  }
  if (judgment.hard_questions.length) {
    md.push('', '**Likely hard questions:**');
    judgment.hard_questions.forEach((q, i) => md.push(`${i + 1}. Q: "${q.q}" → A: ${q.a}`));
  }
  md.push('');

  md.push('## G) Posting Legitimacy', '');
  md.push('**Verification:** unconfirmed (batch mode — Playwright unavailable)', '');
  md.push('| Signal | Assessment |', '|--------|------------|');
  md.push(`| Salary transparency | ${salary ? 'Disclosed' : 'Not disclosed'} |`);
  md.push(`| Requirements | ${evidence.length} parsed, ${coverage.mustCount} must-have |`);
  md.push(`| Reposting | check scan-history.tsv manually |`);
  md.push('');
  md.push(`**Tier:** ${judgment.legitimacy_tier}`);
  md.push('', `**Reason:** ${judgment.legitimacy_reason}`);
  md.push('');

  if (calibration.length) {
    md.push('## Similar Past Offers (calibration)', '');
    for (const c of calibration) {
      md.push(`- ${c.company} — ${c.role}: ${c.score}/5, ${c.decision}${c.user_label != null ? `, user rating ${c.user_label}/5` : ''} (sim ${c.sim})`);
    }
    md.push('');
  }

  md.push('---', '');
  md.push('## Keywords', '');
  const kws = [...new Set([...(parsed.keywords || []), ...(parsed.tech_stack || [])])].slice(0, 20);
  md.push(kws.join(', '));
  md.push('');
  return md.join('\n');
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  const args = parseArgs(process.argv.slice(2));

  const cv = cleanCvForPrompt(readSafe(resolve(PROJECT_DIR, 'cv.md')));
  const profile = readSafe(resolve(PROJECT_DIR, 'config/profile.md'));
  const config = readSafe(resolve(PROJECT_DIR, 'config/profile.yml'));
  const jd = readSafe(`/tmp/batch-jd-${args.id}.txt`) || readSafe(resolve(__dirname, 'jds', `${args.id}.txt`));
  if (!cv) fatal('cv.md not found or empty');
  if (!jd) fatal(`JD not cached for #${args.id} (run Phase 1 first — the scorer writes batch/jds/<id>.txt)`);

  const scoreCtx = (() => {
    try { return JSON.parse(readFileSync(resolve(__dirname, 'scores', `${args.id}.json`), 'utf8')); } catch { return {}; }
  })();

  const today = new Date().toISOString().split('T')[0];

  // ── Embedder block ──────────────────────────────────────────────────────────
  // Only one model stays resident in 6 GB, so every switch between the 18.5 GB
  // 30B and the 0.6 GB embedder costs a reload. Measured on Phase 3: 4 reloads
  // across 5 calls. Everything the embedder can do *without* stage 1's output —
  // loading the CV index and the calibration RAG — therefore runs here, before
  // the 30B is ever loaded, instead of between the two 30B calls.
  //
  // stage2's own `embed(reqTexts)` genuinely depends on stage1's requirements
  // and cannot be hoisted; it stays a single small request rather than a whole
  // index load plus a RAG query.
  const cvIndex = await loadCvIndex({ ollamaUrl: args.ollamaUrl }).catch(() => null);

  let calibration = [];
  if (args.rag) {
    try {
      calibration = await similarPastOffers(jd, args.id, 3, { ollamaUrl: args.ollamaUrl });
    } catch (e) {
      process.stderr.write(`[staged-evaluator] calibration RAG failed (offer ${args.id}): ${e.message}\n`);
    }
  }

  // Stage 1 — JD parse
  const parsed = await stage1JdParse(jd, args).catch(e => fatal(`stage1 (JD parse) failed: ${e.message}`));
  if (!parsed.requirements?.length) fatal('stage1 returned no requirements');

  // Deterministic comp (code, not model)
  const salary = extractSalary(jd);
  const compTargets = parseCompTargets(config);
  const compDim = compScoreFromSalary(salary, compTargets);

  // Stage 2 — evidence matching
  const evidence = await stage2Evidence(parsed.requirements, args, cvIndex)
    .catch(e => fatal(`stage2 (evidence match) failed: ${e.message}`));
  const coverage = coverageMetric(evidence);

  // Stage 3 — judgment
  const judgment = await stage3Judgment({ jd, parsed, evidence, coverage, calibration, salary, cv, profile, args })
    .catch(e => fatal(`stage3 (judgment) failed: ${e.message}`));

  // `top_strengths` is the one field asserting something on the candidate's
  // behalf, and it is the one that reaches an application. Phase 1 has filtered
  // it since ollama-scorer.mjs:408; the staged Phase 2 — the default path — never
  // did, so a strength naming a technology absent from cv.md shipped straight
  // into the report. Measured over the stored evals: ~1% of strengths, every one
  // a neighbour swap (a sibling cloud provider, another tool in the same family).
  const strengths = verifyAgainstCv(judgment.top_strengths || [], cv);
  judgment.top_strengths = strengths.kept;
  if (strengths.dropped.length) {
    process.stderr.write(`[staged-evaluator] dropped ${strengths.dropped.length} unverifiable strength(s): ${strengths.dropped.join(' | ')}\n`);
  }

  // ── Score computed in code ──────────────────────────────────────────────────
  const modelCv = clampDim(judgment.cv_match) ?? 1;
  let nsDim = clampDim(judgment.north_star) ?? 1;
  const rfDim = clampDim(judgment.red_flags_score) ?? 5;

  // Blend the continuous coverage metric into cv — this is what turns three
  // integer buckets into a real ranking.
  const coverage5 = 1 + 4 * coverage.coverage;
  let cvBlend = modelCv * (1 - COVERAGE_BLEND) + coverage5 * COVERAGE_BLEND;

  // Caps: seniority (structured stage-1 fields + title/JD regex) and stack mismatch.
  const sen = seniorityCaps(parsed.role, jd, parsed);
  cvBlend = Math.min(cvBlend, sen.cvCap);
  nsDim = Math.min(nsDim, sen.nsCap);
  const stack = stackMismatchCap(jd, cv);
  cvBlend = Math.min(cvBlend, stack.cap);
  // Required natural language the candidate lacks (e.g. "German speaking") is a
  // hard blocker — one Gap row barely moves the coverage average, so gate in code.
  const langCap = languageMismatchCap(jd, cv);
  if (langCap.missing) {
    cvBlend = Math.min(cvBlend, langCap.cvCap);
    nsDim = Math.min(nsDim, langCap.nsCap);
    judgment.hard_stops = [...new Set([`Requires ${langCap.missing} fluency — not in CV languages`, ...(judgment.hard_stops || [])])];
  }
  // Office attendance somewhere the profile will not commute to — same gate, and
  // it has to be one: `hard_stops` never reaches `final_decision`, so Phase 2 was
  // printing this exact deal-breaker on offers it then told the user to Apply to.
  const locCap = locationMismatchCap(jd, config);
  if (locCap.city) {
    cvBlend = Math.min(cvBlend, locCap.cvCap);
    nsDim = Math.min(nsDim, locCap.nsCap);
    judgment.hard_stops = [...new Set([`On-site/hybrid in ${locCap.city} — outside the commutable base`, ...(judgment.hard_stops || [])])];
  }
  // Deterministic junk-input guard: a hiring thread, blog page, or homepage is
  // not an evaluable posting — a capable model happily "matches" the candidate
  // against garbage (measured: HN thread scored 4.0 without this cap).
  // Either signal trips it: the model's flag, or the JD advertising several jobs
  // in plain text (the model missed that on #38 six times out of six).
  if (parsed.is_single_posting === false || looksMultiPosting(jd)) {
    cvBlend = Math.min(cvBlend, 2);
    nsDim = Math.min(nsDim, 2);
    judgment.legitimacy_tier = 'Suspicious';
    judgment.legitimacy_reason = `NOT a single job posting (thread/list/blog/homepage) — evaluation unreliable. ${judgment.legitimacy_reason || ''}`.trim();
    judgment.hard_stops = [...new Set(['Not a single job posting — verify the URL manually', ...(judgment.hard_stops || [])])];
  }
  cvBlend = +cvBlend.toFixed(2);

  const score = compDim !== null
    ? Math.round((cvBlend * 0.50 + nsDim * 0.30 + compDim * 0.20) * 10) / 10
    : Math.round((cvBlend * 0.625 + nsDim * 0.375) * 10) / 10;

  const pdfDecision = score >= args.threshold;
  let finalDecision = judgment.final_decision || 'Consider';
  if (score < 3) finalDecision = 'Skip';
  else if (score < 3.5 && finalDecision === 'Apply') finalDecision = 'Consider';

  const machineSummary = {
    company: parsed.company,
    role: parsed.role,
    cv_match: modelCv,
    cv_coverage: coverage.coverage,
    cv_blended: cvBlend,
    north_star: nsDim,
    comp_inferred: compDim,
    red_flags_score: rfDim,
    score,
    archetype: judgment.archetype,
    final_decision: finalDecision,
    hard_stops: judgment.hard_stops,
    soft_gaps: judgment.soft_gaps,
    top_strengths: judgment.top_strengths,
    legitimacy_tier: judgment.legitimacy_tier,
    pdf_decision: pdfDecision,
    notes: judgment.notes,
  };

  const slug = slugify(parsed.company || 'unknown');
  const reportFilename = `${args.reportNum}-${slug}-${today}.md`;
  const reportPath = resolve(REPORTS_DIR, reportFilename);
  const pdfNote = pdfDecision
    ? 'to be generated in Phase 3'
    : `not generated — run /snipe pdf ${slug} to create on demand`;

  const report = assembleReport({
    args, today, parsed, evidence, coverage, judgment, salary, compDim, compTargets,
    score, preScore: scoreCtx.score ?? null, pdfNote, machineSummary, calibration,
  });

  mkdirSync(REPORTS_DIR, { recursive: true });
  writeFileSync(reportPath, report, 'utf8');
  mkdirSync(EVALS_DIR, { recursive: true });

  const output = {
    status: 'evaled',
    id: args.id,
    url: args.url,
    report_num: args.reportNum,
    report_path: reportPath,
    report_filename: reportFilename,
    ...machineSummary,
    salary_posted: salary,
    evaluator: 'staged',
    error: null,
  };
  writeFileSync(resolve(EVALS_DIR, `${args.id}.json`), JSON.stringify(output, null, 2) + '\n', 'utf8');
  process.stdout.write(JSON.stringify(output, null, 2) + '\n');
}

main().catch(e => {
  process.stdout.write(JSON.stringify({ status: 'eval_failed', error: e.message }) + '\n');
  process.exit(1);
});
