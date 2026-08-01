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

// ── JD index and past-offer retrieval ───────────────────────────────
//
// loadJdIndex/similarPastOffers are the calibration RAG: they embed every
// cached JD once, then join the nearest past offers to their eval payloads,
// the labels file and the tracker's real-world outcome. All three joins are
// silent on a miss, so only a fixture with known ids proves they happen.
// Driven against the fake embedder — no model, and the real index is restored.
{
  const { startFakeOllama } = await import('./fake-ollama.mjs');
  const { loadJdIndex, similarPastOffers, loadOutcomes } = await import(
    pathToFileURL(join(ROOT, 'batch/embeddings.mjs')).href
  );
  const { preserve, writeFileSync, mkdirSync, rmSync } = await import('./harness.mjs');

  const IDS = ['998001', '998002'];
  const restore = preserve(['batch/jd-index.json']);
  const planted = [];
  const plant = (rel, body) => {
    const p = join(ROOT, rel);
    mkdirSync(join(ROOT, rel.slice(0, rel.lastIndexOf('/'))), { recursive: true });
    writeFileSync(p, body, 'utf8');
    planted.push(p);
  };
  const server = await startFakeOllama();

  try {
    plant(`batch/jds/${IDS[0]}.txt`, 'Senior Rust engineer building distributed payment infrastructure. '.repeat(20));
    plant(`batch/jds/${IDS[1]}.txt`, 'Frontend React designer working on marketing landing pages. '.repeat(20));
    plant(`batch/evals/${IDS[0]}.json`, JSON.stringify({
      status: 'evaled', id: IDS[0], company: 'Past Corp', role: 'Rust Engineer',
      score: 4.4, final_decision: 'Apply', report_num: 991,
    }));
    // No eval payload for IDS[1] — an indexed JD with nothing to learn from must
    // be skipped rather than returned with an undefined score.
    plant('batch/labels.tsv', `${IDS[0]}\t4.5\n`);

    const opts = { model: 'snipe-embed', ollamaUrl: server.url };
    const entries = await loadJdIndex(opts);
    const ids = new Set(entries.map(e => e.id));
    if (IDS.every(id => ids.has(id))) pass('loadJdIndex embeds every cached JD it has not seen before');
    else fail(`loadJdIndex indexed ${entries.length} JDs but missed the fixtures`);

    // Second call must hit the on-disk index rather than re-embedding.
    const before = server.calls.filter(c => c.path === '/api/embed').length;
    await loadJdIndex(opts);
    if (server.calls.filter(c => c.path === '/api/embed').length === before) {
      pass('loadJdIndex is incremental — a second call embeds nothing');
    } else fail('loadJdIndex re-embedded JDs that were already in the index');

    const near = await similarPastOffers('Rust distributed payments platform', IDS[1], 3, opts);
    if (near.some(o => o.id === IDS[0])) pass('similarPastOffers returns a past offer with its eval score');
    else fail(`similarPastOffers returned ${JSON.stringify(near.map(o => o.id))}`);
    if (!near.some(o => o.id === IDS[1])) pass('similarPastOffers excludes the offer being scored');
    else fail('similarPastOffers returned the offer it was asked to exclude');

    const hit = near.find(o => o.id === IDS[0]);
    if (hit && hit.score === 4.4 && hit.company === 'Past Corp' && hit.decision === 'Apply') {
      pass('similarPastOffers joins the eval payload onto each hit');
    } else fail(`the joined eval payload was ${JSON.stringify(hit)}`);
    if (hit && hit.user_label === 4.5) pass('similarPastOffers joins the user label from labels.tsv');
    else fail(`user_label was ${JSON.stringify(hit?.user_label)}, expected 4.5`);

    // Outcomes are joined live from the tracker at query time, never baked into
    // the index — a status change has to show up without a rebuild.
    const trackerFixture = join(ROOT, 'batch/.outcomes-fixture.md');
    plant('batch/.outcomes-fixture.md', [
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |',
      '| 991 | 2026-07-01 | Past Corp | Rust Engineer | 4.4/5 | Interview | Y | [991](x) | — |',
      '| 992 | 2026-07-01 | Other | Role | 3.0/5 | Evaluated | N | [992](x) | — |',
    ].join('\n'));
    const outcomes = loadOutcomes(trackerFixture);
    if (outcomes.get(991) === 'reached interview') pass('loadOutcomes maps a tracker status to its outcome phrase');
    else fail(`loadOutcomes gave ${JSON.stringify(outcomes.get(991))} for an Interview row`);
    if (!outcomes.has(992)) pass('loadOutcomes treats "Evaluated" as no outcome yet');
    else fail('loadOutcomes invented an outcome for an un-actioned row');
    if (loadOutcomes(join(ROOT, 'batch/.does-not-exist.md')).size === 0) {
      pass('loadOutcomes returns an empty map when the tracker is absent');
    } else fail('loadOutcomes did not tolerate a missing tracker');
  } finally {
    await server.close();
    for (const p of planted) rmSync(p, { force: true });
    restore();
  }
}

// ── CV atom extraction ──────────────────────────────────────────────────────

// Regression: the contact header is skipped so **Email:** never becomes an atom,
// which took **Location:** with it. No location atom meant a JD's "currently
// based in <countries>" MUST could only ever grade Gap — it did on reports 050
// and 144, both against a CV that says Edinburgh, UK on line 3.
{
  const { extractCvAtoms } = await import(
    pathToFileURL(join(ROOT, 'batch/embeddings.mjs')).href
  );
  const cv = [
    '# Alex Fixture',
    '**Email:** alex@example.com | **Phone:** +44 7700 900000 | **Location:** Edinburgh, UK (open to remote)',
    '',
    '## Skills',
    '**Languages:** Rust, Java, C/C++',
    '',
    '## Education',
    '### BSc Computer Science',
    '**Location:** Berlin',
  ].join('\n');
  const atoms = extractCvAtoms(cv);
  const texts = atoms.map(a => a.text);

  const locAtom = atoms.find(a => a.source === 'contact');
  if (locAtom && /Edinburgh, UK/.test(locAtom.text)) {
    pass('extractCvAtoms emits a location atom from the contact header');
  } else {
    fail(`no contact location atom; got ${JSON.stringify(texts)}`);
  }

  // Phrased as the JD asks it, not as the CV writes it — this is what makes the
  // embedding match "Currently based in the U.S., Canada, UK, ...".
  if (locAtom && /^Currently based in /.test(locAtom.text)) {
    pass('the location atom is phrased to match JD wording');
  } else {
    fail(`location atom reads ${JSON.stringify(locAtom?.text)}`);
  }

  // The trailing "| **Location:**" must not drag the email and phone in with it.
  if (!texts.some(t => /alex@example\.com|900000/.test(t))) {
    pass('email and phone still never become atoms');
  } else {
    fail(`contact PII leaked into the index: ${JSON.stringify(texts)}`);
  }

  // Gated on being pre-section, so a **Location:** inside Education still takes
  // the labelled-line path rather than being hijacked as a contact atom.
  if (texts.some(t => t === 'Education — Location: Berlin')) {
    pass('a **Location:** line inside a section stays a labelled atom');
  } else {
    fail(`Education location was not a labelled atom: ${JSON.stringify(texts)}`);
  }

  if (texts.some(t => /^Skills — Languages: Rust/.test(t))) {
    pass('the new branch does not shadow the labelled skills row');
  } else {
    fail(`skills row missing: ${JSON.stringify(texts)}`);
  }
}
