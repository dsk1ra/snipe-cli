// @ts-check
/**
 * embeddings.mjs — semantic retrieval layer for the staged evaluator.
 *
 * Brute-force cosine over JSON-cached vectors. At this corpus size (~100 CV
 * atoms, a few hundred JDs) a vector DB is pure overhead — revisit only past
 * ~50k vectors.
 *
 * Indexes (cached beside this file, invalidated by content hash + model):
 *   batch/cv-index.json  — CV atoms (bullets, skills lines, summary sentences)
 *   batch/jd-index.json  — past JDs from batch/jds/*.txt (calibration RAG)
 *
 * CLI: node batch/embeddings.mjs rebuild   (force-rebuild both indexes)
 *      node batch/embeddings.mjs sync      (incremental catch-up, no deletion)
 *      node batch/embeddings.mjs query "some requirement text"
 */

import { readFileSync, writeFileSync, existsSync, readdirSync } from 'fs';
import { resolve, dirname, join } from 'path';
import { fileURLToPath } from 'url';
import { createHash } from 'crypto';
import { cleanCvForPrompt, cleanJd } from './text-utils.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(__dirname, '..');

export const EMBED_MODEL = 'snipe-embed';
const CV_INDEX_PATH = resolve(__dirname, 'cv-index.json');
const JD_INDEX_PATH = resolve(__dirname, 'jd-index.json');

// ── Ollama embed API ──────────────────────────────────────────────────────────

