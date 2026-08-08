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
import { selectCvForJd, extractBlockBRequirements, remapProjectNames, enforceChronoOrder,
         reconcileExperience, verifyBulletNumbers, verifyBulletFigures,
         verifyProjectFigures, cvCompanies, parseCvSections, parseEntries,
         padProjectDescriptions, stripUnsupportedTenure,
         verifySummaryFigures } from './cv-select.mjs';
import { logCall } from './timing.mjs';
import { generateSummary, selectedBullets, stripFabricatedProducts,
         stripFabricatedCredentials, stripJdProperNouns,
         verifyBulletProducts, filterSkillItems } from './summary-stage.mjs';
import { verbatimContent } from './cv-writers.mjs';
import { createHash } from 'crypto';

const __dirname  = dirname(fileURLToPath(import.meta.url));
const PROJECT    = resolve(__dirname, '..');
// Prefer a gitignored personal override (real metrics) if present; else the shipped generic prompt
const PROMPT_LOCAL = resolve(__dirname, 'local-tailor-prompt.local.md');
const PROMPT_TPL = existsSync(PROMPT_LOCAL) ? PROMPT_LOCAL : resolve(__dirname, 'local-tailor-prompt.md');
// SNIPE_ADDITIONS mirrors merge-tracker's override: the tests point it at a temp
// dir so a killed run cannot leave a fixture TSV that the next real run merges
// into the user's tracker.
const TRACKER_DIR= process.env.SNIPE_ADDITIONS
  ? resolve(process.env.SNIPE_ADDITIONS)
  : resolve(__dirname, 'tracker-additions');
const REPORTS_DIR= resolve(PROJECT, 'reports');
const APPS_FILE  = resolve(PROJECT, 'data/applications.md');

// ── Args ──────────────────────────────────────────────────────────────────────

function parseArgs(argv) {
  const a = {
    id: null, url: null, reportPath: null, reportNum: null, jdFile: null,
    evalScore: null, company: null, role: null, date: null, p1Score: null,
    p1Archetype: null, model: 'snipe-screen',
    ollamaUrl: 'http://localhost:11434', threshold: 3.7, numCtx: 8192,
    // Benchmarking only. --bench-dir redirects the output folder and stops
    // before PDF generation (the model's work is done once cv-content.json is
    // written); --temperature overrides the production 0.15 so a benchmark can
    // run greedy, where this stack is byte-identical and one run is a valid A/B.
    benchDir: null, temperature: 0.15,
    // Which component writes the bullets. `verbatim` is the shipped path: render
    // the selection as cv.md already words it, no generation call at all. `model`
    // hands the selection to snipe-cv to rewrite — the old default, kept as the
    // benchmark control. It loses on every axis that matters (see
    // docs/PHASE3-RETENTION-LEDGER.md §4); a bigger writer only loses differently.
    writer: 'verbatim',
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
      case '--bench-dir':    a.benchDir     = argv[++i]; break;
      case '--temperature':  a.temperature  = parseFloat(argv[++i]); break;
      case '--writer':       a.writer       = argv[++i]; break;
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
    // minItems is raised per-run to the number of roles cv-select actually
    // passed (see experienceSchemaFloor) — the prose "ALL companies from the
    // CV" lost to a worked example and a template that both showed one.
    experience:   { type: 'array', minItems: 1, items: {
      type: 'object',
      properties: { company: { type: 'string' }, bullets: { type: 'array', items: { type: 'string' } } },
      required: ['company', 'bullets'],
    } },
  },
  required: ['summary', 'competencies', 'projects', 'education_modules', 'skills', 'experience'],
};

/**
 * Grammar-enforced floor on experience entries, derived from the CV that was
 * actually handed to the model rather than hardcoded — a one-role CV must stay
 * satisfiable. cv-select never drops experience entries (it only trims bullets
 * within each), so the count it passes is the count that must come back.
 * @param {string} selectedCv
 * @returns {object}
 */
