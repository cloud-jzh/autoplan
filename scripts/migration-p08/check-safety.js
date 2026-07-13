'use strict';

// P08 safety checks are read-only. They validate frozen inputs and evidence
// before the verifier is allowed to spawn a Node golden or any Go command.
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const { buildContract } = require('./inventory-static-contract');
const { containsSensitive } = require('./scan-sensitive-output');

const ROOT = path.resolve(__dirname, '../..');
const TEMPORARY_PREFIX = 'autoplan-p08-verify-';
const P07_EVIDENCE_ROOT = 'docs/migration/p07/evidence/runs';
const NODE_WRITER_COMMANDS = new Set(['node-static-golden']);
const P00_GATE_COMMANDS = new Set(['check', 'test']);
const GO_WRITER_COMMANDS = new Set([
  'go-static-repository',
  'go-static-automation',
  'go-static-chat-config',
  'go-static-secrets',
  'go-static-httpapi',
  'go-static-mcp',
  'go-secret-migration',
]);
const REQUIRED_STATIC_ROUTES = [
  '/api/v1/projects/{project_id}/scripts:',
  '/api/v1/projects/{project_id}/executors:',
  '/api/v1/projects/{project_id}/conversations:',
  '/api/v1/projects/{project_id}/conversations/{conversation_id}/messages:',
  '/api/v1/ai-configs:',
  '/api/v1/claude-cli-configs:',
  '/api/v1/mcp-config:',
];
const FORBIDDEN_OUTPUT = [
  ['usable-key', /\b(?:sk|rk|pk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{12,}\b/i],
  ['usable-bearer', /Bearer\s+(?!<redacted>)[A-Za-z0-9._~+\/-]{12,}/i],
  ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
  ['credential-value', /(?:api[_-]?key|auth[_-]?token|session[_-]?credential|password|cookie)[^\r\n]{0,16}[=:][^\r\n]{0,4}["']?[A-Za-z0-9._~+\/-]{12,}/i],
  ['secret-reference', /\b(?:secret_ref|provider(?:_name)?|reference|locator)\b/i],
  ['environment-values', /(?:^|\n)\s*[A-Za-z_][A-Za-z0-9_]*\s*=\s*[^\s<][^\n]*/],
  ['electron-userdata', /(?:electron[\\/]user\s*data|app\.getpath\s*\(\s*["']userdata)/i],
  ['production-database', /\bautoplan\.sqlite\b/i],
  ['local-file-url', /(?:file|autoplan-file):\/\//i],
  ['stored-path-field', /\bstored_path\b/i],
  ['raw-chat-data', /"(?:content|tool_calls|tool_result|codex_session_id)"\s*:/i],
  ['absolute-user-path', /(?:[A-Za-z]:[\\/]|\/(?:Users|home|var|private)\/)/],
];

class SafetyError extends Error {
  constructor(code) {
    super(`P08 safety check failed (${code})`);
    this.name = 'SafetyError';
    this.code = code;
  }
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function toPosix(value) {
  return String(value).replace(/\\/g, '/');
}

function readJson(file) {
  const value = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new SafetyError('json_root_invalid');
  return value;
}

function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function samePath(left, right) {
  const normalizedLeft = path.normalize(left);
  const normalizedRight = path.normalize(right);
  return process.platform === 'win32'
    ? normalizedLeft.toLowerCase() === normalizedRight.toLowerCase()
    : normalizedLeft === normalizedRight;
}

function scanSensitiveText(value) {
  const text = String(value || '');
  return FORBIDDEN_OUTPUT.filter(([, pattern]) => pattern.test(text)).map(([name]) => name);
}

function isOwnedTemporaryRoot(value) {
  if (typeof value !== 'string' || !value) return false;
  const resolved = path.resolve(value);
  const temporary = path.resolve(os.tmpdir());
  const relative = path.relative(temporary, resolved);
  return path.basename(resolved).startsWith(TEMPORARY_PREFIX) && relative !== '' &&
    !relative.startsWith('..') && !path.isAbsolute(relative);
}

function requireSource(root, relative, markers, code) {
  const target = path.join(root, relative);
  if (!fs.existsSync(target) || !fs.statSync(target).isFile()) throw new SafetyError(code);
  const bytes = fs.readFileSync(target);
  const source = bytes.toString('utf8');
  if (markers.some((marker) => !source.includes(marker))) throw new SafetyError(code);
  return { path: toPosix(relative), sha256: sha256(bytes) };
}

function inspectP07Evidence(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const runs = path.join(root, P07_EVIDENCE_ROOT);
  if (!fs.existsSync(runs)) throw new SafetyError('p07_evidence_missing');
  const candidates = fs.readdirSync(runs, { withFileTypes: true })
    .filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort().reverse();
  if (!candidates.length) throw new SafetyError('p07_evidence_missing');
  const run = path.join(runs, candidates[0]);
  const summaryBytes = fs.readFileSync(path.join(run, 'summary.json'));
  const manifestBytes = fs.readFileSync(path.join(run, 'evidence-manifest.json'));
  const summary = JSON.parse(summaryBytes);
  const manifest = JSON.parse(manifestBytes);
  const summarySha256 = sha256(summaryBytes);
  const linked = Array.isArray(manifest.artifacts) && manifest.artifacts.some(
    (item) => item.path === 'summary.json' && item.sha256 === summarySha256,
  );
  if (summary.schemaVersion !== 1 || summary.status !== 'completed' || summary.ok !== true ||
      summary.sourceHashesStable !== true || manifest.schemaVersion !== 1 ||
      manifest.immutableRunDirectory !== true || !linked) {
    throw new SafetyError('p07_evidence_invalid');
  }
  return {
    stage: 'p07', runId: candidates[0], summarySha256,
    manifestSha256: sha256(manifestBytes), sourceHashesStable: true,
  };
}

function assertManifestHash(root, record, code) {
  const target = path.join(root, record?.path || '');
  if (!record || !/^[a-f0-9]{64}$/.test(record.sha256 || '') || !fs.existsSync(target) ||
      sha256(fs.readFileSync(target)) !== record.sha256) {
    throw new SafetyError(code);
  }
}

function inspectSourceSafety(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const expectations = readJson(path.join(root, 'docs/migration/p00/baseline-expectations.json'));
  if (expectations.schemaVersion !== 1 || expectations.commands?.check?.outcome !== 'exact-known-failure' ||
      expectations.commands?.test?.outcome !== 'success' || !Array.isArray(expectations.commands.check.allowedFailureSignatures)) {
    throw new SafetyError('p00_baseline_signature_drift');
  }

  const manifestPath = path.join(root, 'fixtures/migration/p08/manifest.json');
  const goldenPath = path.join(root, 'fixtures/migration/p08/node-static.golden.json');
  const paginationPath = path.join(root, 'fixtures/migration/p08/pagination-cases.json');
  const errorsPath = path.join(root, 'fixtures/migration/p08/expected-errors.json');
  const manifest = readJson(manifestPath);
  const pagination = readJson(paginationPath);
  const expectedErrors = readJson(errorsPath);
  const goldenRecord = Array.isArray(manifest.artifacts)
    ? manifest.artifacts.find((item) => item.name === 'node-static.golden.json') : null;
  if (manifest.schemaVersion !== 1 || manifest.fixture_set !== 'autoplan-p08-static-contract' ||
      manifest.source?.syntheticOnly !== true || manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false ||
      manifest.writerHandoff?.nodeClosedBeforeArtifactCommit !== true || !goldenRecord ||
      sha256(fs.readFileSync(goldenPath)) !== goldenRecord.sha256 ||
      sha256(fs.readFileSync(path.join(root, manifest.generator || ''))) !== manifest.generatorSha256 ||
      sha256(fs.readFileSync(path.join(root, manifest.inventory?.path || ''))) !== manifest.inventory?.sha256 ||
      !Array.isArray(pagination.cases) || pagination.cases.length < 10 || !Array.isArray(expectedErrors.transport_errors) ||
      expectedErrors.transport_errors.length < 8 ||
      containsSensitive(fs.readFileSync(goldenPath, 'utf8')) ||
      containsSensitive(JSON.stringify(pagination)) || containsSensitive(JSON.stringify(expectedErrors))) {
    throw new SafetyError('p08_fixture_or_golden_drift');
  }
  assertManifestHash(root, manifest.prerequisites?.staticContract, 'p08_static_contract_hash_drift');
  for (const record of manifest.prerequisites?.upstream || []) assertManifestHash(root, record, 'p08_upstream_hash_drift');
  try {
    buildContract(root);
  } catch {
    throw new SafetyError('p08_static_contract_drift');
  }

  const migration = requireSource(root, 'backend/migrations/0001_schema_v1.sql',
    ['CREATE TABLE IF NOT EXISTS scripts', 'CREATE TABLE IF NOT EXISTS executors', 'CREATE TABLE IF NOT EXISTS conversations',
      'CREATE TABLE IF NOT EXISTS chat_messages', 'CREATE TABLE IF NOT EXISTS secret_refs'], 'p08_schema_drift');
  const owner = requireSource(root, 'backend/internal/repository/sqlite/transaction.go',
    ['DatabaseOwnerProof', 'AuthorizedCopy', 'ErrWriterUnauthorized', 'LevelSerializable', 'BeginTx'], 'database_owner_guard_drift');
  const secretRef = requireSource(root, 'backend/internal/repository/sqlite/secret_refs.go',
    ['CreateSecretRef', 'ReplaceSecretRef', 'RetireSecretRef', 'PurgeSecretRef'], 'secret_ref_persistence_drift');
  const secretService = requireSource(root, 'backend/internal/application/secrets/service.go',
    ['ErrCompensation', 'compensateDelete', 'Status is safe to expose'], 'secret_compensation_drift');
  const keyring = requireSource(root, 'backend/internal/platform/secrets/keyring/provider.go',
    ['os-keyring-v1', 'ValidateBinding', 'NewOpaqueReference'], 'os_keyring_provider_drift');
  const fallback = requireSource(root, 'backend/internal/platform/secrets/encryptedfile/provider.go',
    ['aes.NewCipher', 'cipher.NewGCM', 'associatedData', 'keyRoot'], 'encrypted_fallback_drift');
  const copyPreparation = requireSource(root, 'scripts/migration-p08/prepare-secret-copy.js',
    ['--sanitized-copy', 'ensureNewDirectory', 'node_sqljs_closed', 'production_database_opened'], 'explicit_copy_authorization_drift');
  const scanner = requireSource(root, 'scripts/migration-p08/scan-sensitive-output.js',
    ['sensitiveKey', 'absolutePath', 'sensitive_output'], 'sensitive_scanner_drift');
  const automation = requireSource(root, 'backend/internal/application/automation/service.go',
    ['ErrRuntimeDisabled', 'RunScript', 'RunExecutorAction'], 'automation_runtime_closure_drift');
  const chat = requireSource(root, 'backend/internal/application/chat/service.go',
    ['ErrRuntimeDisabled', 'Runtime capabilities remain deliberately disabled', 'GenerateTitle'], 'chat_runtime_closure_drift');
  const config = requireSource(root, 'backend/internal/application/config/static_service.go',
    ['ErrStaticRuntimeDisabled', 'StartMCP', 'StopMCP'], 'mcp_runtime_closure_drift');
  const mcp = requireSource(root, 'backend/internal/mcp/static_tools.go',
    ['StaticTools', 'mapStaticToolError', 'automation.list_scripts'], 'mcp_static_adapter_drift');
  const openapi = requireSource(root, 'backend/openapi/openapi.yaml',
    [...REQUIRED_STATIC_ROUTES, 'not_implemented', 'Safe static script metadata'], 'openapi_static_surface_drift');
  const httpClient = requireSource(root, 'src/renderer/lib/api/httpClient.ts',
    ['listStaticScripts', 'listStaticConversations', 'getStaticMCPConfig', 'invalid_response'], 'renderer_static_transport_drift');
  const transport = requireSource(root, 'src/renderer/lib/api/transport.ts',
    ["DEFAULT_AUTOPLAN_TRANSPORT = 'ipc'", 'getStaticHttpOperations', 'return null'], 'renderer_runtime_owner_drift');
  const comparator = requireSource(root, 'scripts/migration-p08/compare-static-golden.js',
    ['shared_database_copy', 'golden_mismatch', 'sensitive_output'], 'golden_comparator_drift');
  if (/writeFileSync\s*\([^)]*(?:golden|node|go)/i.test(fs.readFileSync(path.join(root, comparator.path), 'utf8'))) {
    throw new SafetyError('golden_comparator_escape_hatch');
  }

  return {
    ok: true,
    schemaVersion: 1,
    p00BaselineSha256: sha256(fs.readFileSync(path.join(root, 'docs/migration/p00/baseline-expectations.json'))),
    manifestSha256: sha256(fs.readFileSync(manifestPath)),
    goldenSha256: goldenRecord.sha256,
    paginationSha256: sha256(fs.readFileSync(paginationPath)),
    expectedErrorsSha256: sha256(fs.readFileSync(errorsPath)),
    schemaSha256: migration.sha256,
    databaseOwnerGuardSha256: owner.sha256,
    secretIsolation: { secretRefs: secretRef.sha256, service: secretService.sha256, keyring: keyring.sha256, fallback: fallback.sha256 },
    safetyTools: { copyPreparation: copyPreparation.sha256, scanner: scanner.sha256, comparator: comparator.sha256 },
    runtimeClosure: { automation: automation.sha256, chat: chat.sha256, config: config.sha256, mcp: mcp.sha256 },
    renderer: { httpClient: httpClient.sha256, transport: transport.sha256 },
    openapiRoutes: REQUIRED_STATIC_ROUTES.map((route) => route.slice(0, -1)),
  };
}

function commandIntervals(commandResults) {
  if (!Array.isArray(commandResults)) throw new SafetyError('command_results_missing');
  return commandResults.map((item) => ({ id: item.id, start: Date.parse(item.startedAt), end: Date.parse(item.endedAt) }));
}

function inspectWriterTimeline(commandResults) {
  const intervals = commandIntervals(commandResults);
  if (intervals.some((item) => !Number.isFinite(item.start) || !Number.isFinite(item.end) || item.end < item.start)) {
    throw new SafetyError('command_timeline_invalid');
  }
  for (let index = 1; index < intervals.length; index += 1) {
    if (intervals[index].start < intervals[index - 1].end) throw new SafetyError('command_timeline_overlap');
  }
  const node = intervals.filter((item) => NODE_WRITER_COMMANDS.has(item.id));
  const go = intervals.filter((item) => GO_WRITER_COMMANDS.has(item.id));
  const nodeIndex = intervals.findIndex((item) => NODE_WRITER_COMMANDS.has(item.id));
  const p00GateIndexes = intervals
    .map((item, index) => (P00_GATE_COMMANDS.has(item.id) ? index : -1))
    .filter((index) => index >= 0);
  if (node.length !== 1 || go.length !== GO_WRITER_COMMANDS.size ||
      new Set(go.map((item) => item.id)).size !== GO_WRITER_COMMANDS.size || go.some((item) => item.start < node[0].end)) {
    throw new SafetyError('node_go_writer_handoff_invalid');
  }
  if (p00GateIndexes.length !== P00_GATE_COMMANDS.size ||
      new Set(p00GateIndexes.map((index) => intervals[index].id)).size !== P00_GATE_COMMANDS.size ||
      p00GateIndexes.map((index) => intervals[index].id).join(',') !== 'check,test' ||
      p00GateIndexes.some((index) => index >= nodeIndex)) {
    throw new SafetyError('p00_gate_order_invalid');
  }
  return {
    sequential: true, simultaneousNodeGoWriter: false, nodeClosedBeforeGo: true,
    p00GateCommands: intervals.filter((item) => P00_GATE_COMMANDS.has(item.id)).map((item) => item.id),
    nodeWriterCommands: node.map((item) => item.id), goWriterCommands: go.map((item) => item.id),
  };
}

function inspectEvidenceSummary(summary, options = {}) {
  if (!object(summary) || summary.schemaVersion !== 1 || summary.status !== 'completed' || !Array.isArray(summary.commandResults) ||
      summary.environment?.electronUserDataAccessed !== false || summary.environment?.productionDatabaseOpened !== false ||
      summary.environment?.databaseContentCaptured !== false || summary.environment?.userContentCaptured !== false ||
      summary.environment?.externalProcessStarted !== false || summary.environment?.chatStreamStarted !== false ||
      summary.environment?.mcpListenerStarted !== false || summary.environment?.temporaryRootsOnly !== true ||
      summary.databaseOwnership?.p07GateAccepted !== true || summary.databaseOwnership?.p07EvidenceAccepted !== true ||
      summary.databaseOwnership?.p00BaselineAccepted !== true || summary.databaseOwnership?.authorizedCopiesOnly !== true ||
      summary.databaseOwnership?.goWriteRequiresOwnerProof !== true || summary.databaseOwnership?.secretStorageSeparateFromDatabase !== true ||
      !/^[a-f0-9]{64}$/.test(summary.databaseOwnership?.ownerGuardSha256 || '') || summary.sourceHashesStable !== true) {
    throw new SafetyError('evidence_summary_invalid');
  }
  if (options.temporaryRoot && !isOwnedTemporaryRoot(options.temporaryRoot)) throw new SafetyError('temporary_root_not_owned');
  if (summary.commandResults[0]?.id !== 'p07-gate' || summary.commandResults[1]?.id !== 'p08-safety-preflight') {
    throw new SafetyError('gate_order_invalid');
  }
  if (summary.commandResults.some((item) => item.evaluation?.accepted !== true)) throw new SafetyError('command_not_accepted');
  const sensitive = scanSensitiveText(JSON.stringify(summary.commandResults.map((item) => [
    item.command, item.evaluation?.reason, item.failureSignatures, item.structuredOutput,
  ])));
  if (sensitive.length) throw new SafetyError('evidence_sensitive');
  return {
    ok: true, schemaVersion: 1, writerTimeline: inspectWriterTimeline(summary.commandResults), sensitiveFindings: [],
    authorizedCopiesOnly: true, electronUserDataAccessed: false, productionDatabaseOpened: false,
  };
}

function preflight(rootDir = ROOT) {
  return { p07Evidence: inspectP07Evidence(rootDir), sourceSafety: inspectSourceSafety(rootDir) };
}

function parseArgs(argv) {
  if (argv.length === 1 && argv[0] === 'preflight') return { mode: 'preflight' };
  if (argv.length === 2 && argv[0] === 'evidence') return { mode: 'evidence', summary: argv[1] };
  throw new Error('usage: node scripts/migration-p08/check-safety.js <preflight|evidence summary.json>');
}

if (require.main === module) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = args.mode === 'preflight' ? preflight(ROOT) : inspectEvidenceSummary(readJson(path.resolve(args.summary)));
    process.stdout.write(`${JSON.stringify({ ok: true, ...result })}\n`);
  } catch (error) {
    const code = error instanceof SafetyError ? error.code : 'safety_internal';
    process.stderr.write(`blocked: ${code}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  GO_WRITER_COMMANDS,
  NODE_WRITER_COMMANDS,
  P00_GATE_COMMANDS,
  REQUIRED_STATIC_ROUTES,
  SafetyError,
  inspectEvidenceSummary,
  inspectP07Evidence,
  inspectSourceSafety,
  inspectWriterTimeline,
  isOwnedTemporaryRoot,
  parseArgs,
  preflight,
  scanSensitiveText,
  sha256,
  toPosix,
};
