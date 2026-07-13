'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { createSafeEnvironment, isWithin, sanitizeText, scanSensitiveText, sha256, validateFixtureRoot } = require('./check-safety');

const PLATFORM_NAMES = new Set(['win32', 'darwin', 'linux']);
const MAXIMUM_OUTPUT_BYTES = 256 * 1024;

function parseArgs(argv) {
  if (argv.length !== 6 || argv[0] !== '--platform' || argv[2] !== '--artifact' || argv[4] !== '--fixture-root') throw new Error('arguments_invalid');
  if (!PLATFORM_NAMES.has(argv[1]) || !argv[3] || !argv[5]) throw new Error('arguments_invalid');
  return { platform: argv[1], artifact: argv[3], fixtureRoot: argv[5] };
}

function artifactRecord(rootDir, artifact) {
  const releaseRoot = path.resolve(rootDir, 'release');
  const target = path.resolve(artifact);
  if (!isWithin(target, releaseRoot)) return { ok: false, code: 'packaged_artifact_outside_release' };
  const info = fs.lstatSync(target, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'packaged_artifact_missing' };
  const bytes = fs.readFileSync(target);
  return { ok: true, code: 'packaged_artifact_accepted', bytes: bytes.length, sha256: sha256(bytes), relative_path: path.relative(rootDir, target).replaceAll('\\', '/') };
}

function execute(executable, args, options) {
  return new Promise((resolve) => {
    let stdout = '';
    let stderr = '';
    let settled = false;
    let child;
    const startedAt = new Date().toISOString();
    try { child = spawn(executable, args, { cwd: options.cwd, env: options.env, shell: false, windowsHide: true }); }
    catch (error) { resolve({ startedAt, endedAt: new Date().toISOString(), exit_code: null, signal: null, error: error?.message || 'spawn_failed', stdout, stderr }); return; }
    const timer = setTimeout(() => { if (!settled) child.kill(); }, 5 * 60 * 1000);
    const append = (current, chunk) => `${current}${chunk.toString()}`.slice(-MAXIMUM_OUTPUT_BYTES);
    child.stdout?.on('data', (chunk) => { stdout = append(stdout, chunk); });
    child.stderr?.on('data', (chunk) => { stderr = append(stderr, chunk); });
    const finish = (result) => { if (settled) return; settled = true; clearTimeout(timer); resolve({ ...result, stdout, stderr, startedAt, endedAt: new Date().toISOString() }); };
    child.on('error', (error) => finish({ exit_code: null, signal: null, error: error.message }));
    child.on('close', (exitCode, signal) => finish({ exit_code: exitCode, signal: signal || null, error: null }));
  });
}

function parseSmokeResult(stdout, platform) {
  for (const line of String(stdout || '').split(/\r?\n/).filter(Boolean).reverse()) {
    try {
      const value = JSON.parse(line);
      if (value?.kind === 'autoplan-terminal-packaged-smoke' && value.platform === platform && value.ok === true && Array.isArray(value.checks) && value.checks.length >= 8) return { ok: true, checks: value.checks.length };
    } catch { /* only the final structured app result is accepted */ }
  }
  return { ok: false, code: 'packaged_smoke_result_invalid' };
}

async function runPackagedSmoke(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixtureRoot = path.resolve(options.fixtureRoot || '');
  const fixture = validateFixtureRoot(fixtureRoot);
  const artifact = artifactRecord(rootDir, options.artifact || '');
  if (options.platform !== process.platform) return { status: 'blocked', ok: false, code: 'cross_platform_smoke_not_executable', platform: options.platform, fixture, artifact };
  if (!fixture.ok || !artifact.ok) return { status: 'blocked', ok: false, code: !fixture.ok ? fixture.code : artifact.code, platform: options.platform, fixture, artifact };
  const createdTemporaryRoot = !options.temporaryRoot;
  const temporaryRoot = options.temporaryRoot || fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p14-smoke-'));
  const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
  // The package smoke verifies the gated Go owner itself. The value is created
  // locally after inherited AUTOPLAN_* values were removed; it is never read
  // from a developer shell or written to evidence.
  safe.environment.AUTOPLAN_SIDECAR_GO_TERMINAL_API = 'true';
  for (const name of ['home', 'appdata', 'localappdata', 'xdg-config', 'xdg-cache', 'xdg-data']) fs.mkdirSync(path.join(temporaryRoot, name), { recursive: true, mode: 0o700 });
  let actual;
  try {
    actual = await (options.executeCommand || execute)(path.resolve(options.artifact), ['--autoplan-terminal-smoke', '--fixture-root', fixtureRoot], { cwd: rootDir, env: safe.environment });
  } catch (error) {
    actual = { stdout: '', stderr: '', exit_code: null, signal: null, error: error?.message || 'command_executor_failed' };
  }
  const raw = `${actual.stdout || ''}\n${actual.stderr || ''}`;
  const findings = scanSensitiveText(raw);
  const result = parseSmokeResult(actual.stdout, options.platform);
  let temporaryCleanup = { cleaned: !createdTemporaryRoot, code: createdTemporaryRoot ? 'temporary_cleanup_pending' : 'temporary_root_supplied' };
  if (createdTemporaryRoot) {
    try { fs.rmSync(temporaryRoot, { recursive: true, force: true }); temporaryCleanup = { cleaned: true, code: 'temporary_cleanup_completed' }; }
    catch { temporaryCleanup = { cleaned: false, code: 'temporary_cleanup_failed' }; }
  }
  const succeeded = !actual.error && actual.exit_code === 0 && findings.length === 0 && result.ok && temporaryCleanup.cleaned;
  return {
    status: succeeded ? 'completed' : 'failed',
    ok: succeeded,
    code: actual.error ? 'packaged_smoke_spawn_failed' : findings.length ? 'packaged_smoke_unsafe_output' : !temporaryCleanup.cleaned ? temporaryCleanup.code : result.ok ? 'packaged_smoke_completed' : result.code,
    platform: options.platform, fixture, artifact,
    command: { executable: artifact.relative_path, args: ['--autoplan-terminal-smoke', '--fixture-root', '<fixture>'] },
    exit_code: Number.isInteger(actual.exit_code) ? actual.exit_code : null, signal: actual.signal || null,
    stdout: sanitizeText(actual.stdout || '', { rootDir, fixtureRoot, temporaryRoot }), stderr: sanitizeText(actual.stderr || '', { rootDir, fixtureRoot, temporaryRoot }),
    sensitive_findings: findings, checks: result.checks || 0, temporary_cleanup: temporaryCleanup,
  };
}

if (require.main === module) {
  try {
    const options = parseArgs(process.argv.slice(2));
    runPackagedSmoke(options).then((result) => { process.stdout.write(`${JSON.stringify(result)}\n`); process.exitCode = result.ok ? 0 : 2; }).catch(() => { process.stdout.write('{"status":"blocked","code":"packaged_smoke_setup_invalid"}\n'); process.exitCode = 2; });
  } catch { process.stdout.write('{"status":"blocked","code":"packaged_smoke_arguments_invalid"}\n'); process.exitCode = 2; }
}

module.exports = { artifactRecord, execute, parseArgs, parseSmokeResult, runPackagedSmoke };
