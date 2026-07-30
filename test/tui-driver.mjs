// test/tui-driver.mjs — runs snipe-tui.mjs headlessly and captures what it drew.
//
// The TUI refuses to start without a TTY on both ends, and it reads keys off raw
// stdin rather than through Ink's useInput, so there is no in-process API to
// drive it with. Faking the two TTYs and writing key bytes into stdin is what
// makes its render and key-handling paths reachable from a test at all.
//
// Usage: node test/tui-driver.mjs <out-file> <keys...>
//   Each key argument is either literal text or one of the \x1b-escape aliases
//   below. Frames are captured in order and written to <out-file> as JSON.

import { PassThrough } from 'node:stream';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const KEYS = {
  UP: '\x1b[A', DOWN: '\x1b[B', RIGHT: '\x1b[C', LEFT: '\x1b[D',
  ENTER: '\r', ESC: '\x1b', TAB: '\t', SHIFTTAB: '\x1b[Z',
};

const [outFile, ...keySpec] = process.argv.slice(2);

const stdin = new PassThrough();
stdin.isTTY = true;
stdin.setRawMode = () => stdin;
stdin.ref = () => stdin;
stdin.unref = () => stdin;
Object.defineProperty(process, 'stdin', { value: stdin, configurable: true });

process.stdout.isTTY = true;
process.stdout.columns = Number(process.env.TUI_COLS || 140);
process.stdout.rows = Number(process.env.TUI_ROWS || 34);
// Faking isTTY is not enough: chalk decides its colour level when Ink imports
// it, and off a pipe that level is 0 — every colour and the `inverse` used to
// mark the focused row would vanish from the frames, which is precisely what
// the focus assertions read.
process.env.FORCE_COLOR = process.env.FORCE_COLOR || '3';

// Capture instead of print: the parent reads frames from the file, so stdout
// staying clean keeps the driver usable from a test runner.
const frames = [];
const realWrite = process.stdout.write.bind(process.stdout);
process.stdout.write = chunk => { frames.push(String(chunk)); return true; };

const flush = code => {
  process.stdout.write = realWrite;
  try { writeFileSync(outFile, JSON.stringify(frames)); } catch {}
  process.exit(code);
};

// The TUI calls process.exit() on 'q'; that must still flush what it drew.
const realExit = process.exit.bind(process);
process.exit = code => { process.exit = realExit; flush(code || 0); };

const ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
await import(resolve(ROOT, 'snipe-tui.mjs'));

// Ink needs a beat to mount before the first key lands, and a beat between keys
// so each render settles — without it the whole sequence collapses into one frame.
let t = 300;
for (const spec of keySpec) {
  const bytes = KEYS[spec] ?? spec;
  setTimeout(() => stdin.write(bytes), t);
  t += 140;
}
setTimeout(() => flush(0), t + 500);
