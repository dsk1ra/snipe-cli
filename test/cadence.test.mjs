// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 12. FOLLOW-UP CADENCE LOGIC ─────────────────────────────────

console.log('\n12. Follow-up cadence logic');

try {
  const cadence = await import(pathToFileURL(join(ROOT, 'tracker/followup-cadence.mjs')).href);

  // CLI regression: the import.meta.url guard must still let the module run as a CLI.
  // Data-independent — default mode emits the result as JSON: a `metadata` object when
  // the tracker has applications, or an `{error}` object (exit 1) when it is empty.
  // Empty output would mean the guard wrongly suppressed main().
  let cliOut = '';
  try {
    cliOut = execFileSync(NODE, [join(ROOT, 'tracker/followup-cadence.mjs')], { cwd: ROOT, encoding: 'utf-8', timeout: 30000 });
  } catch (cliErr) {
    cliOut = `${cliErr.stdout || ''}`; // exit 1 on an empty tracker is expected; keep stdout
  }
  let cliJson = null;
  try { cliJson = JSON.parse(cliOut.trim()); } catch { /* leave null → fail below */ }
  if (cliJson && typeof cliJson === 'object' && ('metadata' in cliJson || 'error' in cliJson)) {
    pass('CLI still executes under the import.meta.url guard (emits result JSON)');
  } else {
    fail('CLI produced no structured JSON when run directly — import.meta.url guard may be broken');
  }

  // Date helpers
  if (cadence.addDays(cadence.parseDate('2026-05-01'), 7) === '2026-05-08') {
    pass('addDays advances a parsed date by N days (UTC)');
  } else {
    fail(`addDays produced ${cadence.addDays(cadence.parseDate('2026-05-01'), 7)}`);
  }
  if (cadence.daysBetween(cadence.parseDate('2026-05-01'), cadence.parseDate('2026-05-08')) === 7) {
    pass('daysBetween counts whole days between two dates');
  } else {
    fail('daysBetween miscounted');
  }
  if (cadence.parseDate('not-a-date') === null && cadence.parseDate('2026-05-01') instanceof Date) {
    pass('parseDate rejects malformed input and accepts ISO dates');
  } else {
    fail('parseDate validation wrong');
  }

  // Status normalization (strips bold + trailing date, lowercases, maps aliases)
  if (cadence.normalizeStatus('**Applied** 2026-05-01') === 'applied') {
    pass('normalizeStatus strips bold + trailing date and lowercases');
  } else {
    fail(`normalizeStatus produced ${cadence.normalizeStatus('**Applied** 2026-05-01')}`);
  }

  const cadenceTmp = mkdtempSync(join(tmpdir(), 'co-cadence-'));
  const profilePath = join(cadenceTmp, 'profile.yml');
  writeFileSync(profilePath, [
    'followup_cadence:',
    '  applied_first_days: 11',
    '  applied_subsequent_days: 5',
    '  applied_max_followups: 4',
    '  responded_initial_days: 2',
    '  responded_subsequent_days: 6',
    '  interview_thankyou_days: 3',
  ].join('\n'));

  const profileCadence = cadence.resolveCadenceConfig({ profilePath });
  if (
    profileCadence.applied_first === 11 &&
    profileCadence.applied_subsequent === 5 &&
    profileCadence.applied_max_followups === 4 &&
    profileCadence.responded_initial === 2 &&
    profileCadence.responded_subsequent === 6 &&
    profileCadence.interview_thankyou === 3
  ) {
    pass('follow-up cadence reads profile.yml overrides');
  } else {
    fail(`profile cadence override failed: ${JSON.stringify(profileCadence)}`);
  }

  const cliCadence = cadence.resolveCadenceConfig({ profilePath, appliedDays: 9 });
  if (cliCadence.applied_first === 9 && cliCadence.applied_subsequent === 5) {
    pass('follow-up cadence CLI override wins over profile applied_first');
  } else {
    fail(`CLI cadence override failed: ${JSON.stringify(cliCadence)}`);
  }

  const malformedProfile = join(cadenceTmp, 'malformed.yml');
  writeFileSync(malformedProfile, 'followup_cadence: [');
  const fallbackCadence = cadence.resolveCadenceConfig({ profilePath: malformedProfile });
  if (fallbackCadence.applied_first === cadence.DEFAULT_CADENCE.applied_first) {
    pass('follow-up cadence ignores malformed optional profile config');
  } else {
    fail(`malformed profile did not fall back to defaults: ${JSON.stringify(fallbackCadence)}`);
  }

  rmSync(cadenceTmp, { recursive: true, force: true });

  // Urgency decision tree (CADENCE defaults: applied_first=7, max_followups=2, responded_initial=1, interview_thankyou=1)
  const urgencyCases = [
    [['applied', 7, null, 0], 'overdue', 'applied past applied_first → overdue'],
    [['applied', 3, null, 0], 'waiting', 'applied within window → waiting'],
    [['applied', 30, null, 2], 'cold', 'applied at max follow-ups → cold'],
    [['responded', 0, null, 0], 'urgent', 'responded before responded_initial → urgent'],
    [['interview', 1, null, 0], 'overdue', 'interview past thank-you window → overdue'],
  ];
  for (const [args, expected, label] of urgencyCases) {
    const got = cadence.computeUrgency(...args);
    if (got === expected) pass(`computeUrgency: ${label}`);
    else fail(`computeUrgency ${label}: expected ${expected}, got ${got}`);
  }

  // Next follow-up date scheduling
  const nextCases = [
    [['applied', '2026-05-01', null, 0], '2026-05-08', 'first applied follow-up = appDate + applied_first'],
    [['applied', '2026-05-01', null, 2], null, 'cold (max follow-ups) → null'],
    [['interview', '2026-05-01', null, 0], '2026-05-02', 'interview = appDate + interview_thankyou'],
  ];
  for (const [args, expected, label] of nextCases) {
    const got = cadence.computeNextFollowupDate(...args);
    if (got === expected) pass(`computeNextFollowupDate: ${label}`);
    else fail(`computeNextFollowupDate ${label}: expected ${expected}, got ${got}`);
  }

  // The branches the cases above skip. Each is a decision about whether the user
  // chases a real employer today, so a wrong one is either a missed follow-up or
  // a second nudge that reads as pestering.
  const moreUrgency = [
    [['applied', 30, 7, 1], 'overdue', 'a follow-up sent applied_subsequent days ago is due again'],
    [['applied', 30, 2, 1], 'waiting', 'and is not due before that'],
    [['responded', 5, null, 0], 'overdue', 'a reply left responded_subsequent days without an answer is overdue'],
    [['responded', 2, null, 0], 'waiting', 'and inside that window it is merely waiting'],
    [['interview', 0, null, 0], 'waiting', 'an interview today has not missed its thank-you yet'],
    [['rejected', 99, null, 0], 'waiting', 'a non-actionable status never becomes urgent'],
  ];
  for (const [args, expected, label] of moreUrgency) {
    const got = cadence.computeUrgency(...args);
    if (got === expected) pass(`computeUrgency: ${label}`);
    else fail(`computeUrgency ${label}: expected ${expected}, got ${got}`);
  }

  const moreNext = [
    [['applied', '2026-05-01', '2026-05-10', 1], '2026-05-17', 'a subsequent follow-up is counted from the last one, not the application'],
    [['applied', '2026-05-01', null, 1], '2026-05-08', 'a counted follow-up with no recorded date falls back to the application date'],
    [['responded', '2026-05-01', null, 0], '2026-05-04', 'a reply is chased responded_subsequent days after applying'],
    [['responded', '2026-05-01', '2026-05-10', 1], '2026-05-13', 'and after the last follow-up once there is one'],
    [['rejected', '2026-05-01', null, 0], null, 'a non-actionable status is never scheduled'],
  ];
  for (const [args, expected, label] of moreNext) {
    const got = cadence.computeNextFollowupDate(...args);
    if (got === expected) pass(`computeNextFollowupDate: ${label}`);
    else fail(`computeNextFollowupDate ${label}: expected ${expected}, got ${got}`);
  }
} catch (e) {
  fail(`follow-up cadence module crashed: ${e.message}`);
}

