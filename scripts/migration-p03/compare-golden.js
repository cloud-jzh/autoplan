'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const {
  createGeneratedDatabase,
  readNodeContracts,
} = require('./generate-node-golden');
const {
  NORMALIZATION_VERSION,
  assertSanitizedContract,
  normalizeContracts,
} = require('./normalize-contract');

const ROOT = path.resolve(__dirname, '../..');
const GOLDEN_ROOT = path.join('fixtures', 'migration', 'p03');
const GO_EXPORT_TEST = '^TestGoldenExport$';
const SCENARIOS = Object.freeze([
  ['projects', 'projects.golden.json'],
  ['emptySnapshot', 'snapshot-empty.golden.json'],
  ['projectSnapshot', 'snapshot-project.golden.json'],
]);

class GoldenMismatchError extends Error {
  constructor(label, differences) {
    const pointers = differences.slice(0, 20).map((item) => item.pointer).join(', ');
    super(`golden mismatch (${label}): ${pointers}`);
    this.name = 'GoldenMismatchError';
    this.code = 'P03_GOLDEN_MISMATCH';
    this.label = label;
    this.differences = differences.map(({ pointer, reason }) => ({ pointer, reason }));
  }
}

function sha256File(file) {
  return crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex');
}

function readCheckedGolden(rootDir = ROOT) {
  const directory = path.join(rootDir, GOLDEN_ROOT);
  const manifest = readJson(path.join(directory, 'manifest.json'));
  if (manifest.schemaVersion !== 1 || manifest.normalization?.version !== NORMALIZATION_VERSION ||
      manifest.source?.syntheticOnly !== true) {
    throw new Error('checked golden manifest is incompatible');
  }
  const normalizer = path.join(rootDir, manifest.normalization.source || '');
  if (!fs.existsSync(normalizer) || sha256File(normalizer) !== manifest.normalization.sourceSha256) {
    throw new Error('checked golden normalizer digest mismatch');
  }
  const declared = new Map((manifest.artifacts || []).map((item) => [item.name, item.sha256]));
  const result = {};
  for (const [scenario, name] of SCENARIOS) {
    const file = path.join(directory, name);
    const digest = sha256File(file);
    if (!/^[a-f0-9]{64}$/.test(declared.get(name) || '') || declared.get(name) !== digest) {
      throw new Error('checked golden artifact digest mismatch');
    }
    result[scenario] = readJson(file);
    assertSanitizedContract(result[scenario]);
  }
  return { manifest, scenarios: result };
}

function readJson(file) {
  const content = fs.readFileSync(file, 'utf8');
  const value = JSON.parse(content);
  if (JSON.stringify(value) === undefined) throw new Error('invalid JSON artifact');
  return value;
}

function compareStructured(expected, actual, label = 'contract') {
  const differences = [];
  collectDifferences(expected, actual, [], differences);
  if (differences.length) throw new GoldenMismatchError(label, differences);
  return true;
}

