'use strict';

// P09 is intentionally fail-closed: the only accepted input is a generated,
// sanitized scale copy and every following command stops at its first failure.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const { createSafeEnvironment, inspectEvidenceFile, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const EVIDENCE_ROOT = 'docs/migration/p09/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p09-verify-';
const SOURCE_FILES = [
  'package.json',
  'scripts/migration-p09/check-prerequisites.js',
  'scripts/migration-p09/check-prerequisites.test.js',
  'scripts/migration-p09/check-safety.js',
  'scripts/migration-p09/check-safety.test.js',
  'scripts/migration-p09/verify.js',
  'scripts/migration-p09/verify.test.js',
  'docs/migration/p09/README.md',
  'docs/migration/p09/evidence/README.md',
  'docs/migration/p09/evidence/manifest.json',
];

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
}

function fileHash(root, relative) {
  const target = path.join(root, relative);
  const info = fs.lstatSync(target, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { path: relative.replaceAll('\\', '/'), missing: true };
  const bytes = fs.readFileSync(target);
  return { path: relative.replaceAll('\\', '/'), bytes: bytes.length, sha256: sha256(bytes) };
}

function npmSpec(id, args, description) {
  if (process.platform === 'win32') {
    return { id, description, executable: process.env.ComSpec || 'cmd.exe', args: ['/d', '/s', '/c', `call npm.cmd ${args.join(' ')}`], display: `npm.cmd ${args.join(' ')}`, cwd: '.' };
  }
  return { id, description, executable: 'npm', args, display: `npm ${args.join(' ')}`, cwd: '.' };
}

function nodeSpec(id, args, description) {
  return { id, description, executable: process.execPath, args, display: `node ${args.join(' ')}`, cwd: '.' };
}

function goSpec(id, args, description, cwd = 'backend') {
  return { id, description, executable: 'go', args, display: `go ${args.join(' ')}`, cwd };
}

function verificationCommands(temporaryRoot) {
  const copyRoot = path.join(temporaryRoot, 'sanitized-scale-copy');
  const copyFile = path.join(copyRoot, 'scale-copy.json');
  const drillEvidence = path.join(copyRoot, 'drill-evidence');
  const runtimeProgram = [
    "const {loadScaleCopy,verifyLegacyRuntime}=require('./scripts/migration-p09/verify-legacy-runtime');",
    "verifyLegacyRuntime(loadScaleCopy(process.argv[1])).then((result)=>process.stdout.write(JSON.stringify({status:result.status,command_count:result.command_count,snapshot_sha256:result.snapshot_sha256,owner:result.owner})+'\\n')).catch((error)=>{process.stdout.write(JSON.stringify({status:'blocked',code:error&&error.code||'runtime_verification_failed'})+'\\n');process.exitCode=2;});",
  ].join('');
  return [
    npmSpec('p04-completion-evidence', ['run', 'migration:p04:verify'], 'Re-run and verify daemon supervisor/readiness completion evidence.'),
    npmSpec('p05-completion-evidence', ['run', 'migration:p05:verify'], 'Re-run and verify maintenance ownership cutover completion evidence.'),
    npmSpec('p06-completion-evidence', ['run', 'migration:p06:verify'], 'Re-run and verify rollback, backup recovery and fault-injection evidence.'),
    npmSpec('p07-completion-evidence', ['run', 'migration:p07:verify'], 'Re-run and verify sanitized near-real scale compatibility evidence.'),
    npmSpec('p08-completion-evidence', ['run', 'migration:p08:verify'], 'Re-run and verify the frozen static/secret safety evidence.'),
    nodeSpec('p09-gate-tests', ['--test', 'scripts/migration-p09/check-prerequisites.test.js', 'scripts/migration-p09/check-safety.test.js', 'scripts/migration-p09/verify.test.js'],
      'Verify P09 fail-closed prerequisites, evidence sanitization and serial stop behavior.'),
    nodeSpec('node-db-audit', ['--test', 'scripts/migration-p09/audit-node-db-access.test.js'], 'Audit legacy Node database access without opening user data.'),
    nodeSpec('godata-client-contracts', ['--test',
      'src/data/goDataClient.test.js', 'src/data/databaseOwnerGuard.test.js', 'src/data/goDataClient.contract.test.js',
      'src/loop/runtime.goDataClient.test.js', 'src/chat/chatController.goDataClient.test.js',
      'src/loop/scriptHooks.goDataClient.test.js', 'src/executors/executorRunner.goDataClient.test.js',
    ], 'Verify GoDataClient command compatibility and Node SQL ownership blocking.'),
    nodeSpec('daemon-supervisor', ['--test', 'src/daemon/supervisor.test.js'], 'Verify daemon readiness, owner handoff and process cleanup.'),
    goSpec('maintenance-cutover', ['test', './internal/application/maintenance', './internal/migration', '-count=1'], 'Verify maintenance drain, owner lock, first Go write and backup recovery boundaries.'),
    nodeSpec('scale-copy-generate', ['scripts/migration-p09/generate-scale-copy.js', '--output-dir', copyRoot], 'Generate an isolated system-temporary sanitized scale copy.'),
    nodeSpec('legacy-runtime-godata', ['-e', runtimeProgram, copyFile], 'Exercise legacy Loop, Chat, hooks and executors only through GoDataClient.'),
    nodeSpec('cutover-recovery-drill', ['scripts/migration-p09/run-cutover-drill.js', '--fixture-root', copyRoot, '--evidence-dir', drillEvidence], 'Run maintenance fault matrix, backup recovery and process/lock cleanup drill serially.'),
    nodeSpec('scale-copy-cleanup', ['scripts/migration-p09/generate-scale-copy.js', '--output-dir', copyRoot, '--cleanup'], 'Remove only the verifier-owned generated sanitized scale copy.'),
  ];
}

function execute(spec, root, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    const child = spawn(spec.executable, spec.args, {
      cwd: path.join(root, spec.cwd || '.'), env: environment, shell: false, windowsHide: true,
      windowsVerbatimArguments: process.platform === 'win32' && spec.executable.endsWith('cmd.exe'),
    });
    const timeout = setTimeout(() => { if (!settled) child.kill(); }, 30 * 60 * 1000);
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      resolve({ ...result, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exitCode, signal: signal || null, error: null }));
  });
}

