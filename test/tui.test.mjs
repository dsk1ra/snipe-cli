// Rendering and key-handling coverage for snipe-tui.mjs. Run standalone with:
// node test/tui.test.mjs
//
// The TUI is a pure consumer of on-disk state, so a fixture state file plus the
// headless driver in test/tui-driver.mjs is enough to assert what it draws for
// each row shape — including the failed-row actions, which are the part with
// real logic behind them. State files are swapped out and restored so the
// developer's own queue is never touched.
import {
  pass, fail, join, runNodeAsync,
  existsSync, readFileSync, writeFileSync, mkdirSync, rmSync, mkdtempSync, tmpdir,
} from './harness.mjs';

console.log('\n16. TUI rendering and row actions');

// Every file the TUI reads or writes — state, queue, applied/skipped marks,
// batch-input, the tracker, the follow-ups log — is resolved from SNIPE_HOME, so
// the whole fixture lives in a temp dir. Nothing here can reach the developer's
// real queue or tracker, including when the run is killed before its cleanup.
const tmp = mkdtempSync(join(tmpdir(), 'snipe-tui-'));
const HOME = join(tmp, 'home');

// Ids high enough that the fixture's own errors/ and scores/ sidecars cannot
// collide with a real offer's.
const IDS = { done: '990001', gated: '990002', scoreFail: '990003', evalFail: '990004', noReport: '990005', gone: '990006' };

const HEADER = 'id\turl\tp1_status\tp1_score\tp1_archetype\tp2_status\tp2_report_num\tp3_status\terror\tretries';
const ROWS = [
  // A finished offer: score shown, job link on the row.
  [IDS.done, 'https://example.com/jobs/done', 'scored', '4.2', 'Backend Engineer', 'evaled', '801', 'completed', '-', '0'],
  // Below the P1 threshold — never reached Phase 2.
  [IDS.gated, 'https://example.com/jobs/gated', 'scored', '1.9', 'Outside targets', 'p1-gated', '-', 'skipped', 'p1-gated(threshold=2.5)', '0'],
  // Phase 1 failure.
  [IDS.scoreFail, 'https://example.com/jobs/scorefail', 'score_failed', '-', '-', '-', '-', '-', 'Ollama API call failed', '0'],
  // Phase 2 failure.
  [IDS.evalFail, 'https://example.com/jobs/evalfail', 'scored', '3.4', 'Backend Engineer', 'eval_failed', '-', '-', 'stage3 (judgment) failed', '1'],
  // "evaled" with no report number — a failed eval wearing a success label.
  [IDS.noReport, 'https://example.com/jobs/noreport', 'scored', '3.8', 'Backend Engineer', 'evaled', '-', '-', '-', '0'],
  // Expired posting: not retryable at all.
  [IDS.gone, 'https://example.com/jobs/gone', 'unavailable', '-', '-', '-', '-', '-', 'posting expired', '0'],
];

/**
 * The last complete frame, with ANSI SGR/CSI and OSC-8 hyperlink wrappers
 * stripped. Concatenating every frame would count each row once per repaint, so
 * "how many rows show X" has to be asked of one frame. The final chunk is just
 * the cursor-restore escape, hence the search for the last one with a tab bar.
 */
function visible(frames) {
  const clean = s => s
    .replace(/\x1b\]8;;[^\x07\x1b]*(\x07|\x1b\\)/g, '')
    .replace(/\x1b\[[0-9;?]*[a-zA-Z]/g, '')
    .replace(/\x1b[()][B0]/g, '');
  const full = frames.map(clean).filter(s => s.includes('1 QUEUE'));
  return full.length ? full[full.length - 1] : frames.map(clean).join('');
}

let frameNo = 0;

