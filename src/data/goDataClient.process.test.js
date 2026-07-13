'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { GoDataClient, GoDataClientError, RUNTIME_COMMANDS } = require('./goDataClient');

function jsonResponse(body) {
  return { ok: true, status: 200, headers: { get: () => null }, json: async () => body };
}

test('P12 GoDataClient uses a fixed Script action path and empty body', async () => {
  const calls = [];
  const client = new GoDataClient({
    baseUrl: 'http://127.0.0.1:4312',
    fetch: async (url, options) => {
      calls.push({ url, options });
      if (options.method === 'GET') return jsonResponse({ request_id: 'p12-request', data: { projectId: 7 } });
      return jsonResponse({ request_id: 'p12-request', data: { operation_id: 'operation-p12', type: 'script.run', status: 'queued', request_id: 'p12-request', accepted_at: '2026-07-12T09:00:00Z' } });
    },
  });
  const result = await client.runScript(7, 11, { requestId: 'p12-request', idempotencyKey: 'p12-key' });
  assert.equal(result.operation.operation_id, 'operation-p12');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, 'http://127.0.0.1:4312/api/v1/projects/7/scripts/11/actions/run');
  assert.equal(calls[0].options.body, '{}');
  assert.equal(calls[0].options.headers['idempotency-key'], 'p12-key');
  assert.equal(calls[1].url, 'http://127.0.0.1:4312/api/v1/projects/7/snapshot');
  assert.equal(calls[1].options.method, 'GET');
});

test('P12 GoDataClient closes generic and malformed process command paths', async () => {
  const client = new GoDataClient({ baseUrl: 'http://127.0.0.1:4312', fetch: async () => { throw new Error('must not fetch'); } });
  await assert.rejects(client.executeRuntimeCommand(RUNTIME_COMMANDS.SCRIPT_RUN, { projectId: 7, scriptId: 11 }), (error) => error instanceof GoDataClientError && error.code === 'invalid_runtime_command');
  await assert.rejects(client.runExecutor(7, 'not-an-id'), (error) => error instanceof GoDataClientError && error.code === 'invalid_runtime_command');
  await assert.rejects(client.runExecutorAction(7, 21, 'shell'), (error) => error instanceof GoDataClientError && error.code === 'invalid_runtime_command');
});
