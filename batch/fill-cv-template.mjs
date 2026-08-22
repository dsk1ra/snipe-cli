#!/usr/bin/env node
/**
 * fill-cv-template.mjs — Merges Ollama JSON content + cv.md into source.tex
 *
 * Usage:
 *   node batch/fill-cv-template.mjs \
 *     --content /tmp/cv-content-42.json \
 *     --output output/2026-01-01_company_042/source.tex
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import yaml from 'js-yaml';
import { groundSkillItems, sanitizeAtsText } from './text-utils.mjs';

const __dirname  = dirname(fileURLToPath(import.meta.url));
const PROJECT    = resolve(__dirname, '..');
const CV_PATH    = resolve(PROJECT, 'cv.md');
const PROFILE    = resolve(PROJECT, 'config/profile.yml');
const TEMPLATE   = resolve(PROJECT, 'templates/cv-template.tex');

// ── Args ──────────────────────────────────────────────────────────────────────

const args = {};
for (let i = 2; i < process.argv.length; i++) {
  if (process.argv[i].startsWith('--')) {
    args[process.argv[i].slice(2)] = process.argv[i + 1];
    i++;
  }
}

const contentPath = args['content'];
const outputPath  = args['output'];
const maxSkills   = args['max-skills'] ? parseInt(args['max-skills'], 10) : null; // null = all
const targetRole  = (args['role'] || '').trim(); // printed under the name; omitted when absent
const maxBullets  = args['max-bullets'] ? parseInt(args['max-bullets'], 10) : null; // null = all; caps bullets per role
// Same idea for the project lists, so the density ladder can shorten a project
// instead of only being able to delete one.
const maxProjBullets = args['max-project-bullets'] ? parseInt(args['max-project-bullets'], 10) : null;

// Checked after the self-check below, so a bare invocation runs that instead.
function requireArgs() {
  if (!contentPath || !outputPath) {
    console.error('Usage: fill-cv-template.mjs --content <json> --output <tex> [--max-skills N] [--role "Title"]');
    process.exit(1);
  }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// LaTeX's ten special characters, plus the three T1 text-mode traps: bare <, >
// and | typeset as ¡, ¿ and — rather than themselves, silently, with no error.
// cv.md is markdown written by a human and carries all of them (C++ in a module
// list, 85%+ coverage, snake_case identifiers, "Redis/Valkey | Caddy").
const TEX_ESCAPES = {
  '\\': '\\textbackslash{}', '{': '\\{',  '}': '\\}',
  '$':  '\\$',  '&': '\\&',  '#': '\\#',  '_': '\\_', '%': '\\%',
  // cv.md's tildes are all "approximately" (~200 TB, ~0.4 ms, ~11K/day).
  // \textasciitilde is the faithful glyph and typesets as a raised squiggle
  // that reads as a macron at this size; $\sim$ is what the text means.
  '~':  '$\\sim$', '^': '\\textasciicircum{}',
  '<':  '\\textless{}', '>': '\\textgreater{}', '|': '\\textbar{}',
};

// ATS normalization runs BEFORE escaping, not on the finished .tex. `•` maps to
// ` | `, and a pipe introduced after the escaper has run reaches LaTeX bare —
// where T1 typesets it as an em-dash. The HTML path normalizes the rendered
// document instead; see sanitizeAtsText.
function tex(s) {
  return sanitizeAtsText(String(s ?? '')).replace(/[\\{}$&#_%~^<>|]/g, c => TEX_ESCAPES[c]);
}

// hyperref reads the href target verbatim, so only the two characters TeX still
// eats there need escaping. Everything else in a URL — including ~ and & — is
// safe inside \href's first argument.
function texUrl(s) {
  return String(s ?? '').replace(/([%#\\])/g, '\\$1');
}

// Wrap measurable metrics in \textbf. Targets counts, percentages, latencies,
// currency, units (k/M/ms/x/GB…) and durations; leaves bare years (2025),
// version numbers (2.0) and "AES-256" untouched since they lack a
// separator/sign/unit.
const METRIC_RE = new RegExp([
  '[£$€]\\s?\\d[\\d,]*(?:\\.\\d+)?\\+?[kKmMbB]?',                 // £3,500, $120k
  'sub-\\d[\\d,]*\\s?ms',                                          // sub-500ms
  '\\d[\\d,]*(?:\\.\\d+)?%\\+?',                                   // 80%, 95%+, 300%+
  '\\d[\\d,]*(?:\\.\\d+)?\\s?(?:ms|x|×|GB|MB|TB|bn)\\b\\+?',       // 250ms, 10x
  '(?<![A-Za-z])\\d[\\d,]*(?:\\.\\d+)?[kKMB]\\+?(?![a-zA-Z])',     // 3M+, 11K, 50K (not the "2B" in B2B)
  '\\d[\\d,]*\\+',                                                 // 100+, 50,000+, 5+
  '\\d{1,3}(?:,\\d{3})+',                                          // 50,000
  '\\d+\\s?(?:weeks?|days?|hours?|minutes?|months?|years?)\\b',    // 4 weeks, 30 minutes
].join('|'), 'g');

/**
 * Escape for LaTeX and bold the metrics, in one pass over the RAW string.
 *
 * The HTML template could run its metric regex over already-escaped text,
 * because escaping HTML leaves digits, %, £ and $ alone. LaTeX escaping does
 * not: `95%` becomes `95\%` and `$120k` becomes `\$120k`, so a regex tuned to
 * the source text stops matching the moment it is escaped. Matching first and
 * escaping the pieces keeps METRIC_RE reading the text it was calibrated on.
 *
 * @param {string} raw
 * @returns {string} LaTeX-safe text with metrics wrapped in \textbf
 */
