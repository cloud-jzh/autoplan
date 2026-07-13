'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { inspectEvidenceFile, isWithin, sha256, validateFixtureRoot } = require('./check-p13a-safety');

const P13A_TEST_SOURCES = [
  'backend/internal/application/chat/integration_test.go',
  'backend/internal/httpapi/chat_contract_test.go',
  'backend/internal/httpapi/chat_sse_integration_test.go',
  'backend/internal/runtime/agentcli/chat_security_test.go',
  'src/renderer/lib/api/chatTransport.contract.test.ts',
  'src/renderer/hooks/useChat.test.ts',
  'src/renderer/hooks/useChatQueue.test.ts',
];

function checkPrerequisites(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '..', '..'));
  const fixture = validateFixtureRoot(options.fixtureRoot);
  const p00 = inspectStageEvidence(rootDir, 'p00', true);
  const stages = ['p10', 'p11', 'p12'].map((stage) => inspectStageEvidence(rootDir, stage, false));
  const owner = inspectGoOwnerBoundary(rootDir);
  const renderer = inspectRendererBoundary(rootDir);
  const tests = inspectTestPolicy(rootDir);
  const failures = [fixture, p00, ...stages, owner, renderer, tests].filter((item) => !item.ok).map((item) => item.code);
  return { schema_version: 1, status: failures.length ? 'blocked' : 'ready', ok: failures.length === 0, code: failures[0] || 'prerequisites_ready', failures, fixture, p00, stages, owner, renderer, test_policy: tests };
}

function inspectStageEvidence(rootDir, stage, allowP00) {
  const evidenceRoot = path.join(rootDir, 'docs', 'migration', stage, 'evidence', 'runs');
  const info = fs.lstatSync(evidenceRoot, { throwIfNoEntry: false });
  if (!info?.isDirectory() || info.isSymbolicLink()) return { ok: false, stage, code: `${stage}_evidence_runs_missing` };
  const names = fs.readdirSync(evidenceRoot, { withFileTypes: true }).filter((item) => item.isDirectory() && /^[A-Za-z0-9._-]{1,160}$/.test(item.name)).map((item) => item.name).sort().reverse();
  for (const name of names) {
    const runDir = path.join(evidenceRoot, name);
    const summaryPath = path.join(runDir, 'summary.json');
    const manifestPath = path.join(runDir, 'evidence-manifest.json');
    const summary = readSafeJSON(summaryPath, runDir);
    const manifest = readSafeJSON(manifestPath, runDir);
    if (!summary || !manifest || !verifyManifest(runDir, manifest) || summary.ok !== true || (summary.status !== 'completed' && summary.status !== 'passed')) continue;
    const stable = summary.source_hashes_stable === true || summary.sourceHashesStable === true;
    if (!stable) continue;
    if (allowP00 && !p00BaselineAccepted(summary)) continue;
    return { ok: true, stage, code: `${stage}_evidence_accepted`, run_id: name, summary_sha256: sha256(fs.readFileSync(summaryPath)) };
  }
  return { ok: false, stage, code: `${stage}_evidence_integrity_invalid` };
}

function p00BaselineAccepted(summary) {
  const commands = Array.isArray(summary.commandResults) ? summary.commandResults : [];
  const check = commands.find((item) => item?.id === 'check');
  return check?.expectedOutcome === 'exact-known-failure' && check?.evaluation?.accepted === true;
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
    const safe = inspectEvidenceFile(target, runDir);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    return isWithin(target, runDir) && Boolean(info?.isFile() && !info.isSymbolicLink() && info.size === item.bytes && safe.ok && safe.sha256 === item.sha256);
  });
}

function inspectGoOwnerBoundary(rootDir) {
  const files = ['src/main.js', 'src/data/databaseOwnerGuard.js', 'src/data/goDataClient.js', 'backend/internal/config/features.go'];
  let text;
  try { text = files.map((file) => fs.readFileSync(path.join(rootDir, file), 'utf8')).join('\n'); } catch { return { ok: false, code: 'go_owner_source_unreadable' }; }
  const required = ['FeatureGoChatAPI', 'assertGoDataClientFallbackAllowed', 'NODE_SQL_FORBIDDEN', 'assertLegacyChatAdapterEnabled', 'go_chat_api'];
  const missing = required.filter((item) => !text.includes(item));
  return missing.length ? { ok: false, code: 'go_owner_boundary_invalid', missing } : { ok: true, code: 'go_owner_boundary_accepted', owner: 'go', writer_count: 1 };
}

function inspectRendererBoundary(rootDir) {
  const files = [
    'src/renderer/lib/api/httpClient.ts', 'src/renderer/hooks/useChat.ts', 'src/renderer/hooks/useChatQueue.ts',
    'src/renderer/components/workspace/ChatView.tsx', 'src/renderer/components/workspace/WorkspaceSidebar.tsx',
  ];
  let text;
  try { text = files.map((file) => fs.readFileSync(path.join(rootDir, file), 'utf8')).join('\n'); } catch { return { ok: false, code: 'renderer_chat_source_unreadable' }; }
  const missing = ['isChatHTTPEnabled', 'connectChatEvents', 'useHttpChatOperations'].filter((item) => !text.includes(item));
  if (missing.length) return { ok: false, code: 'renderer_chat_http_boundary_missing', missing };
  if (/window\.autoplan\.(?:chat|conversation)/.test(text)) return { ok: false, code: 'renderer_chat_direct_preload_call' };
  return { ok: true, code: 'renderer_chat_boundary_accepted' };
}

function inspectTestPolicy(rootDir) {
  for (const relative of P13A_TEST_SOURCES) {
    const target = path.join(rootDir, relative);
    const info = fs.lstatSync(target, { throwIfNoEntry: false });
    if (!info?.isFile() || info.isSymbolicLink()) return { ok: false, code: 'p13a_test_source_invalid', file: relative };
    let text;
    try { text = fs.readFileSync(target, 'utf8'); } catch { return { ok: false, code: 'p13a_test_source_unreadable', file: relative }; }
    if (/\b(?:describe|it|test)\s*\.\s*(?:skip|only)\s*\(/.test(text) || /(?:^|\s)--(?:run|test-name-pattern)\b/.test(text)) return { ok: false, code: 'p13a_test_filter_forbidden', file: relative };
  }
  return { ok: true, code: 'p13a_test_policy_accepted', files: P13A_TEST_SOURCES.length };
}

function parseArgs(argv) {
  if (argv.length !== 2 || argv[0] !== '--fixture-root' || !argv[1]) throw new Error('arguments_invalid');
  return { fixtureRoot: argv[1] };
}

if (require.main === module) {
  try {
    const result = checkPrerequisites(parseArgs(process.argv.slice(2)));
    process.stdout.write(`${JSON.stringify({ status: result.status, code: result.code, failures: result.failures, stages: result.stages.map((item) => ({ stage: item.stage, run_id: item.run_id || null })), renderer: result.renderer, owner: result.owner })}\n`);
    process.exitCode = result.ok ? 0 : 2;
  } catch {
    process.stdout.write('{"status":"blocked","code":"prerequisite_arguments_invalid"}\n');
    process.exitCode = 2;
  }
}

module.exports = { P13A_TEST_SOURCES, checkPrerequisites, inspectGoOwnerBoundary, inspectRendererBoundary, inspectStageEvidence, inspectTestPolicy, parseArgs, verifyManifest };
