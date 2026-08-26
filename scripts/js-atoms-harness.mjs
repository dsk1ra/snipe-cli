// Dump the CV atom split, for `go run ./cmd/gate atoms`.
//
// Atoms are what retrieval can see. One the splitter drops can never be matched
// to a requirement, and the report reads that as a Gap rather than as a bug —
// which is exactly how this stayed invisible in the Node tree for three days
// when cv.md's headings changed spelling.
//
//   node scripts/js-atoms-harness.mjs <node-repo> > /tmp/js-atoms.tsv
//   go run ./cmd/gate atoms <node-repo> /tmp/js-atoms.tsv
//
// Order matters: the gate compares position by position, because the index is
// built in this order and a reordering moves every cached vector's id.
import { readFileSync } from 'fs';

import { fileURLToPath } from 'url';
import { dirname, resolve } from 'path';

// Both implementations live in this repo now, so the default is its root,
// resolved from this file rather than the cwd: a dynamic import() resolves
// against the module while readFileSync resolves against the process.
const REPO = resolve(process.argv[2] || process.env.SNIPE_NODE_REPO
  || resolve(dirname(fileURLToPath(import.meta.url)), '..'));
const { extractCvAtoms } = await import(`${REPO}/batch/embeddings.mjs`);

// extractCvAtoms appends article-digest.md itself, resolved against its own
// module directory — so this must run against the same checkout the gate reads
// the digest from, and the cv.md passed here has to be that repo's.
const cv = readFileSync(process.argv[3] || `${REPO}/cv.md`, 'utf8');

for (const a of extractCvAtoms(cv)) console.log(`${a.source}\t${a.text}`);
