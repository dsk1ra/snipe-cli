// @ts-check
// Shared text/context helpers for the local pipeline (Phase 1 scorer, Phase 2
// evaluator, Phase 3 local tailor). Single source of truth for CV/JD cleaning
// and deterministic salary parsing — the model never guesses comp anymore.

// Strip embedded base64 images, trailing whitespace, and blank runs — they burn
// context tokens for zero evaluation value.
export function cleanCvForPrompt(md) {
  return (md || '')
    .replace(/!\[\]\(data:image\/[^)]*\)/g, '')
    .replace(/[ \t]+$/gm, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

// Trim JD boilerplate (benefits, EEO, company blurb) and hard-cap length so the
// requirements/responsibilities survive within the context budget.
export function cleanJd(jd, cap = 4000) {
  if (!jd) return '(no JD available)';
  let t = jd.replace(/\r/g, '');
  // JD caches from Apify pre-fetch may be raw HTML — strip it (idempotent on text).
  if (/<\/(?:div|p|li|span|ul|h\d)>/i.test(t)) {
    t = t
      .replace(/<script[\s\S]*?<\/script>/gi, '')
      .replace(/<style[\s\S]*?<\/style>/gi, '')
      .replace(/<\/(?:p|div|li|ul|ol|h\d|br|tr)>/gi, '\n')
      .replace(/<[^>]+>/g, ' ')
      .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&nbsp;/g, ' ')
      .replace(/[ \t]{2,}/g, ' ');
  }
  t = t.replace(/\n{3,}/g, '\n\n').trim();
  // Cut a trailing boilerplate section if a clear marker appears past the midpoint.
  const markers = /\n[^\n]{0,60}\b(equal opportunity|equal employment|we are an equal|EEO|diversity, equity|how to apply|benefits & perks|perks & benefits|our benefits|what we offer)\b/i;
  const m = t.match(markers);
  if (m && m.index > t.length * 0.45) t = t.slice(0, m.index).trim();
  if (t.length > cap) t = t.slice(0, cap).trim() + '\n[...]';
  return t;
}

// ── Salary parsing ────────────────────────────────────────────────────────────

function parseMoney(s) {
  const t = String(s).toLowerCase().replace(/,/g, '');
  const k = t.endsWith('k');
  const n = parseFloat(k ? t.slice(0, -1) : t);
  return Number.isFinite(n) ? Math.round(k ? n * 1000 : n) : null;
}

const PLAUSIBLE_MIN = 18_000;
const PLAUSIBLE_MAX = 500_000;
const NUM = String.raw`\d{1,3}(?:,\d{3})+|\d+(?:\.\d+)?[kK]|\d{5,6}`;
const RANGE_RE = new RegExp(
  // classic "£40,000 - £55,000" / "£40k to £55k"
  String.raw`([£$€])\s?(${NUM})\s?(?:-|–|—|to)\s?(?:[£$€]\s?)?(${NUM})|` +
  // adjacent double-currency "£325,000 £485,000" — happens when an HTML pay-range
  // widget (<span>lo</span><span>hi</span>) is flattened to text
  String.raw`([£$€])\s?(${NUM})\s{1,4}[£$€]\s?(${NUM})|` +
  String.raw`\b(GBP|USD|EUR)\s?(${NUM})\s?(?:-|–|—|to)\s?(${NUM})`, 'g');
const SINGLE_RE = new RegExp(
  String.raw`([£$€])\s?(${NUM})|\b(GBP|USD|EUR)\s?(${NUM})|\b(${NUM})\s?(GBP|USD|EUR)\b`, 'g');
const RATE_RE = /^\s*(?:per|a|an|\/)\s*(?:day|hour|hr|week|month)|^\s*(?:daily|hourly|weekly|monthly|p\/?[dh])/i;

const CUR_WORD = { GBP: '£', USD: '$', EUR: '€' };

function isAnnualContext(text, matchEnd) {
  // Reject day/hour/week/month rates — we only score annual salary.
  return !RATE_RE.test(text.slice(matchEnd, matchEnd + 12));
}

/**
 * Deterministically extract an annual salary (range or single figure) from JD
 * text. Returns {currency, min, max, raw} or null. Ranges win over singles.
 */
export function extractSalary(text) {
  // JD caches may hold raw HTML (Apify pre-fetch) — strip tags so ranges split
  // across <span>s are seen as adjacent numbers.
  const t = String(text || '').replace(/<[^>]+>/g, ' ').replace(/[ \t]{2,}/g, ' ');

  for (const m of t.matchAll(RANGE_RE)) {
    const currency = m[1] || m[4] || CUR_WORD[m[7]] || '£';
    const lo = parseMoney(m[2] ?? m[5] ?? m[8]);
    const hi = parseMoney(m[3] ?? m[6] ?? m[9]);
    if (lo === null || hi === null || lo > hi) continue;
    if (lo < PLAUSIBLE_MIN || hi > PLAUSIBLE_MAX) continue;
    if (!isAnnualContext(t, m.index + m[0].length)) continue;
    return { currency, min: lo, max: hi, raw: m[0].trim() };
  }

  for (const m of t.matchAll(SINGLE_RE)) {
    const currency = m[1] || CUR_WORD[m[3]] || CUR_WORD[m[6]] || '£';
    const n = parseMoney(m[2] ?? m[4] ?? m[5]);
    if (n === null || n < PLAUSIBLE_MIN || n > PLAUSIBLE_MAX) continue;
    if (!isAnnualContext(t, m.index + m[0].length)) continue;
    return { currency, min: n, max: n, raw: m[0].trim() };
  }

  return null;
}