// ── The dashboard itself, against a fixture tracker ──────────────────────────
// SNIPE_TRACKER points the script at a fixture; SNIPE_PROFILE at a path that does
// not exist, so the developer's own cadence overrides cannot change the answers.
try {
  const tmp = mkdtempSync(join(tmpdir(), 'co-cadence-cli-'));
  const daysAgo = (n) => new Date(Date.now() - n * 86400000).toISOString().split('T')[0];
  const header = '# Applications Tracker\n\n'
    + '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n'
    + '|---|------|---------|------|-------|--------|-----|--------|-------|\n';
  const row = (num, date, company, status, notes) =>
    `| ${num} | ${date} | ${company} | Engineer | 4.2/5 | ${status} | Y | [${num}](reports/${num}-x-${date}.md) | ${notes} |\n`;

  // App numbers are deliberately high: follow-ups.md is not redirectable, so a
  // low number could collide with the developer's real follow-up rows and change
  // the count this asserts on.
  const tracker = join(tmp, 'applications.md');
  writeFileSync(tracker, header
    + row(9001, daysAgo(30), 'Overdue Ltd', 'Applied', 'Emailed Jane Doe at jane@overdue.example — no reply')
    + row(9002, daysAgo(0), 'Fresh GmbH', 'Applied', 'submitted via portal')
    + row(9003, daysAgo(0), 'Replied Inc', 'Responded', 'recruiter replied')
    + row(9004, daysAgo(99), 'Rejected SA', 'Rejected', 'closed')
    + '| not a number | x | y | z | | | | | |\n'
    + 'a line that is not a row at all\n');
  const env = { ...process.env, SNIPE_TRACKER: tracker, SNIPE_PROFILE: join(tmp, 'no-profile.yml') };
  const cli = (args) => {
    try { return execFileSync(NODE, [join(ROOT, 'tracker/followup-cadence.mjs'), ...args], { cwd: ROOT, encoding: 'utf-8', timeout: 30000, env }); }
    catch (e) { return `${e.stdout || ''}`; }
  };

  const json = JSON.parse(cli([]));
  if (json.metadata.totalTracked === 4 && json.metadata.actionable === 3) {
    pass('a rejected row is tracked but not actionable, and junk rows are skipped');
  } else fail(`cadence CLI counted ${JSON.stringify(json.metadata)}`);
  if (json.entries[0].urgency === 'urgent' && json.entries[0].company === 'Replied Inc') {
    pass('entries are sorted urgent first, so the dashboard leads with what to do now');
  } else fail(`cadence CLI sorted wrong: ${json.entries.map(e => e.urgency).join(',')}`);
  const overdue = json.entries.find(e => e.company === 'Overdue Ltd');
  if (overdue.urgency === 'overdue' && overdue.daysSinceApplication === 30) {
    pass('an application 30 days old with no follow-up is overdue');
  } else fail(`30-day-old application read as ${JSON.stringify(overdue.urgency)}`);
  if (overdue.contacts[0]?.email === 'jane@overdue.example' && overdue.contacts[0]?.name === 'Jane Doe') {
    pass('a contact name is lifted out of the note alongside the address');
  } else fail(`contact extraction gave ${JSON.stringify(overdue.contacts)}`);
  if (overdue.reportPath === null) pass('a report link pointing at nothing resolves to null, not a broken path');
  else fail(`reportPath was ${overdue.reportPath}`);

  const only = JSON.parse(cli(['--overdue-only']));
  if (only.entries.length === 2 && only.metadata.actionable === 3) {
    pass('--overdue-only filters the list but still reports the real actionable total');
  } else fail(`--overdue-only gave ${only.entries.length} entries`);

  const relaxed = JSON.parse(cli(['--applied-days', '60']));
  if (relaxed.entries.find(e => e.company === 'Overdue Ltd').urgency === 'waiting') {
    pass('--applied-days widens the window end to end, not just in the config object');
  } else fail('--applied-days did not reach the urgency decision');

  const summary = cli(['--summary']);
  if (/Follow-up Cadence Dashboard —/.test(summary) && /4 total applications, 3 actionable/.test(summary)) {
    pass('--summary prints the dashboard header');
  } else fail(`--summary header wrong:\n${summary}`);
  if (/1 urgent \| 1 overdue \| 1 waiting \| 0 cold/.test(summary)) pass('and the urgency counts');
  else fail(`--summary counts wrong:\n${summary}`);
  if (/9001\s+Overdue Ltd\s+applied\s+30\s+0\s+\S+\s+OVERDUE\s+jane@overdue\.example/.test(summary)) {
    pass('and one padded row per entry, ending in the contact to write to');
  } else fail(`--summary row wrong:\n${summary}`);

  // Both of printSummary's early exits: nothing actionable, and no tracker at all.
  writeFileSync(tracker, header + row(9004, daysAgo(99), 'Rejected SA', 'Rejected', 'closed'));
  if (/No active applications to track/.test(cli(['--summary']))) {
    pass('--summary with nothing actionable says so instead of printing an empty table');
  } else fail('--summary printed a table with no entries');
  writeFileSync(tracker, header);
  if (/No applications found in tracker/.test(cli(['--summary']))) {
    pass('--summary on an empty tracker reports the error rather than crashing');
  } else fail('--summary did not report an empty tracker');

  rmSync(tmp, { recursive: true, force: true });
} catch (e) {
  fail(`follow-up cadence dashboard tests crashed: ${e.message}`);
}


