'use strict';

const fs = require('node:fs');
const path = require('node:path');

// Go migration ledger entries. The checksums MUST match the ones compiled into
// backend/migrations/registry.go; the Go migration runner validates them on
// startup and rejects the database (ErrDirtyHistory) on any mismatch.
const GO_MIGRATION_LEDGER = [
  {
    version: 1,
    name: 'schema_v1',
    checksum: 'a8d932e46a8f09d49b103cf420a408074688f1de1c20c97eee0d1ba8431b786d',
    userVersion: 1,
  },
  {
    version: 2,
    name: 'operations_outbox_v2',
    checksum: '5fe6f19198f4de50386bcf16727341731e4ec536d2aba8449932aa75f9286a68',
    userVersion: 2,
  },
  {
    version: 3,
    name: 'operation_start_times_v3',
    checksum: '218a31580d985a36b58abf254ef1695688685bab5ab2dacca112afb136449c32',
    userVersion: 3,
  },
];

const GO_SCHEMA_USER_VERSION = 3;

// Tables to migrate from the legacy Node database, ordered by foreign-key
// dependency. Each entry maps the legacy table to the target Go-schema table.
// Column lists are the intersection of Node and Go schemas; Go-only columns
// (e.g. version) take their DEFAULT via the schema's ADD COLUMN.
const MIGRATION_TABLES = [
  { from: 'projects', to: 'projects',
    columns: 'id, name, workspace_path, description, created_at, updated_at' },
  { from: 'project_states', to: 'project_states',
    columns: 'project_id, running, phase, interval_seconds, validation_command, project_prompt, agent_cli_provider, agent_cli_command, codex_reasoning_effort, plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model, plan_generation_codex_reasoning_effort, plan_generation_claude_base_url, plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id, plan_execution_strategy, plan_execution_provider, plan_execution_command, plan_execution_model, plan_execution_codex_reasoning_effort, plan_execution_claude_base_url, plan_execution_claude_auth_token, plan_execution_claude_model, plan_execution_claude_config_id, last_issue_hash, last_error, env_vars, updated_at' },
  { from: 'plans', to: 'plans',
    columns: 'id, project_id, issue_hash, file_path, hash, status, sort_order, total_tasks, completed_tasks, validation_passed, agent_cli_provider, agent_cli_command, codex_reasoning_effort, plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model, plan_generation_codex_reasoning_effort, plan_generation_claude_base_url, plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id, plan_generation_duration_ms, plan_execution_strategy, plan_execution_provider, plan_execution_command, plan_execution_model, plan_execution_codex_reasoning_effort, plan_execution_claude_base_url, plan_execution_claude_auth_token, plan_execution_claude_model, plan_execution_claude_config_id, agent_cli_session_id, created_at, updated_at, accepted_at' },
  { from: 'plan_tasks', to: 'plan_tasks',
    columns: 'id, plan_id, task_key, title, raw_line, scope, status, sort_order, started_at, finished_at, duration_ms, agent_cli_session_id, codex_session_id, updated_at, accepted_at' },
  { from: 'requirements', to: 'requirements',
    columns: 'id, project_id, title, body, status, agent_cli_provider, agent_cli_command, codex_reasoning_effort, plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model, plan_generation_codex_reasoning_effort, plan_generation_claude_base_url, plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id, generate_fail_count, last_generate_fail_at, last_generate_error, last_generate_log_file, last_generate_agent_cli_provider, last_generate_codex_reasoning_effort, source_path, source_hash, created_at, updated_at, accepted_at, linked_plan_id' },
  { from: 'feedback', to: 'feedback',
    columns: 'id, project_id, requirement_id, title, body, status, agent_cli_provider, agent_cli_command, codex_reasoning_effort, plan_generation_strategy, plan_generation_provider, plan_generation_command, plan_generation_model, plan_generation_codex_reasoning_effort, plan_generation_claude_base_url, plan_generation_claude_auth_token, plan_generation_claude_model, plan_generation_claude_config_id, agent_cli_session_id, generate_fail_count, last_generate_fail_at, last_generate_error, last_generate_log_file, last_generate_agent_cli_provider, last_generate_codex_reasoning_effort, created_at, updated_at, accepted_at, linked_plan_id' },
  { from: 'intake_plan_links', to: 'intake_plan_links',
    columns: 'id, project_id, intake_type, intake_id, plan_id, phase_index, phase_title, created_at, updated_at' },
  { from: 'ai_configs', to: 'ai_configs',
    columns: 'id, project_id, name, provider, base_url, api_key, model, temperature, thinking_depth, thinking_budget_tokens, created_at, updated_at' },
  { from: 'claude_cli_configs', to: 'claude_cli_configs',
    columns: 'id, project_id, name, base_url, auth_token, model, is_default, created_at, updated_at' },
  { from: 'conversations', to: 'conversations',
    columns: 'id, project_id, title, ai_config_id, pinned_at, created_at, updated_at, codex_session_id' },
  { from: 'chat_messages', to: 'chat_messages',
    columns: 'id, project_id, role, content, tool_calls, tool_result, status, created_at, conversation_id' },
  { from: 'scripts', to: 'scripts',
    columns: 'id, project_id, name, path, runtime, body, description, trigger_mode, hook_stage, schedule_cron, enabled, work_dir, timeout_seconds, fail_aborts, context_inject, sort_order, last_status, last_exit_code, last_duration_ms, last_log, last_run_at, created_at, updated_at, source_type' },
  { from: 'executors', to: 'executors',
    columns: 'id, project_id, label, type, command, args_json, actions_json, options_json, group_kind, group_is_default, presentation_json, problem_matcher_json, depends_on_json, depends_order, enabled, sort_order, last_status, last_exit_code, last_duration_ms, last_log, last_run_at, plugin_state_json, created_at, updated_at' },
  { from: 'scan_files', to: 'scan_files',
    columns: 'project_id, scan_type, file_path, hash, size, modified_at, scanned_at' },
];

