#!/usr/bin/env node
// Row-level quality audit. Offer-level rho cannot resolve anything at n=18 (label
// -noise band 0.297), but each run grades ~200 requirement rows and writes ~90
// STAR stories, and two failure classes there are OBJECTIVELY wrong — no human
// label needed:
//
//   mis-grounded : row graded Strong/Transferable, the requirement names tech X,
//                  and the picked evidence atom does not contain X. The report
//                  visibly cites evidence that does not support the claim.
//   fabricated   : a STAR story or hard-question answer names a technology that
//                  does not appear in cv.md at all.
//
// Both are counted with examples printed, because the tech list is a heuristic and
// the counts are only trustworthy if the hits can be eyeballed.
import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { dirname, resolve as _resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
// Repo root, resolved from this file so the tools work from any cwd and survive
// being moved. Labels live in batch/labels/ which is gitignored — the tooling is
// tracked, the personal data is not.
const ROOT = _resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const LABELS = _resolve(ROOT, 'batch/labels/labels-rev.tsv');


const cv = readFileSync(_resolve(ROOT, 'cv.md'), 'utf8');
const TECH = [
  'Spring Boot', 'Springboot', 'Next.js', 'Node.js', 'React Native', 'React', 'Angular', 'Vue',
  'Django', 'FastAPI', 'Flask', 'Express', 'Axum', 'Flutter', 'Dart', 'JSP', 'Servlet', 'JSF',
  'Struts', 'PostgreSQL', 'MySQL', 'MariaDB', 'MongoDB', 'Redis', 'Valkey', 'SQLite', 'Cassandra',
  'DynamoDB', 'Elasticsearch', 'Kafka', 'RabbitMQ', 'GraphQL', 'gRPC', 'Hibernate', 'JPA',
  'AWS', 'Azure', 'GCP', 'Lambda', 'EC2', 'S3', 'Docker', 'Kubernetes', 'Terraform', 'Ansible',
  'Linux', 'Jenkins', 'GitHub Actions', 'GitLab', 'Prometheus', 'Grafana', 'Zipkin', 'Datadog',
  'Resilience4j', 'Okta', 'Stripe', 'Selenium', 'Jest', 'JUnit', 'Pytest', 'Cypress', 'Playwright',
  'PySpark', 'Spark', 'Databricks', 'Delta Lake', 'Iceberg', 'Unity Catalog', 'Airflow', 'dbt',
  'Snowflake', 'Redshift', 'BigQuery', 'LangChain', 'LlamaIndex', 'Bedrock', 'Vertex AI',
  'TypeScript', 'JavaScript', 'Python', 'Java', 'Kotlin', 'Scala', 'Golang', 'Rust', 'Ruby',
  'PHP', 'Swift', 'Perl', 'Haskell', 'Elixir', 'Erlang', '.NET', 'Blazor', 'jQuery', 'Backbone',
  'Tomcat', 'JBoss', 'WebLogic', 'Oracle', 'SAP', 'Salesforce', 'Twilio', 'Firebase',
];
const esc = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const RE = TECH.map(t => ({ t, re: new RegExp(`(^|[^A-Za-z0-9+#.])${esc(t)}([^A-Za-z0-9+#]|$)`, 'i') }));
const techIn = s => RE.filter(({ re }) => re.test(s)).map(({ t }) => t);
// "Springboot" (JD) and "Spring Boot" (CV) are the same technology; comparing raw
// strings reported a false mis-grounding on #34. Canonicalise to alphanumerics so
// spacing and punctuation variants collapse — Node.js/Nodejs likewise.
const canon = t => t.toLowerCase().replace(/[^a-z0-9+#]/g, '');
const canonSet = s => new Set(techIn(s).map(canon));
const cvTech = canonSet(cv);

for (const dir of process.argv.slice(2)) {
  const rdir = join(dir, 'reports');
  if (!existsSync(rdir)) { console.log(`\n=== ${dir}: missing ===`); continue; }
  let rows = 0, graded = 0, misgrounded = 0, gapWithCvTech = 0, pickNone = 0;
  const misEx = [], fabEx = [];
  let stories = 0, fabricated = 0;
  const fabTally = {};
  // Atom concentration needs no tech list, so it catches semantic failures the
  // lexical check cannot: a run that grounds "5+ years of X" on an unrelated CV
  // line will also reuse that one line across many requirements. One atom
  // answering many requirements = the model found a plausible-looking line and
  // recycled it rather than matching each requirement on its merits.
  let concOffers = 0, concWorst = 0, reuseSum = 0;
  const concEx = [];

  for (const f of readdirSync(rdir).filter(f => f.endsWith('.md'))) {
    const id = f.split('-')[0];
    const md = readFileSync(join(rdir, f), 'utf8');

    const atomUse = new Map();
    for (const line of md.split('\n')) {
      const m = /^\|(.*)\|(.*)\|\s*(Strong|Transferable|Gap)\s*\|\s*$/.exec(line);
      if (!m) continue;
      const req = m[1].replace(/\*\*\[(must|nice)[^\]]*\]\*\*/, '').trim();
      const ev = m[2].trim(), strength = m[3];
      rows++;
      if (strength !== 'Gap' && ev && ev !== '—') {
        const k = ev.slice(0, 60);
        atomUse.set(k, (atomUse.get(k) || 0) + 1);
      }
      const rt = techIn(req);
      if (strength === 'Gap') {
        if (ev === '—' || ev === '') pickNone++;
        // Named tech the CV demonstrably has, yet graded Gap.
        if (rt.some(t => cvTech.has(canon(t)))) gapWithCvTech++;
        continue;
      }
      graded++;
      // Mis-grounded: requirement names tech, none of it in the cited evidence.
      const evT = canonSet(ev);
      if (rt.length && !rt.some(t => evT.has(canon(t)))) {
        misgrounded++;
        if (misEx.length < 6) misEx.push(`#${id} [${strength}] "${req.slice(0, 52)}" needs ${rt.join('/')} | ev: ${ev.slice(0, 58)}`);
      }
    }

    if (atomUse.size) {
      const worst = [...atomUse.entries()].sort((a, b) => b[1] - a[1])[0];
      const used = [...atomUse.values()].reduce((a, b) => a + b, 0);
      concOffers++; reuseSum += used / atomUse.size;
      if (worst[1] > concWorst) concWorst = worst[1];
      if (worst[1] >= 4) concEx.push(`#${id} one atom answers ${worst[1]}/${used} rows: ${worst[0].slice(0, 62)}`);
    }

    // STAR table + hard questions: any technology named that cv.md lacks.
    const storyBlock = md.split(/\*\*Likely hard questions/)[0];
    for (const line of storyBlock.split('\n')) {
      const m = /^\|\s*\d+\s*\|(.*)\|(.*)$/.exec(line);
      if (!m) continue;
      stories++;
      const bad = techIn(m[2]).filter(t => !cvTech.has(canon(t)));
      if (bad.length) {
        fabricated++;
        for (const b of bad) fabTally[b] = (fabTally[b] || 0) + 1;
        if (fabEx.length < 8) fabEx.push(`#${id} invents ${bad.join('/')}: ${m[2].trim().slice(0, 72)}`);
      }
    }
  }

  console.log(`\n=== ${dir} ===`);
  console.log(`  evidence rows            ${rows}   (graded non-Gap: ${graded})`);
  console.log(`  MIS-GROUNDED             ${misgrounded}  (${(100 * misgrounded / Math.max(graded, 1)).toFixed(1)}% of graded rows)`);
  console.log(`  Gap despite tech in CV   ${gapWithCvTech}`);
  console.log(`  pick=none / Gap rows     ${pickNone}`);
  console.log(`  atom reuse (rows/atom)   ${(reuseSum / Math.max(concOffers, 1)).toFixed(2)}   worst single atom: ${concWorst} rows`);
  for (const e of concEx) console.log(`    CONC ${e}`);
  console.log(`  STAR/story rows          ${stories}`);
  console.log(`  FABRICATED tech          ${fabricated}  (${(100 * fabricated / Math.max(stories, 1)).toFixed(1)}% of stories)`);
  if (Object.keys(fabTally).length) console.log(`    invented: ${Object.entries(fabTally).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k}x${v}`).join(', ')}`);
  for (const e of fabEx) console.log(`    FAB  ${e}`);
  for (const e of misEx) console.log(`    MIS  ${e}`);
}
