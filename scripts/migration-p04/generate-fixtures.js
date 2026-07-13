'use strict';

const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const initSqlJs = require('sql.js');

const REPOSITORY_ROOT = path.resolve(__dirname, '..', '..');
const RECIPE_PATH = path.join(REPOSITORY_ROOT, 'fixtures', 'migration', 'p04', 'manifest.json');
const NODE_DATABASE_PATH = path.join(REPOSITORY_ROOT, 'src', 'database.js');
const MIGRATION_PATH = path.join(REPOSITORY_ROOT, 'backend', 'migrations', '0001_schema_v1.sql');
const GENERATED_MANIFEST = 'generated-manifest.json';
const FIXED_TIME = '2026-07-11T04:00:00.000Z';
const SQLITE_HEADER = Buffer.from('SQLite format 3\u0000', 'binary');

function sha256(content) {
  return crypto.createHash('sha256').update(content).digest('hex');
}

function canonicalJson(value) {
  return JSON.stringify(value, null, 2) + '\n';
}

function loadRecipe() {
  const bytes = fs.readFileSync(RECIPE_PATH);
  const recipe = JSON.parse(bytes.toString('utf8'));
  validateRecipe(recipe);
  return { recipe, bytes };
}

function validateRecipe(recipe) {
  if (!recipe || recipe.format_version !== 1 ||
      recipe.fixture_set !== 'autoplan-p04-copy-migration' ||
      recipe.generated_by !== 'scripts/migration-p04/generate-fixtures.js' ||
      recipe.artifact_manifest !== GENERATED_MANIFEST ||
      recipe.fixed_time !== FIXED_TIME ||
      !Array.isArray(recipe.fixtures) || recipe.fixtures.length < 18 ||
      !Array.isArray(recipe.fault_stages) || recipe.fault_stages.length < 10 ||
      !Array.isArray(recipe.fault_modes) || recipe.fault_modes.length < 7) {
    throw new Error('fixture_recipe_invalid');
  }
  const ids = new Set();
  const files = new Set();
  for (const fixture of recipe.fixtures) {
    if (!fixture || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(fixture.id) ||
        !/^[a-z0-9-]+\.sqlite\.copy$/.test(fixture.file) ||
        fixture.id !== fixture.kind || ids.has(fixture.id) || files.has(fixture.file) ||
        !['valid-history', 'valid-current', 'invalid-data', 'invalid-schema', 'invalid-file'].includes(fixture.classification) ||
        !['migrated', 'no-op', 'blocked'].includes(fixture.expected_result)) {
      throw new Error('fixture_recipe_invalid');
    }
    if (fixture.expected_result === 'blocked' && !fixture.expected_code) {
      throw new Error('fixture_recipe_invalid');
    }
    ids.add(fixture.id);
    files.add(fixture.file);
  }
  if (new Set(recipe.fault_stages).size !== recipe.fault_stages.length ||
      new Set(recipe.fault_modes).size !== recipe.fault_modes.length) {
    throw new Error('fixture_recipe_invalid');
  }
}

function withinTemporaryRoot(candidate) {
  const root = fs.realpathSync.native(path.resolve(os.tmpdir()));
  const resolved = path.resolve(candidate);
  let existing = resolved;
  while (!fs.existsSync(existing)) {
    const parent = path.dirname(existing);
    if (parent === existing) return false;
    existing = parent;
  }
  const realExisting = fs.realpathSync.native(existing);
  const suffix = path.relative(existing, resolved);
  const realCandidate = path.resolve(realExisting, suffix);
  const relative = path.relative(root, realCandidate);
  return relative !== '' && relative !== '..' && !relative.startsWith('..' + path.sep) && !path.isAbsolute(relative);
}

