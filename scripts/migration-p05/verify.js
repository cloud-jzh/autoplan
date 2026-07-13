'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const {
  evaluateCommand,
  failureSignatures,
  sanitizeForSignature,
  stableRunId,
} = require('../migration-baseline/run-baseline');
const {
  inspectEvidenceSummary,
  inspectSourceSafety,
  isOwnedTemporaryRoot,
  scanSensitiveText,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p05/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p05-verify-';
const SENSITIVE_ENV = /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie|userdata|database|db[_-]?path)/i;
const GOLDEN_CONTROL_ENV = /^AUTOPLAN_P0[35]_(?:DATABASE|ALLOWED_ROOT|GO_OUTPUT|PROJECT_ID)$/i;
const APPROVED_CONTROLLED_SKIPS = new Set([
  'backend/internal/application/projects/golden_test.go:t.Skip("controlled golden export environment is absent")',
  'backend/internal/application/projects/golden_test.go:t.Skip("controlled P05 mutation export environment is absent")',
]);
const SOURCE_FILES = [
  'package.json',
  'scripts/migration-p05/inventory-write-contract.js',
  'scripts/migration-p05/inventory-write-contract.test.js',
  'scripts/migration-p05/generate-node-golden.js',
  'scripts/migration-p05/compare-mutation-golden.js',
  'scripts/migration-p05/compare-mutation-golden.test.js',
  'scripts/migration-p05/check-safety.js',
  'scripts/migration-p05/check-safety.test.js',
  'scripts/migration-p05/verify.js',
  'scripts/migration-p05/verify.test.js',
  'fixtures/migration/p05/manifest.json',
  'fixtures/migration/p05/node-mutations.golden.json',
  'fixtures/migration/p05/expected-errors.json',
  'docs/migration/p05/write-contract.json',
  'docs/migration/p04/schema-inventory.json',
  'fixtures/migration/p04/manifest.json',
  'backend/migrations/0001_schema_v1.sql',
  'backend/openapi/openapi.yaml',
  'backend/internal/repository/sqlite/transaction.go',
  'backend/internal/repository/sqlite/project_contract_test.go',
  'backend/internal/application/projects/golden_test.go',
  'backend/internal/httpapi/projects_contract_test.go',
  'backend/internal/httpapi/file_policy_contract_test.go',
  'src/renderer/lib/api/httpClient.test.js',
  'src/renderer/lib/api/projectTransport.contract.test.js',
];

function toPosix(value) {
  return String(value).replace(/\\/g, '/');
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function writeJson(file, value) {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n', { encoding: 'utf8', flag: 'wx' });
}

function fileRecord(root, relative) {
  const target = path.join(root, relative);
  if (!fs.existsSync(target)) return { path: relative, missing: true };
  const bytes = fs.readFileSync(target);
  return { path: relative, bytes: bytes.length, sha256: sha256(bytes) };
}

function sanitizeLog(value, root, temporaryRoot) {
  let text = toPosix(value || '');
  for (const [target, replacement] of [
    [temporaryRoot, '<p05-temp>'], [root, '<repo>'], [os.homedir(), '<home>'], [os.tmpdir(), '<tmp>'],
  ]) {
    if (target) text = text.replaceAll(toPosix(target), replacement);
  }
  return text
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|tmp|var|private|mnt|opt)\/[^\s"'<>]*/g, '$1<absolute-path>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth|session|cookie)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/env_vars/gi, '<redacted-field>');
}

function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (SENSITIVE_ENV.test(name) || GOLDEN_CONTROL_ENV.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  Object.assign(environment, {
    TEMP: temporaryRoot,
    TMP: temporaryRoot,
    TMPDIR: temporaryRoot,
    GOTMPDIR: temporaryRoot,
    AUTOPLAN_P05_VERIFY: '1',
  });
  return { environment, removedCount };
}

function execute(spec, root, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: path.join(root, spec.cwd || '.'), env: environment, shell: false,
      windowsHide: true, windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    const timer = setTimeout(() => { if (!settled) child.kill(); }, spec.timeoutMS || 20 * 60 * 1000);
    const finish = (actual) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ...actual, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exitCode, signal: signal || null, error: null }));
  });
}

