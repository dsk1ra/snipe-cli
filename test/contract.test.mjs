// Auto-composed from the former test-all.mjs monolith. Imports the shared
// harness (counters + reporters + re-exported node builtins); assertions run at
// import time. Run standalone with: node test/<name>.test.mjs
import {
  pass, fail, warn, run, fileExists, readFile, ROOT, NODE,
  execSync, execFileSync, spawn,
  readFileSync, existsSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, rmSync,
  join, dirname, tmpdir, fileURLToPath, pathToFileURL,
} from './harness.mjs';

// ── 4. DATA CONTRACT ────────────────────────────────────────────

console.log('\n4. Data contract validation');

// Check system files exist
const systemFiles = [
  'CLAUDE.md',
  'modes/_shared.md', 'config/profile.template.md',
  'modes/pdf.md', 'modes/scan.md',
  'templates/states.yml', 'templates/cv-template.tex',
];

for (const f of systemFiles) {
  if (fileExists(f)) {
    pass(`System file exists: ${f}`);
  } else {
    fail(`Missing system file: ${f}`);
  }
}

// Check user files are NOT tracked (gitignored)
const userFiles = [
  'config/profile.yml', 'config/profile.md', 'portals.yml',
];
for (const f of userFiles) {
  const tracked = run('git', ['ls-files', f]);
  if (tracked === '') {
    pass(`User file gitignored: ${f}`);
  } else if (tracked === null) {
    pass(`User file gitignored: ${f}`);
  } else {
    fail(`User file IS tracked (should be gitignored): ${f}`);
  }
}

// Batch-runner tests removed — the cloud batch runner (batch-runner.sh) was
// deleted; this project runs the fully local pipeline (batch/local-runner.sh).

// ── 6. PERSONAL DATA LEAK CHECK ─────────────────────────────────

console.log('\n6. Personal data leak check');

const leakPatterns = [
  'John', 'linkedin.com/in/john', 'hi@john', '0712345678', '/Users/john',
];

const scanExtensions = ['md', 'yml', 'html', 'mjs', 'sh', 'json'];
const allowedFiles = ['package.json', 'CLAUDE.md', 'test-all.mjs'];

// Build pathspec for git grep — only scan tracked files matching these
// extensions. This is what `grep -rn` was trying to do, but git-aware:
// untracked files (debate artifacts, AI tool scratch, local plans/) and
// gitignored files can't trigger false positives because they were never
// going to reach a commit anyway.
const grepPathspec = scanExtensions.map(e => `'*.${e}'`).join(' ');

let leakFound = false;
for (const pattern of leakPatterns) {
  const result = run(
    `git grep -n "${pattern}" -- ${grepPathspec} 2>/dev/null`
  );
  if (result) {
    for (const line of result.split('\n')) {
      const file = line.split(':')[0];
      if (allowedFiles.some(a => file.includes(a))) continue;
      warn(`Possible personal data in ${file}: "${pattern}"`);
      leakFound = true;
    }
  }
}
if (!leakFound) {
  pass('No personal data leaks outside allowed files');
}

// ── 7. ABSOLUTE PATH CHECK ──────────────────────────────────────

console.log('\n7. Absolute path check');

// Same git grep approach: only scans tracked files. Untracked AI tool
// outputs, local debate artifacts, etc. can't false-positive here.
const absPathResult = run(
  `git grep -n "/Users/" -- '*.mjs' '*.sh' '*.md' '*.go' '*.yml' 2>/dev/null | grep -v README.md | grep -v LICENSE | grep -v CLAUDE.md | grep -v test-all.mjs | grep -v 'test/'`
);
if (!absPathResult) {
  pass('No absolute paths in code files');
} else {
  for (const line of absPathResult.split('\n').filter(Boolean)) {
    fail(`Absolute path: ${line.slice(0, 100)}`);
  }
}

// ── 7b. PDF RENDER WAIT CONDITION ───────────────────────────────

console.log('\n7b. PDF render wait condition');

