// Drive the Node selector with a fixed embedder, so the allocation can be
// compared with no model in the loop and any difference is the funnel.
//
// It must run against a `##`-heading CV. The Node parser matches only that
// spelling, so on a bold-heading CV selectCvForJd returns its input unchanged
// and there is nothing to compare.
//
//   node scripts/js-select-harness.mjs <node-repo> <cv-path> > js-select.tsv
//   go run ./cmd/gate select <cv-path> js-select.tsv
//
// The score function and the three queries are mirrored in cmd/gate/select.go
// and must stay in step.
import { readFileSync } from 'fs';
import { createHash } from 'crypto';
import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

// Both implementations live in this repo now, so the default is its root,
// resolved from this file rather than the cwd: a dynamic import() resolves
// against the module while readFileSync resolves against the process.
const REPO = resolve(process.argv[2] || process.env.SNIPE_NODE_REPO
  || resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const sel = await import(`${REPO}/batch/cv-select.mjs`);

const cv = readFileSync(process.argv[3], 'utf8');

// Deterministic pseudo-scores: hash each text to [0,1). Same function in Go, so
// both sides rank identically without either calling a model.
const score = t => (parseInt(createHash('sha256').update(t).digest('hex').slice(0,8),16) % 100000) / 100000;

const fakeEmbed = async (texts) => texts.map(t => [score(t), 1 - score(t)]);

const out = await sel.selectCvForJd(cv, ['Java experience','Kafka streaming','security'], 'jd text', {
  _embed: fakeEmbed,
  _spikeBackground: async () => null,
  judgeShots: [],
  lineBudget: 22, projMaxLines: 13, maxProjects: 4,
  maxBulletsPerProject: 4, maxBulletsPerRole: 4, minTopExpBullets: 2,
  spikeWeight: 0,
});
for (const s of sel.parseCvSections(out)) {
  if (!['Experience','Projects'].includes(s.name)) continue;
  const p = sel.parseEntries(s.lines);
  for (const e of p.entries) {
    console.log(`${s.name}\t${e.head[0]}\t${e.bullets.length}`);
    for (const b of e.bullets) console.log(`  BULLET\t${b.slice(0,70)}`);
  }
}