function successSpec(id, executable, args, description, cwd = '.') {
  return {
    id, executable, args, description, cwd,
    command: [(executable === process.execPath ? 'node' : executable), ...args].join(' '),
    outcome: 'success', allowedFailureSignatures: [],
  };
}

function npmSpec(id, args, expectation) {
  const command = 'npm.cmd ' + args.join(' ');
  if (process.platform === 'win32') {
    return {
      id, ...expectation, executable: process.env.ComSpec || 'cmd.exe',
      args: ['/d', '/s', '/c', 'call ' + command], windowsVerbatimArguments: true,
      command, cwd: '.',
    };
  }
  return { id, ...expectation, executable: 'npm', args, command: 'npm ' + args.join(' '), cwd: '.' };
}

function parseStructuredOutput(stdout) {
  const lines = String(stdout || '').split(/\r?\n/).filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    try {
      const value = JSON.parse(lines[index]);
      if (value && typeof value === 'object') return value;
    } catch {
      // Commands may print ordinary diagnostics before their final JSON record.
    }
  }
  return null;
}

async function runCommand(spec, state) {
  const actual = await (state.executeCommand || execute)(spec, state.root, state.environment);
  actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
    ? failureSignatures(actual.stdout, actual.stderr, state.root) : [];
  if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
    actual.failureSignatures = ['unclassified|sha256=' + sha256(
      sanitizeForSignature(String(actual.stdout) + '\n' + String(actual.stderr), state.root),
    )];
  }
  const rawFindings = scanSensitiveText(String(actual.stdout || '') + '\n' + String(actual.stderr || ''));
  const evaluation = evaluateCommand(spec, actual);
  if (rawFindings.length) {
    evaluation.accepted = false;
    evaluation.reason = 'credential-shaped output detected: ' + rawFindings.join(', ');
  }
  const prefix = String(state.results.length + 1).padStart(2, '0') + '-' + spec.id;
  const stdout = sanitizeLog(actual.stdout, state.root, state.temporaryRoot);
  const stderr = sanitizeLog(actual.stderr, state.root, state.temporaryRoot);
  const stdoutLog = prefix + '.stdout.log';
  const stderrLog = prefix + '.stderr.log';
  fs.writeFileSync(path.join(state.runDir, stdoutLog), stdout, 'utf8');
  fs.writeFileSync(path.join(state.runDir, stderrLog), stderr, 'utf8');
  const structuredOutput = parseStructuredOutput(stdout);
  const record = {
    id: spec.id,
    description: spec.description,
    command: sanitizeLog(spec.command, state.root, state.temporaryRoot),
    expectedOutcome: spec.outcome,
    exitCode: actual.exitCode,
    signal: actual.signal || null,
    startedAt: actual.startedAt,
    endedAt: actual.endedAt,
    failureSignatures: actual.failureSignatures.map((item) => sanitizeLog(item, state.root, state.temporaryRoot)),
    secretFindings: rawFindings,
    evaluation: { ...evaluation, reason: sanitizeLog(evaluation.reason, state.root, state.temporaryRoot) },
    structuredOutput,
    logs: {
      stdout: { path: stdoutLog, bytes: Buffer.byteLength(stdout), sha256: sha256(stdout) },
      stderr: { path: stderrLog, bytes: Buffer.byteLength(stderr), sha256: sha256(stderr) },
    },
  };
  state.results.push(record);
  return record;
}

function gitStatus(root) {
  const result = spawnSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], {
    cwd: root, encoding: 'utf8', windowsHide: true,
  });
  return {
    exitCode: typeof result.status === 'number' ? result.status : null,
    entries: String(result.stdout || '').split(/\r?\n/).filter(Boolean),
    stderr: String(result.stderr || ''),
  };
}