export async function embed(texts, { model = EMBED_MODEL, ollamaUrl = 'http://localhost:11434' } = {}) {
  const input = Array.isArray(texts) ? texts : [texts];
  const res = await fetch(`${ollamaUrl}/api/embed`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, input, truncate: true }),
    signal: AbortSignal.timeout(120_000),
  });
  if (!res.ok) throw new Error(`Ollama embed HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const data = await res.json();
  if (!data.embeddings?.length) throw new Error('Ollama embed returned no embeddings');
  return data.embeddings;
}

export function cosine(a, b) {
  let dot = 0, na = 0, nb = 0;
  for (let i = 0; i < a.length; i++) { dot += a[i] * b[i]; na += a[i] * a[i]; nb += b[i] * b[i]; }
  const d = Math.sqrt(na) * Math.sqrt(nb);
  return d ? dot / d : 0;
}

export function topK(queryVec, index, k = 3) {
  return index
    .map(item => ({ ...item, sim: cosine(queryVec, item.vec) }))
    .sort((a, b) => b.sim - a.sim)
    .slice(0, k);
}

// ── CV atom index ─────────────────────────────────────────────────────────────

// Split the CV (+ article-digest if present) into retrievable atoms. Each atom
// carries an entity prefix ("Acme Corp — Software Engineer / PM: <bullet>") so a
// bullet stays meaningful out of context.
export function extractCvAtoms() {
  const atoms = [];
  const cv = cleanCvForPrompt(readFileSync(resolve(PROJECT, 'cv.md'), 'utf8'));

  const SECTIONS = ['Summary', 'Experience', 'Projects', 'Education', 'Certifications', 'Skills', 'Languages'];
  let entity = '';
  let section = '';
  for (const raw of cv.split('\n')) {
    const s = raw.trim();
    if (!s) continue;
    // Entity header: "### Role/Project" (current) — checked before ## (no overlap: /^##\s/ needs whitespace)
    const entityH = s.match(/^###\s+(.+?)\s*$/);
    if (entityH) { entity = entityH[1]; continue; }
    // Section header: "## Experience" (current) or "**Experience**" (legacy, where non-section bold-alone = entity)
    const sectionH = s.match(/^##\s+(.+?)\s*$/) || s.match(/^\*\*([^*]+)\*\*$/);
    if (sectionH) {
      const name = sectionH[1].trim();
      if (SECTIONS.includes(name)) { section = name; entity = ''; }
      else entity = name;
      continue;
    }
    // Labelled line: "**Category:** items" or "**Category**: items" — skills rows,
    // Key Modules / Achievement in Education. Gated by section so contact-header
    // lines (**Email:** …) never become atoms.
    const labelled = s.match(/^\*\*([^*:]+):?\*\*:?\s*(.+)$/);
    if (labelled && (section === 'Skills' || section === 'Education')) {
      atoms.push({ text: `${section} — ${labelled[1].trim()}: ${labelled[2].trim()}`, source: section.toLowerCase() });
      continue;
    }
    // Metadata line under an entity: "**Org/Type** | tech, tech | dates" — the tech
    // stack is prime retrieval material (degree line in Education likewise).
    if ((section === 'Projects' || section === 'Education') && /^\*\*/.test(s) && s.includes('|')) {
      const meta = s.replace(/\*\*/g, '').trim();
      atoms.push({ text: entity ? `${entity} — ${meta}` : meta, source: section.toLowerCase() });
      continue;
    }
    // Bullet
    if (s.startsWith('- ')) {
      const prefix = entity ? `${entity}: ` : '';
      atoms.push({ text: prefix + s.slice(2).trim(), source: section.toLowerCase() || 'cv' });
      continue;
    }
    // Summary sentences
    if (section === 'Summary' && s.length > 60) {
      for (const sent of s.split(/(?<=[.!?])\s+/)) {
        if (sent.trim().length > 40) atoms.push({ text: sent.trim(), source: 'summary' });
      }
    }
  }

  const digestPath = resolve(PROJECT, 'article-digest.md');
  if (existsSync(digestPath)) {
    for (const raw of readFileSync(digestPath, 'utf8').split('\n')) {
      const s = raw.trim();
      if (s.startsWith('- ') && s.length > 30) atoms.push({ text: s.slice(2).trim(), source: 'article-digest' });
    }
  }
  return atoms;
}

function hashOf(str) {
  return createHash('sha256').update(str).digest('hex').slice(0, 16);
}

export async function loadCvIndex(opts = {}) {
  const atoms = extractCvAtoms();
  const hash = hashOf(JSON.stringify(atoms.map(a => a.text)) + (opts.model || EMBED_MODEL));
  if (existsSync(CV_INDEX_PATH)) {
    try {
      const cached = JSON.parse(readFileSync(CV_INDEX_PATH, 'utf8'));
      if (cached.hash === hash) return cached.atoms;
    } catch {}
  }
  const vecs = await embed(atoms.map(a => a.text), opts);
  const indexed = atoms.map((a, i) => ({ ...a, vec: vecs[i] }));
  writeFileSync(CV_INDEX_PATH, JSON.stringify({ hash, model: opts.model || EMBED_MODEL, built_at: new Date().toISOString(), atoms: indexed }), 'utf8');
  return indexed;
}

// ── Past-JD index (calibration RAG) ───────────────────────────────────────────

// One vector per past JD (head of the cleaned text). Incremental: only new ids
// are embedded on refresh.
export async function loadJdIndex(opts = {}) {
  const jdsDir = resolve(__dirname, 'jds');
  let cached = { model: opts.model || EMBED_MODEL, entries: [] };
  if (existsSync(JD_INDEX_PATH)) {
    try { cached = JSON.parse(readFileSync(JD_INDEX_PATH, 'utf8')); } catch {}
  }
  if (cached.model !== (opts.model || EMBED_MODEL)) cached = { model: opts.model || EMBED_MODEL, entries: [] };
  const have = new Set(cached.entries.map(e => e.id));

  const ids = existsSync(jdsDir)
    ? readdirSync(jdsDir).filter(f => f.endsWith('.txt')).map(f => f.replace('.txt', ''))
    : [];
  const fresh = ids.filter(id => !have.has(id));
  if (fresh.length) {
    const texts = fresh.map(id => cleanJd(readFileSync(join(jdsDir, `${id}.txt`), 'utf8'), 2000));
    // Embed in chunks of 32 to keep request sizes sane
    for (let i = 0; i < fresh.length; i += 32) {
      const chunk = fresh.slice(i, i + 32);
      const vecs = await embed(texts.slice(i, i + 32), opts);
      chunk.forEach((id, j) => cached.entries.push({ id, vec: vecs[j] }));
    }
    writeFileSync(JD_INDEX_PATH, JSON.stringify(cached), 'utf8');
  }
  return cached.entries;
}

/**
 * Top-k most similar PAST offers with their eval outcomes — few-shot calibration
 * anchored to the user's own history. Excludes the offer itself.
 */
export async function similarPastOffers(jdText, selfId, k = 3, opts = {}) {
  const entries = await loadJdIndex(opts);
  const evalsDir = resolve(__dirname, 'evals');
  const [qVec] = await embed(cleanJd(jdText, 2000), opts);
  const labels = loadLabelsFile();
  const outcomes = loadOutcomes();
  const ranked = entries
    .filter(e => e.id !== String(selfId))
    .map(e => ({ ...e, sim: cosine(qVec, e.vec) }))
    .sort((a, b) => b.sim - a.sim);

  const out = [];
  for (const e of ranked) {
    const evalPath = join(evalsDir, `${e.id}.json`);
    if (!existsSync(evalPath)) continue;
    try {
      const ev = JSON.parse(readFileSync(evalPath, 'utf8'));
      if (typeof ev.score !== 'number') continue;
      out.push({
        id: e.id, sim: +e.sim.toFixed(3), score: ev.score,
        company: ev.company, role: ev.role,
        decision: ev.final_decision,
        user_label: labels.get(e.id) ?? null,
        outcome: outcomes.get(Number(ev.report_num)) ?? null,
      });
    } catch {}
    if (out.length >= k) break;
  }
  return out;
}

// ── Tracker outcomes (calibration feedback) ──────────────────────────────────
// Real-world result per past offer, joined LIVE from the tracker at query time
// (like labels.tsv) — never baked into jd-index, so it's always current.
// Keyed by tracker row # == report_num. 'Evaluated' = no outcome yet → absent.
const OUTCOME_BY_STATUS = {
  applied:   'applied, no response yet',
  responded: 'company responded',
  interview: 'reached interview',
  offer:     'received offer',
  rejected:  'rejected',
  discarded: 'user chose not to apply',
  skip:      'user chose not to apply',
};

export function loadOutcomes(trackerPath = resolve(PROJECT, 'data', 'applications.md')) {
  const out = new Map(); // report_num (int) → outcome string
  const p = trackerPath;
  if (!existsSync(p)) return out;
  for (const line of readFileSync(p, 'utf8').split('\n')) {
    // | # | Date | Company | Role | Score | Status | ... → cells[1]=#, cells[6]=Status
    const cells = line.split('|').map(s => s.trim());
    if (cells.length < 7 || !/^\d+$/.test(cells[1])) continue;
    const outcome = OUTCOME_BY_STATUS[cells[6].toLowerCase()];
    if (outcome) out.set(Number(cells[1]), outcome);
  }
  return out;
}

function loadLabelsFile() {
  const p = resolve(__dirname, 'labels.tsv');
  const out = new Map();
  if (!existsSync(p)) return out;
  for (const line of readFileSync(p, 'utf8').split('\n')) {
    const [id, v] = line.split('\t');
    if (id && v) out.set(id.trim(), parseFloat(v));
  }
  return out;
}

// ── CLI ───────────────────────────────────────────────────────────────────────

import { unlinkSync } from 'fs';
const isMain = process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1];
if (isMain) {
  const cmd = process.argv[2];
  if (cmd === 'rebuild') {
    for (const p of [CV_INDEX_PATH, JD_INDEX_PATH]) { try { unlinkSync(p); } catch {} }
    const atoms = await loadCvIndex();
    console.log(`✓ cv-index: ${atoms.length} atoms`);
    const jds = await loadJdIndex();
    console.log(`✓ jd-index: ${jds.length} JDs`);
  } else if (cmd === 'sync') {
    // Incremental catch-up (no cache deletion) — safe to run after every batch;
    // only embeds JDs/CV atoms that aren't already indexed.
    const atoms = await loadCvIndex();
    console.log(`✓ cv-index: ${atoms.length} atoms`);
    const jds = await loadJdIndex();
    console.log(`✓ jd-index: ${jds.length} JDs`);
  } else if (cmd === 'query') {
    const q = process.argv[3];
    if (!q) { console.error('usage: embeddings.mjs query "text"'); process.exit(1); }
    const index = await loadCvIndex();
    const [qv] = await embed(q);
    for (const hit of topK(qv, index, 5)) {
      console.log(`  ${hit.sim.toFixed(3)}  [${hit.source}] ${hit.text.slice(0, 110)}`);
    }
  } else {
    console.log('usage: embeddings.mjs <rebuild|sync|query "text">');
  }
}
