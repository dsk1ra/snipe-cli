// The measurement tooling, which nothing measured.
//
// timing.mjs, goldset.mjs, retrieval-bench.mjs and pseudo-label.mjs are what
// decide whether a Phase 2/3 change lands, so a wrong number in any of them
// silently approves a regression. All four only ever ran by hand, which is how
// they became the four least-covered files in the repo.
//
// Everything here is offline. The metrics and statistics are pure; the embedding
// and judge calls go to a stubbed `fetch` or to test/fake-ollama.mjs, so no model
// is ever needed. Nothing writes into batch/bench/ either — SNIPE_BENCH_DIR
// redirects the bench root at a temp dir, because the real one holds a 28 MB
// embedding cache that a fixture fingerprint would overwrite.
import {
  pass, fail, warn, ROOT, join, tmpdir, mkdtempSync, mkdirSync, writeFileSync,
  readFileSync, readdirSync, rmSync, existsSync, pathToFileURL, runNodeAsync, preserve,
  ensureUserLayer,
} from './harness.mjs';
import { startFakeOllama, fakeVector } from './fake-ollama.mjs';

const eq = (actual, expected, label) =>
  actual === expected ? pass(label) : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
const near = (actual, expected, label, tol = 1e-9) =>
  Math.abs(actual - expected) <= tol ? pass(label) : fail(`${label} — got ${actual}, want ${expected}`);
const deepEq = (actual, expected, label) =>
  JSON.stringify(actual) === JSON.stringify(expected)
    ? pass(label)
    : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
const ok = (cond, label) => cond ? pass(label) : fail(label);

const TMP = mkdtempSync(join(tmpdir(), 'snipe-bench-'));

// One bench root for the whole file, exported before any bench module is
// imported: BENCH is a module-level const, and retrieval-bench.mjs imports
// tailor-harness.mjs, so whichever section imports first freezes it for both.
const BENCH_TMP = join(TMP, 'bench');
mkdirSync(BENCH_TMP, { recursive: true });
process.env.SNIPE_BENCH_DIR = BENCH_TMP;

console.log('\n20. Bench tooling — timing, goldset, retrieval-bench, pseudo-label');

// ── batch/timing.mjs ─────────────────────────────────────────────────────────
try {
  const { logCall, logWall, summarise, TIMING_HEADER } =
    await import(pathToFileURL(join(ROOT, 'batch/timing.mjs')).href);

  // Columns are positional and the CLI reads them back by index, so the header
  // and the row builder have to agree on the order or every number shifts.
  eq(TIMING_HEADER.trim().split('\t').length, 12, 'timing header declares 12 columns');

  const row = (id, phase, model, total, load, ptok, ps, otok, os, done = 'stop', extra = '-') =>
    ['2026-01-01T00:00:00.000Z', id, phase, model, total, load, ptok, ps, otok, os, done, extra].join('\t');
  const tsv = [
    TIMING_HEADER.trim(),
    row('7', 'p1-score', 'snipe-screen', 10, 0.01, 1000, 2, 200, 4),
    row('7', 'p2-judgment', 'snipe-eval', 60, 5, 3000, 20, 400, 30),
    row('8', 'p1-score', 'snipe-screen', 12, 0.02, 1100, 3, 220, 5),
    row('7', 'p3-wall', 'wall', 120, 0, 0, 0, 0, 0, 'wall'),
    row('-', 'embed', 'snipe-embed', 1, 0, 10, 1, 0, 0, 'stop', 'n=5'),
  ].join('\n');

  const s = summarise(tsv);
  eq(s.calls, 5, 'summarise skips the header row and counts the rest');
  eq(s.offers, 2, "an id of '-' is instrumentation, not an offer");
  // Wall rows enclose the model calls made inside them; adding the two would
  // double-count every call, so model_s must exclude the 120s wall row.
  eq(s.model_s, 83, 'model_s sums only the model phases');
  eq(s.load_s, 5.03, 'load_s sums load time across model phases');
  eq(s.reloads, 1, 'only the 5s load counts as a reload at the 1s threshold');
  eq(s.reloads, 1, 'the 0.01s and 0.02s loads found the model resident');
  eq(summarise(tsv, 0.005).reloads, 3, 'a lower threshold reclassifies the small loads');
  eq(s.per_jd_s, 120, 'per JD comes from the wall row when one exists');
  eq(s.per_jd_from, 'phase wall clock', 'and says so');
  deepEq(s.phases.map(p => p.phase), ['p3-wall', 'p2-judgment', 'p1-score', 'embed'],
    'phases are ordered by total time descending');
  const p1 = s.phases.find(p => p.phase === 'p1-score');
  eq(p1.calls, 2, 'the two p1 rows aggregate into one phase');
  eq(p1.total, 22, 'phase totals add');
  eq(p1.tok_per_s, 46.7, 'tok/s is output tokens over output seconds');

  // No wall rows: the per-JD figure has to fall back and label itself honestly,
  // or a run made without logWall reads as a run that took 0s per JD.
  const noWall = summarise(tsv.split('\n').filter(l => !l.includes('p3-wall')).join('\n'));
  eq(noWall.per_jd_s, 41.5, 'without wall rows per JD is model time over offers');
  eq(noWall.per_jd_from, 'model time / offers', 'and says which it used');
  const wallOnlyPhase = summarise([TIMING_HEADER.trim(), row('7', 'embed', 'wall', 4, 0, 0, 0, 0, 0)].join('\n'));
  eq(wallOnlyPhase.model_s, 0, "model 'wall' is a wall row whatever the phase is called");

  const empty = summarise('');
  eq(empty.calls, 0, 'an empty file summarises to zero calls');
  eq(empty.per_jd_s, 0, 'with no offers per JD is the model total, not a divide by zero');

  // ---- appending -----------------------------------------------------------
  const logPath = join(TMP, 'timings.tsv');
  const savedTiming = process.env.SNIPE_TIMING;
  try {
    delete process.env.SNIPE_TIMING;
    logCall('p1-score', 'm', { total_duration: 1e9 });
    ok(!existsSync(logPath), 'no SNIPE_TIMING means no file and no cost');

    process.env.SNIPE_TIMING = logPath;
    logCall('p1-score', 'snipe-screen', {
      total_duration: 2e9, load_duration: 5e8, prompt_eval_count: '10',
      prompt_eval_duration: 1e9, eval_count: 20, eval_duration: 4e9, done_reason: 'stop',
    }, { id: 5, extra: 'x' });
    logCall('p2-judgment', 'snipe-eval', null);
    logWall('p3-wall', 12.5, { id: 9 });
    const lines = readFileSync(logPath, 'utf8').trim().split('\n');
    eq(lines.length, 3, 'each call appends exactly one line');
    const c = lines[0].split('\t');
    deepEq(c.slice(1), ['5', 'p1-score', 'snipe-screen', '2', '0.5', '10', '1', '20', '4', 'stop', 'x'],
      'nanosecond durations become seconds and counts stay counts');
    ok(!Number.isNaN(Date.parse(c[0])), 'the first column is an ISO timestamp');
    deepEq(lines[1].split('\t').slice(4), ['0', '0', '0', '0', '0', '0', '-', '-'],
      'a null response logs zeros rather than NaN');
    const w = lines[2].split('\t');
    deepEq([w[1], w[3], w[4], w[10]], ['9', 'wall', '12.5', 'wall'],
      "logWall records seconds under the model name 'wall'");

    // Round trip: what logCall writes is what summarise reads.
    const back = summarise(TIMING_HEADER + readFileSync(logPath, 'utf8'));
    eq(back.calls, 3, 'summarise reads back what logCall wrote');
    eq(back.per_jd_s, 12.5, 'and finds the wall row');

    // Telemetry must never be the thing that breaks a pipeline run.
    process.env.SNIPE_TIMING = TMP;
    logCall('p1-score', 'm', { total_duration: 1e9 });
    pass('an unwritable SNIPE_TIMING path is swallowed, not thrown');
  } finally {
    if (savedTiming === undefined) delete process.env.SNIPE_TIMING;
    else process.env.SNIPE_TIMING = savedTiming;
  }

  // ---- CLI ----------------------------------------------------------------
  const rep = await runNodeAsync([join(ROOT, 'batch/timing.mjs'), 'report', logPath]);
  eq(rep.code, 0, 'timing.mjs report exits clean');
  ok(/offers 2\s+calls 3/.test(rep.out), 'report prints the offer and call counts');
  ok(/per JD 12\.5s = 0\.21 min/.test(rep.out), 'report converts per-JD seconds to minutes');
  ok(/is loading models — 0 reloads/.test(rep.out), 'report attributes load time');
  ok(/p3-wall/.test(rep.out) && /p1-score/.test(rep.out), 'report prints both the wall and model tables');

  const noArg = await runNodeAsync([join(ROOT, 'batch/timing.mjs'), 'report'], { env: { PATH: process.env.PATH } });
  eq(noArg.code, 1, 'report with no file and no SNIPE_TIMING exits 1');
  ok(/usage: timing\.mjs report/.test(noArg.err), 'and prints usage');
} catch (e) {
  fail(`timing.mjs tests crashed: ${e.message}`);
}

