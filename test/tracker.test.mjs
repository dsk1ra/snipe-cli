// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 12. TRACKER REPORT LINK NORMALIZATION (#760) ────────────────

console.log('\n12. Tracker report-link normalization');

try {
  const { normalizeReportLink } = await import(pathToFileURL(join(ROOT, 'tracker/tracker-links.mjs')).href);
  const repo = '/repo';
  const dataDir = join(repo, 'data');

  // data/ layout: root-relative TSV link → ../reports/...
  const fromTsv = normalizeReportLink('[12](reports/012-acme-2026-01-04.md)', dataDir, repo);
  if (fromTsv === '[12](../reports/012-acme-2026-01-04.md)') {
    pass('data/ layout: root-relative link rewritten to ../reports/...');
  } else {
    fail(`data/ layout normalization wrong: ${fromTsv}`);
  }

  // Idempotent: re-running on an already-normalized link must not double-prefix
  const twice = normalizeReportLink(fromTsv, dataDir, repo);
  if (twice === fromTsv) {
    pass('normalization is idempotent (no double-prefix on re-run)');
  } else {
    fail(`normalization not idempotent: ${twice}`);
  }

  // Root layout: tracker at repo root → link stays reports/...
  const atRoot = normalizeReportLink('[12](reports/012-acme-2026-01-04.md)', repo, repo);
  if (atRoot === '[12](reports/012-acme-2026-01-04.md)') {
    pass('root layout: link stays root-relative reports/...');
  } else {
    fail(`root layout normalization wrong: ${atRoot}`);
  }

  // Non-report links are left untouched — including external URLs that happen
  // to contain an embedded "/reports/" segment (must not be rewritten).
  const other = normalizeReportLink('[site](https://example.com/reports/foo.md)', dataDir, repo);
  if (other === '[site](https://example.com/reports/foo.md)') {
    pass('non-report links (incl. URLs with embedded /reports/) are left untouched');
  } else {
    fail(`non-report link altered: ${other}`);
  }

  // End-to-end migration against a fictional fixture tracker (no personal data)
  const tmpDir = mkdtempSync(join(tmpdir(), 'snipe-migrate-'));
  try {
    mkdirSync(join(tmpDir, 'data'));
    mkdirSync(join(tmpDir, 'reports'));
    writeFileSync(join(tmpDir, 'reports', '012-acme-2026-01-04.md'), '# fixture\n');
    const tracker = join(tmpDir, 'data', 'applications.md');
    writeFileSync(tracker,
      '# Applications Tracker\n\n' +
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n' +
      '|---|------|---------|------|-------|--------|-----|--------|-------|\n' +
      '| 12 | 2026-01-04 | Acme | Engineer | 4.2/5 | Evaluated | ✅ | [12](reports/012-acme-2026-01-04.md) | ok |\n');

    // Migrate by pointing the script at the fixture tracker via env override.
    run(NODE, ['tracker/merge-tracker.mjs', '--migrate'], { env: { ...process.env, SNIPE_TRACKER: tracker } });
    const after = readFileSync(tracker, 'utf-8');
    if (after.includes('[12](../reports/012-acme-2026-01-04.md)')) {
      pass('migration rewrites fixture tracker links to ../reports/...');
    } else {
      fail('migration did not rewrite fixture tracker link');
    }
  } finally {
    rmSync(tmpDir, { recursive: true, force: true });
  }
} catch (e) {
  fail(`tracker-link normalization tests crashed: ${e.message}`);
}

