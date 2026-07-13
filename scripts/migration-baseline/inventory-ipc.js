'use strict';

const fs = require('node:fs');
const path = require('node:path');

const SOURCE_PATHS = Object.freeze({
  types: 'src/renderer/types.ts',
  preload: 'src/preload.js',
  main: 'src/main.js',
  terminalIpc: 'src/terminal/terminalIpc.js',
  terminalTypes: 'src/terminal/terminalTypes.js',
  renderer: 'src/renderer',
  client: 'src/renderer/lib/api/client.ts',
  events: 'src/renderer/lib/api/events.ts',
  desktopBridge: 'src/renderer/lib/desktop/bridge.ts',
  matrix: 'docs/migration/p00/capability-matrix.json',
});

const REQUIRED_CAPABILITY_FIELDS = Object.freeze([
  'id',
  'kind',
  'api',
  'preload',
  'channel',
  'handler',
  'module',
  'renderer',
  'contract',
  'sideEffects',
  'owner',
  'target',
  'stage',
  'featureFlag',
  'fallback',
]);

const REQUIRED_EVENT_FIELDS = Object.freeze([
  'id',
  'api',
  'preload',
  'channel',
  'producer',
  'consumer',
  'payload',
  'ordering',
  'owner',
  'target',
  'stage',
  'featureFlag',
  'fallback',
]);

const OWNERS = new Set(['go-api', 'desktop-bridge', 'temporary-ipc', 'deprecated']);

function readText(rootDir, relativePath) {
  return fs.readFileSync(path.join(rootDir, relativePath), 'utf8');
}

function walkFiles(dir) {
  const result = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) result.push(...walkFiles(target));
    else result.push(target);
  }
  return result;
}

function sortedUnique(values) {
  return [...new Set(values.filter(Boolean))].sort((left, right) => left.localeCompare(right));
}

function sorted(values) {
  return values.filter(Boolean).sort((left, right) => left.localeCompare(right));
}

function extractBalancedBlock(source, marker) {
  const markerIndex = source.indexOf(marker);
  if (markerIndex < 0) throw new Error(`未找到源码标记：${marker}`);
  const openIndex = source.indexOf('{', markerIndex);
  if (openIndex < 0) throw new Error(`源码标记缺少块：${marker}`);
  let depth = 0;
  for (let index = openIndex; index < source.length; index += 1) {
    if (source[index] === '{') depth += 1;
    if (source[index] === '}') depth -= 1;
    if (depth === 0) return source.slice(openIndex + 1, index);
  }
  throw new Error(`源码块未闭合：${marker}`);
}

function extractAutoplanApi(source) {
  const block = extractBalancedBlock(source, 'export interface AutoplanApi');
  const members = [];
  let nested = null;
  for (const line of block.split(/\r?\n/)) {
    const top = /^  ([A-Za-z_$][\w$]*):\s*(.*)$/.exec(line);
    if (top) {
      const [, name, tail] = top;
      if (tail.trim() === '{') nested = name;
      else members.push(name);
      continue;
    }
    const child = /^    ([A-Za-z_$][\w$]*):/.exec(line);
    if (nested && child) members.push(`${nested}.${child[1]}`);
    if (/^  };/.test(line)) nested = null;
  }
  return sortedUnique(members);
}

function extractConstTuple(source, name) {
  const marker = `export const ${name} = [`;
  const start = source.indexOf(marker);
  if (start < 0) throw new Error(`未找到源码常量：${name}`);
  const open = source.indexOf('[', start);
  const close = source.indexOf('] as const', open);
  if (close < 0) throw new Error(`源码常量未闭合：${name}`);
  return sortedUnique([...source.slice(open + 1, close).matchAll(/['"]([^'"]+)['"]/g)].map((match) => match[1]));
}

