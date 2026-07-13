'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { createSafeEnvironment, inspectEvidenceFile, inspectOwnerSafety, sanitizeText, scanSensitiveText, validateFixtureRoot } = require('./check-safety');

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p09-fixture-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, '.autoplan-p09-scale-copy'), 'p09-scale-v1\n');
  fs.writeFileSync(path.join(root, 'scale-copy.json'), JSON.stringify({ rows: { projects: [] } }));
  fs.writeFileSync(path.join(root, 'scale-manifest.json'), JSON.stringify({ kind: 'p09-generated-scale-copy' }));
  return root;
}

test('generated scale copy is the only accepted fixture shape', (t) => {
  const root = fixture(t);
  const accepted = validateFixtureRoot(root);
  assert.equal(accepted.ok, true);
  assert.equal(accepted.code, 'fixture_authorized');
  fs.writeFileSync(path.join(root, 'autoplan.sqlite'), 'not-a-database');
  assert.equal(validateFixtureRoot(root).code, 'fixture_active_database_rejected');
  assert.equal(validateFixtureRoot('C:\\Users\\example\\AppData\\Roaming\\autoplan').code, 'fixture_real_userdata_rejected');
});

test('owner sidecars and sensitive evidence are fail-closed', (t) => {
  const root = fixture(t);
  assert.equal(inspectOwnerSafety(root).ok, true);
  fs.writeFileSync(path.join(root, 'autoplan.sqlite-wal'), 'active');
  assert.equal(inspectOwnerSafety(root).code, 'owner_or_sidecar_active');
  const evidence = path.join(root, 'unsafe.log');
  fs.writeFileSync(evidence, 'token=not-safe-to-persist');
  assert.equal(inspectEvidenceFile(evidence, root).code, 'evidence_sensitive');
});

test('logs are sanitized and sensitive runtime environment values are removed', () => {
  const temporaryRoot = path.join(os.tmpdir(), 'autoplan-p09-verify-env');
  const environment = createSafeEnvironment(temporaryRoot, { PATH: 'safe', API_TOKEN: 'secret', HOME: 'unsafe-home', AUTOPLAN_PREVIOUS: 'unsafe' });
  assert.equal(environment.environment.PATH, 'safe');
  assert.equal(environment.environment.API_TOKEN, undefined);
  assert.equal(environment.environment.HOME, path.join(temporaryRoot, 'home'));
  assert.equal(environment.environment.AUTOPLAN_PREVIOUS, undefined);
  assert.deepEqual(scanSensitiveText('Authorization=Bearer sk-test_not_safe_12345678'), ['sensitive_pattern']);
  assert.ok(!sanitizeText('token=not-safe C:\\Users\\example\\secret', { temporaryRoot }).includes('not-safe'));
});
