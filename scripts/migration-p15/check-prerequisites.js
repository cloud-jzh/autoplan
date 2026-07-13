'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, isWithin, sha256, validateFixtureRoot } = require('./check-safety');
const { checkIpcAllowlist } = require('./check-ipc-allowlist');
const { inventoryTopology } = require('./inventory-topology');

const ROOT = path.resolve(__dirname, '..', '..');
const STAGES = Object.freeze([
  { id: 'p09', evidence: 'p09' }, { id: 'p10', evidence: 'p10' }, { id: 'p11', evidence: 'p11' },
  { id: 'p12', evidence: 'p12' }, { id: 'p13a', evidence: 'p13' }, { id: 'p13b', evidence: 'p13' },
  { id: 'p14', evidence: 'p14' },
]);

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

function stageMatches(stage, summary) {
  const kind = String(summary?.kind || '').toLowerCase();
  return !kind || kind.includes(stage);
}

function acceptedCommandEvidence(summary) {
  const commands = Array.isArray(summary?.command_results) ? summary.command_results :
    Array.isArray(summary?.commandResults) ? summary.commandResults : null;
  if (!commands || commands.length === 0) return { ok: false, code: 'evidence_command_results_missing' };
  for (const command of commands) {
    const exitCode = command?.exit_code ?? command?.exitCode;
    const accepted = command?.accepted === true || command?.evaluation?.accepted === true;
    if (exitCode !== 0 || !accepted) return { ok: false, code: 'evidence_command_nonzero_or_unaccepted' };
  }
  return { ok: true, code: 'evidence_command_results_accepted' };
}

function inspectStageEvidence(rootDir, stage) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', stage.evidence, 'evidence', 'runs');
  const rootInfo = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) return { ok: false, stage: stage.id, code: `${stage.id}_evidence_runs_missing` };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(entry.name)).map((entry) => entry.name).sort().reverse();
  let rejectedCode = null;
  for (const name of names) {
    const runDir = path.join(evidenceRoot, name);
    const summary = readSafeJSON(path.join(runDir, 'summary.json'), runDir);
    const manifest = readSafeJSON(path.join(runDir, 'evidence-manifest.json'), runDir);
    if (!summary || !manifest || summary.ok !== true || (summary.status !== 'completed' && summary.status !== 'passed') || !stageMatches(stage.id, summary) || !verifyManifest(runDir, manifest)) continue;
    const commands = acceptedCommandEvidence(summary);
    if (!commands.ok) { rejectedCode ??= commands.code; continue; }
    return { ok: true, stage: stage.id, code: `${stage.id}_evidence_accepted`, run_id: name, summary_sha256: sha256(JSON.stringify(summary)) };
  }
  return { ok: false, stage: stage.id, code: rejectedCode ? `${stage.id}_${rejectedCode}` : `${stage.id}_evidence_integrity_invalid` };
}

function inspectBackend(rootDir) {
  const required = [
    ['backend/go.mod', 'file'], ['backend/openapi/openapi.yaml', 'file'],
    ['backend/internal/httpapi', 'directory'], ['backend/internal/application', 'directory'], ['backend/internal/repository', 'directory'],
  ];
  const missing = required.filter(([relative, kind]) => {
    const info = fs.lstatSync(path.join(rootDir, relative), { throwIfNoEntry: false });
    return !info || info.isSymbolicLink() || (kind === 'file' && !info.isFile()) || (kind === 'directory' && !info.isDirectory());
  }).map(([relative]) => relative);
  return missing.length ? { ok: false, code: 'go_backend_prerequisite_missing', missing } : { ok: true, code: 'go_backend_prerequisite_present' };
}

function inspectFeatureFlags(rootDir) {
  const files = ['src/main.js', 'src/preload.js', 'backend/internal/config/features.go'];
  let text;
  try { text = files.map((relative) => fs.readFileSync(path.join(rootDir, relative), 'utf8')).join('\n'); }
  catch { return { ok: false, code: 'feature_flag_source_unreadable' }; }
  const flags = ['go_loop_actions', 'go_plan_actions', 'go_task_actions', 'go_acceptance_retry_actions', 'go_scripts_api', 'go_executors_api', 'go_chat_api', 'go_mcp_api', 'go_terminal_api', 'go_agent_cli_runtime'];
  const missing = flags.filter((flag) => !text.includes(flag));
  return missing.length ? { ok: false, code: 'feature_flag_inventory_incomplete', missing } : { ok: true, code: 'feature_flag_inventory_complete', flags };
}

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const backend = inspectBackend(rootDir);
  const features = inspectFeatureFlags(rootDir);
  let predecessorAccepted = true;
  const stages = STAGES.map((stage) => {
    const evidence = inspectStageEvidence(rootDir, stage);
    const ordered = predecessorAccepted && evidence.ok;
    predecessorAccepted = ordered;
    return { ...evidence, ordered, code: ordered ? evidence.code : evidence.ok ? `${stage.id}_order_blocked` : evidence.code };
  });
  const topology = inventoryTopology({ rootDir });
  const ipc = checkIpcAllowlist({ rootDir });
  const failures = [fixture, backend, features, ...stages.filter((stage) => !stage.ordered), topology, ipc]
    .filter((item) => !item.ok).map((item) => item.code);
  return {
    schema_version: 1,
    kind: 'p15-prerequisites',
    status: failures.length ? 'blocked' : 'ready',
    ok: failures.length === 0,
    code: failures[0] || 'p15_prerequisites_ready',
    failures,
    fixture,
    backend,
    features,
    stages,
    topology: { ok: topology.ok, code: topology.code, direct_bridge_accesses: topology.renderer_direct_bridge_access?.length || 0 },
    ipc: { ok: ipc.ok, code: ipc.code, thin_shell_ready: ipc.thin_shell_ready },
  };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, ok: result.ok, code: result.code, failures: result.failures, stages: result.stages.map((stage) => ({ stage: stage.stage, run_id: stage.run_id || null, ordered: stage.ordered })) })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p15_prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { ROOT, STAGES, acceptedCommandEvidence, checkPrerequisites, inspectBackend, inspectFeatureFlags, inspectStageEvidence, parseArgs, verifyManifest };
