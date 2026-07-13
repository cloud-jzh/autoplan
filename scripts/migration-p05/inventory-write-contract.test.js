'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const {
  CONTRACT_VERSION,
  RELATION_POLICIES,
  buildContract,
  parseArgs: parseInventoryArgs,
  stableJson,
} = require('./inventory-write-contract');
const {
  BlockedError,
  NORMALIZATION_VERSION,
  buildGoldenBundle,
  normalizeGolden,
  parseArgs: parseGoldenArgs,
  safeRemoveTemporaryRoot,
} = require('./generate-node-golden');

const ROOT = path.resolve(__dirname, '../..');

test('committed write contract is an exact inventory of current source anchors', () => {
  const generated = buildContract(ROOT);
  const committed = JSON.parse(fs.readFileSync(path.join(ROOT, 'docs/migration/p05/write-contract.json'), 'utf8'));
  assert.equal(generated.version, CONTRACT_VERSION);
  assert.equal(stableJson(committed), stableJson(generated));
  assert.deepEqual(generated.snapshot.topLevelFields, [
    'activeProjectId', 'activeProject', 'projects', 'mcp', 'state', 'requirements', 'feedback',
    'attachments', 'plans', 'tasks', 'events', 'scans', 'scanSummary', 'scripts', 'executors',
    'terminals', 'activeOperation', 'activeOperations', 'lastOperation',
  ]);
  for (const source of generated.evidence) {
    assert.match(source.sha256, /^[a-f0-9]{64}$/);
    assert.ok(source.anchors.length > 0);
    assert.ok(source.anchors.every((anchor) => Number.isInteger(anchor.line) && anchor.line > 0));
  }
});

test('delete policy fails closed for every P04 restrictive project relation', () => {
  const byTable = new Map(RELATION_POLICIES.map((item) => [item.table, item]));
  for (const table of ['attachments', 'intake_plan_links', 'scripts', 'executors', 'operations']) {
    assert.match(byTable.get(table)?.decision || '', /restrict/);
  }
  assert.equal(byTable.get('requirements').decision, 'managed-cascade');
  assert.equal(byTable.get('feedback').decision, 'managed-cascade');
  assert.equal(byTable.get('plans').decision, 'managed-cascade');
  assert.equal(byTable.get('project_states').decision, 'cascade');
  assert.equal(byTable.get('conversations').decision, 'cascade');
  assert.equal(byTable.get('chat_messages').decision, 'cascade');
  assert.equal(byTable.get('settings').decision, 'retain');
});

test('config inventory freezes aliases, version participants and secret return rules', () => {
  const contract = buildContract(ROOT);
  const fields = new Map(contract.configFields.projectState.map((item) => [item.column, item]));
  assert.deepEqual(fields.get('validation_command').inputAliases, ['validationCommand', 'validation_command']);
  assert.ok(fields.get('codex_reasoning_effort').inputAliases.includes('thinking_depth'));
  assert.equal(fields.get('env_vars').returnPolicy, 'never-return-raw; presence/mask only');
  assert.equal(fields.get('plan_execution_claude_auth_token').sensitivity, 'secret');
  assert.match(fields.get('interval_seconds').businessIdempotencyKey, /normalized-value/);
  assert.equal(contract.configFields.settings.find((item) => item.key === 'mcp.authToken').emptyRule, 'explicit empty clears; omitted retains');
  assert.deepEqual(contract.configFields.versionContract.participants, ['settings.version', 'project_states.version']);
  assert.equal(contract.configFields.versionContract.errors.stale, 'VERSION_CONFLICT');
});

test('golden normalization preserves key presence, null, booleans, numbers, order and project references', () => {
  const fixtureRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p05-normalize-'));
  try {
    const source = {
      scenarios: [{
        id: 'sample',
        request: { projectId: 42, workspacePath: path.join(fixtureRoot, 'workspace'), envVars: [{ name: 'A', value: 'private' }] },
        response: {
          ok: true,
          snapshot: {
            activeProjectId: 42,
            activeProject: { id: 42, created_at: '2026-07-11T00:00:00.000Z' },
            projects: [{ id: 42, updated_at: '2026-07-11T00:00:00.000Z' }],
            state: { project_id: 42, env_vars: '[{"name":"A","value":"private"}]', optional: null, running: 0 },
            activeOperations: [],
          },
        },
      }],
    };
    const normalized = normalizeGolden(source, { fixtureRoot });
    const snapshot = normalized.value.scenarios[0].response.snapshot;
    assert.equal(normalized.metadata.version, NORMALIZATION_VERSION);
    assert.equal(snapshot.activeProjectId, 1);
    assert.equal(snapshot.activeProject.id, 1);
    assert.equal(snapshot.state.project_id, 1);
    assert.equal(snapshot.state.env_vars, '<redacted-env-vars>');
    assert.equal(snapshot.state.optional, null);
    assert.equal(snapshot.state.running, 0);
    assert.deepEqual(snapshot.activeOperations, []);
    assert.equal(normalized.value.scenarios[0].request.workspacePath, '<fixture-root>/workspace');
  } finally {
    fs.rmSync(fixtureRoot, { recursive: true, force: true });
  }
});

test('golden builder serializes all frozen scenarios and closes Node before handoff', async () => {
  const bundle = await buildGoldenBundle({ rootDir: ROOT });
  assert.deepEqual(bundle.manifest.scenarios, [
    'create', 'duplicate-create', 'delete-duplicate-create', 'update', 'configure',
    'duplicate-configure', 'missing-update', 'missing-delete', 'running-delete', 'delete', 'duplicate-delete',
  ]);
  assert.equal(bundle.golden.handoff.sqlJsClosed, true);
  assert.equal(bundle.golden.handoff.databaseOwnerReleased, true);
  assert.equal(bundle.manifest.writerHandoff.sameCopyConcurrentWritersAllowed, false);
  assert.match(bundle.manifest.source.databaseBeforeSha256, /^[a-f0-9]{64}$/);
  assert.match(bundle.manifest.source.databaseAfterSha256, /^[a-f0-9]{64}$/);
  assert.ok(!JSON.stringify(bundle.golden).includes('non-working-'));
});

test('CLI and cleanup boundaries expose no force, skip-gate or arbitrary database mode', () => {
  assert.deepEqual(parseInventoryArgs(['--write']), { write: true });
  assert.deepEqual(parseGoldenArgs([]), {});
  assert.throws(() => parseInventoryArgs(['--force']), /unknown argument/);
  assert.throws(() => parseGoldenArgs(['--database', 'autoplan.sqlite']), /unknown argument/);
  assert.throws(
    () => safeRemoveTemporaryRoot(path.resolve(ROOT, 'fixtures')),
    (error) => error instanceof BlockedError && error.reason === 'temporary_cleanup_boundary_failed',
  );
});
