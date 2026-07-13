const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const vm = require('node:vm');
const ts = require('typescript');

const root = process.cwd();

function source(...parts) {
  return readFileSync(join(root, ...parts), 'utf8');
}

function tupleKeys(sourceText, exportName) {
  const pattern = new RegExp(`export const ${exportName} = \\[([\\s\\S]*?)\\] as const`);
  const match = sourceText.match(pattern);
  assert.ok(match, `missing ${exportName}`);
  return [...match[1].matchAll(/'([^']+)'/g)].map((item) => item[1]);
}

const operationKeys = tupleKeys(
  source('src', 'renderer', 'lib', 'api', 'client.ts'),
  'AUTOPLAN_CLIENT_OPERATION_KEYS',
);
const eventKeys = tupleKeys(
  source('src', 'renderer', 'lib', 'api', 'events.ts'),
  'AUTOPLAN_CLIENT_EVENT_KEYS',
);
const capabilityMatrix = JSON.parse(
  source('docs', 'migration', 'p00', 'capability-matrix.json'),
);

function loadIpcClient() {
  const compiled = ts.transpileModule(
    source('src', 'renderer', 'lib', 'api', 'ipcClient.ts'),
    {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
      },
      fileName: 'ipcClient.ts',
    },
  ).outputText;
  const module = { exports: {} };
  const wrapper = vm.runInThisContext(`(function (require, module, exports) { ${compiled}\n})`);
  wrapper(
    (id) => {
      if (id === './client') return { AUTOPLAN_CLIENT_OPERATION_KEYS: operationKeys };
      throw new Error(`unexpected runtime import: ${id}`);
    },
    module,
    module.exports,
  );
  return module.exports.IpcAutoplanClient;
}

function createApi(overrides = {}) {
  const calls = [];
  const listeners = new Map();
  const releases = new Map();
  const api = {};

  for (const key of operationKeys) {
    if (key === 'mcpToolNames') {
      api[key] = ['list_projects', 'get_project'];
    } else if (key === 'fileAccess') {
      api[key] = {
        get: (...args) => {
          calls.push({ key: 'fileAccess.get', args });
          return overrides['fileAccess.get']?.(...args) ?? { operation: 'fileAccess.get' };
        },
        save: (...args) => {
          calls.push({ key: 'fileAccess.save', args });
          return overrides['fileAccess.save']?.(...args) ?? { operation: 'fileAccess.save' };
        },
      };
    } else {
      api[key] = (...args) => {
        calls.push({ key, args });
        return overrides[key]?.(...args) ?? { operation: key };
      };
    }
  }

  for (const key of eventKeys) {
    listeners.set(key, []);
    releases.set(key, []);
    api[key] = (handler) => {
      if (overrides[key]) return overrides[key](handler);
      listeners.get(key).push(handler);
      let releaseCount = 0;
      const release = () => {
        releaseCount += 1;
        const handlers = listeners.get(key);
        const index = handlers.indexOf(handler);
        if (index >= 0) handlers.splice(index, 1);
      };
      releases.get(key).push(() => releaseCount);
      return release;
    };
  }

  return {
    api: Object.assign(api, overrides.api),
    calls,
    emit(key, event) {
      for (const handler of [...listeners.get(key)]) handler(event);
    },
    listenerCount(key) {
      return listeners.get(key).length;
    },
    releaseCounts(key) {
      return releases.get(key).map((read) => read());
    },
  };
}

describe('IpcAutoplanClient controlled capability coverage', () => {
  it('matches every P00 go-api operation and event without desktop capabilities', () => {
    const expectedOperations = [...new Set(
      capabilityMatrix.capabilities
        .filter((item) => item.owner === 'go-api')
        .map((item) => item.api || item.preload)
        .map((key) => key.split('.')[0]),
    )].sort();
    const expectedEvents = capabilityMatrix.events
      .filter((item) => item.owner === 'go-api')
      .map((item) => item.api)
      .sort();

    assert.deepStrictEqual([...operationKeys].sort(), expectedOperations);
    assert.deepStrictEqual([...eventKeys].sort(), expectedEvents);
    assert.ok(capabilityMatrix.capabilities.every((item) => item.owner));
    assert.ok(capabilityMatrix.events.every((item) => item.owner));
  });
});

describe('IpcAutoplanClient operation mapping', () => {
  it('forwards every P00 business operation without changing arguments or results', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi();
    const client = new IpcAutoplanClient(fixture.api);

    assert.equal(client.api, undefined, 'the complete preload object must not be publicly reachable');
    for (const desktopKey of ['pickDirectory', 'openExternal', 'updateStatus', 'getDroppedFilePath']) {
      assert.equal(client[desktopKey], undefined, `${desktopKey} must remain in DesktopBridge`);
    }
    assert.strictEqual(client.mcpToolNames, fixture.api.mcpToolNames);
    for (const key of operationKeys.filter((item) => item !== 'mcpToolNames' && item !== 'fileAccess')) {
      const input = { operation: key };
      const expected = fixture.api[key](input);
      fixture.calls.pop();
      assert.deepStrictEqual(client[key](input), expected, `${key} should return the preload result`);
      const call = fixture.calls.pop();
      assert.equal(call.key, key);
      assert.strictEqual(call.args[0], input, `${key} should preserve input identity`);
    }

    const saveInput = { scope: 'project' };
    assert.deepStrictEqual(client.fileAccess.get(), { operation: 'fileAccess.get' });
    assert.deepStrictEqual(client.fileAccess.save(saveInput), { operation: 'fileAccess.save' });
    assert.strictEqual(fixture.calls.at(-1).args[0], saveInput);
  });

  it('preserves synchronous throws and rejected Promise identity', async () => {
    const IpcAutoplanClient = loadIpcClient();
    const syncError = new Error('sync failure');
    const asyncError = new Error('async failure');
    const rejected = Promise.reject(asyncError);
    rejected.catch(() => {});
    const fixture = createApi({
      createProject: () => { throw syncError; },
      snapshot: () => rejected,
    });
    const client = new IpcAutoplanClient(fixture.api);

    assert.throws(() => client.createProject({}), (error) => error === syncError);
    const result = client.snapshot(1);
    assert.strictEqual(result, rejected);
    await assert.rejects(result, (error) => error === asyncError);
  });

  it('fails closed when a required operation is absent or malformed', () => {
    const IpcAutoplanClient = loadIpcClient();
    const missing = createApi();
    delete missing.api.runTask;
    assert.throws(() => new IpcAutoplanClient(missing.api), /runTask must be a function/);

    const badFileAccess = createApi();
    badFileAccess.api.fileAccess = { get() {} };
    assert.throws(() => new IpcAutoplanClient(badFileAccess.api), /fileAccess must expose get and save/);
  });
});

