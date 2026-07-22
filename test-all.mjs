#!/usr/bin/env node

/**
 * test-all.mjs — launcher for the split test suite in test/.
 *
 * Each test/<name>.test.mjs imports the shared harness and runs its assertions
 * at import time, mutating the harness counters. Importing them in sequence runs
 * the whole suite; this file prints the aggregate summary and sets the exit
 * code. Run one suite in isolation with: node test/<name>.test.mjs
 *
 * Run before merging any PR or pushing changes.
 */

import { counters } from './test/harness.mjs';

console.log('\nsnipe test suite\n');

// Order is cosmetic — suites are independent. scanScript's cross-check lives
// entirely within scan.test.mjs, so no ordering constraint survives the split.
const suites = [
  'smoke', 'liveness', 'contract', 'scan', 'cadence',
  'providers', 'tracker', 'pdf', 'pipeline',
];

for (const name of suites) {
  await import(`./test/${name}.test.mjs`);
}

console.log('\n' + '='.repeat(50));
console.log(`Results: ${counters.passed} passed, ${counters.failed} failed, ${counters.warnings} warnings`);

if (counters.failed > 0) {
  console.log('TESTS FAILED — do NOT push/merge until fixed\n');
  process.exit(1);
} else if (counters.warnings > 0) {
  console.log('Tests passed with warnings — review before pushing\n');
  process.exit(0);
} else {
  console.log('All tests passed — safe to push/merge\n');
  process.exit(0);
}
