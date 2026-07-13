'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const TEMPORARY_PREFIX = 'autoplan-p05-verify-';
const REQUIRED_HTTP_ROUTES = [
  '/api/v1/projects:',
  '/api/v1/projects/{project_id}:',
  '/api/v1/projects/{project_id}/loop-config:',
  '/api/v1/projects/{project_id}/snapshot:',
];
const NODE_WRITER_COMMANDS = new Set(['node-write-contracts']);
const GO_WRITER_COMMANDS = new Set([
  'go-repository', 'go-application', 'go-http', 'go-files', 'mutation-golden-compare',
]);
const FORBIDDEN_OUTPUT = [
  ['usable-key', /\b(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9_-]{12,}\b/i],
  ['usable-bearer', /Bearer\s+(?!<redacted>)[A-Za-z0-9._~+\/-]{12,}/i],
  ['private-key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
  ['credential-value', /(?:api[_-]?key|auth[_-]?token|session[_-]?credential|password|cookie)[^\r\n]{0,16}[=:][^\r\n]{0,4}["']?[A-Za-z0-9._~+\/-]{12,}/i],
  ['electron-userdata', /(?:electron[\\/]user\s*data|app\.getpath\s*\(\s*['"]userdata)/i],
  ['production-database', /\bautoplan\.sqlite\b/i],
];

class SafetyError extends Error {
  constructor(code) {
    super(`P05 safety check failed (${code})`);
    this.name = 'SafetyError';
    this.code = code;
  }
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function readJson(file) {
  const value = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new SafetyError('json_root_invalid');
  return value;
}

function assertFileHash(root, record, code) {
  const target = path.join(root, record?.path || '');
  if (!record || !fs.existsSync(target) || !/^[a-f0-9]{64}$/.test(record.sha256 || '') ||
      sha256(fs.readFileSync(target)) !== record.sha256) {
    throw new SafetyError(code);
  }
}

function inspectSourceSafety(rootDir = ROOT) {
  const root = path.resolve(rootDir);
  const manifest = readJson(path.join(root, 'fixtures/migration/p05/manifest.json'));
  const goldenPath = path.join(root, 'fixtures/migration/p05/node-mutations.golden.json');
  const expectedErrors = readJson(path.join(root, 'fixtures/migration/p05/expected-errors.json'));
  const generator = path.join(root, manifest.generator || '');
  const declaredGolden = (manifest.artifacts || []).find((item) => item.name === 'node-mutations.golden.json');
  if (manifest.schemaVersion !== 1 || manifest.source?.syntheticOnly !== true ||
      manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false ||
      manifest.writerHandoff?.nodeClosedBeforeArtifactCommit !== true ||
      !fs.existsSync(generator) || sha256(fs.readFileSync(generator)) !== manifest.generatorSha256 ||
      !declaredGolden || sha256(fs.readFileSync(goldenPath)) !== declaredGolden.sha256 ||
      expectedErrors.schemaVersion !== 1 || expectedErrors.fixturePolicy?.syntheticOnly !== true) {
    throw new SafetyError('frozen_fixture_drift');
  }
  assertFileHash(root, manifest.prerequisites?.writeContract, 'write_contract_drift');
  for (const item of manifest.prerequisites?.p04 || []) {
    assertFileHash(root, item, 'p04_prerequisite_drift');
  }

  const openapi = fs.readFileSync(path.join(root, 'backend/openapi/openapi.yaml'), 'utf8');
  if (REQUIRED_HTTP_ROUTES.some((route) => !openapi.includes(route)) ||
      !openapi.includes('Idempotency-Key') || !openapi.includes('version_required') ||
      !openapi.includes('version_conflict') || !openapi.includes('idempotency_key_reused')) {
    throw new SafetyError('openapi_project_config_drift');
  }
  const filePolicySource = fs.readFileSync(path.join(root, 'backend/internal/httpapi/file_policy.go'), 'utf8');
  if (!filePolicySource.includes('/api/v1/file-access-policy') || !filePolicySource.includes('Version')) {
    throw new SafetyError('file_policy_transport_drift');
  }
  const transactionPath = path.join(root, 'backend/internal/repository/sqlite/transaction.go');
  const transactionSource = fs.readFileSync(transactionPath, 'utf8');
  if (!transactionSource.includes('DatabaseOwnerProof') || !transactionSource.includes('AuthorizedCopy') ||
      !transactionSource.includes('ErrWriterUnauthorized') || !transactionSource.includes('BeginTx')) {
    throw new SafetyError('database_owner_guard_drift');
  }
  const httpClient = fs.readFileSync(path.join(root, 'src/renderer/lib/api/httpClient.ts'), 'utf8');
  if (!httpClient.includes("hostname !== '127.0.0.1'") ||
      !httpClient.includes("'Idempotency-Key'") || !httpClient.includes('version_required') ||
      /localStorage|sessionStorage|window\.autoplan/.test(httpClient)) {
    throw new SafetyError('renderer_transport_safety_drift');
  }
  const comparator = fs.readFileSync(path.join(root, 'scripts/migration-p05/compare-mutation-golden.js'), 'utf8');
  if (/updateGolden|ignoreFields|writeFileSync\s*\([^)]*golden/i.test(comparator) ||
      !comparator.includes("reason: 'unknown'") || !comparator.includes('node-closed')) {
    throw new SafetyError('golden_comparator_escape_hatch');
  }
  const findings = scanSensitiveText(
    fs.readFileSync(goldenPath, 'utf8') + '\n' + JSON.stringify(expectedErrors),
  );
  if (findings.length) throw new SafetyError('checked_fixture_sensitive');
  return {
    ok: true,
    schemaVersion: 1,
    manifestSha256: sha256(fs.readFileSync(path.join(root, 'fixtures/migration/p05/manifest.json'))),
    goldenSha256: declaredGolden.sha256,
    expectedErrorsSha256: sha256(fs.readFileSync(path.join(root, 'fixtures/migration/p05/expected-errors.json'))),
    writeContractSha256: manifest.prerequisites.writeContract.sha256,
    databaseOwnerGuardSha256: sha256(fs.readFileSync(transactionPath)),
    p04PrerequisiteHashes: manifest.prerequisites.p04.map((item) => item.sha256),
    openapiRoutes: REQUIRED_HTTP_ROUTES.map((route) => route.slice(0, -1)),
  };
}

function scanSensitiveText(value) {
  const text = String(value || '');
  return FORBIDDEN_OUTPUT.filter(([, pattern]) => pattern.test(text)).map(([name]) => name);
}

function isOwnedTemporaryRoot(value) {
  if (typeof value !== 'string' || !value) return false;
  const resolved = path.resolve(value);
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  return path.basename(resolved).startsWith(TEMPORARY_PREFIX) &&
    relative !== '' && !relative.startsWith('..') && !path.isAbsolute(relative);
}

function commandIntervals(commandResults) {
  return commandResults.map((item) => ({
    id: item.id,
    start: Date.parse(item.startedAt),
    end: Date.parse(item.endedAt),
  }));
}

function inspectWriterTimeline(commandResults) {
  if (!Array.isArray(commandResults)) throw new SafetyError('command_results_missing');
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
  if (!summary || summary.schemaVersion !== 1 || !Array.isArray(summary.commandResults) ||
      summary.environment?.electronUserDataAccessed !== false ||
      summary.environment?.productionDatabaseOpened !== false ||
      summary.environment?.databaseContentCaptured !== false ||
      summary.databaseOwnership?.authorizedCopiesOnly !== true ||
      summary.databaseOwnership?.p04OwnerGateAccepted !== true ||
      summary.databaseOwnership?.goWriteRequiresOwnerProof !== true ||
      !/^[a-f0-9]{64}$/.test(summary.databaseOwnership?.ownerGuardSha256 || '') ||
      summary.sourceHashesStable !== true) {
    throw new SafetyError('evidence_summary_invalid');
  }
  if (options.temporaryRoot && !isOwnedTemporaryRoot(options.temporaryRoot)) {
    throw new SafetyError('temporary_root_not_owned');
  }
  const rejected = summary.commandResults.filter((item) => item.evaluation?.accepted !== true);
  if (rejected.length) throw new SafetyError('command_not_accepted');
  const combined = JSON.stringify(summary.commandResults.map((item) => [
    item.command, item.evaluation?.reason, item.failureSignatures, item.structuredOutput,
  ]));
  if (scanSensitiveText(combined).length) throw new SafetyError('evidence_sensitive');
  const writerTimeline = inspectWriterTimeline(summary.commandResults);
  return {
    ok: true,
    schemaVersion: 1,
    writerTimeline,
    sensitiveFindings: [],
    authorizedCopiesOnly: true,
    electronUserDataAccessed: false,
    productionDatabaseOpened: false,
  };
}

function parseArgs(argv) {
  if (argv.length === 1 && argv[0] === 'preflight') return { mode: 'preflight' };
  if (argv.length === 2 && argv[0] === 'evidence') return { mode: 'evidence', summary: argv[1] };
  throw new Error('usage: node scripts/migration-p05/check-safety.js <preflight|evidence summary.json>');
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
  inspectSourceSafety,
  inspectWriterTimeline,
  isOwnedTemporaryRoot,
  parseArgs,
  scanSensitiveText,
  sha256,
};
