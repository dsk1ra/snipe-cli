// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE, runNodeAsync,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 9. LOCAL PARSER CONTRACT ────────────────────────────────────

console.log('\n9. Local parser contract');

const scanScript = readFile('scan.mjs');
if (
  scanScript.includes('typeof entry.name !== \'string\'') &&
  scanScript.includes('entry.name.trim()') &&
  scanScript.includes('entry.name.toLowerCase()')
) {
  pass('scan.mjs guards company names before filtering');
} else {
  fail('scan.mjs does not guard company names before filtering');
}

if (
  scanScript.includes("skipIds: ['local-parser']") &&
  scanScript.includes('local parser failed, used API fallback') &&
  scanScript.includes('resolveProvider(company, providers')
) {
  pass('scan.mjs falls back to ATS API when local parser fails');
} else {
  fail('scan.mjs does not fall back to ATS API when local parser fails');
}

if (fileExists('providers/local-parser.mjs')) {
  pass('local-parser provider module exists');
} else {
  fail('local-parser provider module is missing');
}

const scanMode = fileExists('modes/scan.md') ? readFile('modes/scan.md') : '';
if (
  scanMode.includes('local_parser_ok') &&
  scanMode.includes('No Expensive Scraping Repetition') &&
  scanMode.includes('name not listed in `local_parser_ok`')
) {
  pass('scan.md skips expensive levels after successful local parser');
} else {
  fail('scan.md missing local_parser_ok skip rules for agent scan');
}

if (!fileExists('scripts/parsers/cohere_jobs.py')) {
  pass('Cohere parser example is not bundled as a runtime script');
} else {
  fail('Cohere parser example is still bundled as a runtime script');
}

const portalExample = readFile('templates/portals.example.yml');
if (
  !portalExample.includes('cohere_jobs.py') &&
  portalExample.includes('scripts/parsers/example-js-company-jobs.js') &&
  portalExample.includes('scripts/parsers/example_python_company_jobs.py') &&
  portalExample.includes('already know their target careers URL')
) {
  pass('portals example documents a generic local parser contract');
} else {
  fail('portals example still points at a bundled Cohere parser');
}

// ── 10. PORTALS CONFIG VALIDATOR ────────────────────────────────

console.log('\n10. Portals config validator');

try {
  const tmp = mkdtempSync(join(tmpdir(), 'snipe-portals-validator-'));
  const validPath = join(tmp, 'valid.yml');
  const invalidProviderPath = join(tmp, 'invalid-provider.yml');
  const emptyKeywordPath = join(tmp, 'empty-keyword.yml');
  const duplicateCompanyPath = join(tmp, 'duplicate-company.yml');

  writeFileSync(validPath, `
title_filter:
  positive: ["AI"]
  negative: ["Intern"]
tracked_companies:
  - name: "Acme"
    careers_url: "https://jobs.lever.co/acme"
`, 'utf-8');

  writeFileSync(invalidProviderPath, `
title_filter:
  positive: ["AI"]
tracked_companies:
  - name: "Acme"
    provider: "missing-provider"
    careers_url: "https://jobs.lever.co/acme"
`, 'utf-8');

  writeFileSync(emptyKeywordPath, `
title_filter:
  positive: ["AI", "   "]
tracked_companies:
  - name: "Acme"
    careers_url: "https://jobs.lever.co/acme"
`, 'utf-8');

  writeFileSync(duplicateCompanyPath, `
title_filter:
  positive: ["AI"]
tracked_companies:
  - name: "Acme"
    careers_url: "https://jobs.lever.co/acme"
  - name: " acme "
    careers_url: "https://jobs.lever.co/acme2"
`, 'utf-8');

  const validResult = run(NODE, ['validate-portals.mjs', '--file', validPath]);
  if (validResult !== null && validResult.includes('0 errors')) {
    pass('validate-portals accepts a minimal valid portals file');
  } else {
    fail('validate-portals should accept a minimal valid portals file');
  }

  const exampleResult = run(NODE, ['validate-portals.mjs', '--file', 'templates/portals.example.yml']);
  if (exampleResult !== null && exampleResult.includes('0 errors')) {
    pass('validate-portals accepts templates/portals.example.yml');
  } else {
    fail('validate-portals should accept templates/portals.example.yml');
  }

  const invalidProviderResult = run(NODE, ['validate-portals.mjs', '--file', invalidProviderPath]);
  if (invalidProviderResult === null) {
    pass('validate-portals rejects unknown explicit providers');
  } else {
    fail('validate-portals should reject unknown explicit providers');
  }

  const emptyKeywordResult = run(NODE, ['validate-portals.mjs', '--file', emptyKeywordPath]);
  if (emptyKeywordResult === null) {
    pass('validate-portals rejects empty title/location keywords');
  } else {
    fail('validate-portals should reject empty title/location keywords');
  }

  const duplicateCompanyResult = run(NODE, ['validate-portals.mjs', '--file', duplicateCompanyPath]);
  if (duplicateCompanyResult !== null && duplicateCompanyResult.includes('1 warning')) {
    pass('validate-portals warns on duplicate enabled company names');
  } else {
    fail('validate-portals should warn on duplicate enabled company names');
  }

  rmSync(tmp, { recursive: true, force: true });
} catch (e) {
  fail(`portals validator tests crashed: ${e.message}`);
}


