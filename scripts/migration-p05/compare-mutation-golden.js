'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const { NORMALIZATION_VERSION, normalizeGolden } = require('./generate-node-golden');

const ROOT = path.resolve(__dirname, '../..');
const FIXTURE_DIRECTORY = path.join('fixtures', 'migration', 'p05');
const GOLDEN_NAME = 'node-mutations.golden.json';
const ERROR_NAME = 'expected-errors.json';
const GO_EXPORT_TEST = '^TestMutationGoldenExport$';
const TEMPORARY_PREFIX = 'autoplan-p05-compare-';
const MAXIMUM_DIFFERENCES = 100;

class GoldenMismatchError extends Error {
  constructor(label, differences) {
    const safe = differences.slice(0, 20).map(({ pointer, reason }) => ({ pointer, reason }));
    super(`mutation golden mismatch (${label}): ${safe.map((item) => item.pointer).join(', ')}`);
    this.name = 'GoldenMismatchError';
    this.code = 'P05_MUTATION_GOLDEN_MISMATCH';
    this.label = label;
    this.differences = safe;
  }
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sha256File(file) {
  return sha256(fs.readFileSync(file));
}

function readJson(file) {
  const content = fs.readFileSync(file, 'utf8');
  const value = JSON.parse(content);
  if (jsonType(value) !== 'object') throw new Error('JSON artifact root must be an object');
  return value;
}

function readCheckedArtifacts(rootDir = ROOT) {
  const directory = path.join(rootDir, FIXTURE_DIRECTORY);
  const manifestPath = path.join(directory, 'manifest.json');
  const goldenPath = path.join(directory, GOLDEN_NAME);
  const errorsPath = path.join(directory, ERROR_NAME);
  const manifest = readJson(manifestPath);
  const golden = readJson(goldenPath);
  const errors = readJson(errorsPath);
  const declared = new Map((manifest.artifacts || []).map((item) => [item.name, item.sha256]));
  const generatorPath = path.join(rootDir, manifest.generator || '');
  const prerequisites = [manifest.prerequisites?.writeContract, ...(manifest.prerequisites?.p04 || [])];
  if (manifest.schemaVersion !== 1 || manifest.source?.syntheticOnly !== true ||
      manifest.normalization?.version !== NORMALIZATION_VERSION ||
      declared.get(GOLDEN_NAME) !== sha256File(goldenPath) ||
      !fs.existsSync(generatorPath) || manifest.generatorSha256 !== sha256File(generatorPath) ||
      prerequisites.some((item) => !item || !fs.existsSync(path.join(rootDir, item.path || '')) ||
        item.sha256 !== sha256File(path.join(rootDir, item.path))) ||
      manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false ||
      manifest.writerHandoff?.nodeClosedBeforeArtifactCommit !== true ||
      golden.version !== manifest.version || golden.normalization?.version !== NORMALIZATION_VERSION ||
      errors.schemaVersion !== 1 || errors.fixturePolicy?.syntheticOnly !== true) {
    throw new Error('checked P05 mutation artifacts are incompatible');
  }
  const scenarioIDs = golden.scenarios?.map((scenario) => scenario.id);
  if (!Array.isArray(scenarioIDs) || new Set(scenarioIDs).size !== scenarioIDs.length ||
      JSON.stringify(scenarioIDs) !== JSON.stringify(manifest.scenarios)) {
    throw new Error('checked P05 scenario manifest drifted');
  }
  const errorScenarioIDs = golden.scenarios
    .filter((scenario) => scenario.response?.ok === false)
    .map((scenario) => scenario.id)
    .sort();
  if (JSON.stringify(errorScenarioIDs) !== JSON.stringify(Object.keys(errors.scenarios || {}).sort()) ||
      !Array.isArray(errors.httpCatalog) || new Set(errors.httpCatalog.map((item) => item.code)).size !== errors.httpCatalog.length ||
      errors.httpCatalog.some((item) => typeof item.code !== 'string' || !Number.isInteger(item.status) ||
        item.status < 400 || item.status > 599 || typeof item.retryable !== 'boolean')) {
    throw new Error('checked P05 stable error catalog drifted');
  }
  assertSanitized(golden);
  assertSanitized(errors);
  return { manifest, golden, errors };
}

function compareStructured(expected, actual, label = 'mutation') {
  const differences = [];
  collectDifferences(expected, actual, [], differences);
  if (differences.length) throw new GoldenMismatchError(label, differences);
  return true;
}

function collectDifferences(expected, actual, trail, differences) {
  if (differences.length >= MAXIMUM_DIFFERENCES) return;
  const expectedType = jsonType(expected);
  const actualType = jsonType(actual);
  if (expectedType !== actualType) {
    differences.push({ pointer: pointer(trail), reason: `type:${expectedType}->${actualType}` });
    return;
  }
  if (expectedType === 'array') {
    if (expected.length !== actual.length) {
      differences.push({ pointer: pointer(trail), reason: `length:${expected.length}->${actual.length}` });
    }
    for (let index = 0; index < Math.min(expected.length, actual.length); index += 1) {
      collectDifferences(expected[index], actual[index], [...trail, index], differences);
    }
    return;
  }
  if (expectedType === 'object') {
    const expectedKeys = Object.keys(expected).sort();
    const actualKeys = Object.keys(actual).sort();
    for (const key of expectedKeys) {
      if (!Object.prototype.hasOwnProperty.call(actual, key)) {
        differences.push({ pointer: pointer([...trail, key]), reason: 'missing' });
      } else {
        collectDifferences(expected[key], actual[key], [...trail, key], differences);
      }
    }
    for (const key of actualKeys) {
      if (!Object.prototype.hasOwnProperty.call(expected, key)) {
        differences.push({ pointer: pointer([...trail, key]), reason: 'unknown' });
      }
    }
    return;
  }
  if (!Object.is(expected, actual)) differences.push({ pointer: pointer(trail), reason: 'value' });
}

function jsonType(value) {
  if (value === null) return 'null';
  if (Array.isArray(value)) return 'array';
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error('non-finite JSON number');
    return 'number';
  }
  if (typeof value === 'string' || typeof value === 'boolean') return typeof value;
  if (typeof value === 'object' && value !== null) return 'object';
  return 'missing';
}

