#!/usr/bin/env node
/**
 * local-pdf-offer.mjs — Phase 3 worker (local Ollama)
 *
 * Reads the pre-written Phase 2 report, extracts Block E + keywords, calls
 * Ollama for tailored CV JSON, fills the HTML template, generates PDF, writes
 * tracker TSV.
 *
 * Usage: called by local-runner.sh pdf_offer_local()
 *   node batch/local-pdf-offer.mjs --id N --url URL --report-path PATH
 *     --report-num NNN --jd-file PATH --eval-score X.X --company CO
 *     --role ROLE --date YYYY-MM-DD [--model snipe-screen]
 *     [--ollama-url http://localhost:11434] [--threshold 3.7] [--num-ctx 16384]
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync, copyFileSync } from 'fs';
import { resolve, dirname, basename } from 'path';
import { fileURLToPath } from 'url';
import { execFileSync, execSync } from 'child_process';
import { cleanCvForPrompt, cleanJd } from './text-utils.mjs';
import { selectCvForJd, extractBlockBRequirements, remapProjectNames, enforceChronoOrder } from './cv-select.mjs';

const __dirname  = dirname(fileURLToPath(import.meta.url));
const PROJECT    = resolve(__dirname, '..');
// Prefer a gitignored personal override (real metrics) if present; else the shipped generic prompt
const PROMPT_LOCAL = resolve(__dirname, 'local-tailor-prompt.local.md');
const PROMPT_TPL = existsSync(PROMPT_LOCAL) ? PROMPT_LOCAL : resolve(__dirname, 'local-tailor-prompt.md');
const TRACKER_DIR= resolve(__dirname, 'tracker-additions');
const REPORTS_DIR= resolve(PROJECT, 'reports');
const APPS_FILE  = resolve(PROJECT, 'data/applications.md');

// ── Args ──────────────────────────────────────────────────────────────────────

function parseArgs(argv) {
  const a = {
    id: null, url: null, reportPath: null, reportNum: null, jdFile: null,
    evalScore: null, company: null, role: null, date: null, p1Score: null,
    p1Archetype: null, model: 'snipe-screen',
    ollamaUrl: 'http://localhost:11434', threshold: 3.7, numCtx: 8192,
  };
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--id':           a.id           = argv[++i]; break;
      case '--url':          a.url          = argv[++i]; break;
      case '--report-path':  a.reportPath   = argv[++i]; break;
      case '--report-num':   a.reportNum    = argv[++i]; break;
      case '--jd-file':      a.jdFile       = argv[++i]; break;
      case '--eval-score':   a.evalScore    = parseFloat(argv[++i]); break;
      case '--company':      a.company      = argv[++i]; break;
      case '--role':         a.role         = argv[++i]; break;
      case '--date':         a.date         = argv[++i]; break;
      case '--p1-score':     a.p1Score      = argv[++i]; break;
      case '--p1-archetype': a.p1Archetype  = argv[++i]; break;
      case '--model':        a.model        = argv[++i]; break;
      case '--ollama-url':   a.ollamaUrl    = argv[++i]; break;
      case '--threshold':    a.threshold    = parseFloat(argv[++i]); break;
      case '--num-ctx':      a.numCtx       = parseInt(argv[++i], 10); break;
    }
  }
  return a;
}

const args = parseArgs(process.argv.slice(2));

function out(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}

function fail(msg, extra = {}) {
  out({ status: 'failed', id: args.id, report_num: args.reportNum,
        company: args.company || 'unknown', role: args.role || 'unknown',
        score: args.evalScore, pdf: null, report: args.reportPath,
        tracker: null, error: msg, ...extra });
  process.exit(1);
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function readSafe(p) {
  try { return p && existsSync(p) ? readFileSync(p, 'utf8') : ''; } catch { return ''; }
}

function slugify(s) {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40);
}

function nextTrackerNum() {
  if (!existsSync(APPS_FILE)) return 1;
  const text = readFileSync(APPS_FILE, 'utf8');
  let max = 0;
  for (const line of text.split('\n')) {
    const m = line.match(/^\|\s*(\d+)\s*\|/);
    if (m) max = Math.max(max, parseInt(m[1], 10));
  }
  return max + 1;
}

function detectFormat(reportText, jdText) {
  const combined = (reportText + jdText).toLowerCase();
  if (/\b(united states|usa|\bus\b|canada|san francisco|new york|seattle|boston|austin)\b/.test(combined)) return 'letter';
  return 'a4';
}

// ── Extract profile narrative ─────────────────────────────────────────────────

function extractProfileNarrative(profileText) {
  const lines = [];
  const headline = profileText.match(/headline:\s*["']?([^"'\n]+)/)?.[1]?.trim();
  const exit     = profileText.match(/exit_story:\s*["']?([^"'\n]+)/)?.[1]?.trim();
  if (headline) lines.push(`Role focus: ${headline}`);
  if (exit)     lines.push(`Positioning: ${exit}`);
  // Superpowers
  const spSection = profileText.match(/superpowers:\n([\s\S]*?)(?=\n\S|\nproof_points)/)?.[1] || '';
  const sps = [...spSection.matchAll(/^\s*-\s+"?([^"\n]+)"?/gm)].map(m => m[1].trim());
  if (sps.length) lines.push(`Key strengths: ${sps.join('; ')}`);
  return lines.join('\n');
}

// ── Ollama helpers ────────────────────────────────────────────────────────────

// Available for manual debugging: unload the model when it's running at a
// different ctx than needed. Display runs on the iGPU (Radeon 680M), so the
// full 6144 MB of the 3060 is available to Ollama (verified 2026-07-17).
// NOT called in the normal pipeline — Phase 2+3 both use 8k so the model
// stays warm with no ctx change, no reload, no overhead.
async function ensureUnloaded(baseUrl, model) {
  try {
    const ps   = await fetch(`${baseUrl}/api/ps`, { signal: AbortSignal.timeout(5_000) });
    const psData = await ps.json();
    const slug = model.split(':')[0];
    const isRunning = (psData.models || []).some(m => m.name.startsWith(slug));
    if (!isRunning) return; // Already cold — skip

    // Send unload signal (keep_alive: 0 = unload immediately after response)
    await fetch(`${baseUrl}/api/generate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model, prompt: '', keep_alive: 0 }),
      signal: AbortSignal.timeout(15_000),
    });

    // Poll until confirmed unloaded (max 15s)
    for (let i = 0; i < 15; i++) {
      await new Promise(r => setTimeout(r, 1_000));
      const r2 = await fetch(`${baseUrl}/api/ps`, { signal: AbortSignal.timeout(5_000) });
      const d2 = await r2.json();
      if (!(d2.models || []).some(m => m.name.startsWith(slug))) return;
    }
  } catch { /* proceed anyway */ }
}

