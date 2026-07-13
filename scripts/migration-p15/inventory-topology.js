'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { isWithin, safeRelative, sha256 } = require('./check-safety');

const ROOT = path.resolve(__dirname, '..', '..');
const SOURCE_ROOTS = Object.freeze([
  'src/renderer/components', 'src/renderer/hooks', 'src/renderer/pages', 'src/renderer/lib/api',
  'src/renderer/lib/desktop', 'src/preload.js', 'src/main.js', 'src/data', 'src/loop', 'src/chat',
  'src/executors', 'src/terminal', 'src/mcpServer.js', 'src/mcpTools.js', 'src/database.js',
  'src/loopService.js', 'src/intakeService.js', 'src/attachments.js', 'backend/internal/httpapi',
  'backend/internal/mcp', 'backend/internal/application', 'backend/internal/repository',
]);
const DIRECT_BRIDGE_ADAPTERS = new Set([
  'src/renderer/lib/api/ipcClient.ts', 'src/renderer/lib/desktop/ipcBridge.ts',
]);

function walkFiles(rootDir, relative) {
  const absolute = path.join(rootDir, relative);
  const info = fs.lstatSync(absolute, { throwIfNoEntry: false });
  if (!info || info.isSymbolicLink()) return [];
  if (info.isFile()) return [relative.replaceAll('\\', '/')];
  if (!info.isDirectory()) return [];
  const result = [];
  for (const entry of fs.readdirSync(absolute, { withFileTypes: true })) {
    if (entry.isSymbolicLink()) continue;
    result.push(...walkFiles(rootDir, path.join(relative, entry.name)));
  }
  return result;
}

function sourceFiles(rootDir) {
  return [...new Set(SOURCE_ROOTS.flatMap((relative) => walkFiles(rootDir, relative)))]
    .filter((relative) => /\.(?:[cm]?js|tsx?|go)$/.test(relative))
    .sort();
}

function fileDigest(rootDir, relative) {
  const target = path.join(rootDir, relative);
  const info = fs.lstatSync(target, { throwIfNoEntry: false });
  if (!info?.isFile() || info.isSymbolicLink()) return { path: relative, missing: true };
  const bytes = fs.readFileSync(target);
  return { path: relative, bytes: bytes.length, sha256: sha256(bytes) };
}

function isTestSource(relative) { return /(?:^|\.)test\.(?:[cm]?js|tsx?)$/.test(relative); }

function directBridgeAccess(rootDir, files) {
  const hits = [];
  for (const relative of files) {
    if (!relative.startsWith('src/renderer/') || isTestSource(relative) || DIRECT_BRIDGE_ADAPTERS.has(relative)) continue;
    const text = fs.readFileSync(path.join(rootDir, relative), 'utf8');
    if (/\bwindow\.autoplan\b/.test(text)) hits.push(relative);
  }
  return hits;
}

function includesInFiles(rootDir, files, value) {
  return files.some((relative) => fs.readFileSync(path.join(rootDir, relative), 'utf8').includes(value));
}

function countFiles(files, prefix) { return files.filter((file) => file === prefix || file.startsWith(`${prefix}/`)).length; }

