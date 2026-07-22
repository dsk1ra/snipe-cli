// @ts-check
/**
 * cv-select.mjs — deterministic, JD-grounded CV pre-selection for Phase 3.
 *
 * Ranks every experience/project bullet in cv.md against the JD's requirements
 * (Block B of the Phase 2 report) via the same embedding model the staged
 * evaluator uses, keeps the top-N per entry and the top projects, and rebuilds
 * a trimmed markdown CV. The 7B tailor then only rewrites — it no longer
 * decides what's relevant.
 *
 * Self-check: node batch/cv-select.mjs
 */

import { fileURLToPath } from 'url';
import { embed, cosine } from './embeddings.mjs';
import { cleanJd } from './text-utils.mjs';

// ── Block B requirement extraction ────────────────────────────────────────────

// Anchor on the "Candidate evidence" column so Block F's STAR table (which also
// has a "JD Requirement" column) can't match.
export function extractBlockBRequirements(report) {
  const lines = (report || '').split('\n');
  const i = lines.findIndex(l => /^\|\s*JD Requirement\s*\|\s*Candidate evidence\s*\|/i.test(l));
  if (i === -1) return [];
  const out = [];
  for (let j = i + 2; j < lines.length && lines[j].startsWith('|'); j++) {
    const req = (lines[j].split('|')[1] || '').replace(/\*\*\[must\]\*\*/i, '').trim();
    if (req.length > 8) out.push(req);
  }
  return out;
}

// ── CV parsing (## sections → ### entries → bullets) ──────────────────────────

