'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { createSafeEnvironment, scanSensitiveText, sha256 } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const MAX_RESULT_BYTES = 256 * 1024;
const USER_DATA_PATH = /(?:appdata[\\/]roaming[\\/]autoplan|library[\\/]application support[\\/]autoplan|[\\/]\.config[\\/]autoplan|[\\/]userdata(?:[\\/]|$))/i;

function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function validateFixture(value) {
  if (!value || !path.isAbsolute(value)) return blocked('install_upgrade_fixture_path_invalid');
  const root = path.resolve(value);
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (USER_DATA_PATH.test(root) || !info?.isDirectory() || info.isSymbolicLink()) return blocked('install_upgrade_real_userdata_rejected');
  const manifestFile = path.join(root, 'fixture-manifest.json');
  const scenariosFile = path.join(root, 'install-upgrade-scenarios.json');
  try {
    const manifestInfo = fs.lstatSync(manifestFile, { throwIfNoEntry: false });
    const scenariosInfo = fs.lstatSync(scenariosFile, { throwIfNoEntry: false });
    if (!manifestInfo?.isFile() || manifestInfo.isSymbolicLink() || manifestInfo.size > MAX_RESULT_BYTES ||
        !scenariosInfo?.isFile() || scenariosInfo.isSymbolicLink() || scenariosInfo.size > MAX_RESULT_BYTES) return blocked('install_upgrade_fixture_authorization_missing');
    const manifest = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));
    const scenarios = JSON.parse(fs.readFileSync(scenariosFile, 'utf8'));
    if (manifest?.kind !== 'p15-authorized-electron-go-fixture' || manifest.authorized_copy !== true || manifest.real_userdata !== false ||
        !Array.isArray(manifest.contents) || !manifest.contents.includes('install-upgrade-scenarios.json') ||
        !Array.isArray(scenarios?.required_scenarios) || !Array.isArray(scenarios?.required_observations)) return blocked('install_upgrade_fixture_manifest_invalid');
    return { ok: true, root, scenarios };
  } catch { return blocked('install_upgrade_fixture_manifest_invalid'); }
}

function resolveDriver(value) {
  if (!value) return blocked('install_upgrade_driver_missing');
  const driver = path.resolve(ROOT, value);
  const relative = path.relative(ROOT, driver);
  const info = fs.lstatSync(driver, { throwIfNoEntry: false });
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !info?.isFile() || info.isSymbolicLink() || info.size > MAX_RESULT_BYTES || path.extname(driver) !== '.js') {
    return blocked('install_upgrade_driver_invalid');
  }
  return { ok: true, driver };
}

function validateResult(value, scenarios) {
  if (!value || value.schema_version !== 1 || value.kind !== 'p15-install-upgrade-runtime-result' || value.status !== 'completed' ||
      value.real_userdata_touched !== false || value.publish_attempted !== false || value.second_writer_detected !== false ||
      value.integrity_check !== 'ok' || !Array.isArray(value.scenarios) || !value.observations || typeof value.observations !== 'object') {
    return blocked('install_upgrade_result_invalid');
  }
  const passed = new Set(value.scenarios.filter((scenario) => scenario?.status === 'passed').map((scenario) => scenario.id));
  const missingScenarios = scenarios.required_scenarios.filter((id) => !passed.has(id));
  const missingObservations = scenarios.required_observations.filter((name) => value.observations[name] !== true);
  if (missingScenarios.length || missingObservations.length) return blocked('install_upgrade_coverage_incomplete', {
    missing_scenarios: missingScenarios, missing_observations: missingObservations,
  });
  if (scanSensitiveText(JSON.stringify(value)).length) return blocked('install_upgrade_result_sensitive');
  return { ok: true, status: 'completed', code: 'install_upgrade_completed', scenario_count: passed.size };
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!['--fixture-root', '--driver'].includes(argv[index]) || !argv[index + 1]) throw new Error('arguments_invalid');
    options[argv[index] === '--fixture-root' ? 'fixtureRoot' : 'driver'] = argv[index + 1];
  }
  if (!options.fixtureRoot) throw new Error('arguments_invalid');
  return options;
}

function runInstallUpgrade(options = {}) {
  const fixture = validateFixture(path.resolve(options.fixtureRoot || ''));
  if (!fixture.ok) return fixture;
  const driver = resolveDriver(options.driver);
  if (!driver.ok) return driver;
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p15-install-upgrade-'));
  const resultFile = path.join(temporaryRoot, 'result.json');
  try {
    const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
    Object.assign(safe.environment, {
      AUTOPLAN_P15_INSTALL_UPGRADE: '1', AUTOPLAN_DATABASE_OWNER: 'go', AUTOPLAN_INSTALL_DATA_DIR: path.join(temporaryRoot, 'data'),
      AUTOPLAN_INSTALL_RESULT_FILE: resultFile,
    });
    fs.mkdirSync(safe.environment.AUTOPLAN_INSTALL_DATA_DIR, { recursive: true, mode: 0o700 });
    const command = spawnSync(process.execPath, [driver.driver, '--fixture-root', fixture.root, '--result-file', resultFile], {
      cwd: ROOT, env: safe.environment, encoding: 'utf8', windowsHide: true, shell: false, timeout: 20 * 60 * 1000,
    });
    const output = `${command.stdout || ''}\n${command.stderr || ''}`;
    if (command.error || command.status !== 0) return blocked('install_upgrade_driver_failed', { exit_code: Number.isInteger(command.status) ? command.status : null });
    if (scanSensitiveText(output).length) return blocked('install_upgrade_driver_output_sensitive');
    const info = fs.lstatSync(resultFile, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink() || info.size === 0 || info.size > MAX_RESULT_BYTES) return blocked('install_upgrade_result_missing');
    let result;
    try { result = JSON.parse(fs.readFileSync(resultFile, 'utf8')); } catch { return blocked('install_upgrade_result_invalid'); }
    return { ...validateResult(result, fixture.scenarios), result_sha256: sha256(fs.readFileSync(resultFile)) };
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true, maxRetries: 1 });
  }
}

if (require.main === module) {
  try {
    const result = runInstallUpgrade(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"install_upgrade_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { ROOT, parseArgs, runInstallUpgrade, validateFixture, validateResult };
