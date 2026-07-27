// Retrieval-integrity checks for batch/embeddings.mjs. Run standalone with:
// node test/embeddings.test.mjs
import {
  pass, fail, ROOT, join, pathToFileURL,
} from './harness.mjs';

// ── 13. EMBEDDING INDEX INTEGRITY ───────────────────────────────

console.log('\n13. Embedding index integrity');

const { cosine, topK, modelFingerprint } = await import(
  pathToFileURL(join(ROOT, 'batch/embeddings.mjs')).href
);

// Regression: a stale index (built by a different embedder) used to make cosine
// return 0 for every atom instead of failing — retrieval silently degraded to
// "first k atoms" and every eval scored against arbitrary CV lines.
try {
  cosine(new Array(2560).fill(0.1), new Array(1024).fill(0.1));
  fail('cosine() must throw on embedding dim mismatch (got a value instead)');
} catch (e) {
  if (/dim mismatch/i.test(e.message) && /rebuild/i.test(e.message)) {
    pass('cosine() throws on dim mismatch and names the rebuild command');
  } else {
    fail(`cosine() threw, but not the dim-mismatch guard: ${e.message}`);
  }
}

// The guard must not fire on the normal path.
const sim = cosine([1, 0, 0], [1, 0, 0]);
if (Math.abs(sim - 1) < 1e-9) pass('cosine() returns 1 for identical vectors');
else fail(`cosine() identical vectors → ${sim}, expected 1`);

const orth = cosine([1, 0], [0, 1]);
if (Math.abs(orth) < 1e-9) pass('cosine() returns 0 for orthogonal vectors');
else fail(`cosine() orthogonal vectors → ${orth}, expected 0`);

// A zero vector has no direction; the d===0 branch must stay a 0, not NaN.
const zero = cosine([0, 0], [1, 1]);
if (zero === 0) pass('cosine() returns 0 (not NaN) for a zero vector');
else fail(`cosine() zero vector → ${zero}, expected 0`);

// topK ranks by similarity, so a stale-index throw propagates rather than
// producing a confidently-wrong ordering.
try {
  topK(new Array(4).fill(0.5), [{ text: 'a', vec: [1, 2] }], 1);
  fail('topK() must propagate the dim-mismatch throw');
} catch (e) {
  if (/dim mismatch/i.test(e.message)) pass('topK() propagates the dim-mismatch guard');
  else fail(`topK() threw unexpectedly: ${e.message}`);
}

const ranked = topK([1, 0], [
  { text: 'orthogonal', vec: [0, 1] },
  { text: 'exact', vec: [1, 0] },
], 1);
if (ranked.length === 1 && ranked[0].text === 'exact') pass('topK() ranks the nearest vector first');
else fail(`topK() picked ${JSON.stringify(ranked.map(r => r.text))}, expected ["exact"]`);

// The index cache key must change when the base behind the tag changes,
// otherwise `ollama create snipe-embed` on a new base keeps the stale index.
const fpA = await modelFingerprint({ model: 'qwen3-embedding:0.6b-q8_0' });
const fpB = await modelFingerprint({ model: 'qwen3-embedding:4b' });
if (fpA !== fpB) pass('modelFingerprint() distinguishes two embedder bases');
else fail(`modelFingerprint() collapsed both bases to "${fpA}"`);

// Offline / unreachable Ollama must degrade to the tag, not crash the pipeline.
const offline = await modelFingerprint({ model: 'snipe-embed', ollamaUrl: 'http://127.0.0.1:1' });
if (offline === 'snipe-embed') pass('modelFingerprint() falls back to the tag when Ollama is unreachable');
else fail(`modelFingerprint() offline fallback → "${offline}", expected "snipe-embed"`);