function prepareOutputDirectory(outputDirectory) {
  if (!path.isAbsolute(outputDirectory) || !withinTemporaryRoot(outputDirectory)) {
    throw new Error('fixture_output_unsafe');
  }
  if (fs.existsSync(outputDirectory)) {
    const info = fs.lstatSync(outputDirectory);
    if (!info.isDirectory() || info.isSymbolicLink() || fs.readdirSync(outputDirectory).length !== 0) {
      throw new Error('fixture_output_not_empty');
    }
  } else {
    fs.mkdirSync(outputDirectory, { recursive: true, mode: 0o700 });
  }
}

function extractRunTemplate(source, marker) {
  const markerIndex = source.indexOf(marker);
  const tick = String.fromCharCode(96);
  const runStart = source.indexOf('this.db.run(' + tick, markerIndex);
  const bodyStart = runStart + 'this.db.run('.length + 1;
  const bodyEnd = source.indexOf(tick + ');', bodyStart);
  if (markerIndex < 0 || runStart < 0 || bodyEnd < 0) {
    throw new Error('fixture_source_schema_missing');
  }
  return source.slice(bodyStart, bodyEnd);
}

function extractEnsureColumns(source) {
  const migrateStart = source.indexOf('  migrate() {');
  const migrateEnd = source.indexOf('    const defaultProjectId', migrateStart);
  if (migrateStart < 0 || migrateEnd < 0) {
    throw new Error('fixture_source_schema_missing');
  }
  const block = source.slice(migrateStart, migrateEnd);
  const expression = /this\.ensureColumn\(\s*'([^']+)'\s*,\s*'([^']+)'\s*,\s*(?:'([^']*)'|"([^"]*)")\s*\)/g;
  const columns = [];
  for (let match = expression.exec(block); match; match = expression.exec(block)) {
    columns.push({ table: match[1], column: match[2], definition: match[3] === undefined ? match[4] : match[3] });
  }
  if (columns.length < 80) {
    throw new Error('fixture_source_schema_missing');
  }
  return columns;
}

function hasTable(db, table) {
  const escaped = table.replaceAll("'", "''");
  const result = db.exec("SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = '" + escaped + "'");
  return result.length > 0 && result[0].values.length > 0;
}

function hasColumn(db, table, column) {
  if (!hasTable(db, table)) return false;
  const result = db.exec('PRAGMA table_info("' + table.replaceAll('"', '""') + '")');
  return result.length > 0 && result[0].values.some((row) => row[1] === column);
}

function applyEnsureColumns(db, columns, limit = columns.length) {
  for (const item of columns.slice(0, limit)) {
    if (!hasColumn(db, item.table, item.column)) {
      db.run('ALTER TABLE "' + item.table + '" ADD COLUMN "' + item.column + '" ' + item.definition);
    }
  }
}

function normalizeClockSQL(sql) {
  return sql
    .replaceAll("datetime('now')", "'" + FIXED_TIME + "'")
    .replaceAll("strftime('%Y-%m-%dT%H:%M:%fZ', 'now')", "'" + FIXED_TIME + "'");
}

function createLegacySchema(db, source, columnLimit = 0, includeIntake = false) {
  db.run(normalizeClockSQL(extractRunTemplate(source, '  migrate() {')));
  const columns = extractEnsureColumns(source);
  applyEnsureColumns(db, columns, columnLimit);
  if (includeIntake) {
    db.run(extractRunTemplate(source, '  ensureIntakePlanLinksTable() {'));
  }
  db.run("UPDATE loop_state SET updated_at = ? WHERE id = 1", [FIXED_TIME]);
}

function createCurrentNodeSchema(db, source) {
  createLegacySchema(db, source, extractEnsureColumns(source).length, true);
  db.run('CREATE INDEX IF NOT EXISTS idx_plans_project_sort ON plans (project_id, sort_order, created_at, id)');
  db.run('CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation ON chat_messages (conversation_id, created_at)');
  db.run('CREATE INDEX IF NOT EXISTS idx_conversations_project_pinned_updated ON conversations (project_id, pinned_at, updated_at, id)');
}

