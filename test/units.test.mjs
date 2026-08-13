// Pure exported helpers — the ones the pipeline scripts import rather than the
// scripts themselves. Everything here runs in-process with no model, browser or
// network, so these are the assertions that pin exact numbers; the end-to-end
// suites only check that the wiring holds.
import { readFileSync, existsSync } from 'fs';
import { pass, fail, ROOT, join, pathToFileURL } from './harness.mjs';

const eq = (actual, expected, label) =>
  actual === expected ? pass(label) : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
const deepEq = (actual, expected, label) =>
  JSON.stringify(actual) === JSON.stringify(expected)
    ? pass(label)
    : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);

// ── 19. UNIT — text-utils and the scan filters ──────────────────────

console.log('\n19. Units — text-utils and the scan filters');

// ---- batch/text-utils.mjs ------------------------------------------------
try {
  const {
    cleanCvForPrompt, cleanJd, extractSalary, parseCompTargets,
    compScoreFromSalary, buildCompBlock,
  } = await import(pathToFileURL(join(ROOT, 'batch/text-utils.mjs')).href);

  // cleanCvForPrompt — embedded images are pure token burn.
  const cv = 'Header\n![](data:image/png;base64,AAAA)\ntrailing   \n\n\n\nBody\n';
  eq(cleanCvForPrompt(cv), 'Header\n\ntrailing\n\nBody', 'cleanCvForPrompt drops data-URI images, trailing space and blank runs');
  eq(cleanCvForPrompt(null), '', 'cleanCvForPrompt tolerates a null CV');

  eq(cleanJd(''), '(no JD available)', 'cleanJd names an empty JD rather than returning nothing');

  // Apify pre-fetch caches JDs as raw HTML; cleanJd has to be idempotent on text.
  const html = '<div><h2>Role</h2><script>evil()</script><style>.x{}</style>'
    + '<ul><li>Rust &amp; Go</li><li>5+ years</li></ul><p>Apply now</p></div>';
  const cleaned = cleanJd(html);
  if (!/[<>]/.test(cleaned) && /Rust & Go/.test(cleaned) && !/evil\(\)/.test(cleaned)) {
    pass('cleanJd strips HTML, drops script/style bodies and decodes entities');
  } else fail(`cleanJd on HTML gave: ${JSON.stringify(cleaned)}`);
  eq(cleanJd('plain text\r\nwith CR'), 'plain text\nwith CR', 'cleanJd normalises CRLF and leaves plain text alone');

  // A boilerplate marker past the midpoint ends the JD; one near the top does not.
  const body = 'Requirements: Rust, Go, Kubernetes. '.repeat(20);
  eq(/equal opportunity/i.test(cleanJd(`${body}\nWe are an equal opportunity employer and value diversity.`)), false,
    'cleanJd cuts a trailing EEO section');
  eq(/equal opportunity/i.test(cleanJd(`We are an equal opportunity employer.\n${body}`)), true,
    'cleanJd keeps a boilerplate marker that appears before the midpoint');

  const capped = cleanJd('x'.repeat(500), 100);
  if (capped.length <= 106 && capped.endsWith('[...]')) pass('cleanJd hard-caps the JD and marks the cut');
  else fail(`cleanJd cap gave ${capped.length} chars ending ${JSON.stringify(capped.slice(-8))}`);

  // extractSalary — ranges win over singles, and only annual figures count.
  deepEq(extractSalary('Salary £40,000 - £55,000 depending on experience'),
    { currency: '£', min: 40000, max: 55000, raw: '£40,000 - £55,000' },
    'extractSalary reads a symbol-prefixed range');
  deepEq(extractSalary('£40k to £55k'), { currency: '£', min: 40000, max: 55000, raw: '£40k to £55k' },
    'extractSalary expands the k suffix on both bounds');
  deepEq(extractSalary('GBP 45000 - 60000'), { currency: '£', min: 45000, max: 60000, raw: 'GBP 45000 - 60000' },
    'extractSalary maps a currency word to its symbol');
  deepEq(extractSalary('€85,000 – €110,000'), { currency: '€', min: 85000, max: 110000, raw: '€85,000 – €110,000' },
    'extractSalary handles an en-dash range');
  // A flattened <span>lo</span><span>hi</span> pay widget reads as two adjacent figures.
  deepEq(extractSalary('<span>£325,000</span> <span>£485,000</span>'),
    { currency: '£', min: 325000, max: 485000, raw: '£325,000 £485,000' },
    'extractSalary reads an adjacent double-currency pair as one range');
  deepEq(extractSalary('The offer is £45,000 annually'), { currency: '£', min: 45000, max: 45000, raw: '£45,000' },
    'extractSalary returns a single figure as a zero-width range');
  deepEq(extractSalary('90000 EUR per annum'), { currency: '€', min: 90000, max: 90000, raw: '90000 EUR' },
    'extractSalary reads a suffix currency word');

  eq(extractSalary('£500 per day'), null, 'extractSalary rejects a day rate');
  eq(extractSalary('$12,000'), null, 'extractSalary rejects a figure below the plausible floor');
  eq(extractSalary('$900,000'), null, 'extractSalary rejects a figure above the plausible ceiling');
  eq(extractSalary('a role with no numbers'), null, 'extractSalary returns null when no salary is stated');
  eq(extractSalary(null), null, 'extractSalary tolerates null input');

  // KNOWN BUG, pinned rather than fixed here: with the currency written as a
  // suffix and a plain hyphen, the range regex does not match, so the single-
  // figure pass wins and the lower bound is lost. The symbol-prefixed form of
  // the same range (asserted above) parses correctly. Phase 2 weights comp at
  // 0.20 off these numbers, so a posting written this way is mis-scored.
  deepEq(extractSalary('85,000 - 110,000 EUR'), { currency: '€', min: 110000, max: 110000, raw: '110,000 EUR' },
    'extractSalary currently drops the lower bound of a suffix-currency hyphen range (known bug)');

  // parseCompTargets — read out of config/profile.yml text, never guessed.
  deepEq(parseCompTargets('target_range: "70,000 - 90,000"\nminimum: "60,000"'),
    { floor: 60000, targetLow: 70000, targetHigh: 90000 }, 'parseCompTargets reads the range and the floor');
  deepEq(parseCompTargets('target_range: "70,000 - 90,000"'),
    { floor: 70000, targetLow: 70000, targetHigh: 90000 }, 'parseCompTargets falls back to the range low as the floor');
  eq(parseCompTargets('no targets here'), null, 'parseCompTargets returns null with no target_range');
  eq(parseCompTargets(null), null, 'parseCompTargets tolerates null config text');

  // compScoreFromSalary — the whole 1-5 band ladder.
  const targets = { floor: 60000, targetLow: 70000, targetHigh: 90000 };
  const at = n => compScoreFromSalary({ min: n, max: n }, targets);
  eq(compScoreFromSalary(null, targets), null, 'compScoreFromSalary returns null with no salary');
  eq(compScoreFromSalary({ min: 80000, max: 80000 }, null), 3, 'compScoreFromSalary scores neutral with no targets configured');
  eq(at(50000), 1, 'compScoreFromSalary scores 1 below the floor');
  eq(at(65000), 2, 'compScoreFromSalary scores 2 between the floor and the target low');
  eq(at(75000), 3, 'compScoreFromSalary scores 3 in the lower half of the target band');
  eq(at(85000), 4, 'compScoreFromSalary scores 4 in the upper half of the target band');
  eq(at(95000), 5, 'compScoreFromSalary scores 5 above the target high');

  // buildCompBlock — the code-owned Block D, both arms.
  const withSalary = buildCompBlock({ currency: '£', min: 70000, max: 90000 }, 4, targets);
  if (/£70,000–£90,000/.test(withSalary) && /vs target £70,000–£90,000 \(floor £60,000\)/.test(withSalary)) {
    pass('buildCompBlock prints the parsed range against the configured targets');
  } else fail(`buildCompBlock with salary gave: ${withSalary.slice(0, 200)}`);
  if (/no comp targets configured/.test(buildCompBlock({ currency: '£', min: 70000, max: 70000 }, 3, null))) {
    pass('buildCompBlock says so when no comp targets are configured');
  } else fail('buildCompBlock did not flag the missing comp targets');

  const noSalary = buildCompBlock(null, null, targets);
  if (/Not stated/.test(noSalary) && /excluded from the composite/.test(noSalary) && /0\.625/.test(noSalary)) {
    pass('buildCompBlock drops comp from the composite and states the two-term weights');
  } else fail(`buildCompBlock without salary gave: ${noSalary.slice(0, 200)}`);
} catch (e) {
  fail(`text-utils unit tests crashed: ${e.message}`);
}

