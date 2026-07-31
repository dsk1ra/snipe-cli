// Provider fetch paths — the half of providers/ that talks to the network.
//
// Every provider takes its transport as a `ctx` argument ({ fetchJson,
// fetchText }), so the whole parse/normalise path is reachable by handing it a
// stub instead of standing up a server. The two providers that call global
// fetch directly (euremotejobs, apify) get globalThis.fetch swapped for the
// duration and restored in a finally.
//
// providers.test.mjs covers workable/smartrecruiters/recruitee/solidjobs; this
// file covers the rest, plus the shared _http.mjs transport.
import {
  pass, fail, warn, ROOT,
  mkdtempSync, rmSync, existsSync, readFileSync, readdirSync,
  join, tmpdir, pathToFileURL,
} from './harness.mjs';

const load = async name => await import(pathToFileURL(join(ROOT, `providers/${name}.mjs`)).href);
const provider = async name => (await load(name)).default;

/** A transport stub. `json`/`text` may be a value or a (url, opts) => value. */
function ctxOf({ json, text } = {}) {
  const calls = [];
  const resolve = (v, url, opts) => (typeof v === 'function' ? v(url, opts) : v);
  return {
    calls,
    transport: 'http',
    async fetchJson(url, opts = {}) { calls.push({ url, opts }); return resolve(json, url, opts); },
    async fetchText(url, opts = {}) { calls.push({ url, opts }); return resolve(text, url, opts); },
  };
}

/** Assert `fn` rejects with a message matching `re`. */
async function throwsWith(fn, re, label) {
  try {
    await fn();
    fail(`${label} should have thrown`);
  } catch (e) {
    if (re.test(e.message)) pass(label);
    else fail(`${label} — threw the wrong error: ${e.message}`);
  }
}

const eq = (actual, expected, label) =>
  actual === expected ? pass(label) : fail(`${label} — got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);

// ── 18. PROVIDERS — fetch paths ─────────────────────────────────────

console.log('\n18. Providers — fetch paths');

// ---- shared transport: providers/_http.mjs -------------------------------
try {
  const http = await import('node:http');
  const { fetchJson, fetchText, makeHttpCtx } = await load('_http');

  let lastPath = null;
  const server = http.createServer((req, res) => {
    lastPath = req.url;
    if (req.url === '/json') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ ok: true, ua: req.headers['user-agent'] }));
    }
    if (req.url === '/text') { res.writeHead(200); return res.end('plain body'); }
    if (req.url === '/slow') return; // never answers — exercises the abort timer
    res.writeHead(503);
    res.end('  upstream   exploded  ');
  });
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const base = `http://127.0.0.1:${server.address().port}`;

  try {
    const body = await fetchJson(`${base}/json`);
    if (body.ok === true) pass('_http fetchJson parses a JSON body');
    else fail(`_http fetchJson returned ${JSON.stringify(body)}`);

    if (/snipe/.test(body.ua || '')) pass('_http sends the snipe user-agent by default');
    else fail(`_http user-agent was ${JSON.stringify(body.ua)}`);

    eq(await fetchText(`${base}/text`), 'plain body', '_http fetchText returns the raw body');

    await fetchJson(`${base}/json`, { headers: { 'x-test': '1' } });
    eq(lastPath, '/json', '_http passes the request through to the given path');

    try {
      await fetchText(`${base}/boom`);
      fail('_http should throw on a non-2xx response');
    } catch (e) {
      // The snippet is whitespace-collapsed and capped at 300 chars.
      if (e.status === 503 && /HTTP 503: upstream exploded/.test(e.message)) {
        pass('_http throws HTTP <status> with a collapsed body snippet and .status');
      } else {
        fail(`_http error was ${e.status} / ${e.message}`);
      }
    }

    try {
      await fetchText(`${base}/slow`, { timeoutMs: 150 });
      fail('_http should abort a request that exceeds timeoutMs');
    } catch (e) {
      if (/abort/i.test(e.name + e.message)) pass('_http aborts a request past timeoutMs');
      else fail(`_http timeout threw ${e.name}: ${e.message}`);
    }

    const ctx = makeHttpCtx();
    if (ctx.transport === 'http' && typeof ctx.fetchJson === 'function' && typeof ctx.fetchText === 'function') {
      pass('makeHttpCtx returns the http transport context');
    } else {
      fail(`makeHttpCtx returned ${JSON.stringify(Object.keys(ctx))}`);
    }
  } finally {
    await new Promise(r => server.close(r));
  }
} catch (e) {
  fail(`_http transport tests crashed: ${e.message}`);
}