function texBold(raw) {
  const s = String(raw ?? '');
  let out = '', last = 0;
  for (const m of s.matchAll(METRIC_RE)) {
    out += tex(s.slice(last, m.index)) + `\\textbf{${tex(m[0])}}`;
    last = m.index + m[0].length;
  }
  return out + tex(s.slice(last));
}

function wordSet(s) {
  return new Set(s.toLowerCase().replace(/[^a-z0-9 ]/g, ' ').split(/\s+/).filter(Boolean));
}

function jaccardSim(a, b) {
  const sa = wordSet(a), sb = wordSet(b);
  let inter = 0;
  for (const w of sa) if (sb.has(w)) inter++;
  const union = new Set([...sa, ...sb]).size;
  return union === 0 ? 0 : inter / union;
}

function matchProject(jsonName, cvName) {
  const j = jsonName.toLowerCase();
  const c = cvName.toLowerCase();
  if (c.includes(j.slice(0, Math.min(j.length, 20)))) return true;
  if (jaccardSim(j, c) >= 0.35) return true;
  return false;
}

function matchCompany(jsonCompany, cvCompany) {
  const j = String(jsonCompany ?? '').toLowerCase().trim();
  const c = String(cvCompany ?? '').toLowerCase().trim();
  // Neither side may be empty. `''.includes('')` and `x.includes('')` are both
  // true, so a cv.md entry whose company failed to parse used to match the
  // FIRST model entry and every employer after it rendered the same bullets.
  if (!j || !c) return false;
  return j === c || c.includes(j) || j.includes(c);
}

const PERIOD_MONTHS = { jan: 0, feb: 1, mar: 2, apr: 3, may: 4, jun: 5, jul: 6, aug: 7, sep: 8, oct: 9, nov: 10, dec: 11 };

// "Oct 2024 – Sep 2025" / "Sep 2025 – Present" / "2025 – 2026" → sortable end-date value.
function parsePeriodEnd(period) {
  const end = String(period || '').split(/[–-]/).pop().trim();
  if (/present/i.test(end)) return Infinity;
  const m = end.match(/([A-Za-z]{3,9})\s+(\d{4})/);
  if (m) return parseInt(m[2], 10) * 12 + (PERIOD_MONTHS[m[1].slice(0, 3).toLowerCase()] ?? 0);
  const y = end.match(/(\d{4})/);
  return y ? parseInt(y[1], 10) * 12 : -Infinity;
}