function commandDisplay(spec, state) {
  return sanitizeText(spec.display || [spec.executable, ...spec.args].join(' '), {
    rootDir: state.root, fixtureRoot: state.fixtureRoot, temporaryRoot: state.temporaryRoot,
  });
}

function structuredEvidence(id, stdout) {
  const lines = String(stdout || '').split(/\r?\n/).filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    try {
      const value = JSON.parse(lines[index]);
      if (!value || typeof value !== 'object' || Array.isArray(value)) continue;
      if (id === 'legacy-runtime-godata' && value.status === 'completed' && Number.isInteger(value.command_count) &&
          /^[a-f0-9]{64}$/.test(value.snapshot_sha256 || '') && value.owner?.owner === 'go' &&
          value.owner?.node_sql_attempt === 'node_sql_blocked' && value.owner?.second_go_attempt === 'go_owner_locked' && value.owner?.writer_count === 1) {
        return { status: value.status, command_count: value.command_count, snapshot_sha256: value.snapshot_sha256, owner: value.owner };
      }
      if (id === 'cutover-recovery-drill' && (value.status === 'completed' || value.status === 'blocked') && Number.isInteger(value.fault_count)) {
        return { status: value.status, fault_count: value.fault_count };
      }
      if (id === 'scale-copy-generate' && value.status === 'generated' && /^[a-f0-9]{64}$/.test(value.sha256 || '')) return { status: value.status, sha256: value.sha256 };
      if (id === 'scale-copy-cleanup' && value.status === 'cleaned') return { status: value.status };
      if (id === 'prerequisites' && (value.status === 'ready' || value.status === 'blocked') && typeof value.code === 'string' && Array.isArray(value.failures)) {
        const stageEvidence = Array.isArray(value.stages) ? value.stages.filter((stage) => /^[a-z0-9-]{1,32}$/.test(stage?.stage || '') &&
          (stage.run_id === null || /^[A-Za-z0-9._-]{1,160}$/.test(stage.run_id)) && (stage.summary_sha256 === null || /^[a-f0-9]{64}$/.test(stage.summary_sha256))).map((stage) => ({
          stage: stage.stage, run_id: stage.run_id, summary_sha256: stage.summary_sha256,
        })) : [];
        const p00 = value.p00?.run_id && /^[A-Za-z0-9._-]{1,160}$/.test(value.p00.run_id) && /^[a-f0-9]{64}$/.test(value.p00.summary_sha256 || '')
          ? { run_id: value.p00.run_id, summary_sha256: value.p00.summary_sha256 } : null;
        return { status: value.status, code: value.code, failures: value.failures.filter((item) => typeof item === 'string').slice(0, 32), p00, stages: stageEvidence };
      }
    } catch {
      // Command diagnostics may precede a final structured result.
    }
  }
  return null;
}

