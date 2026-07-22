#!/usr/bin/env node

/**
 * ollama-evaluator.mjs — Phase 2: Full A-G evaluation via local Ollama model.
 *
 * Reads Phase 1 score + JD, runs web search for comp/company data, calls Ollama
 * for a full A-G evaluation, and writes the report .md to disk. Outputs a JSON
 * summary to stdout for the orchestrator to record in state.
 *
 * Does NOT write tracker lines — the orchestrator handles that after Phase 3.
 *
 * Usage:
 *   node batch/ollama-evaluator.mjs --id N --url URL --report-num NNN
 *   node batch/ollama-evaluator.mjs --id N --url URL --report-num NNN \
 *     --model qwen2.5:7b --ollama-url http://localhost:11434 --threshold 3.0
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { stackMismatchCap, languageMismatchCap } from './fit-rules.mjs';
import { cleanCvForPrompt, cleanJd, extractSalary, parseCompTargets, compScoreFromSalary } from './text-utils.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT_DIR = resolve(__dirname, '..');
// let (not const): --bench-dir redirects both so benchmark runs never touch
// real reports/ or batch/evals/ (used by eval-harness.mjs model comparisons).
let REPORTS_DIR = resolve(PROJECT_DIR, 'reports');
let EVALS_DIR = resolve(__dirname, 'evals');

// ── Args ──────────────────────────────────────────────────────────────────────

function parseArgs(argv) {
  const args = {
    id: null,
    url: null,
    reportNum: null,
    model: 'qwen2.5:7b',
    ollamaUrl: 'http://localhost:11434',
    timeout: 300_000,
    threshold: 3.5,
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
    }
  }
  if (args.benchDir) {
    REPORTS_DIR = resolve(args.benchDir, 'reports');
    EVALS_DIR = resolve(args.benchDir, 'evals');
  }
  if (!args.id)        fatal('--id is required');
  if (!args.url)       fatal('--url is required');
  if (!args.reportNum) fatal('--report-num is required');
  return args;
}

// ── Utilities ─────────────────────────────────────────────────────────────────

function fatal(msg) {
  const out = { status: 'eval_failed', error: msg };
  process.stdout.write(JSON.stringify(out) + '\n');
  process.exit(1);
}

function readSafe(p) {
  try { return existsSync(p) ? readFileSync(p, 'utf8').trim() : ''; } catch { return ''; }
}

function slugify(str) {
  return str.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 40);
}

// ── Ollama call ───────────────────────────────────────────────────────────────

async function callOllama(baseUrl, model, systemPrompt, userMessage, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  let data;
  try {
    const res = await fetch(`${baseUrl}/api/chat`, {
      method: 'POST',
      signal: controller.signal,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model,
        messages: [
          { role: 'system', content: systemPrompt },
          { role: 'user', content: userMessage },
        ],
        stream: false,
        // num_predict 3072: the full report + summary runs ~1.5k tokens on the
        // 7B but ~2.5k on more verbose 30B-class models — 2048 truncated the
        // response before the <SUMMARY> block. num_ctx sized for input (~8.5k) + output.
        options: { temperature: 0.15, num_ctx: 14336, num_predict: 3072 },
      }),
    });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      throw new Error(`HTTP ${res.status}: ${body.slice(0, 200)}`);
    }
    data = await res.json();
  } finally {
    clearTimeout(timer);
  }
  return data?.message?.content || '';
}

// ── Parse model output ────────────────────────────────────────────────────────

function extractBlock(raw, tag) {
  const open = `<${tag}>`;
  const close = `</${tag}>`;
  const start = raw.indexOf(open);
  if (start === -1) return null;
  const end = raw.indexOf(close, start);
  if (end !== -1) return raw.slice(start + open.length, end).trim();
  // Closing tag missing (output truncated) — for REPORT, grab up to <SUMMARY> or end of string
  if (tag === 'REPORT') {
    const summaryStart = raw.indexOf('<SUMMARY>', start);
    const fallbackEnd = summaryStart !== -1 ? summaryStart : raw.length;
    const content = raw.slice(start + open.length, fallbackEnd).trim();
    return content.length >= 200 ? content : null;
  }
  // For SUMMARY, grab from open tag to end of string and let parseSummary extract JSON
  return raw.slice(start + open.length).trim() || null;
}

function parseSummary(raw) {
  const tryParse = (s) => { try { return JSON.parse(s); } catch { return null; } };
  const block = extractBlock(raw, 'SUMMARY');
  if (block) {
    // Bare JSON, then fence-wrapped (```json ... ```), then outermost {...} in
    // the block — newer models decorate the block instead of emitting raw JSON.
    let j = tryParse(block);
    if (j) return j;
    const fenced = block.match(/```(?:json)?\s*([\s\S]*?)```/);
    if (fenced) { j = tryParse(fenced[1].trim()); if (j) return j; }
    const s = block.indexOf('{'), e = block.lastIndexOf('}');
    if (s !== -1 && e > s) { j = tryParse(block.slice(s, e + 1)); if (j) return j; }
  }
  // Global fallback: outermost {...} after the report block
  const anchor = raw.lastIndexOf('</REPORT>');
  const tail = anchor !== -1 ? raw.slice(anchor) : raw;
  const s = tail.indexOf('{'), e = tail.lastIndexOf('}');
  if (s !== -1 && e > s) { const j = tryParse(tail.slice(s, e + 1)); if (j) return j; }
  process.stderr.write(`[eval] SUMMARY parse failed; response tail: ${raw.slice(-300).replace(/\n/g, '\\n')}\n`);
  return null;
}

// ── JD re-fetch (fallback when /tmp and batch/jds/ are both missing) ──────────

async function refetchJd(url, timeoutMs = 30_000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  async function httpGet(targetUrl, asJson = false) {
    const res = await fetch(targetUrl, {
      signal: controller.signal,
      headers: { 'User-Agent': 'Mozilla/5.0 (compatible; snipe/1.0)', Accept: asJson ? 'application/json' : 'text/html' },
    });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    return asJson ? res.json() : res.text();
  }

  function stripHtml(html) {
    return html.replace(/<script[\s\S]*?<\/script>/gi, '').replace(/<style[\s\S]*?<\/style>/gi, '')
      .replace(/<[^>]+>/g, ' ').replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&nbsp;/g, ' ').replace(/\s{2,}/g, ' ').trim();
  }

  try {
    const parsed = new URL(url);
    let m, json;

    // Greenhouse job-boards URL
    m = url.match(/greenhouse\.io\/([^/?#]+)\/jobs\/(\d+)/);
    if (m) {
      json = await httpGet(`https://boards-api.greenhouse.io/v1/boards/${m[1]}/jobs/${m[2]}`, true);
      return `${json.title || ''}\n\n${stripHtml(json.content || '')}`.slice(0, 16_000);
    }

    // Greenhouse embedded ?gh_jid=
    const ghJid = parsed.searchParams.get('gh_jid');
    if (ghJid) {
      const board = parsed.searchParams.get('board') || parsed.hostname.replace(/^www\./, '').split('.')[0];
      json = await httpGet(`https://boards-api.greenhouse.io/v1/boards/${board}/jobs/${ghJid}`, true);
      return `${json.title || ''}\n\n${stripHtml(json.content || '')}`.slice(0, 16_000);
    }

    // Ashby
    m = url.match(/jobs\.ashbyhq\.com\/([^/?#]+)\/([a-fA-F0-9-]{36})/);
    if (m) {
      json = await httpGet(`https://api.ashbyhq.com/posting-api/job-board/${m[1]}?includeCompensation=true`, true);
      const p = (json.jobs || json.jobPostings || []).find(x => x.id?.toLowerCase() === m[2].toLowerCase());
      if (!p) throw new Error('Ashby posting not found');
      return `${p.title || ''}\n${p.locationName || ''}\n\n${p.descriptionPlain || stripHtml(p.descriptionHtml || '')}`.slice(0, 16_000);
    }

    // Lever
    m = url.match(/jobs\.lever\.co\/([^/?#]+)\/([a-fA-F0-9-]{36})/);
    if (m) {
      json = await httpGet(`https://api.lever.co/v0/postings/${m[1]}/${m[2]}`, true);
      return `${json.text || ''}\n\n${json.descriptionPlain || stripHtml(json.description || '')}`.slice(0, 16_000);
    }

    // HTML fallback
    const html = await httpGet(url);
    return stripHtml(html).slice(0, 16_000);
  } finally {
    clearTimeout(timer);
  }
}

// ── Build report with correct header (substitutes placeholders) ───────────────

function injectReportHeader(reportText, { url, reportNum, date, id, pdfNote, preScore, score }) {
  // Replace placeholder lines in the report header that the model may have left.
  // The pre-screening score is injected here (not given to the model) so the
  // evaluator reaches its own verdict without anchoring on the Phase 1 number.
  // The headline Score is overwritten with the CODE-COMPUTED value so the report
  // header matches the authoritative score (the model's self-reported number is
  // unreliable and decouples from its own dimension analysis).
  let out = reportText
    .replace(/\*\*URL:\*\*.*/, `**URL:** ${url}`)
    .replace(/\*\*PDF:\*\*.*/, `**PDF:** ${pdfNote}`)
    .replace(/\*\*Batch ID:\*\*.*/, `**Batch ID:** ${id}`)
    .replace(/\*\*Score pre-screening \(local model\):\*\*.*/, `**Score pre-screening (local model):** ${preScore ?? 'N/A'}/5`)
    .replace(/\{pre-score\}/g, preScore ?? 'N/A')
    .replace(/\{today's date\}/, date)
    .replace(/\{url\}/, url)
    .replace(/\{id\}/, id);
  if (score !== null && score !== undefined) {
    // Header line: "**Score:** {X.X}/5" — replace the first occurrence only.
    out = out.replace(/\*\*Score:\*\*\s*[^\n]*/, `**Score:** ${score}/5`);
  }
  return out;
}

// ── Code-owned report blocks ──────────────────────────────────────────────────
// Block D and the Machine Summary are written by CODE, not the model. The model
// used to echo template placeholders here ("hiring freeze / active hiring / no
// data") which Block G then treated as real signals, and the Machine Summary
// kept a self-reported score that disagreed with the header.

function buildBlockD(salary, compDim, targets) {
  const lines = ['## D) Comp & Demand', ''];
  if (salary) {
    const range = salary.min === salary.max
      ? `${salary.currency}${salary.min.toLocaleString()}`
      : `${salary.currency}${salary.min.toLocaleString()}–${salary.currency}${salary.max.toLocaleString()}`;
    const targetNote = targets
      ? `vs target ${salary.currency}${targets.targetLow.toLocaleString()}–${salary.currency}${targets.targetHigh.toLocaleString()} (floor ${salary.currency}${targets.floor.toLocaleString()})`
      : '— no comp targets configured, scored neutral';
    lines.push(
      '| Source | Salary range | Confidence |',
      '|--------|-------------|------------|',
      `| Job posting (parsed) | ${range} | High |`,
      '',
      `**Comp score (1–5):** ${compDim} — posted salary ${targetNote}.`,
    );
  } else {
    lines.push(
      '| Source | Salary range | Confidence |',
      '|--------|-------------|------------|',
      '| Job posting | Not stated | — |',
      '',
      '**Comp score:** excluded from the composite — no salary stated in the posting (score = cv_match × 0.625 + north_star × 0.375).',
    );
  }
  lines.push('');
  return lines.join('\n');
}

// Replace whatever the model wrote between "## D)" and the next "## " heading.
function injectBlockD(report, dText) {
  const re = /^## D\)[^\n]*\n[\s\S]*?(?=^## [E-G]\))/m;
  if (!re.test(report)) return report; // unexpected shape — leave untouched
  return report.replace(re, dText + '\n');
}

// Replace the Machine Summary section with the authoritative, code-computed JSON
// so the report never carries two disagreeing scores.
function injectMachineSummary(report, summaryObj) {
  const json = JSON.stringify(summaryObj);
  const re = /^## Machine Summary\s*\n+[\s\S]*?(?=^---$|^## )/m;
  if (!re.test(report)) return report;
  return report.replace(re, `## Machine Summary\n\n${json}\n\n`);
}

// Drop the web-derived hiring-signal row if the model still emits one — there is
// no web signal anymore, so anything in it is invented.
function stripHiringSignalRow(report) {
  return report.replace(/^\|\s*Company hiring signal[^\n]*\n/gim, '');
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  const args = parseArgs(process.argv.slice(2));

  // Prefer a gitignored personal override (real metrics) if present; else the shipped generic prompt
  const systemPrompt = readSafe(resolve(__dirname, 'ollama-eval-prompt.local.md'))
                    || readSafe(resolve(__dirname, 'ollama-eval-prompt.md'));
  if (!systemPrompt) fatal('ollama-eval-prompt.md not found');

  const scoreFile = resolve(__dirname, 'scores', `${args.id}.json`);
  const jdTmp = `/tmp/batch-jd-${args.id}.txt`;
  const jdPersist = resolve(__dirname, 'jds', `${args.id}.txt`);

  const cv = cleanCvForPrompt(readSafe(resolve(PROJECT_DIR, 'cv.md')));
  const profile = readSafe(resolve(PROJECT_DIR, 'config/profile.md'));
  const config = readSafe(resolve(PROJECT_DIR, 'config/profile.yml'));
  const jdText = readSafe(jdTmp) || readSafe(jdPersist);

  if (!cv) fatal('cv.md not found or empty');

  // JD missing from both cache locations — re-fetch from URL (handles pre-persistence scored offers)
  if (!jdText) {
    try {
      process.stderr.write(`[eval] JD cache miss for #${args.id}, re-fetching from URL...\n`);
      const fetched = await refetchJd(args.url);
      if (!fetched || fetched.length < 100) fatal(`Re-fetched JD too short (${fetched?.length ?? 0} chars) — posting may be closed`);
      mkdirSync(resolve(__dirname, 'jds'), { recursive: true });
      writeFileSync(jdPersist, fetched, 'utf8');
      // reassign — can't use const here, use a local block
      Object.assign(args, { _jdText: fetched });
    } catch (e) {
      fatal(`JD file not found and re-fetch failed: ${e.message}`);
    }
  }

  const jd = jdText || args._jdText;

  // Read Phase 1 score context
  let scoreCtx = {};
  if (existsSync(scoreFile)) {
    try { scoreCtx = JSON.parse(readFileSync(scoreFile, 'utf8')); } catch {}
  }

  // Company/role come from the Phase 1 structured output. Fall back to the old
  // jd_summary regex guess only for score files written before that field existed.
  const jdSummary = scoreCtx.jd_summary || '';
  const companyGuess = scoreCtx.company
    || jdSummary.split(/\s+at\s+/i)[1]?.split(/[,;]/)[0]?.trim()
    || jdSummary.split(/\s+/)[0] || 'unknown';
  const roleGuess = scoreCtx.role || scoreCtx.archetype || 'Software Engineer';

  // Deterministic comp: parse the salary out of the FULL JD text (before any
  // trimming — salary often sits in the benefits tail) and score it in code
  // against the profile targets. No web search, no model guess.
  const salary = extractSalary(jd);
  const compTargets = parseCompTargets(config);
  const compFromSalary = compScoreFromSalary(salary, compTargets);

  const today = new Date().toISOString().split('T')[0];

  const userMessage = [
    `## Context`,
    `URL: ${args.url}`,
    `Report number: ${args.reportNum}`,
    `Date: ${today}`,
    `Batch ID: ${args.id}`,
    '',
    `## Phase 1 Pre-screening Context (qualitative only — numeric score withheld on purpose)`,
    `Archetype: ${scoreCtx.archetype ?? 'Unknown'}`,
    `Hard stops: ${(scoreCtx.hard_stops ?? []).join(', ') || 'None'}`,
    `Soft gaps: ${(scoreCtx.soft_gaps ?? []).join(', ') || 'None'}`,
    `Top strengths: ${(scoreCtx.top_strengths ?? []).join(', ') || 'N/A'}`,
    `JD summary: ${scoreCtx.jd_summary ?? 'N/A'}`,
    `Confidence: ${scoreCtx.confidence ?? 'Medium'}`,
    '',
    `## Posted Salary (parsed from the JD by the system)`,
    salary
      ? `${salary.currency}${salary.min.toLocaleString()}–${salary.currency}${salary.max.toLocaleString()} (raw: "${salary.raw}")`
      : 'No salary stated in the posting. Do NOT guess one.',
    '',
    `## Job Description`,
    cleanJd(jd, 6_000),
    '',
    `## Candidate CV (complete)`,
    cv,
    '',
    `## Candidate Profile`,
    profile || '(no profile)',
    '',
    `## Candidate Configuration`,
    config || '(no config)',
    '',
    [
      'Evaluate this job offer for the candidate. Follow the report template exactly.',
      `Use the Phase 1 pre-screening as context but derive your own authoritative score.`,
      `Output the full report between <REPORT> and </REPORT>, then the JSON summary between <SUMMARY> and </SUMMARY>.`,
    ].join(' '),
  ].join('\n');

  // Call Ollama
  let raw;
  try {
    raw = await callOllama(args.ollamaUrl, args.model, systemPrompt, userMessage, args.timeout);
  } catch (e) {
    fatal(`Ollama API error: ${e.message}`);
  }

  // Extract report block
  const reportText = extractBlock(raw, 'REPORT');
  if (!reportText || reportText.length < 200) {
    fatal(`Model did not produce a valid <REPORT> block. Raw output: ${raw.slice(0, 300)}`);
  }

  // Extract summary JSON
  const summary = parseSummary(raw);

  const company = summary?.company || companyGuess;
  const role = summary?.role || roleGuess;
  const archetype = summary?.archetype || scoreCtx.archetype || 'Unknown';

  // Compute the authoritative score IN CODE from the integer dimensions — never
  // trust the model's self-reported `score`, which decouples from its own analysis
  // and collapses toward ~4 on small models. Falls back to the reported score only
  // if the dimensions are missing (older/malformed outputs).
  const clampDim = (v) => {
    const n = Math.round(Number(v));
    return Number.isFinite(n) ? Math.min(5, Math.max(1, n)) : null;
  };
  let cvDim = clampDim(summary?.cv_match);
  let nsDim = clampDim(summary?.north_star);
  // Comp comes ONLY from the code-parsed posted salary (null when not stated).
  // The model's comp guess is ignored — with no data it hallucinated (e.g.
  // scoring 2 for "salary not disclosed" against the rubric).
  const compDim = compFromSalary;
  const rfDim = clampDim(summary?.red_flags_score) ?? 5; // informational only — NOT scored

  // Code-enforced SENIORITY CAP. The candidate is early-career, so roles demanding
  // Staff/Principal/etc. are a stretch the 7B model fails to penalise on its own.
  // Detect required seniority from the role TITLE (reliable) + explicit year demands
  // in the JD head, and cap cv_match / north_star accordingly.
  const roleTitle = String(role || '').toLowerCase();
  // Scan the whole JD for explicit year demands — they often sit below the
  // first screenful (e.g. in a "Requirements" block), which is why some senior
  // roles slipped through when only the head was scanned.
  const jdAll = String(jd || '').toLowerCase();
  const highYears = /\b(8|9|10|11|12|15)\+?\s*years/.test(jdAll);
  const midYears = /\b(5|6|7)\+?\s*years/.test(jdAll);
  // "Forward Deployed" (Palantir/Sierra FDE) and "architect" are senior-demand
  // roles whose titles omit the usual Staff/Senior keyword — treat them as a
  // seniority stretch for an early-career candidate.
  const fwdDeployed = /\bforward[\s-]?deployed\b|\bfde\b/.test(roleTitle);
  const architect = /\barchitect\b/.test(roleTitle);
  if (cvDim !== null && nsDim !== null) {
    if (/\b(staff|principal|distinguished|director|vp|vice president|head of)\b/.test(roleTitle) || architect || highYears) {
      cvDim = Math.min(cvDim, 2); nsDim = Math.min(nsDim, 3);
    } else if (/\b(senior|sr\.?|lead)\b/.test(roleTitle) || fwdDeployed || midYears) {
      cvDim = Math.min(cvDim, 3); nsDim = Math.min(nsDim, 4);
    }
  }

  // Code-enforced STACK-MISMATCH CAP. The 7B inflates cv_match when the domain
  // looks familiar even though the role's core language/platform is one the
  // candidate doesn't have. If the JD's required ecosystem(s) don't overlap the
  // candidate's (parsed from cv.md), cap cv_match at 3 — a real but rampable gap.
  const stack = stackMismatchCap(jd, cv);
  if (cvDim !== null && stack.cap < 5) {
    cvDim = Math.min(cvDim, stack.cap);
  }

  // Code-enforced LANGUAGE-MISMATCH CAP: a required natural language the
  // candidate lacks (e.g. "German speaking") is a hard blocker the model
  // under-penalises even when it flags it.
  const langCap = languageMismatchCap(jd, cv);
  if (langCap.missing && cvDim !== null && nsDim !== null) {
    cvDim = Math.min(cvDim, langCap.cvCap);
    nsDim = Math.min(nsDim, langCap.nsCap);
  }

  // Authoritative score computed IN CODE from the (capped) integer dimensions.
  // red_flags is intentionally excluded from the formula — the model hallucinates
  // deal-breakers; genuine ones already show up via low cv_match/north_star and are
  // surfaced separately in hard_stops for the human to see.
  // Comp enters the formula ONLY when the posting states a salary (cv:ns stays
  // 5:3 either way, so scores remain comparable with Phase 1 and with each other).
  let score;
  if (cvDim !== null && nsDim !== null) {
    score = compDim !== null
      ? Math.round((cvDim * 0.50 + nsDim * 0.30 + compDim * 0.20) * 10) / 10
      : Math.round((cvDim * 0.625 + nsDim * 0.375) * 10) / 10;
  } else {
    score = typeof summary?.score === 'number' ? summary.score : (scoreCtx.score ?? null);
  }
  // pdf_decision derives from the computed score, not the model's claim.
  const pdfDecision = score !== null ? score >= args.threshold : false;

  // Clamp the model's decision to the authoritative score band — it happily says
  // "Apply" on a sub-3 score. Only downgrade; within-band judgment stays its own.
  let finalDecision = summary?.final_decision ?? 'Consider';
  if (score !== null) {
    if (score < 3) finalDecision = 'Skip';
    else if (score < 3.5 && finalDecision === 'Apply') finalDecision = 'Consider';
  }

  const slug = slugify(company);
  const reportFilename = `${args.reportNum}-${slug}-${today}.md`;
  const reportPath = resolve(REPORTS_DIR, reportFilename);

  const pdfNote = pdfDecision
    ? `to be generated in Phase 3`
    : `not generated — run /snipe pdf ${slug} to create on demand`;

  // Authoritative machine summary — the code-computed dims/score, NOT the model's
  // self-reported object (which used to disagree with the header score).
  const machineSummary = {
    company,
    role,
    cv_match: cvDim,
    north_star: nsDim,
    comp_inferred: compDim,
    red_flags_score: rfDim,
    score,
    archetype,
    final_decision: finalDecision,
    hard_stops: summary?.hard_stops ?? [],
    soft_gaps: summary?.soft_gaps ?? [],
    top_strengths: summary?.top_strengths ?? [],
    legitimacy_tier: summary?.legitimacy_tier ?? 'Proceed with Caution',
    pdf_decision: pdfDecision,
    notes: summary?.notes ?? '',
  };

  // Inject orchestrator-controlled fields + code-owned blocks into the report
  let finalReport = injectReportHeader(reportText, {
    url: args.url,
    reportNum: args.reportNum,
    date: today,
    id: args.id,
    pdfNote,
    preScore: scoreCtx.score ?? null,
    score,
  });
  finalReport = injectBlockD(finalReport, buildBlockD(salary, compDim, compTargets));
  finalReport = injectMachineSummary(finalReport, machineSummary);
  finalReport = stripHiringSignalRow(finalReport);

  mkdirSync(REPORTS_DIR, { recursive: true });
  writeFileSync(reportPath, finalReport, 'utf8');

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
    model_reported_score: typeof summary?.score === 'number' ? summary.score : null,
    error: null,
  };

  // Persist to batch/evals/<id>.json so pdf_offer() can find metadata across restarts
  writeFileSync(resolve(EVALS_DIR, `${args.id}.json`), JSON.stringify(output, null, 2) + '\n', 'utf8');

  process.stdout.write(JSON.stringify(output, null, 2) + '\n');
}

main().catch(e => {
  process.stdout.write(JSON.stringify({ status: 'eval_failed', error: e.message }) + '\n');
  process.exit(1);
});
