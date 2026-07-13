'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');

const {
  GoldenMismatchError,
  compareMutationBundles,
  compareMutationGolden,
  compareStructured,
  readCheckedArtifacts,
  validateVersionTrace,
} = require('./compare-mutation-golden');

function snapshot(projectID, workspace, version) {
  const project = {
    id: projectID,
    name: 'Synthetic',
    workspace_path: workspace,
    description: '',
    created_at: '<time-1>',
    updated_at: '<time-2>',
  };
  return {
    activeProjectId: projectID,
    activeProject: project,
    projects: [{ ...project, running: 0, phase: 'idle', interval_seconds: 5 }],
    mcp: { enabled: false, toolDocs: [], tools: [] },
    state: {
      project_id: projectID,
      workspace_path: workspace,
      running: 0,
      phase: 'idle',
      interval_seconds: 5,
      version,
    },
    requirements: [],
    feedback: [],
    attachments: [],
    plans: [],
    tasks: [],
    events: [],
    scans: [],
    scanSummary: { count: 0 },
    scripts: [],
    executors: [],
    terminals: [],
    activeOperation: null,
    activeOperations: [],
    lastOperation: null,
  };
}

function bundles(fixtureRoot) {
  const nodeSnapshot = snapshot(1, '<fixture-root>/workspace', undefined);
  delete nodeSnapshot.state.version;
  const goSnapshot = snapshot(41, path.join(fixtureRoot, 'workspace'), 2);
  goSnapshot.activeProject.created_at = '2026-07-11T00:00:00.000Z';
  goSnapshot.activeProject.updated_at = '2026-07-11T00:00:01.000Z';
  goSnapshot.projects[0].created_at = '2026-07-11T00:00:00.000Z';
  goSnapshot.projects[0].updated_at = '2026-07-11T00:00:01.000Z';
  const node = {
    schemaVersion: 1,
    version: 'p05-node-mutation-golden-v1',
    scenarios: [
      { id: 'create', request: { workspacePath: '<fixture-root>/workspace' }, response: { ok: true, snapshot: nodeSnapshot } },
      { id: 'missing-delete', request: { projectId: 999999 }, response: { ok: false, error: { code: 'PROJECT_NOT_FOUND', message: 'synthetic' } } },
    ],
    handoff: { sqlJsClosed: true, databaseOwnerReleased: true },
  };
  const go = {
    schemaVersion: 1,
    version: 'p05-node-mutation-golden-v1',
    fixtureRoot,
    database: {
      kind: 'authorized-transactional-test-copy',
      schema_version: 1,
      before_sha256: 'a'.repeat(64),
      after_sha256: 'b'.repeat(64),
    },
    scenarios: [
      { id: 'create', request: { workspacePath: path.join(fixtureRoot, 'workspace') }, response: { ok: true, snapshot: goSnapshot } },
      { id: 'missing-delete', request: { projectId: 999999 }, response: { ok: false, error: { code: 'project_not_found' } } },
    ],
    handoff: { sqlJsClosed: true, databaseOwnerReleased: true },
  };
  const errors = {
    scenarios: {
      'missing-delete': {
        canonical: 'PROJECT_NOT_FOUND',
        node: ['PROJECT_NOT_FOUND'],
        go: ['project_not_found'],
      },
    },
    versionTrace: { create: 2 },
  };
  return { node, go, errors };
}

describe('P05 mutation golden comparator', () => {
  it('rejects missing, unknown, null, type, value, and ordering drift with redacted JSON Pointers', () => {
    const cases = [
      [{ a: 1 }, {}, '/a', 'missing'],
      [{}, { extra: true }, '/extra', 'unknown'],
      [{ a: null }, { a: '' }, '/a', 'type:null->string'],
      [{ a: 1 }, { a: '1' }, '/a', 'type:number->string'],
      [{ a: false }, { a: true }, '/a', 'value'],
      [{ rows: [1, 2] }, { rows: [2, 1] }, '/rows/0', 'value'],
      [{ 'a/b': 1 }, { 'a/b': 2 }, '/a~1b', 'value'],
    ];
    for (const [expected, actual, pointer, reason] of cases) {
      assert.throws(() => compareStructured(expected, actual), (error) => {
        assert.ok(error instanceof GoldenMismatchError);
        assert.deepStrictEqual(error.differences[0], { pointer, reason });
        assert.doesNotMatch(error.message, /true|false|Synthetic/);
        return true;
      });
    }
  });

  it('deep-compares complete snapshots, canonical errors, project IDs, paths, and version trace', () => {
    const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p05-comparator-test-'));
    try {
      const { node, go, errors } = bundles(temporaryRoot);
      const result = compareMutationBundles(node, go, errors);
      assert.equal(result.go.versions.create, 2);
      assert.equal(result.go.scenarios[0].response.snapshot.state.version, undefined);
      assert.deepStrictEqual(result.go.scenarios[1].response.error, { code: 'PROJECT_NOT_FOUND' });

      const drift = structuredClone(go);
      drift.scenarios[0].response.snapshot.projects[0].phase = 'running';
      assert.throws(
        () => compareMutationBundles(node, drift, errors),
        (error) => error instanceof GoldenMismatchError &&
          error.differences.some((item) => item.pointer.endsWith('/projects/0/phase')),
      );
    } finally {
      fs.rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('requires exact optimistic version outcomes', () => {
    assert.doesNotThrow(() => validateVersionTrace({ create: 2, delete: null }, { create: 2, delete: null }));
    assert.throws(() => validateVersionTrace({ create: 3 }, { create: 2 }), /version trace drifted/);
  });

  it('proves Node close precedes Go export and input marker bytes remain unchanged', async () => {
    const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p05-compare-injected-'));
    try {
      const marker = path.join(temporaryRoot, 'sanitized-copy.sqlite');
      fs.writeFileSync(marker, 'synthetic immutable marker');
      const before = crypto.createHash('sha256').update(fs.readFileSync(marker)).digest('hex');
      const { node, go, errors } = bundles(temporaryRoot);
      const checked = {
        golden: node,
        errors,
        manifest: { schemaVersion: 1 },
      };
      const result = await compareMutationGolden({
        rootDir: process.cwd(),
        temporaryRoot,
        checked,
        readGo: async () => go,
      });
      assert.deepStrictEqual(result.writerTimeline, [
        'node-golden-read', 'node-closed', 'go-open', 'go-closed',
      ]);
      assert.equal(
        crypto.createHash('sha256').update(fs.readFileSync(marker)).digest('hex'),
        before,
      );
    } finally {
      fs.rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('checks committed artifact hashes and exposes no update/ignore/coercion escape hatch', () => {
    const checked = readCheckedArtifacts(process.cwd());
    assert.equal(checked.golden.scenarios.length, checked.manifest.scenarios.length);
    const source = fs.readFileSync(path.join(__dirname, 'compare-mutation-golden.js'), 'utf8');
    assert.doesNotMatch(source, /updateGolden|ignoreFields|coerce|writeFileSync.*golden/i);
    assert.match(source, /reason: 'unknown'/);
    assert.match(source, /node-closed.*go-open/s);
  });
});
