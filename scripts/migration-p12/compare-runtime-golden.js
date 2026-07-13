'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, sha256 } = require('./check-safety');

function compareRuntimeGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixtureRoot = path.join(rootDir, 'fixtures', 'migration', 'p12');
  const files = [
    { key: 'manifest.json', target: path.join(fixtureRoot, 'manifest.json'), root: fixtureRoot },
    { key: 'node-runtime.golden.json', target: path.join(fixtureRoot, 'node-runtime.golden.json'), root: fixtureRoot },
    { key: 'runtime-contract.json', target: path.join(rootDir, 'docs', 'migration', 'p12', 'runtime-contract.json'), root: path.join(rootDir, 'docs', 'migration', 'p12') },
  ];
  const values = {};
  for (const file of files) {
    const safe = inspectEvidenceFile(file.target, file.root);
    if (!safe.ok) return { ok: false, status: 'blocked', code: `fixture_${safe.code}`, file: file.key };
    try { values[file.key] = JSON.parse(fs.readFileSync(file.target, 'utf8')); } catch { return { ok: false, status: 'blocked', code: 'fixture_json_invalid', file: file.key }; }
  }
  const golden = values['node-runtime.golden.json'];
  const contract = values['runtime-contract.json'];
  const manifest = values['manifest.json'];
  if (manifest?.kind !== 'p12-sanitized-node-script-executor-runtime-fixture' || golden?.kind !== 'p12-node-script-executor-runtime-golden' || contract?.kind !== 'p12-script-executor-runtime-compatibility-contract') {
    return { ok: false, status: 'blocked', code: 'fixture_kind_invalid' };
  }
  const goldenActions = Array.isArray(golden.runtime_actions) ? golden.runtime_actions : [];
  const contractActions = Array.isArray(contract.runtime_actions) ? contract.runtime_actions : [];
  if (goldenActions.length !== 5 || contractActions.length !== 5) return { ok: false, status: 'blocked', code: 'runtime_action_count_invalid' };
  const byID = new Map(contractActions.map((item) => [item?.id, item]));
  for (const action of goldenActions) {
    const frozen = byID.get(action?.id);
    if (!action || !/^(?:script|executor)\.(?:run|stop|action)$/.test(action.id) || !frozen ||
        frozen.feature_flag !== action.feature_flag || frozen.legacy_ipc !== action.legacy_ipc || frozen.legacy_return !== action.legacy_return || frozen.go_command !== action.go_command) {
      return { ok: false, status: 'blocked', code: 'runtime_contract_metadata_drift', action: action?.id || null };
    }
  }
  const scenarios = Array.isArray(golden.scenarios) ? golden.scenarios : [];
  if (scenarios.length < 5 || scenarios.some((item) => !item?.go_accept || !Array.isArray(item.sse) || item.sse.length !== 4 || item.sse[0]?.type !== 'operation.queued' || item.sse[3]?.type !== 'project.snapshot')) {
    return { ok: false, status: 'blocked', code: 'operation_sse_matrix_invalid' };
  }
  const fixtureHashes = Object.fromEntries(files.map((file) => [file.key, sha256(fs.readFileSync(file.target))]));
  return { ok: true, status: 'completed', code: 'runtime_golden_matches', action_count: goldenActions.length, scenario_count: scenarios.length, fixture_hashes: fixtureHashes };
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
