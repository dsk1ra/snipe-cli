#!/usr/bin/env node

/**
 * generate-pdf.mjs — .tex → PDF via pdflatex, .html → PDF via Playwright
 *
 * Usage:
 *   node generate-pdf.mjs <input.tex|input.html> <output.pdf> [--max-pages=N] [--source-url=URL]
 *
 * The CV renders from LaTeX (templates/cv-template.tex, Jake's Resume) and
 * needs pdflatex on PATH. The cover letter is still HTML and still needs
 * Playwright; the input extension picks the path.
 */

import { chromium } from 'playwright';
import { resolve, dirname, relative, isAbsolute, join } from 'path';
import { readFile } from 'fs/promises';
import { mkdirSync, writeFileSync, rmSync, readFileSync, mkdtempSync } from 'fs';
import { tmpdir } from 'os';
import { fileURLToPath, pathToFileURL } from 'url';
import { execFileSync } from 'child_process';
import { sanitizeAtsText } from './batch/text-utils.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));

// Ensure output directory exists (fresh setup)
mkdirSync(resolve(__dirname, 'output'), { recursive: true });

/**
 * Normalize text for ATS compatibility by converting problematic Unicode.
 *
 * ATS parsers and legacy systems often fail on em-dashes, smart quotes,
 * zero-width characters, and non-breaking spaces. These cause mojibake,
 * parsing errors, or display issues. See issue #1.
 *
 * Only touches body text — preserves CSS, JS, tag attributes, and URLs.
 * Returns { html, replacements } so the caller can log what was changed.
 */
function normalizeTextForATS(html) {
  const replacements = {};
  const bump = (key) => { replacements[key] = (replacements[key] || 0) + 1; };

  const masks = [];
  const masked = html.replace(
    /<(style|script)\b[^>]*>[\s\S]*?<\/\1>/gi,
    (match) => {
      const token = `\u0000MASK${masks.length}\u0000`;
      masks.push(match);
      return token;
    }
  );

  let out = '';
  let i = 0;
  while (i < masked.length) {
    const lt = masked.indexOf('<', i);
    if (lt === -1) { out += sanitizeAtsText(masked.slice(i), bump); break; }
    out += sanitizeAtsText(masked.slice(i, lt), bump);
    const gt = masked.indexOf('>', lt);
    if (gt === -1) { out += masked.slice(lt); break; }
    out += masked.slice(lt, gt + 1);
    i = gt + 1;
  }

  const restored = out.replace(/\u0000MASK(\d+)\u0000/g, (_, n) => masks[Number(n)]);
  return { html: restored, replacements };
}

const SECTION_ALIASES = new Map([
  ['summary', 'summary'],
  ['professional summary', 'summary'],
  ['competencies', 'competencies'],
  ['core competencies', 'competencies'],
  ['experience', 'experience'],
  ['work experience', 'experience'],
  ['professional experience', 'experience'],
  ['projects', 'projects'],
  ['selected projects', 'projects'],
  ['personal projects', 'projects'],
  ['education', 'education'],
  ['education & certifications', 'education'],
  ['certifications', 'certifications'],
  ['skills', 'skills'],
  ['technical skills', 'skills'],
]);

