// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Remotive provider — public JSON API at https://remotive.com/api/remote-jobs
// Returns remote roles across all companies; optional category filter.
//
// Use in portals.yml under job_boards:
//   provider: remotive
//   careers_url: https://remotive.com/api/remote-jobs
//   category: software-dev   # optional — omit to get all categories
//   limit: 100               # optional — default 100, max ~300
//
// Available categories: software-dev, data, devops-sysadmin, product,
//   design, marketing, customer-support, sales, business, writing,
//   finance-legal, hr, qa, teaching, all-others

const ALLOWED_HOST = 'remotive.com';
const BASE_URL = 'https://remotive.com/api/remote-jobs';

function buildUrl(entry) {
  const params = new URLSearchParams();
  if (entry.category) params.set('category', entry.category);
  if (entry.limit)    params.set('limit', String(entry.limit));
  const qs = params.toString();
  return qs ? `${BASE_URL}?${qs}` : BASE_URL;
}

/** @type {Provider} */
export default {
  id: 'remotive',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      if (new URL(url).hostname === ALLOWED_HOST) return { url: BASE_URL };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    const url = buildUrl(entry);
    const json = await ctx.fetchJson(url, { redirect: 'error' });
    const rows = Array.isArray(json?.jobs) ? json.jobs : [];
    return rows
      .filter(j => j && typeof j === 'object' && j.title && j.url)
      .map(j => ({
        title: String(j.title).trim(),
        url: j.url,
        company: String(j.company_name || '').trim(),
        location: String(j.candidate_required_location || 'Remote').trim(),
        postedAt: j.publication_date ? new Date(j.publication_date).getTime() : undefined,
      }));
  },
};