describe('IpcAutoplanClient event lifecycle', () => {
  it('maps every declared business event and releases only its own listener', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi();
    const client = new IpcAutoplanClient(fixture.api);

    for (const key of eventKeys) {
      const event = { key };
      const received = [];
      const unsubscribe = client[key]((payload) => received.push(payload));
      assert.equal(fixture.listenerCount(key), 1, `${key} should subscribe once`);
      fixture.emit(key, event);
      assert.deepStrictEqual(received, [event], `${key} should preserve its payload`);
      unsubscribe();
      assert.equal(fixture.listenerCount(key), 0, `${key} should release its own listener`);
      assert.deepStrictEqual(fixture.releaseCounts(key), [1]);
    }
  });

  it('preserves event arrival order and callback payload identity', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi();
    const client = new IpcAutoplanClient(fixture.api);
    const received = [];
    const chunk = { type: 'text_delta', data: { content: 'a' } };
    const done = { status: 'done', conversationId: 7 };

    client.onChatChunk((event) => received.push(event));
    client.onChatDone((event) => received.push(event));
    fixture.emit('onChatChunk', chunk);
    fixture.emit('onChatDone', done);
    assert.deepStrictEqual(received, [chunk, done]);
    assert.strictEqual(received[0], chunk);
    assert.strictEqual(received[1], done);
  });

  it('deduplicates repeated handlers while keeping independent idempotent ownership', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi();
    const client = new IpcAutoplanClient(fixture.api);
    let calls = 0;
    const handler = () => { calls += 1; };
    const first = client.onLoopUpdate(handler);
    const second = client.onLoopUpdate(handler);

    assert.equal(fixture.listenerCount('onLoopUpdate'), 1);
    first();
    first();
    assert.equal(fixture.listenerCount('onLoopUpdate'), 1);
    assert.deepStrictEqual(fixture.releaseCounts('onLoopUpdate'), [0]);
    fixture.emit('onLoopUpdate', {});
    assert.equal(calls, 1);
    second();
    assert.equal(fixture.listenerCount('onLoopUpdate'), 0);
    assert.deepStrictEqual(fixture.releaseCounts('onLoopUpdate'), [1]);
  });

  it('supports cancellation from inside a callback without removing peers', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi();
    const client = new IpcAutoplanClient(fixture.api);
    const received = [];
    let stopFirst;
    stopFirst = client.onTerminalStatus((event) => {
      received.push(`first:${event.status}`);
      stopFirst();
    });
    client.onTerminalStatus((event) => received.push(`second:${event.status}`));

    fixture.emit('onTerminalStatus', { status: 'running' });
    fixture.emit('onTerminalStatus', { status: 'exited' });
    assert.deepStrictEqual(received, ['first:running', 'second:running', 'second:exited']);
    assert.equal(fixture.listenerCount('onTerminalStatus'), 1);
  });

  it('propagates subscription failures without retaining phantom releases', () => {
    const IpcAutoplanClient = loadIpcClient();
    const subscribeError = new Error('subscribe failed');
    const fixture = createApi({
      onChatQueue: () => { throw subscribeError; },
    });
    const client = new IpcAutoplanClient(fixture.api);

    assert.throws(() => client.onChatQueue(() => {}), (error) => error === subscribeError);
    client.destroy();
  });

  it('rejects a preload subscription that does not return an unsubscribe function', () => {
    const IpcAutoplanClient = loadIpcClient();
    const fixture = createApi({ onChatDone: () => undefined });
    const client = new IpcAutoplanClient(fixture.api);

    assert.throws(
      () => client.onChatDone(() => {}),
      /event subscription must return an unsubscribe function/,
    );
    client.destroy();
  });

  it('destroy releases all listeners once, continues after release errors, and rejects new subscriptions', () => {
    const IpcAutoplanClient = loadIpcClient();
    const releaseError = new Error('release failed');
    let failingReleaseCalls = 0;
    const fixture = createApi({
      onLoopPatch: () => () => {
        failingReleaseCalls += 1;
        throw releaseError;
      },
    });
    const client = new IpcAutoplanClient(fixture.api);
    client.onLoopPatch(() => {});
    client.onTerminalClosed(() => {});

    assert.throws(() => client.destroy(), (error) => error === releaseError);
    assert.equal(failingReleaseCalls, 1);
    assert.deepStrictEqual(fixture.releaseCounts('onTerminalClosed'), [1]);
    client.destroy();
    assert.equal(failingReleaseCalls, 1);
    assert.throws(() => client.onLoopUpdate(() => {}), /has been destroyed/);
  });
});
