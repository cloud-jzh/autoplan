'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  cleanupTemporaryRoot,
  coverageEvidence,
  nodeGoldenSpec,
  p00GateCommands,
  p08Commands,
  p08PreflightCommand,
  parseArgs,
  parseStructuredOutput,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  testControlViolations,
} = require('./verify');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p08-verify-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'config'), { recursive: true });
  fs.writeFileSync(path.join(root, 'config/expectations.json'), JSON.stringify({ commands: {
    check: { outcome: 'success', allowedFailureSignatures: [] }, test: { outcome: 'success', allowedFailureSignatures: [] },
  } }));
  return root;
}

function cleanGitStatus() {
  return { exitCode: 0, entries: [], stderr: '' };
}

function sourceSafety() {
  return {
    databaseOwnerGuardSha256: 'a'.repeat(64), manifestSha256: 'b'.repeat(64), goldenSha256: 'c'.repeat(64),
    schemaSha256: 'd'.repeat(64), paginationSha256: 'e'.repeat(64), expectedErrorsSha256: 'f'.repeat(64),
  };
}

function successfulExecutor(calls) {
  let sequence = 0;
  return async (spec) => {
    calls.push(spec.id);
    const start = Date.parse('2026-07-12T09:00:00.000Z') + sequence++ * 20;
    return { exitCode: 0, signal: null, error: null, stdout: spec.id === 'p08-safety-preflight' ? '{"ok":true}' : '', stderr: '',
      startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString() };
  };
}

test('argument and structured output parsing are strict', () => {
  assert.deepEqual(parseArgs(['verify']), { mode: 'verify' });
  assert.throws(() => parseArgs([]), /usage:/);
  assert.deepEqual(parseStructuredOutput('noise\n{"ok":true}\n'), { ok: true });
  assert.equal(parseStructuredOutput('noise only'), null);
});

test('safe environment removes credentials, user storage and prior-stage controls', () => {
  const temporaryRoot = path.join(os.tmpdir(), 'autoplan-p08-verify-env');
  const safe = safeEnvironment(temporaryRoot, {
    PATH: 'safe', API_TOKEN: 'secret', DB_PATH: 'unsafe', HOME: 'unsafe-home', GOCACHE: 'unsafe-cache', AUTOPLAN_P07_VERIFY: 'previous',
  });
  assert.equal(safe.environment.PATH, 'safe');
  assert.equal(safe.environment.API_TOKEN, undefined);
  assert.equal(safe.environment.DB_PATH, undefined);
  assert.equal(safe.environment.HOME, path.join(temporaryRoot, 'home'));
  assert.equal(safe.environment.GOCACHE, path.join(temporaryRoot, 'go-cache'));
  assert.equal(safe.environment.AUTOPLAN_P07_VERIFY, undefined);
  assert.equal(safe.environment.AUTOPLAN_P08_DATABASE_ROOT, path.join(temporaryRoot, 'database'));
  assert.equal(safe.environment.AUTOPLAN_P08_VERIFY, '1');
  assert.ok(!sanitizeLog(temporaryRoot, process.cwd(), temporaryRoot).includes(temporaryRoot));
});

test('test control scan rejects skip and only markers in guarded P08 tests', (t) => {
  const root = fixtureRoot(t);
  const relative = 'scripts/migration-p08/example.test.js';
  const target = path.join(root, relative);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, ['test.' + 'only("hidden", () => {})', 't.' + 'Skip("hidden")'].join('\n'));
  assert.deepEqual(testControlViolations(root, [relative]), [relative + ':1', relative + ':2']);
});

test('P08 command plan gates the frozen P00 baseline before Node static golden and Go writers', () => {
  const expectations = { commands: { check: { outcome: 'success', allowedFailureSignatures: [] }, test: { outcome: 'success', allowedFailureSignatures: [] } } };
  assert.equal(p08PreflightCommand().id, 'p08-safety-preflight');
  assert.equal(nodeGoldenSpec().id, 'node-static-golden');
  assert.deepEqual(p00GateCommands(expectations).map((item) => item.id), ['check', 'test']);
  assert.deepEqual(p08Commands(expectations).map((item) => item.id), [
    'node-static-golden', 'go-static-repository', 'go-static-automation', 'go-static-chat-config', 'go-static-secrets',
    'go-static-httpapi', 'go-static-mcp', 'go-secret-migration', 'renderer-static-transport', 'p08-golden-and-safety-tests',
  ]);
});

