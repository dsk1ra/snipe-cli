/**
 * paths.mjs — shared root and tracker-file resolution for the tracker scripts.
 *
 * Every script in this folder derives data/, reports/ and batch/ paths from the
 * project root, and each used to compute that root independently from its own
 * file location. When the scripts moved from the repo root into tracker/, a
 * missed `..` would not have crashed: resolution fell through to a
 * non-existent applications.md and the script silently created a second, empty
 * tracker. The env override used by the test suite masks that class of bug, so
 * the guard below is the only thing that catches it.
 */

import { existsSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';

/** Project root — one level above tracker/. */
export const SNIPE_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..');

if (!existsSync(join(SNIPE_ROOT, 'package.json'))) {
  throw new Error(
    `snipe root not found at ${SNIPE_ROOT} — tracker scripts must sit one level below the project root`,
  );
}

/**
 * Resolve the tracker markdown file.
 *
 * Both layouts are supported: `data/applications.md` (current) and
 * `applications.md` (original). `SNIPE_TRACKER` overrides both — the test
 * suite and non-standard layouts rely on it.
 *
 * @returns {string} Absolute path to the tracker markdown.
 */
export function trackerPath() {
  if (process.env.SNIPE_TRACKER) return process.env.SNIPE_TRACKER;
  const nested = join(SNIPE_ROOT, 'data/applications.md');
  return existsSync(nested) ? nested : join(SNIPE_ROOT, 'applications.md');
}
