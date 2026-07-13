'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, isWithin, sha256, validateFixtureRoot } = require('./check-safety');

const REQUIRED_STAGE = 'p10';
const P11_TEST_SOURCES = [
  'backend/internal/application/runtime/golden_test.go',
  'backend/internal/application/runtime/concurrency_test.go',
  'backend/internal/application/runtime/recovery_test.go',
  'backend/internal/runtime/process/process_tree_integration_test.go',
  'backend/internal/runtime/agentcli/contract_test.go',
  'backend/internal/httpapi/runtime_contract_test.go',
  'src/data/goDataClient.runtime.test.js',
  'src/renderer/lib/api/runtimeTransport.contract.test.ts',
];

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const p00 = inspectP00Baseline(rootDir);
  const p10 = inspectStageEvidence(rootDir, REQUIRED_STAGE);
  const owner = inspectGoOwnerBoundary(rootDir);
  const runtimeFixture = inspectFrozenRuntimeFixture(rootDir);
  const testPolicy = inspectTestPolicy(rootDir);
  const failures = [fixture, p00, p10, owner, runtimeFixture, testPolicy].filter((item) => !item.ok).map((item) => item.code);
  return {
    schema_version: 1,
    status: failures.length ? 'blocked' : 'ready',
    ok: failures.length === 0,
    code: failures[0] || 'prerequisites_ready',
    failures,
    fixture,
    p00,
    stages: [p10],
    owner,
    runtime_fixture: runtimeFixture,
    test_policy: testPolicy,
  };
}

function inspectP00Baseline(rootDir) {
  const result = inspectLatestRun(path.join(rootDir, 'docs', 'migration', 'p00', 'evidence', 'runs'));
  if (!result.ok) return { ...result, code: `p00_${result.code}` };
  const commands = Array.isArray(result.summary?.commandResults) ? result.summary.commandResults : [];
  const check = commands.find((item) => item?.id === 'check');
  const test = commands.find((item) => item?.id === 'test');
  const expectedRed = check?.expectedOutcome === 'exact-known-failure' && check?.exitCode !== 0 &&
    Array.isArray(check?.failureSignatures) && check.failureSignatures.includes('file-length|scripts/smoke-test.js|limit=3800');
  if (!result.summary?.ok || !result.summary?.sourceHashesStable || !result.summary?.expectationsHashStable || !expectedRed ||
      check?.evaluation?.accepted !== true || test?.evaluation?.accepted !== true) {
    return { ok: false, code: 'p00_baseline_signature_invalid' };
  }
  return {
    ok: true,
    code: 'p00_baseline_accepted',
    run_id: result.run_id,
    summary_sha256: result.summary_sha256,
    check_failure_signatures: check.failureSignatures,
  };
}

function inspectStageEvidence(rootDir, stage) {
  const result = inspectLatestRun(path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs'));
  if (!result.ok) return { ...result, stage, code: `${stage}_${result.code}` };
  if (result.summary?.status !== 'completed' || result.summary?.ok !== true ||
      (result.summary?.sourceHashesStable !== true && result.summary?.source_hashes_stable !== true)) {
    return { ok: false, stage, code: `${stage}_summary_not_completed` };
  }
  return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: result.run_id, summary_sha256: result.summary_sha256 };
}

function inspectLatestRun(evidenceRoot) {
  const rootInfo = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) return { ok: false, code: 'evidence_runs_missing' };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(entry.name))
    .map((entry) => entry.name).sort().reverse();
  for (const runID of names) {
    const runDir = path.join(evidenceRoot, runID);
    const summaryPath = path.join(runDir, 'summary.json');
    const manifestPath = path.join(runDir, 'evidence-manifest.json');
    const summary = readSafeJSON(summaryPath, runDir);
    const manifest = readSafeJSON(manifestPath, runDir);
    if (!summary || !manifest || manifest.immutable_run_directory !== true || !verifyManifest(runDir, manifest)) continue;
    return { ok: true, code: 'evidence_run_valid', run_id: runID, summary, summary_sha256: sha256(fs.readFileSync(summaryPath)) };
  }
  return { ok: false, code: 'evidence_integrity_invalid' };
}

function readSafeJSON(file, root) {
  if (!inspectEvidenceFile(file, root).ok) return null;
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return null; }
}

