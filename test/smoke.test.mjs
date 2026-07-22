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


