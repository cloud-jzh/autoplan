'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, sha256 } = require('./check-safety');

function compareRuntimeGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixtureRoot = path.join(rootDir, 'fixtures', 'migration', 'p11');
  const files = [
    { key: 'node-runtime.golden.json', target: path.join(fixtureRoot, 'node-runtime.golden.json'), root: fixtureRoot },
    { key: 'state-machine-cases.json', target: path.join(fixtureRoot, 'state-machine-cases.json'), root: fixtureRoot },
    { key: 'runtime-contract.json', target: path.join(rootDir, 'docs', 'migration', 'p11', 'runtime-contract.json'), root: path.join(rootDir, 'docs', 'migration', 'p11') },
    { key: 'manifest.json', target: path.join(fixtureRoot, 'manifest.json'), root: fixtureRoot },
  ];
  const values = {};
  for (const file of files) {
    const safety = inspectEvidenceFile(file.target, file.root);
    if (!safety.ok) return { ok: false, status: 'blocked', code: `fixture_${safety.code}`, file: file.key };
    try { values[file.key] = JSON.parse(fs.readFileSync(file.target, 'utf8')); } catch { return { ok: false, status: 'blocked', code: 'fixture_json_invalid', file: file.key }; }
  }
  if (values['node-runtime.golden.json']?.kind !== 'p11-node-runtime-golden' ||
      values['state-machine-cases.json']?.kind !== 'p11-runtime-state-machine-matrix' ||
      values['runtime-contract.json']?.kind !== 'p11-node-runtime-compatibility-contract' ||
      values['manifest.json']?.kind !== 'p11-sanitized-node-runtime-fixture') {
    return { ok: false, status: 'blocked', code: 'fixture_kind_invalid' };
  }
  const goldenActions = values['node-runtime.golden.json'].runtime_actions;
  const matrixCases = values['state-machine-cases.json'].cases;
  if (!Array.isArray(goldenActions) || !Array.isArray(matrixCases)) return { ok: false, status: 'blocked', code: 'runtime_actions_invalid' };
  const ids = new Set();
  for (const item of goldenActions) {
    if (!item || typeof item.id !== 'string' || !/^[a-z][a-z0-9._-]{2,127}$/.test(item.id) ||
        typeof item.feature_flag !== 'string' || !/^go_[a-z0-9_]+$/.test(item.feature_flag) || ids.has(item.id)) {
      return { ok: false, status: 'blocked', code: 'golden_action_invalid' };
    }
    ids.add(item.id);
  }
  const matrixActions = new Set(matrixCases.map((item) => item?.operation));
  const missing = [...ids].filter((id) => !matrixActions.has(id));
  if (missing.length) return { ok: false, status: 'blocked', code: 'state_machine_action_missing', missing };
  const contractActions = new Set((values['runtime-contract.json'].actions || []).map((item) => item?.id));
  const contractMissing = [...ids].filter((id) => !contractActions.has(id));
  if (contractMissing.length) return { ok: false, status: 'blocked', code: 'runtime_contract_action_missing', missing: contractMissing };
  const contractByID = new Map((values['runtime-contract.json'].actions || []).map((item) => [item?.id, item]));
  for (const golden of goldenActions) {
    const contract = contractByID.get(golden.id);
    if (!contract || contract.feature_flag !== golden.feature_flag || contract.legacy_ipc !== golden.legacy_ipc ||
        contract.legacy_return !== golden.legacy_return || contract.go_return !== golden.go_return ||
        contract.database_owner !== 'go' || typeof contract.go_application_service !== 'string' || contract.go_application_service === '' ||
        typeof contract.rest_adapter !== 'string' || contract.rest_adapter === '' || typeof contract.mcp_adapter !== 'string' ||
        typeof contract.ui_adapter !== 'string' || contract.ui_adapter === '') {
      return { ok: false, status: 'blocked', code: 'runtime_contract_metadata_drift', action: golden.id };
    }
  }
  const expectedBackoff = values['node-runtime.golden.json']?.retry_and_recovery?.task_retry_backoff_seconds;
  const matrixBackoff = matrixCases.find((item) => item?.id === 'task_retry_backoff_schedule')?.backoff_seconds;
  if (!Array.isArray(expectedBackoff) || JSON.stringify(expectedBackoff) !== JSON.stringify(matrixBackoff)) {
    return { ok: false, status: 'blocked', code: 'retry_backoff_matrix_drift' };
  }
  const hashes = Object.fromEntries(files.map((file) => [file.key, sha256(fs.readFileSync(file.target))]));
  return { ok: true, status: 'completed', code: 'runtime_golden_matches', action_count: ids.size, fixture_hashes: hashes };
}

if (require.main === module) {
  try {
    const result = compareRuntimeGolden();
    process.stdout.write(`${JSON.stringify(result)}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"ok":false,"status":"blocked","code":"runtime_golden_compare_failed"}\n');
    process.exitCode = 2;
  }
}

module.exports = { compareRuntimeGolden };