const SENTINEL_FILE = '.legacy-migration-completed';

/**
 * Migrate the legacy Node database into a fresh Go-schema database at the
 * target path. The Go sidecar opens this database on startup; by pre-building
 * a complete schema (user_version=3 with a valid ledger) the Go migration
 * runner reports NoOp and the audit passes because all stored paths resolve
 * under the temp data directory.
 *
 * @param {object} options
 * @param {string} options.sourceDbPath  Absolute path to legacy autoplan.sqlite.
 * @param {string} options.targetDbPath  Absolute path for the new database file.
 * @param {string} options.sourceAttachmentsDir  Legacy attachments directory.
 * @param {string} options.targetAttachmentsDir  Destination attachments directory (under dataDir).
 * @param {Function} options.initSqlJs  sql.js factory (require('sql.js')).
 * @param {object} [options.sqlJsOptions]  locateFile options for sql.js.
 * @returns {Promise<{ migrated: boolean, tables: Record<string, number> }>}
 */
async function migrateLegacyDatabase(options = {}) {
  const {
    sourceDbPath, targetDbPath, sourceAttachmentsDir, targetAttachmentsDir,
    initSqlJs, sqlJsOptions = {},
  } = options;

  if (!sourceDbPath || !targetDbPath || typeof initSqlJs !== 'function') {
    throw new Error('migration_invalid_options');
  }
  if (!fs.existsSync(sourceDbPath)) throw new Error('migration_source_missing');

  const SQL = await initSqlJs(sqlJsOptions);
  const sourceBuffer = fs.readFileSync(sourceDbPath);
  const source = new SQL.Database(sourceBuffer);

  // Build a fresh empty database; sql.js exports the raw binary on save().
  const target = new SQL.Database();

  try {
    applyGoSchema(target);
    seedMigrationLedger(target);
    const counts = copyTables(source, target);
    copySettings(source, target);
    copyLoopState(source, target);
    copyAttachments(source, target, sourceAttachmentsDir, targetAttachmentsDir);
    seedPostMigration(target);

    fs.mkdirSync(path.dirname(targetDbPath), { recursive: true });
    const data = target.export();
    fs.writeFileSync(targetDbPath, Buffer.from(data));
    return { migrated: true, tables: counts };
  } finally {
    source.close();
    target.close();
  }
}

function applyGoSchema(target) {
  // 0001_schema_v1.sql — CREATE TABLE IF NOT EXISTS + ALTER + seed INSERTs.
  target.run(SCHEMA_V1_SQL);
  // 0002_operations_outbox_v2.sql — ALTER + CREATE + seed INSERTs.
  target.run(SCHEMA_V2_SQL);
  // 0003_operation_start_times_v3.sql — UPDATE (no-op on empty operations).
  target.run(SCHEMA_V3_SQL);
}

function seedMigrationLedger(target) {
  const appliedAt = new Date().toISOString();
  const stmt = target.prepare(
    'INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)',
  );
  try {
    for (const entry of GO_MIGRATION_LEDGER) {
      stmt.run([entry.version, entry.name, entry.checksum, appliedAt]);
    }
  } finally {
    stmt.free();
  }
  target.run(`PRAGMA user_version = ${GO_SCHEMA_USER_VERSION}`);
}