// ── SHARED ROLE MATCHER + DEDUP-TRACKER SAFETY (#947) ───────────
// dedup-tracker.mjs used to ship an older fuzzy role matcher than
// merge-tracker.mjs. That weaker matcher collapsed sibling roles at the same
// company when they shared generic title words such as "Full Stack Engineer",
// and could delete an already-Applied row because data/applications.md is
// normally gitignored. The matcher is now shared, and dedup protects advanced
// application states from fuzzy-only deletion.
console.log('\nTesting shared role matcher and dedup-tracker safety...');
try {
  const { roleFuzzyMatch } = await import(pathToFileURL(join(ROOT, 'tracker/role-matcher.mjs')).href);

  if (!roleFuzzyMatch('Full Stack Engineer, Foundation', 'Full Stack Engineer, Guarded Releases')) {
    pass('role matcher keeps Full Stack Engineer sibling teams distinct (#947)');
  } else {
    fail('role matcher still collapses distinct Full Stack Engineer sibling teams');
  }

  if (!roleFuzzyMatch('Staff Software Engineer, API', 'Staff Software Engineer, SDK')) {
    pass('role matcher keeps short-acronym sibling teams distinct');
  } else {
    fail('role matcher collapsed API and SDK sibling teams');
  }

  if (roleFuzzyMatch('Staff Software Engineer, API', 'Staff Software Engineer, API Platform')) {
    pass('role matcher still uses short specialty acronyms for true overlaps');
  } else {
    fail('role matcher ignored a real short-acronym overlap');
  }

  const dedupTmp = mkdtempSync(join(tmpdir(), 'snipe-dedup-'));
  try {
    mkdirSync(join(dedupTmp, 'data'));
    const tracker = join(dedupTmp, 'data', 'applications.md');
    writeFileSync(tracker,
      '# Applications Tracker\n\n' +
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n' +
      '|---|------|---------|------|-------|--------|-----|--------|-------|\n' +
      '| 21 | 2026-01-08 | Acme | Full Stack Engineer, Foundation | 3.9/5 | Applied | ❌ | [21](../reports/021-foundation.md) | applied sibling |\n' +
      '| 22 | 2026-01-08 | Acme | Full Stack Engineer, Guarded Releases | 4.3/5 | Evaluated | ❌ | [22](../reports/022-guarded.md) | evaluated sibling |\n' +
      '| 23 | 2026-01-08 | Acme | Staff Software Engineer, API | 4.0/5 | Evaluated | ❌ | [23](../reports/023-api.md) | acronym sibling |\n' +
      '| 24 | 2026-01-08 | Acme | Staff Software Engineer, SDK | 4.2/5 | Evaluated | ❌ | [24](../reports/024-sdk.md) | acronym sibling |\n' +
      '| 25 | 2026-01-08 | Acme | Product Engineer, Growth | 3.8/5 | Evaluated | ❌ | [25](../reports/025-growth-old.md) | duplicate old |\n' +
      '| 26 | 2026-01-09 | Acme | Product Engineer, Growth | 4.0/5 | Evaluated | ❌ | [26](../reports/026-growth-new.md) | duplicate new |\n' +
      '| 27 | 2026-01-08 | Acme | Solutions Engineer, Revenue | 3.0/5 | Applied | ❌ | [27](../reports/027-revenue-applied.md) | applied exact-title row |\n' +
      '| 28 | 2026-01-09 | Acme | Solutions Engineer, Revenue | 4.6/5 | Evaluated | ❌ | [28](../reports/028-revenue-eval.md) | evaluated exact-title row |\n' +
      '| 29 | 2026-01-08 | Acme | Data Engineer, Search | 3.1/5 | Applied | ❌ | [29](../reports/029-search-old.md) | malformed duplicate-number old row |\n' +
      '| 29 | 2026-01-09 | Acme | Data Engineer, Search | 4.1/5 | Evaluated | ❌ | [30](../reports/030-search-new.md) | malformed duplicate-number new row |\n');

    const dedupResult = run(NODE, ['tracker/dedup-tracker.mjs'], { env: { ...process.env, SNIPE_TRACKER: tracker } });
    if (dedupResult === null) {
      fail('tracker/dedup-tracker.mjs crashed during shared role matcher safety test');
    } else {
      const deduped = readFileSync(tracker, 'utf-8');

      if (deduped.includes('Full Stack Engineer, Foundation') && deduped.includes('Full Stack Engineer, Guarded Releases')) {
        pass('dedup-tracker preserves distinct Full Stack Engineer sibling rows');
      } else {
        fail('dedup-tracker removed a distinct Full Stack Engineer sibling row');
      }

      if (deduped.includes('Staff Software Engineer, API') && deduped.includes('Staff Software Engineer, SDK')) {
        pass('dedup-tracker preserves short-acronym sibling rows');
      } else {
        fail('dedup-tracker removed a short-acronym sibling row');
      }

      const growthRows = deduped.split('\n').filter(l => l.includes('Product Engineer, Growth'));
      if (growthRows.length === 1 && growthRows[0].includes('4.0/5')) {
        pass('dedup-tracker still removes a real duplicate evaluated row');
      } else {
        fail(`dedup-tracker duplicate handling broken: ${growthRows.length} Growth rows`);
      }

      const revenueRows = deduped.split('\n').filter(l => l.includes('Solutions Engineer, Revenue'));
      if (revenueRows.length === 2 && revenueRows.some(l => l.includes('Applied'))) {
        pass('dedup-tracker never removes Applied+ rows by fuzzy title match');
      } else {
        fail('dedup-tracker removed an Applied+ row by fuzzy title match');
      }

      const searchRows = deduped.split('\n').filter(l => l.includes('Data Engineer, Search'));
      if (searchRows.length === 1 && searchRows[0].includes('4.1/5') && searchRows[0].includes('Applied')) {
        pass('dedup-tracker handles duplicate tracker numbers using row-local line indexes');
      } else {
        fail(`dedup-tracker duplicate-number handling broken: ${searchRows.length} Search rows`);
      }
    }
  } finally {
    rmSync(dedupTmp, { recursive: true, force: true });
  }
} catch (e) {
  fail(`shared role matcher / dedup safety tests crashed: ${e.message}`);
}

