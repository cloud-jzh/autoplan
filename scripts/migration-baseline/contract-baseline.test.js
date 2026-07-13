'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');

const {
  extractConstObjectValues,
  extractInterface,
  extractLargestReturnObjectKeys,
  loadContract,
  scanContractForSecrets,
  validateContract,
} = require('./contract-baseline');

const ROOT = path.resolve(__dirname, '../..');

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

test('extractInterface freezes field order, optionality, nullability, and normalized types', () => {
  const source = `
export interface Example extends Base {
  id: number;
  title?: string | null;
  nested: { ok: boolean; value: string | null };
}
`;
  assert.deepEqual(extractInterface(source, 'Example'), {
    header: 'export interface Example extends Base',
    fields: ['id:number', 'title?:string|null', 'nested:{ok:boolean;value:string|null}'],
  });
});

test('extractLargestReturnObjectKeys selects the complete object shape', () => {
  const source = `
function sample(ok) {
  if (!ok) return { ok: false };
  return {
    ok: true,
    items: [{ id: 1, label: 'x' }],
    state: ok ? { phase: 'ready' } : null,
  };
}
`;
  assert.deepEqual(extractLargestReturnObjectKeys(source, 'sample'), ['ok', 'items', 'state']);
});

test('extractConstObjectValues preserves declared state order', () => {
  const source = `const STATUS = Object.freeze({ FIRST: 'first', SECOND: 'second' });`;
  assert.deepEqual(extractConstObjectValues(source, 'STATUS'), ['first', 'second']);
});

test('controlled contract exactly matches current source facts', () => {
  const contract = loadContract(ROOT);
  assert.deepEqual(validateContract(ROOT, contract), []);
});

test('missing DTO fields and changed optional/null types fail', () => {
  const baseline = loadContract(ROOT);

  const missing = clone(baseline);
  missing.dtoContracts.find((item) => item.id === 'dto.app-snapshot').fields.pop();
  assert(validateContract(ROOT, missing).some((error) => error.includes('dto.app-snapshot 字段 漂移')));

  const changed = clone(baseline);
  const terminal = changed.dtoContracts.find((item) => item.id === 'dto.terminal-session');
  terminal.fields[terminal.fields.indexOf('endedAt:string|null')] = 'endedAt?:string';
  assert(validateContract(ROOT, changed).some((error) => error.includes('dto.terminal-session 字段 漂移')));
});

test('snapshot defaults, sort clauses, and state enums drift explicitly', () => {
  const baseline = loadContract(ROOT);

  const defaults = clone(baseline);
  defaults.snapshotContracts.find((item) => item.id === 'snapshot.empty-defaults').keys.splice(3, 1);
  assert(validateContract(ROOT, defaults).some((error) => error.includes('snapshot.empty-defaults 字段顺序 漂移')));

  const sorting = clone(baseline);
  sorting.sourceAssertions.find((item) => item.id === 'source.snapshot-sorting-defaults').contains[0] =
    'ORDER BY sort_order DESC';
  assert(validateContract(ROOT, sorting).some((error) => error.includes('source.snapshot-sorting-defaults 缺少源码事实')));

  const states = clone(baseline);
  states.stateMachines.find((item) => item.id === 'state.terminal').states[0] = 'created';
  assert(validateContract(ROOT, states).some((error) => error.includes('state.terminal 状态 漂移')));
});

test('event order changes fail rather than accepting reordered markers', () => {
  const contract = clone(loadContract(ROOT));
  const chat = contract.eventOrdering.find((item) => item.id === 'order.chat-terminal');
  chat.ordered[0] = [...chat.ordered[0]].reverse();
  assert(validateContract(ROOT, contract).some((error) => error.includes('order.chat-terminal 顺序漂移')));
});

test('secret scanner rejects credential-like values and machine-local user paths', () => {
  assert.deepEqual(scanContractForSecrets({ sample: 'masked-only' }), []);
  assert(scanContractForSecrets({ sample: 'sk-exampleCredential123456789' }).some((error) => error.includes('OpenAI')));
  assert(scanContractForSecrets({ sample: 'C:\\Users\\someone\\AppData\\Roaming' }).some((error) => error.includes('绝对 Windows 路径')));
});
