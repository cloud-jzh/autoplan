'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { GoDataClient, GoDataClientError, RUNTIME_COMMANDS } = require('./goDataClient');

function response(status, body) { return { ok: status >= 200 && status < 300, status, headers: { get: () => body?.request_id || null }, async json() { return body; } }; }

describe('P09 GoDataClient compatibility contract', () => {
  it('retains exact snake_case DTOs, stable ordering, and a committed snapshot across retried requests', async () => {
    const requests = [];
    let attempts = 0;
    const client = new GoDataClient({
      baseUrl: 'http://127.0.0.1:43123', retryAttempts: 1, retryDelayMs: 0,
      fetch: async (_url, init) => {
        attempts += 1;
        requests.push(JSON.parse(init.body));
        if (attempts === 1) return response(503, { error: { code: 'service_unavailable', retryable: true, request_id: init.headers['x-request-id'] } });
        return response(202, { data: { operation: { operation_id: 'p09-op-1', type: RUNTIME_COMMANDS.TASK_RUN_BATCHES, status: 'accepted', request_id: init.headers['x-request-id'], accepted_at: '2026-07-11T04:45:56Z' }, snapshot: { activeProjectId: 7, projects: [{ id: 7 }], tasks: [{ id: 3, plan_id: 2 }, { id: 4, plan_id: 2 }] } } });
      },
    });
    const result = await client.runTaskBatches(7, 2, [{ taskIds: [3, 4] }], { requestId: 'p09-request', idempotencyKey: 'p09-intent' });
    assert.equal(attempts, 2);
    assert.deepEqual(requests[0], { version: 'v1', command: 'task.run_batches', project_id: 7, plan_id: 2, batches: [{ task_ids: [3, 4] }] });
    assert.deepEqual(requests[0], requests[1]);
    assert.deepEqual(client.snapshot(7), result.snapshot);
    assert.equal(typeof client.run, 'undefined');
    assert.equal(typeof client.export, 'undefined');
  });

  it('keeps cancellation and duplicate task IDs fail-closed before a Node fallback is possible', async () => {
    let calls = 0;
    const client = new GoDataClient({ baseUrl: 'http://127.0.0.1:43123', fetch: async () => { calls += 1; return response(500, {}); } });
    await assert.rejects(client.runTaskBatches(1, 2, [{ taskIds: [3, 3] }]), (error) => error instanceof GoDataClientError && error.code === 'invalid_runtime_command');
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(client.startLoop(1, { signal: controller.signal }), (error) => error instanceof GoDataClientError && error.code === 'operation_cancelled');
    assert.equal(calls, 1);
  });
});
