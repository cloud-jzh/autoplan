'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { createSafeEnvironment, scanSensitiveText, sha256 } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const PLATFORMS = new Set(['windows', 'macos', 'linux']);
const MODES = new Set(['unsigned-test', 'signed-notarized']);
const MAX_RESULT_BYTES = 256 * 1024;

function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function validateFixture(value) {
  const root = path.resolve(value || '');
  const manifest = path.join(root, 'fixture-manifest.json');
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  const manifestInfo = fs.lstatSync(manifest, { throwIfNoEntry: false });
  if (!path.isAbsolute(value || '') || !info?.isDirectory() || info.isSymbolicLink() || !manifestInfo?.isFile() || manifestInfo.isSymbolicLink()) return blocked('package_smoke_fixture_invalid');
  try {
    const descriptor = JSON.parse(fs.readFileSync(manifest, 'utf8'));
    if (descriptor?.kind !== 'p15-authorized-electron-go-fixture' || descriptor.authorized_copy !== true || descriptor.real_userdata !== false) return blocked('package_smoke_fixture_invalid');
    return { ok: true, root };
  } catch { return blocked('package_smoke_fixture_invalid'); }
}

function resolveDriver(value) {
  if (!value) return blocked('package_smoke_driver_missing');
  const driver = path.resolve(ROOT, value);
  const relative = path.relative(ROOT, driver);
  const info = fs.lstatSync(driver, { throwIfNoEntry: false });
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !info?.isFile() || info.isSymbolicLink() || info.size > MAX_RESULT_BYTES || path.extname(driver) !== '.js') return blocked('package_smoke_driver_invalid');
  return { ok: true, driver };
}

function resolveReleaseDirectory(value) {
  const directory = path.resolve(value || '');
  const relative = path.relative(ROOT, directory);
  const info = fs.lstatSync(directory, { throwIfNoEntry: false });
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !info?.isDirectory() || info.isSymbolicLink()) return blocked('package_smoke_release_directory_invalid');
  return { ok: true, directory };
}

function verifyPackageLayout(platform, releaseDir, mode, environment) {
  const verifier = path.join(ROOT, 'scripts', 'verify-release-artifacts.js');
  const info = fs.lstatSync(verifier, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return blocked('package_smoke_layout_verifier_missing');
  const result = spawnSync(process.execPath, [verifier, '--platform', platform, '--release-dir', releaseDir, '--mode', mode], {
    cwd: ROOT, env: environment, encoding: 'utf8', windowsHide: true, shell: false, timeout: 5 * 60 * 1000,
  });
  if (result.error || result.status !== 0 || scanSensitiveText(`${result.stdout || ''}\n${result.stderr || ''}`).length) return blocked('package_smoke_layout_invalid');
  return { ok: true, code: mode === 'signed-notarized' ? 'package_layout_signed_verified' : 'package_layout_unsigned_verified' };
}

function validateResult(value, platform, mode) {
  if (!value || value.schema_version !== 1 || value.kind !== 'p15-package-smoke-result' || value.status !== 'completed' ||
      value.platform !== platform || value.real_userdata_touched !== false || value.publish_attempted !== false ||
      value.sidecar_resource_verified !== true || value.sidecar_executable !== true || value.process_tree_cleaned !== true ||
      value.fresh_install !== 'passed' || value.launch !== 'ready') return blocked('package_smoke_result_invalid');
  if (mode === 'signed-notarized' && value.trust_status !== 'verified') return blocked('package_smoke_trust_invalid');
  if (mode === 'unsigned-test' && !['blocked', 'unsigned-test'].includes(value.trust_status)) return blocked('package_smoke_unsigned_status_invalid');
  if (scanSensitiveText(JSON.stringify(value)).length) return blocked('package_smoke_result_sensitive');
  return { ok: true, status: 'completed', code: mode === 'signed-notarized' ? 'package_smoke_completed' : 'package_smoke_unsigned_completed' };
}

function parseArgs(argv) {
  const options = { mode: 'unsigned-test' };
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index]; const value = argv[index + 1];
    if (!['--platform', '--release-dir', '--fixture-root', '--driver', '--mode'].includes(key) || !value) throw new Error('arguments_invalid');
    options[{ '--platform': 'platform', '--release-dir': 'releaseDir', '--fixture-root': 'fixtureRoot', '--driver': 'driver', '--mode': 'mode' }[key]] = value;
  }
  if (!PLATFORMS.has(options.platform) || !MODES.has(options.mode) || !options.releaseDir || !options.fixtureRoot) throw new Error('arguments_invalid');
  return options;
}

function smokePackage(options = {}) {
  const fixture = validateFixture(path.resolve(options.fixtureRoot || ''));
  if (!fixture.ok) return fixture;
  const release = resolveReleaseDirectory(options.releaseDir);
  if (!release.ok) return release;
  const driver = resolveDriver(options.driver);
  if (!driver.ok) return driver;
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p15-package-smoke-'));
  const resultFile = path.join(temporaryRoot, 'result.json');
  try {
    const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
    Object.assign(safe.environment, {
      AUTOPLAN_P15_PACKAGE_SMOKE: '1', AUTOPLAN_PACKAGE_SMOKE_ROOT: temporaryRoot,
      AUTOPLAN_PACKAGE_SMOKE_RESULT: resultFile, AUTOPLAN_DATABASE_OWNER: 'go',
    });
    const layout = verifyPackageLayout(options.platform, release.directory, options.mode, safe.environment);
    if (!layout.ok) return layout;
    const result = spawnSync(process.execPath, [driver.driver, '--platform', options.platform, '--release-dir', release.directory, '--fixture-root', fixture.root, '--result-file', resultFile, '--mode', options.mode], {
      cwd: ROOT, env: safe.environment, encoding: 'utf8', windowsHide: true, shell: false, timeout: 20 * 60 * 1000,
    });
    if (result.error || result.status !== 0) return blocked('package_smoke_driver_failed', { exit_code: Number.isInteger(result.status) ? result.status : null });
    if (scanSensitiveText(`${result.stdout || ''}\n${result.stderr || ''}`).length) return blocked('package_smoke_driver_output_sensitive');
    const info = fs.lstatSync(resultFile, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink() || info.size === 0 || info.size > MAX_RESULT_BYTES) return blocked('package_smoke_result_missing');
    let parsed;
    try { parsed = JSON.parse(fs.readFileSync(resultFile, 'utf8')); } catch { return blocked('package_smoke_result_invalid'); }
    return { ...validateResult(parsed, options.platform, options.mode), result_sha256: sha256(fs.readFileSync(resultFile)) };
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true, maxRetries: 1 });
  }
}

if (require.main === module) {
  try {
    const result = smokePackage(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"package_smoke_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { ROOT, parseArgs, smokePackage, validateResult, verifyPackageLayout };