// ---- greenhouse ----------------------------------------------------------
try {
  const gh = await provider('greenhouse');

  eq(gh.detect({ careers_url: 'https://job-boards.greenhouse.io/acme' })?.url,
    'https://boards-api.greenhouse.io/v1/boards/acme/jobs',
    'greenhouse.detect() derives the boards-api URL from a job-boards URL');
  eq(gh.detect({ careers_url: 'https://job-boards.eu.greenhouse.io/acme' })?.url,
    'https://boards-api.greenhouse.io/v1/boards/acme/jobs',
    'greenhouse.detect() handles the .eu job-boards host');
  eq(gh.detect({ api: 'https://boards-api.greenhouse.io/v1/boards/x/jobs' })?.url,
    'https://boards-api.greenhouse.io/v1/boards/x/jobs',
    'greenhouse.detect() takes an explicit api: URL');
  eq(gh.detect({ careers_url: 'https://example.com/careers' }), null,
    'greenhouse.detect() returns null for an unrelated URL');
  eq(gh.detect({ api: 'https://evil.example.com/jobs' }), null,
    'greenhouse.detect() swallows the allowlist error and returns null');
  eq(gh.detect({ api: 'not a url' }), null,
    'greenhouse.detect() returns null for an unparseable api: URL');

  const ctx = ctxOf({
    json: {
      jobs: [
        { title: 'Staff Engineer', absolute_url: 'https://boards.greenhouse.io/acme/jobs/1', location: { name: 'Remote, EU' }, first_published: '2026-05-01T00:00:00Z' },
        { title: 'No URL', location: { name: 'X' } },
        { absolute_url: 'https://boards.greenhouse.io/acme/jobs/2', first_published: 'not-a-date' },
      ],
    },
  });
  const jobs = await gh.fetch({ name: 'Acme', careers_url: 'https://job-boards.greenhouse.io/acme' }, ctx);
  eq(jobs.length, 2, 'greenhouse.fetch() drops postings with no absolute_url');
  eq(jobs[0].location, 'Remote, EU', 'greenhouse.fetch() reads location from location.name');
  eq(jobs[0].postedAt, Date.parse('2026-05-01T00:00:00Z'), 'greenhouse.fetch() parses first_published');
  eq(jobs[1].postedAt, undefined, 'greenhouse.fetch() leaves postedAt undefined for an unparseable date');
  eq(jobs[1].location, '', 'greenhouse.fetch() falls back to an empty location');
  eq(ctx.calls[0].opts.redirect, 'error', 'greenhouse.fetch() passes redirect:"error" (SSRF guard)');

  eq((await gh.fetch({ name: 'Acme', careers_url: 'https://job-boards.greenhouse.io/acme' }, ctxOf({ json: {} }))).length,
    0, 'greenhouse.fetch() tolerates a payload with no jobs array');

  await throwsWith(() => gh.fetch({ name: 'Acme', careers_url: 'https://example.com' }, ctxOf({ json: {} })),
    /cannot derive API URL/, 'greenhouse.fetch() names the company when no API URL can be derived');
  await throwsWith(() => gh.fetch({ name: 'Acme', api: 'https://evil.example.com/jobs' }, ctxOf({ json: {} })),
    /untrusted hostname/, 'greenhouse.fetch() rejects an off-allowlist api: host');
  await throwsWith(() => gh.fetch({ name: 'Acme', api: 'http://boards-api.greenhouse.io/x' }, ctxOf({ json: {} })),
    /must use HTTPS/, 'greenhouse.fetch() rejects a plain-HTTP api: URL');
} catch (e) {
  fail(`greenhouse tests crashed: ${e.message}`);
}

// ---- lever ---------------------------------------------------------------
try {
  const lever = await provider('lever');

  eq(lever.detect({ careers_url: 'https://jobs.lever.co/acme' })?.url,
    'https://api.lever.co/v0/postings/acme', 'lever.detect() derives the postings API URL');
  eq(lever.detect({ careers_url: 'https://example.com' }), null, 'lever.detect() returns null off-domain');

  const jobs = await lever.fetch({ name: 'Acme', careers_url: 'https://jobs.lever.co/acme' }, ctxOf({
    json: [
      { text: 'Backend Engineer', hostedUrl: 'https://jobs.lever.co/acme/1', categories: { location: 'Berlin' }, createdAt: 1_700_000_000_000 },
      { hostedUrl: 'https://jobs.lever.co/acme/2', createdAt: 'not-a-number' },
    ],
  }));
  eq(jobs.length, 2, 'lever.fetch() maps every posting in the array');
  eq(jobs[0].location, 'Berlin', 'lever.fetch() reads location from categories.location');
  eq(jobs[0].postedAt, 1_700_000_000_000, 'lever.fetch() keeps a numeric createdAt');
  eq(jobs[1].postedAt, undefined, 'lever.fetch() drops a non-numeric createdAt');
  eq(jobs[1].location, '', 'lever.fetch() tolerates a posting with no categories');

  eq((await lever.fetch({ name: 'Acme', careers_url: 'https://jobs.lever.co/acme' }, ctxOf({ json: { oops: 1 } }))).length,
    0, 'lever.fetch() returns [] when the payload is not an array');

  await throwsWith(() => lever.fetch({ name: 'Acme', careers_url: 'https://example.com' }, ctxOf({ json: [] })),
    /cannot derive API URL/, 'lever.fetch() throws when no API URL can be derived');
} catch (e) {
  fail(`lever tests crashed: ${e.message}`);
}

