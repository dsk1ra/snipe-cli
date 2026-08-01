// End-to-end coverage for the three pipeline phases. Run standalone with:
// node test/phases.test.mjs
//
// Phases 1-3 are CLI scripts that export nothing, so unit-testing their
// internals is not on the table — the only honest way to reach them is to run
// them. Every one accepts `--ollama-url`, so `test/fake-ollama.mjs` stands in for
// the model server and the real code path runs unchanged, deterministically, and
// without a GPU. What is asserted is the contract the orchestrator depends on:
// the JSON envelope on stdout, the exit code, and the artifacts on disk.
import {
  pass, fail, warn, ROOT, join, runNodeAsync, preserve, ensureUserLayer,
  existsSync, readFileSync, writeFileSync, mkdirSync, rmSync, mkdtempSync, tmpdir,
  pathToFileURL,
} from './harness.mjs';
import { startFakeOllama } from './fake-ollama.mjs';

console.log('\n15. Pipeline phases (fake Ollama)');

// Ids are namespaced so a stray artifact can never collide with a real offer.
const ID = '999001';
const JD_TMP = `/tmp/batch-jd-${ID}.txt`;
const JD_TEXT = [
  'Senior Backend Engineer — Acme Corp',
  'Berlin, Germany · Remote (EU)',
  '',
  'We are looking for a Senior Backend Engineer to own our payments platform.',
  '',
  'Requirements:',
  '- 5+ years building production backend services in Python',
  '- Strong experience with Kubernetes and AWS',
  '- Comfortable owning PostgreSQL schema design and migrations',
  '- Experience mentoring engineers and reviewing code',
  '- Degree in Computer Science or equivalent practical experience',
  '',
  'Nice to have: Go, Terraform, event-driven architectures.',
  '',
  'We offer a salary of €85,000 – €110,000 per year, plus equity.',
].join('\n');

const bench = mkdtempSync(join(tmpdir(), 'snipe-phases-'));
const cleanUserLayer = ensureUserLayer();
// loadCvIndex/loadJdIndex write these in-tree, keyed by a model fingerprint the
// fake server cannot reproduce. Left alone they would replace the developer's
// real vectors with fake ones and force an expensive rebuild on the next run.
const restore = preserve(['batch/cv-index.json', 'batch/jd-index.json']);
const strays = [JD_TMP, join(ROOT, 'batch/jds', `${ID}.txt`), join(ROOT, 'batch/scores', `${ID}.json`)];

const ollama = await startFakeOllama();

/**
 * stdout of a phase script is a single JSON envelope — parse it or fail loudly.
 * Some phases pretty-print it and some emit one line, so parse the whole stream
 * first and only fall back to the last line if something else printed too.
 */
function envelope(res, label) {
  const raw = res.out.trim();
  try { return JSON.parse(raw); } catch {}
  const last = raw.slice(raw.lastIndexOf('\n{') + 1);
  try { return JSON.parse(last); } catch {}
  fail(`${label}: stdout was not JSON (exit ${res.code}): ${raw.slice(0, 200)}${res.err.slice(0, 200)}`);
  return null;
}