// Schema for the tailored-CV JSON (Ollama `format`): the grammar guarantees the
// shape, so parse failures and "prose around the JSON" disappear. Word counts
// and content rules still go through validateContent/Tier-4 (grammar can't count
// words).
const TAILOR_SCHEMA = {
  type: 'object',
  properties: {
    summary:      { type: 'string' },
    competencies: { type: 'array', items: { type: 'string' }, minItems: 6, maxItems: 9 },
    projects:     { type: 'array', minItems: 3, maxItems: 4, items: {
      type: 'object',
      properties: { name: { type: 'string' }, description: { type: 'string' } },
      required: ['name', 'description'],
    } },
    education_modules: { type: 'array', items: { type: 'string' }, maxItems: 6 },
    skills:       { type: 'array', minItems: 5, maxItems: 6, items: {
      type: 'object',
      properties: { category: { type: 'string' }, items: { type: 'string' } },
      required: ['category', 'items'],
    } },
    experience:   { type: 'array', minItems: 1, items: {
      type: 'object',
      properties: { company: { type: 'string' }, bullets: { type: 'array', items: { type: 'string' } } },
      required: ['company', 'bullets'],
    } },
  },
  required: ['summary', 'competencies', 'projects', 'education_modules', 'skills', 'experience'],
};

