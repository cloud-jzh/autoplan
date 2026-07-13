'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const initSqlJs = require('sql.js');

const {
  GENERATED_MANIFEST,
  canonicalJson,
  generateFixtures,
  loadRecipe,
  sha256,
  validateRecipe,
  withinTemporaryRoot,
} = require('./generate-fixtures');

function temporaryCase(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p04-fixtures-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function readArtifact(root, artifact) {
  return fs.readFileSync(path.join(root, artifact.file));
}

function sqliteUserVersion(bytes) {
  if (bytes.length === 0) return 0;
  assert.equal(bytes.subarray(0, 16).toString('binary'), 'SQLite format 3\u0000');
  return bytes.readUInt32BE(60);
}

test('recipe is canonical, complete, unique, and contains the full fault-stage matrix', () => {
  const { recipe } = loadRecipe();
  validateRecipe(recipe);
  const recipePath = path.resolve(__dirname, '..', '..', 'fixtures', 'migration', 'p04', 'manifest.json');
  assert.equal(fs.readFileSync(recipePath, 'utf8'), canonicalJson(recipe));
  assert.equal(new Set(recipe.fixtures.map((fixture) => fixture.id)).size, recipe.fixtures.length);
  assert.deepEqual(recipe.fault_stages, [
    'before-backup',
    'after-backup',
    'migration-transaction',
    'migration-sql',
    'before-ledger-record',
    'after-ledger-record',
    'wal-checkpoint',
    'post-migration-audit',
    'restore-staging',
    'restore-atomic-replace',
  ]);
  assert.deepEqual(recipe.fault_modes, [
    'cancellation', 'panic', 'process-interruption', 'short-write',
    'enospc', 'permission-denied', 'checksum-damage',
  ]);
  for (const required of [
    'empty-file', 'empty-sqlite', 'initial-single-project', 'ensure-column-intermediate',
    'scan-files-old-primary-key', 'no-intake-plan-links', 'project-ai-configs',
    'chat-without-conversation', 'current-node-valid', 'valid-edge-data', 'schema-v1',
    'orphan-relations', 'invalid-paths', 'foreign-key-conflict',
    'schema-checksum-drift', 'schema-object-drift', 'corrupt-page', 'truncated-file',
  ]) {
    assert.ok(recipe.fixtures.some((fixture) => fixture.id === required), required);
  }
});

test('two independent generations are byte-identical and the generated manifest pins every artifact', async (t) => {
  const root = temporaryCase(t);
  const firstRoot = path.join(root, 'first');
  const secondRoot = path.join(root, 'second');
  const first = await generateFixtures(firstRoot);
  const second = await generateFixtures(secondRoot);

  assert.deepEqual(first, second);
  assert.equal(
    fs.readFileSync(path.join(firstRoot, GENERATED_MANIFEST), 'utf8'),
    fs.readFileSync(path.join(secondRoot, GENERATED_MANIFEST), 'utf8'),
  );
  for (const artifact of first.artifacts) {
    const firstBytes = readArtifact(firstRoot, artifact);
    const secondBytes = readArtifact(secondRoot, artifact);
    assert.deepEqual(firstBytes, secondBytes, artifact.id);
    assert.equal(artifact.byte_size, firstBytes.length, artifact.id);
    assert.equal(artifact.sha256, sha256(firstBytes), artifact.id);
    assert.match(artifact.sha256, /^[a-f0-9]{64}$/);
  }
});

test('generated histories expose the declared version and synthetic edge data', async (t) => {
  const root = temporaryCase(t);
  const output = path.join(root, 'generated');
  const manifest = await generateFixtures(output);
  const SQL = await initSqlJs({
    locateFile(file) {
      return path.resolve(__dirname, '..', '..', 'node_modules', 'sql.js', 'dist', file);
    },
  });

  for (const artifact of manifest.artifacts) {
    const bytes = readArtifact(output, artifact);
    if (artifact.id === 'empty-file') {
      assert.equal(bytes.length, 0);
      continue;
    }
    if (artifact.classification === 'invalid-file') continue;
    assert.equal(sqliteUserVersion(bytes), artifact.source_user_version, artifact.id);
    const db = new SQL.Database(bytes);
    try {
      assert.equal(db.exec('PRAGMA user_version')[0].values[0][0], artifact.source_user_version);
    } finally {
      db.close();
    }
  }

  const edge = manifest.artifacts.find((artifact) => artifact.id === 'valid-edge-data');
  const edgeDB = new SQL.Database(readArtifact(output, edge));
  try {
    const projects = edgeDB.exec('SELECT COUNT(*) FROM projects')[0].values[0][0];
    const boundary = edgeDB.exec('SELECT title, source_path FROM requirements WHERE id = 2147483647')[0].values[0];
    const aggregate = edgeDB.exec([
      'SELECT plans.total_tasks, plans.completed_tasks,',
      "SUM(CASE WHEN plan_tasks.status = 'completed' THEN 1 ELSE 0 END)",
      'FROM plans JOIN plan_tasks ON plan_tasks.plan_id = plans.id',
      'WHERE plans.id = 10 GROUP BY plans.id',
    ].join(' '))[0].values[0];
    assert.equal(projects, 2);
    assert.deepEqual(boundary, ['fixture-unicode-边界-🚀', null]);
    assert.deepEqual(aggregate, [1, 1, 1]);
  } finally {
    edgeDB.close();
  }
});

test('every valid history reaches schema v1 with stable business rows and then is a no-op', async (t) => {
  const root = temporaryCase(t);
  const output = path.join(root, 'generated');
  const manifest = await generateFixtures(output);
  const migrationPath = path.resolve(__dirname, '..', '..', 'backend', 'migrations', '0001_schema_v1.sql');
  const migrationBytes = fs.readFileSync(migrationPath);
  const migrationSQL = migrationBytes.toString('utf8');
  const checksum = sha256(migrationBytes);
  const SQL = await initSqlJs({
    locateFile(file) {
      return path.resolve(__dirname, '..', '..', 'node_modules', 'sql.js', 'dist', file);
    },
  });
  const businessTables = [
    'projects', 'project_states', 'requirements', 'feedback', 'attachments', 'scan_files',
    'plans', 'plan_tasks', 'events', 'scripts', 'executors', 'ai_configs',
    'claude_cli_configs', 'conversations', 'chat_messages', 'intake_plan_links',
  ];

  function tableExists(db, table) {
    const escaped = table.replaceAll("'", "''");
    return db.exec("SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = '" + escaped + "'").length > 0;
  }

  function businessSnapshot(db, tables = businessTables) {
    const result = {};
    for (const table of tables) {
      if (!tableExists(db, table)) continue;
      result[table] = db.exec('SELECT COUNT(*), MIN(rowid), MAX(rowid) FROM "' + table + '"')[0].values[0];
    }
    return result;
  }

  function migrateOnce(db) {
    const version = db.exec('PRAGMA user_version')[0].values[0][0];
    if (version === 1) {
      const ledger = db.exec('SELECT name, checksum FROM schema_migrations WHERE version = 1')[0].values[0];
      assert.deepEqual(ledger, ['schema_v1', checksum]);
      return 'no-op';
    }
    assert.equal(version, 0);
    db.run('BEGIN IMMEDIATE');
    try {
      db.run(migrationSQL);
      db.run(
        'INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)',
        [1, 'schema_v1', checksum, '2026-07-11T04:00:00.000Z'],
      );
      db.run('PRAGMA user_version = 1');
      db.run('COMMIT');
    } catch (error) {
      db.run('ROLLBACK');
      throw error;
    }
    return 'migrated';
  }

  for (const artifact of manifest.artifacts.filter((item) => item.expected_result !== 'blocked')) {
    const sourcePath = path.join(output, artifact.file);
    const sourceBytes = fs.readFileSync(sourcePath);
    const sourceDigest = sha256(sourceBytes);
    const db = sourceBytes.length === 0 ? new SQL.Database() : new SQL.Database(sourceBytes);
    try {
      const before = businessSnapshot(db);
      assert.equal(migrateOnce(db), artifact.expected_result, artifact.id);
      assert.equal(db.exec('PRAGMA user_version')[0].values[0][0], 1, artifact.id);
      assert.deepEqual(businessSnapshot(db, Object.keys(before)), before, artifact.id);
      assert.equal(migrateOnce(db), 'no-op', artifact.id);
    } finally {
      db.close();
    }
    assert.equal(sha256(fs.readFileSync(sourcePath)), sourceDigest, artifact.id);
  }
});

test('invalid fixtures are separate inputs and damaged files cannot be treated as healthy SQLite', async (t) => {
  const root = temporaryCase(t);
  const output = path.join(root, 'generated');
  const manifest = await generateFixtures(output);
  const SQL = await initSqlJs({
    locateFile(file) {
      return path.resolve(__dirname, '..', '..', 'node_modules', 'sql.js', 'dist', file);
    },
  });
  const byID = new Map(manifest.artifacts.map((artifact) => [artifact.id, artifact]));
  const blockedHashes = new Map(
    manifest.artifacts
      .filter((artifact) => artifact.expected_result === 'blocked')
      .map((artifact) => [artifact.id, sha256(readArtifact(output, artifact))]),
  );

  const orphanDB = new SQL.Database(readArtifact(output, byID.get('orphan-relations')));
  try {
    assert.equal(
      orphanDB.exec('SELECT COUNT(*) FROM plan_tasks WHERE plan_id NOT IN (SELECT id FROM plans)')[0].values[0][0],
      1,
    );
  } finally {
    orphanDB.close();
  }

  const pathDB = new SQL.Database(readArtifact(output, byID.get('invalid-paths')));
  try {
    assert.equal(pathDB.exec("SELECT workspace_path FROM projects WHERE id = 1")[0].values[0][0], '../fixture-escape');
  } finally {
    pathDB.close();
  }

  for (const id of ['corrupt-page', 'truncated-file']) {
    const bytes = readArtifact(output, byID.get(id));
    let healthy = false;
    try {
      const db = new SQL.Database(bytes);
      const rows = db.exec('PRAGMA integrity_check');
      healthy = rows.length === 1 && rows[0].values.length === 1 && rows[0].values[0][0] === 'ok';
      db.close();
    } catch {
      healthy = false;
    }
    assert.equal(healthy, false, id);
  }
  for (const [id, digest] of blockedHashes) {
    assert.equal(sha256(readArtifact(output, byID.get(id))), digest, id);
  }
});

test('generation is temp-root confined, non-overwriting, and does not disclose its absolute output path', async (t) => {
  const root = temporaryCase(t);
  const output = path.join(root, 'generated');
  assert.equal(withinTemporaryRoot(output), true);
  assert.equal(withinTemporaryRoot(path.resolve(__dirname, 'generated')), false);

  const first = await generateFixtures(output);
  const before = new Map(first.artifacts.map((artifact) => [
    artifact.file,
    sha256(fs.readFileSync(path.join(output, artifact.file))),
  ]));
  await assert.rejects(generateFixtures(output), /fixture_output_not_empty/);
  for (const [file, digest] of before) {
    assert.equal(sha256(fs.readFileSync(path.join(output, file))), digest);
  }

  const generatedText = fs.readFileSync(path.join(output, GENERATED_MANIFEST), 'utf8');
  assert.equal(generatedText.includes(output), false);
  assert.equal(generatedText.includes(os.homedir()), false);
  assert.equal(generatedText.includes('database_content'), true);
});
