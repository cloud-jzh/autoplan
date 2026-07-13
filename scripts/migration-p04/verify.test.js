'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { generateFixtures } = require('./generate-fixtures');
const {
  cleanupTemporaryRoot,
  evaluateResult,
  parseArgs,
  parseStructuredReport,
  p04Commands,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  secretFindings,
  summarizeFixtureMatrix,
  verifyStageEvidence,
} = require('./verify');

const ROOT = path.resolve(__dirname, '..', '..');

function digest(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function temporaryRoot(t, prefix = 'autoplan-p04-test-') {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function writeJson(name, value) {
  fs.mkdirSync(path.dirname(name), { recursive: true });
  fs.writeFileSync(name, JSON.stringify(value, null, 2) + '\n', 'utf8');
}

test('parseArgs accepts only the complete verify mode', () => {
  assert.deepEqual(parseArgs(['verify']), { mode: 'verify' });
  for (const invalid of [[], ['run'], ['verify', '--force'], ['--help']]) {
    assert.throws(() => parseArgs(invalid), /usage:/);
  }
});

test('safe environment removes sensitive names and confines all temporary variables', () => {
  const result = safeEnvironment('fixture-temporary-root', {
    PATH: 'fixture-path',
    API_KEY: 'credential-shaped-value',
    AUTOPLAN_DATABASE_PATH: 'unsafe-database-path',
    SESSION_COOKIE: 'unsafe-session',
    LANG: 'en_US.UTF-8',
  });
  assert.deepEqual(result.environment, {
    PATH: 'fixture-path',
    LANG: 'en_US.UTF-8',
    TEMP: 'fixture-temporary-root',
    TMP: 'fixture-temporary-root',
    TMPDIR: 'fixture-temporary-root',
    GOTMPDIR: 'fixture-temporary-root',
    AUTOPLAN_P04_VERIFY: '1',
  });
  assert.equal(result.removedCount, 3);
});

test('logs are sanitized and credential-shaped output is rejected before evidence write', () => {
  const root = path.resolve('fixture-repository-root');
  const temporary = path.join(os.tmpdir(), 'autoplan-p04-fixture');
  const unsafe = [
    root,
    temporary,
    os.homedir(),
    'Authorization: Bearer abcdefghijklmnopqrstuvwxyz',
    'api_key=sk-fixture-abcdefghijklmnop',
    'env_vars=fixture',
  ].join('\n');
  const sanitized = sanitizeLog(unsafe, root, temporary);
  assert.equal(sanitized.includes(root), false);
  assert.equal(sanitized.includes(temporary), false);
  assert.equal(sanitized.includes(os.homedir()), false);
  assert.equal(sanitized.includes('abcdefghijklmnop'), false);
  assert.deepEqual(secretFindings(unsafe, ''), ['usable-key', 'usable-bearer', 'credential-value']);
  assert.deepEqual(secretFindings('status=blocked code=source_invalid', ''), []);
});

test('structured expected failures require both a real non-zero exit and a stable blocked code', () => {
  const spec = {
    outcome: 'structured-failure',
    allowedExitCodes: [1, 3],
    allowedCodes: ['source_invalid'],
  };
  const stdout = JSON.stringify({
    schema_version: 1,
    command: 'preflight',
    status: 'blocked',
    code: 'source_invalid',
  }) + '\n';
  assert.equal(parseStructuredReport('go diagnostic\n' + stdout).code, 'source_invalid');
  assert.equal(evaluateResult(spec, { exitCode: 1, stdout }).accepted, true);
  assert.equal(evaluateResult(spec, { exitCode: 0, stdout }).accepted, false);
  assert.equal(evaluateResult(spec, {
    exitCode: 3,
    stdout: JSON.stringify({ status: 'blocked', code: 'unexpected' }),
  }).accepted, false);
});

test('command plan includes drift, fixtures, process preflight, fault, audit, lock, readiness and baselines', () => {
  const expectations = {
    commands: {
      check: { outcome: 'exact-known-failure', allowedExitCodes: [1], allowedFailureSignatures: [] },
      test: { outcome: 'success', allowedFailureSignatures: [] },
    },
  };
  const plan = p04Commands(expectations, path.join(os.tmpdir(), 'autoplan-p04-plan'));
  const ids = [plan.generate.id, ...plan.afterGeneration.map((item) => item.id)];
  assert.deepEqual(ids, [
    'generate-fixtures',
    'inventory-guard',
    'fixture-contracts',
    'cli-preflight',
    'cli-corrupt-preflight',
    'go-migration-workflow',
    'go-audit',
    'go-owner-lock',
    'go-readiness',
    'node-schema-rejection',
    'go-all',
    'check',
    'test',
  ]);
  const fault = plan.afterGeneration.find((item) => item.id === 'cli-corrupt-preflight');
  assert.equal(fault.outcome, 'structured-failure');
  assert.deepEqual(fault.allowedCodes, ['source_invalid']);
  for (const spec of [plan.generate, ...plan.afterGeneration]) {
    assert.equal(spec.command.includes('autoplan.sqlite'), false);
  }
});

test('completed prerequisite evidence must bind summary bytes in an immutable manifest', (t) => {
  const root = temporaryRoot(t);
  const run = path.join(root, 'docs', 'migration', 'p03', 'evidence', 'runs', 'fixture-run');
  const summary = Buffer.from(JSON.stringify({
    schemaVersion: 1,
    status: 'completed',
    ok: true,
    sourceHashesStable: true,
  }, null, 2) + '\n');
  fs.mkdirSync(run, { recursive: true });
  fs.writeFileSync(path.join(run, 'summary.json'), summary);
  writeJson(path.join(run, 'evidence-manifest.json'), {
    schemaVersion: 1,
    immutableRunDirectory: true,
    artifacts: [{ path: 'summary.json', sha256: digest(summary) }],
  });
  const evidence = verifyStageEvidence(root, 'p03');
  assert.equal(evidence.runId, 'fixture-run');
  assert.equal(evidence.summarySha256, digest(summary));
  writeJson(path.join(run, 'evidence-manifest.json'), {
    schemaVersion: 1,
    immutableRunDirectory: true,
    artifacts: [{ path: 'summary.json', sha256: '0'.repeat(64) }],
  });
  assert.throws(() => verifyStageEvidence(root, 'p03'), /p03_evidence_invalid/);
});

test('fixture matrix records versions, row-count deltas, no-op and immutable restore hashes without rows', async (t) => {
  const root = temporaryRoot(t);
  const output = path.join(root, 'fixtures');
  await generateFixtures(output);
  const matrix = await summarizeFixtureMatrix(ROOT, output);
  assert.equal(matrix.schemaVersion, 1);
  assert.equal(matrix.databaseContentCaptured, false);
  assert.equal(matrix.fixtures.length, 18);
  const current = matrix.fixtures.find((item) => item.id === 'current-node-valid');
  const migrated = matrix.fixtures.find((item) => item.id === 'schema-v1');
  const blocked = matrix.fixtures.find((item) => item.id === 'truncated-file');
  assert.equal(current.fromVersion, 0);
  assert.equal(current.toVersion, 1);
  assert.equal(current.ledgerValid, true);
  assert.equal(current.secondRunNoOp, true);
  assert.equal(current.sourceSha256, current.expectedBackupSha256);
  assert.equal(current.sourceSha256, current.expectedRestoreSha256);
  assert.equal(Array.isArray(current.auditDifferences), true);
  assert.equal(migrated.expectedResult, 'no-op');
  assert.equal(blocked.expectedCode, 'source_invalid');
  assert.equal(JSON.stringify(matrix).includes('fixture-message'), false);
  assert.equal(JSON.stringify(matrix).includes(root), false);
});

test('hard-gate failure records blocked evidence and never starts P04 fixture generation', async (t) => {
  const root = temporaryRoot(t);
  writeJson(path.join(root, 'docs', 'migration', 'p00', 'baseline-expectations.json'), {
    commands: {
      check: { outcome: 'success', allowedFailureSignatures: [] },
      test: { outcome: 'success', allowedFailureSignatures: [] },
    },
  });
  const calls = [];
  const executeCommand = async (spec) => {
    calls.push(spec.id);
    return {
      exitCode: 1,
      signal: null,
      error: null,
      stdout: '',
      stderr: 'synthetic gate failure',
      startedAt: '2026-07-11T04:00:00.000Z',
      endedAt: '2026-07-11T04:00:01.000Z',
    };
  };
  const { summary, runDir } = await runVerification({
    rootDir: root,
    runId: 'fixture-blocked-run',
    environment: { PATH: process.env.PATH || '' },
    executeCommand,
  });
  assert.deepEqual(calls, ['p02-gate']);
  assert.equal(summary.status, 'blocked');
  assert.equal(summary.ok, false);
  assert.equal(summary.environment.electronUserDataAccessed, false);
  assert.equal(fs.existsSync(path.join(runDir, 'summary.json')), true);
  assert.equal(fs.existsSync(path.join(runDir, 'fixture-matrix.json')), false);
});

test('cleanup refuses foreign paths and removes only its owned temporary root', (t) => {
  const foreign = temporaryRoot(t, 'foreign-p04-');
  assert.deepEqual(cleanupTemporaryRoot(foreign), {
    cleaned: false,
    error: 'refused_non_owned_cleanup',
  });
  const owned = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p04-verify-'));
  assert.deepEqual(cleanupTemporaryRoot(owned), { cleaned: true, error: null });
  assert.equal(fs.existsSync(owned), false);
});
