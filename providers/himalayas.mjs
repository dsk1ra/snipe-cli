// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Himalayas provider — public JSON feed at https://himalayas.app/jobs/api
// Returns the latest ~20 remote roles across all companies under { jobs: [...] }.
//
// Use in portals.yml under job_boards with `provider: himalayas` and
// `careers_url: https://himalayas.app/jobs/api`.

const ALLOWED_HOSTS = new Set(['himalayas.app']);
const API_URL = 'https://himalayas.app/jobs/api';

function toEpochMs(value) {
  if (!value) return undefined;
  // Himalayas pubDate is a unix epoch (seconds) or an ISO string depending on field
  if (typeof value === 'number') return value > 1e12 ? value : value * 1000;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

/** @type {Provider} */
export default {
  id: 'himalayas',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      const parsed = new URL(url);
      if (ALLOWED_HOSTS.has(parsed.hostname)) return { url: API_URL };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    // redirect:'error' prevents SSRF via server-side redirects
    const json = await ctx.fetchJson(API_URL, { redirect: 'error' });
    const rows = Array.isArray(json?.jobs) ? json.jobs : (Array.isArray(json) ? json : []);
    return rows
      .filter(j => j && typeof j === 'object' && j.title && (j.applicationLink || j.guid))
      .map(j => ({
        title: j.title || '',
        url: j.applicationLink || j.guid,
        company: j.companyName || entry.name,
        location: Array.isArray(j.locationRestrictions) ? j.locationRestrictions.join(', ')
          : (j.locationRestrictions || 'Remote'),
        postedAt: toEpochMs(j.pubDate),
      }));
  },
};