// ---- scan.mjs filters ----------------------------------------------------
// Importing scan.mjs is safe: it only runs main() when it is process.argv[1].
try {
  const {
    buildTitleFilter, buildLocationFilter, buildSalaryFilter,
    extractCareersUrlDomain, pickRediscoveredUrl, shouldDedupScanHistoryRow,
  } = await import(pathToFileURL(join(ROOT, 'scan.mjs')).href);

  // ---- title filter ----
  eq(buildTitleFilter(null)('anything'), true, 'buildTitleFilter with no config keeps everything');
  const title = buildTitleFilter({ positive: ['engineer', 'developer'], negative: ['intern', 'sales'] });
  eq(title('Senior Backend Engineer'), true, 'title filter keeps a title matching a positive keyword');
  eq(title('Marketing Manager'), false, 'title filter drops a title matching no positive keyword');
  eq(title('Engineer Intern'), false, 'a negative keyword outranks a positive one');
  eq(buildTitleFilter({ negative: ['intern'] })('Anything At All'), true,
    'a filter with only negatives keeps everything else');

  // ---- location filter ----
  // Tier order: always_allow → block → allow. always_allow outranks block so a
  // multi-location string ("Remote, Belgium or France") survives a France block.
  eq(buildLocationFilter(null)('Mars'), true, 'buildLocationFilter with no config keeps everything');
  const loc = buildLocationFilter({ always_allow: ['belgium'], allow: ['remote', 'london'], block: ['france', 'onsite only'] });
  eq(loc('Remote — Europe'), true, 'location filter keeps a location matching an allow keyword');
  eq(loc('Austin, TX'), false, 'location filter drops a location matching no allow keyword');
  eq(loc('London (onsite only)'), false, 'a blocked keyword outranks an allowed one');
  eq(loc('Remote, Belgium or France'), true, 'always_allow outranks block');
  eq(loc(''), true, 'an empty location passes rather than being penalised');
  eq(loc(null), true, 'a non-string location passes rather than being penalised');
  eq(buildLocationFilter({ block: ['france'] })('Berlin'), true,
    'a filter with only blocks keeps everything else');
  // A bare string is wrapped, and an empty keyword is dropped — String.includes('')
  // is true for every location, which would silently disable the other tiers.
  eq(buildLocationFilter({ allow: 'remote' })('Fully Remote'), true, 'a bare-string keyword list is wrapped to one item');
  eq(buildLocationFilter({ allow: ['', 'london'] })('Berlin'), false, 'an empty keyword is dropped, not matched against everything');
  eq(buildLocationFilter({ allow: [42, 'london'] })('London'), true, 'a non-string keyword is filtered out without crashing');

  // ---- salary filter ----
  eq(buildSalaryFilter(null)({ min: 1, max: 2 }), true, 'buildSalaryFilter with no config keeps everything');
  eq(buildSalaryFilter({ min: 0, max: 0 })({ min: 1 }), true, 'a 0/0 salary filter is treated as disabled');
  eq(buildSalaryFilter({ min: -1 })({ min: 1 }), true, 'a negative bound disables the salary filter rather than mis-filtering');
  eq(buildSalaryFilter({ min: 'abc' })({ min: 1 }), true, 'a non-numeric bound disables the salary filter');
  eq(buildSalaryFilter({ min: 90000, max: 50000 })({ min: 1 }), true, 'min above max disables the salary filter');

  const sal = buildSalaryFilter({ min: 60000, max: 100000, currency: 'gbp' });
  eq(sal(null), true, 'salary filter passes a job with no salary data at all');
  eq(sal({}), true, 'salary filter passes a job whose salary object has no usable bounds');
  eq(sal({ min: 70000, max: 90000, currency: 'GBP' }), true, 'salary filter keeps a range inside the window');
  eq(sal({ min: 20000, max: 40000, currency: 'GBP' }), false, 'salary filter drops a range entirely below the floor');
  eq(sal({ min: 150000, max: 200000, currency: 'GBP' }), false, 'salary filter drops a range entirely above the ceiling');
  eq(sal({ min: 50000, max: 70000, currency: 'GBP' }), true, 'salary filter keeps a range that merely overlaps the window');
  eq(sal({ min: 70000, max: 90000, currency: 'EUR' }), false, 'salary filter drops a job in a different stated currency');
  eq(sal({ min: 70000, max: 90000 }), true, 'salary filter keeps a job whose currency is unstated');
  eq(sal({ max: 80000, currency: 'GBP' }), true, 'salary filter mirrors a missing min onto the max');
  eq(sal({ min: 80000, currency: 'GBP' }), true, 'salary filter mirrors a missing max onto the min');
  eq(buildSalaryFilter({ min: 60000 })({ max: 30000 }), false, 'a floor-only filter still drops a job below it');
  eq(buildSalaryFilter({ max: 100000 })({ min: 200000 }), false, 'a ceiling-only filter still drops a job above it');

  // ---- 404 rediscovery ----
  eq(extractCareersUrlDomain('https://jobs.acme.com/careers'), 'jobs.acme.com',
    'extractCareersUrlDomain returns the hostname');
  eq(extractCareersUrlDomain('not a url'), null, 'extractCareersUrlDomain returns null for an unparseable URL');
  eq(extractCareersUrlDomain(''), null, 'extractCareersUrlDomain returns null for an empty URL');

  eq(pickRediscoveredUrl(['https://jobs.acme.com/x'], 'jobs.acme.com'), 'https://jobs.acme.com/x',
    'pickRediscoveredUrl takes the first same-host result');
  eq(pickRediscoveredUrl(['https://evil-jobs.acme.com.attacker.net/x'], 'jobs.acme.com'), null,
    'pickRediscoveredUrl requires an exact hostname, not a substring');
  eq(pickRediscoveredUrl(['/l/?uddg=' + encodeURIComponent('https://jobs.acme.com/y')], 'jobs.acme.com'),
    'https://jobs.acme.com/y', 'pickRediscoveredUrl unwraps a DuckDuckGo redirect before matching');
  eq(pickRediscoveredUrl(['not a url', 'https://jobs.acme.com/z'], 'jobs.acme.com'), 'https://jobs.acme.com/z',
    'pickRediscoveredUrl skips unparseable hrefs');
  eq(pickRediscoveredUrl(['https://jobs.acme.com/x'], null), null, 'pickRediscoveredUrl returns null with no domain');
  eq(pickRediscoveredUrl('not an array', 'jobs.acme.com'), null, 'pickRediscoveredUrl returns null for a non-array');
  eq(pickRediscoveredUrl([], 'jobs.acme.com'), null, 'pickRediscoveredUrl returns null when nothing matches');

  // ---- scan-history dedup policy ----
  const today = '2026-07-30';
  eq(shouldDedupScanHistoryRow({ firstSeen: '2026-07-29', status: 'added' }, { today }), true,
    'a row seen yesterday is deduped when no recheck window is configured');
  eq(shouldDedupScanHistoryRow({ firstSeen: '2026-07-01', status: 'added' }, { recheckAfterDays: 7, today }), false,
    'a row older than the recheck window becomes eligible again');
  eq(shouldDedupScanHistoryRow({ firstSeen: '2026-07-29', status: 'added' }, { recheckAfterDays: 7, today }), true,
    'a row inside the recheck window stays deduped');
  eq(shouldDedupScanHistoryRow({ firstSeen: '2026-07-01', status: 'skipped_blocked_host' }, { recheckAfterDays: 7, today }), true,
    'a blocked-host row is permanently deduped regardless of the recheck window');
  eq(shouldDedupScanHistoryRow({ firstSeen: '2026-07-01', status: 'skipped_invalid_url' }, { recheckAfterDays: 7, today }), true,
    'an invalid-url row is permanently deduped regardless of the recheck window');
  eq(shouldDedupScanHistoryRow({ firstSeen: 'garbage', status: 'added' }, { recheckAfterDays: 7, today }), true,
    'an unparseable firstSeen date falls back to deduping');
} catch (e) {
  fail(`scan filter unit tests crashed: ${e.message}`);
}

