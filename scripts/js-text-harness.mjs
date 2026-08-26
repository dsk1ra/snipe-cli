// Dump extractSalary and cleanJd over the whole cached JD corpus, for
// `go run ./cmd/gate salary` and `… cleanjd`.
//
// Both are pure functions of a JD, so the corpus is the test: 351 real postings
// exercise more of the money regex and the UTF-16 slice than any table would.
//
//   node scripts/js-text-harness.mjs <node-repo> salary  > /tmp/js-salary.tsv
//   go run ./cmd/gate salary  <node-repo>/batch/jds /tmp/js-salary.tsv
//
//   node scripts/js-text-harness.mjs <node-repo> cleanjd > /tmp/js-cleanjd.tsv
//   go run ./cmd/gate cleanjd <node-repo>/batch/jds /tmp/js-cleanjd.tsv
import { readFileSync, readdirSync } from 'fs';
import { createHash } from 'crypto';

import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

// Both implementations live in this repo now, so the default is its root,
// resolved from this file rather than the cwd: a dynamic import() resolves
// against the module while readFileSync resolves against the process.
const REPO = resolve(process.argv[2] || process.env.SNIPE_NODE_REPO
  || resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const MODE = process.argv[3] || 'salary';
if (MODE !== 'salary' && MODE !== 'cleanjd') {
  console.error(`usage: js-text-harness.mjs <node-repo> <salary|cleanjd>`);
  process.exit(2);
}
const { extractSalary, cleanJd } = await import(`${REPO}/batch/text-utils.mjs`);

// 4000 is text-utils' own default and internal/text.DefaultJDCap. The cap is
// where the UTF-16-vs-bytes bug lived, so it has to be the real one.
const CAP = 4000;
const dir = `${REPO}/batch/jds`;

for (const f of readdirSync(dir).filter(n => n.endsWith('.txt')).sort()) {
  const jd = readFileSync(`${dir}/${f}`, 'utf8');
  let value;
  if (MODE === 'salary') {
    const s = extractSalary(jd);
    // `raw` is a slice of the posting. Rewriting its whitespace here would hide
    // a real difference from the Go side, so it goes out verbatim — and a
    // newline in it would end the TSV row early and silently drop the rest of
    // the corpus, so that is a stop rather than a shrug. None of the 85 matches
    // in the current 351 postings trips it.
    if (s && /[\n\r\t]/.test(s.raw)) {
      console.error(`${f}: salary raw spans a line break — the TSV cannot carry it: ${JSON.stringify(s.raw)}`);
      process.exit(1);
    }
    value = s ? `${s.currency}|${s.min}|${s.max}|${s.raw}` : 'null';
  } else {
    value = createHash('sha256').update(cleanJd(jd, CAP)).digest('hex').slice(0, 16);
  }
  console.log(`${f}\t${value}`);
}