function statusPaths(status) {
  return status.entries.map((entry) => entry.slice(3).split(' -> ').at(-1)).map(toPosix).sort();
}

function verifyStageEvidence(root, stage = 'p04') {
  const runs = path.join(root, 'docs', 'migration', stage, 'evidence', 'runs');
  const names = fs.readdirSync(runs, { withFileTypes: true })
    .filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort().reverse();
  if (!names.length) throw new Error(stage + '_evidence_missing');
  const run = path.join(runs, names[0]);
  const summaryBytes = fs.readFileSync(path.join(run, 'summary.json'));
  const manifestBytes = fs.readFileSync(path.join(run, 'evidence-manifest.json'));
  const summary = JSON.parse(summaryBytes);
  const manifest = JSON.parse(manifestBytes);
  const digest = sha256(summaryBytes);
  const matched = Array.isArray(manifest.artifacts) && manifest.artifacts.some(
    (artifact) => artifact.path === 'summary.json' && artifact.sha256 === digest,
  );
  if (summary.schemaVersion !== 1 || summary.status !== 'completed' || summary.ok !== true ||
      summary.sourceHashesStable !== true || manifest.schemaVersion !== 1 ||
      manifest.immutableRunDirectory !== true || !matched) {
    throw new Error(stage + '_evidence_invalid');
  }
  return {
    stage, runId: names[0], summarySha256: digest,
    manifestSha256: sha256(manifestBytes), sourceHashesStable: true,
  };
}

function testControlViolations(root, sourceFiles = SOURCE_FILES) {
  const violations = [];
  for (const relative of sourceFiles.filter((file) => /(?:_test\.go|\.test\.(?:js|ts|tsx))$/.test(file))) {
    const target = path.join(root, relative);
    if (!fs.existsSync(target)) continue;
    fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
      const controlled = APPROVED_CONTROLLED_SKIPS.has(toPosix(relative) + ':' + line.trim());
      if (!controlled && (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line) ||
          /\bt\.Skip(?:f|Now)?\s*\(/.test(line))) {
        violations.push(relative + ':' + (index + 1));
      }
    });
  }
  return violations.sort();
}

function p05Commands(expectations) {
  return [
    successSpec('node-write-contracts', process.execPath,
      ['--test', 'scripts/migration-p05/inventory-write-contract.test.js'],
      'Generate and close the synthetic Node mutation bundle and verify its frozen write contract.'),
    successSpec('go-repository', 'go', ['test', './internal/repository/sqlite', '-count=1'],
      'Repository transactions, optimistic versioning, idempotency, and SQLite constraints.', 'backend'),
    successSpec('go-application', 'go', [
      'test', './internal/application/projects', './internal/application/config', './internal/application/files', '-count=1',
    ], 'Application mutation services, snapshots, configuration, and file policy.', 'backend'),
    successSpec('go-http', 'go', ['test', './internal/httpapi', '-count=1'],
      'HTTP mutation, stable error, version, and idempotency contracts.', 'backend'),
    successSpec('go-files', 'go', ['test', './internal/platform/filesystem', '-count=1'],
      'Authorized-root file access policy and path rejection contracts.', 'backend'),
    successSpec('renderer-transport', process.execPath, [
      '--test', 'src/renderer/lib/api/httpClient.test.js',
      'src/renderer/lib/api/projectTransport.contract.test.js',
    ], 'Renderer loopback transport, version, idempotency, and complete snapshot contracts.'),
    successSpec('mutation-golden-compare', process.execPath,
      ['scripts/migration-p05/compare-mutation-golden.js'],
      'Deep-compare normalized Node and Go mutation snapshots after the Node writer is closed.'),
    successSpec('p05-safety-preflight', process.execPath,
      ['scripts/migration-p05/check-safety.js', 'preflight'],
      'OpenAPI, frozen checksum, owner-copy, renderer, and sensitive fixture drift scan.'),
    successSpec('p05-orchestration-tests', process.execPath, [
      '--test', 'scripts/migration-p05/verify.test.js', 'scripts/migration-p05/check-safety.test.js',
      'scripts/migration-p05/compare-mutation-golden.test.js',
    ], 'P05 verifier, safety, deep comparator, and blocked-path contracts.'),
    npmSpec('check', ['run', 'check'], expectations.commands.check),
    npmSpec('test', ['test'], expectations.commands.test),
  ];
}