// ---- ashby ---------------------------------------------------------------
try {
  const ashby = await provider('ashby');
  const { parseCompensation } = await load('ashby');

  eq(ashby.detect({ careers_url: 'https://jobs.ashbyhq.com/acme' })?.url,
    'https://api.ashbyhq.com/posting-api/job-board/acme?includeCompensation=true',
    'ashby.detect() derives the posting-api URL');
  eq(ashby.detect({ careers_url: 'https://example.com' }), null, 'ashby.detect() returns null off-domain');

  eq(parseCompensation({}), null, 'parseCompensation returns null with no compensation block');
  eq(parseCompensation({ compensation: { interval: '1 FORTNIGHT', minValue: 1 } }), null,
    'parseCompensation returns null for an unknown interval');
  eq(parseCompensation({ compensation: { minValue: null, maxValue: null } }), null,
    'parseCompensation returns null when neither bound is present');
  eq(parseCompensation({ compensation: { minValue: '', maxValue: '  ' } }), null,
    'parseCompensation treats blank strings as absent');
  eq(parseCompensation({ compensation: { minValue: -5, maxValue: 'abc' } }), null,
    'parseCompensation rejects negative and non-numeric values');

  const hourly = parseCompensation({ compensation: { interval: '1 HOUR', minValue: 50, maxValue: 60, currency: 'usd' } });
  eq(hourly.min, 104_000, 'parseCompensation annualises an hourly rate (×2080)');
  eq(hourly.max, 124_800, 'parseCompensation annualises the hourly upper bound');
  eq(hourly.currency, 'USD', 'parseCompensation upper-cases the currency');

  eq(parseCompensation({ compensation: { interval: '1 MONTH', minValue: 5000 } }).max, 60_000,
    'parseCompensation mirrors a missing max onto the min (×12 monthly)');
  eq(parseCompensation({ compensation: { maxValue: 90_000 } }).min, 90_000,
    'parseCompensation mirrors a missing min and defaults to a yearly interval');
  eq(parseCompensation({ compensation: { minValue: 90_000, maxValue: 70_000 } }).min, 70_000,
    'parseCompensation reorders an inverted min/max pair');
  eq(parseCompensation({ compensation: { minValue: 1, maxValue: 2 } }).currency, '',
    'parseCompensation leaves the currency empty when the payload omits it');

  const jobs = await ashby.fetch({ name: 'Acme', careers_url: 'https://jobs.ashbyhq.com/acme' }, ctxOf({
    json: { jobs: [{ title: 'SRE', jobUrl: 'https://jobs.ashbyhq.com/acme/1', location: 'Remote', publishedAt: '2026-06-01', compensation: { minValue: 100_000, currency: 'EUR' } }] },
  }));
  eq(jobs[0].salary.min, 100_000, 'ashby.fetch() attaches parsed compensation to the job');
  eq(jobs[0].postedAt, Date.parse('2026-06-01'), 'ashby.fetch() parses publishedAt');

  const noDate = await ashby.fetch({ name: 'Acme', careers_url: 'https://jobs.ashbyhq.com/acme' }, ctxOf({
    json: { jobs: [{ publishedAt: 'never' }] },
  }));
  eq(noDate[0].postedAt, undefined, 'ashby.fetch() drops an unparseable publishedAt');
  eq(noDate[0].url, '', 'ashby.fetch() defaults a missing jobUrl to an empty string');

  eq((await ashby.fetch({ name: 'Acme', careers_url: 'https://jobs.ashbyhq.com/acme' }, ctxOf({ json: null }))).length,
    0, 'ashby.fetch() tolerates a null payload');

  // One transient failure must be retried, not surfaced (backoff is ~1s).
  let attempts = 0;
  const flaky = ctxOf({
    json: () => { if (++attempts === 1) throw new Error('rate limited'); return { jobs: [{ title: 'Retried', jobUrl: 'u' }] }; },
  });
  const retried = await ashby.fetch({ name: 'Acme', careers_url: 'https://jobs.ashbyhq.com/acme' }, flaky);
  if (attempts === 2 && retried[0]?.title === 'Retried') pass('ashby.fetch() retries after a transient failure');
  else fail(`ashby.fetch() retry gave ${attempts} attempts / ${JSON.stringify(retried)}`);

  eq(flaky.calls[0].opts.timeoutMs, 30_000, 'ashby.fetch() raises the transport timeout to 30s');

  await throwsWith(() => ashby.fetch({ name: 'Acme', careers_url: 'https://example.com' }, ctxOf({ json: {} })),
    /cannot derive API URL/, 'ashby.fetch() throws when no API URL can be derived');
} catch (e) {
  fail(`ashby tests crashed: ${e.message}`);
}

// ---- workday -------------------------------------------------------------
try {
  const workday = await provider('workday');

  eq(workday.detect({ careers_url: 'https://23andme.wd5.myworkdayjobs.com/23' })?.url,
    'https://23andme.wd5.myworkdayjobs.com/wday/cxs/23andme/23/jobs',
    'workday.detect() derives the CXS endpoint');
  eq(workday.detect({ careers_url: 'https://acme.wd3.myworkdayjobs.com/en-US/External' })?.url,
    'https://acme.wd3.myworkdayjobs.com/wday/cxs/acme/External/jobs',
    'workday.detect() skips the optional locale segment');
  eq(workday.detect({ careers_url: 'https://example.com/careers' }), null,
    'workday.detect() returns null for a non-Workday URL');

  // Two pages: a full first page forces a second request, a short one stops it.
  const page = (n, count) => ({
    jobPostings: Array.from({ length: count }, (_, i) => ({
      title: `Role ${n}-${i}`, externalPath: `/job/${n}-${i}`, locationsText: 'Remote',
      postedOn: ['Posted Today', 'Posted Yesterday', 'Posted 5 Days Ago', 'Posted 30+ Days Ago', ''][i % 5],
    })),
  });
  let pageNo = 0;
  const ctx = ctxOf({ json: () => (pageNo++ === 0 ? page(0, 20) : page(1, 3)) });
  const jobs = await workday.fetch({ name: 'Acme', careers_url: 'https://acme.wd5.myworkdayjobs.com/Careers' }, ctx);

  eq(jobs.length, 23, 'workday.fetch() pages until a short page ends it');
  eq(ctx.calls.length, 2, 'workday.fetch() stops requesting after the short page');
  eq(ctx.calls[0].opts.method, 'POST', 'workday.fetch() POSTs to the CXS endpoint');
  eq(JSON.parse(ctx.calls[1].opts.body).offset, 20, 'workday.fetch() advances the offset by the page size');
  eq(jobs[0].url, 'https://acme.wd5.myworkdayjobs.com/Careers/job/0-0',
    'workday.fetch() builds the job URL from the site base, not the host root');

  const near = ms => Math.abs(Date.now() - ms) < 60_000;
  if (near(jobs[0].postedAt)) pass('workday.fetch() reads "Posted Today" as now');
  else fail(`workday "Posted Today" → ${jobs[0].postedAt}`);
  if (Math.abs(Date.now() - jobs[1].postedAt - 86_400_000) < 60_000) pass('workday.fetch() reads "Posted Yesterday" as ~24h ago');
  else fail(`workday "Posted Yesterday" → ${jobs[1].postedAt}`);
  if (Math.abs(Date.now() - jobs[2].postedAt - 5 * 86_400_000) < 60_000) pass('workday.fetch() reads "Posted N Days Ago"');
  else fail(`workday "Posted 5 Days Ago" → ${jobs[2].postedAt}`);
  eq(jobs[3].postedAt, undefined, 'workday.fetch() leaves "Posted 30+ Days Ago" undated (unbounded)');
  eq(jobs[4].postedAt, undefined, 'workday.fetch() leaves an empty postedOn undated');

  const skipped = await workday.fetch({ name: 'Acme', careers_url: 'https://acme.wd5.myworkdayjobs.com/Careers' },
    ctxOf({ json: { jobPostings: [{ title: 'Ghost' }, { title: 'Real', externalPath: '/job/1' }] } }));
  eq(skipped.length, 1, 'workday.fetch() skips a posting with no externalPath');
  eq(skipped[0].location, '', 'workday.fetch() falls back to an empty location');

  eq((await workday.fetch({ name: 'Acme', careers_url: 'https://acme.wd5.myworkdayjobs.com/Careers' },
    ctxOf({ json: {} }))).length, 0, 'workday.fetch() tolerates a payload with no jobPostings');

  await throwsWith(() => workday.fetch({ name: 'Acme', careers_url: 'https://example.com' }, ctxOf({ json: {} })),
    /cannot derive CXS endpoint for Acme/, 'workday.fetch() names the company when the endpoint cannot be derived');
} catch (e) {
  fail(`workday tests crashed: ${e.message}`);
}

