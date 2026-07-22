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

// A suite run in isolation (`node test/scan.test.mjs`) has no launcher to set the
// exit code, so a failed assertion would otherwise exit 0 — a silent pass. This
// makes any failure non-zero. Under the launcher it's a no-op: the launcher's
// explicit process.exit() already encodes the same failed>0 → exit 1.
process.on('exit', () => { if (counters.failed > 0 && !process.exitCode) process.exitCode = 1; });
