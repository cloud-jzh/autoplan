'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { parseArgs, PLATFORM_BY_HOST, REQUIRED_PLATFORMS } = require('./verify');
const { parseArgs: parseE2EArgs, safeResult } = require('./run-electron-go-e2e');
const { parseArgs: parseInstallArgs, validateResult: validateInstallResult } = require('./run-install-upgrade');
const { parseArgs: parseSmokeArgs, validateResult: validateSmokeResult } = require('./smoke-package');

test('P15 verifier arguments require an explicit authorized fixture', () => {
  const platform = PLATFORM_BY_HOST[process.platform] || 'linux';
  const args = parseArgs(['verify', '--fixture-root', 'fixtures/migration/p15/electron-go', '--platform', platform]);
  assert.equal(args.fixtureRoot, 'fixtures/migration/p15/electron-go');
  assert.ok(REQUIRED_PLATFORMS.includes(args.platform));
  assert.throws(() => parseArgs(['verify']));
  const fixture = path.join('fixtures', 'migration', 'p15', 'electron-go');
  assert.deepEqual(parseE2EArgs(['--fixture-root', fixture]), { fixtureRoot: fixture });
  assert.deepEqual(parseInstallArgs(['--fixture-root', fixture]), { fixtureRoot: fixture });
  assert.throws(() => parseSmokeArgs(['--platform', 'windows']));
});

test('P15 runtime result contracts reject incomplete or unsafe evidence', () => {
  const e2e = safeResult({ schema_version: 1, kind: 'p15-electron-go-runtime-result', status: 'completed', active_database_owner: 'go', node_database_open_count: 0, node_database_write_count: 0, go_database_write_count: 1, scenarios: [], observations: {} }, { required_scenarios: ['core_crud'], required_observations: ['go_database_write_only'] });
  assert.equal(e2e.ok, false);
  const install = validateInstallResult({ schema_version: 1, kind: 'p15-install-upgrade-runtime-result', status: 'completed', real_userdata_touched: false, publish_attempted: false, second_writer_detected: false, integrity_check: 'ok', scenarios: [], observations: {} }, { required_scenarios: ['fresh_install'], required_observations: ['temporary_database_only'] });
  assert.equal(install.ok, false);
  const smoke = validateSmokeResult({ schema_version: 1, kind: 'p15-package-smoke-result', status: 'completed', platform: 'windows', real_userdata_touched: false, publish_attempted: false, sidecar_resource_verified: true, sidecar_executable: true, process_tree_cleaned: true, fresh_install: 'passed', launch: 'ready', trust_status: 'verified' }, 'windows', 'signed-notarized');
  assert.equal(smoke.ok, true);
});
