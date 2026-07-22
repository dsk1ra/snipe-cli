#!/usr/bin/env node

/**
 * ollama-scorer.mjs — Phase 1 scorer for the local batch pipeline.
 *
 * Fetches a job description, reads the candidate's CV and profile from disk,
 * calls a local Ollama model to score the offer, writes the result to
 * batch/scores/<id>.json, and prints a JSON summary to stdout.
 *
 * The saved JD file (/tmp/batch-jd-<id>.txt) is reused by local-runner.sh
 * when spawning the Phase 2 Sonnet tailoring worker.
 *
 * Usage:
 *   node batch/ollama-scorer.mjs --id 42 --url https://...
 *   node batch/ollama-scorer.mjs --id 42 --url https://... --model qwen2.5:7b
 *   node batch/ollama-scorer.mjs --id 42 --url https://... --ollama-url http://localhost:11434
 *   node batch/ollama-scorer.mjs --id 42 --url https://... --jd-file /path/to/jd.txt
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { stackMismatchCap, languageMismatchCap, seniorityCaps } from './fit-rules.mjs';
import { cleanCvForPrompt, cleanJd } from './text-utils.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT_DIR = resolve(__dirname, '..');

// ── Argument parsing ──────────────────────────────────────────────────────────

function parseArgs(argv) {
  const args = {
    id: null,
    url: null,
    jdFile: null,
    model: 'snipe-screen',
    ollamaUrl: 'http://localhost:11434',
    timeout: 120_000,
  };

  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--id':          args.id = argv[++i]; break;
      case '--url':         args.url = argv[++i]; break;
      case '--jd-file':     args.jdFile = argv[++i]; break;
      case '--model':       args.model = argv[++i]; break;
      case '--ollama-url':  args.ollamaUrl = argv[++i]; break;
      case '--timeout':     args.timeout = parseInt(argv[++i], 10) * 1000; break;
    }
  }

  if (!args.id)  fatal('--id is required');
  if (!args.url) fatal('--url is required');

  return args;
}

// ── Utilities ─────────────────────────────────────────────────────────────────

function fatal(msg, detail = null) {
  const out = { status: 'failed', id: null, score: null, error: msg + (detail ? ': ' + detail : '') };
  process.stdout.write(JSON.stringify(out) + '\n');
  process.exit(1);
}

function readFileSafe(p) {
  try {
    return existsSync(p) ? readFileSync(p, 'utf8').trim() : '';
  } catch {
    return '';
  }
}

function stripHtml(html) {
  return html
    .replace(/<script[\s\S]*?<\/script>/gi, '')
    .replace(/<style[\s\S]*?<\/style>/gi, '')
    .replace(/<[^>]+>/g, ' ')
    .replace(/&amp;/g, '&')
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&nbsp;/g, ' ')
    .replace(/&#\d+;/g, ' ')
    .replace(/&[a-z]{2,6};/g, ' ')
    .replace(/\s{2,}/g, ' ')
    .trim();
}

// ── JD fetching ───────────────────────────────────────────────────────────────

// Signals that a posting is definitively gone or blocked — do not retry.
class ProviderError extends Error {
  constructor(msg) { super(msg); this.name = 'ProviderError'; }
}

async function httpGet(targetUrl, timeoutMs, asJson = false) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(targetUrl, {
      signal: controller.signal,
      headers: {
        'User-Agent': 'Mozilla/5.0 (compatible; snipe/1.0)',
        'Accept': asJson ? 'application/json' : 'text/html,application/xhtml+xml,application/json',
      },
    });
    if (!res.ok) {
      // 404 = expired, 403 = blocked — both are non-retryable
      if (res.status === 404 || res.status === 403) {
        throw new ProviderError(`HTTP ${res.status} from ${targetUrl} — posting expired or access blocked`);
      }
      throw new Error(`HTTP ${res.status} from ${targetUrl}`);
    }
    return asJson ? res.json() : res.text();
  } finally {
    clearTimeout(timer);
  }
}

// Provider-aware API fetching — avoids JS-rendered pages for known boards.
async function fetchJdFromProvider(url, timeoutMs) {
  let m, json, title, desc;
  const parsed = new URL(url);

  // ── Greenhouse (job-boards.greenhouse.io/{company}/jobs/{id}) ─────────────
  m = url.match(/greenhouse\.io\/([^/?#]+)\/jobs\/(\d+)/);
  if (m) {
    const apiUrl = `https://boards-api.greenhouse.io/v1/boards/${m[1]}/jobs/${m[2]}`;
    json = await httpGet(apiUrl, timeoutMs, true);
    return `${json.title || ''}\n\n${stripHtml(json.content || '')}`.slice(0, 16_000);
  }

  // ── Greenhouse (embedded via ?gh_jid= query param, optional ?board=) ──────
  const ghJid = parsed.searchParams.get('gh_jid');
  if (ghJid) {
    const ghBoard = parsed.searchParams.get('board')
      || parsed.hostname.replace(/^www\./, '').split('.')[0];
    const apiUrl = `https://boards-api.greenhouse.io/v1/boards/${ghBoard}/jobs/${ghJid}`;
    json = await httpGet(apiUrl, timeoutMs, true);
    return `${json.title || ''}\n\n${stripHtml(json.content || '')}`.slice(0, 16_000);
  }

  // ── Ashby (jobs.ashbyhq.com/{company}/{uuid}) ─────────────────────────────
  m = url.match(/jobs\.ashbyhq\.com\/([^/?#]+)\/([a-fA-F0-9-]{36})/);
  if (m) {
    const [, company, postingId] = m;
    const apiUrl = `https://api.ashbyhq.com/posting-api/job-board/${company}?includeCompensation=true`;
    json = await httpGet(apiUrl, timeoutMs, true);
    const posting = (json.jobs || json.jobPostings || []).find(
      p => p.id?.toLowerCase() === postingId.toLowerCase()
    );
    // Posting not in board = definitively closed, not a transient error
    if (!posting) throw new ProviderError(`Ashby posting ${postingId} no longer listed for ${company} — likely closed`);
    title = posting.title || '';
    desc = posting.descriptionPlain || stripHtml(posting.descriptionHtml || posting.descriptionSafeHtml || '');
    const loc = posting.locationName ? `Location: ${posting.locationName}` : '';
    return `${title}\n${loc}\n\n${desc}`.slice(0, 16_000);
  }

  // ── Lever (jobs.lever.co/{company}/{uuid}) ────────────────────────────────
  m = url.match(/jobs\.lever\.co\/([^/?#]+)\/([a-fA-F0-9-]{36})/);
  if (m) {
    const [, company, postingId] = m;
    const apiUrl = `https://api.lever.co/v0/postings/${company}/${postingId}`;
    json = await httpGet(apiUrl, timeoutMs, true);
    title = json.text || '';
    desc = json.descriptionPlain || stripHtml(json.description || '');
    const lists = (json.lists || []).map(l => `${l.text}:\n${l.content}`).join('\n\n');
    const categories = json.categories ? JSON.stringify(json.categories) : '';
    return `${title}\n${categories}\n\n${desc}\n\n${lists}`.slice(0, 16_000);
  }

  return null; // No provider matched — caller falls back to HTML scraping
}

async function fetchJd(url, jdFilePath, timeoutMs) {
  if (jdFilePath && existsSync(jdFilePath)) {
    const content = readFileSync(jdFilePath, 'utf8').trim();
    if (content.length > 50) return content;
  }

  // Provider APIs first — if they throw ProviderError, propagate immediately (no HTML fallback)
  let providerText = null;
  try {
    providerText = await fetchJdFromProvider(url, timeoutMs);
  } catch (e) {
    if (e instanceof ProviderError) throw e; // expired/blocked — stop here
    // transient provider error — fall through to HTML scraping
  }
  if (providerText && providerText.length > 100) return providerText;

  // Fall back to raw HTML scraping for unknown providers
  const html = await httpGet(url, timeoutMs, false);
  const stripped = stripHtml(html);
  if (stripped.length < 100) throw new Error(
    `JD content too short after HTML stripping (${stripped.length} chars) — page may be JS-rendered and no provider handler matched`
  );
  return stripped.slice(0, 16_000);
}

// ── Ollama API ────────────────────────────────────────────────────────────────

async function checkOllamaHealth(baseUrl, model, timeoutMs) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 5_000);

  let tags;
  try {
    const res = await fetch(`${baseUrl}/api/tags`, { signal: controller.signal });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    tags = await res.json();
  } catch (e) {
    fatal('Ollama is not running', `start it with: ollama serve (${e.message})`);
  } finally {
    clearTimeout(timer);
  }

  const models = (tags.models || []).map(m => m.name);
  const found = models.some(m => m === model || m.startsWith(model.replace(':latest', '') + ':'));
  if (!found) {
    fatal(`Model "${model}" not found in Ollama`, `pull it with: ollama pull ${model} (available: ${models.slice(0, 5).join(', ')})`);
  }
}

// Schema-constrained decoding (Ollama `format`): the grammar guarantees valid
// JSON with integer dimensions in [1,5] — no prose, no parse retries. `company`
// and `role` are extracted here once so Phase 2 never regex-guesses them.
const SCORE_SCHEMA = {
  type: 'object',
  properties: {
    company:         { type: 'string' },
    role:            { type: 'string' },
    cv_match:        { type: 'integer', minimum: 1, maximum: 5 },
    north_star:      { type: 'integer', minimum: 1, maximum: 5 },
    red_flags_score: { type: 'integer', minimum: 1, maximum: 5 },
    archetype:       { type: 'string' },
    hard_stops:      { type: 'array', items: { type: 'string' } },
    soft_gaps:       { type: 'array', items: { type: 'string' }, maxItems: 5 },
    top_strengths:   { type: 'array', items: { type: 'string' }, maxItems: 3 },
    jd_summary:      { type: 'string' },
    confidence:      { type: 'string', enum: ['Low', 'Medium', 'High'] },
  },
  required: ['company', 'role', 'cv_match', 'north_star', 'red_flags_score',
             'archetype', 'hard_stops', 'soft_gaps', 'top_strengths',
             'jd_summary', 'confidence'],
};

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
        format: SCORE_SCHEMA,
        options: {
          temperature: 0.1,
          num_ctx: 12288,
        },
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

// ── JSON extraction from model response ──────────────────────────────────────

function extractJson(raw) {
  // Model should output raw JSON but may wrap it in a fence or add prose
  // Try 1: parse the whole response
  try { return JSON.parse(raw.trim()); } catch {}

  // Try 2: extract from a ```json ... ``` fence
  const fenced = raw.match(/```(?:json)?\s*([\s\S]*?)```/);
  if (fenced) {
    try { return JSON.parse(fenced[1].trim()); } catch {}
  }

  // Try 3: find the first { ... } block in the response
  const start = raw.indexOf('{');
  const end = raw.lastIndexOf('}');
  if (start !== -1 && end > start) {
    try { return JSON.parse(raw.slice(start, end + 1)); } catch {}
  }

  throw new Error('Could not extract JSON from model response. Raw: ' + raw.slice(0, 500));
}

// Clamp a model-reported dimension to an integer in [1,5]; null if unparseable.
function clampDim(v) {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(5, Math.max(1, n)) : null;
}

function validateScore(parsed, { cvCap = 5, nsCap = 5 } = {}) {
  let cv = clampDim(parsed.cv_match);
  let ns = clampDim(parsed.north_star);
  const rf = clampDim(parsed.red_flags_score) ?? 5;          // informational only — NOT scored

  // The composite is computed HERE from the integer dimensions — never trust the
  // model's self-reported `score`, which collapses toward the centre on small models.
  // red_flags is EXCLUDED from the formula (the 7B hallucinates deal-breakers) —
  // same as Phase 2. Comp is no longer collected at Phase 1 at all: with no salary
  // data it was a dead constant. The score is pure fit: cv_match + north_star in
  // the same 5:3 ratio Phase 2 uses, so the two phases stay methodologically
  // consistent.
  if (cv === null || ns === null) {
    throw new Error(`Missing dimension scores (cv_match=${parsed.cv_match}, north_star=${parsed.north_star})`);
  }
  // Caps (computed in main() from JD + cv.md): stack mismatch caps cv_match;
  // a required natural language the candidate lacks caps both dimensions.
  if (cvCap < 5) cv = Math.min(cv, cvCap);
  if (nsCap < 5) ns = Math.min(ns, nsCap);
  const score = Math.round((cv * 0.625 + ns * 0.375) * 10) / 10;

  return {
    score,
    cv_match: cv,
    north_star: ns,
    red_flags_score: rf,
    company: String(parsed.company || '').trim() || 'unknown',
    role: String(parsed.role || '').trim() || 'unknown',
    archetype: String(parsed.archetype || 'Unknown'),
    hard_stops: Array.isArray(parsed.hard_stops) ? parsed.hard_stops : [],
    soft_gaps: Array.isArray(parsed.soft_gaps) ? parsed.soft_gaps : [],
    top_strengths: Array.isArray(parsed.top_strengths) ? parsed.top_strengths : [],
    jd_summary: String(parsed.jd_summary || ''),
    confidence: String(parsed.confidence || 'Medium'),
  };
}

// ── Main ──────────────────────────────────────────────────────────────────────

async function main() {
  const args = parseArgs(process.argv.slice(2));

  // Read system prompt
  // Prefer a gitignored personal override (real metrics) if present; else the shipped generic prompt
  const localScorePrompt = resolve(__dirname, 'ollama-score-prompt.local.md');
  const systemPromptPath = existsSync(localScorePrompt)
    ? localScorePrompt
    : resolve(__dirname, 'ollama-score-prompt.md');
  if (!existsSync(systemPromptPath)) {
    fatal('ollama-score-prompt.md not found', systemPromptPath);
  }
  const systemPrompt = readFileSync(systemPromptPath, 'utf8');

  // Check Ollama health before doing any work
  await checkOllamaHealth(args.ollamaUrl, args.model, 5_000);

  // Read candidate context files (cleaned — base64/blank runs waste context)
  const cv = cleanCvForPrompt(readFileSafe(resolve(PROJECT_DIR, 'cv.md')));
  const profile = readFileSafe(resolve(PROJECT_DIR, 'config/profile.md'));
  const config = readFileSafe(resolve(PROJECT_DIR, 'config/profile.yml'));

  if (!cv) fatal('cv.md not found or empty', resolve(PROJECT_DIR, 'cv.md'));

  // Fetch JD
  const jdTmpPath = `/tmp/batch-jd-${args.id}.txt`;
  let jdText;
  try {
    jdText = await fetchJd(args.url, args.jdFile, Math.min(args.timeout, 30_000));
  } catch (e) {
    // ProviderError = expired or blocked — emit status:unavailable so orchestrator skips retries
    if (e.name === 'ProviderError') {
      process.stdout.write(JSON.stringify({ status: 'unavailable', id: args.id, score: null, error: e.message }) + '\n');
      process.exit(0);
    }
    fatal('Failed to fetch JD', e.message);
  }

  // Save JD for Phase 2 reuse — both ephemeral (fast) and persistent (survives restart)
  writeFileSync(jdTmpPath, jdText, 'utf8');
  try {
    const jdPersistDir = resolve(__dirname, 'jds');
    mkdirSync(jdPersistDir, { recursive: true });
    writeFileSync(resolve(jdPersistDir, `${args.id}.txt`), jdText, 'utf8');
  } catch {}

  // Trim JD boilerplate + cap to keep total prompt within num_ctx 12288.
  // Fixed context (system prompt + cleaned CV + profile + config) is ~21k chars
  // (~5.5k tokens); a 10k-char JD (~2.5k tokens) leaves ample output room.
  const jdTruncated = cleanJd(jdText, 10_000);

  // Build user message with all context
  const userMessage = [
    '## Candidate CV',
    cv,
    '',
    '## Candidate Target Role Profile',
    profile || '(no profile found)',
    '',
    '## Candidate Configuration',
    config || '(no config found)',
    '',
    '## Job Description to Score',
    `URL: ${args.url}`,
    '',
    jdTruncated,
    '',
    'Score this job offer for the candidate. Output only the JSON object, no other text.',
  ].join('\n');

  // Call Ollama
  let rawResponse;
  try {
    rawResponse = await callOllama(args.ollamaUrl, args.model, systemPrompt, userMessage, args.timeout);
  } catch (e) {
    fatal('Ollama API call failed', e.message);
  }

  // Parse and validate
  let scored;
  try {
    const parsed = extractJson(rawResponse);
    const { cap: stackCap } = stackMismatchCap(jdText, cv);
    const langCap = languageMismatchCap(jdText, cv);
    // Same seniority caps Phase 2 applies (benchmarked 2026-07-17: every tested
    // model overscored Senior-titled roles at P1 — this gate can't live in the model).
    const sen = seniorityCaps(parsed.role, jdText);
    scored = validateScore(parsed, {
      cvCap: Math.min(stackCap, langCap.cvCap, sen.cvCap),
      nsCap: Math.min(langCap.nsCap, sen.nsCap),
    });
    if (sen.reason) {
      scored.soft_gaps = [...new Set([`Seniority cap applied: ${sen.reason}`, ...scored.soft_gaps])].slice(0, 5);
    }
    if (langCap.missing) {
      scored.hard_stops = [...new Set([`Requires ${langCap.missing} fluency — not in CV languages`, ...scored.hard_stops])];
    }
  } catch (e) {
    fatal('Failed to parse model response', e.message);
  }

  // Output the full payload to stdout — the orchestrator redirects this to
  // batch/scores/<id>.json. Do NOT also writeFileSync to the same path;
  // that would corrupt the file because bash's redirect and the Node write
  // both target the same file descriptor.
  const output = {
    status: 'scored',
    id: args.id,
    url: args.url,
    jd_file: jdTmpPath,
    model: args.model,
    scored_at: new Date().toISOString(),
    ...scored,
    error: null,
  };
  process.stdout.write(JSON.stringify(output, null, 2) + '\n');
}

main().catch(e => {
  const out = { status: 'failed', id: null, score: null, error: e.message };
  process.stdout.write(JSON.stringify(out) + '\n');
  process.exit(1);
});
