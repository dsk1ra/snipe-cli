// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Jobicy provider — RSS feed at https://jobicy.com/?feed=job_feed
// Uses the job_listing: custom namespace for company, location, and job type.
// Note: Jobicy publishes with a 6-hour delay by design; once-per-day polling is fine.
//
// Use in portals.yml under job_boards:
//   provider: jobicy
//   careers_url: https://jobicy.com/?feed=job_feed
//   # Optional tag filter (appended as &job_categories=...):
//   # tag: smm
//   # Optional type filter: full-time | part-time | freelance | internship
//   # job_type: full-time

const ALLOWED_HOST = 'jobicy.com';
const BASE_URL = 'https://jobicy.com/?feed=job_feed';

function buildUrl(entry) {
  const base = entry.careers_url || BASE_URL;
  const params = new URLSearchParams();
  if (entry.tag)      params.set('job_categories', entry.tag);
  if (entry.job_type) params.set('job_types', entry.job_type);
  const qs = params.toString();
  return qs ? `${base}&${qs}` : base;
}

// Handles plain text, CDATA on same line, and CDATA on separate lines.
function xmlField(block, tag) {
  const esc = tag.replace(':', '\\:');
  const m = block.match(new RegExp(`<${esc}(?:\\s[^>]*)?>([\\s\\S]*?)<\\/${esc}>`, 'i'));
  if (!m) return '';
  const cdata = m[1].match(/<!\[CDATA\[([\s\S]*?)\]\]>/);
  return (cdata ? cdata[1] : m[1]).trim();
}

function parseRss(xml) {
  const jobs = [];
  const itemRe = /<item>([\s\S]*?)<\/item>/g;
  let m;
  while ((m = itemRe.exec(xml)) !== null) {
    const block = m[1];
    const title   = xmlField(block, 'title');
    const url     = xmlField(block, 'link') || xmlField(block, 'guid');
    const pubDate = xmlField(block, 'pubDate');
    const company = xmlField(block, 'job_listing:company');
    const location = xmlField(block, 'job_listing:location');

    if (!title || !url) continue;

    jobs.push({
      title: title.trim(),
      url,
      company: company.trim(),
      location: location.trim() || 'Remote',
      postedAt: pubDate ? new Date(pubDate).getTime() : undefined,
    });
  }
  return jobs;
}

/** @type {Provider} */
export default {
  id: 'jobicy',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      if (new URL(url).hostname.replace(/^www\./, '') === ALLOWED_HOST)
        return { url: BASE_URL };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    const url = buildUrl(entry);
    const xml = await ctx.fetchText(url, {
      headers: { Accept: 'application/rss+xml, text/xml, */*' },
      redirect: 'follow',
    });
    return parseRss(xml);
  },
};
