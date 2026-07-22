#!/usr/bin/env node

/**
 * import-pipeline.mjs — Bridges pipeline.md → batch-input.tsv.
 *
 * Reads unchecked `- [ ]` lines from data/pipeline.md, finds URLs not already
 * in batch-input.tsv, and appends them with sequential IDs. Marks imported
 * lines as `- [x]` in pipeline.md so they won't be re-imported next run.
 *
 * Usage:
 *   node batch/import-pipeline.mjs [--dry-run]
 */

import { readFileSync, writeFileSync, existsSync, copyFileSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { createHash } from 'crypto';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT_DIR = resolve(__dirname, '..');

const PIPELINE_PATH = resolve(PROJECT_DIR, 'data/pipeline.md');
const BATCH_INPUT_PATH = resolve(__dirname, 'batch-input.tsv');
const BATCH_JDS_DIR = resolve(__dirname, 'jds');
const APIFY_JD_DIR = process.env.HOME
  ? `${process.env.HOME}/.cache/snipe-apify/jds`
  : '/tmp/snipe-apify/jds';
const DRY_RUN = process.argv.includes('--dry-run');

// URLs from these hosts cannot be fetched by the batch scorer (bot-protected listing pages).
// Keep them in pipeline.md for interactive processing via /snipe pipeline instead.
const BATCH_SKIP_HOSTS = [
  'www.linkedin.com', 'linkedin.com',
  'uk.indeed.com', 'indeed.com', 'www.indeed.com',
];

// ── Read existing batch-input URLs ────────────────────────────────────────────

const batchText = existsSync(BATCH_INPUT_PATH)
  ? readFileSync(BATCH_INPUT_PATH, 'utf8')
  : 'id\turl\tsource\tnotes\n';

const existingUrls = new Set(
  batchText.split('\n')
    .slice(1)
    .map(l => l.split('\t')[1]?.trim())
    .filter(Boolean)
);

let maxId = batchText.split('\n')
  .slice(1)
  .map(l => parseInt(l.split('\t')[0], 10))
  .filter(n => !isNaN(n))
  .reduce((m, n) => Math.max(m, n), 0);

// ── Parse pipeline.md ─────────────────────────────────────────────────────────

if (!existsSync(PIPELINE_PATH)) {
  console.log('pipeline.md not found — nothing to import.');
  process.exit(0);
}

const pipelineText = readFileSync(PIPELINE_PATH, 'utf8');
const lines = pipelineText.split('\n');

const today = new Date().toISOString().split('T')[0];
const source = `pipeline-scan-${today}`;

const toImport = [];
const importedLineIndices = new Set();

for (let i = 0; i < lines.length; i++) {
  const line = lines[i];
  // Match unchecked checkbox lines: `- [ ] URL | Company | Role`
  const m = line.match(/^-\s+\[\s+\]\s+(https?:\/\/\S+)(.*)/);
  if (!m) continue;

  const url = m[1].trim();
  const rest = m[2].trim().replace(/^\|/, '').trim(); // "Company | Role"

  if (existingUrls.has(url)) continue; // already in batch

  // Skip bot-protected listing pages — unless the scanner already cached the
  // JD via Apify, in which case phases 1-3 never need to fetch the URL.
  try {
    const host = new URL(url).hostname;
    if (BATCH_SKIP_HOSTS.includes(host)) {
      const urlHash = createHash('sha1').update(url).digest('hex').slice(0, 16);
      if (!existsSync(`${APIFY_JD_DIR}/${urlHash}.txt`)) continue;
    }
  } catch {}

  toImport.push({ url, notes: rest, lineIndex: i });
  importedLineIndices.add(i);
}

if (toImport.length === 0) {
  console.log('No new URLs to import from pipeline.md.');
  process.exit(0);
}

console.log(`Found ${toImport.length} new URL${toImport.length === 1 ? '' : 's'} to import:`);

// ── Append to batch-input.tsv ─────────────────────────────────────────────────

const newRows = toImport.map(({ url, notes }) => {
  maxId++;
  const id = maxId;
  // Copy pre-fetched Apify JD (LinkedIn/Indeed/Glassdoor) so phases 1-2 don't
  // need to hit the URL themselves.
  const urlHash = createHash('sha1').update(url).digest('hex').slice(0, 16);
  const apifyJd = `${APIFY_JD_DIR}/${urlHash}.txt`;
  const batchJd = resolve(BATCH_JDS_DIR, `${id}.txt`);
  if (existsSync(apifyJd) && !existsSync(batchJd)) {
    mkdirSync(BATCH_JDS_DIR, { recursive: true });
    copyFileSync(apifyJd, batchJd);
    console.log(`  #${id}: JD pre-loaded from Apify cache`);
  }
  console.log(`  #${id}: ${notes || url}`);
  return `${id}\t${url}\t${source}\t${notes}`;
});

if (!DRY_RUN) {
  const appendText = newRows.join('\n') + '\n';
  writeFileSync(BATCH_INPUT_PATH, batchText.trimEnd() + '\n' + appendText, 'utf8');

  // Mark imported lines as checked in pipeline.md
  for (const i of importedLineIndices) {
    lines[i] = lines[i].replace(/^(-\s+)\[\s+\]/, '$1[x]');
  }
  writeFileSync(PIPELINE_PATH, lines.join('\n'), 'utf8');

  console.log(`\nImported ${toImport.length} offers into batch-input.tsv (IDs ${maxId - toImport.length + 1}–${maxId}).`);
  console.log('Pipeline.md entries marked as [x].');
} else {
  console.log('\n(dry run — no files written)');
}