function extractBoundarySurfaces(clientSource, eventsSource, desktopBridgeSource) {
  const clientOperations = extractConstTuple(clientSource, 'AUTOPLAN_CLIENT_OPERATION_KEYS')
    .flatMap((member) => member === 'fileAccess' ? ['fileAccess.get', 'fileAccess.save'] : [member]);
  const clientEvents = extractConstTuple(eventsSource, 'AUTOPLAN_CLIENT_EVENT_KEYS');
  const desktopOperations = extractConstTuple(desktopBridgeSource, 'DESKTOP_BRIDGE_OPERATION_KEYS');
  const desktopEvents = [...extractBalancedBlock(desktopBridgeSource, 'export interface DesktopBridgeEvents')
    .matchAll(/^\s*([A-Za-z_$][\w$]*):/gm)]
    .map((match) => match[1]);
  const clientMembers = sortedUnique([...clientOperations, ...clientEvents]);
  const desktopMembers = sortedUnique([...desktopOperations, ...desktopEvents]);
  return { clientMembers, desktopMembers, members: sortedUnique([...clientMembers, ...desktopMembers]) };
}

function extractBoundaryMembers(clientSource, eventsSource, desktopBridgeSource) {
  return extractBoundarySurfaces(clientSource, eventsSource, desktopBridgeSource).members;
}

function effectiveCapabilityApi(item) {
  return item.api || (item.kind === 'ipc' ? item.preload : null);
}

function extractPreload(source) {
  const block = extractBalancedBlock(source, "contextBridge.exposeInMainWorld('autoplan'");
  const members = [];
  const invokes = {};
  const subscriptions = {};
  let currentTop = null;
  let nested = null;

  for (const line of block.split(/\r?\n/)) {
    const top = /^  ([A-Za-z_$][\w$]*):\s*(.*)$/.exec(line);
    const shorthand = /^  ([A-Za-z_$][\w$]*),\s*$/.exec(line);
    if (top) {
      currentTop = top[1];
      if (top[2].trim() === '{') {
        nested = currentTop;
      } else {
        members.push(currentTop);
        const invoke = /ipcRenderer\.invoke\('([^']+)'/.exec(top[2]);
        if (invoke) invokes[currentTop] = invoke[1];
      }
      continue;
    }
    if (shorthand) {
      currentTop = shorthand[1];
      members.push(currentTop);
      continue;
    }
    const child = /^    ([A-Za-z_$][\w$]*):\s*(.*)$/.exec(line);
    if (nested && child) {
      const member = `${nested}.${child[1]}`;
      members.push(member);
      const invoke = /ipcRenderer\.invoke\('([^']+)'/.exec(child[2]);
      if (invoke) invokes[member] = invoke[1];
      continue;
    }
    const listener = /ipcRenderer\.on\('([^']+)'/.exec(line);
    if (currentTop && listener) subscriptions[currentTop] = listener[1];
    if (/^  },?\s*$/.test(line)) {
      if (nested) nested = null;
      currentTop = null;
    }
  }

  return { members: sortedUnique(members), memberOccurrences: sorted(members), invokes, subscriptions };
}

function extractTerminalChannels(source) {
  const block = extractBalancedBlock(source, 'const TERMINAL_CHANNELS');
  const channels = {};
  for (const match of block.matchAll(/^\s*([A-Z_]+):\s*'([^']+)'/gm)) channels[match[1]] = match[2];
  return channels;
}

function extractHandlers(mainSource, terminalSource, terminalTypeSource) {
  const channels = [];
  for (const match of mainSource.matchAll(/ipcMain\.handle\('([^']+)'/g)) channels.push(match[1]);
  const terminalChannels = extractTerminalChannels(terminalTypeSource);
  for (const match of terminalSource.matchAll(/ipcMain\.handle\(TERMINAL_CHANNELS\.([A-Z_]+)/g)) {
    channels.push(terminalChannels[match[1]] || `UNRESOLVED:${match[1]}`);
  }
  return sorted(channels);
}

function extractEventProducers(mainSource, terminalSource, terminalTypeSource) {
  const channels = [];
  for (const match of mainSource.matchAll(/(?:webContents\.send|sendToRendererWindow|\bsend)\('([^']+)'/g)) {
    channels.push(match[1]);
  }
  const terminalChannels = extractTerminalChannels(terminalTypeSource);
  for (const match of terminalSource.matchAll(/terminalService\.on\(TERMINAL_CHANNELS\.([A-Z_]+)/g)) {
    channels.push(terminalChannels[match[1]] || `UNRESOLVED:${match[1]}`);
  }
  return sortedUnique(channels);
}

function stripComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|\s)\/\/.*$/gm, '$1');
}

