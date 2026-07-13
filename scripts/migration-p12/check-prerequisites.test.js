'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');
const { P12_TEST_SOURCES, checkPrerequisites, inspectTestPolicy, parseArgs } = require('./check-prerequisites');

const root = path.resolve(__dirname, '..', '..');

test('P12 prerequisite checker requires all scoped test sources without filters', () => {
  assert.equal(P12_TEST_SOURCES.length, 9);
  assert.equal(inspectTestPolicy(root).ok, true);
  assert.deepEqual(parseArgs(['--fixture-root', 'fixture']), { fixtureRoot: 'fixture' });
  assert.throws(() => parseArgs([]));
});

test('P12 prerequisite result stays structured when prior evidence is absent', () => {
  const result = checkPrerequisites({ rootDir: root, fixtureRoot: path.join(root, 'fixtures', 'migration', 'p12', 'process-fixtures') });
  assert.equal(typeof result.status, 'string');
  assert.equal(Array.isArray(result.failures), true);
  assert.equal(result.fixture.ok, true);
});
