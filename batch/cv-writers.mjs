// @ts-check
/**
 * cv-writers.mjs — the alternatives to "let a 7B rewrite every bullet".
 *
 * Phase 3 has always had exactly one writer: hand `snipe-cv` the selected CV and
 * ask for tailored JSON. Everything downstream of that call — eighteen guard
 * functions in local-pdf-offer.mjs — exists to undo what it breaks. Two of those
 * guards are the evidence that motivated this file: `verifyBulletFigures` reverts
 * a rewrite to its CV source whenever a figure was dropped, and doing so *raised*
 * ats_coverage (+0.025, 16 wins 1 loss) and grounding (+0.105). The source bullet
 * carried more of the posting's own vocabulary than the rewrite of it did.
 *
 * If reverting helps when a figure is lost, the obvious question is whether it
 * helps when one isn't. `verbatim` answers that: the same selection, rendered
 * with no generation call at all.
 *
 * Self-check: node batch/cv-writers.mjs
 */

import { parseCvSections, parseEntries, entryCompany, padProjectDescriptions } from './cv-select.mjs';

const STOPWORDS = new Set(['and','the','of','for','to','in','with','on','our','you','your','we','is','are','as','at','an','or','by','be','this','that','will','have','has','from','using','use']);

/**
 * The `.` in the character class is there for `Next.js` and `node.js`, but it also
 * welds a sentence-final period onto the last word — a JD ending "…we need JWT."
 * yields `jwt.`, which matches the CV's `jwt` nowhere. Trim the edges after the
 * match so the dot only survives where it is medial.
 * @param {string} s
 */