// ── batch/goldset.mjs ────────────────────────────────────────────────────────
const FIXTURE_CV = [
  '# Fixture Candidate', 'fixture@example.com · Berlin', '',
  '## Summary', 'Backend engineer.', '',
  '## Experience', '',
  '### Backend Engineer', '**Acme Corp** — Berlin, Germany', '2021 - Present',
  '- Built a Rust ingest service sustaining 40k events per second in production.',
  '- Cut Postgres p99 latency 60% by adding a Redis read-through cache layer.',
  '',
  '### Junior Developer', '**Globex** — Remote', '2019 - 2021',
  '- Wrote Python ETL jobs orchestrated on Kubernetes for the analytics team.',
  '',
  '## Projects', '',
  '### Snipe', '- A Node.js CLI that scores job postings against a CV with local models.',
  '- Ships a three-phase Ollama pipeline with a schema-constrained evaluator.',
  '',
  '## Skills', '- Rust, Python, Node.js, Postgres, Redis, Kubernetes', '',
].join('\n');

try {
  const { cvAtoms, parseSheet, loadExemplars, cvHash } =
    await import(pathToFileURL(join(ROOT, 'batch/goldset.mjs')).href);

  const atoms = cvAtoms(FIXTURE_CV);
  eq(atoms.length, 4, 'cvAtoms takes Experience bullets and whole Projects, nothing else');
  deepEq(atoms.map(a => a.id), [1, 2, 3, 4], 'ids are positional, 1..n');
  deepEq(atoms.map(a => a.kind), ['bullet', 'bullet', 'bullet', 'project'],
    'experience yields one atom per bullet, a project yields one per entry');
  deepEq(atoms.map(a => a.group), ['Acme Corp', 'Acme Corp', 'Globex', 'Projects'],
    'a bullet is grouped by its employer, taken from the head line before the em dash');
  eq(atoms[3].text, 'Snipe', "a project's text is its name");
  eq(atoms[3].parts.length, 2, 'a project is embedded as its separate bullets');
  ok(!atoms[3].parts.join(' ').includes('  '), "and its body is the bullets joined, not the raw block");
  eq(atoms[0].parts.length, 1, 'a bullet is its own only part');
  ok(!atoms.some(a => /Rust, Python/.test(a.text)), 'the Skills line is not a selectable atom');
  eq(cvAtoms('# no sections here').length, 0, 'a CV with no Experience or Projects yields no atoms');

  // parseSheet is the only reader of 25 minutes of hand labelling; a tick it
  // misses is a label silently lost.
  const sheet = [
    '# preamble', '- [x] **9** this line has no offer heading above it',
    '## 42 · Acme — Backend Engineer  *(eval 4.1)*',
    '- [x] **1** ticked', '- [ ] **2** not ticked', '- [X] **3** capital X counts',
    '## 43 · Globex — Data Engineer',
    '- [x] **2** second offer', 'not a checkbox at all',
  ].join('\n');
  const picks = parseSheet(sheet);
  deepEq([...picks.keys()], ['42', '43'], 'each offer heading opens a new pick set');
  deepEq([...picks.get('42')], [1, 3], 'only ticked boxes are picks, and [X] is a tick');
  deepEq([...picks.get('43')], [2], 'offers do not bleed into each other');
  eq(parseSheet('- [x] **1** orphan').size, 0, 'a tick before any heading is dropped, not crashed on');
  eq([...parseSheet('## 42 · A — B\n- [ ] **1** x').get('42')].length, 0, 'an untouched sheet has no picks');

  // ---- exemplars ----------------------------------------------------------
  const shotsPath = join(TMP, 'shots.json');
  const shot = (wantText) => ({ cvHash: 'x', shots: [{ id: '42', reqs: ['r'], jd: 'j', wantText }] });
  writeFileSync(shotsPath, JSON.stringify(shot([atoms[0].text, atoms[1].text])));
  const ex = loadExemplars(FIXTURE_CV, shotsPath);
  eq(ex.length, 1, 'an exemplar whose bullet texts all still exist loads');
  ok(ex[0].want instanceof Set && ex[0].want.has(atoms[0].text),
    'want comes back as bullet text, so cv-select never has to import goldset');
  // A reworded bullet makes the exemplar a lie about what the human picked, and
  // the reranker is worse 0-shot than with no rerank at all — so it must drop.
  writeFileSync(shotsPath, JSON.stringify(shot([atoms[0].text, 'a bullet that is not on this CV'])));
  eq(loadExemplars(FIXTURE_CV, shotsPath).length, 0, 'one unmatched bullet drops the whole exemplar');
  writeFileSync(shotsPath, JSON.stringify(shot([])));
  eq(loadExemplars(FIXTURE_CV, shotsPath).length, 0, 'an exemplar with no picks is not an exemplar');
  writeFileSync(shotsPath, '{ not json');
  eq(loadExemplars(FIXTURE_CV, shotsPath).length, 0, 'a corrupt shots file disables the reranker rather than throwing');
  eq(loadExemplars(FIXTURE_CV, join(TMP, 'absent.json')).length, 0, 'a missing shots file is empty, not fatal');

  eq(cvHash(FIXTURE_CV).length, 16, 'cvHash is a 16-char digest');
  eq(cvHash(FIXTURE_CV), cvHash(FIXTURE_CV), 'cvHash is stable for the same text');
  ok(cvHash(FIXTURE_CV) !== cvHash(FIXTURE_CV + ' '), 'and moves when the CV does');
} catch (e) {
  fail(`goldset.mjs unit tests crashed: ${e.message}`);
}

