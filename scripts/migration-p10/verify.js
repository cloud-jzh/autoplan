'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

const { createSafeEnvironment, inspectEvidenceFile, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const EVIDENCE_ROOT = 'docs/migration/p10/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p10-verify-';
const SOURCE_FILES = [
  'package.json',
  'scripts/migration-p10/check-prerequisites.js',
  'scripts/migration-p10/check-prerequisites.test.js',
  'scripts/migration-p10/check-safety.js',
  'scripts/migration-p10/check-safety.test.js',
  'scripts/migration-p10/verify.js',
  'scripts/migration-p10/verify.test.js',
  'docs/migration/p10/README.md',
  'docs/migration/p10/runbook.md',
  'docs/migration/p10/evidence/README.md',
  'docs/migration/p10/evidence/manifest.json',
  'scripts/migration-p10/protocol-contract.test.js',
  'fixtures/migration/p10/operation-cases.json',
  'fixtures/migration/p10/event-streams.json',
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
  if (process.platform === 'win32') return { id, description, executable: process.env.ComSpec || 'cmd.exe', args: ['/d', '/s', '/c', `call npm.cmd ${args.join(' ')}`], display: `npm.cmd ${args.join(' ')}`, cwd: '.' };
  return { id, description, executable: 'npm', args, display: `npm ${args.join(' ')}`, cwd: '.' };
}

function nodeSpec(id, args, description) {
  return { id, description, executable: process.execPath, args, display: `node ${args.join(' ')}`, cwd: '.' };
}

function goSpec(id, args, description) {
  return { id, description, executable: 'go', args, display: `go ${args.join(' ')}`, cwd: 'backend' };
}

function verificationCommands() {
  return [
    nodeSpec('p10-gate-tests', ['--test', 'scripts/migration-p10/check-prerequisites.test.js', 'scripts/migration-p10/check-safety.test.js', 'scripts/migration-p10/verify.test.js'], 'Verify fail-closed prerequisite, sanitization, evidence, and serial-stop behavior.'),
    nodeSpec('p10-protocol-contract', ['--test', 'scripts/migration-p10/protocol-contract.test.js'], 'Validate P10 Operation and event-stream fixture contracts.'),
    goSpec('p10-operation-integration', ['test', './internal/application/operations', '-count=1'], 'Validate Operation, idempotency, recovery, and transactional outbox matrix.'),
    goSpec('p10-eventbus-recovery', ['test', './internal/runtime/eventbus', '-count=1'], 'Validate outbox replay, retention, Last-Event-ID, and slow-consumer recovery.'),
    goSpec('p10-httpapi-sse', ['test', './internal/httpapi', '-count=1'], 'Validate authenticated project and Operation SSE boundaries.'),
    nodeSpec('renderer-http-transport', ['--test', 'src/renderer/lib/api/httpClient.test.js'], 'Validate renderer HTTP client compatibility without launching Electron.'),
    npmSpec('renderer-typecheck', ['run', 'check'], 'Run the frozen renderer/Node static check surface.'),
    npmSpec('renderer-baseline-tests', ['test'], 'Run the frozen Node test surface without hiding baseline failures.'),
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
    const timer = setTimeout(() => { if (!settled) child.kill(); }, 30 * 60 * 1000);
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
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

function structuredPrerequisite(stdout) {
  const lines = String(stdout || '').split(/\r?\n/).filter(Boolean);
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    try {
      const value = JSON.parse(lines[index]);
      if (!value || typeof value !== 'object' || Array.isArray(value) || !Array.isArray(value.failures) || typeof value.status !== 'string') continue;
      const p00 = value.p00?.run_id && /^[A-Za-z0-9._-]{1,160}$/.test(value.p00.run_id) && /^[a-f0-9]{64}$/.test(value.p00.summary_sha256 || '')
        ? { run_id: value.p00.run_id, summary_sha256: value.p00.summary_sha256 } : null;
      const stages = Array.isArray(value.stages) ? value.stages.filter((stage) => stage?.stage === 'p09' &&
        /^[A-Za-z0-9._-]{1,160}$/.test(stage.run_id || '') && /^[a-f0-9]{64}$/.test(stage.summary_sha256 || '')).map((stage) => ({
        stage: stage.stage, run_id: stage.run_id, summary_sha256: stage.summary_sha256,
      })) : [];
      return { status: value.status, code: typeof value.code === 'string' ? value.code : 'invalid_prerequisites', failures: value.failures.filter((item) => typeof item === 'string').slice(0, 32), p00, stages };
    } catch {
      // Diagnostics may precede the one structured prerequisite result.
    }
  }
  return null;
}

async function runCommand(spec, state) {
  let actual;
  try { actual = await (state.executeCommand || execute)(spec, state.root, state.environment); }
  catch (error) { actual = { exitCode: null, signal: null, error: error?.message || 'command_executor_failed', stdout: '', stderr: '', startedAt: new Date().toISOString(), endedAt: new Date().toISOString() }; }
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
    structured_output: spec.id === 'prerequisites' ? structuredPrerequisite(stdout) : null,
    logs: { stdout: { path: stdoutFile, bytes: Buffer.byteLength(stdout), sha256: sha256(stdout) }, stderr: { path: stderrFile, bytes: Buffer.byteLength(stderr), sha256: sha256(stderr) } },
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
  try { fs.rmSync(temporaryRoot, { recursive: true, force: true }); return { cleaned: true, code: 'temporary_cleanup_completed' }; }
  catch { return { cleaned: false, code: 'temporary_cleanup_failed' }; }
}

function listArtifacts(runDir) {
  return fs.readdirSync(runDir, { withFileTypes: true }).filter((entry) => entry.isFile() && entry.name !== 'evidence-manifest.json').map((entry) => {
    const bytes = fs.readFileSync(path.join(runDir, entry.name));
    return { path: entry.name, bytes: bytes.length, sha256: sha256(bytes) };
  }).sort((left, right) => left.path.localeCompare(right.path));
}

function parseArgs(argv) {
  if (argv.length !== 3 || argv[0] !== 'verify' || argv[1] !== '--fixture-root' || !argv[2]) throw new Error('usage: node scripts/migration-p10/verify.js verify --fixture-root <authorized-p10-fixture>');
  return { fixtureRoot: argv[2] };
}

function completePrerequisites(value) {
  return value?.status === 'ready' && value.failures?.length === 0 && Boolean(value.p00) &&
    Array.isArray(value.stages) && value.stages.length === 1 && value.stages[0]?.stage === 'p09';
}

async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const evidenceRoot = path.resolve(root, options.evidenceRoot || EVIDENCE_ROOT);
  if (!isWithin(evidenceRoot, root)) throw new Error('evidence_root_invalid');
  const runID = options.runId || `p10-${new Date().toISOString().replace(/[:.]/g, '-')}-${process.pid}`;
  if (!/^[A-Za-z0-9._-]{1,160}$/.test(runID)) throw new Error('run_id_invalid');
  const runDir = path.join(evidenceRoot, runID);
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  for (const name of ['home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data', 'go-cache', 'go-mod-cache', 'fixture-work']) {
    fs.mkdirSync(path.join(temporaryRoot, name), { recursive: true, mode: 0o700 });
  }
  const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
  Object.assign(safe.environment, {
    GOCACHE: path.join(temporaryRoot, 'go-cache'), GOMODCACHE: path.join(temporaryRoot, 'go-mod-cache'),
    AUTOPLAN_P10_FIXTURE_ROOT: fixtureRoot, AUTOPLAN_P10_WORK_ROOT: path.join(temporaryRoot, 'fixture-work'),
  });
  const sourceFiles = options.sourceFiles || SOURCE_FILES;
  const summary = {
    schema_version: 1, kind: 'p10-unified-verification', run_id: runID, status: 'running', started_at: new Date().toISOString(),
    environment: { temporary_roots_only: true, environment_values_captured: false, removed_sensitive_environment_variable_count: safe.removedCount },
    fixture: validateFixtureRoot(fixtureRoot), source_hashes_start: sourceFiles.map((file) => fileHash(root, file)), command_results: [],
    prerequisite_evidence: null, blocked_exit_code: null, remaining_risks: [],
  };
  const state = { root, fixtureRoot, runDir, temporaryRoot, environment: safe.environment, results: summary.command_results, executeCommand: options.executeCommand };
  const prerequisite = nodeSpec('prerequisites', ['scripts/migration-p10/check-prerequisites.js', '--fixture-root', fixtureRoot],
    'Validate P00 frozen baseline, P09 immutable completion evidence, explicit P10 copy authorization, and single-writer safety before P10 tests.');
  const gate = await runCommand(prerequisite, state);
  summary.prerequisite_evidence = gate.structured_output;
  if (!gate.accepted || !completePrerequisites(gate.structured_output)) {
    summary.status = 'blocked';
    summary.blocked_exit_code = gate.exit_code;
    summary.failure = gate.accepted ? 'prerequisite_output_incomplete' : 'prerequisite_command_failed';
  } else {
    for (const spec of verificationCommands()) {
      const result = await runCommand(spec, state);
      if (!result.accepted) {
        summary.status = 'failed';
        summary.failure = `${spec.id}_failed_stopped_remaining_steps`;
        break;
      }
    }
    if (summary.status === 'running') summary.status = 'completed';
  }
  summary.source_hashes_end = sourceFiles.map((file) => fileHash(root, file));
  summary.source_hashes_stable = JSON.stringify(summary.source_hashes_start) === JSON.stringify(summary.source_hashes_end);
  summary.affected_files = summary.source_hashes_start.filter((item) => !item.missing).map((item) => ({ path: item.path, sha256: item.sha256 }));
  summary.temporary_cleanup = cleanupTemporaryRoot(temporaryRoot);
  summary.ended_at = new Date().toISOString();
  if (!summary.fixture.ok) summary.remaining_risks.push(summary.fixture.code);
  if (!summary.source_hashes_stable) summary.remaining_risks.push('verification_source_hash_drift');
  if (!summary.temporary_cleanup.cleaned) summary.remaining_risks.push(summary.temporary_cleanup.code);
  if (summary.status !== 'completed') summary.remaining_risks.push('p10_execution_not_authorized_or_not_completed');
  summary.ok = summary.status === 'completed' && summary.fixture.ok && summary.source_hashes_stable && summary.temporary_cleanup.cleaned &&
    summary.command_results.length === verificationCommands().length + 1 && summary.command_results.every((item) => item.accepted) && completePrerequisites(summary.prerequisite_evidence);
  writeJSON(path.join(runDir, 'summary.json'), summary);
  writeJSON(path.join(runDir, 'evidence-manifest.json'), {
    schema_version: 1, kind: 'p10-evidence-manifest', run_id: runID, generated_at: summary.ended_at,
    immutable_run_directory: true, artifacts: listArtifacts(runDir),
  });
  return { runDir, summary };
}

if (require.main === module) {
  try {
    runVerification(parseArgs(process.argv.slice(2))).then(({ summary }) => {
      process.stdout.write(`${JSON.stringify({ status: summary.status, ok: summary.ok, command_count: summary.command_results.length })}\n`);
      process.exitCode = summary.ok ? 0 : 2;
    }).catch(() => {
      process.stdout.write('{"status":"blocked","code":"verification_arguments_or_setup_invalid"}\n');
      process.exitCode = 2;
    });
  } catch {
    process.stdout.write('{"status":"blocked","code":"verification_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { EVIDENCE_ROOT, SOURCE_FILES, cleanupTemporaryRoot, completePrerequisites, execute, npmSpec, parseArgs, runCommand, runVerification, structuredPrerequisite, verificationCommands };
