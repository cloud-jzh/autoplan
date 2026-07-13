'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createSafeEnvironment, inspectEvidenceFile, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const EVIDENCE_ROOT = 'docs/migration/p14/evidence/runs';
const TEMPORARY_PREFIX = 'autoplan-p14-verify-';
const SOURCE_FILES = [
  'package.json',
  'backend/internal/runtime/terminal/pty_contract_test.go',
  'backend/internal/runtime/terminal/process_tree_integration_test.go',
  'backend/internal/application/terminal/integration_test.go',
  'backend/internal/application/terminal/replay_test.go',
  'backend/internal/httpapi/terminal_contract_test.go',
  'backend/internal/httpapi/terminal_ws_integration_test.go',
  'backend/internal/httpapi/terminal_security_test.go',
  'src/renderer/lib/api/terminalTransport.contract.test.ts',
  'src/renderer/hooks/useTerminalSessions.test.ts',
  'scripts/migration-p14/check-prerequisites.js',
  'scripts/migration-p14/check-safety.js',
  'scripts/migration-p14/smoke-packaged-terminal.js',
  'scripts/migration-p14/verify.js',
  'scripts/migration-p14/verify.test.js',
  'fixtures/migration/p14/terminal/.autoplan-p14-terminal-fixture',
  'fixtures/migration/p14/terminal/terminal-fixture-manifest.json',
  'fixtures/migration/p14/terminal/scenarios.json',
  'docs/migration/p14/README.md',
  'docs/migration/p14/runbook.md',
  'docs/migration/p14/evidence/manifest.json',
];
const PACKAGED_ARTIFACTS = Object.freeze({
  win32: 'release/win-unpacked/AutoPlan.exe',
  darwin: 'release/mac/AutoPlan.app/Contents/MacOS/AutoPlan',
  linux: 'release/linux-unpacked/autoplan',
});

