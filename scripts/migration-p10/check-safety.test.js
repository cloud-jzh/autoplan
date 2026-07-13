'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { FIXTURE_MANIFEST, FIXTURE_MARKER, createSafeEnvironment, inspectEvidenceFile, inspectOwnerSafety, sanitizeText, scanSensitiveText, validateFixtureRoot } = require('./check-safety');

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p10-fixture-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, FIXTURE_MARKER), 'p10-authorized-v1\n');
  fs.writeFileSync(path.join(root, FIXTURE_MANIFEST), JSON.stringify({ kind: 'p10-authorized-fixture', schema_version: 1, authorized_copy: true }));
  return root;
}

test('only an explicit temporary P10 fixture copy is accepted', (t) => {
  const root = fixture(t);
  assert.equal(validateFixtureRoot(root).code, 'fixture_authorized');
  fs.writeFileSync(path.join(root, 'autoplan.sqlite-wal'), 'active');
  assert.equal(validateFixtureRoot(root).code, 'fixture_owner_or_sidecar_active');
  assert.equal(validateFixtureRoot('C:\\Users\\example\\AppData\\Roaming\\autoplan').code, 'fixture_real_userdata_rejected');
});

test('owner sidecars and unsafe evidence fail closed', (t) => {
  const root = fixture(t);
  assert.equal(inspectOwnerSafety(root).ok, true);
  fs.writeFileSync(path.join(root, '.autoplan-owner.lock'), 'active');
  assert.equal(inspectOwnerSafety(root).code, 'owner_or_sidecar_active');
  const evidence = path.join(root, 'unsafe.log');
  fs.writeFileSync(evidence, 'token=not-safe-to-persist');
  assert.equal(inspectEvidenceFile(evidence, root).code, 'evidence_sensitive');
});

test('sanitization removes runtime credentials and real user paths', () => {
  const temporaryRoot = path.join(os.tmpdir(), 'autoplan-p10-verify-env');
  const environment = createSafeEnvironment(temporaryRoot, { PATH: 'safe', API_TOKEN: 'unsafe', AUTOPLAN_OLD: 'unsafe', HOME: 'unsafe' });
  assert.equal(environment.environment.PATH, 'safe');
  assert.equal(environment.environment.API_TOKEN, undefined);
  assert.equal(environment.environment.AUTOPLAN_OLD, undefined);
  assert.equal(environment.environment.HOME, path.join(temporaryRoot, 'home'));
  assert.deepEqual(scanSensitiveText('Authorization=Bearer sk-test_not_safe_12345678'), ['sensitive_pattern']);
  assert.ok(!sanitizeText('token=unsafe C:\\Users\\example\\private', { temporaryRoot }).includes('unsafe'));
});
