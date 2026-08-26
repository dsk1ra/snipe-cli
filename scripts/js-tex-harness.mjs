// Dump tex(), texBold() and texUrl() over every real CV bullet plus a battery of
// edge cases, for `go run ./cmd/gate tex`.
//
// fill-cv-template.mjs exports none of them, so the block is re-evaluated out of
// the source rather than retyped. Retyping an escape table is exactly the kind
// of transcription that produces a document which compiles and reads wrong.
//
//   node scripts/js-tex-harness.mjs <node-repo> <cv> > /tmp/js-tex.jsonl
//   go run ./cmd/gate tex /tmp/js-tex.jsonl
import { readFileSync } from 'fs';
import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

// Both implementations live in this repo now, so the default is its root,
// resolved from this file rather than the cwd: a dynamic import() resolves
// against the module while readFileSync resolves against the process.
const REPO = resolve(process.argv[2] || process.env.SNIPE_NODE_REPO
  || resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const { sanitizeAtsText } = await import(`${REPO}/batch/text-utils.mjs`);
const src = readFileSync(`${REPO}/batch/fill-cv-template.mjs`,'utf8');
const grab = (start, end) => src.slice(src.indexOf(start), src.indexOf(end));
const code = grab('const TEX_ESCAPES', 'function wordSet');
const mod = new Function('sanitizeAtsText', code + '\nreturn { tex, texBold, texUrl };')(sanitizeAtsText);

const cases = [];
const cv = readFileSync(process.argv[3] || `${REPO}/cv.md`,'utf8');
for (const l of cv.split('\n')) { const s=l.trim(); if (s.startsWith('- ')) cases.push(s.slice(2)); }
cases.push(
  'Cut latency 95% and $120k spend', 'B2B revenue up 3M+ in 4 weeks',
  'sub-500ms p99 across 10x load', 'AES-256-GCM with ~200 TB and 50,000 rows',
  'Costs £3,500 & 80%+ uptime {braces} #hash _under 100% <x> | y ^ z \\ w',
  'Version 2.0 released 2025 with 250ms latency', '3Mb file, 2B in B2B, 11K users',
  'a — b, “quoted”, x → y, CEH • 2025, €50 and £50', '',
);
for (const c of cases) console.log(JSON.stringify([c, mod.texBold(c), mod.tex(c)]));
console.log(JSON.stringify(['URL', mod.texUrl('https://x.test/a%b#c\\d?e=1&f=~g')]));
