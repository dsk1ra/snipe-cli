// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// RemoteOK provider — public JSON feed at https://remoteok.com/api
// Returns ~100 most-recent remote roles across all companies. The first array
// element is a legal/notice object with no `position`, so it is filtered out.
//
// Use in portals.yml under job_boards with `provider: remoteok` and
// `careers_url: https://remoteok.com/api`.

const ALLOWED_HOSTS = new Set(['remoteok.com', 'remoteok.io']);
const API_URL = 'https://remoteok.com/api';

function toEpochMs(value) {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

/** @type {Provider} */
export default {
  id: 'remoteok',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      const parsed = new URL(url);
      if (ALLOWED_HOSTS.has(parsed.hostname.replace(/^www\./, ''))) return { url: API_URL };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    // redirect:'error' prevents SSRF via server-side redirects
    const json = await ctx.fetchJson(API_URL, { redirect: 'error' });
    const rows = Array.isArray(json) ? json : [];
    return rows
      .filter(j => j && typeof j === 'object' && j.position && (j.url || j.apply_url))
      .map(j => ({
        title: j.position || '',
        url: j.url || j.apply_url,
        company: j.company || entry.name,
        location: j.location || 'Remote',
        postedAt: toEpochMs(j.date) ?? (j.epoch ? j.epoch * 1000 : undefined),
      }));
  },
};