function tokenize(s) {
  return (String(s || '').toLowerCase().match(/[a-z0-9+#.]{3,}/g) || [])
    .map(w => w.replace(/^\.+|\.+$/g, ''))
    .filter(w => w.length >= 3 && !STOPWORDS.has(w));
}

/**
 * A skill item's parenthesis holds one of two very different things, and the
 * old rule — drop every parenthesis before splitting — treated them alike:
 *
 *   Message Queues (RabbitMQ, Kafka)   → the keywords ARE the parenthesis
 *   Kubernetes (working knowledge, …)  → a caveat about depth
 *
 * Dropping both shipped "Message Queues" to a posting that asked for Kafka, and
 * the same for AES-256-GCM, EC2/Lambda/S3/IAM, Jest, Jenkins, Ollama and MCP —
 * the CV's most searchable terms, deleted at parse time from every tailored PDF.
 *
 * Each comma-separated part is judged on its own, so a mixed parenthesis keeps
 * what is a name and drops what is prose: "(AES-256-GCM, HMAC-SHA256, end-to-end
 * encryption)" promotes the first two and leaves the third. A name starts with a
 * capital or a digit, or carries a digit; prose ("working knowledge", "server
 * administration", "daily driver") does neither.
 *
 * @param {string} raw one comma-separated skill item, parentheses intact
 * @returns {string[]} the item without its parentheses, plus any promoted names
 */
function expandSkillItem(raw) {
  const promoted = [];
  const base = raw.replace(/\(([^)]*)\)/g, (_, inner) => {
    for (const part of inner.split(',').map(s => s.trim()).filter(Boolean)) {
      if (/^[A-Z0-9]/.test(part) || /\d/.test(part)) promoted.push(part);
    }
    return ' ';
  }).replace(/\s{2,}/g, ' ').trim();
  return [base, ...promoted].filter(Boolean);
}

/**
 * The CV's `**Category:** a, b, c` skill lines, as `{category, items[]}`.
 *
 * Split on top-level commas only: several items carry a comma inside their
 * parenthesis ("Message Queues (RabbitMQ, Kafka)") and splitting naively ships
 * "Message Queues (RabbitMQ" as a skill.
 * @param {string} cvText
 */
export function parseSkillCategories(cvText) {
  const sec = (cvText || '').split(/^##\s+/m).find(s => /^Skills/i.test(s)) || '';
  return [...sec.matchAll(/^\*\*([^*]+):\*\*\s*(.+)$/gm)].map(m => ({
    category: m[1].trim(),
    items: m[2].split(/,(?![^(]*\))/).map(s => s.trim()).filter(Boolean)
      .flatMap(expandSkillItem),
  }));
}

/**
 * A phrase, space-wrapped for whole-phrase containment tests.
 *
 * `.` survives the character class for `Next.js` and `node.js`, and that welds a
 * sentence-final period onto the last word: a JD reading "…experience with
 * Kafka." yields `kafka.`, which contains `kafka` nowhere as a phrase. Postings
 * end sentences on technology names constantly, so this silently dropped real
 * matches. `tokenize` above already documents and fixes the same trap; exported
 * rather than copied so the harness scores what the selector selected.
 * @param {string} s
 */
export const normPhrase = (s) => ` ${String(s || '').toLowerCase()
  .replace(/[^a-z0-9+#.]+/g, ' ')
  .replace(/\.(?=\s|$)/g, '')
  .trim()} `;
const norm = normPhrase;

/**
 * The forms a posting may legitimately use to name one taxonomy item.
 *
 * `cv.md` writes alternatives with a **spaced** slash and compound names with a
 * **tight** one, consistently across the whole taxonomy: "TypeScript /
 * JavaScript", "Agile / Scrum" and "MongoDB / Atlas" are two names for one
 * thing, while "CI/CD", "C/C++" and "STUN/TURN" are single names that happen to
 * contain a slash. Reading that is not guessing at the user's intent — it is the
 * notation the file already uses.
 *
 * `selectSkills` never needed this because `hits()` scores token overlap, so a
 * posting naming TypeScript already ranks "TypeScript / JavaScript" first and
 * ships it. `skillCoverage` matches whole phrases, so it asked whether the
 * posting had written the CV's exact string — and 31 postings naming TypeScript
 * were not counted as misses but were not counted at all. That is how
 * `skill_coverage` reported 1.000 over 3.5 scored skills a posting.
 *
 * Exported for the same reason `normPhrase` is: the harness must score the thing
 * the selector selected, and the two drifting apart is the failure this repo has
 * now hit three times.
 *
 * @param {string} item one taxonomy item as `cv.md` writes it
 * @returns {string[]} the item, or its alternatives if it lists any
 */
export function skillForms(item) {
  const s = String(item || '').trim();
  return /\s\/\s/.test(s) ? s.split(/\s+\/\s+/).map(x => x.trim()).filter(Boolean) : [s];
}

/**
 * Which skill items the CV itself ties to the ones the posting named.
 *
 * A posting asking for Java is also asking, in every way that matters, about
 * Spring Boot — but "Spring Boot" shares no token with it, so a pure JD-overlap
 * filter drops it. The relatedness signal is already written down: cv.md says
 * "(Java / Spring Boot / Kafka)" on one line, because that is what was actually
 * built together. Two items on the same line are related **for this candidate**,
 * which is a stronger claim than two items being related in general, and it costs
 * no model call to read.
 *
 * The Skills section is excluded from the evidence, and that is the whole design
 * rather than a detail: its lines list a category's items together by definition,
 * so counting them would make every item related to every sibling and promote
 * whole categories on one match. Relatedness has to be earned in the prose.
 *
 * @param {string} cvText
 * @param {string[]} allItems every skill item across every category
 * @param {(s: string) => number} hits JD overlap score
 * @returns {Set<string>} items co-written with something the posting asked for
 */
function relatedToJd(cvText, allItems, hits) {
  const present = allItems.filter(i => hits(i) > 0).map(norm);
  if (!present.length) return new Set();
  const evidence = String(cvText || '').split(/^##\s+/m).filter(s => !/^Skills/i.test(s)).join('\n');
  const out = new Set();
  for (const line of evidence.split('\n')) {
    const n = norm(line);
    if (!present.some(p => n.includes(p))) continue;
    for (const item of allItems) if (hits(item) === 0 && n.includes(norm(item))) out.add(item);
  }
  return out;
}

/**
 * Skill categories ranked by JD overlap, items within each ranked the same way.
 *
 * Deterministic and grounded by construction — every item is lifted verbatim from
 * cv.md — which is the whole point: `filterSkillItems` exists downstream purely to
 * throw away the ones the model invented, and it has nothing to do here.
 *
 * Items are kept in three tiers, and everything below them is **dropped, not
 * merely ranked last**. This function already computed the score that says so; it
 * just used it to sort. On the 32-offer bench the block shipped 52 items of which
 * 12.9 shared a term with the posting, so 75% of it bought nothing an ATS could
 * read and cost 4.4 rendered lines — on a page whose entire evidence budget is 21.
 *
 *   1. the posting's own terms      — `hits > 0`
 *   2. what cv.md ties to them      — `relatedToJd`, so Java brings Spring Boot
 *   3. a floor in cv.md's order     — `minItems`, so nothing reads as pandering
 *
 * Tier 3 is the guard against optimising the metric into a lie. A block holding
 * only what the posting asked for drops the distinctive evidence — Rust vanishing
 * from a Java posting — which is the exact blindness
 * docs/PHASE3-RETENTION-LEDGER.md §1 was written about.
 *
 * @param {string} cvText
 * @param {string} jdText
 * @param {number} maxCats
 * @param {number} maxItems
 * @param {number} minItems floor per category, in cv.md's own priority order
 * @returns {{category: string, items: string}[]}
 */
export function selectSkills(cvText, jdText, maxCats = 6, maxItems = 12, minItems = 3) {
  const jd = new Set(tokenize(jdText));
  const jdNorm = norm(jdText);
  // `tokenize` has a 3-character floor, so "C#" and "CI/CD" yield no tokens and
  // score zero however loudly the posting asks for them. Ranking hid that — they
  // still shipped, just last — and filtering exposed it: C# was dropped from five
  // postings that named it. Whole-phrase presence is the honest test, and it is
  // stricter than token overlap rather than looser.
  const named = (s) => jdNorm.includes(norm(s));
  const hits = (s) => tokenize(s).filter(t => jd.has(t)).length + (named(s) ? 1 : 0);
  const cats = parseSkillCategories(cvText);
  const related = relatedToJd(cvText, cats.flatMap(c => c.items), hits);
  const scored = cats
    .map((c, idx) => {
      // Tier as the primary key, so a related item outranks a floor item but
      // never displaces one the posting actually named.
      const ranked = c.items
        .map((item, i) => ({ item, i, score: hits(item), tier: hits(item) > 0 ? 2 : related.has(item) ? 1 : 0 }))
        .sort((a, b) => b.tier - a.tier || b.score - a.score || a.i - b.i);
      const kept = ranked.filter(x => x.tier > 0);
      const items = (kept.length >= minItems ? kept : ranked.slice(0, minItems))
        .slice(0, maxItems);
      // A category earns its slot on its items, not its name: "Security &
      // Cryptography" shares no token with a JD that asks for AES and OAuth.
      return {
        category: c.category, items, idx,
        score: items.reduce((a, b) => a + b.score, 0),
        // Whether the posting named anything in here at all. Summed score alone
        // let a category the posting asked for lose its slot to one scoring
        // higher on many weak matches: "Databases & Caching" placed seventh of
        // six on the Spotify and J.P. Morgan postings, which is why PostgreSQL
        // and MySQL went missing from two CVs that claim both.
        named: items.some(x => x.tier === 2),
      };
    })
    .sort((a, b) => b.score - a.score || a.idx - b.idx);

  // `maxCats` is a budget for the categories the posting did NOT name. One it did
  // is never cut: dropping it silently deletes a skill the CV genuinely claims and
  // the posting explicitly asked for, which no saved space is worth. On this
  // corpus it buys a seventh row on 2 of 32 offers.
  const askedFor = scored.filter(c => c.named);
  const filler = scored.filter(c => !c.named).slice(0, Math.max(0, maxCats - askedFor.length));
  return [...askedFor, ...filler]
    // Back to CV order once the cut is made — the categories read as a list, and
    // reordering them by posting relevance makes the same CV look different to a
    // human reading two of them side by side for no gain.
    .sort((a, b) => a.idx - b.idx)
    .map(c => ({ category: c.category, items: c.items.map(x => x.item).join(', ') }));
}

/**
 * A tailored-CV content object built entirely from the selected CV, with no
 * generation call.
 *
 * `summary` is deliberately left null rather than filled: it is generated by its
 * own stage (summary-stage.mjs) and keeping that stage identical is what makes
 * this a clean test of the *bullet* rewrite rather than a test of two changes at
 * once. The caller fills it.
 *
 * `competencies` and `education_modules` are also left out — local-pdf-offer.mjs
 * already overwrites both with deterministic, JD-ranked selections regardless of
 * what the writer returned, so producing them here would be dead code.
 *
 * @param {string} selectedCv the CV that cv-select trimmed for this posting
 * @param {string} cvText the full cv.md
 * @param {string} jdText
 * @param {{projectBullets?: number}} [opts] 0 keeps the one-paragraph blurb
 * @returns {{summary: null, experience: any[], projects: any[], skills: any[]}}
 */
export function verbatimContent(selectedCv, cvText, jdText, opts = {}) {
  // A per-project ceiling, no longer the count. cv-select now shares one total
  // budget (4 projects x 2 bullets, unchanged) across the projects it kept, so
  // the entries arriving here already hold 1-4 bullets and the page still gets
  // eight of them; clipping every project back to 2 here would undo the split.
  // The number still matters as a ceiling — the bench stops before the PDF, so
  // a shape that only fits in principle scores well on every metric and renders
  // three pages in production.
  const { projectBullets = 4 } = opts;
  const named = (n) => parseCvSections(selectedCv).find(s => s.name === n);
  const expSec = named('Experience');
  const projSec = named('Projects');

  const experience = expSec
    ? parseEntries(expSec.lines).entries.map(e => ({
        company: entryCompany(e),
        bullets: [...e.bullets],
      }))
    : [];

  // An empty description makes padProjectDescriptions build the whole blurb from
  // the project's own CV clauses, least-covered first — which is exactly the
  // verbatim rendering wanted here, and is already the shipped repair path for a
  // description the 7B truncated. Reusing it beats a second clause-joiner that
  // would drift from it.
  const projEntries = projSec ? parseEntries(projSec.lines).entries : [];
  const projects = padProjectDescriptions(
    projEntries.map(e => ({ name: e.head[0].replace(/^###\s+/, '').trim(), description: '' })),
    selectedCv)
    .map((p, i) => ({
      ...p,
      // Bullets alongside the blurb, not instead of it: the template picks the
      // list when it is there and falls back to the paragraph otherwise, so a
      // run with projectBullets 0 renders exactly as before.
      //
      // This is the field that carries the differentiators. cv-select already
      // ranked these bullets against the posting, and the paragraph form was
      // discarding all but the first clause or two of them — measured against
      // the Opus labels, every differentiator lost on a sampled offer was a
      // project bullet the blurb had no room for.
      bullets: projectBullets ? (projEntries[i]?.bullets || []).slice(0, projectBullets) : [],
    }));

  return { summary: null, experience, projects, skills: selectSkills(cvText, jdText) };
}

// ── self-check ────────────────────────────────────────────────────────────────

if (process.argv[1] && process.argv[1].endsWith('cv-writers.mjs')) {
  const assert = (await import('assert')).default;
  const cv = [
    '# X', '', '## Experience', '',
    '### Engineer', '**Acme** — London | Jan 2024 – Present', '',
    '- Shipped a thing serving 170 users', '- Cut latency by 80%', '',
    '## Projects', '', '### Widget', '**Personal** | Rust | 2025', '',
    '- Built a Rust widget with AES-256-GCM encryption and a lock-free ring buffer',
    '- Benchmarked it across 7 schemes and 5 payload sizes with bootstrap CIs', '',
    '## Skills', '',
    '**Languages:** Rust, Java, Python',
    '**Security:** AES-256-GCM, OAuth 2.0 (working knowledge, self-study), JWT',
    '**Cloud:** AWS, Docker',
  ].join('\n');

  const c = verbatimContent(cv, cv, 'We need Rust and AES-256-GCM encryption experience.');

  assert.equal(c.experience.length, 1, 'one employer');
  assert.deepEqual(c.experience[0].bullets,
    ['Shipped a thing serving 170 users', 'Cut latency by 80%'], 'bullets verbatim');
  assert.equal(c.projects.length, 1);
  assert.match(c.projects[0].description, /Rust widget/, 'description built from CV clauses');

  // The parenthesised aside must not split into a bogus item.
  const sec = parseSkillCategories(cv).find(s => s.category === 'Security');
  assert.deepEqual(sec.items, ['AES-256-GCM', 'OAuth 2.0', 'JWT'], 'aside stripped before split');

  // Security outranks Cloud on this JD, but CV order survives the cut.
  const cats = c.skills.map(s => s.category);
  assert.deepEqual(cats, ['Languages', 'Security', 'Cloud'], 'all three fit under maxCats');
  // Tight cut: Security is the only category this JD scores, so it must survive
  // it — and the pair must still come back in CV order, not relevance order.
  const two = selectSkills(cv, 'We need AES-256-GCM encryption.', 2);
  assert.deepEqual(two.map(s => s.category), ['Languages', 'Security'],
    'scored category kept, ties broken by CV order, output re-sorted by CV order');

  // Items inside a category are ranked, so a tight item cap keeps the matched one.
  const [oneItem] = selectSkills(cv, 'We need JWT.', 1, 1);
  assert.deepEqual(oneItem, { category: 'Security', items: 'JWT' }, 'items ranked within a category');

  // Non-earning items are dropped once the floor is already met. Five languages,
  // four of them named by the posting, so the fifth has nothing to buy.
  const wide = cv.replace('**Languages:** Rust, Java, Python',
    '**Languages:** Rust, Java, Python, Dart, Kotlin');
  const [langs] = selectSkills(wide, 'We need Rust, Java, Python and Dart.', 1);
  assert.deepEqual(langs, { category: 'Languages', items: 'Rust, Java, Python, Dart' },
    'items the posting never mentions are dropped, not ranked last');

  // ...but the floor holds when the posting matches almost nothing, so the block
  // never shrinks to a mirror of the JD.
  const [floored] = selectSkills(wide, 'We need Kotlin.', 1);
  assert.equal(floored.items.split(', ').length, 3, 'floors at minItems in CV order');
  assert.match(floored.items, /^Kotlin, /, 'the earner still leads');

  // A parenthesis holding names is the keywords, not an aside: promote them to
  // items so a posting asking for Kafka can match one.
  const parens = [
    '## Skills', '',
    '**Backend:** Message Queues (RabbitMQ, Kafka), Kubernetes (working knowledge, self-study)',
    '**Crypto:** Applied Cryptography (AES-256-GCM, HMAC-SHA256, end-to-end encryption)',
  ].join('\n');
  const [backend, crypto] = parseSkillCategories(parens);
  assert.deepEqual(backend.items, ['Message Queues', 'RabbitMQ', 'Kafka', 'Kubernetes'],
    'names promoted, prose aside dropped');
  assert.deepEqual(crypto.items, ['Applied Cryptography', 'AES-256-GCM', 'HMAC-SHA256'],
    'a mixed parenthesis keeps the names and drops the prose');

  // Relatedness: the posting says Java and never says Spring Boot, but cv.md
  // wrote them on one line, so Spring Boot ships too.
  const rel = [
    '# X', '', '## Experience', '',
    '### Engineer', '**Acme** — London | Jan 2024 – Present', '',
    '- Built the billing service in Java / Spring Boot / Kafka', '',
    '## Skills', '',
    '**Backend:** Java, Spring Boot, Kafka, Django, Flutter',
  ].join('\n');
  const [byRel] = selectSkills(rel, 'We need a Java engineer.', 1);
  assert.deepEqual(byRel.items.split(', '), ['Java', 'Spring Boot', 'Kafka'],
    'items the CV ties to the posting ship; unrelated ones do not');

  // The tie must be earned in the prose. Same CV, same posting, but with the
  // evidence bullet gone there is nothing linking Spring Boot to Java.
  const noEvidence = rel.replace('- Built the billing service in Java / Spring Boot / Kafka', '- Built the billing service');
  const [noRel] = selectSkills(noEvidence, 'We need a Java engineer.', 1);
  assert.deepEqual(noRel.items.split(', '), ['Java', 'Spring Boot', 'Kafka'],
    'floor still fills to minItems in CV order');
  assert.equal(selectSkills(noEvidence, 'We need a Java engineer.', 1, 12, 1)[0].items, 'Java',
    'with no floor and no prose tie, only the named item survives');

  console.log('cv-writers self-check OK');
}
