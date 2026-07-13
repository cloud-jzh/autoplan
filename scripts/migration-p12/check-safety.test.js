'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { FIXTURE_MANIFEST, FIXTURE_MARKER, inspectEvidenceFile, sanitizeText, validateFixtureRoot } = require('./check-safety');

test('P12 fixture safety accepts only the checked-in authorized fixture root', () => {
  const root = path.resolve(__dirname, '..', '..', 'fixtures', 'migration', 'p12', 'process-fixtures');
  const result = validateFixtureRoot(root);
  assert.equal(result.ok, true);
  assert.equal(fs.existsSync(path.join(root, FIXTURE_MARKER)), true);
  assert.equal(fs.existsSync(path.join(root, FIXTURE_MANIFEST)), true);
  assert.equal(validateFixtureRoot(path.join(os.tmpdir(), 'AppData', 'Roaming', 'AutoPlan')).code, 'fixture_real_userdata_rejected');
});

test('P12 evidence safety redacts locations and rejects sensitive output', () => {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p12-safety-test-'));
  try {
    const safe = path.join(temporary, 'safe.log');
    const unsafe = path.join(temporary, 'unsafe.log');
    fs.writeFileSync(safe, 'fixture summary only\n', 'utf8');
    fs.writeFileSync(unsafe, 'token=fixture-credential\n', 'utf8');
    assert.equal(inspectEvidenceFile(safe, temporary).ok, true);
    assert.equal(inspectEvidenceFile(unsafe, temporary).ok, false);
    assert.equal(sanitizeText('token=fixture-credential C:\\fixture\\workspace', { temporaryRoot: temporary }).includes('fixture-credential'), false);
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
});
