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
  inspectP05Evidence,
  inspectSourceSafety,
  isOwnedTemporaryRoot,
  scanSensitiveText,
  toPosix,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p06/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p06-verify-';
const SENSITIVE_ENV = /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie|userdata|database|db[_-]?path)/i;
const CONTROL_ENV = /^AUTOPLAN_P0[356]_/i;
const SOURCE_FILES = [
  'package.json',
  'src/intakeService.js',
  'src/attachments.js',
  'src/main.js',
  'src/mcpTools.js',
  'src/mcpServer.js',
  'src/preload.js',
  'src/loop/intakeAttachments.js',
  'src/loop/intakeDeletion.js',
  'src/loop/intakePlanLinks.js',
  'src/loop/snapshots.js',
  'scripts/migration-p06/inventory-intake-contract.js',
  'scripts/migration-p06/generate-node-golden.js',
  'scripts/migration-p06/compare-intake-golden.js',
  'scripts/migration-p06/compare-intake-golden.test.js',
  'scripts/migration-p06/check-safety.js',
  'scripts/migration-p06/check-safety.test.js',
  'scripts/migration-p06/verify.js',
  'scripts/migration-p06/verify.test.js',
  'fixtures/migration/p06/manifest.json',
  'fixtures/migration/p06/node-intake.golden.json',
  'fixtures/migration/p06/expected-errors.json',
  'docs/migration/p06/intake-contract.json',
  'docs/migration/p06/README.md',
  'docs/migration/p06/runbook.md',
  'docs/migration/p06/attachment-recovery.md',
  'docs/migration/p06/evidence/README.md',
  'backend/migrations/0001_schema_v1.sql',
  'backend/internal/domain/intake/model.go',
  'backend/internal/domain/files/attachment.go',
  'backend/internal/repository/repository.go',
  'backend/internal/repository/sqlite/transaction.go',
  'backend/internal/repository/sqlite/intake.go',
  'backend/internal/repository/sqlite/intake_plan_links.go',
  'backend/internal/repository/sqlite/intake_transactions.go',
  'backend/internal/repository/sqlite/attachments.go',
  'backend/internal/repository/sqlite/attachment_operations.go',
  'backend/internal/repository/sqlite/intake_contract_test.go',
  'backend/internal/application/intake/service.go',
  'backend/internal/application/intake/commands.go',
  'backend/internal/application/intake/queries.go',
  'backend/internal/application/intake/links.go',
  'backend/internal/application/intake/deletion.go',
  'backend/internal/application/intake/dto.go',
  'backend/internal/application/intake/golden_test.go',
  'backend/internal/application/snapshot/assembler.go',
  'backend/internal/application/attachments/service.go',
  'backend/internal/application/attachments/recovery.go',
  'backend/internal/application/attachments/audit.go',
  'backend/internal/application/attachments/fault_test.go',
  'backend/internal/application/attachments/recovery_test.go',
  'backend/internal/httpapi/router.go',
  'backend/internal/httpapi/intake.go',
  'backend/internal/httpapi/attachments.go',
  'backend/internal/httpapi/multipart.go',
  'backend/internal/httpapi/errors.go',
  'backend/internal/httpapi/intake_contract_test.go',
  'backend/internal/httpapi/attachments_contract_test.go',
  'backend/internal/platform/filesystem/attachment_store.go',
  'backend/internal/platform/filesystem/attachment_store_test.go',
  'backend/internal/mcp/intake_tools.go',
  'backend/internal/mcp/attachment_inputs.go',
  'backend/internal/mcp/intake_tools_test.go',
  'backend/internal/bootstrap/dependencies.go',
  'backend/openapi/openapi.yaml',
  'backend/openapi/schemas/intake.schema.json',
  'backend/openapi/schemas/attachment.schema.json',
  'src/renderer/lib/api/client.ts',
  'src/renderer/lib/api/httpClient.ts',
  'src/renderer/lib/api/transport.ts',
  'src/renderer/lib/api/httpClient.test.js',
  'src/renderer/lib/api/intakeTransport.contract.test.js',
];

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function writeJson(file, value) {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n', { encoding: 'utf8', flag: 'wx' });
}

function fileRecord(root, relative) {
  const target = path.join(root, relative);
  if (!fs.existsSync(target) || !fs.statSync(target).isFile()) return { path: toPosix(relative), missing: true };
  const bytes = fs.readFileSync(target);
  return { path: toPosix(relative), bytes: bytes.length, sha256: sha256(bytes) };
}