function cleanupTemporaryRoot(temporaryRoot) {
  if (!isOwnedTemporaryRoot(temporaryRoot)) return { cleaned: false, error: 'refused_non_owned_cleanup' };
  try {
    fs.rmSync(path.resolve(temporaryRoot), { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch {
    return { cleaned: false, error: 'owned_temporary_cleanup_failed' };
  }
}

function coverageEvidence(commandResults, sourceSafety, mutationGolden) {
  const accepted = (id) => commandResults.find((item) => item.id === id)?.evaluation.accepted === true;
  const mutationChecks = [
    ['create', ['go-repository', 'go-application', 'go-http', 'mutation-golden-compare']],
    ['update', ['go-repository', 'go-application', 'go-http', 'mutation-golden-compare']],
    ['configure', ['go-repository', 'go-application', 'go-http', 'mutation-golden-compare']],
    ['delete-and-relations', ['go-repository', 'go-application', 'go-http', 'mutation-golden-compare']],
    ['rollback-and-cancellation', ['go-repository', 'go-application']],
    ['concurrent-version-conflict', ['go-repository', 'go-application', 'go-http']],
    ['idempotent-replay', ['go-repository', 'go-application', 'go-http', 'renderer-transport']],
    ['idempotency-key-reuse-conflict', ['go-repository', 'go-application', 'go-http', 'renderer-transport']],
    ['missing-and-running-project-errors', ['go-application', 'go-http', 'mutation-golden-compare']],
  ].map(([scenario, commands]) => ({
    scenario, commands, verified: commands.every(accepted),
  }));
  const pathChecks = [
    'project-and-workspace-scope', 'custom-allowlist', 'all-scope-controlled-writes',
    'dotdot-and-absolute-path', 'windows-drive-unc-and-device-path',
    'symlink-junction-and-reparse', 'missing-write-target', 'directory-replacement-race',
  ].map((scenario) => ({
    scenario,
    commands: ['go-files', 'go-http'],
    verified: accepted('go-files') && accepted('go-http'),
  }));
  return {
    schemaVersion: 1,
    mutationChecks,
    pathRejectionChecks: pathChecks,
    openapiClientCoverage: {
      routes: sourceSafety?.openapiRoutes || [],
      openapiDriftAccepted: accepted('p05-safety-preflight'),
      httpContractAccepted: accepted('go-http'),
      rendererDualTransportAccepted: accepted('renderer-transport'),
    },
    databaseHashes: mutationGolden ? {
      nodeBeforeSha256: mutationGolden.nodeDatabaseBeforeSha256 || null,
      nodeAfterSha256: mutationGolden.nodeDatabaseAfterSha256 || null,
      goBeforeSha256: mutationGolden.goDatabaseBeforeSha256 || null,
      goAfterSha256: mutationGolden.goDatabaseAfterSha256 || null,
      databaseContentCaptured: false,
    } : null,
    goldenDiff: {
      accepted: accepted('mutation-golden-compare') && mutationGolden?.ok === true,
      comparator: 'exact-deep-comparison',
      ignoredUnknownFields: false,
    },
  };
}

async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const sourceFiles = options.sourceFiles ?? SOURCE_FILES;
  const expectations = JSON.parse(fs.readFileSync(path.join(root, options.expectations || EXPECTATIONS), 'utf8'));
  const runDir = path.join(root, options.evidenceRoot || EVIDENCE_ROOT, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  const safe = safeEnvironment(temporaryRoot, options.environment || process.env);
  const getGitStatus = options.gitStatus || gitStatus;
  const startStatus = getGitStatus(root);
  const summary = {
    schemaVersion: 1,
    runId: path.basename(runDir),
    startedAt: new Date().toISOString(),
    status: 'running',
    environment: {
      platform: process.platform, arch: process.arch, node: process.version,
      temporaryRoot: '<system-temp>/' + path.basename(temporaryRoot),
      removedSensitiveEnvironmentVariableCount: safe.removedCount,
      environmentValuesCaptured: false,
      electronUserDataAccessed: false,
      productionDatabaseOpened: false,
      databaseContentCaptured: false,
      simultaneousNodeGoWriter: false,
    },
    databaseOwnership: {
      p04OwnerGateAccepted: false,
      authorizedCopiesOnly: false,
      goWriteRequiresOwnerProof: false,
      ownerGuardSha256: null,
      nodeClosedBeforeGo: false,
    },
    sourceHashesStart: sourceFiles.map((file) => fileRecord(root, file)),
    commandResults: [],
    testControlViolations: testControlViolations(root, sourceFiles),
  };
  summary.sourceFilesComplete = summary.sourceHashesStart.every((record) => !record.missing);
  const state = {
    root, runDir, temporaryRoot, environment: safe.environment,
    results: summary.commandResults, executeCommand: options.executeCommand,
  };
  const p04Gate = npmSpec('p04-gate', ['run', 'migration:p04:verify'], {
    description: 'Real P04 schema, checksum, owner lock, restore, and evidence hard gate.',
    outcome: 'success', allowedFailureSignatures: [],
  });
  const gateResult = await runCommand(p04Gate, state);
  if (!gateResult.evaluation.accepted) {
    summary.status = 'blocked';
    summary.blocked = 'p04_gate_failed_no_p05_writer_started';
  } else {
    try {
      summary.prerequisiteEvidence = (options.verifyStageEvidence || verifyStageEvidence)(root, 'p04');
      summary.sourceSafety = (options.inspectSourceSafety || inspectSourceSafety)(root);
      if (!summary.sourceFilesComplete) throw new Error('guarded_source_missing');
      if (summary.testControlViolations.length) throw new Error('forbidden_test_control');
      summary.databaseOwnership.p04OwnerGateAccepted = true;
      summary.databaseOwnership.authorizedCopiesOnly = true;
      summary.databaseOwnership.goWriteRequiresOwnerProof = true;
      summary.databaseOwnership.ownerGuardSha256 = summary.sourceSafety.databaseOwnerGuardSha256;
    } catch (error) {
      summary.status = 'blocked';
      summary.blocked = sanitizeLog(error.message, root, temporaryRoot);
    }
  }
  if (summary.status !== 'blocked') {
    for (const command of p05Commands(expectations)) {
      const result = await runCommand(command, state);
      if (!result.evaluation.accepted) {
        summary.status = 'failed';
        summary.failure = command.id + '_failed_stopped_remaining_steps';
        break;
      }
    }
    if (summary.status === 'running') summary.status = 'completed';
  }
  summary.sourceHashesEnd = sourceFiles.map((file) => fileRecord(root, file));
  summary.sourceHashesStable = JSON.stringify(summary.sourceHashesStart) === JSON.stringify(summary.sourceHashesEnd);
  summary.temporaryCleanup = cleanupTemporaryRoot(temporaryRoot);
  const endStatus = getGitStatus(root);
  summary.gitStatusStart = { ...startStatus, stderr: sanitizeLog(startStatus.stderr, root, temporaryRoot) };
  summary.gitStatusEnd = { ...endStatus, stderr: sanitizeLog(endStatus.stderr, root, temporaryRoot) };
  summary.affectedFiles = [...new Set([...statusPaths(startStatus), ...statusPaths(endStatus)])].sort();
  summary.endedAt = new Date().toISOString();
  summary.mutationGolden = summary.commandResults.find((item) => item.id === 'mutation-golden-compare')?.structuredOutput || null;
  summary.coverage = coverageEvidence(summary.commandResults, summary.sourceSafety, summary.mutationGolden);
  summary.remainingRisks = summary.commandResults.filter((item) => !item.evaluation.accepted)
    .map((item) => item.id + ': ' + item.evaluation.reason);
  if (!summary.sourceFilesComplete) summary.remainingRisks.push('guarded_source_missing');
  if (!summary.sourceHashesStable) summary.remainingRisks.push('guarded_source_hash_drift');
  if (summary.testControlViolations.length) summary.remainingRisks.push('forbidden_test_control');
  if (!summary.temporaryCleanup.cleaned) summary.remainingRisks.push('temporary_cleanup_failed');
  if (summary.status === 'completed') {
    try {
      summary.safety = (options.inspectEvidenceSummary || inspectEvidenceSummary)(summary);
      summary.databaseOwnership.nodeClosedBeforeGo = summary.safety.writerTimeline.nodeClosedBeforeGo;
      summary.environment.simultaneousNodeGoWriter = summary.safety.writerTimeline.simultaneousNodeGoWriter;
    } catch (error) {
      summary.safety = { ok: false, code: error.code || 'evidence_safety_failed' };
      summary.remainingRisks.push(summary.safety.code);
    }
  }
  summary.remainingRisks.push('P15 retains disposition of frozen baseline failures.');
  summary.ok = summary.status === 'completed' &&
    startStatus.exitCode === 0 && endStatus.exitCode === 0 &&
    summary.sourceFilesComplete && summary.sourceHashesStable &&
    summary.testControlViolations.length === 0 && summary.temporaryCleanup.cleaned &&
    summary.databaseOwnership.p04OwnerGateAccepted && summary.databaseOwnership.authorizedCopiesOnly &&
    summary.databaseOwnership.goWriteRequiresOwnerProof &&
    /^[a-f0-9]{64}$/.test(summary.databaseOwnership.ownerGuardSha256 || '') &&
    summary.commandResults.every((item) => item.evaluation.accepted) && summary.safety?.ok === true &&
    summary.mutationGolden?.ok === true &&
    summary.coverage.mutationChecks.every((item) => item.verified) &&
    summary.coverage.pathRejectionChecks.every((item) => item.verified) &&
    summary.coverage.openapiClientCoverage.routes.length >= 4 &&
    summary.coverage.openapiClientCoverage.openapiDriftAccepted &&
    summary.coverage.openapiClientCoverage.httpContractAccepted &&
    summary.coverage.openapiClientCoverage.rendererDualTransportAccepted &&
    summary.coverage.goldenDiff.accepted;
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = fs.readdirSync(runDir).sort().map((name) => fileRecord(runDir, name));
  writeJson(path.join(runDir, 'evidence-manifest.json'), {
    schemaVersion: 1, runId: summary.runId, generatedAt: summary.endedAt,
    immutableRunDirectory: true, artifacts,
  });
  return { runDir, summary };
}

function parseArgs(argv) {
  if (argv.length !== 1 || argv[0] !== 'verify') {
    throw new Error('usage: node scripts/migration-p05/verify.js verify');
  }
  return { mode: 'verify' };
}

if (require.main === module) {
  try {
    parseArgs(process.argv.slice(2));
    runVerification().then(({ runDir, summary }) => {
      for (const item of summary.commandResults) {
        process.stdout.write(item.id + ': exit=' + (item.exitCode ?? 'missing') +
          ' accepted=' + item.evaluation.accepted + '\n');
      }
      if (summary.status === 'blocked') process.stderr.write('blocked: ' + summary.blocked + '\n');
      process.stdout.write('evidence: ' + toPosix(path.relative(ROOT, runDir)) + '\n');
      process.exitCode = summary.ok ? 0 : 1;
    }).catch((error) => {
      process.stderr.write(sanitizeLog(error.message, ROOT, os.tmpdir()) + '\n');
      process.exitCode = 1;
    });
  } catch (error) {
    process.stderr.write(error.message + '\n');
    process.exitCode = 1;
  }
}

module.exports = {
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
};
