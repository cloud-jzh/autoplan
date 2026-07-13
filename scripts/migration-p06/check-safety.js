'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const TEMPORARY_PREFIX = 'autoplan-p06-verify-';
const P05_EVIDENCE_ROOT = 'docs/migration/p05/evidence/runs';
const NODE_WRITER_COMMANDS = new Set(['node-golden-generator']);
const GO_WRITER_COMMANDS = new Set([
  'go-repository', 'go-intake-application', 'go-attachments', 'go-http', 'go-mcp', 'go-files',
]);
const REQUIRED_HTTP_ROUTES = [
  '/api/v1/projects/{project_id}/requirements:',
  '/api/v1/projects/{project_id}/requirements/{intake_id}/plan-links:',
  '/api/v1/projects/{project_id}/requirements/{intake_id}/attachments:',
  '/api/v1/projects/{project_id}/feedback:',
  '/api/v1/projects/{project_id}/feedback/{intake_id}/plan-links:',
  '/api/v1/projects/{project_id}/feedback/{intake_id}/attachments:',
  '/api/v1/attachments/{attachment_id}/content:',
  '/api/v1/attachments/{attachment_id}:',
];
const FORBIDDEN_OUTPUT = [
  ['usable-key', /\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{12,}\b/i],
  ['usable-bearer', /Bearer\s+(?!<redacted>)[A-Za-z0-9._~+\/-]{12,}/i],
  ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
  ['credential-value', /(?:api[_-]?key|auth[_-]?token|session[_-]?credential|password|cookie)[^\r\n]{0,16}[=:][^\r\n]{0,4}["']?[A-Za-z0-9._~+\/-]{12,}/i],
  ['electron-userdata', /(?:electron[\\/]user\s*data|app\.getpath\s*\(\s*["']userdata)/i],
  ['production-database', /\bautoplan\.sqlite\b/i],
  ['local-file-url', /(?:file|autoplan-file):\/\//i],
];

class SafetyError extends Error {
  constructor(code) {
    super(`P06 safety check failed (${code})`);
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

function assertFileHash(root, record, code) {
  const relative = record?.path;
  const target = path.resolve(root, relative || '');
  const containment = path.relative(root, target);
  if (!relative || containment === '' || containment.startsWith('..') || path.isAbsolute(containment) ||
      !fs.existsSync(target) || !/^[a-f0-9]{64}$/.test(record.sha256 || '') ||
      sha256(fs.readFileSync(target)) !== record.sha256) {
    throw new SafetyError(code);
  }
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
  if (!fs.existsSync(target)) throw new SafetyError(code);
  const source = fs.readFileSync(target, 'utf8');
  if (markers.some((marker) => !source.includes(marker))) throw new SafetyError(code);
  return { source, sha256: sha256(source) };
}

function inspectP05Evidence(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const runs = path.join(root, P05_EVIDENCE_ROOT);
  if (!fs.existsSync(runs)) throw new SafetyError('p05_evidence_missing');
  const candidates = fs.readdirSync(runs, { withFileTypes: true })
    .filter((entry) => entry.isDirectory()).map((entry) => entry.name).sort().reverse();
  if (!candidates.length) throw new SafetyError('p05_evidence_missing');
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
    throw new SafetyError('p05_evidence_invalid');
  }
  return {
    stage: 'p05', runId: candidates[0], summarySha256,
    manifestSha256: sha256(manifestBytes), sourceHashesStable: true,
  };
}

function inspectSourceSafety(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const manifestPath = path.join(root, 'fixtures/migration/p06/manifest.json');
  const goldenPath = path.join(root, 'fixtures/migration/p06/node-intake.golden.json');
  const expectedErrorsPath = path.join(root, 'fixtures/migration/p06/expected-errors.json');
  const contractPath = path.join(root, 'docs/migration/p06/intake-contract.json');
  const intakeSchemaPath = path.join(root, 'backend/openapi/schemas/intake.schema.json');
  const attachmentSchemaPath = path.join(root, 'backend/openapi/schemas/attachment.schema.json');
  const manifest = readJson(manifestPath);
  const contract = readJson(contractPath);
  const expectedErrors = readJson(expectedErrorsPath);
  const intakeSchema = readJson(intakeSchemaPath);
  const attachmentSchema = readJson(attachmentSchemaPath);
  const golden = fs.readFileSync(goldenPath, 'utf8');
  const generatorPath = path.join(root, manifest.generator || '');
  const declaredGolden = (manifest.artifacts || []).find((item) => item.name === 'node-intake.golden.json');

  if (manifest.schemaVersion !== 1 || manifest.version !== 'p06-node-intake-golden-v1' ||
      manifest.source?.syntheticOnly !== true || manifest.source?.databaseCopy?.includes('Electron userData') !== true ||
      !/^[a-f0-9]{64}$/.test(manifest.source?.databaseLogicalBeforeSha256 || '') ||
      !/^[a-f0-9]{64}$/.test(manifest.source?.databaseLogicalAfterSha256 || '') ||
      !/^[a-f0-9]{64}$/.test(manifest.source?.attachmentBytesSha256 || '') ||
      manifest.artifact_policy?.productionDiscovery !== false || manifest.artifact_policy?.realUserDataAllowed !== false ||
      manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false ||
      manifest.writerHandoff?.nodeClosedBeforeArtifactCommit !== true ||
      !fs.existsSync(generatorPath) || sha256(fs.readFileSync(generatorPath)) !== manifest.generatorSha256 ||
      !declaredGolden || sha256(golden) !== declaredGolden.sha256 ||
      contract.schemaVersion !== 1 || contract.version !== 'p06-node-intake-contract-v1' ||
      contract.status !== 'frozen-with-recorded-legacy-gaps' ||
      expectedErrors.schemaVersion !== 1 || expectedErrors.version !== 'p06-intake-expected-errors-v1' ||
      !expectedErrors.normalization || !expectedErrors.scenarios) {
    throw new SafetyError('frozen_fixture_drift');
  }
  assertFileHash(root, manifest.prerequisites?.intakeContract, 'intake_contract_drift');
  for (const item of manifest.prerequisites?.p05 || []) assertFileHash(root, item, 'p05_prerequisite_drift');

  const openapi = requireSource(root, 'backend/openapi/openapi.yaml', REQUIRED_HTTP_ROUTES, 'openapi_intake_attachment_drift');
  const publicAttachmentFields = ['id', 'display_name', 'size', 'mime_type', 'download_url'];
  if (intakeSchema.additionalProperties !== false || !intakeSchema.properties?.linked_plans ||
      !intakeSchema.properties?.accepted_at || attachmentSchema.additionalProperties !== false ||
      JSON.stringify(Object.keys(attachmentSchema.properties || {}).sort()) !== JSON.stringify(publicAttachmentFields.slice().sort()) ||
      !attachmentSchema.properties?.download_url?.pattern?.includes('/api/v1/attachments/') ||
      Object.keys(attachmentSchema.properties || {}).some((key) =>
        ['project_id', 'owner_type', 'owner_id', 'original_name', 'stored_path', 'hash'].includes(key))) {
    throw new SafetyError('openapi_schema_drift');
  }
  const owner = requireSource(root, 'backend/internal/repository/sqlite/transaction.go',
    ['DatabaseOwnerProof', 'AuthorizedCopy', 'ErrWriterUnauthorized', 'BeginTx'], 'database_owner_guard_drift');
  requireSource(root, 'backend/internal/platform/filesystem/attachment_store.go',
    ['os.O_EXCL', 'file.Sync()', 'Promote', 'Quarantine'], 'attachment_store_safety_drift');
  requireSource(root, 'backend/internal/mcp/attachment_inputs.go',
    ['ErrAttachmentPathDenied', 'localCaller'], 'mcp_attachment_policy_drift');
  const renderer = requireSource(root, 'src/renderer/lib/api/httpClient.ts',
    ['createAttachmentFormData', 'controlledAttachmentURL', 'download_url'], 'renderer_attachment_transport_drift');
  if (/pathToFileURL|autoplan-file|File\.path/.test(renderer.source)) throw new SafetyError('renderer_attachment_transport_drift');
  const comparator = requireSource(root, 'scripts/migration-p06/compare-intake-golden.js',
    ['assertStrictEqual', 'unknown scenario', 'PUBLIC_ATTACHMENT_FIELDS'], 'golden_comparator_drift');
  if (/updateGolden|ignoreFields|writeFileSync\s*\([^)]*golden/i.test(comparator.source)) {
    throw new SafetyError('golden_comparator_escape_hatch');
  }
  if (scanSensitiveText(golden + '\n' + JSON.stringify(expectedErrors)).length) {
    throw new SafetyError('checked_fixture_sensitive');
  }
  return {
    ok: true,
    schemaVersion: 1,
    manifestSha256: sha256(fs.readFileSync(manifestPath)),
    goldenSha256: declaredGolden.sha256,
    expectedErrorsSha256: sha256(fs.readFileSync(expectedErrorsPath)),
    intakeContractSha256: manifest.prerequisites.intakeContract.sha256,
    nodeDatabaseLogicalBeforeSha256: manifest.source.databaseLogicalBeforeSha256,
    nodeDatabaseLogicalAfterSha256: manifest.source.databaseLogicalAfterSha256,
    nodeAttachmentBytesSha256: manifest.source.attachmentBytesSha256,
    databaseOwnerGuardSha256: owner.sha256,
    openapiRoutes: REQUIRED_HTTP_ROUTES.map((route) => route.slice(0, -1)),
    intakeSchemaSha256: sha256(fs.readFileSync(intakeSchemaPath)),
    attachmentSchemaSha256: sha256(fs.readFileSync(attachmentSchemaPath)),
    attachmentStorageGuardSha256: sha256(fs.readFileSync(path.join(root, 'backend/internal/platform/filesystem/attachment_store.go'))),
  };
}

function commandIntervals(commandResults) {
  if (!Array.isArray(commandResults)) throw new SafetyError('command_results_missing');
  return commandResults.map((item) => ({
    id: item.id, start: Date.parse(item.startedAt), end: Date.parse(item.endedAt),
  }));
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
  if (node.length !== 1 || go.length !== GO_WRITER_COMMANDS.size ||
      new Set(go.map((item) => item.id)).size !== GO_WRITER_COMMANDS.size ||
      go.some((item) => item.start < node[0].end)) {
    throw new SafetyError('node_go_writer_handoff_invalid');
  }
  return {
    sequential: true,
    simultaneousNodeGoWriter: false,
    nodeClosedBeforeGo: true,
    nodeWriterCommands: node.map((item) => item.id),
    goWriterCommands: go.map((item) => item.id),
  };
}

function inspectEvidenceSummary(summary, options = {}) {
  if (!summary || summary.schemaVersion !== 1 || summary.status !== 'completed' || !Array.isArray(summary.commandResults) ||
      summary.environment?.electronUserDataAccessed !== false || summary.environment?.productionDatabaseOpened !== false ||
      summary.environment?.databaseContentCaptured !== false || summary.environment?.attachmentContentCaptured !== false ||
      summary.environment?.temporaryRootsOnly !== true || summary.databaseOwnership?.p05GateAccepted !== true ||
      summary.databaseOwnership?.p05EvidenceAccepted !== true || summary.databaseOwnership?.authorizedCopiesOnly !== true ||
      summary.databaseOwnership?.goWriteRequiresOwnerProof !== true ||
      !/^[a-f0-9]{64}$/.test(summary.databaseOwnership?.ownerGuardSha256 || '') ||
      summary.sourceHashesStable !== true) {
    throw new SafetyError('evidence_summary_invalid');
  }
  if (options.temporaryRoot && !isOwnedTemporaryRoot(options.temporaryRoot)) {
    throw new SafetyError('temporary_root_not_owned');
  }
  if (summary.commandResults[0]?.id !== 'p05-gate') throw new SafetyError('p05_gate_not_first');
  if (summary.commandResults.some((item) => item.evaluation?.accepted !== true)) throw new SafetyError('command_not_accepted');
  const sensitive = scanSensitiveText(JSON.stringify(summary.commandResults.map((item) => [
    item.command, item.evaluation?.reason, item.failureSignatures, item.structuredOutput,
  ])));
  if (sensitive.length) throw new SafetyError('evidence_sensitive');
  return {
    ok: true,
    schemaVersion: 1,
    writerTimeline: inspectWriterTimeline(summary.commandResults),
    sensitiveFindings: [],
    authorizedCopiesOnly: true,
    electronUserDataAccessed: false,
    productionDatabaseOpened: false,
  };
}

function parseArgs(argv) {
  if (argv.length === 1 && argv[0] === 'preflight') return { mode: 'preflight' };
  if (argv.length === 2 && argv[0] === 'evidence') return { mode: 'evidence', summary: argv[1] };
  throw new Error('usage: node scripts/migration-p06/check-safety.js <preflight|evidence summary.json>');
}

if (require.main === module) {
  try {
    const args = parseArgs(process.argv.slice(2));
    const result = args.mode === 'preflight'
      ? inspectSourceSafety(ROOT)
      : inspectEvidenceSummary(readJson(path.resolve(args.summary)));
    process.stdout.write(`${JSON.stringify(result)}\n`);
  } catch (error) {
    const code = error instanceof SafetyError ? error.code : 'safety_internal';
    process.stderr.write(`blocked: ${code}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  GO_WRITER_COMMANDS,
  NODE_WRITER_COMMANDS,
  SafetyError,
  inspectEvidenceSummary,
  inspectP05Evidence,
  inspectSourceSafety,
  inspectWriterTimeline,
  isOwnedTemporaryRoot,
  parseArgs,
  scanSensitiveText,
  sha256,
  toPosix,
};