function tableExists(db, name) {
  const stmt = db.prepare(
    "SELECT COUNT(*) AS c FROM sqlite_master WHERE type = 'table' AND name = ?",
  );
  try {
    stmt.bind([name]);
    stmt.step();
    return stmt.getAsObject().c > 0;
  } finally {
    stmt.free();
  }
}

function copyTables(source, target) {
  const counts = {};
  for (const { from, to, columns } of MIGRATION_TABLES) {
    if (!tableExists(source, from)) continue;
    const colList = columns.split(',').map((c) => c.trim());
    const placeholders = colList.map(() => '?').join(', ');
    const insert = target.prepare(
      `INSERT OR REPLACE INTO ${to} (${columns}) VALUES (${placeholders})`,
    );
    try {
      const select = source.prepare(`SELECT ${columns} FROM ${from}`);
      let copied = 0;
      try {
        while (select.step()) {
          const row = select.getAsObject();
          insert.run(colList.map((col) => row[col] ?? null));
          copied += 1;
        }
      } finally {
        select.free();
      }
      counts[to] = copied;
    } finally {
      insert.free();
    }
  }
  return counts;
}

// Settings: the legacy values are user configuration that must survive the
// migration. Go's seed INSERT used OR IGNORE, so overwriting is safe and keeps
// the user's actual MCP/chat/terminal/update preferences.
function copySettings(source, target) {
  if (!tableExists(source, 'settings')) return;
  const insert = target.prepare(
    'INSERT OR REPLACE INTO settings (key, value, version) VALUES (?, ?, ?)',
  );
  const select = source.prepare('SELECT key, value FROM settings');
  try {
    while (select.step()) {
      const { key, value } = select.getAsObject();
      if (typeof key !== 'string' || key.includes('\x00')) continue;
      insert.run([key, String(value ?? ''), 1]);
    }
  } finally {
    select.free();
    insert.free();
  }
}

// loop_state is a single-row singleton (id=1). The legacy row reflects the
// user's last loop configuration; overwrite the Go seed.
function copyLoopState(source, target) {
  if (!tableExists(source, 'loop_state')) return;
  const select = source.prepare(
    'SELECT running, phase, workspace_path, interval_seconds, validation_command, last_issue_hash, last_error, updated_at FROM loop_state WHERE id = 1',
  );
  try {
    if (!select.step()) return;
    const row = select.getAsObject();
    target.run(
      `INSERT OR REPLACE INTO loop_state
       (id, running, phase, workspace_path, interval_seconds, validation_command, last_issue_hash, last_error, updated_at)
       VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?)`,
      [
        Number(row.running) || 0,
        String(row.phase || 'idle'),
        String(row.workspace_path || ''),
        Number(row.interval_seconds) || 5,
        String(row.validation_command || ''),
        row.last_issue_hash ?? null,
        row.last_error ?? null,
        String(row.updated_at || new Date().toISOString()),
      ],
    );
  } finally {
    select.free();
  }
}

function copyAttachments(source, target, sourceAttachmentsDir, targetAttachmentsDir) {
  if (!tableExists(source, 'attachments')) return;
  if (!sourceAttachmentsDir || !targetAttachmentsDir) return;
  let copiedDir = false;
  const ensureTargetDir = () => {
    if (copiedDir) return;
    fs.mkdirSync(targetAttachmentsDir, { recursive: true });
    copiedDir = true;
  };

  const insert = target.prepare(
    'INSERT OR REPLACE INTO attachments (id, project_id, owner_type, owner_id, original_name, stored_path, mime_type, size, hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
  );
  const select = source.prepare(
    'SELECT id, project_id, owner_type, owner_id, original_name, stored_path, mime_type, size, hash, created_at FROM attachments',
  );
  try {
    while (select.step()) {
      const row = select.getAsObject();
      // Rewrite the stored path so it resolves under the temp data directory
      // (the Go audit's AuthorizedRoot). Copy the physical file alongside.
      const sourceFile = path.win32.basename(String(row.stored_path || ''));
      const ownerSegment = `${row.owner_type}/${row.owner_id}`;
      const sourceSubdir = path.join(sourceAttachmentsDir, ownerSegment);
      const targetSubdir = path.join(targetAttachmentsDir, ownerSegment);
      let resolvedTarget = String(row.stored_path || '');
      const candidate = path.join(sourceSubdir, sourceFile);
      if (sourceFile && fs.existsSync(candidate)) {
        ensureTargetDir();
        fs.mkdirSync(targetSubdir, { recursive: true });
        fs.copyFileSync(candidate, path.join(targetSubdir, sourceFile));
        resolvedTarget = path.join(targetSubdir, sourceFile);
      }
      insert.run([
        row.id, row.project_id ?? null, row.owner_type, row.owner_id,
        row.original_name, resolvedTarget, row.mime_type ?? null,
        Number(row.size) || 0, String(row.hash || ''), row.created_at,
      ]);
    }
  } finally {
    select.free();
    insert.free();
  }
}