async function runCommand(spec, state) {
  let actual;
  try {
    actual = await (state.executeCommand || execute)(spec, state.root, state.environment);
  } catch (error) {
    actual = { exitCode: null, signal: null, error: error?.message || 'command_executor_failed', stdout: '', stderr: '', startedAt: new Date().toISOString(), endedAt: new Date().toISOString() };
  }
  const rawOutput = `${actual.stdout || ''}\n${actual.stderr || ''}`;
  const findings = scanSensitiveText(rawOutput);
  const stdout = sanitizeText(actual.stdout || '', { rootDir: state.root, fixtureRoot: state.fixtureRoot, temporaryRoot: state.temporaryRoot });
  const stderr = sanitizeText(actual.stderr || '', { rootDir: state.root, fixtureRoot: state.fixtureRoot, temporaryRoot: state.temporaryRoot });
  const prefix = `${String(state.results.length + 1).padStart(2, '0')}-${spec.id}`;
  const stdoutFile = `${prefix}.stdout.log`;
  const stderrFile = `${prefix}.stderr.log`;
  fs.writeFileSync(path.join(state.runDir, stdoutFile), stdout, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
  fs.writeFileSync(path.join(state.runDir, stderrFile), stderr, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
  const accepted = actual.exitCode === 0 && !actual.error && findings.length === 0;
  const record = {
    id: spec.id, description: spec.description, command: commandDisplay(spec, state), input_sha256: sha256(JSON.stringify({ executable: spec.executable, args: spec.args })),
    started_at: actual.startedAt, ended_at: actual.endedAt, exit_code: Number.isInteger(actual.exitCode) ? actual.exitCode : null,
    signal: actual.signal || null, error: actual.error ? sanitizeText(actual.error, { rootDir: state.root, fixtureRoot: state.fixtureRoot, temporaryRoot: state.temporaryRoot }) : null,
    accepted, reason: accepted ? 'accepted' : findings.length ? 'unsafe_output_rejected' : 'nonzero_or_spawn_failure', sensitive_findings: findings,
    structured_output: structuredEvidence(spec.id, stdout),
    logs: {
      stdout: { path: stdoutFile, bytes: Buffer.byteLength(stdout), sha256: sha256(stdout) },
      stderr: { path: stderrFile, bytes: Buffer.byteLength(stderr), sha256: sha256(stderr) },
    },
  };
  state.results.push(record);
  return record;
}

function ownedTemporaryRoot(value) {
  const resolved = path.resolve(value || '');
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  return path.basename(resolved).startsWith(TEMPORARY_PREFIX) && relative !== '' && !relative.startsWith('..') && !path.isAbsolute(relative);
}

function cleanupTemporaryRoot(temporaryRoot) {
  if (!ownedTemporaryRoot(temporaryRoot)) return { cleaned: false, code: 'temporary_cleanup_refused' };
  try {
    fs.rmSync(temporaryRoot, { recursive: true, force: true });
    return { cleaned: true, code: 'temporary_cleanup_completed' };
  } catch {
    return { cleaned: false, code: 'temporary_cleanup_failed' };
  }
}

function fixtureMetadata(fixtureRoot) {
  const fixture = validateFixtureRoot(fixtureRoot);
  if (!fixture.ok) return fixture;
  const manifestFile = path.join(path.resolve(fixtureRoot), 'scale-manifest.json');
  const copyFile = path.join(path.resolve(fixtureRoot), 'scale-copy.json');
  const manifestSafety = inspectEvidenceFile(manifestFile, fixtureRoot);
  const copySafety = inspectEvidenceFile(copyFile, fixtureRoot);
  if (!manifestSafety.ok || !copySafety.ok) return { ok: false, code: 'fixture_content_unsafe' };
  try {
    const manifest = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));
    if (manifest?.kind !== 'p09-generated-scale-copy' || !Number.isInteger(manifest.schema_version_target) ||
        !manifest.row_counts || !manifest.table_sha256 || Object.values(manifest.table_sha256).some((value) => !/^[a-f0-9]{64}$/.test(value))) {
      return { ok: false, code: 'fixture_manifest_invalid' };
    }
    return {
      ok: true, code: 'fixture_metadata_accepted', fixture_id: fixture.fixture_id, marker_sha256: fixture.marker_sha256,
      copy_sha256: copySafety.sha256, manifest_sha256: manifestSafety.sha256, schema_version_target: manifest.schema_version_target,
      row_counts: manifest.row_counts, table_sha256: manifest.table_sha256,
    };
  } catch { return { ok: false, code: 'fixture_manifest_invalid' }; }
}