function extractRendererMembers(rootDir) {
  const rendererRoot = path.join(rootDir, SOURCE_PATHS.renderer);
  const members = [];
  const filesByMember = {};
  for (const filePath of walkFiles(rendererRoot)) {
    if (!/\.(?:ts|tsx|js|jsx)$/.test(filePath) || /(?:^|\.)test\./.test(path.basename(filePath))) continue;
    const source = stripComments(fs.readFileSync(filePath, 'utf8'));
    const relative = path.relative(rootDir, filePath).replace(/\\/g, '/');
    const found = [];
    for (const match of source.matchAll(/window\.autoplan\s*(?:\?\.)?\.\s*([A-Za-z_$][\w$]*)(?:\s*\.\s*([A-Za-z_$][\w$]*))?/g)) {
      found.push(match[2] ? `${match[1]}.${match[2]}` : match[1]);
    }
    for (const match of source.matchAll(/window\.autoplan\s*\[([^\]]+)\]/g)) {
      const expression = match[1];
      const exact = /^\s*'([A-Za-z_$][\w$]*)'\s*$/.exec(expression);
      if (exact) found.push(exact[1]);
      for (const literal of expression.matchAll(/[?:]\s*'([A-Za-z_$][\w$]*)'/g)) found.push(literal[1]);
    }
    for (const match of source.matchAll(/\(window\.autoplan\s+as[\s\S]{0,500}?\}\)\.([A-Za-z_$][\w$]*)/g)) found.push(match[1]);
    for (const member of sortedUnique(found)) {
      members.push(member);
      (filesByMember[member] ||= []).push(relative);
    }
  }
  for (const member of Object.keys(filesByMember)) filesByMember[member] = sortedUnique(filesByMember[member]);
  return { members: sortedUnique(members), filesByMember };
}

function extractInventory(rootDir) {
  const types = readText(rootDir, SOURCE_PATHS.types);
  const preloadSource = readText(rootDir, SOURCE_PATHS.preload);
  const main = readText(rootDir, SOURCE_PATHS.main);
  const terminalIpc = readText(rootDir, SOURCE_PATHS.terminalIpc);
  const terminalTypes = readText(rootDir, SOURCE_PATHS.terminalTypes);
  const client = readText(rootDir, SOURCE_PATHS.client);
  const events = readText(rootDir, SOURCE_PATHS.events);
  const desktopBridge = readText(rootDir, SOURCE_PATHS.desktopBridge);
  const boundary = extractBoundarySurfaces(client, events, desktopBridge);
  return {
    apiMembers: extractAutoplanApi(types),
    preload: extractPreload(preloadSource),
    handlers: extractHandlers(main, terminalIpc, terminalTypes),
    eventProducers: extractEventProducers(main, terminalIpc, terminalTypes),
    renderer: extractRendererMembers(rootDir),
    boundaryMembers: boundary.members,
    clientBoundaryMembers: boundary.clientMembers,
    desktopBoundaryMembers: boundary.desktopMembers,
  };
}

function compareSets(label, actual, expected, errors) {
  const actualSet = new Set(actual);
  const expectedSet = new Set(expected);
  const added = actual.filter((item) => !expectedSet.has(item));
  const removed = expected.filter((item) => !actualSet.has(item));
  if (added.length) errors.push(`${label} 新增/改名未分类：${added.join(', ')}`);
  if (removed.length) errors.push(`${label} 已删除/改名但矩阵仍保留：${removed.join(', ')}`);
}

