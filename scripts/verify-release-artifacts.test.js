'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { LINUX_APP_IMAGE, LINUX_DEB, verificationResult } = require('./verify-release-artifacts');

test('linux artifact patterns accept electron-builder architecture names', () => {
  assert.match('AutoPlan-0.2.3-beta.11-linux-x86_64.AppImage', LINUX_APP_IMAGE);
  assert.match('AutoPlan-0.2.3-beta.11-linux-x64.AppImage', LINUX_APP_IMAGE);
  assert.match('AutoPlan-0.2.3-beta.11-linux-amd64.deb', LINUX_DEB);
  assert.match('AutoPlan-0.2.3-beta.11-linux-x64.deb', LINUX_DEB);
});

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
