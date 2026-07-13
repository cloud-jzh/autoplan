'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  cleanupTemporaryRoot,
  coverageEvidence,
  p06Commands,
  parseArgs,
  parseStructuredOutput,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  testControlViolations,
} = require('./verify');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p06-verify-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, 'config'), { recursive: true });
  fs.writeFileSync(path.join(root, 'config', 'expectations.json'), JSON.stringify({
    commands: {
      check: { description: 'synthetic check', outcome: 'success', allowedFailureSignatures: [] },
      test: { description: 'synthetic test', outcome: 'success', allowedFailureSignatures: [] },
    },
  }));
  return root;
}

function cleanGitStatus() {
  return { exitCode: 0, entries: [], stderr: '' };
}

function successfulExecutor(calls) {
  const epoch = Date.parse('2026-07-11T09:00:00.000Z');
  return async (spec) => {
    const index = calls.length;
    calls.push(spec.id);
    const structured = spec.id === 'node-golden-generator'
      ? { ok: true, artifacts: ['node-intake.golden.json', 'manifest.json'] }
      : (spec.id === 'p06-safety-preflight' ? { ok: true, schemaVersion: 1 } : null);
    return {
      exitCode: 0, signal: null, error: null, stdout: structured ? JSON.stringify(structured) + '\n' : '', stderr: '',
      startedAt: new Date(epoch + index * 20).toISOString(), endedAt: new Date(epoch + index * 20 + 10).toISOString(),
    };
  };
}

function sourceSafety() {
  return {
    ok: true,
    schemaVersion: 1,
    databaseOwnerGuardSha256: 'a'.repeat(64),
    manifestSha256: 'b'.repeat(64),
    goldenSha256: 'c'.repeat(64),
    expectedErrorsSha256: 'd'.repeat(64),
    intakeContractSha256: 'e'.repeat(64),
    nodeDatabaseLogicalBeforeSha256: '3'.repeat(64),
    nodeDatabaseLogicalAfterSha256: '4'.repeat(64),
    nodeAttachmentBytesSha256: '5'.repeat(64),
    attachmentStorageGuardSha256: 'f'.repeat(64),
    intakeSchemaSha256: '1'.repeat(64),
    attachmentSchemaSha256: '2'.repeat(64),
    openapiRoutes: [
      '/api/v1/projects/{project_id}/requirements',
      '/api/v1/projects/{project_id}/requirements/{intake_id}/plan-links',
      '/api/v1/projects/{project_id}/requirements/{intake_id}/attachments',
      '/api/v1/projects/{project_id}/feedback',
      '/api/v1/projects/{project_id}/feedback/{intake_id}/plan-links',
      '/api/v1/projects/{project_id}/feedback/{intake_id}/attachments',
      '/api/v1/attachments/{attachment_id}/content',
      '/api/v1/attachments/{attachment_id}',
    ],
  };
}

test('argument and structured output parsing are strict', () => {
  assert.deepEqual(parseArgs(['verify']), { mode: 'verify' });
  assert.throws(() => parseArgs([]), /usage:/);
  assert.deepEqual(parseStructuredOutput('diagnostic\n{"ok":true}\n'), { ok: true });
  assert.equal(parseStructuredOutput('diagnostic only'), null);
});

test('safe environment removes credential, database and prior-stage control variables', () => {
  const temporaryRoot = path.join(os.tmpdir(), 'autoplan-p06-verify-env');
  const safe = safeEnvironment(temporaryRoot, {
    PATH: 'safe', API_TOKEN: 'secret', DB_PATH: 'unsafe', AUTOPLAN_P06_GO_GOLDEN: 'outside',
  });
  assert.equal(safe.environment.PATH, 'safe');
  assert.equal(safe.environment.API_TOKEN, undefined);
  assert.equal(safe.environment.DB_PATH, undefined);
  assert.equal(safe.environment.AUTOPLAN_P06_GO_GOLDEN, undefined);
  assert.equal(safe.removedCount, 3);
  assert.equal(safe.environment.AUTOPLAN_P06_VERIFY, '1');
  assert.ok(!sanitizeLog(temporaryRoot, process.cwd(), temporaryRoot).includes(temporaryRoot));
});

test('test control scan rejects skip and only markers in P06 scoped tests', (t) => {
  const root = fixtureRoot(t);
  const relative = 'backend/internal/application/intake/golden_test.go';
  const target = path.join(root, relative);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, ['t.' + 'Skip("hidden test")', 'test.' + 'only("hidden node test", () => {})'].join('\n'));
  assert.deepEqual(testControlViolations(root, [relative]), [relative + ':1', relative + ':2']);
});

