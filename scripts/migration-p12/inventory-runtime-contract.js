'use strict';

// P12 freezes the legacy Script/Executor runtime boundary before those
// process-owning paths move to Go. This is deliberately static inspection:
// it never opens sql.js, launches Electron, starts a process, or reads an
// external workspace.
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '..', '..');
const CONTRACT_PATH = 'docs/migration/p12/runtime-contract.json';
const SOURCE_FILES = Object.freeze([
  'src/loop/scriptHooks.js',
  'src/executors/executorRunner.js',
  'src/executors/executorConfig.js',
  'src/executors/executorStore.js',
  'src/loop/snapshots.js',
  'src/mcpTools.js',
  'src/main.js',
  'src/preload.js',
  'src/renderer/types.ts',
]);

function isWithin(candidate, root) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function readUtf8(root, relative) {
  const target = path.resolve(root, relative);
  if (!isWithin(target, root)) throw new Error(`source_outside_root:${relative}`);
  return fs.readFileSync(target, 'utf8');
}

function readContract(root = ROOT) {
  const parsed = JSON.parse(readUtf8(root, CONTRACT_PATH));
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('runtime_contract_invalid');
  return parsed;
}

function sourceLine(source, index) {
  return source.slice(0, index).split(/\r?\n/).length;
}

function assertSanitizedContract(contract) {
  const text = JSON.stringify(contract);
  const forbidden = [
    /(?:^|[^A-Za-z])(?:[A-Z]:[\\/]|\\\\[^\\/]+[\\/])/i,
    /(?:^|[^A-Za-z])\/(?:Users|home|private|var)\//i,
    /(?:sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9_-]{8,}/,
    /(?:authorization|cookie)\s*[:=]\s*(?!<redacted>)/i,
  ];
  for (const expression of forbidden) {
    if (expression.test(text)) throw new Error('runtime_contract_contains_sensitive_or_absolute_value');
  }
}

function assertSourceMarkers(contract, sources) {
  const expected = contract?.inventory?.source_markers;
  if (!expected || typeof expected !== 'object' || Array.isArray(expected)) {
    throw new Error('runtime_source_marker_inventory_invalid');
  }
  const expectedPaths = Object.keys(expected).sort();
  const sourcePaths = [...sources.keys()].sort();
  if (JSON.stringify(expectedPaths) !== JSON.stringify(sourcePaths)) {
    throw new Error('runtime_source_marker_scope_drift');
  }

  const evidence = [];
  for (const relative of sourcePaths) {
    const markers = expected[relative];
    if (!Array.isArray(markers) || markers.length === 0 || markers.some((marker) => typeof marker !== 'string' || !marker)) {
      throw new Error(`runtime_source_marker_invalid:${relative}`);
    }
    if (new Set(markers).size !== markers.length) throw new Error(`runtime_source_marker_duplicate:${relative}`);
    const source = sources.get(relative);
    const missing = markers.filter((marker) => !source.includes(marker));
    if (missing.length) throw new Error(`runtime_source_marker_missing:${relative}:${missing.join(',')}`);
    evidence.push({
      path: relative,
      markers: markers.map((marker) => ({ marker, line: sourceLine(source, source.indexOf(marker)) })),
    });
  }
  return evidence;
}

function validateContract(contract) {
  const failures = [];
  if (contract?.schema_version !== 1 || contract?.kind !== 'p12-script-executor-runtime-compatibility-contract') {
    failures.push('runtime_contract_header_invalid');
  }
  const scope = Array.isArray(contract?.scope) ? [...contract.scope].sort() : [];
  if (JSON.stringify(scope) !== JSON.stringify([...SOURCE_FILES].sort())) failures.push('runtime_contract_scope_invalid');

  const ownership = contract?.migration_boundary?.runtime_ownership;
  if (!ownership || ownership.script_feature_flag !== 'go_scripts_api' || ownership.executor_feature_flag !== 'go_executors_api' ||
      ownership.owner_is_fixed_until_terminal !== true || ownership.forbid_cross_runtime_takeover !== true) {
    failures.push('runtime_ownership_contract_invalid');
  }

  const forbiddenInputs = contract?.authorization_boundary?.untrusted_client_fields;
  const requiredForbidden = ['manual', 'command', 'args', 'shell', 'cwd', 'env', 'pid', 'operation_id'];
  if (!Array.isArray(forbiddenInputs) || requiredForbidden.some((field) => !forbiddenInputs.includes(field))) {
    failures.push('runtime_authorization_contract_invalid');
  }

  const operation = contract?.operation_and_sse;
  if (!operation || operation.accepted_status !== 'queued' || !Array.isArray(operation.required_operation_fields) ||
      !operation.required_operation_fields.includes('operation_id') || !Array.isArray(operation.sse_event_order)) {
    failures.push('runtime_operation_contract_invalid');
  }
  return failures;
}

function collectInventory(options = {}) {
  const root = path.resolve(options.rootDir || ROOT);
  const contract = options.contract || readContract(root);
  assertSanitizedContract(contract);
  const sources = new Map(SOURCE_FILES.map((relative) => [relative, readUtf8(root, relative)]));
  const failures = validateContract(contract);
  let sourceEvidence = [];
  try {
    sourceEvidence = assertSourceMarkers(contract, sources);
  } catch (error) {
    failures.push(error?.message || 'runtime_source_inventory_failed');
  }
  return {
    schema_version: 1,
    kind: 'p12-node-runtime-contract-inventory',
    ok: failures.length === 0,
    failures,
    source_evidence: sourceEvidence,
    runtime_actions: contract?.runtime_actions || [],
  };
}

if (require.main === module) {
  try {
    const report = collectInventory();
    process.stdout.write(`${JSON.stringify(report)}\n`);
    process.exitCode = report.ok ? 0 : 2;
  } catch (error) {
    process.stdout.write(`${JSON.stringify({ schema_version: 1, kind: 'p12-node-runtime-contract-inventory', ok: false, failures: [error?.message || 'inventory_failed'] })}\n`);
    process.exitCode = 2;
  }
}

module.exports = {
  CONTRACT_PATH,
  ROOT,
  SOURCE_FILES,
  assertSanitizedContract,
  assertSourceMarkers,
  collectInventory,
  readContract,
  validateContract,
};