const generatePdfScript = readFile('generate-pdf.mjs');
if (/waitUntil:\s*['"]load['"]/.test(generatePdfScript)) {
  pass('generate-pdf waits for load before rendering');
} else {
  fail('generate-pdf does not wait for load before rendering');
}
if (!/waitUntil:\s*['"]networkidle['"]/.test(generatePdfScript)) {
  pass('generate-pdf does not wait for networkidle');
} else {
  fail('generate-pdf still waits for networkidle');
}

// ── 8. MODE FILE INTEGRITY ──────────────────────────────────────

console.log('\n8. Mode file integrity');

const expectedModes = [
  '_shared.md', 'pdf.md', 'scan.md', 'deep.md',
  'pipeline.md', 'tracker.md', 'interview.md',
];

for (const mode of expectedModes) {
  if (fileExists(`modes/${mode}`)) {
    pass(`Mode exists: ${mode}`);
  } else {
    fail(`Missing mode: ${mode}`);
  }
}

// Check _shared.md references config/profile.md
const shared = readFile('modes/_shared.md');
if (shared.includes('config/profile.md')) {
  pass('_shared.md references config/profile.md');
} else {
  fail('_shared.md does NOT reference config/profile.md');
}


// Stage-3 grammar ceiling. llama.cpp expands `maxLength: N` into N nested
// optional char rules and stops compiling between 1800 and 2000 — the call then
// fails with HTTP 400 "failed to parse grammar", killing the whole evaluation.
// Offline guard, so it runs in CI without Ollama.
const staged = readFile('batch/staged-evaluator.mjs');
const overCap = [...staged.matchAll(/maxLength:\s*(\d+)/g)]
  .map(m => Number(m[1]))
  .filter(n => n > 1500);
if (overCap.length === 0) {
  pass('staged-evaluator maxLength caps stay under the grammar ceiling');
} else {
  fail(`staged-evaluator has maxLength ${overCap.join(', ')} — over the 1500 ceiling, grammar will not compile`);
}

// pdflatex's default output has no /ToUnicode map for its Type1 fonts, so the
// text layer extracts as mojibake and an ATS reads nothing. These two lines are
// the entire fix and the reason the template keeps them.
const cvTemplate = readFile('templates/cv-template.tex');
for (const [re, what] of [
  [/\\input\{glyphtounicode\}/, 'loads glyphtounicode'],
  [/\\pdfgentounicode=1/, 'sets pdfgentounicode'],
]) {
  if (re.test(cvTemplate)) pass(`cv-template.tex ${what} (ATS-parseable text layer)`);
  else fail(`cv-template.tex ${what.replace(/^/, 'does not ')} — the PDF's text extracts as mojibake`);
}

// The filler is a blind replaceAll over the template, so a token name written
// inside a comment is substituted into that comment. The first render did
// exactly this and put the whole experience section in the preamble, which
// failed as "Missing \begin{document}" a hundred lines away from the cause.
const tokenInComment = cvTemplate.split('\n')
  .filter(l => /^\s*%/.test(l) && /\{\{[A-Z_]+\}\}/.test(l));
if (tokenInComment.length === 0) {
  pass('cv-template.tex names no placeholder token in a comment');
} else {
  fail(`cv-template.tex has a token in a comment (${tokenInComment.length} line(s)) — replaceAll will substitute into it`);
}

// A project title comes from cv.md, which the template does not control, and a
// tabular* column does not wrap: the longest one ran 89pt into the margin as a
// mere log warning, so the ladder saw a clean one-page compile and shipped it.
if (/\\newcommand\{\\resumeProjectHeading\}[\s\S]{0,400}?\\begin\{tabularx\}/.test(cvTemplate)) {
  pass('cv-template.tex wraps the project heading (tabularx), so a long title cannot overflow silently');
} else {
  fail('cv-template.tex project heading does not wrap — a long cv.md title overflows the margin with no error');
}

// Every contact detail and repo the CV prints must be clickable in the PDF.
// hyperref turns \href into a link annotation and plain text into nothing, so a
// URL rendered as text is one a reviewer has to retype. Built in code rather
// than the template, because every field is optional and the brace nesting
// (\href{mailto:{{EMAIL}}}{\underline{{{EMAIL}}}}) does not survive a blank one.
const fillSrc = readFile('batch/fill-cv-template.mjs');
const contactLinks = [
  [/\\\\href\{tel:/, 'phone is a tel: link'],
  [/\\\\href\{mailto:/, 'email is a mailto: link'],
  [/linkedin\.startsWith\('http'\)/, 'LinkedIn is a link'],
  [/github\.startsWith\('http'\)/, 'GitHub profile is a link'],
];
for (const [re, what] of contactLinks) {
  if (re.test(fillSrc)) pass(`fill-cv-template.mjs: ${what}`);
  else fail(`fill-cv-template.mjs: ${what} — renders as plain text, so the PDF carries no annotation`);
}

// The repo link rides the project tech line. tex() around the whole line would
// escape the backslash of \href into visible markup, so this pins the shape.
if (/techLine = \[cv\.tech \? tex\(cv\.tech\) : '', repoTex\]/.test(fillSrc)) {
  pass('fill-cv-template.mjs escapes the tech text but not the \\href, so the repo link survives');
} else {
  fail('fill-cv-template.mjs escapes the whole tech line — the \\href would print as literal markup');
}
if (/`https:\/\/\$\{s\}`/.test(fillSrc)) {
  pass('fill-cv-template.mjs makes bare repo URLs absolute');
} else {
  fail('fill-cv-template.mjs does not absolutise repo URLs — a bare href points nowhere');
}

// LaTeX escaping is not optional: cv.md carries %, &, _, # and C++ in ordinary
// prose, and an unescaped one is a compile error or a silently wrong glyph.
if (/sanitizeAtsText\(String\(s \?\? ''\)\)\.replace\(\/\[/.test(fillSrc)) {
  pass('fill-cv-template.mjs normalizes for ATS before escaping for LaTeX');
} else {
  fail('fill-cv-template.mjs escapes without normalizing first — a • becomes a bare pipe, which T1 sets as an em-dash');
}

// ── 11. CLAUDE.md INTEGRITY ─────────────────────────────────────

console.log('\n11. CLAUDE.md integrity');

// The instructions are split by directory so a session only loads what its work
// needs, so each section is asserted against the file that owns it. Checking
// them all against the root file would pass on a stray mention in its pointer
// table while the real section had been deleted.
const requiredSections = [
  ['CLAUDE.md', 'Data contract'],
  ['CLAUDE.md', 'Ethics'],
  ['CLAUDE.md', 'Conventions'],
  ['batch/CLAUDE.md', '3-phase pipeline'],
  ['batch/CLAUDE.md', 'Benchmark rules'],
  ['tracker/CLAUDE.md', 'Tracker rules'],
  ['tracker/CLAUDE.md', 'TSV format'],
  ['tracker/CLAUDE.md', 'canonical'],
  ['test/CLAUDE.md', 'fake-ollama'],
];

for (const [file, section] of requiredSections) {
  if (fileExists(file) && readFile(file).includes(section)) {
    pass(`${file} has section: ${section}`);
  } else {
    fail(`${file} missing section: ${section}`);
  }
}