async function callOllama(baseUrl, model, systemPrompt, userMessage, numCtx, format = null) {
  const body = JSON.stringify({
    model,
    system: systemPrompt,
    prompt: userMessage,
    stream: false,
    ...(format ? { format } : {}),
    // num_predict 2400: ample for the richer JSON (realistic output ~1.1-1.4k
    // tokens — summary, 6-9 competencies, 3-4 project descriptions, 5-6 skill
    // categories, experience bullets). Inputs are trimmed (Block E brief +
    // capped JD + base64-stripped CV) to keep input + output under the 8k window.
    options: { num_ctx: numCtx, temperature: 0.15, num_predict: 2400 },
  });

  const resp = await fetch(`${baseUrl}/api/generate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body,
    signal: AbortSignal.timeout(300_000),
  });

  if (!resp.ok) throw new Error(`Ollama HTTP ${resp.status}: ${await resp.text()}`);
  const data = await resp.json();
  return data.response || '';
}

function parseJsonResponse(raw) {
  let text = raw.trim();
  // Strip markdown fences if present
  const fenceMatch = text.match(/```(?:json)?\s*([\s\S]*?)```/);
  if (fenceMatch) text = fenceMatch[1].trim();
  // Find the first { … } block
  const start = text.indexOf('{');
  const end   = text.lastIndexOf('}');
  if (start === -1 || end === -1) throw new Error('No JSON object found in Ollama response');
  return JSON.parse(text.slice(start, end + 1));
}

// ── Context trimming (8k window is the binding constraint) ──────────────────────
// cleanCvForPrompt / cleanJd now live in text-utils.mjs (shared with Phase 1/2).

// Extract a focused tailoring brief from the full A–G report: the header
// (score / archetype / legitimacy), Block E (Personalisation Plan), and the
// Keywords list. Blocks A–D, F, G are irrelevant to CV tailoring and only eat
// the context window (~1.5k tokens saved per run).
function extractTailoringBrief(report) {
  if (!report) return '(no report available)';
  const parts = [];
  const headerEnd = report.indexOf('\n---');
  if (headerEnd > 0) parts.push(report.slice(0, headerEnd).trim());

  // `$(?![\s\S])` = true end-of-string (a bare `\s*$` under /m stops at the first
  // line break and truncates the block). Lazy match stops at the next `## ` heading.
  const END = '(?=\\n##\\s|\\n---|$(?![\\s\\S]))';
  const eMatch = report.match(new RegExp(`^##\\s*E[)\\.][^\\n]*\\n([\\s\\S]*?)${END}`, 'm'));
  if (eMatch) parts.push('## Personalisation Plan (Block E)\n' + eMatch[1].trim());

  const kwMatch = report.match(new RegExp(`^##\\s*Keywords[^\\n]*\\n([\\s\\S]*?)${END}`, 'mi'));
  if (kwMatch) parts.push('## ATS Keywords\n' + kwMatch[1].trim());

  let brief = parts.join('\n\n').trim();
  // Fallback: if the report has an unexpected shape, cap the raw text instead.
  if (brief.length <= 80) brief = report.slice(0, 1600);
  // Hard cap so an over-long Block E can't crowd out the JD in the 8k window.
  const CAP = 2600; // ~650 tokens
  if (brief.length > CAP) brief = brief.slice(0, CAP).trim() + '\n[...]';
  return brief;
}

// ── Output validation + clamps ──────────────────────────────────────────────────

function wordCount(s) {
  return String(s || '').trim().split(/\s+/).filter(Boolean).length;
}

function companyRe(company) {
  const esc = String(company || '').trim().replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return esc.length >= 3 ? new RegExp(`\\b${esc}\\b`, 'i') : null;
}

// Soft schema check — returns a list of human-readable problems (empty = OK).
function validateContent(c, company) {
  const errs = [];
  if (!c || typeof c.summary !== 'string' || !c.summary.trim()) {
    errs.push('"summary" is missing');
  } else {
    const w = wordCount(c.summary);
    if (w < 45 || w > 78) errs.push(`"summary" is ${w} words (must be 50-70)`);
    if (/\b(he|she|they)\b\s+(has|is|was|brings|demonstrat)/i.test(c.summary)) {
      errs.push('"summary" uses third person — write in implied first person (no name, no he/she)');
    }
    // Fabrication guard: naming the target company implies past work FOR them
    // (observed: "Led development of production systems for ElevenLabs").
    const cre = companyRe(company);
    if (cre && cre.test(c.summary)) {
      errs.push(`"summary" mentions ${company} — never name the target company; describe only real past work`);
    }
  }
  if (!Array.isArray(c.experience) || c.experience.length === 0) errs.push('"experience" is missing');
  if (Array.isArray(c.competencies) && c.competencies.length < 6) errs.push('"competencies" has fewer than 6 entries');
  return errs;
}

// Hard clamps applied before rendering (defends the layout regardless of model).
function clampContent(c) {
  if (Array.isArray(c.competencies) && c.competencies.length > 9) c.competencies = c.competencies.slice(0, 9);
  if (Array.isArray(c.projects)     && c.projects.length > 4)     c.projects     = c.projects.slice(0, 4);
  if (Array.isArray(c.skills)       && c.skills.length > 6)       c.skills       = c.skills.slice(0, 6);
  if (Array.isArray(c.education_modules) && c.education_modules.length > 6) {
    c.education_modules = c.education_modules.slice(0, 6);
  }
  return c;
}

// ── Tier 3: deterministic fields (offload the coder model's weakest jobs) ────────

const STOPWORDS = new Set(['and','the','of','for','to','in','with','on','our','you','your','we','is','are','as','at','an','or','by','be','this','that','will','have','has','from','using','use']);
function tokenize(s) {
  return (String(s || '').toLowerCase().match(/[a-z0-9+#.]{3,}/g) || []).filter(w => !STOPWORDS.has(w));
}

// Parse the report's "## Keywords" section into a clean, ordered list.
function extractReportKeywords(report) {
  const m = (report || '').match(/^##\s*Keywords[^\n]*\n([\s\S]*?)(?=\n##\s|$(?![\s\S]))/mi);
  if (!m) return [];
  return m[1]
    .replace(/^[-*]\s*/gm, ',')
    .split(/[,\n]/)
    .map(s => s.replace(/[`*]/g, '').trim())
    .filter(Boolean);
}

// Title-case a keyword while preserving acronyms (OIDC, AWS) and dotted/mixed
// names (Next.js, gRPC).
function caseKeyword(kw) {
  if (/^[A-Z0-9.+/&-]{2,}$/.test(kw)) return kw;   // acronym
  if (/[a-z][A-Z]|\.[a-z]/.test(kw)) return kw;     // Next.js, gRPC
  return kw.replace(/\b\w/g, c => c.toUpperCase());
}

// Build 6-9 competency tags from the report keywords (already JD-extracted by the
// evaluator), de-duplicated and cased. Falls back to the model's competencies if
// the report has too few usable keywords.
function deriveCompetencies(report, fallback) {
  const seen = new Set();
  const out = [];
  for (const kw of extractReportKeywords(report)) {
    if (kw.length < 2 || kw.length > 34) continue;
    const norm = kw.toLowerCase();
    if (seen.has(norm)) continue;
    seen.add(norm);
    out.push(caseKeyword(kw));
    if (out.length >= 9) break;
  }
  if (out.length >= 6) return out;
  if (Array.isArray(fallback) && fallback.length >= 6) return fallback;
  return out.length ? out : (Array.isArray(fallback) ? fallback : []);
}

// Parse the CV's "Key Modules:" list.
function extractCvModules(cv) {
  const m = (cv || '').match(/\*\*Key Modules:\*\*\s*([^\n]+)/i);
  return m ? m[1].split(',').map(s => s.trim()).filter(Boolean) : [];
}

// Rank CV modules by token overlap with the JD; return the top N names. Ties keep
// CV order; zero overlap falls back to the CV's first N.
function rankModules(cv, jd, n = 5) {
  const mods = extractCvModules(cv);
  if (mods.length === 0) return [];
  const jdTok = new Set(tokenize(jd));
  const scored = mods.map((mod, idx) => {
    let score = 0;
    for (const t of new Set(tokenize(mod))) if (jdTok.has(t)) score++;
    return { mod, score, idx };
  });
  scored.sort((a, b) => b.score - a.score || a.idx - b.idx);
  const top = scored.slice(0, n);
  if (top.every(s => s.score === 0)) return mods.slice(0, n);
  return top.map(s => s.mod);
}

// ── Tier 4: targeted summary revision ───────────────────────────────────────────
// The all-in-one pass reliably writes tight prose but under-hits the 50-70 word
// floor. A focused single-field rewrite lands the length far more often than the
// generic JSON repair retry.
async function reviseSummary(current, jd, narrative, baseUrl, model, numCtx) {
  const sys = `You rewrite ONE CV professional summary. Output ONLY the revised summary as plain text — no JSON, no quotes, no preamble, no label.
Rules:
- Length: 50 to 70 words. Count them. This is mandatory.
- Implied first person: never use the candidate's name or "he/she/they".
- Use only concrete, real achievements from the material below. Never invent.
- Weave in 3-4 keywords relevant to the target role.`;
  const user = `Target role (keyword context):\n${(jd || '').slice(0, 1000)}\n\nCandidate highlights:\n${narrative || '(none)'}\n\nCurrent summary to rewrite (wrong length or voice):\n${current}\n\nRewrite to 50-70 words.`;
  const raw = await callOllama(baseUrl, model, sys, user, numCtx);
  return raw
    .trim()
    .replace(/^```[\s\S]*?\n|```$/g, '')
    .replace(/^["'`]+|["'`]+$/g, '')
    .replace(/^(summary|professional summary)\s*[:\-]\s*/i, '')
    .trim();
}

// ── Main ──────────────────────────────────────────────────────────────────────

const reportText = readSafe(args.reportPath);
if (!reportText) fail(`Report not found: ${args.reportPath}`);

const jdText   = readSafe(args.jdFile) || readSafe(`/tmp/batch-jd-${args.id}.txt`);
const cvText   = readSafe(resolve(PROJECT, 'cv.md'));
const prompt   = readSafe(PROMPT_TPL);

if (!prompt) fail('local-tailor-prompt.md not found');

// Gate on threshold
if (args.evalScore !== null && args.evalScore < args.threshold) {
  // Below threshold — write tracker skip and exit cleanly
  const slug      = slugify(args.company || 'unknown');
  const trackerN  = nextTrackerNum();
  const trackerLine = [
    trackerN, args.date, args.company, args.role,
    'Evaluated', `${args.evalScore}/5`, '❌',
    `[${args.reportNum}](reports/${args.reportNum}-${slug}-${args.date}.md)`,
    `Below local threshold (${args.threshold}) — no PDF — ${args.url}`,
  ].join('\t');
  mkdirSync(TRACKER_DIR, { recursive: true });
  writeFileSync(resolve(TRACKER_DIR, `${args.id}.tsv`), trackerLine + '\n', 'utf8');
  out({ status: 'skipped', id: args.id, report_num: args.reportNum,
        company: args.company, role: args.role, score: args.evalScore,
        pdf: null, report: args.reportPath,
        tracker: `batch/tracker-additions/${args.id}.tsv`, error: null });
  process.exit(0);
}

const profileText     = readSafe(resolve(PROJECT, 'config/profile.yml'));
const profileNarrative = extractProfileNarrative(profileText);

// Pre-select CV content: rank every experience/project bullet against the JD's
// requirements (Block B of the report) with the embedding model, keep top-N per
// entry and the top projects. The 7B then only rewrites — it no longer decides
// what's relevant. Any failure (embedding model missing, odd report shape)
// falls back to the full CV.
let cvForPrompt = cvText;
try {
  cvForPrompt = await selectCvForJd(
    cvText, extractBlockBRequirements(reportText), jdText, { ollamaUrl: args.ollamaUrl });
} catch (err) {
  process.stderr.write(`cv-select failed (${err.message}) — using full CV\n`);
}

// Build system prompt — focused tailoring brief (Block E + keywords) + cleaned
// JD + base64-stripped CV + profile narrative, to stay within the 8k window.
const systemPrompt = prompt
  .replace('{{COMPANY}}',           args.company || '')
  .replace('{{ROLE}}',              args.role    || '')
  .replace('{{CANDIDATE_PROFILE}}', profileNarrative || '(see cv.md)')
  .replace('{{CV_CONTENT}}',        cleanCvForPrompt(cvForPrompt))
  .replace('{{FULL_REPORT}}',       extractTailoringBrief(reportText))
  .replace('{{JD_FULL}}',           cleanJd(jdText));

const userMessage = `Tailor the CV for ${args.company} — ${args.role}. Score: ${args.evalScore}/5. Report: ${args.reportPath}`;

// Generate with one validate-and-repair retry. We keep the latest parseable
// JSON so a word-count miss on the retry still ships (clamped) rather than
// failing the whole offer; only a total parse failure is fatal.
let cvContent = null;
let lastErr   = '';
for (let attempt = 1; attempt <= 2; attempt++) {
  const um = attempt === 1
    ? userMessage
    : `${userMessage}\n\nYour previous JSON had these problems: ${lastErr}. Return ONLY corrected JSON in the exact schema. The "summary" MUST be 50-70 words in implied first person (no name, no he/she).`;
  let parsed;
  try {
    const raw = await callOllama(args.ollamaUrl, args.model, systemPrompt, um, args.numCtx, TAILOR_SCHEMA);
    parsed = parseJsonResponse(raw);
  } catch (err) {
    lastErr = `invalid JSON (${err.message})`;
    continue;
  }
  cvContent = parsed; // latest parseable result wins
  const errs = validateContent(parsed, args.company);
  if (errs.length === 0) break;
  lastErr = errs.join('; ');
}

if (!cvContent) fail(`Ollama returned no parseable JSON after 2 attempts: ${lastErr}`);
if (!cvContent.summary || !Array.isArray(cvContent.experience)) {
  fail(`Ollama JSON missing required fields. Got: ${JSON.stringify(Object.keys(cvContent))}`);
}
cvContent = clampContent(cvContent);

// Normalize legacy field names → new schema so trimming + fill stay consistent.
if (!cvContent.projects && Array.isArray(cvContent.selected_projects)) {
  cvContent.projects = cvContent.selected_projects.map(p =>
    typeof p === 'string' ? { name: p, description: '' } : p);
}

// Tier 3 — remap renamed/fabricated project names back to real CV projects so
// the template's name match can't silently drop a project slot (observed:
// "Distributed Odds Feed Orchestrator" invented for a betting-infra JD).
if (Array.isArray(cvContent.projects)) {
  cvContent.projects = remapProjectNames(cvContent.projects, cvText);
}

// Tier 3 — the model doesn't reliably keep UK reverse-chronological order;
// re-sort both sections by real CV end date regardless of its output order.
cvContent.experience = enforceChronoOrder(cvContent.experience, cvText, 'Experience', 'company');
if (Array.isArray(cvContent.projects)) {
  cvContent.projects = enforceChronoOrder(cvContent.projects, cvText, 'Projects', 'name');
}

// Tier 3 — replace the coder model's two weakest fields with deterministic,
// JD-grounded selections. Competencies come from the report's already-extracted
// keywords; education modules are ranked by JD token overlap.
cvContent.competencies = deriveCompetencies(reportText, cvContent.competencies);
const rankedModules = rankModules(cvText, jdText, 5);
if (rankedModules.length) cvContent.education_modules = rankedModules;

// Tier 4 — if the summary still misses 50-70 words, run up to 2 targeted
// rewrites and keep whichever lands closest to the range.
const distOf = n => (n < 50 ? 50 - n : n > 70 ? n - 70 : 0);
if (typeof cvContent.summary === 'string') {
  let best = cvContent.summary;
  let bestDist = distOf(wordCount(best));
  for (let i = 0; i < 2 && bestDist > 0; i++) {
    try {
      const revised = await reviseSummary(
        cvContent.summary, jdText, profileNarrative, args.ollamaUrl, args.model, args.numCtx);
      const d = distOf(wordCount(revised));
      if (revised && d < bestDist) { best = revised; bestDist = d; }
    } catch { /* keep best-so-far */ }
  }
  cvContent.summary = best;

  // Deterministic fabrication strip — if the target company name survived the
  // retries, drop the sentence claiming it (runs before the length-floor pad).
  const cre = companyRe(args.company);
  if (cre && cre.test(cvContent.summary)) {
    cvContent.summary = cvContent.summary
      .split(/(?<=[.!?])\s+/)
      .filter(s => !cre.test(s))
      .join(' ')
      .trim();
  }

  // Deterministic floor guarantee — the 7B reliably writes tight but under-hits
  // 50 words. If still short, append a natural, role-specific targeting closer
  // (on-brand: end-to-end ownership is a stated profile superpower).
  if (wordCount(cvContent.summary) < 50 && !/\btarget/i.test(cvContent.summary)) {
    // Strip a trailing " - Apollo Platform" qualifier, but NOT an in-word hyphen
    // like "Full-Stack" (which `\s*[-–—]` would truncate to "Full").
    const role = (args.role || 'engineering').replace(/\s+[-–—]\s+.*$/, '').trim();
    const closer = ` Targeting ${role} roles where I can own systems end-to-end and ship secure, production-grade software.`;
    const combined = cvContent.summary.replace(/\s+$/, '') + closer;
    if (wordCount(combined) <= 72) cvContent.summary = combined;
  }

  // Deterministic guard — the model anchors on the report's seniority assessment
  // and self-labels ("Mid-level …") despite the prompt rule. Self-labelling down
  // is off-strategy on a CV, so strip it and re-capitalise.
  const stripped = cvContent.summary.replace(
    /\b(mid|junior|senior|entry|associate)[\s-]?level\s+/gi, '');
  cvContent.summary = stripped.charAt(0).toUpperCase() + stripped.slice(1);
}

// Build output folder
const companySlug = slugify(args.company || 'unknown');
const appDir      = resolve(PROJECT, `output/${args.date}_${companySlug}_${args.reportNum}`);
const contentFile = resolve(appDir, 'cv-content.json');
const htmlFile    = resolve(appDir, 'source.html');
const cvName      = 'Candidate'; // fallback for PDF filename; overridden by profile.yml full_name below
mkdirSync(appDir, { recursive: true });

// Write content JSON for fill-cv-template.mjs
writeFileSync(contentFile, JSON.stringify(cvContent, null, 2), 'utf8');

// Copy JD
const jdDest = resolve(appDir, 'job-description.txt');
if (jdText) writeFileSync(jdDest, jdText, 'utf8');

// Derive candidate name from profile.yml for PDF filename
let candidateName = cvName;
try {
  const profileText = readSafe(resolve(PROJECT, 'config/profile.yml'));
  const m = profileText.match(/full_name:\s*["']?([^"'\n]+)["']?/);
  if (m) candidateName = m[1].trim().replace(/\s+/g, '-');
} catch {}

const pdfFile   = resolve(appDir, `${candidateName}-CV.pdf`);
const format    = detectFormat(reportText, jdText);
const fillScript = resolve(__dirname, 'fill-cv-template.mjs');
const generatePdf= resolve(PROJECT, 'generate-pdf.mjs');

function runFill(maxSkills, maxBullets) {
  const a = [
    fillScript,
    '--content',    contentFile,
    '--output',     htmlFile,
    '--format',     format,
    '--max-skills', String(maxSkills),
  ];
  if (maxBullets) a.push('--max-bullets', String(maxBullets));
  execFileSync(process.execPath, a, { stdio: 'inherit', cwd: PROJECT });
}

// Tier 5 — relevance-preserving density ladder. The summary and competencies are
// NEVER cut to fit the page; we only reduce experience-bullet depth (fill caps
// per role, hitting the least-relevant backfilled roles too), skill breadth, and
// the weakest (last-ranked) projects. Each step renders and re-checks the page
// count; we stop at the first step that fits ≤ 2 pages.
const LADDER = [
  { skills: 6, bullets: 4, projects: 4 }, // full
  { skills: 6, bullets: 3, projects: 4 },
  { skills: 5, bullets: 3, projects: 3 },
  { skills: 5, bullets: 3, projects: 3 },
  { skills: 4, bullets: 3, projects: 2 }, // tightest
];

let pdfPath = null;
let pdfError = null;

for (let step = 0; step < LADDER.length; step++) {
  const { skills, bullets, projects } = LADDER[step];

  if (Array.isArray(cvContent.projects) && cvContent.projects.length > projects) {
    cvContent.projects = cvContent.projects.slice(0, projects);
  }
  writeFileSync(contentFile, JSON.stringify(cvContent, null, 2), 'utf8');

  try {
    runFill(skills, bullets);
  } catch (err) {
    if (step === 0) fail(`fill-cv-template.mjs failed: ${err.message}`);
    continue; // a later, tighter step may still render
  }

  try {
    execFileSync(process.execPath, [
      generatePdf, htmlFile, pdfFile,
      `--format=${format}`,
      '--max-pages=2',
      `--source-url=${args.url}`,
    ], { stdio: 'inherit', cwd: PROJECT });
    pdfPath = `output/${args.date}_${companySlug}_${args.reportNum}/${candidateName}-CV.pdf`;
    break;
  } catch {
    if (step === LADDER.length - 1) pdfError = `PDF still >2 pages after ${LADDER.length} density steps`;
  }
}

// Update **PDF:** line in the report
if (pdfPath && args.reportPath && existsSync(args.reportPath)) {
  try {
    const rText = readFileSync(args.reportPath, 'utf8');
    const updated = rText.replace(/\*\*PDF:\*\*[^\n]*/,
      `**PDF:** ${pdfPath}`);
    if (updated !== rText) writeFileSync(args.reportPath, updated, 'utf8');
  } catch {}
}

// Write tracker TSV
const slug       = slugify(args.company || 'unknown');
const trackerN   = nextTrackerNum();
// Use actual report filename (preserves original creation date; --date is CV output date, not report date)
const reportBasename = args.reportPath ? basename(args.reportPath) : `${args.reportNum}-${slug}-${args.date}.md`;
const reportLink = `[${args.reportNum}](reports/${reportBasename})`;
const note       = `${pdfPath ? 'PDF generated' : 'PDF failed'} — score ${args.evalScore}/5 — ${args.url}`;
const trackerLine = [
  trackerN, args.date, args.company, args.role,
  'Evaluated', `${args.evalScore}/5`, pdfPath ? '✅' : '❌',
  reportLink, note,
].join('\t');

mkdirSync(TRACKER_DIR, { recursive: true });
writeFileSync(resolve(TRACKER_DIR, `${args.id}.tsv`), trackerLine + '\n', 'utf8');

out({
  status:     pdfPath ? 'completed' : 'pdf_failed',
  id:         args.id,
  report_num: args.reportNum,
  company:    args.company,
  role:       args.role,
  score:      args.evalScore,
  pdf:        pdfPath,
  report:     args.reportPath,
  tracker:    `batch/tracker-additions/${args.id}.tsv`,
  error:      pdfError,
});
