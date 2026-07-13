'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createSafeEnvironment, inspectEvidenceFile, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot } = require('./check-p13a-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const EVIDENCE_ROOT = 'docs/migration/p13/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p13a-verify-';
const SOURCE_FILES = [
  'package.json',
  'backend/internal/application/chat/integration_test.go',
  'backend/internal/httpapi/chat_contract_test.go',
  'backend/internal/httpapi/chat_sse_integration_test.go',
  'backend/internal/runtime/agentcli/chat_security_test.go',
  'src/renderer/lib/api/chatTransport.contract.test.ts',
  'src/renderer/hooks/useChat.test.ts',
  'src/renderer/hooks/useChatQueue.test.ts',
  'scripts/migration-p13/check-prerequisites.js',
  'scripts/migration-p13/check-p13a-safety.js',
  'scripts/migration-p13/verify-p13a.js',
  'scripts/migration-p13/verify-p13a.test.js',
  'fixtures/migration/p13/chat/.autoplan-p13a-authorized-chat-copy',
  'fixtures/migration/p13/chat/p13a-fixture-manifest.json',
  'fixtures/migration/p13/chat/security-cases.json',
  'docs/migration/p13/p13a-runbook.md',
  'docs/migration/p13/evidence/p13a-manifest.json',
];

function writeJSON(file, value) { fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' }); }

function fileHash(root, relative) {
  const target = path.join(root, relative);
  const info = fs.lstatSync(target, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { path: relative.replaceAll('\\', '/'), missing: true };
  const bytes = fs.readFileSync(target);
  return { path: relative.replaceAll('\\', '/'), bytes: bytes.length, sha256: sha256(bytes) };
}

function nodeSpec(id, args, description) { return { id, executable: process.execPath, args, cwd: '.', display: `node ${args.join(' ')}`, description }; }
function goSpec(id, args, description) { return { id, executable: 'go', args, cwd: 'backend', display: `go ${args.join(' ')}`, description }; }
function gitSpec() { return { id: 'p13a-worktree-status', executable: 'git', args: ['status', '--short', '--untracked-files=all'], cwd: '.', display: 'git status --short --untracked-files=all', description: 'Record worktree state without modifying it.' }; }

function verificationCommands() {
  return [
    nodeSpec('p13a-gate-tests', ['--test', 'scripts/migration-p13/verify-p13a.test.js'], 'Exercise fail-closed prerequisites, fixture safety, evidence hashing, and command planning.'),
    nodeSpec('p13a-renderer-contracts', ['scripts/migration-p13/verify-p13a.js', 'renderer-contract'], 'Transpile and execute renderer Chat transport and hook contract tests without Electron.'),
    goSpec('p13a-go-contracts', ['test', './internal/application/chat', './internal/httpapi', './internal/runtime/agentcli', '-count=1'], 'Run P13A admission/FIFO/recovery, REST/SSE sequence, and CLI redaction tests.'),
    gitSpec(),
  ];
}

function execute(spec, root, environment) {
  return new Promise((resolve) => {
    const startedAt = new Date().toISOString();
    let stdout = '';
    let stderr = '';
    let settled = false;
    let child;
    try { child = spawn(spec.executable, spec.args, { cwd: path.join(root, spec.cwd), env: environment, shell: false, windowsHide: true }); }
    catch (error) { resolve({ stdout, stderr, exitCode: null, signal: null, error: error?.message || 'spawn_failed', startedAt, endedAt: new Date().toISOString() }); return; }
    const timer = setTimeout(() => { if (!settled) child.kill(); }, 30 * 60 * 1000);
    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ...result, stdout, stderr, startedAt, endedAt: new Date().toISOString() });
    };
    child.stdout?.on('data', (chunk) => { stdout += chunk.toString(); });
    child.stderr?.on('data', (chunk) => { stderr += chunk.toString(); });
    child.on('error', (error) => finish({ exitCode: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exitCode, signal: signal || null, error: null }));
  });
}

