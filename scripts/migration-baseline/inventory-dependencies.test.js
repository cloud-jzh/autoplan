'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');

const {
  effectiveSchema,
  extractCreateTables,
  extractEnsureColumns,
  extractIndexes,
  loadInventory,
  scanInventoryForSecrets,
  validateInventory,
} = require('./inventory-dependencies');

const ROOT = path.resolve(__dirname, '../..');

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

test('extracts CREATE TABLE columns, constraints, and indexes without treating table constraints as columns', () => {
  const source = `
    CREATE TABLE IF NOT EXISTS sample (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      project_id INTEGER NOT NULL DEFAULT 1,
      label TEXT NOT NULL DEFAULT '',
      UNIQUE(project_id, label)
    );
    CREATE UNIQUE INDEX IF NOT EXISTS idx_sample_project_label ON sample(project_id, label);
  `;
  const tables = extractCreateTables(source);
  assert.deepEqual([...tables.get('sample').keys()], ['id', 'project_id', 'label']);
  assert.deepEqual(extractIndexes(source), ['idx_sample_project_label']);
});

test('ensureColumn extraction preserves order and definitions and contributes to effective schema', () => {
  const source = `
    CREATE TABLE sample (id INTEGER PRIMARY KEY);
    this.ensureColumn('sample', 'status', "TEXT NOT NULL DEFAULT 'pending'");
    this.ensureColumn('sample', 'updated_at', 'TEXT');
  `;
  assert.deepEqual([...extractEnsureColumns(source).entries()], [[
    'sample',
    ["status TEXT NOT NULL DEFAULT 'pending'", 'updated_at TEXT'],
  ]]);
  assert.deepEqual([...effectiveSchema(source).get('sample').keys()], ['id', 'status', 'updated_at']);
});

test('controlled database and dependency inventory matches current sources', () => {
  const inventory = loadInventory(ROOT);
  assert.deepEqual(validateInventory(ROOT, inventory), []);
});

test('new or removed schema, ensureColumn, index, SQL reference, and external import classifications fail', () => {
  const baseline = loadInventory(ROOT);
  const cases = [
    {
      name: 'schema column',
      mutate(value) { value.schema.projects.push('unclassified_column'); },
      message: 'schema.projects 漂移',
    },
    {
      name: 'ensure column',
      mutate(value) { value.ensureColumns.projects = ['unclassified TEXT']; },
      message: 'ensureColumns key 漂移',
    },
    {
      name: 'index',
      mutate(value) { value.indexes.pop(); },
      message: 'indexes 漂移',
    },
    {
      name: 'SQL table',
      mutate(value) { value.sqlTables.push('unclassified_table'); },
      message: 'SQL table references 漂移',
    },
    {
      name: 'external import',
      mutate(value) { value.externalImports['node:tls'] = ['src/new-network.js']; },
      message: 'externalImports key 漂移',
    },
  ];

  for (const item of cases) {
    const inventory = clone(baseline);
    item.mutate(inventory);
    const errors = validateInventory(ROOT, inventory);
    assert(errors.some((error) => error.includes(item.message)), `${item.name}: ${errors.join('\n')}`);
  }
});

test('migration order and source dependency marker drift fail explicitly', () => {
  const inventory = clone(loadInventory(ROOT));
  const order = inventory.migrationSteps.find((item) => item.id === 'migration.order');
  order.ordered[0] = [...order.ordered[0]].reverse();
  const terminal = inventory.sourceAssertions.find((item) => item.id === 'source.terminal-pty');
  terminal.contains[1] = 'unclassifiedPtyFactory.spawn';
  const errors = validateInventory(ROOT, inventory);
  assert(errors.some((error) => error.includes('migration.order 顺序漂移')));
  assert(errors.some((error) => error.includes('source.terminal-pty 缺少源码事实')));
});

test('sensitive classifications require all four output policies', () => {
  const inventory = clone(loadInventory(ROOT));
  delete inventory.sensitiveData[0].eventPolicy;
  assert(validateInventory(ROOT, inventory).some((error) => error.includes('缺少敏感策略字段 eventPolicy')));
});

test('inventory secret scanner rejects credential-like values and machine-local paths', () => {
  assert.deepEqual(scanInventoryForSecrets({ sample: 'masked-placeholder' }), []);
  assert(scanInventoryForSecrets({ sample: 'sk-notARealButCredentialShaped1234' }).some((error) => error.includes('OpenAI')));
  assert(scanInventoryForSecrets({ sample: 'C:\\Users\\local-user\\AppData\\Roaming' }).some((error) => error.includes('本机用户路径')));
});