// ── MERGE-TRACKER FUZZY DEDUP (#751 / #721 family) ──────────────
// roleFuzzyMatch over-matched whenever the token overlap dominated the
// SMALLER side: two distinct roles sharing a long prefix ("Full-Stack
// Engineer 5, AI Insights & Visualizations" vs "Full Stack Engineer 5, Ads
// Reporting") or a brand token (#751: "UberEats Feed" vs "Consumer
// Fulfillment (UberEats)") collapsed onto one tracker row — silently
// dropping evaluations. The ratio now divides by the token UNION (true
// Jaccard): genuine reposts (identical token sets) still score 1.0, while
// distinct specialties fall below the 0.6 threshold.
console.log('\nTesting merge-tracker fuzzy dedup (distinct roles vs reposts)...');
try {
  const mergeTmp = mkdtempSync(join(tmpdir(), 'snipe-merge-'));
  try {
    mkdirSync(join(mergeTmp, 'data'));
    mkdirSync(join(mergeTmp, 'reports'));
    const additionsDir = join(mergeTmp, 'additions');
    mkdirSync(additionsDir);
    const tracker = join(mergeTmp, 'data', 'applications.md');
    writeFileSync(tracker,
      '# Applications Tracker\n\n' +
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n' +
      '|---|------|---------|------|-------|--------|-----|--------|-------|\n' +
      '| 1 | 2026-01-04 | StreamCo | Full Stack Engineer 5, Ads Reporting | 4.4/5 | Evaluated | ❌ | [1](../reports/001-streamco-2026-01-04.md) | existing |\n' +
      '| 2 | 2026-01-04 | Uber | Senior Software Engineer, Consumer Fulfillment (UberEats) | 4.2/5 | Evaluated | ❌ | [2](../reports/002-uber-2026-01-04.md) | existing |\n');
    for (const n of ['001-streamco-2026-01-04', '002-uber-2026-01-04', '003-streamco-2026-01-05', '004-uber-2026-01-05', '005-streamco-2026-01-06']) {
      writeFileSync(join(mergeTmp, 'reports', `${n}.md`), '# fixture\n');
    }
    // Two DISTINCT roles (long shared prefix / shared brand token) + one true repost (score bump).
    writeFileSync(join(additionsDir, '003-streamco.tsv'),
      '3\t2026-01-05\tStreamCo\tFull-Stack Engineer 5, AI Insights & Visualizations\tEvaluated\t4.6/5\t❌\t[3](reports/003-streamco-2026-01-05.md)\tdistinct role\n');
    writeFileSync(join(additionsDir, '004-uber.tsv'),
      '4\t2026-01-05\tUber\tSenior Software Engineer, UberEats Feed\tEvaluated\t4.1/5\t❌\t[4](reports/004-uber-2026-01-05.md)\tdistinct team (#751)\n');
    writeFileSync(join(additionsDir, '005-streamco.tsv'),
      '5\t2026-01-06\tStreamCo\tFull Stack Engineer 5, Ads Reporting\tEvaluated\t4.5/5\t❌\t[5](reports/005-streamco-2026-01-06.md)\trepost\n');

    const mergeResult = run(NODE, ['tracker/merge-tracker.mjs'], { env: { ...process.env, SNIPE_TRACKER: tracker, SNIPE_ADDITIONS: additionsDir } });
    if (mergeResult === null) {
      fail('tracker/merge-tracker.mjs crashed during fuzzy dedup regression test');
    } else {
      const merged = readFileSync(tracker, 'utf-8');

      // Distinct role sharing a long prefix must be ADDED, not folded into the existing row.
      if (merged.includes('AI Insights & Visualizations') && merged.includes('Ads Reporting')) {
        pass('distinct roles with shared prefix kept as separate rows');
      } else {
        fail('distinct role with shared prefix was merged away (silent data loss)');
      }

      // #751 repro: different teams under one brand token must both survive.
      if (merged.includes('UberEats Feed') && merged.includes('Consumer Fulfillment')) {
        pass('brand-token roles (#751: UberEats Feed vs Consumer Fulfillment) kept separate');
      } else {
        fail('brand-token roles were deduped (#751 regression)');
      }

      // True repost (identical role tokens) must still UPDATE in place — exactly one row, score bumped.
      const adsRows = merged.split('\n').filter(l => l.includes('Ads Reporting'));
      if (adsRows.length === 1 && adsRows[0].includes('4.5/5')) {
        pass('true repost still updates the existing row in place (4.4 → 4.5, no duplicate)');
      } else {
        fail(`repost handling broken: ${adsRows.length} 'Ads Reporting' rows, expected 1 updated to 4.5/5`);
      }
    }
  } finally {
    rmSync(mergeTmp, { recursive: true, force: true });
  }
} catch (e) {
  fail(`merge-tracker fuzzy dedup tests crashed: ${e.message}`);
}