function sanitizeLog(value, root, temporaryRoot) {
  let text = toPosix(value || '');
  for (const [target, replacement] of [
    [temporaryRoot, '<p06-temp>'], [root, '<repo>'], [os.homedir(), '<home>'], [os.tmpdir(), '<tmp>'],
  ]) {
    if (target) text = text.replaceAll(toPosix(target), replacement);
  }
  return text
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|tmp|var|private|mnt|opt)\/[^\s"'<>]*/g, '$1<absolute-path>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth|session|cookie)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/(?:file|autoplan-file):\/\/[^\s"']+/gi, '<controlled-url>');
}

function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (SENSITIVE_ENV.test(name) || CONTROL_ENV.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  Object.assign(environment, {
    TEMP: temporaryRoot,
    TMP: temporaryRoot,
    TMPDIR: temporaryRoot,
    GOTMPDIR: temporaryRoot,
    AUTOPLAN_P06_VERIFY: '1',
    AUTOPLAN_P06_TEMPORARY_ROOT: temporaryRoot,
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
      // Diagnostics may precede the final structured result.
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
  const sensitive = scanSensitiveText(String(actual.stdout || '') + '\n' + String(actual.stderr || ''));
  const evaluation = evaluateCommand(spec, actual);
  if (sensitive.length) {
    evaluation.accepted = false;
    evaluation.reason = 'credential-shaped or unsafe-path output detected: ' + sensitive.join(', ');
  }
  const prefix = String(state.results.length + 1).padStart(2, '0') + '-' + spec.id;
  const stdout = sanitizeLog(actual.stdout, state.root, state.temporaryRoot);
  const stderr = sanitizeLog(actual.stderr, state.root, state.temporaryRoot);
  const stdoutLog = prefix + '.stdout.log';
  const stderrLog = prefix + '.stderr.log';
  fs.writeFileSync(path.join(state.runDir, stdoutLog), stdout, 'utf8');
  fs.writeFileSync(path.join(state.runDir, stderrLog), stderr, 'utf8');
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
    secretFindings: sensitive,
    evaluation: { ...evaluation, reason: sanitizeLog(evaluation.reason, state.root, state.temporaryRoot) },
    structuredOutput: parseStructuredOutput(stdout),
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

function testControlViolations(root, sourceFiles = SOURCE_FILES) {
  const violations = [];
  for (const relative of sourceFiles.filter((file) => /(?:_test\.go|\.test\.(?:js|ts|tsx))$/.test(file))) {
    const target = path.join(root, relative);
    if (!fs.existsSync(target)) continue;
    fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
      if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line) || /\bt\.Skip(?:f|Now)?\s*\(/.test(line)) {
        violations.push(toPosix(relative) + ':' + (index + 1));
      }
    });
  }
  return violations.sort();
}