function captureArtifact(state, source, targetName) {
  const sourceInfo = fs.lstatSync(source, { throwIfNoEntry: false });
  if (!sourceInfo?.isFile() || sourceInfo.isSymbolicLink() || sourceInfo.size > 8 * 1024 * 1024) throw new Error('artifact_source_invalid');
  const bytes = fs.readFileSync(source);
  if (scanSensitiveText(bytes.toString('utf8')).length) throw new Error('artifact_source_sensitive');
  const target = path.join(state.runDir, targetName);
  if (!isWithin(target, state.runDir)) throw new Error('artifact_target_invalid');
  fs.writeFileSync(target, bytes, { mode: 0o600, flag: 'wx' });
  return { path: targetName, bytes: bytes.length, sha256: sha256(bytes) };
}

function listArtifacts(runDir) {
  return fs.readdirSync(runDir, { withFileTypes: true }).filter((entry) => entry.isFile() && entry.name !== 'evidence-manifest.json').map((entry) => {
    const bytes = fs.readFileSync(path.join(runDir, entry.name));
    return { path: entry.name, bytes: bytes.length, sha256: sha256(bytes) };
  }).sort((left, right) => left.path.localeCompare(right.path));
}

function parseArgs(argv) {
  if (argv.length !== 3 || argv[0] !== 'verify' || argv[1] !== '--fixture-root' || !argv[2]) {
    throw new Error('usage: node scripts/migration-p09/verify.js verify --fixture-root <sanitized-scale-copy>');
  }
  return { fixtureRoot: argv[2] };
}

