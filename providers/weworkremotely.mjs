// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// WeWorkRemotely provider — RSS feeds at weworkremotely.com
// Title format in the feed is "Company: Role Title"; the provider splits on
// the first colon to extract company and role separately.
//
// Use in portals.yml under job_boards:
//   provider: weworkremotely
//   careers_url: https://weworkremotely.com/remote-jobs.rss            # all jobs
//   # or a category feed:
//   # careers_url: https://weworkremotely.com/categories/remote-programming-jobs.rss
//
// Category feed slugs:
//   remote-programming-jobs, remote-devops-sysadmin-jobs,
//   remote-data-science-jobs, remote-design-jobs, remote-product-jobs,
//   remote-customer-support-jobs, remote-sales-jobs,
//   remote-management-finance-jobs, remote-writing-jobs

const ALLOWED_HOST = 'weworkremotely.com';

// Minimal RSS field extractor — handles plain text and CDATA sections.
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
    const rawTitle = xmlField(block, 'title');
    const url      = xmlField(block, 'guid') || xmlField(block, 'link');
    const pubDate  = xmlField(block, 'pubDate');
    const region   = xmlField(block, 'region');

    if (!rawTitle || !url) continue;

    // "Company: Role Title" → split on first colon only
    const colonIdx = rawTitle.indexOf(': ');
    const company = colonIdx > 0 ? rawTitle.slice(0, colonIdx).trim() : '';
    const title   = colonIdx > 0 ? rawTitle.slice(colonIdx + 2).trim() : rawTitle;

    jobs.push({
      title,
      url,
      company,
      location: region || 'Remote',
      postedAt: pubDate ? new Date(pubDate).getTime() : undefined,
    });
  }
  return jobs;
}

/** @type {Provider} */
export default {
  id: 'weworkremotely',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      if (new URL(url).hostname.replace(/^www\./, '') === ALLOWED_HOST)
        return { url };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    const url = entry.careers_url || `https://${ALLOWED_HOST}/remote-jobs.rss`;
    const xml = await ctx.fetchText(url, {
      headers: { Accept: 'application/rss+xml, text/xml, */*' },
      redirect: 'follow',
    });
    return parseRss(xml);
  },
};
