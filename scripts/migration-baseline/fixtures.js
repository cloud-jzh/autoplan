'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const DEFAULT_SEED = 20260711;
const DEFAULT_LARGE_COUNT = 250;
const FIXED_TIME = '2026-01-02T03:04:05.000Z';
const MANIFEST_PATH = 'fixtures/migration/p00/manifest.json';
const DATABASE_SOURCE_PATH = 'src/database.js';
const FIXTURE_NAMES = Object.freeze(['empty', 'legacy-normal', 'orphan-cross-project', 'invalid-paths', 'large']);

function isInside(root, target) {
  const relative = path.relative(path.resolve(root), path.resolve(target));
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function knownUserDataRoots() {
  const roots = [];
  for (const base of [process.env.APPDATA, process.env.LOCALAPPDATA]) {
    if (base) roots.push(path.join(base, 'autoplan'));
  }
  if (process.platform === 'darwin') roots.push(path.join(os.homedir(), 'Library', 'Application Support', 'autoplan'));
  else roots.push(path.join(os.homedir(), '.config', 'autoplan'));
  return roots.map((item) => path.resolve(item));
}

function looksLikeUserData(target) {
  const normalized = path.resolve(target).replace(/\\/g, '/').toLowerCase();
  return knownUserDataRoots().some((root) => isInside(root, target) || isInside(target, root))
    || /\/appdata\/(?:roaming|local)\/autoplan(?:\/|$)/i.test(normalized)
    || /\/library\/application support\/autoplan(?:\/|$)/i.test(normalized)
    || /\/\.config\/autoplan(?:\/|$)/i.test(normalized);
}

function assertNoSymlinkAncestors(target) {
  let current = path.resolve(target);
  const existing = [];
  while (!fs.existsSync(current)) {
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }
  while (true) {
    existing.push(current);
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }
  for (const item of existing) {
    if (fs.lstatSync(item).isSymbolicLink()) throw new Error(`拒绝符号链接路径祖先：${item}`);
  }
}

function resolveOutput(options = {}) {
  const seed = positiveInteger(options.seed, DEFAULT_SEED, 'seed');
  const largeCount = positiveInteger(options.largeCount, DEFAULT_LARGE_COUNT, 'largeCount');
  const tempRoot = fs.realpathSync(os.tmpdir());
  if (options.allowRoot && !path.isAbsolute(options.allowRoot)) throw new Error('--allow-root 必须是绝对目录');
  if (options.outputDir && !path.isAbsolute(options.outputDir)) throw new Error('--output 必须是绝对路径');
  const allowRoot = options.allowRoot ? path.resolve(options.allowRoot) : null;
  if (allowRoot) {
    if (!path.isAbsolute(allowRoot) || !fs.existsSync(allowRoot) || !fs.statSync(allowRoot).isDirectory()) {
      throw new Error('--allow-root 必须是已存在的绝对目录');
    }
    if (looksLikeUserData(allowRoot)) throw new Error('拒绝将 Electron userData 设为授权根');
    assertNoSymlinkAncestors(allowRoot);
  }

  const defaultName = `autoplan-p00-fixtures-seed-${seed}-n-${largeCount}`;
  const target = options.outputDir ? path.resolve(options.outputDir) : path.join(tempRoot, defaultName);
  if (looksLikeUserData(target)) throw new Error('拒绝读写 Electron userData');
  if (!isInside(tempRoot, target) && !(allowRoot && isInside(allowRoot, target))) {
    throw new Error('输出目录必须位于系统临时目录，或位于 --allow-root 显式授权根内');
  }
  if (path.parse(target).root === target) throw new Error('拒绝使用文件系统根目录');
  assertNoSymlinkAncestors(target);
  if (fs.existsSync(target)) throw new Error(`拒绝覆盖已存在目标：${target}`);
  const parent = path.dirname(target);
  if (!fs.existsSync(parent)) throw new Error('输出目录的父目录必须已存在，生成器不会递归创建未授权路径');
  if (!fs.statSync(parent).isDirectory()) throw new Error('输出目录父路径不是目录');
  return { seed, largeCount, target, parent, tempRoot, allowRoot };
}

function positiveInteger(value, fallback, label) {
  const number = value === undefined ? fallback : Number(value);
  if (!Number.isSafeInteger(number) || number <= 0) throw new Error(`${label} 必须是正整数`);
  return number;
}

function acquireLock(parent, target) {
  const suffix = crypto.createHash('sha256').update(path.resolve(target)).digest('hex').slice(0, 16);
  const lockPath = path.join(parent, `.autoplan-p00-fixtures-${suffix}.lock`);
  const handle = fs.openSync(lockPath, 'wx', 0o600);
  fs.writeFileSync(handle, `${process.pid}\n`, 'utf8');
  return { handle, lockPath };
}

function releaseLock(lock) {
  if (!lock) return;
  try { fs.closeSync(lock.handle); } catch {}
  try { fs.unlinkSync(lock.lockPath); } catch {}
}

function safeRemoveStaging(staging, parent) {
  if (!staging || !isInside(parent, staging) || path.dirname(staging) !== path.resolve(parent)) return;
  if (!path.basename(staging).startsWith('.autoplan-p00-staging-')) return;
  try { fs.rmSync(staging, { recursive: true, force: true }); } catch {}
}

async function loadSqlJs(rootDir) {
  const initSqlJs = require('sql.js');
  const wasmPath = require.resolve('sql.js/dist/sql-wasm.wasm', { paths: [rootDir] });
  return initSqlJs({ locateFile: () => wasmPath });
}

function extractInitialDdl(databaseSource) {
  const migrateIndex = databaseSource.indexOf('migrate() {');
  const runIndex = databaseSource.indexOf('this.db.run(`', migrateIndex);
  if (migrateIndex < 0 || runIndex < 0) throw new Error('无法提取 database.js 初始 DDL');
  const start = runIndex + 'this.db.run(`'.length;
  const end = databaseSource.indexOf('`);', start);
  if (end < 0) throw new Error('database.js 初始 DDL 未闭合');
  const ddl = databaseSource.slice(start, end);
  if (ddl.includes('${')) throw new Error('初始 DDL 含未解析模板表达式');
  return ddl;
}

function extractEnsureColumns(databaseSource) {
  const entries = [];
  const pattern = /this\.ensureColumn\(\s*'([^']+)'\s*,\s*'([^']+)'\s*,\s*(?:'([^']*)'|"([^"]*)")\s*\)/g;
  for (const match of databaseSource.matchAll(pattern)) {
    entries.push({ table: match[1], column: match[2], definition: match[3] ?? match[4] });
  }
  return entries;
}

function applyCurrentSchema(db, databaseSource) {
  db.run(extractInitialDdl(databaseSource));
  db.run(`
    CREATE TABLE IF NOT EXISTS intake_plan_links (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      project_id INTEGER NOT NULL,
      intake_type TEXT NOT NULL CHECK (intake_type IN ('requirement', 'feedback')),
      intake_id INTEGER NOT NULL,
      plan_id INTEGER NOT NULL,
      phase_index INTEGER NOT NULL DEFAULT 1,
      phase_title TEXT NOT NULL DEFAULT '',
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL,
      UNIQUE(project_id, intake_type, intake_id, plan_id)
    );
  `);
  for (const entry of extractEnsureColumns(databaseSource)) {
    const columns = queryRows(db, `PRAGMA table_info(${entry.table})`).map((item) => item.name);
    if (!columns.includes(entry.column)) db.run(`ALTER TABLE ${entry.table} ADD COLUMN ${entry.column} ${entry.definition}`);
  }
  for (const match of databaseSource.matchAll(/CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS[\s\S]*?;/gi)) {
    const sql = match[0];
    if (!sql.includes('${')) db.run(sql);
  }
  db.run('UPDATE loop_state SET updated_at = ? WHERE id = 1', [FIXED_TIME]);
  db.run("INSERT OR REPLACE INTO settings (key, value) VALUES ('mcp.authToken', 'fixture-not-a-token')");
}

function queryRows(db, sql, params = []) {
  const statement = db.prepare(sql);
  try {
    statement.bind(params);
    const rows = [];
    while (statement.step()) rows.push(statement.getAsObject());
    return rows;
  } finally {
    statement.free();
  }
}

function makeDatabase(SQL, setup) {
  const db = new SQL.Database();
  try {
    setup(db);
    return Buffer.from(db.export());
  } finally {
    db.close();
  }
}

function insertProject(db, id, name, workspace) {
  db.run(
    'INSERT INTO projects (id, name, workspace_path, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)',
    [id, name, workspace, 'synthetic fixture project', FIXED_TIME, FIXED_TIME],
  );
  db.run(
    `INSERT INTO project_states
      (project_id, running, phase, interval_seconds, validation_command, project_prompt, agent_cli_provider,
       agent_cli_command, plan_generation_strategy, plan_generation_command, plan_generation_model,
       plan_generation_claude_base_url, plan_generation_claude_auth_token, plan_generation_claude_model,
       plan_generation_claude_config_id, plan_execution_strategy, plan_execution_command, plan_execution_model,
       plan_execution_claude_base_url, plan_execution_claude_auth_token, plan_execution_claude_model,
       plan_execution_claude_config_id, env_vars, updated_at)
     VALUES (?, 0, 'idle', 5, '', '', 'codex', '', 'external-cli-markdown', '', '', '', '', '', 0,
             'external-cli', '', '', '', '', '', 0, '', ?)`,
    [id, FIXED_TIME],
  );
}

function createEmptyFixture(SQL, databaseSource) {
  return makeDatabase(SQL, (db) => applyCurrentSchema(db, databaseSource));
}

function createLegacyFixture(SQL) {
  return makeDatabase(SQL, (db) => {
    db.run(`
      CREATE TABLE loop_state (
        id INTEGER PRIMARY KEY CHECK (id = 1), running INTEGER NOT NULL DEFAULT 0,
        phase TEXT NOT NULL DEFAULT 'idle', workspace_path TEXT NOT NULL DEFAULT '',
        interval_seconds INTEGER NOT NULL DEFAULT 5, validation_command TEXT NOT NULL DEFAULT '',
        last_issue_hash TEXT, last_error TEXT, updated_at TEXT NOT NULL
      );
      CREATE TABLE requirements (
        id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, body TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'open', source_path TEXT, source_hash TEXT, linked_plan_id INTEGER,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL
      );
      CREATE TABLE feedback (
        id INTEGER PRIMARY KEY AUTOINCREMENT, requirement_id INTEGER, title TEXT NOT NULL,
        body TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open', linked_plan_id INTEGER,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL
      );
      CREATE TABLE attachments (
        id INTEGER PRIMARY KEY AUTOINCREMENT, owner_type TEXT NOT NULL, owner_id INTEGER NOT NULL,
        original_name TEXT NOT NULL, stored_path TEXT NOT NULL, mime_type TEXT, size INTEGER NOT NULL,
        hash TEXT NOT NULL, created_at TEXT NOT NULL
      );
      CREATE TABLE plans (
        id INTEGER PRIMARY KEY AUTOINCREMENT, issue_hash TEXT NOT NULL, file_path TEXT NOT NULL,
        hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', total_tasks INTEGER NOT NULL DEFAULT 0,
        completed_tasks INTEGER NOT NULL DEFAULT 0, validation_passed INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL
      );
      CREATE TABLE plan_tasks (
        id INTEGER PRIMARY KEY AUTOINCREMENT, plan_id INTEGER NOT NULL, task_key TEXT NOT NULL,
        title TEXT NOT NULL, raw_line TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
        sort_order INTEGER NOT NULL, updated_at TEXT NOT NULL, UNIQUE(plan_id, task_key)
      );
      CREATE TABLE events (
        id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, message TEXT NOT NULL,
        meta TEXT, created_at TEXT NOT NULL
      );
      CREATE TABLE scan_files (
        scan_type TEXT NOT NULL, file_path TEXT NOT NULL, hash TEXT NOT NULL, size INTEGER NOT NULL,
        modified_at TEXT NOT NULL, scanned_at TEXT NOT NULL, PRIMARY KEY (scan_type, file_path)
      );
      CREATE TABLE chat_messages (
        id INTEGER PRIMARY KEY AUTOINCREMENT, role TEXT NOT NULL, content TEXT NOT NULL,
        tool_calls TEXT, tool_result TEXT, status TEXT, created_at TEXT NOT NULL
      );
      CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
    `);
    db.run('INSERT INTO loop_state VALUES (1, 0, ?, ?, 5, ?, NULL, NULL, ?)', ['idle', '/__autoplan_fixture__/legacy-workspace', '', FIXED_TIME]);
    db.run('INSERT INTO requirements (id,title,body,status,source_path,source_hash,linked_plan_id,created_at,updated_at) VALUES (1,?,?,?,?,?,?,?,?)', ['Synthetic requirement', 'fixture-only body', 'open', 'requirements/1.md', 'fake-hash-r1', 1, FIXED_TIME, FIXED_TIME]);
    db.run('INSERT INTO feedback (id,requirement_id,title,body,status,linked_plan_id,created_at,updated_at) VALUES (1,1,?,?,?,?,?,?)', ['Synthetic feedback', 'fixture-only feedback', 'open', 1, FIXED_TIME, FIXED_TIME]);
    db.run('INSERT INTO attachments VALUES (1,?,?,?,?,?,?,?,?)', ['requirement', 1, 'fixture.txt', 'data/attachments/fixture-only.txt', 'text/plain', 12, 'fake-hash-a1', FIXED_TIME]);
    db.run('INSERT INTO plans VALUES (1,?,?,?,?,?,?,?, ?,?)', ['fake-issue-hash', 'docs/plan/legacy-fixture.md', 'fake-plan-hash', 'pending', 1, 0, 0, FIXED_TIME, FIXED_TIME]);
    db.run('INSERT INTO plan_tasks VALUES (1,1,?,?,?,?,?,?)', ['P001', 'Synthetic legacy task', '- [ ] P001', 'pending', 1, FIXED_TIME]);
    db.run('INSERT INTO events VALUES (1,?,?,?,?)', ['fixture.created', 'synthetic legacy event', '{}', FIXED_TIME]);
    db.run('INSERT INTO scan_files VALUES (?,?,?,?,?,?)', ['workspace', 'src/fixture.js', 'fake-scan-hash', 10, FIXED_TIME, FIXED_TIME]);
    db.run('INSERT INTO chat_messages VALUES (1,?,?,?,?,?,?)', ['user', 'synthetic legacy chat', null, null, 'done', FIXED_TIME]);
    db.run("INSERT INTO settings VALUES ('mcp.authToken','fixture-not-a-token'),('chat.provider','openai'),('chat.baseUrl','https://example.invalid/v1'),('chat.apiKey','fixture-not-a-key'),('chat.model','fixture-model'),('chat.temperature','0.3')");
  });
}

function createOrphanFixture(SQL, databaseSource) {
  return makeDatabase(SQL, (db) => {
    applyCurrentSchema(db, databaseSource);
    insertProject(db, 1, 'Fixture Alpha', '/__autoplan_fixture__/alpha');
    insertProject(db, 2, 'Fixture Beta', '/__autoplan_fixture__/beta');
    db.run(`INSERT INTO requirements
      (id,project_id,title,body,status,agent_cli_command,plan_generation_command,plan_generation_model,
       plan_generation_claude_base_url,plan_generation_claude_auth_token,plan_generation_claude_model,
       plan_generation_claude_config_id,generate_fail_count,created_at,updated_at)
      VALUES (1,1,'Synthetic orphan owner','fixture-only','open','','','','','','',0,0,?,?)`, [FIXED_TIME, FIXED_TIME]);
    db.run(`INSERT INTO plans
      (id,project_id,issue_hash,file_path,hash,status,sort_order,total_tasks,completed_tasks,validation_passed,
       agent_cli_command,plan_generation_strategy,plan_generation_command,plan_generation_model,
       plan_generation_claude_base_url,plan_generation_claude_auth_token,plan_generation_claude_model,
       plan_generation_claude_config_id,plan_generation_duration_ms,plan_execution_strategy,plan_execution_command,
       plan_execution_model,plan_execution_claude_base_url,plan_execution_claude_auth_token,
       plan_execution_claude_model,plan_execution_claude_config_id,created_at,updated_at)
      VALUES (10,2,'fake-cross-project','docs/plan/cross-project.md','fake-hash','pending',1,1,0,0,
              '','external-cli-markdown','','','','','',0,0,'external-cli','','','','','',0,?,?)`, [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO plan_tasks (id,plan_id,task_key,title,raw_line,scope,status,sort_order,duration_ms,updated_at) VALUES (99,999,'ORPHAN','Synthetic orphan task','- [ ] orphan','','pending',1,0,?)", [FIXED_TIME]);
    db.run("INSERT INTO attachments (id,project_id,owner_type,owner_id,original_name,stored_path,size,hash,created_at) VALUES (9,1,'requirement',999,'orphan.txt','data/attachments/orphan-fixture.txt',1,'fake-hash',?)", [FIXED_TIME]);
    db.run("INSERT INTO intake_plan_links (id,project_id,intake_type,intake_id,plan_id,phase_index,phase_title,created_at,updated_at) VALUES (1,1,'requirement',1,10,1,'Cross project',?,?)", [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO conversations (id,project_id,title,ai_config_id,created_at,updated_at) VALUES (7,1,'Synthetic missing config',777,?,?)", [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO chat_messages (id,project_id,conversation_id,role,content,status,created_at) VALUES (8,2,7,'user','synthetic cross-project message','done',?)", [FIXED_TIME]);
  });
}

function createInvalidPathFixture(SQL, databaseSource) {
  return makeDatabase(SQL, (db) => {
    applyCurrentSchema(db, databaseSource);
    insertProject(db, 1, 'Fixture Invalid Paths', '/__autoplan_fixture__/workspace');
    db.run(`INSERT INTO requirements
      (id,project_id,title,body,status,agent_cli_command,plan_generation_command,plan_generation_model,
       plan_generation_claude_base_url,plan_generation_claude_auth_token,plan_generation_claude_model,
       plan_generation_claude_config_id,generate_fail_count,source_path,created_at,updated_at)
      VALUES (1,1,'Synthetic traversal','fixture-only','open','','','','','','',0,0,'../../outside/requirement.md',?,?)`, [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO attachments (id,project_id,owner_type,owner_id,original_name,stored_path,size,hash,created_at) VALUES (1,1,'requirement',1,'traversal.txt','../../outside/fixture.txt',1,'fake-hash',?)", [FIXED_TIME]);
    db.run(`INSERT INTO scripts
      (id,project_id,name,path,runtime,body,source_type,description,trigger_mode,enabled,work_dir,timeout_seconds,
       fail_aborts,context_inject,sort_order,created_at,updated_at)
      VALUES (1,1,'Synthetic missing Windows path','Z:\\__autoplan_fixture__\\missing.cmd','cmd','','file','fixture-only','manual',1,
              'Z:\\__autoplan_fixture__\\missing-cwd',60,0,'none',1,?,?)`, [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO scan_files (project_id,scan_type,file_path,hash,size,modified_at,scanned_at) VALUES (1,'workspace','../escape.txt','fake-hash',1,?,?)", [FIXED_TIME, FIXED_TIME]);
    db.run("INSERT INTO settings (key,value) VALUES ('update.localInstallerPath','Z:\\__autoplan_fixture__\\expired-installer.exe')");
  });
}

function createLargeFixture(SQL, databaseSource, count, seed = DEFAULT_SEED) {
  return makeDatabase(SQL, (db) => {
    applyCurrentSchema(db, databaseSource);
    insertProject(db, 1, 'Fixture Large', '/__autoplan_fixture__/large');
    db.run('BEGIN TRANSACTION');
    try {
      for (let index = 1; index <= count; index += 1) {
        const suffix = String(index).padStart(6, '0');
        db.run(`INSERT INTO requirements
          (id,project_id,title,body,status,agent_cli_command,plan_generation_command,plan_generation_model,
           plan_generation_claude_base_url,plan_generation_claude_auth_token,plan_generation_claude_model,
           plan_generation_claude_config_id,generate_fail_count,created_at,updated_at)
          VALUES (?,1,?,?, 'open','','','','','','',0,0,?,?)`, [index, `Synthetic requirement ${seed}-${suffix}`, `fixture body ${seed}-${suffix}`, FIXED_TIME, FIXED_TIME]);
        db.run(`INSERT INTO events (id,project_id,type,message,meta,created_at) VALUES (?,1,'fixture.large',?,?,?)`, [index, `synthetic event ${seed}-${suffix}`, JSON.stringify({ fixtureSeed: seed, fixtureIndex: index }), FIXED_TIME]);
      }
      db.run('COMMIT');
    } catch (error) {
      try { db.run('ROLLBACK'); } catch {}
      throw error;
    }
  });
}

function writeArtifact(staging, relativePath, data) {
  const target = path.join(staging, relativePath);
  if (!isInside(staging, target)) throw new Error(`非法产物路径：${relativePath}`);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, data, { flag: 'wx' });
  return target;
}

function sha256File(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

function inspectDatabase(SQL, filePath) {
  const db = new SQL.Database(fs.readFileSync(filePath));
  try {
    const tables = queryRows(db, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
      .map((row) => row.name);
    const rowCounts = {};
    for (const table of tables) rowCounts[table] = Number(queryRows(db, `SELECT COUNT(*) AS count FROM ${table}`)[0]?.count || 0);
    return { tables, rowCounts };
  } finally {
    db.close();
  }
}

function assertSanitizedArtifacts(staging) {
  const forbidden = [
    os.homedir(),
    process.env.APPDATA,
    process.env.LOCALAPPDATA,
  ].filter(Boolean).map((item) => String(item).replace(/\\/g, '/').toLowerCase());
  const credentialPatterns = [
    /\bsk-[A-Za-z0-9_-]{12,}\b/,
    /\b(?:ghp|github_pat)_[A-Za-z0-9_]{12,}\b/i,
    /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/,
  ];
  for (const entry of fs.readdirSync(staging, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const filePath = path.join(staging, entry.name);
    const text = fs.readFileSync(filePath).toString('latin1');
    const normalized = text.replace(/\\/g, '/').toLowerCase();
    if (forbidden.some((value) => value && normalized.includes(value))) throw new Error(`产物包含本机路径：${entry.name}`);
    if (credentialPatterns.some((pattern) => pattern.test(text))) throw new Error(`产物包含凭据形态：${entry.name}`);
  }
}

async function generateFixtures(options = {}) {
  const rootDir = path.resolve(options.rootDir || path.join(__dirname, '../..'));
  const resolved = resolveOutput(options);
  const recipeManifest = JSON.parse(readText(rootDir, MANIFEST_PATH));
  const databaseSource = readText(rootDir, DATABASE_SOURCE_PATH);
  const SQL = await loadSqlJs(rootDir);
  let lock;
  let staging;
  try {
    lock = acquireLock(resolved.parent, resolved.target);
    staging = path.join(resolved.parent, `.autoplan-p00-staging-${process.pid}-${crypto.randomBytes(6).toString('hex')}`);
    fs.mkdirSync(staging, { recursive: false, mode: 0o700 });

    const buffers = {
      empty: createEmptyFixture(SQL, databaseSource),
      'legacy-normal': createLegacyFixture(SQL),
      'orphan-cross-project': createOrphanFixture(SQL, databaseSource),
      'invalid-paths': createInvalidPathFixture(SQL, databaseSource),
      large: createLargeFixture(SQL, databaseSource, resolved.largeCount, resolved.seed),
    };
    const artifacts = [];
    for (const name of FIXTURE_NAMES) {
      const fileName = `${name}.sqlite`;
      const filePath = writeArtifact(staging, fileName, buffers[name]);
      const inspection = inspectDatabase(SQL, filePath);
      artifacts.push({ name, file: fileName, sha256: sha256File(filePath), ...inspection });
    }
    writeArtifact(staging, 'invalid-path-cases.json', Buffer.from(`${JSON.stringify(recipeManifest.pathCases, null, 2)}\n`, 'utf8'));
    const generatedManifest = {
      schemaVersion: 1,
      recipeVersion: recipeManifest.version,
      seed: resolved.seed,
      largeCount: resolved.largeCount,
      fixedTime: FIXED_TIME,
      syntheticOnly: true,
      artifacts,
    };
    writeArtifact(staging, 'generated-manifest.json', Buffer.from(`${JSON.stringify(generatedManifest, null, 2)}\n`, 'utf8'));
    assertSanitizedArtifacts(staging);
    fs.renameSync(staging, resolved.target);
    staging = null;
    return { outputDir: resolved.target, manifest: generatedManifest };
  } catch (error) {
    safeRemoveStaging(staging, resolved.parent);
    throw error;
  } finally {
    releaseLock(lock);
  }
}

function readText(rootDir, relativePath) {
  return fs.readFileSync(path.join(rootDir, relativePath), 'utf8');
}

function parseArgs(argv) {
  const options = {};
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--output') options.outputDir = argv[++index];
    else if (arg === '--allow-root') options.allowRoot = argv[++index];
    else if (arg === '--seed') options.seed = argv[++index];
    else if (arg === '--large-count') options.largeCount = argv[++index];
    else throw new Error(`未知参数：${arg}`);
    if (argv[index] === undefined) throw new Error(`参数缺少值：${arg}`);
  }
  return options;
}

if (require.main === module) {
  generateFixtures(parseArgs(process.argv.slice(2)))
    .then((result) => process.stdout.write(`${JSON.stringify({ ok: true, ...result })}\n`))
    .catch((error) => {
      process.stderr.write(`${error.message}\n`);
      process.exitCode = 1;
    });
}

module.exports = {
  DEFAULT_LARGE_COUNT,
  DEFAULT_SEED,
  FIXED_TIME,
  FIXTURE_NAMES,
  acquireLock,
  assertSanitizedArtifacts,
  createEmptyFixture,
  createInvalidPathFixture,
  createLargeFixture,
  createLegacyFixture,
  createOrphanFixture,
  extractEnsureColumns,
  extractInitialDdl,
  generateFixtures,
  isInside,
  knownUserDataRoots,
  looksLikeUserData,
  parseArgs,
  releaseLock,
  resolveOutput,
};
