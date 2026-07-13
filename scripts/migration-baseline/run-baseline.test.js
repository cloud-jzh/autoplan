'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  changedStatusEntries,
  cleanupSmokeEnvironment,
  commandSpecs,
  directoryDelta,
  evaluateCommand,
  failureSignatures,
  hashSources,
  parseArgs,
  runBaseline,
  safeSmokeEnvironment,
  sanitizeForSignature,
  smokeSafety,
  stableRunId,
  testControlViolations,
} = require('./run-baseline');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS_PATH = path.join(ROOT, 'docs/migration/p00/baseline-expectations.json');

test('package exposes the single P00 verification entry point', () => {
  const packageJson = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
  assert.equal(packageJson.scripts['migration:p00:verify'], 'node scripts/migration-baseline/run-baseline.js verify');
  assert.deepEqual(parseArgs(['verify']), { mode: 'verify' });
  assert.throws(() => parseArgs([]), /usage/);
  assert.throws(() => parseArgs(['verify', '--update']), /usage/);
});

test('expectations freeze command order and only the precise existing check signature', () => {
  const expectations = JSON.parse(fs.readFileSync(EXPECTATIONS_PATH, 'utf8'));
  assert.deepEqual(expectations.commandOrder, ['specialized', 'check', 'test', 'build', 'smoke']);
  assert.equal(expectations.commands.check.outcome, 'exact-known-failure');
  assert.deepEqual(expectations.commands.check.allowedExitCodes, [1]);
  assert.deepEqual(expectations.commands.check.allowedFailureSignatures, [
    'file-length|scripts/smoke-test.js|limit=3800',
  ]);
  for (const id of ['specialized', 'test', 'build', 'smoke']) {
    assert.equal(expectations.commands[id].outcome, 'success');
    assert.deepEqual(expectations.commands[id].allowedFailureSignatures, []);
  }
});

test('every controlled source exists and can be hashed without changing it', () => {
  const expectations = JSON.parse(fs.readFileSync(EXPECTATIONS_PATH, 'utf8'));
  const hashes = hashSources(ROOT, expectations.sourceFiles);
  assert.equal(hashes.length, expectations.sourceFiles.length);
  assert.equal(hashes.every((item) => /^[a-f0-9]{64}$/.test(item.sha256) && item.bytes > 0), true);
  assert.deepEqual(hashes.map((item) => item.path), expectations.sourceFiles);
});

test('evidence definition requires both raw logs, repository snapshots, summary, and hashes', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(ROOT, 'docs/migration/p00/evidence/manifest.json'), 'utf8'));
  const required = manifest.requiredArtifacts.map((item) => item.path);
  for (const file of [
    'git-status-start.json',
    'git-status-end.json',
    'dist-start.json',
    'dist-end.json',
    '02-check.stdout.log',
    '02-check.stderr.log',
    '05-smoke.stdout.log',
    '05-smoke.stderr.log',
    'summary.json',
    'evidence-manifest.json',
  ]) assert.equal(required.includes(file), true, file);
  assert.equal(manifest.integrity.algorithm, 'sha256');
  assert.equal(manifest.runDirectory.overwrite, 'never');
});

test('failure extraction normalizes known source, test, dependency, and TypeScript failures', () => {
  const stdout = [
    '- scripts/smoke-test.js: 4130 lines (limit 3800)',
    'src/example.ts(2,3): error TS2322: Type string is not assignable',
    'not ok 7 - preserves event order # time=12.3ms',
  ].join('\n');
  const stderr = "Error: Cannot find module 'synthetic-module'";
  assert.deepEqual(failureSignatures(stdout, stderr, ROOT), [
    'dependency|module-not-found|synthetic-module',
    'file-length|scripts/smoke-test.js|limit=3800',
    'node-test|preserves event order',
    'typescript|src/example.ts|TS2322|Type string is not assignable',
  ]);
  assert.equal(sanitizeForSignature(`${ROOT}/file.js 12.5ms`, ROOT), '<repo>/file.js <duration>');
});

test('known-red evaluation accepts only the exact exit code and exact signature set', () => {
  const expectation = {
    outcome: 'exact-known-failure',
    allowedExitCodes: [1],
    allowedFailureSignatures: ['file-length|scripts/smoke-test.js|limit=3800'],
  };
  const exact = { exitCode: 1, failureSignatures: ['file-length|scripts/smoke-test.js|limit=3800'] };
  assert.equal(evaluateCommand(expectation, exact).accepted, true);
  assert.equal(evaluateCommand(expectation, { ...exact, exitCode: 0, failureSignatures: [] }).accepted, false);
  assert.equal(evaluateCommand(expectation, { ...exact, failureSignatures: ['node-test|new failure'] }).accepted, false);
  assert.equal(evaluateCommand(expectation, { ...exact, exitCode: 2 }).accepted, false);
  assert.equal(evaluateCommand({ outcome: 'success' }, { exitCode: 1 }).accepted, false);
  assert.equal(evaluateCommand({ outcome: 'success' }, { exitCode: 0 }).accepted, true);
  const extra = failureSignatures('- scripts/smoke-test.js: 4130 lines (limit 3800)', 'Error: unexpected explosion', ROOT);
  assert.deepEqual(extra, ['diagnostic|Error: unexpected explosion', 'file-length|scripts/smoke-test.js|limit=3800']);
  assert.equal(evaluateCommand(expectation, { ...exact, failureSignatures: extra }).accepted, false);
});