async function runCommand(spec, state) {
  let actual;
  try { actual = await (state.executeCommand || execute)(spec, state.root, state.environment); }
  catch (error) { actual = { stdout: '', stderr: '', exitCode: null, signal: null, error: error?.message || 'command_executor_failed', startedAt: new Date().toISOString(), endedAt: new Date().toISOString() }; }
  const raw = `${actual.stdout || ''}\n${actual.stderr || ''}`;
  const findings = scanSensitiveText(raw);
  const stdout = sanitizeText(actual.stdout || '', state);
  const stderr = sanitizeText(actual.stderr || '', state);
  const prefix = `${String(state.results.length + 1).padStart(2, '0')}-${spec.id}`;
  const stdoutName = `${prefix}.stdout.log`;
  const stderrName = `${prefix}.stderr.log`;
  fs.writeFileSync(path.join(state.runDir, stdoutName), stdout, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
  fs.writeFileSync(path.join(state.runDir, stderrName), stderr, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
  const accepted = !actual.error && actual.exitCode === 0 && findings.length === 0;
  const record = {
    id: spec.id, description: spec.description, command: sanitizeText(spec.display, state), input_sha256: sha256(JSON.stringify({ executable: spec.executable, args: spec.args })),
    started_at: actual.startedAt, ended_at: actual.endedAt, exit_code: Number.isInteger(actual.exitCode) ? actual.exitCode : null, signal: actual.signal || null,
    error: actual.error ? sanitizeText(actual.error, state) : null, accepted, reason: accepted ? 'accepted' : findings.length ? 'unsafe_output_rejected' : 'nonzero_or_spawn_failure', sensitive_findings: findings,
    logs: { stdout: { path: stdoutName, bytes: Buffer.byteLength(stdout), sha256: sha256(stdout) }, stderr: { path: stderrName, bytes: Buffer.byteLength(stderr), sha256: sha256(stderr) } },
  };
  state.results.push(record);
  return record;
}

function ownedTemporaryRoot(value) {
  const resolved = path.resolve(value || '');
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  return path.basename(resolved).startsWith(TEMPORARY_PREFIX) && relative !== '' && !relative.startsWith('..') && !path.isAbsolute(relative);
}

function cleanupTemporaryRoot(value) {
  if (!ownedTemporaryRoot(value)) return { cleaned: false, code: 'temporary_cleanup_refused' };
  try { fs.rmSync(value, { recursive: true, force: true }); return { cleaned: true, code: 'temporary_cleanup_completed' }; }
  catch { return { cleaned: false, code: 'temporary_cleanup_failed' }; }
}

function listArtifacts(runDir) {
  return fs.readdirSync(runDir, { withFileTypes: true }).filter((entry) => entry.isFile() && entry.name !== 'evidence-manifest.json').map((entry) => {
    const bytes = fs.readFileSync(path.join(runDir, entry.name));
    return { path: entry.name, bytes: bytes.length, sha256: sha256(bytes) };
  }).sort((left, right) => left.path.localeCompare(right.path));
}

function parseArgs(argv) {
  if (argv.length !== 3 || argv[0] !== 'verify' || argv[1] !== '--fixture-root' || !argv[2]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[2] };
}

function structuredPrerequisite(stdout) {
  for (const line of String(stdout || '').split(/\r?\n/).filter(Boolean).reverse()) {
    try {
      const value = JSON.parse(line);
      if (value && typeof value === 'object' && typeof value.status === 'string' && Array.isArray(value.failures)) return value;
    } catch { /* only the final structured record is authoritative */ }
  }
  return null;
}

function completePrerequisites(value) {
  return value?.status === 'ready' && Array.isArray(value.failures) && value.failures.length === 0 && value?.owner?.owner === 'go' &&
    value?.renderer?.ok === true && Array.isArray(value.stages) && value.stages.length === 3 && value.stages.every((item) => /^[A-Za-z0-9._-]{1,160}$/.test(item?.run_id || ''));
}

function runRendererContract(rootDir = ROOT) {
  const files = [
    'src/renderer/lib/api/chatTransport.contract.test.ts',
    'src/renderer/hooks/useChat.test.ts',
    'src/renderer/hooks/useChatQueue.test.ts',
  ];
  const typescript = require('typescript');
  for (const relative of files) {
    const sourcePath = path.join(rootDir, relative);
    const source = fs.readFileSync(sourcePath, 'utf8');
    const nodeRegistration = "const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };";
    if (!source.includes(nodeRegistration)) throw new Error('renderer_contract_harness_anchor_missing');
    const harness = 'const describe: TestRegistrar = (_name, run) => run();\nconst it: TestRegistrar = (_name, run) => run();';
    const compiled = typescript.transpileModule(source.replace(nodeRegistration, harness), {
      compilerOptions: { target: typescript.ScriptTarget.ES2022, module: typescript.ModuleKind.CommonJS }, fileName: sourcePath,
    });
    new Function('require', 'exports', 'process', compiled.outputText)(require, {}, { cwd: () => rootDir });
  }
  return { ok: true, code: 'renderer_contracts_passed', files: files.length };
}

async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const fixture = validateFixtureRoot(fixtureRoot);
  const evidenceRoot = path.resolve(root, options.evidenceRoot || EVIDENCE_ROOT);
  if (!isWithin(evidenceRoot, root)) throw new Error('evidence_root_invalid');
  const runID = options.runId || `p13a-${new Date().toISOString().replace(/[:.]/g, '-')}-${process.pid}`;
  if (!/^[A-Za-z0-9._-]{1,160}$/.test(runID)) throw new Error('run_id_invalid');
  const runDir = path.join(evidenceRoot, runID);
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  for (const name of ['home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data', 'go-cache', 'go-mod-cache']) fs.mkdirSync(path.join(temporaryRoot, name), { recursive: true, mode: 0o700 });
  const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
  Object.assign(safe.environment, { GOCACHE: path.join(temporaryRoot, 'go-cache'), GOMODCACHE: path.join(temporaryRoot, 'go-mod-cache'), AUTOPLAN_P13A_FIXTURE_ROOT: fixtureRoot });
  const sourceFiles = options.sourceFiles || SOURCE_FILES;
  const summary = {
    schema_version: 1, kind: 'p13a-chat-verification', run_id: runID, status: 'running', started_at: new Date().toISOString(), fixture,
    environment: { temporary_roots_only: true, environment_values_captured: false, removed_sensitive_environment_variable_count: safe.removedCount },
    source_hashes_start: sourceFiles.map((file) => fileHash(root, file)), command_results: [], prerequisite_evidence: null, rollback: { flag: 'go_chat_api', enabled_for_verification: false, in_flight_turns_started: 0, cross_runtime_adoptions: 0 }, remaining_risks: [], blocked_exit_code: null,
  };
  const state = { root, fixtureRoot, temporaryRoot, runDir, environment: safe.environment, results: summary.command_results, executeCommand: options.executeCommand };
  const prerequisite = nodeSpec('prerequisites', ['scripts/migration-p13/check-prerequisites.js', '--fixture-root', fixtureRoot], 'Validate P00/P10/P11/P12 evidence, one Go writer, renderer client boundary, and authorized fixture before P13A commands.');
  const gate = await runCommand(prerequisite, state);
  summary.prerequisite_evidence = structuredPrerequisite(fs.readFileSync(path.join(runDir, gate.logs.stdout.path), 'utf8'));
  if (!fixture.ok || !gate.accepted || !completePrerequisites(summary.prerequisite_evidence)) {
    summary.status = 'blocked';
    summary.blocked_exit_code = gate.exit_code;
    summary.failure = gate.accepted ? 'prerequisite_output_incomplete' : 'prerequisite_command_failed';
    await runCommand(gitSpec(), state);
  } else {
    for (const spec of verificationCommands().slice(0, -1)) {
      const result = await runCommand(spec, state);
      if (!result.accepted) { summary.status = 'failed'; summary.failure = `${spec.id}_failed_stopped_remaining_steps`; break; }
    }
    const worktree = await runCommand(gitSpec(), state);
    if (!worktree.accepted && summary.status === 'running') { summary.status = 'failed'; summary.failure = 'p13a_worktree_status_failed'; }
    if (summary.status === 'running') summary.status = 'completed';
  }
  summary.source_hashes_end = sourceFiles.map((file) => fileHash(root, file));
  summary.source_hashes_stable = JSON.stringify(summary.source_hashes_start) === JSON.stringify(summary.source_hashes_end);
  summary.temporary_cleanup = cleanupTemporaryRoot(temporaryRoot);
  summary.ended_at = new Date().toISOString();
  if (!summary.fixture.ok) summary.remaining_risks.push(summary.fixture.code);
  if (!summary.source_hashes_stable) summary.remaining_risks.push('verification_source_hash_drift');
  if (!summary.temporary_cleanup.cleaned) summary.remaining_risks.push(summary.temporary_cleanup.code);
  if (summary.status !== 'completed') summary.remaining_risks.push('p13a_execution_not_authorized_or_not_completed');
  summary.ok = summary.status === 'completed' && summary.fixture.ok && summary.source_hashes_stable && summary.temporary_cleanup.cleaned && summary.command_results.every((item) => item.accepted) && completePrerequisites(summary.prerequisite_evidence);
  writeJSON(path.join(runDir, 'summary.json'), summary);
  writeJSON(path.join(runDir, 'evidence-manifest.json'), { schema_version: 1, kind: 'p13a-evidence-manifest', run_id: runID, generated_at: summary.ended_at, immutable_run_directory: true, artifacts: listArtifacts(runDir) });
  return { runDir, summary };
}

if (require.main === module && process.argv[2] === 'renderer-contract') {
  try { process.stdout.write(`${JSON.stringify(runRendererContract())}\n`); }
  catch { process.stdout.write('{"ok":false,"code":"renderer_contract_failed"}\n'); process.exitCode = 2; }
} else if (require.main === module) {
  try {
    runVerification(parseArgs(process.argv.slice(2))).then(({ summary }) => {
      process.stdout.write(`${JSON.stringify({ status: summary.status, ok: summary.ok, command_count: summary.command_results.length })}\n`);
      process.exitCode = summary.ok ? 0 : 2;
    }).catch(() => { process.stdout.write('{"status":"blocked","code":"verification_arguments_or_setup_invalid"}\n'); process.exitCode = 2; });
  } catch {
    process.stdout.write('{"status":"blocked","code":"verification_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { EVIDENCE_ROOT, SOURCE_FILES, cleanupTemporaryRoot, completePrerequisites, execute, fileHash, parseArgs, runCommand, runRendererContract, runVerification, structuredPrerequisite, verificationCommands };
