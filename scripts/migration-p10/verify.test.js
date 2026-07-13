'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { FIXTURE_MANIFEST, FIXTURE_MARKER } = require('./check-safety');
const { parseArgs, runVerification, verificationCommands } = require('./verify');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p10-verify-test-'));
  const fixture = path.join(root, 'authorized-fixture');
  fs.mkdirSync(fixture);
  fs.writeFileSync(path.join(fixture, FIXTURE_MARKER), 'p10-authorized-v1\n');
  fs.writeFileSync(path.join(fixture, FIXTURE_MANIFEST), JSON.stringify({ kind: 'p10-authorized-fixture', schema_version: 1, authorized_copy: true }));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return { root, fixture };
}

function result(exitCode, stdout = '', stderr = '') {
  return { exitCode, signal: null, error: null, stdout, stderr, startedAt: '2026-07-12T10:00:00.000Z', endedAt: '2026-07-12T10:00:00.010Z' };
}

function readyPrerequisiteOutput() {
  return JSON.stringify({
    status: 'ready', code: 'prerequisites_ready', failures: [],
    p00: { run_id: 'p00-run', summary_sha256: 'a'.repeat(64) },
    stages: [{ stage: 'p09', run_id: 'p09-run', summary_sha256: 'b'.repeat(64) }],
  });
}

test('arguments and serial command plan are frozen', () => {
  assert.deepEqual(parseArgs(['verify', '--fixture-root', 'D:\\fixture']), { fixtureRoot: 'D:\\fixture' });
  assert.throws(() => parseArgs(['verify']), /usage:/);
  const ids = verificationCommands().map((item) => item.id);
  assert.deepEqual(ids.slice(0, 2), ['p10-gate-tests', 'p10-protocol-contract']);
  assert.ok(ids.indexOf('p10-operation-integration') < ids.indexOf('renderer-baseline-tests'));
});

test('failed prerequisite preserves its exit code and prevents P10 commands', async (t) => {
  const { root, fixture } = fixtureRoot(t);
  const calls = [];
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'blocked-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => { calls.push(spec.id); return result(41, '', 'gate failed'); },
  });
  assert.deepEqual(calls, ['prerequisites']);
  assert.equal(verification.summary.status, 'blocked');
  assert.equal(verification.summary.blocked_exit_code, 41);
  assert.equal(verification.summary.temporary_cleanup.cleaned, true);
});

test('zero prerequisite exit with incomplete P09 evidence is blocked', async (t) => {
  const { root, fixture } = fixtureRoot(t);
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'incomplete-run', sourceFiles: [], environment: {},
    executeCommand: async () => result(0, '{"status":"ready","code":"prerequisites_ready","failures":[]}'),
  });
  assert.equal(verification.summary.status, 'blocked');
  assert.equal(verification.summary.blocked_exit_code, 0);
});

test('a nonzero verification command stops the remaining serial plan', async (t) => {
  const { root, fixture } = fixtureRoot(t);
  const calls = [];
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'failed-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => { calls.push(spec.id); return result(spec.id === 'p10-eventbus-recovery' ? 19 : 0, spec.id === 'prerequisites' ? readyPrerequisiteOutput() : ''); },
  });
  assert.deepEqual(calls, ['prerequisites', 'p10-gate-tests', 'p10-protocol-contract', 'p10-operation-integration', 'p10-eventbus-recovery']);
  assert.equal(verification.summary.status, 'failed');
  assert.equal(verification.summary.command_results.at(-1).exit_code, 19);
});

test('complete synthetic results produce immutable sanitized evidence', async (t) => {
  const { root, fixture } = fixtureRoot(t);
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'completed-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => result(0, spec.id === 'prerequisites' ? readyPrerequisiteOutput() : ''),
  });
  assert.equal(verification.summary.status, 'completed');
  assert.equal(verification.summary.ok, true);
  assert.ok(fs.existsSync(path.join(verification.runDir, 'evidence-manifest.json')));
});

test('unsafe output is rejected even if the command exits zero', async (t) => {
  const { root, fixture } = fixtureRoot(t);
  const verification = await runVerification({ rootDir: root, fixtureRoot: fixture, evidenceRoot: 'evidence', runId: 'unsafe-run', sourceFiles: [], environment: {},
    executeCommand: async (spec) => result(0, spec.id === 'prerequisites' ? readyPrerequisiteOutput() : spec.id === 'p10-gate-tests' ? 'token=not-safe-to-persist' : ''),
  });
  assert.equal(verification.summary.status, 'failed');
  assert.equal(verification.summary.command_results.at(-1).reason, 'unsafe_output_rejected');
});
