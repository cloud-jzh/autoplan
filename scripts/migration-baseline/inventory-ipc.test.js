'use strict';

const assert = require('node:assert/strict');
const path = require('node:path');
const test = require('node:test');

const {
  extractAutoplanApi,
  extractBoundaryMembers,
  extractBoundarySurfaces,
  extractConstTuple,
  extractPreload,
  extractHandlers,
  extractEventProducers,
  extractInventory,
  loadMatrix,
  validateMatrix,
} = require('./inventory-ipc');

const ROOT = path.resolve(__dirname, '../..');

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

test('extracts AutoplanApi leaves including nested namespaces', () => {
  const source = `export interface AutoplanApi {
  snapshot: () => Promise<unknown>;
  fileAccess: {
    get: () => Promise<unknown>;
    save: () => Promise<unknown>;
  };
}`;
  assert.deepEqual(extractAutoplanApi(source), ['fileAccess.get', 'fileAccess.save', 'snapshot']);
});

test('extracts explicit client and desktop boundary members', () => {
  assert.deepEqual(extractConstTuple("export const KEYS = ['alpha', 'beta'] as const;", 'KEYS'), ['alpha', 'beta']);
  const client = "export const AUTOPLAN_CLIENT_OPERATION_KEYS = ['snapshot', 'fileAccess'] as const;";
  const events = "export const AUTOPLAN_CLIENT_EVENT_KEYS = ['onLoopUpdate'] as const;";
  const desktop = `
export const DESKTOP_BRIDGE_OPERATION_KEYS = ['pickDirectory'] as const;
export interface DesktopBridgeEvents {
  onUpdateStatus: Subscribe<unknown>;
}`;
  assert.deepEqual(extractBoundaryMembers(client, events, desktop), [
    'fileAccess.get',
    'fileAccess.save',
    'onLoopUpdate',
    'onUpdateStatus',
    'pickDirectory',
    'snapshot',
  ]);
  assert.deepEqual(extractBoundarySurfaces(client, events, desktop), {
    clientMembers: ['fileAccess.get', 'fileAccess.save', 'onLoopUpdate', 'snapshot'],
    desktopMembers: ['onUpdateStatus', 'pickDirectory'],
    members: ['fileAccess.get', 'fileAccess.save', 'onLoopUpdate', 'onUpdateStatus', 'pickDirectory', 'snapshot'],
  });
});

test('extracts preload invoke, subscription, shorthand, and nested mappings', () => {
  const source = `contextBridge.exposeInMainWorld('autoplan', {
  snapshot: (id) => ipcRenderer.invoke('snapshot', id),
  toFileUrl,
  onLoopUpdate: (handler) => {
    ipcRenderer.on('loop:update', handler);
  },
  fileAccess: {
    get: () => ipcRenderer.invoke('file-access:get'),
  },
});`;
  const result = extractPreload(source);
  assert.deepEqual(result.members, ['fileAccess.get', 'onLoopUpdate', 'snapshot', 'toFileUrl']);
  assert.deepEqual(result.invokes, { snapshot: 'snapshot', 'fileAccess.get': 'file-access:get' });
  assert.deepEqual(result.subscriptions, { onLoopUpdate: 'loop:update' });
});

test('extracts literal main handlers and resolved terminal constants', () => {
  const main = "ipcMain.handle('snapshot', () => {});";
  const terminal = 'ipcMain.handle(TERMINAL_CHANNELS.CREATE, () => {});';
  const terminalTypes = "const TERMINAL_CHANNELS = Object.freeze({ CREATE: 'terminal:create' });";
  assert.deepEqual(extractHandlers(main, terminal, terminalTypes), ['snapshot', 'terminal:create']);
});

