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

/**
 * cv.md minus its `## Skills` taxonomy. Same doctrine `strengthFrom` already
 * applies to evidence rows: **a catalogue line is a claim, not a demonstration.**
 * An ecosystem named only in the skills block has no bullet behind it anywhere
 * on the CV, so `stackMismatchCap` must not read it as coverage.
 */
function evidenceText(cvText) {
  return String(cvText || '').replace(/\n##\s*Skills\b[\s\S]*?(?=\n##\s|$)/i, '\n');
}

/**
 * Ecosystems the candidate clearly has, parsed from cv.md text — experience,
 * projects and education only, never the skills catalogue.
 *
 * Measured on this CV: every other claimed ecosystem is named 3-6 times outside
 * the skills block (java 3, python 6, javascript 6, rust 4, c++ 1) and c#/.net
 * exactly 0, so the whole effect of the exclusion is that five C#-only postings
 * stop reading as covered — three of which scored above the Phase 3 threshold
 * and generated a tailored CV for a stack with no project behind it.
 */
export function candidateEcosystems(cvText) {
  const have = new Set();
  const evidence = evidenceText(cvText);
  for (const [eco, re] of Object.entries(ECOSYSTEM_PATTERNS)) {
    if (countMatches(evidence, re) > 0) have.add(eco);
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

// ── Location policy ──────────────────────────────────────────────────────────
// Same shape, and the same reason, as languageMismatchCap: a commute the profile
// calls a hard no is one requirement among forty, so it barely moves the coverage
// average and the model scores the role on its merits. Measured over the stored
// evals: 8 offers scored >= 4.0 on a London hybrid/on-site posting, and Phase 2
// printed the deal-breaker verbatim in `hard_stops` on two of them while still
// returning "Apply" and generating a tailored PDF — `hard_stops` never feeds
// `final_decision` (staged-evaluator.mjs), so a detected hard stop is decoration.
//
// A named city list, not "any capitalised word": a JD names a dozen places it is
// not hiring in — the HQ, the customers, the last funding round's office — and
// every one of them would cap an otherwise remote role.
const UK_CITIES = [
  'london', 'manchester', 'leeds', 'liverpool', 'sheffield', 'nottingham',
  'leicester', 'bristol', 'cardiff', 'newcastle', 'york', 'cambridge', 'oxford',
  'reading', 'brighton', 'southampton', 'portsmouth', 'edinburgh', 'glasgow',
  'aberdeen', 'dundee', 'belfast', 'dublin', 'milton keynes', 'swindon',
  'derby', 'norwich', 'exeter', 'bath',
];

const ONSITE_SIGNAL = /\bon-?site\b|\bhybrid\b|\bin[- ]office\b|\boffice[- ]based\b|\d\s*days?\s*(?:a|per)\s*week\s*(?:in|at|from)\b/i;
const FULLY_REMOTE  = /\b(?:fully|100%|entirely|permanently)\s+remote\b|\bremote[- ]first\b|\bwork\s+from\s+anywhere\b/i;
const MONTHLY_CADENCE = /\d+\s*days?\s*(?:a|per)\s*month|\bmonthly\s+(?:travel|visit|on-?site)/i;
const WEEKLY_CADENCE  = /\d+\s*days?\s*(?:a|per)\s*week|\bweekly\s+(?:travel|visit|on-?site)/i;

/**
 * Places the candidate will physically attend an office, from `config/profile.yml`:
 * `location.city`, plus any `search_locations` entry that is NOT remote-only.
 * That is the whole policy — one commutable base, everywhere else remote — so it
 * is read from the user layer rather than hardcoded here.
 *
 * @param {string} profileText  raw config/profile.yml
 * @returns {string[]} lowercase place names, [] when the profile says nothing
 */
export function commutableTerms(profileText) {
  const text = String(profileText || '');
  const terms = [];
  const city = text.match(/^\s*city:\s*["']?([^"'\n]+)/m)?.[1];
  if (city) terms.push(city.trim().toLowerCase());
  const block = text.match(/^\s*search_locations:\s*\n([\s\S]*?)(?=\n\s*\w+:|$)/m)?.[1] || '';
  for (const line of block.split('\n')) {
    if (!/^\s*-\s/.test(line) || /remote\s+only/i.test(line)) continue;
    const place = line.replace(/^\s*-\s*["']?/, '').split(/[—–]|"|'/)[0];
    for (const part of place.split('/')) {
      const t = part.trim().toLowerCase();
      if (t) terms.push(t);
    }
  }
  return [...new Set(terms.filter(Boolean))];
}

/**
 * Hard cap when the JD requires office presence somewhere the candidate will not
 * commute to. Caps both dimensions at 2 — the same 2/2 languageMismatchCap uses,
 * which lands the composite at 2.0: under Phase 1's 2.5 gate and under Phase 2's
 * `score < 3 => Skip`, so a hard-no role stops before Phase 3 tailors a CV for it.
 *
 * Deliberately conservative, because a false positive deletes a good offer while
 * a false negative only leaves noise the human discards. The two signals must be
 * NEAR each other, which is how these ads are actually written ("Location:
 * Glasgow … Work Setup: Hybrid - 3 days in the office", "an on-site role based in
 * either our London or Frankfurt office"). Testing them independently capped
 * a vendor's remote-friendly posting at 2.0 — it says "there is no minimum
 * in-office qualification requirement" and names London in a list of global
 * offices, 1.7k characters apart.
 *
 * ponytail: a JD saying both "hybrid" and "fully remote" is left uncapped. The
 * profile calls that on-site ("remote, two days a week in the office"); telling
 * the two apart needs more than proximity, and no such offer has shown up yet.
 *
 * @param {string} jdText
 * @param {string} profileText  raw config/profile.yml
 * @returns {{cvCap: number, nsCap: number, city: string|null}}
 */
const PROXIMITY = 160;  // chars either side of the city name

export function locationMismatchCap(jdText, profileText) {
  const jd = String(jdText || '').toLowerCase();
  const commutable = commutableTerms(profileText);
  const none = { cvCap: 5, nsCap: 5, city: null };
  if (!commutable.length) return none;              // no stated policy — no gate
  if (!ONSITE_SIGNAL.test(jd)) return none;         // nothing demands attendance
  if (commutable.some(t => jd.includes(t))) return none;
  if (FULLY_REMOTE.test(jd)) return none;
  for (const city of UK_CITIES) {
    const re = new RegExp(`\\b${city}\\b`, 'g');
    let m;
    while ((m = re.exec(jd))) {
      const window = jd.slice(Math.max(0, m.index - PROXIMITY), m.index + city.length + PROXIMITY);
      if (!ONSITE_SIGNAL.test(window)) continue;
      // The policy allows a monthly cadence ("a few days a month") and refuses a
      // weekly one, so an ad offering "6 days a month travel to office" is a
      // yes and must not be capped for saying "hybrid" in the same breath.
      if (MONTHLY_CADENCE.test(window) && !WEEKLY_CADENCE.test(window)) continue;
      return { cvCap: 2, nsCap: 2, city };
    }
  }
  return none;
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
 *
 * `requirement`/`evidence` are the grounding guard. `same_tooling` is the
 * model's own opinion and it is optimistic: it grades a requirement Strong on a
 * CV line that never mentions the technology the requirement names — a
 * "5+ years of <language>" requirement matched to an education line, a
 * "production <language>" one matched to an architecture bullet in a different
 * language. Measured on an A/B over 10 offers, ~7% of Strong rows. A Strong row
 * is also what feeds Phase 3's bullet selection, so an ungrounded one propagates
 * into the tailored CV.
 *
 * `source` is the atom's CV section. A Skills or Education *catalogue* line is a
 * claim, not a demonstration, and it cannot lose: it lexically contains every
 * technology it lists, so it matches any requirement naming one and proves it.
 * Measured over 573 requirement rows from 60 reports, 184 cited a catalogue line
 * — 138 Strong, 46 Transferable, **0 Gap**. A surface that has never once failed
 * is not grading anything. Capped at Transferable: the tool is genuinely on the
 * CV, so this is not a Gap, but nothing here shows it being used.
 *
 * The prompt has carried an exemplar against exactly this since the stage was
 * written ("Kubernetes internals" vs "Skills — Kubernetes (working knowledge)"
 * → same_activity FALSE) and lost 138 times. Ledger V4's lesson: when the
 * prompt-side fix measures zero, the repair belongs in code.
 *
 * All three default to '' so three-argument callers keep the old behaviour.
 */
export function strengthFrom(pick, sameActivity, tooling, requirement = '', evidence = '', source = '') {
  if (pick === 'none' || !sameActivity) return 'Gap';
  if (tooling === 'different') return 'Transferable';
  if (source === 'skills' || source === 'education') return 'Transferable';
  // Same catalog as verifyAgainstCv but the opposite quantifier: a *claim*
  // naming two technologies asserts both, a *requirement* listing them ("AWS,
  // Azure or GCP") accepts any one. Demote rather than Gap — the activity still
  // matched, only the proof of the named technology is missing.
  if (requirement) {
    const all = { ...ECOSYSTEM_PATTERNS, ...TOOL_PATTERNS };
    const named = Object.values(all).filter(re => countMatches(requirement, re) > 0);
    if (named.length && !named.some(re => countMatches(evidence, re) > 0)) return 'Transferable';
  }
  return 'Strong';
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

  // locationMismatchCap. Fictional policy, same as the languages above — the
  // assertions need only "one commutable base" and "somewhere they will not go".
  const profileYml = 'location:\n  city: "Ashcombe"\n  search_locations:\n' +
    '    - "Ashcombe / Mereside — on-site, hybrid or remote"\n' +
    '    - "United Kingdom — fully remote only"\n    - "London — fully remote only"\n';
  assert(commutableTerms(profileYml).join(',') === 'ashcombe,mereside',
    'remote-only search locations are not commutable bases');
  const loc = jd => locationMismatchCap(jd, profileYml);
  assert(loc('Location: London (Hybrid Working). Rust as a main language.').city === 'london',
    'London hybrid capped');
  assert(loc('Location & Work Model: London, UK | Hybrid (3 days per week in the office)').cvCap === 2,
    'weekly commute caps both dimensions at 2');
  assert(loc('Location: Glasgow. Work Setup: Hybrid - 3 days in the office').city === 'glasgow',
    'any non-commutable city counts, not just London');
  assert(loc('Backend Engineer, Ashcombe. Hybrid, 3 days a week in the office.').city === null,
    'the commutable base is never capped');
  assert(loc('Fully remote (UK). The team meets in London twice a year.').city === null,
    'a fully-remote role naming London is not capped');
  assert(loc('Locations: London, Essex | Hybrid: 6 days a month travel to office').city === null,
    'a monthly cadence is inside the travel policy');
  assert(loc('Backend Engineer. Hybrid working available.').city === null,
    'an on-site signal naming no city is not capped');
  // The proximity rule. One vendor posting says it has no in-office requirement
  // and names London 1.7k characters away in a list of global offices; testing the
  // two signals independently capped it at 2.0 from a score of 5.0.
  assert(loc(`There is no minimum in-office qualification requirement. ${'x'.repeat(400)} `
    + 'Headquartered overseas with key offices in London, New York and Paris.').city === null,
    'city and on-site signal must be near each other');
  assert(loc('Location: London. Hybrid working, 2 days a week in the office.').city === 'london',
    'near-adjacent signals still cap');
  assert(locationMismatchCap('Location: London (Hybrid)', '').city === null,
    'no stated policy means no gate');

  // candidateEcosystems reads work, not the skills catalogue: a technology named
  // only in the taxonomy has no bullet behind it anywhere on the CV.
  const cataloguedOnly = '## Experience\n- Built services in Java and Rust\n\n'
    + '## Skills\n**Languages:** Java, Rust, C#\n\n## Education\n- BEng\n';
  assert(candidateEcosystems(cataloguedOnly).has('java'), 'an ecosystem with a bullet counts');
  assert(!candidateEcosystems(cataloguedOnly).has('c#/.net'), 'a skills-only claim does not');
  assert(stackMismatchCap('C# developer wanted. Strong C# and .NET Core throughout.', cataloguedOnly).cap === 3,
    'a C#-only posting is capped against a CV that only catalogues C#');

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

  // A catalogue line is a claim, not a demonstration, and cannot lose: it holds
  // every technology it lists. Over 573 rows from 60 reports, 184 cited one —
  // 138 Strong, 46 Transferable, 0 Gap.
  assert(strengthFrom('A', true, 'same', 'AWS knowledge', 'Skills — Cloud: AWS, Docker', 'skills')
         === 'Transferable', 'a skills catalogue caps at Transferable, never Strong');
  assert(strengthFrom('A', true, 'not_applicable', 'A degree', 'Education — Modules: ...', 'education')
         === 'Transferable', 'an education catalogue caps the same way');
  assert(strengthFrom('A', true, 'same', 'AWS knowledge', 'Deployed services to AWS EC2', 'experience')
         === 'Strong', 'a work bullet still reaches Strong');
  assert(strengthFrom('A', false, 'same', '', '', 'skills') === 'Gap',
         'the cap never rescues a row the model already called a Gap');

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

  // strengthFrom grounding guard. Fixtures are fictional.
  const sf = (req, ev) => strengthFrom('A', true, 'same', req, ev);
  assert(sf('5+ years of Java full stack', 'Awarded a departmental prize for the final-year project') === 'Transferable',
    'Java requirement demoted when evidence never mentions Java');
  assert(sf('Production-grade Rust experience', 'Designed the retry and compensation flow between two services') === 'Transferable',
    'Rust requirement demoted on unrelated evidence');
  assert(sf('Strong AWS and Linux expertise', 'Skills — Cloud: AWS (Lambda, S3); OS: Linux server administration') === 'Strong',
    'grounded row stays Strong');
  // Requirements naming nothing checkable must not be taxed.
  assert(sf('Excellent written and verbal communication', 'Ran the weekly design review for the team') === 'Strong',
    'non-technical requirement unaffected');
  // One named technology present is enough — a list is a disjunction.
  assert(sf('Cloud platforms such as AWS, Azure or GCP', 'Deployed the API on AWS Lambda') === 'Strong',
    'partial match on an alternatives list stays Strong');
  // The guard never rescues, never overrides the earlier two axes.
  assert(strengthFrom('none', true, 'same', 'Java', 'Java everywhere') === 'Gap', 'guard does not rescue a Gap');
  assert(strengthFrom('A', true, 'different', 'Java', 'Java everywhere') === 'Transferable', 'different tooling still Transferable');
  // Three-argument callers keep the old behaviour.
  assert(strengthFrom('A', true, 'same') === 'Strong', 'legacy 3-arg call unchanged');

  console.log('✓ fit-rules self-check passed');
}
