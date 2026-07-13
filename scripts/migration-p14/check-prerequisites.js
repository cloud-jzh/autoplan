'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, isWithin, sha256, validateFixtureRoot } = require('./check-safety');

const P14_TEST_SOURCES = [
  'backend/internal/runtime/terminal/pty_contract_test.go',
  'backend/internal/runtime/terminal/process_tree_integration_test.go',
  'backend/internal/application/terminal/integration_test.go',
  'backend/internal/application/terminal/replay_test.go',
  'backend/internal/httpapi/terminal_contract_test.go',
  'backend/internal/httpapi/terminal_ws_integration_test.go',
  'backend/internal/httpapi/terminal_security_test.go',
  'src/renderer/lib/api/terminalTransport.contract.test.ts',
  'src/renderer/hooks/useTerminalSessions.test.ts',
  'scripts/migration-p14/check-prerequisites.js',
  'scripts/migration-p14/check-safety.js',
  'scripts/migration-p14/smoke-packaged-terminal.js',
  'scripts/migration-p14/verify.js',
  'scripts/migration-p14/verify.test.js',
];

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const stages = ['p00', 'p11', 'p13'].map((stage) => inspectStageEvidence(rootDir, stage));
  const owner = inspectOwnerBoundary(rootDir);
  const tests = inspectTestPolicy(rootDir);
  const failures = [fixture, ...stages, owner, tests].filter((item) => !item.ok).map((item) => item.code);
  return { schema_version: 1, status: failures.length ? 'blocked' : 'ready', ok: failures.length === 0, code: failures[0] || 'prerequisites_ready', failures, fixture, stages, owner, test_policy: tests };
}

function inspectStageEvidence(rootDir, stage) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs');
  const rootInfo = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) return { ok: false, stage, code: `${stage}_evidence_runs_missing` };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true }).filter((item) => item.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(item.name)).map((item) => item.name).sort().reverse();
  for (const name of names) {
    const runDir = path.join(evidenceRoot, name);
    const summary = readSafeJSON(path.join(runDir, 'summary.json'), runDir);
    const manifest = readSafeJSON(path.join(runDir, 'evidence-manifest.json'), runDir);
    if (!summary || !manifest || summary.ok !== true || (summary.status !== 'completed' && summary.status !== 'passed') || !verifyManifest(runDir, manifest)) continue;
    return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: name, summary_sha256: sha256(JSON.stringify(summary)) };
  }
  return { ok: false, stage, code: `${stage}_evidence_integrity_invalid` };
}

function readSafeJSON(file, root) {
  if (!inspectEvidenceFile(file, root).ok) return null;
  try { return JSON.parse(fs.readFileSync(file, 'utf8')); } catch { return null; }
}

function verifyManifest(runDir, manifest) {
  if (manifest?.immutable_run_directory !== true || !Array.isArray(manifest.artifacts) || manifest.artifacts.length === 0) return false;
  return manifest.artifacts.every((item) => {
    if (!item || typeof item.path !== 'string' || !/^[A-Za-z0-9._/-]+$/.test(item.path) || !/^[a-f0-9]{64}$/.test(item.sha256 || '') || !Number.isInteger(item.bytes) || item.bytes < 0) return false;
    const target = path.resolve(runDir, item.path);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    const safe = inspectEvidenceFile(target, runDir);
    return isWithin(target, runDir) && Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === item.bytes && safe.ok && safe.sha256 === item.sha256);
  });
}

function inspectOwnerBoundary(rootDir) {
  const files = ['src/main.js', 'src/preload.js', 'src/renderer/lib/api/terminalTransport.ts', 'backend/internal/config/features.go'];
  let text;
  try { text = files.map((file) => fs.readFileSync(path.join(rootDir, file), 'utf8')).join('\n'); } catch { return { ok: false, code: 'terminal_owner_source_unreadable' }; }
  const required = ['go_terminal_api', 'runtime: copied.runtime', 'terminal_session_not_found', 'FeatureGoTerminalAPI'];
  const missing = required.filter((value) => !text.includes(value));
  return missing.length ? { ok: false, code: 'terminal_owner_boundary_invalid', missing } : { ok: true, code: 'terminal_owner_boundary_accepted', terminal_owner: 'session_runtime_fixed' };
}

function inspectTestPolicy(rootDir) {
  for (const relative of P14_TEST_SOURCES) {
    const target = path.join(rootDir, relative);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'p14_test_source_invalid', file: relative };
    let text;
    try { text = fs.readFileSync(target, 'utf8'); } catch { return { ok: false, code: 'p14_test_source_unreadable', file: relative }; }
    if (/\b(?:describe|it|test)\s*\.\s*(?:skip|only)\s*\(/.test(text) || /(?:^|\s)--(?:run|test-name-pattern)\b/.test(text)) return { ok: false, code: 'p14_test_filter_forbidden', file: relative };
  }
  return { ok: true, code: 'p14_test_policy_accepted', files: P14_TEST_SOURCES.length };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, code: result.code, failures: result.failures, stages: result.stages.map((item) => ({ stage: item.stage, run_id: item.run_id || null })), owner: result.owner })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p14_prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { P14_TEST_SOURCES, checkPrerequisites, inspectOwnerBoundary, inspectStageEvidence, inspectTestPolicy, parseArgs, verifyManifest };