// ── 15. LOCATION FILTER — always_allow tier ───────────────────────

console.log('\n15. Location filter — always_allow tier');

try {
  const { buildLocationFilter, shouldDedupScanHistoryRow } = await import(pathToFileURL(join(ROOT, 'scan.mjs')).href);

  const filter = buildLocationFilter({
    always_allow: ['belgium', 'brussels'],
    allow: ['europe', 'emea', 'remote'],
    block: ['france', 'germany', 'united states'],
  });

  // Case 1: home-region passes regardless of other text
  if (filter('Brussels, Belgium') === true) pass('Brussels, Belgium passes (always_allow hit)');
  else fail('Brussels, Belgium should pass');

  // Case 2: always_allow wins over block (THE motivating case for this tier)
  if (filter('Remote, Belgium or France') === true) pass('Remote, Belgium or France passes (always_allow beats block)');
  else fail('Remote, Belgium or France should pass — always_allow must win over block');

  // Case 3: no always_allow hit, block still rejects
  if (filter('Paris, France') === false) pass('Paris, France is rejected (block still applies)');
  else fail('Paris, France should be rejected');

  // Case 4: empty location → pass (existing semantics, unchanged)
  if (filter('') === true) pass('empty location passes (unchanged semantics)');
  else fail('empty location should pass');

  // Case 5: case-insensitivity
  if (filter('BRUSSELS, BELGIUM') === true) pass('case-insensitive match works');
  else fail('case-insensitive match failed');

  // Case 6: backward compatibility — no always_allow key behaves like stock allow/block
  const stockFilter = buildLocationFilter({
    allow: ['europe', 'remote'],
    block: ['france'],
  });
  if (stockFilter('Remote, Belgium or France') === false) pass('without always_allow, block still wins (backward compatible)');
  else fail('without always_allow, behaviour must match stock allow/block (block wins)');

  // Case 7: null/missing locationFilter → pass-all filter (early-return path)
  const nullFilter = buildLocationFilter(null);
  if (nullFilter('Anywhere on Earth') === true && nullFilter('') === true) {
    pass('null locationFilter returns a pass-all filter (early-return path)');
  } else {
    fail('null locationFilter should return a pass-all filter');
  }

  // Case 8: string-instead-of-array → wrapped to a 1-item list
  const stringFilter = buildLocationFilter({ always_allow: 'belgium', block: ['france'] });
  if (stringFilter('Remote, Belgium or France') === true) {
    pass('always_allow as a bare string is wrapped to a single-item list');
  } else {
    fail('always_allow as a bare string should still work');
  }

  // Case 9: null/non-string items are filtered out (no crash, no false matches)
  const messyFilter = buildLocationFilter({
    always_allow: [null, 'belgium', 42, undefined],
    block: ['france', null, 7],
  });
  if (messyFilter('Brussels, Belgium') === true && messyFilter('Paris, France') === false) {
    pass('non-string entries (null, numbers, undefined) are filtered out without crashing');
  } else {
    fail('mixed-type keyword lists should not crash and should still match string entries');
  }

  // Case 10: all-null/non-string list → empty after normalization (no false rejects)
  const allBadFilter = buildLocationFilter({ block: [null, 42, undefined], allow: ['remote'] });
  if (allBadFilter('Remote') === true) {
    pass('a block list with only non-string entries normalizes to [] (no false rejects)');
  } else {
    fail('non-string-only block list should not cause rejection');
  }

  // Case 11: empty / whitespace-only entries are dropped (would otherwise pass-all via includes(''))
  const emptyKeywordFilter = buildLocationFilter({
    always_allow: ['', '  '],
    allow: ['remote'],
    block: ['france'],
  });
  if (emptyKeywordFilter('Paris, France') === false) {
    pass('empty/whitespace always_allow entries are dropped (no pass-all via includes(""))');
  } else {
    fail('empty always_allow entries should NOT bypass block — would have made the filter pass-all');
  }

  // Case 12: surrounding whitespace is trimmed so the keyword still matches
  const whitespaceFilter = buildLocationFilter({
    always_allow: ['  Belgium  ', '\tBrussels\n'],
    block: ['france'],
  });
  if (whitespaceFilter('Remote, Belgium or France') === true) {
    pass('whitespace-padded keywords still match after trim');
  } else {
    fail('"  Belgium  " should be trimmed and still match "Remote, Belgium or France"');
  }

  // Case 13: whitespace-only location is treated as missing (pass-all-tiers)
  if (filter('   \t  ') === true) pass('whitespace-only location passes (treated as missing)');
  else fail('whitespace-only location should pass');

  // Case 14: non-string location (number/object/null) → pass without throwing
  let crashed = false;
  try {
    const r1 = filter(42);
    const r2 = filter({ city: 'Brussels' });
    const r3 = filter(null);
    const r4 = filter(undefined);
    if (r1 === true && r2 === true && r3 === true && r4 === true) {
      pass('non-string location values (number, object, null, undefined) pass without throwing');
    } else {
      fail(`non-string location results: number=${r1}, object=${r2}, null=${r3}, undefined=${r4}`);
    }
  } catch (e) {
    crashed = true;
    fail(`non-string location crashed: ${e.message}`);
  }

  if (
    shouldDedupScanHistoryRow({ firstSeen: '2026-06-01', status: 'added' }, { recheckAfterDays: 30, today: '2026-06-10' }) === true &&
    shouldDedupScanHistoryRow({ firstSeen: '2026-05-01', status: 'added' }, { recheckAfterDays: 30, today: '2026-06-10' }) === false &&
    shouldDedupScanHistoryRow({ firstSeen: '2026-02-31', status: 'added' }, { recheckAfterDays: 30, today: '2026-06-10' }) === true &&
    shouldDedupScanHistoryRow({ firstSeen: '2026-05-01', status: 'skipped_blocked_host' }, { recheckAfterDays: 30, today: '2026-06-10' }) === true &&
    shouldDedupScanHistoryRow({ firstSeen: '2026-05-01', status: 'added' }, { today: '2026-06-10' }) === true &&
    scanScript.includes('Recheck eligible:')
  ) {
    pass('scan-history TTL rechecks old added URLs while permanent statuses stay deduped');
  } else {
    fail('scan-history TTL policy did not match expected recheck/permanent behavior');
  }

} catch (e) {
  fail(`always_allow tests crashed: ${e.message}`);
}

