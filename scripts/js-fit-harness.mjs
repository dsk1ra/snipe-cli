// Dump every deterministic cap in fit-rules.mjs over the whole cached JD corpus,
// for `go run ./cmd/gate fit`.
//
// These are 551 lines of measured constants — the 160-character proximity window
// on the location cap, the monthly-vs-weekly cadence carve-out, the
// two-mentions-per-ecosystem threshold — and neither side has a unit test that
// would notice one of them being transcribed a character off. The corpus is the
// test.
//
//   node scripts/js-fit-harness.mjs <node-repo> > /tmp/js-fit.tsv
//   go run ./cmd/gate fit <node-repo> /tmp/js-fit.tsv
//
// The column layout is mirrored in cmd/gate/fit.go and must stay in step.
import { existsSync, readFileSync, readdirSync } from 'fs';

import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

// Both implementations live in this repo now, so the default is its root,
// resolved from this file rather than the cwd: a dynamic import() resolves
// against the module while readFileSync resolves against the process.
const REPO = resolve(process.argv[2] || process.env.SNIPE_NODE_REPO
  || resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const fit = await import(`${REPO}/batch/fit-rules.mjs`);

const cv = readFileSync(`${REPO}/cv.md`, 'utf8');
const profile = readFileSync(`${REPO}/config/profile.yml`, 'utf8');
const dir = `${REPO}/batch/jds`;

// The Go side prints these to stderr too: an ecosystem or commute term read out
// of a different cv.md makes every cap below it disagree for a reason that has
// nothing to do with the port.
console.error('ecosystems=' + [...fit.candidateEcosystems(cv)].sort().join(','));
console.error('commutable=' + fit.commutableTerms(profile).join(','));

for (const f of readdirSync(dir).filter(n => n.endsWith('.txt')).sort()) {
  const jd = readFileSync(`${dir}/${f}`, 'utf8');

  // seniorityCaps keys off the role title, which Phase 1 wrote to the score
  // file. No score yet means no title, which is what the Go side sees too.
  let title = '';
  const score = `${REPO}/batch/scores/${f.replace(/\.txt$/, '')}.json`;
  if (existsSync(score)) {
    try { title = JSON.parse(readFileSync(score, 'utf8')).role || ''; } catch { /* unscored */ }
  }

  const sm = fit.stackMismatchCap(jd, cv);
  const sc = fit.seniorityCaps(title, jd);
  const lm = fit.languageMismatchCap(jd, cv);
  const lo = fit.locationMismatchCap(jd, profile);

  console.log([
    f,
    `stack=${sm.cap}|${sm.jdStack.join('+')}|${sm.missing.join('+')}`,
    `sen=${sc.cvCap}/${sc.nsCap}|${sc.reason ?? ''}`,
    `lang=${lm.cvCap}/${lm.nsCap}|${lm.missing ?? ''}`,
    `loc=${lo.cvCap}/${lo.nsCap}|${lo.city ?? ''}`,
    `multi=${fit.looksMultiPosting(jd)}`,
  ].join('\t'));
}
