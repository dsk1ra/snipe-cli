#!/usr/bin/env node
// @ts-check
/**
 * staged-evaluator.mjs — Phase 2 as three small, schema-constrained calls
 * instead of one monolithic report generation.
 *
 *   Stage 1  JD parse        → company/role/seniority/requirements/keywords (JSON)
 *   Stage 2  evidence match  → each requirement vs top-3 embedded CV atoms,
 *                              graded Strong/Partial/Gap (JSON); coverage metric
 *                              computed in code
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
import { stackMismatchCap, seniorityCaps, languageMismatchCap } from './fit-rules.mjs';
import {
  cleanCvForPrompt, cleanJd, extractSalary, parseCompTargets,
  compScoreFromSalary, buildCompBlock,
} from './text-utils.mjs';
import { loadCvIndex, embed, topK, similarPastOffers } from './embeddings.mjs';

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

async function ollamaJson({ baseUrl, model, system, user, schema, numPredict, timeoutMs, temperature = 0.1 }) {
  // One retry: grammar-constrained output can still truncate at num_predict on a
  // verbose sample, which breaks JSON.parse; a second sample almost always lands.
  let lastErr;
  for (let attempt = 0; attempt < 2; attempt++) {
    const res = await fetch(`${baseUrl}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model,
        messages: [{ role: 'system', content: system }, { role: 'user', content: user }],
        stream: false,
        format: schema,
        options: { temperature: attempt === 0 ? temperature : temperature + 0.15, num_ctx: 8192, num_predict: numPredict },
      }),
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!res.ok) throw new Error(`Ollama HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
    const data = await res.json();
    const content = data?.message?.content || '';
    try { return JSON.parse(content); }
    catch (e) { lastErr = new Error(`invalid JSON (${content.length} chars, done_reason=${data?.done_reason}): ${content.slice(-80)}`); }
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
    schema: STAGE1_SCHEMA, numPredict: 900, timeoutMs: args.timeout,
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
        strength: { type: 'string', enum: ['Strong', 'Partial', 'Gap'] },
        pick:     { type: 'string', enum: ['A', 'B', 'C', 'none'] },
        note:     { type: 'string' },
      },
      required: ['req', 'strength', 'pick', 'note'],
    } },
  },
  required: ['matches'],
};

async function stage2Evidence(requirements, args) {
  const cvIndex = await loadCvIndex({ ollamaUrl: args.ollamaUrl });
  const reqTexts = requirements.map(r => r.text);
  const reqVecs = await embed(reqTexts, { ollamaUrl: args.ollamaUrl });

  const candidates = reqVecs.map(v => topK(v, cvIndex, 3));

  const lines = requirements.map((r, i) => {
    const cands = candidates[i]
      .map((c, j) => `   ${'ABC'[j]}) ${c.text.slice(0, 200)}`)
      .join('\n');
    return `R${i + 1} (${r.must_have ? 'MUST' : 'nice-to-have'}): ${r.text}\n${cands}`;
  }).join('\n\n');

  const system = [
    'You grade how well a candidate\'s CV evidence covers each job requirement.',
    'For each requirement R1..Rn, the top candidate CV lines (A/B/C, retrieved by semantic similarity) are shown.',
    'Rules:',
    '- `strength`: Strong = the picked evidence directly demonstrates the requirement; Partial = adjacent/transferable; Gap = none of the candidates actually covers it.',
    '- `pick`: the single best evidence line (A/B/C), or "none" for a Gap.',
    '- Similarity retrieval can surface superficially-similar lines — judge the CONTENT, not the wording. A requirement for "Kubernetes internals" is NOT covered by a line that merely lists Kubernetes as a tool.',
    '- `note`: one short clause (max 15 words) justifying the grade.',
    '- Output exactly one entry per requirement, req numbered from 1.',
  ].join('\n');

  const out = await ollamaJson({
    baseUrl: args.ollamaUrl, model: args.model, system,
    user: lines, schema: STAGE2_SCHEMA, numPredict: 1800, timeoutMs: args.timeout,
  });

  // Normalize: exactly one entry per requirement, in order.
  const byReq = new Map();
  for (const m of out.matches || []) {
    if (m.req >= 1 && m.req <= requirements.length && !byReq.has(m.req)) byReq.set(m.req, m);
  }
  return requirements.map((r, i) => {
    const m = byReq.get(i + 1) || { strength: 'Gap', pick: 'none', note: 'no grade returned' };
    const pickIdx = 'ABC'.indexOf(m.pick);
    const atom = pickIdx >= 0 ? candidates[i][pickIdx] : null;
    return {
      requirement: r.text,
      must_have: r.must_have,
      strength: ['Strong', 'Partial', 'Gap'].includes(m.strength) ? m.strength : 'Gap',
      evidence: atom ? atom.text : '—',
      sim: atom ? +atom.sim.toFixed(3) : null,
      note: m.note || '',
    };
  });
}

function coverageMetric(evidence) {
  const w = { Strong: 1, Partial: 0.5, Gap: 0 };
  const avg = pool => pool.reduce((a, e) => a + w[e.strength], 0) / pool.length;
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

const STAGE3_SCHEMA = {
  type: 'object',
  properties: {
    cv_match:        { type: 'integer', minimum: 1, maximum: 5 },
    north_star:      { type: 'integer', minimum: 1, maximum: 5 },
    red_flags_score: { type: 'integer', minimum: 1, maximum: 5 },
    archetype:       { type: 'string' },
    hard_stops:      { type: 'array', items: { type: 'string' } },
    soft_gaps:       { type: 'array', items: { type: 'string' }, maxItems: 5 },
    top_strengths:   { type: 'array', items: { type: 'string' }, maxItems: 3 },
    strategy:        { type: 'array', items: { type: 'string' }, minItems: 2, maxItems: 4 },
    personalisation: { type: 'array', minItems: 3, maxItems: 5, items: {
      type: 'object',
      properties: {
        section:  { type: 'string' },
        current:  { type: 'string' },
        proposed: { type: 'string' },
        why:      { type: 'string' },
      },
      required: ['section', 'current', 'proposed', 'why'],
    } },
    linkedin:        { type: 'array', items: { type: 'string' }, minItems: 2, maxItems: 3 },
    stories:         { type: 'array', minItems: 3, maxItems: 5, items: {
      type: 'object',
      properties: {
        requirement: { type: 'string' },
        story:       { type: 'string' },
        situation:   { type: 'string' },
        task:        { type: 'string' },
        action:      { type: 'string' },
        result:      { type: 'string' },
      },
      required: ['requirement', 'story', 'situation', 'task', 'action', 'result'],
    } },
    hard_questions:  { type: 'array', minItems: 2, maxItems: 3, items: {
      type: 'object',
      properties: { q: { type: 'string' }, a: { type: 'string' } },
      required: ['q', 'a'],
    } },
    legitimacy_tier:   { type: 'string', enum: ['High Confidence', 'Proceed with Caution', 'Suspicious'] },
    legitimacy_reason: { type: 'string' },
    final_decision:    { type: 'string', enum: ['Apply', 'Research first', 'Consider', 'Skip'] },
    notes:             { type: 'string' },
  },
  required: ['cv_match', 'north_star', 'red_flags_score', 'archetype', 'hard_stops',
             'soft_gaps', 'top_strengths', 'strategy', 'personalisation', 'linkedin',
             'stories', 'hard_questions', 'legitimacy_tier', 'legitimacy_reason',
             'final_decision', 'notes'],
};

async function stage3Judgment({ jd, parsed, evidence, coverage, calibration, salary, cv, profile, args }) {
  const evidenceTable = evidence.map((e, i) =>
    `${i + 1}. [${e.strength}${e.must_have ? ', MUST' : ''}] ${e.requirement}\n   evidence: ${e.evidence.slice(0, 180)}${e.note ? `\n   note: ${e.note}` : ''}`
  ).join('\n');

  const calibLines = calibration.length
    ? calibration.map(c =>
        `- ${c.company} — ${c.role}: scored ${c.score}/5, decision ${c.decision}${c.user_label != null ? `, user's own rating ${c.user_label}/5` : ''}${c.outcome ? `, real outcome: ${c.outcome}` : ''} (similarity ${c.sim})`
      ).join('\n')
    : '(none available)';

  const system = [
    'You are a job-offer evaluator for a software engineering candidate. A pre-verified evidence table (each JD requirement graded against real CV lines) is provided — treat it as ground truth for what the candidate does and does not have. Never invent experience.',
    '',
    'Scoring (integers 1-5, commit — no hedging toward 3):',
    '- cv_match: 5 = nearly every requirement Strong; 4 = most Strong, 1-2 minor gaps; 3 = about half covered; 2 = a minority covered; 1 = different stack/domain. Must be CONSISTENT with the evidence table — do not score 4-5 while MUST requirements sit at Gap.',
    '- north_star: 5 = squarely a primary archetype from the profile at a reachable seniority; 3 = adjacent archetype or seniority stretch; 1 = outside all targets.',
    '- red_flags_score: start at 5, subtract 1 per deal-breaker from the profile that ACTUALLY applies (informational — not scored).',
    '- The SYSTEM computes the composite and applies seniority caps in code; your job is honest dimensions.',
    '- Similar past offers (with the candidate\'s own scores/ratings) are calibration anchors: a materially better fit than a past 3.3 should score above 3.3, a similar one should land nearby. Where a real outcome is given it outranks the past model score: similar offers that were rejected argue for scoring lower; ones that reached interview validate the fit.',
    '',
    'Content blocks: strategy (positioning without overpromising), personalisation (specific CV changes for THIS role, referencing real CV content), stories (STAR mapped to the hardest requirements, from real CV projects), hard_questions (likely tough interview questions with grounded answers), legitimacy (is this POSTING genuine and well-specified — description quality, realistic requirements, transparency? It is NOT about candidate fit; a poor fit at a legitimate company is still "High Confidence". Judge ONLY from the JD itself — never invent hiring freezes or company signals).',
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
    `## Similar past offers (calibration anchors)`,
    calibLines,
    '',
    `## Candidate profile (archetypes, framing, deal-breakers)`,
    profile || '(no profile)',
    '',
    `## Candidate CV (for content blocks — cite real lines only)`,
    cv,
    '',
    `## JD excerpt (context)`,
    cleanJd(jd, 2500),
  ].join('\n');

  return ollamaJson({
    baseUrl: args.ollamaUrl, model: args.model, system, user,
    schema: STAGE3_SCHEMA, numPredict: 3000, timeoutMs: args.timeout, temperature: 0.2,
  });
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
  md.push(`_Evidence retrieved semantically from cv.md and graded by the model. Requirement coverage (75% must / 25% nice-to-have): **${(coverage.coverage * 100).toFixed(0)}%**._`, '');
  md.push('| JD Requirement | Candidate evidence | Strength |');
  md.push('|----------------|-------------------|----------|');
  for (const e of evidence) {
    const req = `${e.must_have ? '**[must]** ' : ''}${e.requirement}`;
    md.push(`| ${req.replace(/\|/g, '/')} | ${e.evidence.replace(/\|/g, '/').slice(0, 160)} | ${e.strength} |`);
  }
  md.push('');
  md.push(`**Gaps:** ${judgment.soft_gaps.join('; ') || 'none identified'}`);
  md.push(`**Top strengths:** ${judgment.top_strengths.join('; ')}`);
  md.push('');

  md.push('## C) Level & Strategy', '');
  md.push(`**JD seniority level:** ${parsed.seniority_level}`);
  md.push(`**Candidate natural level:** early-career engineer with production track record`, '');
  md.push('**Strategy to position without overpromising:**');
  for (const s of judgment.strategy) md.push(`- ${s}`);
  md.push('', '**If downlevelled:** accept if comp is fair; set a written 6-month review criteria.', '');

  md.push(buildCompBlock(salary, compDim, compTargets));

  md.push('## E) Personalisation Plan', '');
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

  md.push('## F) Interview Prep', '');
  md.push('STAR stories mapped to JD requirements:', '');
  md.push('| # | JD Requirement | Story | S | T | A | R |');
  md.push('|---|----------------|-------|---|---|---|---|');
  judgment.stories.forEach((s, i) => {
    const c = v => String(v).replace(/\|/g, '/');
    md.push(`| ${i + 1} | ${c(s.requirement)} | ${c(s.story)} | ${c(s.situation)} | ${c(s.task)} | ${c(s.action)} | ${c(s.result)} |`);
  });
  md.push('', '**Likely hard questions:**');
  judgment.hard_questions.forEach((q, i) => md.push(`${i + 1}. Q: "${q.q}" → A: ${q.a}`));
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

  // Stage 1 — JD parse
  const parsed = await stage1JdParse(jd, args).catch(e => fatal(`stage1 (JD parse) failed: ${e.message}`));
  if (!parsed.requirements?.length) fatal('stage1 returned no requirements');

  // Deterministic comp (code, not model)
  const salary = extractSalary(jd);
  const compTargets = parseCompTargets(config);
  const compDim = compScoreFromSalary(salary, compTargets);

  // Stage 2 — evidence matching
  const evidence = await stage2Evidence(parsed.requirements, args)
    .catch(e => fatal(`stage2 (evidence match) failed: ${e.message}`));
  const coverage = coverageMetric(evidence);

  // Calibration RAG (best-effort — never blocks the eval)
  let calibration = [];
  if (args.rag) {
    try {
      calibration = await similarPastOffers(jd, args.id, 3, { ollamaUrl: args.ollamaUrl });
    } catch (e) {
      process.stderr.write(`[staged-evaluator] calibration RAG failed (offer ${args.id}): ${e.message}\n`);
    }
  }

  // Stage 3 — judgment
  const judgment = await stage3Judgment({ jd, parsed, evidence, coverage, calibration, salary, cv, profile, args })
    .catch(e => fatal(`stage3 (judgment) failed: ${e.message}`));

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
  // Deterministic junk-input guard: a hiring thread, blog page, or homepage is
  // not an evaluable posting — a capable model happily "matches" the candidate
  // against garbage (measured: HN thread scored 4.0 without this cap).
  if (parsed.is_single_posting === false) {
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
