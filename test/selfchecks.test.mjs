// Several modules carry their own `node <file>` self-check — a block of real
// assertions guarded by process.argv[1]. Two of them (retrieval-bench,
// pseudo-label) were already wired into the suite; the rest ran only when a
// developer remembered to invoke them by hand, which meant they were both
// unrun and uncovered. Spawning them here costs a subprocess each and counts,
// because c8 works through NODE_V8_COVERAGE.
//
// These assert on the module's own maths — bootstrap CIs, sign tests, atom
// extraction, skill selection. Nothing here re-states what the block already
// checks; the value is that a broken self-check now fails CI instead of
// waiting to be noticed.
import {
  pass, fail, warn, runNodeAsync, ensureUserLayer,
  ROOT, join, existsSync, readFileSync, mkdtempSync, writeFileSync, rmSync, tmpdir,
} from './harness.mjs';

const eq = (actual, expected, label) =>
  actual === expected ? pass(label) : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
const ok = (cond, label) => cond ? pass(label) : fail(label);

console.log('\nself-checks');

/** Run a module's self-check and assert it passes with the message it promises. */
async function selfCheck(script, args, expect, label) {
  const r = await runNodeAsync([join(ROOT, script), ...args], { timeout: 90_000 });
  if (r.code === 0 && expect.test(r.out)) {
    pass(`${label} self-check passes`);
  } else {
    fail(`${label} self-check: exit ${r.code}, output ${JSON.stringify((r.out + r.err).slice(-200))}`);
  }
}

// goldset's self-check is the one that will not run on a fixture. It asserts
// the CV yields more than ten atoms, and that writeSheet refuses to overwrite
// an already-ticked batch/bench/goldset.md, so it needs the developer's own
// cv.md and their own gold sheet. On a checkout with no ticked sheet that
// second assertion does not merely fail: writeSheet has nothing to refuse, so
// it writes an empty sheet over the path it was meant to be protecting.
//
// So it is gated rather than faked. A fixture CV big enough to satisfy the
// atom count would only be asserting on the fixture, and seeding a ticked
// sheet means writing into the real batch/bench/ — SHEET is resolved from
// __dirname and, unlike retrieval-bench.mjs, does not honour SNIPE_BENCH_DIR.
//
// Checked before ensureUserLayer, which would otherwise make cv.md exist.
const goldsetSheet = join(ROOT, 'batch/bench/goldset.md');
const hasGoldsetData = existsSync(join(ROOT, 'cv.md')) && existsSync(goldsetSheet)
  && /^- \[[xX]\]/m.test(readFileSync(goldsetSheet, 'utf8'));

// cv.md is user-layer and gitignored, so a fresh checkout has none; several of
// these read it directly.
const restoreUserLayer = ensureUserLayer();

try {
  await selfCheck('batch/stats.mjs', [], /stats self-check OK/, 'stats');
  await selfCheck('batch/opus-metrics.mjs', [], /opus-metrics self-check OK/, 'opus-metrics');
  await selfCheck('batch/cv-writers.mjs', [], /cv-writers self-check OK/, 'cv-writers');
  await selfCheck('batch/fit-rules.mjs', [], /fit-rules self-check passed/, 'fit-rules');
  await selfCheck('batch/text-utils.mjs', [], /text-utils self-check passed/, 'text-utils');
  if (hasGoldsetData) {
    await selfCheck('batch/goldset.mjs', ['selfcheck'], /goldset selfcheck ok/, 'goldset');
  } else {
    warn('goldset self-check skipped — needs a real cv.md and a ticked batch/bench/goldset.md');
  }
} finally {
  restoreUserLayer?.();
}

// ── timing.mjs report ────────────────────────────────────────────────────────
// The CLI half of timing.mjs had no coverage at all: it is only reachable as
// `node batch/timing.mjs report <file>`, which nothing ran.

const TMP = mkdtempSync(join(tmpdir(), 'snipe-timing-'));
try {
  // Two model calls in one phase plus an enclosing wall row. The wall row is
  // the interesting one — summarise() must report it separately rather than
  // adding it to model time, since it encloses the calls it sits beside.
  const tsv =
    'at\tid\tphase\tmodel\ttotal_s\tload_s\tprompt_tok\tprompt_s\tout_tok\tout_s\tdone\textra\n' +
    '2026-08-11T10:00:00Z\t1\tscore\tsnipe-screen\t4.0\t2.5\t900\t0.4\t120\t2.0\tstop\t-\n' +
    '2026-08-11T10:00:10Z\t1\teval\tsnipe-eval\t60.0\t0.2\t8000\t3.0\t900\t50.0\tstop\t-\n' +
    '2026-08-11T10:01:20Z\t1\teval-wall\twall\t75.0\t0\t0\t0\t0\t0\twall\t-\n';
  const file = join(TMP, 'timings.tsv');
  writeFileSync(file, tsv);

  const rep = await runNodeAsync([join(ROOT, 'batch/timing.mjs'), 'report', file]);
  eq(rep.code, 0, 'timing report exits clean on a well-formed TSV');
  ok(/offers 1/.test(rep.out), 'and counts the distinct offers');
  ok(/calls 3/.test(rep.out), 'and every logged row as a call');
  // 4.0 + 60.0 model seconds. The 75s wall row encloses both and must not be
  // added to them — 139 here would mean every call was counted twice.
  ok(/model time 64s/.test(rep.out), 'model time sums the model rows only, leaving the wall row out');
  ok(/per JD 75s/.test(rep.out), 'per-JD time is the wall row, not the model sum');
  ok(/phase wall clock/.test(rep.out), 'and says so, rather than implying it divided model time');

  // Only score's 2.5s load crosses the 1s threshold; eval's 0.2s does not. A
  // reload is a cost the phase order caused, which is why they are counted
  // rather than folded into total time.
  ok(/1 reloads/.test(rep.out), 'a load over the threshold counts as a reload, one under does not');
  ok(/eval-wall/.test(rep.out) && /^score\s/m.test(rep.out),
    'wall phases and model phases are tabulated separately');

  const noFile = await runNodeAsync([join(ROOT, 'batch/timing.mjs'), 'report'], {
    env: { ...process.env, SNIPE_TIMING: '' },
  });
  eq(noFile.code, 1, 'timing report with no file exits nonzero');
  ok(/usage: timing.mjs report/.test(noFile.err), 'and prints usage');
} finally {
  rmSync(TMP, { recursive: true, force: true });
}
