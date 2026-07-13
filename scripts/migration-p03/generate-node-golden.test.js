'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  BlockedError,
  buildGoldenBundle,
  generateNodeGolden,
  parseArgs,
  validateExplicitDatabase,
  verifyPrerequisites,
} = require('./generate-node-golden');
const {
  NORMALIZATION_VERSION,
  REDACTED_ENV_VARS,
  normalizeContracts,
} = require('./normalize-contract');

const ROOT = path.resolve(__dirname, '../..');
const PASSED_GATES = Object.freeze({
  p00: { status: 'completed', frozenSourcesSha256: '0'.repeat(64) },
  p01: { status: 'completed', frozenSourcesSha256: '1'.repeat(64) },
  p02: { status: 'completed', frozenSourcesSha256: '2'.repeat(64) },
});

function temporaryDirectory(label) {
  return fs.mkdtempSync(path.join(os.tmpdir(), `autoplan-p03-test-${label}-`));
}

function removeTemporaryDirectory(directory) {
  fs.rmSync(directory, { recursive: true, force: true });
}

test('normalization preserves contract meaning while stabilizing approved fields', () => {
  const temporaryRoot = temporaryDirectory('normalize');
  try {
    const source = {
      projects: [
        { id: 42, name: 'Second', workspace_path: path.join(temporaryRoot, 'second'), created_at: '2026-01-02T03:04:05Z', updated_at: '2026-01-02T03:04:06+00:00' },
        { id: 7, name: 'First', workspace_path: '/__autoplan_fixture__/first', created_at: '2026-01-02T03:04:05.000Z', updated_at: '2026-01-02T03:04:05.000Z' },
      ],
      snapshot: {
        activeProjectId: 42,
        activeProject: { id: 42 },
        state: { project_id: 42, env_vars: '[{"name":"A","value":"private"}]', optional: null, enabled: false, count: 0 },
        activeOperations: [],
      },
    };
    const result = normalizeContracts(source, { fixtureRoot: temporaryRoot });
    assert.equal(result.metadata.version, NORMALIZATION_VERSION);
    assert.deepEqual(result.metadata.idMaps.projects, { 7: 1, 42: 2 });
    assert.equal(result.value.projects[0].id, 2);
    assert.equal(result.value.projects[0].workspace_path, '<fixture-root>/second');
    assert.equal(result.value.projects[1].workspace_path, '<fixture-workspace>/first');
    assert.equal(result.value.projects[0].updated_at, '2026-01-02T03:04:06.000Z');
    assert.equal(result.value.snapshot.state.env_vars, REDACTED_ENV_VARS);
    assert.equal(result.value.snapshot.state.optional, null);
    assert.equal(result.value.snapshot.state.enabled, false);
    assert.equal(result.value.snapshot.state.count, 0);
    assert.deepEqual(result.value.snapshot.activeOperations, []);
  } finally {
    removeTemporaryDirectory(temporaryRoot);
  }
});

test('missing prerequisite evidence blocks before any golden output is created', async () => {
  const fakeRoot = temporaryDirectory('blocked-root');
  const output = path.join(fakeRoot, 'fixtures', 'migration', 'p03');
  try {
    assert.throws(() => verifyPrerequisites(fakeRoot), (error) => (
      error instanceof BlockedError && error.reason === 'p00_evidence_missing'
    ));
    await assert.rejects(
      generateNodeGolden({ rootDir: fakeRoot, outputDir: output }),
      (error) => error instanceof BlockedError && error.reason === 'p00_evidence_missing',
    );
    assert.equal(fs.existsSync(output), false);
  } finally {
    removeTemporaryDirectory(fakeRoot);
  }
});

test('P00-derived Node contracts and manifest are byte reproducible', async () => {
  const outputA = temporaryDirectory('output-a');
  const outputB = temporaryDirectory('output-b');
  const gateCheck = () => PASSED_GATES;
  try {
    await generateNodeGolden({ rootDir: ROOT, outputDir: outputA, allowExternalOutput: true, gateCheck });
    await generateNodeGolden({ rootDir: ROOT, outputDir: outputB, allowExternalOutput: true, gateCheck });
    for (const name of ['manifest.json', 'projects.golden.json', 'snapshot-empty.golden.json', 'snapshot-project.golden.json']) {
      assert.deepEqual(fs.readFileSync(path.join(outputA, name)), fs.readFileSync(path.join(outputB, name)), name);
    }
    const projects = JSON.parse(fs.readFileSync(path.join(outputA, 'projects.golden.json'), 'utf8'));
    const empty = JSON.parse(fs.readFileSync(path.join(outputA, 'snapshot-empty.golden.json'), 'utf8'));
    const project = JSON.parse(fs.readFileSync(path.join(outputA, 'snapshot-project.golden.json'), 'utf8'));
    assert.deepEqual(projects.map((item) => item.id), [3, 2, 1]);
    assert.deepEqual(empty.projects, projects);
    assert.equal(empty.activeProjectId, null);
    assert.equal(project.activeProjectId, 1);
    assert.equal(project.state.env_vars, REDACTED_ENV_VARS);
    assert.equal(project.state.plan_generation_claude_auth_token, '····mask');
    assert.equal(project.state.plan_generation_has_claude_auth_token, true);
    assert.deepEqual(project.requirements, []);
    assert.deepEqual(project.activeOperations, []);
  } finally {
    removeTemporaryDirectory(outputA);
    removeTemporaryDirectory(outputB);
  }
});

test('bundle records missing-project equivalence without persisting a database', async () => {
  const bundle = await buildGoldenBundle({ rootDir: ROOT, gateCheck: () => PASSED_GATES });
  assert.equal(bundle.manifest.scenarios.snapshotMissing, 'structurally identical to snapshot-empty.golden.json');
  assert.deepEqual(Object.keys(bundle.artifacts).sort(), [
    'manifest.json',
    'projects.golden.json',
    'snapshot-empty.golden.json',
    'snapshot-project.golden.json',
  ]);
});

test('explicit copies require authorization and production database names remain forbidden', () => {
  const root = temporaryDirectory('explicit');
  try {
    const productionName = path.join(root, 'autoplan.sqlite');
    fs.writeFileSync(productionName, 'not-a-database');
    assert.throws(
      () => validateExplicitDatabase(productionName, root, true),
      (error) => error instanceof BlockedError && error.reason === 'database_path_forbidden',
    );
    const copy = path.join(root, 'sanitized-copy.sqlite');
    fs.writeFileSync(copy, 'not-a-database');
    assert.throws(
      () => validateExplicitDatabase(copy, root, false),
      (error) => error instanceof BlockedError && error.reason === 'database_copy_not_marked_sanitized',
    );
  } finally {
    removeTemporaryDirectory(root);
  }
});

test('CLI parser has no force, skip, or implicit sanitized-copy mode', () => {
  assert.deepEqual(parseArgs(['--database', 'D:\\fixture\\copy.sqlite', '--allow-root', 'D:\\fixture', '--sanitized-copy']), {
    databasePath: 'D:\\fixture\\copy.sqlite',
    allowRoot: 'D:\\fixture',
    sanitizedCopy: true,
  });
  assert.throws(() => parseArgs(['--force']), /未知参数/);
  assert.throws(() => parseArgs(['--skip-gates']), /未知参数/);
});
