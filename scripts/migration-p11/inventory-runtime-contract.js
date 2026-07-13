'use strict';

// P11 freezes the Node runtime as a migration input.  This script deliberately
// performs static inspection only: it never creates an Electron app, opens a
// database, reads userData, or starts an Agent CLI.
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '..', '..');
const CONTRACT_PATH = 'docs/migration/p11/runtime-contract.json';
const RUNTIME_IPC_PREFIXES = Object.freeze(['loop:', 'plans:', 'tasks:', 'acceptance:', 'intake:']);
const SOURCE_FILES = Object.freeze([
  'src/loopService.js',
  'src/loop/runtime.js',
  'src/loop/taskExecution.js',
  'src/loop/planLifecycle.js',
  'src/loop/acceptance.js',
  'src/loop/concurrency.js',
  'src/loop/agentCliRunner.js',
  'src/loop/agentCliConfig.js',
  'src/loop/planAgentCli.js',
  'src/agentCli.js',
  'src/main.js',
  'src/preload.js',
]);

function readUtf8(root, relative) {
  const target = path.resolve(root, relative);
  if (!isWithin(target, root)) throw new Error(`source_outside_root:${relative}`);
  return fs.readFileSync(target, 'utf8');
}

function readContract(root) {
  const text = readUtf8(root, CONTRACT_PATH);
  const value = JSON.parse(text);
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('runtime_contract_invalid');
  return value;
}

function isWithin(candidate, root) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function sourceLine(text, index) {
  return text.slice(0, index).split(/\r?\n/).length;
}

function withoutComments(text) {
  // The inspected patterns are intentionally narrow and never occur in string
  // literals in this surface.  Removing comments prevents explanatory prose
  // from looking like a process launch or mutation.
  return String(text || '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1');
}

function matches(text, expression) {
  return Array.from(text.matchAll(expression));
}

function discoverRuntimeIpcHandlers(mainSource) {
  return matches(mainSource, /ipcMain\.handle\('([^']+)'/g)
    .map((match) => ({ channel: match[1], line: sourceLine(mainSource, match.index) }))
    .filter((entry) => RUNTIME_IPC_PREFIXES.some((prefix) => entry.channel.startsWith(prefix)))
    .sort((left, right) => left.channel.localeCompare(right.channel));
}

