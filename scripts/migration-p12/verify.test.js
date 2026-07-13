'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { cleanupTemporaryRoot, parseArgs, structuredPrerequisite, verificationCommands } = require('./verify');
const { compareRuntimeGolden } = require('./compare-runtime-golden');

test('P12 golden comparison covers all frozen Script and Executor actions', () => {
  const result = compareRuntimeGolden();
  assert.equal(result.ok, true);
  assert.equal(result.action_count, 5);
  assert.ok(result.scenario_count >= 5);
});

test('P12 verifier command plan stays explicit and parser is fail-closed', () => {
  const commands = verificationCommands('C:/fixture');
  assert.deepEqual(commands.map((item) => item.id), ['p12-runtime-golden', 'p12-node-tests', 'p12-renderer-contract', 'p12-go-process-contracts', 'p12-worktree-status']);
  assert.deepEqual(parseArgs(['verify', '--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseArgs(['verify']));
  assert.deepEqual(structuredPrerequisite('{"status":"ready","failures":[]}\n'), { status: 'ready', failures: [] });
  assert.equal(cleanupTemporaryRoot('C:/not-an-autoplan-p12-temporary-root').cleaned, false);
});
