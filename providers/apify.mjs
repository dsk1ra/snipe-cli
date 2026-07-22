// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Apify adapter — runs any Apify actor and maps its output to the standard
// Job schema. Designed for platforms that block plain HTTP clients (LinkedIn,
// Indeed, Glassdoor). Results are cached to avoid burning credits on every scan.
//
// API key: APIFY_API_KEY env var (loaded from .env) — preferred.
//          Falls back to entry.api_key in portals.yml.
//
// Built-in field maps cover the three most common actors:
//   curious_coder/linkedin-jobs-scraper
//   misceres/indeed-scraper
//   valig/glassdoor-jobs-scraper
//
// Override or extend via field_map in portals.yml (dot notation for nested fields):
//   field_map:
//     title: positionName
//     url: applyUrl
//     company: employer.name
//
// portals.yml usage:
//   - name: LinkedIn — Software Engineer UK
//     provider: apify
//     actor_id: curious_coder/linkedin-jobs-scraper
//     actor_input:
//       urls:
//         - "https://www.linkedin.com/jobs/search/?keywords=Software+Engineer&f_TPR=r86400"
//       count: 100
//     cache_ttl_hours: 12   # optional, default 6
//     enabled: true

import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'fs';
import { createHash } from 'crypto';

const APIFY_BASE = 'https://api.apify.com/v2';
const CACHE_DIR = process.env.HOME
  ? `${process.env.HOME}/.cache/snipe-apify`
  : '/tmp/snipe-apify';
const JD_CACHE_DIR = `${CACHE_DIR}/jds`;
const DEFAULT_TTL_HOURS = 6;
const DEFAULT_TIMEOUT_S = 270;  // Apify sync run timeout (server-side max is 300 s)
const DEFAULT_MEMORY_MB = 256;

// Built-in field maps keyed by actor id (slash form).
// Values use dot-notation to resolve nested fields.
const BUILT_IN_MAPS = {
  'curious_coder/linkedin-jobs-scraper': {
    title:    'title',
    url:      'link',
    company:  'companyName',
    location: 'location',
    postedAt: 'postedAt',        // "YYYY-MM-DD"
  },
  'misceres/indeed-scraper': {
    title:    'positionName',
    url:      'url',
    company:  'company',
    location: 'location',
    postedAt: 'postingDateParsed', // ISO string or null
  },
  'valig/glassdoor-jobs-scraper': {
    title:    'title',
    url:      'applyUrl',        // company ATS link (Greenhouse/Lever/Ashby), not the Glassdoor listing page
    company:  'employer.name',
    location: 'location.name',
    postedAt: 'ageInDays',       // number → converted below
  },
};

// Resolve a dot-notation path against an object.
function dig(obj, path) {
  return path.split('.').reduce((o, k) => o?.[k], obj);
}

// Strip HTML tags and decode common entities for plain-text JD storage.
function stripHtml(html) {
  return html
    .replace(/<br\s*\/?>/gi, '\n')
    .replace(/<\/p>/gi, '\n\n')
    .replace(/<\/li>/gi, '\n')
    .replace(/<[^>]+>/g, '')
    .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&nbsp;/g, ' ')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}

// Try common description field names in order; return stripped plain text.
const DESCRIPTION_CANDIDATES = ['description', 'descriptionHtml', 'jobDescription', 'descriptionText', 'fullDescription'];
function extractDescription(raw) {
  for (const field of DESCRIPTION_CANDIDATES) {
    const val = raw[field];
    if (val && typeof val === 'string' && val.length > 50) return stripHtml(val);
  }
  return '';
}

function jdCachePath(url) {
  const hash = createHash('sha1').update(url).digest('hex').slice(0, 16);
  return `${JD_CACHE_DIR}/${hash}.txt`;
}

function saveJd(url, text) {
  if (!text || text.length < 100) return;
  mkdirSync(JD_CACHE_DIR, { recursive: true });
  writeFileSync(jdCachePath(url), text, 'utf8');
}