// ── Comp scoring against the candidate's targets ─────────────────────────────

/**
 * Parse the compensation targets out of config/profile.yml text.
 * Returns {floor, targetLow, targetHigh} or null if unparseable.
 */
export function parseCompTargets(configText) {
  const t = String(configText || '');
  const rangeStr = t.match(/target_range:\s*["']?([^"'\n]+)/)?.[1] || '';
  const floorStr = t.match(/minimum:\s*["']?([^"'\n]+)/)?.[1] || '';
  const nums = [...rangeStr.matchAll(new RegExp(NUM, 'g'))].map(m => parseMoney(m[0]));
  const floor = parseMoney(floorStr.match(new RegExp(NUM))?.[0] ?? '');
  if (nums.length < 2 || nums.some(n => n === null)) return null;
  return { floor: floor ?? nums[0], targetLow: nums[0], targetHigh: nums[1] };
}

/**
 * Comp dimension (1-5) from a posted salary vs the candidate's targets, computed
 * in code — replaces the model-guessed comp_inferred, which was hallucination-prone.
 */
export function compScoreFromSalary(salary, targets) {
  if (!salary) return null;
  if (!targets) return 3; // salary posted but no targets configured — neutral
  const mid = (salary.min + salary.max) / 2;
  if (mid < targets.floor) return 1;
  if (mid < targets.targetLow) return 2;
  if (mid < (targets.targetLow + targets.targetHigh) / 2) return 3;
  if (mid < targets.targetHigh) return 4;
  return 5;
}

// ── Report Block D builder (shared: evaluator + staged evaluator) ─────────────

/** Markdown for the code-owned "## D) Comp & Demand" section. */
export function buildCompBlock(salary, compDim, targets) {
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

// ── Self-check (ponytail: one runnable check) ─────────────────────────────────

import { fileURLToPath } from 'url';
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const assert = (cond, msg) => { if (!cond) { console.error('FAIL:', msg); process.exit(1); } };

  let s = extractSalary('Salary: £45,000 - £60,000 per annum plus benefits');
  assert(s && s.min === 45000 && s.max === 60000 && s.currency === '£', 'GBP range');

  s = extractSalary('We offer £48k–£55k depending on experience');
  assert(s && s.min === 48000 && s.max === 55000, 'k-suffix range');

  s = extractSalary('Compensation: $120,000 to $150,000');
  assert(s && s.min === 120000 && s.currency === '$', 'USD range');

  s = extractSalary('GBP 40000 - 52000, remote-first');
  assert(s && s.min === 40000 && s.max === 52000 && s.currency === '£', 'GBP word range');

  s = extractSalary('<div class="pay-range"><span>£325,000</span><span class="divider"> </span><span>£485,000 GBP</span></div>');
  assert(s && s.min === 325000 && s.max === 485000, 'HTML span range');

  s = extractSalary('Salary of £52,000 for the right candidate');
  assert(s && s.min === 52000 && s.max === 52000, 'single figure');

  assert(extractSalary('£500 per day contract role') === null, 'day rate rejected');
  assert(extractSalary('£450/day outside IR35') === null, 'slash day rate rejected');
  assert(extractSalary('a £250 annual learning budget and £50 vouchers') === null, 'small figures rejected');
  assert(extractSalary('Join our team of 45,000 employees') === null, 'bare number without currency rejected');
  assert(extractSalary('no salary mentioned here') === null, 'null when absent');

  const targets = parseCompTargets('compensation:\n  target_range: "£35,000–£55,000"\n  minimum: "£30,000"\n');
  assert(targets && targets.floor === 30000 && targets.targetLow === 35000 && targets.targetHigh === 55000, 'targets parsed');

  assert(compScoreFromSalary({ min: 25000, max: 25000 }, targets) === 1, 'below floor = 1');
  assert(compScoreFromSalary({ min: 31000, max: 33000 }, targets) === 2, 'below target = 2');
  assert(compScoreFromSalary({ min: 38000, max: 44000 }, targets) === 3, 'lower half = 3');
  assert(compScoreFromSalary({ min: 45000, max: 55000 }, targets) === 4, 'upper half = 4');
  assert(compScoreFromSalary({ min: 60000, max: 70000 }, targets) === 5, 'above target = 5');
  assert(compScoreFromSalary(null, targets) === null, 'no salary = null');

  assert(cleanCvForPrompt('a  \n   \n\n\nb') === 'a\n\nb', 'cv cleaning');

  console.log('✓ text-utils self-check passed');
}
