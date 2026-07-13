'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');

const {
  GoldenMismatchError,
  compareGolden,
  compareStructured,
} = require('./compare-golden');

function digest(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function contracts() {
  const project = {
    id: 1, name: 'Synthetic', workspace_path: '<fixture-workspace>/project', description: '',
    created_at: '2026-01-02T03:04:05.000Z', updated_at: '2026-01-02T03:04:06.000Z',
  };
  const emptySnapshot = completeSnapshot([project], null);
  const projectSnapshot = completeSnapshot([project], project);
  return { projects: [project], emptySnapshot, projectSnapshot };
}

function completeSnapshot(projects, activeProject) {
  return {
    activeProjectId: activeProject?.id ?? null, activeProject, projects, mcp: {},
    state: activeProject ? { project_id: activeProject.id, workspace_path: activeProject.workspace_path } : null,
    requirements: [], feedback: [], attachments: [], plans: [], tasks: [], events: [], scans: [],
    scanSummary: {}, scripts: [], executors: [], terminals: [], activeOperation: null,
    activeOperations: [], lastOperation: null,
  };
}

describe('P03 structured golden comparator', () => {
  it('rejects unknown, missing, null, type, value, and array-order drift with JSON Pointers', () => {
    const cases = [
      [{ a: 1 }, { a: 1, extra: true }, '/extra', 'unknown'],
      [{ a: 1 }, {}, '/a', 'missing'],
      [{ a: null }, { a: '' }, '/a', 'type:null->string'],
      [{ a: 1 }, { a: '1' }, '/a', 'type:number->string'],
      [{ a: true }, { a: false }, '/a', 'value'],
      [{ rows: [{ id: 1 }, { id: 2 }] }, { rows: [{ id: 2 }, { id: 1 }] }, '/rows/0/id', 'value'],
      [{ 'a/b': 1 }, { 'a/b': 2 }, '/a~1b', 'value'],
    ];
    for (const [expected, actual, pointer, reason] of cases) {
      assert.throws(() => compareStructured(expected, actual), (error) => {
        assert.ok(error instanceof GoldenMismatchError);
        assert.deepStrictEqual(error.differences[0], { pointer, reason });
        assert.doesNotMatch(error.message, /"extra"|true|false|Synthetic/);
        return true;
      });
    }
  });

  it('accepts only exact JSON structures and preserves number/boolean/null semantics', () => {
    const value = { count: 1, enabled: false, nullable: null, rows: [{ id: 1 }] };
    assert.equal(compareStructured(value, structuredClone(value)), true);
  });

  it('serializes Node close before Go read and proves fixture bytes remain immutable', async () => {
    const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p03-compare-test-'));
    try {
      const databasePath = path.join(temporaryRoot, 'synthetic.sqlite');
      const bytes = Buffer.from('synthetic immutable database bytes');
      fs.writeFileSync(databasePath, bytes);
      const expectedHash = digest(bytes);
      const expected = contracts();
      const order = [];
      const result = await compareGolden({
        rootDir: process.cwd(),
        temporaryRoot,
        databasePath,
        checkedGolden: expected,
        readNode: async () => {
          order.push('node-open');
          order.push('node-closed');
          return { ...expected, missingSnapshotEquivalent: true, requestedProjectId: 1 };
        },
        readGo: async () => {
          order.push('go-open');
          assert.deepStrictEqual(order, ['node-open', 'node-closed', 'go-open']);
          return { ...expected, missingSnapshot: 'project_not_found' };
        },
      });
      assert.equal(result.databaseSha256, expectedHash);
      assert.equal(digest(fs.readFileSync(databasePath)), expectedHash);
    } finally {
      fs.rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('never exposes an update-golden or ignore-fields escape hatch', () => {
    const source = fs.readFileSync(path.join(__dirname, 'compare-golden.js'), 'utf8');
    assert.doesNotMatch(source, /updateGolden|ignoreFields|coerce|writeFileSync.*golden/i);
    assert.match(source, /node\/go\/\$\{scenario\}/);
    assert.match(source, /database changed during Go read/);
  });
});