// ── 20. UNIT — Phase 3 tailoring-harness metrics ─────────────────────
// These decide whether a benchmark says a change worked, so a silent break
// here invalidates every comparison rather than failing loudly.
try {
  const h = await import(pathToFileURL(join(ROOT, 'batch/tailor-harness.mjs')).href);
  // The product vocabulary lives with the pipeline guard that enforces it,
  // not with the bench that only measures it.
  const g = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);

  // numsOf: what counts as a "number the CV must contain"
  deepEq([...h.numsOf('grew from 80 at launch to 170')], ['80', '170'],
    'numsOf pulls plain integers');
  deepEq([...h.numsOf('cut onboarding by 80% across 5 locations')], ['80%'],
    'numsOf keeps the percent sign, so 80 and 80% are not interchangeable');
  deepEq([...h.numsOf('serving 10,000+ users')], ['10,000+'],
    'numsOf keeps thousands separators and the plus, so 10,000+ != 10000');
  deepEq([...h.numsOf('no digits here')], [],
    'numsOf returns nothing for prose');
  deepEq([...h.numsOf('version 3 of 4')], [],
    'numsOf drops single digits — too noisy to attribute to the CV');

  // shingles: the example-copy detector
  const sh = h.shingles('one two three four five six seven eight nine', 8);
  eq(sh.size, 2, 'shingles yields (n - k + 1) windows');
  eq(sh.has('one two three four five six seven eight'), true, 'shingles keeps word order');
  eq(h.shingles('too short for a window', 8).size, 0,
    'shingles yields nothing below the window size');
  eq(h.shingles('ONE Two THREE four five six seven eight').has('one two three four five six seven eight'), true,
    'shingles is case-insensitive, so recased copying is still caught');

  // exampleShingles: reads the real prompt, so it breaks if the example moves
  const ex = h.exampleShingles();
  eq(ex.size > 0, true, 'exampleShingles finds bullets in the tailor prompt');
  // Probe the snapshot the harness itself would pick rather than a phrase from
  // one particular CV: the committed file is generic placeholder text, and a
  // real run overrides it with example-bullets.local.json. Hardcoding either
  // one's wording here would fail on whichever machine has the other.
  const activeSnapshot = ['batch/bench/example-bullets.local.json', 'batch/bench/example-bullets.json']
    .map(p => join(ROOT, p)).find(existsSync);
  const snapBullets = JSON.parse(readFileSync(activeSnapshot, 'utf8'));
  eq(snapBullets.every(b => [...h.shingles(b)].every(s => ex.has(s))), true,
    'exampleShingles covers every bullet of the active worked-example snapshot');
  // The detector must not be definable purely by the live prompt: deleting the
  // worked example would then zero example_copy_pct by construction and score a
  // win the model never earned. The snapshot is what keeps the question honest.
  const snapOnly = h.exampleShingles.length === 0 && (() => {
    const fixture = JSON.parse(readFileSync(join(ROOT, 'batch/bench/example-bullets.json'), 'utf8'));
    return fixture.length > 0 && fixture.every(b => typeof b === 'string');
  })();
  eq(snapOnly, true,
    'a committed snapshot of the worked example exists, so the copy detector survives its deletion');

  // ── skill_coverage: the metric ats_coverage was too blunt to be ──
  // Scored as phrases against cv.md's own taxonomy, so it is bounded above by
  // what the CV actually claims and cannot be gamed by inventing.
  const skillCv = [
    '## Skills', '',
    '**Languages:** Rust, Java, C#',
    '**Backend:** Message Queues (RabbitMQ, Kafka), Kubernetes (working knowledge, self-study)',
  ].join('\n');
  const sc = (jd, out) => h.skillCoverage(jd, skillCv, out);

  eq(sc('We need Java and Kafka.', 'Built with Java and Kafka').coverage, 1,
    'skillCoverage is 1.0 when every skill the posting named reached the page');
  eq(sc('We need Java and Kafka.', 'Built with Java').coverage, 0.5,
    'and drops when one the CV genuinely claims did not');
  deepEq(sc('We need Java and Kafka.', 'Built with Java').missed, ['Kafka'],
    'and names which, so a regression is actionable rather than just lower');
  eq(sc('We need Go and Elixir.', 'Built with Java').coverage, null,
    'a posting naming no skill the CV claims scores null, not 0 — nothing was missed');
  eq(sc('We need Kotlin.', 'Kotlin Kotlin Kotlin').coverage, null,
    'and cannot be gamed by output the CV never claimed');
  // The 3-char token floor made these invisible to JD-overlap scoring, so they
  // were dropped from postings that asked for them by name.
  eq(sc('Strong C# required.', 'Shipped C# services').coverage, 1,
    'a short skill name is matched as a phrase, not as tokens');
  // Parenthesised names are the keywords, so the taxonomy has to contain them.
  eq(sc('Experience with RabbitMQ.', 'Ran RabbitMQ in production').coverage, 1,
    'names promoted out of a parenthesis count as claimed skills');

  // An item cv.md writes as alternatives. Postings write one of them, never the
  // CV's exact string, and matching the whole phrase dropped them from the
  // denominator rather than counting them as misses — 31 postings named
  // TypeScript against a CV that writes "TypeScript / JavaScript".
  const altCv = ['## Skills', '', '**Languages:** TypeScript / JavaScript, C/C++'].join('\n');
  const alt = (jd, out) => h.skillCoverage(jd, altCv, out);
  eq(alt('Strong TypeScript required.', 'Shipped TypeScript / JavaScript services').asked, 1,
    'a posting naming one alternative asks for the item');
  eq(alt('Strong TypeScript required.', 'Shipped TypeScript / JavaScript services').coverage, 1,
    'and the item shipping under its full name covers it');
  eq(alt('Strong TypeScript required.', 'Shipped Rust services').coverage, 0,
    'and a page that dropped it scores a real miss rather than a null');
  // The tight slash is one name, not two: splitting it would have a posting
  // asking for the letter C match a CV that claims C/C++.
  eq(alt('We use C for embedded work.', 'Wrote C/C++ firmware').asked, 0,
    'a compound name containing a slash is not split into alternatives');
  const w = await import(pathToFileURL(join(ROOT, 'batch/cv-writers.mjs')).href);
  deepEq(w.skillForms('Agile / Scrum'), ['Agile', 'Scrum'], 'a spaced slash lists alternatives');
  deepEq(w.skillForms('CI/CD'), ['CI/CD'], 'a tight slash is part of the name');

  // ── generateSummary: clean is a floor, not a ranking ──
  // Both drafts pass every guard, so the old code shipped whichever came first.
  // `scoreSummary` existed and only ever fired on the repair path.
  {
    const sumCv = [
      '## Experience', '',
      '### Engineer', '**Acme** | 2024', '',
      '- Built a payment service in Rust handling 900 requests per second with 99.9% uptime',
      '- Shipped a Postgres migration tool cutting deploy time from 40 minutes to 6 minutes',
    ].join('\n');
    const bullets = ['Engineer: Built a payment service in Rust handling 900 requests per second with 99.9% uptime',
                     'Engineer: Shipped a Postgres migration tool cutting deploy time from 40 minutes to 6 minutes'];
    // 50-80 words, prose, every claim in the CV. The first is thin on evidence
    // overlap; the second reuses the bullets' own wording, which is what
    // scoreSummary rewards.
    const thin = 'Engineer with experience across backend work and a steady record of delivery in '
      + 'production settings. Comfortable owning services end to end, working with relational stores, '
      + 'and keeping systems available for the people who depend on them day to day across a range of '
      + 'teams, projects and reporting lines over several years of steady practice.';
    const rich = 'Engineer with proven expertise in Rust backend services and database tooling. '
      + 'Built a payment service in Rust handling 900 requests per second with 99.9% uptime, and '
      + 'shipped a Postgres migration tool cutting deploy time from 40 minutes to 6 minutes. '
      + 'Comfortable owning production services end to end across the whole delivery cycle.';
    const calls = [];
    // The tailored call carries the requirements; the sibling withholds them.
    const call = async (_sys, usr) => { calls.push(usr); return /REQUIREMENT-MARKER/.test(usr) ? thin : rich; };
    const got = await g.generateSummary({ bullets, role: 'Engineer', cvText: sumCv,
      reqs: ['REQUIREMENT-MARKER'], jdText: '', call });
    eq(calls.length, 2, 'the sibling is drafted even when the first draft is clean');
    eq(/900 requests per second/.test(String(got)), true,
      'and the better-scoring draft ships, not merely the first usable one');

    // Reversed: the tailored draft is the strong one and must keep the page.
    const call2 = async (_sys, usr) => (/REQUIREMENT-MARKER/.test(usr) ? rich : thin);
    const got2 = await g.generateSummary({ bullets, role: 'Engineer', cvText: sumCv,
      reqs: ['REQUIREMENT-MARKER'], jdText: '', call: call2 });
    eq(/900 requests per second/.test(String(got2)), true,
      'and a tie or near-tie keeps the tailored draft, so showing the posting is not undone');
  }

  // ── product_fab: the truth invariant behind the two-tier vocabulary rule ──
  const cvMd = 'Built services in Rust and TypeScript on AWS with PostgreSQL and Redis.';
  deepEq(g.productFab('Delivered Kotlin microservices on GCP with Terraform', cvMd),
    ['gcp', 'kotlin', 'terraform'],
    'productFab reports every named product the CV never mentions');
  deepEq(g.productFab('Built Rust services on AWS backed by PostgreSQL', cvMd), [],
    'productFab stays silent when every product is in the CV');
  deepEq(g.productFab('Owned distributed low-latency event-driven architecture', cvMd), [],
    'productFab ignores capability phrases — those are the tier that stays free');
  deepEq(g.productFab('Shipped AZURE and Angular work', cvMd), ['azure', 'angular'],
    'productFab is case-insensitive, so shouting a fabrication does not hide it');
  // Substring safety: "ray" must not fire inside "array", "sap" inside "sapling".
  deepEq(g.productFab('Optimised an array of sapling growth models', cvMd), [],
    'productFab matches whole phrases, not substrings of unrelated words');
  // Multi-word products have to survive the normaliser that strips punctuation.
  deepEq(g.productFab('Ran pipelines on Google Cloud and Power BI', cvMd),
    ['google cloud', 'power bi'],
    'productFab catches multi-word product names');

  // ── ats_coverage: scored against the supportable subset, so it cannot be gamed ──
  const jd = 'We need Kubernetes and PostgreSQL and GraphQL and Elixir experience';
  const cvAts = 'Ran Kubernetes clusters against PostgreSQL with a GraphQL gateway';
  const full = h.atsCoverage(jd, cvAts, 'Kubernetes PostgreSQL GraphQL delivery');
  eq(full.supportable, 3, 'only the three terms the CV supports are scorable — Elixir is not');
  eq(full.coverage, 1, 'covering every supportable term scores 1.0');
  const half = h.atsCoverage(jd, cvAts, 'Kubernetes only');
  eq(+half.coverage.toFixed(3), 0.333, 'coverage is the fraction of supportable terms reached');
  eq(h.atsCoverage(jd, cvAts, 'Kubernetes PostgreSQL GraphQL Elixir Elixir').coverage, 1,
    'stuffing an unsupportable term cannot push coverage above the CV\'s honest ceiling');

  // ── selection_regret: 0 when the shipped picks are the best available ──
  const atoms = [{ text: 'a' }, { text: 'b' }, { text: 'c' }];
  const V = { a: [1, 0], b: [0.6, 0.8], c: [0, 1] };
  const av = [V.a, V.b, V.c];
  const reqs = [[1, 0]];  // requirement points straight at atom "a"
  eq(h.selectionRegret(atoms, av, reqs, new Set([0])), 0,
    'picking the single best-matching atom is zero regret');
  eq(h.selectionRegret(atoms, av, reqs, new Set([2])) > 0.9, true,
    'picking the worst atom over the best is near-total regret');
  eq(h.selectionRegret(atoms, av, reqs, new Set([0, 1])), 0,
    'regret is against the best pick OF THE SAME SIZE, not against the single best');
  eq(h.selectionRegret(atoms, av, [], new Set([0])), null,
    'no requirements means no opinion, not a perfect score');
  eq(h.selectionRegret(atoms, av, reqs, new Set()), null,
    'shipping nothing is unscoreable rather than optimal');

  // shippedAtomIndices: maps rewritten output back to the atom it came from
  const srcAtoms = [
    { text: 'Migrated payment handling to Stripe removing card data from databases' },
    { text: 'Taught programming to 800 undergraduate students across two languages' },
  ];
  deepEq([...h.shippedAtomIndices(['Moved payment handling onto Stripe, removing card data'], srcAtoms)], [0],
    'a reworded bullet is credited to the atom it derives from');
  deepEq([...h.shippedAtomIndices(['Invented an unrelated quantum blockchain claim'], srcAtoms)], [],
    'a bullet resembling no atom is credited to none, rather than to the least-bad match');
} catch (e) {
  fail(`tailor-harness metric unit tests crashed: ${e.message}`);
}