function createSchemaV1(db, migrationSQL, migrationChecksum) {
  db.run('PRAGMA foreign_keys = OFF');
  db.run(normalizeClockSQL(migrationSQL));
  db.run(
    'INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)',
    [1, 'schema_v1', migrationChecksum, FIXED_TIME],
  );
  db.run('PRAGMA user_version = 1');
}

function insertIfColumns(db, table, values) {
  if (!hasTable(db, table)) return;
  const entries = Object.entries(values).filter(([column]) => hasColumn(db, table, column));
  if (entries.length === 0) return;
  const columns = entries.map(([column]) => '"' + column + '"').join(', ');
  const placeholders = entries.map(() => '?').join(', ');
  db.run(
    'INSERT INTO "' + table + '" (' + columns + ') VALUES (' + placeholders + ')',
    entries.map(([, value]) => value),
  );
}

function seedProjects(db, options = {}) {
  insertIfColumns(db, 'projects', {
    id: 1, name: 'fixture-project-alpha', workspace_path: 'fixture/workspace-alpha',
    description: '', created_at: FIXED_TIME, updated_at: FIXED_TIME,
  });
  if (options.multiProject) {
    insertIfColumns(db, 'projects', {
      id: 2, name: 'fixture-project-unicode-项目', workspace_path: 'fixture/workspace-beta',
      description: '', created_at: FIXED_TIME, updated_at: FIXED_TIME,
    });
  }
  insertIfColumns(db, 'project_states', { project_id: 1, updated_at: FIXED_TIME });
  if (options.multiProject) {
    insertIfColumns(db, 'project_states', { project_id: 2, updated_at: FIXED_TIME });
  }
}

function seedCore(db, options = {}) {
  seedProjects(db, options);
  insertIfColumns(db, 'requirements', {
    id: 100, project_id: 1, title: 'fixture-requirement', body: '',
    status: 'open', source_path: null, source_hash: null,
    created_at: FIXED_TIME, updated_at: FIXED_TIME, linked_plan_id: 10,
  });
  insertIfColumns(db, 'plans', {
    id: 10, project_id: 1, issue_hash: 'fixture-issue-a', file_path: 'fixture/plans/plan-a.md',
    hash: 'fixture-plan-hash-a', status: 'completed', sort_order: 1,
    total_tasks: 1, completed_tasks: 1, validation_passed: 1,
    created_at: FIXED_TIME, updated_at: FIXED_TIME,
  });
  insertIfColumns(db, 'plan_tasks', {
    id: 1000, plan_id: 10, task_key: 'P001', title: 'fixture-task',
    raw_line: '- [x] P001: fixture-task', scope: '', status: 'completed',
    sort_order: 1, duration_ms: 0, updated_at: FIXED_TIME,
  });
  insertIfColumns(db, 'feedback', {
    id: 200, project_id: 1, requirement_id: 100, title: 'fixture-feedback',
    body: '', status: 'open', created_at: FIXED_TIME, updated_at: FIXED_TIME,
    linked_plan_id: 10,
  });
  insertIfColumns(db, 'attachments', {
    id: 250, project_id: 1, owner_type: 'requirement', owner_id: 100,
    original_name: 'fixture.txt', stored_path: 'fixture/attachments/fixture.txt',
    mime_type: null, size: 0, hash: 'fixture-empty-hash', created_at: FIXED_TIME,
  });
  insertIfColumns(db, 'scan_files', {
    project_id: 1, scan_type: 'workspace', file_path: 'fixture/workspace-alpha/file.txt',
    hash: 'fixture-scan-hash', size: 0, modified_at: FIXED_TIME, scanned_at: FIXED_TIME,
  });
  insertIfColumns(db, 'ai_configs', {
    id: 300, project_id: null, name: 'fixture-config', provider: 'openai',
    base_url: '', api_key: '', model: 'fixture-model', temperature: '0.3',
    thinking_depth: null, thinking_budget_tokens: null,
    created_at: FIXED_TIME, updated_at: FIXED_TIME,
  });
  insertIfColumns(db, 'conversations', {
    id: 400, project_id: 1, title: 'fixture-conversation', ai_config_id: 300,
    pinned_at: null, codex_session_id: null, created_at: FIXED_TIME, updated_at: FIXED_TIME,
  });
  insertIfColumns(db, 'chat_messages', {
    id: 500, project_id: 1, conversation_id: 400, role: 'user',
    content: 'fixture-message', tool_calls: null, tool_result: null,
    status: 'completed', created_at: FIXED_TIME,
  });
  insertIfColumns(db, 'intake_plan_links', {
    id: 600, project_id: 1, intake_type: 'requirement', intake_id: 100,
    plan_id: 10, phase_index: 1, phase_title: '', created_at: FIXED_TIME, updated_at: FIXED_TIME,
  });
}

