-- AutoPlan schema v1. This file is append-only migration history.
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

-- These ALTER statements deliberately run for both an empty database and a
-- normalized Node database. Their transaction also contains the ledger row;
-- a partially upgraded database with an unexplained version column fails
-- closed instead of being treated as migrated.
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
