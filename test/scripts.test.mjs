// Coverage for the standalone CLIs around the pipeline. Run standalone with:
// node test/scripts.test.mjs
//
// These are all file-in / file-out tools, so they need no model server — only a
// fixture directory and an assertion on what they print or write. The
// destructive ones (reserve-report-num claims a slot in the real reports/) are
// exercised through their own release path so nothing is left behind.
import {
  pass, fail, warn, ROOT, join, run, runNodeAsync, ensureUserLayer,
  existsSync, readFileSync, writeFileSync, mkdirSync, rmSync, mkdtempSync, readdirSync, tmpdir,
} from './harness.mjs';
import { utimesSync } from 'node:fs';
import http from 'node:http';

console.log('\n17. Standalone CLIs');

const tmp = mkdtempSync(join(tmpdir(), 'snipe-scripts-'));
// eval-harness compare grounds Block B evidence against cv.md, which is
// gitignored — without a stand-in it returns nothing at all on a fresh checkout.
const cleanUserLayer = ensureUserLayer();

/** A bench-shaped directory: <root>/evals/<id>.json, the layout compare expects. */
function benchDir(name, evals) {
  const dir = join(tmp, name);
  mkdirSync(join(dir, 'evals'), { recursive: true });
  for (const ev of evals) {
    writeFileSync(join(dir, 'evals', `${ev.id}.json`), JSON.stringify({ status: 'evaled', ...ev }), 'utf8');
  }
  return dir;
}

const A = benchDir('a', [
  { id: '1', company: 'Alpha', role: 'Backend Engineer', score: 4.1, cv_match: 4, north_star: 4, comp_inferred: 4, final_decision: 'Apply', salary_posted: { min: 80000, max: 90000 } },
  { id: '2', company: 'Beta', role: 'Platform Engineer', score: 2.6, cv_match: 2, north_star: 3, comp_inferred: 3, final_decision: 'Skip' },
  { id: '3', company: 'Gamma', role: 'Data Engineer', score: 3.4, cv_match: 3, north_star: 4, comp_inferred: 3, final_decision: 'Apply' },
  { id: '4', company: 'Delta', role: 'SRE', score: 1.8, cv_match: 2, north_star: 1, comp_inferred: 2, final_decision: 'Skip' },
]);
const B = benchDir('b', [
  { id: '1', company: 'Alpha', role: 'Backend Engineer', score: 4.4, cv_match: 5, north_star: 4, comp_inferred: 4, final_decision: 'Apply' },
  { id: '2', company: 'Beta', role: 'Platform Engineer', score: 2.2, cv_match: 2, north_star: 2, comp_inferred: 3, final_decision: 'Skip' },
  { id: '3', company: 'Gamma', role: 'Data Engineer', score: 3.9, cv_match: 4, north_star: 4, comp_inferred: 3, final_decision: 'Apply' },
  { id: '4', company: 'Delta', role: 'SRE', score: 1.5, cv_match: 1, north_star: 2, comp_inferred: 2, final_decision: 'Skip' },
]);

// Labels deliberately disagree with A's ranking on one pair, so rho and pair
// accuracy have something to measure rather than coming back a perfect 1.
const labels = join(tmp, 'labels.tsv');
writeFileSync(labels, ['1\t5\tAlpha', '2\t2\tBeta', '3\t4\tGamma', '4\t1\tDelta'].join('\n') + '\n', 'utf8');

// ── batch/eval-harness.mjs ───────────────────────────────────────────────────

const HARNESS = 'batch/eval-harness.mjs';

const usage = run('node', [HARNESS]);
if (usage && /Usage: eval-harness/.test(usage)) pass('eval-harness prints usage with no command');
else fail(`eval-harness usage line missing: ${String(usage).slice(0, 120)}`);

const badCmd = await runNodeAsync([HARNESS, 'nonsense']);
if (badCmd.code === 1) pass('eval-harness exits 1 on an unknown command');
else fail(`eval-harness exited ${badCmd.code} on an unknown command, expected 1`);

const stats = run('node', [HARNESS, 'stats', '--evals', join(A, 'evals')]);
if (stats && /4 evals/.test(stats)) pass('eval-harness stats counts the evals in --evals');
else fail(`eval-harness stats output: ${String(stats).slice(0, 160)}`);
if (stats && /score histogram/.test(stats)) pass('eval-harness stats prints the score histogram');
else fail('eval-harness stats printed no histogram');
if (stats && /cv4\/ns4/.test(stats)) pass('eval-harness stats tallies the cv/ns dimension combos');
else fail('eval-harness stats printed no dimension combos');

// Backtest re-scores stored evals under new weights — the point is that changing
// the weights changes the output, otherwise the knob does nothing.
const backtest = run('node', [HARNESS, 'backtest', '--evals', join(A, 'evals'), '--weights', 'cv=0.5,ns=0.3,comp=0.2']);
if (backtest && /"cv":0\.5/.test(backtest)) pass('eval-harness backtest echoes the weights it parsed');
else fail(`eval-harness backtest weights not echoed: ${String(backtest).slice(0, 160)}`);
if (backtest && /biggest moves/.test(backtest)) pass('eval-harness backtest reports the biggest score moves');
else fail('eval-harness backtest printed no move list');