// ---- remoteok ------------------------------------------------------------
try {
  const remoteok = await provider('remoteok');

  eq(remoteok.detect({ careers_url: 'https://remoteok.com/api' })?.url, 'https://remoteok.com/api',
    'remoteok.detect() accepts remoteok.com');
  eq(remoteok.detect({ careers_url: 'https://www.remoteok.io/' })?.url, 'https://remoteok.com/api',
    'remoteok.detect() strips www and accepts the .io host');
  eq(remoteok.detect({ careers_url: 'https://example.com' }), null, 'remoteok.detect() returns null off-domain');
  eq(remoteok.detect({}), null, 'remoteok.detect() returns null with no careers_url');

  const ctx = ctxOf({
    json: [
      { legal: 'this feed is provided under...' },
      { position: 'Rust Engineer', url: 'https://remoteok.com/l/1', company: 'Acme', location: 'EU', date: '2026-04-02T00:00:00Z' },
      { position: 'Go Engineer', apply_url: 'https://remoteok.com/l/2', epoch: 1_700_000_000 },
      { position: 'No link at all' },
    ],
  });
  const jobs = await remoteok.fetch({ name: 'RemoteOK' }, ctx);
  eq(jobs.length, 2, 'remoteok.fetch() drops the legal notice row and rows with no link');
  eq(jobs[0].postedAt, Date.parse('2026-04-02T00:00:00Z'), 'remoteok.fetch() parses the date field');
  eq(jobs[1].url, 'https://remoteok.com/l/2', 'remoteok.fetch() falls back to apply_url');
  eq(jobs[1].company, 'RemoteOK', 'remoteok.fetch() falls back to the portal entry name');
  eq(jobs[1].location, 'Remote', 'remoteok.fetch() defaults the location to Remote');
  eq(jobs[1].postedAt, 1_700_000_000_000, 'remoteok.fetch() converts the epoch-seconds fallback to ms');
  eq(ctx.calls[0].opts.redirect, 'error', 'remoteok.fetch() passes redirect:"error"');

  eq((await remoteok.fetch({ name: 'X' }, ctxOf({ json: { not: 'an array' } }))).length, 0,
    'remoteok.fetch() tolerates a non-array payload');
} catch (e) {
  fail(`remoteok tests crashed: ${e.message}`);
}

// ---- himalayas -----------------------------------------------------------
try {
  const himalayas = await provider('himalayas');

  eq(himalayas.detect({ careers_url: 'https://himalayas.app/jobs/api' })?.url, 'https://himalayas.app/jobs/api',
    'himalayas.detect() accepts himalayas.app');
  eq(himalayas.detect({ careers_url: 'https://example.com' }), null, 'himalayas.detect() returns null off-domain');
  eq(himalayas.detect({ careers_url: 'not a url' }), null, 'himalayas.detect() returns null for an unparseable URL');

  const jobs = await himalayas.fetch({ name: 'Himalayas' }, ctxOf({
    json: {
      jobs: [
        { title: 'Platform Engineer', applicationLink: 'https://x/1', companyName: 'Acme', locationRestrictions: ['EU', 'UK'], pubDate: 1_700_000_000 },
        { title: 'Data Engineer', guid: 'https://x/2', locationRestrictions: 'Worldwide', pubDate: 1_700_000_000_000 },
        { title: 'Designer', guid: 'https://x/3', pubDate: '2026-01-05T00:00:00Z' },
        { title: 'No link' },
      ],
    },
  }));
  eq(jobs.length, 3, 'himalayas.fetch() drops rows with neither applicationLink nor guid');
  eq(jobs[0].location, 'EU, UK', 'himalayas.fetch() joins an array of location restrictions');
  eq(jobs[0].postedAt, 1_700_000_000_000, 'himalayas.fetch() scales an epoch-seconds pubDate to ms');
  eq(jobs[1].postedAt, 1_700_000_000_000, 'himalayas.fetch() leaves an epoch-ms pubDate alone');
  eq(jobs[1].company, 'Himalayas', 'himalayas.fetch() falls back to the portal entry name');
  eq(jobs[2].location, 'Remote', 'himalayas.fetch() defaults the location to Remote');
  eq(jobs[2].postedAt, Date.parse('2026-01-05T00:00:00Z'), 'himalayas.fetch() parses an ISO pubDate');

  const bare = await himalayas.fetch({ name: 'X' }, ctxOf({ json: [{ title: 'T', guid: 'g' }] }));
  eq(bare.length, 1, 'himalayas.fetch() accepts a bare top-level array');
  eq(bare[0].postedAt, undefined, 'himalayas.fetch() leaves postedAt undefined with no pubDate');

  eq((await himalayas.fetch({ name: 'X' }, ctxOf({ json: null }))).length, 0,
    'himalayas.fetch() tolerates a null payload');
} catch (e) {
  fail(`himalayas tests crashed: ${e.message}`);
}

