#!/usr/bin/env node
/**
 * fill-cv-template.mjs — Merges Ollama JSON content + cv.md into source.html
 *
 * Usage:
 *   node batch/fill-cv-template.mjs \
 *     --content /tmp/cv-content-42.json \
 *     --output output/2026-01-01_company_042/source.html \
 *     --format a4|letter
 */

import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import yaml from 'js-yaml';

const __dirname  = dirname(fileURLToPath(import.meta.url));
const PROJECT    = resolve(__dirname, '..');
const CV_PATH    = resolve(PROJECT, 'cv.md');
const PROFILE    = resolve(PROJECT, 'config/profile.yml');
const TEMPLATE   = resolve(PROJECT, 'templates/cv-template.html');

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
const format      = (args['format'] || 'a4').toLowerCase();
const maxSkills   = args['max-skills'] ? parseInt(args['max-skills'], 10) : null; // null = all
const maxBullets  = args['max-bullets'] ? parseInt(args['max-bullets'], 10) : null; // null = all; caps bullets per role

if (!contentPath || !outputPath) {
  console.error('Usage: fill-cv-template.mjs --content <json> --output <html> [--format a4|letter] [--max-skills N]');
  process.exit(1);
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function esc(s) {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// Wrap measurable metrics in <strong>. MUST run on already-esc()'d text so the
// injected <strong> tags are intentional HTML (not re-escaped). Targets counts,
// percentages, latencies, currency, units (k/M/ms/x/GB…) and durations; leaves
// bare years (2025), version numbers (2.0) and "AES-256" untouched since they
// lack a separator/sign/unit.
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

function boldMetrics(escaped) {
  return String(escaped).replace(METRIC_RE, m => `<strong>${m}</strong>`);
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
  const j = jsonCompany.toLowerCase().trim();
  const c = cvCompany.toLowerCase().trim();
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
    if (/^\*\*[^*]+\*\*\s*(?:—|–)/.test(s)) {
      const m = s.match(/^\*\*([^*]+)\*\*\s*(?:—|–)\s*([^|]+)\|\s*(.+)$/);
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

// ── HTML builders ─────────────────────────────────────────────────────────────

function buildCompetencies(list) {
  return list.map(c => `<span class="competency-tag">${esc(c)}</span>`).join('\n      ');
}

// UK CV convention: ALL companies, reverse-chronological, never reordered or
// silently dropped by the model — the CV itself is the source of truth for
// the company list and order; the model only supplies (or fails to supply,
// in which case we fall back to the CV's own) bullets per company.
function buildExperienceHtml(cvExp, jsonExp, maxBullets) {
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
    return `
    <div class="job">
      <div class="job-header">
        <span class="job-company">${esc(e.company)}</span>
        <span class="job-period">${esc(e.period)}</span>
      </div>
      <div class="job-role">${esc(e.role)}</div>
      <div class="job-location">${esc(e.location)}</div>
      <ul>
        ${bullets.map(b => `<li>${boldMetrics(esc(b))}</li>`).join('\n        ')}
      </ul>
    </div>`;
  }).join('\n');
}

// Accepts the new `projects` field (array of {name, description}) OR the legacy
// `selected_projects` (array of name strings). When the LLM supplies a tailored
// description we use it; otherwise we fall back to the project's first 2 CV
// bullets (never the full dump) so descriptions stay tight.
function buildProjectsHtml(cvProjects, projectsField) {
  const sel = Array.isArray(projectsField)
    ? projectsField
        .map(p => (typeof p === 'string' ? { name: p, description: '' } : { name: p?.name || '', description: p?.description || '' }))
        .filter(p => p.name)
    : [];

  let chosen;
  if (sel.length > 0) {
    chosen = [];
    for (const s of sel) {
      const match = cvProjects.find(p => matchProject(s.name, p.name));
      if (match && !chosen.some(c => c.cv === match)) chosen.push({ cv: match, description: s.description });
    }
  } else {
    chosen = cvProjects.slice(0, 4).map(cv => ({ cv, description: '' }));
  }

  return chosen.map(({ cv, description }) => {
    const desc = description && description.trim()
      ? description.trim()
      : cv.bullets.slice(0, 2).join(' ');
    const techLine = [cv.tech, cv.period].filter(Boolean).join(' | ');
    return `
    <div class="project">
      <div>
        <span class="project-title">${esc(cv.name)}</span>${cv.badge ? `\n        <span class="project-badge">${esc(cv.badge)}</span>` : ''}
      </div>
      <div class="project-desc">${boldMetrics(esc(desc))}</div>
      ${techLine ? `<div class="project-tech">${esc(techLine)}</div>` : ''}
    </div>`;
  }).join('\n');
}

// When the LLM supplies `education_modules` (a JD-relevant subset), rewrite the
// "Key Modules:" line to those modules only; other extra lines (Achievement,
// etc.) are kept as-is. Without the field, the CV's modules pass through verbatim.
function buildEducationHtml(eduEntries, selectedModules) {
  const sel = Array.isArray(selectedModules) && selectedModules.length > 0 ? selectedModules : null;
  return eduEntries.map(e => {
    const degreeMain = e.degree.split('—')[0].trim();
    const extra = sel
      ? e.extra.map(line => (/^key modules\s*:/i.test(line) ? `Key Modules: ${sel.join(', ')}` : line))
      : e.extra;
    const extraText = extra.filter(x => x.trim()).join(' • ');
    return `
    <div class="edu-item">
      <div class="edu-header">
        <span><span class="edu-title">${esc(degreeMain)}</span> <span class="edu-org">${esc(e.school)}</span></span>
        <span class="edu-year">${esc(e.period)}</span>
      </div>
      ${extraText ? `<div class="edu-desc">${esc(extraText)}</div>` : ''}
    </div>`;
  }).join('\n');
}

function buildCertsHtml(certs) {
  return certs.map(c => `
    <div class="cert-item">
      <div class="cert-title">${esc(c.name)}${c.org ? ` <span class="cert-org">— ${esc(c.org)}</span>` : ''}</div>
      <div class="cert-year">${esc(c.year)}</div>
    </div>`).join('\n');
}

// Resolve which skill rows to render. Accepts either the new `skills` field
// (array of {category, items} with curated items) or the legacy
// `selected_skills` (array of category-name strings). For curated items we use
// the LLM's subset; when items are empty we fall back to the category's full CV
// items so a category name alone still renders correctly. Anything the LLM picks
// is matched back to a real CV category (so the canonical name is used and
// invented categories are dropped).
function resolveSkills(allRows, content, maxCount) {
  const skillsField = Array.isArray(content?.skills) ? content.skills : null;
  const legacyNames = Array.isArray(content?.selected_skills) ? content.selected_skills : null;

  let resolved = [];

  if (skillsField) {
    for (const s of skillsField) {
      const name = (typeof s === 'string' ? s : s?.category || '').trim();
      if (!name) continue;
      const cvRow = allRows.find(r => r.category.toLowerCase().includes(name.toLowerCase().split(' ')[0]));
      if (!cvRow) continue; // drop categories not in the CV
      const curated = (typeof s === 'object' && s?.items && s.items.trim()) ? s.items.trim() : cvRow.items;
      if (!resolved.some(r => r.category === cvRow.category)) {
        resolved.push({ category: cvRow.category, items: curated });
      }
    }
  } else if (legacyNames) {
    resolved = legacyNames
      .map(name => allRows.find(r => r.category.toLowerCase().includes(name.toLowerCase().split(' ')[0])))
      .filter(Boolean);
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

function buildSkillsHtml(rows) {
  return `<div class="skills-grid">\n` +
    rows.map(r => `      <span class="skill-item"><span class="skill-category">${esc(r.category)}:</span> ${esc(r.items)}</span>`).join('\n') +
    `\n    </div>`;
}

// ── Main ──────────────────────────────────────────────────────────────────────

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

const pageWidth = format === 'letter' ? '816px' : '794px';

const replacements = {
  '{{LANG}}':                  'en',
  '{{PAGE_WIDTH}}':            pageWidth,
  '{{NAME}}':                  esc(cand.full_name || ''),
  '{{PHONE}}':                 esc(cand.phone || ''),
  '{{EMAIL}}':                 esc(cand.email || ''),
  '{{LINKEDIN_URL}}':          linkedin.startsWith('http') ? linkedin : `https://${linkedin}`,
  '{{LINKEDIN_DISPLAY}}':      esc(linkedin),
  '{{PORTFOLIO_URL}}':         github.startsWith('http') ? github : `https://${github}`,
  '{{PORTFOLIO_DISPLAY}}':     esc(github),
  '{{LOCATION}}':              esc(cand.location || ''),
  '{{SECTION_SUMMARY}}':       'Professional Summary',
  '{{SUMMARY_TEXT}}':          boldMetrics(esc(content.summary || '')),
  '{{SECTION_COMPETENCIES}}':  'Core Competencies',
  '{{COMPETENCIES}}':          buildCompetencies(content.competencies || []),
  '{{SECTION_EXPERIENCE}}':    'Work Experience',
  '{{EXPERIENCE}}':            buildExperienceHtml(cvExp, content.experience, maxBullets),
  '{{SECTION_PROJECTS}}':      'Projects',
  '{{PROJECTS}}':              buildProjectsHtml(cvProjects, content.projects || content.selected_projects),
  '{{SECTION_EDUCATION}}':     'Education',
  '{{EDUCATION}}':             buildEducationHtml(cvEdu, content.education_modules),
  '{{SECTION_CERTIFICATIONS}}':'Certifications',
  '{{CERTIFICATIONS}}':        buildCertsHtml(cvCerts),
  '{{SECTION_SKILLS}}':        'Skills',
  '{{SKILLS}}':                buildSkillsHtml(resolveSkills(cvSkills, content, maxSkills)),
};

let html = template;
for (const [k, v] of Object.entries(replacements)) {
  html = html.replaceAll(k, v);
}

mkdirSync(dirname(outputPath), { recursive: true });
writeFileSync(outputPath, html, 'utf8');
console.log(`✓ source.html written → ${outputPath}`);
