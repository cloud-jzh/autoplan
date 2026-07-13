'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { checkP13BPrerequisites, parseArgs } = require('./check-p13b-architecture');
const { createSafeEnvironment, sanitizeText, scanSensitiveText, validateFixtureRoot } = require('./check-p13b-safety');
const {
  cleanupTemporaryRoot,
  completePrerequisites,
  parseArgs: parseVerifyArgs,
  structuredPrerequisite,
  verificationCommands,
} = require('./verify-p13b');

test('P13B verifier command plan and argument parser are fail-closed', () => {
  assert.deepEqual(verificationCommands().map((item) => item.id), ['p13b-gate-tests', 'p13b-go-contracts', 'p13b-worktree-status']);
  assert.deepEqual(parseVerifyArgs(['verify', '--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseVerifyArgs(['verify']));
  assert.deepEqual(parseArgs(['--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseArgs(['--fixture-root']));
  assert.equal(cleanupTemporaryRoot('C:/not-an-autoplan-p13b-temporary-root').cleaned, false);
});

test('P13B fixture and prerequisite status remain independent and explicit', () => {
  const rootDir = path.resolve(__dirname, '..', '..');
  const fixtureRoot = path.join(rootDir, 'fixtures', 'migration', 'p13', 'mcp');
  assert.equal(validateFixtureRoot(fixtureRoot).ok, true);
  const result = checkP13BPrerequisites({ rootDir, fixtureRoot });
  assert.ok(result.status === 'ready' || result.status === 'blocked');
  assert.equal(typeof result.architecture.ok, 'boolean');
  assert.equal(completePrerequisites({
    status: 'ready',
    failures: [],
    architecture: { ok: true },
    stages: [{ run_id: 'p10-example' }, { run_id: 'p11-example' }, { run_id: 'p12-example' }],
  }), true);
  assert.equal(completePrerequisites({ status: 'ready', failures: [], architecture: { ok: false }, stages: [] }), false);
  assert.deepEqual(structuredPrerequisite('{"status":"ready","failures":[]}\n'), { status: 'ready', failures: [] });
});

test('P13B safety helpers remove sensitive environment values and redact evidence text', () => {
  const safe = createSafeEnvironment('C:/autoplan-p13b-temporary-root', { PATH: 'safe-path', AUTOPLAN_EXAMPLE: 'removed' });
  assert.equal(safe.environment.AUTOPLAN_EXAMPLE, undefined);
  assert.equal(safe.environment.AUTOPLAN_SIDECAR_GO_MCP_API, 'false');
  assert.deepEqual(scanSensitiveText('api_key=value'), ['sensitive_pattern']);
  assert.equal(sanitizeText('api_key=value').includes('value'), false);
});