function normalizeSectionTitle(text) {
  return text
    .replace(/<[^>]+>/g, ' ')
    .replace(/\{\{[^}]+\}\}/g, ' ')
    .replace(/&amp;/g, '&')
    .replace(/[*_`~]/g, '')
    .replace(/\s+/g, ' ')
    .trim()
    .toLowerCase();
}

function sectionKey(text) {
  const normalized = normalizeSectionTitle(text);
  return SECTION_ALIASES.get(normalized) ?? normalized;
}

// `\section{Work Experience}` in the LaTeX template, `class="section-title"` in
// the HTML one. The check itself — rendered order must not contradict cv.md —
// is the same for both, and is the reason a template swap cannot silently
// reorder the document.
function extractRenderedSectionOrder(source, isTex = false) {
  const titleMatches = isTex
    ? [...source.matchAll(/^\s*\\section\{([^{}]*)\}/gm)]
    : [...source.matchAll(/class=["'][^"']*\bsection-title\b[^"']*["'][^>]*>([\s\S]*?)<\/[^>]+>/gi)];
  const sections = [];

  for (const match of titleMatches) {
    // \& is how "Education & Certifications" reaches the .tex.
    const text = normalizeSectionTitle(match[1].replace(/\\&/g, '&'));
    if (!text) continue;
    sections.push({ key: sectionKey(text), title: text });
  }

  return sections;
}

function extractSourceSectionOrder(markdown) {
  const sections = [];

  for (const line of markdown.split(/\r?\n/)) {
    const heading = line.match(/^\s{0,3}(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (!heading) continue;
    const text = normalizeSectionTitle(heading[2]);
    if (!text) continue;
    sections.push({ key: sectionKey(text), title: text });
  }

  return sections;
}

// Sections the template places itself, so cv.md's ordering does not bind them.
// Skills renders directly under the summary wherever cv.md happens to put it:
// that position is a measured layout decision, not a content one. It used to
// render dead last, below Certifications on page 2 — the worst spot on the
// document for the one section a keyword scan reads first. Everything else still
// has to follow cv.md, which is what this check exists for.
const TEMPLATE_ORDERED = new Set(['skills']);

function validateCvSectionOrder(doc, cvMarkdown, isTex = false) {
  const rendered = extractRenderedSectionOrder(doc, isTex);
  const source = extractSourceSectionOrder(cvMarkdown);
  if (rendered.length < 2 || source.length < 2) return;

  const sourcePositions = new Map(source.map((section, index) => [section.key, index]));
  const renderedComparable = rendered.filter(
    section => sourcePositions.has(section.key) && !TEMPLATE_ORDERED.has(section.key));
  if (renderedComparable.length < 2) return;

  for (let i = 1; i < renderedComparable.length; i++) {
    const previous = renderedComparable[i - 1];
    const current = renderedComparable[i];
    if (sourcePositions.get(current.key) < sourcePositions.get(previous.key)) {
      const renderedOrder = renderedComparable.map(section => section.title).join(' -> ');
      const sourceOrder = source
        .filter(section => renderedComparable.some(renderedSection => renderedSection.key === section.key))
        .map(section => section.title)
        .join(' -> ');
      throw new Error(`CV section order diverges from cv.md: rendered ${renderedOrder}; cv.md ${sourceOrder}`);
    }
  }
}

async function generatePDF() {
  const args = process.argv.slice(2);

  // Parse arguments
  let inputPath, outputPath, maxPages = null, sourceUrl = null;

  for (const arg of args) {
    if (arg.startsWith('--max-pages=')) {
      maxPages = parseInt(arg.split('=')[1], 10);
    } else if (arg.startsWith('--source-url=')) {
      sourceUrl = arg.slice('--source-url='.length);
    } else if (!inputPath) {
      inputPath = arg;
    } else if (!outputPath) {
      outputPath = arg;
    }
  }

  if (!inputPath || !outputPath) {
    console.error('Usage: node generate-pdf.mjs <input.tex|input.html> <output.pdf> [--max-pages=N] [--source-url=URL]');
    process.exit(1);
  }

  inputPath = resolve(inputPath);
  outputPath = resolve(outputPath);

  console.log(`Input:  ${inputPath}`);
  console.log(`Output: ${outputPath}`);

  const isTex = inputPath.toLowerCase().endsWith('.tex');
  let source = await readFile(inputPath, 'utf-8');
  let cvMarkdown = '';
  try {
    cvMarkdown = await readFile(resolve(__dirname, 'cv.md'), 'utf-8');
  } catch (err) {
    if (err?.code !== 'ENOENT') throw err;
  }
  validateCvSectionOrder(source, cvMarkdown, isTex);

  // Normalize text for ATS compatibility (issue #1). The LaTeX path already
  // did this in fill-cv-template.mjs, on the content rather than the document —
  // it has to run before escaping, or the pipes this emits reach TeX bare.
  if (!isTex) {
    const normalized = normalizeTextForATS(source);
    source = normalized.html;
    const totalReplacements = Object.values(normalized.replacements).reduce((a, b) => a + b, 0);
    if (totalReplacements > 0) {
      const breakdown = Object.entries(normalized.replacements).map(([k, v]) => `${k}=${v}`).join(', ');
      console.log(`ATS normalization: ${totalReplacements} replacements (${breakdown})`);
    }
  }

  const result = isTex
    ? renderTexToPdf(source, outputPath)
    : await renderHtmlToPdf(source, outputPath, { baseDir: dirname(inputPath) });

  // Embed the job-posting URL into PDF metadata (Subject + Keywords) so the
  // source posting is visible in any PDF reader's document properties.
  if (sourceUrl) {
    try {
      execFileSync('exiftool', [
        '-overwrite_original',
        `-Subject=Job posting: ${sourceUrl}`,
        `-Keywords=${sourceUrl}`,
        outputPath,
      ], { stdio: 'pipe' });
      console.log(`Source URL embedded in metadata: ${sourceUrl}`);
    } catch (err) {
      console.warn(`WARN: Could not write source-url metadata via exiftool: ${err.message}`);
    }
  }

  // Hard page-count limit. The file is kept so its layout can be inspected,
  // but a non-zero exit signals the caller to trim content and regenerate.
  if (maxPages && Number.isFinite(maxPages) && result.pageCount > maxPages) {
    console.error(`ERROR: PDF is ${result.pageCount} pages — exceeds the ${maxPages}-page hard limit. Trim content and regenerate.`);
    process.exit(2);
  }

  return result;
}

/**
 * Render a LaTeX string to a PDF file via pdflatex.
 *
 * One pass, not two: the template sets `bookmarks=false`, so hyperref writes no
 * .out file and has nothing to ask for a rerun about. Nothing else in the
 * document cross-references, and the density ladder compiles this up to six
 * times per offer.
 *
 * pdflatex insists on writing .aux/.log next to its output, so it renders into
 * a scratch directory and the PDF is moved into place. `-output-directory`
 * rather than a chdir, because the ladder runs several of these and cwd is
 * process-global.
 *
 * @param {string} tex - Full LaTeX document.
 * @param {string} outputPath - Absolute path to write the PDF to.
 * @param {{keepLog?: boolean}} [opts]
 * @returns {{outputPath: string, pageCount: number, pages: number|null, size: number}}
 */
export function renderTexToPdf(tex, outputPath, opts = {}) {
  mkdirSync(dirname(outputPath), { recursive: true });
  const scratch = mkdtempSync(join(tmpdir(), 'snipe-tex-'));
  const texPath = join(scratch, 'cv.tex');

  try {
    writeFileSync(texPath, tex);
    let log = '';
    try {
      log = execFileSync('pdflatex', [
        '-interaction=nonstopmode',
        '-halt-on-error',
        `-output-directory=${scratch}`,
        texPath,
      ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (err) {
      if (err?.code === 'ENOENT') {
        throw new Error('pdflatex not found on PATH — install TeX Live (texlive-latex-recommended, texlive-fonts-recommended)');
      }
      // nonstopmode puts the real complaint in stdout, prefixed "! ".
      const out = String(err.stdout || '');
      const why = (out.match(/^!.*$/gm) || []).slice(0, 3).join('; ') || err.message;
      throw new Error(`pdflatex failed: ${why}`);
    }

    // The page-count regex the HTML path uses reads /Type /Page out of the raw
    // bytes; pdflatex compresses those into object streams, so it finds zero on
    // every document. pdflatex states the count itself, so read that instead of
    // adding a poppler dependency to get it back.
    const m = log.match(/Output written on .*?\((\d+) pages?/);
    if (!m) throw new Error(`pdflatex wrote no page count — ${(log.match(/^!.*$/gm) || []).slice(0, 2).join('; ') || 'no output'}`);
    const pageCount = parseInt(m[1], 10);

    // Fractional page count, from the template's \AtEndDocument hook: whole
    // pages, plus how full the last one is. Null if the hook is absent (a .tex
    // that did not come from templates/cv-template.tex).
    const f = log.match(/SNIPE-FILL ([\d.]+)pt ([\d.]+)pt/);
    const pages = f && +f[2] ? +((pageCount - 1 + +f[1] / +f[2]).toFixed(3)) : null;

    const pdf = readFileSync(join(scratch, 'cv.pdf'));
    writeFileSync(outputPath, pdf);

    // stderr, not stdout: tailor-harness.mjs calls this per offer and prints its
    // metrics as JSON on stdout, so a "PDF generated" line here is parsed as
    // part of the report. The CLI still shows it.
    process.stderr.write(`PDF generated: ${outputPath}\n`);
    process.stderr.write(`Pages: ${pageCount}${pages ? ` (${pages} incl. last-page fill)` : ''}\n`);
    process.stderr.write(`Size: ${(pdf.length / 1024).toFixed(1)} KB\n`);

    return { outputPath, pageCount, pages, size: pdf.length };
  } finally {
    if (!opts.keepLog) rmSync(scratch, { recursive: true, force: true });
  }
}

/**
 * Inline url('./fonts/...') references as base64 data: URLs.
 *
 * Chromium refuses to load file:// subresources from a setContent() page
 * (the document stays at about:blank), so fonts referenced by path are
 * silently dropped and PDFs fall back to system fonts. data: URLs carry
 * no origin restriction, so they load from any page. See #951.
 *
 * Missing font files keep their original reference and log a warning.
 *
 * @param {string} html - HTML that may reference url('./fonts/<file>').
 * @returns {Promise<string>} HTML with local font references inlined.
 */
export async function inlineLocalFonts(html) {
  const FONT_REF = /url\(\s*(['"]?)\.\/fonts\/([^'")\s]+)\1\s*\)/g;
  const MIME = { woff2: 'font/woff2', woff: 'font/woff', otf: 'font/otf', ttf: 'font/ttf' };
  const fontsDir = resolve(__dirname, 'fonts');
  const names = [...new Set([...html.matchAll(FONT_REF)].map((m) => m[2]))];
  const dataUrls = new Map();
  for (const name of names) {
    // Containment check: ".." segments and absolute names (./fonts//etc/passwd)
    // would otherwise resolve outside fonts/.
    const fontPath = resolve(fontsDir, name);
    const rel = relative(fontsDir, fontPath);
    if (rel.startsWith('..') || isAbsolute(rel)) {
      console.warn(`WARN: Font reference escapes fonts/, keeping original reference: ${name}`);
      continue;
    }
    try {
      const buf = await readFile(fontPath);
      const ext = name.slice(name.lastIndexOf('.') + 1).toLowerCase();
      dataUrls.set(name, `url('data:${MIME[ext] || 'application/octet-stream'};base64,${buf.toString('base64')}')`);
    } catch (err) {
      if (err?.code !== 'ENOENT') throw err;
      console.warn(`WARN: Font file not found, keeping original reference: fonts/${name}`);
    }
  }
  return html.replace(FONT_REF, (match, _quote, name) => dataUrls.get(name) || match);
}

/**
 * Print margin, and the A4 content box it leaves, at 96 CSS px per inch.
 *
 * 0.45in rather than the 0.6in this used to be. Measured: the content box goes
 * 679x1007 -> 708x1036 px. The extra width matters as much as the height, since
 * a wider column wraps fewer bullets onto a third line. Well inside any
 * printer's unprintable margin, and the PDF is read on screen anyway. Do not go
 * below 0.4in; that starts to look cramped.
 *
 * Exported because `tailor-harness.mjs` measures rendered CVs against exactly
 * this box. Two copies of the geometry would let a margin change here silently
 * invalidate every page measurement there.
 */
export const MARGIN_IN = 0.45;
export const CONTENT_BOX = {
  width:  Math.round((8.27  - 2 * MARGIN_IN) * 96),   // 708
  height: Math.round((11.69 - 2 * MARGIN_IN) * 96),   // 1036
};

/**
 * Render an HTML string to a PDF file via headless Chromium.
 *
 * Local url('./fonts/...') references are inlined as data: URLs first so
 * fonts render regardless of page origin (see inlineLocalFonts).
 *
 * Always A4. US Letter used to be selectable, chosen by a regex over the JD that
 * matched the pronoun in "join us" and so put UK postings on the wrong paper. The
 * option was not worth keeping: Letter is 66px shorter, which costs about four
 * bullet lines on a CV fighting to stay on one page, and no ATS or recruiter
 * screens on a 6mm width difference.
 *
 * @param {string} html - Full HTML document to render.
 * @param {string} outputPath - Absolute path to write the PDF to.
 * @param {{baseDir?: string}} [opts]
 * @returns {Promise<{outputPath: string, pageCount: number, size: number}>}
 */
export async function renderHtmlToPdf(html, outputPath, opts = {}) {
  const baseDir = opts.baseDir || process.cwd();

  mkdirSync(dirname(outputPath), { recursive: true });

  html = await inlineLocalFonts(html);

  const browser = await chromium.launch({ headless: true });
  // Relative resources (a logo, an image) only resolve if the document is served
  // from baseDir. setContent() loads it as about:blank, and Chromium then refuses
  // every file:// subresource ("Not allowed to load local resource") — a <base>
  // tag sets document.baseURI but does not lift that block, and setContent()
  // silently ignores the `baseURL` option, which is for navigation only. Writing
  // the document into baseDir and navigating to it is what actually works.
  const scratch = join(baseDir, `.snipe-render-${process.pid}-${Date.now()}.html`);
  let scratchWritten = false;
  try {
    const page = await browser.newPage();

    try {
      writeFileSync(scratch, html);
      scratchWritten = true;
    } catch (err) {
      // Read-only baseDir: fall back to setContent. Fonts are already inlined,
      // so a self-contained document still renders correctly.
      console.warn(`WARN: Cannot stage HTML in ${baseDir} (${err?.code || err?.message}); relative resources will not resolve`);
    }
    if (scratchWritten) await page.goto(pathToFileURL(scratch).href, { waitUntil: 'load' });
    else await page.setContent(html, { waitUntil: 'load' });

    // Wait for fonts to load
    await page.evaluate(() => document.fonts.ready);

    // Generate PDF
    const pdfBuffer = await page.pdf({
      format: 'a4',
      printBackground: true,
      margin: {
        top: `${MARGIN_IN}in`,
        right: `${MARGIN_IN}in`,
        bottom: `${MARGIN_IN}in`,
        left: `${MARGIN_IN}in`,
      },
      preferCSSPageSize: false,
    });

    // Write PDF
    const { writeFile } = await import('fs/promises');
    await writeFile(outputPath, pdfBuffer);

    // Count pages (approximate from PDF structure)
    const pdfString = pdfBuffer.toString('latin1');
    const pageCount = (pdfString.match(/\/Type\s*\/Page[^s]/g) || []).length;

    console.log(`PDF generated: ${outputPath}`);
    console.log(`Pages: ${pageCount}`);
    console.log(`Size: ${(pdfBuffer.length / 1024).toFixed(1)} KB`);

    return { outputPath, pageCount, size: pdfBuffer.length };
  } finally {
    if (scratchWritten) rmSync(scratch, { force: true });
    await browser.close();
  }
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href;
if (isMain) {
  generatePDF().catch((err) => {
    console.error('ERROR: PDF generation failed:', err.message);
    process.exit(1);
  });
}

export { normalizeTextForATS };