/**
 * Returns true when the migration sentinel exists, indicating the target
 * database directory has already received legacy data and must not be
 * re-migrated.
 */
function isLegacyMigrationCompleted(dataDir) {
  return fs.existsSync(path.join(dataDir, SENTINEL_FILE));
}

function markLegacyMigrationCompleted(dataDir) {
  fs.writeFileSync(path.join(dataDir, SENTINEL_FILE), new Date().toISOString());
}

// Seeds that depend on migrated data. These INSERTs run in the Go migration
// SQL (0002) but query empty tables at apply time; re-run them here so the
// project_revisions and event_cursors tables are consistent for the Go runtime.
function seedPostMigration(target) {
  target.run('INSERT OR IGNORE INTO project_revisions (project_id, revision) SELECT id, 0 FROM projects');
  target.run("INSERT OR IGNORE INTO event_cursors (name, next_event_id) SELECT 'outbox', COALESCE(MAX(id), 0) FROM event_outbox");
  reconcileIntakePlanLinks(target);
}

// The Go audit requires that every requirement/feedback with a linked_plan_id
// has a matching intake_plan_links row at phase_index=1 whose plan_id equals
// the linked_plan_id. Some legacy rows have linked_plan_id set without a
// matching phase-1 link. Remove stale phase-1 links then insert correct ones.
function reconcileIntakePlanLinks(target) {
  const now = new Date().toISOString();
  // Remove phase-1 links that disagree with the linked_plan_id on the intake.
  target.run(
    `DELETE FROM intake_plan_links
     WHERE phase_index = 1 AND intake_type = 'requirement'
       AND EXISTS (SELECT 1 FROM requirements r
         WHERE r.id = intake_plan_links.intake_id AND r.project_id = intake_plan_links.project_id
           AND r.linked_plan_id IS NOT NULL AND r.linked_plan_id != intake_plan_links.plan_id)`,
  );
  target.run(
    `DELETE FROM intake_plan_links
     WHERE phase_index = 1 AND intake_type = 'feedback'
       AND EXISTS (SELECT 1 FROM feedback f
         WHERE f.id = intake_plan_links.intake_id AND f.project_id = intake_plan_links.project_id
           AND f.linked_plan_id IS NOT NULL AND f.linked_plan_id != intake_plan_links.plan_id)`,
  );
  // Insert the canonical phase-1 link for every intake with a linked plan.
  target.run(
    `INSERT OR IGNORE INTO intake_plan_links (project_id, intake_type, intake_id, plan_id, phase_index, phase_title, created_at, updated_at)
     SELECT r.project_id, 'requirement', r.id, r.linked_plan_id, 1, '', '${now}', '${now}'
     FROM requirements r
     WHERE r.linked_plan_id IS NOT NULL`,
  );
  target.run(
    `INSERT OR IGNORE INTO intake_plan_links (project_id, intake_type, intake_id, plan_id, phase_index, phase_title, created_at, updated_at)
     SELECT f.project_id, 'feedback', f.id, f.linked_plan_id, 1, '', '${now}', '${now}'
     FROM feedback f
     WHERE f.linked_plan_id IS NOT NULL`,
  );
}

module.exports = {
  migrateLegacyDatabase,
  isLegacyMigrationCompleted,
  markLegacyMigrationCompleted,
};