// ── 21. UNIT — Phase 3 experience reconciliation ─────────────────────
// The failure shapes are the ones measured across 24 benchmark offers: the
// schema floor guarantees the entry count, this guarantees the entries are the
// real employers. See batch/PHASE3-EXPERIMENT-LEDGER.md.
try {
  const { reconcileExperience } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const cv = [
    '## Experience', '',
    '### Teaching Assistant',
    '**Northgate College** — Edinburgh | Sep 2025 – Present', '',
    '- Taught programming to 800+ undergraduates across two languages',
    '- Wrote setup guides cutting configuration time to 30 minutes', '',
    '### PM / Software Engineer',
    '**Acme SaaS** — Edinburgh | Oct 2024 – Sep 2025', '',
    '- Led a two-developer team building a subscription platform, MVP in 4 weeks',
    '- Automated billing and onboarding with a payment provider and OAuth 2.0',
  ].join('\n');
  const names = items => reconcileExperience(items, cv).map(e => e.company);
  const acme = { company: 'Acme SaaS', bullets: ['Led team, membership platform MVP 4 weeks'] };
  const college = { company: 'Northgate College', bullets: ['Taught 800+ students Java and C++'] };

  deepEq(names([college, acme]), ['Northgate College', 'Acme SaaS'],
    'a correct pair passes through unchanged');
  deepEq(names([acme, { company: 'Acme SaaS', bullets: ['Automated billing with Stripe'] }]),
    ['Northgate College', 'Acme SaaS'],
    'a duplicated employer is replaced by the missing one');
  deepEq(names([acme, { company: 'Analytics Dashboard', bullets: ['Built a SIEM dashboard with Okta'] }]),
    ['Northgate College', 'Acme SaaS'],
    'a project promoted to a job is dropped, not kept as a second employer');
  deepEq(names([acme]), ['Northgate College', 'Acme SaaS'],
    'a role the model omitted entirely is backfilled');
  deepEq(names([acme, college]), ['Northgate College', 'Acme SaaS'],
    'entries arriving out of CV order are returned in CV order');

  // Backfill must come from the CV verbatim; a claimed role keeps its rewrite.
  const out = reconcileExperience([acme], cv);
  eq(out[0].bullets.length, 2, 'a backfilled role carries the CV bullets');
  eq(out[0].bullets[0].includes('800+ undergraduates'), true,
    'backfilled bullets are the real CV text, not invented');
  eq(out[1].bullets[0], acme.bullets[0], 'a claimed role keeps the model rewrite first');

  // A claimed role the model half-filled is topped back up: the schema had no
  // floor on bullets at all, so one bullet under a four-bullet role shipped as a
  // one-line job. The rewrite leads; the CV bullets it did NOT come from follow.
  eq(out[1].bullets.length, 2, 'a claimed role is topped up to the CV bullet count');
  eq(out[1].bullets[1].includes('Automated billing'), true,
    'the topped-up bullet is the CV source the rewrite did not claim');
  eq(out[1].bullets.some(b => /two-developer team/.test(b) && /MVP in 4 weeks/.test(b)
                              && b !== acme.bullets[0]), false,
    'the CV bullet the rewrite came from is not re-appended alongside it');

  // A role the model filled completely is left exactly as it rewrote it.
  const full = reconcileExperience([{ company: 'Acme SaaS', bullets: [
    'Led a two-dev team, subscription platform MVP in 4 weeks',
    'Automated billing and onboarding with Stripe and OAuth 2.0',
  ] }], cv);
  eq(full[1].bullets.length, 2, 'a fully-filled role gains nothing from the top-up');

  // Project descriptions: the prompt asks for 35-55 words and got a median of 17
  // across twelve runs, 0 of 36 in band. Padding comes from the project's own CV
  // bullets, stops at the floor, and never repeats what the model already said.
  const { padProjectDescriptions } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const pcv = [
    '## Projects', '',
    '### Re:Link — Remote Access',
    '**Honours** | Rust | 2025', '',
    '- Designed a blind rendezvous protocol with client-side AES-256-GCM encryption; the server holds only ephemeral state',
    '- Eliminated an 8 MB-per-frame copy with a lock-free pre-allocated frame ring using atomic CAS slot ownership',
    '- Shipped cross-platform builds with 4 GitHub Actions CI pipelines and dual-stack IPv4/IPv6 ICE handling',
  ].join('\n');
  const wc = s => s.trim().split(/\s+/).length;

  const padded = padProjectDescriptions(
    [{ name: 'Re:Link — Remote Access', description: 'Built a peer-to-peer remote access system with AES-256-GCM encryption.' }],
    pcv);
  eq(wc(padded[0].description) >= 35, true, 'a short description is padded to the floor');
  eq(wc(padded[0].description) <= 55, true, 'and stops at the ceiling rather than overshooting');
  eq(/frame ring|CI pipelines/.test(padded[0].description), true,
    'the padding is real CV text from that project');

  // A pin naming nothing ranks as if the list were empty, which is the one
  // failure that looks like success. The runner asks this at startup because the
  // per-offer warning went to a log opened only when an offer failed.
  const { unmatchedPins } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  deepEq(unmatchedPins(pcv, ['Re:Link']), [], 'a pin matching a title is not reported');
  deepEq(unmatchedPins(pcv, ['re:link — remote access']), [],
    'the whole title matches, and case does not matter');
  deepEq(unmatchedPins(pcv, ['Zero Trust SIEM']), ['zero trust siem'],
    'a pin naming no title is reported — the shipped typo, pinned here');
  deepEq(unmatchedPins(pcv, ['Remote Access', 'Nowhere']), ['nowhere'],
    'a substring of a title counts as a match, so only the real miss is reported');
  deepEq(unmatchedPins(pcv, []), [], 'no pins is not a failure');
  eq((padded[0].description.match(/AES-256-GCM/g) || []).length, 1,
    'the clause the model already rewrote is not repeated');

  // A semicolon inside a parenthetical is not a clause boundary — splitting there
  // shipped a description ending mid-aside with the bracket never closed.
  const bracketCv = [
    '## Projects', '', '### Testbed', '**Personal** | Rust | 2025', '',
    '- Built a Rust microservice testbed (API gateway, hashing; manifest-signing services) comparing NIST post-quantum signatures against classical baselines',
    '- Executed 63,000+ benchmark runs across 7 signature schemes and 5 payload sizes with bootstrap confidence intervals',
    '- Hardened the pipeline against STRIDE-class threats with keyed token-bucket rate limiting and structured audit logging',
  ].join('\n');
  const bd = padProjectDescriptions([{ name: 'Testbed', description: 'Benchmarked signatures for cloud migration.' }], bracketCv)[0].description;
  const opens = (bd.match(/\(/g) || []).length, closes = (bd.match(/\)/g) || []).length;
  eq(opens, closes, 'padding never leaves an unbalanced bracket');

  // A fragment for an opening sentence is discarded, not padded around.
  const frag = padProjectDescriptions([{ name: 'Testbed', description: 'Built a high-performance.' }], bracketCv)[0].description;
  eq(/^Built a high-performance\./.test(frag), false, 'a fragment opening is dropped, not kept at the head');
  eq(wc(frag) >= 35, true, 'and the description is rebuilt from the CV to the floor');
  eq(/^[A-Z]/.test(frag) && !frag.startsWith('.'), true, 'a rebuilt description does not start with a stray period');

  const long = 'x '.repeat(40).trim();
  deepEq(padProjectDescriptions([{ name: 'Re:Link — Remote Access', description: long }], pcv),
    [{ name: 'Re:Link — Remote Access', description: long }],
    'a description already over the floor is untouched');
  deepEq(padProjectDescriptions([{ name: 'Nothing On The CV', description: 'short' }], pcv),
    [{ name: 'Nothing On The CV', description: 'short' }],
    'a project with no CV entry is left alone rather than padded from another');
  eq(Array.isArray(padProjectDescriptions(/** @type {any} */ (null), pcv)), false,
    'a non-array is returned untouched');
  deepEq(padProjectDescriptions([{ name: 'Re:Link', description: 'short' }], 'no projects section'),
    [{ name: 'Re:Link', description: 'short' }],
    'a CV with no Projects section pads nothing');

  // Regression (report 146): the model named the right employer but pasted the
  // OTHER role's bullets under it. A name hit scored 1+overlap, which cleared
  // the 0.35 floor on the name alone, so the mislabel was rubber-stamped and
  // the real bullets were lost. Content decides provenance now.
  const mislabelled = { company: 'Northgate College',
    bullets: ['Led a two-developer team building a subscription platform, MVP in 4 weeks'] };
  const fixed = reconcileExperience([mislabelled, acme], cv);
  eq(fixed[0].bullets[0].includes('800+ undergraduates'), true,
    'a bullet belonging to another role is rejected despite the right company name');
  eq(fixed.some(e => e.bullets.some(b => /subscription platform/.test(b) && e.company === 'Northgate College')), false,
    "the other role's bullet never appears under the wrong employer");
  eq(fixed[1].bullets[0], acme.bullets[0], 'the correctly-labelled role still keeps its rewrite');

  // Degenerate inputs must not throw or silently empty the section.
  deepEq(reconcileExperience([], cv).map(e => e.company),
    ['Northgate College', 'Acme SaaS'],
    'an empty model array backfills every role rather than yielding no experience');
  eq(Array.isArray(reconcileExperience(/** @type {any} */ (null), cv)), false,
    'a non-array is returned untouched for the caller to reject');
  deepEq(reconcileExperience([acme], 'no experience section here'), [acme],
    'a CV with no Experience section leaves the model output alone');

  // ── Summary fabrication guards ────────────────────────────────────────────
  // One shipped summary carried three false claims at once and every metric read
  // 0, because metric_fab only inspects experience bullets. The summary was the
  // one surface with no figure, tenure-range or credential guard at all.
  {
    const { verifySummaryFigures } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
    const { stripFabricatedCredentials, credentialFab } =
      await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const scv = ['# Me', '', '## Experience', '', '### Dev', '**Acme** | 2024', '',
      '- Grew paying subscribers from 80 at launch to 170 across 4 locations',
      '- Tested at 3M+ simulated events', '',
      '## Education', '', '**Northgate College**',
      '**BEng (Hons) Software Engineering — First Class Honours** | 2022 – 2026'].join('\n');
    const scvPlus = scv + '\n- Taught 800+ undergraduate students across two languages';

    // A figure the CV does not state — 170 members reported as "150+ users".
    const figs = verifySummaryFigures(
      'Engineer building secure systems. Delivered a platform serving 150+ users daily.', scv);
    eq(/150\+/.test(figs), false, 'a summary figure the CV never states is stripped');
    eq(/secure systems/.test(figs), true, 'and the clause that was fine survives');
    eq(verifySummaryFigures('Tested at 3M+ simulated events.', scv).includes('3M+'), true,
      'a figure the CV does state is kept');
    // The real 170 with a "+" appended overstates it — the pattern the bullet
    // guard already documents as a measured fabrication.
    // The "+" is the lie, not the claim — deflate to the CV's figure and keep the
    // sentence rather than deleting real evidence to remove one character.
    eq(verifySummaryFigures('Grew a GDPR-compliant platform to 170+ paying users.', scv),
      'Grew a GDPR-compliant platform to 170 paying users.',
      'an inflating "+" is corrected to the CV figure, keeping the clause');
    eq(/150\+/.test(verifySummaryFigures('Delivered a platform serving 150+ users.', scv)), false,
      'a figure with no CV basis at all is still stripped, not deflated');
    // Understating a "N+" figure is not a fabrication — "over 800 students" is
    // exactly what "800+" means, and deleting that sentence cost a true claim.
    eq(verifySummaryFigures('Taught over 800 students across two languages.', scvPlus),
      'Taught over 800 students across two languages.',
      'dropping the "+" from a CV figure understates it and is left alone');
    // A digit inside an identifier is a name, not a claim.
    eq(verifySummaryFigures('Targeting L40 Engineer roles at scale.', scv),
      'Targeting L40 Engineer roles at scale.',
      'a job level like L40 is not mistaken for an invented figure');

    // A credential the CV never claims. Napier is post-1992; the model inferred
    // "Russell Group" from the city in the school's name.
    deepEq(credentialFab('Russell Group graduate with First Class Honours.', scv), ['russell group'],
      'an ungrounded credential is detected and a grounded one is not');
    eq(credentialFab('First Class Honours graduate.', scv).length, 0,
      'a credential the CV does state is never flagged');
    eq(/Russell Group/i.test(stripFabricatedCredentials('Built systems. Russell Group graduate, strong fundamentals.', scv)),
      false, 'the ungrounded credential clause is dropped');

    // Clause surgery must not leave a sentence starting lowercase.
    const rebuilt = stripFabricatedCredentials('Russell Group graduate, strong fundamentals.', scv);
    eq(rebuilt === '' || /^[A-Z]/.test(rebuilt), true,
      'a rebuilt sentence is re-capitalised after its first clause is dropped');

    // A name lifted from the posting. productFab is an allowlist of technologies,
    // so a company or brand can never be on it: a JD.com posting shipped a summary
    // claiming the platform was built "for Joybuy Systems" — JD.com's own brand —
    // and every truth guard passed it.
    const { stripJdProperNouns } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const jd = 'We are Joybuy Systems, part of JD.com. You will build Kubernetes tooling.';
    eq(/Joybuy/.test(stripJdProperNouns(
      'Led a team building a membership platform for Joybuy Systems. Grew subscribers from 80 to 170.', scv, jd)),
      false, 'a company name the posting supplies and the CV lacks is dropped');
    // ...but a name the CV does state is the candidate's, however often the
    // posting also mentions it.
    eq(stripJdProperNouns('Studied at Northgate College and shipped systems.', scv, 'Northgate College alumni welcome.'),
      'Studied at Northgate College and shipped systems.',
      'a name grounded in the CV survives even when the posting names it too');
    // Stripping to nothing is a worse failure than the leak; the caller pads a
    // short summary, so an empty one would be padded into shapelessness.
    eq(stripJdProperNouns('Built tooling for Joybuy Systems.', scv, jd),
      'Built tooling for Joybuy Systems.', 'the guard never empties the summary');

    // Shape. Every case below is a real summary from offer 305 (Trustpilot), and
    // every one of them scores clean on all eight falsity metrics.
    const { summaryShape } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const w = n => Array.from({ length: n }, (_, i) => `word${i}`).join(' ');

    // Shipped 237: three bullets with the bullet points removed. 56 words, so
    // the band is fine — it is the missing positioning clause that makes it a
    // list rather than a summary.
    deepEq(summaryShape('Led a team to build a membership platform with bi-weekly releases. '
      + `Designed an event-driven microservices system ${w(30)}. Built a resilience layer.`),
      ['no_positioning'], 'a summary whose every sentence opens with a bullet verb has no positioning clause');

    // One positioning clause is enough — the rest may be achievements, and
    // should be.
    deepEq(summaryShape(`Backend and platform engineer who builds the automation ${w(28)}. `
      + `Led a team to build a membership platform ${w(20)}.`),
      [], 'an opening identity clause clears the shape check, achievements after it and all');

    // The run-on. clampSummaryWords kept sentences[0] unconditionally, so a
    // summary with no second sentence walked through a hard 70-word band
    // untouched — which is how a 75-word one shipped into a 2-page-capped PDF.
    const runOn = `Delivered technical instruction to over 800 students, ${w(70)}.`;
    eq(summaryShape(runOn).includes('run_on'), true,
      'a single sentence carrying the whole summary is flagged as a run-on');
    const { clampSummaryWords } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    eq(clampSummaryWords(runOn, 70).split(/\s+/).length, 70,
      'and the word-level fallback clamps it — the band is layout, not preference');
    eq(clampSummaryWords(runOn, 70).endsWith('.'), true,
      'a mid-clause cut still closes its sentence');
    // The sentence-level path is still preferred where it can do the job: it is
    // the one that produces readable output.
    eq(clampSummaryWords(`Backend engineer. ${w(80)}.`, 70), 'Backend engineer.',
      'a clean sentence boundary is taken in preference to cutting mid-clause');

    // The opener is the only sentence some readers finish.
    eq(summaryShape('Experienced software engineer with a proven track record in billing. '
      + `Led a team ${w(45)}.`).includes('filler_open'), true,
      'filler in the first sentence is flagged even when the rest is specific');
    eq(summaryShape(`Software engineer who ships billing systems ${w(40)}. `
      + 'Holds a proven track record.').includes('filler_open'), false,
      'filler after the opener is fillerCount\'s business, not the shape check\'s');

    deepEq(summaryShape(''), ['empty'], 'an empty summary is a shape defect, not a clean score');
    eq(summaryShape('Backend engineer.').includes('off_band'), true,
      'a two-word summary clears no_positioning and still fails the band — shape is not quality');

    // The posting's requirements are in the prompt as vocabulary, and are
    // labelled as such. This is the reversal the 7B could not be trusted with:
    // the requirement text must reach the model, and the evidence must still be
    // named as the only source of fact.
    const { summaryUser, scoreSummary } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const up = summaryUser(['Snipe: built an LLM pipeline'], 'Software Engineer',
      ['Solid understanding of backend development with Node.js', 'Hands-on experience with AWS or GCP']);
    eq(up.includes('Node.js') && up.includes('AWS or GCP'), true,
      'the posting\'s own wording reaches the writer');
    eq(up.includes('only source of fact'), true,
      'and is framed against the evidence rather than as material to claim');
    eq(up.includes('Snipe: built an LLM pipeline'), true, 'the evidence is still there');
    eq(summaryUser(['x'], 'Engineer').includes('(none listed'), true,
      'a report with no parseable Block B degrades to the evidence-only prompt');
    // Ranked below the cap so a twelve-requirement report cannot crowd out the
    // evidence.
    eq(summaryUser(['x'], 'Engineer', Array.from({ length: 12 }, (_, i) => `req${i}`)).includes('req8'),
      false, 'the requirement list is capped');

    // Domain, cased-product and figure fabrication — the three classes that
    // opened the moment the summary started reading the posting. Each case below
    // is a real shipped summary from the 32-offer bench, and every one of them
    // was reported clean by `product_fab`.
    const { domainFab, casedFab, summaryUnsupported } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const cvNoFin = 'Built a membership platform. Languages: Rust, Java, Python, TypeScript.';
    deepEq(domainFab('Python Software Engineer with deep expertise in financial services IT systems.', cvNoFin).sort(),
      ['financial', 'financial services'],
      'an industry the CV never claims is caught, in both the bare and qualified form');
    // The bare forms exist because the qualifier was doing the work: "clinical
    // trials" did not catch "building and improving clinical AI agents".
    deepEq(domainFab('Builds clinical AI agents.', cvNoFin), ['clinical'],
      'a domain named without its usual qualifier is still a domain');
    deepEq(domainFab('Engineer who builds retail operations tooling.', 'Designed a system for retail operations'),
      [], 'a domain the CV does state is not flagged');
    // The 3-char floor in stripJdProperNouns cannot see "Go"; case is what
    // separates the language from the verb.
    deepEq(casedFab('Delivers production systems in Go and Rust.', cvNoFin), ['Go'],
      'a two-letter language name is caught even though the proper-noun guard skips it');
    deepEq(casedFab('Ready to go live with the platform.', cvNoFin), [],
      'the lowercase verb is not the language');
    deepEq(casedFab('Builds systems in Rust.', cvNoFin), [],
      'a cased name the CV does claim is not flagged');

    // The gate and the metric are one function, so a class the metric grew
    // cannot be one the gate ignores.
    const { summaryFab } = await import(pathToFileURL(join(ROOT, 'batch/tailor-harness.mjs')).href);
    eq(summaryFab === summaryUnsupported, true,
      'the harness metric IS the generation gate — no second detector to drift');
    eq(summaryUnsupported('Engineer in financial services delivering systems in Go.', cvNoFin).sort().join('+'),
      'cased_product+domain', 'both new classes are named');
    deepEq(summaryUnsupported('', cvNoFin), [], 'an empty summary claims nothing');

    // Figure attribution — a real figure handed to the wrong entry. Every case
    // is from the 32-offer bench, and every one is reported clean by
    // summaryUnsupported, because each number genuinely appears in cv.md.
    const { figureAttribution, namingPhrases, cvEntries } = await import(pathToFileURL(join(ROOT, 'batch/summary-stage.mjs')).href);
    const attribCv = [
      '## Experience', '',
      '### PM / Software Engineer',
      '**UBWIS** — Edinburgh | Oct 2024 – Sep 2025', '',
      '- Built the admin console backed by Redis caching and 85%+ test coverage',
      '- Owned production security, maintaining 99.9% average uptime', '',
      '## Projects', '',
      '### Re:Link — Privacy-Preserving Peer-to-Peer Remote Access System',
      '**Honours Dissertation** | Rust, Flutter, WebRTC | 2025 – 2026', '',
      '- Designed a blind rendezvous protocol with end-to-end encryption', '',
      '### Post-Quantum Signature Benchmarking',
      '**Personal research** | Rust, Python | 2025 – 2026', '',
      '- Executed 63,000+ benchmark runs across 7 signature schemes', '',
    ].join('\n');

    // The shipped Sophos defect: both figures real, both UBWIS's, sentence names Re:Link.
    deepEq(figureAttribution(
      'Delivered a privacy-preserving peer-to-peer system with 85%+ test coverage and maintained 99.9% uptime.',
      attribCv).map(x => x.figure).sort(), ['85%+', '99.9%'],
      'figures welded onto an entry that does not own them are caught');

    // A clause that names no entry asserts no owner, so it cannot have one wrong.
    // Flagging it would measure vagueness, not falsity.
    deepEq(figureAttribution('Achieved 99.9% uptime by resolving critical SSL/DNS failures.', attribCv), [],
      'a figure with no entity named is not an attribution error');
    deepEq(figureAttribution('Executed 63,000+ benchmark runs across 7 signature schemes.', attribCv), [],
      'a correctly attributed figure is clean');

    // Clause granularity, not sentence: two entries reported in one sentence,
    // each correctly, must not read as one claiming the other's figure.
    deepEq(figureAttribution(
      'Achieved sub-500ms load times through cache warming, and executed 63,000+ benchmark runs in the post-quantum signature testbed.',
      attribCv), [],
      'two correctly-attributed clauses in one sentence stay clean');

    // A figure absent from the CV is a fabrication, not a misattribution —
    // summaryUnsupported owns that, and double-charging it would inflate both.
    deepEq(figureAttribution('Shipped a privacy-preserving peer-to-peer system serving 970%+ growth.', attribCv), [],
      'an invented figure is summaryUnsupported\'s job, not this metric\'s');

    // Naming is read off entry heads, never bullets. Built over bullet text, any
    // summary reusing a bullet's wording names its entry — which is most of them.
    const ph = namingPhrases(cvEntries(attribCv));
    eq(ph.get('privacy-preserving peer-to-peer'), 'Re:Link — Privacy-Preserving Peer-to-Peer Remote Access System',
      'a title phrase identifies its entry');
    eq(ph.has('admin console'), false, 'a bullet phrase does not');
    deepEq(figureAttribution('Anything at all with 85%+ coverage.', 'no entries here'), [],
      'a CV with under two entries yields no attribution rather than throwing');

    // Shape is priced into the score, or a well-overlapping list beats a
    // well-shaped summary. Both candidates carry the same tail, so evidence
    // overlap is near-identical and the shape term is what separates them — and
    // the posed one is a word *longer*, so it pays more length penalty and still
    // wins.
    const tail = 'Delivered technical instruction to over 800 students across a range of '
      + 'courses and formats, and built a membership platform with bi-weekly releases '
      + 'for the teams that depended on it.';
    const opts = { bullets: [`Teaching: ${tail}`], cvText: tail };
    eq(scoreSummary(`Backend engineer who delivers teaching and platforms. ${tail}`, opts)
       > scoreSummary(`Delivered teaching and platforms as a backend engineer. ${tail}`, opts), true,
      'a summary with a positioning clause beats the same content as a bullet list');
  }

  // A tenure the CV never states. verifyBulletNumbers cannot catch this: "2+"
  // also occurs as "2+ hours" in an unrelated bullet, so the token is allowed —
  // the claim is what is invented, not the digit.
  const { stripUnsupportedTenure } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const tenureCv = cv + '\n- Cut configuration time from 2+ hours to 30 minutes';
  eq(stripUnsupportedTenure('Engineer with 2+ years of hands-on experience in Rust.', tenureCv),
    'Engineer with experience in Rust.',
    'an unsupported tenure claim is stripped without mangling the sentence');
  // A *range* — the shape the original pattern missed entirely, which is how
  // "1-3 years of real production experience" (lifted from the posting) shipped.
  eq(stripUnsupportedTenure('Engineer with 1-3 years of real production experience in Rust.', tenureCv),
    'Engineer with experience in Rust.',
    'a tenure range is stripped, not just a single value');
  eq(stripUnsupportedTenure('Engineer with 2 to 4 years of experience in Rust.', tenureCv),
    'Engineer with experience in Rust.',
    'a written "N to M years" range is stripped too');
  eq(stripUnsupportedTenure('Engineer with 5 years in Rust.', tenureCv),
    'Engineer with experience in Rust.',
    'the bare "with N years" form is stripped too');
  eq(stripUnsupportedTenure('ML-DSA adds ~200 TB over 5 years for a log pipeline.', tenureCv),
    'ML-DSA adds ~200 TB over 5 years for a log pipeline.',
    'a duration in a projection is not a tenure claim and survives');
  eq(stripUnsupportedTenure('Engineer with 6 years of experience.', 'Engineer with 6 years of experience shipping things.'),
    'Engineer with 6 years of experience.',
    'a tenure the CV does state is left alone');
  eq(stripUnsupportedTenure(/** @type {any} */ (null), tenureCv), null,
    'a non-string summary is returned untouched');
} catch (e) {
  fail(`experience reconciliation unit tests crashed: ${e.message}`);
}

