'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');

const { scanSource, tokenizeJavaScript } = require('./check-renderer-boundary');

const FILE = 'src/renderer/components/Unsafe.tsx';

test('allows direct access only in the two approved adapters', () => {
  assert.deepEqual(scanSource('src/renderer/lib/api/ipcClient.ts', 'window.autoplan.snapshot()'), []);
  assert.deepEqual(scanSource('src/renderer/lib/desktop/ipcBridge.ts', "window['autoplan'].openExternal('x')"), []);
});

test('rejects direct, optional, bracket, globalThis and destructured access', () => {
  for (const source of [
    'window.autoplan.snapshot()',
    'window?.autoplan.snapshot()',
    "window['autoplan'].snapshot()",
    "window?.['autoplan'].snapshot()",
    'globalThis.window.autoplan.snapshot()',
    "globalThis['window']['autoplan'].snapshot()",
    'const { autoplan } = window; autoplan.snapshot()',
  ]) {
    assert(scanSource(FILE, source).length > 0, source);
  }
});

test('rejects window aliases and dynamic property bypasses', () => {
  for (const source of [
    'const w = window; w.autoplan.snapshot()',
    "const w = globalThis.window; w['autoplan'].snapshot()",
    "const key = 'autoplan'; window[key].snapshot()",
  ]) {
    assert(scanSource(FILE, source).length > 0, source);
  }
});

test('ignores comments and strings and accepts unrelated window APIs', () => {
  const source = `
    // window.autoplan.snapshot()
    const example = 'window.autoplan.snapshot()';
    window.setTimeout(() => {}, 0);
    window.location.href;
  `;
  assert.deepEqual(scanSource(FILE, source), []);
  assert(tokenizeJavaScript(source).some((token) => token.type === 'string'));
});

test('still inspects executable template expressions', () => {
  assert(scanSource(FILE, 'const value = `${window.autoplan.snapshot()}`;').length > 0);
});
