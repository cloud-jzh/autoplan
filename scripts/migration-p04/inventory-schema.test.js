'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const {
  BASELINE_PATH,
  DATABASE_PATH,
  collectDatabaseEvidence,
  hydrateInventory,
  loadInventory,
  scanForSecrets,
  stableStringify,
  validateInventory,
} = require('./inventory-schema');

const ROOT = path.resolve(__dirname, '../..');

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

test('extracts the final effective schema rather than only the initial CREATE block', () => {
  const source = fs.readFileSync(path.join(ROOT, DATABASE_PATH), 'utf8');
  const evidence = collectDatabaseEvidence(source);
  assert.equal(evidence.tables.length, 18);
  assert.equal(evidence.indexes.length, 17);
  const requirements = evidence.tables.find((table) => table.name === 'requirements');
  assert(requirements.columns.some((column) => column.name === 'linked_plan_id'
    && column.declarations.some((item) => item.kind === 'ensure-column')));
  const conversations = evidence.tables.find((table) => table.name === 'conversations');
  assert(conversations.columns.some((column) => column.name === 'codex_session_id'));
  const scanFiles = evidence.tables.find((table) => table.name === 'scan_files');
  assert.deepEqual(scanFiles.primaryKey, ['project_id', 'scan_type', 'file_path']);
});

test('checked schema v1 inventory cross-checks current Node source and P00 baseline', () => {
  const inventory = loadInventory(ROOT);
  assert.deepEqual(validateInventory(ROOT, inventory), []);
});

test('rendering is byte deterministic and uses canonical table, column, and index ordering', () => {
  const inventory = loadInventory(ROOT);
  const databaseSource = fs.readFileSync(path.join(ROOT, DATABASE_PATH), 'utf8');
  const baselineSource = fs.readFileSync(path.join(ROOT, BASELINE_PATH), 'utf8');
  const first = stableStringify(hydrateInventory(inventory, databaseSource, baselineSource));
  const second = stableStringify(hydrateInventory(inventory, databaseSource, baselineSource));
  assert.equal(first, second);
  assert.equal(first, fs.readFileSync(path.join(ROOT, 'docs/migration/p04/schema-inventory.json'), 'utf8'));
});

test('new compatibility schema logic fails the frozen inventory guard', () => {
  const inventory = loadInventory(ROOT);
  const databaseSource = `${fs.readFileSync(path.join(ROOT, DATABASE_PATH), 'utf8')}\nthis.ensureColumn('projects', 'drift_probe', 'TEXT');\n`;
  const baselineSource = fs.readFileSync(path.join(ROOT, BASELINE_PATH), 'utf8');
  const errors = validateInventory(ROOT, inventory, { databaseSource, baselineSource });
  assert(errors.some((error) => error.includes('source-derived tables drift')));
  assert(errors.some((error) => error.includes('input hashes drift')));
});

test('missing migration evidence, relation decisions, and sensitive output policy fail closed', () => {
  const inventory = clone(loadInventory(ROOT));
  inventory.compatibilityMigrations[0].markers[0] = 'missing compatibility marker';
  delete inventory.sensitiveData[0].eventPolicy;
  delete inventory.foreignKeyDecisions[0].precondition;
  const errors = validateInventory(ROOT, inventory);
  assert(errors.some((error) => error.includes('marker missing')));
  assert(errors.some((error) => error.includes('lacks eventPolicy')));
  assert(errors.some((error) => error.includes('lacks precondition')));
});

test('inventory rejects credentials and machine-local user paths', () => {
  assert.deepEqual(scanForSecrets({ example: 'fixture-placeholder' }), []);
  assert(scanForSecrets({ example: 'sk-credentialShapedFixture123' }).length > 0);
  assert(scanForSecrets({ example: 'C:\\Users\\local\\AppData\\secret' }).length > 0);
});