test('extracts literal and terminal main-to-renderer event producers', () => {
  const main = `
    mainWindow.webContents.send('loop:update', snapshot);
    sendToRendererWindow('updates:status', status);
    send('chat:done', payload);
  `;
  const terminal = 'terminalService.on(TERMINAL_CHANNELS.DATA, listener);';
  const terminalTypes = "const TERMINAL_CHANNELS = Object.freeze({ DATA: 'terminal:data' });";
  assert.deepEqual(extractEventProducers(main, terminal, terminalTypes), [
    'chat:done',
    'loop:update',
    'terminal:data',
    'updates:status',
  ]);
});

test('controlled matrix exactly owns the current source inventory', () => {
  const matrix = loadMatrix(ROOT);
  const inventory = extractInventory(ROOT);
  assert.deepEqual(validateMatrix(matrix, inventory), []);
});

test('drift reports additions, deletions, renames, missing handlers, missing mappings, and renderer gaps', () => {
  const matrix = loadMatrix(ROOT);
  const baseline = extractInventory(ROOT);
  const cases = [
    {
      name: 'new API member',
      inventory: { ...baseline, apiMembers: [...baseline.apiMembers, 'newMethod'] },
      message: 'AutoplanApi 新增/改名未分类',
    },
    {
      name: 'deleted API member',
      inventory: { ...baseline, apiMembers: baseline.apiMembers.filter((item) => item !== 'snapshot') },
      message: 'AutoplanApi 已删除/改名但矩阵仍保留',
    },
    {
      name: 'missing handler',
      inventory: { ...baseline, handlers: baseline.handlers.filter((item) => item !== 'snapshot') },
      message: '无 handler：snapshot',
    },
    {
      name: 'missing preload mapping',
      inventory: {
        ...baseline,
        preload: { ...baseline.preload, invokes: { ...baseline.preload.invokes, snapshot: 'snapshot:renamed' } },
      },
      message: 'channel 漂移',
    },
    {
      name: 'unclassified renderer call',
      inventory: {
        ...baseline,
        renderer: { ...baseline.renderer, members: [...baseline.renderer.members, 'newRendererCall'] },
      },
      message: 'renderer window.autoplan 调用未分类',
    },
    {
      name: 'new boundary member',
      inventory: { ...baseline, boundaryMembers: [...baseline.boundaryMembers, 'newBoundaryMethod'] },
      message: 'renderer client/desktop boundary 新增/改名未分类',
    },
    {
      name: 'missing boundary member',
      inventory: {
        ...baseline,
        boundaryMembers: baseline.boundaryMembers.filter((item) => item !== 'snapshot'),
      },
      message: 'renderer client/desktop boundary 已删除/改名但矩阵仍保留',
    },
    {
      name: 'business capability moved to desktop boundary',
      inventory: {
        ...baseline,
        clientBoundaryMembers: baseline.clientBoundaryMembers.filter((item) => item !== 'snapshot'),
        desktopBoundaryMembers: [...baseline.desktopBoundaryMembers, 'snapshot'],
      },
      message: 'renderer AutoplanClient boundary 已删除/改名但矩阵仍保留',
    },
    {
      name: 'unclassified event producer',
      inventory: { ...baseline, eventProducers: [...baseline.eventProducers, 'domain:new-event'] },
      message: 'main-to-renderer 事件生产者 新增/改名未分类',
    },
  ];

  for (const item of cases) {
    const errors = validateMatrix(matrix, item.inventory);
    assert(errors.some((error) => error.includes(item.message)), `${item.name}: ${errors.join('\n')}`);
  }
});

test('duplicate ownership and incomplete event semantics fail explicitly', () => {
  const matrix = clone(loadMatrix(ROOT));
  const inventory = extractInventory(ROOT);
  matrix.capabilities.push({ ...matrix.capabilities[0], id: 'duplicate.meta' });
  matrix.events[0].ordering = '';
  const errors = validateMatrix(matrix, inventory);
  assert(errors.some((error) => error.includes('能力 preload 重复归属')));
  assert(errors.some((error) => error.includes('事件 event.loop-update 缺少字段 ordering')));
});