function pointer(trail) {
  if (!trail.length) return '/';
  return `/${trail.map((part) => String(part).replace(/~/g, '~0').replace(/\//g, '~1')).join('/')}`;
}

function canonicalScenarioBundle(raw, options = {}) {
  const normalized = options.alreadyNormalized
    ? structuredClone(raw)
    : normalizeGolden(raw, { fixtureRoot: options.fixtureRoot }).value;
  if (!Array.isArray(normalized.scenarios)) throw new Error('mutation scenarios are missing');
  const expectedErrors = options.expectedErrors || { scenarios: {} };
  canonicalizeTimestamps(normalized);
  const versions = {};
  const scenarios = normalized.scenarios.map((scenario) => {
    const copy = structuredClone(scenario);
    if (!copy || typeof copy.id !== 'string' || !copy.response || typeof copy.response.ok !== 'boolean') {
      throw new Error('mutation scenario shape is invalid');
    }
    if (copy.response.ok) {
      if (!copy.response.snapshot) throw new Error('successful mutation snapshot is missing');
      versions[copy.id] = extractVersion(copy.response.snapshot);
      stabilizeInheritedSnapshot(copy.response.snapshot, options.inheritedSnapshotBaseline);
    } else {
      copy.response.error = canonicalError(copy.id, copy.response.error, expectedErrors);
    }
    return copy;
  });
  return {
    schemaVersion: normalized.schemaVersion,
    version: normalized.version,
    scenarios,
    handoff: normalized.handoff,
    versions,
  };
}

function canonicalizeTimestamps(value) {
  if (Array.isArray(value)) {
    value.forEach(canonicalizeTimestamps);
    return;
  }
  if (!value || typeof value !== 'object') return;
  for (const [key, child] of Object.entries(value)) {
    if (typeof child === 'string' && /^<time-\d+>$/.test(child)) value[key] = '<time>';
    else canonicalizeTimestamps(child);
  }
}

function extractVersion(snapshot) {
  if (!snapshot || typeof snapshot !== 'object' || snapshot.state === null) return null;
  const version = snapshot.state?.version;
  if (version === undefined) return null;
  if (!Number.isSafeInteger(version) || version <= 0) throw new Error('snapshot version is invalid');
  delete snapshot.state.version;
  return version;
}

function stabilizeInheritedSnapshot(snapshot, baseline) {
  assertCompleteSnapshot(snapshot);
  canonicalizeExpandedProjectSummaries(snapshot);
  if (baseline === undefined) return;
  if (!baseline || typeof baseline !== 'object') throw new Error('inherited snapshot baseline is invalid');
  // MCP documentation is frozen and deep-compared by P03. P05 verifies that
  // mutations do not alter that inherited branch while deeply comparing every
  // mutation-owned field and all top-level collection ordering.
  if (!snapshot.mcp || typeof snapshot.mcp !== 'object' || Array.isArray(snapshot.mcp) ||
      !Array.isArray(snapshot.mcp.tools) || !Array.isArray(snapshot.mcp.toolDocs)) {
    throw new Error('inherited P03 MCP branch is invalid');
  }
  assertSanitized(snapshot.mcp);
  snapshot.mcp = structuredClone(baseline);
}

