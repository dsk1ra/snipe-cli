#!/usr/bin/env node
// snipe-tui — interactive cockpit for snipe.
// Paste a JD → Enter enqueues (snipe --jdf-q) → ▶ runs the queue
// (snipe --drain). Pure consumer: all pipeline state is read from disk
// (snipe-queue.txt, local-state.tsv, scores/, evals/, output/) every second.
//
// Keys: ←→ or 1/2/3 switch tabs · ↑↓ walk every element top-to-bottom (tab
//       level → list → JD → URL → Add; ↑ past the top returns to tab level;
//       → hops JD → ▶) · on a selected list row with a link, → focuses the
//       link and Enter opens it in the browser (← back to the row) · Tab/
//       Shift-Tab still cycles input ↔ ▶ ↔ list · Enter loops JD → URL → Add
//       to queue (enqueues) · o open result folder · a mark applied ✉ ·
//       x mark skip ⊘ (mutually exclusive) ·
//       Esc clear field/step out · q quit (outside input fields)
//       Follow-ups tab: ↓ enters list · Enter mark nudged · u undo · o report
// Slash commands (typed in the JD box, or just press /): /scan runs the
//       portal scanner and queues whatever it finds.
//
// Self-check (no TTY needed): node snipe-tui.mjs --stats

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { spawn, execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import React from 'react';
import { render, Box, Text, measureElement } from 'ink';

const h = React.createElement;

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const BATCH = path.join(ROOT, 'batch');
const QUEUE_FILE = path.join(BATCH, 'snipe-queue.txt');
const STATE_FILE = path.join(BATCH, 'local-state.tsv');
const LOCK_FILE = path.join(BATCH, 'local-runner.pid');
const SCORES_DIR = path.join(BATCH, 'scores');
const EVALS_DIR = path.join(BATCH, 'evals');
const OUTPUT_DIR = path.join(ROOT, 'output');
const SNIPE = path.join(ROOT, 'snipe');

// ── disk readers ─────────────────────────────────────────────────────────────

const jsonCache = new Map(); // fullPath → {mtime, data}
function readJsonCached(fullPath) {
  let st;
  try { st = fs.statSync(fullPath); } catch { return null; }
  const hit = jsonCache.get(fullPath);
  if (hit && hit.mtime === st.mtimeMs) return hit.data;
  let data = null;
  try { data = JSON.parse(fs.readFileSync(fullPath, 'utf8')); } catch {}
  jsonCache.set(fullPath, { mtime: st.mtimeMs, data });
  return data;
}

function readQueueIds() {
  try {
    return fs.readFileSync(QUEUE_FILE, 'utf8').split('\n').map(s => s.trim()).filter(Boolean);
  } catch { return []; }
}

// local-state.tsv, 10 cols: id url p1_status p1_score p1_archetype p2_status p2_report_num p3_status error retries
function readStateRows() {
  const rows = new Map();
  let txt = '';
  try { txt = fs.readFileSync(STATE_FILE, 'utf8'); } catch { return rows; }
  for (const line of txt.split('\n').slice(1)) {
    const c = line.split('\t');
    if (c.length < 10 || !/^\d+$/.test(c[0])) continue;
    rows.set(c[0], { p1s: c[2], p2s: c[5], rnum: c[6], p3s: c[7], err: c[8] });
  }
  return rows;
}

// id → job link from batch-input.tsv. Placeholder urls (manual:snipe-N,
// local:…) are not links — those ids stay out of the map → "no link" in the UI.
function readUrlMap() {
  const map = new Map();
  let txt = '';
  try { txt = fs.readFileSync(INPUT_FILE, 'utf8'); } catch { return map; }
  for (const line of txt.split('\n').slice(1)) {
    const c = line.split('\t');
    if (c[0] && /^https?:\/\//.test(c[1] || '')) map.set(c[0], c[1]);
  }
  return map;
}

// Ids with activity in the last 24 h (JD added or eval written) + still-queued
// ids, sorted numerically — ids are sequential so this is chronological.
function recentIds() {
  const cutoff = Date.now() - 24 * 3600 * 1000;
  const ids = new Set(readQueueIds());
  for (const dir of [path.join(BATCH, 'jds'), EVALS_DIR]) {
    let files = [];
    try { files = fs.readdirSync(dir); } catch {}
    for (const f of files) {
      const m = f.match(/^(\d+)\.(txt|json)$/);
      if (!m) continue;
      try { if (fs.statSync(path.join(dir, f)).mtimeMs >= cutoff) ids.add(m[1]); } catch {}
    }
  }
  return [...ids].sort((a, b) => a - b);
}

function runnerActive() {
  try {
    process.kill(Number(fs.readFileSync(LOCK_FILE, 'utf8').trim()), 0);
    return true;
  } catch { return false; }
}

function lifetimeAvg() {
  let files = [];
  try { files = fs.readdirSync(EVALS_DIR); } catch {}
  let sum = 0, n = 0;
  for (const f of files) {
    if (!f.endsWith('.json')) continue;
    const e = readJsonCached(path.join(EVALS_DIR, f));
    if (e && e.status === 'evaled' && typeof e.score === 'number') { sum += e.score; n++; }
  }
  return n ? sum / n : null;
}

function lifetimeStats(rows) {
  let failed = 0, completed = 0, cv = 0, p1Gated = 0;
  for (const r of rows.values()) {
    if (r.p1s === 'score_failed' || r.p2s === 'eval_failed' || r.p3s === 'pdf_failed') failed++;
    if (r.p2s === 'p1-gated') p1Gated++;
    if (r.p2s === 'evaled' && (r.p3s === 'completed' || r.p3s === 'skipped')) {
      completed++;
      if (r.p3s === 'completed') cv++;
    }
  }
  return { failed, completed, cv, p1Gated, rate: completed ? cv / completed : null };
}

const resultDirCache = new Map(); // report_num → abs dir
function resultDirFor(rnum) {
  if (!rnum || rnum === '-') return null;
  if (resultDirCache.has(rnum)) return resultDirCache.get(rnum);
  let dir = null;
  try {
    const hit = fs.readdirSync(OUTPUT_DIR).find(d => d.endsWith(`_${rnum}`));
    if (hit) dir = path.join(OUTPUT_DIR, hit);
  } catch {}
  if (dir) resultDirCache.set(rnum, dir); // only cache hits — dir appears late
  return dir;
}

function labelFor(id) {
  const s = readJsonCached(path.join(SCORES_DIR, `${id}.json`));
  const e = readJsonCached(path.join(EVALS_DIR, `${id}.json`));
  const pick = f => {
    const v = (e && e[f]) || (s && s[f]) || '';
    return v && v !== 'unknown' ? v : '';
  };
  return [pick('company'), pick('role')].filter(Boolean).join(' — ') || `#${id}`;
}

// ── activity data (Activity tab grid + ✉ applied marks) ──────────────────────

const APPLIED_FILE = path.join(BATCH, 'applied.tsv');
const SKIPPED_FILE = path.join(BATCH, 'skipped.tsv'); // reviewed, decided not to apply
const TRACKER_FILE = path.join(ROOT, 'data', 'applications.md');
const FOLLOWUPS_FILE = path.join(ROOT, 'data', 'follow-ups.md');
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

const dayKey = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
const addDays = (d, n) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
const mondayOf = d => addDays(d, -((d.getDay() + 6) % 7));

function readMarkMap(file) { // id → ISO timestamp (applied.tsv / skipped.tsv)
  const map = new Map();
  let txt = '';
  try { txt = fs.readFileSync(file, 'utf8'); } catch { return map; }
  for (const line of txt.split('\n')) {
    const [id, ts] = line.split('\t');
    if (id && ts) map.set(id, ts);
  }
  return map;
}

const writeMarkMap = (file, map) =>
  fs.writeFileSync(file, [...map].map(([i, t]) => `${i}\t${t}`).join('\n') + (map.size ? '\n' : ''));

function bucketAdd(b, d) {
  const k = dayKey(d);
  b.days.set(k, (b.days.get(k) || 0) + 1);
  const hk = `${k}:${d.getHours()}`;
  b.hours.set(hk, (b.hours.get(hk) || 0) + 1);
}

// ponytail: eval mtime is the scan date — a copy/rebuild shifts history;
// reports/ filenames stay the durable record.
function activityBuckets() {
  const scans = { days: new Map(), hours: new Map() };
  let files = [];
  try { files = fs.readdirSync(EVALS_DIR); } catch {}
  for (const f of files) {
    if (!f.endsWith('.json')) continue;
    try { bucketAdd(scans, new Date(fs.statSync(path.join(EVALS_DIR, f)).mtimeMs)); } catch {}
  }
  const apps = { days: new Map(), hours: new Map() };
  for (const ts of readMarkMap(APPLIED_FILE).values()) {
    const d = new Date(ts);
    if (!isNaN(d)) bucketAdd(apps, d);
  }
  return { scans, apps };
}

// ── mutable app state (single instance; ink only renders it) ─────────────────

const INPUTS = ['jd', 'url', 'add']; // one Tab group; Enter loops through them
const S = {
  tab: 'stats',      // 'stats' | 'activity' | 'followups'
  actView: 'year',   // 'year' | 'month' | 'day'
  actType: 'scans',  // 'scans' | 'apps'
  actDate: new Date(),
  actHr: new Date().getHours(),
  gridSel: false,    // grid cursor mode — arrows move the selected cell
  gridH: 0,          // measured grid-area height, cached across tab switches (0 = unmeasured)
  act: null,         // activityBuckets() — only while the Activity tab is open
  applied: new Map(),
  skipped: new Map(),
  followups: null,   // followup-cadence.mjs --json output (null until first load)
  fuIdx: 0,          // selection on the Follow-ups tab
  fuIn: false,       // false = tab level; ↓/Enter/Tab dives into the list
  focus: 'none', // nothing focused at launch — h/l/q work immediately; paste auto-focuses jd
  lastInput: 'jd', // where Tab re-enters the input group
  jd: '', url: '',
  msg: '', msgIsError: false,
  busy: false,
  drainActive: false,
  scanActive: false,
  listIdx: 0,
  rowFocus: 'row', // 'row' | 'link' — sub-focus within the selected queue row
  scroll: 0,   // first visible list row
  listWin: 0,  // rows currently visible (for PgUp/PgDn)
  sessionIds: recentIds(), // last 24 h of activity + queued leftovers + this session's adds
  snap: { queueIds: [], rows: new Map(), urls: new Map(), runner: false, avg: null, stats: { failed: 0, completed: 0, cv: 0, rate: null } },
  frame: 0,
};

function poll() {
  // React dev build emits performance.measure() per render; Node retains those
  // entries forever → 4 GB heap OOM after hours. Clear the timeline each poll.
  performance.clearMeasures();
  performance.clearMarks();
  const rows = readStateRows();
  S.snap = {
    queueIds: readQueueIds(),
    rows,
    urls: readUrlMap(),
    runner: runnerActive(),
    avg: lifetimeAvg(),
    stats: lifetimeStats(rows),
  };
  S.applied = readMarkMap(APPLIED_FILE);
  S.skipped = readMarkMap(SKIPPED_FILE);
  if (S.tab === 'activity') S.act = activityBuckets();
}

function itemInfo(id) {
  const row = S.snap.rows.get(id);
  const evalJson = readJsonCached(path.join(EVALS_DIR, `${id}.json`));
  const score = evalJson && typeof evalJson.score === 'number' ? evalJson.score : null;
  const base = { label: labelFor(id), score };
  if (row && (row.p1s === 'score_failed' || row.p1s === 'unavailable'
    || row.p2s === 'eval_failed' || row.p3s === 'pdf_failed')) {
    return { ...base, kind: 'failed', err: row.err && row.err !== '-' ? row.err : 'failed' };
  }
  if (row && row.p3s === 'completed') return { ...base, kind: 'done', resultDir: resultDirFor(row.rnum) };
  if (row && row.p3s === 'skipped') {
    const note = row.p2s === 'p1-gated' ? 'P1-gated (no Phase 2)' : 'below threshold';
    return { ...base, kind: 'done', note };
  }
  if (S.snap.queueIds.includes(id)) return { ...base, kind: 'waiting' };
  if (S.drainActive || S.snap.runner) {
    const phase = !row || row.p1s !== 'scored' ? 'scoring'
      : row.p2s !== 'evaled' ? 'evaluating' : 'tailoring CV';
    return { ...base, kind: 'running', phase };
  }
  return { ...base, kind: 'pending' }; // popped but no active run (interrupted drain)
}

function setMsg(msg, isError = false) { S.msg = msg; S.msgIsError = isError; }

// Follow-ups change daily, not per second — refreshed on mount, every 10 min,
// and after toggling applied. Async; panel shows '—' until first result.
function refreshFollowups(bump) {
  execFile('node', [path.join(ROOT, 'tracker/followup-cadence.mjs'), '--json'], (err, stdout) => {
    if (err) return;
    try { S.followups = JSON.parse(stdout); } catch { return; }
    S.fuIdx = Math.min(S.fuIdx, Math.max(0, (S.followups.entries?.length || 1) - 1));
    if (bump) bump();
  });
}

const dueFollowups = () => S.followups
  ? S.followups.entries.filter(e => e.urgency === 'urgent' || e.urgency === 'overdue')
  : null;

// ── actions ──────────────────────────────────────────────────────────────────

function enqueue(bump) {
  if (S.busy) return;
  const cmd = S.jd.trim();
  if (cmd.startsWith('/')) { // slash command, not a JD
    if (cmd === '/scan') runScan(bump);
    else { setMsg(`Unknown command: ${cmd} — try /scan`, true); bump(); }
    return;
  }
  if (!cmd) { setMsg('Paste a job description first', true); bump(); return; }
  S.busy = true;
  setMsg('Adding to queue…');
  bump();
  const tmp = path.join(os.tmpdir(), `snipe-tui-${process.pid}-${Date.now()}.txt`);
  fs.writeFileSync(tmp, S.jd);
  const args = ['--jdf-q', tmp];
  if (S.url.trim()) args.push('--link', S.url.trim());
  execFile(SNIPE, args, (err, stdout = '', stderr = '') => {
    fs.unlink(tmp, () => {});
    S.busy = false;
    const m = stdout.match(/Added offer #(\d+)/);
    if (err || !m) {
      setMsg((stdout + ' ' + stderr).replace(/\s+/g, ' ').trim().slice(0, 120) || `snipe failed (${err?.code})`, true);
    } else {
      S.sessionIds.push(m[1]);
      S.jd = ''; S.url = '';
      S.focus = 'jd'; // ready for the next JD
      setMsg(`Queued #${m[1]}`);
    }
    poll();
    bump();
  });
}

function startDrain(bump) {
  if (S.drainActive || S.snap.runner) { setMsg('A run is already active', true); bump(); return; }
  if (!S.snap.queueIds.length) { setMsg('Queue is empty', true); bump(); return; }
  fs.mkdirSync(path.join(BATCH, 'logs'), { recursive: true });
  const fd = fs.openSync(path.join(BATCH, 'logs', 'snipe-tui-drain.log'), 'a');
  const drainedIds = [...S.snap.queueIds];
  const child = spawn(SNIPE, ['--drain'], { detached: true, stdio: ['ignore', fd, fd] });
  fs.closeSync(fd);
  child.on('exit', code => {
    S.drainActive = false;
    setMsg(code === 0 ? 'Run finished' : `Run exited with code ${code} — see batch/logs/snipe-tui-drain.log`, code !== 0);
    poll();
    notifyDrainDone(drainedIds, code);
    bump();
  });
  child.unref(); // survives TUI quit; state stays on disk
  S.drainActive = true;
  setMsg('Pipeline running…');
  bump();
}

// ── /scan command ────────────────────────────────────────────────────────────

const INPUT_FILE = path.join(BATCH, 'batch-input.tsv');

function maxBatchId() {
  try {
    return fs.readFileSync(INPUT_FILE, 'utf8').split('\n').slice(1)
      .reduce((m, l) => Math.max(m, parseInt(l.split('\t')[0], 10) || 0), 0);
  } catch { return 0; }
}

// /scan — run the zero-token portal scanner (scan.mjs → pipeline.md), import
// the new URLs into batch-input.tsv, and queue the fresh ids. No JD text is
// needed: Phase 1 fetches JDs from the URLs itself. If the TUI quits mid-scan
// the import never runs, but pipeline.md keeps the URLs unchecked, so the
// next /scan (or `node batch/import-pipeline.mjs`) picks them up — no loss.
function runScan(bump) {
  if (S.scanActive) { setMsg('A scan is already running', true); bump(); return; }
  fs.mkdirSync(path.join(BATCH, 'logs'), { recursive: true });
  const fd = fs.openSync(path.join(BATCH, 'logs', 'snipe-tui-scan.log'), 'a');
  const child = spawn('node', [path.join(ROOT, 'scan.mjs')], { detached: true, stdio: ['ignore', fd, fd] });
  fs.closeSync(fd);
  child.on('exit', code => {
    S.scanActive = false;
    if (code !== 0) { setMsg(`Scan exited with code ${code} — see batch/logs/snipe-tui-scan.log`, true); bump(); return; }
    const before = maxBatchId();
    execFile('node', [path.join(BATCH, 'import-pipeline.mjs')], ierr => {
      if (ierr) { setMsg('Scan done but import failed — see batch/logs/snipe-tui-scan.log', true); bump(); return; }
      const ids = [];
      for (let id = before + 1; id <= maxBatchId(); id++) ids.push(String(id));
      if (!ids.length) { setMsg('Scan done — nothing new'); poll(); bump(); return; }
      // same flock discipline as snipe: a concurrent drain rewrites the
      // queue file in place, so an unlocked append could be lost mid-rewrite
      execFile('bash', ['-c', 'exec 9>>"$0"; flock 9; printf \'%s\\n\' "$@" >&9', QUEUE_FILE, ...ids], qerr => {
        if (qerr) { setMsg('Scan done but queueing failed — run: snipe --drain', true); bump(); return; }
        S.sessionIds.push(...ids);
        setMsg(`Scan done — ${ids.length} new offer${ids.length === 1 ? '' : 's'} queued`);
        poll();
        bump();
      });
    });
  });
  child.unref(); // scanner survives TUI quit; results land in pipeline.md
  S.scanActive = true;
  S.jd = ''; S.url = '';
  setMsg('Scanning portals…');
  bump();
}

// Drains run for many minutes while the user is elsewhere — one desktop
// notification per drain with the best score. Silently a no-op without notify-send.
function notifyDrainDone(ids, code) {
  let body = `Run exited with code ${code}`;
  if (code === 0) {
    let best = null;
    for (const id of ids) {
      const e = readJsonCached(path.join(EVALS_DIR, `${id}.json`));
      if (e && e.status === 'evaled' && typeof e.score === 'number' && (!best || e.score > best.score)) best = { id, score: e.score };
    }
    body = best
      ? `${ids.length} processed · best ${best.score.toFixed(1)} — ${labelFor(best.id)}`
      : `${ids.length} processed`;
  }
  const n = spawn('notify-send', ['snipe', body], { detached: true, stdio: 'ignore' });
  n.on('error', () => {});
  n.unref();
}

function openResult() {
  const id = S.sessionIds[S.listIdx];
  if (!id) return;
  const info = itemInfo(id);
  if (info.kind === 'done' && info.resultDir) {
    spawn('xdg-open', [info.resultDir], { detached: true, stdio: 'ignore' }).unref();
    setMsg(`Opened ${path.basename(info.resultDir)}`);
  } else {
    setMsg('No result folder for this item', true);
  }
}

function openLink() {
  const id = S.sessionIds[S.listIdx];
  const url = id && S.snap.urls.get(id);
  if (!url) { setMsg('No link for this item', true); return; }
  spawn('xdg-open', [url], { detached: true, stdio: 'ignore' }).unref();
  setMsg(`Opened ${url}`);
}

// Flip only the Status cell of the tracker row whose Report cell links [<num>](…),
// and only if it currently reads `from` — so it never clobbers a later-stage
// status (Interview/Offer/…).
function syncTracker(rnum, from, to) {
  if (!rnum || rnum === '-') return false;
  let txt;
  try { txt = fs.readFileSync(TRACKER_FILE, 'utf8'); } catch { return false; }
  const numRe = new RegExp(`\\[0*${Number(rnum)}\\]\\([^)]*reports/`);
  const lines = txt.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const cells = lines[i].split('|');
    // | # | Date | Company | Role | Score | Status | PDF | Report | Notes |
    if (cells.length < 10 || !numRe.test(cells[8])) continue;
    if (cells[6].trim() !== from) return false;
    cells[6] = ` ${to} `;
    lines[i] = cells.join('|');
    fs.writeFileSync(TRACKER_FILE, lines.join('\n'));
    return true;
  }
  return false;
}

// a (applied ✉) and x (skip ⊘) are mutually exclusive triage marks: setting
// one clears the other, tracker status follows via syncTracker.
const MARKS = {
  applied: { file: APPLIED_FILE, state: 'Applied', label: 'applied ✉' },
  skipped: { file: SKIPPED_FILE, state: 'SKIP', label: 'skip ⊘' },
};

function toggleMark(kind) {
  const other = kind === 'applied' ? 'skipped' : 'applied';
  const id = S.sessionIds[S.listIdx];
  if (!id) return;
  const e = readJsonCached(path.join(EVALS_DIR, `${id}.json`));
  if (!e || e.status !== 'evaled') { setMsg('Not evaluated yet — nothing to mark', true); return; }
  const map = readMarkMap(MARKS[kind].file);
  const marking = !map.has(id);
  let clearedOther = false;
  if (marking) {
    map.set(id, new Date().toISOString());
    const omap = readMarkMap(MARKS[other].file);
    if (omap.delete(id)) { // roll the other mark's tracker state back first
      writeMarkMap(MARKS[other].file, omap);
      syncTracker(e.report_num, MARKS[other].state, 'Evaluated');
      S[other] = omap;
      clearedOther = true;
    }
  } else map.delete(id);
  writeMarkMap(MARKS[kind].file, map);
  const { state } = MARKS[kind];
  const synced = syncTracker(e.report_num, marking ? 'Evaluated' : state, marking ? state : 'Evaluated');
  S[kind] = map;
  // applied set/cleared changes the cadence; next poll renders it
  if (kind === 'applied' || clearedOther) refreshFollowups();
  setMsg(`#${id} ${marking ? `marked ${MARKS[kind].label}` : 'unmarked'}${synced ? ' · tracker updated' : ' · tracker unchanged'}`);
}

// ── follow-up actions (Follow-ups tab) ───────────────────────────────────────
// A nudge = one appended row in data/follow-ups.md — the same table
// followup-cadence.mjs reads, so the cadence clock resets automatically and the
// entry leaves every due list. User-layer file, written only on user action.

function appendFollowup(e) {
  let txt = '';
  try { txt = fs.readFileSync(FOLLOWUPS_FILE, 'utf8'); } catch {}
  if (!txt.trim()) {
    txt = '# Follow-ups Log\n\n| # | App | Date | Company | Role | Channel | Contact | Notes |\n|---|-----|------|---------|------|---------|---------|-------|\n';
  }
  let max = 0;
  for (const line of txt.split('\n')) {
    const n = parseInt(line.split('|')[1]);
    if (!isNaN(n)) max = Math.max(max, n);
  }
  const num = max + 1;
  const clean = s => String(s).replace(/\|/g, '/');
  const row = `| ${num} | ${e.num} | ${dayKey(new Date())} | ${clean(e.company)} | ${clean(e.role)} | manual | — | via TUI |`;
  fs.writeFileSync(FOLLOWUPS_FILE, txt.trimEnd() + '\n' + row + '\n');
  return num;
}

function markNudged(bump) {
  const e = S.followups?.entries?.[S.fuIdx];
  if (!e) { setMsg('Nothing to mark', true); return; }
  appendFollowup(e);
  // optimistic: the entry leaves the due lists this frame; refresh confirms from disk
  const cad = S.followups.cadenceConfig || {};
  e.followupCount++;
  e.daysSinceLastFollowup = 0;
  e.urgency = e.status === 'applied' && e.followupCount >= (cad.applied_max_followups ?? 2) ? 'cold' : 'waiting';
  setMsg(`${e.company} nudged ✓ — ${e.urgency === 'cold' ? 'that was the final follow-up' : `next due in ${cad.applied_subsequent ?? 7}d`} · u undo`);
  refreshFollowups(bump);
}

// u peels the LATEST recorded nudge off the selected entry, one per press
// (2 → 1 → 0). Reads the file, not session state, so it survives restarts and
// also rolls back nudges recorded outside the TUI.
function undoNudge(bump) {
  const e = S.followups?.entries?.[S.fuIdx];
  if (!e) { setMsg('Nothing selected', true); return; }
  let txt = '';
  try { txt = fs.readFileSync(FOLLOWUPS_FILE, 'utf8'); } catch {}
  const lines = txt.split('\n');
  let best = -1, bestNum = -1;
  for (let i = 0; i < lines.length; i++) {
    const p = lines[i].split('|').map(s => s.trim());
    if (p.length < 8) continue;
    const num = parseInt(p[1]);
    if (isNaN(num) || parseInt(p[2]) !== e.num) continue;
    if (num >= bestNum) { bestNum = num; best = i; }
  }
  if (best < 0) { setMsg(`No nudges recorded for ${e.company}`, true); return; }
  lines.splice(best, 1);
  fs.writeFileSync(FOLLOWUPS_FILE, lines.join('\n'));
  e.followupCount = Math.max(0, e.followupCount - 1); // optimistic; refresh recomputes urgency
  setMsg(`Rolled back last nudge for ${e.company} — ${e.followupCount} left`);
  refreshFollowups(bump);
}

// Cadence's own reportPath mis-joins the tracker's ../reports/ links, so find
// the report by its number prefix instead.
function openFuReport() {
  const e = S.followups?.entries?.[S.fuIdx];
  if (!e) return;
  let f = null;
  try { f = fs.readdirSync(path.join(ROOT, 'reports')).find(x => x.startsWith(`${String(e.num).padStart(3, '0')}-`)); } catch {}
  if (f) {
    spawn('xdg-open', [path.join(ROOT, 'reports', f)], { detached: true, stdio: 'ignore' }).unref();
    setMsg(`Opened ${f}`);
  } else setMsg('No report found for this application', true);
}

// ── raw stdin: bracketed paste + key parsing ─────────────────────────────────
// Ink's useInput is bypassed entirely so a pasted JD can't trigger key handlers.

const PASTE_START = '\x1b[200~';
const PASTE_END = '\x1b[201~';

function partialTail(s, marker) {
  for (let k = Math.min(marker.length - 1, s.length); k > 0; k--) {
    if (s.endsWith(marker.slice(0, k))) return k;
  }
  return 0;
}

function makeStdinHandler(bump, quit) {
  let raw = '', pasting = false, pasteBuf = '';

  const onPaste = text => {
    if (S.tab !== 'stats') { setMsg('Switch to the Queue tab (← or 1) to paste a JD', true); return; }
    if (S.focus === 'url') {
      S.url += text.replace(/\s+/g, ' ').trim();
    } else {
      S.jd = S.jd ? `${S.jd}\n${text}` : text;
      S.focus = 'jd';
    }
    setMsg('');
  };

  // Tab cycles three groups: inputs (re-enters at the last-used field) → play → list
  const moveFocus = dir => {
    if (S.focus === 'none') { S.focus = S.lastInput; return; }
    if (INPUTS.includes(S.focus)) S.lastInput = S.focus;
    const GROUPS = ['input', 'play', 'list'];
    const group = GROUPS.includes(S.focus) ? S.focus : 'input';
    const next = GROUPS[(GROUPS.indexOf(group) + dir + GROUPS.length) % GROUPS.length];
    S.focus = next === 'input' ? S.lastInput : next;
    if (S.focus === 'list' && S.sessionIds.length) { S.listIdx = S.sessionIds.length - 1; S.rowFocus = 'row'; } // start at the most recent
  };

  // Enter loops jd → url → add → enqueue (focus returns to jd)
  const onEnter = () => {
    if (S.focus === 'jd') S.focus = 'url';
    else if (S.focus === 'url') S.focus = 'add';
    else if (S.focus === 'add') enqueue(bump);
    else if (S.focus === 'play') startDrain(bump);
    else if (S.focus === 'list') { S.rowFocus === 'link' ? openLink() : openResult(); }
  };

  const TABS = ['stats', 'activity', 'followups'];
  const setTab = t => {
    S.tab = t;
    if (t === 'activity') S.act = activityBuckets();
    if (t === 'followups') { S.fuIdx = 0; S.fuIn = false; refreshFollowups(bump); }
    setMsg('');
  };
  const cycleTab = dir => setTab(TABS[(TABS.indexOf(S.tab) + dir + TABS.length) % TABS.length]);
  // typing in a text field claims all printable keys and ←→
  const typing = () => S.tab === 'stats' && (S.focus === 'jd' || S.focus === 'url');

  const movePeriod = dir => {
    const d = new Date(S.actDate);
    if (S.actView === 'year') d.setFullYear(d.getFullYear() + dir);
    else if (S.actView === 'month') d.setMonth(d.getMonth() + dir);
    else d.setDate(d.getDate() + dir);
    S.actDate = d;
  };

  const toggleType = () => { S.actType = S.actType === 'scans' ? 'apps' : 'scans'; };

  // grid cursor: year/month ←→ = ±week, ↑↓ = ±day; day view ←→ = ±hour (rolls the day)
  const moveGridSel = (dx, dy) => {
    if (S.actView === 'day') {
      let hr = S.actHr + dx + dy;
      const d = new Date(S.actDate);
      if (hr < 0) { hr = 23; d.setDate(d.getDate() - 1); }
      if (hr > 23) { hr = 0; d.setDate(d.getDate() + 1); }
      S.actHr = hr; S.actDate = d;
    } else {
      S.actDate = addDays(S.actDate, dx * 7 + dy);
    }
  };

  const actKey = ch => {
    if (ch === 'y') S.actView = 'year';
    else if (ch === 'm') S.actView = 'month';
    else if (ch === 'd') S.actView = 'day';
    else if (ch === 'j' || ch === 'k') toggleType();
    else if (ch === '<' || ch === ',') movePeriod(-1); // the header's ‹ › — work even in cell mode
    else if (ch === '>' || ch === '.') movePeriod(1);
    else if (ch === 'q') quit();
  };

  const fuKey = ch => {
    if (ch === 'q') quit();
    else if (ch === 'o') openFuReport();
    else if (ch === 'u') undoNudge(bump);
    else if (ch === 'j' || ch === 'k') moveFuSel(ch === 'j' ? 1 : -1);
  };

  const onChar = ch => {
    if (!typing()) {
      const jump = '123'.indexOf(ch);
      if (jump >= 0) { setTab(TABS[jump]); return; }
      if (ch === 'h' || ch === 'l') { cycleTab(ch === 'l' ? 1 : -1); return; } // legacy aliases for ←→
    }
    if (S.tab === 'activity') { actKey(ch); return; }
    if (S.tab === 'followups') { fuKey(ch); return; }
    if (S.focus === 'jd') S.jd += ch;
    else if (S.focus === 'url') S.url += ch;
    else if (ch === '/') { S.focus = 'jd'; S.lastInput = 'jd'; S.jd = '/'; } // start a slash command from anywhere on the tab
    else if (ch === 'q') quit();
    else if (ch === 'o' && S.focus === 'list') openResult();
    else if (ch === 'a' && S.focus === 'list') toggleMark('applied');
    else if (ch === 'x' && S.focus === 'list') toggleMark('skipped');
    else if ((ch === 'j' || ch === 'k') && S.focus === 'list') moveSel(ch === 'j' ? 1 : -1);
  };

  const moveSel = dir => {
    if (!S.sessionIds.length) return;
    S.listIdx = Math.min(S.sessionIds.length - 1, Math.max(0, S.listIdx + dir));
    S.rowFocus = 'row';
  };

  const moveFuSel = dir => {
    const n = S.followups?.entries?.length || 0;
    if (n) S.fuIdx = Math.min(n - 1, Math.max(0, S.fuIdx + dir));
  };

  // spatial ↑↓: one vertical chain per tab. ↓ from the tab level dives into
  // content; ↑ past the top returns to it; list/grid edges hand focus onward.
  const moveV = dir => {
    if (S.tab === 'stats') {
      const lastIdx = S.sessionIds.length - 1;
      if (S.focus === 'none') {
        if (dir === 1) { S.focus = lastIdx >= 0 ? 'list' : 'jd'; S.listIdx = 0; S.rowFocus = 'row'; }
      } else if (S.focus === 'list') {
        if (dir === -1 && S.listIdx === 0) S.focus = 'none';
        else if (dir === 1 && S.listIdx >= lastIdx) S.focus = 'jd';
        else moveSel(dir);
      } else if (S.focus === 'jd' || S.focus === 'play') {
        if (dir === -1) { S.focus = lastIdx >= 0 ? 'list' : 'none'; S.listIdx = lastIdx; S.rowFocus = 'row'; }
        else S.focus = 'url';
      } else if (S.focus === 'url') {
        S.focus = dir === -1 ? 'jd' : 'add';
      } else if (S.focus === 'add') {
        if (dir === -1) S.focus = 'url'; // ↓ at the end of the chain: no-op
      }
      if (INPUTS.includes(S.focus)) S.lastInput = S.focus;
      return;
    }
    if (S.tab === 'activity') {
      if (!S.gridSel) { if (dir === 1) S.gridSel = true; return; }
      // top edge (Monday row, or the single-row day view) hands focus back up
      const atTop = S.actView === 'day' || (new Date(S.actDate).getDay() + 6) % 7 === 0;
      if (dir === -1 && atTop) { S.gridSel = false; return; }
      if (S.actView !== 'day') moveGridSel(0, dir);
      return;
    }
    // followups
    if (!S.fuIn) { if (dir === 1) S.fuIn = true; return; }
    if (dir === -1 && S.fuIdx === 0) { S.fuIn = false; return; }
    moveFuSel(dir);
  };

  const handleKeys = seg => {
    let i = 0;
    while (i < seg.length) {
      const rest = seg.slice(i);
      const inStats = S.tab === 'stats';
      if (rest.startsWith('\x1b[Z')) { if (inStats) moveFocus(-1); i += 3; continue; }
      if (rest.startsWith('\x1b[A')) { moveV(-1); i += 3; continue; }
      if (rest.startsWith('\x1b[B')) { moveV(1); i += 3; continue; }
      // ←→ switch tabs, except: grid cell (moves cell), JD⇄▶ (side by side),
      // text fields, and a selected list row with a link (row ⇄ link)
      if (rest.startsWith('\x1b[C')) {
        if (S.tab === 'activity' && S.gridSel) moveGridSel(1, 0);
        else if (inStats && S.focus === 'jd') S.focus = 'play';
        else if (inStats && S.focus === 'list') {
          const id = S.sessionIds[S.listIdx];
          if (id && S.snap.urls.get(id)) S.rowFocus = 'link';
        }
        else if (!typing()) cycleTab(1);
        i += 3; continue;
      }
      if (rest.startsWith('\x1b[D')) {
        if (S.tab === 'activity' && S.gridSel) moveGridSel(-1, 0);
        else if (inStats && S.focus === 'play') { S.focus = 'jd'; S.lastInput = 'jd'; }
        else if (inStats && S.focus === 'list' && S.rowFocus === 'link') S.rowFocus = 'row';
        else if (!typing()) cycleTab(-1);
        i += 3; continue;
      }
      if (rest.startsWith('\x1b[5~')) { if (inStats && S.focus === 'list') moveSel(-(S.listWin || 5)); i += 4; continue; } // PgUp
      if (rest.startsWith('\x1b[6~')) { if (inStats && S.focus === 'list') moveSel(S.listWin || 5); i += 4; continue; }    // PgDn
      if (rest.startsWith('\x1b[')) { // any other CSI: skip to its final byte
        let j = 2;
        while (j < rest.length && !/[@-~]/.test(rest[j])) j++;
        i += j + 1;
        continue;
      }
      const ch = rest[0];
      if (ch === '\x1b') { // Esc: clear focused field, then blur / leave the grid cursor
        if (inStats) {
          if (S.focus === 'jd' && S.jd) S.jd = '';
          else if (S.focus === 'url' && S.url) S.url = '';
          else S.focus = 'none';
        } else if (S.tab === 'activity') {
          S.gridSel = false;
        } else {
          S.fuIn = false;
        }
        i++;
        continue;
      }
      if (ch === '\x03') { quit(); return; }
      if (ch === '\t') {
        if (inStats) moveFocus(1);
        else if (S.tab === 'activity') S.gridSel = !S.gridSel;
        else S.fuIn = !S.fuIn;
        i++; continue;
      }
      if (ch === '\r' || ch === '\n') {
        if (inStats) onEnter();
        else if (S.tab === 'activity') S.gridSel = !S.gridSel;
        else if (S.fuIn) markNudged(bump);
        else S.fuIn = true;
        i++; continue;
      }
      if (ch === '\x7f' || ch === '\b') {
        if (inStats && S.focus === 'jd') S.jd = S.jd.slice(0, -1);
        if (inStats && S.focus === 'url') S.url = S.url.slice(0, -1);
        i++;
        continue;
      }
      if (ch >= ' ') onChar(ch);
      i++;
    }
  };

  return chunk => {
    raw += chunk.toString('utf8');
    while (raw.length) {
      if (pasting) {
        const end = raw.indexOf(PASTE_END);
        if (end === -1) {
          const keep = partialTail(raw, PASTE_END);
          pasteBuf += raw.slice(0, raw.length - keep);
          raw = raw.slice(raw.length - keep);
          break;
        }
        pasteBuf += raw.slice(0, end);
        raw = raw.slice(end + PASTE_END.length);
        pasting = false;
        onPaste(pasteBuf);
        pasteBuf = '';
        continue;
      }
      const start = raw.indexOf(PASTE_START);
      if (start === 0) { pasting = true; raw = raw.slice(PASTE_START.length); continue; }
      const keep = start === -1 ? partialTail(raw, PASTE_START) : raw.length - start;
      const seg = raw.slice(0, raw.length - keep);
      raw = raw.slice(raw.length - keep);
      if (seg) handleKeys(seg);
      if (start !== 0 && start !== -1) continue;
      break;
    }
    // a chunk ending in exactly ESC is a real Esc keypress, not a partial
    // sequence — terminals send multi-byte sequences in one chunk
    if (raw === '\x1b') { handleKeys(raw); raw = ''; }
    bump();
  };
}

// ── UI ───────────────────────────────────────────────────────────────────────

const SPINNER = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const GOLD = ['#b8860b', '#d4af37', '#ffd700', '#ffe766', '#fff2a8', '#ffe766', '#ffd700', '#d4af37'];
// 1-1.9 deep red · 2-2.9 orange · 3-3.9 yellow · 4-4.9 green · 5.0 gold (below, via ScoreText)
const scoreColor = s => s < 2 ? '#d70000' : s < 3 ? '#ff8700' : s < 4 ? 'yellow' : 'green';
function ScoreText({ score }) {
  const t = score.toFixed(1);
  if (score < 5) return h(Text, { color: scoreColor(score) }, `  ${t}`);
  S.hasGold = true; // keeps the animation timer ticking while a 5.0 is on screen
  return h(Text, { bold: true }, '  ',
    ...[...t].map((c, i) => h(Text, { key: i, color: GOLD[(S.frame + i) % GOLD.length] }, c)));
}
const osc8 = (text, url) => `\u001B]8;;${url}\u0007${text}\u001B]8;;\u0007`;

function StatRow({ label, value, indent, color }) {
  return h(Box, { justifyContent: 'space-between' },
    h(Text, { dimColor: !!indent }, `${indent ? ' └ ' : ''}${label}`),
    h(Text, { bold: !indent, color }, String(value)));
}

function StatsPanel() {
  const { stats, avg, queueIds, runner } = S.snap;
  // urgent/overdue only — 'waiting'/'cold' entries aren't actionable today
  const due = dueFollowups();
  return h(Box, { flexDirection: 'column', width: 20, paddingX: 1, marginRight: 1, borderStyle: 'single', borderTop: false, borderBottom: false, borderLeft: false },
    h(Text, { bold: true, underline: true }, 'Stats'),
    h(Box, { height: 1 }),
    h(StatRow, { label: 'Queue', value: queueIds.length }),
    h(StatRow, { label: 'Active', value: runner ? 1 : 0 }),
    h(StatRow, { label: 'Failed', value: stats.failed }),
    h(StatRow, { label: 'Completed', value: stats.completed }),
    h(StatRow, { label: 'CV', value: stats.cv, indent: true }),
    h(StatRow, { label: 'AVG', value: avg == null ? '—' : avg.toFixed(1), indent: true }),
    h(StatRow, { label: 'Rate', value: stats.rate == null ? '—' : `${Math.round(stats.rate * 100)}%`, indent: true }),
    h(StatRow, { label: 'P1-gated', value: stats.p1Gated }),
    h(Box, { height: 1 }),
    h(StatRow, { label: 'Follow-ups', value: due == null ? '—' : due.length, color: due?.length ? 'yellow' : undefined }),
    ...(due || []).slice(0, 3).map((e, i) =>
      h(StatRow, { key: i, label: e.company.slice(0, 9), value: `${e.daysSinceApplication}d`, indent: true })));
}

// One terminal line per row, always: suffixes get their width reserved and the
// label is truncated to what's left, so the windowing math stays exact.
function QueueRow({ id, selected, width }) {
  const info = itemInfo(id);
  const sel = selected && S.focus === 'list';
  const linkSel = sel && S.rowFocus === 'link';
  const icon =
    info.kind === 'done' ? h(Text, { color: 'green' }, '✓') :
    info.kind === 'failed' ? h(Text, { color: 'red' }, '✗') :
    info.kind === 'running' ? h(Text, { color: 'cyan' }, SPINNER[S.frame % SPINNER.length]) :
    info.kind === 'waiting' ? h(Text, { dimColor: true }, '○') :
    h(Text, { dimColor: true }, '·');
  const suffix = []; // [visible text, props]
  if (S.applied.has(id)) suffix.push(['  ✉', { color: 'cyan' }]);
  if (S.skipped.has(id)) suffix.push(['  ⊘', { dimColor: true }]);
  if (info.kind === 'running') suffix.push([`  ${info.phase}…`, { dimColor: true }]);
  if (info.kind === 'waiting') suffix.push(['  waiting', { dimColor: true }]);
  if (info.kind === 'pending') suffix.push(['  pending (press ▶)', { dimColor: true }]);
  if (info.note) suffix.push([`  ${info.note}`, { dimColor: true }]);
  if (info.err) suffix.push([`  ${info.err.slice(0, 60)}`, { color: 'red' }]);
  const url = S.snap.urls.get(id); // result folder stays on the o hotkey
  const scoreLen = info.score != null ? 5 : 0; // '  X.X'
  const suffixLen = scoreLen + suffix.reduce((a, [t]) => a + t.length, 0) + (url ? 6 : 9); // '  link' / '  no link'
  const avail = Math.max(4, width - 2 - suffixLen); // 2 = icon + leading space
  const label = info.label.length > avail ? info.label.slice(0, avail - 1) + '…' : info.label;
  return h(Box, { height: 1, overflow: 'hidden' },
    icon,
    h(Text, { inverse: sel }, ` ${label}`),
    info.score != null ? h(ScoreText, { score: info.score }) : null,
    ...suffix.map(([t, p], i) => h(Text, { key: i, ...p }, t)),
    // spaces outside the underlined span — underline starts at "link"
    url ? h(Text, null, '  ', h(Text, { color: 'blueBright', underline: true, inverse: linkSel }, osc8('link', url)))
        : h(Text, { dimColor: true }, '  no link'));
}

// Renders at most `height` rows regardless of queue length: a window of items
// plus "N more" indicators, so the frame never outgrows the terminal.
function QueueList({ height, width }) {
  const n = S.sessionIds.length;
  if (!n) return h(Text, { dimColor: true }, 'Nothing yet — paste a JD below.');
  if (n <= height) {
    S.listWin = n;
    S.scroll = 0;
    return h(React.Fragment, null,
      S.sessionIds.map((id, i) => h(QueueRow, { key: id, id, width, selected: i === S.listIdx })));
  }
  const win = Math.max(1, height - 2); // reserve the indicator lines
  S.listWin = win;
  const maxScroll = n - win;
  if (S.focus === 'list') { // keep the selection visible
    S.scroll = Math.min(Math.max(S.scroll, S.listIdx - win + 1), S.listIdx);
    S.scroll = Math.min(Math.max(S.scroll, 0), maxScroll);
  } else {
    S.scroll = maxScroll; // not navigating — follow the latest additions
  }
  const below = maxScroll - S.scroll;
  return h(React.Fragment, null,
    h(Text, { dimColor: true }, S.scroll > 0 ? `↑ ${S.scroll} more` : ' '),
    S.sessionIds.slice(S.scroll, S.scroll + win)
      .map((id, i) => h(QueueRow, { key: id, id, width, selected: S.scroll + i === S.listIdx })),
    h(Text, { dimColor: true }, below > 0 ? `↓ ${below} more` : ' '));
}

// ── Activity tab ─────────────────────────────────────────────────────────────

const GREENS = ['#064D00', '#0EA300', '#15FF00', '#93FF8A'];

// One grid cell, `width`×`height` terminal cells (last line is a gap row unless
// `solid`). count null = outside the period. The max cell gets the same
// animated gold as a 5.0 score. The grid cursor renders as ◆/◇.
function Cell({ count, max, seed, width, height = 1, line = 0, solid = false, selected }) {
  const pad = ' '.repeat(width);
  if (count == null) return h(Text, null, pad);
  const content = solid ? height : (height > 1 ? height - 1 : 1);
  if (line >= content) return h(Text, null, pad); // gap row between cell rows
  if (!count) {
    // empty days are squares too — dim ANSI gray adapts to the terminal theme.
    // Checkerboard via glyph shade (█ vs ▓) so adjacent cells stay distinguishable
    // when narrow widths fuse them together; ▓ lets the background bleed through
    // slightly, which keeps the pattern subtle on any theme.
    const g = (seed % 2 ? '▓' : '█').repeat(Math.max(1, width - 1)).padEnd(width);
    return selected
      ? h(Text, { bold: true }, '◇'.repeat(Math.max(1, width - 1)).padEnd(width))
      : h(Text, { color: 'gray', dimColor: true }, g);
  }
  const block = (selected ? '◆' : '█').repeat(Math.max(1, width - 1)).padEnd(width);
  if (count === max) {
    S.hasGold = true;
    return h(Text, { color: GOLD[(S.frame + seed) % GOLD.length] }, block);
  }
  return h(Text, { color: GREENS[Math.min(3, Math.floor((count / max) * 4)) ] }, block);
}

function Legend({ gutter }) {
  return h(Box, null,
    h(Text, null, ' '.repeat(gutter)),
    h(Text, { dimColor: true }, 'less '),
    h(Text, { color: 'gray', dimColor: true }, '█ '),
    ...GREENS.map((g, i) => h(Text, { key: i, color: g }, '█ ')),
    h(Text, { color: '#ffd700' }, '█'),
    h(Text, { dimColor: true }, ' more (gold = top)'));
}

function TabBar() {
  const due = dueFollowups()?.length || 0;
  // bold+cyan marks the CURRENT view ("you are here") — deliberately not
  // underlined, so it doesn't read as a focused menu selection. Widget focus
  // starts at 'none'; Tab begins it.
  const tab = (t, label) => h(Text, {
    bold: S.tab === t,
    color: S.tab === t ? 'cyan' : undefined, dimColor: S.tab !== t,
  }, label);
  return h(Box, { justifyContent: 'center', gap: 4 },
    tab('stats', '1 QUEUE'), tab('activity', '2 ACTIVITY'),
    h(Box, null, tab('followups', '3 FOLLOW-UPS'),
      due ? h(Text, { color: 'yellow' }, ` (${due})`) : null));
}

// ── Follow-ups tab ───────────────────────────────────────────────────────────

function FuRow({ e, selected, maxNudges }) {
  const due = e.urgency === 'urgent' || e.urgency === 'overdue';
  const icon =
    e.urgency === 'urgent' ? h(Text, { color: 'red', bold: true }, '!') :
    e.urgency === 'overdue' ? h(Text, { color: 'yellow' }, '●') :
    e.urgency === 'cold' ? h(Text, { dimColor: true }, '✕') :
    h(Text, { dimColor: true }, '○');
  const when = due ? `due now — applied ${e.daysSinceApplication}d ago`
    : e.urgency === 'cold' ? 'max nudges sent'
    : e.daysUntilNext != null ? `next in ${e.daysUntilNext}d` : '';
  return h(Box, { height: 1, overflow: 'hidden' },
    icon,
    h(Text, { inverse: selected, wrap: 'truncate' }, ` ${e.company} — ${e.role} `),
    h(Text, { dimColor: true, wrap: 'truncate' },
      ` ${e.status} · nudges ${e.followupCount}/${maxNudges}`),
    h(Text, { color: due ? 'yellow' : undefined, dimColor: !due, wrap: 'truncate' }, ` · ${when}`));
}

function FollowupsTab() {
  const fu = S.followups;
  const maxNudges = fu?.cadenceConfig?.applied_max_followups ?? 2;
  return h(Box, { flexDirection: 'column', flexGrow: 1, paddingX: 1 },
    h(Text, { bold: true, underline: true }, 'Follow-ups'),
    h(Box, { height: 1 }),
    !fu ? h(Text, { dimColor: true }, 'Loading…')
      : !fu.entries.length
        ? h(Text, { dimColor: true }, 'No active applications to follow up — mark items applied (a) on the Queue tab first.')
        : h(Box, { flexDirection: 'column', flexGrow: 1, overflow: 'hidden' },
            fu.entries.map((e, i) => h(FuRow, { key: e.num, e, selected: S.fuIn && i === S.fuIdx, maxNudges }))),
    h(Box, { height: 1 }),
    h(Text, { dimColor: true }, 'Enter records a follow-up in data/follow-ups.md and resets its clock.'));
}

// "▸ 16 Jul — 2 scans" for the grid cursor — right under the labels, next to the grid
function SelReadout({ count }) {
  if (!S.gridSel) return null;
  const d = S.actDate;
  const when = S.actView === 'day'
    ? `${String(S.actHr).padStart(2, '0')}:00–${String((S.actHr + 1) % 24).padStart(2, '0')}:00, ${d.getDate()} ${MONTHS[d.getMonth()]}`
    : `${d.getDate()} ${MONTHS[d.getMonth()]} ${d.getFullYear()}`;
  return h(Box, { marginLeft: 4 }, // sits inline after the legend
    h(Text, { color: 'cyan', bold: true }, '▸ '),
    h(Text, null, `${when} — `),
    h(Text, { bold: true }, String(count)),
    h(Text, { dimColor: true }, ` ${(S.actType === 'scans' ? 'scans' : 'applications').replace(/s$/, count === 1 ? '' : 's')}`));
}

// Cell width AND height scale with the terminal so the grid fills the window
// (terminal chars are ~2:1 tall, so 3-wide × 2-tall cells read as squares).
function ActivityGrid({ b, width, height }) {
  const d0 = S.actDate;
  if (S.actView === 'day') { // 24 hourly bars
    const cw = Math.max(2, Math.min(5, Math.floor(width / 24)));
    const barH = Math.max(1, Math.min(4, Math.floor((height - 4) / 3)));
    const k = dayKey(d0);
    const counts = Array.from({ length: 24 }, (_, hr) => b.hours.get(`${k}:${hr}`) || 0);
    const max = Math.max(...counts);
    const bar = li => h(Box, { key: li }, counts.map((c, hr) =>
      h(Cell, { key: hr, count: c, max, seed: hr, width: cw, height: barH, line: li, solid: true, selected: S.gridSel && hr === S.actHr })));
    return h(Box, { flexDirection: 'column' },
      ...Array.from({ length: barH }, (_, li) => bar(li)),
      h(Text, { dimColor: true }, ['00', '06', '12', '18'].map(t => t.padEnd(6 * cw)).join('')),
      h(Box, { marginTop: 1 }, h(SelReadout, { count: counts[S.actHr] })));
  }
  const year = d0.getFullYear();
  const inRange = dt => S.actView === 'year'
    ? dt.getFullYear() === year
    : dt.getFullYear() === year && dt.getMonth() === d0.getMonth();
  const first = S.actView === 'year' ? new Date(year, 0, 1) : new Date(year, d0.getMonth(), 1);
  const last = S.actView === 'year' ? new Date(year, 11, 31) : new Date(year, d0.getMonth() + 1, 0);
  const cols = [];
  for (let w = mondayOf(first); w <= last; w = addDays(w, 7)) cols.push(w);
  const GUTTER = 4; // weekday labels on the left
  const cellW = S.actView === 'year'
    ? Math.max(1, Math.min(3, Math.floor((width - GUTTER) / cols.length)))
    : Math.max(3, Math.min(8, Math.floor((width - GUTTER) / cols.length)));
  const cellH = Math.max(1, Math.min(3, Math.floor((height - 5) / 7)));
  const contentRows = cellH > 1 ? cellH - 1 : 1;
  const midLine = Math.floor((contentRows - 1) / 2);
  const WEEKDAYS = ['Mon', '', 'Wed', '', 'Fri', '', ''];
  let max = 0;
  for (const w of cols) for (let r = 0; r < 7; r++) {
    const dt = addDays(w, r);
    if (inRange(dt)) max = Math.max(max, b.days.get(dayKey(dt)) || 0);
  }
  const rows = [];
  for (let r = 0; r < 7; r++) for (let li = 0; li < cellH; li++) {
    rows.push(h(Box, { key: `${r}.${li}` },
      h(Text, { dimColor: true }, (li === midLine ? WEEKDAYS[r] : '').padEnd(GUTTER)),
      ...cols.map((w, ci) => {
        const dt = addDays(w, r);
        return h(Cell, {
          key: ci, max, seed: ci + r, width: cellW, height: cellH, line: li,
          count: inRange(dt) ? (b.days.get(dayKey(dt)) || 0) : null,
          selected: S.gridSel && dayKey(dt) === dayKey(d0),
        });
      })));
  }
  // labels under the grid: month names (year) / day-of-month of each week's Monday (month).
  // A week "belongs" to the month of its Thursday (majority of its days).
  const wkMonth = w => addDays(w, 3).getMonth();
  const label = [];
  cols.forEach((w, ci) => {
    const x = ci * cellW;
    if (S.actView === 'year') {
      // label only weeks whose Thursday is inside the displayed year — edge weeks
      // belong to Dec of the previous / Jan of the next year and must stay unlabeled
      if (addDays(w, 3).getFullYear() === year && (ci === 0 || wkMonth(w) !== wkMonth(cols[ci - 1]))) {
        const name = MONTHS[wkMonth(w)];
        for (let j = 0; j < name.length; j++) label[x + j] = name[j];
      }
    } else {
      const day = String(w.getDate()).padStart(2);
      label[x] = day[0]; label[x + 1] = day[1];
    }
  });
  return h(Box, { flexDirection: 'column' }, ...rows,
    h(Text, { dimColor: true, wrap: 'truncate' }, ' '.repeat(GUTTER) + Array.from(label, c => c || ' ').join('')),
    h(Box, { marginTop: 1 },
      h(Legend, { gutter: GUTTER }),
      h(SelReadout, { count: b.days.get(dayKey(d0)) || 0 })));
}

function streakOf(days) {
  let d = new Date(), n = 0;
  if (!days.get(dayKey(d))) d = addDays(d, -1); // today still pending doesn't break it
  while (days.get(dayKey(d))) { n++; d = addDays(d, -1); }
  return n;
}

function ActivityTab({ width }) {
  const ref = React.useRef(null);
  const [, force] = React.useState(0);
  React.useEffect(() => { // measured, not chrome math — footer/labels can wrap
    if (ref.current) {
      const { height } = measureElement(ref.current);
      if (height && height !== S.gridH) { S.gridH = height; force(n => n + 1); }
    }
  });
  if (!S.act) S.act = activityBuckets();
  const b = S.act[S.actType === 'scans' ? 'scans' : 'apps'];
  const d0 = S.actDate;
  const now = new Date();
  const periodLabel = S.actView === 'year' ? String(d0.getFullYear())
    : S.actView === 'month' ? `${MONTHS[d0.getMonth()]} ${d0.getFullYear()}`
    : `${d0.getDate()} ${MONTHS[d0.getMonth()]} ${d0.getFullYear()}`;
  // period totals + best day of the visible period
  const prefix = S.actView === 'year' ? `${d0.getFullYear()}-`
    : S.actView === 'month' ? dayKey(d0).slice(0, 8) : dayKey(d0);
  let best = null;
  for (const [k, v] of b.days) {
    if (k.startsWith(prefix) && (!best || v > best[1])) best = [k, v];
  }
  const sumBy = p => [...b.days].reduce((a, [k, v]) => a + (k.startsWith(p) ? v : 0), 0);
  const today = b.days.get(dayKey(now)) || 0;
  const thisMonth = sumBy(dayKey(now).slice(0, 8));
  const total = [...b.days.values()].reduce((a, v) => a + v, 0);
  const scanTotal = [...S.act.scans.days.values()].reduce((a, v) => a + v, 0);
  const appTotal = [...S.act.apps.days.values()].reduce((a, v) => a + v, 0);
  const typeRow = t => h(Text, {
    bold: S.actType === t, color: S.actType === t ? 'cyan' : undefined, dimColor: S.actType !== t,
  }, `${S.actType === t ? '>' : ' '} ${t === 'scans' ? 'scans' : 'applications'}`);
  const stat = (l, v) => h(Text, null, h(Text, { dimColor: true }, `${l}: `), String(v));
  const bestLabel = best
    ? `${Number(best[0].slice(8, 10))} ${MONTHS[Number(best[0].slice(5, 7)) - 1]} (${best[1]})`
    : '—';
  return h(Box, { flexDirection: 'column', flexGrow: 1, paddingX: 1 },
    h(Text, { bold: true }, h(Text, { dimColor: true }, '‹ '), periodLabel, h(Text, { dimColor: true }, ' ›')),
    h(Box, { ref, flexDirection: 'column', flexGrow: 1, justifyContent: 'center', overflow: 'hidden' }, // grid centered in the free space
      // don't paint until measured — the grid appears at its final size, no resize flicker
      S.gridH ? h(ActivityGrid, { b, width, height: S.gridH }) : null),
    h(Box, { gap: 6 },
      h(Box, { flexDirection: 'column' }, typeRow('scans'), typeRow('apps')),
      h(Box, { flexDirection: 'column' },
        stat('Today', today), stat('This month', thisMonth), stat('Total', total)),
      h(Box, { flexDirection: 'column' },
        stat('Streak', `${streakOf(b.days)}d`),
        h(Text, null, h(Text, { dimColor: true }, 'Best: '), h(Text, { color: GOLD[S.frame % GOLD.length] }, bestLabel)),
        stat('Rate', scanTotal ? `${Math.round((appTotal / scanTotal) * 100)}%` : '—'))));
}

function InputArea() {
  const jdFocus = S.focus === 'jd', urlFocus = S.focus === 'url';
  const addFocus = S.focus === 'add', playFocus = S.focus === 'play';
  const firstLine = S.jd.split('\n')[0].slice(0, 34);
  const playBusy = S.drainActive || S.snap.runner;
  // cursor sits before the placeholder, not after it — placeholder is just a hint
  const jdText = S.jd
    ? h(Text, { wrap: 'truncate' }, `${S.jd.length.toLocaleString()} chars — ${firstLine}`, jdFocus ? '▏' : '')
    : h(Text, { wrap: 'truncate' }, jdFocus ? '▏' : '', h(Text, { dimColor: true }, 'Paste the Job Description — or type /scan'));
  return h(Box, { flexDirection: 'column' },
    h(Box, null,
      h(Box, { borderStyle: 'round', borderColor: jdFocus ? 'cyan' : 'gray', flexGrow: 1, paddingX: 1 }, jdText),
      h(Box, { borderStyle: 'round', borderColor: playFocus ? 'cyan' : 'gray', paddingX: 1, marginLeft: 1 },
        playBusy
          ? h(Text, { color: 'cyan' }, SPINNER[S.frame % SPINNER.length])
          : h(Text, { color: 'green', bold: true }, '▶'))),
    h(Box, { paddingX: 1 },
      h(Text, null, 'URL (optional): ',
        S.url ? h(Text, null, S.url) : (urlFocus ? '' : h(Text, { dimColor: true }, '—')),
        urlFocus ? '▏' : '')),
    h(Box, { borderStyle: 'round', borderColor: addFocus ? 'cyan' : 'gray', paddingX: 1, alignSelf: 'flex-start' },
      h(Text, { bold: addFocus, color: addFocus ? 'cyan' : undefined }, 'Add to queue')));
}

function App() {
  const [, setTick] = React.useState(0);
  const bump = React.useCallback(() => setTick(t => t + 1), []);

  const [size, setSize] = React.useState({ cols: process.stdout.columns || 80, rows: process.stdout.rows || 24 });
  const listRef = React.useRef(null);
  const [listDim, setListDim] = React.useState({ h: 1, w: 60 });
  React.useEffect(() => { // measure the real list box after layout — no chrome math
    if (listRef.current) {
      const { height, width } = measureElement(listRef.current);
      if (height && width && (height !== listDim.h || width !== listDim.w)) setListDim({ h: height, w: width });
    }
  });

  React.useEffect(() => {
    poll();
    refreshFollowups(bump);
    const quit = () => {
      process.stdout.write('\x1b[?2004l\x1b[?1049l'); // paste mode off, leave alt screen
      process.stdin.setRawMode(false);
      process.exit(0);
    };
    const onData = makeStdinHandler(bump, quit);
    const onResize = () => {
      if (S.tab !== 'activity') S.gridH = 0; // stale cache — remeasure on next Activity visit
      setSize({ cols: process.stdout.columns || 80, rows: process.stdout.rows || 24 });
    };
    process.stdin.setRawMode(true);
    process.stdin.resume();
    process.stdin.on('data', onData);
    process.stdout.on('resize', onResize);
    process.stdout.write('\x1b[?2004h'); // bracketed paste on
    const pollTimer = setInterval(() => { poll(); bump(); }, 1000);
    const followupTimer = setInterval(() => refreshFollowups(bump), 10 * 60 * 1000);
    const spinTimer = setInterval(() => {
      if (S.drainActive || S.snap.runner || S.busy || S.hasGold) { S.frame++; bump(); }
    }, 120);
    return () => {
      clearInterval(pollTimer);
      clearInterval(spinTimer);
      clearInterval(followupTimer);
      process.stdin.off('data', onData);
      process.stdout.off('resize', onResize);
      process.stdout.write('\x1b[?2004l\x1b[?1049l');
    };
  }, [bump]);

  S.hasGold = false; // re-set by any gold cell/score rendered below
  const body = S.tab === 'stats'
    ? h(Box, { borderStyle: 'round', flexDirection: 'row', paddingX: 1, flexGrow: 1 },
        h(StatsPanel),
        h(Box, { flexDirection: 'column', flexGrow: 1 },
          h(Text, { bold: true, underline: true }, 'Queue'),
          h(Box, { height: 1 }),
          h(Box, { ref: listRef, flexDirection: 'column', flexGrow: 1, overflow: 'hidden' },
            h(QueueList, { height: Math.max(1, listDim.h), width: Math.max(20, listDim.w) })),
          h(InputArea)))
    : S.tab === 'activity'
      ? h(Box, { borderStyle: 'round', flexDirection: 'row', paddingX: 1, flexGrow: 1 },
          h(ActivityTab, { width: Math.max(40, size.cols - 8) }))
      : h(Box, { borderStyle: 'round', flexDirection: 'row', paddingX: 1, flexGrow: 1 },
          h(FollowupsTab));
  const hints = S.tab === 'stats'
    ? '←→ tabs/link · ↑↓ navigate · Enter next/queue/run/link · o result · a applied · x skip · q quit'
    : S.tab === 'followups'
      ? '←→ tabs · ↑↓ navigate · Enter mark nudged · u undo · o report · q quit'
      : S.gridSel
        ? (S.actView === 'day' ? '←→ hour · ↑/Esc leave · ‹› period · j/k type · q quit'
          : '←→ week · ↑↓ day · ↑top/Esc leave · ‹› period · j/k type · q quit')
        : '←→ tabs · ↓ or Tab grid · y/m/d view · ‹› period · j/k type · q quit';
  return h(Box, { flexDirection: 'column', width: size.cols, height: size.rows },
    h(TabBar),
    body,
    h(Box, { paddingX: 2, justifyContent: 'space-between' },
      h(Text, { dimColor: true, wrap: 'truncate' }, hints),
      h(Text, { color: S.msgIsError ? 'red' : 'yellow', wrap: 'truncate' }, S.msg)));
}

// ── entry ────────────────────────────────────────────────────────────────────

if (process.argv.includes('--stats')) {
  // ponytail: self-check — same readers the TUI renders from, no TTY needed
  const rows = readStateRows();
  const stats = lifetimeStats(rows);
  console.log(JSON.stringify({
    queue: readQueueIds().length,
    recent24h: recentIds().length,
    active: runnerActive() ? 1 : 0,
    ...stats,
    avg: lifetimeAvg(),
  }, null, 2));
  process.exit(0);
}

if (!process.stdin.isTTY || !process.stdout.isTTY) {
  console.error('snipe-tui needs an interactive terminal (or run with --stats).');
  process.exit(1);
}
if (!fs.existsSync(SNIPE)) {
  console.error(`snipe not found at ${SNIPE}`);
  process.exit(1);
}

process.stdout.write('\x1b[?1049h'); // alt screen: own the terminal, restore shell on exit
render(h(App), { exitOnCtrlC: false });