function inventoryTopology(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  if (!fs.existsSync(rootDir) || !isWithin(path.join(rootDir, 'src'), rootDir)) {
    return { schema_version: 1, status: 'blocked', ok: false, code: 'topology_root_invalid' };
  }
  const files = sourceFiles(rootDir);
  const directAccess = directBridgeAccess(rootDir, files);
  const nodes = [
    { id: 'renderer_components', files: countFiles(files, 'src/renderer/components') + countFiles(files, 'src/renderer/pages') },
    { id: 'renderer_hooks', files: countFiles(files, 'src/renderer/hooks') },
    { id: 'renderer_api', files: countFiles(files, 'src/renderer/lib/api') },
    { id: 'autoplan_client_transport', files: ['src/renderer/lib/api/client.ts', 'src/renderer/lib/api/transport.ts', 'src/renderer/lib/api/httpClient.ts', 'src/renderer/lib/api/eventStream.ts', 'src/renderer/lib/api/terminalSocket.ts'].filter((file) => files.includes(file)).length },
    { id: 'desktop_bridge', files: countFiles(files, 'src/renderer/lib/desktop') },
    { id: 'electron_preload', files: files.includes('src/preload.js') ? 1 : 0 },
    { id: 'electron_main', files: files.includes('src/main.js') ? 1 : 0 },
    { id: 'node_legacy_services', files: ['src/database.js', 'src/loopService.js', 'src/intakeService.js', 'src/attachments.js', 'src/mcpServer.js', 'src/mcpTools.js'].filter((file) => files.includes(file)).length + countFiles(files, 'src/chat') + countFiles(files, 'src/executors') + countFiles(files, 'src/terminal') },
    { id: 'go_httpapi', files: countFiles(files, 'backend/internal/httpapi') },
    { id: 'go_sse_handler', files: files.filter((file) => file.startsWith('backend/internal/httpapi/') && /(?:^|\/)sse(?:_|\.)/.test(file)).length },
    { id: 'go_terminal_websocket_handler', files: files.filter((file) => file.startsWith('backend/internal/httpapi/') && /terminal_ws\.go$/.test(file)).length },
    { id: 'go_mcp', files: countFiles(files, 'backend/internal/mcp') },
    { id: 'go_application', files: countFiles(files, 'backend/internal/application') },
    { id: 'go_repository', files: countFiles(files, 'backend/internal/repository') },
  ];
  const edges = [
    { from: 'renderer_components', to: 'renderer_api', kind: 'client_import', observed: includesInFiles(rootDir, files.filter((file) => file.startsWith('src/renderer/')), "lib/api") },
    { from: 'renderer_api', to: 'autoplan_client_transport', kind: 'transport_neutral_client', observed: files.includes('src/renderer/lib/api/client.ts') && files.includes('src/renderer/lib/api/transport.ts') },
    { from: 'renderer_api', to: 'electron_preload', kind: 'legacy_ipc_adapter', observed: files.includes('src/renderer/lib/api/ipcClient.ts') },
    { from: 'renderer_api', to: 'go_httpapi', kind: 'rest_sse_transport', observed: includesInFiles(rootDir, files, 'HttpAutoplanClient') },
    { from: 'renderer_api', to: 'go_httpapi', kind: 'terminal_websocket_transport', observed: includesInFiles(rootDir, files, 'TerminalSocket') },
    { from: 'electron_preload', to: 'electron_main', kind: 'privileged_ipc', observed: includesInFiles(rootDir, files, 'ipcRenderer.invoke') },
    { from: 'go_httpapi', to: 'go_application', kind: 'shared_application_service', observed: includesInFiles(rootDir, files.filter((file) => file.startsWith('backend/internal/httpapi/')), 'internal/application') },
    { from: 'go_sse_handler', to: 'go_application', kind: 'bounded_sse_application_service', observed: includesInFiles(rootDir, files.filter((file) => file.startsWith('backend/internal/httpapi/') && /(?:^|\/)sse(?:_|\.)/.test(file)), 'internal/application') },
    { from: 'go_terminal_websocket_handler', to: 'go_application', kind: 'terminal_websocket_application_service', observed: includesInFiles(rootDir, files.filter((file) => /backend\/internal\/httpapi\/terminal_ws\.go$/.test(file.replaceAll('\\', '/'))), 'internal/application') },
    { from: 'go_mcp', to: 'go_application', kind: 'shared_application_service', observed: includesInFiles(rootDir, files.filter((file) => file.startsWith('backend/internal/mcp/')), 'internal/application') },
    { from: 'go_application', to: 'go_repository', kind: 'repository_adapter', observed: includesInFiles(rootDir, files.filter((file) => file.startsWith('backend/internal/application/')), 'internal/repository') },
  ];
  const missingNodes = nodes.filter((node) => node.files === 0).map((node) => node.id);
  const missingEdges = edges.filter((edge) => !edge.observed).map((edge) => `${edge.from}:${edge.kind}`);
  const result = {
    schema_version: 1,
    kind: 'p15-topology-inventory',
    status: missingNodes.length || missingEdges.length ? 'blocked' : 'inventory',
    ok: missingNodes.length === 0 && missingEdges.length === 0,
    code: missingNodes.length ? 'topology_nodes_missing' : missingEdges.length ? 'topology_edges_missing' : 'topology_inventory_complete',
    nodes,
    edges,
    renderer_direct_bridge_access: directAccess.map((file) => ({ file, disposition: 'must_be_removed_or_moved_before_p15_p005' })),
    source_hashes: files.map((file) => fileDigest(rootDir, file)),
  };
  return result;
}

function parseArgs(argv) {
  if (argv.length !== 0) throw new Error('arguments_invalid');
  return {};
}

if (require.main === module) {
  try {
    const result = inventoryTopology(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, ok: result.ok, code: result.code, direct_bridge_accesses: result.renderer_direct_bridge_access.length })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_topology_inventory_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { DIRECT_BRIDGE_ADAPTERS, ROOT, SOURCE_ROOTS, directBridgeAccess, fileDigest, inventoryTopology, isTestSource, parseArgs, sourceFiles, walkFiles };