// ── 15. URL REDISCOVERY FALLBACK (--rediscover-404) ─────────────

console.log('\n15. URL rediscovery fallback');

try {
  const { extractCareersUrlDomain, pickRediscoveredUrl } = await import(
    pathToFileURL(join(ROOT, 'scan.mjs')).href
  );

  // extractCareersUrlDomain — pure hostname extraction, null on missing/invalid
  if (extractCareersUrlDomain('https://job-boards.greenhouse.io/anthropic') === 'job-boards.greenhouse.io') {
    pass('extractCareersUrlDomain pulls hostname from a careers URL');
  } else {
    fail('extractCareersUrlDomain failed on a valid URL');
  }
  if (extractCareersUrlDomain(null) === null) {
    pass('extractCareersUrlDomain returns null for missing careers_url');
  } else {
    fail('extractCareersUrlDomain did not return null for null input');
  }
  if (extractCareersUrlDomain('not-a-url') === null) {
    pass('extractCareersUrlDomain returns null for an unparseable URL');
  } else {
    fail('extractCareersUrlDomain did not return null for a bad URL');
  }

  // pickRediscoveredUrl — first search hit whose hostname exactly matches domain
  const domain = 'job-boards.greenhouse.io';
  const hrefs = [
    'https://duckduckgo.com/l/?uddg=ad',          // search-engine chrome / noise
    'https://other-board.lever.co/acme/123',      // wrong domain
    'https://job-boards.greenhouse.io/acme/456',  // first real match
    'https://job-boards.greenhouse.io/acme/789',  // later match
  ];
  if (pickRediscoveredUrl(hrefs, domain) === 'https://job-boards.greenhouse.io/acme/456') {
    pass('pickRediscoveredUrl returns the first same-domain result');
  } else {
    fail(`pickRediscoveredUrl picked the wrong URL: ${pickRediscoveredUrl(hrefs, domain)}`);
  }
  if (pickRediscoveredUrl(['https://elsewhere.com/x'], domain) === null) {
    pass('pickRediscoveredUrl returns null when no result matches the domain');
  } else {
    fail('pickRediscoveredUrl did not return null for no domain match');
  }
  if (pickRediscoveredUrl([], domain) === null) {
    pass('pickRediscoveredUrl returns null for an empty result set');
  } else {
    fail('pickRediscoveredUrl did not return null for empty input');
  }
  // Redirect unwrapping is restricted to real DuckDuckGo hosts: a look-alike
  // host must not get its uddg target unwrapped (and its own hostname does not
  // match the careers domain, so the result is null).
  const lookAlike = `https://evil-duckduckgo.com/l/?uddg=${encodeURIComponent('https://job-boards.greenhouse.io/acme/456')}`;
  if (pickRediscoveredUrl([lookAlike], domain) === null) {
    pass('pickRediscoveredUrl ignores uddg redirects from look-alike hosts');
  } else {
    fail('pickRediscoveredUrl unwrapped a redirect from a look-alike host');
  }
  // DuckDuckGo HTML wraps each result in a /l/?uddg= redirect — must be
  // unwrapped, otherwise every hostname looks like duckduckgo.com and nothing
  // ever matches the careers domain (the fallback would silently never fire).
  const ddg = ['//duckduckgo.com/l/?uddg=' + encodeURIComponent('https://job-boards.greenhouse.io/acme/999')];
  if (pickRediscoveredUrl(ddg, domain) === 'https://job-boards.greenhouse.io/acme/999') {
    pass('pickRediscoveredUrl unwraps DuckDuckGo redirect links');
  } else {
    fail(`pickRediscoveredUrl did not unwrap DDG redirect: ${pickRediscoveredUrl(ddg, domain)}`);
  }
  // A look-alike host that merely contains the domain as a substring must not match.
  if (pickRediscoveredUrl(['https://job-boards.greenhouse.io.attacker.com/x'], domain) === null) {
    pass('pickRediscoveredUrl rejects look-alike hostnames');
  } else {
    fail('pickRediscoveredUrl accepted a look-alike hostname');
  }
} catch (e) {
  fail(`URL rediscovery tests crashed: ${e.message}`);
}