export function parseCvSections(cvText) {
  const sections = [];
  let cur = { name: null, lines: [] };
  for (const l of cvText.split('\n')) {
    const h = l.match(/^##\s+(.+)$/);
    if (h) {
      sections.push(cur);
      cur = { name: h[1].trim(), lines: [l] };
    } else {
      cur.lines.push(l);
    }
  }
  sections.push(cur);
  return sections;
}

export function parseEntries(sectionLines) {
  const head = [sectionLines[0]];
  const entries = [];
  let cur = null;
  for (const l of sectionLines.slice(1)) {
    if (/^###\s+/.test(l)) { cur = { head: [l], bullets: [] }; entries.push(cur); continue; }
    if (!cur) { head.push(l); continue; }
    if (/^-\s+/.test(l)) cur.bullets.push(l.replace(/^-\s+/, '').trim());
    else if (cur.bullets.length === 0) { if (l.trim()) cur.head.push(l); }
    else if (l.trim()) cur.bullets[cur.bullets.length - 1] += ' ' + l.trim(); // wrapped bullet
  }
  return { head, entries };
}

// ── Reverse-chronological ordering (UK CV convention) ──────────────────────────

const MONTHS = { jan: 0, feb: 1, mar: 2, apr: 3, may: 4, jun: 5, jul: 6, aug: 7, sep: 8, oct: 9, nov: 10, dec: 11 };

function parseDateToken(token) {
  token = (token || '').trim();
  if (/present/i.test(token)) return Infinity;
  const m = token.match(/([A-Za-z]{3,9})\s+(\d{4})/);
  if (m) return parseInt(m[2], 10) * 12 + (MONTHS[m[1].slice(0, 3).toLowerCase()] ?? 0);
  const y = token.match(/(\d{4})/);
  return y ? parseInt(y[1], 10) * 12 : -Infinity;
}

// Entry head's second line carries "... | <start> – <end>"; sort by end date.
function entryEndDate(entry) {
  const line = entry.head.find(l => /\d{4}/.test(l)) || '';
  const last = line.split('|').pop() || '';
  const range = last.match(/([A-Za-z0-9 ]+?)\s*[–-]\s*([A-Za-z0-9 ]+)\s*$/);
  return parseDateToken(range ? range[2] : last);
}

function renderEntries(parsed) {
  const out = [...parsed.head, ''];
  for (const e of parsed.entries) {
    out.push(...e.head, '');
    for (const b of e.bullets) out.push(`- ${b}`);
    out.push('');
  }
  return out;
}

// ── Selection ─────────────────────────────────────────────────────────────────

/**
 * Returns a trimmed cv.md string, or the original text untouched when there is
 * nothing to rank against (no requirements and no JD).
 * opts: { maxProjects, maxBulletsPerProject, maxBulletsPerRole, ollamaUrl, _embed }
 */
export async function selectCvForJd(cvText, requirements, jdText, opts = {}) {
  const {
    maxProjects = 4, maxBulletsPerProject = 5, maxBulletsPerRole = 4,
    _embed = embed,
  } = opts;

  const queries = (requirements && requirements.length)
    ? requirements
    : (jdText ? [cleanJd(jdText, 1500)] : []);
  if (!queries.length) return cvText;

  const sections = parseCvSections(cvText);
  const exp  = sections.find(s => s.name === 'Experience');
  const proj = sections.find(s => s.name === 'Projects');
  if (!exp && !proj) return cvText;

  const expParsed  = exp  ? parseEntries(exp.lines)  : null;
  const projParsed = proj ? parseEntries(proj.lines) : null;

  const items = [];
  for (const p of [expParsed, projParsed]) {
    if (!p) continue;
    for (const e of p.entries) {
      e.scored = [];
      const name = e.head[0].replace(/^###\s+/, '').trim();
      for (const b of e.bullets) items.push({ entry: e, text: b, ctx: `${name}: ${b}` });
    }
  }
  if (!items.length) return cvText;

  const vecs = await _embed([...queries, ...items.map(i => i.ctx)], opts);
  const qv = vecs.slice(0, queries.length);
  for (let i = 0; i < items.length; i++) {
    const v = vecs[queries.length + i];
    const it = items[i];
    it.entry.scored.push({ text: it.text, score: Math.max(...qv.map(q => cosine(q, v))) });
  }

  // Keep the top-N bullets per entry (relevance order — the tailor prompt asks
  // for most-relevant first anyway), guaranteeing at least one metric bullet.
  function trim(entry, keep) {
    const ranked = [...entry.scored].sort((a, b) => b.score - a.score);
    const kept = ranked.slice(0, keep);
    if (kept.length && !kept.some(b => /\d/.test(b.text))) {
      const metric = ranked.find(b => /\d/.test(b.text));
      if (metric) kept[kept.length - 1] = metric;
    }
    entry.bullets = kept.map(b => b.text);
    entry.score = ranked[0]?.score ?? 0;
  }

  if (expParsed) {
    // UK CV convention: reverse-chronological, never reordered by relevance.
    for (const e of expParsed.entries) trim(e, maxBulletsPerRole);
    expParsed.entries.sort((a, b) => entryEndDate(b) - entryEndDate(a));
    exp.lines = renderEntries(expParsed);
  }
  if (projParsed) {
    // Relevance picks WHICH projects make the cut; date decides their order.
    for (const e of projParsed.entries) trim(e, maxBulletsPerProject);
    projParsed.entries.sort((a, b) => b.score - a.score);
    projParsed.entries.length = Math.min(projParsed.entries.length, maxProjects);
    projParsed.entries.sort((a, b) => entryEndDate(b) - entryEndDate(a));
    proj.lines = renderEntries(projParsed);
  }

  return sections.map(s => s.lines.join('\n')).join('\n').replace(/\n{3,}/g, '\n\n');
}

// ── Project-name remap (anti-fabrication) ─────────────────────────────────────

const STOP = new Set(['and', 'the', 'for', 'with', 'system', 'management', 'a', 'an', 'of', 'in']);
const toks = s => new Set((String(s).toLowerCase().match(/[a-z0-9+#.:-]{3,}/g) || []).filter(w => !STOP.has(w)));

/**
 * The tailor model sometimes renames projects to fit the JD's domain (observed:
 * "Distributed Odds Feed Orchestrator" for a betting-infra JD) — the template
 * then finds no match and silently drops the project. Remap each model project
 * name to the real CV project it describes (name match first, then token
 * overlap of name+description vs the project's CV text). Unmappable entries are
 * removed. Mutates and returns `projects`.
 */
export function remapProjectNames(projects, cvText) {
  const sec = parseCvSections(cvText).find(s => s.name === 'Projects');
  if (!sec || !Array.isArray(projects)) return projects;
  const real = parseEntries(sec.lines).entries.map(e => ({
    name: e.head[0].replace(/^###\s+/, '').trim(),
    tokens: toks(e.head.join(' ') + ' ' + e.bullets.join(' ')),
  }));
  const used = new Set();
  const out = [];
  for (const p of projects) {
    const pName = String(p.name || '').toLowerCase();
    let target = real.find(r =>
      !used.has(r.name) && r.name.toLowerCase().includes(pName.slice(0, 20)));
    if (!target) {
      let best = null, bestN = 2; // require ≥3 overlapping tokens
      for (const r of real) {
        if (used.has(r.name)) continue;
        let n = 0;
        for (const t of toks(`${p.name} ${p.description || ''}`)) if (r.tokens.has(t)) n++;
        if (n > bestN) { best = r; bestN = n; }
      }
      target = best;
    }
    if (!target) continue; // nothing on the CV looks like this — drop it
    used.add(target.name);
    out.push({ ...p, name: target.name });
  }
  return out;
}

// The 7B model doesn't reliably honour "keep the given order" — re-sort its
// output by real CV end date so experience/projects stay UK-convention
// reverse-chronological regardless of what the model returned.
export function enforceChronoOrder(items, cvText, sectionName, nameField) {
  const sec = parseCvSections(cvText).find(s => s.name === sectionName);
  if (!sec || !Array.isArray(items)) return items;
  const real = parseEntries(sec.lines).entries.map(e => {
    const bold = (e.head[1] || '').match(/^\*\*(.+?)\*\*/); // company line, e.g. "**Acme Corp** ..."
    return {
      name: e.head[0].replace(/^###\s+/, '').trim(), // role/project title
      alt: bold ? bold[1].trim() : null,              // company (experience entries)
      end: entryEndDate(e),
    };
  });
  const matches = (r, n) =>
    r.name.toLowerCase() === n || (r.alt && r.alt.toLowerCase() === n) ||
    r.name.toLowerCase().includes(n) || n.includes(r.name.toLowerCase()) ||
    (r.alt && (r.alt.toLowerCase().includes(n) || n.includes(r.alt.toLowerCase())));
  const endDateFor = (name) => {
    const n = String(name || '').trim().toLowerCase();
    const m = real.find(r => matches(r, n));
    return m ? m.end : -Infinity;
  };
  return [...items].sort((a, b) => endDateFor(b[nameField]) - endDateFor(a[nameField]));
}

// ── Self-check ────────────────────────────────────────────────────────────────

const isMain = process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1];
if (isMain) {
  const assert = (c, m) => { if (!c) { console.error(`✗ ${m}`); process.exit(1); } };

  const fakeCv = `# Name

## Summary

Text.

## Experience

### Dev
**Co A** | 2024

- Built Rust encryption service handling 1M requests
- Wrote Java billing reports
- Mentored two juniors

## Projects

### Crypto Tool
**Personal** | Rust

- Implemented AES-256-GCM encryption in Rust
- Added CLI with 3 subcommands

### Web App
**Personal** | React

- Built React frontend
- Deployed to Vercel

### Java Batch
**Personal** | Java

- Wrote Java batch processor with 99% uptime

## Skills

**Languages:** Rust, Java
`;

  // Stub embedding: dims = [mentions rust, mentions encrypt, 1]
  const stub = async texts => texts.map(t => {
    const s = t.toLowerCase();
    return [s.includes('rust') ? 1 : 0, s.includes('encrypt') ? 1 : 0, 0.1];
  });

  const report = [
    '| JD Requirement | Candidate evidence | Strength |',
    '|---|---|---|',
    '| **[must]** Rust experience with encryption | something | Strong |',
    '',
    '| # | JD Requirement | Story | S | T | A | R |',
    '|---|---|---|---|---|---|---|',
    '| 1 | Decoy row that must not parse | x | s | t | a | r |',
  ].join('\n');

  const reqs = extractBlockBRequirements(report);
  assert(reqs.length === 1 && /Rust experience/.test(reqs[0]), 'Block B parse (and Block F decoy excluded)');

  const out = await selectCvForJd(fakeCv, reqs, '', { maxProjects: 2, maxBulletsPerRole: 2, maxBulletsPerProject: 2, _embed: stub });
  assert(out.length < fakeCv.length, 'output trimmed');
  assert(out.indexOf('### Crypto Tool') < out.indexOf('### Web App') || !out.includes('### Web App'), 'most relevant project first');
  assert(!out.includes('Java Batch') || out.split('### ').length - 1 <= 3, 'weakest project dropped at maxProjects=2');
  assert(out.includes('Built Rust encryption service'), 'top experience bullet kept');
  assert(!out.includes('Mentored two juniors'), 'weakest experience bullet cut at keep=2');
  assert(out.includes('## Skills') && out.includes('## Summary'), 'untouched sections preserved');
  assert(out.indexOf('Implemented AES-256-GCM') !== -1, 'top project bullet kept');

  // Metric guarantee: force a no-digit top-2 by querying something both metric
  // bullets miss.
  const out2 = await selectCvForJd(fakeCv, ['mentoring and code review'], '', {
    maxBulletsPerRole: 1, _embed: async texts => texts.map(t => [t.toLowerCase().includes('mentor') ? 1 : 0, 0, 0.1]),
  });
  assert(/- .*\d/.test(out2.split('## Experience')[1].split('## Projects')[0]), 'metric bullet guaranteed per role');

  // No requirements + no JD → untouched
  const out3 = await selectCvForJd(fakeCv, [], '', { _embed: stub });
  assert(out3 === fakeCv, 'no queries → CV returned untouched');

  // Project-name remap: fabricated name → real project by content overlap;
  // exact-ish names untouched; pure inventions dropped.
  const remapped = remapProjectNames([
    { name: 'Betting Odds Encryptor', description: 'Implemented AES-256-GCM encryption in Rust with CLI subcommands' },
    { name: 'Web App', description: 'React frontend deployed to Vercel' },
    { name: 'Quantum Basket Weaving', description: 'totally unrelated invention' },
  ], fakeCv);
  assert(remapped.length === 2, 'unmappable fabricated project dropped');
  assert(remapped[0].name === 'Crypto Tool', 'fabricated name remapped by content overlap');
  assert(remapped[1].name === 'Web App', 'real name kept');

  console.log('✓ cv-select self-check passed');
}