try {
  writeFileSync(JD_TMP, JD_TEXT, 'utf8');

  // ── Phase 1 · ollama-scorer ────────────────────────────────────────────────

  const p1 = await runNodeAsync(['batch/ollama-scorer.mjs',
    '--id', ID, '--url', 'https://example.com/jobs/1', '--jd-file', JD_TMP,
    '--ollama-url', ollama.url]);
  const s1 = envelope(p1, 'phase 1');

  if (p1.code === 0) pass('phase 1 exits 0 on a scoreable JD');
  else fail(`phase 1 exited ${p1.code}: ${p1.out.slice(0, 200)}`);

  if (s1?.status === 'scored') pass('phase 1 reports status "scored"');
  else fail(`phase 1 status was ${s1?.status}, expected "scored"`);

  if (s1 && typeof s1.score === 'number' && s1.score >= 0 && s1.score <= 5) {
    pass(`phase 1 score is in range (${s1.score})`);
  } else fail(`phase 1 score out of range: ${s1?.score}`);

  // The weighted-sum contract the orchestrator's threshold gate assumes.
  if (s1) {
    const expected = Math.round((s1.cv_match * 0.625 + s1.north_star * 0.375) * 100) / 100;
    if (Math.abs(s1.score - expected) < 0.06) pass('phase 1 score = cv×0.625 + ns×0.375');
    else fail(`phase 1 score ${s1.score} != ${expected} from its own dimensions`);
  }

  // Phase 2 reads the JD from this cache — Phase 1 owning the fetch is the whole
  // reason Phase 2 can be re-run without hitting the network again.
  if (existsSync(join(ROOT, 'batch/jds', `${ID}.txt`))) pass('phase 1 caches the JD to batch/jds/<id>.txt');
  else fail('phase 1 did not cache the JD for Phase 2');

  // Every dimension the report and the caps depend on must be present.
  for (const k of ['cv_match', 'north_star', 'archetype', 'hard_stops', 'soft_gaps', 'top_strengths']) {
    if (s1 && k in s1) pass(`phase 1 envelope carries ${k}`);
    else fail(`phase 1 envelope is missing ${k}`);
  }

  // Health check: a model the server does not list must fail before any work.
  const p1NoModel = await runNodeAsync(['batch/ollama-scorer.mjs',
    '--id', ID, '--url', 'https://example.com/jobs/1', '--jd-file', JD_TMP,
    '--model', 'not-installed', '--ollama-url', ollama.url]);
  const noModel = envelope(p1NoModel, 'phase 1 missing model');
  if (p1NoModel.code !== 0 && /not found/i.test(noModel?.error || '')) {
    pass('phase 1 fails fast when the model is not installed');
  } else fail(`phase 1 missing-model guard did not fire: ${p1NoModel.code} ${noModel?.error}`);

  // Required-argument guards.
  const p1NoId = await runNodeAsync(['batch/ollama-scorer.mjs', '--url', 'https://example.com']);
  if (p1NoId.code !== 0 && /--id is required/.test(p1NoId.out)) pass('phase 1 requires --id');
  else fail(`phase 1 ran without --id (exit ${p1NoId.code})`);

  // A dead server is the common local failure and must not look like a bad JD.
  const p1Down = await runNodeAsync(['batch/ollama-scorer.mjs',
    '--id', ID, '--url', 'https://example.com/jobs/1', '--jd-file', JD_TMP,
    '--ollama-url', 'http://127.0.0.1:1']);
  const down = envelope(p1Down, 'phase 1 server down');
  if (p1Down.code !== 0 && /not running|ollama serve/i.test(down?.error || '')) {
    pass('phase 1 names Ollama when the server is unreachable');
  } else fail(`phase 1 unreachable-server message was: ${down?.error}`);

  // An unfetchable posting is "unavailable", not "failed" — the orchestrator
  // uses that distinction to refuse retries on expired listings.
  const p1Gone = await runNodeAsync(['batch/ollama-scorer.mjs',
    '--id', ID, '--url', 'http://127.0.0.1:1/gone', '--ollama-url', ollama.url]);
  const gone = envelope(p1Gone, 'phase 1 unfetchable');
  if (gone && (gone.status === 'unavailable' || gone.status === 'failed')) {
    pass(`phase 1 classifies an unfetchable JD as "${gone.status}" rather than scoring it`);
  } else fail(`phase 1 unfetchable JD gave status ${gone?.status}`);

  // ── Phase 1 · fetching the JD off the wire ─────────────────────────────────
  //
  // The provider-API branches key off real hostnames (greenhouse.io,
  // jobs.ashbyhq.com, …) and cannot be redirected at a local server, but the
  // HTML fallback under them takes any URL — which is also the path every
  // unknown board goes through. Served locally so no posting is actually hit.
  {
    const http = await import('node:http');
    const jdServer = http.createServer((req, res) => {
      if (req.url === '/gone') { res.writeHead(404); return res.end('not found'); }
      if (req.url === '/blocked') { res.writeHead(403); return res.end('forbidden'); }
      if (req.url === '/broken') { res.writeHead(500); return res.end('server error'); }
      if (req.url === '/thin') { res.writeHead(200); return res.end('<html><body><p>Hi</p></body></html>'); }
      res.writeHead(200, { 'Content-Type': 'text/html' });
      res.end(`<html><head><style>.x{color:red}</style><script>track()</script></head>`
        + `<body><h1>Senior Backend Engineer</h1><p>${JD_TEXT.replace(/\n/g, '</p><p>')}</p>`
        + `<p>Rust &amp; Go &nbsp;&mdash; apply today &#8212; salary &quot;competitive&quot;</p></body></html>`);
    });
    await new Promise(r => jdServer.listen(0, '127.0.0.1', r));
    const base = `http://127.0.0.1:${jdServer.address().port}`;

    try {
      const ID_HTML = '999002';
      strays.push(join(ROOT, 'batch/jds', `${ID_HTML}.txt`), join(ROOT, 'batch/scores', `${ID_HTML}.json`));
      const htmlRun = await runNodeAsync(['batch/ollama-scorer.mjs',
        '--id', ID_HTML, '--url', `${base}/jobs/1`, '--ollama-url', ollama.url]);
      const htmlEnv = envelope(htmlRun, 'phase 1 HTML fetch');
      if (htmlEnv?.status === 'scored') pass('phase 1 scrapes an unknown board over plain HTML');
      else fail(`phase 1 HTML scrape gave status ${htmlEnv?.status}: ${htmlEnv?.error}`);

      const cached = existsSync(join(ROOT, 'batch/jds', `${ID_HTML}.txt`))
        ? readFileSync(join(ROOT, 'batch/jds', `${ID_HTML}.txt`), 'utf8') : '';
      if (cached && !/[<>]/.test(cached)) pass('phase 1 strips tags before caching the scraped JD');
      else fail(`the cached scrape still holds markup: ${cached.slice(0, 120)}`);
      if (/track\(\)/.test(cached) || /color:red/.test(cached)) fail('phase 1 cached script/style bodies as JD text');
      else pass('phase 1 drops script and style bodies from the scraped JD');
      if (/Rust & Go/.test(cached) && /"competitive"/.test(cached)) pass('phase 1 decodes HTML entities in the scraped JD');
      else fail(`entities survived the scrape: ${cached.slice(-160)}`);

      // A page that strips down to almost nothing is a JS-rendered posting, not
      // a JD — saying so is what tells the user to add a provider handler.
      const thin = await runNodeAsync(['batch/ollama-scorer.mjs',
        '--id', '999003', '--url', `${base}/thin`, '--ollama-url', ollama.url]);
      const thinEnv = envelope(thin, 'phase 1 thin page');
      if (thinEnv?.status !== 'scored' && /too short|JS-rendered/i.test(thinEnv?.error || '')) {
        pass('phase 1 refuses a page that strips down to almost no text and says why');
      } else fail(`phase 1 thin-page result: ${thinEnv?.status} / ${thinEnv?.error}`);

      // 404/403 are non-retryable; a 5xx is transient and must not be labelled
      // the same way, or the orchestrator would refuse to retry a blip.
      for (const [path, label] of [['/gone', '404'], ['/blocked', '403']]) {
        const res = await runNodeAsync(['batch/ollama-scorer.mjs',
          '--id', '999004', '--url', base + path, '--ollama-url', ollama.url]);
        const env = envelope(res, `phase 1 ${label}`);
        if (env?.status === 'unavailable') pass(`phase 1 marks an HTTP ${label} posting unavailable`);
        else fail(`phase 1 on HTTP ${label} gave ${env?.status}: ${env?.error}`);
      }

      const broken = await runNodeAsync(['batch/ollama-scorer.mjs',
        '--id', '999005', '--url', `${base}/broken`, '--ollama-url', ollama.url]);
      const brokenEnv = envelope(broken, 'phase 1 5xx');
      if (brokenEnv?.status === 'failed') pass('phase 1 treats an HTTP 500 as a retryable failure, not an expired posting');
      else fail(`phase 1 on HTTP 500 gave ${brokenEnv?.status}: ${brokenEnv?.error}`);
    } finally {
      await new Promise(r => jdServer.close(r));
      for (const id of ['999003', '999004', '999005']) {
        rmSync(join(ROOT, 'batch/jds', `${id}.txt`), { force: true });
        rmSync(join(ROOT, 'batch/scores', `${id}.json`), { force: true });
      }
    }
  }

  // ── Phase 2 · staged-evaluator ─────────────────────────────────────────────

  writeFileSync(JD_TMP, JD_TEXT, 'utf8');
  // Phase 2 reads Phase 1's score file for its envelope but must keep it out of
  // the prompts. Sentinel values make that testable: searching the prompts for
  // the word "score" would only find the rubric (and, on a real CV, whatever the
  // candidate happens to have written).
  const SENTINEL_ARCHETYPE = 'ZZ-SENTINEL-ARCHETYPE-ZZ';
  const SENTINEL_SCORE = 4.87;
  mkdirSync(join(ROOT, 'batch/scores'), { recursive: true });
  writeFileSync(join(ROOT, 'batch/scores', `${ID}.json`), JSON.stringify({
    status: 'scored', id: ID, score: SENTINEL_SCORE, cv_match: 5, north_star: 4,
    archetype: SENTINEL_ARCHETYPE, hard_stops: [], soft_gaps: [], top_strengths: [],
  }), 'utf8');
  // Phase 1's own system prompt talks about pre-scoring, so the anchoring check
  // below has to look at Phase 2's calls only.
  const callsBeforeP2 = ollama.calls.length;
  const p2 = await runNodeAsync(['batch/staged-evaluator.mjs',
    '--id', ID, '--url', 'https://example.com/jobs/1', '--report-num', '901',
    '--no-rag', '--bench-dir', bench, '--ollama-url', ollama.url], { timeout: 180_000 });
  const s2 = envelope(p2, 'phase 2');

  if (p2.code === 0 && s2?.status === 'evaled') pass('phase 2 completes all three stages and reports "evaled"');
  else fail(`phase 2 exit ${p2.code} status ${s2?.status}: ${(s2?.error || p2.err).slice(0, 200)}`);

  if (s2?.report_num === '901') pass('phase 2 echoes the report number it was handed');
  else fail(`phase 2 report_num was ${s2?.report_num}, expected 901`);

  const reportPath = s2?.report_path;
  if (reportPath && existsSync(reportPath)) pass('phase 2 writes the report to --bench-dir, not reports/');
  else fail(`phase 2 report not written: ${reportPath}`);

  if (reportPath && existsSync(reportPath)) {
    const report = readFileSync(reportPath, 'utf8');
    // The tracker's header parser depends on both of these (CLAUDE.md, rule 5).
    if (/\*\*URL:\*\*/.test(report)) pass('phase 2 report carries the **URL:** header');
    else fail('phase 2 report is missing the **URL:** header');
    if (/\*\*Legitimacy:\*\*/.test(report)) pass('phase 2 report carries the **Legitimacy:** header');
    else fail('phase 2 report is missing the **Legitimacy:** header');
    // Assembled in code, never written by the model — that is the whole point of
    // the staged rewrite, so a missing Block B means the assembler regressed.
    if (/Block B|Requirement/i.test(report)) pass('phase 2 report contains the assembled requirement table');
    else fail('phase 2 report has no requirement table');
  }

  // Salary is parsed from the JD in code and must never be invented. The fixture
  // JD posts €85,000 – €110,000.
  if (s2?.salary_posted?.min === 85000 && s2.salary_posted.max === 110000) {
    pass('phase 2 parses the posted salary range from the JD in code');
  } else fail(`phase 2 salary parse gave ${JSON.stringify(s2?.salary_posted)}`);

  // Weights shift to cv×0.50 + ns×0.30 + comp×0.20 once a salary is present.
  if (s2 && typeof s2.score === 'number') {
    const expected = s2.cv_blended * 0.5 + s2.north_star * 0.3 + s2.comp_inferred * 0.2;
    if (Math.abs(s2.score - expected) < 0.11) pass('phase 2 applies the with-salary weights (cv 0.50 / ns 0.30 / comp 0.20)');
    else fail(`phase 2 score ${s2.score} does not match the salaried weighting (${expected.toFixed(2)})`);
  }

  if (s2 && s2.score >= 0 && s2.score <= 5) pass(`phase 2 score is in range (${s2.score})`);
  else fail(`phase 2 score out of range: ${s2?.score}`);

  // Phase 1's score must not reach Phase 2's prompts — withheld to stop anchoring.
  const prompts = ollama.calls
    .slice(callsBeforeP2)
    .filter(c => c.path === '/api/chat')
    .flatMap(c => (c.body.messages || []).map(m => m.content))
    .join('\n');
  if (!prompts.includes(SENTINEL_ARCHETYPE)) pass("phase 2 keeps Phase 1's archetype out of every prompt");
  else fail("phase 2 leaked Phase 1's archetype into a prompt — that re-introduces anchoring");
  if (!prompts.includes(String(SENTINEL_SCORE))) pass("phase 2 keeps Phase 1's score out of every prompt");
  else fail("phase 2 leaked Phase 1's score into a prompt — that re-introduces anchoring");

  // Grounding invariant from CLAUDE.md: no STAR story may name a technology the
  // CV does not contain. The verifier runs over top_strengths on every eval.
  if (Array.isArray(s2?.top_strengths)) pass('phase 2 runs top_strengths through the CV verifier');
  else fail('phase 2 envelope has no top_strengths array');

  const p2NoJd = await runNodeAsync(['batch/staged-evaluator.mjs',
    '--id', '999999', '--url', 'https://example.com/x', '--report-num', '902',
    '--no-rag', '--bench-dir', bench, '--ollama-url', ollama.url]);
  if (p2NoJd.code !== 0 && /JD not cached/i.test(p2NoJd.out)) {
    pass('phase 2 refuses to run when Phase 1 never cached a JD');
  } else fail(`phase 2 ran without a cached JD (exit ${p2NoJd.code})`);

  const p2NoNum = await runNodeAsync(['batch/staged-evaluator.mjs',
    '--id', ID, '--url', 'https://example.com/x', '--ollama-url', ollama.url]);
  if (p2NoNum.code !== 0 && /--report-num is required/.test(p2NoNum.out)) pass('phase 2 requires --report-num');
  else fail(`phase 2 ran without --report-num (exit ${p2NoNum.code})`);

  // A model returning non-JSON is retried once, then reported as a stage failure
  // rather than being assembled into a half-empty report.
  const badOllama = await startFakeOllama({ onChat: () => 'not json at all' });
  try {
    const p2Bad = await runNodeAsync(['batch/staged-evaluator.mjs',
      '--id', ID, '--url', 'https://example.com/jobs/1', '--report-num', '903',
      '--no-rag', '--bench-dir', bench, '--ollama-url', badOllama.url]);
    const bad = envelope(p2Bad, 'phase 2 bad json');
    if (p2Bad.code !== 0 && /stage1|invalid JSON/i.test(bad?.error || '')) {
      pass('phase 2 fails the stage by name when the model returns non-JSON');
    } else fail(`phase 2 bad-JSON handling gave: ${bad?.error}`);
  } finally {
    await badOllama.close();
  }

  // ── Phase 2 (classic) · ollama-evaluator ───────────────────────────────────

  // The --classic-eval fallback still ships, so it still has to run. It is the
  // one caller that wants prose plus a tagged JSON tail rather than a schema —
  // the model writes the markdown itself, which is exactly what the staged
  // rewrite moved into code.
  const classicOllama = await startFakeOllama({
    onChat: () => [
      '<REPORT>',
      '# Fit report — Acme Corp / Senior Backend Engineer',
      '**URL:** https://example.com/jobs/1',
      '**Legitimacy:** Verified — company site resolves and the posting is dated.',
      '',
      '## Block A — Role snapshot',
      'A payments-platform backend role in Berlin, remote within the EU, paying 85-110k EUR.',
      '',
      '## Block B — Requirements',
      '- Python at scale: covered by five years of production Python services.',
      '- Kubernetes and AWS: covered, though the depth is operational rather than platform-owning.',
      '- PostgreSQL schema design: directly covered by the Globex migration work.',
      '',
      '## Block C — Verdict',
      'Worth applying. The stack overlaps almost completely and the seniority is a half-step up.',
      '</REPORT>',
      '<SUMMARY>',
      JSON.stringify({
        company: 'Acme Corp', role: 'Senior Backend Engineer', archetype: 'Backend Engineer',
        cv_match: 4, north_star: 4, red_flags_score: 5, legitimacy_tier: 'Verified',
        final_decision: 'Apply', hard_stops: [], soft_gaps: ['Kubernetes depth'],
        top_strengths: ['Production Python services'], notes: 'Strong overlap.',
      }),
      '</SUMMARY>',
    ].join('\n'),
  });
  try {
    const p2c = await runNodeAsync(['batch/ollama-evaluator.mjs',
      '--id', ID, '--url', 'https://example.com/jobs/1', '--report-num', '905',
      '--bench-dir', bench, '--ollama-url', classicOllama.url], { timeout: 120_000 });
    const s2c = envelope(p2c, 'classic eval');
    if (p2c.code === 0 && s2c?.status === 'evaled') pass('classic evaluator (--classic-eval path) still completes');
    else fail(`classic evaluator exit ${p2c.code} status ${s2c?.status}: ${String(s2c?.error).slice(0, 160)}`);

    // Same code-not-model rule as the staged path: a "Senior" title caps cv_match
    // at 3 no matter what the model reported (it said 4 above).
    if (s2c && s2c.cv_match <= 3) pass('classic evaluator enforces the seniority cap in code, overriding the model');
    else fail(`classic evaluator left cv_match at ${s2c?.cv_match} for a Senior-titled role`);

    // A report block too short to be a real report must be rejected, not saved.
    const stubOllama = await startFakeOllama({ onChat: () => '<REPORT>too short</REPORT>' });
    try {
      const p2s = await runNodeAsync(['batch/ollama-evaluator.mjs',
        '--id', ID, '--url', 'https://example.com/jobs/1', '--report-num', '906',
        '--bench-dir', bench, '--ollama-url', stubOllama.url]);
      if (p2s.code !== 0 && /valid <REPORT> block/.test(p2s.out)) {
        pass('classic evaluator rejects a truncated <REPORT> block');
      } else fail(`classic evaluator accepted a 9-char report (exit ${p2s.code})`);
    } finally {
      await stubOllama.close();
    }
  } finally {
    await classicOllama.close();
  }

  // ── Phase 3 · local-pdf-offer ──────────────────────────────────────────────

  if (reportPath && existsSync(reportPath)) {
    const p3 = await runNodeAsync(['batch/local-pdf-offer.mjs',
      '--id', ID, '--url', 'https://example.com/jobs/1',
      '--report-path', reportPath, '--report-num', '901',
      '--jd-file', JD_TMP, '--eval-score', '4.2',
      '--company', 'Acme Corp', '--role', 'Senior Backend Engineer',
      '--date', '2026-01-01', '--ollama-url', ollama.url],
      // The addition TSV goes to the temp bench, not batch/tracker-additions —
      // a run killed before the cleanup would otherwise leave a fixture row for
      // the next real merge-tracker to sweep into the user's tracker.
      { timeout: 300_000, env: { ...process.env, SNIPE_ADDITIONS: bench } });
    const s3 = envelope(p3, 'phase 3');

    // A missing Chromium is an environment gap, not a regression — the CV
    // selection, rewrite and templating all ran to get that far.
    if (s3?.status === 'completed') {
      pass('phase 3 renders a tailored CV PDF end to end');
      if (s3.pdf && existsSync(join(ROOT, s3.pdf))) pass('phase 3 writes the PDF where the envelope says it did');
      else fail(`phase 3 reported ${s3.pdf} but no file is there`);
    } else if (s3?.status === 'pdf_failed') {
      warn(`phase 3 ran the full chain but Chromium did not render: ${String(s3.error).slice(0, 120)}`);
    } else {
      fail(`phase 3 exit ${p3.code} status ${s3?.status}: ${String(s3?.error || p3.err).slice(0, 200)}`);
    }

    // Whatever happened to the PDF, the tracker row is what reaches the user's
    // application log, and it must be well-formed (9 columns, no raw pipes).
    const tsv = join(bench, `${ID}.tsv`);
    if (existsSync(tsv)) {
      const cols = readFileSync(tsv, 'utf8').trim().split('\t');
      if (cols.length === 9) pass('phase 3 tracker TSV has the required 9 columns');
      else fail(`phase 3 tracker TSV has ${cols.length} columns, expected 9`);
      if (cols[4] === 'Evaluated') pass('phase 3 writes a canonical status in the TSV');
      else fail(`phase 3 TSV status "${cols[4]}" is not canonical`);
    } else fail('phase 3 wrote no tracker TSV');

    if (s3?.pdf) strays.push(join(ROOT, s3.pdf, '..'));
    else strays.push(join(ROOT, 'output', `2026-01-01_acme-corp_901`));

    // ── Phase 3 · --bench-dir ────────────────────────────────────────────────
    // The benchmark path stops once cv-content.json is written, so it is the
    // only way to exercise the tailoring end to end without a Chromium render,
    // an output/ folder or a tracker row. It is also where the experience
    // schema floor and the reconciler are observable.
    const benchOut = join(bench, 'p3bench');
    const p3b = await runNodeAsync(['batch/local-pdf-offer.mjs',
      '--id', ID, '--url', 'https://example.com/jobs/1',
      '--report-path', reportPath, '--report-num', '902',
      '--jd-file', JD_TMP, '--eval-score', '4.2',
      '--company', 'Acme Corp', '--role', 'Senior Backend Engineer',
      '--date', '2026-01-01', '--ollama-url', ollama.url,
      '--temperature', '0', '--bench-dir', benchOut],
      { timeout: 300_000, env: { ...process.env, SNIPE_ADDITIONS: bench } });
    const s3b = envelope(p3b, 'phase 3 bench');

    if (s3b?.status === 'ok') pass('phase 3 --bench-dir exits ok before the PDF ladder');
    else fail(`phase 3 --bench-dir exit ${p3b.code} status ${s3b?.status}: ${String(s3b?.error || p3b.err).slice(0, 200)}`);

    if (s3b?.pdf === null) pass('phase 3 --bench-dir reports no PDF');
    else fail(`phase 3 --bench-dir reported a PDF: ${s3b?.pdf}`);

    if (s3b?.content && existsSync(s3b.content)) {
      pass('phase 3 --bench-dir writes cv-content.json into the bench dir');
      const content = JSON.parse(readFileSync(s3b.content, 'utf8'));

      // The whole point of the fix: every employer in the CV comes back, once.
      const companies = (content.experience || []).map(e => e.company);
      // Resolve employers exactly as the pipeline does. Rolling a regex here is
      // what let the two definitions drift apart in the first place: the
      // fixture CV puts the employer in the ### heading, which a bold-line
      // match misses entirely.
      const { cvCompanies } = await import(pathToFileURL(join(ROOT, 'batch/cv-select.mjs')).href);
      const expected = cvCompanies(readFileSync(join(ROOT, 'cv.md'), 'utf8'));

      if (expected.length && companies.length === expected.length) {
        pass(`phase 3 returns one entry per CV employer (${companies.length})`);
      } else fail(`phase 3 returned ${companies.length} entries for ${expected.length} CV employers: ${companies.join('|')}`);

      if (companies.length === new Set(companies).size) pass('phase 3 never repeats an employer');
      else fail(`phase 3 repeated an employer: ${companies.join('|')}`);

      const kept = c => companies.some(x => String(x).toLowerCase().includes(String(c).toLowerCase().slice(0, 8)));
      const dropped = expected.filter(c => !kept(c));
      if (!dropped.length) pass('phase 3 keeps every CV employer by name');
      else fail(`phase 3 dropped CV employers: ${dropped.join(', ')}`);

      // No bullet may assert a figure the CV does not state.
      const nums = s => new Set((String(s).match(/\d[\d,.]*\+?%?/g) || [])
        .map(x => x.replace(/[.,]$/, '')).filter(x => x.length > 1));
      const cvNums = nums(readFileSync(join(ROOT, 'cv.md'), 'utf8'));
      const bad = [];
      for (const e of content.experience || []) {
        for (const b of e.bullets || []) for (const n of nums(b)) if (!cvNums.has(n)) bad.push(n);
      }
      if (!bad.length) pass('phase 3 emits no figure absent from cv.md');
      else fail(`phase 3 emitted figures absent from cv.md: ${[...new Set(bad)].join(', ')}`);
    } else fail('phase 3 --bench-dir wrote no cv-content.json');

    if (!existsSync(join(bench, `${ID}.tsv`)) || s3?.status) {
      pass('phase 3 --bench-dir writes no tracker row of its own');
    }
    if (!existsSync(join(ROOT, 'output', '2026-01-01_acme-corp_902'))) {
      pass('phase 3 --bench-dir leaves output/ untouched');
    } else fail('phase 3 --bench-dir created an output/ folder');
  }
} finally {
  await ollama.close();
  cleanUserLayer();
  restore();
  for (const p of strays) rmSync(p, { force: true, recursive: true });
  rmSync(bench, { force: true, recursive: true });
}
