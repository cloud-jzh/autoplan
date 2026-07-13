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
  inspectP07Evidence,
  inspectSourceSafety,
  isOwnedTemporaryRoot,
  scanSensitiveText,
  toPosix,
} = require('./check-safety');

const ROOT = path.resolve(__dirname, '../..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p08/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p08-verify-';
const SENSITIVE_ENV = /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie|userdata|database|db[_-]?path)/i;
const CONTROL_ENV = /^AUTOPLAN_P0[3-8]_/i;
const USER_STORAGE_ENV = /^(?:HOME|USERPROFILE|APPDATA|LOCALAPPDATA|XDG_CONFIG_HOME|XDG_CACHE_HOME|XDG_DATA_HOME|GOCACHE|GOMODCACHE)$/i;
const SOURCE_FILES = [
  'package.json',
  'scripts/migration-p08/inventory-static-contract.js',
  'scripts/migration-p08/generate-node-golden.js',
  'scripts/migration-p08/prepare-secret-copy.js',
  'scripts/migration-p08/scan-sensitive-output.js',
  'scripts/migration-p08/compare-static-golden.js',
  'scripts/migration-p08/compare-static-golden.test.js',
  'scripts/migration-p08/check-safety.js',
  'scripts/migration-p08/check-safety.test.js',
  'scripts/migration-p08/verify.js',
  'scripts/migration-p08/verify.test.js',
  'fixtures/migration/p08/manifest.json',
  'fixtures/migration/p08/node-static.golden.json',
  'fixtures/migration/p08/pagination-cases.json',
  'fixtures/migration/p08/expected-errors.json',
  'docs/migration/p08/static-contract.json',
  'docs/migration/p08/README.md',
  'docs/migration/p08/runbook.md',
  'docs/migration/p08/evidence/README.md',
  'backend/internal/repository/sqlite/automation_test.go',
  'backend/internal/repository/sqlite/chat_config_test.go',
  'backend/internal/application/automation/golden_test.go',
  'backend/internal/application/chat/golden_test.go',
  'backend/internal/application/secrets/secrets_test.go',
  'backend/internal/platform/secrets/provider_test.go',
  'backend/internal/httpapi/static_contract_test.go',
  'backend/internal/mcp/static_tools_test.go',
  'src/renderer/lib/api/httpClient.test.js',
  'src/renderer/lib/api/staticTransport.contract.test.js',
];

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function writeJson(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', flag: 'wx' });
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
    [temporaryRoot, '<p08-temp>'], [root, '<repo>'], [os.homedir(), '<home>'], [os.tmpdir(), '<tmp>'],
  ]) {
    if (target) text = text.replaceAll(toPosix(target), replacement);
  }
  return text
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|tmp|var|private|mnt|opt)\/[^\s"'<>]*/g, '$1<absolute-path>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+/-]+/gi, '$1<redacted>')
    .replace(/\b(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth|session|cookie)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/(?:file|autoplan-file):\/\/[^\s"']+/gi, '<controlled-url>');
}

function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (SENSITIVE_ENV.test(name) || CONTROL_ENV.test(name) || USER_STORAGE_ENV.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  const home = path.join(temporaryRoot, 'home');
  Object.assign(environment, {
    TEMP: temporaryRoot,
    TMP: temporaryRoot,
    TMPDIR: temporaryRoot,
    GOTMPDIR: temporaryRoot,
    HOME: home,
    USERPROFILE: home,
    APPDATA: path.join(temporaryRoot, 'appdata'),
    LOCALAPPDATA: path.join(temporaryRoot, 'localappdata'),
    XDG_CONFIG_HOME: path.join(temporaryRoot, 'xdg-config'),
    XDG_CACHE_HOME: path.join(temporaryRoot, 'xdg-cache'),
    XDG_DATA_HOME: path.join(temporaryRoot, 'xdg-data'),
    GOCACHE: path.join(temporaryRoot, 'go-cache'),
    GOMODCACHE: path.join(temporaryRoot, 'go-mod-cache'),
    AUTOPLAN_P08_VERIFY: '1',
    AUTOPLAN_P08_TEMPORARY_ROOT: temporaryRoot,
    AUTOPLAN_P08_DATABASE_ROOT: path.join(temporaryRoot, 'database'),
    AUTOPLAN_P08_NODE_GOLDEN_ROOT: path.join(temporaryRoot, 'node-golden'),
    AUTOPLAN_P08_GO_GOLDEN_ROOT: path.join(temporaryRoot, 'go-golden'),
    AUTOPLAN_P08_SECRET_STORE_ROOT: path.join(temporaryRoot, 'secret-store'),
    AUTOPLAN_P08_SECRET_KEY_ROOT: path.join(temporaryRoot, 'secret-key'),
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
      cwd: path.join(root, spec.cwd || '.'), env: environment, shell: false, windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
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
  return { id, executable, args, description, cwd, command: [(executable === process.execPath ? 'node' : executable), ...args].join(' '), outcome: 'success', allowedFailureSignatures: [] };
}

function npmSpec(id, args, expectation) {
  const command = 'npm.cmd ' + args.join(' ');
  if (process.platform === 'win32') {
    return {
      id, ...expectation, executable: process.env.ComSpec || 'cmd.exe', args: ['/d', '/s', '/c', 'call ' + command],
      windowsVerbatimArguments: true, command, cwd: '.',
    };
  }
  return { id, ...expectation, executable: 'npm', args, command: 'npm ' + args.join(' '), cwd: '.' };
}

function p08PreflightCommand() {
  return successSpec('p08-safety-preflight', process.execPath, ['scripts/migration-p08/check-safety.js', 'preflight'],
    'P08 frozen contract, P07 evidence, P00 signatures, schema, owner, copy and secret-isolation hard gate.');
}

function nodeGoldenSpec() {
  const program = [
    "const fs=require('node:fs');",
    "const {buildGoldenBundle}=require('./scripts/migration-p08/generate-node-golden');",
    "(async()=>{const bundle=await buildGoldenBundle();const committed=fs.readFileSync('fixtures/migration/p08/node-static.golden.json','utf8');",
    "if(bundle.artifacts['node-static.golden.json']!==committed)throw new Error('node_static_golden_drift');",
    "process.stdout.write(JSON.stringify({schema_version:1,status:'ok',code:'node_static_golden_checked'})+'\\n');})().catch((error)=>{process.stderr.write((error&&error.message)||'node_static_golden_failed');process.exitCode=1;});",
  ].join('');
  return successSpec('node-static-golden', process.execPath, ['-e', program],
    'Build the Node static golden on a generator-owned system-temporary database and compare it without writing fixtures.');
}

function p00GateCommands(expectations) {
  return [
    npmSpec('check', ['run', 'check'], expectations.commands.check),
    npmSpec('test', ['test'], expectations.commands.test),
  ];
}

function p08Commands(expectations) {
  void expectations;
  return [
    nodeGoldenSpec(),
    successSpec('go-static-repository', 'go', ['test', './internal/repository/sqlite', '-count=1'],
      'Scripts, Executors, Conversations, ChatMessages, config relations, transactional rollback and project scoping.', 'backend'),
    successSpec('go-static-automation', 'go', ['test', './internal/application/automation', '-count=1'],
      'Automation DTO redaction, golden metadata and disabled runtime capabilities.', 'backend'),
    successSpec('go-static-chat-config', 'go', ['test', './internal/application/chat', './internal/application/config', '-count=1'],
      'Conversation metadata, static config defaults, MCP configuration and disabled chat/MCP runtime capabilities.', 'backend'),
    successSpec('go-static-secrets', 'go', ['test', './internal/application/secrets', './internal/platform/secrets/...', '-count=1'],
      'Secret reference lifecycle, provider failure compensation, OS-keyring preference and encrypted fallback isolation.', 'backend'),
    successSpec('go-static-httpapi', 'go', ['test', './internal/httpapi', '-count=1'],
      'Static REST/OpenAPI response, pagination, stable errors and non-runtime capability boundaries.', 'backend'),
    successSpec('go-static-mcp', 'go', ['test', './internal/mcp', '-count=1'],
      'MCP static adapter shares application services and exposes no listener or runtime action.', 'backend'),
    successSpec('go-secret-migration', 'go', ['test', './internal/migration/secrets', './cmd/autoplan-migrate-secrets', '-count=1'],
      'Explicit-copy secret migration, restore drill, backup retention and no-production-path enforcement.', 'backend'),
    successSpec('renderer-static-transport', process.execPath, ['--test',
      'src/renderer/lib/api/httpClient.test.js', 'src/renderer/lib/api/staticTransport.contract.test.js',
    ], 'Renderer HTTP static DTO validation retains IPC runtime ownership and does not replay failed mutations.'),
    successSpec('p08-golden-and-safety-tests', process.execPath, ['--test',
      'scripts/migration-p08/compare-static-golden.test.js', 'scripts/migration-p08/check-safety.test.js', 'scripts/migration-p08/verify.test.js',
    ], 'P08 strict golden comparison, blocked gates, evidence sanitization and temporary-root cleanup contracts.'),
  ];
}

function parseStructuredOutput(stdout) {
  const lines = String(stdout || '').split(/\r?\n/).filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    try {
      const value = JSON.parse(lines[index]);
      if (value && typeof value === 'object') return value;
    } catch {
      // Diagnostics may precede a final structured result.
    }
  }
  return null;
}

async function runCommand(spec, state) {
  const actual = await (state.executeCommand || execute)(spec, state.root, state.environment);
  actual.failureSignatures = actual.exitCode && actual.exitCode !== 0
    ? failureSignatures(actual.stdout, actual.stderr, state.root) : [];
  if (actual.exitCode && actual.exitCode !== 0 && actual.failureSignatures.length === 0) {
    actual.failureSignatures = ['unclassified|sha256=' + sha256(sanitizeForSignature(`${actual.stdout || ''}\n${actual.stderr || ''}`, state.root))];
  }
  const sensitive = scanSensitiveText(`${actual.stdout || ''}\n${actual.stderr || ''}`);
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
    id: spec.id, description: spec.description, command: sanitizeLog(spec.command, state.root, state.temporaryRoot),
    expectedOutcome: spec.outcome, exitCode: actual.exitCode, signal: actual.signal || null, startedAt: actual.startedAt, endedAt: actual.endedAt,
    failureSignatures: actual.failureSignatures.map((item) => sanitizeLog(item, state.root, state.temporaryRoot)),
    secretFindings: sensitive, evaluation: { ...evaluation, reason: sanitizeLog(evaluation.reason, state.root, state.temporaryRoot) },
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
  const result = spawnSync('git', ['status', '--porcelain=v1', '--untracked-files=all'], { cwd: root, encoding: 'utf8', windowsHide: true });
  return { exitCode: typeof result.status === 'number' ? result.status : null, entries: String(result.stdout || '').split(/\r?\n/).filter(Boolean), stderr: String(result.stderr || '') };
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

function cleanupTemporaryRoot(temporaryRoot) {
  if (!isOwnedTemporaryRoot(temporaryRoot)) return { cleaned: false, error: 'refused_non_owned_cleanup' };
  try {
    fs.rmSync(path.resolve(temporaryRoot), { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch {
    return { cleaned: false, error: 'owned_temporary_cleanup_failed' };
  }
}

function coverageEvidence(commandResults, sourceSafety) {
  const accepted = (id) => commandResults.find((item) => item.id === id)?.evaluation.accepted === true;
  const matrix = [
    ['node-go-static-golden', ['node-static-golden', 'go-static-repository', 'p08-safety-preflight']],
    ['pagination-relations-and-project-scope', ['go-static-repository', 'go-static-chat-config', 'go-static-httpapi']],
    ['transaction-concurrency-and-rollback', ['go-static-repository', 'go-static-secrets', 'go-static-httpapi']],
    ['secret-copy-migration-and-restore', ['go-static-secrets', 'go-secret-migration', 'p08-safety-preflight']],
    ['http-mcp-renderer-static-boundary', ['go-static-httpapi', 'go-static-mcp', 'renderer-static-transport']],
    ['runtime-capability-closure', ['go-static-automation', 'go-static-chat-config', 'go-static-httpapi', 'go-static-mcp']],
    ['evidence-and-sensitive-output', ['p08-safety-preflight', 'p08-golden-and-safety-tests']],
  ].map(([scenario, commands]) => ({ scenario, commands, verified: commands.every(accepted) }));
  return {
    schemaVersion: 1, matrix,
    hashes: sourceSafety ? {
      fixtureManifestSha256: sourceSafety.manifestSha256, goldenSha256: sourceSafety.goldenSha256,
      schemaSha256: sourceSafety.schemaSha256, paginationSha256: sourceSafety.paginationSha256,
      expectedErrorsSha256: sourceSafety.expectedErrorsSha256,
    } : null,
    runtime: { externalProcessStarted: false, chatStreamStarted: false, mcpListenerStarted: false, disabledActionsReturnedNonSuccess: true },
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
  for (const name of [
    'database', 'node-golden', 'go-golden', 'secret-store', 'secret-key', 'immutable-backups', 'sentinels',
    'home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data', 'go-cache', 'go-mod-cache',
  ]) {
    fs.mkdirSync(path.join(temporaryRoot, name), { mode: 0o700 });
  }
  const safe = safeEnvironment(temporaryRoot, options.environment || process.env);
  const getGitStatus = options.gitStatus || gitStatus;
  const startStatus = getGitStatus(root);
  const summary = {
    schemaVersion: 1, runId: path.basename(runDir), startedAt: new Date().toISOString(), status: 'running',
    environment: {
      platform: process.platform, arch: process.arch, node: process.version,
      temporaryRoot: '<system-temp>/' + path.basename(temporaryRoot), temporaryRootsOnly: true,
      temporaryDatabaseRoot: '<p08-temp>/database', temporaryNodeGoldenRoot: '<p08-temp>/node-golden', temporaryGoGoldenRoot: '<p08-temp>/go-golden',
      temporarySecretStoreRoot: '<p08-temp>/secret-store', temporarySecretKeyRoot: '<p08-temp>/secret-key',
      temporaryHomeRoot: '<p08-temp>/home', temporaryGoCacheRoot: '<p08-temp>/go-cache',
      temporaryGoModuleCacheRoot: '<p08-temp>/go-mod-cache',
      removedSensitiveEnvironmentVariableCount: safe.removedCount, environmentValuesCaptured: false,
      electronUserDataAccessed: false, productionDatabaseOpened: false, databaseContentCaptured: false, userContentCaptured: false,
      externalProcessStarted: false, chatStreamStarted: false, mcpListenerStarted: false, simultaneousNodeGoWriter: false, unauthorizedWorkspaceTouched: false,
    },
    databaseOwnership: {
      p07GateAccepted: false, p07EvidenceAccepted: false, p00BaselineAccepted: false, authorizedCopiesOnly: false,
      goWriteRequiresOwnerProof: false, secretStorageSeparateFromDatabase: false, ownerGuardSha256: null, nodeClosedBeforeGo: false,
    },
    sourceHashesStart: sourceFiles.map((file) => fileRecord(root, file)), commandResults: [], testControlViolations: testControlViolations(root, sourceFiles),
  };
  summary.sourceFilesComplete = summary.sourceHashesStart.every((record) => !record.missing);
  const state = { root, runDir, temporaryRoot, environment: safe.environment, results: summary.commandResults, executeCommand: options.executeCommand };
  const p07Gate = npmSpec('p07-gate', ['run', 'migration:p07:verify'], {
    description: 'Real P07 completion evidence, P00 frozen signature, schema/checksum and owner-lock hard gate.', outcome: 'success', allowedFailureSignatures: [],
  });
  const p07 = await runCommand(p07Gate, state);
  if (!p07.evaluation.accepted) {
    summary.status = 'blocked';
    summary.blocked = 'p07_gate_failed_no_p08_node_or_go_writer_started';
  }
  if (summary.status !== 'blocked') {
    const preflight = await runCommand(p08PreflightCommand(), state);
    if (!preflight.evaluation.accepted) {
      summary.status = 'blocked';
      summary.blocked = 'p08_safety_preflight_failed_no_p08_node_or_go_writer_started';
    }
  }
  if (summary.status !== 'blocked') {
    for (const command of p00GateCommands(expectations)) {
      const result = await runCommand(command, state);
      if (!result.evaluation.accepted) {
        summary.status = 'blocked';
        summary.blocked = 'p00_baseline_gate_failed_no_p08_node_or_go_writer_started';
        break;
      }
    }
  }
  if (summary.status !== 'blocked') {
    try {
      summary.p07Evidence = (options.inspectP07Evidence || inspectP07Evidence)(root);
      summary.sourceSafety = (options.inspectSourceSafety || inspectSourceSafety)(root);
      if (!summary.sourceFilesComplete) throw new Error('guarded_source_missing');
      if (summary.testControlViolations.length) throw new Error('forbidden_test_control');
      summary.databaseOwnership.p07GateAccepted = true;
      summary.databaseOwnership.p07EvidenceAccepted = true;
      summary.databaseOwnership.p00BaselineAccepted = true;
      summary.databaseOwnership.authorizedCopiesOnly = true;
      summary.databaseOwnership.goWriteRequiresOwnerProof = true;
      summary.databaseOwnership.secretStorageSeparateFromDatabase = true;
      summary.databaseOwnership.ownerGuardSha256 = summary.sourceSafety.databaseOwnerGuardSha256;
    } catch (error) {
      summary.status = 'blocked';
      summary.blocked = sanitizeLog(error.message, root, temporaryRoot);
    }
  }
  if (summary.status !== 'blocked') {
    for (const command of p08Commands(expectations)) {
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
  summary.coverage = coverageEvidence(summary.commandResults, summary.sourceSafety);
  summary.remainingRisks = summary.commandResults.filter((item) => !item.evaluation.accepted).map((item) => item.id + ': ' + item.evaluation.reason);
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
    summary.sourceFilesComplete && summary.sourceHashesStable && summary.testControlViolations.length === 0 && summary.temporaryCleanup.cleaned &&
    summary.databaseOwnership.p07GateAccepted && summary.databaseOwnership.p07EvidenceAccepted && summary.databaseOwnership.p00BaselineAccepted &&
    summary.databaseOwnership.authorizedCopiesOnly && summary.databaseOwnership.goWriteRequiresOwnerProof && summary.databaseOwnership.secretStorageSeparateFromDatabase &&
    /^[a-f0-9]{64}$/.test(summary.databaseOwnership.ownerGuardSha256 || '') && summary.commandResults.every((item) => item.evaluation.accepted) &&
    summary.safety?.ok === true && summary.coverage.matrix.every((item) => item.verified) && summary.coverage.runtime.disabledActionsReturnedNonSuccess;
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = fs.readdirSync(runDir).filter((name) => name !== 'evidence-manifest.json').sort().map((name) => {
    const target = path.join(runDir, name);
    return { path: name, bytes: fs.statSync(target).size, sha256: sha256(fs.readFileSync(target)) };
  });
  writeJson(path.join(runDir, 'evidence-manifest.json'), {
    schemaVersion: 1, runId: summary.runId, generatedAt: summary.endedAt, immutableRunDirectory: true, artifacts,
  });
  return { runDir, summary };
}

function parseArgs(argv) {
  if (argv.length !== 1 || argv[0] !== 'verify') throw new Error('usage: node scripts/migration-p08/verify.js verify');
  return { mode: 'verify' };
}

if (require.main === module) {
  try {
    parseArgs(process.argv.slice(2));
    runVerification().then(({ runDir, summary }) => {
      for (const item of summary.commandResults) process.stdout.write(`${item.id}: exit=${item.exitCode ?? 'blocked'} accepted=${item.evaluation.accepted}\n`);
      process.stdout.write(`evidence: ${toPosix(path.relative(ROOT, runDir))}\n`);
      process.exitCode = summary.ok ? 0 : 1;
    }).catch((error) => {
      process.stderr.write(`${error.stack || error.message}\n`);
      process.exitCode = 1;
    });
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  SOURCE_FILES,
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
};
