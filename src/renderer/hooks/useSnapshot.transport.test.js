const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');
const vm = require('node:vm');
const ts = require('typescript');

let activeHarness;
let activeClient;

function loadUseSnapshot() {
  const file = join(process.cwd(), 'src', 'renderer', 'hooks', 'useSnapshot.ts');
  const compiled = ts.transpileModule(readFileSync(file, 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    fileName: 'useSnapshot.ts',
  }).outputText;
  const module = { exports: {} };
  const wrapper = vm.runInThisContext(`(function (require, module, exports) { ${compiled}\n})`);
  wrapper((id) => {
    if (id === 'react') {
      return {
        useState: (initial) => activeHarness.useState(initial),
        useRef: (initial) => activeHarness.useRef(initial),
        useCallback: (callback) => callback,
        useEffect: (effect, dependencies) => activeHarness.useEffect(effect, dependencies),
      };
    }
    if (id === '../lib/api/provider') return { useAutoplanClient: () => activeClient };
    throw new Error(`unexpected runtime import: ${id}`);
  }, module, module.exports);
  return module.exports.useSnapshot;
}

class HookHarness {
  constructor(hook, client) {
    this.hook = hook;
    this.client = client;
    this.states = [];
    this.refs = [];
    this.effects = [];
    this.frames = new Map();
    this.nextFrame = 1;
  }

  useState(initial) {
    const index = this.stateCursor++;
    if (index >= this.states.length) this.states.push(initial);
    const setState = (next) => {
      this.states[index] = typeof next === 'function' ? next(this.states[index]) : next;
    };
    return [this.states[index], setState];
  }

  useRef(initial) {
    const index = this.refCursor++;
    if (index >= this.refs.length) this.refs.push({ current: initial });
    return this.refs[index];
  }

  useEffect(effect, dependencies) {
    const index = this.effectCursor++;
    const previous = this.effects[index];
    const changed = !previous || dependencies.length !== previous.dependencies.length ||
      dependencies.some((value, position) => !Object.is(value, previous.dependencies[position]));
    if (!changed) return;
    previous?.cleanup?.();
    this.effects[index] = { dependencies: [...dependencies], cleanup: effect() };
  }

  render(projectId) {
    this.stateCursor = 0;
    this.refCursor = 0;
    this.effectCursor = 0;
    activeHarness = this;
    activeClient = this.client;
    try {
      return this.hook(projectId);
    } finally {
      activeHarness = undefined;
      activeClient = undefined;
    }
  }

  requestFrame(callback) {
    const id = this.nextFrame++;
    this.frames.set(id, callback);
    return id;
  }

  flushFrames() {
    const frames = [...this.frames.values()];
    this.frames.clear();
    for (const callback of frames) callback();
  }

  dispose() {
    for (const effect of this.effects) effect?.cleanup?.();
    this.effects = [];
    this.frames.clear();
  }
}

function project(id) {
  return {
    id, name: `Synthetic ${id}`, workspace_path: `<fixture-workspace>/project-${id}`,
    description: '', created_at: '2026-01-02T03:04:05.000Z',
    updated_at: '2026-01-02T03:04:06.000Z', running: 0, phase: 'idle', interval_seconds: 5,
  };
}

function snapshot(id) {
  const active = id === null ? null : project(id);
  return {
    activeProjectId: id, activeProject: active, projects: active ? [active] : [project(2), project(1)],
    mcp: {}, state: active ? { project_id: id, phase: 'idle', running: 0, interval_seconds: 5 } : null,
    requirements: [], feedback: [], attachments: [], plans: [], tasks: [], events: [], scans: [],
    scanSummary: {}, scripts: [], executors: [], terminals: [], activeOperation: null,
    activeOperations: [], lastOperation: null,
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function clientFixture(snapshotForProject) {
  const snapshotHandlers = new Set();
  const patchHandlers = new Set();
  let releases = 0;
  return {
    snapshot: snapshotForProject,
    onLoopUpdate(handler) {
      snapshotHandlers.add(handler);
      return () => { if (snapshotHandlers.delete(handler)) releases += 1; };
    },
    onLoopPatch(handler) {
      patchHandlers.add(handler);
      return () => { if (patchHandlers.delete(handler)) releases += 1; };
    },
    emitSnapshot(value) { for (const handler of [...snapshotHandlers]) handler(value); },
    emitPatch(value) { for (const handler of [...patchHandlers]) handler(value); },
    releaseCount: () => releases,
  };
}

async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

describe('useSnapshot IPC/HTTP transport lifecycle contract', () => {
  const useSnapshot = loadUseSnapshot();
  const originalWindow = globalThis.window;

  it('produces identical loading and resolved state for IPC and HTTP clients', async () => {
    try {
      for (const transport of ['ipc', 'http']) {
        const client = clientFixture(async (id) => snapshot(id));
        const harness = new HookHarness(useSnapshot, client);
        globalThis.window = {
          requestAnimationFrame: (callback) => harness.requestFrame(callback),
          cancelAnimationFrame: (id) => harness.frames.delete(id),
        };
        const initial = harness.render(1);
        assert.equal(initial.snapshot, null, `${transport} initial loading state`);
        await settle();
        assert.deepStrictEqual(harness.states[0], snapshot(1));
        assert.equal(harness.states[1], null);
        harness.dispose();
        assert.equal(client.releaseCount(), 2);
      }
    } finally {
      globalThis.window = originalWindow;
    }
  });

  it('ignores late project responses and isolates project event updates', async () => {
    try {
      const first = deferred();
      const second = deferred();
      const client = clientFixture((id) => id === 1 ? first.promise : second.promise);
      const harness = new HookHarness(useSnapshot, client);
      globalThis.window = {
        requestAnimationFrame: (callback) => harness.requestFrame(callback),
        cancelAnimationFrame: (id) => harness.frames.delete(id),
      };
      harness.render(1);
      harness.render(2);
      first.resolve(snapshot(1));
      second.resolve(snapshot(2));
      await settle();
      assert.equal(harness.states[0].activeProjectId, 2);

      client.emitSnapshot(snapshot(1));
      harness.flushFrames();
      assert.equal(harness.states[0].activeProjectId, 2);
      assert.deepStrictEqual(harness.states[0].projects, snapshot(1).projects);
      harness.dispose();
      assert.equal(client.releaseCount(), 4);
    } finally {
      globalThis.window = originalWindow;
    }
  });

  it('keeps transport failures in the same error boundary and ignores rejection after disposal', async () => {
    try {
      const failure = new Error('stable transport failure');
      const client = clientFixture(async () => { throw failure; });
      const harness = new HookHarness(useSnapshot, client);
      globalThis.window = {
        requestAnimationFrame: (callback) => harness.requestFrame(callback),
        cancelAnimationFrame: (id) => harness.frames.delete(id),
      };
      harness.render(1);
      await settle();
      assert.equal(harness.states[1], failure.message);

      const late = deferred();
      const lateClient = clientFixture(() => late.promise);
      const lateHarness = new HookHarness(useSnapshot, lateClient);
      globalThis.window = {
        requestAnimationFrame: (callback) => lateHarness.requestFrame(callback),
        cancelAnimationFrame: (id) => lateHarness.frames.delete(id),
      };
      lateHarness.render(1);
      lateHarness.dispose();
      late.reject(new Error('late failure'));
      await settle();
      assert.equal(lateHarness.states[1], null);
    } finally {
      globalThis.window = originalWindow;
    }
  });
});
