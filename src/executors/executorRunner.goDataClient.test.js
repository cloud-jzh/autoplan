'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { GoDataClient } = require('../data/goDataClient');

describe('P09 executor metadata compatibility through GoDataClient', () => {
  it('uses typed Go run/action/stop commands and preserves executor snapshot ordering', async () => {
    const commands = [];
    const client = new GoDataClient({
      baseUrl: 'http://127.0.0.1:43123',
      fetch: async (_url, init) => {
        const command = JSON.parse(init.body);
        commands.push(command);
        return accepted(command, init, { activeProjectId: 6, executors: [{ id: 19, project_id: 6, sort_order: 0, status: command.command }] });
      },
    });
    await client.runExecutor(6, 19, metadata('run'));
    await client.runExecutorAction(6, 19, 'reload', metadata('action'));
    const stopped = await client.stopExecutor(6, 19, metadata('stop'));
    assert.deepEqual(commands, [
      { version: 'v1', command: 'executor.run', project_id: 6, executor_id: 19 },
      { version: 'v1', command: 'executor.action', project_id: 6, executor_id: 19, action: 'reload' },
      { version: 'v1', command: 'executor.stop', project_id: 6, executor_id: 19 },
    ]);
    assert.equal(stopped.snapshot.executors[0].id, 19);
    assert.equal(typeof client.spawn, 'undefined');
    assert.equal(typeof client.db, 'undefined');
  });
});

function accepted(command, init, snapshot) { return { ok: true, status: 202, headers: { get: () => null }, async json() { return { data: { operation: { operation_id: `executor-${command.command}`, type: command.command, status: 'accepted', request_id: init.headers['x-request-id'], accepted_at: '2026-07-11T04:45:56Z' }, snapshot } }; } }; }
function metadata(name) { return { requestId: `p09-executor-${name}`, idempotencyKey: `p09-executor-intent-${name}` }; }
