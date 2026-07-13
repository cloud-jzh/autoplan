'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { SidecarSmokeError, parseArgs, resourceBinaryPath } = require('./smoke-go-sidecar');

test('sidecar smoke accepts release platform names', () => {
  assert.deepEqual(parseArgs(['--platform', 'windows']), { platform: 'windows' });
  assert.deepEqual(parseArgs(['--platform', 'macos']), { platform: 'macos' });
  assert.deepEqual(parseArgs(['--platform', 'linux']), { platform: 'linux' });
});

test('sidecar smoke rejects ambiguous arguments', () => {
  assert.throws(() => parseArgs([]), (error) =>
    error instanceof SidecarSmokeError && error.code === 'sidecar_smoke_arguments_invalid');
  assert.throws(() => parseArgs(['--platform', 'win32']), (error) =>
    error instanceof SidecarSmokeError && error.code === 'sidecar_smoke_arguments_invalid');
});

test('sidecar smoke resolves only supported packaged targets', () => {
  assert.equal(
    resourceBinaryPath('/release/resources', 'win32', 'x64'),
    path.join(path.resolve('/release/resources'), 'sidecar', 'win32', 'x64', 'autoplan-server.exe'),
  );
  assert.throws(() => resourceBinaryPath('/release/resources', 'linux', 'arm64'), (error) =>
    error instanceof SidecarSmokeError && error.code === 'sidecar_smoke_target_unsupported');
});
