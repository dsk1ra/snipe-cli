// @ts-check
/** @typedef {import('./_types.js').Provider} Provider */

// Hacker News "Ask HN: Who is hiring?" provider (best-effort).
//
// Monthly threads are posted by the `whoishiring` account. Each top-level
// comment is a freeform job post, loosely following the convention:
//   "Company | Role | Location | REMOTE | https://apply..."
// We strip the HTML, parse that first line, and extract the first URL. Posts
// without a URL are skipped (no URL = nothing to evaluate or dedup on), so
// coverage is partial by design — this is a discovery aid, not a clean API.
//
// Use in portals.yml under job_boards with `provider: hn-hiring` and
// `careers_url: https://hn.algolia.com/whoishiring` (sentinel — the host is
// what the provider detects on).

const ALLOWED_HOSTS = new Set(['hn.algolia.com']);
const SEARCH_URL = 'https://hn.algolia.com/api/v1/search_by_date?tags=story,author_whoishiring&query=hiring&hitsPerPage=10';
const ITEM_URL = id => `https://hn.algolia.com/api/v1/items/${encodeURIComponent(id)}`;
const MAX_POSTS = 200;

function decodeEntities(s) {
  return String(s)
    // numeric/hex entities first — HN encodes "/" as &#x2F;, "'" as &#x27;, etc.
    .replace(/&#x([0-9a-f]+);/gi, (_, h) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(parseInt(d, 10)))
    .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"').replace(/&nbsp;/g, ' ');
}

function stripHtml(html) {
  return decodeEntities(
    String(html || '')
      .replace(/<a\b[^>]*\bhref="([^"]+)"[^>]*>.*?<\/a>/gi, ' $1 ') // keep link targets
      .replace(/<\/p>/gi, '\n')
      .replace(/<[^>]+>/g, ' ')
  )
    .replace(/[ \t]{2,}/g, ' ')
    .trim();
}

function firstUrl(text) {
  const m = text.match(/https?:\/\/[^\s)<>"']+/i);
  if (!m) return null;
  let url = m[0].replace(/[.,);]+$/, '');
  // Unwrap google.com/url?q=<real>&... redirect wrappers
  const wrapped = url.match(/^https?:\/\/(?:www\.)?google\.com\/url\?q=([^&]+)/i);
  if (wrapped) { try { url = decodeURIComponent(wrapped[1]); } catch {} }
  return url;
}

// A first-line segment is a plausible company name only if it's short and not prose.
function cleanCompany(seg, fallback) {
  const s = (seg || '').trim();
  if (!s || s.length > 45 || /\b(hi|hey|hello)\s+hn\b/i.test(s) || /\bis hiring\b/i.test(s) || s.includes('http'))
    return fallback;
  return s;
}

/** @type {Provider} */
export default {
  id: 'hn-hiring',

  detect(entry) {
    const url = entry.careers_url || '';
    try {
      if (ALLOWED_HOSTS.has(new URL(url).hostname)) return { url: SEARCH_URL };
    } catch {}
    return null;
  },

  async fetch(entry, ctx) {
    const search = await ctx.fetchJson(SEARCH_URL, { redirect: 'error' });
    const hits = Array.isArray(search?.hits) ? search.hits : [];
    const thread = hits.find(h =>
      /who\s+is\s+hiring\??/i.test(h.title || '') &&
      !/wants to be hired|freelancer/i.test(h.title || ''));
    if (!thread) return [];

    const item = await ctx.fetchJson(ITEM_URL(thread.objectID), { redirect: 'error' });
    const children = Array.isArray(item?.children) ? item.children : [];

    const out = [];
    for (const c of children) {
      if (!c || !c.text || out.length >= MAX_POSTS) continue;
      const text = stripHtml(c.text);
      const url = firstUrl(text);
      if (!url) continue; // skip posts with no apply link

      const firstLine = text.split('\n').map(s => s.trim()).filter(Boolean)[0] || '';
      const parts = firstLine.split('|').map(s => s.trim()).filter(Boolean);
      const company = cleanCompany(parts[0], c.author || 'Unknown');
      const title = (parts[1] || parts[0] || firstLine).slice(0, 120);
      const remote = /\bremote\b/i.test(text);

      out.push({
        title,
        url,
        company,
        location: remote ? 'Remote' : '',
        postedAt: c.created_at_i ? c.created_at_i * 1000 : undefined,
      });
    }
    return out;
  },
};