// ---- remotive ------------------------------------------------------------
try {
  const remotive = await provider('remotive');

  eq(remotive.detect({ careers_url: 'https://remotive.com/api/remote-jobs' })?.url,
    'https://remotive.com/api/remote-jobs', 'remotive.detect() accepts remotive.com');
  eq(remotive.detect({ careers_url: 'https://www.remotive.com/' }), null,
    'remotive.detect() matches the bare host exactly (www is not accepted)');
  eq(remotive.detect({ careers_url: '' }), null, 'remotive.detect() returns null with no careers_url');

  const plain = ctxOf({ json: { jobs: [] } });
  await remotive.fetch({ name: 'Remotive' }, plain);
  eq(plain.calls[0].url, 'https://remotive.com/api/remote-jobs',
    'remotive.fetch() hits the bare API URL when no filters are set');

  const filtered = ctxOf({
    json: {
      jobs: [
        { title: '  Senior Dev  ', url: 'https://remotive.com/j/1', company_name: 'Acme', candidate_required_location: 'Europe', publication_date: '2026-02-02T00:00:00' },
        { title: 'No URL' },
        { url: 'https://remotive.com/j/3' },
        { title: 'Bare', url: 'https://remotive.com/j/4' },
      ],
    },
  });
  const jobs = await remotive.fetch({ name: 'Remotive', category: 'software-dev', limit: 50 }, filtered);
  eq(filtered.calls[0].url, 'https://remotive.com/api/remote-jobs?category=software-dev&limit=50',
    'remotive.fetch() appends the category and limit filters');
  eq(jobs.length, 2, 'remotive.fetch() drops rows missing a title or a url');
  eq(jobs[0].title, 'Senior Dev', 'remotive.fetch() trims the title');
  eq(jobs[0].postedAt, new Date('2026-02-02T00:00:00').getTime(), 'remotive.fetch() parses publication_date');
  eq(jobs[1].company, '', 'remotive.fetch() leaves the company empty when the feed omits it');
  eq(jobs[1].location, 'Remote', 'remotive.fetch() defaults the location to Remote');
  eq(jobs[1].postedAt, undefined, 'remotive.fetch() leaves postedAt undefined with no publication_date');

  eq((await remotive.fetch({ name: 'X' }, ctxOf({ json: {} }))).length, 0,
    'remotive.fetch() tolerates a payload with no jobs array');
} catch (e) {
  fail(`remotive tests crashed: ${e.message}`);
}

// ---- jobicy (RSS) --------------------------------------------------------
try {
  const jobicy = await provider('jobicy');

  eq(jobicy.detect({ careers_url: 'https://www.jobicy.com/?feed=job_feed' })?.url,
    'https://jobicy.com/?feed=job_feed', 'jobicy.detect() strips www and returns the canonical feed');
  eq(jobicy.detect({ careers_url: 'https://example.com' }), null, 'jobicy.detect() returns null off-domain');
  eq(jobicy.detect({}), null, 'jobicy.detect() returns null with no careers_url');

  const rss = `<rss><channel>
    <item>
      <title><![CDATA[Senior Platform Engineer]]></title>
      <link>https://jobicy.com/jobs/1</link>
      <pubDate>Mon, 02 Feb 2026 10:00:00 +0000</pubDate>
      <job_listing:company><![CDATA[ Acme ]]></job_listing:company>
      <job_listing:location>Europe</job_listing:location>
    </item>
    <item>
      <title>No location</title>
      <guid>https://jobicy.com/jobs/2</guid>
    </item>
    <item><title>No link</title></item>
    <item><link>https://jobicy.com/jobs/4</link></item>
  </channel></rss>`;

  const ctx = ctxOf({ text: rss });
  const jobs = await jobicy.fetch({ name: 'Jobicy', tag: 'smm', job_type: 'full-time' }, ctx);
  eq(ctx.calls[0].url, 'https://jobicy.com/?feed=job_feed&job_categories=smm&job_types=full-time',
    'jobicy.fetch() appends the tag and job_type filters to the feed URL');
  eq(jobs.length, 2, 'jobicy RSS parse drops items missing a title or a link');
  eq(jobs[0].title, 'Senior Platform Engineer', 'jobicy RSS parse unwraps CDATA in the title');
  eq(jobs[0].company, 'Acme', 'jobicy RSS parse trims the CDATA company');
  eq(jobs[0].postedAt, new Date('Mon, 02 Feb 2026 10:00:00 +0000').getTime(), 'jobicy RSS parse converts pubDate');
  eq(jobs[1].url, 'https://jobicy.com/jobs/2', 'jobicy RSS parse falls back from link to guid');
  eq(jobs[1].location, 'Remote', 'jobicy RSS parse defaults the location to Remote');
  eq(jobs[1].postedAt, undefined, 'jobicy RSS parse leaves postedAt undefined with no pubDate');

  const bare = ctxOf({ text: '<rss></rss>' });
  eq((await jobicy.fetch({ name: 'Jobicy' }, bare)).length, 0, 'jobicy RSS parse returns [] for a feed with no items');
  eq(bare.calls[0].url, 'https://jobicy.com/?feed=job_feed', 'jobicy.fetch() uses the bare feed URL with no filters');
} catch (e) {
  fail(`jobicy tests crashed: ${e.message}`);
}

// ---- weworkremotely (RSS) ------------------------------------------------
try {
  const wwr = await provider('weworkremotely');

  eq(wwr.detect({ careers_url: 'https://weworkremotely.com/remote-jobs.rss' })?.url,
    'https://weworkremotely.com/remote-jobs.rss', 'weworkremotely.detect() echoes back the feed URL it was given');
  eq(wwr.detect({ careers_url: 'https://example.com' }), null, 'weworkremotely.detect() returns null off-domain');
  eq(wwr.detect({ careers_url: 'nope' }), null, 'weworkremotely.detect() returns null for an unparseable URL');

  const rss = `<rss><channel>
    <item>
      <title><![CDATA[Acme Corp: Senior Backend Engineer]]></title>
      <guid>https://weworkremotely.com/jobs/1</guid>
      <pubDate>Tue, 03 Mar 2026 09:00:00 +0000</pubDate>
      <region>Europe Only</region>
    </item>
    <item><title>Title with no company prefix</title><link>https://weworkremotely.com/jobs/2</link></item>
    <item><title>Broken</title></item>
  </channel></rss>`;

  const ctx = ctxOf({ text: rss });
  const jobs = await wwr.fetch({ name: 'WWR' }, ctx);
  eq(ctx.calls[0].url, 'https://weworkremotely.com/remote-jobs.rss',
    'weworkremotely.fetch() defaults to the all-jobs feed');
  eq(jobs.length, 2, 'weworkremotely RSS parse drops an item with no link');
  eq(jobs[0].company, 'Acme Corp', 'weworkremotely RSS parse splits "Company: Role" on the first colon');
  eq(jobs[0].title, 'Senior Backend Engineer', 'weworkremotely RSS parse keeps the role after the colon');
  eq(jobs[0].location, 'Europe Only', 'weworkremotely RSS parse reads <region> as the location');
  eq(jobs[1].company, '', 'weworkremotely RSS parse leaves the company empty with no colon');
  eq(jobs[1].title, 'Title with no company prefix', 'weworkremotely RSS parse keeps the whole title with no colon');
  eq(jobs[1].location, 'Remote', 'weworkremotely RSS parse defaults the location to Remote');
  eq(jobs[1].url, 'https://weworkremotely.com/jobs/2', 'weworkremotely RSS parse falls back from guid to link');
  eq(jobs[1].postedAt, undefined, 'weworkremotely RSS parse leaves postedAt undefined with no pubDate');

  const cat = ctxOf({ text: '<rss/>' });
  await wwr.fetch({ name: 'WWR', careers_url: 'https://weworkremotely.com/categories/remote-programming-jobs.rss' }, cat);
  eq(cat.calls[0].url, 'https://weworkremotely.com/categories/remote-programming-jobs.rss',
    'weworkremotely.fetch() honours a category feed URL');
} catch (e) {
  fail(`weworkremotely tests crashed: ${e.message}`);
}