function collectDifferences(expected, actual, trail, differences) {
  if (differences.length >= 100) return;
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
    const count = Math.min(expected.length, actual.length);
    for (let index = 0; index < count; index += 1) {
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
  if (typeof value === 'object') return 'object';
  return 'missing';
}

function pointer(trail) {
  if (!trail.length) return '/';
  return `/${trail.map((part) => String(part).replace(/~/g, '~0').replace(/\//g, '~1')).join('/')}`;
}

function normalizedScenarios(raw, fixtureRoot) {
  const normalized = normalizeContracts({
    projects: raw.projects,
    emptySnapshot: raw.emptySnapshot,
    projectSnapshot: raw.projectSnapshot,
  }, { fixtureRoot });
  if (normalized.metadata.version !== NORMALIZATION_VERSION) throw new Error('normalization version drift');
  return normalized;
}

function compareScenarioBundles(nodeBundle, goBundle, checked) {
  for (const [scenario] of SCENARIOS) {
    compareStructured(checked[scenario], nodeBundle.value[scenario], `checked/node/${scenario}`);
    compareStructured(checked[scenario], goBundle.value[scenario], `checked/go/${scenario}`);
    compareStructured(nodeBundle.value[scenario], goBundle.value[scenario], `node/go/${scenario}`);
  }
  compareStructured(nodeBundle.metadata.idMaps, goBundle.metadata.idMaps, 'node/go/id-maps');
  return true;
}

function runGoExport(rootDir, databasePath, allowRoot, outputPath, projectId, spawn = spawnSync) {
  const result = spawn('go', [
    'test', './internal/application/projects', '-run', GO_EXPORT_TEST, '-count=1',
  ], {
    cwd: path.join(rootDir, 'backend'),
    env: {
      ...process.env,
      AUTOPLAN_P03_DATABASE: databasePath,
      AUTOPLAN_P03_ALLOWED_ROOT: allowRoot,
      AUTOPLAN_P03_GO_OUTPUT: outputPath,
      AUTOPLAN_P03_PROJECT_ID: String(projectId),
    },
    encoding: 'utf8',
    windowsHide: true,
    maxBuffer: 4 * 1024 * 1024,
  });
  if (result.error || result.status !== 0 || !fs.existsSync(outputPath)) {
    throw new Error(`Go golden export failed with status ${Number.isInteger(result.status) ? result.status : -1}`);
  }
  return readJson(outputPath);
}

async function compareGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const temporaryRoot = options.temporaryRoot || fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p03-compare-'));
  const ownsTemporaryRoot = !options.temporaryRoot;
  try {
    const source = options.databasePath
      ? { databasePath: path.resolve(options.databasePath) }
      : await (options.createDatabase || createGeneratedDatabase)(rootDir, temporaryRoot);
    const databasePath = source.databasePath;
    const before = sha256File(databasePath);
    const nodeRaw = await (options.readNode || readNodeContracts)(rootDir, databasePath);
    const afterNode = sha256File(databasePath);
    if (before !== afterNode) throw new Error('database changed during Node read');
    const goOutput = path.join(temporaryRoot, 'go-contracts.json');
    const goRaw = options.readGo
      ? await options.readGo(rootDir, databasePath, temporaryRoot, nodeRaw.requestedProjectId)
      : runGoExport(rootDir, databasePath, temporaryRoot, goOutput, nodeRaw.requestedProjectId, options.spawn);
    const afterGo = sha256File(databasePath);
    if (before !== afterGo) throw new Error('database changed during Go read');
    if (nodeRaw.missingSnapshotEquivalent !== true || goRaw.missingSnapshot !== 'project_not_found') {
      throw new Error('missing project snapshot semantics drifted');
    }
    const nodeBundle = normalizedScenarios(nodeRaw, temporaryRoot);
    const goBundle = normalizedScenarios(goRaw, temporaryRoot);
    const checkedBundle = options.checkedGolden || readCheckedGolden(rootDir);
    const checked = checkedBundle.scenarios || checkedBundle;
    compareScenarioBundles(nodeBundle, goBundle, checked);
    if (checkedBundle.manifest) {
      compareStructured(
        checkedBundle.manifest.normalization.idMaps,
        nodeBundle.metadata.idMaps,
        'checked/node/id-maps',
      );
    }
    return {
      ok: true,
      normalizationVersion: NORMALIZATION_VERSION,
      databaseSha256: before,
      scenarios: SCENARIOS.map(([scenario]) => scenario),
    };
  } finally {
    if (ownsTemporaryRoot) safeRemove(temporaryRoot);
  }
}

function safeRemove(directory) {
  const resolved = path.resolve(directory);
  const relative = path.relative(path.resolve(os.tmpdir()), resolved);
  if (!path.basename(resolved).startsWith('autoplan-p03-compare-') || relative.startsWith('..') || path.isAbsolute(relative)) {
    throw new Error('temporary cleanup boundary failed');
  }
  fs.rmSync(resolved, { recursive: true, force: true });
}

if (require.main === module) {
  compareGolden()
    .then((result) => process.stdout.write(`${JSON.stringify(result)}\n`))
    .catch((error) => {
      process.stderr.write(`${error instanceof GoldenMismatchError ? error.message : 'golden comparison failed safely'}\n`);
      process.exitCode = 1;
    });
}

module.exports = {
  GoldenMismatchError,
  SCENARIOS,
  collectDifferences,
  compareGolden,
  compareScenarioBundles,
  compareStructured,
  normalizedScenarios,
  readCheckedGolden,
  runGoExport,
};
