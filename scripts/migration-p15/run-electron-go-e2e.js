'use strict';

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { createSafeEnvironment, scanSensitiveText, sha256 } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const FIXTURE_MARKER = '.autoplan-p15-electron-go-fixture';
const REQUIRED_KIND = 'p15-authorized-electron-go-fixture';
const MAX_RESULT_BYTES = 256 * 1024;
const USER_DATA_PATH = /(?:appdata[\\/]roaming[\\/]autoplan|library[\\/]application support[\\/]autoplan|[\\/]\.config[\\/]autoplan|[\\/]userdata(?:[\\/]|$))/i;

function blocked(code, extra = {}) { return { ok: false, status: 'blocked', code, ...extra }; }

function validateFixture(value) {
  if (!value || !path.isAbsolute(value)) return blocked('electron_go_fixture_path_invalid');
  const root = path.resolve(value);
  const info = fs.lstatSync(root, { throwIfNoEntry: false });
  if (USER_DATA_PATH.test(root) || !info?.isDirectory() || info.isSymbolicLink()) return blocked('electron_go_real_userdata_rejected');
  const marker = path.join(root, FIXTURE_MARKER);
  const manifestFile = path.join(root, 'fixture-manifest.json');
  const scenariosFile = path.join(root, 'e2e-scenarios.json');
  const files = [marker, manifestFile, scenariosFile];
  if (files.some((file) => { const entry = fs.lstatSync(file, { throwIfNoEntry: false }); return !entry?.isFile() || entry.isSymbolicLink() || entry.size > MAX_RESULT_BYTES; })) {
    return blocked('electron_go_fixture_authorization_missing');
  }
  try {
    const manifest = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));
    const scenarios = JSON.parse(fs.readFileSync(scenariosFile, 'utf8'));
    if (manifest?.schema_version !== 1 || manifest.kind !== REQUIRED_KIND || manifest.authorized_copy !== true || manifest.real_userdata !== false ||
        !Array.isArray(manifest.contents) || !manifest.contents.includes('e2e-scenarios.json') ||
        scenarios?.schema_version !== 1 || !Array.isArray(scenarios.required_scenarios) || scenarios.required_scenarios.length < 8 ||
        !Array.isArray(scenarios.required_observations) || scenarios.required_observations.length < 4) {
      return blocked('electron_go_fixture_manifest_invalid');
    }
    return { ok: true, status: 'ready', code: 'electron_go_fixture_authorized', root, scenarios };
  } catch { return blocked('electron_go_fixture_manifest_invalid'); }
}

function resolveDriver(value) {
  if (!value) return blocked('electron_go_driver_missing');
  const driver = path.resolve(ROOT, value);
  const relative = path.relative(ROOT, driver);
  const info = fs.lstatSync(driver, { throwIfNoEntry: false });
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !info?.isFile() || info.isSymbolicLink() || info.size > MAX_RESULT_BYTES || path.extname(driver) !== '.js') {
    return blocked('electron_go_driver_invalid');
  }
  return { ok: true, driver };
}

function safeResult(value, scenarios) {
  if (!value || value.schema_version !== 1 || value.kind !== 'p15-electron-go-runtime-result' || value.status !== 'completed' ||
      value.active_database_owner !== 'go' || value.node_database_open_count !== 0 || value.node_database_write_count !== 0 ||
      !Number.isSafeInteger(value.go_database_write_count) || value.go_database_write_count < 1 || !Array.isArray(value.scenarios) || !value.observations || typeof value.observations !== 'object') {
    return blocked('electron_go_result_invalid');
  }
  const passed = new Set(value.scenarios.filter((scenario) => scenario?.status === 'passed').map((scenario) => scenario.id));
  const missingScenarios = scenarios.required_scenarios.filter((id) => !passed.has(id));
  const missingObservations = scenarios.required_observations.filter((key) => value.observations[key] !== true);
  if (missingScenarios.length || missingObservations.length) return blocked('electron_go_coverage_incomplete', {
    missing_scenarios: missingScenarios, missing_observations: missingObservations,
  });
  if (scanSensitiveText(JSON.stringify(value)).length) return blocked('electron_go_result_sensitive');
  return { ok: true, status: 'completed', code: 'electron_go_e2e_completed', scenario_count: passed.size };
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!['--fixture-root', '--driver'].includes(argv[index]) || !argv[index + 1]) throw new Error('arguments_invalid');
    if (argv[index] === '--fixture-root') options.fixtureRoot = argv[index + 1];
    if (argv[index] === '--driver') options.driver = argv[index + 1];
  }
  if (!options.fixtureRoot) throw new Error('arguments_invalid');
  return options;
}

function runElectronGoE2E(options = {}) {
  const fixture = validateFixture(path.resolve(options.fixtureRoot || ''));
  if (!fixture.ok) return fixture;
  const driver = resolveDriver(options.driver);
  if (!driver.ok) return driver;
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p15-electron-go-'));
  const resultFile = path.join(temporaryRoot, 'result.json');
  try {
    const safe = createSafeEnvironment(temporaryRoot, options.environment || process.env);
    Object.assign(safe.environment, {
      AUTOPLAN_P15_E2E: '1', AUTOPLAN_DATABASE_OWNER: 'go', AUTOPLAN_E2E_DATA_DIR: path.join(temporaryRoot, 'data'),
      AUTOPLAN_E2E_RESULT_FILE: resultFile,
    });
    fs.mkdirSync(safe.environment.AUTOPLAN_E2E_DATA_DIR, { recursive: true, mode: 0o700 });
    const command = spawnSync(process.execPath, [driver.driver, '--fixture-root', fixture.root, '--result-file', resultFile], {
      cwd: ROOT, env: safe.environment, encoding: 'utf8', windowsHide: true, shell: false, timeout: 20 * 60 * 1000,
    });
    const output = `${command.stdout || ''}\n${command.stderr || ''}`;
    if (command.error || command.status !== 0) return blocked('electron_go_driver_failed', { exit_code: Number.isInteger(command.status) ? command.status : null });
    if (scanSensitiveText(output).length) return blocked('electron_go_driver_output_sensitive');
    const info = fs.lstatSync(resultFile, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink() || info.size === 0 || info.size > MAX_RESULT_BYTES) return blocked('electron_go_result_missing');
    let result;
    try { result = JSON.parse(fs.readFileSync(resultFile, 'utf8')); } catch { return blocked('electron_go_result_invalid'); }
    return { ...safeResult(result, fixture.scenarios), result_sha256: sha256(fs.readFileSync(resultFile)) };
  } finally {
    fs.rmSync(temporaryRoot, { recursive: true, force: true, maxRetries: 1 });
  }
}

if (require.main === module) {
  try {
    const result = runElectronGoE2E(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"electron_go_e2e_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { FIXTURE_MARKER, ROOT, parseArgs, runElectronGoE2E, safeResult, validateFixture };