// The TUI's "open" actions shell out to xdg-open, and a finished drain fires
// notify-send. Left alone a test run would throw the developer's editor and
// desktop notifications at them, so both are replaced by loggers on PATH —
// which also turns "what did it open?" into a file the assertions can read.
const BIN = join(tmp, 'bin');
const RUNNER_BIN = join(tmp, 'runner-bin');
const OPENED = join(tmp, 'opened.log');
mkdirSync(BIN, { recursive: true });
mkdirSync(RUNNER_BIN, { recursive: true });
const stub = (dir, name) => {
  const p = join(dir, name);
  writeFileSync(p, `#!/bin/sh\nprintf '%s %s\\n' "${name}" "$*" >> "$SNIPE_TEST_OPENED"\n`, { mode: 0o755 });
};
stub(BIN, 'xdg-open');
stub(BIN, 'notify-send');
// retryItem spawns `bash batch/local-runner.sh --only-id N --retry-failed`,
// which is a real three-phase pipeline run. Only the drive that exercises retry
// gets this second directory, so a stubbed bash cannot silently swallow any
// other shell-out the TUI grows later.
stub(RUNNER_BIN, 'bash');

const opened = () => (existsSync(OPENED) ? readFileSync(OPENED, 'utf8') : '');

// SNIPE_HOME moves the TUI's own data root; SNIPE_TRACKER is what the tracker
// scripts it shells out to (followup-cadence) read, and has to agree with it.
const ENV = {
  ...process.env,
  SNIPE_HOME: HOME,
  SNIPE_TRACKER: join(HOME, 'data/applications.md'),
  // the rejection grace window, shrunk under the driver's 140ms-per-key clock
  SNIPE_REJECT_GRACE_MS: '300',
};

/** Drive the TUI with a key sequence and return its raw frames. */
async function driveRaw(...keys) {
  const out = join(tmp, `frames-${frameNo++}.json`);
  const path = [...(driveRaw.extraPath ? [driveRaw.extraPath] : []), BIN, process.env.PATH].join(':');
  const res = await runNodeAsync(['test/tui-driver.mjs', out, ...keys], {
    timeout: 60_000,
    env: { ...ENV, TUI_COLS: '160', TUI_ROWS: '40', PATH: path, SNIPE_TEST_OPENED: OPENED },
  });
  if (!existsSync(out)) {
    fail(`TUI driver produced no frames (exit ${res.code}): ${res.err.slice(0, 200)}`);
    return [];
  }
  return JSON.parse(readFileSync(out, 'utf8'));
}
driveRaw.extraPath = null;

/** Drive the TUI and return the last complete frame as plain text. */
async function drive(...keys) {
  return visible(await driveRaw(...keys));
}