// ── Parse cv.md ───────────────────────────────────────────────────────────────

const KNOWN_SECTIONS = ['Summary', 'Experience', 'Projects', 'Education', 'Certifications', 'Skills'];

function splitSections(text) {
  const sections = { header: [] };
  let current = 'header';
  for (const line of text.split('\n')) {
    const s = line.trim();
    // Section header: **Section** (legacy) or ## Section (markdown). ### stays content.
    const m = s.match(/^\*\*([^*]+)\*\*\s*$/) || s.match(/^##\s+(.+?)\s*$/);
    if (m && KNOWN_SECTIONS.includes(m[1])) {
      sections[m[1]] = sections[m[1]] ?? [];
      current = m[1];
    } else {
      (sections[current] ??= []).push(line);
    }
  }
  return sections;
}

function parseExperience(lines) {
  const entries = [];
  let cur = null;

  for (const raw of lines) {
    const s = raw.trim();
    if (!s) continue;

    // Company line: **Company** — Location | Period
    //
    // The dash may be an em-dash, an en-dash or a plain ASCII hyphen. cv.md is
    // a user-layer file edited by hand, and matching only the typographic ones
    // fails *silently*: the line falls through to the role-title branch, which
    // does not match either, so company, location and period all render empty
    // and the CV ships with no employer names or dates on it. The empty company
    // then makes every JSON entry match the first role (see matchCompany), so
    // every employer gets the same bullets.
    if (/^\*\*[^*]+\*\*\s*(?:—|–|-)\s/.test(s)) {
      const m = s.match(/^\*\*([^*]+)\*\*\s*(?:—|–|-)\s*([^|]+)\|\s*(.+)$/);
      if (m && cur) {
        cur.company  = m[1].trim();
        cur.location = m[2].trim();
        cur.period   = m[3].trim();
      }
      continue;
    }

    // Bullet
    if (s.startsWith('- ')) {
      cur?.bullets.push(s.slice(2));
      continue;
    }

    // Role title: **Title** alone or ### Title
    const t = s.match(/^\*\*([^*]+)\*\*$/) || s.match(/^###\s+(.+)$/);
    if (t) {
      if (cur) entries.push(cur);
      cur = { role: t[1].trim(), company: '', location: '', period: '', bullets: [] };
      continue;
    }
  }
  if (cur) entries.push(cur);
  return entries;
}

function parseProjects(lines) {
  const entries = [];
  let cur = null;

  for (const raw of lines) {
    const s = raw.trim();
    if (!s) continue;

    // Badge + tech line: **Badge** | Tech | Period
    if (/^\*\*[^*]+\*\*\s*\|/.test(s)) {
      if (cur) {
        const m = s.match(/^\*\*([^*]+)\*\*\s*\|\s*(.+?)\s*\|\s*(.+)$/);
        if (m) {
          cur.badge  = m[1].trim();
          cur.tech   = m[2].trim();
          cur.period = m[3].trim();
        }
      }
      continue;
    }

    // URL line
    if (/^(https?:\/\/|github\.com\/)/.test(s)) {
      if (cur) cur.url = s;
      continue;
    }

    // Bullet
    if (s.startsWith('- ')) {
      cur?.bullets.push(s.slice(2));
      continue;
    }

    // Project title: **Title** alone or ### Title (may contain dashes)
    const t = s.match(/^\*\*([^*]+)\*\*$/) || s.match(/^###\s+(.+)$/);
    if (t) {
      if (cur) entries.push(cur);
      cur = { name: t[1].trim(), badge: '', tech: '', period: '', url: '', bullets: [] };
      continue;
    }
  }
  if (cur) entries.push(cur);
  return entries;
}

function parseEducation(lines) {
  const entries = [];
  let cur = null;
  for (const raw of lines) {
    const s = raw.trim();
    if (!s) continue;

    // Degree line with pipe: **Degree** | Period   (or **School** — Degree | Period)
    if (/^\*\*[^*]+\*\*\s*\|/.test(s)) {
      if (cur) {
        const m = s.match(/^\*\*([^*]+)\*\*\s*\|\s*(.+)$/);
        if (m) { cur.degree = m[1].trim(); cur.period = m[2].trim(); }
      }
      continue;
    }
    // Inline key–value: **Key Modules:** ... or **Achievement:** ...
    if (/^\*\*[^*]+:\*\*/.test(s) || /^\*\*[^*]+\*\*:/.test(s)) {
      if (cur) cur.extra.push(s.replace(/\*\*/g, '').trim());
      continue;
    }
    // School name: **School Name** alone
    if (/^\*\*[^*]+\*\*$/.test(s)) {
      if (cur) entries.push(cur);
      cur = { school: s.slice(2, -2), degree: '', period: '', extra: [] };
      continue;
    }
    // Plain text lines (achievements etc.)
    if (cur && s) cur.extra.push(s.replace(/\*\*/g, ''));
  }
  if (cur) entries.push(cur);
  return entries;
}

function parseCertifications(lines) {
  const certs = [];
  for (const raw of lines) {
    const s = raw.trim();
    if (!s.startsWith('- ')) continue;
    const parts = s.slice(2).split(/\s*—\s*/);
    certs.push({
      name: (parts[0] || '').trim(),
      org:  (parts[1] || '').trim(),
      year: (parts[2] || '').trim(),
    });
  }
  return certs;
}

function parseSkills(lines) {
  const rows = [];
  let humanLangMode = false;
  const humanLangs = [];

  for (const raw of lines) {
    const s = raw.trim();
    if (!s) continue;

    // Human languages section header: **Languages** or ## Languages (no colon)
    if (/^(\*\*Languages\*\*|##\s+Languages)$/.test(s)) { humanLangMode = true; continue; }

    // In human-lang mode, collect bullet lines
    if (humanLangMode && s.startsWith('- ')) {
      humanLangs.push(s.slice(2));
      continue;
    }

    // Skip base64 image lines
    if (s.startsWith('![](data:image')) continue;

    // Skill category: **Category:** items
    const m = s.match(/^\*\*([^*]+)\*\*:\s*(.+)$/);
    if (m) { rows.push({ category: m[1].trim(), items: m[2].trim() }); continue; }

    // Alternate: **Category:** \n items
    const m2 = s.match(/^\*\*([^*]+:\*\*)\s*(.*)$/);
    if (m2) { rows.push({ category: m2[1].replace(/\*\*/g, '').replace(/:$/, '').trim(), items: m2[2].trim() }); }
  }

  if (humanLangs.length > 0) {
    rows.push({ category: 'Human Languages', items: humanLangs.join(', ') });
  }
  return rows;
}

// ── LaTeX builders ────────────────────────────────────────────────────────────

// `\begin{itemize}\end{itemize}` with no \item is a LaTeX error ("Something's
// wrong--perhaps a missing \item"), not the harmless empty <ul> the HTML
// template used to emit. Every list the density ladder can empty goes through
// here, so an entry whose bullets are all trimmed away renders as a bare
// heading rather than failing the whole compile.
function itemList(items, indent = '      ') {
  const kept = items.filter(Boolean);
  if (!kept.length) return '';
  return `\n${indent}\\resumeItemListStart\n${kept.join('\n')}\n${indent}\\resumeItemListEnd`;
}

// UK CV convention: ALL companies, reverse-chronological, never reordered or
// silently dropped by the model — the CV itself is the source of truth for
// the company list and order; the model only supplies (or fails to supply,
// in which case we fall back to the CV's own) bullets per company.
function buildExperienceTex(cvExp, jsonExp, maxBullets) {
  const ordered = [...cvExp]
    .sort((a, b) => parsePeriodEnd(b.period) - parsePeriodEnd(a.period))
    .map(ce => {
      const je = (jsonExp || []).find(j => matchCompany(j.company, ce.company));
      return je ? { ...ce, bullets: je.bullets } : ce;
    });

  return ordered.map(e => {
    // Cap bullets per role (applies to model-provided AND cv-backfilled entries)
    // so the page-fit ladder can reduce depth uniformly without gutting any one
    // role. Bullets are already ordered most-relevant-first.
    const bullets = maxBullets ? e.bullets.slice(0, maxBullets) : e.bullets;
    // Company/period on the bold line, role/location italic underneath — the
    // template's own \resumeSubheading shape. The HTML template welded all four
    // onto one line because three stacked <div>s cost 51px a role; LaTeX stacks
    // two at 0.97\textwidth for far less, so the upstream two-line form stands.
    return `    \\resumeSubheading
      {${tex(e.company)}}{${tex(e.period)}}
      {${tex(e.role)}}{${tex(e.location)}}${
      itemList(bullets.map(b => `        \\resumeItem{${texBold(b)}}`))}`;
  }).join('\n');
}

// `github.com/Drakon4ik-Coder/okta-dashboard-back` is the longest string on the
// page. The host is redundant — the header already carries the GitHub profile —
// so a github.com URL renders as `owner/repo`. Any other host keeps its name,
// because there the domain is the information.
// cv.md writes repo URLs bare ("github.com/dsk1ra/snipe-cli"). An href without a
// scheme resolves against the local file and points nowhere.
function absoluteUrl(url) {
  const s = String(url || '').trim();
  return /^https?:\/\//i.test(s) ? s : `https://${s}`;
}

function shortRepo(url) {
  const s = String(url || '').replace(/^https?:\/\//, '').replace(/^www\./, '');
  const m = s.match(/^github\.com\/(.+)$/i);
  return m ? m[1].replace(/\/$/, '') : s;
}

// Accepts the new `projects` field (array of {name, description}) OR the legacy
// `selected_projects` (array of name strings). When the LLM supplies a tailored
// description we use it; otherwise we fall back to the project's first 2 CV
// bullets (never the full dump) so descriptions stay tight.
function buildProjectsTex(cvProjects, projectsField, maxProjBullets) {
  const sel = Array.isArray(projectsField)
    ? projectsField
        .map(p => (typeof p === 'string'
          ? { name: p, description: '', bullets: [] }
          : { name: p?.name || '', description: p?.description || '',
              bullets: Array.isArray(p?.bullets)
                ? (maxProjBullets ? p.bullets.slice(0, maxProjBullets) : p.bullets) : [] }))
        .filter(p => p.name)
    : [];

  let chosen;
  if (sel.length > 0) {
    chosen = [];
    for (const s of sel) {
      const match = cvProjects.find(p => matchProject(s.name, p.name));
      if (match && !chosen.some(c => c.cv === match)) {
        chosen.push({ cv: match, description: s.description, bullets: s.bullets });
      }
    }
  } else {
    chosen = cvProjects.slice(0, 4).map(cv => ({ cv, description: '', bullets: [] }));
  }

  return chosen.map(({ cv, description, bullets }) => {
    // The repo is a link, not text. A reviewer who wants to check a claim should
    // not have to retype a URL off a PDF — and the tech line is the only place
    // the CV offers one per project.
    const repoTex = cv.url
      ? `\\href{${texUrl(absoluteUrl(cv.url))}}{\\underline{${tex(shortRepo(cv.url))}}}`
      : '';
    const techLine = [cv.tech ? tex(cv.tech) : '', repoTex].filter(Boolean).join(' $|$ ');
    // Bullets when the writer supplies them, one paragraph otherwise.
    //
    // The paragraph was the only shape for a long time, and it is where the
    // candidate's most distinctive evidence was going to die. Projects hold 24 of
    // this CV's 33 selectable atoms and most of what a reviewer called a
    // differentiator, and squashing five selected bullets into a 35-55 word blurb
    // physically cannot carry them: measured against the Opus labels, every
    // differentiator lost on a sampled offer was a project bullet, and
    // differentiator_coverage sat at 0.17-0.50 while every falsity metric read
    // perfect. A list costs vertical space, which is what the density ladder is
    // for; losing the evidence costs the application.
    const body = bullets.length
      ? bullets.map(b => `        \\resumeItem{${texBold(String(b))}}`)
      : [`        \\resumeItem{${texBold(
          description && description.trim()
            ? description.trim()
            // Joining two bullets with a bare space runs two sentences together
            // with no separator.
            : cv.bullets.slice(0, 2).join('. ').replace(/\.\.\s/g, '. '))}}`];
    const head = [`\\textbf{${tex(cv.name)}}`, cv.badge ? `\\emph{${tex(cv.badge)}}` : '']
      .filter(Boolean).join(' $|$ ');
    return `    \\resumeProjectHeading
      {${head}}{${tex(cv.period)}}${
      itemList([...body, techLine ? `        \\resumeTechLine{${techLine}}` : ''])}`;
  }).join('\n');
}

// "Achievement: 2nd place out of 24 teams" — bold up to the colon so the label
// reads as a heading for its row. Bounded at 20 chars so a line that merely
// contains a colon mid-sentence is not carved up at it.
function labelled(line) {
  const m = String(line).match(/^([^:]{1,20}):\s*([\s\S]*)$/);
  return m ? `\\textbf{${tex(m[1])}:} ${tex(m[2])}` : tex(line);
}

// When the LLM supplies `education_modules` (a JD-relevant subset), rewrite the
// "Key Modules:" line to those modules only; other extra lines (Achievement,
// etc.) are kept as-is. Without the field, the CV's modules pass through verbatim.
function buildEducationTex(eduEntries, selectedModules, certs = []) {
  const sel = Array.isArray(selectedModules) && selectedModules.length > 0 ? selectedModules : null;
  // Certifications ride the last education entry as one more line. Its own
  // section cost a heading and a rule to carry two lines of content.
  // Name and year only — the awarding body repeats what the name already says.
  const certLine = certs.length
    ? `Certifications: ${certs.map(c => [c.name, c.year].filter(Boolean).join(', ')).join(' • ')}`
    : '';
  return eduEntries.map((e, i) => {
    // The degree line is "<Degree> — <Classification>"; splitting on the dash and
    // keeping [0] threw the classification away, so "First Class Honours (80+
    // average)" — the strongest single credential a recent graduate has — never
    // reached the PDF.
    const degreeMain = e.degree.replace(/\s*—\s*/g, ' — ').trim();
    const extra = sel
      ? e.extra.map(line => (/^key modules\s*:/i.test(line) ? `Key Modules: ${sel.join(', ')}` : line))
      : e.extra;
    // One row per line, not all of them joined with " • ". A CTF placing that
    // trails five module names behind a bullet separator reads as a footnote to
    // the module list; on its own row with its label in bold it reads as a result.
    const lines = extra.filter(x => x.trim());
    if (i === eduEntries.length - 1 && certLine) lines.push(certLine);
    return `    \\resumeSubheading
      {${tex(degreeMain)}}{${tex(e.period)}}
      {${tex(e.school)}}{}${
      itemList(lines.map(line => `        \\resumeItem{${labelled(line)}}`))}`;
  }).join('\n');
}

// Resolve which skill rows to render. Accepts either the new `skills` field
// (array of {category, items} with curated items) or the legacy
// `selected_skills` (array of category-name strings). For curated items we use
// the LLM's subset; when items are empty we fall back to the category's full CV
// items so a category name alone still renders correctly. Anything the LLM picks
// is matched back to a real CV category (so the canonical name is used and
// invented categories are dropped).
function resolveSkills(allRows, content, maxCount, cvText = '') {
  const skillsField = Array.isArray(content?.skills) ? content.skills : null;
  const legacyNames = Array.isArray(content?.selected_skills) ? content.selected_skills : null;

  let resolved = [];

  if (skillsField) {
    for (const s of skillsField) {
      const name = (typeof s === 'string' ? s : s?.category || '').trim();
      if (!name) continue;
      const cvRow = allRows.find(r => r.category.toLowerCase().includes(name.toLowerCase().split(' ')[0]));
      if (!cvRow) continue; // drop categories not in the CV
      const curated = (typeof s === 'object' && s?.items && s.items.trim())
        ? groundSkillItems(s.items.trim(), cvRow.items, cvText)
        : cvRow.items;
      if (!resolved.some(r => r.category === cvRow.category)) {
        resolved.push({ category: cvRow.category, items: curated });
      }
    }
  } else if (legacyNames) {
    resolved = legacyNames
      .map(name => allRows.find(r => r.category.toLowerCase().includes(name.toLowerCase().split(' ')[0])))
      .filter(Boolean);
  }

  // Languages is not the model's to drop. It omitted it from the Neo4j CV whose
  // first requirement was "strong TypeScript / JavaScript" — leaving the shipped
  // skills block with no language row at all, the one row every ATS scans.
  const langRow = allRows.find(r => /^languages/i.test(r.category));
  if (langRow && !resolved.some(r => r.category === langRow.category)) {
    resolved.unshift(langRow);
  }

  // Keep the model's curation as priority. When it under-delivers, PAD with the
  // remaining CV categories (verbatim) up to the target count — never dump all
  // categories, which is the "everything stuffed in" symptom we're fixing.
  const target = maxCount || 6;
  if (resolved.length === 0) {
    resolved = allRows.slice(0, target);
  } else if (resolved.length < Math.min(4, target)) {
    for (const r of allRows) {
      if (resolved.length >= target) break;
      if (!resolved.some(x => x.category === r.category)) resolved.push(r);
    }
  }
  return maxCount ? resolved.slice(0, maxCount) : resolved;
}

function buildSkillsTex(rows) {
  if (!rows.length) return '';
  const body = rows
    .map(r => `     \\textbf{${tex(r.category)}}{: ${tex(r.items)}}`)
    .join(' \\\\\n');
  return ` \\begin{itemize}[leftmargin=0.15in, label={}]\n    \\small{\\item{\n${body}\n    }}\n \\end{itemize}`;
}

// ── Self-check (ponytail: one runnable check) ─────────────────────────────────
//
// `node batch/fill-cv-template.mjs` with no arguments runs this instead of the
// usage error. It guards the two failures that render a *plausible* CV with the
// employers silently missing off it, which no page-count or compile check sees.

if (process.argv.length === 2) {
  const assert = (cond, msg) => { if (!cond) { console.error('FAIL:', msg); process.exit(1); } };

  for (const dash of ['—', '–', '-']) {
    const [e] = parseExperience([
      '### Teaching Assistant',
      `**Napier University** ${dash} Edinburgh, UK | Sep 2025 – Present`,
      '- Taught 800+ undergraduates',
    ]);
    assert(e.company === 'Napier University', `company parses with "${dash}"`);
    assert(e.location === 'Edinburgh, UK', `location parses with "${dash}"`);
    assert(e.period === 'Sep 2025 – Present', `period parses with "${dash}"`);
    assert(e.bullets.length === 1, `bullets survive with "${dash}"`);
  }

  // An entry that failed to parse must match nothing, or it takes the first
  // model entry's bullets and every employer renders the same ones.
  assert(matchCompany('Acme', 'Acme') === true, 'exact company matches');
  assert(matchCompany('Acme Ltd', 'Acme') === true, 'substring company matches');
  assert(matchCompany('Acme', '') === false, 'an empty CV company matches nothing');
  assert(matchCompany('', 'Acme') === false, 'an empty JSON company matches nothing');

  // LaTeX escaping, and ATS normalization running before it.
  assert(tex('100% & <C++>') === '100\\% \\& \\textless{}C++\\textgreater{}', 'specials escape');
  assert(tex('a — b') === 'a - b', 'em-dash normalised before escaping');
  assert(tex('CEH • 2025') === 'CEH \\textbar{} 2025', 'a bullet becomes an escaped pipe, not a bare one');
  assert(texBold('cut latency by 90% to 250ms') ===
    'cut latency by \\textbf{90\\%} to \\textbf{250ms}', 'metrics bold, and stay escaped');
  assert(texBold('shipped in 2025') === 'shipped in 2025', 'a bare year is not a metric');

  // An empty itemize is a LaTeX error, not an empty list.
  assert(itemList([]) === '', 'no items renders no list at all');
  assert(itemList(['  \\resumeItem{x}']).includes('\\resumeItemListStart'), 'items render a list');

  console.log('✓ fill-cv-template self-check passed');
  process.exit(0);
}

// ── Main ──────────────────────────────────────────────────────────────────────

requireArgs();

const cvText     = readFileSync(CV_PATH, 'utf8');
const profileRaw = readFileSync(PROFILE, 'utf8');
const template   = readFileSync(TEMPLATE, 'utf8');
const content    = JSON.parse(readFileSync(contentPath, 'utf8'));
const profile    = yaml.load(profileRaw);

const sections   = splitSections(cvText);
const cvExp      = parseExperience(sections['Experience'] || []);
const cvProjects = parseProjects(sections['Projects'] || []);
const cvEdu      = parseEducation(sections['Education'] || []);
const cvCerts    = parseCertifications(sections['Certifications'] || []);
const cvSkills   = parseSkills([...(sections['Skills'] || []), ...(sections['Languages'] || [])]);

const cand = profile.candidate || {};
const linkedin = cand.linkedin || '';
const github   = cand.github   || '';

// One token rather than ten. The header is a run of `$|$`-separated \href calls
// where every field is optional, and threading that through the template would
// need `\href{mailto:{{EMAIL}}}{\underline{{{EMAIL}}}}` — four levels of brace
// that break the moment a field is blank.
const contact = [
  cand.phone ? `\\href{tel:${texUrl(String(cand.phone).replace(/[^\d+]/g, ''))}}{${tex(cand.phone)}}` : '',
  cand.email ? `\\href{mailto:${texUrl(cand.email)}}{\\underline{${tex(cand.email)}}}` : '',
  linkedin ? `\\href{${texUrl(linkedin.startsWith('http') ? linkedin : `https://${linkedin}`)}}{\\underline{${tex(linkedin)}}}` : '',
  github ? `\\href{${texUrl(github.startsWith('http') ? github : `https://${github}`)}}{\\underline{${tex(github)}}}` : '',
  cand.location ? tex(cand.location) : '',
].filter(Boolean).join(' $|$ ');

const replacements = {
  '{{NAME}}':                  tex(cand.full_name || ''),
  '{{TARGET_ROLE}}':           targetRole ? `\\small{\\textit{${tex(targetRole)}}} \\\\ \\vspace{1pt}` : '',
  '{{CONTACT}}':               contact,
  '{{SECTION_EXPERIENCE}}':    'Work Experience',
  '{{EXPERIENCE}}':            buildExperienceTex(cvExp, content.experience, maxBullets),
  '{{SECTION_PROJECTS}}':      'Projects',
  '{{PROJECTS}}':              buildProjectsTex(cvProjects, content.projects || content.selected_projects, maxProjBullets),
  '{{SECTION_EDUCATION}}':     cvCerts.length ? 'Education \\& Certifications' : 'Education',
  '{{EDUCATION}}':             buildEducationTex(cvEdu, content.education_modules, cvCerts),
  '{{SECTION_SKILLS}}':        'Technical Skills',
  '{{SKILLS}}':                buildSkillsTex(resolveSkills(cvSkills, content, maxSkills, cvText)),
};

let out = template;
for (const [k, v] of Object.entries(replacements)) {
  out = out.replaceAll(k, v);
}

mkdirSync(dirname(outputPath), { recursive: true });
writeFileSync(outputPath, out, 'utf8');
console.log(`✓ source.tex written → ${outputPath}`);