function rebuildOldScanFiles(db) {
  db.run('ALTER TABLE scan_files RENAME TO scan_files_current');
  db.run([
    'CREATE TABLE scan_files (',
    'scan_type TEXT NOT NULL,',
    'file_path TEXT NOT NULL,',
    'hash TEXT NOT NULL,',
    'size INTEGER NOT NULL,',
    'modified_at TEXT NOT NULL,',
    'scanned_at TEXT NOT NULL,',
    'PRIMARY KEY (scan_type, file_path)',
    ')',
  ].join(' '));
  db.run('DROP TABLE scan_files_current');
}

function rebuildChatWithoutConversation(db) {
  db.run('DROP INDEX IF EXISTS idx_chat_messages_conversation');
  db.run('ALTER TABLE chat_messages RENAME TO chat_messages_current');
  db.run([
    'CREATE TABLE chat_messages (',
    'id INTEGER PRIMARY KEY AUTOINCREMENT,',
    'project_id INTEGER, role TEXT NOT NULL, content TEXT NOT NULL,',
    'tool_calls TEXT, tool_result TEXT, status TEXT, created_at TEXT NOT NULL',
    ')',
  ].join(' '));
  db.run([
    'INSERT INTO chat_messages (id, project_id, role, content, tool_calls, tool_result, status, created_at)',
    'SELECT id, project_id, role, content, tool_calls, tool_result, status, created_at',
    'FROM chat_messages_current',
  ].join(' '));
  db.run('DROP TABLE chat_messages_current');
  db.run('CREATE INDEX idx_chat_messages_project ON chat_messages (project_id, created_at)');
}

