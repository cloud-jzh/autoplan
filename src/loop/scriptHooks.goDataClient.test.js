'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { GoDataClient } = require('../data/goDataClient');

describe('P09 script hook compatibility through GoDataClient', () => {
  it('delegates manual run and stop to Go without invoking shell hooks or local persistence', async () => {
    const commands = [];
    const client = new GoDataClient({
      baseUrl: 'http://127.0.0.1:43123',
      fetch: async (_url, init) => {
        const command = JSON.parse(init.body);
        commands.push(command);
        return accepted(command, init, { activeProjectId: 3, scripts: [{ id: 12, project_id: 3, status: command.command === 'script.stop' ? 'stopped' : 'running' }] });
      },
    });
    await client.runScript(3, 12, metadata('run'));
    const stopped = await client.stopScript(3, 12, metadata('stop'));
    assert.deepEqual(commands, [
      { version: 'v1', command: 'script.run', project_id: 3, script_id: 12 },
      { version: 'v1', command: 'script.stop', project_id: 3, script_id: 12 },
    ]);
    assert.equal(stopped.snapshot.scripts[0].status, 'stopped');
    assert.equal(typeof client.runShell, 'undefined');
    assert.equal(typeof client.export, 'undefined');
  });
});

function accepted(command, init, snapshot) { return { ok: true, status: 202, headers: { get: () => null }, async json() { return { data: { operation: { operation_id: `script-${command.command}`, type: command.command, status: 'accepted', request_id: init.headers['x-request-id'], accepted_at: '2026-07-11T04:45:56Z' }, snapshot } }; } }; }
function metadata(name) { return { requestId: `p09-script-${name}`, idempotencyKey: `p09-script-intent-${name}` }; }
