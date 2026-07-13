'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { describe, it } = require('node:test');

const {
  scanReadonlySources,
  scanSourceText,
  sha256File,
  verifyFileUnchanged,
} = require('./check-readonly');

describe('P03 readonly source guard', () => {
  it('accepts the checked repository, routes, and OpenAPI boundary', () => {
    const result = scanReadonlySources(path.resolve(__dirname, '../..'));
    assert.equal(result.ok, true, JSON.stringify(result.findings));
    assert.deepEqual(result.authorizedTables, ['project_states', 'projects', 'settings']);
    assert.ok(result.inspectedFiles.length >= 8);
  });

  it('rejects SQL execution, mutation SQL, write handles, and migrations', () => {
    const samples = [
      'func (r *Reader) bad() { r.db.Exec("INSERT INTO projects VALUES (1)") }',
      'func (r *Reader) bad() { os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600) }',
      'func bad() { os.WriteFile(name, value, 0600) }',
      'func (r *Reader) UpdateProject() {}',
      'func bad() { AutoMigrate() }',
      'import _ "modernc.org/sqlite"',
    ];
    const rules = samples.flatMap((source) => scanSourceText('repository', 'sample.go', source).map((item) => item.rule));
    assert.ok(rules.includes('sql-execution'));
    assert.ok(rules.includes('write-sql'));
    assert.ok(rules.includes('write-file-api'));
    assert.ok(rules.includes('mutation-method'));
    assert.ok(rules.includes('automatic-migration'));
    assert.ok(rules.includes('database-engine'));
  });

  it('allows schema parsing but rejects user-data discovery and unauthorized table reads', () => {
    assert.deepEqual(scanSourceText('repository', 'parser.go', 'func parseCreateTable(sql string) {}'), []);
    const discovery = scanSourceText(
      'repository',
      'unsafe.go',
      'func bad() { _, _ = os.UserHomeDir(); _ = os.Getenv("APPDATA"); db.readRows(ctx, "requirements") }',
    );
    assert.deepEqual(new Set(discovery.map((item) => item.rule)), new Set(['user-data-discovery', 'unauthorized-table']));
  });

  it('rejects project write routes and non-loopback OpenAPI servers', () => {
    const routes = scanSourceText('route', 'projects.go', 'router.Handle(http.MethodPost, ProjectsPath, endpoint)');
    assert.equal(routes[0].rule, 'write-route');
    const api = [
      'servers:',
      '  - url: http://0.0.0.0:{port}',
      'paths:',
      '  /api/v1/projects:',
      '    delete:',
    ].join('\n');
    const findings = scanSourceText('openapi', 'openapi.yaml', api);
    assert.deepEqual(new Set(findings.map((item) => item.rule)), new Set(['non-loopback-server', 'write-operation']));
  });
});

describe('P03 runtime byte guard', () => {
  it('reports identical before and after hashes for a read', async () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p03-readonly-test-'));
    try {
      const fixture = path.join(directory, 'sanitized.sqlite');
      fs.writeFileSync(fixture, Buffer.from('synthetic-readonly-fixture'));
      const result = await verifyFileUnchanged(fixture, ({ path: target }) => fs.readFileSync(target));
      assert.equal(result.unchanged, true);
      assert.equal(result.beforeSha256, result.afterSha256);
      assert.equal(result.beforeSha256, sha256File(fixture));
    } finally {
      fs.rmSync(directory, { recursive: true, force: true });
    }
  });

  it('fails closed when an operation changes bytes or creates a sidecar', async () => {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p03-readonly-test-'));
    try {
      const changed = path.join(directory, 'changed.sqlite');
      fs.writeFileSync(changed, 'before');
      await assert.rejects(
        verifyFileUnchanged(changed, ({ path: target }) => fs.writeFileSync(target, 'after')),
        /changed during operation/,
      );
      const sidecar = path.join(directory, 'sidecar.sqlite');
      fs.writeFileSync(sidecar, 'stable');
      await assert.rejects(
        verifyFileUnchanged(sidecar, ({ path: target }) => fs.writeFileSync(`${target}-wal`, 'synthetic')),
        /changed during operation/,
      );
    } finally {
      fs.rmSync(directory, { recursive: true, force: true });
    }
  });
});