// ── MERGE-TRACKER CONCURRENT WRITES (#781 follow-up) ─────────────────────
// Report-number reservation is atomic now (#803), but tracker merges are a
// separate read/modify/write step. If two merge-tracker processes read the same
// old applications.md snapshot and then write back independently, one process
// can erase the row added by the other. This fixture gives each process a
// different additions dir and pauses the first process after it has read the
// tracker, making the old race deterministic.
console.log('\nTesting merge-tracker concurrent writes...');
try {
  const mergeTmp = mkdtempSync(join(tmpdir(), 'snipe-merge-lock-'));
  /**
   * Spawn one isolated `merge-tracker.mjs` process against the temporary fixture.
   *
   * Each spawned process receives the same tracker path and lock path but a
   * different additions directory. Without serialization, both processes can
   * read the same old tracker and the later write can lose the other row. The
   * first worker also sends an IPC readiness message after reading the tracker
   * and before its test hold, which lets the test launch the second worker at
   * the exact old race point instead of relying on scheduler timing.
   *
   * @param {string} additionsDir - Directory containing this process's TSV row.
   * @param {number} [holdMs=0] - Optional post-read delay injected into the merge.
   * @returns {{ready: Promise<void>, result: Promise<{code:number|null,stdout:string,stderr:string}>}}
   * Worker readiness and final process result promises.
   */
  function spawnMerge(additionsDir, holdMs = 0) {
    let markReady;
    let readyMarked = false;
    const ready = new Promise(resolve => { markReady = resolve; });
    const result = new Promise(resolve => {
      const child = spawn(NODE, ['tracker/merge-tracker.mjs'], {
        cwd: ROOT,
        env: {
          ...process.env,
          SNIPE_TRACKER: join(mergeTmp, 'data', 'applications.md'),
          SNIPE_ADDITIONS: additionsDir,
          SNIPE_TRACKER_LOCK: join(mergeTmp, 'snipe-merge-tracker-fixture.lock'),
          SNIPE_MERGE_HOLD_MS: String(holdMs),
          SNIPE_MERGE_READY_IPC: '1',
        },
        stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
      });
      let stdout = '';
      let stderr = '';
      const resolveReady = () => {
        if (readyMarked) return;
        readyMarked = true;
        markReady();
      };
      child.stdout.on('data', chunk => { stdout += chunk; });
      child.stderr.on('data', chunk => { stderr += chunk; });
      child.on('message', msg => {
        if (msg?.type === 'merge-tracker-ready') resolveReady();
      });
      child.on('error', err => {
        resolveReady();
        resolve({ code: -1, stdout, stderr: String(err) });
      });
      child.on('close', code => {
        resolveReady();
        resolve({ code, stdout, stderr });
      });
    });
    return { ready, result };
  }

  /**
   * Fail fast when a worker never reaches the deterministic race checkpoint.
   *
   * A missing readiness signal would otherwise hang the test suite. Timing out
   * turns that broken test contract into a normal assertion failure with a clear
   * message.
   *
   * @param {Promise<void>} ready - Worker readiness promise.
   * @param {number} timeoutMs - Maximum milliseconds to wait.
   * @returns {Promise<void>} Resolves when ready arrives before the timeout.
   */
  function waitForReady(ready, timeoutMs) {
    return Promise.race([
      ready,
      new Promise((_, reject) => setTimeout(() => reject(new Error('merge worker did not signal readiness')), timeoutMs)),
    ]);
  }

  try {
    mkdirSync(join(mergeTmp, 'data'));
    mkdirSync(join(mergeTmp, 'reports'));
    const additionsA = join(mergeTmp, 'additions-a');
    const additionsB = join(mergeTmp, 'additions-b');
    mkdirSync(additionsA);
    mkdirSync(additionsB);

    writeFileSync(join(mergeTmp, 'data', 'applications.md'),
      '# Applications Tracker\n\n' +
      '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n' +
      '|---|------|---------|------|-------|--------|-----|--------|-------|\n');
    writeFileSync(join(mergeTmp, 'reports', '010-alpha-2026-01-07.md'), '# fixture\n');
    writeFileSync(join(mergeTmp, 'reports', '011-beta-2026-01-07.md'), '# fixture\n');
    writeFileSync(join(additionsA, '010-alpha.tsv'),
      '10\t2026-01-07\tAlpha\tPlatform Engineer\tEvaluated\t4.1/5\t❌\t[10](reports/010-alpha-2026-01-07.md)\tfirst concurrent merge\n');
    writeFileSync(join(additionsB, '011-beta.tsv'),
      '11\t2026-01-07\tBeta\tData Engineer\tEvaluated\t4.2/5\t❌\t[11](reports/011-beta-2026-01-07.md)\tsecond concurrent merge\n');

    const first = spawnMerge(additionsA, 350);
    await waitForReady(first.ready, 2_000);
    const second = spawnMerge(additionsB, 0);
    const [firstResult, secondResult] = await Promise.all([first.result, second.result]);

    if (firstResult.code === 0 && secondResult.code === 0) {
      pass('concurrent merge processes both exited successfully');
    } else {
      fail(`concurrent merge process failed: first=${firstResult.code} second=${secondResult.code} stderr=${firstResult.stderr || secondResult.stderr}`);
    }

    const merged = readFileSync(join(mergeTmp, 'data', 'applications.md'), 'utf-8');
    if (merged.includes('Alpha') && merged.includes('Beta')) {
      pass('concurrent tracker merges preserve rows from both processes');
    } else {
      fail(`concurrent tracker merge lost a row: ${merged}`);
    }
  } finally {
    rmSync(mergeTmp, { recursive: true, force: true });
  }
} catch (e) {
  fail(`merge-tracker concurrent write test crashed: ${e.message}`);
}

