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
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { embed, cosine, modelFingerprint } from './embeddings.mjs';
import { cleanJd } from './text-utils.mjs';
import { logCall } from './timing.mjs';

const __dir = dirname(fileURLToPath(import.meta.url));

// ── LLM rerank ────────────────────────────────────────────────────────────────

const JUDGE_SYSTEM = `You are a recruiter deciding which items from a candidate's master CV to keep on a CV tailored to one specific job posting.

Grade EVERY item from 0 to 3:
  3 - directly evidences a requirement of this posting; must appear
  2 - clearly relevant; earns its place if there is room
  1 - tangential; only if nothing better
  0 - not relevant to this posting

Judge on what the item DEMONSTRATES, not keyword overlap: a project in a
different language still grades high if it shows the engineering this posting
asks for. Being impressive is not relevance. Use the full range — a grading
where most items are 2 or 3 is not a grading.

Return one entry per item id.`;

const JUDGE_SCHEMA = {
  type: 'object',
  properties: {
    grades: {
      type: 'array',
      items: { type: 'object',
               properties: { id: { type: 'integer' }, grade: { type: 'integer' } },
               required: ['id', 'grade'] },
    },
  },
  required: ['grades'],
};

const judgeUser = (reqs, jd, list) =>
  `## Requirements\n\n${reqs.map(r => `- ${r}`).join('\n')}`
  + (jd ? `\n\n## Posting (excerpt)\n\n${String(jd).slice(0, 2500)}` : '')
  + `\n\n## Candidate CV items\n\n${list}\n\nGrade every item 0-3 for this posting.`;

/**
 * Grade each bullet 0-3 with snipe-eval, few-shot from the gold set.
 *
 * Without exemplars the judge grades almost everything 2 or 3 and scores 0.670
 * pair accuracy — worse than plain cosine at 0.756 — so this returns null
 * rather than run 0-shot. Two exemplars lift it to 0.739 alone and to 0.851
 * blended with cosine, which is what makes it worth a 30B call.
 *
 * @returns {Promise<Map<string, number>|null>} bullet text -> grade, or null
 */