function toEpochMs(value) {
  if (value == null) return undefined;
  // ageInDays (Glassdoor): number of days since posted
  if (typeof value === 'number') {
    return Number.isFinite(value) ? Date.now() - value * 86_400_000 : undefined;
  }
  // ISO date string or RFC 2822
  const ms = Date.parse(String(value));
  return Number.isNaN(ms) ? undefined : ms;
}

function cacheKey(actorId, input) {
  return createHash('sha1')
    .update(actorId + JSON.stringify(input))
    .digest('hex')
    .slice(0, 16);
}

function readCache(path, ttlMs) {
  if (!existsSync(path)) return null;
  try {
    const { savedAt, items } = JSON.parse(readFileSync(path, 'utf8'));
    if (Date.now() - savedAt < ttlMs) return items;
  } catch {}
  return null;
}

function writeCache(path, items) {
  mkdirSync(CACHE_DIR, { recursive: true });
  writeFileSync(path, JSON.stringify({ savedAt: Date.now(), items }));
}

function resolveFieldMap(actorId, entryFieldMap) {
  const base = BUILT_IN_MAPS[actorId] ?? {};
  return { ...base, ...(entryFieldMap ?? {}) };
}

function mapItem(raw, fieldMap) {
  const title    = String(dig(raw, fieldMap.title    ?? 'title')    ?? '').trim();
  const url      = String(dig(raw, fieldMap.url      ?? 'url')      ?? '').trim();
  const company  = String(dig(raw, fieldMap.company  ?? 'company')  ?? '').trim();
  const location = String(dig(raw, fieldMap.location ?? 'location') ?? '').trim();
  const rawDate  = dig(raw, fieldMap.postedAt ?? 'postedAt');

  if (!title || !url || url === 'undefined') return null;

  return {
    title,
    url,
    company,
    location,
    postedAt: toEpochMs(rawDate),
  };
}

async function apiFetch(url, opts = {}) {
  let res;
  try {
    res = await fetch(url, { signal: AbortSignal.timeout(310_000), ...opts });
  } catch (err) {
    const e = new Error(`apify: network error — ${err.message}`);
    throw e;
  }
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    const e = new Error(`apify: HTTP ${res.status} — ${body.slice(0, 200)}`);
    // @ts-ignore
    e.status = res.status;
    // @ts-ignore
    e.timedOut = res.status === 408 || body.includes('TIMED-OUT');
    // Parse the timed-out run ID so we can poll it instead of starting a new one
    // (avoids double-billing when sync times out mid-run).
    // Error message format: "Actor run did not succeed (run ID: abc123, status: TIMED-OUT)."
    const runIdMatch = body.match(/run ID:\s*([A-Za-z0-9]+)/);
    // @ts-ignore
    if (runIdMatch) e.runId = runIdMatch[1];
    throw e;
  }
  return res.json();
}

async function runSync(apiActorId, apiKey, input, memoryMb = DEFAULT_MEMORY_MB) {
  const url = `${APIFY_BASE}/acts/${apiActorId}/run-sync-get-dataset-items`
    + `?token=${apiKey}&timeout=${DEFAULT_TIMEOUT_S}&memory=${memoryMb}`;
  const data = await apiFetch(url, {
    method:  'POST',
    headers: { 'Content-Type': 'application/json' },
    body:    JSON.stringify(input),
  });
  return Array.isArray(data) ? data : (data?.items ?? []);
}