function canonicalizeExpandedProjectSummaries(snapshot) {
  const compatibilityExtensions = new Set([
    'codex_reasoning_effort',
    'plan_generation_strategy', 'plan_generation_provider', 'plan_generation_command',
    'plan_generation_model', 'plan_generation_codex_reasoning_effort',
    'plan_generation_claude_base_url', 'plan_generation_claude_model',
    'plan_generation_has_claude_auth_token', 'plan_generation_claude_config_id',
    'plan_execution_strategy', 'plan_execution_provider', 'plan_execution_command',
    'plan_execution_model', 'plan_execution_codex_reasoning_effort',
    'plan_execution_claude_base_url', 'plan_execution_claude_model',
    'plan_execution_has_claude_auth_token', 'plan_execution_claude_config_id',
  ]);
  for (const project of snapshot.projects) {
    if (!project || typeof project !== 'object' || Array.isArray(project)) {
      throw new Error('project summary is invalid');
    }
    for (const key of Object.keys(project)) {
      if (compatibilityExtensions.has(key)) delete project[key];
    }
  }
}

function inheritedMCPBaseline(golden) {
  const snapshots = golden.scenarios
    .filter((scenario) => scenario.response?.ok)
    .map((scenario) => scenario.response.snapshot);
  if (!snapshots.length) throw new Error('checked mutation snapshots are missing');
  const baseline = snapshots[0].mcp;
  for (let index = 1; index < snapshots.length; index += 1) {
    compareStructured(baseline, snapshots[index].mcp, `checked/mcp/${index}`);
  }
  return baseline;
}

function assertCompleteSnapshot(snapshot) {
  const required = [
    'activeProjectId', 'activeProject', 'projects', 'mcp', 'state', 'requirements', 'feedback',
    'attachments', 'plans', 'tasks', 'events', 'scans', 'scanSummary', 'scripts', 'executors',
    'terminals', 'activeOperation', 'activeOperations', 'lastOperation',
  ];
  const keys = Object.keys(snapshot).sort();
  if (JSON.stringify(keys) !== JSON.stringify([...required].sort())) {
    throw new Error('snapshot top-level shape is not exact');
  }
  for (const key of [
    'projects', 'requirements', 'feedback', 'attachments', 'plans', 'tasks', 'events', 'scans',
    'scripts', 'executors', 'terminals', 'activeOperations',
  ]) {
    if (!Array.isArray(snapshot[key])) throw new Error(`snapshot collection is invalid: ${key}`);
  }
}

function canonicalError(scenarioID, error, expectedErrors) {
  if (!error || typeof error.code !== 'string') throw new Error('mutation error is invalid');
  const expected = expectedErrors.scenarios?.[scenarioID];
  if (!expected) throw new Error(`unexpected mutation error scenario: ${scenarioID}`);
  const aliases = [expected.canonical, ...(expected.node || []), ...(expected.go || [])];
  if (!aliases.includes(error.code)) throw new Error(`mutation error code drifted: ${scenarioID}`);
  return { code: expected.canonical };
}

function compareMutationBundles(nodeRaw, goRaw, expectedErrors) {
  const baseline = inheritedMCPBaseline(nodeRaw);
  const node = canonicalScenarioBundle(nodeRaw, {
    alreadyNormalized: true,
    expectedErrors,
    inheritedSnapshotBaseline: baseline,
  });
  const go = canonicalScenarioBundle(goRaw, {
    fixtureRoot: goRaw.fixtureRoot,
    expectedErrors,
    inheritedSnapshotBaseline: baseline,
  });
  compareStructured(node.scenarios, go.scenarios, 'node/go/scenarios');
  compareStructured(node.handoff, go.handoff, 'node/go/writer-handoff');
  validateVersionTrace(go.versions, expectedErrors.versionTrace);
  return { node, go };
}

function validateDatabaseRecord(value) {
  if (!value || value.kind !== 'authorized-transactional-test-copy' || value.schema_version !== 1 ||
      !/^[a-f0-9]{64}$/.test(value.before_sha256 || '') ||
      !/^[a-f0-9]{64}$/.test(value.after_sha256 || '') ||
      value.before_sha256 === value.after_sha256) {
    throw new Error('Go transactional fixture hash record is invalid');
  }
}

