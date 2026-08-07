// @ts-check
/**
 * stats.mjs — paired-comparison statistics, shared by the benchmarks.
 *
 * These lived in retrieval-bench.mjs, which imports tailor-harness.mjs. When the
 * tailor harness grew a paired comparison of its own and reached back for them,
 * that edge closed a cycle and the import deadlocked. They are pure functions
 * over an array of numbers with no benchmark-specific knowledge, so the fix is
 * for neither benchmark to own them.
 *
 * Both benchmarks answering "is this real" with the same code is the point:
 * PHASE3-RETRIEVAL-LEDGER's rule is that a dozen variants against a dozen offers
 * will always produce a winner, and two different implementations of the
 * bootstrap would eventually disagree about which winner was real.
 *
 * Self-check: node batch/stats.mjs
 */

/** Deterministic PRNG, so a bootstrap is reproducible across runs. */
function mulberry32(a) {
  return function () {
    // Without this the state never advances, every draw picks the same index,
    // and the CI collapses to a single value — which reads as a suspiciously
    // tight interval rather than as the plumbing failure it is (rule 6).
    a |= 0; a = (a + 0x6D2B79F5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * Bootstrap CI of the mean of `xs`, resampling with replacement.
 * `xs` are per-offer paired deltas, so the resampling unit is the offer.
 * @param {number[]} xs
 */
export function bootstrapCI(xs, draws = 5000, seed = 42, alpha = 0.05) {
  if (!xs.length) return { lo: 0, hi: 0, mean: 0 };
  const rnd = mulberry32(seed);
  const means = [];
  for (let d = 0; d < draws; d++) {
    let s = 0;
    for (let i = 0; i < xs.length; i++) s += xs[Math.floor(rnd() * xs.length)];
    means.push(s / xs.length);
  }
  means.sort((a, b) => a - b);
  return {
    mean: xs.reduce((a, b) => a + b, 0) / xs.length,
    lo: means[Math.floor(draws * alpha / 2)],
    hi: means[Math.floor(draws * (1 - alpha / 2))],
  };
}

/** Two-sided exact sign test on paired deltas; ties dropped, as is conventional. */
export function signTest(xs, eps = 1e-9) {
  const pos = xs.filter(x => x > eps).length;
  const neg = xs.filter(x => x < -eps).length;
  const n = pos + neg;
  if (!n) return { pos, neg, p: 1 };
  const C = (n, k) => { let r = 1; for (let i = 0; i < k; i++) r = r * (n - i) / (i + 1); return r; };
  let tail = 0;
  const k = Math.min(pos, neg);
  for (let i = 0; i <= k; i++) tail += C(n, i);
  return { pos, neg, p: Math.min(1, 2 * tail / 2 ** n) };
}

if (process.argv[1] && process.argv[1].endsWith('stats.mjs')) {
  const assert = (await import('assert')).default;
  // A local truthiness helper rather than assert.ok: TS2775 requires an explicit
  // type annotation on any imported assertion function, and cv-select.mjs and
  // goldset.mjs already hand-roll theirs for the same reason.
  const ok = (/** @type {any} */ c, /** @type {string} */ m) => { if (!c) throw new Error(m); };

  // A constant positive shift: the CI must exclude 0 and every offer must win.
  const ci = bootstrapCI([0.1, 0.12, 0.09, 0.11, 0.10, 0.13]);
  ok(ci.lo > 0, 'a uniformly positive effect has a CI above 0');
  ok(Math.abs(ci.mean - 0.1083) < 0.001, 'the reported mean is the sample mean');

  // Noise around zero must not.
  const noise = bootstrapCI([0.1, -0.09, 0.02, -0.11, 0.05, -0.04]);
  ok(noise.lo < 0 && noise.hi > 0, 'noise straddles 0');

  assert.equal(bootstrapCI([]).mean, 0, 'empty is not a crash');

  const s = signTest([1, 1, 1, 1, 1, 1, -1]);
  assert.equal(s.pos, 6); assert.equal(s.neg, 1);
  ok(s.p < 0.15 && s.p > 0.1, `6-1 is p=0.125, got ${s.p}`);
  assert.equal(signTest([0, 0, 0]).p, 1, 'all ties is no evidence');

  console.log('stats self-check OK');
}
