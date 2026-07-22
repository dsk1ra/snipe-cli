// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 20. PIPELINE LOGIC SELF-CHECKS ──────────────────────────────

console.log('\n20. Pipeline logic (fit-rules + text-utils)');

// These modules own the deterministic scoring guardrails CLAUDE.md calls
// "code-enforced in both phases": seniority/stack/language caps (fit-rules) and
// salary parsing + comp scoring (text-utils). Each ships an assert-based
// self-check under its own import.meta.url guard; run them here so a broken edit
// fails the suite instead of only surfacing when the file is invoked directly.
// Reusing the in-module self-checks keeps one copy of the expected values — the
// alternative (re-asserting them inline) would drift the moment either changes.
for (const mod of ['batch/fit-rules.mjs', 'batch/text-utils.mjs']) {
  const out = run(NODE, [mod]);
  if (out !== null && out.includes('self-check passed')) {
    pass(`${mod} self-check passed`);
  } else {
    // run() swallows stderr; re-run to surface the failing assertion.
    let detail = '';
    try {
      execFileSync(NODE, [join(ROOT, mod)], { cwd: ROOT, encoding: 'utf-8', timeout: 30000 });
    } catch (e) {
      detail = `${e.stdout || ''}${e.stderr || ''}`.replace(/\s+/g, ' ').trim().slice(0, 200);
    }
    fail(`${mod} self-check failed${detail ? ` — ${detail}` : ''}`);
  }
}