export async function judgeGrades(items, reqs, jdText, opts = {}) {
  const { ollamaUrl = 'http://localhost:11434', judgeModel = 'snipe-eval',
          judgeTimeoutMs = 180_000, judgeShots = [], _fetch = fetch } = opts;
  // No exemplars means 0-shot, and 0-shot the judge scores 0.670 against plain
  // cosine's 0.756 — actively worse. Refuse rather than degrade.
  if (!judgeShots.length) return null;
  const shots = judgeShots;

  const list = items.map((it, i) => `${i + 1}. ${it.text}`).join('\n');
  const messages = [{ role: 'system', content: JUDGE_SYSTEM }];
  for (const s of shots) {
    messages.push({ role: 'user', content: judgeUser(s.reqs, s.jd, list) });
    messages.push({ role: 'assistant', content: JSON.stringify({
      grades: items.map((it, i) => ({ id: i + 1, grade: s.want.has(it.text) ? 3 : 0 })) }) });
  }
  messages.push({ role: 'user', content: judgeUser(reqs, jdText, list) });

  try {
    const res = await _fetch(`${ollamaUrl}/api/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: judgeModel, messages, stream: false, format: JUDGE_SCHEMA,
                             options: { temperature: 0, num_ctx: 12288, num_predict: 1536 } }),
      signal: AbortSignal.timeout(judgeTimeoutMs),
    });
    if (!res.ok) return null;
    const judged = await res.json();
    logCall('p3-judge', judgeModel, judged, { extra: `items=${items.length}` });
    const parsed = JSON.parse(judged?.message?.content || '{}');
    const out = new Map();
    // The per-item `id` looks redundant — the order is fixed by the prompt — and
    // dropping it for a bare positional array would save ~660 output tokens, some
    // 35 s of this call. Measured on goldset-2 it costs 0.052 pair accuracy
    // (0.930 -> 0.878), roughly half the judge's entire value. The id is not
    // redundant to the model; it is what keeps it aligned and deliberate per item.
    for (const e of parsed.grades || []) {
      const it = items[Number(e.id) - 1];
      if (it) out.set(it.text, Math.max(0, Math.min(3, Number(e.grade) || 0)));
    }
    return out.size ? out : null;
  } catch {
    return null; // model missing, timeout, bad JSON — cosine alone still works
  }
}

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
    // scored/score are filled in by selectCvBullets; declared here so the entry
    // shape is complete at construction rather than grown by assignment.
    if (/^###\s+/.test(l)) { cur = { head: [l], bullets: [], scored: [], score: 0 }; entries.push(cur); continue; }
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

// ── Corpus-relative specificity ("spike") ────────────────────────────────────

const SPIKE_CACHE = resolve(__dir, 'cv-spike.json');
// Below this the mean is a description of a handful of postings rather than of
// the market, and subtracting it is noise. Returning null leaves plain cosine.
const SPIKE_MIN_OFFERS = 20;

/**
 * Mean relevance of each CV bullet across *past* postings.
 *
 * Cosine answers "does this bullet match this posting", which every generic
 * bullet also answers well — so ranking by it ships the fourth way of saying
 * "CI/CD" and drops the lock-free frame ring. Subtracting each bullet's corpus
 * mean turns the score into "does this posting like this bullet *more than
 * postings usually do*", which is what differentiating means.
 *
 * Measured on the 128-offer label corpus: +0.084 differentiator coverage on the
 * train split, +0.072 held out (CI95 [0.032, 0.112], 26-8, p=0.003) at weight 6,
 * with grade_yield flat — the gain is not bought by shipping less relevant work.
 * The background MUST come from requirement sets: using the full-JD vectors in
 * `jd-index.json` instead is a different scale and measured *negative* (-0.025).
 *
 * @returns {Promise<number[]|null>} per-bullet corpus mean, aligned to `bullets`
 */
export async function spikeBackground(bullets, opts = {}) {
  const { reportsDir = resolve(__dir, '..', 'reports'), _embed = embed } = opts;
  if (!existsSync(reportsDir)) return null;
  const reports = readdirSync(reportsDir).filter(f => f.endsWith('.md')).sort();
  if (reports.length < SPIKE_MIN_OFFERS) return null;

  // A stubbed embedder must neither read nor write the real cache: the
  // self-check and any test passing `_embed` would otherwise fill it with fake
  // vectors — one guard here beats a stub at every call site.
  // The fingerprint is part of the key for the same reason cv-index.json uses
  // it: `ollama create snipe-embed` on a new base leaves the tag unchanged, so
  // a tag-keyed hash keeps stale means silently.
  const caching = _embed === embed;
  const hash = caching
    ? createHash('sha1').update(JSON.stringify(bullets)).update('\n')
        .update(reports.join(',')).update(await modelFingerprint(opts)).digest('hex')
    : '';
  if (caching && existsSync(SPIKE_CACHE)) {
    try {
      const c = JSON.parse(readFileSync(SPIKE_CACHE, 'utf8'));
      if (c.hash === hash && c.mean?.length === bullets.length) return c.mean;
    } catch { /* rebuild */ }
  }

  // One requirement set per past report; reports without a parseable Block B
  // contribute nothing rather than a zero, which would drag every mean down.
  const reqSets = [];
  for (const f of reports) {
    try {
      const reqs = extractBlockBRequirements(readFileSync(resolve(reportsDir, f), 'utf8'));
      if (reqs.length) reqSets.push(reqs);
    } catch { /* skip */ }
  }
  if (reqSets.length < SPIKE_MIN_OFFERS) return null;

  const flat = reqSets.flat();
  const vecs = await _embed([...bullets, ...flat], opts);
  const bv = vecs.slice(0, bullets.length);
  const sums = new Array(bullets.length).fill(0);
  let at = bullets.length;
  for (const set of reqSets) {
    const qv = vecs.slice(at, at + set.length);
    at += set.length;
    // Max over requirements, matching how selectCvForJd scores a live offer.
    for (let i = 0; i < bullets.length; i++) sums[i] += Math.max(...qv.map(q => cosine(q, bv[i])));
  }
  const mean = sums.map(s => s / reqSets.length);
  if (caching) try { writeFileSync(SPIKE_CACHE, JSON.stringify({ hash, offers: reqSets.length, mean }), 'utf8'); } catch { /* cache is optional */ }
  return mean;
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
    // Weight on corpus-relative specificity. 6 is the measured optimum and the
    // curve is broad (4 -> +0.075, 6 -> +0.084, 8 -> +0.079), so it is a plateau,
    // not a knife edge. 0 disables it and restores plain cosine ranking.
    spikeWeight = 6,
    _embed = embed, _spikeBackground = spikeBackground,
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
      // The bullet is embedded bare. Prefixing it with its entry name
      // ("Teaching Assistant: …") injects a constant into every vector of that
      // entry, and on a 25-word bullet the constant is a large share of the
      // text — it pulls the whole entry toward one point and flattens the
      // ranking. Measured against the gold set: pair accuracy 0.689 prefixed
      // vs 0.757 bare, better on 11 of 12 offers. Prefixing projects only
      // (their titles being the informative ones) scored 0.720 — worse too.
      for (const b of e.bullets) items.push({ entry: e, text: b, ctx: b, score: 0 });
    }
  }
  if (!items.length) return cvText;

  const vecs = await _embed([...queries, ...items.map(i => i.ctx)], opts);
  const qv = vecs.slice(0, queries.length);
  for (let i = 0; i < items.length; i++) {
    const v = vecs[queries.length + i];
    const it = items[i];
    it.score = Math.max(...qv.map(q => cosine(q, v)));
  }

  // Subtract each bullet's corpus mean, so the score reads "more than postings
  // usually like this" rather than "matches this posting". Any failure — too few
  // past reports, unreadable cache — leaves the plain cosine scores untouched.
  if (spikeWeight) {
    try {
      const bg = await _spikeBackground(items.map(i => i.ctx), opts);
      // The sweep scored `cos + w*(cos - mean)`. Dividing by (1+w) is a positive
      // scale factor, so it ranks identically — but it keeps cosine on its
      // original scale, which matters because the judge term below is a flat
      // +0.10/grade benchmarked against unscaled cosine. Applied raw, w=6 would
      // inflate the cosine part sevenfold and quietly delete the rerank.
      const alpha = spikeWeight / (1 + spikeWeight);
      if (bg) for (let i = 0; i < items.length; i++) items[i].score -= alpha * bg[i];
    } catch { /* plain cosine */ }
  }

  // Rerank with the 30B judge, if exemplars are available. Measured on the gold
  // set at +0.10 pair accuracy over cosine alone (CI [0.027, 0.190], 7 offers
  // better 0 worse), and the gain held when the exemplar pair was swapped, so
  // it is not fitted to two lucky offers. Grades are 0-3; the weight is the one
  // benchmarked. Any failure leaves the cosine scores untouched.
  const grades = await judgeGrades(items, queries, jdText, opts);
  for (const it of items) {
    it.entry.scored.push({ text: it.text, score: it.score + (grades ? 0.10 * (grades.get(it.text) ?? 0) : 0) });
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
 * name to the real CV project it *describes*: the description decides, the name
 * only breaks ties. Naming one project while describing another used to pass
 * straight through on the name (report 149: "Re:Link" over a description of the
 * UBWIS platform, which is not a project at all), which is the same failure
 * reconcileExperience fixes for bullets. Entries matching nothing are dropped,
 * then the section is topped back up from the CV — untailored but true beats a
 * one-project CV. Mutates and returns `projects`.
 */
export function remapProjectNames(projects, cvText, minProjects = 3) {
  const sec = parseCvSections(cvText).find(s => s.name === 'Projects');
  if (!sec || !Array.isArray(projects)) return projects;
  const real = parseEntries(sec.lines).entries.map(e => ({
    name: e.head[0].replace(/^###\s+/, '').trim(),
    bullets: e.bullets,
    tokens: toks(e.head.join(' ') + ' ' + e.bullets.join(' ')),
  }));
  const used = new Set();
  const out = [];
  for (const p of projects) {
    const pName = String(p.name || '').toLowerCase();
    let best = null, bestN = 2; // require ≥3 overlapping tokens
    for (const r of real) {
      if (used.has(r.name)) continue;
      let n = 0;
      for (const t of toks(`${p.name} ${p.description || ''}`)) if (r.tokens.has(t)) n++;
      // The name is worth half a token — enough to break a tie between two real
      // projects, never enough to outvote the description.
      if (pName && r.name.toLowerCase().includes(pName.slice(0, 20))) n += 0.5;
      if (n > bestN) { best = r; bestN = n; }
    }
    if (!best) continue; // nothing on the CV looks like this — drop it
    used.add(best.name);
    out.push({ ...p, name: best.name });
  }
  // Backfill what the drops cost, in CV order, from the projects nothing claimed.
  for (const r of real) {
    if (out.length >= minProjects) break;
    if (used.has(r.name)) continue;
    used.add(r.name);
    // One bullet, not two space-joined: `.join(' ')` welded two sentences into
    // "…with zero cloud LLM calls Cut fabricated job requirements ~9x…", a
    // run-on with no separator. padProjectDescriptions extends this to the
    // length floor afterwards, with real punctuation between clauses.
    out.push({ name: r.name, description: r.bullets[0] || '' });
  }
  return out;
}

/**
 * Split a CV bullet at sentence/clause boundaries, ignoring the ones inside
 * brackets. A plain `/(?<=[.;])\s+/` split shipped
 * "Built a Rust microservice testbed (API gateway, hashing, and manifest-signing
 * services." — the semicolon it broke on was inside the parenthetical, so the
 * clause ended mid-aside with the bracket never closed. No harness metric parses
 * sentences, so this was only ever going to be caught by reading the output.
 * @param {string} s
 * @returns {string[]}
 */
function splitClauses(s) {
  const out = [];
  let depth = 0, start = 0;
  for (let i = 0; i < s.length; i++) {
    const ch = s[i];
    if (ch === '(' || ch === '[') depth++;
    else if (ch === ')' || ch === ']') depth = Math.max(0, depth - 1);
    // Whitespace must follow, so "0.4 ms" and "e.g." are not boundaries.
    else if ((ch === '.' || ch === ';') && depth === 0 && /\s/.test(s[i + 1] ?? ' ')) {
      out.push(s.slice(start, i));
      start = i + 1;
    }
  }
  out.push(s.slice(start));
  return out;
}

/**
 * Pad a short project description from that project's own CV bullets.
 *
 * The prompt asks for two sentences and 35-55 words. Measured over twelve
 * consecutive runs it got a median of 17 and **0 of 36 descriptions inside the
 * band** — so this is not a wording problem, it is the missing floor again:
 * nothing downstream ever checked, so nothing ever changed. Spending the repair
 * retry on an instruction the model has ignored 36 times out of 36 buys a second
 * 7B call and probably the same answer; the CV text is already here, already
 * true, and free.
 *
 * Padding is clause-by-clause and stops the moment the floor is met, so a
 * description lands near the bottom of the band instead of inheriting a whole
 * 50-word bullet. A clause the description already covers is skipped — the model
 * usually rewrites the leading bullet, and repeating it reads worse than being
 * short.
 *
 * @param {any[]} projects `{name, description}`, names already remapped to the CV's
 * @param {string} cvText the CV handed to the model (bullets relevance-ordered)
 * @param {number} minWords
 */
export function padProjectDescriptions(projects, cvText, minWords = 35, maxWords = 55) {
  const sec = parseCvSections(cvText).find(s => s.name === 'Projects');
  if (!sec || !Array.isArray(projects)) return projects;
  const real = parseEntries(sec.lines).entries.map(e => ({
    name: e.head[0].replace(/^###\s+/, '').trim(),
    bullets: e.bullets,
  }));
  const words = s => String(s || '').trim().split(/\s+/).filter(Boolean).length;

  return projects.map(p => {
    let desc = String(p.description || '').trim();
    // "Built a high-performance." — the model sometimes stops mid-clause. Padding
    // such a description only welds good CV prose onto a broken opening, which
    // reads worse than not tailoring at all, so discard it and build from the CV.
    if (words(splitClauses(desc)[0] || '') < 6) desc = '';
    if (desc && words(desc) >= minWords) return p;
    const pn = String(p.name || '').toLowerCase();
    const src = real.find(r => r.name.toLowerCase() === pn)
             || real.find(r => pn && (r.name.toLowerCase().includes(pn) || pn.includes(r.name.toLowerCase())));
    if (!src) return p;
    // A CV bullet is often one long semicolon-joined sentence; splitting on
    // clause boundaries is what keeps the pad from overshooting the band.
    const dt = toks(desc);
    const covered = c => {
      const ct = toks(c);
      if (!ct.size) return 1;
      let shared = 0;
      for (const t of ct) if (dt.has(t)) shared++;
      return shared / ct.size;
    };
    // Least-covered first, so the clause the model already rewrote is the last
    // one reached and usually never is. A stable sort leaves the untouched
    // clauses — nearly all of them, scoring 0 — in cv-select's relevance order,
    // so this costs nothing but the redundancy it removes. Ranking beats a
    // skip-threshold here: there is no ratio that separates "already said" from
    // "shares two tokens with what was said".
    const chunks = src.bullets
      .flatMap(splitClauses)
      .map(s => s.replace(/^[;\s]+|[.;\s]+$/g, '').trim())
      .filter(Boolean)
      .map((c, i) => ({ c, i, cov: covered(c) }))
      .sort((a, b) => a.cov - b.cov || a.i - b.i);
    const add = c => {
      const s = `${c.charAt(0).toUpperCase()}${c.slice(1)}`.replace(/[.\s]+$/, '');
      desc = desc ? `${desc.replace(/[.\s]+$/, '')}. ${s}.` : `${s}.`;
    };
    const spare = [];
    for (const { c, cov } of chunks) {
      if (words(desc) >= minWords) break;
      if (cov > 0.6) continue; // a near-verbatim repeat is worse than being short
      // Respect the ceiling while there is any other clause to try. Overshooting
      // is not free: a 75-word blurb costs four lines, and the page-fit ladder
      // pays for it by dropping a whole project — which is exactly the floor this
      // was added to defend.
      if (words(desc) + words(c) > maxWords) { spare.push(c); continue; }
      add(c);
    }
    // Nothing short enough was left. A project below the floor reads as an
    // afterthought, so the floor outranks the ceiling on the last clause.
    if (words(desc) < minWords && spare.length) add(spare[0]);
    return { ...p, description: desc };
  });
}

/**
 * Reconcile the model's experience array against the employers the CV actually
 * lists. Measured on 24 offers: the model returns the right *number* of entries
 * once the schema floors it, but fills them with a duplicated employer (9/24) or
 * a project promoted to a job (8/24), and naming the employers in the prompt
 * changed neither figure. See PHASE3-EXPERIMENT-LEDGER.md — the lever is here,
 * not in the wording.
 *
 * Each real employer claims its best unclaimed model entry: an entry whose
 * company names it, otherwise one whose bullets overlap its CV bullets. An
 * employer that claims nothing is backfilled from the CV itself — those bullets
 * are already relevance-ranked and trimmed by selectCvForJd, so a backfilled
 * role is untailored but true, which beats absent. An employer that claims an
 * entry with fewer bullets than the CV gave it is topped back up the same way
 * (`topUpBullets`). Unclaimed model entries are dropped: they are the duplicates
 * and the projects.
 *
 * @param {any[]} items model experience entries
 * @param {string} selectedCv the CV text handed to the model
 * @returns {any[]} one entry per real employer, in CV order
 */
/**
 * Top a role's bullets back up to the count `selectCvForJd` handed the model.
 *
 * Every content guard in Phase 3 is a ceiling — the schema caps counts,
 * `clampContent` slices, the density ladder trims — so a model that returned one
 * bullet for a four-bullet role shipped a one-line job and nothing objected
 * (observed across twelve consecutive CVs, several with 1 bullet per employer).
 * The whole-entry backfill below already makes this trade for a role the model
 * dropped entirely; a role it half-dropped deserves the same. These bullets are
 * relevance-ranked and already trimmed, so an appended one is untailored but
 * true.
 *
 * A rewrite is traced to its source by the same token-overlap argmax the number
 * guard uses; the sources nothing was rewritten from are what gets appended, in
 * CV (relevance) order. A rewrite that merged two CV bullets only claims one of
 * them, so its sibling can reappear — a near-duplicate is a cheaper failure than
 * a missing bullet, and the alternative is an embedding call per role.
 *
 * @param {string[]} modelBullets
 * @param {string[]} cvBullets the role's bullets from the CV the model was given
 * @returns {string[]}
 */
function topUpBullets(modelBullets, cvBullets) {
  const kept = (modelBullets || []).filter(b => String(b || '').trim());
  if (!kept.length || !cvBullets?.length || kept.length >= cvBullets.length) return kept;
  const used = new Set();
  for (const b of kept) {
    const bt = toks(b);
    let best = -1, bestN = -1;
    for (let i = 0; i < cvBullets.length; i++) {
      let n = 0;
      for (const t of toks(cvBullets[i])) if (bt.has(t)) n++;
      if (n > bestN) { bestN = n; best = i; }
    }
    if (best >= 0) used.add(best);
  }
  const spare = cvBullets.filter((_, i) => !used.has(i));
  return [...new Set([...kept, ...spare.slice(0, cvBullets.length - kept.length)])];
}

export function reconcileExperience(items, selectedCv) {
  const sec = parseCvSections(selectedCv).find(s => s.name === 'Experience');
  if (!sec || !Array.isArray(items)) return items;
  const real = parseEntries(sec.lines).entries.map(e => ({
    company: entryCompany(e),
    role: e.head[0].replace(/^###\s+/, '').trim(),
    bullets: e.bullets,
  }));
  if (!real.length) return items;

  // One token set per real entry — the bullets are compared against every entry,
  // not just the one being filled, so a rewrite can be traced to its true owner.
  const realTokens = real.map(r => toks(r.bullets.join(' ')));
  const overlap = (bt, tk) => {
    if (!tk.size || !bt.size) return 0;
    let n = 0;
    for (const t of bt) if (tk.has(t)) n++;
    return n / bt.size;
  };

  const claimed = new Set();
  const out = [];
  for (let ri = 0; ri < real.length; ri++) {
    const r = real[ri];
    const target = r.company.toLowerCase();
    const role = r.role.toLowerCase();
    let best = -1, bestScore = 0;
    for (let i = 0; i < items.length; i++) {
      if (claimed.has(i)) continue;
      const name = String(items[i]?.company || '').trim().toLowerCase();
      const bt = toks((items[i]?.bullets || []).join(' '));
      const ov = overlap(bt, realTokens[ri]);
      // A name hit outranks any overlap — but only when the bullets under it are
      // not a better match for some OTHER role. Naming the right company buys
      // +1, which cleared the floor below on its own, so the model getting the
      // label right while pasting another role's bullets went straight through
      // (observed on report 146: a UBWIS bullet filed under the university —
      // overlap 0.12 there, 0.96 with UBWIS). Content decides provenance; the
      // name only breaks ties.
      const namedRaw = Boolean(name) && (target.includes(name) || name.includes(target) || role.includes(name));
      const named = namedRaw && !realTokens.some((tk, j) => j !== ri && overlap(bt, tk) > ov);
      const score = named ? 1 + ov : ov;
      if (score > bestScore) { bestScore = score; best = i; }
    }
    // Below the floor the entry is about some other employer entirely, so taking
    // it would relabel one role's bullets with another's name.
    if (best >= 0 && bestScore >= 0.35) {
      claimed.add(best);
      out.push({ ...items[best], company: r.company,
                 bullets: topUpBullets(items[best]?.bullets, r.bullets) });
    } else {
      out.push({ company: r.company, bullets: r.bullets });
    }
  }
  return out;
}

/**
 * The employer name for a parsed CV entry.
 *
 * Two layouts are in the wild: the company on its own bold line under the
 * heading (`### Role` / `**Company** - City | dates`), and the company folded
 * into the heading itself (`### Company - Role`). Callers used to each do their
 * own bold-line match, so a heading-only CV yielded no employers for the schema
 * floor and the prompt injection while the reconciler still resolved them —
 * the same CV producing two different answers depending on who asked.
 *
 * @param {{head: string[]}} entry
 * @returns {string}
 */
export function entryCompany(entry) {
  const title = (entry.head?.[0] || '').replace(/^###\s+/, '').trim();
  const bold = (entry.head?.[1] || '').match(/^\*\*(.+?)\*\*/);
  return bold ? bold[1].trim() : title;
}

/** Employer names from a CV's Experience section, in CV order. */
export function cvCompanies(cvText) {
  const sec = parseCvSections(cvText).find(s => s.name === 'Experience');
  if (!sec) return [];
  return parseEntries(sec.lines).entries.map(entryCompany).filter(Boolean);
}

/** Numbers worth attributing to the CV. Single digits are too noisy to track. */
// Not preceded by a letter: "L40 Engineer" (Monzo's internal job level) and
// "v4"/"H100"-style identifiers are names, not claims, and flagging the 40 in
// one made a job title read as an invented figure. Separators still count, so
// "AES-256" and "sub-500ms" are unaffected.
const NUMERIC = /(?<![A-Za-z])\d[\d,.]*\+?%?/g;
const numbersIn = s => new Set((String(s).match(NUMERIC) || [])
  .map(x => x.replace(/[.,]$/, '')).filter(x => x.length > 1));

/**
 * Revert any tailored bullet asserting a number the CV does not state.
 *
 * Measured across 24 offers: 15 carried an invented figure — `100+` on 11,
 * `170+` on 3 (the real 170 with a `+` appended, which overstates it), `150+`
 * on 1. Deleting the prompt's own fabricated metric examples changed this by
 * exactly nothing (ledger V4), so the repair belongs in code. Same shape as
 * `verifyAgainstCv()` in fit-rules.mjs, which fixed the equivalent Phase 1
 * surface.
 *
 * A bad bullet is replaced with the CV bullet it most resembles rather than
 * dropped, so the role keeps its depth; only the offending bullet loses its
 * rewrite. A bullet whose numbers all appear in the CV is untouched.
 *
 * @param {any[]} items experience entries, already reconciled
 * @param {string} cvText the full CV — a figure may legitimately come from any part of it
 * @returns {any[]}
 */
// A tenure claim: "2+ years of hands-on experience", or "with 5 years". Bare
// "over five years" (word form) and "~200 TB over 5 years" are deliberately not
// matched — those are durations in a projection, not a claim about the person.
// The experience-anchored branch takes an optional leading "with" so it wins the
// whole span at that position — listing the bare "with N years" branch first
// clipped it to "with 2+ years" and left "of hands-on experience" dangling.
// The word forms matter as much as the digits. A tailored summary produced
// "over a decade of experience" against a CV claiming no duration at all — no
// digit anywhere, so the numeric branches could not see it.
// A tenure span ("3+", "1-3", "2 to 4") and up to three qualifier words before
// "experience". The original pattern allowed a single value and a single
// qualifier, so "1-3 years of real production experience" — a range lifted from
// the posting, which cv.md states nowhere — matched nothing and shipped.
const TEN_N = String.raw`\d[\d.,]*\+?(?:\s*(?:[-–—]|to)\s*\d[\d.,]*\+?)?`;
const TEN_Q = String.raw`(?:\s+[a-z-]+){0,3}`;
const TENURE = new RegExp([
  String.raw`\b(?:with\s+)?${TEN_N}\s*years?(?:\s+of)?${TEN_Q}\s+experience\b`,
  String.raw`\bwith\s+${TEN_N}\s*years?\b`,
  String.raw`\b(?:over|nearly|almost|more\s+than)?\s*(?:a|one|two|three|several|many)?\s*decades?(?:\s+of)?${TEN_Q}\s+experience\b`,
  String.raw`\b(?:over|more\s+than)\s+(?:a|one)\s+decade\b`,
].join('|'), 'gi');

/**
 * Strip a years-of-experience claim the CV does not make.
 *
 * `verifyBulletNumbers` cannot catch this: it tests numeric tokens against the
 * whole CV, and the offending "2+" also occurs in an unrelated bullet ("2+
 * hours"), so the token is already allowed. The claim, not the digit, is what
 * is unsupported — cv.md states no tenure anywhere. Observed on the Mercor
 * tailor, which opened "Security-first Software Engineer with 2+ years of
 * hands-on experience in low-level systems (C++, Java)" against a CV whose
 * low-level work is Rust and which claims no duration at all.
 *
 * @param {string} summary the tailored summary
 * @param {string} cvText the full CV — a tenure it does state is left alone
 * @returns {string}
 */
/**
 * Strip a summary clause asserting a figure `cv.md` does not state.
 *
 * The experience bullets have `verifyBulletFigures` and the project blurbs have
 * `verifyProjectFigures`. The summary — the first block anyone reads — had
 * neither, and shipped "a live subscription platform serving 150+ users" against
 * a CV that says 170 paying members. `verifyBulletNumbers`' own docstring already
 * lists `150+` among the invented figures measured across 24 offers, so this was
 * a known fabrication pattern reaching the one surface nothing guarded.
 *
 * Clause surgery rather than reversion, for the reason products get it: a summary
 * has no single source line to revert to, and a shorter true summary beats a
 * longer false one.
 *
 * @param {string} summary
 * @param {string} cvText
 * @returns {string}
 */
export function verifySummaryFigures(summary, cvText) {
  if (typeof summary !== 'string' || !summary) return summary;
  const cvNums = numbersIn(cvText);
  // Deflate before cutting. "170+" is the CV's own 170 with an inflating "+"
  // appended — the exact pattern verifyBulletNumbers measured on 3 of 24 offers.
  // The claim around it is true and CV-specific ("a GDPR-compliant membership
  // platform with 170+ paying users"), so deleting the clause threw away real
  // evidence to remove one character. Correct the figure; only genuinely
  // unsupported ones then reach the clause surgery.
  const deflated = summary.replace(/(?<![A-Za-z])(\d[\d,.]*)\+/g,
    (m, base) => (!cvNums.has(m) && cvNums.has(base) ? base : m));
  // "800" against a CV that says "800+" is the same claim, weakened — and "over
  // 800 students" is literally what "800+" means. Requiring the token to match
  // exactly deleted a true sentence about teaching 800 students, and the
  // 50-word pad then filled the hole with boilerplate. Only inflation is a
  // fabrication; understatement is the candidate's own loss to take.
  const supported = n => cvNums.has(n) || cvNums.has(`${n}+`);
  return stripUnsupportedClauses(deflated, t => [...numbersIn(t)].some(n => !supported(n)));
}

export function stripUnsupportedTenure(summary, cvText) {
  if (typeof summary !== 'string' || !summary) return summary;
  const stated = new Set((String(cvText).match(TENURE) || []).map(m => m.toLowerCase().trim()));
  return summary
    .replace(TENURE, m => stated.has(m.toLowerCase().trim())
      ? m
      : (/^with\b/i.test(m) ? 'with experience' : 'experience'))
    .replace(/\s{2,}/g, ' ')
    .trim();
}

export function verifyBulletNumbers(items, cvText) {
  // Scoped to the employer's own entry, not the whole CV. A CV-global allow-set
  // answers "does this figure exist somewhere in the document", which is the
  // wrong question: it lets a UBWIS bullet claim the Zero Trust dashboard's
  // sub-500ms load times, because the number is real — just not theirs.
  return revertUnsupportedBullets(items, cvText, (b, _src, entryText) => {
    const allowed = numbersIn(entryText);
    return [...numbersIn(b)].some(n => !allowed.has(n));
  });
}

/**
 * Strip a clause claiming a figure this project's own CV entry does not state.
 *
 * Projects had no figure guard at all — only `stripFabricatedProducts`, which
 * knows product names and not numbers. Observed on one CV: a Re:Link blurb
 * claiming "970%+ revenue growth" (a figure absent from `cv.md` entirely) and a
 * DE-Store blurb claiming "sub-500ms load times" (real, but the Zero Trust
 * dashboard's). One entry-scoped check catches both, because neither figure is
 * in the entry doing the claiming.
 *
 * Clause surgery rather than revert: a blurb is synthesised from several source
 * bullets, so reverting joins them into a run-on — which is exactly what shipped
 * on 3 of 12 CVs as a 55-word PQC paragraph next to 12-word siblings.
 *
 * Runs after `remapProjectNames`, which has already resolved every `name` to a
 * real CV project, so the lookup is exact. A project that resolves to nothing is
 * left alone rather than gutted.
 *
 * @param {any[]} projects
 * @param {string} cvText
 */
export function verifyProjectFigures(projects, cvText) {
  if (!Array.isArray(projects)) return projects;
  const sec = parseCvSections(cvText).find(s => s.name === 'Projects');
  if (!sec) return projects;
  const byName = new Map();
  for (const e of parseEntries(sec.lines).entries) {
    byName.set(e.head[0].replace(/^###\s+/, '').trim().toLowerCase(),
               numbersIn(`${e.head.join(' ')} ${e.bullets.join(' ')}`));
  }
  return projects.map(p => {
    const allowed = byName.get(String(p?.name || '').trim().toLowerCase());
    if (!allowed || typeof p.description !== 'string') return p;
    return { ...p, description: stripUnsupportedClauses(p.description,
      t => [...numbersIn(t)].some(n => !allowed.has(n))) };
  });
}

/**
 * Drop the clauses of `text` that fail `isBad`, sentence by sentence.
 *
 * The repair for surfaces with no single source line to revert to — a summary or
 * a project blurb, both synthesised from several bullets. A sentence is kept
 * whole if it passes; otherwise its clean clauses are rejoined, and the rebuild
 * is only accepted if it actually cleared the problem (a claim sitting in the
 * sentence's only clause survives the join otherwise).
 *
 * @param {string} text
 * @param {(fragment: string) => boolean} isBad
 */
export function stripUnsupportedClauses(text, isBad) {
  if (!text || !isBad(text)) return text;
  const kept = [];
  for (const sentence of String(text).split(/(?<=[.!?])\s+/)) {
    if (!isBad(sentence)) { kept.push(sentence); continue; }
    const clauses = sentence.split(/,\s*/);
    const clean = clauses.filter(c => !isBad(c));
    if (clean.length && clean.length < clauses.length) {
      // ponytail: comma-splitting treats a comma-separated adjective list as two
      // clauses, so dropping one can orphan the other ("building secure," when
      // the clause carrying the noun goes). Deflating an inflated figure instead
      // of cutting its clause removed every observed instance — 1 summary in 12
      // now reaches this path at all, and none of the current corpus orphans.
      // Upgrade path if that changes: drop the whole sentence when the survivor
      // ends on a bare adjective, rather than splitting smarter.
      let rebuilt = clean.join(', ').replace(/\s+and\s*$/i, '').replace(/,\s*$/, '').trim();
      // Dropping the leading clause promotes a mid-sentence one to sentence start,
      // which shipped "…across Python, React, and Next.js. strong fundamentals,…".
      if (rebuilt) rebuilt = rebuilt.charAt(0).toUpperCase() + rebuilt.slice(1);
      if (rebuilt && !isBad(rebuilt)) kept.push(/[.!?]$/.test(rebuilt) ? rebuilt : `${rebuilt}.`);
    }
  }
  return kept.join(' ').replace(/\s+/g, ' ').trim();
}

/**
 * Revert a bullet that silently dropped the figures its CV source stated.
 *
 * The mirror of `verifyBulletNumbers`: that one catches a figure the model
 * invented, this one catches a figure it deleted. Measured across 12 shipped
 * CVs, 44 % of source figures survived the rewrite — the 7B truncates to the
 * first clause, so "Authored troubleshooting documentation and standardised
 * environment setup guides, cutting configuration time from 2+ hours to 30
 * minutes per student and reducing staff escalations by 90 %" ships as
 * "Authored troubleshooting documentation for students". Nothing is false, so
 * every existing guard passes it.
 *
 * Reverting was expected to be a trade — the bullet loses its JD keywords along
 * with the truncation — so it shipped only after measurement. Paired over 24
 * offers it cost nothing and paid: ats_coverage +0.025, CI [0.014, 0.039],
 * 16 wins 1 loss. The 7B's rewrite was net-negative on the one axis it existed
 * to improve, because the full CV bullet already contains more of the posting's
 * vocabulary than a truncation of it can.
 *
 * @param {any[]} items
 * @param {string} cvText
 */
export function verifyBulletFigures(items, cvText) {
  return revertUnsupportedBullets(items, cvText, (b, src) => {
    const kept = numbersIn(b);
    return [...numbersIn(src)].some(n => !kept.has(n));
  });
}

/**
 * Revert every bullet failing `isUnsupported` to the `cv.md` bullet it was
 * rewritten from — untailored but true beats tailored and false.
 *
 * Shared by the number guard and the named-product guard: both answer "this
 * bullet asserts something cv.md does not support", and both want the same
 * repair. A bullet is one sentence, so deleting it would cost a whole slot;
 * reverting keeps the slot and loses only the tailoring.
 *
 * The predicate is handed the source bullet as well as the rewrite, because
 * "this dropped something" is only answerable against the line it came from.
 * That means the argmax now runs for every bullet rather than only the failing
 * ones — a few token-set intersections per CV, against four model calls.
 *
 * It also gets the whole source *entry* as text, for the questions that are
 * about ownership rather than about one line: a figure belonging to a different
 * employer is unsupported here even though it is real somewhere in the CV.
 *
 * @param {any[]} items experience entries, `{company, bullets}`
 * @param {string} cvText
 * @param {(bullet: string, source: string, entryText: string) => boolean} isUnsupported
 */
export function revertUnsupportedBullets(items, cvText, isUnsupported) {
  if (!Array.isArray(items)) return items;
  const sec = parseCvSections(cvText).find(s => s.name === 'Experience');
  const byCompany = new Map();
  if (sec) {
    for (const e of parseEntries(sec.lines).entries) {
      byCompany.set(entryCompany(e).toLowerCase(), e);
    }
  }
  return items.map(entry => {
    const src = byCompany.get(String(entry?.company || '').trim().toLowerCase());
    const source = src?.bullets || [];
    // Head as well as bullets: the entry's dates and tech-stack line carry
    // figures a bullet may legitimately reference.
    const entryText = src ? `${src.head.join(' ')} ${src.bullets.join(' ')}` : '';
    const bullets = (entry?.bullets || []).map(b => {
      if (!source.length) return b;
      // Prefer the CV bullet this rewrite came from; overlap picks it out.
      const bt = toks(b);
      let best = source[0], bestN = -1;
      for (const cb of source) {
        let n = 0;
        for (const t of toks(cb)) if (bt.has(t)) n++;
        if (n > bestN) { bestN = n; best = cb; }
      }
      return isUnsupported(b, best, entryText) ? best : b;
    });
    // Reverting can collide two bullets onto the same CV line.
    return { ...entry, bullets: [...new Set(bullets)] };
  });
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

  // Every check below runs a stub embedder against the developer's real
  // reports/ — none of it may reach the real spike cache.
  const spikeBefore = existsSync(SPIKE_CACHE) ? readFileSync(SPIKE_CACHE, 'utf8') : null;

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

  // Reranker: a stub judge that grades one otherwise-weak bullet 3 must pull it
  // above a bullet cosine ranked higher, and a judge failure must change nothing.
  const shots = [{ reqs: ['r'], jd: 'j', want: new Set(['Built Rust encryption service']) }];
  // Near-identical vectors, so cosines land in a narrow band the way real ones
  // do (0.4-0.7). Against the original stub's 1.00-vs-0.07 gap a 0.3 grade
  // boost correctly cannot win, which tests nothing about the blend.
  // keep=2, not 1: at keep=1 trim()'s metric-bullet guarantee replaces whatever
  // won with the only bullet carrying a number, which masks the judge entirely.
  const tightStub = async (texts) => texts.map(t => [1, /rust|encrypt/i.test(t) ? 0.62 : 0.55, 0.2]);
  const gradeFor = (t) => (/Mentored two juniors/.test(t) ? 3 : 0);
  const fakeFetch = async (_u, init) => {
    const body = JSON.parse(init.body);
    const list = body.messages[body.messages.length - 1].content.split('## Candidate CV items\n\n')[1].split('\n\n')[0];
    const grades = list.split('\n').map(l => {
      const m = l.match(/^(\d+)\.\s*(.*)$/);
      return { id: Number(m[1]), grade: gradeFor(m[2]) };
    });
    return { ok: true, json: async () => ({ message: { content: JSON.stringify({ grades }) } }) };
  };
  const reranked = await selectCvForJd(fakeCv, reqs, '', {
    maxProjects: 2, maxBulletsPerRole: 2, maxBulletsPerProject: 2, _embed: tightStub,
    judgeShots: shots, _fetch: fakeFetch });
  assert(reranked.includes('Mentored two juniors'), 'judge grade 3 promotes a bullet cosine ranked last');
  const dead = await selectCvForJd(fakeCv, reqs, '', {
    maxProjects: 2, maxBulletsPerRole: 2, maxBulletsPerProject: 2, _embed: tightStub,
    judgeShots: shots, _fetch: async () => { throw new Error('ollama down'); } });
  assert(!dead.includes('Mentored two juniors'), 'judge failure falls back to cosine order');
  const noShots = await selectCvForJd(fakeCv, reqs, '', {
    maxProjects: 2, maxBulletsPerRole: 2, maxBulletsPerProject: 2, _embed: tightStub,
    _fetch: async () => { throw new Error('must not be called'); } });
  assert(!noShots.includes('Mentored two juniors'), 'no exemplars means no judge call at all');

  // Metric guarantee: force a no-digit top-2 by querying something both metric
  // bullets miss.
  const out2 = await selectCvForJd(fakeCv, ['mentoring and code review'], '', {
    maxBulletsPerRole: 1, _embed: async texts => texts.map(t => [t.toLowerCase().includes('mentor') ? 1 : 0, 0, 0.1]),
  });
  assert(/- .*\d/.test(out2.split('## Experience')[1].split('## Projects')[0]), 'metric bullet guaranteed per role');

  // No requirements + no JD → untouched
  const out3 = await selectCvForJd(fakeCv, [], '', { _embed: stub });
  assert(out3 === fakeCv, 'no queries → CV returned untouched');

  // Spike: a bullet that every past posting likes equally is filler, and must
  // lose to one this posting likes unusually much even at a lower raw cosine.
  // 'generic' scores 0.9 here and 0.9 everywhere; 'niche' scores 0.8 here and
  // 0.1 in the corpus, so plain cosine keeps 'generic' and spike must not.
  const spikeCv = '## Experience\n\n### Dev — Co (2020-2024)\n\n- generic agile delivery work\n- niche lock-free ring buffer\n';
  const dim = t => (/generic/.test(t) ? [0.9, 0] : /niche/.test(t) ? [0.8, 0] : [1, 0]);
  const spikeEmbed = async texts => texts.map(dim);
  const bg = { 'generic agile delivery work': 0.9, 'niche lock-free ring buffer': 0.1 };
  const withSpike = await selectCvForJd(spikeCv, ['the requirement'], '', {
    maxBulletsPerRole: 1, _embed: spikeEmbed,
    _spikeBackground: async (bullets) => bullets.map(b => bg[b] ?? 0),
  });
  assert(/niche/.test(withSpike), 'spike keeps the bullet this posting likes unusually much');
  const noSpike = await selectCvForJd(spikeCv, ['the requirement'], '', {
    maxBulletsPerRole: 1, spikeWeight: 0, _embed: spikeEmbed,
    _spikeBackground: async (bullets) => bullets.map(b => bg[b] ?? 0),
  });
  assert(/generic/.test(noSpike), 'spikeWeight 0 restores plain cosine ranking');

  // Too few past reports must degrade to plain cosine, not to zeros.
  assert(await spikeBackground(['a'], { reportsDir: resolve(__dir, 'no-such-dir') }) === null,
    'missing reports dir → null, not a crash');

  // Project-name remap: fabricated name → real project by content overlap;
  // exact-ish names untouched; pure inventions dropped.
  const remapped = remapProjectNames([
    { name: 'Betting Odds Encryptor', description: 'Implemented AES-256-GCM encryption in Rust with CLI subcommands' },
    { name: 'Web App', description: 'React frontend deployed to Vercel' },
    { name: 'Quantum Basket Weaving', description: 'totally unrelated invention' },
  ], fakeCv, 0);
  assert(remapped.length === 2, 'unmappable fabricated project dropped');
  assert(remapped[0].name === 'Crypto Tool', 'fabricated name remapped by content overlap');
  assert(remapped[1].name === 'Web App', 'real name kept');

  // The description decides provenance, not the name: naming one real project
  // while describing another lands on the one described (report 149).
  const misfiled = remapProjectNames([
    { name: 'Crypto Tool', description: 'Wrote Java batch processor with 99% uptime' },
  ], fakeCv, 0);
  assert(misfiled.length === 1 && misfiled[0].name === 'Java Batch',
    'misfiled project follows its description, not its name');

  // Dropping an entry must not leave a one-project CV — top up from the CV.
  const backfilled = remapProjectNames([
    { name: 'Quantum Basket Weaving', description: 'totally unrelated invention' },
  ], fakeCv, 3);
  assert(backfilled.length === 3, 'dropped projects backfilled from the CV');
  assert(backfilled.every(p => /Crypto Tool|Web App|Java Batch/.test(p.name)), 'backfill uses real CV projects');

  assert((existsSync(SPIKE_CACHE) ? readFileSync(SPIKE_CACHE, 'utf8') : null) === spikeBefore,
    'self-check must not write the real spike cache');

  console.log('✓ cv-select self-check passed');
}
