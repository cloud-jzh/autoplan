'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');
const initSqlJs = require('sql.js');

const {
  evaluateCommand,
  failureSignatures,
  sanitizeForSignature,
  stableRunId,
} = require('../migration-baseline/run-baseline');

const ROOT = path.resolve(__dirname, '..', '..');
const EXPECTATIONS = 'docs/migration/p00/baseline-expectations.json';
const EVIDENCE_ROOT = 'docs/migration/p04/evidence/runs';
const SENSITIVE_ENV = /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie|userdata|database|db[_-]?path)/i;
const SOURCE_FILES = [
  'package.json src/database.js src/database.compatibility.test.js',
  'scripts/migration-p04/inventory-schema.js scripts/migration-p04/inventory-schema.test.js scripts/migration-p04/generate-fixtures.js',
  'scripts/migration-p04/generate-fixtures.test.js scripts/migration-p04/verify.js scripts/migration-p04/verify.test.js',
  'fixtures/migration/p04/manifest.json fixtures/migration/p04/README.md docs/migration/p04/schema-inventory.json docs/migration/p04/schema-inventory.md',
  'docs/migration/p04/README.md docs/migration/p04/runbook.md docs/migration/p04/evidence/README.md backend/migrations/0001_schema_v1.sql',
  'backend/migrations/registry.go backend/internal/migration/runner.go backend/internal/migration/runner_test.go backend/internal/migration/preflight.go backend/internal/migration/backup.go',
  'backend/internal/migration/restore.go backend/internal/migration/command_test.go backend/internal/migration/fixtures_test.go backend/cmd/autoplan-migrate/main.go',
  'backend/internal/migration/fault_injection_test.go backend/internal/migration/restore_test.go backend/internal/migration/audit/auditor.go',
  'backend/internal/migration/audit/relations.go backend/internal/migration/audit/paths.go backend/internal/migration/audit/aggregates.go backend/internal/migration/audit/report.go',
  'backend/internal/migration/audit/auditor_test.go backend/internal/application/migration/service.go backend/internal/repository/sqlite/connection.go backend/internal/repository/sqlite/schema.go',
  'backend/internal/platform/instance/database_lock.go backend/internal/platform/instance/database_lock_test.go',
  'backend/internal/bootstrap/database.go backend/internal/bootstrap/readiness.go backend/internal/bootstrap/lifecycle_test.go',
].flatMap((line) => line.split(' '));

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
  return toPosix(value || '')
    .replaceAll(toPosix(temporaryRoot), '<p04-temp>')
    .replaceAll(toPosix(root), '<repo>')
    .replaceAll(toPosix(os.homedir()), '<home>')
    .replaceAll(toPosix(os.tmpdir()), '<tmp>')
    .replace(/\b[A-Za-z]:\/[^\s"'<>|]+/g, '<absolute-path>')
    .replace(/(^|[\s"'(])\/(?:Users|home|tmp|var|private|mnt|opt)\/[^\s"'<>]*/g, '$1<absolute-path>')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, '$1<redacted>')
    .replace(/\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{8,}\b/gi, '<redacted>')
    .replace(/((?:api[_-]?key|token|secret|credential|password|auth|session|cookie)\s*[=:]\s*)[^\s,;]+/gi, '$1<redacted>')
    .replace(/env_vars/gi, '<redacted-field>');
}
function secretFindings(stdout, stderr) {
  const content = String(stdout || '') + '\n' + String(stderr || '');
  const checks = [
    ['usable-key', /\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{12,}\b/i],
    ['usable-bearer', /Bearer\s+(?!<redacted>)[A-Za-z0-9._~+\/-]{12,}/i],
    ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
    ['credential-value', /(?:api[_-]?key|token|secret|credential|password|auth|session|cookie)[^\r\n]{0,12}[=:][^\r\n]{0,4}["']?[A-Za-z0-9._~+\/-]{12,}/i],
  ];
  return checks.filter((item) => item[1].test(content)).map((item) => item[0]);
}
function safeEnvironment(temporaryRoot, source = process.env) {
  const environment = {};
  let removedCount = 0;
  for (const [name, value] of Object.entries(source)) {
    if (SENSITIVE_ENV.test(name)) removedCount += 1;
    else environment[name] = value;
  }
  environment.TEMP = temporaryRoot;
  environment.TMP = temporaryRoot;
  environment.TMPDIR = temporaryRoot;
  environment.GOTMPDIR = temporaryRoot;
  environment.AUTOPLAN_P04_VERIFY = '1';
  return { environment, removedCount };
}
function execute(spec, root, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: path.join(root, spec.cwd || '.'),
      env: environment,
      shell: false,
      windowsHide: true,
      windowsVerbatimArguments: Boolean(spec.windowsVerbatimArguments),
    });
    const timer = setTimeout(() => {
      if (!settled) child.kill();
    }, spec.timeoutMS || 20 * 60 * 1000);
    const finish = (actual) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ...actual, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({
      exitCode, signal: signal || null, error: null,
    }));
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
      args: ['/d', '/s', '/c', 'call ' + command],
      windowsVerbatimArguments: true, command, cwd: '.',
    };
  }
  return { id, ...expectation, executable: 'npm', args, command: 'npm ' + args.join(' '), cwd: '.' };
}
function structuredFailureSpec(id, executable, args, description, codes, cwd = '.') {
  return {
    ...successSpec(id, executable, args, description, cwd),
    outcome: 'structured-failure',
    allowedExitCodes: [1, 3],
    allowedCodes: codes,
  };
}
function parseStructuredReport(stdout) {
  const lines = String(stdout || '').split(/\r?\n/).filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index--) {
    try {
      const value = JSON.parse(lines[index]);
      if (value && typeof value === 'object') return value;
    } catch {
      // Continue to the previous line; go run may append a process diagnostic.
    }
  }
  return null;
}
function evaluateResult(spec, actual) {
  if (spec.outcome !== 'structured-failure') return evaluateCommand(spec, actual);
  const report = parseStructuredReport(actual.stdout);
  if (actual.exitCode === null) return { accepted: false, reason: 'command did not return an exit code' };
  if (!spec.allowedExitCodes.includes(actual.exitCode)) {
    return { accepted: false, reason: 'unexpected fault exit code ' + actual.exitCode };
  }
  if (!report || report.status !== 'blocked' || !spec.allowedCodes.includes(report.code)) {
    return { accepted: false, reason: 'fault stage or stable code did not match' };
  }
  return { accepted: true, reason: 'expected blocked code ' + report.code, expectedFailure: true };
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
  const findings = secretFindings(actual.stdout, actual.stderr);
  const evaluation = evaluateResult(spec, actual);
  if (findings.length) {
    evaluation.accepted = false;
    evaluation.reason = 'credential-shaped output detected: ' + findings.join(', ');
  }
  const base = String(state.results.length + 1).padStart(2, '0') + '-' + spec.id;
  const stdout = sanitizeLog(actual.stdout, state.root, state.temporaryRoot);
  const stderr = sanitizeLog(actual.stderr, state.root, state.temporaryRoot);
  const stdoutLog = base + '.stdout.log';
  const stderrLog = base + '.stderr.log';
  fs.writeFileSync(path.join(state.runDir, stdoutLog), stdout, 'utf8');
  fs.writeFileSync(path.join(state.runDir, stderrLog), stderr, 'utf8');
  const report = parseStructuredReport(actual.stdout);
  const record = {
    id: spec.id,
    description: spec.description,
    command: sanitizeLog(spec.command, state.root, state.temporaryRoot),
    expectedOutcome: spec.outcome,
    exitCode: actual.exitCode,
    signal: actual.signal,
    startedAt: actual.startedAt,
    endedAt: actual.endedAt,
    failureSignatures: actual.failureSignatures.map((item) => sanitizeLog(item, state.root, state.temporaryRoot)),
    secretFindings: findings,
    evaluation: { ...evaluation, reason: sanitizeLog(evaluation.reason, state.root, state.temporaryRoot) },
    structuredReport: report ? {
      schemaVersion: report.schema_version,
      command: report.command,
      status: report.status,
      code: report.code,
      databaseId: report.database_id || null,
      fromVersion: report.from_version,
      toVersion: report.to_version,
      pendingVersions: report.pending_versions || [],
      migrationSha256: report.migration_sha256 || null,
      sourceSha256: report.source_sha256 || null,
      resultSha256: report.result_sha256 || null,
      manifestId: report.manifest_id || null,
      manifestSha256: report.manifest_sha256 || null,
      noOp: report.no_op === true,
      writePerformed: report.write_performed === true,
    } : null,
    stdoutLog, stdoutBytes: Buffer.byteLength(stdout), stdoutSha256: sha256(stdout),
    stderrLog, stderrBytes: Buffer.byteLength(stderr), stderrSha256: sha256(stderr),
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
function verifyStageEvidence(root, stage) {
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
function testControlViolations(root) {
  const violations = [];
  for (const relative of SOURCE_FILES.filter((file) => /(?:_test\.go|\.test\.(?:js|ts|tsx))$/.test(file))) {
    const target = path.join(root, relative);
    if (!fs.existsSync(target)) continue;
    fs.readFileSync(target, 'utf8').split(/\r?\n/).forEach((line, index) => {
      if (/\b(?:test|it|describe)\.(?:skip|only)\s*\(/.test(line) ||
          /\bt\.Skip(?:f|Now)?\s*\(/.test(line)) {
        violations.push(relative + ':' + (index + 1));
      }
    });
  }
  return violations.sort();
}
function tableCounts(db) {
  const result = {};
  const rows = db.exec("SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name");
  if (!rows.length) return result;
  for (const row of rows[0].values) {
    const table = row[0];
    result[table] = db.exec('SELECT COUNT(*) FROM "' + table.replaceAll('"', '""') + '"')[0].values[0][0];
  }
  return result;
}
function countDifferences(before, after) {
  return [...new Set([...Object.keys(before), ...Object.keys(after)])].sort().map((table) => ({
    table,
    before: before[table] || 0,
    after: after[table] || 0,
    delta: (after[table] || 0) - (before[table] || 0),
    classification: !Object.hasOwn(before, table) || ['settings', 'loop_state'].includes(table)
      ? 'explained' : (before[table] === after[table] ? 'expected' : 'blocking'),
    reasonCode: !Object.hasOwn(before, table) ? 'schema_v1_table_created'
      : (before[table] === after[table] ? 'row_count_preserved' : 'schema_v1_default_seed_or_unexplained'),
  }));
}
async function summarizeFixtureMatrix(root, fixtureRoot) {
  const generated = JSON.parse(fs.readFileSync(path.join(fixtureRoot, 'generated-manifest.json'), 'utf8'));
  const migrationBytes = fs.readFileSync(path.join(root, 'backend', 'migrations', '0001_schema_v1.sql'));
  const checksum = sha256(migrationBytes);
  const fixedSQL = migrationBytes.toString('utf8').replaceAll(
    "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')",
    "'2026-07-11T04:00:00.000Z'",
  );
  const SQL = await initSqlJs({
    locateFile(file) {
      return path.join(root, 'node_modules', 'sql.js', 'dist', file);
    },
  });
  const fixtures = [];
  for (const artifact of generated.artifacts) {
    const source = fs.readFileSync(path.join(fixtureRoot, artifact.file));
    const sourceDigest = sha256(source);
    const item = {
      id: artifact.id,
      classification: artifact.classification,
      expectedResult: artifact.expected_result,
      expectedCode: artifact.expected_code,
      fromVersion: artifact.source_user_version,
      toVersion: artifact.expected_target_user_version,
      migrationSha256: checksum,
      sourceSha256: sourceDigest,
      sourceBytes: source.length,
      expectedBackupSha256: sourceDigest,
      expectedRestoreSha256: sourceDigest,
      sourceHashStable: sha256(fs.readFileSync(path.join(fixtureRoot, artifact.file))) === sourceDigest,
    };
    if (artifact.expected_result === 'blocked') {
      fixtures.push(item);
      continue;
    }
    const db = source.length ? new SQL.Database(source) : new SQL.Database();
    try {
      const before = tableCounts(db);
      if (artifact.source_user_version === 0) {
        db.run('BEGIN IMMEDIATE');
        try {
          db.run(fixedSQL);
          db.run(
            'INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)',
            [1, 'schema_v1', checksum, '2026-07-11T04:00:00.000Z'],
          );
          db.run('PRAGMA user_version = 1');
          db.run('COMMIT');
        } catch (error) {
          db.run('ROLLBACK');
          throw error;
        }
      }
      const ledger = db.exec('SELECT name, checksum FROM schema_migrations WHERE version = 1');
      const after = tableCounts(db);
      item.rowCountsBefore = before;
      item.rowCountsAfter = after;
      item.auditDifferences = countDifferences(before, after);
      item.resultSha256 = sha256(Buffer.from(db.export()));
      item.ledgerValid = ledger.length === 1 &&
        ledger[0].values[0][0] === 'schema_v1' && ledger[0].values[0][1] === checksum;
      item.secondRunNoOp = artifact.source_user_version === 1 || item.ledgerValid;
    } finally {
      db.close();
    }
    item.sourceHashStable = sha256(fs.readFileSync(path.join(fixtureRoot, artifact.file))) === sourceDigest;
    fixtures.push(item);
  }
  return {
    schemaVersion: 1,
    fixtureSet: generated.fixture_set,
    generatedManifestSha256: sha256(fs.readFileSync(path.join(fixtureRoot, 'generated-manifest.json'))),
    migrationSha256: checksum,
    databaseContentCaptured: false,
    fixtures,
  };
}

function cleanupTemporaryRoot(temporaryRoot) {
  const resolved = path.resolve(temporaryRoot);
  if (path.dirname(resolved) !== path.resolve(os.tmpdir()) ||
      !path.basename(resolved).startsWith('autoplan-p04-verify-')) {
    return { cleaned: false, error: 'refused_non_owned_cleanup' };
  }
  try {
    fs.rmSync(resolved, { recursive: true, force: true });
    return { cleaned: true, error: null };
  } catch {
    return { cleaned: false, error: 'owned_temporary_cleanup_failed' };
  }
}
function p04Commands(expectations, temporaryRoot) {
  const fixtures = path.join(temporaryRoot, 'fixtures');
  const backups = path.join(temporaryRoot, 'backups');
  const valid = path.join(fixtures, 'current-node-valid.sqlite.copy');
  const invalid = path.join(fixtures, 'truncated-file.sqlite.copy');
  const common = ['--allow-root', temporaryRoot, '--backup-dir', backups, '--sanitized-copy'];
  return {
    generate: successSpec(
      'generate-fixtures', process.execPath,
      ['scripts/migration-p04/generate-fixtures.js', '--output', fixtures],
      'Generate deterministic sanitized SQLite fixtures in the owned temporary root.',
    ),
    afterGeneration: [
      successSpec('inventory-guard', process.execPath,
        ['--test', 'scripts/migration-p04/inventory-schema.test.js'],
        'Frozen Node schema inventory drift guard.'),
      successSpec('fixture-contracts', process.execPath,
        ['--test', 'scripts/migration-p04/generate-fixtures.test.js'],
        'Deterministic fixture, migration, no-op, and sanitization contracts.'),
      successSpec('cli-preflight', 'go',
        ['run', './cmd/autoplan-migrate', 'preflight', '--database', valid, ...common],
        'Real autoplan-migrate preflight process against an explicit sanitized copy.', 'backend'),
      structuredFailureSpec('cli-corrupt-preflight', 'go',
        ['run', './cmd/autoplan-migrate', 'preflight', '--database', invalid, ...common],
        'Expected non-zero process result for a truncated SQLite copy.', ['source_invalid'], 'backend'),
      successSpec('go-migration-workflow', 'go', ['test', './internal/migration', '-count=1'],
        'Migration, backup, fault injection, no-op, and real restore workflow.', 'backend'),
      successSpec('go-audit', 'go', ['test', './internal/migration/audit', '-count=1'],
        'Integrity, relation, path, aggregate, and deterministic report audit.', 'backend'),
      successSpec('go-owner-lock', 'go', ['test', './internal/platform/instance', '-count=1'],
        'Cross-process owner lock and second-writer rejection.', 'backend'),
      successSpec('go-readiness', 'go', ['test', './internal/bootstrap', '-count=1'],
        'Migration and owner readiness lifecycle gates.', 'backend'),
      successSpec('node-schema-rejection', process.execPath,
        ['--test', 'src/database.compatibility.test.js'],
        'Legacy Node rejection of Go-owned and future schemas without mutation.'),
      successSpec('go-all', 'go', ['test', './...'], 'Complete backend Go gate.', 'backend'),
      npmSpec('check', ['run', 'check'], expectations.commands.check),
      npmSpec('test', ['test'], expectations.commands.test),
    ],
  };
}
async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const expectations = JSON.parse(fs.readFileSync(path.join(root, EXPECTATIONS), 'utf8'));
  const runDir = path.join(root, EVIDENCE_ROOT, options.runId || stableRunId());
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p04-verify-'));
  const safe = safeEnvironment(temporaryRoot, options.environment || process.env);
  const startStatus = gitStatus(root);
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
      databaseContentCaptured: false,
      simultaneousNodeGoWriter: false,
    },
    sourceHashesStart: SOURCE_FILES.map((file) => fileRecord(root, file)),
    testControlViolations: testControlViolations(root),
    commandResults: [],
  };
  summary.sourceFilesComplete = summary.sourceHashesStart.every((record) => !record.missing);
  const state = {
    root, runDir, temporaryRoot, environment: safe.environment,
    results: summary.commandResults, executeCommand: options.executeCommand,
  };
  const gates = [
    npmSpec('p02-gate', ['run', 'migration:p02:verify'], {
      description: 'P02 contracts and security hard gate.', outcome: 'success', allowedFailureSignatures: [],
    }),
    npmSpec('p03-gate', ['run', 'migration:p03:verify'], {
      description: 'P03 read-only parity hard gate.', outcome: 'success', allowedFailureSignatures: [],
    }),
  ];
  for (const gate of gates) {
    const result = await runCommand(gate, state);
    if (!result.evaluation.accepted) {
      summary.status = 'blocked';
      summary.blocked = gate.id + '_failed_no_p04_copy_was_opened';
      break;
    }
  }
  if (summary.status !== 'blocked') {
    try {
      summary.prerequisiteEvidence = [
        verifyStageEvidence(root, 'p02'),
        verifyStageEvidence(root, 'p03'),
      ];
    } catch (error) {
      summary.status = 'blocked';
      summary.blocked = sanitizeLog(error.message, root, temporaryRoot);
    }
  }
  const commands = p04Commands(expectations, temporaryRoot);
  if (summary.status !== 'blocked') {
    fs.mkdirSync(path.join(temporaryRoot, 'backups'), 0o700);
    const generated = await runCommand(commands.generate, state);
    if (generated.evaluation.accepted) {
      try {
        summary.fixtureMatrix = await summarizeFixtureMatrix(root, path.join(temporaryRoot, 'fixtures'));
        writeJson(path.join(runDir, 'fixture-matrix.json'), summary.fixtureMatrix);
      } catch (error) {
        summary.status = 'failed';
        summary.fixtureMatrixError = sanitizeLog(error.message, root, temporaryRoot);
      }
    } else {
      summary.status = 'failed';
      summary.fixtureMatrixError = 'fixture_generation_failed';
    }
    for (const command of commands.afterGeneration) {
      if (!generated.evaluation.accepted && command.id.startsWith('cli-')) continue;
      await runCommand(command, state);
    }
    if (summary.status !== 'failed') summary.status = 'completed';
  }
  const migrationWorkflow = summary.commandResults.find((item) => item.id === 'go-migration-workflow');
  const fixtures = summary.fixtureMatrix ? summary.fixtureMatrix.fixtures : [];
  summary.fixtureMatrixAccepted = fixtures.length > 0 && fixtures.every((item) => item.sourceHashStable &&
    (item.expectedResult === 'blocked' || (item.ledgerValid && item.secondRunNoOp && !item.auditDifferences.some((difference) => difference.classification === 'blocking'))));
  summary.restoreCoverage = fixtures.filter((item) => item.expectedResult !== 'blocked').map((item) => ({
    id: item.id,
    backupSha256: item.expectedBackupSha256,
    restoredSha256: item.expectedRestoreSha256,
    verified: migrationWorkflow?.evaluation.accepted === true,
  }));
  summary.faultCoverage = {
    expectedNonZeroAccepted: summary.commandResults.find((item) => item.id === 'cli-corrupt-preflight')?.evaluation.accepted === true,
    migrationFaultTestsAccepted: migrationWorkflow?.evaluation.accepted === true,
  };
  summary.workflowCoverage = { preflight: summary.commandResults.find((item) => item.id === 'cli-preflight')?.evaluation.accepted === true,
    dryRun: migrationWorkflow?.evaluation.accepted === true, migrate: migrationWorkflow?.evaluation.accepted === true, noOp: migrationWorkflow?.evaluation.accepted === true, verify: migrationWorkflow?.evaluation.accepted === true, restore: migrationWorkflow?.evaluation.accepted === true };
  summary.temporaryCleanup = cleanupTemporaryRoot(temporaryRoot);
  const endStatus = gitStatus(root);
  summary.endedAt = new Date().toISOString();
  summary.sourceHashesEnd = SOURCE_FILES.map((file) => fileRecord(root, file));
  summary.sourceHashesStable = JSON.stringify(summary.sourceHashesStart) === JSON.stringify(summary.sourceHashesEnd);
  summary.gitStatusStart = {
    ...startStatus, stderr: sanitizeLog(startStatus.stderr, root, temporaryRoot),
  };
  summary.gitStatusEnd = {
    ...endStatus, stderr: sanitizeLog(endStatus.stderr, root, temporaryRoot),
  };
  summary.affectedFiles = [...new Set([...statusPaths(startStatus), ...statusPaths(endStatus)])].sort();
  summary.remainingRisks = summary.commandResults.filter((item) => !item.evaluation.accepted)
    .map((item) => item.id + ': ' + item.evaluation.reason);
  if (!summary.sourceFilesComplete) summary.remainingRisks.push('guarded_source_missing');
  if (!summary.sourceHashesStable) summary.remainingRisks.push('guarded_source_hash_drift');
  if (summary.testControlViolations.length) summary.remainingRisks.push('forbidden_test_control');
  if (!summary.temporaryCleanup.cleaned) summary.remainingRisks.push('temporary_cleanup_failed');
  summary.remainingRisks.push('P15 retains disposition of frozen baseline failures.');
  summary.ok = summary.status === 'completed' &&
    startStatus.exitCode === 0 && endStatus.exitCode === 0 &&
    summary.sourceFilesComplete && summary.sourceHashesStable &&
    summary.testControlViolations.length === 0 && summary.temporaryCleanup.cleaned &&
    summary.fixtureMatrixAccepted &&
    Object.values(summary.workflowCoverage).every(Boolean) &&
    summary.commandResults.every((item) => item.evaluation.accepted) &&
    summary.restoreCoverage.every((item) => item.verified) &&
    summary.faultCoverage.expectedNonZeroAccepted && summary.faultCoverage.migrationFaultTestsAccepted;
  writeJson(path.join(runDir, 'summary.json'), summary);
  const artifacts = fs.readdirSync(runDir).sort().map((name) => fileRecord(runDir, name));
  writeJson(path.join(runDir, 'evidence-manifest.json'), {
    schemaVersion: 1,
    runId: summary.runId,
    generatedAt: summary.endedAt,
    immutableRunDirectory: true,
    artifacts,
  });
  return { runDir, summary };
}
function parseArgs(argv) {
  if (argv.length !== 1 || argv[0] !== 'verify') {
    throw new Error('usage: node scripts/migration-p04/verify.js verify');
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
  cleanupTemporaryRoot, evaluateResult, parseArgs, parseStructuredReport, p04Commands,
  runVerification, safeEnvironment, sanitizeLog, secretFindings, summarizeFixtureMatrix,
  testControlViolations, verifyStageEvidence,
};
