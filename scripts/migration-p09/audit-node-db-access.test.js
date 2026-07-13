'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { auditRepository, validateManifest } = require('./audit-node-db-access');

function manifestFor(rule = {}) {
  return {
    schema_version: 1,
    contracts: { loop: {} },
    source_rules: {
      'src/owned.js': {
        contract_ids: ['loop'],
        methods: { get: 1 },
        ...rule,
      },
    },
  };
}

function withFixture(source, manifest, callback) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-db-audit-'));
  try {
    fs.mkdirSync(path.join(root, 'src'));
    fs.writeFileSync(path.join(root, 'src', 'owned.js'), source, 'utf8');
    return callback(root, manifest);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
}

test('reports only relative source metadata for a reviewed database read', () => {
  const report = withFixture(
    "const db = {};\ndb.get('SELECT id FROM plans WHERE id = ?', [id]);\n",
    manifestFor(),
    (root, manifest) => auditRepository(root, manifest),
  );

  assert.equal(report.ok, true);
  assert.deepEqual(report.call_sites, [{
    source_path: 'src/owned.js',
    line: 2,
    category: 'read',
    contract_id: 'loop',
  }]);
  assert.equal(JSON.stringify(report).includes('SELECT id FROM plans'), false);
});

test('fails closed when a reviewed source gains an unowned write', () => {
  const report = withFixture(
    "const db = {};\ndb.get('SELECT 1');\ndb.run('UPDATE plans SET status = ?', ['running']);\n",
    manifestFor(),
    (root, manifest) => auditRepository(root, manifest),
  );

  assert.equal(report.ok, false);
  assert.ok(report.errors.some((error) => error.code === 'database_access_count_mismatch' && error.detail === 'run'));
});

test('fails closed for an unregistered database source', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-db-audit-'));
  try {
    fs.mkdirSync(path.join(root, 'src'));
    fs.writeFileSync(path.join(root, 'src', 'owned.js'), "const db = {};\ndb.get('SELECT 1');\n", 'utf8');
    fs.writeFileSync(path.join(root, 'src', 'new-source.js'), "const db = {};\ndb.run('DELETE FROM plans');\n", 'utf8');
    const report = auditRepository(root, manifestFor());
    assert.equal(report.ok, false);
    assert.ok(report.errors.some((error) => error.code === 'unmapped_database_source' && error.source_path === 'src/new-source.js'));
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('requires explicit review for dynamic SQL and direct settings SQL', () => {
  const dynamicReport = withFixture(
    'const db = {};\ndb.get(`SELECT * FROM ${table} WHERE id = ?`, [id]);\n',
    manifestFor(),
    (root, manifest) => auditRepository(root, manifest),
  );
  assert.ok(dynamicReport.errors.some((error) => error.code === 'unmapped_dynamic_sql'));

  const settingsReport = withFixture(
    "const db = {};\ndb.get('SELECT value FROM settings WHERE key = ?', ['ui.theme']);\n",
    manifestFor(),
    (root, manifest) => auditRepository(root, manifest),
  );
  assert.ok(settingsReport.errors.some((error) => error.code === 'unmapped_direct_settings_sql'));
});

test('rejects sensitive manifest fields and absolute locations', () => {
  const errors = validateManifest({
    schema_version: 1,
    contracts: { loop: { secret_value: 'not-allowed' } },
    source_rules: {},
    evidence: { location: 'C:\\real\\profile' },
  });

  assert.ok(errors.some((error) => error.code === 'unsafe_artifact_field'));
  assert.ok(errors.some((error) => error.code === 'unsafe_artifact_value'));
});