// Schema SQL embedded from backend/migrations/. The Go migration runner
// checksums the exact bytes of these statements; do not modify without
// updating backend/migrations/registry.go in lockstep.
const SCHEMA_V1_SQL = `-- AutoPlan schema v1. This file is append-only migration history.
-- It intentionally contains no raw credential values or fixture data.

CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  checksum TEXT NOT NULL,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  workspace_path TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_updated
ON projects (updated_at DESC);

CREATE TABLE IF NOT EXISTS project_states (
  project_id INTEGER PRIMARY KEY,
  running INTEGER NOT NULL DEFAULT 0,
  phase TEXT NOT NULL DEFAULT 'idle',
  interval_seconds INTEGER NOT NULL DEFAULT 5,
  validation_command TEXT NOT NULL DEFAULT '',
  project_prompt TEXT NOT NULL DEFAULT '',
  agent_cli_provider TEXT NOT NULL DEFAULT 'codex',
  agent_cli_command TEXT NOT NULL DEFAULT '',
  codex_reasoning_effort TEXT,
  plan_generation_strategy TEXT NOT NULL DEFAULT 'external-cli-markdown',
  plan_generation_provider TEXT,
  plan_generation_command TEXT NOT NULL DEFAULT '',
  plan_generation_model TEXT NOT NULL DEFAULT '',
  plan_generation_codex_reasoning_effort TEXT,
  plan_generation_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_generation_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_generation_claude_model TEXT NOT NULL DEFAULT '',
  plan_generation_claude_config_id INTEGER NOT NULL DEFAULT 0,
  plan_execution_strategy TEXT NOT NULL DEFAULT 'external-cli',
  plan_execution_provider TEXT,
  plan_execution_command TEXT NOT NULL DEFAULT '',
  plan_execution_model TEXT NOT NULL DEFAULT '',
  plan_execution_codex_reasoning_effort TEXT,
  plan_execution_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_execution_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_execution_claude_model TEXT NOT NULL DEFAULT '',
  plan_execution_claude_config_id INTEGER NOT NULL DEFAULT 0,
  last_issue_hash TEXT,
  last_error TEXT,
  env_vars TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS requirements (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  agent_cli_provider TEXT,
  agent_cli_command TEXT NOT NULL DEFAULT '',
  codex_reasoning_effort TEXT,
  plan_generation_strategy TEXT,
  plan_generation_provider TEXT,
  plan_generation_command TEXT NOT NULL DEFAULT '',
  plan_generation_model TEXT NOT NULL DEFAULT '',
  plan_generation_codex_reasoning_effort TEXT,
  plan_generation_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_generation_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_generation_claude_model TEXT NOT NULL DEFAULT '',
  plan_generation_claude_config_id INTEGER NOT NULL DEFAULT 0,
  generate_fail_count INTEGER DEFAULT 0,
  last_generate_fail_at TEXT,
  last_generate_error TEXT,
  last_generate_log_file TEXT,
  last_generate_agent_cli_provider TEXT,
  last_generate_codex_reasoning_effort TEXT,
  source_path TEXT,
  source_hash TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  accepted_at TEXT,
  linked_plan_id INTEGER,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS feedback (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  requirement_id INTEGER,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'open',
  agent_cli_provider TEXT,
  agent_cli_command TEXT NOT NULL DEFAULT '',
  codex_reasoning_effort TEXT,
  plan_generation_strategy TEXT,
  plan_generation_provider TEXT,
  plan_generation_command TEXT NOT NULL DEFAULT '',
  plan_generation_model TEXT NOT NULL DEFAULT '',
  plan_generation_codex_reasoning_effort TEXT,
  plan_generation_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_generation_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_generation_claude_model TEXT NOT NULL DEFAULT '',
  plan_generation_claude_config_id INTEGER NOT NULL DEFAULT 0,
  agent_cli_session_id TEXT,
  generate_fail_count INTEGER DEFAULT 0,
  last_generate_fail_at TEXT,
  last_generate_error TEXT,
  last_generate_log_file TEXT,
  last_generate_agent_cli_provider TEXT,
  last_generate_codex_reasoning_effort TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  accepted_at TEXT,
  linked_plan_id INTEGER,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
  FOREIGN KEY (requirement_id) REFERENCES requirements(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS attachments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('requirement', 'feedback')),
  owner_id INTEGER NOT NULL,
  original_name TEXT NOT NULL,
  stored_path TEXT NOT NULL,
  mime_type TEXT,
  size INTEGER NOT NULL,
  hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_attachments_owner
ON attachments (owner_type, owner_id);

CREATE TABLE IF NOT EXISTS scan_files (
  project_id INTEGER NOT NULL DEFAULT 1,
  scan_type TEXT NOT NULL,
  file_path TEXT NOT NULL,
  hash TEXT NOT NULL,
  size INTEGER NOT NULL,
  modified_at TEXT NOT NULL,
  scanned_at TEXT NOT NULL,
  PRIMARY KEY (project_id, scan_type, file_path),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS plans (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  issue_hash TEXT NOT NULL,
  file_path TEXT NOT NULL,
  hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  sort_order INTEGER NOT NULL DEFAULT 0,
  total_tasks INTEGER NOT NULL DEFAULT 0,
  completed_tasks INTEGER NOT NULL DEFAULT 0,
  validation_passed INTEGER NOT NULL DEFAULT 0,
  agent_cli_provider TEXT,
  agent_cli_command TEXT NOT NULL DEFAULT '',
  codex_reasoning_effort TEXT,
  plan_generation_strategy TEXT NOT NULL DEFAULT 'external-cli-markdown',
  plan_generation_provider TEXT,
  plan_generation_command TEXT NOT NULL DEFAULT '',
  plan_generation_model TEXT NOT NULL DEFAULT '',
  plan_generation_codex_reasoning_effort TEXT,
  plan_generation_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_generation_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_generation_claude_model TEXT NOT NULL DEFAULT '',
  plan_generation_claude_config_id INTEGER NOT NULL DEFAULT 0,
  plan_generation_duration_ms INTEGER NOT NULL DEFAULT 0,
  plan_execution_strategy TEXT NOT NULL DEFAULT 'external-cli',
  plan_execution_provider TEXT,
  plan_execution_command TEXT NOT NULL DEFAULT '',
  plan_execution_model TEXT NOT NULL DEFAULT '',
  plan_execution_codex_reasoning_effort TEXT,
  plan_execution_claude_base_url TEXT NOT NULL DEFAULT '',
  plan_execution_claude_auth_token TEXT NOT NULL DEFAULT '',
  plan_execution_claude_model TEXT NOT NULL DEFAULT '',
  plan_execution_claude_config_id INTEGER NOT NULL DEFAULT 0,
  agent_cli_session_id TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  accepted_at TEXT,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_plans_project_sort
ON plans (project_id, sort_order, created_at, id);

CREATE TABLE IF NOT EXISTS plan_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  plan_id INTEGER NOT NULL,
  task_key TEXT NOT NULL,
  title TEXT NOT NULL,
  raw_line TEXT NOT NULL,
  scope TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  sort_order INTEGER NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  agent_cli_session_id TEXT,
  codex_session_id TEXT,
  updated_at TEXT NOT NULL,
  accepted_at TEXT,
  UNIQUE (plan_id, task_key),
  FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_plan_tasks_plan_status_sort
ON plan_tasks (plan_id, status, sort_order, id);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  type TEXT NOT NULL,
  message TEXT NOT NULL,
  meta TEXT,
  created_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS scripts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  name TEXT NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  runtime TEXT NOT NULL DEFAULT 'node',
  body TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  trigger_mode TEXT NOT NULL DEFAULT 'manual',
  hook_stage TEXT,
  schedule_cron TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  work_dir TEXT NOT NULL DEFAULT '',
  timeout_seconds INTEGER NOT NULL DEFAULT 60,
  fail_aborts INTEGER NOT NULL DEFAULT 0,
  context_inject TEXT NOT NULL DEFAULT 'none',
  sort_order INTEGER NOT NULL DEFAULT 0,
  last_status TEXT,
  last_exit_code INTEGER,
  last_duration_ms INTEGER,
  last_log TEXT,
  last_run_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  source_type TEXT NOT NULL DEFAULT 'inline',
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_scripts_project
ON scripts (project_id);

CREATE INDEX IF NOT EXISTS idx_scripts_project_hook_stage
ON scripts (project_id, hook_stage);

CREATE TABLE IF NOT EXISTS executors (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  label TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'shell',
  command TEXT NOT NULL,
  args_json TEXT NOT NULL DEFAULT '[]',
  actions_json TEXT,
  options_json TEXT NOT NULL DEFAULT '{}',
  group_kind TEXT,
  group_is_default INTEGER NOT NULL DEFAULT 0,
  presentation_json TEXT NOT NULL DEFAULT '{}',
  problem_matcher_json TEXT,
  depends_on_json TEXT NOT NULL DEFAULT '[]',
  depends_order TEXT NOT NULL DEFAULT 'parallel',
  enabled INTEGER NOT NULL DEFAULT 1,
  sort_order INTEGER NOT NULL DEFAULT 0,
  last_status TEXT,
  last_exit_code INTEGER,
  last_duration_ms INTEGER,
  last_log TEXT,
  last_run_at TEXT,
  plugin_state_json TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_executors_project_sort
ON executors (project_id, sort_order, id);

CREATE INDEX IF NOT EXISTS idx_executors_project_label
ON executors (project_id, label);

CREATE TABLE IF NOT EXISTS ai_configs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  name TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'openai',
  base_url TEXT NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  temperature TEXT NOT NULL DEFAULT '0.3',
  thinking_depth TEXT,
  thinking_budget_tokens INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_ai_configs_project
ON ai_configs (project_id);

CREATE TABLE IF NOT EXISTS claude_cli_configs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  is_default INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_claude_cli_configs_project
ON claude_cli_configs (project_id);

CREATE TABLE IF NOT EXISTS conversations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  title TEXT NOT NULL DEFAULT '',
  ai_config_id INTEGER,
  pinned_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  codex_session_id TEXT,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
  FOREIGN KEY (ai_config_id) REFERENCES ai_configs(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_conversations_project
ON conversations (project_id);

CREATE INDEX IF NOT EXISTS idx_conversations_project_pinned_updated
ON conversations (project_id, pinned_at, updated_at, id);

CREATE TABLE IF NOT EXISTS chat_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER,
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  tool_calls TEXT,
  tool_result TEXT,
  status TEXT,
  created_at TEXT NOT NULL,
  conversation_id INTEGER,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE,
  FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_chat_messages_project
ON chat_messages (project_id, created_at);

CREATE INDEX IF NOT EXISTS idx_chat_messages_conversation
ON chat_messages (conversation_id, created_at);

CREATE TABLE IF NOT EXISTS loop_state (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  running INTEGER NOT NULL DEFAULT 0,
  phase TEXT NOT NULL DEFAULT 'idle',
  workspace_path TEXT NOT NULL DEFAULT '',
  interval_seconds INTEGER NOT NULL DEFAULT 5,
  validation_command TEXT NOT NULL DEFAULT '',
  last_issue_hash TEXT,
  last_error TEXT,
  updated_at TEXT NOT NULL
);

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
  UNIQUE (project_id, intake_type, intake_id, plan_id),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
  FOREIGN KEY (plan_id) REFERENCES plans(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_plan_links_intake_phase
ON intake_plan_links (project_id, intake_type, intake_id, phase_index);

CREATE INDEX IF NOT EXISTS idx_intake_plan_links_intake
ON intake_plan_links (project_id, intake_type, intake_id, phase_index, plan_id);

CREATE INDEX IF NOT EXISTS idx_intake_plan_links_plan
ON intake_plan_links (project_id, plan_id, intake_type, intake_id);

CREATE TABLE IF NOT EXISTS operations (
  operation_id TEXT PRIMARY KEY,
  project_id INTEGER,
  type TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled', 'interrupted')),
  request_id TEXT NOT NULL,
  idempotency_scope TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT,
  request_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  result_json TEXT,
  error_json TEXT,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_operations_idempotency
ON operations (idempotency_scope, idempotency_key)
WHERE idempotency_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_operations_project_status
ON operations (project_id, status, created_at, operation_id);

CREATE TABLE IF NOT EXISTS event_outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL UNIQUE,
  schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version = 1),
  stream_key TEXT NOT NULL,
  sequence INTEGER NOT NULL CHECK (sequence >= 0),
  type TEXT NOT NULL,
  request_id TEXT NOT NULL,
  operation_id TEXT,
  project_id INTEGER,
  occurred_at TEXT NOT NULL,
  data_json TEXT NOT NULL,
  published_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error TEXT,
  created_at TEXT NOT NULL,
  UNIQUE (stream_key, sequence),
  FOREIGN KEY (operation_id) REFERENCES operations(operation_id) ON DELETE RESTRICT,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_event_outbox_pending
ON event_outbox (published_at, id);

CREATE INDEX IF NOT EXISTS idx_event_outbox_stream
ON event_outbox (stream_key, sequence, id);

CREATE TABLE IF NOT EXISTS secret_refs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_type TEXT NOT NULL,
  owner_id TEXT NOT NULL,
  field_name TEXT NOT NULL,
  provider TEXT NOT NULL,
  reference TEXT NOT NULL,
  has_value INTEGER NOT NULL DEFAULT 1 CHECK (has_value IN (0, 1)),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  UNIQUE (owner_type, owner_id, field_name)
);

CREATE INDEX IF NOT EXISTS idx_secret_refs_owner
ON secret_refs (owner_type, owner_id, field_name);

ALTER TABLE settings ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE project_states ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE ai_configs ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE claude_cli_configs ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE scripts ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
ALTER TABLE executors ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);

INSERT OR IGNORE INTO loop_state (
  id, running, phase, workspace_path, interval_seconds, validation_command, updated_at
) VALUES (1, 0, 'idle', '', 5, '', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

INSERT OR IGNORE INTO settings (key, value) VALUES
  ('mcp.enabled', 'true'),
  ('mcp.transport', 'http'),
  ('mcp.host', '127.0.0.1'),
  ('mcp.port', '43847'),
  ('mcp.path', '/mcp'),
  ('mcp.authToken', ''),
  ('chat.provider', 'openai'),
  ('chat.baseUrl', 'https://api.openai.com'),
  ('chat.apiKey', ''),
  ('chat.model', 'gpt-5.5'),
  ('chat.temperature', '0.3'),
  ('terminal.defaultProfile', 'default'),
  ('terminal.initialCwd', ''),
  ('terminal.fontSize', '13'),
  ('terminal.scrollbackLimit', '10000'),
  ('terminal.retainOnExit', 'true'),
  ('terminal.confirmBeforeKill', 'true'),
  ('update.autoCheck', 'true'),
  ('update.intervalMinutes', '360'),
  ('update.lastCheckedAt', ''),
  ('update.dismissedVersion', ''),
  ('update.installerAssetAvailable', 'false'),
  ('update.installerAssetStatus', ''),
  ('update.installerAssetReason', ''),
  ('update.installerAssetName', ''),
  ('update.installerAssetDownloadUrl', ''),
  ('update.installerAssetSize', ''),
  ('update.installerAssetPlatform', ''),
  ('update.installerAssetArch', ''),
  ('update.installerAssetKind', ''),
  ('update.downloadPhase', 'idle'),
  ('update.downloadProgress', '0'),
  ('update.downloadError', ''),
  ('update.downloadReason', ''),
  ('update.downloadStartedAt', ''),
  ('update.downloadCompletedAt', ''),
  ('update.downloadBytesReceived', '0'),
  ('update.downloadTotalBytes', '0'),
  ('update.downloadAssetKey', ''),
  ('update.downloadVersion', ''),
  ('update.localInstallerPath', '');
`;