// ── 22. UNIT — Phase 3 bullet-number verification ────────────────────
// The prompt-side fix for invented figures measured zero effect across 24
// offers (ledger V4), so this is the repair. Same shape as fit-rules'
// verifyAgainstCv, which fixed the equivalent Phase 1 surface.
try {
  const { verifyBulletNumbers } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const cv = [
    '## Experience', '',
    '### PM / Software Engineer',
    '**Acme SaaS** — Edinburgh | Oct 2024 – Sep 2025', '',
    '- Led a two-developer team building a membership platform: MVP in 4 weeks, grew paying subscribers from 80 at launch to 170',
    '- Automated billing with Stripe, cutting onboarding time by over 80%',
  ].join('\n');
  const bullets = bs => verifyBulletNumbers([{ company: 'Acme SaaS', bullets: bs }], cv)[0].bullets;

  eq(bullets(['Led delivery serving 100+ subscribers, MVP in 4 weeks.'])[0].includes('80 at launch to 170'), true,
    'a bullet inventing 100+ reverts to the CV bullet it came from');
  eq(bullets(['Grew subscribers to 170+ across 5 locations.'])[0].includes('to 170'), true,
    'appending a plus to a real figure counts as fabrication and reverts');
  deepEq(bullets(['Cut onboarding time by over 80% with Stripe.']), ['Cut onboarding time by over 80% with Stripe.'],
    'a rewrite whose figures are all in the CV keeps its tailoring');
  deepEq(bullets(['Led a team with no figures at all.']), ['Led a team with no figures at all.'],
    'a bullet with no numbers is untouched');
  deepEq(bullets(['Shipped in 4 weeks.', 'Grew to 170 members.']),
    ['Shipped in 4 weeks.', 'Grew to 170 members.'],
    'multiple clean bullets all survive');

  // Two bad bullets can revert onto the same CV line; the CV must not repeat.
  const collided = bullets(['Served 100+ users on the platform.', 'Reached 200+ users on the platform.']);
  eq(collided.length, new Set(collided).size, 'reverting two bullets onto one CV line does not duplicate it');

  // Degenerate inputs
  deepEq(verifyBulletNumbers([{ company: 'Nowhere Ltd', bullets: ['Invented 999+ things.'] }], cv)[0].bullets,
    ['Invented 999+ things.'],
    'an employer absent from the CV has no source to revert to and is left alone');
  eq(Array.isArray(verifyBulletNumbers(/** @type {any} */ (null), cv)), false,
    'a non-array is returned untouched');

  // The mirror guard: figures the rewrite DELETED. Shares the same revert, so
  // it inherits the source-matching and the collision handling above.
  const { verifyBulletFigures } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const figs = bs => verifyBulletFigures([{ company: 'Acme SaaS', bullets: bs }], cv)[0].bullets;

  eq(figs(['Automated billing with Stripe.'])[0].includes('80%'), true,
    'a rewrite truncated past its figure reverts to the CV line that stated it');
  deepEq(figs(['Cut onboarding by over 80% with Stripe subscriptions.']),
    ['Cut onboarding by over 80% with Stripe subscriptions.'],
    'a rewrite keeping every source figure keeps its tailoring');
  eq(figs(['Led a team, MVP in 4 weeks.'])[0].includes('80 at launch to 170'), true,
    'keeping one figure but dropping the rest still reverts');
  deepEq(verifyBulletFigures([{ company: 'Nowhere Ltd', bullets: ['Anything at all.'] }], cv)[0].bullets,
    ['Anything at all.'],
    'no CV source means nothing to compare against, so the bullet is left alone');

  // Cross-entry theft: a figure that is real, but belongs to a different entry.
  // The allow-set is the employer's own entry, not the whole document.
  const twoJobs = [
    '## Experience', '',
    '### Teaching Assistant', '**Northgate College** — Edinburgh | Sep 2025 – Present', '',
    '- Cut configuration time to 30 minutes per student', '',
    '### PM / Software Engineer', '**Acme SaaS** — Edinburgh | Oct 2024 – Sep 2025', '',
    '- Grew paying subscribers from 80 at launch to 170',
  ].join('\n');
  const stolen = verifyBulletNumbers(
    [{ company: 'Acme SaaS', bullets: ['Cut configuration time to 30 minutes for 170 members.'] }], twoJobs);
  eq(stolen[0].bullets[0].includes('80 at launch'), true,
    'a figure belonging to another employer is unsupported here and reverts');

  // ── project figures, scoped to the project's own CV entry ──
  const { verifyProjectFigures } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
  const projCv = [
    '## Projects', '',
    '### Re:Link — Remote Access', '**Personal** | Rust, Flutter | Jan 2025 – Jun 2025', '',
    '- Designed a blind rendezvous protocol with AES-256-GCM encryption', '',
    '### Zero Trust Dashboard', '**Academic** | Django | Sep 2024 – Dec 2024', '',
    '- Built an ingestion pipeline achieving sub-500ms dashboard load times',
  ].join('\n');
  const descOf = (name, description) =>
    verifyProjectFigures([{ name, description }], projCv)[0].description;

  eq(descOf('Re:Link — Remote Access',
    'Built a P2P remote access system with AES-256-GCM, serving 970%+ revenue growth for a client.')
    .includes('970'), false,
    'a figure absent from the whole CV is stripped from a project blurb');
  eq(descOf('Re:Link — Remote Access',
    'Built a P2P remote access system with AES-256-GCM, achieving sub-500ms load times.')
    .includes('500'), false,
    "another project's real figure is stripped — the number is real, the owner is not");
  eq(descOf('Zero Trust Dashboard', 'Built an ingestion pipeline achieving sub-500ms load times.'),
    'Built an ingestion pipeline achieving sub-500ms load times.',
    'the project that actually owns the figure keeps it');
  eq(descOf('Re:Link — Remote Access', 'Built a P2P remote access system with AES-256-GCM.')
    .includes('256'), true,
    'a figure stated in the project\'s own entry survives');
  eq(descOf('Nothing On The CV', 'Claims 999% of everything.').includes('999'), true,
    'a project that resolves to no CV entry is left alone rather than gutted');
  deepEq(verifyProjectFigures(/** @type {any} */ (null), projCv), null,
    'a non-array is returned untouched');
} catch (e) {
  fail(`bullet-number verification unit tests crashed: ${e.message}`);
}