/**
 * Employer names from the CV handed to the model, in CV order. The grammar can
 * compel the *number* of experience entries but not which company goes in each,
 * so V1's floor was satisfied by duplicating one employer or promoting a project
 * to a job. Naming them in the prompt is the half the schema cannot express.
 * @param {string} selectedCv
 * @returns {string[]}
 */
function experienceCompanies(selectedCv) {
  try { return cvCompanies(selectedCv); } catch { return []; }
}

/**
 * Bullet- and project-count floors, read off the CV the model was handed.
 *
 * `minItems` on the entry count stopped the model dropping a whole employer, but
 * nothing stopped it dropping four fifths of one: the bullets array had no floor
 * at all and `projects` floored at the schema's hardcoded 3, which the model
 * then took as the answer on every run. Both must come from the selected CV, not
 * a constant — demanding 3 bullets from a role that only has 1 is an instruction
 * to invent, which is the one failure this pipeline spends everything to avoid.
 *
 * @param {string} selectedCv
 * @returns {{bullets: number, projects: number}}
 */
function contentFloors(selectedCv) {
  const floors = { bullets: 1, projects: 3 };
  try {
    const named = n => parseCvSections(selectedCv).find(s => s.name === n);
    const exp = named('Experience');
    if (exp) {
      const counts = parseEntries(exp.lines).entries.map(e => e.bullets.length).filter(n => n > 0);
      if (counts.length) floors.bullets = Math.min(3, ...counts);
    }
    const proj = named('Projects');
    const n = proj ? parseEntries(proj.lines).entries.length : 0;
    if (n) floors.projects = Math.min(4, n);
  } catch { /* keep the conservative defaults */ }
  return floors;
}

function schemaWithExperienceFloor(selectedCv) {
  const roles = experienceCompanies(selectedCv).length;
  const floors = contentFloors(selectedCv);
  const exp = TAILOR_SCHEMA.properties.experience;
  return {
    ...TAILOR_SCHEMA,
    properties: {
      ...TAILOR_SCHEMA.properties,
      projects: { ...TAILOR_SCHEMA.properties.projects, minItems: floors.projects },
      experience: {
        ...exp,
        ...(roles >= 2 ? { minItems: roles } : {}),
        items: {
          ...exp.items,
          properties: {
            ...exp.items.properties,
            bullets: { ...exp.items.properties.bullets, minItems: floors.bullets },
          },
        },
      },
    },
  };
}

