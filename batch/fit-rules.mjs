// @ts-check
// Shared deterministic fit rules for the local pipeline (Phase 1 scorer + Phase 2
// evaluator). These enforce judgments the local 7B model is unreliable at — it
// inflates cv_match when the domain/keywords look familiar even though the role's
// core LANGUAGE/PLATFORM is one the candidate doesn't have.
//
// The candidate's ecosystems are parsed from cv.md, so this stays in sync with the
// CV automatically — add a language to cv.md and the penalty stops firing for it.

// Ecosystem → detection regex. Kept deliberately narrow to avoid false positives
// (e.g. "go-live" must NOT count as the Go language).
const ECOSYSTEM_PATTERNS = {
  // `\b` cannot fence a token that starts or ends with punctuation: `\bc#\b`
  // needs a word char right after the `#`, and `\b\.net` needs one right before
  // the dot — so the old pattern matched NEITHER "C#" nor ".NET" in any real
  // spelling, only "dotnet"/"asp.net". The ecosystem was therefore invisible on
  // BOTH sides of every comparison: JDs demanding it, and a CV claiming it.
  // Lookaround instead, and `c#(?:\.net)?` first so "C#.Net" counts once, not
  // twice, against minMentions. The leading lookbehind is what keeps a ".net"
  // TLD ("example.net") from reading as the ecosystem.
  'c#/.net':    /(?<![a-z0-9])(?:c#(?:\.net)?|asp\.net|jscript\.net|dotnet|\.net)(?![a-z0-9])/gi,
  'java':       /\bjava\b(?!script)/gi,
  'go':         /\b(golang|go\s+(?:developer|engineer|programmer|programming)|written\s+in\s+go|microservices\s+in\s+go)\b/gi,
  'ruby':       /\b(ruby on rails|\bruby\b|\brails\b)\b/gi,
  'php':        /\b(php|laravel|symfony)\b/gi,
  'python':     /\b(python|django|flask|fastapi)\b/gi,
  'javascript': /\b(javascript|typescript|node\.?js|react|angular|vue|next\.?js)\b/gi,
  'rust':       /\brust\b/gi,
  'scala':      /\bscala\b/gi,
  'elixir':     /\belixir\b/gi,
  'kotlin':     /\bkotlin\b/gi,
  'cpp':        /c\+\+/gi,
};

// Named technologies that are NOT programming languages, so ECOSYSTEM_PATTERNS
// above does not cover them. Used only to check claims about the candidate
// against cv.md — keep it to things a CV would name explicitly.
const TOOL_PATTERNS = {
  'azure':      /\bazure\b/gi,
  'gcp':        /\b(gcp|google cloud)\b/gi,
  'aws':        /\baws\b/gi,
  'kubernetes': /\b(kubernetes|k8s)\b/gi,
  'terraform':  /\bterraform\b/gi,
  'flask':      /\bflask\b/gi,
  'pytorch':    /\bpytorch\b/gi,
  'tensorflow': /\btensorflow\b/gi,
  'spark':      /\b(apache\s+)?spark\b/gi,
  'databricks': /\bdatabricks\b/gi,
  'kafka':      /\bkafka\b/gi,
  'elasticsearch': /\belasticsearch\b/gi,
  'snowflake':  /\bsnowflake\b/gi,
  'airflow':    /\bairflow\b/gi,
};

function countMatches(text, re) {
  const m = String(text || '').match(re);
  return m ? m.length : 0;
}

/**
 * Drop claims about the candidate that name a technology `cv.md` never mentions.
 *
 * `top_strengths` is the one field where the pipeline asserts something on the
 * candidate's *behalf*, and it fabricates: measured over 115 offers, 3.7% of the
 * strengths that named a technology named one the CV does not contain — usually
 * a neighbour of something it does (a sibling cloud provider, another framework
 * in the same family). Unlike a wrong gap, a wrong strength can reach an
 * application, so this is a hard filter rather than a prompt instruction.
 *
 * Only technologies in ECOSYSTEM_PATTERNS/TOOL_PATTERNS are checked — a claim
 * naming nothing checkable is left alone.
 *
 * @param {string[]} claims
 * @param {string} cvText
 * @returns {{kept: string[], dropped: string[]}}
 */
export function verifyAgainstCv(claims, cvText) {
  const all = { ...ECOSYSTEM_PATTERNS, ...TOOL_PATTERNS };
  const kept = [], dropped = [];
  for (const claim of Array.isArray(claims) ? claims : []) {
    const named = Object.entries(all)
      .filter(([, re]) => countMatches(claim, re) > 0)
      .map(([name]) => name);
    const absent = named.filter(n => countMatches(cvText, all[n]) === 0);
    (absent.length ? dropped : kept).push(claim);
  }
  return { kept, dropped };
}

/** Ecosystems the candidate clearly has, parsed from cv.md text. */
export function candidateEcosystems(cvText) {
  const have = new Set();
  for (const [eco, re] of Object.entries(ECOSYSTEM_PATTERNS)) {
    if (countMatches(cvText, re) > 0) have.add(eco);
  }
  return have;
}

/**
 * Stack-mismatch cap on cv_match. If the JD's required ecosystem(s) — those
 * mentioned ≥2 times — have NO overlap with the candidate's ecosystems, cap
 * cv_match (a strong engineer can ramp on a new language, but it's a real gap).
 *
 * @param {string} jdText
 * @param {string} cvText
 * @param {{cap?: number, minMentions?: number}} [opts]
 * @returns {{cap: number, jdStack: string[], missing: string[]}}
 *   cap=5 means no penalty; cap=3 means cv_match is capped at 3.
 */
/**
 * Seniority caps for an early-career candidate. Mirrors the logic proven in the
 * Phase 2 evaluator (title regex + explicit year demands), extended with the
 * staged evaluator's structured stage-1 fields when available.
 *
 * @param {string} roleTitle
 * @param {string} jdText
 * @param {{seniority_level?: string, years_required?: number}} [stage1]
 * @returns {{cvCap: number, nsCap: number, reason: string|null}}
 */
export function seniorityCaps(roleTitle, jdText, stage1 = {}) {
  const title = String(roleTitle || '').toLowerCase();
  const jd = String(jdText || '').toLowerCase();
  const years = Number(stage1.years_required) || 0;
  const level = String(stage1.seniority_level || '').toLowerCase();

  const highYears = years >= 8 || /\b(8|9|10|11|12|15)\+?\s*years/.test(jd);
  const midYears = (years >= 5 && years < 8) || /\b(5|6|7)\+?\s*years/.test(jd);
  const fwdDeployed = /\bforward[\s-]?deployed\b|\bfde\b/.test(title);
  const architect = /\barchitect\b/.test(title);
  // People-management titles are treated like Staff+ for an early-career IC
  // candidate: an "(Engineering) Manager" role is a track mismatch, not a
  // stretch (user-validated: rated such a role 1/5 vs the pipeline's 3.4).
  const staffish = /\b(staff|principal|distinguished|director|vp|vice president|head of|manager)\b/.test(title)
    || /staff|principal/.test(level);
  const seniorish = /\b(senior|sr\.?|lead)\b/.test(title) || /senior/.test(level);

  if (staffish || architect || highYears) {
    return { cvCap: 2, nsCap: 3, reason: 'Staff/Principal/8+ yrs demand' };
  }
  if (seniorish || fwdDeployed || midYears) {
    return { cvCap: 3, nsCap: 4, reason: 'Senior/5+ yrs demand' };
  }
  return { cvCap: 5, nsCap: 5, reason: null };
}

// Natural languages a JD can demand. English is never a mismatch (CV is in English).
const HUMAN_LANGUAGES = ['german', 'french', 'spanish', 'italian', 'dutch', 'portuguese',
  'japanese', 'korean', 'mandarin', 'chinese', 'arabic', 'polish', 'swedish', 'norwegian',
  'danish', 'finnish', 'turkish', 'czech', 'ukrainian', 'russian'];

/** Languages the candidate lists under the CV's "Languages" section (lowercase). */
export function candidateLanguages(cvText) {
  const have = new Set(['english']);
  const m = String(cvText || '').match(/\*\*Languages\*\*\s*\n([\s\S]*?)(?=\n\s*\*\*|$)/i);
  for (const line of (m?.[1] || '').split('\n')) {
    const lang = line.match(/^\s*-\s*([A-Za-z]+)/)?.[1]?.toLowerCase();
    if (lang) have.add(lang);
  }
  return have;
}

/**
 * Hard cap when the JD REQUIRES fluency in a natural language the candidate
 * doesn't have (e.g. "(German speaking)", "professional fluency in German").
 * User-validated: such roles are a 1-2, not a 4.5 — one missed requirement
 * barely moves the coverage average, so this must be a code-level gate.
 * "Nice to have / a plus" language mentions do NOT trigger the cap.
 *
 * @returns {{cvCap: number, nsCap: number, missing: string|null}}
 */
export function languageMismatchCap(jdText, cvText) {
  const jd = String(jdText || '').toLowerCase();
  const have = candidateLanguages(cvText);
  for (const lang of HUMAN_LANGUAGES) {
    if (have.has(lang)) continue;
    const re = new RegExp(
      `\\b${lang}[\\s-]*(speaking|fluen\\w*|proficien\\w*|native)|` +
      `\\b(fluen\\w*|proficien\\w*|native)[^.\\n]{0,30}\\b${lang}\\b`, 'i');
    const m = jd.match(re);
    if (!m) continue;
    // Skip soft mentions: "German is a plus/bonus/nice to have/preferred"
    const ctx = jd.slice(Math.max(0, m.index - 60), m.index + m[0].length + 60);
    if (/\bplus\b|\bbonus\b|nice[\s-]to[\s-]have|preferred|advantage|desirable/.test(ctx)) continue;
    return { cvCap: 2, nsCap: 2, missing: lang };
  }
  return { cvCap: 5, nsCap: 5, missing: null };
}

export function stackMismatchCap(jdText, cvText, { cap = 3, minMentions = 2 } = {}) {
  const have = candidateEcosystems(cvText);
  const present = [];
  for (const [eco, re] of Object.entries(ECOSYSTEM_PATTERNS)) {
    if (countMatches(jdText, re) >= minMentions) present.push(eco);
  }
  if (present.length === 0) return { cap: 5, jdStack: [], missing: [] }; // language-agnostic JD
  const overlap = present.filter(e => have.has(e));
  if (overlap.length > 0) return { cap: 5, jdStack: present, missing: [] }; // candidate covers ≥1
  return { cap, jdStack: present, missing: present }; // whole required stack is foreign
}

// Aggregator/hiring-thread giveaways. The stage-1 model has an
// `is_single_posting` flag guarding a hard score cap, but it is not reliable:
// on batch/jds/38.txt ("40+ top trading firms seeking exceptional engineers.
// Multiple immediate openings.") snipe-eval answered `true` 6 times out of 6 at
// temperature 0.1, so the cap never fired and a multi-firm advert scored 4.8
// with "Apply". Text this explicit is a regex's job, not a judgment call.
/**
 * Stage-2 evidence strength, derived from the model's two axes rather than asked
 * for directly. The old single Strong/Partial/Gap enum made the model fold two
 * independent calls into one severity label, and `Partial` conflated "same work,
 * different tool" (real partial credit) with "vaguely adjacent" (worth nothing)
 * — which is how a Databricks role scored 5/5 cv_match against a CV with no
 * Databricks on it, then grew fabricated Spark stories to match.
 *
 * Different activity is a Gap however well the tooling lines up: listing
 * Kubernetes as a tool does not cover a requirement for Kubernetes internals.
 *
 * `tooling` is three-way because most requirements name no technology to
 * compare. As a boolean it collapsed "no tooling involved" into "wrong tooling"
 * and quietly taxed every degree, communication and mentoring requirement 40%.
 * Only an explicit "different" costs credit.
 */
export function strengthFrom(pick, sameActivity, tooling) {
  if (pick === 'none' || !sameActivity) return 'Gap';
  return tooling === 'different' ? 'Transferable' : 'Strong';
}

// Every pattern must name the OPENINGS, never the employer's scale. A count of
// firms/companies/clients reads as an aggregator ("40+ top trading firms
// seeking engineers") but far more often it is a company bragging about its
// customers — "100,000+ companies" and "1000 clients" in real postings both
// tripped an earlier `\d{2,}\s+(firms|companies|clients)` rule. A false positive caps
// a real offer at 2/2 and brands it Suspicious, so the bar is deliberately high.
const MULTI_POSTING_PATTERNS = [
  /\bmultiple\s+(immediate\s+)?(openings|positions|roles|vacancies)\b/i,
  /\bwho['’]?s\s+hiring\b/i,
  /\bhiring\s+thread\b/i,
  /\bseveral\s+(openings|positions|roles|vacancies)\b/i,
  /\bvarious\s+(openings|positions|roles|vacancies)\b/i,
];

/**
 * True when the JD text itself advertises more than one job. OR this with the
 * model's `is_single_posting === false` so either signal trips the cap.
 */
export function looksMultiPosting(jdText) {
  const jd = String(jdText || '');
  return MULTI_POSTING_PATTERNS.some(re => re.test(jd));
}

// ── Self-check (ponytail: one runnable check) ─────────────────────────────────
import { fileURLToPath as _f } from 'url';
if (process.argv[1] && _f(import.meta.url) === process.argv[1]) {
  const assert = (c, m) => { if (!c) { console.error('FAIL:', m); process.exit(1); } };
  // Fictional profile — the assertions only need 'a language the candidate has'
  // and 'one they do not', so real CV languages are never required here.
  const cv = '**Languages**\n- English: Advanced (B2 certified)\n- Spanish: Native\n- Portuguese: Native\n';

  let r = languageMismatchCap('Software Engineer, Agent (German speaking). Professional fluency in both German and English.', cv);
  assert(r.missing === 'german' && r.cvCap === 2, 'german-speaking role capped');
  r = languageMismatchCap('Fluency in Spanish required for client calls', cv);
  assert(r.missing === null, 'spanish ok — candidate has it');
  r = languageMismatchCap('German is a plus but not required. Fluent German would be a bonus.', cv);
  assert(r.missing === null, 'nice-to-have german not capped');
  r = languageMismatchCap('We serve the German market from our Berlin office.', cv);
  assert(r.missing === null, 'market mention not capped');

  let s = seniorityCaps('Senior Security Engineering Manager', '', {});
  assert(s.cvCap === 2 && s.nsCap === 3, 'manager title capped as staff-tier');
  s = seniorityCaps('Software Engineer I', 'some jd text', {});
  assert(s.cvCap === 5, 'junior role uncapped');

  // strengthFrom: policy lives in code, the model only answers the two axes.
  assert(strengthFrom('A', true,  'same')           === 'Strong',       'same work + same tools = Strong');
  assert(strengthFrom('A', true,  'different')      === 'Transferable', 'same work, another tool = Transferable');
  assert(strengthFrom('A', true,  'not_applicable') === 'Strong',       'no technology named = full match, not a partial one');
  assert(strengthFrom('A', false, 'different')      === 'Gap',          'different work = Gap');
  assert(strengthFrom('A', false, 'same')           === 'Gap',          'tooling match cannot rescue a different activity');
  assert(strengthFrom('none', true, 'same')         === 'Gap',          'no evidence picked outranks both axes');

  // verifyAgainstCv: a strength may only name technology the CV actually has.
  // Fictional skills line, like the profile above — the assertions only need
  // 'a technology the CV lists' and 'one it does not'.
  const cvTech = 'Skills: Ruby, Rails, Elasticsearch, GCP, Docker';
  let v = verifyAgainstCv([
    'Ruby on Rails services backed by Elasticsearch',   // both present  -> keep
    'Strong communication with non-technical stakeholders', // names nothing -> keep
    'Built and tuned Spark batch jobs',                 // Spark absent  -> drop
    'Deployed containers to Azure App Service',         // Azure absent  -> drop
  ], cvTech);
  assert(v.kept.length === 2, 'two verifiable strengths kept');
  assert(v.dropped.length === 2, 'two fabricated strengths dropped');
  assert(v.kept.some(s => /non-technical/.test(s)), 'claim naming no technology is left alone');
  assert(verifyAgainstCv([], cvTech).kept.length === 0, 'empty input is safe');

  // c#/.net: `\b` cannot fence `c#` or `.net`, so the old pattern matched neither
  // in any spelling a JD actually uses. stackMismatchCap is the consumer.
  const noDotnetCv = 'Skills: Ruby, Rails, Elasticsearch, GCP, Docker';
  const capOf = jd => stackMismatchCap(jd, noDotnetCv);
  assert(capOf('C#/OO software engineer. Strong C# required.').missing.includes('c#/.net'), 'bare C# is detected');
  assert(capOf('We build in .NET 8 and ship .NET services daily.').missing.includes('c#/.net'), 'bare .NET is detected');
  assert(capOf('ASP.NET Core and dotnet tooling throughout.').missing.includes('c#/.net'), 'asp.net/dotnet still detected');
  // Counted once, not twice — otherwise a single "C#.Net" trips minMentions 2 alone.
  assert(capOf('C#.Net is used here.').missing.length === 0, 'one C#.Net mention is below minMentions');
  // A .net TLD is not an ecosystem.
  assert(capOf('Apply at example.net or careers.foo.net today.').missing.length === 0, '.net TLD is not the ecosystem');
  assert(capOf('We write C++ and Rust.').missing.includes('c#/.net') === false, 'C++ is not C#');

  // looksMultiPosting: the model called offer #38 a single posting 6/6 times.
  assert(looksMultiPosting('40+ top trading firms seeking exceptional engineers. Multiple immediate openings.'), 'aggregator advert detected');
  assert(looksMultiPosting("Ask HN: Who's hiring? (July 2026)"), 'hiring thread detected');
  assert(looksMultiPosting('We have several openings across the platform team'), 'several openings detected');
  // Must not fire on an ordinary single posting. The last two are real JDs that
  // an earlier employer-scale pattern wrongly flagged (batch/jds/18, /48).
  assert(!looksMultiPosting('Senior Rust Engineer. You will join a team of 12 engineers building trading systems.'), 'single posting not flagged');
  assert(!looksMultiPosting('Software Engineer — 5+ years experience with distributed systems required.'), 'years-of-experience not flagged');
  assert(!looksMultiPosting('Acme is Europe\'s leading freelance marketplace, connecting over 1,000,000 talented freelancers with 100,000+ companies.'), 'marketplace scale not flagged');
  assert(!looksMultiPosting('You will own the AWS business within a portfolio of over 1000 clients.'), 'client count not flagged');
  assert(!looksMultiPosting(''), 'empty JD not flagged');

  console.log('✓ fit-rules self-check passed');
}
