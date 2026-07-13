'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { GoRuntimeAdapter } = require('./goRuntimeAdapter');

describe('P09 loop runtime through GoDataClient', () => {
  it('preserves plan/task state and infers snake_case plan ownership from Go snapshots', async () => {
    const calls = [];
    const snapshot = { activeProjectId: 4, projects: [{ id: 4, state: { phase: 'idle' } }], tasks: [{ id: 9, plan_id: 3 }], plans: [{ id: 3, sort_order: 0 }], events: [] };
    const client = fakeClient(calls, snapshot);
    const adapter = new GoRuntimeAdapter(client);
    const updates = [];
    adapter.on('update', (next) => updates.push(next));
    await adapter.start(4);
    await adapter.runTask(4, 9);
    await adapter.stopTask(4, 9, { plan_id: 3 });
    assert.deepEqual(calls, [['startLoop', 4], ['runTask', 4, 3, 9], ['stopTask', 4, 3, 9]]);
    assert.equal(adapter.project(4).id, 4);
    assert.equal(adapter.status(4).phase, 'idle');
    assert.equal(updates.length, 3);
    assert.equal(typeof client.run, 'undefined');
  });
});

function fakeClient(calls, snapshot) {
  const result = () => Promise.resolve({ snapshot });
  return {
    snapshot: () => snapshot,
    startLoop: (projectId) => { calls.push(['startLoop', projectId]); return result(); },
    runTask: (projectId, planId, taskId) => { calls.push(['runTask', projectId, planId, taskId]); return result(); },
    stopTask: (projectId, planId, taskId) => { calls.push(['stopTask', projectId, planId, taskId]); return result(); },
  };
}