function validateVersionTrace(actual, expected) {
  if (!expected || typeof expected !== 'object') throw new Error('expected version trace is missing');
  for (const [scenario, rule] of Object.entries(expected)) {
    const value = actual[scenario];
    if (rule === null) {
      if (value !== null) throw new Error(`unexpected version for ${scenario}`);
    } else if (!Number.isSafeInteger(value) || value !== rule) {
      throw new Error(`version trace drifted for ${scenario}`);
    }
  }
}

function runGoMutationExport(rootDir, temporaryRoot, outputPath, spawn = spawnSync) {
  const result = spawn('go', [
    'test', './internal/application/projects', '-run', GO_EXPORT_TEST, '-count=1',
  ], {
    cwd: path.join(rootDir, 'backend'),
    env: {
      ...process.env,
      AUTOPLAN_P05_ALLOWED_ROOT: temporaryRoot,
      AUTOPLAN_P05_GO_OUTPUT: outputPath,
    },
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.error || result.status !== 0 || !fs.existsSync(outputPath)) {
    throw new Error(`Go mutation export failed with status ${Number.isInteger(result.status) ? result.status : -1}`);
  }
  return readJson(outputPath);
}

async function compareMutationGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const checked = options.checked || readCheckedArtifacts(rootDir);
  const temporaryRoot = options.temporaryRoot || fs.mkdtempSync(path.join(os.tmpdir(), TEMPORARY_PREFIX));
  const ownsTemporaryRoot = !options.temporaryRoot;
  const timeline = [];
  try {
    timeline.push('node-golden-read');
    const nodeHash = sha256(Buffer.from(JSON.stringify(checked.golden)));
    timeline.push('node-closed');
    const goOutput = path.join(temporaryRoot, 'go-mutations.json');
    timeline.push('go-open');
    const goRaw = options.readGo
      ? await options.readGo(rootDir, temporaryRoot, checked)
      : runGoMutationExport(rootDir, temporaryRoot, goOutput, options.spawn);
    timeline.push('go-closed');
    if (JSON.stringify(timeline) !== JSON.stringify([
      'node-golden-read', 'node-closed', 'go-open', 'go-closed',
    ])) throw new Error('writer handoff ordering drifted');
    validateDatabaseRecord(goRaw.database);
    const compared = compareMutationBundles(checked.golden, goRaw, checked.errors);
    assertSanitized(compared.go);
    return {
      ok: true,
      normalizationVersion: NORMALIZATION_VERSION,
      nodeGoldenSha256: nodeHash,
      goOutputSha256: sha256(Buffer.from(JSON.stringify(goRaw))),
      nodeDatabaseBeforeSha256: checked.manifest?.source?.databaseBeforeSha256 || null,
      nodeDatabaseAfterSha256: checked.manifest?.source?.databaseAfterSha256 || null,
      fixtureRecipeSha256: checked.manifest?.source?.fixtureRecipeSha256 || null,
      goDatabaseBeforeSha256: goRaw.database.before_sha256,
      goDatabaseAfterSha256: goRaw.database.after_sha256,
      scenarios: compared.go.scenarios.map((scenario) => scenario.id),
      writerTimeline: timeline,
    };
  } finally {
    if (ownsTemporaryRoot) safeRemove(temporaryRoot);
  }
}

function assertSanitized(value) {
  const encoded = JSON.stringify(value);
  const lower = encoded.toLowerCase().replace(/\\/g, '/');
  const forbidden = [
    /(?:api[_-]?key|auth[_-]?token|session[_-]?credential)\s*["']?\s*[:=]\s*["'][^<]/i,
    /\bsk-[a-z0-9_-]{12,}\b/i,
    /\b(?:appdata|localappdata|userdata)\b/i,
    /(?:[a-z]:\/users\/|\/home\/|\/users\/)/i,
  ];
  if (forbidden.some((pattern) => pattern.test(lower))) {
    throw new Error('mutation artifact contains sensitive material');
  }
}

function safeRemove(directory) {
  const resolved = path.resolve(directory);
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  if (!path.basename(resolved).startsWith(TEMPORARY_PREFIX) || relative.startsWith('..') || path.isAbsolute(relative)) {
    throw new Error('temporary cleanup boundary failed');
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

if (require.main === module) {
  compareMutationGolden()
    .then((result) => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch((error) => {
      process.stderr.write(`${error instanceof GoldenMismatchError ? error.message : 'mutation golden comparison failed safely'}\n`);
      process.exitCode = 1;
    });
}

module.exports = {
  GoldenMismatchError,
  canonicalScenarioBundle,
  collectDifferences,
  compareMutationBundles,
  compareMutationGolden,
  compareStructured,
  pointer,
  readCheckedArtifacts,
  runGoMutationExport,
  validateDatabaseRecord,
  validateVersionTrace,
};
