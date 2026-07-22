// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// EuRemoteJobs provider — RSS feed at https://euremotejobs.com/feed/
// European remote roles, mostly tech. WordPress-based, standard RSS 2.0.
//
// The site blocks plain HTTP clients (returns 403) due to Cloudflare bot
// protection. Primary path is a direct fetch; on 403 it automatically falls
// back to Playwright (already a project dependency for PDF generation), which
// passes Chrome's TLS fingerprint and browser headers, bypassing the block.
//
// Use in portals.yml under job_boards:
//   provider: euremotejobs
//   careers_url: https://euremotejobs.com/feed/

// /?feed=job_feed is the WP Job Manager jobs feed (job_listing: namespace).
// /feed/ is the WordPress blog feed — wrong endpoint.
const FEED_URL = 'https://euremotejobs.com/?feed=job_feed';
const ALLOWED_HOST = 'euremotejobs.com';

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
    const block    = m[1];
    const title    = xmlField(block, 'title');
    const url      = xmlField(block, 'link') || xmlField(block, 'guid');
    const pubDate  = xmlField(block, 'pubDate');
    const company  = xmlField(block, 'job_listing:company') || xmlField(block, 'dc:creator');
    const location = xmlField(block, 'job_listing:location') || 'Europe / Remote';

    if (!title || !url) continue;

    jobs.push({
      title: title.trim(),
      url,
      company: company.trim(),
      location: location.trim(),
      postedAt: pubDate ? new Date(pubDate).getTime() : undefined,
    });
  }
  return jobs;
}

async function fetchWithPlaywright(url) {
  const { chromium } = await import('playwright');
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({
      userAgent: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
    });
    const page = await context.newPage();
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
    });
    // Navigate to the main domain so the page context is on euremotejobs.com.
    // Then fetch /feed/ from that same-origin context — no CORS restriction,
    // and Chrome's own network stack (TLS fingerprint, sec-fetch-* headers)
    // handles the request. Direct page.goto() to an RSS URL aborts because
    // Chrome won't navigate to XML content; page.evaluate fetch from
    // about:blank fails CORS. Same-origin evaluate is the clean middle path.
    const origin = new URL(url).origin;
    const feedPath = url.slice(origin.length) || '/feed/';

    await page.goto(origin, { waitUntil: 'domcontentloaded', timeout: 20_000 });

    // The site has a broken upstream proxy (x-proxy-cache) that returns 304
    // to ALL clients regardless of conditional headers. Cache-busting via a
    // timestamp query param forces a cache miss and gets the real feed.
    const bust = `?_=${Date.now()}`;
    const xml = await page.evaluate(async (path) => {
      const res = await fetch(path, {
        headers: { Accept: 'text/xml, application/rss+xml, */*' },
        cache: 'no-store',
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return res.text();
    }, feedPath + bust);
    return xml;
  } finally {
    await browser.close();
  }
}

/** @type {Provider} */
export default {
  id: 'euremotejobs',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      if (new URL(url).hostname.replace(/^www\./, '') === ALLOWED_HOST)
        return { url: FEED_URL };
    } catch {}
    return null;
  },

  async fetch(entry, _ctx) {
    const url = entry.careers_url || FEED_URL;

    // Fast path: plain fetch
    let res;
    try {
      res = await fetch(url, {
        headers: {
          'User-Agent': 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36',
          'Accept': 'text/xml, application/rss+xml, */*',
          'Cache-Control': 'no-cache',
        },
        redirect: 'follow',
        signal: AbortSignal.timeout(15_000),
      });
    } catch (err) {
      throw new Error(`euremotejobs: network error — ${err.message}`);
    }

    if (res.ok) {
      return parseRss(await res.text());
    }

    // Cloudflare block (403) or spurious 304 — fall back to Playwright
    if (res.status === 403 || res.status === 304) {
      console.warn(`euremotejobs: HTTP ${res.status} on plain fetch — retrying with Playwright`);
      const xml = await fetchWithPlaywright(url);
      return parseRss(xml);
    }

    throw new Error(`euremotejobs: HTTP ${res.status}`);
  },
};