// ── 23. UNIT — Phase 3 harness sample selection and scoring ──────────
// metricsFor decides whether a benchmark says a change worked, so it is scored
// here against a fixture tree with known answers rather than trusted.
try {
  const { mkdtempSync, mkdirSync, writeFileSync, rmSync } = await import('fs');
  const { tmpdir } = await import('os');
  const h = await import(pathToFileURL(join(ROOT, 'batch/tailor-harness.mjs')).href);
  const root = mkdtempSync(join(tmpdir(), 'snipe-bench-'));
  try {
    // Fixture CV: two employers, four bullets, figures 800+, 30, 170, 80%.
    const cvPath = join(root, 'cv.md');
    writeFileSync(cvPath, [
      '## Experience', '',
      '### Teaching Assistant', '**Northgate College** - Edinburgh | Sep 2025 - Present', '',
      '- Taught programming to 800+ undergraduates across two languages',
      '- Wrote setup guides cutting configuration time to 30 minutes', '',
      '### PM / Software Engineer', '**Acme SaaS** - Edinburgh | Oct 2024 - Sep 2025', '',
      '- Led a team building a subscription platform, MVP in 4 weeks, grew to 170 members',
      '- Automated billing, cutting onboarding time by over 80%',
    ].join('\n'), 'utf8');

    const benchRoot = join(root, 'bench');
    const write = (label, dir, experience, projects) => {
      const d = join(benchRoot, label, dir);
      mkdirSync(d, { recursive: true });
      writeFileSync(join(d, 'cv-content.json'), JSON.stringify({ experience, projects }), 'utf8');
    };

    // Perfect run: both employers, CV figures only.
    write('good', 'a', [
      { company: 'Northgate College', bullets: ['Taught 800+ undergraduates across two languages'] },
      { company: 'Acme SaaS', bullets: ['Grew the platform to 170 members in 4 weeks'] },
    ]);
    const good = h.metricsFor('good', { benchRoot, cvPath });
    eq(good.n, 1, 'metricsFor counts one offer per cv-content.json');
    eq(good.role_retention, 1, 'both real employers score full retention');
    eq(good.all_roles_pct, 1, 'an offer keeping every role counts toward all_roles_pct');
    eq(good.invented_roles, 0, 'no entry is unmatched when both name real employers');
    eq(good.metric_fab, 0, 'figures present in the CV are not counted as fabricated');
    eq(good.num_retention, 1, 'a rewrite carrying every source figure retains them all');
    eq(good.num_lost, 0, 'nothing is reported lost when nothing was dropped');

    // Truncation: each bullet keeps its first clause and drops the outcome. Every
    // other metric reads clean, which is exactly why this one had to exist.
    write('truncated', 'a', [
      { company: 'Northgate College', bullets: ['Wrote setup guides'] },
      { company: 'Acme SaaS', bullets: ['Automated billing'] },
    ]);
    const trunc = h.metricsFor('truncated', { benchRoot, cvPath });
    eq(trunc.role_retention, 1, 'truncation keeps both roles, so role_retention misses it');
    eq(trunc.metric_fab, 0, 'truncation invents no figures, so metric_fab misses it too');
    eq(trunc.num_retention, 0, 'dropping 30 minutes and 80% scores zero retention');
    eq(trunc.num_lost, 2, 'both deleted figures are counted');

    // Degenerate run: one role dropped, one employer duplicated, invented figure.
    write('bad', 'a', [
      { company: 'Acme SaaS', bullets: ['Served 100+ members, shipping in 4 weeks'] },
      { company: 'Acme SaaS', bullets: ['Automated billing by 80%'] },
    ]);
    const bad = h.metricsFor('bad', { benchRoot, cvPath });
    eq(bad.role_retention, 0.5, 'a duplicated employer counts once, so retention is halved');
    eq(bad.all_roles_pct, 0, 'an offer missing a role scores zero on all_roles_pct');
    eq(bad.invented_roles, 1, 'the duplicate entry is reported as unmatched, not as a role');
    eq(bad.metric_fab, 1, 'a figure absent from the CV is counted once');
    eq(bad.fab_offers_pct, 1, 'the offer is flagged as carrying a fabrication');

    // An entry naming no real employer at all.
    write('invented', 'a', [
      { company: 'Northgate College', bullets: ['Taught 800+ undergraduates'] },
      { company: 'Acme SaaS', bullets: ['Grew to 170 members'] },
      { company: 'Nowhere Ltd', bullets: ['Co-founded a bakery'] },
    ]);
    const inv = h.metricsFor('invented', { benchRoot, cvPath });
    eq(inv.role_retention, 1, 'a spurious extra entry does not reduce retention of the real roles');
    eq(inv.invented_roles, 1, 'an employer absent from the CV is reported as invented');

    // Section balance. The defect this exists for: a page spent almost entirely
    // on projects, with both employers cut to a single line, which reads as no
    // experience whatever the projects say. Every bullet below is verbatim cv.md,
    // so the falsity metrics are all perfect — that is the point of the fixture,
    // not a detail of it (standing rule 9, asked of sections instead of an empty
    // output).
    const proj9 = (n) => [{ name: 'a project', bullets: Array.from({ length: n }, (_, i) => `built part ${i}`) }];
    write('starved', 'a', [
      { company: 'Northgate College', bullets: ['Taught programming to 800+ undergraduates across two languages'] },
      { company: 'Acme SaaS', bullets: ['Led a team building a subscription platform, MVP in 4 weeks, grew to 170 members'] },
    ], proj9(9));
    const starved = h.metricsFor('starved', { benchRoot, cvPath });
    eq(starved.grounding, 1, 'the starved page is perfectly grounded — every other metric misses it');
    eq(starved.metric_fab, 0, 'and invents no figure, which is why nothing caught this before');
    eq(starved.num_retention, 1, 'and loses no figure either');
    eq(starved.section_balance, 0.182, 'two of eleven bullets carry the whole Experience section');
    eq(starved.exp_starved, 2, 'both employers are at the one-bullet floor');
    eq(starved.all_exp_starved_pct, 1, 'and the offer counts toward every-employer-starved');

    // The healthy shape, same total page.
    write('balanced', 'a', [
      { company: 'Northgate College', bullets: ['Taught programming to 800+ undergraduates across two languages',
                                                'Wrote setup guides cutting configuration time to 30 minutes'] },
      { company: 'Acme SaaS', bullets: ['Led a team building a subscription platform, MVP in 4 weeks, grew to 170 members',
                                        'Automated billing, cutting onboarding time by over 80%'] },
    ], proj9(7));
    const bal = h.metricsFor('balanced', { benchRoot, cvPath });
    eq(bal.section_balance, 0.364, 'the same eleven bullets, four of them experience');
    eq(bal.exp_starved, 0, 'no employer is at the floor');
    eq(bal.all_exp_starved_pct, 0, 'so the offer does not count as starved');

    // A CV with no Experience section at all must not read as the healthy end of
    // the scale. `all_exp_starved` is null there, not 0, so it drops out of the
    // mean rather than reporting that no entry was starved.
    write('noexp', 'a', [], proj9(4));
    const noexp = h.metricsFor('noexp', { benchRoot, cvPath });
    eq(noexp.all_exp_starved_pct, null, 'no experience entries is not the same as none starved');
    eq(noexp.section_balance, 0, 'and the balance reads zero, which is the bad direction');
    eq(noexp.role_retention, 0, 'role_retention is the metric that owns the vanished-section case');

    // Sample selection: only offers with an eval, a report and a cached JD.
    const batchDir = join(root, 'batch');
    const reportsDir = join(root, 'reports');
    mkdirSync(join(batchDir, 'jds'), { recursive: true });
    mkdirSync(join(batchDir, 'evals'), { recursive: true });
    mkdirSync(reportsDir, { recursive: true });
    const stateFile = join(batchDir, 'local-state.tsv');
    writeFileSync(stateFile, [
      'id\turl\tp1_status\tp1_score\tp1_archetype\tp2_status\tp2_report_num\tp3_status\terror\tretries',
      '1\thttps://x/1\tscored\t3\tBackend\tevaled\t001\tcompleted\t-\t0',   // complete
      '2\thttps://x/2\tscored\t3\tBackend\tevaled\t002\tcompleted\t-\t0',   // report missing
      '3\thttps://x/3\tscored\t3\tBackend\tskipped\t-\t-\t-\t0',            // never evaled
    ].join('\n'), 'utf8');
    writeFileSync(join(reportsDir, '001-acme-2026-01-01.md'), '# report', 'utf8');
    writeFileSync(join(batchDir, 'jds/1.txt'), 'a jd', 'utf8');
    writeFileSync(join(batchDir, 'evals/1.json'),
      JSON.stringify({ company: 'Acme', role: 'Engineer', score: 4.2 }), 'utf8');
    writeFileSync(join(batchDir, 'jds/2.txt'), 'a jd', 'utf8');
    writeFileSync(join(batchDir, 'evals/2.json'),
      JSON.stringify({ company: 'Beta', role: 'Engineer', score: 3.1 }), 'utf8');

    const elig = h.eligible({ stateFile, reportsDir, batchDir });
    eq(elig.length, 1, 'only the offer with an eval, a report and a JD is eligible');
    eq(elig[0].id, '1', 'the eligible offer is the complete one');
    eq(elig[0].score, 4.2, 'the eval score is carried through for stratification');
    deepEq(h.eligible({ stateFile: join(root, 'nope.tsv'), reportsDir, batchDir }), [],
      'a missing state file yields no offers rather than throwing');
  } finally {
    rmSync(root, { force: true, recursive: true });
  }
} catch (e) {
  fail(`tailor-harness sample/metrics unit tests crashed: ${e.message}`);
}
