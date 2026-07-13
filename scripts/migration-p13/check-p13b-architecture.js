'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, isWithin, sha256, validateFixtureRoot } = require('./check-p13b-safety');

const MCP_SOURCES = [
  'backend/internal/mcp/server.go', 'backend/internal/mcp/http_transport.go', 'backend/internal/mcp/stdio_transport.go',
  'backend/internal/mcp/registry.go', 'backend/internal/mcp/config.go', 'backend/internal/mcp/auth.go', 'backend/internal/mcp/audit.go',
  'backend/internal/mcp/tools/catalog.go', 'backend/internal/mcp/tools/handlers.go', 'backend/internal/mcp/tools/mapper.go',
];
const P13B_TEST_SOURCES = [
  'backend/internal/mcp/cross_transport_contract_test.go', 'backend/internal/mcp/authorization_test.go',
  'backend/internal/mcp/audit_test.go', 'backend/internal/mcp/architecture_test.go',
  'backend/internal/httpapi/cross_transport_fixture_test.go', 'scripts/migration-p13/verify-p13b.test.js',
];

function readSources(rootDir, files) {
  try { return files.map((file) => ({ file, text: fs.readFileSync(path.join(rootDir, file), 'utf8') })); }
  catch { return null; }
}

function inspectP13BArchitecture(rootDir) {
  const sources = readSources(rootDir, MCP_SOURCES);
  if (!sources) return { ok: false, code: 'p13b_source_unreadable' };
  const tools = sources.find((item) => item.file.endsWith('/tools/handlers.go'))?.text || '';
  let bootstrap;
  let features;
  try {
    bootstrap = fs.readFileSync(path.join(rootDir, 'backend/internal/bootstrap/dependencies.go'), 'utf8');
    features = fs.readFileSync(path.join(rootDir, 'backend/internal/config/features.go'), 'utf8');
  } catch {
    return { ok: false, code: 'p13b_shared_container_source_unreadable' };
  }
  const forbidden = /internal\/(?:repository\/sqlite|runtime\/process)|database\/sql|os\.(?:Open|ReadFile|WriteFile)|exec\.Command/;
  if (forbidden.test(tools)) return { ok: false, code: 'p13b_tools_forbidden_dependency' };
  const required = ['type Factory', 'func NewFactory', 'func (factory *Factory) Handler', 'mcp_tool_unavailable', 'go_mcp_api'];
  const combined = `${sources.map((item) => item.text).join('\n')}\n${features}`;
  const missing = required.filter((needle) => !combined.includes(needle));
  if (missing.length) return { ok: false, code: 'p13b_shared_adapter_missing', missing };
  if (!bootstrap.includes('MCPRegistry') || !bootstrap.includes('MCP')) return { ok: false, code: 'p13b_shared_container_missing' };
  if (bootstrap.includes('NewFrozenRegistry(nil)')) return { ok: false, code: 'p13b_adapter_factory_unwired' };
  return { ok: true, code: 'p13b_architecture_accepted', source_hash: sha256(sources.map((item) => item.text).join('\n')) };
}

function inspectTestPolicy(rootDir) {
  for (const relative of P13B_TEST_SOURCES) {
    const target = path.join(rootDir, relative);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'p13b_test_source_invalid', file: relative };
    let text;
    try { text = fs.readFileSync(target, 'utf8'); } catch { return { ok: false, code: 'p13b_test_source_unreadable', file: relative }; }
    if (/\b(?:describe|it|test)\s*\.\s*(?:skip|only)\s*\(/.test(text) || /(?:^|\s)--(?:run|test-name-pattern)\b/.test(text)) return { ok: false, code: 'p13b_test_filter_forbidden', file: relative };
  }
  return { ok: true, code: 'p13b_test_policy_accepted', files: P13B_TEST_SOURCES.length };
}

function inspectStageEvidence(rootDir, stage, allowP00) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs');
  const info = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return { ok: false, stage, code: `${stage}_evidence_runs_missing` };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(entry.name))
    .map((entry) => entry.name)
    .sort()
    .reverse();
  for (const name of names) {
    const runDir = path.join(evidenceRoot, name);
    const summaryPath = path.join(runDir, 'summary.json');
    const manifestPath = path.join(runDir, 'evidence-manifest.json');
    const summary = readSafeJSON(summaryPath, runDir);
    const manifest = readSafeJSON(manifestPath, runDir);
    if (!summary || !manifest || !verifyManifest(runDir, manifest) || summary.ok !== true || (summary.status !== 'completed' && summary.status !== 'passed')) continue;
    if (!(summary.source_hashes_stable === true || summary.sourceHashesStable === true)) continue;
    if (allowP00 && !p00BaselineAccepted(summary)) continue;
    return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: name, summary_sha256: sha256(fs.readFileSync(summaryPath)) };
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
    const safety = inspectEvidenceFile(target, runDir);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    return isWithin(target, runDir) && Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === item.bytes && safety.ok && safety.sha256 === item.sha256);
  });
}

function p00BaselineAccepted(summary) {
  const commands = Array.isArray(summary.commandResults) ? summary.commandResults : [];
  const check = commands.find((item) => item?.id === 'check');
  return check?.expectedOutcome === 'exact-known-failure' && check?.evaluation?.accepted === true;
}

function checkP13BPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const p00 = inspectStageEvidence(rootDir, 'p00', true);
  const stages = ['p10', 'p11', 'p12'].map((stage) => inspectStageEvidence(rootDir, stage, false));
  const architecture = inspectP13BArchitecture(rootDir);
  const tests = inspectTestPolicy(rootDir);
  const failures = [fixture, p00, ...stages, architecture, tests].filter((item) => !item.ok).map((item) => item.code);
  return { schema_version: 1, status: failures.length ? 'blocked' : 'ready', ok: failures.length === 0, code: failures[0] || 'p13b_prerequisites_ready', failures, fixture, p00, stages, architecture, test_policy: tests };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkP13BPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, code: result.code, failures: result.failures, stages: result.stages.map((item) => ({ stage: item.stage, run_id: item.run_id || null })), architecture: result.architecture })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"p13b_prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { MCP_SOURCES, P13B_TEST_SOURCES, checkP13BPrerequisites, inspectP13BArchitecture, inspectStageEvidence, inspectTestPolicy, parseArgs, verifyManifest };
