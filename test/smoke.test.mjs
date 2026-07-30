// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 1. SYNTAX CHECKS ────────────────────────────────────────────

console.log('1. Syntax checks');

// Root plus the script subfolders — a bare readdir of ROOT silently stops
// covering anything that gets grouped into a directory. `test` covers the split
// suite itself (harness + every *.test.mjs).
const mjsFiles = ['.', 'tracker', 'batch', 'providers', 'test'].flatMap(dir =>
  readdirSync(join(ROOT, dir))
    .filter(f => f.endsWith('.mjs'))
    .map(f => (dir === '.' ? f : `${dir}/${f}`)),
);
for (const f of mjsFiles) {
  const result = run(NODE, ['--check', f]);
  if (result !== null) {
    pass(`${f} syntax OK`);
  } else {
    fail(`${f} has syntax errors`);
  }
}

// ── 2. SCRIPT EXECUTION ─────────────────────────────────────────

console.log('\n2. Script execution (graceful on empty data)');

const scripts = [
  { name: 'cv-sync-check.mjs', expectExit: 1, allowFail: true }, // fails without cv.md (normal in repo)
  { name: 'tracker/verify-pipeline.mjs', expectExit: 0 },
  // --dry-run: these three scripts resolve ROOT from import.meta.url and write
  // data/applications.md in place. On a provisioned working copy with a real
  // tracker present, running them without --dry-run mutates user data. Harmless
  // in this repo (no tracker shipped), risky for end users who run tests inside
  // their active snipe workspace.
  { name: 'tracker/normalize-statuses.mjs --dry-run', expectExit: 0 },
  { name: 'tracker/dedup-tracker.mjs --dry-run', expectExit: 0 },
  { name: 'tracker/merge-tracker.mjs --dry-run', expectExit: 0 },
  { name: 'tracker/tracker-columns-tests.mjs', expectExit: 0 },
  { name: 'validate-portals.mjs --file templates/portals.example.yml', expectExit: 0 },
];

for (const { name, allowFail } of scripts) {
  const result = run(NODE, name.split(' '), { stdio: ['pipe', 'pipe', 'pipe'] });
  if (result !== null) {
    pass(`${name} runs OK`);
  } else if (allowFail) {
    warn(`${name} exited with error (expected without user data)`);
  } else {
    fail(`${name} crashed`);
  }
}



// ── 3. TUI RETRY/DEBUG MAPPING ──────────────────────────────────

// A failed row's phase decides which input "debug" opens and whether "retry" is
// offered at all. Both are silent when wrong: the wrong input hides the file the
// user came to edit, and a retry on an expired posting queues a run that changes
// nothing.
console.log('\n3. TUI retry/debug mapping (snipe-tui.mjs --retry-plan)');

const retryCases = [
  { row: ['score_failed', '-', '-', '-'], phase: 'p1', retryable: true,
    debug: ['batch/jds/7.txt', 'batch/scores/7.json'] },
  { row: ['unavailable', '-', '-', '-'], phase: 'p1', retryable: false,
    debug: ['batch/jds/7.txt', 'batch/scores/7.json'] },
  { row: ['scored', 'eval_failed', '-', '103'], phase: 'p2', retryable: true,
    debug: ['batch/jds/7.txt', 'batch/evals/7.json'] },
  // rnum 999 has no report on disk (reports/*.md is gitignored, so a fresh
  // checkout has none) — keeps the expectation the same everywhere
  { row: ['scored', 'evaled', 'pdf_failed', '999'], phase: 'p3', retryable: true,
    debug: ['batch/logs/pdf-999-7.log'] },
  { row: ['scored', 'evaled', 'completed', '103'], phase: null },
  // `evaled` with no report number: a failed eval back-filled as a success. Has
  // no report for Phase 3 and no failure status for --retry-failed's entry
  // filter, so before this it rendered as "pending" against an empty queue.
  { row: ['scored', 'evaled', '-', '-'], phase: 'p2', retryable: true,
    debug: ['batch/jds/7.txt', 'batch/evals/7.json'] },
  // eval died before a report number existed — the JD is still the input to fix
  { row: ['scored', 'eval_failed', '-', '-'], phase: 'p2', retryable: true,
    debug: ['batch/jds/7.txt', 'batch/evals/7.json'] },
];

for (const c of retryCases) {
  const out = run(NODE, ['snipe-tui.mjs', '--retry-plan', ...c.row, '7'], { stdio: ['pipe', 'pipe', 'pipe'] });
  const label = c.row.join('/');
  if (out === null) { fail(`--retry-plan ${label} crashed`); continue; }
  let got;
  try { got = JSON.parse(out); } catch { fail(`--retry-plan ${label} printed non-JSON`); continue; }
  if (got.phase !== c.phase) { fail(`--retry-plan ${label}: phase ${got.phase} != ${c.phase}`); continue; }
  if (c.phase === null) { pass(`${label} offers no retry`); continue; }
  if (got.retryable !== c.retryable) {
    fail(`--retry-plan ${label}: retryable ${got.retryable} != ${c.retryable}`);
  } else if (JSON.stringify(got.debug) !== JSON.stringify(c.debug)) {
    fail(`--retry-plan ${label}: debug ${got.debug.join(', ')} != ${c.debug.join(', ')}`);
  } else {
    pass(`${label} → ${c.phase}${c.retryable ? '' : ' (no retry)'}, debug ${c.debug[0]}`);
  }
}
