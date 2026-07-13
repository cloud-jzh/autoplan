'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { cleanupTemporaryRoot, parseArgs, sourceSignature, structuredPrerequisite, structuredSmoke, verificationCommands } = require('./verify');
const { artifactRecord, parseArgs: parseSmokeArgs, parseSmokeResult } = require('./smoke-packaged-terminal');

test('P14 verifier plans explicit lifecycle, renderer, security, and packaged gates', () => {
  const commands = verificationCommands('C:/fixture');
  assert.deepEqual(commands.map((item) => item.id), ['p14-node-verifier-tests', 'p14-renderer-contract', 'p14-go-terminal-contracts', 'p14-type-and-syntax', 'p14-node-regression', 'p14-packaged-host-smoke', 'p14-worktree-status']);
  assert.deepEqual(parseArgs(['verify', '--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseArgs(['verify']));
  assert.deepEqual(structuredPrerequisite('{"status":"ready","failures":[]}\n'), { status: 'ready', failures: [] });
  assert.deepEqual(structuredSmoke('{"platform":"win32","ok":true,"code":"packaged_smoke_completed"}\n'), { platform: 'win32', ok: true, code: 'packaged_smoke_completed' });
  assert.equal(cleanupTemporaryRoot('C:/not-an-autoplan-p14-temporary-root').cleaned, false);
  assert.match(sourceSignature([{ path: 'fixture', sha256: 'a' }]), /^[a-f0-9]{64}$/);
});

test('P14 packaged smoke parser refuses synthetic or incomplete evidence', () => {
  assert.deepEqual(parseSmokeArgs(['--platform', 'win32', '--artifact', 'artifact', '--fixture-root', 'fixture']), { platform: 'win32', artifact: 'artifact', fixtureRoot: 'fixture' });
  assert.throws(() => parseSmokeArgs(['--platform', 'win32']));
  assert.equal(parseSmokeResult('{"kind":"autoplan-terminal-packaged-smoke","platform":"win32","ok":true,"checks":[]}\n', 'win32').ok, false);
  assert.equal(artifactRecord(process.cwd(), pathOutsideRelease()).ok, false);
});

function pathOutsideRelease() { return __filename; }
