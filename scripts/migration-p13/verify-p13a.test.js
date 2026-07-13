'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { checkPrerequisites, parseArgs } = require('./check-prerequisites');
const { cleanupTemporaryRoot, completePrerequisites, parseArgs: parseVerifyArgs, structuredPrerequisite, verificationCommands } = require('./verify-p13a');
const { validateFixtureRoot } = require('./check-p13a-safety');

test('P13A verifier command plan and argument parser are fail-closed', () => {
  assert.deepEqual(verificationCommands().map((item) => item.id), ['p13a-gate-tests', 'p13a-renderer-contracts', 'p13a-go-contracts', 'p13a-worktree-status']);
  assert.deepEqual(parseVerifyArgs(['verify', '--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseVerifyArgs(['verify']));
  assert.deepEqual(parseArgs(['--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseArgs(['--fixture-root']));
  assert.equal(cleanupTemporaryRoot('C:/not-an-autoplan-p13a-temporary-root').cleaned, false);
});

test('P13A fixture and prerequisite status remain explicit', () => {
  const root = path.resolve(__dirname, '..', '..', 'fixtures', 'migration', 'p13', 'chat');
  assert.equal(validateFixtureRoot(root).ok, true);
  const result = checkPrerequisites({ rootDir: path.resolve(__dirname, '..', '..'), fixtureRoot: root });
  assert.ok(result.status === 'ready' || result.status === 'blocked');
  assert.equal(typeof result.renderer.ok, 'boolean');
  assert.equal(completePrerequisites({ status: 'ready', failures: [], owner: { owner: 'go' }, renderer: { ok: true }, stages: [{ run_id: 'p10' }, { run_id: 'p11' }, { run_id: 'p12' }] }), true);
  assert.equal(completePrerequisites({ status: 'ready', failures: [] }), false);
  assert.deepEqual(structuredPrerequisite('{"status":"ready","failures":[]}\n'), { status: 'ready', failures: [] });
});
