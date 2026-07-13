'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { verificationResult } = require('./verify-release-artifacts');

test('unsigned macOS artifacts pass integrity verification without claiming signed trust', () => {
  assert.deepEqual(verificationResult({ platform: 'macos', mode: 'unsigned-test' }), {
    status: 'verified',
    code: 'release_artifacts_unsigned_test_verified',
    platform: 'macos',
    release_mode: 'unsigned-test',
    trust_status: 'unsigned-test',
  });
});

test('signed macOS artifacts retain verified trust status', () => {
  assert.deepEqual(verificationResult({ platform: 'macos', mode: 'signed-notarized' }), {
    status: 'verified',
    code: 'release_artifacts_verified',
    platform: 'macos',
    release_mode: 'signed-notarized',
    trust_status: 'verified',
  });
});