function createDatabaseFixture(SQL, fixture, nodeSource, migrationSQL, migrationChecksum) {
  if (fixture.kind === 'empty-file') return Buffer.alloc(0);
  const db = new SQL.Database();
  try {
    switch (fixture.kind) {
      case 'empty-sqlite':
        db.run('VACUUM');
        break;
      case 'initial-single-project':
        createLegacySchema(db, nodeSource);
        seedProjects(db);
        insertIfColumns(db, 'requirements', {
          id: 100, project_id: null, title: 'fixture-legacy-requirement',
          body: '', status: 'open', created_at: FIXED_TIME, updated_at: FIXED_TIME,
        });
        break;
      case 'ensure-column-intermediate': {
        const columns = extractEnsureColumns(nodeSource);
        createLegacySchema(db, nodeSource, Math.ceil(columns.length / 2), false);
        seedProjects(db);
        break;
      }
      case 'scan-files-old-primary-key':
        createCurrentNodeSchema(db, nodeSource);
        rebuildOldScanFiles(db);
        seedCore(db);
        break;
      case 'no-intake-plan-links':
        createCurrentNodeSchema(db, nodeSource);
        db.run('DROP TABLE intake_plan_links');
        seedCore(db);
        break;
      case 'project-ai-configs':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db, { multiProject: true });
        insertIfColumns(db, 'ai_configs', {
          id: 301, project_id: 2, name: 'fixture-project-config', provider: 'openai',
          base_url: '', api_key: '', model: 'fixture-model', temperature: '0.3',
          created_at: FIXED_TIME, updated_at: FIXED_TIME,
        });
        break;
      case 'chat-without-conversation':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db);
        rebuildChatWithoutConversation(db);
        break;
      case 'current-node-valid':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db);
        break;
      case 'valid-edge-data':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db, { multiProject: true });
        insertIfColumns(db, 'requirements', {
          id: 2147483647, project_id: 2, title: 'fixture-unicode-边界-🚀',
          body: '', status: 'open', source_path: null, source_hash: null,
          created_at: FIXED_TIME, updated_at: FIXED_TIME, linked_plan_id: null,
        });
        insertIfColumns(db, 'plans', {
          id: 20, project_id: 2, issue_hash: 'fixture-issue-b',
          file_path: 'fixture/plans/plan-b.md', hash: 'fixture-plan-hash-b',
          status: 'pending', sort_order: 1, total_tasks: 1, completed_tasks: 0,
          validation_passed: 0, created_at: FIXED_TIME, updated_at: FIXED_TIME,
        });
        insertIfColumns(db, 'plan_tasks', {
          id: 2000, plan_id: 20, task_key: 'P001', title: 'fixture-pending-task',
          raw_line: '- [ ] P001: fixture-pending-task', status: 'pending',
          sort_order: 1, updated_at: FIXED_TIME,
        });
        break;
      case 'schema-v1':
        createSchemaV1(db, migrationSQL, migrationChecksum);
        seedCore(db, { multiProject: true });
        break;
      case 'orphan-relations':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db);
        insertIfColumns(db, 'plan_tasks', {
          id: 9000, plan_id: 9999, task_key: 'P999', title: 'fixture-orphan',
          raw_line: '- [ ] P999: fixture-orphan', status: 'pending',
          sort_order: 999, updated_at: FIXED_TIME,
        });
        break;
      case 'invalid-paths':
        createCurrentNodeSchema(db, nodeSource);
        seedCore(db);
        db.run("UPDATE projects SET workspace_path = '../fixture-escape' WHERE id = 1");
        db.run("UPDATE plans SET file_path = '//fixture-host/share/plan.md' WHERE id = 10");
        break;
      case 'foreign-key-conflict':
        createSchemaV1(db, migrationSQL, migrationChecksum);
        seedCore(db);
        db.run('PRAGMA foreign_keys = OFF');
        insertIfColumns(db, 'plan_tasks', {
          id: 9001, plan_id: 9999, task_key: 'P998', title: 'fixture-fk-conflict',
          raw_line: '- [ ] P998: fixture-fk-conflict', status: 'pending',
          sort_order: 998, updated_at: FIXED_TIME,
        });
        break;
      case 'schema-checksum-drift':
        createSchemaV1(db, migrationSQL, migrationChecksum);
        db.run("UPDATE schema_migrations SET checksum = ? WHERE version = 1", ['0'.repeat(64)]);
        break;
      case 'schema-object-drift':
        createSchemaV1(db, migrationSQL, migrationChecksum);
        db.run('CREATE TABLE fixture_unexpected_schema_object (id INTEGER PRIMARY KEY)');
        break;
      case 'corrupt-page':
      case 'truncated-file':
        createSchemaV1(db, migrationSQL, migrationChecksum);
        seedCore(db);
        break;
      default:
        throw new Error('fixture_kind_unsupported');
    }
    db.run('PRAGMA journal_mode = DELETE');
    const exported = Buffer.from(db.export());
    if (fixture.kind === 'corrupt-page') {
      const corrupted = Buffer.from(exported);
      const pageSize = corrupted.readUInt16BE(16) || 65536;
      const start = Math.min(pageSize + 100, corrupted.length - 256);
      corrupted.fill(0xa5, Math.max(100, start), Math.max(228, start + 128));
      return corrupted;
    }
    if (fixture.kind === 'truncated-file') {
      return exported.subarray(0, Math.min(128, exported.length));
    }
    return exported;
  } finally {
    db.close();
  }
}

