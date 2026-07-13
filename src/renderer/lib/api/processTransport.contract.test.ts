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

describe('P12 process transport contract', () => {
  const client = source('src', 'renderer', 'lib', 'api', 'client.ts');
  const http = source('src', 'renderer', 'lib', 'api', 'httpClient.ts');
  const view = source('src', 'renderer', 'components', 'workspace', 'WorkspaceExecutorsView.tsx');
  const bridge = source('src', 'data', 'goDataClient.js');

  it('keeps Script and Executor ownership gates independent and fail-closed', () => {
    expect(client.includes("'go_scripts_api'"), 'missing Script runtime gate');
    expect(client.includes("'go_executors_api'"), 'missing Executor runtime gate');
    expect(http.includes("this.#runtimeFeatureEnabled('go_scripts_api')"), 'Script route is not gated');
    expect(http.includes("this.#runtimeFeatureEnabled('go_executors_api')"), 'Executor route is not gated');
    expect(http.includes('return DEFAULT_RUNTIME_FEATURES'), 'invalid feature handoff is not closed');
  });

  it('submits only fixed resource action paths and follows the accepted Operation', () => {
    for (const path of [
      '/scripts/${scriptId}/actions/run', '/scripts/${scriptId}/actions/stop',
      '/executors/${executorId}/actions/run', '/executors/${executorId}/actions/stop',
      '/executors/${executorId}/actions/${input.action}',
    ]) expect(http.includes(path), `missing process action route ${path}`);
    expect(http.includes('this.#operationOwners.set(operation.operation_id, \'go\')'), 'Go process owner is not pinned');
    expect(http.includes('this.#followRuntimeOperation(projectId, operation)'), 'accepted process operation is not followed through SSE');
    expect(!http.slice(http.indexOf('async #submitProcessAction'), http.indexOf('async #submitProcessStop')).includes('this.#delegate.'),
      'an accepted HTTP process action must not fall back to IPC');
  });

  it('keeps the Go-owned compatibility bridge and workspace view off direct process access', () => {
    expect(bridge.includes("'scripts', 'run'"), 'GoDataClient does not use the fixed Script route');
    expect(bridge.includes("'executors', 'run'"), 'GoDataClient does not use the fixed Executor route');
    expect(bridge.includes('No SQL, command, cwd, env, PID'), 'GoDataClient process boundary is undocumented');
    expect(!view.includes('window.autoplan.runExecutor'), 'Workspace executor run bypasses AutoplanClient');
    expect(!view.includes('window.autoplan.stopExecutor'), 'Workspace executor stop bypasses AutoplanClient');
    expect(!view.includes('window.autoplan.runExecutorAction'), 'Workspace executor action bypasses AutoplanClient');
  });

  it('keeps process SSE payloads bounded and free of process configuration fields', () => {
    const events = source('src', 'renderer', 'lib', 'api', 'events.ts');
    const stream = source('src', 'renderer', 'lib', 'api', 'eventStream.ts');
    expect(events.includes('FORBIDDEN_PAYLOAD_KEY'), 'SSE payload denylist is missing');
    for (const field of ['command', 'stdout', 'stderr', 'token', 'workspace_path', 'cwd']) {
      expect(events.includes(field), `SSE payload rule does not reject ${field}`);
    }
    expect(stream.includes('MAXIMUM_FRAME_BYTES'), 'SSE frame size is unbounded');
    expect(stream.includes("requireResync('invalid_event')"), 'unsafe SSE frame does not force resync');
  });
});
