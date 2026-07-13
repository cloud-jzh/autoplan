'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const fixtureRoot = path.resolve(__dirname, '..', '..', '..', 'fixtures', 'migration', 'p15', 'electron-go');

test('P15 Electron-Go fixture remains synthetic and complete', () => {
  const manifest = JSON.parse(fs.readFileSync(path.join(fixtureRoot, 'fixture-manifest.json'), 'utf8'));
  const scenarios = JSON.parse(fs.readFileSync(path.join(fixtureRoot, 'e2e-scenarios.json'), 'utf8'));
  const install = JSON.parse(fs.readFileSync(path.join(fixtureRoot, 'install-upgrade-scenarios.json'), 'utf8'));
  assert.equal(manifest.authorized_copy, true);
  assert.equal(manifest.real_userdata, false);
  assert.ok(scenarios.required_scenarios.includes('single_writer_lock'));
  assert.ok(scenarios.required_scenarios.includes('terminal_websocket_backpressure'));
  assert.ok(install.required_scenarios.includes('migration_interrupt_recovery'));
});
