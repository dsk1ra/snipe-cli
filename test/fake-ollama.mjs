// test/fake-ollama.mjs — a stand-in Ollama server for the phase scripts.
//
// Phase 1/2/3 are CLI scripts with no exported internals: the only way to reach
// their logic is to run them. They all take `--ollama-url`, so pointing that at
// a local server is enough to exercise the real code paths end-to-end without a
// model — and because c8 works through NODE_V8_COVERAGE, a spawned script is
// counted just like an imported one.
//
// The chat handler reads the JSON Schema the caller passes as `format` and
// synthesises a conforming answer, so it needs no per-stage knowledge and does
// not drift when a schema changes. Callers can still override single fields via
// `fieldValues` when a test needs a specific score or verdict.

import http from 'node:http';
import { createHash } from 'node:crypto';

/** Stable pseudo-random float in [0,1) from a string — same text, same value. */
function h(str, salt = '') {
  const d = createHash('sha256').update(salt + str).digest();
  return d.readUInt32BE(0) / 2 ** 32;
}

/**
 * A deterministic unit-ish vector for `text`. Real embeddings are not needed —
 * only that the same text always maps to the same vector and different texts
 * land at different angles, which is all cosine/topK ranking depends on.
 */
export function fakeVector(text, dim = 32) {
  return Array.from({ length: dim }, (_, i) => h(text, `v${i}:`) * 2 - 1);
}

/**
 * Build a value satisfying `schema`. Supports the JSON Schema subset the phase
 * scripts actually use: object/array/string/number/integer/boolean, enum,
 * required, minItems/maxItems, minimum/maximum.
 *
 * @param {any} schema
 * @param {{ fieldValues?: Record<string, any>, key?: string, seed?: string }} opts
 *   `fieldValues` overrides any property by name, at any depth.
 */
export function sampleFromSchema(schema, opts = {}) {
  const { fieldValues = {}, key = '', seed = '' } = opts;
  if (!schema || typeof schema !== 'object') return null;
  if (key && Object.prototype.hasOwnProperty.call(fieldValues, key)) return fieldValues[key];

  if (Array.isArray(schema.enum)) {
    return schema.enum[Math.floor(h(key + seed, 'e') * schema.enum.length)];
  }

  switch (schema.type) {
    case 'object': {
      const out = {};
      // `required` is advisory here — emit every declared property, which is what
      // a schema-constrained model does and what the callers' normalisers assume.
      for (const [name, sub] of Object.entries(schema.properties || {})) {
        out[name] = sampleFromSchema(sub, { fieldValues, key: name, seed: seed + key });
      }
      return out;
    }
    case 'array': {
      const n = Math.max(schema.minItems ?? 1, 1);
      return Array.from({ length: Math.min(n, schema.maxItems ?? n) }, (_, i) =>
        sampleFromSchema(schema.items, { fieldValues, key, seed: `${seed}${key}${i}` }));
    }
    case 'boolean':
      return h(key + seed, 'b') > 0.5;
    case 'integer':
    case 'number': {
      const lo = schema.minimum ?? 1, hi = schema.maximum ?? 5;
      const v = lo + h(key + seed, 'n') * (hi - lo);
      return schema.type === 'integer' ? Math.round(v) : Math.round(v * 10) / 10;
    }
    default:
      return `${key || 'value'}-${seed || '0'}`;
  }
}

/**
 * Start the fake server on an ephemeral port.
 *
 * @param {object} [opts]
 * @param {string[]} [opts.models] names `/api/tags` reports as installed
 * @param {Record<string, any>} [opts.fieldValues] per-field overrides for chat answers
 * @param {(body: any) => any} [opts.onChat] full override — return the parsed
 *   answer object, a string (sent verbatim as the content), or null to fall
 *   through to the schema sampler
 * @param {number} [opts.chatStatus] HTTP status for `/api/chat`, to test failures
 * @returns {Promise<{ url: string, calls: any[], close: () => Promise<void> }>}
 */
export async function startFakeOllama(opts = {}) {
  const {
    models = ['snipe-screen:latest', 'snipe-eval:latest', 'snipe-cv:latest', 'snipe-embed:latest'],
    fieldValues = {},
    onChat = null,
    chatStatus = 200,
  } = opts;

  const calls = [];

  const server = http.createServer((req, res) => {
    let raw = '';
    req.on('data', c => { raw += c; });
    req.on('end', () => {
      const body = raw ? JSON.parse(raw) : {};
      calls.push({ path: req.url, body });
      const send = (code, obj) => {
        res.writeHead(code, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(obj));
      };

      if (req.url === '/api/tags') {
        return send(200, { models: models.map(name => ({ name })) });
      }
      if (req.url === '/api/show') {
        return send(200, {
          details: { parent_model: 'fake-base', parameter_size: '0.6B', quantization_level: 'Q8_0' },
        });
      }
      if (req.url === '/api/embed') {
        const input = Array.isArray(body.input) ? body.input : [body.input];
        return send(200, { embeddings: input.map(t => fakeVector(String(t))) });
      }
      // Phase 3 warms the model with /api/ps and uses the older /api/generate;
      // Phases 1-2 use /api/chat. Same synthesised answer, two envelopes.
      if (req.url === '/api/ps') {
        return send(200, { models: models.map(name => ({ name })) });
      }
      if (req.url === '/api/chat' || req.url === '/api/generate') {
        if (chatStatus !== 200) return send(chatStatus, { error: 'fake failure' });
        let answer = onChat ? onChat(body) : null;
        if (answer === null || answer === undefined) {
          answer = sampleFromSchema(body.format, { fieldValues });
        }
        const content = typeof answer === 'string' ? answer : JSON.stringify(answer);
        const common = { done_reason: 'stop', prompt_eval_count: 1000, eval_count: 200 };
        return send(200, req.url === '/api/generate'
          ? { response: content, ...common }
          : { message: { role: 'assistant', content }, ...common });
      }
      send(404, { error: 'not found' });
    });
  });

  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address();

  return {
    url: `http://127.0.0.1:${port}`,
    calls,
    close: () => new Promise(resolve => server.close(resolve)),
  };
}