// ── 15. TRACKER DERIVED INDEX (#918 phase 1) ────────────────────
// applications.md is the source of truth; applications.db is a derived index
// rebuilt from it. Round-trip md → db → md must be lossless for clean input
// (a hard condition from #918 before any phase-2 work), sync must DETECT
// corruption without ever modifying the markdown, and reads must never be
// stale.

console.log('\n15. Tracker derived index (sync/query/export round-trip)');

const sqliteAvailable = run(NODE, ['--no-warnings', '-e', "import('node:sqlite').then(()=>process.exit(0),()=>process.exit(1))"]) !== null;
if (!sqliteAvailable) {
  warn('node:sqlite unavailable (Node < 22.5) — tracker index tests skipped');
} else {
  try {
    const idxTmp = mkdtempSync(join(tmpdir(), 'snipe-index-'));
    try {
      const md = join(idxTmp, 'applications.md');
      const env = { ...process.env, SNIPE_TRACKER: md };
      const trackerRun = (args) => run(NODE, ['tracker/tracker.mjs', ...args], { env, stdio: ['pipe', 'pipe', 'pipe'] });

      // 1. Round trip: clean canonical input must export byte-identical.
      const clean =
        '# Applications Tracker\n\n' +
        '| # | Date | Company | Role | Score | Status | PDF | Report | Notes |\n' +
        '|---|------|---------|------|-------|--------|-----|--------|-------|\n' +
        '| 2 | 2026-01-05 | Beta | Designer | 4.0/5 | Applied | ✅ | [2](../reports/002-beta-2026-01-05.md) | second |\n' +
        '| 1 | 2026-01-04 | Acme | Engineer | 4.2/5 | Evaluated | ❌ | [1](../reports/001-acme-2026-01-04.md) | first |\n';
      writeFileSync(md, clean);
      if (trackerRun(['sync']) === null) {
        fail('tracker sync crashed on clean fixture');
      } else {
        const exported = trackerRun(['export']);
        if (exported === clean.trim()) {
          pass('round trip md → db → md is lossless on clean input');
        } else {
          fail('round trip is NOT lossless on clean input');
        }
        if (readFileSync(md, 'utf-8') === clean) {
          pass('sync/export never modify the source markdown');
        } else {
          fail('sync/export modified applications.md (source of truth violated)');
        }
      }

      // 2. Corruption is detected and normalized in the index ONLY.
      const corrupted = clean +
        '| 1 | 2026-01-06 | Gamma | PM | — | 3.5/5 | ❌ | 鈥? | drifted |\n'; // dup id + score in status + mojibake
      writeFileSync(md, corrupted);
      if (trackerRun(['sync', '--check']) === null) {
        pass('sync --check exits non-zero when corruption is present');
      } else {
        fail('sync --check did not flag corrupted fixture');
      }
      const queried = JSON.parse(trackerRun(['query', '--company', 'Gamma', '--json']) || '[]');
      if (queried.length === 1 && queried[0].status === 'Evaluated' && queried[0].score === '3.5/5' && queried[0].id === 3) {
        pass('corrupted row is normalized in the index (status/score/id repaired)');
      } else {
        fail(`corrupted row not normalized in index: ${JSON.stringify(queried)}`);
      }
      if (readFileSync(md, 'utf-8') === corrupted) {
        pass('corruption repair never touches the markdown itself');
      } else {
        fail('sync modified the corrupted markdown (must only diagnose)');
      }

      // 3. Staleness: query after an md edit must auto-resync (no stale reads).
      writeFileSync(md, clean +
        '| 3 | 2026-01-07 | Delta | Analyst | 4.5/5 | Applied | ✅ | [3](../reports/003-delta-2026-01-07.md) | new |\n');
      const fresh = JSON.parse(trackerRun(['query', '--company', 'Delta', '--json']) || '[]');
      if (fresh.length === 1) {
        pass('query auto-resyncs when applications.md changed since last sync');
      } else {
        fail('query served a stale index after the markdown changed');
      }

      // 4. Status transitions across syncs accumulate in status_events.
      writeFileSync(md, readFileSync(md, 'utf-8').replace('| 4.0/5 | Applied |', '| 4.0/5 | Interview |'));
      const log = trackerRun(['history', '--id', '2']);
      if (log && log.includes('Applied') && log.includes('Interview')) {
        pass('history records the Applied → Interview transition across syncs');
      } else {
        fail(`history missing status transition: ${log}`);
      }
    } finally {
      rmSync(idxTmp, { recursive: true, force: true });
    }
  } catch (e) {
    fail(`tracker derived-index tests crashed: ${e.message}`);
  }
}