test('P06 command plan serializes Node golden generation before every Go writer', () => {
  const expectations = { commands: {
    check: { outcome: 'success', allowedFailureSignatures: [] },
    test: { outcome: 'success', allowedFailureSignatures: [] },
  } };
  assert.deepEqual(p06Commands(expectations).map((item) => item.id), [
    'p06-safety-preflight', 'node-golden-generator', 'go-repository', 'go-intake-application',
    'go-attachments', 'go-http', 'go-mcp', 'go-files', 'renderer-intake-transport',
    'p06-golden-contract', 'p06-orchestration-tests', 'check', 'test',
  ]);
});

test('coverage evidence records Intake, attachment, audit, transport and golden matrices', () => {
  const expectations = { commands: {
    check: { outcome: 'success', allowedFailureSignatures: [] },
    test: { outcome: 'success', allowedFailureSignatures: [] },
  } };
  const commands = [{ id: 'p05-gate' }, ...p06Commands(expectations)].map((item) => ({
    id: item.id, evaluation: { accepted: true },
  }));
  const coverage = coverageEvidence(commands, sourceSafety(), { ok: true, artifacts: ['node-intake.golden.json'] });
  assert.ok(coverage.matrix.every((item) => item.verified));
  assert.equal(coverage.openapiCoverage.rendererAccepted, true);
  assert.equal(coverage.nodeGolden.sameCopyConcurrentWritersAllowed, false);
});

test('failed P05 gate creates blocked evidence before P06 writers', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'blocked-run',
    sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      return {
        exitCode: 7, signal: null, error: null, stdout: '', stderr: 'p05 failed',
        startedAt: '2026-07-11T09:00:00.000Z', endedAt: '2026-07-11T09:00:00.010Z',
      };
    },
    inspectP05Evidence() { throw new Error('must not inspect evidence'); },
  });
  assert.deepEqual(calls, ['p05-gate']);
  assert.equal(result.summary.status, 'blocked');
  assert.equal(result.summary.ok, false);
  assert.equal(result.summary.databaseOwnership.authorizedCopiesOnly, false);
  assert.equal(result.summary.commandResults[0].exitCode, 7);
  assert.equal(result.summary.temporaryCleanup.cleaned, true);
});

test('failed Node golden generation stops before the first Go writer', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  let sequence = 0;
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'node-failed-run',
    sourceFiles: [], environment: {}, gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      const start = Date.parse('2026-07-11T09:30:00.000Z') + sequence++ * 20;
      return {
        exitCode: spec.id === 'node-golden-generator' ? 1 : 0, signal: null, error: null, stdout: '', stderr: '',
        startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString(),
      };
    },
    inspectP05Evidence: () => ({ stage: 'p05', runId: 'accepted', sourceHashesStable: true }),
    inspectSourceSafety: sourceSafety,
  });
  assert.deepEqual(calls, ['p05-gate', 'p06-safety-preflight', 'node-golden-generator']);
  assert.equal(result.summary.status, 'failed');
  assert.equal(result.summary.failure, 'node-golden-generator_failed_stopped_remaining_steps');
  assert.equal(result.summary.ok, false);
});

test('successful verification emits immutable, sanitized and sequential evidence', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root, expectations: 'config/expectations.json', evidenceRoot: 'evidence', runId: 'completed-run',
    sourceFiles: [], environment: { PATH: process.env.PATH || '' }, gitStatus: cleanGitStatus,
    executeCommand: successfulExecutor(calls),
    inspectP05Evidence: () => ({ stage: 'p05', runId: 'accepted', sourceHashesStable: true }),
    inspectSourceSafety: sourceSafety,
  });
  assert.equal(calls[0], 'p05-gate');
  assert.equal(result.summary.status, 'completed');
  assert.equal(result.summary.ok, true);
  assert.equal(result.summary.nodeGolden.ok, true);
  assert.equal(result.summary.safety.writerTimeline.nodeClosedBeforeGo, true);
  assert.equal(result.summary.temporaryCleanup.cleaned, true);
  assert.ok(fs.existsSync(path.join(result.runDir, 'summary.json')));
  assert.ok(fs.existsSync(path.join(result.runDir, 'evidence-manifest.json')));
});

test('cleanup refuses any directory not owned by the P06 verifier', (t) => {
  const root = fixtureRoot(t);
  assert.deepEqual(cleanupTemporaryRoot(root), { cleaned: false, error: 'refused_non_owned_cleanup' });
  assert.equal(fs.existsSync(root), true);
});