function assertSanitized(bytes, fixture) {
  if (fixture.kind === 'corrupt-page' || fixture.kind === 'truncated-file') return;
  const text = bytes.toString('utf8');
  const forbidden = [
    /Users[\\/][^\\/]+/i,
    /AppData[\\/]Roaming/i,
    /BEGIN (?:RSA|OPENSSH|EC) PRIVATE KEY/i,
    /(?:sk|ghp|xox[baprs])[-_][A-Za-z0-9]{12,}/,
  ];
  if (forbidden.some((pattern) => pattern.test(text))) {
    throw new Error('fixture_content_not_sanitized');
  }
}

async function generateFixtures(outputDirectory) {
  prepareOutputDirectory(outputDirectory);
  const { recipe, bytes: recipeBytes } = loadRecipe();
  const nodeSource = fs.readFileSync(NODE_DATABASE_PATH, 'utf8');
  const migrationBytes = fs.readFileSync(MIGRATION_PATH);
  const migrationSQL = migrationBytes.toString('utf8');
  const migrationChecksum = sha256(migrationBytes);
  const SQL = await initSqlJs({
    locateFile(file) {
      return path.join(REPOSITORY_ROOT, 'node_modules', 'sql.js', 'dist', file);
    },
  });
  const artifacts = [];
  for (const fixture of recipe.fixtures) {
    const content = createDatabaseFixture(SQL, fixture, nodeSource, migrationSQL, migrationChecksum);
    assertSanitized(content, fixture);
    const destination = path.join(outputDirectory, fixture.file);
    fs.writeFileSync(destination, content, { flag: 'wx', mode: 0o600 });
    artifacts.push({
      id: fixture.id,
      file: fixture.file,
      classification: fixture.classification,
      source_user_version: fixture.source_user_version,
      expected_target_user_version: fixture.expected_target_user_version,
      expected_result: fixture.expected_result,
      expected_code: fixture.expected_code || null,
      byte_size: content.length,
      sha256: sha256(content),
    });
  }
  const generated = {
    format_version: 1,
    fixture_set: recipe.fixture_set,
    generated_by: recipe.generated_by,
    recipe_sha256: sha256(recipeBytes),
    node_schema_source_sha256: sha256(Buffer.from(nodeSource, 'utf8')),
    migration_source_sha256: migrationChecksum,
    fixed_time: recipe.fixed_time,
    database_content_in_manifest: false,
    fault_stages: [...recipe.fault_stages],
    fault_modes: [...recipe.fault_modes],
    artifacts,
  };
  fs.writeFileSync(path.join(outputDirectory, GENERATED_MANIFEST), canonicalJson(generated), {
    flag: 'wx',
    mode: 0o600,
  });
  return generated;
}

function parseCommandLine(argv) {
  if (argv.length !== 2 || argv[0] !== '--output' || !path.isAbsolute(argv[1])) {
    throw new Error('usage_generate_fixtures_output_absolute');
  }
  return argv[1];
}

if (require.main === module) {
  generateFixtures(parseCommandLine(process.argv.slice(2)))
    .then((manifest) => {
      process.stdout.write(canonicalJson({
        fixture_count: manifest.artifacts.length,
        manifest: GENERATED_MANIFEST,
      }));
    })
    .catch((error) => {
      const message = String(error && error.message ? error.message : 'fixture_generation_failed');
      const code = /^[a-z0-9_]+$/.test(message) ? message : 'fixture_generation_failed';
      process.stderr.write(code + '\n');
      process.exitCode = 1;
    });
}

module.exports = {
  FIXED_TIME,
  GENERATED_MANIFEST,
  SQLITE_HEADER,
  canonicalJson,
  generateFixtures,
  loadRecipe,
  sha256,
  validateRecipe,
  withinTemporaryRoot,
};