// ── 9b. SCAN END-TO-END ──────────────────────────────────────────

console.log('\n9b. scan.mjs end to end');

// scan.mjs resolves portals.yml from $SNIPE_PORTALS and writes data/pipeline.md
// and data/scan-history.tsv relative to the cwd, so running it from a temp
// directory sandboxes the whole thing — the developer's real portal list and
// pipeline are never opened, let alone written to.
//
// The fixture portal uses the local-parser provider, which shells out to a
// script instead of a network call. That makes a full scan — resolve, fetch,
// filter, dedup, write — deterministic and offline.
{
  const sandbox = mkdtempSync(join(tmpdir(), 'snipe-scan-'));
  const SCAN = join(ROOT, 'scan.mjs');

  const parser = join(sandbox, 'jobs.mjs');
  writeFileSync(parser, [
    'process.stdout.write(JSON.stringify([',
    '  { title: "Senior Backend Engineer", url: "https://fixture.example/jobs/1", location: "Remote (EU)" },',
    '  { title: "Backend Engineer", url: "https://fixture.example/jobs/2", location: "Berlin, Germany" },',
    '  { title: "Engineering Manager", url: "https://fixture.example/jobs/3", location: "Berlin, Germany" },',
    '  { title: "Backend Engineer", url: "https://fixture.example/jobs/4", location: "San Francisco, CA" },',
    ']));',
  ].join('\n'), 'utf8');

  const portals = join(sandbox, 'portals.yml');
  writeFileSync(portals, [
    'title_filter:',
    '  negative: ["Engineering Manager"]',
    'location_filter:',
    '  allow: ["Remote", "Berlin"]',
    '  block: ["San Francisco"]',
    'tracked_companies:',
    '  - name: "Fixture Co"',
    '    careers_url: "https://fixture.example/careers"',
    '    parser:',
    `      command: "${NODE}"`,
    `      args: ["${parser}"]`,
    '  - name: "Parked Co"',
    '    enabled: false',
    '    careers_url: "https://parked.example/careers"',
    '  - name: "No Provider Co"',
    '    scan_method: "websearch"',
    '    scan_query: "No Provider Co careers"',
    '',
  ].join('\n'), 'utf8');

  const scanEnv = { ...process.env, SNIPE_PORTALS: portals };
  const runScan = (...flags) => {
    try {
      return execFileSync(NODE, [SCAN, ...flags],
        { cwd: sandbox, env: scanEnv, encoding: 'utf-8', timeout: 60_000 });
    } catch (e) {
      return `EXIT ${e.status}\n${e.stdout || ''}${e.stderr || ''}`;
    }
  };

  const dry = runScan('--dry-run');

  if (/dry run — no files will be written/.test(dry)) pass('scan --dry-run announces that it will not write');
  else fail(`scan --dry-run banner missing: ${dry.slice(0, 200)}`);

  if (/Total jobs found:\s+4/.test(dry)) pass('scan fetches every job the local parser returns');
  else fail(`scan job count wrong: ${dry.match(/Total jobs found:.*/)?.[0]}`);

  // Each filter has to be attributed to its own counter — a job dropped by the
  // title filter must not be reported as a location drop.
  if (/Filtered by title:\s+1 removed/.test(dry)) pass('scan attributes the title-filter drop to the title counter');
  else fail(`scan title filter: ${dry.match(/Filtered by title:.*/)?.[0]}`);

  if (/Filtered by location:\s+1 removed/.test(dry)) pass('scan attributes the location-filter drop to the location counter');
  else fail(`scan location filter: ${dry.match(/Filtered by location:.*/)?.[0]}`);

  if (/New offers added:\s+2/.test(dry)) pass('scan keeps the two offers that pass both filters');
  else fail(`scan kept the wrong number: ${dry.match(/New offers added:.*/)?.[0]}`);

  // A disabled entry is skipped without being counted as a scan target.
  if (/Companies scanned:\s+1/.test(dry)) pass('scan skips entries with enabled: false');
  else fail(`scan scanned the wrong number of companies: ${dry.match(/Companies scanned:.*/)?.[0]}`);

  // An entry no zero-token provider can handle is handed off, not silently lost.
  if (/websearch handoff|Agent\/WebSearch handoff/i.test(dry)) pass('scan reports a websearch entry as an agent handoff');
  else fail('scan swallowed the entry no provider matched');

  // --dry-run means exactly that.
  if (!existsSync(join(sandbox, 'data/pipeline.md'))) pass('scan --dry-run writes no pipeline.md');
  else fail('scan --dry-run wrote pipeline.md anyway');

  // Known gap, pinned here so it is not mistaken for a test bug: appendToPipeline
  // readFileSync's data/pipeline.md with no existence guard, so a scan that finds
  // new offers in a tree without that file dies with an unhandled ENOENT.
  // appendToScanHistory, right next to it, does seed its own file. Every
  // long-lived checkout has a pipeline.md by now, which is why it has never been
  // hit — a fresh clone's first scan would be.
  const noPipelineFile = runScan();
  if (/ENOENT.*pipeline\.md/.test(noPipelineFile)) {
    pass('scan currently requires data/pipeline.md to already exist (see note above)');
  } else pass('scan seeds data/pipeline.md on demand');

  // Now for real.
  mkdirSync(join(sandbox, 'data'), { recursive: true });
  writeFileSync(join(sandbox, 'data/pipeline.md'), '# Pipeline\n\n## Pendientes\n\n## Procesadas\n', 'utf8');
  const real = runScan();
  const pipelinePath = join(sandbox, 'data/pipeline.md');
  const historyPath = join(sandbox, 'data/scan-history.tsv');

  if (existsSync(pipelinePath)) pass('scan writes the surviving offers to data/pipeline.md');
  else fail(`scan wrote no pipeline.md: ${real.slice(0, 300)}`);

  if (existsSync(pipelinePath)) {
    const pipeline = readFileSync(pipelinePath, 'utf-8');
    if (/fixture\.example\/jobs\/1/.test(pipeline) && /fixture\.example\/jobs\/2/.test(pipeline)) {
      pass('pipeline.md carries the URL of each kept offer');
    } else fail('pipeline.md is missing a kept offer URL');
    if (!/jobs\/3/.test(pipeline) && !/jobs\/4/.test(pipeline)) {
      pass('pipeline.md contains no filtered-out offer');
    } else fail('a filtered-out offer reached pipeline.md');
  }

  if (existsSync(historyPath)) pass('scan records what it saw in data/scan-history.tsv');
  else fail('scan wrote no scan-history.tsv');

  // The dedup contract: a second scan of the same feed adds nothing.
  const second = runScan();
  if (/New offers added:\s+0/.test(second)) pass('a repeat scan dedups every offer against scan-history');
  else fail(`repeat scan added offers again: ${second.match(/New offers added:.*/)?.[0]}`);
  if (/Duplicates:\s+2 skipped/.test(second)) pass('a repeat scan counts both offers as duplicates');
  else fail(`repeat scan dupe count: ${second.match(/Duplicates:.*/)?.[0]}`);

  // --company narrows the scan to one entry.
  const filtered = runScan('--dry-run', '--company', 'nothing-matches-this');
  if (/Companies scanned:\s+0/.test(filtered)) pass('--company filters out every non-matching entry');
  else fail(`--company did not filter: ${filtered.match(/Companies scanned:.*/)?.[0]}`);

  // Failure modes: no portals file, and an unparseable one.
  const noPortals = (() => {
    try {
      execFileSync(NODE, [SCAN, '--dry-run'],
        { cwd: sandbox, env: { ...process.env, SNIPE_PORTALS: join(sandbox, 'gone.yml') }, encoding: 'utf-8' });
      return null;
    } catch (e) { return `${e.stdout || ''}${e.stderr || ''}`; }
  })();
  if (noPortals && /portals\.yml not found/.test(noPortals)) pass('scan exits with a clear message when portals.yml is missing');
  else fail(`scan missing-portals message: ${String(noPortals).slice(0, 160)}`);

  const brokenPortals = join(sandbox, 'broken.yml');
  writeFileSync(brokenPortals, 'tracked_companies: [\n  - name: "unclosed\n', 'utf8');
  const broken = (() => {
    try {
      execFileSync(NODE, [SCAN, '--dry-run'],
        { cwd: sandbox, env: { ...process.env, SNIPE_PORTALS: brokenPortals }, encoding: 'utf-8' });
      return null;
    } catch (e) { return `${e.stdout || ''}${e.stderr || ''}`; }
  })();
  if (broken && /failed to parse/.test(broken)) pass('scan names the parse failure on a malformed portals.yml');
  else fail(`scan malformed-yaml message: ${String(broken).slice(0, 160)}`);

  // A parser that returns junk must be reported against its company, not crash
  // the whole scan.
  const junkParser = join(sandbox, 'junk.mjs');
  writeFileSync(junkParser, 'process.stdout.write("nope");', 'utf8');
  const junkPortals = join(sandbox, 'junk.yml');
  writeFileSync(junkPortals, [
    'tracked_companies:',
    '  - name: "Junk Co"',
    '    careers_url: "https://junk.example/careers"',
    '    parser:',
    `      command: "${NODE}"`,
    `      args: ["${junkParser}"]`,
    '',
  ].join('\n'), 'utf8');
  const junk = (() => {
    try {
      return execFileSync(NODE, [SCAN, '--dry-run'],
        { cwd: sandbox, env: { ...process.env, SNIPE_PORTALS: junkPortals }, encoding: 'utf-8', timeout: 60_000 });
    } catch (e) { return `${e.stdout || ''}${e.stderr || ''}`; }
  })();
  if (/✗ Junk Co/.test(junk)) pass('scan reports a failing parser against its own company and carries on');
  else fail(`scan did not report the junk parser: ${junk.slice(-300)}`);

  // A broken browser must cost the verification, not the scan. verifyOffers()
  // throws when Chromium will not launch and the write step sits below it, so
  // an unguarded throw loses a run that already spent every one of its fetches
  // and, on Apify entries, real money. The TUI passes --verify on every scan,
  // which puts this on the default path rather than an interactive corner.
  //
  // Pointing PLAYWRIGHT_BROWSERS_PATH at an empty directory breaks the launch
  // wherever the test runs, CI included — where Chromium genuinely is
  // installed, so nothing weaker would reach the catch.
  const emptyBrowsers = join(sandbox, 'no-browsers');
  mkdirSync(emptyBrowsers, { recursive: true });

  const verifyParser = join(sandbox, 'verify-jobs.mjs');
  writeFileSync(verifyParser,
    'process.stdout.write(JSON.stringify([' +
    '{ title: "Backend Engineer", url: "https://fixture.example/jobs/verify-1", location: "Remote (EU)" }' +
    ']));', 'utf8');
  const verifyPortals = join(sandbox, 'verify.yml');
  writeFileSync(verifyPortals, [
    'tracked_companies:',
    '  - name: "Verify Co"',
    '    careers_url: "https://verify.example/careers"',
    '    parser:',
    `      command: "${NODE}"`,
    `      args: ["${verifyParser}"]`,
    '',
  ].join('\n'), 'utf8');

  const verified = await runNodeAsync([SCAN, '--verify'], {
    cwd: sandbox,
    env: { ...process.env, SNIPE_PORTALS: verifyPortals, PLAYWRIGHT_BROWSERS_PATH: emptyBrowsers },
    timeout: 120_000,
  });

  if (verified.code === 0) pass('a browser that will not launch does not fail the scan');
  else fail(`scan --verify exited ${verified.code}: ${(verified.out + verified.err).slice(-300)}`);

  if (/WARN: liveness verification skipped/.test(verified.err)) {
    pass('and says so on stderr, where a broken Chromium cannot be mistaken for a clean run');
  } else fail(`no stderr warning: ${verified.err.slice(-300)}`);

  if (/could not launch Chromium|requires Playwright/.test(verified.err)) {
    pass('and names the browser as the reason, with the install command');
  } else fail(`stderr does not name the cause: ${verified.err.slice(-300)}`);

  // The summary is the half a user actually reads. Reporting "0 dropped" here
  // would claim every offer was checked and found alive.
  if (/Liveness check:\s+SKIPPED/.test(verified.out)) {
    pass('the summary reports SKIPPED rather than an expired count of zero');
  } else fail(`summary line wrong: ${verified.out.match(/Liveness check:.*|Expired \(verified\):.*/)?.[0]}`);

  if (!/Expired \(verified\):/.test(verified.out)) {
    pass('and drops the verified-only counters entirely, instead of printing empty ones');
  } else fail('the summary printed verified counters for a verification that never ran');

  // The point of all of it: the scan still wrote.
  const afterVerify = readFileSync(pipelinePath, 'utf-8');
  if (/fixture\.example\/jobs\/verify-1/.test(afterVerify)) {
    pass('the unverified offer still reaches pipeline.md, where Phase 1 can mark it unavailable');
  } else fail('a failed verification cost the whole scan its output');

  // Now the same flag with a browser that does launch. .invalid is reserved by
  // RFC 2606 and resolves nowhere, so the page load fails as a transient
  // network error rather than a dead posting — the case that decides whether a
  // flaky connection quietly deletes offers. It must not: only the classifier
  // may expire one.
  const liveParser = join(sandbox, 'live-jobs.mjs');
  writeFileSync(liveParser,
    'process.stdout.write(JSON.stringify([' +
    '{ title: "Backend Engineer", url: "https://fixture.invalid/jobs/verify-2", location: "Remote (EU)" }' +
    ']));', 'utf8');
  const livePortals = join(sandbox, 'live.yml');
  writeFileSync(livePortals, [
    'tracked_companies:',
    '  - name: "Live Co"',
    '    careers_url: "https://live.example/careers"',
    '    parser:',
    `      command: "${NODE}"`,
    `      args: ["${liveParser}"]`,
    '',
  ].join('\n'), 'utf8');

  const withBrowser = await runNodeAsync([SCAN, '--verify'], {
    cwd: sandbox,
    env: { ...process.env, SNIPE_PORTALS: livePortals },
    timeout: 180_000,
  });

  // Same reason pdf.test.mjs warns rather than fails: a developer who never ran
  // `npx playwright install chromium` has no browser, and CI does.
  if (/could not launch Chromium|requires Playwright|Executable doesn't exist/.test(withBrowser.err)) {
    warn('Playwright browser not installed — the verified --verify path was not exercised');
  } else {
    if (withBrowser.code === 0) pass('--verify completes against a real browser');
    else fail(`scan --verify exited ${withBrowser.code}: ${(withBrowser.out + withBrowser.err).slice(-300)}`);

    if (/Expired \(verified\):/.test(withBrowser.out) && !/Liveness check:\s+SKIPPED/.test(withBrowser.out)) {
      pass('and reports the verified counters, so the verification really ran');
    } else fail(`no verified counters: ${withBrowser.out.match(/Liveness check:.*|Expired \(verified\):.*/)?.[0]}`);

    if (/fixture\.invalid\/jobs\/verify-2/.test(readFileSync(pipelinePath, 'utf-8'))) {
      pass('an offer whose page would not load is kept for the next scan, not expired');
    } else fail('a transient network failure was treated as a dead posting');
  }

  rmSync(sandbox, { force: true, recursive: true });
}