test('status and dist comparisons retain additions, removals, and modifications', () => {
  assert.deepEqual(changedStatusEntries(
    { entries: [' M src/a.js', '?? old.txt'], contentHashes: { 'src/a.js': 'old', 'old.txt': 'gone' } },
    { entries: [' M src/a.js', '?? new.txt'], contentHashes: { 'src/a.js': 'new', 'new.txt': 'added' } },
  ), {
    appeared: ['?? new.txt'],
    disappeared: ['?? old.txt'],
    contentChanged: ['new.txt', 'old.txt', 'src/a.js'],
  });
  assert.deepEqual(directoryDelta(
    [{ path: 'dist/a.js', sha256: 'old', bytes: 1 }, { path: 'dist/gone.js', sha256: 'x', bytes: 1 }],
    [{ path: 'dist/a.js', sha256: 'new', bytes: 2 }, { path: 'dist/new.js', sha256: 'y', bytes: 1 }],
  ).map(({ path: file, change }) => ({ path: file, change })), [
    { path: 'dist/a.js', change: 'modified' },
    { path: 'dist/gone.js', change: 'deleted' },
    { path: 'dist/new.js', change: 'added' },
  ]);
});

test('smoke preflight proves temporary database, cleanup, stubs, and synthetic credentials', () => {
  const result = smokeSafety(ROOT);
  assert.deepEqual(result.reasons, []);
  assert.equal(result.safe, true);
  assert.equal(result.checks.includes('temporary database'), true);
  assert.equal(result.checks.includes('stubbed agent child'), true);
});

test('safe smoke environment removes credential values and allocates a unique temporary root', () => {
  const safe = safeSmokeEnvironment({ PATH: 'synthetic-path', OPENAI_API_KEY: 'must-not-propagate', SESSION_TOKEN: 'must-not-propagate' });
  try {
    assert.equal(safe.environment.PATH, 'synthetic-path');
    assert.equal(Object.hasOwn(safe.environment, 'OPENAI_API_KEY'), false);
    assert.equal(Object.hasOwn(safe.environment, 'SESSION_TOKEN'), false);
    assert.deepEqual(safe.removedSecretVariableNames, ['OPENAI_API_KEY', 'SESSION_TOKEN']);
    assert.equal(safe.environment.TEMP, safe.temporaryRoot);
    assert.equal(path.dirname(safe.temporaryRoot), path.resolve(os.tmpdir()));
  } finally {
    assert.deepEqual(cleanupSmokeEnvironment(safe.temporaryRoot), { cleaned: true, error: null });
  }
  assert.equal(cleanupSmokeEnvironment(ROOT).cleaned, false);
});

test('command construction uses no shell and discovers every migration test', () => {
  const expectations = JSON.parse(fs.readFileSync(EXPECTATIONS_PATH, 'utf8'));
  const commands = commandSpecs(ROOT, expectations, { AUTOPLAN_P00_SAFE_SMOKE: '1' });
  assert.deepEqual(commands.map((item) => item.id), expectations.commandOrder);
  const specialized = commands.find((item) => item.id === 'specialized');
  assert.equal(specialized.executable, process.execPath);
  assert.equal(specialized.args[0], '--test');
  assert.equal(specialized.args.some((item) => item.endsWith('run-baseline.test.js')), true);
  const smoke = commands.find((item) => item.id === 'smoke');
  assert.equal(smoke.displayExecutable, process.platform === 'win32' ? 'npm.cmd' : 'npm');
  assert.deepEqual(smoke.displayArgs, ['run', 'smoke']);
  assert.equal(smoke.env.AUTOPLAN_P00_SAFE_SMOKE, '1');
});

test('driver preserves real child exits while accepting only the frozen check red light', async () => {
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p00-baseline-test-'));
  try {
    const fakeExecute = async (command) => {
      const failedCheck = command.id === 'check';
      return {
        exitCode: failedCheck ? 1 : 0,
        signal: null,
        error: null,
        stdout: failedCheck ? 'File length check failed:\n- scripts/smoke-test.js: 4130 lines (limit 3800)\n' : `${command.id} ok\n`,
        stderr: '',
        startedAt: '2026-07-11T03:16:21.000Z',
        endedAt: '2026-07-11T03:16:22.000Z',
      };
    };
    const result = await runBaseline({ evidenceRoot: temporaryRoot, runId: 'synthetic-run', execute: fakeExecute });
    assert.equal(result.summary.ok, true);
    assert.deepEqual(result.summary.commandResults.map((item) => item.exitCode), [0, 1, 0, 0, 0]);
    assert.equal(result.summary.commandResults[1].evaluation.knownFailure, true);
    assert.equal(result.summary.evidenceCompleteness.complete, true);
    assert.equal(result.summary.sourceHashesStable, true);
    assert.equal(fs.existsSync(path.join(result.runDir, '02-check.stdout.log')), true);
    const evidence = JSON.parse(fs.readFileSync(path.join(result.runDir, 'evidence-manifest.json'), 'utf8'));
    assert.equal(evidence.artifacts.some((item) => item.path === 'summary.json' && /^[a-f0-9]{64}$/.test(item.sha256)), true);
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test('focused and skipped test controls are absent and run ids are immutable-friendly', () => {
  assert.deepEqual(testControlViolations(ROOT), []);
  const id = stableRunId(new Date('2026-07-11T03:16:21.123Z'), 42);
  assert.equal(id, '2026-07-11T03-16-21-123Z-pid-42');
});
