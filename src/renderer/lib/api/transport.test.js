const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const vm = require('node:vm');
const ts = require('typescript');

function source(...parts) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

class StubHttpError extends Error {
  constructor(code) {
    super(code);
    this.code = code;
  }
}

class StubHttpClient {
  constructor(options) {
    this.options = options;
  }
}

class StubIpcClient {}

function loadTransport(environment = {}, runtimeConfig) {
  const runtimeKey = '__AUTOPLAN_HTTP_RUNTIME__';
  const originalEnvironment = globalThis.__viteEnvironment;
  const originalRuntime = globalThis[runtimeKey];
  globalThis.__viteEnvironment = environment;
  if (runtimeConfig !== undefined) globalThis[runtimeKey] = runtimeConfig;
  else delete globalThis[runtimeKey];
  try {
    const transportSource = source('src', 'renderer', 'lib', 'api', 'transport.ts')
      .replaceAll('import.meta.env', 'globalThis.__viteEnvironment');
    const compiled = ts.transpileModule(transportSource, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
      fileName: 'transport.ts',
    }).outputText;
    const module = { exports: {} };
    const wrapper = vm.runInThisContext(`(function (require, module, exports) { ${compiled}\n})`);
    wrapper(
      (id) => {
        if (id === './httpClient') {
          return {
            AUTOPLAN_HTTP_RUNTIME_CONFIG_KEY: runtimeKey,
            HttpAutoplanClient: StubHttpClient,
            HttpClientError: StubHttpError,
          };
        }
        if (id === './ipcClient') return { IpcAutoplanClient: StubIpcClient };
        throw new Error(`unexpected runtime import: ${id}`);
      },
      module,
      module.exports,
    );
    return { exports: module.exports, runtimeValue: globalThis[runtimeKey] };
  } finally {
    if (originalEnvironment === undefined) delete globalThis.__viteEnvironment;
    else globalThis.__viteEnvironment = originalEnvironment;
    if (originalRuntime === undefined) delete globalThis[runtimeKey];
    else globalThis[runtimeKey] = originalRuntime;
  }
}

describe('single AutoPlan transport feature flag', () => {
  it('defaults missing, empty, invalid, explicit IPC, and all production values to IPC', () => {
    const { resolveAutoplanTransport } = loadTransport().exports;
    for (const requested of [undefined, null, '', 'ipc', ' IPC ']) {
      const result = resolveAutoplanTransport(requested, false);
      assert.equal(result.transport, 'ipc');
      assert.equal(result.fellBackToIpc, false);
    }
    for (const requested of ['rest', 'go', 'disabled', 'http ']) {
      const result = resolveAutoplanTransport(requested, true);
      assert.equal(result.transport, 'ipc');
      assert.equal(result.production, true);
    }
    assert.deepStrictEqual(resolveAutoplanTransport('unknown', false), {
      requestedTransport: 'unknown',
      transport: 'ipc',
      fellBackToIpc: true,
      production: false,
    });
  });

  it('selects HTTP only for an exact explicit non-production request', () => {
    const { resolveAutoplanTransport } = loadTransport().exports;
    assert.deepStrictEqual(resolveAutoplanTransport(' HTTP ', false), {
      requestedTransport: ' HTTP ',
      transport: 'http',
      fellBackToIpc: false,
      production: false,
    });
    assert.equal(resolveAutoplanTransport('http', true).transport, 'ipc');
  });

  it('creates a hybrid only with complete runtime input and never silently falls back', () => {
    const { createAutoplanClient, resolveAutoplanTransport } = loadTransport().exports;
    const ipcClient = { transport: 'ipc' };
    const ipcConfig = resolveAutoplanTransport(undefined, false);
    assert.strictEqual(createAutoplanClient({ config: ipcConfig, ipcClient }), ipcClient);

    const httpConfig = resolveAutoplanTransport('http', false);
    assert.throws(
      () => createAutoplanClient({ config: httpConfig, ipcClient }),
      (error) => error.code === 'http_configuration_invalid',
    );
    const runtime = { baseUrl: 'http://127.0.0.1:43123', sessionCredential: 'A'.repeat(43) };
    const hybrid = createAutoplanClient({ config: httpConfig, ipcClient, httpRuntime: runtime });
    assert.ok(hybrid instanceof StubHttpClient);
    assert.strictEqual(hybrid.options.delegate, ipcClient);
  });

  it('consumes the ephemeral runtime handoff once in explicit HTTP mode', () => {
    const runtime = { baseUrl: 'http://127.0.0.1:43123', sessionCredential: 'A'.repeat(43) };
    const loaded = loadTransport({ VITE_AUTOPLAN_TRANSPORT: 'http', PROD: false }, runtime);
    assert.ok(loaded.exports.getAutoplanClient() instanceof StubHttpClient);
    assert.equal(loaded.runtimeValue, undefined);
  });

  it('keeps Provider transport-neutral and prevents persistent credential sources', () => {
    const provider = source('src', 'renderer', 'lib', 'api', 'provider.tsx');
    const transport = source('src', 'renderer', 'lib', 'api', 'transport.ts');
    assert.match(provider, /client\?: AutoplanClient/);
    assert.match(provider, /client = defaultClient/);
    assert.doesNotMatch(provider, /HttpAutoplanClient|IpcAutoplanClient/);
    assert.doesNotMatch(transport, /localStorage|sessionStorage|document\.cookie/);
  });
});