function p06Commands(expectations) {
  return [
    successSpec('p06-safety-preflight', process.execPath,
      ['scripts/migration-p06/check-safety.js', 'preflight'],
      'Frozen P06 inputs, OpenAPI, Files policy, owner guard, renderer and fixture secrecy scan.'),
    successSpec('node-golden-generator', process.execPath,
      ['scripts/migration-p06/generate-node-golden.js'],
      'Run Node/sql.js intake scenarios only on its generator-owned temporary database and close it before Go.'),
    successSpec('go-repository', 'go', ['test', './internal/repository/sqlite', '-count=1'],
      'Transactional Intake, duplicate, association, link, delete, ownership and concurrent-writer contracts.', 'backend'),
    successSpec('go-intake-application', 'go', ['test', './internal/application/intake', '-count=1'],
      'Shared Intake state, idempotency, snapshot and golden compatibility contracts.', 'backend'),
    successSpec('go-attachments', 'go', ['test', './internal/application/attachments', '-count=1'],
      'Attachment limits, faults, crash recovery, audit and explicit repair contracts.', 'backend'),
    successSpec('go-http', 'go', ['test', './internal/httpapi', '-count=1'],
      'REST CRUD, multipart, authorization, controlled content and stable errors.', 'backend'),
    successSpec('go-mcp', 'go', ['test', './internal/mcp', '-count=1'],
      'MCP shared-service, authorized path input and redacted response contracts.', 'backend'),
    successSpec('go-files', 'go', ['test', './internal/platform/filesystem', '-count=1'],
      'Attachment-root staging, fsync, rename, quarantine and reparse-point policy contracts.', 'backend'),
    successSpec('renderer-intake-transport', process.execPath, ['--test',
      'src/renderer/lib/api/httpClient.test.js', 'src/renderer/lib/api/intakeTransport.contract.test.js',
    ], 'Renderer HTTP intake/attachment transport, idempotency and controlled URL contracts.'),
    successSpec('p06-golden-contract', process.execPath, ['--test',
      'scripts/migration-p06/compare-intake-golden.test.js',
    ], 'Strict Node/Go golden field-presence, type, nullability, order and error-map comparator contracts.'),
    successSpec('p06-orchestration-tests', process.execPath, ['--test',
      'scripts/migration-p06/verify.test.js', 'scripts/migration-p06/check-safety.test.js',
    ], 'P06 blocked-path, evidence, writer-handoff, sanitization and cleanup contracts.'),
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

function coverageEvidence(commandResults, sourceSafety, nodeGolden) {
  const accepted = (id) => commandResults.find((item) => item.id === id)?.evaluation.accepted === true;
  const matrix = [
    ['crud-status-and-nullability', ['go-repository', 'go-intake-application', 'go-http', 'renderer-intake-transport']],
    ['duplicate-and-concurrent-writers', ['go-repository', 'go-intake-application', 'go-http']],
    ['cross-project-owner-and-plan-links', ['go-repository', 'go-intake-application', 'go-http', 'go-mcp']],
    ['idempotency-and-delete-cascade', ['go-repository', 'go-intake-application', 'go-http', 'renderer-intake-transport']],
    ['attachment-input-download-and-range', ['go-attachments', 'go-http', 'renderer-intake-transport']],
    ['fault-crash-and-recovery', ['go-attachments', 'go-files']],
    ['audit-and-explicit-repair', ['go-attachments', 'go-files']],
    ['node-go-golden-contract', ['node-golden-generator', 'go-intake-application', 'p06-golden-contract']],
    ['mcp-and-ui-shared-service', ['go-mcp', 'go-http', 'renderer-intake-transport']],
  ].map(([scenario, commands]) => ({ scenario, commands, verified: commands.every(accepted) }));
  return {
    schemaVersion: 1,
    matrix,
    openapiCoverage: {
      routes: sourceSafety?.openapiRoutes || [],
      preflightAccepted: accepted('p06-safety-preflight'),
      httpAccepted: accepted('go-http'),
      rendererAccepted: accepted('renderer-intake-transport'),
    },
    fixtureHashes: sourceSafety ? {
      manifestSha256: sourceSafety.manifestSha256,
      goldenSha256: sourceSafety.goldenSha256,
      expectedErrorsSha256: sourceSafety.expectedErrorsSha256,
      intakeContractSha256: sourceSafety.intakeContractSha256,
      nodeDatabaseLogicalBeforeSha256: sourceSafety.nodeDatabaseLogicalBeforeSha256,
      nodeDatabaseLogicalAfterSha256: sourceSafety.nodeDatabaseLogicalAfterSha256,
      nodeAttachmentBytesSha256: sourceSafety.nodeAttachmentBytesSha256,
      attachmentStorageGuardSha256: sourceSafety.attachmentStorageGuardSha256,
      intakeSchemaSha256: sourceSafety.intakeSchemaSha256,
      attachmentSchemaSha256: sourceSafety.attachmentSchemaSha256,
      databaseContentCaptured: false,
      attachmentContentCaptured: false,
    } : null,
    nodeGolden: {
      accepted: accepted('node-golden-generator') && nodeGolden?.ok === true,
      artifacts: Array.isArray(nodeGolden?.artifacts) ? nodeGolden.artifacts : [],
      sameCopyConcurrentWritersAllowed: false,
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
  for (const name of ['database', 'attachments', 'go-golden']) fs.mkdirSync(path.join(temporaryRoot, name));
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
      temporaryRootsOnly: true,
      temporaryDatabaseRoot: '<p06-temp>/database',
      temporaryAttachmentRoot: '<p06-temp>/attachments',
      removedSensitiveEnvironmentVariableCount: safe.removedCount,
      environmentValuesCaptured: false,
      electronUserDataAccessed: false,
      productionDatabaseOpened: false,
      databaseContentCaptured: false,
      attachmentContentCaptured: false,
      simultaneousNodeGoWriter: false,
    },
    databaseOwnership: {
      p05GateAccepted: false,
      p05EvidenceAccepted: false,
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
  const p05Gate = npmSpec('p05-gate', ['run', 'migration:p05:verify'], {
    description: 'Real P05 completion, schema/checksum, Files policy, owner lock and evidence hard gate.',
    outcome: 'success', allowedFailureSignatures: [],
  });
  const gate = await runCommand(p05Gate, state);
  if (!gate.evaluation.accepted) {
    summary.status = 'blocked';
    summary.blocked = 'p05_gate_failed_no_p06_node_or_go_writer_started';
  } else {
    try {
      summary.p05Evidence = (options.inspectP05Evidence || inspectP05Evidence)(root);
      summary.sourceSafety = (options.inspectSourceSafety || inspectSourceSafety)(root);
      if (!summary.sourceFilesComplete) throw new Error('guarded_source_missing');
      if (summary.testControlViolations.length) throw new Error('forbidden_test_control');
      summary.databaseOwnership.p05GateAccepted = true;
      summary.databaseOwnership.p05EvidenceAccepted = true;
      summary.databaseOwnership.authorizedCopiesOnly = true;
      summary.databaseOwnership.goWriteRequiresOwnerProof = true;
      summary.databaseOwnership.ownerGuardSha256 = summary.sourceSafety.databaseOwnerGuardSha256;
    } catch (error) {
      summary.status = 'blocked';
      summary.blocked = sanitizeLog(error.message, root, temporaryRoot);
    }
  }
  if (summary.status !== 'blocked') {
    for (const command of p06Commands(expectations)) {
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
  summary.nodeGolden = summary.commandResults.find((item) => item.id === 'node-golden-generator')?.structuredOutput || null;
  summary.coverage = coverageEvidence(summary.commandResults, summary.sourceSafety, summary.nodeGolden);
  summary.remainingRisks = summary.commandResults.filter((item) => !item.evaluation.accepted)
    .map((item) => item.id + ': ' + item.evaluation.reason);
  if (!summary.sourceFilesComplete) summary.remainingRisks.push('guarded_source_missing');
  if (!summary.sourceHashesStable) summary.remainingRisks.push('guarded_source_hash_drift');
  if (summary.testControlViolations.length) summary.remainingRisks.push('forbidden_test_control');
  if (!summary.temporaryCleanup.cleaned) summary.remainingRisks.push('temporary_cleanup_failed');
  if (summary.status === 'completed') {
    try {
      summary.safety = (options.inspectEvidenceSummary || inspectEvidenceSummary)(summary, { temporaryRoot });
      summary.databaseOwnership.nodeClosedBeforeGo = summary.safety.writerTimeline.nodeClosedBeforeGo;
      summary.environment.simultaneousNodeGoWriter = summary.safety.writerTimeline.simultaneousNodeGoWriter;
    } catch (error) {
      summary.safety = { ok: false, code: error.code || 'evidence_safety_failed' };
      summary.remainingRisks.push(summary.safety.code);
    }
  }
  summary.remainingRisks.push('P15 retains disposition of frozen P00 baseline failures.');
  summary.ok = summary.status === 'completed' && startStatus.exitCode === 0 && endStatus.exitCode === 0 &&
    summary.sourceFilesComplete && summary.sourceHashesStable && summary.testControlViolations.length === 0 &&
    summary.temporaryCleanup.cleaned && summary.databaseOwnership.p05GateAccepted &&
    summary.databaseOwnership.p05EvidenceAccepted && summary.databaseOwnership.authorizedCopiesOnly &&
    summary.databaseOwnership.goWriteRequiresOwnerProof &&
    /^[a-f0-9]{64}$/.test(summary.databaseOwnership.ownerGuardSha256 || '') &&
    summary.commandResults.every((item) => item.evaluation.accepted) && summary.safety?.ok === true &&
    summary.nodeGolden?.ok === true && summary.coverage.matrix.every((item) => item.verified) &&
    summary.coverage.openapiCoverage.routes.length >= 8 && summary.coverage.openapiCoverage.preflightAccepted &&
    summary.coverage.openapiCoverage.httpAccepted && summary.coverage.openapiCoverage.rendererAccepted &&
    summary.coverage.nodeGolden.accepted;
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
    throw new Error('usage: node scripts/migration-p06/verify.js verify');
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
  SOURCE_FILES,
  cleanupTemporaryRoot,
  coverageEvidence,
  p06Commands,
  parseArgs,
  parseStructuredOutput,
  runVerification,
  safeEnvironment,
  sanitizeLog,
  testControlViolations,
};
