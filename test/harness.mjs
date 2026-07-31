// test/harness.mjs — shared assertion counters, reporters, and helpers for the
// split suite. Each test/*.test.mjs runs its assertions at import time and
// mutates the shared `counters`; test-all.mjs imports them in sequence and
// prints the aggregate summary.
//
// The node builtins the sections use are re-exported here so the section bodies
// could move out of the old monolith unchanged — a test file needs one import
// line, not a node-builtin preamble plus a harness import.

import { execSync, execFileSync, spawn } from 'child_process';
import { readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'fs';
import { join, dirname } from 'path';
import { tmpdir } from 'os';
import { fileURLToPath, pathToFileURL } from 'url';

export { execSync, execFileSync, spawn };
export { readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync };
export { join, dirname, tmpdir, fileURLToPath, pathToFileURL };

/** Repo root — one level above test/. */
export const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
export const NODE = process.execPath;

/** Shared counters — the launcher reads these after importing every suite. */
export const counters = { passed: 0, failed: 0, warnings: 0 };

/** Record and print one passing assertion. */
export function pass(msg) { console.log(`  PASS ${msg}`); counters.passed++; }

/** Record and print one failing assertion (drives the final exit code). */
export function fail(msg) { console.log(`  FAIL ${msg}`); counters.failed++; }

/** Record and print one non-fatal warning (expected local-env gaps). */
export function warn(msg) { console.log(`  WARN ${msg}`); counters.warnings++; }

/**
 * Run a command and return trimmed stdout, or null on failure. Array-form args
 * use execFileSync (no shell); string-only commands use execSync.
 */
export function run(cmd, args = [], opts = {}) {
  try {
    if (Array.isArray(args) && args.length > 0) {
      return execFileSync(cmd, args, { cwd: ROOT, encoding: 'utf-8', timeout: 30000, ...opts }).trim();
    }
    return execSync(cmd, { cwd: ROOT, encoding: 'utf-8', timeout: 30000, ...opts }).trim();
  } catch (e) {
    return null;
  }
}

/** True when a repo-relative file exists. */
export function fileExists(path) { return existsSync(join(ROOT, path)); }

/** Read a repo-relative text file as UTF-8. */
export function readFile(path) { return readFileSync(join(ROOT, path), 'utf-8'); }

/**
 * Run a node script and resolve with its result. The sync `run()` above cannot
 * be used for anything that talks to a server this process is hosting — execSync
 * blocks the event loop, so the request never gets answered.
 *
 * @returns {Promise<{ code: number|null, out: string, err: string }>}
 */
export function runNodeAsync(args, opts = {}) {
  return new Promise(resolve => {
    const p = spawn(NODE, args, { cwd: ROOT, ...opts });
    let out = '', err = '';
    p.stdout.on('data', d => { out += d; });
    p.stderr.on('data', d => { err += d; });
    const timer = setTimeout(() => p.kill('SIGKILL'), opts.timeout ?? 120_000);
    p.on('close', code => { clearTimeout(timer); resolve({ code, out, err }); });
  });
}

/**
 * Snapshot repo-relative files and hand back a restore function. The phase
 * scripts write into the working tree (embedding indexes, JD cache); tests must
 * leave the developer's real ones exactly as they found them, including when an
 * assertion throws — so callers put the restore in a `finally`.
 *
 * @param {string[]} paths repo-relative
 * @returns {() => void}
 */
export function preserve(paths) {
  const saved = paths.map(p => {
    const abs = join(ROOT, p);
    return { abs, data: existsSync(abs) ? readFileSync(abs) : null };
  });
  return () => {
    for (const { abs, data } of saved) {
      if (data === null) rmSync(abs, { force: true, recursive: true });
      else writeFileSync(abs, data);
    }
  };
}

/**
 * Make sure the user-layer files the phase scripts hard-depend on exist.
 *
 * They are gitignored, so a developer's checkout has real ones and CI has none.
 * Rather than skip the phase tests wherever they are absent — which would mean
 * the paths that matter most are only ever exercised on one machine — stand up a
 * minimal fixture and remove it after. Existing files are never touched.
 *
 * @returns {() => void} cleanup
 */
export function ensureUserLayer() {
  const FIXTURES = {
    'cv.md': [
      '# Alex Fixture', 'alex@example.com · Berlin, Germany', '',
      '## Summary', 'Backend engineer, 6 years, Python and Node.js services on AWS.', '',
      '## Experience', '### Acme Corp — Senior Backend Engineer', '2021-01 - Present',
      '- Built REST APIs in Python serving 12k req/s on Kubernetes.',
      '- Cut p99 latency 40% by adding a Redis cache layer.',
      '- Mentored 3 engineers through code review and pairing.', '',
      '### Globex — Backend Engineer', '2018-03 - 2020-12',
      '- Migrated a Django monolith to Node.js services on AWS ECS.',
      '- Wrote the Postgres schema and its migration tooling.', '',
      '## Projects', '### Ratchet', '- A CLI in Go for replaying HTTP traffic.', '',
      '## Education', '### BSc Computer Science — TU Berlin', '2014 - 2018', '',
      '## Skills', '- Python, Node.js, Go, PostgreSQL, Redis, Kubernetes, AWS, Terraform', '',
      '## Languages', '- English: Fluent (C1)', '- German: Conversational (B1)', '',
    ].join('\n'),
    'config/profile.yml': [
      'full_name: "Alex Fixture"', 'headline: "Backend engineer"',
      'target_roles:', '  archetypes:', '    - name: "Backend Engineer"',
      '    - name: "Platform Engineer"',
      'compensation:', '  target_annual_eur: 90000', '  minimum_annual_eur: 70000', '',
    ].join('\n'),
    'config/profile.md': [
      '# Target profile', '', '## Archetypes', '- Backend Engineer', '- Platform Engineer', '',
      '## North star', 'Own a backend platform end to end.', '',
    ].join('\n'),
  };
  const created = [];
  for (const [rel, body] of Object.entries(FIXTURES)) {
    const abs = join(ROOT, rel);
    if (existsSync(abs)) continue;
    mkdirSync(dirname(abs), { recursive: true });
    writeFileSync(abs, body, 'utf8');
    created.push(abs);
  }
  return () => { for (const abs of created) rmSync(abs, { force: true }); };
}

// A suite run in isolation (`node test/scan.test.mjs`) has no launcher to set the
// exit code, so a failed assertion would otherwise exit 0 — a silent pass. This
// makes any failure non-zero. Under the launcher it's a no-op: the launcher's
// explicit process.exit() already encodes the same failed>0 → exit 1.
process.on('exit', () => { if (counters.failed > 0 && !process.exitCode) process.exitCode = 1; });
