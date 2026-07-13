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

function expect(condition: unknown, message: string) {
  if (!condition) throw new Error(message);
}

function expectIncludes(value: string, snippet: string, message: string) {
  expect(value.includes(snippet), message);
}

function expectNotIncludes(value: string, snippet: string, message: string) {
  expect(!value.includes(snippet), message);
}

describe('P14 terminal transport contract', () => {
  it('keeps the feature atomic and default-off across HTTP and renderer handoff', () => {
    const http = source('src', 'renderer', 'lib', 'api', 'httpClient.ts');
    const client = source('src', 'renderer', 'lib', 'api', 'client.ts');
    const main = source('src', 'main.js');

    expectIncludes(client, "export const TERMINAL_RUNTIME_FEATURE = 'go_terminal_api' as const;", 'terminal feature key must be canonical');
    expectIncludes(client, 'TERMINAL_RUNTIME_FEATURE,', 'terminal feature must join the runtime document');
    expectIncludes(http, 'go_terminal_api: false,', 'terminal feature must default closed');
    expectIncludes(http, "enabled: this.#runtimeFeatureEnabled('go_terminal_api'),", 'one flag must select the terminal transport');
    expectIncludes(main, "go_terminal_api: 'AUTOPLAN_SIDECAR_GO_TERMINAL_API',", 'main handoff must parse the terminal flag');
  });

  it('routes each known terminal session by its creation runtime without mutation fallback', () => {
    const transport = source('src', 'renderer', 'lib', 'api', 'terminalTransport.ts');

    expectIncludes(transport, "runtime: copied.runtime === 'go' ? 'go' : 'node'", 'session runtime must be retained in the ownership map');
    expectIncludes(transport, "if (!this.#enabled) return this.#legacy.createTerminal(input);", 'only future session creation may fall back when the flag is closed');
    expectIncludes(transport, 'Rollback changes only future admission.', 'rollback must preserve Go session discovery');
    expectIncludes(transport, "if (!owner) return terminalFailure('terminal_session_not_found');", 'unknown session IDs must not be guessed');
    expectIncludes(transport, "if (owner.runtime === 'node') return this.#legacy.closeTerminal(input);", 'legacy sessions must retain IPC close ownership');
    expectIncludes(transport, 'this.#control!.close(owner.projectId, owner.session.id)', 'Go sessions must stay on Go control routes');
    expectNotIncludes(transport, 'catch (error) { return this.#legacy', 'Go mutation failures must not fall back to legacy IPC');
  });

  it('keeps raw terminal output in the private socket rather than project SSE', () => {
    const socket = source('src', 'renderer', 'lib', 'api', 'terminalSocket.ts');
    const transport = source('src', 'renderer', 'lib', 'api', 'terminalTransport.ts');

    expectIncludes(socket, '/api/v1/terminals/${encodeURIComponent(sessionId)}/ws', 'socket must use the frozen terminal endpoint');
    expectIncludes(socket, '?project_id=${projectId}&last_seq=${lastSeq}', 'reconnect must carry only scope and sequence cursor');
    expectNotIncludes(socket, 'sessionCredential', 'socket URL must not contain session material');
    expectNotIncludes(socket, 'EventSource', 'terminal output must not use project SSE');
    expectIncludes(socket, 'this.#lastSeq = message.seq;', 'socket must retain applied sequence');
    expectIncludes(socket, 'Math.min(MAXIMUM_RETRY_DELAY_MS, this.#retryBaseMs * 2 **', 'reconnect must use bounded exponential delay');
    expectNotIncludes(socket, 'this.#attempt = 0;', 'a successful but immediately closed socket must not restart the retry budget');
    expectIncludes(transport, 'const socket = new TerminalSocket({', 'transport must own socket construction');
  });

  it('bounds client recovery and keeps terminal credentials at the privileged boundary', () => {
    const socket = source('src', 'renderer', 'lib', 'api', 'terminalSocket.ts');
    const main = source('src', 'main.js');

    expectIncludes(socket, 'const MAXIMUM_FRAME_BYTES = 64 << 10;', 'terminal frames must have a fixed client-side ceiling');
    expectIncludes(socket, 'if (!retryable(reason) || this.#attempt >= this.#retryLimit)', 'retry attempts must stop at a fixed limit');
    expectIncludes(socket, 'socket.close(1002)', 'malformed server frames must close with a protocol error');
    expectIncludes(socket, 'if (message.seq <= this.#lastSeq) return;', 'duplicate output sequences must be ignored');
    expectIncludes(main, "request.onBeforeSendHeaders({ urls: ['ws://127.0.0.1/*'] }", 'only Electron main may add the terminal WebSocket header');
    expectIncludes(main, "requestHeaders['X-Autoplan-Session'] = client.sessionToken;", 'terminal credential handoff must remain header-only');
    expectIncludes(main, 'isTerminalWebSocketRequest(details?.url, client.baseUrl)', 'header injection must validate the exact terminal route');
  });
});