function countSpawnCalls(text) {
  return matches(withoutComments(text), /\bspawn\s*\(/g).length;
}

function countDatabaseWrites(text) {
  return matches(text, /\b(?:this\.db|service\.db|db)\.(?:run|runBatch|insert|setSetting)\s*\(/g).length;
}

function countStateTransitions(text) {
  return matches(text, /(?:UPDATE\s+(?:project_states|loop_state|plans|plan_tasks|requirements|feedback)\s+SET\b|\b(?:runtime\.(?:running|busy)|normalized\.phase)\s*=)/gi).length;
}

function normalizeCountEntries(entries, key) {
  if (!Array.isArray(entries)) throw new Error(`${key}_inventory_invalid`);
  const result = new Map();
  for (const entry of entries) {
    if (!entry || typeof entry !== 'object' || typeof entry.source_path !== 'string' || !Number.isInteger(entry.count) || entry.count < 0) {
      throw new Error(`${key}_entry_invalid`);
    }
    if (result.has(entry.source_path)) throw new Error(`${key}_entry_duplicate:${entry.source_path}`);
    result.set(entry.source_path, entry.count);
  }
  return result;
}

function compareCountSurface(name, expected, actual) {
  const failures = [];
  for (const [sourcePath, expectedCount] of expected.entries()) {
    const actualCount = actual.get(sourcePath);
    if (actualCount !== expectedCount) {
      failures.push(`${name}_drift:${sourcePath}:expected=${expectedCount}:actual=${actualCount ?? 'missing'}`);
    }
  }
  for (const sourcePath of actual.keys()) {
    if (!expected.has(sourcePath)) failures.push(`${name}_unclassified:${sourcePath}`);
  }
  return failures;
}

function validateProcessLaunches(contract, sources) {
  const launches = contract?.inventory?.process_launches;
  if (!Array.isArray(launches) || launches.length === 0) return ['process_launch_inventory_invalid'];
  const failures = [];
  const observed = new Set();
  for (const launch of launches) {
    if (!launch || typeof launch.id !== 'string' || typeof launch.source_path !== 'string' || typeof launch.marker !== 'string' || typeof launch.classification !== 'string') {
      failures.push('process_launch_entry_invalid');
      continue;
    }
    const source = sources.get(launch.source_path);
    if (!source || !source.includes(launch.marker)) {
      failures.push(`process_launch_missing:${launch.id}`);
      continue;
    }
    const key = `${launch.source_path}\u0000${launch.marker}`;
    if (observed.has(key)) failures.push(`process_launch_duplicate:${launch.id}`);
    observed.add(key);
  }
  return failures;
}

function validateIpcSurface(contract, actualHandlers) {
  const expected = contract?.inventory?.runtime_ipc_handlers;
  if (!Array.isArray(expected)) return ['runtime_ipc_inventory_invalid'];
  const expectedSet = new Set(expected);
  if (expectedSet.size !== expected.length || expected.some((channel) => typeof channel !== 'string')) return ['runtime_ipc_inventory_duplicate_or_invalid'];
  const actualSet = new Set(actualHandlers.map((entry) => entry.channel));
  const failures = [];
  for (const channel of actualSet) if (!expectedSet.has(channel)) failures.push(`runtime_ipc_unclassified:${channel}`);
  for (const channel of expectedSet) if (!actualSet.has(channel)) failures.push(`runtime_ipc_missing:${channel}`);
  return failures;
}

function validateActionContract(contract) {
  const actions = contract.actions;
  if (!Array.isArray(actions) || actions.length === 0) return ['action_contract_invalid'];
  const failures = [];
  const ids = new Set();
  for (const action of actions) {
    if (!action || typeof action.id !== 'string' || ids.has(action.id)) {
      failures.push('action_id_invalid_or_duplicate');
      continue;
    }
    ids.add(action.id);
    const required = ['feature_flag', 'go_application_service', 'rest_adapter', 'mcp_adapter', 'ui_adapter', 'node_fallback', 'database_owner', 'legacy_return', 'go_return'];
    for (const field of required) {
      if (typeof action[field] !== 'string' || !action[field]) failures.push(`action_field_missing:${action.id}:${field}`);
    }
    if (action.database_owner !== 'go') failures.push(`action_database_owner_not_go:${action.id}`);
    if (action.go_return !== '202_operation_accepted') failures.push(`action_go_return_not_operation:${action.id}`);
  }
  return failures;
}

function assertSanitizedContract(contract) {
  const text = JSON.stringify(contract);
  const unsafe = [
    /(?:^|[^A-Za-z])(?:[A-Z]:[\\/]|\\\\[^\\/]+[\\/])/i,
    /(?:^|[^A-Za-z])\/(?:Users|home|private|var)\//i,
    /(?:authorization|cookie)\s*[:=]\s*(?!<redacted>)/i,
    /(?:sk-|ghp_|xox[baprs]-)[A-Za-z0-9_-]{8,}/,
  ];
  for (const expression of unsafe) {
    if (expression.test(text)) throw new Error('runtime_contract_contains_sensitive_or_absolute_value');
  }
}

function collectInventory(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const contract = options.contract || readContract(root);
  assertSanitizedContract(contract);
  const sources = new Map(SOURCE_FILES.map((relative) => [relative, readUtf8(root, relative)]));
  const runtimeIpcHandlers = discoverRuntimeIpcHandlers(sources.get('src/main.js'));
  const spawnCounts = new Map(SOURCE_FILES.map((relative) => [relative, countSpawnCalls(sources.get(relative))]));
  const databaseWriteCounts = new Map(SOURCE_FILES.map((relative) => [relative, countDatabaseWrites(sources.get(relative))]));
  const stateTransitionCounts = new Map(SOURCE_FILES.map((relative) => [relative, countStateTransitions(sources.get(relative))]));
  const failures = [
    ...validateIpcSurface(contract, runtimeIpcHandlers),
    ...validateProcessLaunches(contract, sources),
    ...validateActionContract(contract),
    ...compareCountSurface('process_spawn', normalizeCountEntries(contract.inventory.process_spawn_counts, 'process_spawn'), spawnCounts),
    ...compareCountSurface('database_write', normalizeCountEntries(contract.inventory.database_write_counts, 'database_write'), databaseWriteCounts),
    ...compareCountSurface('state_transition', normalizeCountEntries(contract.inventory.state_transition_counts, 'state_transition'), stateTransitionCounts),
  ];
  return {
    schema_version: 1,
    kind: 'p11-node-runtime-contract-inventory',
    ok: failures.length === 0,
    failures,
    runtime_ipc_handlers: runtimeIpcHandlers,
    process_spawn_counts: Object.fromEntries(spawnCounts),
    database_write_counts: Object.fromEntries(databaseWriteCounts),
    state_transition_counts: Object.fromEntries(stateTransitionCounts),
  };
}

if (require.main === module) {
  try {
    const report = collectInventory();
    process.stdout.write(`${JSON.stringify(report)}\n`);
    process.exitCode = report.ok ? 0 : 2;
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ schema_version: 1, kind: 'p11-node-runtime-contract-inventory', ok: false, failures: [error?.message || 'inventory_failed'] })}\n`);
    process.exitCode = 2;
  }
}

module.exports = {
  CONTRACT_PATH,
  ROOT,
  RUNTIME_IPC_PREFIXES,
  SOURCE_FILES,
  assertSanitizedContract,
  collectInventory,
  countDatabaseWrites,
  countSpawnCalls,
  countStateTransitions,
  discoverRuntimeIpcHandlers,
  readContract,
};