const SCHEMA_V2_SQL = `-- P10 Operation and durable event-outbox contract.
-- This is append-only history: it upgrades an already-applied P09 v1 copy
-- without changing the v1 checksum or its compatibility tables.

ALTER TABLE operations ADD COLUMN cancel_requested_at TEXT;
ALTER TABLE operations ADD COLUMN output_json TEXT;

ALTER TABLE event_outbox ADD COLUMN event_class TEXT NOT NULL DEFAULT 'business'
  CHECK (event_class IN ('business', 'operation', 'control'));
ALTER TABLE event_outbox ADD COLUMN project_revision INTEGER;

CREATE TABLE IF NOT EXISTS project_revisions (
  project_id INTEGER PRIMARY KEY,
  revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS event_cursors (
  name TEXT PRIMARY KEY CHECK (name = 'outbox'),
  next_event_id INTEGER NOT NULL DEFAULT 0 CHECK (next_event_id >= 0)
);

CREATE TABLE IF NOT EXISTS event_retention_watermarks (
  project_id INTEGER PRIMARY KEY,
  deleted_through_event_id TEXT NOT NULL DEFAULT '0'
    CHECK (deleted_through_event_id GLOB '[0-9]*'),
  updated_at TEXT NOT NULL,
  FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT
);

INSERT OR IGNORE INTO project_revisions (project_id, revision)
SELECT id, 0 FROM projects;

INSERT OR IGNORE INTO event_cursors (name, next_event_id)
SELECT 'outbox', COALESCE(MAX(id), 0) FROM event_outbox;

CREATE INDEX IF NOT EXISTS idx_operations_project_type_status
ON operations (project_id, type, status, created_at, operation_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_event_outbox_project_revision
ON event_outbox (project_id, project_revision)
WHERE project_id IS NOT NULL AND project_revision IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_event_outbox_operation_cursor
ON event_outbox (operation_id, CAST(event_id AS INTEGER))
WHERE operation_id IS NOT NULL AND project_revision IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_event_outbox_p10_cursor
ON event_outbox (CAST(event_id AS INTEGER))
WHERE project_revision IS NOT NULL;
`;

const SCHEMA_V3_SQL = `-- Repair synchronous idempotency operations written before their running
-- transition persisted started_at. The created timestamp is the exact start
-- boundary for those single-transaction operations.

UPDATE operations
SET started_at = created_at
WHERE started_at IS NULL
  AND status IN ('running', 'succeeded', 'failed', 'cancelled', 'interrupted');
`;