function duplicateValues(values) {
  const seen = new Set();
  const duplicates = new Set();
  for (const value of values.filter(Boolean)) {
    if (seen.has(value)) duplicates.add(value);
    seen.add(value);
  }
  return sortedUnique([...duplicates]);
}

function validateShape(matrix, errors) {
  if (matrix.schemaVersion !== 1) errors.push('capability-matrix.json schemaVersion 必须为 1');
  if (!Array.isArray(matrix.capabilities) || !matrix.capabilities.length) errors.push('capabilities 不能为空');
  if (!Array.isArray(matrix.events) || !matrix.events.length) errors.push('events 不能为空');

  for (const item of matrix.capabilities || []) {
    for (const field of REQUIRED_CAPABILITY_FIELDS) {
      if (!(field in item) || item[field] === '') errors.push(`能力 ${item.id || '<unknown>'} 缺少字段 ${field}`);
    }
    if (!OWNERS.has(item.owner)) errors.push(`能力 ${item.id} owner 非法：${item.owner}`);
    if (!Array.isArray(item.renderer) || item.renderer.length === 0) errors.push(`能力 ${item.id} 缺少 renderer 归属`);
    if (item.kind === 'ipc' && (!item.preload || !item.channel || !item.handler)) {
      errors.push(`IPC 能力 ${item.id} 缺少 preload/channel/handler 链路`);
    }
  }
  for (const event of matrix.events || []) {
    for (const field of REQUIRED_EVENT_FIELDS) {
      if (!(field in event) || event[field] === '') errors.push(`事件 ${event.id || '<unknown>'} 缺少字段 ${field}`);
    }
    if (!OWNERS.has(event.owner)) errors.push(`事件 ${event.id} owner 非法：${event.owner}`);
    if (!Array.isArray(event.consumer) || event.consumer.length === 0) errors.push(`事件 ${event.id} 缺少 consumer`);
  }

  for (const [label, values] of [
    ['能力 id', (matrix.capabilities || []).map((item) => item.id)],
    ['API member', [
      ...(matrix.capabilities || []).map(effectiveCapabilityApi),
      ...(matrix.events || []).map((item) => item.api),
    ]],
    ['能力 preload', (matrix.capabilities || []).map((item) => item.preload)],
    ['全部 preload', [
      ...(matrix.capabilities || []).map((item) => item.preload),
      ...(matrix.events || []).map((item) => item.preload),
    ]],
    ['IPC handler', (matrix.capabilities || []).filter((item) => item.kind === 'ipc').map((item) => item.channel)],
    ['事件 id', (matrix.events || []).map((item) => item.id)],
    ['事件 preload', (matrix.events || []).map((item) => item.preload)],
    ['事件 channel', (matrix.events || []).map((item) => item.channel)],
  ]) {
    const duplicates = duplicateValues(values);
    if (duplicates.length) errors.push(`${label} 重复归属：${duplicates.join(', ')}`);
  }
}