// ---- hn-hiring -----------------------------------------------------------
try {
  const hn = await provider('hn-hiring');

  eq(hn.detect({ careers_url: 'https://hn.algolia.com/whoishiring' })?.url?.startsWith('https://hn.algolia.com/api/v1/search_by_date'),
    true, 'hn-hiring.detect() returns the algolia search URL');
  eq(hn.detect({ careers_url: 'https://news.ycombinator.com' }), null, 'hn-hiring.detect() returns null off-domain');
  eq(hn.detect({}), null, 'hn-hiring.detect() returns null with no careers_url');

  const search = {
    hits: [
      { title: 'Ask HN: Who wants to be hired? (July 2026)', objectID: '1' },
      { title: 'Ask HN: Who is hiring? (July 2026)', objectID: '42' },
    ],
  };
  const item = {
    children: [
      { author: 'alice', created_at_i: 1_700_000_000, text: 'Acme Corp | Senior Engineer | Berlin | REMOTE<p>Apply: <a href="https://acme.example/jobs">here</a>' },
      { author: 'bob', text: 'No link in this post at all' },
      { author: 'carol', text: 'Hi HN, we&#x27;re hiring! <a href="https://www.google.com/url?q=https%3A%2F%2Freal.example%2Fjob&amp;sa=D">apply</a>' },
      { author: 'dave', text: 'A very long line of prose that goes well past the forty-five character ceiling | Role | https://d.example/j' },
      null,
    ],
  };
  const ctx = ctxOf({ json: url => (url.includes('search_by_date') ? search : item) });
  const jobs = await hn.fetch({ name: 'HN' }, ctx);

  eq(ctx.calls[1].url, 'https://hn.algolia.com/api/v1/items/42',
    'hn-hiring.fetch() skips the "who wants to be hired" thread and opens the hiring one');
  eq(jobs.length, 3, 'hn-hiring.fetch() skips posts with no apply link');
  eq(jobs[0].company, 'Acme Corp', 'hn-hiring.fetch() reads the company from the first pipe segment');
  eq(jobs[0].title, 'Senior Engineer', 'hn-hiring.fetch() reads the role from the second pipe segment');
  eq(jobs[0].location, 'Remote', 'hn-hiring.fetch() marks a post mentioning REMOTE as Remote');
  eq(jobs[0].url, 'https://acme.example/jobs', 'hn-hiring.fetch() keeps the href target of a stripped <a>');
  eq(jobs[0].postedAt, 1_700_000_000_000, 'hn-hiring.fetch() converts created_at_i to ms');
  eq(jobs[1].url, 'https://real.example/job', 'hn-hiring.fetch() unwraps a google.com/url?q= redirect');
  eq(jobs[1].company, 'carol', 'hn-hiring.fetch() falls back to the author when the line reads as prose');
  eq(jobs[1].location, '', 'hn-hiring.fetch() leaves the location empty when no REMOTE is mentioned');
  eq(jobs[2].company, 'dave', 'hn-hiring.fetch() falls back to the author when the first segment is too long');
  eq(jobs[1].postedAt, undefined, 'hn-hiring.fetch() leaves postedAt undefined with no created_at_i');

  eq((await hn.fetch({ name: 'HN' }, ctxOf({ json: { hits: [] } }))).length, 0,
    'hn-hiring.fetch() returns [] when no hiring thread is in the search results');
  eq((await hn.fetch({ name: 'HN' }, ctxOf({ json: url => (url.includes('search_by_date') ? search : {}) }))).length, 0,
    'hn-hiring.fetch() tolerates a thread with no children');
} catch (e) {
  fail(`hn-hiring tests crashed: ${e.message}`);
}