function writeJSON(file, value) { fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' }); }
function fileHash(root, relative) {
  const target = path.join(root, relative);
  const info = fs.lstatSync(target, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { path: relative.replaceAll('\\', '/'), missing: true };
  const bytes = fs.readFileSync(target);
  return { path: relative.replaceAll('\\', '/'), bytes: bytes.length, sha256: sha256(bytes) };
}
function sourceSignature(hashes) { return sha256(JSON.stringify(hashes)); }
function nodeSpec(id, args, description) { return { id, executable: process.execPath, args, cwd: '.', display: `node ${args.join(' ')}`, description }; }
function goSpec(id, args, description) { return { id, executable: 'go', args, cwd: 'backend', display: `go ${args.join(' ')}`, description }; }
function npmSpec(id, args, description) { const executable = process.platform === 'win32' ? 'npm.cmd' : 'npm'; return { id, executable, args, cwd: '.', display: `${executable} ${args.join(' ')}`, description }; }
function gitSpec() { return { id: 'p14-worktree-status', executable: 'git', args: ['status', '--short', '--untracked-files=all'], cwd: '.', display: 'git status --short --untracked-files=all', description: 'Record worktree state without modifying it.' }; }

function verificationCommands(fixtureRoot) {
  return [
    nodeSpec('p14-node-verifier-tests', ['--test', 'scripts/migration-p14/verify.test.js'], 'Exercise P14 verifier argument, fixture, and packaged-smoke gates without Electron.'),
    nodeSpec('p14-renderer-contract', ['scripts/migration-p14/verify.js', 'renderer-contract'], 'Transpile and execute the injected Terminal transport and hook contract tests.'),
    goSpec('p14-go-terminal-contracts', ['test', './internal/runtime/terminal', './internal/application/terminal', './internal/httpapi', '-count=1'], 'Run PTY lifecycle, replay, authorization, REST and WebSocket terminal contracts.'),
    npmSpec('p14-type-and-syntax', ['run', 'check'], 'Run the repository TypeScript and JavaScript syntax boundary checks.'),
    npmSpec('p14-node-regression', ['test'], 'Run the repository Node regression suite without test-name filtering.'),
    nodeSpec('p14-packaged-host-smoke', ['scripts/migration-p14/smoke-packaged-terminal.js', '--platform', process.platform, '--artifact', path.join(ROOT, PACKAGED_ARTIFACTS[process.platform] || ''), '--fixture-root', fixtureRoot], 'Run the actual packaged artifact on this host only; other platforms require their own evidence.'),
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
    catch (error) { resolve({ startedAt, endedAt: new Date().toISOString(), stdout, stderr, exitCode: null, signal: null, error: error?.message || 'spawn_failed' }); return; }
    const timer = setTimeout(() => { if (!settled) child.kill(); }, 30 * 60 * 1000);
    const finish = (result) => { if (settled) return; settled = true; clearTimeout(timer); resolve({ ...result, stdout, stderr, startedAt, endedAt: new Date().toISOString() }); };
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
  const record = { id: spec.id, description: spec.description, command: sanitizeText(spec.display, state), input_sha256: sha256(JSON.stringify({ executable: spec.executable, args: spec.args })), started_at: actual.startedAt, ended_at: actual.endedAt, exit_code: Number.isInteger(actual.exitCode) ? actual.exitCode : null, signal: actual.signal || null, error: actual.error ? sanitizeText(actual.error, state) : null, accepted, reason: accepted ? 'accepted' : findings.length ? 'unsafe_output_rejected' : 'nonzero_or_spawn_failure', sensitive_findings: findings, logs: { stdout: { path: stdoutName, bytes: Buffer.byteLength(stdout), sha256: sha256(stdout) }, stderr: { path: stderrName, bytes: Buffer.byteLength(stderr), sha256: sha256(stderr) } } };
  state.results.push(record);
  return record;
}

function ownedTemporaryRoot(root) {
  const resolved = path.resolve(root || '');
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  return path.basename(resolved).startsWith(TEMPORARY_PREFIX) && relative !== '' && !relative.startsWith('..') && !path.isAbsolute(relative);
}
function cleanupTemporaryRoot(root) {
  if (!ownedTemporaryRoot(root)) return { cleaned: false, code: 'temporary_cleanup_refused' };
  try { fs.rmSync(root, { recursive: true, force: true }); return { cleaned: true, code: 'temporary_cleanup_completed' }; }
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
    try { const value = JSON.parse(line); if (value && typeof value === 'object' && typeof value.status === 'string' && Array.isArray(value.failures)) return value; } catch { /* final structured line only */ }
  }
  return null;
}
function structuredSmoke(stdout) {
  for (const line of String(stdout || '').split(/\r?\n/).filter(Boolean).reverse()) {
    try { const value = JSON.parse(line); if (value && typeof value.platform === 'string' && typeof value.ok === 'boolean' && typeof value.code === 'string') return value; } catch { /* final structured line only */ }
  }
  return null;
}

function runRendererContract(rootDir = ROOT) {
  const sources = [
    path.join(rootDir, 'src', 'renderer', 'lib', 'api', 'terminalTransport.contract.test.ts'),
    path.join(rootDir, 'src', 'renderer', 'hooks', 'useTerminalSessions.test.ts'),
  ];
  const nodeRegistration = "const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };";
  const harnessRegistration = 'const describe: TestRegistrar = (_name, run) => run();\nconst it: TestRegistrar = (_name, run) => run();';
  const typescript = require('typescript');
  for (const sourcePath of sources) {
    const source = fs.readFileSync(sourcePath, 'utf8');
    if (!source.includes(nodeRegistration)) throw new Error('renderer_contract_harness_anchor_missing');
    const compiled = typescript.transpileModule(source.replace(nodeRegistration, harnessRegistration), { compilerOptions: { target: typescript.ScriptTarget.ES2022, module: typescript.ModuleKind.CommonJS }, fileName: sourcePath });
    new Function('require', 'exports', 'process', compiled.outputText)(require, {}, { cwd: () => rootDir });
  }
  return { ok: true, code: 'renderer_terminal_contract_passed' };
}

function priorPlatformEvidence(rootDir, signature) {
  const result = Object.fromEntries(['win32', 'darwin', 'linux'].map((platform) => [platform, null]));
  const evidenceRoot = path.join(rootDir, EVIDENCE_ROOT);
  const info = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return result;
  for (const entry of fs.readdirSync(evidenceRoot, { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^[A-Za-z0-9._-]{1,160}$/.test(entry.name)) continue;
    const runDir = path.join(evidenceRoot, entry.name);
    const summaryFile = path.join(runDir, 'summary.json');
    if (!inspectEvidenceFile(summaryFile, runDir).ok) continue;
    let summary;
    try { summary = JSON.parse(fs.readFileSync(summaryFile, 'utf8')); } catch { continue; }
    if (summary?.source_signature !== signature || !summary?.packaged_smoke || typeof summary.packaged_smoke !== 'object') continue;
    for (const platform of Object.keys(result)) {
      const smoke = summary.packaged_smoke[platform];
      if (smoke?.ok === true && smoke.platform === platform && !result[platform]) result[platform] = { run_id: entry.name, ...smoke };
    }
  }
  return result;
}

async function runVerification(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const fixture = validateFixtureRoot(fixtureRoot);
  const evidenceRoot = path.resolve(root, options.evidenceRoot || EVIDENCE_ROOT);
  if (!isWithin(evidenceRoot, root)) throw new Error('evidence_root_invalid');
  const runID = options.runId || `p14-${new Date().toISOString().replace(/[:.]/g, '-')}-${process.pid}`;
  if (!/^[A-Za-z0-9._-]{1,160}$/.test(runID)) throw new Error('run_id_invalid');
  const runDir = path.join(evidenceRoot, runID);
  if (fs.existsSync(runDir)) throw new Error('evidence_directory_exists');
  fs.mkdirSync(runDir, { recursive: true, mode: 0o700 });
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  for (const name of ['home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data', 'go-cache', 'go-mod-cache']) fs.mkdirSync(path.join(temporaryRoot, name), { recursive: true, mode: 0o700 });
  const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
  Object.assign(safe.environment, { GOCACHE: path.join(temporaryRoot, 'go-cache'), GOMODCACHE: path.join(temporaryRoot, 'go-mod-cache'), AUTOPLAN_P14_FIXTURE_ROOT: fixtureRoot });
  const hashesStart = SOURCE_FILES.map((file) => fileHash(root, file));
  const signature = sourceSignature(hashesStart);
  const summary = { schema_version: 1, kind: 'p14-terminal-verification', run_id: runID, status: 'running', started_at: new Date().toISOString(), fixture, environment: { temporary_roots_only: true, environment_values_captured: false, removed_sensitive_environment_variable_count: safe.removedCount }, source_hashes_start: hashesStart, source_signature: signature, command_results: [], prerequisite_evidence: null, packaged_smoke: priorPlatformEvidence(root, signature), remaining_risks: [], blocked_exit_code: null };
  const state = { root, fixtureRoot, temporaryRoot, runDir, environment: safe.environment, results: summary.command_results, executeCommand: options.executeCommand };
  const prerequisite = nodeSpec('p14-prerequisites', ['scripts/migration-p14/check-prerequisites.js', '--fixture-root', fixtureRoot], 'Validate accepted predecessor evidence, fixture authorization, owner boundary, and unfiltered P14 tests.');
  const gate = await runCommand(prerequisite, state);
  summary.prerequisite_evidence = structuredPrerequisite(fs.readFileSync(path.join(runDir, gate.logs.stdout.path), 'utf8'));
  if (!fixture.ok || !gate.accepted || summary.prerequisite_evidence?.status !== 'ready') {
    summary.status = 'blocked';
    summary.blocked_exit_code = gate.exit_code;
    summary.failure = gate.accepted ? 'p14_prerequisite_output_incomplete' : 'p14_prerequisite_command_failed';
    await runCommand(gitSpec(), state);
  } else {
    const commands = verificationCommands(fixtureRoot);
    for (const spec of commands.slice(0, -1)) {
      const result = await runCommand(spec, state);
      if (spec.id === 'p14-packaged-host-smoke') {
        const smoke = structuredSmoke(fs.readFileSync(path.join(runDir, result.logs.stdout.path), 'utf8'));
        summary.packaged_smoke[process.platform] = {
          platform: process.platform, ok: result.accepted && smoke?.ok === true, code: smoke?.code || 'packaged_smoke_output_invalid',
          command_id: result.id, artifact: smoke?.artifact || { relative_path: PACKAGED_ARTIFACTS[process.platform] || null },
          command: smoke?.command || null, exit_code: smoke?.exit_code ?? null, checks: smoke?.checks || 0,
          temporary_cleanup: smoke?.temporary_cleanup || null,
        };
        if (!result.accepted && smoke?.status !== 'blocked') {
          summary.status = 'failed';
          summary.failure = 'p14_packaged_host_smoke_failed';
        }
        continue;
      }
      if (!result.accepted) { summary.status = 'failed'; summary.failure = `${spec.id}_failed_stopped_remaining_steps`; break; }
    }
    const worktree = await runCommand(gitSpec(), state);
    if (!worktree.accepted && summary.status === 'running') { summary.status = 'failed'; summary.failure = 'p14_worktree_status_failed'; }
    const allPlatforms = ['win32', 'darwin', 'linux'].every((platform) => summary.packaged_smoke[platform]?.ok === true);
    if (summary.status === 'running' && !allPlatforms) { summary.status = 'blocked'; summary.failure = 'p14_three_platform_packaged_smoke_evidence_missing'; }
    if (summary.status === 'running') summary.status = 'completed';
  }
  summary.source_hashes_end = SOURCE_FILES.map((file) => fileHash(root, file));
  summary.source_hashes_stable = JSON.stringify(summary.source_hashes_start) === JSON.stringify(summary.source_hashes_end);
  summary.temporary_cleanup = cleanupTemporaryRoot(temporaryRoot);
  summary.ended_at = new Date().toISOString();
  if (!summary.fixture.ok) summary.remaining_risks.push(summary.fixture.code);
  if (!summary.source_hashes_stable) summary.remaining_risks.push('p14_verification_source_hash_drift');
  if (!summary.temporary_cleanup.cleaned) summary.remaining_risks.push(summary.temporary_cleanup.code);
  if (summary.status !== 'completed') summary.remaining_risks.push('p14_execution_not_authorized_or_not_completed');
  summary.ok = summary.status === 'completed' && summary.fixture.ok && summary.source_hashes_stable && summary.temporary_cleanup.cleaned && summary.command_results.every((item) => item.accepted) && ['win32', 'darwin', 'linux'].every((platform) => summary.packaged_smoke[platform]?.ok === true);
  writeJSON(path.join(runDir, 'summary.json'), summary);
  writeJSON(path.join(runDir, 'evidence-manifest.json'), { schema_version: 1, kind: 'p14-evidence-manifest', run_id: runID, generated_at: summary.ended_at, immutable_run_directory: true, artifacts: listArtifacts(runDir) });
  return { runDir, summary };
}

if (require.main === module && process.argv[2] === 'renderer-contract') {
  try { process.stdout.write(`${JSON.stringify(runRendererContract())}\n`); }
  catch { process.stdout.write('{"ok":false,"code":"renderer_terminal_contract_failed"}\n'); process.exitCode = 2; }
} else if (require.main === module) {
  try { runVerification(parseArgs(process.argv.slice(2))).then(({ summary }) => { process.stdout.write(`${JSON.stringify({ status: summary.status, ok: summary.ok, command_count: summary.command_results.length })}\n`); process.exitCode = summary.ok ? 0 : 2; }).catch(() => { process.stdout.write('{"status":"blocked","code":"p14_verification_arguments_or_setup_invalid"}\n'); process.exitCode = 2; }); }
  catch { process.stdout.write('{"status":"blocked","code":"p14_verification_arguments_invalid"}\n'); process.exitCode = 2; }
}

module.exports = { EVIDENCE_ROOT, PACKAGED_ARTIFACTS, SOURCE_FILES, cleanupTemporaryRoot, execute, fileHash, parseArgs, priorPlatformEvidence, runCommand, runRendererContract, runVerification, sourceSignature, structuredPrerequisite, structuredSmoke, verificationCommands };