async function callOllama(baseUrl, model, systemPrompt, userMessage, numCtx, format = null, temperature = 0.15) {
  const body = JSON.stringify({
    model,
    system: systemPrompt,
    prompt: userMessage,
    stream: false,
    // Qwen3.5 and its siblings are hybrid-reasoning models: left to themselves
    // Ollama routes the answer into a `thinking` field and returns `response` as
    // an empty string, so every offer fails with "No JSON object found" and the
    // model looks incapable of structured output when it is merely thinking.
    // Accepted and ignored by the non-reasoning models already in the pipeline
    // (verified against snipe-cv and snipe-eval), so it is safe unconditionally.
    think: false,
    ...(format ? { format } : {}),
    // num_predict 2400: ample for the richer JSON (realistic output ~1.1-1.4k
    // tokens — summary, 6-9 competencies, 3-4 project descriptions, 5-6 skill
    // categories, experience bullets). Inputs are trimmed (Block E brief +
    // capped JD + base64-stripped CV) to keep input + output under the 8k window.
    options: { num_ctx: numCtx, temperature, num_predict: 2400 },
  });

  const resp = await fetch(`${baseUrl}/api/generate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body,
    signal: AbortSignal.timeout(300_000),
  });

  if (!resp.ok) throw new Error(`Ollama HTTP ${resp.status}: ${await resp.text()}`);
  const data = await resp.json();
  logCall(format ? 'p3-tailor' : 'p3-summary', model, data);
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

// Rank the CV's own skill items by JD token overlap. Definitionally grounded —
// they are lifted verbatim from cv.md — so this is the top-up source when the
// report's keywords do not survive the grounding filter below.
function rankCvSkills(cv, jd) {
  const sec = (cv || '').split(/^##\s+/m).find(s => /^Skills/i.test(s)) || '';
  // Drop the parenthesised asides BEFORE splitting: several items carry commas
  // inside them ("Kubernetes (working knowledge, self-study …)"), and splitting
  // first shipped "Kubernetes (Working Knowledge" as a competency tag.
  const items = [...sec.matchAll(/^\*\*[^*]+:\*\*\s*(.+)$/gm)]
    .flatMap(m => m[1].replace(/\([^)]*\)/g, '').split(','))
    .map(s => s.trim())
    .filter(s => s.length >= 2 && s.length <= 34);
  const jdTok = new Set(tokenize(jd));
  return items
    .map((item, idx) => ({ item, idx, score: tokenize(item).filter(t => jdTok.has(t)).length }))
    .sort((a, b) => b.score - a.score || a.idx - b.idx)
    .map(s => s.item);
}

// Build 6-9 competency tags from the report keywords (already JD-extracted by the
// evaluator), de-duplicated and cased.
//
// The keywords come from the JD, so they describe the ROLE, not the candidate —
// printing them unfiltered put "Clinical AI Agents", "Dora Platform" and "NHS
// Consultations" on a CV as claimed competencies (report 152). A tag only ships
// if it appears in cv.md AS A PHRASE; the shortfall is topped up from the CV's
// own skills, ranked by JD overlap. The model's own competencies are the last
// resort and are held to the same filter.
//
// Word-by-word grounding is not enough: it cleared "Graph Analytics" for the Neo4j
// CV (report 155) because "graph" occurs inside "federated graph" and "analytics"
// inside "Security Analytics Dashboard" — two unrelated places, and the candidate
// has never touched a graph database. The phrase has to be there.
function deriveCompetencies(report, fallback, cvText, jdText) {
  const norm = s => String(s || '').toLowerCase().replace(/[^a-z0-9+#.]+/g, ' ').trim();
  const cvNorm = ` ${norm(cvText)} `;
  const grounded = (kw) => {
    const n = norm(kw);
    return n.length >= 2 && cvNorm.includes(` ${n} `);
  };
  const seen = new Set();
  const out = [];
  const add = (kw) => {
    if (out.length >= 9) return;
    if (kw.length < 2 || kw.length > 34) return;
    const n = norm(kw);
    // Overlap, not just equality: "TypeScript" from the JD and "TypeScript /
    // JavaScript" from the CV skills are one tag, not two.
    if (seen.has(n) || [...seen].some(s => s.includes(n) || n.includes(s))) return;
    seen.add(n);
    out.push(caseKeyword(kw));
  };

  for (const kw of extractReportKeywords(report)) if (grounded(kw)) add(kw);
  if (out.length < 6) for (const kw of rankCvSkills(cvText, jdText)) { if (out.length >= 9) break; add(kw); }
  if (out.length < 6 && Array.isArray(fallback)) for (const kw of fallback) if (grounded(kw)) add(kw);
  return out;
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
    'Evaluated', `${args.evalScore}/5`, 'N',
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
// Block B is both what cv-select ranks against and what the summary stage
// uses to decide which evidence to foreground, so it is parsed once.
const blockBReqs = extractBlockBRequirements(reportText);

// Selection costs one 30B judge call — measured 66 s, 80 % of Phase 3's wall
// clock — and is identical for every writer variant, because none of them touch
// cv-select. A generation A/B was therefore paying ~35 min per 32-offer arm to
// recompute the same answer. SNIPE_SELECT_CACHE points at a JSON file keyed on
// everything selection actually reads, so a sweep pays the judge once.
//
// Off unless the env var is set: a production run must always select fresh, and
// a stale cache is a silent wrong answer rather than a loud failure.
const SELECT_CACHE = process.env.SNIPE_SELECT_CACHE || '';
const selectKey = () => createHash('sha1')
  .update(JSON.stringify([args.id, cvText, blockBReqs, jdText]))
  .digest('hex').slice(0, 20);

function cachedSelection() {
  if (!SELECT_CACHE || !existsSync(SELECT_CACHE)) return null;
  try { return JSON.parse(readFileSync(SELECT_CACHE, 'utf8'))[selectKey()] ?? null; }
  catch { return null; }
}
function storeSelection(text) {
  if (!SELECT_CACHE) return;
  try {
    mkdirSync(dirname(SELECT_CACHE), { recursive: true });
    let all = {};
    try { all = JSON.parse(readFileSync(SELECT_CACHE, 'utf8')); } catch {}
    all[selectKey()] = text;
    writeFileSync(SELECT_CACHE, JSON.stringify(all), 'utf8');
  } catch { /* a cache that cannot be written is not a run that should fail */ }
}

const cached = cachedSelection();
let cvForPrompt = cached ?? cvText;
if (!cached) try {
  // Exemplars turn on the 30B reranker inside selectCvForJd (+0.10 pair
  // accuracy on the gold set). Loaded here rather than there because goldset
  // imports cv-select, and the reverse edge is a cycle.
  let judgeShots = [];
  try {
    const { loadExemplars } = await import('./goldset.mjs');
    judgeShots = loadExemplars(cvText);
  } catch { /* no exemplars: cosine-only selection, same as before */ }
  // Selection budget. SNIPE_LINE_BUDGET=0 restores count-based selection, and
  // the three count knobs exist so the E2 control — naive count-cutting tuned
  // until it fits one page — is runnable as an arm. Defaults are the shipped
  // values, so an unset environment is the shipped selector.
  const num = (k, d) => parseInt(process.env[k] ?? String(d), 10) || d;
  const lineBudget = parseInt(process.env.SNIPE_LINE_BUDGET ?? '21', 10) || null;
  cvForPrompt = await selectCvForJd(
    cvText, blockBReqs, jdText,
    { ollamaUrl: args.ollamaUrl, judgeShots, lineBudget,
      maxBulletsPerRole:   num('SNIPE_MAX_ROLE_BULLETS', 4),
      projectBulletBudget: num('SNIPE_PROJ_BUDGET', 8),
      maxProjects:         num('SNIPE_MAX_PROJECTS', 4) });
  storeSelection(cvForPrompt);
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
  .replace('{{JD_FULL}}',           cleanJd(jdText))
  .replace('{{EXPERIENCE_COMPANIES}}', experienceCompanies(cvForPrompt).join(' | ') || '(every company in the CV above)');

const userMessage = `Tailor the CV for ${args.company} — ${args.role}. Score: ${args.evalScore}/5. Report: ${args.reportPath}`;
const tailorSchema = schemaWithExperienceFloor(cvForPrompt);

// Generate with one validate-and-repair retry. We keep the latest parseable
// JSON so a word-count miss on the retry still ships (clamped) rather than
// failing the whole offer; only a total parse failure is fatal.
let cvContent = null;
let lastErr   = '';
if (args.writer === 'verbatim') {
  // No generation call. The selection is rendered as cv.md words it, and the
  // summary stage below still runs — so this isolates the *bullet* rewrite as the
  // single variable, rather than testing two changes at once.
  //
  // Project bullets are a separate axis, flagged separately for the same reason:
  // bundling the rendering change into the writer change would make a win
  // unattributable to either. 4 is a per-project ceiling rather than a count —
  // cv-select allocates the total budget, so this only has to not clip it.
  cvContent = verbatimContent(cvForPrompt, cvText, jdText,
    { projectBullets: parseInt(process.env.SNIPE_PROJECT_BULLETS ?? '4', 10) });

  // Both are overwritten unconditionally by the Tier-3 blocks further down;
  // an empty array here just keeps the shape valid until they are.
  cvContent.summary = '';
  cvContent.competencies = [];
  cvContent.education_modules = [];
} else for (let attempt = 1; attempt <= 2; attempt++) {
  const um = attempt === 1
    ? userMessage
    : `${userMessage}\n\nYour previous JSON had these problems: ${lastErr}. Return ONLY corrected JSON in the exact schema. The "summary" MUST be 50-70 words in implied first person (no name, no he/she).`;
  let parsed;
  try {
    const raw = await callOllama(args.ollamaUrl, args.model, systemPrompt, um, args.numCtx, tailorSchema, args.temperature);
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
// The non-model writers leave `summary` empty on purpose — the stage below fills
// it — so only the model path is held to having one at this point.
if (args.writer === 'model' && (!cvContent.summary || !Array.isArray(cvContent.experience))) {
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
  // The backfill floor has to match the schema floor, or the two disagree and the
  // model's 4th project is dropped here and never replaced (4 of 12 offers).
  cvContent.projects = remapProjectNames(cvContent.projects, cvText, contentFloors(cvForPrompt).projects);
}

// Tier 3 — the model doesn't reliably keep UK reverse-chronological order;
// re-sort both sections by real CV end date regardless of its output order.
// Tier 3 — reconcile experience against the employers the CV actually lists,
// before ordering. The schema floor guarantees the entry count; this guarantees
// they are the right employers, one each, with a missing one backfilled from the
// CV rather than left out.
cvContent.experience = reconcileExperience(cvContent.experience, cvForPrompt);
// Tier 3 — revert any bullet asserting a figure cv.md does not state. The
// prompt-side fix for this measured zero effect (ledger V4).
cvContent.experience = verifyBulletNumbers(cvContent.experience, cvText);
// Tier 3 — and the mirror: revert a bullet that dropped figures its source had.
// Gated behind a flag until measured, because reverting was expected to cost the
// JD keywords the rewrite added. Paired over 24 offers it did the opposite:
// ats_coverage +0.025, CI [0.014, 0.039], 16 wins 1 loss — the full CV bullet
// carries more of the posting's vocabulary than the 7B's truncation of it did.
// Cost is 2 offers of 24 losing one bullet to a revert collision.
cvContent.experience = verifyBulletFigures(cvContent.experience, cvText);
cvContent.experience = enforceChronoOrder(cvContent.experience, cvText, 'Experience', 'company');
if (Array.isArray(cvContent.projects)) {
  cvContent.projects = enforceChronoOrder(cvContent.projects, cvText, 'Projects', 'name');
}

// Tier 3 — replace the coder model's two weakest fields with deterministic,
// JD-grounded selections. Competencies come from the report's already-extracted
// keywords; education modules are ranked by JD token overlap.
cvContent.competencies = deriveCompetencies(reportText, cvContent.competencies, cvText, jdText);
const rankedModules = rankModules(cvText, jdText, 5);
if (rankedModules.length) cvContent.education_modules = rankedModules;

// G4 — the summary is generated by its own call, from the bullets that will
// actually appear on this CV. As one field in a large JSON blob it competed with
// five other sections for attention, and its repair path built the prompt from
// the JD and the profile narrative *without ever seeing the selected bullets* —
// structurally guaranteed to pull the summary toward the posting.
//
// The JSON field is still generated and still the fallback: if this call fails
// the offer ships the old summary rather than nothing.
try {
  const bullets = selectedBullets(cvForPrompt);
  if (bullets.length) {
    const generated = await generateSummary({
      bullets, role: args.role, cvText, incumbent: cvContent.summary,
      call: (sys, usr) => callOllama(args.ollamaUrl, args.model, sys, usr,
                                     args.numCtx, null, args.temperature),
    });
    if (generated) cvContent.summary = generated;
  }
} catch (err) {
  process.stderr.write(`summary stage failed (${err.message}) — keeping the JSON summary\n`);
}

const distOf = n => (n < 50 ? 50 - n : n > 70 ? n - 70 : 0);
// Benchmark-only: the summary as the model wrote it, before any guard touches
// it. Without this the harness can only see the summary the guards already
// repaired, so summary_fab would read 0 by construction and score a perfect mark
// for a model that fabricates on every offer — the example_copy_pct mistake in
// benchmark rule 5. Gated on --bench-dir so a real run's cv-content.json keeps
// its shape.
if (args.benchDir && typeof cvContent.summary === 'string') {
  cvContent._summary_pre_guard = cvContent.summary;
}

if (typeof cvContent.summary === 'string') {
  // Sibling strip: a years-of-experience claim the CV never makes. The number
  // guard above cannot see this one — "2+" occurs elsewhere in the CV, so the
  // token is allowed; it is the tenure that is invented.
  cvContent.summary = stripUnsupportedTenure(cvContent.summary, cvText);

  // The summary's siblings to verifyBulletFigures / verifyProjectFigures /
  // verifyBulletProducts. It had none of them: one shipped summary claimed a
  // platform "serving 150+ users" (the CV says 170) and called the candidate a
  // "Russell Group graduate" (the university is post-1992), while metric_fab
  // reported 0 because it only reads experience bullets.
  //
  // The product strip runs here as well as inside generateSummary, because the
  // stage is in a try/catch: when it fails, the JSON summary ships, and that path
  // was never product-guarded despite T2's comment claiming the summary was.
  //
  // Order matters — all three can shorten the summary, and the 50-word floor pad
  // below has to see the shortened text so it can top it back up.
  cvContent.summary = verifySummaryFigures(cvContent.summary, cvText);
  cvContent.summary = stripFabricatedCredentials(cvContent.summary, cvText);
  cvContent.summary = stripFabricatedProducts(cvContent.summary, cvText);
  // The general case of the company-name strip below: any name the posting
  // supplies and cv.md does not. The `--company` comparison alone missed a
  // summary claiming work "for Joybuy Systems" on a JD.com posting.
  cvContent.summary = stripJdProperNouns(cvContent.summary, cvText, jdText);

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

// T2 — a named product cv.md never mentions is stripped, not counted. The
// two-tier vocabulary rule (capability phrases free, named products grounded)
// is aspirational until violations are actually rejected. Experience bullets
// revert to their CV source rather than losing the sentence; project blurbs and
// the summary get clause surgery, since they have no single source line.
cvContent.experience = verifyBulletProducts(cvContent.experience, cvText);
cvContent.skills = filterSkillItems(cvContent.skills, cvText);
if (Array.isArray(cvContent.projects)) {
  for (const p of cvContent.projects) {
    if (typeof p.description === 'string') {
      p.description = stripFabricatedProducts(p.description, cvText);
    }
  }
  // ...and the figures, scoped to each project's own CV entry. Projects had no
  // number guard at all, so both a wholly invented "970%+ revenue growth" and a
  // real-but-borrowed "sub-500ms load times" shipped on the same CV.
  cvContent.projects = verifyProjectFigures(cvContent.projects, cvText);
  // Tier 3 — the length floor is asserted LAST, after the two guards above.
  // Padding before them was the obvious placement and the wrong one:
  // stripFabricatedProducts does clause surgery, so a description that met the
  // floor could lose a clause and ship under it (every short description in the
  // floors2/floors3 A/B — 27 words against a floor of 35). Nothing re-checked.
  // Safe to run last because padded text is verbatim cv.md: it names no product
  // the CV lacks and quotes no figure the project's own entry lacks, so it
  // passes both guards by construction rather than by inspection.
  cvContent.projects = padProjectDescriptions(cvContent.projects, cvText);

  // Project bullets, for whichever writer produced the entries.
  //
  // The rendering change is orthogonal to who wrote the prose: projects hold 24
  // of this CV's 33 atoms, and collapsing them into one paragraph loses the same
  // evidence whether that paragraph came from the 7B or from cv.md. Attaching
  // them here rather than in each writer keeps the arms comparable — otherwise a
  // model arm would score badly on differentiator_coverage for a reason that has
  // nothing to do with the model.
  //
  // Verbatim from the selected CV, which cv-select already ranked against this
  // posting. The writer's prose stays as the description; these are the evidence
  // under it.
  const wantProjBullets = parseInt(process.env.SNIPE_PROJECT_BULLETS ?? '4', 10);
  if (wantProjBullets) {
    const sel = parseCvSections(cvForPrompt).find(s => s.name === 'Projects');
    const byName = new Map((sel ? parseEntries(sel.lines).entries : [])
      .map(e => [e.head[0].replace(/^###\s+/, '').trim().toLowerCase(), e.bullets]));
    for (const p of cvContent.projects) {
      // Only where the writer left none — `verbatim` has already filled them.
      const src = byName.get(String(p.name || '').toLowerCase());
      if (src?.length && !p.bullets?.length) p.bullets = src.slice(0, wantProjBullets);
    }
  }
}

// Build output folder
const companySlug = slugify(args.company || 'unknown');
const appDir      = args.benchDir
  ? resolve(args.benchDir, `${args.id || args.reportNum}_${companySlug}`)
  : resolve(PROJECT, `output/${args.date}_${companySlug}_${args.reportNum}`);
const contentFile = resolve(appDir, 'cv-content.json');
const htmlFile    = resolve(appDir, 'source.html');
const cvName      = 'Candidate'; // fallback for PDF filename; overridden by profile.yml full_name below
mkdirSync(appDir, { recursive: true });

// Write content JSON for fill-cv-template.mjs
writeFileSync(contentFile, JSON.stringify(cvContent, null, 2), 'utf8');

// Copy JD
const jdDest = resolve(appDir, 'job-description.txt');
if (jdText) writeFileSync(jdDest, jdText, 'utf8');

// Benchmark stop. Everything past this point (PDF ladder, tracker row, report
// back-fill) is deterministic post-processing that costs a chromium render and
// mutates real user state — none of it measures the model. cv-content.json is
// captured pre-ladder, so the ladder's project trimming cannot mask what the
// model actually returned.
if (args.benchDir) {
  out({ status: 'ok', id: args.id, report_num: args.reportNum,
        company: args.company || 'unknown', role: args.role || 'unknown',
        score: args.evalScore, pdf: null, report: args.reportPath,
        tracker: null, content: contentFile, error: null });
  process.exit(0);
}

// Derive candidate name from profile.yml for PDF filename
let candidateName = cvName;
try {
  const profileText = readSafe(resolve(PROJECT, 'config/profile.yml'));
  const m = profileText.match(/full_name:\s*["']?([^"'\n]+)["']?/);
  if (m) candidateName = m[1].trim().replace(/\s+/g, '-');
} catch {}

const pdfFile   = resolve(appDir, `${candidateName}-CV.pdf`);
const fillScript = resolve(__dirname, 'fill-cv-template.mjs');
const generatePdf= resolve(PROJECT, 'generate-pdf.mjs');

function runFill(maxSkills, maxBullets, maxProjectBullets) {
  const a = [
    fillScript,
    '--content',    contentFile,
    '--output',     htmlFile,
    '--max-skills', String(maxSkills),
  ];
  if (args.role) a.push('--role', args.role);
  if (maxBullets) a.push('--max-bullets', String(maxBullets));
  if (maxProjectBullets) a.push('--max-project-bullets', String(maxProjectBullets));
  execFileSync(process.execPath, a, { stdio: 'inherit', cwd: PROJECT });
}

// Keep the N projects most relevant to the JD, in their existing (chronological)
// order. enforceChronoOrder has already re-sorted by date, so the tail slice this
// replaces dropped the OLDEST project rather than the weakest — on the Edenred
// Java/Spring JD that silently cut the Java/Spring project (report 149), the one
// thing the CV most needed to show.
function trimProjectsByRelevance(list, n, jdTok) {
  if (!Array.isArray(list) || list.length <= n) return list;
  const score = p => [...new Set(tokenize(`${p.name} ${p.description || ''}`))]
    .filter(t => jdTok.has(t)).length;
  const keep = new Set([...list].sort((a, b) => score(b) - score(a)).slice(0, n));
  return list.filter(p => keep.has(p));
}
const jdTokens = new Set(tokenize(jdText));

// Tier 5 — relevance-preserving density ladder. The summary is NEVER cut to fit
// the page; we only reduce experience-bullet depth (fill caps per role, hitting
// the least-relevant backfilled roles too), skill breadth, and the weakest
// (last-ranked) projects.
//
// The target is ONE page. It used to be two, which meant the ladder was
// perfectly satisfied by the output that prompted this whole exercise — it
// stopped at step 0 on every offer and never fired at all.
//
// Its role has changed with it. cv-select now spends a rendered-line budget, so
// selection already fits the page on 31 of 32 offers and the ladder is a safety
// net rather than the mechanism: it exists for the residual that selection
// cannot predict, because chrome moves between runs (the summary is
// model-written and varies by ~200 characters). Steps are therefore gentle at
// the top — one project bullet at a time — where they used to jump straight
// from 4 to 1 and throw away a project's worth of evidence to save two lines.
//
// Step 0 must not clip what cv-select allocated: its caps are the same 4/4 the
// selector bounds itself by, so it renders the selection untouched.
const LADDER = [
  { skills: 6, bullets: 4, projects: 4, projBullets: 4 }, // as selected
  { skills: 6, bullets: 4, projects: 4, projBullets: 3 },
  { skills: 6, bullets: 3, projects: 4, projBullets: 2 },
  { skills: 5, bullets: 3, projects: 3, projBullets: 2 },
  { skills: 5, bullets: 2, projects: 3, projBullets: 1 },
  { skills: 4, bullets: 2, projects: 2, projBullets: 1 }, // tightest
];

let pdfPath = null;
let pdfError = null;
let ladderStep = null;
let ladderPages = null;

// One page is the target; two is the ceiling, not the goal. Every density step
// is tried at one page first, and only if none of them fits does the ladder
// accept two — and then at the FULLEST step, because a CV that has to run to two
// pages should carry the evidence it was going to carry rather than the
// stripped-down version that failed to save it.
outer:
for (const maxPages of [1, 2]) {
  // Every step is tried at one page. At the two-page ceiling only the fullest
  // one is: a CV forced onto a second page should carry the evidence it was
  // going to carry, not the stripped-down version that failed to save it.
  const steps = maxPages === 1 ? LADDER.map((s, i) => [i, s]) : [[0, LADDER[0]]];

  for (const [step, { skills, bullets, projects, projBullets }] of steps) {
    // Trimmed from the full project list every time, not cumulatively from the
    // last step's output — the old loop reassigned cvContent.projects, so once a
    // step had cut to 3 no later step could see the 4th again, and the two-page
    // fallback could not restore what the one-page attempts had shed.
    const kept = trimProjectsByRelevance(cvContent.projects, projects, jdTokens);
    writeFileSync(contentFile, JSON.stringify({ ...cvContent, projects: kept }, null, 2), 'utf8');

    try {
      runFill(skills, bullets, projBullets);
    } catch (err) {
      if (step === 0 && maxPages === 1) fail(`fill-cv-template.mjs failed: ${err.message}`);
      continue; // a later, tighter step may still render
    }

    try {
      execFileSync(process.execPath, [
        generatePdf, htmlFile, pdfFile,
        `--max-pages=${maxPages}`,
        `--source-url=${args.url}`,
      ], { stdio: 'inherit', cwd: PROJECT });
      pdfPath = `output/${args.date}_${companySlug}_${args.reportNum}/${candidateName}-CV.pdf`;
      ladderStep = step;
      ladderPages = maxPages;
      break outer;
    } catch { /* try the next step, or the two-page ceiling */ }
  }
}
if (!pdfPath) pdfError = `PDF still >2 pages after ${LADDER.length} density steps`;

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
  'Evaluated', `${args.evalScore}/5`, pdfPath ? 'Y' : 'N',
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
  // Which density step the page actually needed, and how many pages it took.
  // Step 0 at 1 page is the healthy case: selection fitted and the ladder never
  // fired. Anything else is the ladder paying for evidence, and it is only
  // visible if it is recorded — the old loop kept no trace of which step won.
  ladder_step: ladderStep,
  pages:       ladderPages,
});