// ---- reed ----------------------------------------------------------------
try {
  const reed = await provider('reed');

  eq(reed.detect({ provider: 'reed' })?.url, 'https://www.reed.co.uk/api/1.0/search',
    'reed.detect() keys off provider: reed, not a URL pattern');
  eq(reed.detect({ provider: 'lever' }), null, 'reed.detect() returns null for another provider');

  await throwsWith(() => reed.fetch({ name: 'Reed' }, ctxOf({ json: {} })),
    /api_key not set/, 'reed.fetch() refuses to run with no api_key');
  await throwsWith(() => reed.fetch({ name: 'Reed', api_key: 'YOUR_REED_API_KEY' }, ctxOf({ json: {} })),
    /api_key not set/, 'reed.fetch() rejects the placeholder api_key');

  // Page 1 is full (100), so a second request follows; page 2 is short and ends it.
  let n = 0;
  const ctx = ctxOf({
    json: () => (n++ === 0
      ? { totalResults: 150, results: Array.from({ length: 100 }, (_, i) => ({ jobTitle: `R${i}`, jobUrl: `https://reed/${i}`, employerName: 'Acme', locationName: 'London', date: '01/02/2026' })) }
      : { results: [{ jobId: 7 }] }),
  });
  const jobs = await reed.fetch({ name: 'Reed', api_key: 'k', keywords: 'rust', location: 'Manchester', distance: 30 }, ctx);

  eq(jobs.length, 101, 'reed.fetch() pages until a short page ends it');
  eq(ctx.calls.length, 2, 'reed.fetch() stops requesting after the short page');
  if (/keywords=rust/.test(ctx.calls[0].url) && /locationName=Manchester/.test(ctx.calls[0].url) && /distancefromLocation=30/.test(ctx.calls[0].url)) {
    pass('reed.fetch() puts keywords, location and distance in the query');
  } else {
    fail(`reed query was ${ctx.calls[0].url}`);
  }
  if (/resultsToSkip=100/.test(ctx.calls[1].url)) pass('reed.fetch() advances resultsToSkip by the page length');
  else fail(`reed page 2 URL was ${ctx.calls[1].url}`);
  if (/^Basic /.test(ctx.calls[0].opts.headers.Authorization)) pass('reed.fetch() sends HTTP Basic auth built from the api_key');
  else fail(`reed Authorization header was ${ctx.calls[0].opts.headers.Authorization}`);
  eq(jobs[100].url, 'https://www.reed.co.uk/jobs/7', 'reed.fetch() synthesises a job URL from jobId when jobUrl is absent');
  eq(jobs[100].company, 'Reed', 'reed.fetch() falls back to the portal entry name');

  eq((await reed.fetch({ name: 'Reed', api_key: 'k' }, ctxOf({ json: { results: [] } }))).length, 0,
    'reed.fetch() stops on an empty results page');
  eq((await reed.fetch({ name: 'Reed', api_key: 'k' }, ctxOf({ json: null }))).length, 0,
    'reed.fetch() stops on a null payload');
} catch (e) {
  fail(`reed tests crashed: ${e.message}`);
}

// ---- euremotejobs (global fetch + Playwright fallback) -------------------
{
  const realFetch = globalThis.fetch;
  try {
    const eu = await provider('euremotejobs');

    eq(eu.detect({ careers_url: 'https://www.euremotejobs.com/' })?.url, 'https://euremotejobs.com/?feed=job_feed',
      'euremotejobs.detect() strips www and returns the job_feed URL');
    eq(eu.detect({ careers_url: 'https://example.com' }), null, 'euremotejobs.detect() returns null off-domain');
    eq(eu.detect({}), null, 'euremotejobs.detect() returns null with no careers_url');

    const rss = `<rss><channel>
      <item>
        <title>Remote Rust Engineer</title>
        <link>https://euremotejobs.com/job/1</link>
        <pubDate>Wed, 04 Apr 2026 08:00:00 +0000</pubDate>
        <job_listing:company><![CDATA[Acme]]></job_listing:company>
        <job_listing:location>Portugal</job_listing:location>
      </item>
      <item>
        <title>Fallback Fields</title>
        <guid>https://euremotejobs.com/job/2</guid>
        <dc:creator>Beta Corp</dc:creator>
      </item>
      <item><title>No link</title></item>
    </channel></rss>`;

    globalThis.fetch = async () => ({ ok: true, status: 200, text: async () => rss });
    const jobs = await eu.fetch({ name: 'EuRemoteJobs' }, {});
    eq(jobs.length, 2, 'euremotejobs RSS parse drops items with no link');
    eq(jobs[0].company, 'Acme', 'euremotejobs RSS parse reads job_listing:company');
    eq(jobs[0].location, 'Portugal', 'euremotejobs RSS parse reads job_listing:location');
    eq(jobs[0].postedAt, new Date('Wed, 04 Apr 2026 08:00:00 +0000').getTime(), 'euremotejobs RSS parse converts pubDate');
    eq(jobs[1].company, 'Beta Corp', 'euremotejobs RSS parse falls back to dc:creator');
    eq(jobs[1].location, 'Europe / Remote', 'euremotejobs RSS parse defaults the location');
    eq(jobs[1].url, 'https://euremotejobs.com/job/2', 'euremotejobs RSS parse falls back from link to guid');
    eq(jobs[1].postedAt, undefined, 'euremotejobs RSS parse leaves postedAt undefined with no pubDate');

    globalThis.fetch = async () => { throw new Error('ECONNREFUSED'); };
    await throwsWith(() => eu.fetch({ name: 'X' }, {}), /network error — ECONNREFUSED/,
      'euremotejobs wraps a transport error with its own prefix');

    globalThis.fetch = async () => ({ ok: false, status: 500, text: async () => '' });
    await throwsWith(() => eu.fetch({ name: 'X' }, {}), /euremotejobs: HTTP 500/,
      'euremotejobs surfaces a non-403 HTTP error');

    // 403/304 is the Cloudflare block — the provider retries through Playwright.
    // fetchWithPlaywright navigates to the real euremotejobs.com, so the browser
    // is pointed at a path that holds none: chromium.launch() then fails offline
    // and the assertion is on the fallback being entered, not on its result.
    const realBrowsers = process.env.PLAYWRIGHT_BROWSERS_PATH;
    process.env.PLAYWRIGHT_BROWSERS_PATH = '/nonexistent';
    const warned = [];
    const realWarn = console.warn;
    console.warn = msg => warned.push(String(msg));
    globalThis.fetch = async () => ({ ok: false, status: 403, text: async () => '' });
    await eu.fetch({ name: 'X' }, {}).catch(() => {});
    console.warn = realWarn;
    if (realBrowsers === undefined) delete process.env.PLAYWRIGHT_BROWSERS_PATH;
    else process.env.PLAYWRIGHT_BROWSERS_PATH = realBrowsers;

    if (warned.some(m => /HTTP 403 on plain fetch — retrying with Playwright/.test(m))) {
      pass('euremotejobs takes the Playwright fallback on a 403 block');
    } else {
      fail(`euremotejobs 403 did not announce the fallback: ${JSON.stringify(warned)}`);
    }
  } catch (e) {
    fail(`euremotejobs tests crashed: ${e.message}`);
  } finally {
    globalThis.fetch = realFetch;
  }
}