function hasCompleteUpstreamEvidence(value) {
  if (!value?.p00 || !/^[a-f0-9]{64}$/.test(value.p00.summary_sha256 || '') || !Array.isArray(value.stages)) return false;
  const expected = ['p04', 'p05', 'p06', 'p07', 'p08'];
  return value.stages.map((stage) => stage.stage).sort().join(',') === expected.join(',') &&
    value.stages.every((stage) => /^[A-Za-z0-9._-]{1,160}$/.test(stage.run_id || '') && /^[a-f0-9]{64}$/.test(stage.summary_sha256 || ''));
}

async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const runDir = path.join(root, options.evidenceRoot || EVIDENCE_ROOT, options.runId || `p09-${new Date().toISOString().replace(/[:.]/g, '-')}-${process.pid}`);
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  for (const directory of ['database', 'home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data', 'go-cache', 'go-mod-cache']) {
    fs.mkdirSync(path.join(temporaryRoot, directory), { recursive: true, mode: 0o700 });
  }
  const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
  Object.assign(safe.environment, {
    GOCACHE: path.join(temporaryRoot, 'go-cache'), GOMODCACHE: path.join(temporaryRoot, 'go-mod-cache'),
    AUTOPLAN_P09_DATABASE_ROOT: path.join(temporaryRoot, 'database'), AUTOPLAN_P09_FIXTURE_ROOT: fixtureRoot,
  });
  const summary = {
    schema_version: 1, kind: 'p09-unified-verification', run_id: path.basename(runDir), status: 'running', started_at: new Date().toISOString(),
    environment: { temporary_roots_only: true, environment_values_captured: false, removed_sensitive_environment_variable_count: safe.removedCount },
    fixture: fixtureMetadata(fixtureRoot), source_hashes_start: (options.sourceFiles || SOURCE_FILES).map((file) => fileHash(root, file)),
    command_results: [], owner_timeline: [{ event: 'verification_started', owner: 'none', writer_count: 0 }], captured_artifacts: [], remaining_risks: [],
  };
  const state = { root, fixtureRoot, runDir, temporaryRoot, environment: safe.environment, results: summary.command_results, executeCommand: options.executeCommand };
  const prerequisite = nodeSpec('prerequisites', ['scripts/migration-p09/check-prerequisites.js', '--fixture-root', fixtureRoot],
    'Verify P00 frozen red signature, P04-P08 immutable completion evidence, explicit copy authorization and idle owner state.');
  const gate = await runCommand(prerequisite, state);
  if (!summary.fixture.ok || !gate.accepted || gate.structured_output?.status !== 'ready' || !hasCompleteUpstreamEvidence(gate.structured_output)) {
    summary.status = 'blocked';
    summary.blocked_by = gate.id;
    summary.blocked_exit_code = gate.exit_code;
  }
  if (gate.structured_output?.status === 'ready') summary.upstream_evidence = gate.structured_output;
  if (summary.status === 'running') {
    for (const spec of verificationCommands(temporaryRoot)) {
      const result = await runCommand(spec, state);
      if (spec.id === 'legacy-runtime-godata' && result.accepted) {
        if (!result.structured_output) {
          summary.status = 'failed';
          summary.failure = 'runtime_owner_or_snapshot_evidence_missing';
          break;
        }
        summary.runtime_snapshot = result.structured_output;
        summary.owner_timeline.push({ event: 'first_go_runtime_write_verified', owner: 'go', writer_count: 1, node_sql_write: 'blocked', second_go_owner: 'blocked', command: spec.id });
      }
      if (spec.id === 'cutover-recovery-drill' && result.accepted) {
        try {
          summary.captured_artifacts.push(captureArtifact(state, path.join(temporaryRoot, 'sanitized-scale-copy', 'drill-evidence', 'cutover-drill-report.json'), 'cutover-drill-report.json'));
          summary.owner_timeline.push({ event: 'maintenance_lock_and_process_cleanup_verified', owner: 'go', writer_count: 1, command: spec.id });
        } catch (error) {
          summary.status = 'failed';
          summary.failure = sanitizeText(error.message, { rootDir: root, fixtureRoot, temporaryRoot });
          break;
        }
      }
      if (spec.id === 'scale-copy-generate' && result.accepted) {
        try { summary.captured_artifacts.push(captureArtifact(state, path.join(temporaryRoot, 'sanitized-scale-copy', 'scale-manifest.json'), 'temporary-scale-manifest.json')); }
        catch (error) { summary.status = 'failed'; summary.failure = sanitizeText(error.message, { rootDir: root, fixtureRoot, temporaryRoot }); break; }
      }
      if (!result.accepted) {
        summary.status = 'failed';
        summary.failure = `${spec.id}_failed_stopped_remaining_steps`;
        break;
      }
    }
    if (summary.status === 'running') summary.status = 'completed';
  }
  summary.source_hashes_end = (options.sourceFiles || SOURCE_FILES).map((file) => fileHash(root, file));
  summary.source_hashes_stable = JSON.stringify(summary.source_hashes_start) === JSON.stringify(summary.source_hashes_end);
  summary.affected_files = summary.source_hashes_start.filter((record) => !record.missing).map((record) => ({ path: record.path, sha256: record.sha256 }));
  summary.resource_checks = summary.fixture.ok ? {
    schema_version_target: summary.fixture.schema_version_target, row_counts: summary.fixture.row_counts, table_sha256: summary.fixture.table_sha256,
  } : null;
  summary.temporary_cleanup = cleanupTemporaryRoot(temporaryRoot);
  summary.ended_at = new Date().toISOString();
  if (!summary.source_hashes_stable) summary.remaining_risks.push('verification_source_hash_drift');
  if (!summary.temporary_cleanup.cleaned) summary.remaining_risks.push(summary.temporary_cleanup.code);
  if (!summary.fixture.ok) summary.remaining_risks.push(summary.fixture.code);
  if (summary.status !== 'completed') summary.remaining_risks.push('cutover_not_authorized_after_failed_or_blocked_gate');
  summary.remaining_risks.push('P15 retains the documented disposition of the frozen P00 check red signature.');
  summary.ok = summary.status === 'completed' && summary.fixture.ok && summary.source_hashes_stable && summary.temporary_cleanup.cleaned &&
    summary.command_results.length > 1 && summary.command_results.every((item) => item.accepted) &&
    Boolean(summary.runtime_snapshot) && Boolean(summary.upstream_evidence) && summary.upstream_evidence.stages.length === 5 &&
    summary.captured_artifacts.some((item) => item.path === 'cutover-drill-report.json');
  writeJSON(path.join(runDir, 'summary.json'), summary);
  writeJSON(path.join(runDir, 'evidence-manifest.json'), {
    schema_version: 1, kind: 'p09-evidence-manifest', run_id: summary.run_id, generated_at: summary.ended_at,
    immutable_run_directory: true, artifacts: listArtifacts(runDir),
  });
  return { runDir, summary };
}

if (require.main === module) {
  try {
    const args = parseArgs(process.argv.slice(2));
    runVerification(args).then(({ summary }) => {
      process.stdout.write(`${JSON.stringify({ status: summary.status, ok: summary.ok, command_count: summary.command_results.length })}\n`);
      process.exitCode = summary.ok ? 0 : 2;
    }).catch((error) => {
      process.stdout.write(`${JSON.stringify({ status: 'blocked', code: 'verification_arguments_or_setup_invalid' })}\n`);
      process.exitCode = 2;
    });
  } catch {
    process.stdout.write(`${JSON.stringify({ status: 'blocked', code: 'verification_arguments_invalid' })}\n`);
    process.exitCode = 2;
  }
}

module.exports = { EVIDENCE_ROOT, SOURCE_FILES, cleanupTemporaryRoot, execute, fixtureMetadata, hasCompleteUpstreamEvidence, npmSpec, parseArgs, runCommand, runVerification, structuredEvidence, verificationCommands };