const compare = run('node', [HARNESS, 'compare', '--a', A, '--b', B, '--labels', labels]);
if (compare && /4 common offers/.test(compare)) pass('eval-harness compare pairs the offers common to both runs');
else fail(`eval-harness compare output: ${String(compare).slice(0, 200)}`);
if (compare && /A↔B Spearman/.test(compare)) pass('eval-harness compare reports the A↔B rank correlation');
else fail('eval-harness compare printed no A↔B Spearman');
// With --labels it also scores each run against the human labels, which is the
// number that decides whether a change actually improved anything.
if (compare && /A↔labels Spearman/.test(compare) && /B↔labels Spearman/.test(compare)) {
  pass('eval-harness compare scores both runs against --labels');
} else fail('eval-harness compare ignored --labels');
// Discrimination: a run that collapses every offer into one cv/ns combo scores
// well on rho and tells you nothing.
if (compare && /unique cv\/ns combos/.test(compare)) pass('eval-harness compare reports how discriminating each run was');
else fail('eval-harness compare printed no discrimination figure');

const noDirs = await runNodeAsync([HARNESS, 'compare']);
if (noDirs.code === 1 && /requires --a DIR --b DIR/.test(noDirs.err + noDirs.out)) {
  pass('eval-harness compare demands both directories');
} else fail(`eval-harness compare ran without --a/--b (exit ${noDirs.code})`);

// A comparison against two runs with nothing in common is a no-op, not a crash.
const emptyB = benchDir('empty', []);
const noCommon = run('node', [HARNESS, 'compare', '--a', A, '--b', emptyB]);
if (noCommon && /0 common offers/.test(noCommon)) pass('eval-harness compare handles two runs with no common offers');
else fail(`eval-harness compare with no overlap: ${String(noCommon).slice(0, 160)}`);

// sample and unlabeled read the real batch/evals; --out keeps sample's write in
// the temp dir rather than batch/bench/.
const sampleOut = join(tmp, 'sample.tsv');
const sample = run('node', [HARNESS, 'sample', '--n', '3', '--out', sampleOut]);
const sampleRows = sample !== null && existsSync(sampleOut)
  ? readFileSync(sampleOut, 'utf8').trim().split('\n').filter(Boolean)
  : [];
if (!sampleRows.length) {
  warn('eval-harness sample had nothing to sample — batch/evals is empty in this checkout');
} else if (sampleRows.every(r => r.split('\t').length === 4)) {
  pass(`eval-harness sample writes a 4-column TSV (${sampleRows.length} rows)`);
} else fail(`eval-harness sample TSV is malformed: ${sampleRows[0]}`);

const unlabeled = run('node', [HARNESS, 'unlabeled', '--labels', labels]);
if (unlabeled && /without a labels\.tsv entry/.test(unlabeled)) pass('eval-harness unlabeled lists evals missing a label');
else fail(`eval-harness unlabeled output: ${String(unlabeled).slice(0, 160)}`);

// ── tracker/reserve-report-num.mjs ───────────────────────────────────────────

const RESERVE = 'tracker/reserve-report-num.mjs';
const reportsDir = join(ROOT, 'reports');

const reserved = run('node', [RESERVE]);
if (reserved && /^\d{3}$/.test(reserved)) {
  pass(`reserve-report-num prints a zero-padded slot (${reserved})`);

  const sentinel = join(reportsDir, `${reserved}-RESERVED.md`);
  if (existsSync(sentinel)) pass('reserve-report-num claims the slot with a sentinel file');
  else fail('reserve-report-num printed a number but claimed nothing');

  // The claim has to be exclusive, or two parallel runs get the same number and
  // the second one overwrites the first's report.
  const second = run('node', [RESERVE]);
  if (second && second !== reserved) pass(`a second reservation gets the next slot (${second}), never the same one`);
  else fail(`second reservation returned ${second}, expected something after ${reserved}`);

  run('node', [RESERVE, '--release', reserved]);
  if (second) run('node', [RESERVE, '--release', second]);
  if (!existsSync(sentinel)) pass('--release removes the sentinel and frees the slot');
  else fail('--release left the sentinel behind');
  // Belt and braces: never leave a claim in the developer's reports/.
  for (const n of [reserved, second]) {
    if (n) rmSync(join(reportsDir, `${n}-RESERVED.md`), { force: true });
  }
} else fail(`reserve-report-num printed ${String(reserved).slice(0, 60)}, expected NNN`);

const badRelease = await runNodeAsync([RESERVE, '--release', 'abcd']);
if (badRelease.code === 1 && /Usage: node reserve-report-num/.test(badRelease.err)) {
  pass('--release rejects a non-numeric slot');
} else fail(`--release accepted a bad argument (exit ${badRelease.code})`);

const gcRun = await runNodeAsync([RESERVE, '--gc']);
if (gcRun.code === 0) pass('--gc exits 0 (sentinels under 4h old are left alone)');
else fail(`--gc exited ${gcRun.code}`);

// ── batch/import-pipeline.mjs ────────────────────────────────────────────────

// --dry-run so the real batch-input.tsv is never touched by the test.
const importRun = await runNodeAsync(['batch/import-pipeline.mjs', '--dry-run']);
if (importRun.code === 0) pass('import-pipeline --dry-run exits 0');
else fail(`import-pipeline --dry-run exited ${importRun.code}: ${importRun.err.slice(0, 160)}`);
if (/pipeline\.md not found|would import|no new|New URLs|0 new/i.test(importRun.out)) {
  pass('import-pipeline --dry-run reports what it would do without writing');
} else fail(`import-pipeline said: ${importRun.out.slice(0, 160)}`);

// ── check-liveness.mjs ───────────────────────────────────────────────────────

const noUrls = await runNodeAsync(['check-liveness.mjs']);
if (noUrls.code === 1 && /Usage: node check-liveness/.test(noUrls.err)) {
  pass('check-liveness prints usage and exits 1 with no URLs');
} else fail(`check-liveness ran with no URLs (exit ${noUrls.code})`);