test('coverage evidence records static, migration, runtime closure and sensitive-output matrices', () => {
  const expectations = { commands: { check: { outcome: 'success', allowedFailureSignatures: [] }, test: { outcome: 'success', allowedFailureSignatures: [] } } };
  const commands = [{ id: 'p07-gate' }, p08PreflightCommand(), ...p00GateCommands(expectations), ...p08Commands(expectations)].map((item) => ({ id: item.id, evaluation: { accepted: true } }));
  const coverage = coverageEvidence(commands, sourceSafety());
  assert.ok(coverage.matrix.every((item) => item.verified));
  assert.equal(coverage.runtime.disabledActionsReturnedNonSuccess, true);
});

test('failed P07 gate creates blocked evidence before P08 safety or writers', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'p07-blocked-run', sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      return { exitCode: 7, signal: null, error: null, stdout: '', stderr: 'p07 failed', startedAt: '2026-07-12T09:00:00.000Z', endedAt: '2026-07-12T09:00:00.010Z' };
    },
  });
  assert.deepEqual(calls, ['p07-gate']);
  assert.equal(result.summary.status, 'blocked');
  assert.equal(result.summary.commandResults[0].exitCode, 7);
  assert.equal(result.summary.temporaryCleanup.cleaned, true);
});

test('failed P08 safety preflight stops before Node and Go writers', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  let sequence = 0;
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'safety-blocked-run', sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      const start = Date.parse('2026-07-12T09:10:00.000Z') + sequence++ * 20;
      return { exitCode: spec.id === 'p08-safety-preflight' ? 1 : 0, signal: null, error: null, stdout: '', stderr: '', startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString() };
    },
  });
  assert.deepEqual(calls, ['p07-gate', 'p08-safety-preflight']);
  assert.equal(result.summary.status, 'blocked');
  assert.equal(result.summary.blocked, 'p08_safety_preflight_failed_no_p08_node_or_go_writer_started');
});

test('failed Node static golden stops before the first Go writer', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  let sequence = 0;
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'node-failed-run', sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      const start = Date.parse('2026-07-12T09:20:00.000Z') + sequence++ * 20;
      return { exitCode: spec.id === 'node-static-golden' ? 1 : 0, signal: null, error: null, stdout: '', stderr: '', startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString() };
    },
    inspectP07Evidence: () => ({ stage: 'p07', runId: 'accepted' }), inspectSourceSafety: sourceSafety,
  });
  assert.deepEqual(calls, ['p07-gate', 'p08-safety-preflight', 'check', 'test', 'node-static-golden']);
  assert.equal(result.summary.status, 'failed');
  assert.equal(result.summary.failure, 'node-static-golden_failed_stopped_remaining_steps');
});

test('successful verification emits immutable sanitized evidence', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'completed-run', sourceFiles: [], environment: { PATH: process.env.PATH || '' },
    gitStatus: cleanGitStatus, executeCommand: successfulExecutor(calls), inspectP07Evidence: () => ({ stage: 'p07', runId: 'accepted' }), inspectSourceSafety: sourceSafety,
  });
  assert.deepEqual(calls.slice(0, 5), ['p07-gate', 'p08-safety-preflight', 'check', 'test', 'node-static-golden']);
  assert.equal(result.summary.status, 'completed');
  assert.equal(result.summary.ok, true);
  assert.equal(result.summary.safety.writerTimeline.nodeClosedBeforeGo, true);
  assert.ok(fs.existsSync(path.join(result.runDir, 'summary.json')));
  assert.ok(fs.existsSync(path.join(result.runDir, 'evidence-manifest.json')));
});

test('P00 baseline drift blocks before Node golden and every Go writer', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  let sequence = 0;
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'p00-blocked-run', sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      const start = Date.parse('2026-07-12T09:30:00.000Z') + sequence++ * 20;
      return { exitCode: spec.id === 'check' ? 1 : 0, signal: null, error: null, stdout: '', stderr: 'unexpected baseline drift', startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString() };
    },
    inspectP07Evidence: () => ({ stage: 'p07', runId: 'accepted' }), inspectSourceSafety: sourceSafety,
  });
  assert.deepEqual(calls, ['p07-gate', 'p08-safety-preflight', 'check']);
  assert.equal(result.summary.status, 'blocked');
  assert.equal(result.summary.blocked, 'p00_baseline_gate_failed_no_p08_node_or_go_writer_started');
});

test('cleanup refuses directories not owned by the P08 verifier', (t) => {
  const root = fixtureRoot(t);
  assert.deepEqual(cleanupTemporaryRoot(root), { cleaned: false, error: 'refused_non_owned_cleanup' });
  assert.equal(fs.existsSync(root), true);
});