function verifyManifest(runDir, manifest) {
  if (!Array.isArray(manifest?.artifacts) || manifest.artifacts.length === 0) return false;
  return manifest.artifacts.every((item) => {
    if (!item || typeof item.path !== 'string' || !/^[A-Za-z0-9._/-]+$/.test(item.path) ||
        !/^[a-f0-9]{64}$/.test(item.sha256 || '') || !Number.isInteger(item.bytes) || item.bytes < 0) return false;
    const target = path.resolve(runDir, item.path);
    const safe = inspectEvidenceFile(target, runDir);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    return isWithin(target, runDir) && Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === item.bytes && safe.ok && safe.sha256 === item.sha256);
  });
}

function inspectGoOwnerBoundary(rootDir) {
  const files = ['src/main.js', 'src/data/databaseOwnerGuard.js', 'src/data/goDataClient.js', 'src/loopService.js'];
  let text;
  try { text = files.map((file) => fs.readFileSync(path.join(rootDir, file), 'utf8')).join('\n'); } catch { return { ok: false, code: 'go_owner_source_unreadable' }; }
  const required = ['DATABASE_OWNERS.GO', 'createGoModeDatabaseBlocker', 'assertGoDataClientFallbackAllowed', 'NODE_SQL_FORBIDDEN', 'assertNodeMutationAllowed', 'never falls back to IPC or sql.js'];
  const missing = required.filter((value) => !text.includes(value));
  return missing.length ? { ok: false, code: 'go_owner_boundary_invalid', missing } : { ok: true, code: 'go_owner_boundary_accepted', owner: 'go', writer_count: 1, node_writer: false };
}

function inspectFrozenRuntimeFixture(rootDir) {
  const fixtureRoot = path.join(rootDir, 'fixtures', 'migration', 'p11');
  const required = [
    { name: 'manifest.json', root: fixtureRoot },
    { name: 'node-runtime.golden.json', root: fixtureRoot },
    { name: 'state-machine-cases.json', root: fixtureRoot },
    { name: 'docs/migration/p11/runtime-contract.json', root: path.join(rootDir, 'docs', 'migration', 'p11') },
  ];
  const records = [];
  for (const requiredFile of required) {
    const { name, root } = requiredFile;
    const target = name.startsWith('docs/') ? path.join(rootDir, name) : path.join(root, name);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'runtime_fixture_missing', file: name };
    const safety = inspectEvidenceFile(target, root);
    if (!safety.ok) return { ok: false, code: 'runtime_fixture_sensitive', file: name };
    records.push({ name, sha256: safety.sha256 });
  }
  return { ok: true, code: 'runtime_fixture_accepted', artifacts: records };
}

function inspectTestPolicy(rootDir) {
  for (const relative of P11_TEST_SOURCES) {
    const target = path.join(rootDir, relative);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'p11_test_source_invalid', file: relative };
    let text;
    try { text = fs.readFileSync(target, 'utf8'); } catch { return { ok: false, code: 'p11_test_source_unreadable', file: relative }; }
    if (/\b(?:describe|it|test)\s*\.\s*(?:skip|only)\s*\(/.test(text) || /(?:^|\s)--(?:run|test-name-pattern)\b/.test(text)) {
      return { ok: false, code: 'p11_test_filter_forbidden', file: relative };
    }
  }
  return { ok: true, code: 'p11_test_policy_accepted', files: P11_TEST_SOURCES.length };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) {
    throw new Error('usage: node scripts/migration-p11/check-prerequisites.js --fixture-root <authorized-p11-fixture>');
  }
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, code: result.code, failures: result.failures,
      p00: {
        run_id: result.p00?.run_id || null,
        summary_sha256: result.p00?.summary_sha256 || null,
        check_failure_signatures: result.p00?.check_failure_signatures || [],
      },
      stages: result.stages.map((stage) => ({ stage: stage.stage, run_id: stage.run_id || null, summary_sha256: stage.summary_sha256 || null })),
      owner: { owner: result.owner?.owner || null, writer_count: result.owner?.writer_count ?? null },
      test_policy: { files: result.test_policy?.files ?? null },
    })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { P11_TEST_SOURCES, checkPrerequisites, inspectFrozenRuntimeFixture, inspectGoOwnerBoundary, inspectLatestRun, inspectP00Baseline, inspectStageEvidence, inspectTestPolicy, parseArgs, verifyManifest };