// ---- apify (global fetch + on-disk cache) -------------------------------
{
  const realFetch = globalThis.fetch;
  const realHome = process.env.HOME;
  const realKey = process.env.APIFY_API_KEY;
  const realCwd = process.cwd();
  // CACHE_DIR is derived from $HOME at module load, so the sandbox has to be in
  // place before the import — otherwise the test writes into the real ~/.cache.
  // The cwd moves with it because the provider calls dotenv.config() when no key
  // is set, which would otherwise pick up the developer's real .env and make the
  // missing-key assertion pass or fail depending on whose machine it runs on.
  const home = mkdtempSync(join(tmpdir(), 'snipe-apify-home-'));
  process.env.HOME = home;
  process.chdir(home);
  delete process.env.APIFY_API_KEY;

  try {
    const apify = await provider('apify');
    eq(apify.detect(), null, 'apify.detect() always returns null — entries must opt in explicitly');

    globalThis.fetch = async () => { throw new Error('should not be called'); };
    await throwsWith(() => apify.fetch({ name: 'X' }, {}), /APIFY_API_KEY not set/,
      'apify.fetch() refuses to run with no API key');
    await throwsWith(() => apify.fetch({ name: 'X', api_key: 'k' }, {}), /actor_id is required/,
      'apify.fetch() refuses to run with no actor_id');

    const entry = {
      name: 'LinkedIn', api_key: 'k',
      actor_id: 'misceres/indeed-scraper',
      actor_input: { queries: ['rust'] },
    };
    const longDescription = 'A '.repeat(200) + 'real job description body.';
    let posted = null;
    globalThis.fetch = async (url, opts) => {
      posted = { url, opts };
      return {
        ok: true, status: 200,
        json: async () => [
          { positionName: 'Rust Engineer', url: 'https://indeed/1', company: 'Acme', location: 'Remote', postingDateParsed: '2026-03-03T00:00:00Z', description: longDescription },
          { positionName: 'No URL' },
          { url: 'https://indeed/3' },
          { positionName: 'Undefined URL', url: 'undefined' },
        ],
      };
    };

    const jobs = await apify.fetch(entry, {});
    eq(jobs.length, 1, 'apify.fetch() drops items with no title, no url, or the string "undefined"');
    eq(jobs[0].title, 'Rust Engineer', 'apify.fetch() applies the misceres/indeed-scraper built-in field map');
    eq(jobs[0].postedAt, Date.parse('2026-03-03T00:00:00Z'), 'apify.fetch() parses an ISO postedAt');
    if (posted?.url.includes('/acts/misceres~indeed-scraper/run-sync-get-dataset-items')) {
      pass('apify.fetch() normalises the slash actor id to tilde form for the API');
    } else {
      fail(`apify posted to ${posted?.url}`);
    }
    eq(posted.opts.method, 'POST', 'apify.fetch() POSTs the actor input to the sync endpoint');

    const jdDir = join(home, '.cache/snipe-apify/jds');
    if (existsSync(jdDir) && readFileSync(join(jdDir, readdirSync(jdDir)[0]), 'utf8').includes('real job description body')) {
      pass('apify.fetch() caches the stripped job description alongside the job');
    } else {
      fail('apify.fetch() did not write a JD cache file');
    }

    // Second call with identical actor+input must be served from the cache.
    let hitNetwork = false;
    globalThis.fetch = async () => { hitNetwork = true; throw new Error('cache miss'); };
    const cached = await apify.fetch(entry, {});
    if (!hitNetwork && cached.length === 1) pass('apify.fetch() serves a repeat run from the on-disk cache');
    else fail(`apify cache did not hold: hitNetwork=${hitNetwork}, ${cached.length} items`);

    // A stale cache (ttl 0) must go back to the network.
    hitNetwork = false;
    globalThis.fetch = async () => { hitNetwork = true; return { ok: true, status: 200, json: async () => ({ items: [] }) }; };
    await apify.fetch({ ...entry, cache_ttl_hours: 0 }, {});
    if (hitNetwork) pass('apify.fetch() re-runs the actor once the cache TTL has expired');
    else fail('apify.fetch() served an expired cache entry');

    // A non-timeout HTTP error surfaces rather than falling through to polling.
    globalThis.fetch = async () => ({ ok: false, status: 402, text: async () => 'insufficient credit' });
    await throwsWith(() => apify.fetch({ ...entry, actor_input: { queries: ['go'] } }, {}),
      /apify: HTTP 402 — insufficient credit/, 'apify.fetch() surfaces a non-timeout HTTP error');

    globalThis.fetch = async () => { throw new Error('socket hang up'); };
    await throwsWith(() => apify.fetch({ ...entry, actor_input: { queries: ['zig'] } }, {}),
      /apify: network error — socket hang up/, 'apify.fetch() wraps a transport error with its own prefix');

    // A custom field_map overrides the built-in one, dot notation and all.
    globalThis.fetch = async () => ({
      ok: true, status: 200,
      json: async () => ({ items: [{ positionName: 'Mapped', applyUrl: 'https://x/9', employer: { name: 'Nested Co' }, ageInDays: 3 }] }),
    });
    const mapped = await apify.fetch({
      ...entry, actor_input: { queries: ['elixir'] },
      field_map: { url: 'applyUrl', company: 'employer.name', postedAt: 'ageInDays' },
    }, {});
    eq(mapped[0].company, 'Nested Co', 'apify field_map resolves dot-notation paths');
    eq(mapped[0].url, 'https://x/9', 'apify field_map overrides the built-in url field');
    if (Math.abs(Date.now() - mapped[0].postedAt - 3 * 86_400_000) < 60_000) {
      pass('apify converts a numeric ageInDays into an absolute timestamp');
    } else {
      fail(`apify ageInDays → ${mapped[0].postedAt}`);
    }
  } catch (e) {
    fail(`apify tests crashed: ${e.stack}`);
  } finally {
    globalThis.fetch = realFetch;
    process.chdir(realCwd);
    process.env.HOME = realHome;
    if (realKey === undefined) delete process.env.APIFY_API_KEY;
    else process.env.APIFY_API_KEY = realKey;
    rmSync(home, { recursive: true, force: true });
  }
}
