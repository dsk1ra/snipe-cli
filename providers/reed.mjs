// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Reed provider — hits the Reed.co.uk public jobs search API.
// Auto-detects from `provider: reed` in portals.yml (no URL pattern to match).
//
// Requires a free API key from https://www.reed.co.uk/developers
// Set `api_key` in the portals.yml job_boards entry.
//
// Configurable per-entry fields:
//   api_key   — Reed API key (required)
//   keywords  — search keywords (default: reads from title_filter or 'software engineer')
//   location  — location name (default: 'London')
//   distance  — distance in miles from location (default: 15)

const REED_API_BASE = 'https://www.reed.co.uk/api/1.0/search';
const PAGE_SIZE = 100;

function buildUrl(entry, skip = 0) {
  const params = new URLSearchParams({
    keywords: entry.keywords || 'software engineer',
    locationName: entry.location || 'London',
    distancefromLocation: String(entry.distance ?? 15),
    resultsToTake: String(PAGE_SIZE),
    resultsToSkip: String(skip),
  });
  return `${REED_API_BASE}?${params}`;
}

/** @type {Provider} */
export default {
  id: 'reed',

  detect(entry) {
    if (entry.provider === 'reed') return { url: REED_API_BASE };
    return null;
  },

  async fetch(entry, ctx) {
    const apiKey = entry.api_key;
    if (!apiKey || apiKey === 'YOUR_REED_API_KEY') {
      throw new Error('reed: api_key not set. Get a free key at https://www.reed.co.uk/developers');
    }

    const authHeader = 'Basic ' + Buffer.from(`${apiKey}:`).toString('base64');
    const results = [];
    let skip = 0;
    let totalResults = Infinity;

    while (skip < totalResults) {
      const url = buildUrl(entry, skip);
      const json = await ctx.fetchJson(url, { headers: { Authorization: authHeader } });

      if (!json || !Array.isArray(json.results) || json.results.length === 0) break;
      if (totalResults === Infinity) totalResults = json.totalResults ?? json.results.length;

      for (const j of json.results) {
        results.push({
          title: j.jobTitle || '',
          url: j.jobUrl || `https://www.reed.co.uk/jobs/${j.jobId}`,
          company: j.employerName || entry.name,
          location: j.locationName || '',
          postedAt: j.date ? new Date(j.date).getTime() : undefined,
        });
      }

      skip += json.results.length;
      if (json.results.length < PAGE_SIZE) break;
    }

    return results;
  },
};