function validateMatrix(matrix, inventory) {
  const errors = [];
  validateShape(matrix, errors);
  const capabilities = matrix.capabilities || [];
  const events = matrix.events || [];
  const matrixApi = [...capabilities.map(effectiveCapabilityApi), ...events.map((item) => item.api)].filter(Boolean);
  const matrixClientApi = [
    ...capabilities.filter((item) => item.owner === 'go-api').map(effectiveCapabilityApi),
    ...events.filter((item) => item.owner === 'go-api').map((item) => item.api),
  ].filter(Boolean);
  const matrixDesktopApi = [
    ...capabilities.filter((item) => item.owner === 'desktop-bridge').map(effectiveCapabilityApi),
    ...events.filter((item) => item.owner === 'desktop-bridge').map((item) => item.api),
  ].filter(Boolean);
  const matrixPreload = [...capabilities.map((item) => item.preload), ...events.map((item) => item.preload)].filter(Boolean);
  const matrixHandlers = capabilities.filter((item) => item.kind === 'ipc').map((item) => item.channel);

  const duplicateHandlers = duplicateValues(inventory.handlers);
  if (duplicateHandlers.length) errors.push(`源码 ipcMain handler 重复注册：${duplicateHandlers.join(', ')}`);
  const duplicatePreloadMembers = duplicateValues(inventory.preload.memberOccurrences || inventory.preload.members);
  if (duplicatePreloadMembers.length) errors.push(`源码 preload 暴露项重复定义：${duplicatePreloadMembers.join(', ')}`);

  compareSets('AutoplanApi', inventory.apiMembers, sortedUnique(matrixApi), errors);
  compareSets('renderer client/desktop boundary', inventory.boundaryMembers, sortedUnique(matrixApi), errors);
  compareSets('renderer AutoplanClient boundary', inventory.clientBoundaryMembers, sortedUnique(matrixClientApi), errors);
  compareSets('renderer DesktopBridge boundary', inventory.desktopBoundaryMembers, sortedUnique(matrixDesktopApi), errors);
  compareSets('preload 暴露项', inventory.preload.members, sortedUnique(matrixPreload), errors);
  compareSets('ipcMain handler', inventory.handlers, sortedUnique(matrixHandlers), errors);
  compareSets('main-to-renderer 事件生产者', inventory.eventProducers, sortedUnique(events.map((item) => item.channel)), errors);
  for (const member of inventory.renderer.members) {
    if (!matrixPreload.includes(member)) errors.push(`renderer window.autoplan 调用未分类：${member}`);
  }

  const byPreload = new Map(capabilities.filter((item) => item.preload).map((item) => [item.preload, item]));
  for (const [member, channel] of Object.entries(inventory.preload.invokes)) {
    const item = byPreload.get(member);
    if (!item) errors.push(`preload ${member} 没有唯一归属`);
    else if (item.channel !== channel) errors.push(`preload ${member} channel 漂移：源码=${channel} 矩阵=${item.channel}`);
  }
  const eventsByPreload = new Map(events.map((item) => [item.preload, item]));
  for (const [member, channel] of Object.entries(inventory.preload.subscriptions)) {
    const item = eventsByPreload.get(member);
    if (!item) errors.push(`preload 订阅 ${member} 没有事件归属`);
    else if (item.channel !== channel) errors.push(`preload 订阅 ${member} channel 漂移：源码=${channel} 矩阵=${item.channel}`);
  }
  for (const item of capabilities.filter((entry) => entry.kind === 'ipc')) {
    if (!inventory.handlers.includes(item.channel)) errors.push(`能力 ${item.id} 无 handler：${item.channel}`);
    if (inventory.preload.invokes[item.preload] !== item.channel) errors.push(`能力 ${item.id} 无 preload 映射：${item.preload} -> ${item.channel}`);
  }
  return errors;
}

function loadMatrix(rootDir) {
  return JSON.parse(readText(rootDir, SOURCE_PATHS.matrix));
}

function run(rootDir = path.resolve(__dirname, '../..')) {
  const matrix = loadMatrix(rootDir);
  const inventory = extractInventory(rootDir);
  const errors = validateMatrix(matrix, inventory);
  if (errors.length) {
    const error = new Error(`IPC 能力矩阵漂移（${errors.length} 项）\n- ${errors.join('\n- ')}`);
    error.errors = errors;
    throw error;
  }
  return {
    ok: true,
    capabilities: matrix.capabilities.length,
    events: matrix.events.length,
    handlers: inventory.handlers.length,
  };
}

if (require.main === module) {
  try {
    const result = run(process.argv[2] ? path.resolve(process.argv[2]) : undefined);
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  SOURCE_PATHS,
  extractAutoplanApi,
  extractConstTuple,
  extractBoundaryMembers,
  extractBoundarySurfaces,
  effectiveCapabilityApi,
  extractPreload,
  extractHandlers,
  extractEventProducers,
  extractRendererMembers,
  extractInventory,
  validateMatrix,
  loadMatrix,
  run,
};
