export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function source(...parts: string[]) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

function expect(value: unknown, message: string) {
  if (!value) throw new Error(message);
}

describe('P11 runtime transport owner contract', () => {
  const client = source('src', 'renderer', 'lib', 'api', 'client.ts');
  const http = source('src', 'renderer', 'lib', 'api', 'httpClient.ts');
  const transport = source('src', 'renderer', 'lib', 'api', 'transport.ts');
  const nodeBridge = source('src', 'data', 'goDataClient.js');
  const ownerGuard = source('src', 'data', 'databaseOwnerGuard.js');

  it('keeps every runtime family independently configurable and fail-closed', () => {
    for (const feature of [
      'go_loop_actions', 'go_plan_actions', 'go_task_actions',
      'go_acceptance_retry_actions', 'go_agent_cli_runtime',
    ]) {
      expect(client.includes(`'${feature}'`), `missing ${feature} gate`);
    }
    expect(http.includes('normalizeRuntimeFeatures'), 'runtime feature input must be validated');
    expect(http.includes("throw new HttpClientError('runtime_feature_disabled')"), 'disabled Go route must fail closed');
    expect(http.includes("this.#runtimeFeatureEnabled('go_agent_cli_runtime')"), 'Agent CLI owner gate is missing');
  });

  it('pins successful HTTP submissions to Go and follows their Operation SSE stream', () => {
    expect(http.includes("this.#operationOwners.set(accepted.operation_id, 'go')"), 'accepted owner is not pinned');
    expect(http.includes('this.#followRuntimeOperation(projectId, accepted)'), 'accepted operation lacks SSE follow-up');
    expect(http.includes('this.connectOperationEvents(projectId, accepted.operation_id'), 'Operation SSE is not reused');
    expect(http.includes('isTerminalOperationEvent(delivery.event)'), 'terminal stream cleanup is missing');
  });

  it('never chooses IPC after an HTTP Runtime request starts', () => {
    const start = http.indexOf('async #submitRuntimeAction');
    const runtimeSubmission = http.slice(start, http.indexOf('async #submitAcceptanceAction', start));
    expect(!runtimeSubmission.includes('this.#delegate.'), 'HTTP runtime submission must not fall back to IPC');
    expect(!runtimeSubmission.includes('retryTransportFailure: true'), 'HTTP runtime mutation must not be replayed');
    expect(transport.includes('runtimeAvailable || (!production'), 'production HTTP requires the supervised runtime handoff');
  });

  it('keeps Node compatibility access behind GoDataClient and blocks sql.js ownership', () => {
    expect(nodeBridge.includes('operationOwner(operationId)'), 'Node bridge must retain its Go operation owner');
    expect(nodeBridge.includes('never falls back to IPC or sql.js'), 'Node bridge fallback boundary drifted');
    expect(ownerGuard.includes('NODE_SQL_FORBIDDEN'), 'Go database owner must reject Node SQL');
  });

  it('keeps each Runtime REST family independently mapped to the shared client surface', () => {
    for (const route of [
      '/loop/actions/start', '/loop/actions/stop', '/loop/actions/run-once',
      '/plans/${planId}/actions/stop', '/plans/${planId}/actions/resume',
      '/plans/${planId}/actions/re-execute', '/plans/${planId}/actions/recreate',
      '/tasks/${taskId}/actions/run', '/tasks/${taskId}/actions/stop',
      '/actions/run-batches', '/acceptance/actions/accept', '/acceptance/actions/redo',
      '/actions/retry-plan-generation',
    ]) {
      expect(http.includes(route), `missing Runtime REST route fragment ${route}`);
    }
    expect(http.includes("Promise.reject(new HttpClientError('not_implemented'))"),
      'unimplemented Go Agent CLI action must fail closed rather than run in Node');
  });
});