// ---- goldset CLI: sheet generation and the clobber guard --------------------
{
  const cleanUserLayer = ensureUserLayer();
  const GS = join(ROOT, 'batch/goldset.mjs');
  try {
    const out1 = join(TMP, 'sheet1.md');
    const gen = await runNodeAsync([GS, 'sheet', '--n', '2', '--min-score', '3.5', '--out', out1]);
    eq(gen.code, 0, 'goldset sheet writes a sheet');
    ok(/wrote .*sheet1\.md — \d+ offers × \d+ atoms/.test(gen.out), 'and reports what it wrote');
    const body = readFileSync(out1, 'utf8');
    ok(/^# Phase 3 selection gold set/.test(body), 'the sheet names itself');
    ok(/^\*\*1\.\*\* /m.test(body), 'the sheet lists every atom against its id');
    // The checkboxes are written per offer, and the offers come from
    // batch/local-state.tsv — which a fresh checkout does not have.
    if (/^## \d+ ·/m.test(body)) ok(/- \[ \] \*\*1\*\*/.test(body), 'and one unticked checkbox per atom per offer');
    else warn('goldset sheet had no eligible offers to lay out — no batch/local-state.tsv in this checkout');

    // The ticks cost ~25 minutes to reproduce and batch/bench/ is gitignored, so
    // there is no working copy to fall back on if the sheet is regenerated.
    const out2 = join(TMP, 'sheet2.md');
    writeFileSync(out2, '## 42 · Acme — Backend Engineer\n- [x] **1** already labelled\n');
    const clobber = await runNodeAsync([GS, 'sheet', '--n', '1', '--out', out2]);
    ok(clobber.code !== 0, 'regenerating over existing ticks fails');
    ok(/already has ticks/.test(clobber.err), 'and says the ticks are why');
    const forced = await runNodeAsync([GS, 'sheet', '--n', '1', '--out', out2, '--force']);
    eq(forced.code, 0, '--force discards them deliberately');
    ok(!/already labelled/.test(readFileSync(out2, 'utf8')), 'and the old sheet is gone');

    // A second sheet must not re-ask about offers the first already covers.
    const out3 = join(TMP, 'sheet3.md');
    writeFileSync(out2, '## 42 · Acme — Backend Engineer\n- [x] **1** labelled\n');
    const excl = await runNodeAsync([GS, 'sheet', '--n', '2', '--out', out3, '--exclude', out2]);
    eq(excl.code, 0, 'sheet --exclude runs against an already-labelled sheet');
    ok(!/^## 42 ·/m.test(readFileSync(out3, 'utf8')), 'and leaves out the offers that sheet already covers');

    const usage = await runNodeAsync([GS]);
    ok(/usage: sheet/.test(usage.out), 'goldset with no command prints usage');
  } catch (e) {
    fail(`goldset CLI tests crashed: ${e.message}`);
  } finally {
    cleanUserLayer();
  }
}

// ── batch/retrieval-bench.mjs ────────────────────────────────────────────────
try {
  const rb = await import(pathToFileURL(join(ROOT, 'batch/retrieval-bench.mjs')).href);
  const { pairAccuracy, precisionAtK, ndcg, bootstrapCI, signTest, tokens, bm25, jdIdf,
          VARIANTS, evaluate, buildContext, embedCached } = rb;

  // ---- ranking metrics ----------------------------------------------------
  const perfect = [{ id: 1, s: 9 }, { id: 2, s: 8 }, { id: 3, s: 1 }];
  eq(pairAccuracy(perfect, new Set([1, 2])), 1, 'pair accuracy is 1 when every pick outranks every non-pick');
  eq(pairAccuracy(perfect, new Set([3])), 0, 'and 0 when the ranking is exactly inverted');
  eq(pairAccuracy(perfect, new Set([2])), 0.5, 'a pick in the middle of three scores chance');
  // Without half credit for ties a variant that scores everything equally reads
  // as 1.0 or 0.0 depending on sort stability.
  eq(pairAccuracy([{ id: 1, s: 5 }, { id: 2, s: 5 }], new Set([1])), 0.5, 'ties are half credit');
  eq(pairAccuracy(perfect, new Set()), 0, 'no picks means no pairs, not a divide by zero');
  eq(pairAccuracy(perfect, new Set([1, 2, 3])), 0, 'all picks means no pairs either');

  eq(precisionAtK(perfect, new Set([1, 2])), 1, 'p@k is 1 when the top k are the picks');
  eq(precisionAtK(perfect, new Set([1, 3])), 0.5, 'p@k counts hits in the top k only');
  near(ndcg(perfect, new Set([1, 2])), 1, 'ndcg is 1 for a perfect ordering');
  near(ndcg(perfect, new Set([3])), 0.5, 'ndcg discounts a pick buried at rank 3');
  eq(ndcg(perfect, new Set()), 0, 'ndcg with no picks is 0, not NaN');

  // ---- statistics ---------------------------------------------------------
  const flat = bootstrapCI([0.1, 0.1, 0.1, 0.1]);
  near(flat.mean, 0.1, 'the bootstrap mean is the sample mean');
  ok(flat.lo > 0, 'a constant positive delta has a CI above 0');
  ok(bootstrapCI([-0.3, 0.3, -0.2, 0.2]).lo < 0, 'symmetric noise does not');
  deepEq(bootstrapCI([0.2, -0.1, 0.4]), bootstrapCI([0.2, -0.1, 0.4]),
    'the PRNG is seeded, so a reported CI is reproducible');
  near(bootstrapCI([0.2, -0.1, 0.4], 5000, 7).mean, bootstrapCI([0.2, -0.1, 0.4], 5000, 42).mean,
    'the reported mean is the sample mean, so the seed cannot move it');
  deepEq(bootstrapCI([]), { lo: 0, hi: 0, mean: 0 }, 'no offers is a zero CI, not a crash');
  ok(bootstrapCI([0.1, 0.1], 10).lo !== undefined, 'a small bootstrap still indexes inside the array');

  ok(signTest([1, 1, 1, 1, 1, 1]).p < 0.05, 'six wins and no losses is significant');
  ok(signTest([1, -1, 1, -1]).p > 0.5, 'an even split is not');
  deepEq(signTest([0, 0]), { pos: 0, neg: 0, p: 1 }, 'all ties is p=1 rather than 0/0');
  deepEq(signTest([1e-12, -1e-12]), { pos: 0, neg: 0, p: 1 }, 'deltas inside epsilon are ties');
  deepEq(signTest([0.5, -0.2, 0.3]).pos, 2, 'wins and losses are counted separately');

  // ---- lexical ------------------------------------------------------------
  deepEq(tokens('The Rust and C++ experience, 5+ years'), ['rust', 'c++', '5+'],
    'tokens keeps C++ and 5+ intact and drops the stop words');
  deepEq(tokens('-foo- .bar.'), ['foo', 'bar'], 'leading and trailing punctuation is stripped');
  deepEq(tokens('a I of'), [], 'single characters and stop words leave nothing');
  deepEq(tokens(''), [], 'an empty document has no tokens');

  // IDF over a fixture corpus, primed before anything calls jdIdf() bare —
  // the cache is module-level and first call wins.
  const jdDir = join(TMP, 'jds');
  mkdirSync(jdDir, { recursive: true });
  writeFileSync(join(jdDir, '1.txt'), 'python developer wanted, python on kubernetes');
  writeFileSync(join(jdDir, '2.txt'), 'python and postgres, kubernetes cluster work');
  writeFileSync(join(jdDir, '3.txt'), 'rust systems engineer, python tooling, kubernetes');
  writeFileSync(join(jdDir, 'notes.md'), 'rust rust rust rust rust rust — not a JD');
  const idf = jdIdf(jdDir);
  ok(idf.get('rust') > idf.get('python'), 'a term in one JD of three outweighs one in all three');
  ok(idf.get('__default__') > idf.get('rust'), 'an unseen term gets the highest weight of all');
  eq(jdIdf(join(TMP, 'does-not-exist')), idf, 'the IDF corpus is read once and cached');

  ok(bm25(tokens('Rust systems'), tokens('built a Rust microservice'), idf) > 0, 'bm25 scores a match');
  eq(bm25(tokens('Rust'), tokens('a Java servlet'), idf), 0, 'and scores no overlap at 0');
  ok(bm25(tokens('rust'), tokens('rust'), idf) > bm25(tokens('rust'), tokens('rust ' + 'filler '.repeat(40)), idf),
    'bm25 penalises the same match in a much longer document');

  // ---- the scoring context, on a stubbed fetch ----------------------------
  const { cvAtoms } = await import(pathToFileURL(join(ROOT, 'batch/goldset.mjs')).href);
  const atoms = cvAtoms(FIXTURE_CV);
  const gold = [
    { id: '11', company: 'Rustco', role: 'Systems Engineer', want: new Set([1, 4]),
      reqs: ['Strong commercial Rust experience on high-throughput services',
             'Comfortable owning Postgres schema design and caching strategy'],
      jd: 'We are hiring a Rust engineer to own our event ingest path end to end.\n'
        + 'Postgres experience is required, and you should be fluent in caching strategies.' },
    { id: '12', company: 'Pyco', role: 'Data Engineer', want: new Set([3]),
      reqs: ['Python data pipelines orchestrated on Kubernetes at scale'],
      jd: 'You will write Python ETL pipelines that run on Kubernetes for our analytics team.' },
  ];
  const gradesFile = join(TMP, 'judge-grades.json');
  writeFileSync(gradesFile, JSON.stringify({
    model: 'snipe-eval', nshot: 1, shotIds: ['11'],
    offers: { 11: { 1: 3, 2: 1, 3: 0, 4: 2 }, 12: { 1: 0, 2: 1, 3: 3, 4: 1 } },
  }));

  const realFetch = globalThis.fetch;
  let embedCalls = 0, chatCalls = 0;
  globalThis.fetch = async (url, init) => {
    const body = init?.body ? JSON.parse(init.body) : {};
    const res = (obj, status = 200) => ({
      ok: status < 300, status, json: async () => obj, text: async () => JSON.stringify(obj),
    });
    if (String(url).endsWith('/api/show')) {
      return res({ details: { parent_model: 'fake-base', parameter_size: '0.6B', quantization_level: 'Q8_0' } });
    }
    if (String(url).endsWith('/api/embed')) {
      embedCalls++;
      return res({ embeddings: body.input.map(t => fakeVector(String(t))) });
    }
    if (String(url).endsWith('/api/chat')) {
      chatCalls++;
      return res({ message: { content: `Delivered a ${body.messages[1].content.slice(0, 12)} platform, cutting cost 30%.` } });
    }
    return res({ error: 'not found' }, 404);
  };

  let ctx;
  try {
    ctx = await buildContext(gold, atoms, { gradesFile });
    eq(ctx.flat.length, 5, 'the context flattens 4 atoms into 5 embeddable parts');
    deepEq(ctx.span, [[0, 1], [1, 2], [2, 3], [3, 5]], 'span maps each atom back to its parts');
    eq(ctx.partVecs.length, 5, 'every part gets a vector');
    eq(ctx.hubness.length, 5, 'and a hubness correction');
    eq(ctx.offers.length, 2, 'both gold offers survive');
    eq(ctx.offers[0].reqVecs.length, 2, "an offer's requirements are embedded");
    eq(ctx.offers[0].instructVecs.length, 2, 'and again with the instruct prefix');
    ok(ctx.offers[0].hydeVecs?.length === 2, 'HyDE generates one hypothetical bullet per requirement');
    ok(chatCalls > 0, 'the hypotheticals come from a model call, cached on disk');
    ok(ctx.offers[0].jdSentVecs?.length > 0, 'requirement-looking sentences are pulled straight from the JD');
    deepEq(ctx.offers[0].grades, { 1: 3, 2: 1, 3: 0, 4: 2 }, 'cached judge grades are attached per offer');
    eq(ctx.offers[0].isExemplar, true, 'an offer named in shotIds is flagged as training data');
    eq(ctx.offers[1].isExemplar, false, 'the others are not');
    deepEq(ctx.exemplars, ['11'], 'and the exemplar list is reported for the runner to drop');

    // The disk cache is what makes a 20-variant sweep embed each bullet once.
    const before = embedCalls;
    const again = await embedCached(ctx.flat);
    eq(embedCalls, before, 'an already-embedded text costs no second call');
    deepEq(again[0], ctx.partVecs[0], 'and comes back in the order it was asked for');
    ok(existsSync(join(BENCH_TMP, 'hyde-cache.json')), 'the HyDE hypotheticals are cached to disk');

    // A sheet's ticks are ids frozen at labelling time. Delete a CV bullet and
    // the stale id used to index a rank that was not there: `sorted[undefined].s`.
    const stale = [{ ...gold[0], want: new Set([1, 99]) }];
    const staleCtx = await buildContext(stale, atoms, { gradesFile, withHyde: false });
    deepEq([...staleCtx.offers[0].want], [1], 'a tick for an atom no longer in cv.md is dropped');
    const staleScore = pairAccuracy(VARIANTS['base-cos'](staleCtx, staleCtx.offers[0]), staleCtx.offers[0].want);
    ok(Number.isFinite(staleScore) && staleScore >= 0 && staleScore <= 1,
      'so the metrics still score rather than crashing on a missing rank');
  } finally {
    globalThis.fetch = realFetch;
  }

  // ---- every variant ------------------------------------------------------
  // A variant that throws, returns the wrong length, or emits NaN would show up
  // in a sweep as a plausible-looking loss rather than as a bug.
  const names = Object.keys(VARIANTS);
  ok(names.length > 25, `the variant table is populated (${names.length} variants)`);
  const broken = [];
  for (const name of names) {
    try {
      const per = evaluate(ctx, VARIANTS[name]);
      const ranked = VARIANTS[name](ctx, ctx.offers[0]);
      if (ranked.length !== atoms.length) broken.push(`${name}: ${ranked.length} scores for ${atoms.length} atoms`);
      else if (ranked.some(r => !Number.isFinite(r.s))) broken.push(`${name}: non-finite score`);
      else if (per.pair.length !== 2 || per.pair.some(x => !Number.isFinite(x))) broken.push(`${name}: bad pair metric`);
    } catch (e) { broken.push(`${name}: threw ${e.message}`); }
  }
  deepEq(broken, [], 'every variant scores every atom finitely on every offer');

  // The shipped ranker is cosine plus a tenth of the judge grade, so the grades
  // have to actually move the order — a blend that changes nothing is a no-op
  // that a paired CI would report as a clean, meaningless null.
  const baseOrder = VARIANTS['base-cos'](ctx, ctx.offers[0]).sort((a, b) => b.s - a.s).map(r => r.id);
  const judgeOrder = VARIANTS['cos+judge-0.50'](ctx, ctx.offers[0]).sort((a, b) => b.s - a.s).map(r => r.id);
  ok(JSON.stringify(baseOrder) !== JSON.stringify(judgeOrder), 'a heavy judge weight reorders the cosine ranking');
  deepEq(VARIANTS['judge'](ctx, ctx.offers[0]).map(r => r.s), [3, 1, 0, 2],
    'the judge-only variant is the cached grades verbatim');
  const noGrades = { ...ctx.offers[0], grades: null };
  deepEq(VARIANTS['judge'](ctx, noGrades).map(r => r.s), [0, 0, 0, 0],
    'a missing grade file scores 0 rather than undefined');

  // The leave-one-out prior is the control that says whether this is a retrieval
  // problem at all; computed over all offers it reads back its own answer.
  const prior = VARIANTS['prior-only'](ctx, ctx.offers[0]);
  eq(prior.find(r => r.id === 3).s, 1, "the other offer's pick has prior 1");
  eq(prior.find(r => r.id === 1).s, 0, "this offer's own picks are excluded from its prior");

  const rrfScores = VARIANTS['rrf-cos-bm25'](ctx, ctx.offers[0]).map(r => r.s);
  ok(rrfScores.every(s => s > 0 && s < 1), 'RRF is scale-free, so no weight needs tuning');

  // ---- CLI ----------------------------------------------------------------
  const RB = join(ROOT, 'batch/retrieval-bench.mjs');
  const list = await runNodeAsync([RB, 'list']);
  eq(list.code, 0, 'retrieval-bench list exits clean');
  eq(list.out.trim().split('\n').length, names.length, 'and lists every variant');
  const usage = await runNodeAsync([RB]);
  ok(/usage: list \| run/.test(usage.out), 'retrieval-bench with no command prints usage');

  // selfcheck asserts on IDF over the real JD corpus (`jdIdf()`, no override), so
  // it only means anything where that corpus exists. An earlier suite creates an
  // empty batch/jds, so the dir existing is not the test — a .txt in it is.
  const jdCorpus = existsSync(join(ROOT, 'batch/jds'))
    && readdirSync(join(ROOT, 'batch/jds')).some(f => f.endsWith('.txt'));
  if (jdCorpus) {
    const self = await runNodeAsync([RB, 'selfcheck'], { env: { ...process.env, SNIPE_BENCH_DIR: BENCH_TMP } });
    eq(self.code, 0, 'retrieval-bench selfcheck passes');
    ok(/selfcheck ok/.test(self.out), 'and says so');
  } else {
    warn('skipped retrieval-bench selfcheck — its IDF assertions need batch/jds, empty in this checkout');
  }
} catch (e) {
  fail(`retrieval-bench.mjs tests crashed: ${e.stack}`);
}

// ── batch/pseudo-label.mjs ───────────────────────────────────────────────────
try {
  const { compare, judge, judgeGraded } =
    await import(pathToFileURL(join(ROOT, 'batch/pseudo-label.mjs')).href);

  const atoms4 = [{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }];
  const perfect = compare(new Set([1, 2]), new Set([1, 2]), atoms4);
  eq(perfect.jaccard, 1, 'identical label sets agree perfectly');
  eq(perfect.pair, 1, 'and rank the picks above everything else');
  const wrong = compare(new Set([3, 4]), new Set([1, 2]), atoms4);
  eq(wrong.jaccard, 0, 'disjoint sets agree not at all');
  eq(wrong.pair, 0, 'and invert the human ordering');
  eq(compare(new Set([1, 2, 3, 4]), new Set([1, 2]), atoms4).pair, 0.5,
    'selecting everything is chance, not a good oracle');
  eq(compare(new Set(), new Set(), atoms4).jaccard, 1, 'two empty sets are identical, not 0/0');
  eq(compare(new Set([1]), new Set(), atoms4).pair, 0, 'nothing to compare against scores 0');

  // ---- the judge, against the fake server --------------------------------
  const jdAtoms = [
    { id: 1, kind: 'bullet', text: 'Built a Rust ingest service' },
    { id: 2, kind: 'project', text: 'Snipe', body: 'x'.repeat(400) },
  ];
  {
    const fake = await startFakeOllama({ onChat: () => ({ selected: [2, 2, 1, 99] }) });
    try {
      const sel = await judge(['Rust experience'], jdAtoms, { ollamaUrl: fake.url, model: 'snipe-eval' });
      deepEq(sel, [1, 2], 'the judge dedups its ids, drops unknown ones and sorts');
      const sent = fake.calls.find(c => c.path === '/api/chat').body;
      eq(sent.options.temperature, 0, 'temperature 0, so a re-run reproduces the labels exactly');
      eq(sent.format.required[0], 'selected', 'the answer is schema-constrained');
      const list = sent.messages[1].content;
      ok(/2\. \[project\] Snipe — x{220}$/m.test(list),
        "a project's body is truncated to 220 chars in the prompt");
      ok(/^You are a recruiter/.test(sent.messages[0].content), 'the system prompt frames it as a recruiter');
    } finally { await fake.close(); }
  }
  {
    const fake = await startFakeOllama({ onChat: () => '' });
    try {
      deepEq(await judge(['r'], jdAtoms, { ollamaUrl: fake.url, model: 'm' }), [],
        'an empty answer is no selection rather than a crash');
    } finally { await fake.close(); }
  }
  {
    const fake = await startFakeOllama({ chatStatus: 503 });
    try {
      let msg = '';
      try { await judge(['r'], jdAtoms, { ollamaUrl: fake.url, model: 'm' }); }
      catch (e) { msg = e.message; }
      ok(/Ollama HTTP 503/.test(msg), 'an HTTP failure surfaces the status, not a silent empty label');
    } finally { await fake.close(); }
  }

  // ---- the graded judge ---------------------------------------------------
  {
    const fake = await startFakeOllama({ onChat: () => ({ grades: [{ id: 1, grade: '2' }, { id: 99, grade: 3 }] }) });
    try {
      const g = await judgeGraded(['Rust experience'], 'a'.repeat(3000), jdAtoms,
        { ollamaUrl: fake.url, model: 'snipe-eval' });
      eq(g.get(1), 2, 'a grade arrives as a number even when the model sends a string');
      eq(g.get(2), 0, 'an ungraded atom defaults to 0 rather than undefined');
      eq(g.size, 2, 'a grade for an atom that does not exist is ignored');
      const body = fake.calls.find(c => c.path === '/api/chat').body;
      eq(body.messages.length, 2, '0-shot sends one system and one user message');
      eq(body.messages[1].content.includes('a'.repeat(2500)), true, 'the JD excerpt is included');
      eq(body.messages[1].content.includes('a'.repeat(2501)), false, 'and capped at 2500 chars');
    } finally { await fake.close(); }
  }
  {
    // Few-shot is the whole point: the judge is imitating one human's taste, and
    // a worked example transfers that better than an adjective can.
    const fake = await startFakeOllama({ onChat: () => ({ grades: [] }) });
    try {
      const shots = [{ reqs: ['other requirement'], jd: 'other posting', want: new Set([2]) }];
      await judgeGraded(['r'], '', jdAtoms, { ollamaUrl: fake.url, model: 'm', shots });
      const body = fake.calls.find(c => c.path === '/api/chat').body;
      eq(body.messages.length, 4, 'one shot adds a user/assistant pair before the real question');
      deepEq(JSON.parse(body.messages[2].content).grades, [{ id: 1, grade: 0 }, { id: 2, grade: 3 }],
        "the worked answer grades the human's picks 3 and everything else 0");
      ok(!body.messages[3].content.includes('other posting'), "the real question carries its own posting");
    } finally { await fake.close(); }
  }

  // ---- CLI ---------------------------------------------------------------
  const PL = join(ROOT, 'batch/pseudo-label.mjs');
  const self = await runNodeAsync([PL, 'selfcheck']);
  eq(self.code, 0, 'pseudo-label selfcheck passes');
  const usage = await runNodeAsync([PL]);
  ok(/usage: agree \|/.test(usage.out), 'pseudo-label with no command prints usage');

  // stats reads batch/bench/pseudo-labels.json, which is gitignored scratch that
  // normally does not exist — restore whatever was there, including nothing.
  const restore = preserve(['batch/bench/pseudo-labels.json']);
  try {
    const empty = await runNodeAsync([PL, 'stats']);
    ok(/no labels yet/.test(empty.out), 'stats with no labels says so instead of dividing by zero');
    writeFileSync(join(ROOT, 'batch/bench/pseudo-labels.json'), JSON.stringify({
      model: 'snipe-eval',
      offers: { 1: { selected: [1, 2, 3], company: 'A', role: 'R', score: 4 },
                2: { selected: [2, 3], company: 'B', role: 'R', score: 4.2 } },
    }));
    const stats = await runNodeAsync([PL, 'stats']);
    ok(/2 offers · selected\/offer mean 2\.5 min 2 max 3/.test(stats.out), 'stats summarises label size');
    ok(/atom pick rate: 1:0\.50 2:1\.00 3:1\.00/.test(stats.out),
      'and reports how often each atom is picked, which is how a hub bullet shows up');
  } finally { restore(); }
} catch (e) {
  fail(`pseudo-label.mjs tests crashed: ${e.stack}`);
}

// ── batch/tailor-harness.mjs — the embedding metrics and the CLI ─────────────
// Redirected by the same SNIPE_BENCH_DIR above, for the same reason:
// bench/tailor holds the real metric-embeds cache.
{
  const TAILOR = join(BENCH_TMP, 'tailor');
  mkdirSync(TAILOR, { recursive: true });
  const cleanUserLayer = ensureUserLayer();
  const fake = await startFakeOllama();
  try {
    // Cache-busted on purpose. units.test.mjs imports this module earlier in the
    // run, with no SNIPE_BENCH_DIR set, so the cached instance has BENCH frozen
    // on the real batch/bench/tailor — and withEmbedMetrics would then merge
    // fixture vectors into the developer's real 5 MB metric-embeds cache. A
    // distinct specifier gets a fresh instance that reads the env var. Coverage
    // still attributes to the same file.
    const th = await import(`${pathToFileURL(join(ROOT, 'batch/tailor-harness.mjs')).href}?bench-root=${encodeURIComponent(BENCH_TMP)}`);

    const cvPath = join(TMP, 'fixture-cv.md');
    writeFileSync(cvPath, FIXTURE_CV, 'utf8');
    deepEq(th.selectableAtoms(FIXTURE_CV).map(a => a.section),
      ['Experience', 'Experience', 'Experience', 'Projects', 'Projects'],
      'selectableAtoms is the corpus Phase 3 selects from — bullets of both sections');
    eq(th.selectableAtoms(FIXTURE_CV)[3].entity, 'Snipe', 'each atom remembers the entry it came from');

    // ---- summaryFab ---------------------------------------------------------
    // metric_fab reads experience bullets only, so it sat at a clean 0 through
    // every run whose summary claimed a tenure, a figure and a university tier
    // cv.md never states. This names each class instead of missing all of them.
    const sfCv = [FIXTURE_CV, '', '## Education', '', '**Northgate College**',
      '**BEng (Hons) Software Engineering — First Class Honours** | 2022 – 2026'].join('\n');
    deepEq(th.summaryFab('Engineer building Rust ingest services.', sfCv), [],
      'a grounded summary reports no fabrication');
    deepEq(th.summaryFab('Engineer with 1-3 years of real production experience.', sfCv), ['tenure'],
      'a tenure range the CV never states is named');
    deepEq(th.summaryFab('Russell Group graduate with First Class Honours.', sfCv), ['credential'],
      'an ungrounded credential is named and a grounded one is not');
    eq(th.summaryFab('Engineer with 1-3 years of experience, Russell Group graduate.', sfCv).length, 2,
      'a summary carrying two classes reports both');
    deepEq(th.summaryFab('', sfCv), [], 'an empty summary reports nothing rather than throwing');

    // ---- withEmbedMetrics ---------------------------------------------------
    const rows = [
      { dir: '1_acme',
        summary: 'Backend engineer who has built Rust ingest services and tuned Postgres.',
        jdText: 'We want a Rust engineer to own an ingest path and a Postgres schema.',
        reqs: ['Strong Rust experience', 'Postgres schema design'],
        outBullets: ['Built a Rust ingest service sustaining 40k events per second in production.'] },
      // No summary and no requirements: both alignments must read 0 and the
      // regret must be null rather than dragging the mean down with a zero.
      { dir: '2_globex', summary: '', jdText: '', reqs: [], outBullets: [] },
    ];
    const em = await th.withEmbedMetrics({ n: 2, rows }, { ollamaUrl: fake.url, cvPath });
    ok(em.summary_jd_fit > -1 && em.summary_jd_fit < 1, 'summary_jd_fit is a cosine, so it stays in range');
    ok(em.summary_cv_fit > -1 && em.summary_cv_fit < 1, 'so does summary_cv_fit');
    eq(rows[1].summary_jd_fit, 0, 'a row with no summary scores 0 rather than NaN');
    eq(rows[1].selection_regret, null, 'a row with no requirements has no regret to report');
    eq(em.selection_regret_n, 1, 'and is excluded from the regret mean, not counted as 0');
    ok(em.selection_regret >= 0, 'regret is normalised, so it never goes negative');
    ok(existsSync(join(TAILOR, 'metric-embeds.json')), 'the metric embeddings are cached by content hash');
    const embedReqs = fake.calls.filter(c => c.path === '/api/embed').length;
    await th.withEmbedMetrics({ n: 2, rows: rows.map(r => ({ ...r })) }, { ollamaUrl: fake.url, cvPath });
    eq(fake.calls.filter(c => c.path === '/api/embed').length, embedReqs,
      're-scoring the same run costs no further embedding calls');

    // ---- the CLI ------------------------------------------------------------
    const content = (company, bullets, extra = {}) => JSON.stringify({
      summary: 'A backend engineer with Rust and Postgres experience.',
      experience: [{ company, bullets }], ...extra,
    });
    const writeRun = (label, dir, body, jd = 'Rust and Postgres, on Kubernetes.') => {
      const d = join(TAILOR, label, dir);
      mkdirSync(d, { recursive: true });
      writeFileSync(join(d, 'cv-content.json'), body, 'utf8');
      writeFileSync(join(d, 'job-description.txt'), jd, 'utf8');
    };
    writeRun('A', '1_acme', content('Acme Corp', ['Built a Rust ingest service sustaining 40k events per second.']));
    writeRun('A', '2_globex', content('Globex', ['Wrote Python ETL jobs orchestrated on Kubernetes.']));
    writeRun('A', '3_broken', '{ not json at all');
    mkdirSync(join(TAILOR, 'A', '4_empty'), { recursive: true });
    writeFileSync(join(TAILOR, 'A', 'meta.json'), JSON.stringify({ label: 'A', temperature: 0 }), 'utf8');
    writeRun('B', '1_acme', content('Acme Corp', ['Built an ingest service handling 999k events per second.']));

    const TH = join(ROOT, 'batch/tailor-harness.mjs');
    const env = { ...process.env, SNIPE_BENCH_DIR: BENCH_TMP };
    const sample = await runNodeAsync([TH, 'sample', '--n', '3', '--min-score', '3.5'], { env });
    eq(sample.code, 0, 'tailor-harness sample writes the fixed sample');
    ok(/wrote \d+ offers to .*sample\.tsv/.test(sample.out), 'and says how many offers it holds');
    ok(readFileSync(join(TAILOR, 'sample.tsv'), 'utf8').startsWith('id\treport_num\treport\tjd\tcompany\trole\tscore'),
      'the sample carries the columns metricsFor keys its reports on');

    const metrics = await runNodeAsync([TH, 'metrics', 'A', '--no-embed', '--rows'], { env });
    eq(metrics.code, 0, 'tailor-harness metrics scores a run offline with --no-embed');
    const summary = JSON.parse(metrics.out.slice(0, metrics.out.indexOf('}\n') + 1));
    eq(summary.n, 2, 'an unparseable cv-content.json and a dir without one are skipped, not counted');
    eq(summary.meta.label, 'A', "the run's meta.json is carried into the metrics");
    ok(typeof summary.grounding === 'number' && typeof summary.num_retention === 'number',
      'every headline metric comes back as a number');
    ok(/1_acme\s+pg=\S+ bal=\d+\/\d+!? diff=\S+ noise=\S+ yield=\S+ roles=1 fab=/.test(metrics.out),
      '--rows prints one line per offer, pages first then the label metrics');
    // The fixture has no labels in batch/bench/opus/labels for these ids, so the
    // label columns must degrade to a dash rather than to a number that would
    // read as a real score.
    ok(/diff=- /.test(metrics.out), 'an offer with no label shows a dash, not a zero');
    ok(/reg=-/.test(metrics.out), 'and shows regret as a dash when --no-embed skipped it');

    // A --limit run holds a prefix of the sample, so an unpaired compare is
    // mostly the difference between two offer sets rather than the change.
    const cmp = await runNodeAsync([TH, 'compare', 'A', 'B', '--no-embed'], { env });
    eq(cmp.code, 0, 'tailor-harness compare runs');
    ok(/paired on 1 offers common to both runs \(A: 2, B: 1\)/.test(cmp.out),
      'compare pairs on the offers both runs produced and says how many');
    // metric_fab depends on the real cv.md's figures, so the deterministic
    // assertion is the row shape and the paired n, not a particular delta.
    ok(/^n\s+1\s+1\s+0\.000$/m.test(cmp.out), 'the paired table reports one offer on each side');
    ok(/^metric_fab\s+\S+/m.test(cmp.out) && /^selection_regret\s+/m.test(cmp.out),
      'and prints every headline metric, including the ones --no-embed left null');

    // ---- the page metric (E0) ----------------------------------------------
    // Every other metric here scores cv-content.json, which local-pdf-offer
    // writes and then exits on in bench mode, so the whole suite was blind to
    // how tall the CV actually renders. A one-page experiment run against a
    // blind harness scores every arm identically and reads as a clean null.
    //
    // The check that the harness is NOT blind: an arm carrying a deliberately
    // longer summary must measure taller. The summary is the right lever
    // because it renders straight from the content JSON — experience and
    // project bullets are matched back to cv.md first, so a fixture company
    // that cv.md has never heard of would render the real CV's bullets in both
    // arms and the two would come out identical.
    writeRun('SHORT', '1_acme', content('Acme Corp', ['Built a Rust ingest service.']));
    writeRun('LONG', '1_acme', content('Acme Corp', ['Built a Rust ingest service.'], {
      summary: 'A backend engineer with Rust and Postgres experience. '.repeat(40),
    }));
    for (const l of ['SHORT', 'LONG']) {
      writeFileSync(join(TAILOR, l, 'meta.json'), JSON.stringify({ label: l, temperature: 0 }), 'utf8');
    }
    const short = await runNodeAsync([TH, 'metrics', 'SHORT', '--no-embed'], { env });
    const long  = await runNodeAsync([TH, 'metrics', 'LONG',  '--no-embed'], { env });
    eq(short.code, 0, 'tailor-harness metrics renders the page for a run');
    const sM = JSON.parse(short.out.slice(0, short.out.indexOf('}\n') + 1));
    const lM = JSON.parse(long.out.slice(0, long.out.indexOf('}\n') + 1));
    ok(typeof sM.pages === 'number' && typeof sM.page_px === 'number',
      'metrics reports how many pages the CV renders to');
    ok(typeof sM.one_page_rate === 'number' && typeof sM.one_page_n === 'number',
      'and the one-page rate, which is the gate rather than the mean');
    ok(lM.page_px > sM.page_px,
      `a longer CV measures taller (${sM.page_px}px -> ${lM.page_px}px) — the harness can see the page`);
    ok(lM.pages > sM.pages, 'and reports it as more pages, not just more pixels');

    const noPages = await runNodeAsync([TH, 'metrics', 'SHORT', '--no-embed', '--no-pages'], { env });
    eq(noPages.code, 0, '--no-pages runs without a browser');
    ok(!/"pages"/.test(noPages.out.slice(0, noPages.out.indexOf('}\n') + 1)),
      'and reports no page metric at all rather than a zero that would read as fitting');

    // A run whose offers all fail must still finish and record the arm it was.
    writeFileSync(join(TAILOR, 'sample.tsv'),
      'id\treport_num\treport\tjd\tcompany\trole\tscore\n'
      + '9001\t990\t/nonexistent/report.md\t/nonexistent/jd.txt\tAcme\tEngineer\t4.2\n'
      + '9002\t991\t/nonexistent/report2.md\t/nonexistent/jd2.txt\tGlobex\tEngineer\t4.0\n', 'utf8');
    const runOut = await runNodeAsync([TH, 'run', 'C', '--limit', '1', '--ollama-url', fake.url], { env });
    eq(runOut.code, 0, 'a variant run survives an offer that fails outright');
    const meta = JSON.parse(runOut.out);
    eq(meta.n, 1, '--limit takes a prefix of the sample, so two limited runs stay paired');
    eq(meta.limit, 1, 'and records the limit so a later reader cannot mistake it for a full run');
    eq(meta.failed, 1, 'the failed offer is counted, not swallowed');
    eq(meta.ok, 0, 'and not counted as a success');
    ok('SNIPE_BENCH_DIR' in meta.flags, 'env-gated behaviour is recorded, since it is invisible in the output dir');
    deepEq(JSON.parse(readFileSync(join(TAILOR, 'C', 'meta.json'), 'utf8')).n, 1, 'meta.json lands beside the run');

    const usage = await runNodeAsync([TH], { env });
    ok(/usage: sample --n 24/.test(usage.out), 'tailor-harness with no command prints usage');
    const noLabel = await runNodeAsync([TH, 'run'], { env });
    ok(noLabel.code !== 0 && /usage: run <label>/.test(noLabel.err), 'run without a label refuses');
    const noRun = await runNodeAsync([TH, 'metrics', 'nope', '--no-embed'], { env });
    ok(noRun.code !== 0 && /no run at/.test(noRun.err), 'metrics for a label that was never run says so');
  } catch (e) {
    fail(`tailor-harness tests crashed: ${e.stack}`);
  } finally {
    await fake.close();
    cleanUserLayer();
  }
}

delete process.env.SNIPE_BENCH_DIR;

rmSync(TMP, { recursive: true, force: true });
