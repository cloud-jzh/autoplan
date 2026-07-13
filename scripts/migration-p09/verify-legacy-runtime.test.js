'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { generateScaleCopy } = require('./generate-scale-copy');
const { REQUIRED_COMMANDS, assertNoNodeDatabaseSurface, createFixtureGoBridge, verifyLegacyRuntime } = require('./verify-legacy-runtime');

describe('P09 legacy runtime compatibility verifier', () => {
  it('replays typed legacy runtime families through the Go bridge with one owner', async () => {
    const { copy } = generateScaleCopy({ seed: 'p09-runtime-contract', scale: compactScale() });
    const result = await verifyLegacyRuntime(copy);
    assert.equal(result.status, 'completed');
    assert.deepEqual(result.command_types, REQUIRED_COMMANDS);
    assert.deepEqual(result.owner, { owner: 'go', node_sql_attempt: 'node_sql_blocked', second_go_attempt: 'go_owner_locked', writer_count: 1 });
    assert.match(result.snapshot_sha256, /^[a-f0-9]{64}$/);
  });

  it('exposes no Node database method while the fixture bridge owns writes', () => {
    const bridge = createFixtureGoBridge(generateScaleCopy({ scale: compactScale() }).copy);
    assert.doesNotThrow(() => assertNoNodeDatabaseSurface({ snapshot() {}, startLoop() {} }));
    assert.equal(bridge.ownerEvidence().writer_count, 1);
    assert.equal(bridge.attemptNodeSQLWrite().code, 'node_sql_blocked');
    assert.equal(bridge.attemptSecondGoOwner().code, 'go_owner_locked');
  });
});

function compactScale() {
  return { projects: 2, plansPerProject: 2, tasksPerPlan: 3, eventsPerProject: 8, messagesPerProject: 6, scriptsPerProject: 2, executorsPerProject: 2 };
}