// A local server gives it a real page to classify without going near a job board.
const server = http.createServer((req, res) => {
  if (req.url === '/expired') {
    res.writeHead(200, { 'Content-Type': 'text/html' });
    return res.end('<html><body><h1>This job is no longer available</h1><p>The position has been filled.</p></body></html>');
  }
  res.writeHead(200, { 'Content-Type': 'text/html' });
  res.end('<html><body><h1>Senior Backend Engineer</h1><p>Apply now. We are hiring a backend engineer to own our payments platform.</p></body></html>');
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const base = `http://127.0.0.1:${server.address().port}`;

// CI sets PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD, so a missing browser is an expected
// environment gap rather than a regression — and it surfaces after the "Checking
// N URL(s)" banner is already printed, so the banner alone cannot be the signal.
const noBrowser = res => res.code === null
  || /Executable doesn't exist|browserType\.launch|playwright install/i.test(res.err + res.out);

const liveness = await runNodeAsync(['check-liveness.mjs', '--no-fallback', `${base}/live`, `${base}/expired`],
  { timeout: 120_000 });
if (noBrowser(liveness)) {
  warn('check-liveness skipped — no Playwright browser in this environment');
} else if (/Checking 2 URL/.test(liveness.out)) {
  pass('check-liveness drives Chromium over both URLs and reports a verdict');
  if (/active|expired|uncertain/i.test(liveness.out)) pass('check-liveness classifies each URL it visited');
  else fail(`check-liveness printed no classification: ${liveness.out.slice(0, 200)}`);
} else {
  fail(`check-liveness produced no report: ${(liveness.err || liveness.out).slice(0, 200)}`);
}

// --file reads the URL list from disk instead of argv.
const urlFile = join(tmp, 'urls.txt');
writeFileSync(urlFile, `# a comment\n${base}/live\n\n`, 'utf8');
const fromFile = await runNodeAsync(['check-liveness.mjs', '--no-fallback', '--file', urlFile], { timeout: 120_000 });
if (/Checking 1 URL/.test(fromFile.out)) pass('check-liveness --file reads URLs from a file, skipping comments and blanks');
else if (noBrowser(fromFile)) warn('check-liveness --file skipped — no Playwright browser in this environment');
else fail(`check-liveness --file read the wrong list: ${(fromFile.err || fromFile.out).slice(0, 160)}`);

await new Promise(r => server.close(r));

// ── validate-portals.mjs ─────────────────────────────────────────────────────

// The script ships its own round-trip check; running it is the cheapest way to
// know the validator still agrees with the schema it validates against.
const selfTest = await runNodeAsync(['validate-portals.mjs', '--self-test'], { timeout: 60_000 });
if (selfTest.code === 0) pass('validate-portals --self-test passes');
else fail(`validate-portals --self-test failed (exit ${selfTest.code}): ${(selfTest.err || selfTest.out).slice(0, 200)}`);

const goodPortals = join(tmp, 'portals-good.yml');
writeFileSync(goodPortals, [
  'title_filter:',
  '  positive: ["Backend", "Platform"]',
  '  negative: ["Manager"]',
  'location_filter:',
  '  allow: ["Remote", "Berlin"]',
  'tracked_companies:',
  '  - name: "Example Co"',
  '    provider: "greenhouse"',
  '    careers_url: "https://boards.greenhouse.io/exampleco"',
  '  - name: "Disabled Co"',
  '    enabled: false',
  '',
].join('\n'), 'utf8');
const goodRun = await runNodeAsync(['validate-portals.mjs', '--file', goodPortals]);
if (goodRun.code === 0 && /0 errors/.test(goodRun.out)) pass('validate-portals accepts a well-formed portals.yml');
else fail(`validate-portals rejected a valid file (exit ${goodRun.code}): ${goodRun.out.slice(0, 200)}`);

// A disabled entry is skipped wholesale — that is what lets a broken portal be
// parked in the file instead of deleted.
if (goodRun.code === 0 && !/Disabled Co/.test(goodRun.out)) pass('validate-portals skips entries with enabled: false');
else fail('validate-portals validated a disabled entry');

// Every one of these is a config that would fail at scan time, so it has to fail
// here instead: a nameless company, an unroutable provider, and a malformed URL.
const badPortals = join(tmp, 'portals-bad.yml');
writeFileSync(badPortals, [
  'title_filter:',
  '  positive: ["Backend", ""]',
  'tracked_companies:',
  '  - name: ""',
  '    careers_url: "https://jobs.lever.co/nameless"',
  '  - name: "Bad Provider Co"',
  '    provider: "not-a-real-provider"',
  '    careers_url: "https://jobs.lever.co/acme"',
  '  - name: "Bad URL Co"',
  '    careers_url: "not-a-url"',
  '',
].join('\n'), 'utf8');
const badRun = await runNodeAsync(['validate-portals.mjs', '--file', badPortals]);
if (badRun.code === 1 && /error:/.test(badRun.out)) pass('validate-portals exits 1 and lists each error it found');
else fail(`validate-portals accepted a broken file (exit ${badRun.code}): ${badRun.out.slice(0, 200)}`);
for (const [what, re] of [
  ['an empty keyword', /title_filter\.positive/],
  ['an unnamed company', /\.name/],
  ['an unknown provider', /unknown provider/],
  ['a malformed URL', /careers_url/],
]) {
  if (re.test(badRun.out)) pass(`validate-portals flags ${what}`);
  else fail(`validate-portals missed ${what}: ${badRun.out.slice(0, 300)}`);
}

// A duplicate enabled company is a warning, not an error — it still scans, it
// just scans twice.
const dupPortals = join(tmp, 'portals-dup.yml');
writeFileSync(dupPortals, [
  'tracked_companies:',
  '  - name: "Acme Corp"',
  '    careers_url: "https://jobs.lever.co/acme"',
  '  - name: "acme corp"',
  '    careers_url: "https://jobs.lever.co/acme2"',
  '',
].join('\n'), 'utf8');
const dupRun = await runNodeAsync(['validate-portals.mjs', '--file', dupPortals]);
if (dupRun.code === 0 && /warning:.*duplicate/.test(dupRun.out)) {
  pass('validate-portals warns (but does not fail) on a duplicate company name');
} else fail(`validate-portals duplicate handling: exit ${dupRun.code} ${dupRun.out.slice(0, 200)}`);

const missingFile = await runNodeAsync(['validate-portals.mjs', '--file', join(tmp, 'nope.yml')]);
if (missingFile.code === 1) pass('validate-portals exits 1 when the file does not exist');
else fail(`validate-portals exited ${missingFile.code} for a missing file`);

// ── providers/local-parser.mjs ───────────────────────────────────────────────

// The provider shells out to a user-supplied script and normalises whatever
// JSON shape it returns, so the fixture is a script that emits one.
const parserScript = join(tmp, 'parser.mjs');
writeFileSync(parserScript, [
  'const shape = process.argv[2];',
  'const jobs = [',
  '  { title: "Backend Engineer", url: "/jobs/1", location: { name: "Berlin" } },',
  '  { name: "Platform Engineer", job_url: "https://elsewhere.example/jobs/2", locations: ["Remote", "EU"] },',
  '  { title: "", url: "/jobs/3" },',
  '];',
  'process.stdout.write(JSON.stringify(shape === "wrapped" ? { jobs } : jobs));',
].join('\n'), 'utf8');

const { default: localParser } = await import('../providers/local-parser.mjs');
const entry = {
  name: 'Fixture Co',
  careers_url: 'https://fixture.example/careers',
  parser: { command: process.execPath, args: [parserScript] },
};

if (localParser.detect(entry)) pass('local-parser detects an entry with a parser command');
else fail('local-parser did not detect a valid parser entry');

if (localParser.detect({ name: 'No parser', careers_url: 'https://x.example' }) === null) {
  pass('local-parser ignores an entry with no parser command');
} else fail('local-parser claimed an entry that has no parser');

// A configured script that is not on disk must not be claimed — otherwise the
// scan fails at fetch time instead of falling through to another provider.
if (localParser.detect({ ...entry, parser: { command: process.execPath, args: [join(tmp, 'gone.mjs')] } }) === null) {
  pass('local-parser ignores an entry whose parser script is missing');
} else fail('local-parser claimed an entry whose script does not exist');

const jobs = await localParser.fetch(entry);
if (jobs.length === 2) pass('local-parser drops jobs with no title or no URL');
else fail(`local-parser returned ${jobs.length} jobs, expected 2`);
if (jobs[0]?.url === 'https://fixture.example/jobs/1') pass('local-parser resolves a relative job URL against careers_url');
else fail(`local-parser gave url ${jobs[0]?.url}`);
if (jobs[0]?.company === 'Fixture Co') pass('local-parser falls back to the portal name for company');
else fail(`local-parser gave company ${jobs[0]?.company}`);
if (jobs[0]?.location === 'Berlin') pass('local-parser flattens an object location to its name');
else fail(`local-parser gave location ${jobs[0]?.location}`);
if (jobs[1]?.location === 'Remote, EU') pass('local-parser joins an array location');
else fail(`local-parser gave location ${jobs[1]?.location}`);

const wrapped = await localParser.fetch({ ...entry, parser: { command: process.execPath, args: [parserScript, 'wrapped'] } });
if (wrapped.length === 2) pass('local-parser accepts a { jobs: [...] } wrapper as well as a bare array');
else fail(`local-parser returned ${wrapped.length} from the wrapped shape`);

const junkScript = join(tmp, 'junk.mjs');
writeFileSync(junkScript, 'process.stdout.write("not json");', 'utf8');
try {
  await localParser.fetch({ ...entry, parser: { command: process.execPath, args: [junkScript] } });
  fail('local-parser accepted non-JSON output');
} catch (e) {
  if (/invalid JSON/.test(e.message)) pass('local-parser rejects non-JSON parser output by name');
  else fail(`local-parser threw the wrong error: ${e.message}`);
}

const wrongShape = join(tmp, 'wrong.mjs');
writeFileSync(wrongShape, 'process.stdout.write(JSON.stringify({ ok: true }));', 'utf8');
try {
  await localParser.fetch({ ...entry, parser: { command: process.execPath, args: [wrongShape] } });
  fail('local-parser accepted JSON with no job array');
} catch (e) {
  if (/array or contain jobs/.test(e.message)) pass('local-parser rejects JSON that carries no job list');
  else fail(`local-parser threw the wrong error: ${e.message}`);
}

// ── generate-cover-letter.mjs ────────────────────────────────────────────────

const COVER = 'generate-cover-letter.mjs';

const coverHelp = await runNodeAsync([COVER, '--help']);
if (coverHelp.code === 0 && /--payload/.test(coverHelp.out)) pass('generate-cover-letter --help exits 0 and documents --payload');
else fail(`generate-cover-letter --help: exit ${coverHelp.code}`);

const noPayload = await runNodeAsync([COVER]);
if (noPayload.code === 1) pass('generate-cover-letter exits 1 with no --payload');
else fail(`generate-cover-letter exited ${noPayload.code} with no payload`);

const gonePayload = await runNodeAsync([COVER, '--payload', join(tmp, 'nope.json')]);
if (gonePayload.code === 1 && /not found/.test(gonePayload.err)) pass('generate-cover-letter names a missing payload file');
else fail(`generate-cover-letter missing-payload message: ${gonePayload.err.slice(0, 120)}`);

// buildHtml is exported precisely so the templating can be checked without
// Playwright — the escaping is the part that has to be right.
const { buildHtml } = await import('../generate-cover-letter.mjs');
const letterHtml = buildHtml({
  candidate: { name: 'Alex <Fixture>', email: 'alex@example.com', location: 'Berlin' },
  letter: {
    role_title: 'Backend Engineer', opening: 'I am writing about the role.',
    profile_intro: 'Six years of Python & Node.js.',
    achievements: ['Cut p99 latency 40%'], greeting: 'Dear Hiring Team,',
    closing: 'Best regards,', footnotes: ['Metrics from production dashboards'],
  },
});
if (/Backend Engineer/.test(letterHtml)) pass('buildHtml substitutes the role title into the template');
else fail('buildHtml did not substitute the role title');
if (/Alex &lt;Fixture&gt;/.test(letterHtml) && !/Alex <Fixture>/.test(letterHtml)) {
  pass('buildHtml escapes HTML in candidate-supplied text');
} else fail('buildHtml let raw HTML through from the payload');
if (/Dear Hiring Team,/.test(letterHtml)) pass('buildHtml renders an optional greeting when given one');
else fail('buildHtml dropped the greeting');
if (!/\{\{[A-Z_]+\}\}/.test(letterHtml)) pass('buildHtml leaves no unsubstituted {{TOKEN}} in the output');
else fail(`buildHtml left a token behind: ${letterHtml.match(/\{\{[A-Z_]+\}\}/)?.[0]}`);

// An omitted optional block must vanish, not render an empty paragraph.
const minimal = buildHtml({
  candidate: { name: 'Alex' },
  letter: { role_title: 'SRE', opening: 'Hello.', profile_intro: 'Intro.' },
});
if (!/class="greeting"/.test(minimal)) pass('buildHtml omits the greeting block entirely when absent');
else fail('buildHtml rendered an empty greeting block');

// The required-field guard is what stops a half-filled payload becoming a PDF.
try {
  buildHtml({ candidate: { name: 'Alex' } });
  fail('buildHtml accepted a payload with no letter');
} catch (e) {
  if (/letter/.test(e.message)) pass('buildHtml names the missing top-level field');
  else fail(`buildHtml threw the wrong error: ${e.message}`);
}
try {
  buildHtml({ candidate: { name: 'Alex' }, letter: { role_title: 'SRE' } });
  fail('buildHtml accepted a letter with no opening');
} catch (e) {
  if (/opening|profile_intro/.test(e.message)) pass('buildHtml names the missing letter fields');
  else fail(`buildHtml threw the wrong error: ${e.message}`);
}

// ---- cv-select self-check ------------------------------------------------
// cv-select ranks CV bullets against Block B before the 7B ever sees them, and
// carries its own assert-based self-check with a stubbed embedder. Running it is
// cheaper and stricter than re-deriving its fixtures here.
{
  const res = await runNodeAsync(['batch/cv-select.mjs'], { timeout: 120_000 });
  if (res.code === 0 && /self-check passed/.test(res.out)) pass('cv-select self-check passes');
  else fail(`cv-select self-check exited ${res.code}: ${(res.out + res.err).slice(0, 300)}`);
}

// ---- embeddings CLI usage -------------------------------------------------
// rebuild/sync/query all need a live embedder; only the argument handling is
// reachable offline, and that is where the silent-failure risk is.
{
  const usage = await runNodeAsync(['batch/embeddings.mjs']);
  if (/usage: embeddings\.mjs/.test(usage.out)) pass('embeddings.mjs with no subcommand prints usage');
  else fail(`embeddings.mjs printed: ${usage.out.slice(0, 160)}`);

  const bad = await runNodeAsync(['batch/embeddings.mjs', 'nonsense']);
  if (/usage: embeddings\.mjs/.test(bad.out)) pass('embeddings.mjs falls back to usage for an unknown subcommand');
  else fail(`embeddings.mjs on an unknown subcommand printed: ${bad.out.slice(0, 160)}`);

  const noQuery = await runNodeAsync(['batch/embeddings.mjs', 'query']);
  if (noQuery.code === 1 && /usage: embeddings\.mjs query/.test(noQuery.err)) {
    pass('embeddings.mjs query with no text exits 1 and says what it wanted');
  } else fail(`embeddings.mjs query exited ${noQuery.code}: ${(noQuery.err + noQuery.out).slice(0, 160)}`);
}

// ---- verify-pipeline / normalize-statuses --------------------------------
// Both resolve the tracker through SNIPE_TRACKER, so a fixture in a temp dir
// keeps the developer's real applications.md out of reach. The fixture is
// deliberately broken in one way per check.
{
  const trackerDir = join(tmp, 'tracker-fixture');
  mkdirSync(trackerDir, { recursive: true });
  const trackerFile = join(trackerDir, 'applications.md');
  const withTracker = { ...process.env, SNIPE_TRACKER: trackerFile };

  const HEADER = [
    '# Applications Tracker',
    '',
    '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |',
    '|---|------|---------|------|-------|--------|-----|--------|-------|',
  ];
  writeFileSync(trackerFile, [
    ...HEADER,
    '| 1 | 2026-01-01 | Acme | Backend Engineer | 4.2/5 | Evaluated | Y | [1](reports/nope.md) | — |',
    '| 2 | 2026-01-02 | Acme | Backend Engineer | 3.1/5 | **Applied** | N | — | duplicate of #1 |',
    '| 3 | 2026-01-03 | Beta | SRE | 4.0/5 | Applied 2026-01-09 | N | — | — |',
    '| 4 | 2026-01-04 | Gamma | Data Engineer | four out of five | HOLD | N | — | — |',
    '| 5 | 2026-01-05 | Delta | Platform Engineer | 3.9/5 | Vibing | N | — | — |',
    '',
  ].join('\n'), 'utf8');

  const verify = await runNodeAsync(['tracker/verify-pipeline.mjs'], { env: withTracker });
  const vout = verify.out;
  const has = (re, label) => (re.test(vout) ? pass(label) : fail(`${label} — verify-pipeline printed: ${vout.slice(0, 400)}`));
  has(/#5: Non-canonical status "Vibing"/, 'verify-pipeline flags a non-canonical status');
  has(/#2: Status contains markdown bold/, 'verify-pipeline flags markdown bold in a status');
  has(/#3: Status contains date/, 'verify-pipeline flags a date left in the status cell');
  has(/#1: Report not found: reports\/nope\.md/, 'verify-pipeline flags a broken report link');
  has(/#4: Invalid score format/, 'verify-pipeline flags a malformed score');
  has(/Possible duplicates: #1, #2/, 'verify-pipeline warns about a repeated company+role');
  has(/Pipeline has errors/, 'verify-pipeline ends with a non-clean verdict when it found errors');
  // HOLD is a known alias, not an error — normalize-statuses folds it later.
  if (!/#4: Non-canonical status/.test(vout)) pass('verify-pipeline accepts a known alias like HOLD');
  else fail('verify-pipeline flagged the HOLD alias as non-canonical');

  const clean = join(tmp, 'clean-tracker');
  mkdirSync(clean, { recursive: true });
  const cleanFile = join(clean, 'applications.md');
  writeFileSync(cleanFile, [...HEADER,
    '| 1 | 2026-01-01 | Acme | Backend Engineer | 4.2/5 | Evaluated | N | — | — |', ''].join('\n'), 'utf8');
  const verifyClean = await runNodeAsync(['tracker/verify-pipeline.mjs'],
    { env: { ...process.env, SNIPE_TRACKER: cleanFile } });
  if (/Pipeline is clean/.test(verifyClean.out)) pass('verify-pipeline reports a clean tracker as clean');
  else fail(`verify-pipeline on a clean tracker: ${verifyClean.out.slice(-300)}`);

  const missing = await runNodeAsync(['tracker/verify-pipeline.mjs'],
    { env: { ...process.env, SNIPE_TRACKER: join(tmp, 'no-such-tracker.md') } });
  if (missing.code === 0 && /normal for a fresh setup/.test(missing.out)) {
    pass('verify-pipeline treats a missing tracker as a fresh setup, not an error');
  } else fail(`verify-pipeline on a missing tracker exited ${missing.code}`);

  // normalize-statuses --dry-run must report exactly what it would change and
  // touch nothing; the real run rewrites and leaves a .bak beside it.
  const before = readFileSync(trackerFile, 'utf8');
  const dry = await runNodeAsync(['tracker/normalize-statuses.mjs', '--dry-run'], { env: withTracker });
  if (/#4: "HOLD" → "Evaluated"/.test(dry.out)) pass('normalize-statuses maps HOLD to Evaluated');
  else fail(`normalize-statuses --dry-run printed: ${dry.out.slice(0, 300)}`);
  if (/#2: "\*\*Applied\*\*" → "Applied"/.test(dry.out)) pass('normalize-statuses strips markdown bold from a status');
  else fail('normalize-statuses did not strip the bold status');
  if (/unknown statuses/.test(dry.out) && /"Vibing"/.test(dry.out)) pass('normalize-statuses lists statuses it cannot map');
  else fail('normalize-statuses did not report the unmappable status');
  if (/dry-run — no changes written/.test(dry.out) && readFileSync(trackerFile, 'utf8') === before) {
    pass('normalize-statuses --dry-run writes nothing');
  } else fail('normalize-statuses --dry-run modified the tracker');

  const real = await runNodeAsync(['tracker/normalize-statuses.mjs'], { env: withTracker });
  const after = readFileSync(trackerFile, 'utf8');
  if (/backup/.test(real.out) && existsSync(trackerFile + '.bak')) pass('normalize-statuses backs the tracker up before rewriting');
  else fail('normalize-statuses rewrote the tracker with no backup');
  if (/\| Evaluated \|/.test(after) && !/\*\*Applied\*\*/.test(after)) pass('normalize-statuses writes the canonical labels back');
  else fail(`normalized tracker still reads: ${after.slice(0, 300)}`);

  const noop = await runNodeAsync(['tracker/normalize-statuses.mjs'],
    { env: { ...process.env, SNIPE_TRACKER: cleanFile } });
  if (/No changes needed/.test(noop.out)) pass('normalize-statuses says so when nothing needs changing');
  else fail(`normalize-statuses on a clean tracker: ${noop.out.slice(-200)}`);

  const absent = await runNodeAsync(['tracker/normalize-statuses.mjs'],
    { env: { ...process.env, SNIPE_TRACKER: join(tmp, 'no-such-tracker.md') } });
  if (absent.code === 0 && /Nothing to normalize/.test(absent.out)) {
    pass('normalize-statuses exits 0 when there is no tracker');
  } else fail(`normalize-statuses on a missing tracker exited ${absent.code}`);
}

cleanUserLayer();
rmSync(tmp, { force: true, recursive: true });

// Nothing above may leave a reservation in the developer's reports/.
const leftovers = existsSync(reportsDir)
  ? readdirSync(reportsDir).filter(f => f.endsWith('-RESERVED.md'))
  : [];
if (!leftovers.length) pass('no reservation sentinels left in reports/');
else fail(`left ${leftovers.length} sentinel(s) behind: ${leftovers.join(', ')}`);

// ── validate-portals: the field validators, called directly ──────────────────
// The CLI tests above prove a good file passes and a bad one fails, but they
// reach only the happy path of each validator. These call the exported
// validator with one malformed field at a time, which is the only way to see
// that the message names the right path — a validator that reports every fault
// against `<root>` passes a pass/fail assertion and helps nobody.

console.log('\n17b. validate-portals field validators');

const { validatePortalsConfig } = await import(join(ROOT, 'validate-portals.mjs'));
const vp = async (config, opts) => (await validatePortalsConfig(config, opts)).errors.map(e => `${e.path}: ${e.message}`).join(' | ');
const company = (over) => ({ tracked_companies: [{ name: 'Acme', ...over }] });

const hasErr = async (config, needle, label, opts) => {
  const got = await vp(config, opts);
  got.includes(needle) ? pass(label) : fail(`${label} — got ${JSON.stringify(got) || '(no errors)'}`);
};

await hasErr({ tracked_companies: 'nope' }, 'tracked_companies must be an array', 'tracked_companies must be an array');
await hasErr({ tracked_companies: ['nope'] }, 'company entry must be an object', 'a non-object company entry is rejected');
await hasErr(company({ name: '   ' }), 'non-empty string name', 'a blank company name is rejected');

// URLs: three distinct faults, three distinct messages.
await hasErr(company({ careers_url: 42 }), 'must be a string URL', 'a non-string URL is rejected');
await hasErr(company({ careers_url: 'not a url' }), 'invalid URL', 'an unparseable URL is rejected');
await hasErr(company({ careers_url: 'ftp://example.com' }), 'unsupported URL protocol', 'a non-http(s) URL is rejected');
{
  const clean = await vp(company({ careers_url: 'https://acme.recruitee.com' }));
  clean === '' ? pass('a valid https careers_url raises nothing') : fail(`clean URL errored: ${clean}`);
}

// Keyword lists accept a bare string as well as an array, so both shapes have
// to reach the per-item checks.
await hasErr({ title_filter: 'nope' }, 'title_filter must be an object', 'a non-object title_filter is rejected');
await hasErr({ title_filter: { positive: [7] } }, 'keyword must be a string', 'a non-string keyword is rejected');
await hasErr({ title_filter: { negative: ['  '] } }, 'keyword must not be empty', 'a blank keyword is rejected');
await hasErr({ location_filter: { block: [null] } }, 'keyword must be a string', 'location_filter keywords are checked too');
await hasErr({ location_filter: 'nope' }, 'location_filter must be an object', 'a non-object location_filter is rejected');
await hasErr({ search_queries: 'nope' }, 'search_queries must be an array', 'a non-array search_queries is rejected');

// Parser: every optional field has its own guard, and each names its own path.
await hasErr(company({ parser: 'nope' }), 'parser must be an object', 'a non-object parser is rejected');
await hasErr(company({ parser: {} }), 'parser.command must be a non-empty string', 'a parser with no command is rejected');
await hasErr(company({ parser: { command: 'node', script: '' } }), 'parser.script must be a non-empty string', 'a blank parser.script is rejected');
await hasErr(company({ parser: { command: 'node', args: 'x' } }), 'parser.args must be an array', 'a non-array parser.args is rejected');
await hasErr(company({ parser: { command: 'node', timeout_ms: 0 } }), 'timeout_ms must be a positive number', 'a zero parser.timeout_ms is rejected');
await hasErr(company({ parser: { command: 'node', max_buffer_bytes: -1 } }), 'max_buffer_bytes must be a positive number', 'a negative parser.max_buffer_bytes is rejected');

// provider is checked against the real provider directory, so an unknown id is
// a typo rather than a new integration.
await hasErr(company({ provider: 'nosuch' }), 'unknown provider', 'an unknown provider id is rejected', { providerIds: new Set(['greenhouse']) });
await hasErr(company({ provider: '  ' }), 'provider must be a non-empty string', 'a blank provider is rejected');

// enabled:false skips the entry wholesale — a disabled company may be as
// malformed as it likes without failing the file.
{
  const skipped = await vp({ tracked_companies: [{ enabled: false, name: '', careers_url: 'not a url' }] });
  skipped === '' ? pass('a disabled entry is not validated at all') : fail(`disabled entry errored: ${skipped}`);
}

// Duplicates are a warning, not an error: two entries for one company still
// scan, they just scan twice.
{
  const dup = await validatePortalsConfig({ tracked_companies: [{ name: 'Acme' }, { name: '  acme  ' }] });
  const msg = dup.warnings.map(w => w.message).join(' ');
  if (!dup.errors.length && /duplicate enabled company name/.test(msg)) {
    pass('a duplicate company name warns rather than failing, and normalises case and spacing');
  } else fail(`duplicate handling: ${dup.errors.length} errors, warnings ${JSON.stringify(msg)}`);
}

{
  const notObj = await validatePortalsConfig('nope');
  /must be a YAML object/.test(notObj.errors.map(e => e.message).join(''))
    ? pass('a non-object config is rejected at the root')
    : fail('a scalar config was not rejected');
}

// ── cv-sync-check.mjs ────────────────────────────────────────────────────────
// The script resolves everything from its own __dirname, so copying it into a
// temp directory makes that directory the project root. That is the only way
// to exercise its failure branches: pointed at the real repo it reports the
// developer's actual setup, which is whatever it happens to be.

console.log('\n17c. cv-sync-check');

/**
 * Stand up a fixture project root and run the real checker against it.
 *
 * SNIPE_HOME rather than a copy of the script: c8 keys coverage on the file
 * path it executed, so running a copy out of tmpdir would leave the tracked
 * file reading as untested however thoroughly it was exercised.
 *
 * `mtimes` backdates a file, which is the only way to reach the
 * article-digest staleness branch without waiting a month.
 */
async function syncCheck(files, mtimes = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'snipe-sync-'));
  for (const [rel, body] of Object.entries(files)) {
    const abs = join(dir, rel);
    mkdirSync(join(abs, '..'), { recursive: true });
    writeFileSync(abs, body);
  }
  for (const [rel, daysAgo] of Object.entries(mtimes)) {
    const when = new Date(Date.now() - daysAgo * 86_400_000);
    utimesSync(join(dir, rel), when, when);
  }
  const r = await runNodeAsync([join(ROOT, 'cv-sync-check.mjs')], {
    env: { ...process.env, SNIPE_HOME: dir },
  });
  rmSync(dir, { recursive: true, force: true });
  return r;
}

const FULL_CV = '# Alex Fixture\n\n## Summary\n' + 'Backend engineer with a decade of service work. '.repeat(4);
const GOOD_PROFILE = 'full_name: "Alex Fixture"\nemail: alex@example.com\nlocation: Berlin\n';

{
  const r = await syncCheck({});
  if (r.code === 1 && /cv\.md not found/.test(r.out) && /profile\.yml not found/.test(r.out)) {
    pass('cv-sync-check reports both missing files and exits nonzero');
  } else fail(`empty project: exit ${r.code}, ${r.out.slice(0, 160)}`);
}

{
  const r = await syncCheck({ 'cv.md': '# Me\n', 'config/profile.yml': GOOD_PROFILE });
  if (r.code === 0 && /cv\.md seems too short/.test(r.out)) {
    pass('a stub cv.md warns without failing the check');
  } else fail(`short cv: exit ${r.code}, ${r.out.slice(0, 160)}`);
}

{
  // The example file ships full_name: "Jane Smith"; leaving it is the mistake
  // this check exists for, and it must fire even though every field is present.
  const r = await syncCheck({ 'cv.md': FULL_CV, 'config/profile.yml': 'full_name: "Jane Smith"\nemail: x\nlocation: y\n' });
  if (r.code === 0 && /may still have example data/.test(r.out)) {
    pass('an unedited profile.yml is caught by its example name, not by a missing field');
  } else fail(`example profile: exit ${r.code}, ${r.out.slice(0, 160)}`);
}

{
  const r = await syncCheck({
    'cv.md': FULL_CV, 'config/profile.yml': GOOD_PROFILE,
    'modes/_shared.md': 'Cut onboarding to 30 minutes across 170+ hours of work.\n',
  });
  if (/Possible hardcoded metric/.test(r.out) && /_shared\.md:1/.test(r.out)) {
    pass('a hardcoded metric in _shared.md is reported with its line number');
  } else fail(`metric scan: ${r.out.slice(0, 200)}`);
}

{
  // The instruction telling authors not to hardcode metrics contains a number
  // itself; flagging it would make the check cry wolf on its own rule.
  const r = await syncCheck({
    'cv.md': FULL_CV, 'config/profile.yml': GOOD_PROFILE,
    'modes/_shared.md': 'NEVER hardcode 170+ hours — read it from cv.md.\n# 90% is a heading\n',
  });
  if (!/Possible hardcoded metric/.test(r.out)) {
    pass('the "NEVER hardcode" line and headings are exempt from the metric scan');
  } else fail(`false positive on exempt lines: ${r.out.slice(0, 200)}`);
}

{
  const r = await syncCheck({ 'cv.md': FULL_CV, 'config/profile.yml': GOOD_PROFILE });
  if (r.code === 0 && /All checks passed/.test(r.out)) {
    pass('a complete setup passes clean');
  } else fail(`good setup: exit ${r.code}, ${r.out.slice(0, 160)}`);
}

{
  // article-digest.md is where the CV's metrics come from, so a stale one means
  // the evaluator is quoting numbers the projects have moved past. 30 days is
  // the threshold; 90 is comfortably over it.
  const r = await syncCheck(
    { 'cv.md': FULL_CV, 'config/profile.yml': GOOD_PROFILE, 'article-digest.md': '# Digest\n' },
    { 'article-digest.md': 90 },
  );
  if (/article-digest\.md is 90 days old/.test(r.out)) {
    pass('a stale article-digest.md is reported with its age');
  } else fail(`stale digest: ${r.out.slice(0, 200)}`);
}

{
  const r = await syncCheck(
    { 'cv.md': FULL_CV, 'config/profile.yml': GOOD_PROFILE, 'article-digest.md': '# Digest\n' },
    { 'article-digest.md': 3 },
  );
  if (r.code === 0 && /All checks passed/.test(r.out)) {
    pass('a recent article-digest.md raises nothing');
  } else fail(`fresh digest: exit ${r.code}, ${r.out.slice(0, 200)}`);
}