// Poll an existing or new run until SUCCEEDED, then return its dataset items.
// Pass existingRunId to resume a run that was started by runSync (avoids double-billing).
async function runAsync(apiActorId, apiKey, input, memoryMb = DEFAULT_MEMORY_MB, existingRunId = null) {
  let runId = existingRunId;

  if (!runId) {
    const run = await apiFetch(
      `${APIFY_BASE}/acts/${apiActorId}/runs?token=${apiKey}&memory=${memoryMb}`,
      { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) },
    );
    runId = run?.data?.id;
    if (!runId) throw new Error('apify: could not get run ID from async start');
  }

  // Poll until SUCCEEDED or terminal failure (max 10 min)
  const deadline = Date.now() + 600_000;
  while (Date.now() < deadline) {
    await new Promise(r => setTimeout(r, 8_000));
    const status = await apiFetch(`${APIFY_BASE}/actor-runs/${runId}?token=${apiKey}`);
    const state = status?.data?.status;
    if (state === 'SUCCEEDED') break;
    if (state === 'FAILED' || state === 'ABORTED' || state === 'TIMED-OUT') {
      throw new Error(`apify: run ${state} (id: ${runId})`);
    }
  }

  // Fetch dataset
  const dataset = await apiFetch(
    `${APIFY_BASE}/actor-runs/${runId}/dataset/items?token=${apiKey}&format=json`,
  );
  return Array.isArray(dataset) ? dataset : (dataset?.items ?? []);
}

/** @type {Provider} */
export default {
  id: 'apify',

  // detect() is not used — entries must set provider: apify explicitly.
  detect() { return null; },

  async fetch(entry, _ctx) {
    // ── Resolve API key ───────────────────────────────────────────────
    // Load .env lazily so the provider works even if scan.mjs doesn't load it.
    if (!process.env.APIFY_API_KEY) {
      try {
        const { default: dotenv } = await import('dotenv');
        dotenv.config();
      } catch {}
    }
    const apiKey = process.env.APIFY_API_KEY || entry.api_key;
    if (!apiKey) {
      throw new Error('apify: APIFY_API_KEY not set. Add it to .env or set api_key in portals.yml.');
    }

    const actorId  = entry.actor_id;
    if (!actorId) throw new Error('apify: actor_id is required in portals.yml entry.');

    const input    = entry.actor_input ?? {};
    const ttlMs    = (entry.cache_ttl_hours ?? DEFAULT_TTL_HOURS) * 3_600_000;
    const memoryMb = entry.memory_mb ?? DEFAULT_MEMORY_MB;
    const fieldMap = resolveFieldMap(actorId, entry.field_map);

    // ── Cache check ───────────────────────────────────────────────────
    const slug      = actorId.replace(/[^a-z0-9]/gi, '-');
    const cachePath = `${CACHE_DIR}/${slug}-${cacheKey(actorId, input)}.json`;
    const cached    = readCache(cachePath, ttlMs);
    if (cached) {
      console.log(`apify(${actorId}): cache hit (${cached.length} items)`);
      return cached;
    }

    // ── Run actor ─────────────────────────────────────────────────────
    // Normalize slash → tilde for the API endpoint.
    const apiActorId = actorId.replace('/', '~');

    console.log(`apify(${actorId}): starting run…`);

    // Try synchronous endpoint first (blocks until done, max 270 s).
    // On timeout (408) fall back to async: start run → poll status → fetch items.
    let rows;
    try {
      rows = await runSync(apiActorId, apiKey, input, memoryMb);
    } catch (err) {
      if (err.timedOut) {
        console.warn(`apify(${actorId}): sync timeout — polling existing run (no double billing)`);
        rows = await runAsync(apiActorId, apiKey, input, memoryMb, err.runId);
      } else {
        throw err;
      }
    }
    console.log(`apify(${actorId}): run complete — ${rows.length} raw items`);

    const jobs = [];
    for (const raw of rows) {
      const job = mapItem(raw, fieldMap);
      if (!job) continue;
      jobs.push(job);
      // Save JD alongside standard fields so batch pipeline doesn't need to fetch the URL.
      saveJd(job.url, extractDescription(raw));
    }

    writeCache(cachePath, jobs);
    return jobs;
  },
};
