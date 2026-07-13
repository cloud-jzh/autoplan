'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  cleanupTemporaryRoot,
  coverageEvidence,
  parseArgs,
  parseStructuredOutput,
  p05Commands,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  testControlViolations,
  verifyStageEvidence,
} = require('./verify');

function fixtureRoot(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p05-test-'));
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
  const epoch = Date.parse('2026-07-11T06:00:00.000Z');
  return async (spec) => {
    const index = calls.length;
    calls.push(spec.id);
    const structured = spec.id === 'mutation-golden-compare'
      ? { ok: true, writerTimeline: ['node-golden-read', 'node-closed', 'go-open', 'go-closed'] }
      : (spec.id === 'p05-safety-preflight' ? { ok: true, schemaVersion: 1 } : null);
    return {
      exitCode: 0,
      signal: null,
      error: null,
      stdout: structured ? JSON.stringify(structured) + '\n' : '',
      stderr: '',
      startedAt: new Date(epoch + index * 20).toISOString(),
      endedAt: new Date(epoch + index * 20 + 10).toISOString(),
    };
  };
}

test('argument and structured output parsing are strict', () => {
  assert.deepEqual(parseArgs(['verify']), { mode: 'verify' });
  assert.throws(() => parseArgs([]), /usage:/);
  assert.deepEqual(parseStructuredOutput('diagnostic\n{"ok":true}\n'), { ok: true });
  assert.equal(parseStructuredOutput('diagnostic only'), null);
});

test('safe environment removes credential-shaped variables and log sanitizer removes paths', () => {
  const temporaryRoot = path.join(os.tmpdir(), 'autoplan-p05-verify-env');
  const safe = safeEnvironment(temporaryRoot, {
    PATH: 'safe', API_TOKEN: 'secret', DB_PATH: 'unsafe', AUTOPLAN_P05_ALLOWED_ROOT: 'outside',
  });
  assert.equal(safe.environment.PATH, 'safe');
  assert.equal(safe.environment.API_TOKEN, undefined);
  assert.equal(safe.environment.DB_PATH, undefined);
  assert.equal(safe.removedCount, 3);
  assert.equal(safe.environment.AUTOPLAN_P05_ALLOWED_ROOT, undefined);
  assert.equal(safe.environment.AUTOPLAN_P05_VERIFY, '1');
  assert.ok(!sanitizeLog(temporaryRoot, process.cwd(), temporaryRoot).includes(temporaryRoot));
});

test('test control scan only permits the two fail-closed golden export sentinels', (t) => {
  const root = fixtureRoot(t);
  const relative = 'backend/internal/application/projects/golden_test.go';
  const target = path.join(root, relative);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, [
    't.Skip("controlled golden export environment is absent")',
    't.Skip("hidden ordinary test")',
    't.Skip("controlled P05 mutation export environment is absent")',
  ].join('\n'));
  assert.deepEqual(testControlViolations(root, [relative]), [relative + ':2']);
});

test('P05 command plan keeps writer and transport stages in deterministic order', () => {
  const expectations = { commands: {
    check: { outcome: 'success', allowedFailureSignatures: [] },
    test: { outcome: 'success', allowedFailureSignatures: [] },
  } };
  assert.deepEqual(p05Commands(expectations).map((item) => item.id), [
    'node-write-contracts', 'go-repository', 'go-application', 'go-http', 'go-files',
    'renderer-transport', 'mutation-golden-compare', 'p05-safety-preflight',
    'p05-orchestration-tests', 'check', 'test',
  ]);
});

test('coverage evidence exposes CRUD, concurrency, paths, OpenAPI, and golden results', () => {
  const expectations = { commands: {
    check: { outcome: 'success', allowedFailureSignatures: [] },
    test: { outcome: 'success', allowedFailureSignatures: [] },
  } };
  const commands = [{ id: 'p04-gate' }, ...p05Commands(expectations)].map((item) => ({
    id: item.id, evaluation: { accepted: true },
  }));
  const coverage = coverageEvidence(commands, { openapiRoutes: ['/api/v1/projects'] }, { ok: true });
  assert.ok(coverage.mutationChecks.every((item) => item.verified));
  assert.ok(coverage.pathRejectionChecks.every((item) => item.verified));
  assert.equal(coverage.openapiClientCoverage.rendererDualTransportAccepted, true);
  assert.equal(coverage.goldenDiff.ignoredUnknownFields, false);
});