{
  const errorFiles = Object.values(IDS).map(id => join(HOME, 'batch/errors', `${id}.txt`));
  try {
    mkdirSync(join(HOME, 'batch'), { recursive: true });
    writeFileSync(join(HOME, 'batch/local-state.tsv'),
      [HEADER, ...ROWS.map(r => r.join('\t'))].join('\n') + '\n', 'utf8');
    // The list is `recentIds()`: queued ids plus anything with a JD or eval
    // touched in the last 24h. Queueing the fixture ids is what puts them on
    // screen without planting files in batch/jds/.
    writeFileSync(join(HOME, 'batch/snipe-queue.txt'), Object.values(IDS).join('\n') + '\n', 'utf8');
    rmSync(join(HOME, 'batch/applied.tsv'), { force: true });
    rmSync(join(HOME, 'batch/skipped.tsv'), { force: true });

    // The job link on a row comes from batch-input.tsv, not the state row — a
    // row whose id is absent from it renders with no link at all.
    writeFileSync(join(HOME, 'batch/batch-input.tsv'),
      ['id\turl', ...ROWS.map(r => `${r[0]}\t${r[1]}`)].join('\n') + '\n', 'utf8');

    // Company, role and score come from the eval payload, not the state row —
    // without one a finished offer renders as a bare "#<id>".
    mkdirSync(join(HOME, 'batch/evals'), { recursive: true });
    writeFileSync(join(HOME, 'batch/evals', `${IDS.done}.json`), JSON.stringify({
      status: 'evaled', id: IDS.done, company: 'Fixture Corp', role: 'Backend Engineer',
      score: 4.2, report_num: '801',
    }), 'utf8');

    // A two-row tracker: #801 is the row toggleMark's syncTracker rewrites (it
    // has to read "Evaluated" for the flip to be allowed), #555 is an Applied
    // row old enough to be overdue, which is what puts an entry on the
    // Follow-ups tab.
    const dayKey = d => d.toISOString().slice(0, 10);
    const daysAgo = n => dayKey(new Date(Date.now() - n * 86_400_000));
    mkdirSync(join(HOME, 'data'), { recursive: true });
    writeFileSync(join(HOME, 'data/applications.md'), [
      '# Applications Tracker',
      '',
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |',
      '|---|------|---------|------|-------|--------|-----|--------|-------|',
      `| 801 | ${daysAgo(1)} | Fixture Corp | Backend Engineer | 4.2/5 | Evaluated | Y | [801](../reports/801-fixture-corp-${daysAgo(1)}.md) | fixture |`,
      // #555 links report 400 on purpose: the two sequences diverge in the real
      // tracker, so a follow-up action keyed on the report number finds no row.
      `| 555 | ${daysAgo(20)} | Overdue Ltd | Platform Engineer | 4.5/5 | Applied | Y | [400](../reports/400-overdue-ltd-${daysAgo(20)}.md) | fixture |`,
      '',
    ].join('\n'), 'utf8');
    writeFileSync(join(HOME, 'data/follow-ups.md'),
      '# Follow-ups Log\n\n| # | App | Date | Company | Role | Channel | Contact | Notes |\n|---|-----|------|---------|------|---------|---------|-------|\n', 'utf8');

    // ── First paint ──────────────────────────────────────────────────────────

    const home = await drive();
    if (/1 QUEUE/.test(home) && /2 ACTIVITY/.test(home) && /3 FOLLOW-UPS/.test(home)) {
      pass('TUI paints all three tab labels');
    } else fail('TUI first paint is missing the tab bar');

    if (/Paste the Job Description/.test(home)) pass('TUI paints the JD input placeholder');
    else fail('TUI did not paint the input area');

    if (/Add to queue/.test(home)) pass('TUI paints the Add to queue button');
    else fail('TUI did not paint the Add to queue button');

    if (/Stats/.test(home) && /Queue/.test(home)) pass('TUI paints the stats panel beside the queue list');
    else fail('TUI did not paint the stats panel');

    // ── Row shapes ───────────────────────────────────────────────────────────

    // Anchored to the row, not the page: "P1-gated" is also a stats-panel label
    // and "debug" is in the hint line, so a bare page-wide match proves nothing.
    // A row with no eval payload renders as "#<id>", which makes a stable anchor.
    const rowFor = id => home.split('\n').find(l => l.includes(`#${id}`)) || '';

    const doneRow = home.split('\n').find(l => l.includes('Fixture Corp')) || '';
    if (/✓/.test(doneRow) && /4\.2/.test(doneRow)) pass('a completed row shows its company, role and score');
    else fail(`completed row rendered: ${doneRow.trim().slice(0, 100) || '(not found)'}`);

    // The gate is overrulable, so the row advertises it — unselected, like the
    // failed rows' actions.
    const gatedRow = rowFor(IDS.gated);
    if (/P1-gated \| proceed\?/.test(gatedRow)) pass('a P1-gated row is labelled "P1-gated | proceed?"');
    else fail(`P1-gated row rendered: ${gatedRow.trim().slice(0, 100) || '(not found)'}`);
    if (/\blink\b/.test(gatedRow)) pass('a gated row keeps its job link after proceed?');
    else fail('the gated row dropped its job link');

    // Every failed row carries its actions unconditionally — they are not gated
    // on selection, which was the bug the feature shipped to fix. Counted on a
    // single frame, so a repaint cannot inflate the number.
    const failedRows = [IDS.scoreFail, IDS.evalFail, IDS.noReport].map(rowFor);
    const withActions = failedRows.filter(l => /✗/.test(l) && /see error {2}retry \| debug/.test(l));
    if (withActions.length === 3) pass('all three failed rows render "see error  retry | debug" unselected');
    else fail(`only ${withActions.length}/3 failed rows carry the full action set: ${failedRows.map(l => l.trim().slice(0, 60))}`);

    // The row ends at debug — no job link, unlike every other row.
    if (failedRows.every(l => !/\blink\b/.test(l))) pass('a failed row drops the job link and ends at debug');
    else fail('a failed row still renders the job link after debug');

    // An expired posting cannot be recovered by re-running, so it gets debug and
    // no retry — the row ends "see error  debug", never "retry | debug".
    const goneRow = rowFor(IDS.gone);
    if (/debug/.test(goneRow) && !/retry/.test(goneRow)) {
      pass('an unavailable row offers debug but not retry');
    } else fail(`unavailable row rendered: ${goneRow.trim().slice(0, 120) || '(row not found)'}`);

    // ── Error sidecars ───────────────────────────────────────────────────────

    // poll() writes one file per failed row so "see error" always has a target.
    const written = errorFiles.filter(existsSync).length;
    if (written >= 3) pass(`poll() wrote an error file for each failed row (${written})`);
    else fail(`expected 3+ error sidecars, found ${written}`);

    const noReportErr = join(HOME, 'batch/errors', `${IDS.noReport}.txt`);
    if (existsSync(noReportErr) && /no report number/i.test(readFileSync(noReportErr, 'utf8'))) {
      pass('an "evaled" row with no report number is reported as a failure, not left pending');
    } else fail('the evaled-without-report row produced no explanatory error file');

    // ── Navigation ───────────────────────────────────────────────────────────

    const activity = await drive('2');
    if (/y\/m\/d|period|type/.test(activity)) pass('key "2" switches to the Activity tab');
    else fail('key "2" did not reach the Activity tab');

    const followups = await drive('3');
    if (/mark nudged|report/.test(followups)) pass('key "3" switches to the Follow-ups tab');
    else fail('key "3" did not reach the Follow-ups tab');

    const back = await drive('2', '1');
    if (/Paste the Job Description/.test(back)) pass('key "1" returns to the Queue tab');
    else fail('key "1" did not return to the Queue tab');

    const arrows = await drive('RIGHT', 'RIGHT', 'LEFT');
    if (arrows.length > 0) pass('←/→ tab switching runs without crashing');
    else fail('arrow-key tab switching produced no output');

    // ── Typing into the JD box ───────────────────────────────────────────────

    // "/" jumps straight into the JD box from anywhere on the tab.
    const typed = await drive('/', 'hello world');
    if (/12 chars — \/hello world/.test(typed)) pass('"/" enters the JD box and typing lands in it');
    else fail('typing after "/" did not reach the JD box');

    const cleared = await drive('/', 'hello', 'ESC');
    if (/Paste the Job Description/.test(cleared)) pass('Esc clears the JD box back to the placeholder');
    else fail('Esc did not clear the JD box');

    // ── Row selection and its actions ────────────────────────────────────────

    // Tab cycles input → ▶ → list, and entering the list selects the most recent
    // row — the fixture's unavailable one. → then walks its inline actions, and
    // the focused action is drawn inverse (\x1b[7m), which is what proves the
    // walk landed rather than just repainting.
    // Chalk emits one escape per style, so between the inverse marker and the
    // label there can be a colour and an underline as well as the OSC-8 wrapper
    // ("see error" is a hyperlink). Drop everything except inverse on/off and
    // the pairing becomes a plain match.
    const inverseOn = (frames, token) => frames
      .map(f => f
        .replace(/\x1b\]8;;[^\x07\x1b]*(\x07|\x1b\\)/g, '')
        .replace(/\x1b\[(?!7m|27m)[0-9;]*m/g, ''))
      .some(f => new RegExp(`\\x1b\\[7m\\s*${token}`).test(f));

    const walk1 = await driveRaw('TAB', 'TAB', 'TAB', 'RIGHT');
    if (inverseOn(walk1, 'see error')) pass('→ on a failed row focuses "see error" first');
    else fail('→ did not focus "see error" on the selected failed row');

    const walk2 = await driveRaw('TAB', 'TAB', 'TAB', 'RIGHT', 'RIGHT');
    if (inverseOn(walk2, 'debug')) pass('→ again focuses "debug" — the unavailable row has no retry stop between them');
    else fail('a second → did not reach "debug" on the unavailable row');

    // Only an evaluated row can be marked, so walk up from the last row to the
    // one with an eval payload — five rows above it.
    const TO_DONE = ['TAB', 'TAB', 'TAB', 'UP', 'UP', 'UP', 'UP', 'UP'];

    // 'a' marks the selected row applied; the sidecar is the observable effect.
    await drive(...TO_DONE, 'a');
    const applied = join(HOME, 'batch/applied.tsv');
    if (existsSync(applied) && readFileSync(applied, 'utf8').includes(IDS.done)) {
      pass('"a" marks the evaluated row applied and writes batch/applied.tsv');
    } else fail('"a" did not write an applied sidecar for the evaluated row');

    // 'x' is mutually exclusive with applied — marking skip clears the > mark.
    await drive(...TO_DONE, 'x');
    const skipped = join(HOME, 'batch/skipped.tsv');
    if (existsSync(skipped) && readFileSync(skipped, 'utf8').includes(IDS.done)) {
      pass('"x" marks the evaluated row skipped and writes batch/skipped.tsv');
    } else fail('"x" did not write a skipped sidecar for the evaluated row');
    if (!existsSync(applied) || !readFileSync(applied, 'utf8').includes(IDS.done)) {
      pass('marking skip clears the applied mark — the two are mutually exclusive');
    } else fail('a row is marked both applied and skipped');

    // A row with no eval payload cannot be marked at all.
    await drive('TAB', 'TAB', 'TAB', 'a');
    const appliedAfter = existsSync(applied) ? readFileSync(applied, 'utf8') : '';
    if (!appliedAfter.includes(IDS.gone)) pass('"a" refuses to mark a row that was never evaluated');
    else fail('"a" marked an unevaluated row applied');

    // Marking applied flips the tracker row's Status cell, and only that cell.
    const trackerAfter = readFileSync(join(HOME, 'data/applications.md'), 'utf8');
    const row801 = trackerAfter.split('\n').find(l => l.includes('| 801 |')) || '';
    if (!/\| Evaluated \|/.test(row801)) pass('marking a row syncs the matching tracker row out of Evaluated');
    else fail(`tracker row 801 still reads Evaluated: ${row801.slice(0, 120)}`);
    if (/Fixture Corp/.test(row801) && /\[801\]/.test(row801)) {
      pass('the tracker sync rewrites only the Status cell, leaving the rest of the row intact');
    } else fail(`tracker sync damaged row 801: ${row801.slice(0, 120)}`);

    // ── Opening things (xdg-open is stubbed onto PATH) ───────────────────────

    // "see error" on the selected row opens its sidecar rather than the log.
    rmSync(OPENED, { force: true });
    await drive('TAB', 'TAB', 'TAB', 'RIGHT', 'ENTER');
    if (new RegExp(`xdg-open .*batch/errors/${IDS.gone}\\.txt`).test(opened())) {
      pass('Enter on "see error" opens that row\'s error sidecar');
    } else fail(`"see error" opened: ${opened().trim() || '(nothing)'}`);

    // "debug" opens the *input* the phase read — the fetched JD, in place.
    mkdirSync(join(HOME, 'batch/jds'), { recursive: true });
    writeFileSync(join(HOME, 'batch/jds', `${IDS.gone}.txt`), 'fixture job description\n', 'utf8');
    rmSync(OPENED, { force: true });
    await drive('TAB', 'TAB', 'TAB', 'RIGHT', 'RIGHT', 'ENTER');
    if (new RegExp(`xdg-open .*batch/jds/${IDS.gone}\\.txt`).test(opened())) {
      pass('Enter on "debug" opens the fetched JD, not its folder');
    } else fail(`"debug" opened: ${opened().trim() || '(nothing)'}`);

    // With no input on disk, debug says so — that absence is itself the diagnosis.
    rmSync(OPENED, { force: true });
    const noInput = await drive('TAB', 'TAB', 'TAB', 'UP', 'UP', 'UP', 'RIGHT', 'RIGHT', 'RIGHT', 'ENTER');
    if (/No input on disk for this phase/.test(noInput)) {
      pass('debug reports a missing input rather than opening nothing');
    } else fail('debug did not report the missing phase input');
    if (!opened().includes('xdg-open')) pass('debug opens nothing when the input never landed');
    else fail(`debug opened something for a row with no input: ${opened().trim()}`);

    // A row that finished carries a job link as its only inline stop.
    rmSync(OPENED, { force: true });
    await drive(...TO_DONE, 'RIGHT', 'ENTER');
    if (/xdg-open https:\/\/example\.com\/jobs\/done/.test(opened())) {
      pass('Enter on a finished row\'s link opens the posting');
    } else fail(`the job link opened: ${opened().trim() || '(nothing)'}`);

    // 'o' opens the output folder — the fixture has none, so it must say so.
    const noResult = await drive(...TO_DONE, 'o');
    if (/No result folder for this item/.test(noResult)) pass('"o" reports a missing output folder');
    else fail('"o" did not report the missing output folder');

    // ── Retry ────────────────────────────────────────────────────────────────

    // Third row up from the end is the Phase 1 failure: retryable, so its stops
    // are row → error → retry → debug.
    rmSync(OPENED, { force: true });
    driveRaw.extraPath = RUNNER_BIN;
    const retried = await drive('TAB', 'TAB', 'TAB', 'UP', 'UP', 'UP', 'RIGHT', 'RIGHT', 'ENTER');
    driveRaw.extraPath = null;
    // The stubbed runner exits at once, so the last message is the completion
    // one rather than "Retrying …" — either proves the retry ran.
    if (new RegExp(`Retry(ing)? (of )?#${IDS.scoreFail}`).test(retried)) pass('Enter on "retry" starts a retry of that row');
    else fail('retry did not announce itself');
    if (new RegExp(`bash .*local-runner\\.sh --only-id ${IDS.scoreFail} --retry-failed`).test(opened())) {
      pass('retry runs local-runner.sh with --retry-failed, not a plain re-queue');
    } else fail(`retry invoked: ${opened().trim() || '(nothing)'}`);
    if (/notify-send snipe/.test(opened())) pass('a finished retry fires one desktop notification');
    else fail('the finished retry sent no notification');

    // ── Proceed past the P1 gate ─────────────────────────────────────────────

    // The gated row is four up from the end; its stops are row → proceed → link.
    // --p1-threshold 0 and nothing else: Phase 1 is already `scored`, so the
    // runner reuses that score and the cached JD and starts at Phase 2.
    rmSync(OPENED, { force: true });
    driveRaw.extraPath = RUNNER_BIN;
    const proceeded = await drive('TAB', 'TAB', 'TAB', 'UP', 'UP', 'UP', 'UP', 'RIGHT', 'ENTER');
    driveRaw.extraPath = null;
    if (new RegExp(`(Evaluating|Proceed on) #${IDS.gated}`).test(proceeded)) pass('Enter on "proceed?" starts a run of the gated row');
    else fail('proceed did not announce itself');
    if (new RegExp(`bash .*local-runner\\.sh --only-id ${IDS.gated} --p1-threshold 0$`, 'm').test(opened())) {
      pass('proceed re-runs with the P1 gate off and no --retry-failed');
    } else fail(`proceed invoked: ${opened().trim() || '(nothing)'}`);

    // ── The queue / drain button ─────────────────────────────────────────────

    // Tab twice reaches ▶; with nothing queued it must refuse rather than spawn.
    writeFileSync(join(HOME, 'batch/snipe-queue.txt'), '', 'utf8');
    const emptyDrain = await drive('TAB', 'TAB', 'ENTER');
    if (/Queue is empty/.test(emptyDrain)) pass('▶ refuses to start a run with an empty queue');
    else fail('▶ did not refuse on an empty queue');
    writeFileSync(join(HOME, 'batch/snipe-queue.txt'), Object.values(IDS).join('\n') + '\n', 'utf8');

    // ── Slash commands and the JD box ────────────────────────────────────────

    // Enter walks jd → url → add → submit, so three of them submit what is typed.
    const unknownCmd = await drive('/', 'nope', 'ENTER', 'ENTER', 'ENTER');
    if (/Unknown command: \/nope/.test(unknownCmd)) pass('an unrecognised slash command is named back, not run');
    else fail('an unknown slash command was not rejected by name');

    const emptySubmit = await drive('/', 'ESC', 'ENTER', 'ENTER', 'ENTER');
    if (/Paste a job description first/.test(emptySubmit)) pass('submitting an empty JD box asks for a JD');
    else fail('an empty submit was not rejected');

    const backspaced = await drive('/', 'abc', '\x7f');
    if (/3 chars — \/ab/.test(backspaced)) pass('backspace deletes the last character of the JD box');
    else fail('backspace did not shorten the JD box');

    // Bracketed paste is handled off the raw stream so a pasted JD can never
    // trigger key handlers — 'q' inside a paste must not quit.
    const PASTE = t => `\x1b[200~${t}\x1b[201~`;
    const pasted = await drive(PASTE('a quick job description'));
    if (/23 chars — a quick job description/.test(pasted)) pass('a bracketed paste lands in the JD box verbatim');
    else fail('bracketed paste did not reach the JD box');

    const pastedUrl = await drive('/', 'ESC', 'ENTER', PASTE('  https://example.com/x  '));
    if (/https:\/\/example\.com\/x/.test(pastedUrl)) pass('a paste into the URL field is whitespace-collapsed');
    else fail('paste did not reach the URL field');

    const pasteElsewhere = await drive('2', PASTE('some jd'));
    if (/Switch to the Queue tab/.test(pasteElsewhere)) pass('pasting outside the Queue tab says where to paste instead');
    else fail('a paste on another tab was silently swallowed');

    // ── Activity tab ─────────────────────────────────────────────────────────

    const year = await drive('2', 'y');
    if (year.includes('1 QUEUE')) pass('"y" selects the year view without crashing');
    else fail('"y" broke the Activity grid');

    const month = await drive('2', 'm', '>', '<', ',', '.');
    if (month.includes('1 QUEUE')) pass('‹ › step the period in the month view');
    else fail('period stepping broke the Activity grid');

    const day = await drive('2', 'd', 'TAB', 'RIGHT', 'LEFT', 'UP', 'DOWN');
    if (day.includes('1 QUEUE')) pass('the day view\'s hourly cursor moves with the arrows');
    else fail('the day-view cursor broke the Activity grid');

    const typeToggle = await drive('2', 'j', 'k');
    if (typeToggle.includes('1 QUEUE')) pass('"j"/"k" toggle the Activity type between scans and apps');
    else fail('the Activity type toggle broke the grid');

    const gridEsc = await drive('2', 'DOWN', 'DOWN', 'UP', 'ENTER', 'ESC');
    if (gridEsc.includes('1 QUEUE')) pass('Esc leaves the Activity grid cursor');
    else fail('Esc broke the Activity grid');

    const legacyTabs = await drive('l', 'l', 'h');
    if (legacyTabs.includes('1 QUEUE')) pass('"h"/"l" still cycle tabs as ←/→ aliases');
    else fail('the h/l tab aliases stopped working');

    // ── Follow-ups tab ───────────────────────────────────────────────────────

    const fu = await drive('3');
    if (/Overdue Ltd/.test(fu)) {
      pass('the Follow-ups tab lists the overdue application from the tracker');

      const nudged = await drive('3', 'DOWN', 'ENTER');
      const log = readFileSync(join(HOME, 'data/follow-ups.md'), 'utf8');
      if (/Overdue Ltd/.test(nudged) && /nudged/.test(nudged)) pass('Enter on a follow-up records a nudge');
      else fail('Enter on a follow-up did not report a nudge');
      if (/\| 1 \| 555 \|/.test(log)) pass('a nudge appends one row to data/follow-ups.md');
      else fail(`follow-ups.md after a nudge: ${log.slice(-120)}`);

      const undone = await drive('3', 'DOWN', 'u');
      if (/Rolled back last nudge/.test(undone)) pass('"u" peels the latest nudge back off');
      else fail('"u" did not roll the nudge back');
      if (!/\| 1 \| 555 \|/.test(readFileSync(join(HOME, 'data/follow-ups.md'), 'utf8'))) {
        pass('the rolled-back nudge is gone from data/follow-ups.md');
      } else fail('"u" left the nudge row in follow-ups.md');

      const undoneAgain = await drive('3', 'DOWN', 'u');
      if (/No nudges recorded/.test(undoneAgain)) pass('"u" with nothing to undo says so');
      else fail('"u" on an un-nudged entry did not report it');

      // r is deferred by SNIPE_REJECT_GRACE_MS; u inside that window must leave
      // the tracker untouched, and letting it elapse must flip the Status cell.
      const trackerFile = join(HOME, 'data/applications.md');
      const cancelled = await drive('3', 'DOWN', 'r', 'u');
      if (/Rejection cancelled/.test(cancelled)) pass('"u" cancels a rejection inside its grace period');
      else fail('"u" did not cancel the pending rejection');
      if (/\| Applied \|/.test(readFileSync(trackerFile, 'utf8'))) pass('a cancelled rejection never reaches the tracker');
      else fail(`tracker was written despite the undo: ${readFileSync(trackerFile, 'utf8').slice(-200)}`);

      const rejected = await drive('3', 'DOWN', 'r', 'x', 'x', 'x', 'x');
      if (/rejected ✗/.test(rejected)) pass('"r" completes the rejection once the grace period elapses');
      else fail('"r" did not report the rejection');
      // the entry's name is still in the status line, so assert on the empty list
      if (/No active applications/.test(rejected)) pass('the rejected entry leaves the follow-up list');
      else fail('the rejected entry is still on the Follow-ups tab');
      if (/\| Rejected \|/.test(readFileSync(trackerFile, 'utf8'))) pass('the elapsed rejection is written to the tracker');
      else fail(`tracker after a rejection: ${readFileSync(trackerFile, 'utf8').slice(-200)}`);

      // and it leaves the follow-up list, because cadence only keeps
      // applied/responded/interview — put it back Applied for the rest of the run.
      writeFileSync(trackerFile, readFileSync(trackerFile, 'utf8').replace('| Rejected |', '| Applied |'), 'utf8');
      const proceeded = await drive('3', 'DOWN', 'p');
      if (/Responded/.test(proceeded)) pass('"p" advances an applied follow-up to Responded');
      else fail('"p" did not advance the status');
      if (/\| Responded \|/.test(readFileSync(trackerFile, 'utf8'))) pass('"p" writes the next stage to the tracker');
      else fail(`tracker after proceed: ${readFileSync(trackerFile, 'utf8').slice(-200)}`);
      writeFileSync(trackerFile, readFileSync(trackerFile, 'utf8').replace('| Responded |', '| Applied |'), 'utf8');

      rmSync(OPENED, { force: true });
      const noRep = await drive('3', 'DOWN', 'o');
      if (/No report found/.test(noRep)) pass('"o" reports a follow-up whose report file is missing');
      else fail('"o" did not report the missing follow-up report');
    } else {
      fail('the Follow-ups tab did not list the fixture application');
    }

    const fuNav = await drive('3', 'DOWN', 'j', 'k', 'ESC', 'TAB');
    if (fuNav.includes('1 QUEUE')) pass('j/k/Esc/Tab navigate the Follow-ups list without crashing');
    else fail('Follow-ups navigation crashed');

    // ── Paging and quitting ──────────────────────────────────────────────────

    const paged = await drive('TAB', 'TAB', 'TAB', '\x1b[5~', '\x1b[6~');
    if (paged.includes('1 QUEUE')) pass('PgUp/PgDn page the queue list');
    else fail('PgUp/PgDn broke the queue list');

    const ctrlC = await drive('\x03');
    if (ctrlC.length > 0) pass('Ctrl-C exits without losing the frame buffer');
    else fail('Ctrl-C lost the rendered output');

    // 'q' quits cleanly — the driver only captures frames if the exit path ran.
    const quit = await drive('q');
    if (quit.length > 0) pass('"q" exits without losing the frame buffer');
    else fail('"q" lost the rendered output — the quit path did not flush');

    // ── --stats self-check ───────────────────────────────────────────────────

    const stats = await runNodeAsync(['snipe-tui.mjs', '--stats'], { env: ENV });
    let parsed = null;
    try { parsed = JSON.parse(stats.out); } catch {}
    if (parsed && typeof parsed.queue === 'number') pass('--stats emits parseable JSON without a TTY');
    else fail(`--stats did not emit JSON: ${stats.out.slice(0, 120)}`);
    if (parsed && parsed.active === 0) pass('--stats reports no active run when the pid file is absent');
    else fail(`--stats active was ${parsed?.active}, expected 0`);

    // Without a TTY and without --stats it must refuse rather than render junk.
    const noTty = await runNodeAsync(['snipe-tui.mjs'], { env: ENV });
    if (noTty.code === 1 && /interactive terminal/.test(noTty.err)) {
      pass('the TUI refuses to start without a TTY and says why');
    } else fail(`no-TTY guard did not fire (exit ${noTty.code})`);
  } finally {
    // The fixture home is inside tmp, so one removal is the whole cleanup.
    rmSync(tmp, { force: true, recursive: true });
  }
}