test('failed P04 gate blocks before every P05 writer', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root,
    expectations: 'config/expectations.json',
    evidenceRoot: 'evidence',
    runId: 'blocked-run',
    sourceFiles: [],
    environment: {},
    gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      return {
        exitCode: 1, signal: null, error: null, stdout: '', stderr: 'p04 failed',
        startedAt: '2026-07-11T06:00:00.000Z', endedAt: '2026-07-11T06:00:00.010Z',
      };
    },
    verifyStageEvidence() { throw new Error('must not inspect evidence'); },
  });
  assert.deepEqual(calls, ['p04-gate']);
  assert.equal(result.summary.status, 'blocked');
  assert.equal(result.summary.ok, false);
  assert.equal(result.summary.databaseOwnership.authorizedCopiesOnly, false);
  assert.equal(result.summary.temporaryCleanup.cleaned, true);
});

test('a failed Node contract stops before the first Go command', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  let sequence = 0;
  const result = await runVerification({
    rootDir: root,
    expectations: 'config/expectations.json',
    evidenceRoot: 'evidence',
    runId: 'node-failed-run',
    sourceFiles: [],
    environment: {},
    gitStatus: cleanGitStatus,
    executeCommand: async (spec) => {
      calls.push(spec.id);
      const start = Date.parse('2026-07-11T06:30:00.000Z') + sequence++ * 20;
      return {
        exitCode: spec.id === 'node-write-contracts' ? 1 : 0,
        signal: null, error: null, stdout: '', stderr: '',
        startedAt: new Date(start).toISOString(), endedAt: new Date(start + 10).toISOString(),
      };
    },
    verifyStageEvidence: () => ({ stage: 'p04', runId: 'accepted', sourceHashesStable: true }),
    inspectSourceSafety: () => ({
      ok: true, databaseOwnerGuardSha256: 'a'.repeat(64), openapiRoutes: [],
    }),
  });
  assert.deepEqual(calls, ['p04-gate', 'node-write-contracts']);
  assert.equal(result.summary.status, 'failed');
  assert.equal(result.summary.failure, 'node-write-contracts_failed_stopped_remaining_steps');
  assert.equal(result.summary.ok, false);
});

test('successful verification records sequential commands, golden output, and safe evidence', async (t) => {
  const root = fixtureRoot(t);
  const calls = [];
  const result = await runVerification({
    rootDir: root,
    expectations: 'config/expectations.json',
    evidenceRoot: 'evidence',
    runId: 'completed-run',
    sourceFiles: [],
    environment: { PATH: process.env.PATH || '' },
    gitStatus: cleanGitStatus,
    executeCommand: successfulExecutor(calls),
    verifyStageEvidence: () => ({ stage: 'p04', runId: 'accepted', sourceHashesStable: true }),
    inspectSourceSafety: () => ({
      ok: true, schemaVersion: 1, databaseOwnerGuardSha256: 'a'.repeat(64),
      openapiRoutes: [
        '/api/v1/projects', '/api/v1/projects/{project_id}',
        '/api/v1/projects/{project_id}/loop-config', '/api/v1/projects/{project_id}/snapshot',
      ],
    }),
  });
  assert.equal(calls[0], 'p04-gate');
  assert.equal(result.summary.status, 'completed');
  assert.equal(result.summary.ok, true);
  assert.equal(result.summary.mutationGolden.ok, true);
  assert.equal(result.summary.safety.writerTimeline.nodeClosedBeforeGo, true);
  assert.equal(result.summary.temporaryCleanup.cleaned, true);
  assert.ok(fs.existsSync(path.join(result.runDir, 'summary.json')));
  assert.ok(fs.existsSync(path.join(result.runDir, 'evidence-manifest.json')));
});

test('previous-stage evidence must be immutable and hash-linked', (t) => {
  const root = fixtureRoot(t);
  const run = path.join(root, 'docs/migration/p04/evidence/runs/20260711');
  fs.mkdirSync(run, { recursive: true });
  const summaryBytes = Buffer.from(JSON.stringify({
    schemaVersion: 1, status: 'completed', ok: true, sourceHashesStable: true,
  }) + '\n');
  fs.writeFileSync(path.join(run, 'summary.json'), summaryBytes);
  const digest = require('node:crypto').createHash('sha256').update(summaryBytes).digest('hex');
  fs.writeFileSync(path.join(run, 'evidence-manifest.json'), JSON.stringify({
    schemaVersion: 1,
    immutableRunDirectory: true,
    artifacts: [{ path: 'summary.json', sha256: digest }],
  }));
  assert.equal(verifyStageEvidence(root).summarySha256, digest);
  fs.writeFileSync(path.join(run, 'summary.json'), '{}');
  assert.throws(() => verifyStageEvidence(root), /p04_evidence_invalid/);
});

test('cleanup refuses any directory not owned by the P05 verifier', (t) => {
  const root = fixtureRoot(t);
  assert.deepEqual(cleanupTemporaryRoot(root), { cleaned: false, error: 'refused_non_owned_cleanup' });
  assert.equal(fs.existsSync(root), true);
});
