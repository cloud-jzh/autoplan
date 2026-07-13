'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_PATH = 'docs/migration/p08/static-contract.json';
const FORMAT_VERSION = 1;
const CONTRACT_VERSION = 'p08-node-static-contract-v1';

const SOURCE_MARKERS = Object.freeze({
  'src/database.js': [
    'CREATE TABLE IF NOT EXISTS scripts',
    'CREATE TABLE IF NOT EXISTS executors',
    'CREATE TABLE IF NOT EXISTS chat_messages',
    'CREATE TABLE IF NOT EXISTS ai_configs',
    'CREATE TABLE IF NOT EXISTS claude_cli_configs',
    'CREATE TABLE IF NOT EXISTS conversations',
    "this.ensureColumn('conversations', 'codex_session_id'",
    "this.ensureColumn('project_states', 'env_vars'",
    "this.db.run('UPDATE ai_configs SET project_id = NULL WHERE project_id IS NOT NULL')",
    'migrateChatMessagesToConversation(defaultProjectId)',
  ],
  'src/main.js': [
    'const SCRIPT_COLUMN_LIST =',
    "ipcMain.handle('scripts:create'",
    "ipcMain.handle('executors:importTasksJson'",
    "ipcMain.handle('chat:history'",
    "ipcMain.handle('ai-config:create'",
    "ipcMain.handle('claude-cli-config:set-default'",
    "ipcMain.handle('conversation:list'",
    "ipcMain.handle('mcp:saveConfig'",
    'function readSavedMcpAuthToken()',
  ],
  'src/loop/snapshots.js': [
    "'SELECT * FROM scripts WHERE project_id = ? ORDER BY sort_order ASC, id ASC'",
    "'SELECT * FROM executors WHERE project_id = ? ORDER BY sort_order ASC, id ASC'",
    'function executorSnapshotRow(row = {}, runtime = null, projectId = null)',
    'function mcpStatusSnapshot(db, liveStatus = null)',
    'function planBackendSnapshotFields(row = {})',
    'authTokenMasked',
  ],
  'src/executors/executorConfig.js': [
    "const EXECUTOR_TYPES = new Set(['shell', 'process', 'plugin'])",
    "const PLUGIN_ACTION_TYPES = new Set(['command', 'input'])",
    "const DEPENDS_ORDERS = new Set(['parallel', 'sequence'])",
    'function validateExecutorConfig(input = {}, options = {})',
    'function normalizeTasksJson(input = {}, options = {})',
    'options.env 必须是键值对象',
  ],
  'src/executors/executorStore.js': [
    'const EXECUTOR_CONFIG_COLUMNS = [',
    'const LAST_LOG_MAX_CHARS = 24000',
    'SELECT * FROM executors WHERE project_id = ? ORDER BY sort_order ASC, id ASC',
    'function updateExecutorRunState(db, projectId, executorId, patch = {})',
    'function executorFromRow(row = {})',
  ],
  'src/chat/chatController.js': [
    'INSERT INTO chat_messages (project_id, conversation_id, role, content, tool_calls, tool_result, status, created_at)',
    'ORDER BY created_at ASC, id ASC',
    "CASE WHEN pinned_at IS NULL OR pinned_at = '' THEN 1 ELSE 0 END ASC",
    'updated_at DESC,',
    'id DESC',
    'function serializeConversation(row)',
    'function ensureDefaultConversation(db, projectId)',
  ],
  'src/chat/aiConfigService.js': [
    'function createAiConfig(db, input = {})',
    'UPDATE conversations SET ai_config_id = NULL, updated_at = ? WHERE ai_config_id = ?',
    'SELECT * FROM ai_configs WHERE project_id IS NULL ORDER BY id ASC',
    'function sanitizeAiConfig(row)',
    'function maskApiKey(value)',
  ],
  'src/chat/claudeCliConfigService.js': [
    "const GLOBAL_WHERE = 'project_id IS NULL'",
    'function setDefaultClaudeCliConfig(db, id)',
    'function resolveDefaultClaudeCliConfig(db)',
    'function sanitizeClaudeCliConfig(row)',
    'function maskAuthToken(value)',
  ],
  'src/mcpConfig.js': [
    'const MCP_DEFAULT_CONFIG = Object.freeze',
    "const MCP_AUTH_TOKEN_INPUT_KEYS = Object.freeze(['mcpAuthToken', 'mcp_auth_token', 'authToken'])",
    'const MCP_SAVE_FIELDS = Object.freeze',
    'function mcpServerConfig(db, env = process.env)',
    'function saveMcpSettings(db, input = {})',
    'function normalizeMcpAuthToken(value)',
  ],
  'src/renderer/types.ts': [
    'export interface Script {',
    'export interface Executor {',
    'export interface AppSnapshot {',
    'export interface McpConfigInput',
    'export interface AiConfig {',
    'export interface ClaudeCliConfig {',
    'export interface Conversation {',
    'export interface ChatMessage {',
  ],
});

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sortValue(value) {
  if (Array.isArray(value)) return value.map(sortValue);
  if (!value || typeof value !== 'object') return value;
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, sortValue(value[key])]));
}

function stableJson(value) {
  return `${JSON.stringify(sortValue(value), null, 2)}\n`;
}

function sourceEvidence(rootDir = ROOT) {
  return Object.entries(SOURCE_MARKERS).map(([relativePath, markers]) => {
    const absolutePath = path.join(rootDir, relativePath);
    if (!fs.existsSync(absolutePath)) throw new Error(`contract source missing: ${relativePath}`);
    const bytes = fs.readFileSync(absolutePath);
    const source = bytes.toString('utf8');
    const missing = markers.filter((marker) => !source.includes(marker));
    if (missing.length) throw new Error(`contract marker missing in ${relativePath}: ${missing.join(', ')}`);
    return {
      path: relativePath,
      sha256: sha256(bytes),
      markers: markers.map((marker) => ({
        marker,
        line: source.slice(0, source.indexOf(marker)).split(/\r?\n/).length,
      })),
    };
  });
}

function deriveContract(rootDir = ROOT) {
  return {
    schemaVersion: FORMAT_VERSION,
    version: CONTRACT_VERSION,
    status: 'frozen-node-static-contract',
    purpose: 'Freeze Node/sql.js static Automation, Chat, AI/Claude config, MCP settings and secret redaction behavior for P08 Go migration parity.',
    evidence: sourceEvidence(rootDir),
    prerequisites: {
      p07: 'P07 static/project/plan transport evidence must remain intact before P08 writers are enabled.',
      p06: 'P06 intake contract and golden artifacts are prerequisites for AppSnapshot compatibility.',
      databaseCopies: 'Node/sql.js may open only a generator-owned sanitized temporary database or caller-explicit sanitized copy. Go may open a separately reset copy only after Node closes.',
      runtimeDisabledForMigration: 'Scripts/executors run/stop/action, Chat send/stream/queue pump and MCP start/stop/listener are runtime capabilities and remain disabled for new Go static services.',
    },
    namingAndPresence: {
      storage: 'SQLite table and config keys are snake_case except settings keys such as mcp.authToken.',
      snapshot: 'scripts are returned as raw snake_case rows; executors add camelCase DTO fields plus selected snake_case aliases; AppSnapshot collections are always present.',
      time: 'created_at, updated_at, last_run_at and pinned_at are UTC ISO strings when present; null and empty string are distinct.',
      ordering: 'All frozen lists include deterministic secondary keys; golden comparison must reject unknown or omitted fields.',
    },
    database: databaseContract(),
    dto: dtoContract(),
    operations: operationContract(),
    secrets: secretContract(),
    golden: goldenContract(),
    risks: [
      { id: 'legacy-script-snapshot-raw-row', severity: 'retained', statement: 'scripts snapshot still exposes body, path, work_dir and last_log to the authorized renderer; P08 public summaries must redact them.' },
      { id: 'executor-options-env', severity: 'blocking', statement: 'executors.options.env and actions/command/args are secret-capable process inputs and must not appear in golden summaries, errors or logs.' },
      { id: 'chat-content-sensitive', severity: 'blocking', statement: 'chat_messages.content, tool_calls, tool_result and conversations.codex_session_id must not enter public P08 snapshot/golden summaries.' },
      { id: 'mcp-event-runtime-inference', severity: 'retained', statement: 'legacy snapshot can infer MCP running from latest events without live status; Go static snapshots must report unavailable/disabled rather than fabricate runtime.' },
    ],
  };
}

function databaseContract() {
  return {
    scripts: {
      order: ['sort_order ASC', 'id ASC'],
      projectScoped: true,
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'integer', null, true),
        field('name', 'string', null, false),
        field('path', 'string', '', false, 'path-sensitive'),
        field('runtime', 'enum:node|bash|ps|cmd', 'node', false),
        field('body', 'string', '', false, 'secret-capable-command'),
        field('description', 'string', '', false),
        field('trigger_mode', 'enum:hook|manual|schedule', 'manual', false),
        field('hook_stage', 'nullable enum:plan:after|task:after|validation:before|loop:end|on:fail', null, true),
        field('schedule_cron', 'nullable-string', null, true),
        field('enabled', 'integer-boolean', 1, false),
        field('work_dir', 'string', '', false, 'path-sensitive'),
        field('timeout_seconds', 'integer', 60, false),
        field('fail_aborts', 'integer-boolean', 0, false),
        field('context_inject', 'enum:env|stdin|none', 'none', false),
        field('sort_order', 'integer', 0, false),
        field('last_status', 'nullable-string', null, true),
        field('last_exit_code', 'nullable-integer', null, true),
        field('last_duration_ms', 'nullable-integer', null, true),
        field('last_log', 'nullable-string', null, true, 'process-output'),
        field('last_run_at', 'nullable-utc-string', null, true),
        field('created_at', 'utc-string', null, false),
        field('updated_at', 'utc-string', null, false),
        field('source_type', 'enum:inline|file', 'inline', false),
      ],
    },
    executors: {
      order: ['sort_order ASC', 'id ASC'],
      projectScoped: true,
      validation: {
        types: ['shell', 'process', 'plugin'],
        dependsOrder: ['parallel', 'sequence'],
        duplicateLabel: 'create/update reject a duplicate label in the same project; import may dedupe unless explicitly disabled',
        lastLogMaxChars: 24000,
      },
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'integer', null, false),
        field('label', 'string', '', false),
        field('type', 'enum:shell|process|plugin', 'shell', false),
        field('command', 'string', '', false, 'secret-capable-command'),
        field('args_json', 'json-array-string', '[]', false, 'secret-capable-command'),
        field('actions_json', 'nullable-json-object-string', null, true, 'secret-capable-command'),
        field('options_json', 'json-object-string', '{}', false, 'secret-capable-env'),
        field('group_kind', 'nullable-string', null, true),
        field('group_is_default', 'integer-boolean', 0, false),
        field('presentation_json', 'json-object-string', '{}', false),
        field('problem_matcher_json', 'nullable-json', null, true),
        field('depends_on_json', 'json-array-string', '[]', false),
        field('depends_order', 'enum:parallel|sequence', 'parallel', false),
        field('enabled', 'integer-boolean', 1, false),
        field('sort_order', 'integer', 0, false),
        field('last_status', 'nullable-string', null, true),
        field('last_exit_code', 'nullable-integer', null, true),
        field('last_duration_ms', 'nullable-integer', null, true),
        field('last_log', 'nullable-string', null, true, 'process-output'),
        field('last_run_at', 'nullable-utc-string', null, true),
        field('plugin_state_json', 'nullable-json-object-string', null, true, 'runtime-session'),
        field('created_at', 'utc-string', null, false),
        field('updated_at', 'utc-string', null, false),
      ],
    },
    chat_messages: {
      order: ['created_at ASC', 'id ASC'],
      projectScoped: true,
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'integer', null, true),
        field('role', 'enum:user|assistant|tool|system', null, false),
        field('content', 'string', null, false, 'user-content'),
        field('tool_calls', 'nullable-json-array-string', null, true, 'tool-data'),
        field('tool_result', 'nullable-json-object-string', null, true, 'tool-data'),
        field('status', 'nullable enum:streaming|done|aborted|error|queued', null, true),
        field('created_at', 'utc-string', null, false),
        field('conversation_id', 'nullable-integer', null, true),
      ],
    },
    ai_configs: {
      scope: 'global rows use project_id IS NULL after migration',
      order: ['id ASC'],
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'nullable-integer', null, true),
        field('name', 'string', null, false),
        field('provider', 'enum:openai|deepseek|anthropic|codex', 'openai', false),
        field('base_url', 'string', '', false, 'endpoint'),
        field('api_key', 'string', '', false, 'secret'),
        field('model', 'string', '', false),
        field('temperature', 'string', '0.3', false),
        field('thinking_depth', 'nullable enum:low|medium|high|xhigh', null, true),
        field('thinking_budget_tokens', 'nullable-integer', null, true),
        field('created_at', 'utc-string', null, false),
        field('updated_at', 'utc-string', null, false),
      ],
    },
    claude_cli_configs: {
      scope: 'global rows use project_id IS NULL',
      order: ['id ASC'],
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'nullable-integer', null, true),
        field('name', 'string', null, false),
        field('base_url', 'string', '', false, 'endpoint'),
        field('auth_token', 'string', '', false, 'secret'),
        field('model', 'string', '', false),
        field('is_default', 'integer-boolean', 0, false),
        field('created_at', 'utc-string', null, false),
        field('updated_at', 'utc-string', null, false),
      ],
    },
    conversations: {
      order: ['pinned first', 'updated_at DESC', 'id DESC'],
      projectScoped: true,
      columns: [
        field('id', 'integer', null, false),
        field('project_id', 'nullable-integer', null, true),
        field('title', 'string', '', false),
        field('ai_config_id', 'nullable-integer', null, true),
        field('pinned_at', 'nullable-utc-string', null, true),
        field('created_at', 'utc-string', null, false),
        field('updated_at', 'utc-string', null, false),
        field('codex_session_id', 'nullable-string', null, true, 'runtime-session'),
      ],
    },
    mcp_settings: {
      storage: 'settings table rows with mcp.* keys',
      envOverrides: ['AUTOPLAN_MCP_ENABLED', 'AUTOPLAN_MCP_TRANSPORT', 'AUTOPLAN_MCP_HOST', 'AUTOPLAN_MCP_PORT', 'AUTOPLAN_MCP_PATH', 'AUTOPLAN_MCP_AUTH_TOKEN'],
      keys: [
        field('mcp.enabled', 'string-boolean', 'true', false),
        field('mcp.transport', 'enum-string:http|stdio', 'http', false),
        field('mcp.host', 'string', '127.0.0.1', false, 'endpoint'),
        field('mcp.port', 'string-integer', '43847', false, 'endpoint'),
        field('mcp.path', 'string-path-prefix', '/mcp', false, 'endpoint'),
        field('mcp.authToken', 'string', 'generated', false, 'secret'),
        field('mcp.portExplicit', 'string-boolean', '', true),
      ],
    },
  };
}

function dtoContract() {
  return {
    scriptSnapshot: {
      source: 'SELECT * raw row',
      fields: 'all scripts database columns in snake_case only; renderer types tolerate camelCase aliases but Node snapshot does not synthesize them',
      redactionRule: 'P08 golden records field presence and safe scalar metadata only; path, body, work_dir and last_log values are placeholders.',
    },
    executorSnapshot: {
      source: 'executorFromRow(row) plus selected snake_case aliases and runtime overlay',
      requiredCamelFields: ['id', 'projectId', 'label', 'type', 'command', 'args', 'options', 'group', 'presentation', 'problemMatcher', 'dependsOn', 'dependsOrder', 'enabled', 'sortOrder', 'lastStatus', 'lastExitCode', 'lastDurationMs', 'lastLog', 'lastRunAt', 'createdAt', 'updatedAt', 'running', 'runStatus', 'activeOperation'],
      requiredSnakeAliases: ['project_id', 'sort_order', 'group_kind', 'group_is_default', 'depends_order', 'last_status', 'last_exit_code', 'last_duration_ms', 'last_log', 'last_run_at', 'created_at', 'updated_at'],
      redactionRule: 'command, args, options.env, options.cwd, actions, pluginState and logs are secret-capable and never copied verbatim into P08 public summaries.',
    },
    conversation: {
      fields: ['id', 'project_id', 'projectId', 'title', 'ai_config_id', 'aiConfigId', 'pinned_at', 'pinnedAt', 'pinned', 'created_at', 'createdAt', 'updated_at', 'updatedAt'],
      omitted: ['codex_session_id'],
      ordering: ['pinned=true first', 'updated_at DESC', 'id DESC'],
    },
    chatHistory: {
      rawFields: ['id', 'project_id', 'conversation_id', 'role', 'content', 'tool_calls', 'tool_result', 'status', 'created_at'],
      ordering: ['created_at ASC', 'id ASC'],
      redactionRule: 'Only role/status/field-presence/counts enter golden summaries. Content, tool_calls and tool_result do not.',
    },
    aiConfig: {
      sanitizedFields: ['id', 'projectId', 'name', 'provider', 'baseUrl', 'hasApiKey', 'maskedKey', 'model', 'temperature', 'thinkingDepth', 'thinkingBudgetTokens', 'createdAt', 'updatedAt'],
      forbiddenFields: ['api_key', 'apiKey', 'secret_ref'],
      mask: 'empty string when missing; otherwise hasApiKey=true and maskedKey contains only mask prefix plus at most legacy last four characters.',
    },
    claudeCliConfig: {
      sanitizedFields: ['id', 'projectId', 'name', 'baseUrl', 'hasAuthToken', 'maskedKey', 'model', 'isDefault', 'createdAt', 'updatedAt'],
      forbiddenFields: ['auth_token', 'authToken', 'secret_ref'],
      defaultRule: 'set-default clears all global rows then marks one default; deleting the default promotes the first remaining row by id.',
    },
    mcpSnapshot: {
      fields: ['enabled', 'running', 'status', 'transport', 'host', 'port', 'path', 'url', 'hasAuthToken', 'authTokenMasked', 'authHeader', 'localOnly', 'tools', 'toolDocs', 'connectionExample', 'note', 'lastEvent', 'lastError', 'startedAt'],
      forbiddenFields: ['authToken', 'secret_ref'],
      staticRule: 'When Go owns only static config it must not infer running from old events; report disabled/unavailable unless a live runtime provider proves otherwise.',
    },
    appSnapshotStaticBoundary: {
      includedNow: ['mcp', 'scripts', 'executors'],
      excludedNow: ['conversations', 'chat_messages', 'aiConfigs', 'claudeCliConfigs'],
      compatibilityRule: 'P08 may add static Go query surfaces for Chat/config, but current Node AppSnapshot parity must preserve the observed top-level keys and must not inject chat content or secret-bearing config rows into snapshot.',
    },
  };
}

function operationContract() {
  return {
    scripts: {
      createUpdateDeleteToggle: 'project scoped; create/update normalize names/enums/booleans/timeouts and return AppSnapshot(projectId)',
      schedule: 'schedule_cron is persisted only for trigger_mode=schedule and must parse successfully',
      runtimeDisabled: ['scripts:run', 'scripts:stop', 'hook execution', 'schedule execution'],
    },
    executors: {
      createUpdateDeleteToggle: 'project scoped; returns AppSnapshot through main IPC after ExecutorStore mutation',
      importTasksJson: 'legacy Node imports valid tasks serially; new Go static import must prevalidate and commit atomically',
      runtimeDisabled: ['executors:run', 'executors:stop', 'executors:runAction', 'plugin process actions'],
    },
    chat: {
      conversations: 'create/update/delete/list are static persistence; delete also deletes same-project chat_messages',
      history: 'history is read-only by conversation_id plus project_id, ordered oldest first',
      runtimeDisabled: ['chat:send', 'chat:stop', 'queue pump', 'SSE chunk/done/queue/config streams', 'title generation', 'LLM/tool execution'],
    },
    config: {
      aiConfigs: 'global project_id NULL configs; delete unlinks conversations.ai_config_id in one logical operation',
      claudeCliConfigs: 'global project_id NULL configs; default switch uses runBatch',
      mcpSettings: 'saveMcpSettings persists only explicitly supplied aliases; saving config currently schedules restart in Node, but Go static save must not start/stop listeners.',
    },
  };
}

function secretContract() {
  return {
    publicShape: 'Business DTOs and snapshots expose has_xxx/hasXxx plus masked_xxx/maskedKey only. They never return raw secret, secret ref, provider locator or storage path.',
    kinds: [
      secretKind('ai_config_api_key', 'global-config', ['ai_configs.api_key', 'settings:chat.apiKey'], ['hasApiKey', 'maskedKey']),
      secretKind('claude_cli_auth_token', 'global-config', ['claude_cli_configs.auth_token'], ['hasAuthToken', 'maskedKey']),
      secretKind('mcp_auth_token', 'install-settings', ['settings:mcp.authToken', 'AUTOPLAN_MCP_AUTH_TOKEN'], ['hasAuthToken', 'authTokenMasked']),
      secretKind('plan_generation_claude_auth_token', 'project/intake/plan snapshot', [
        'project_states.plan_generation_claude_auth_token',
        'requirements.plan_generation_claude_auth_token',
        'feedback.plan_generation_claude_auth_token',
        'plans.plan_generation_claude_auth_token',
      ], ['plan_generation_has_claude_auth_token', 'plan_generation_claude_auth_token masked']),
      secretKind('plan_execution_claude_auth_token', 'project/plan snapshot', [
        'project_states.plan_execution_claude_auth_token',
        'plans.plan_execution_claude_auth_token',
      ], ['plan_execution_has_claude_auth_token', 'plan_execution_claude_auth_token masked']),
      secretKind('env_vars', 'project/executor process input', ['project_states.env_vars', 'executors.options_json.env'], ['presence/count only']),
    ],
    replacement: 'Replacing a secret creates or updates the secret first, then commits the business row to reference/presence metadata. Clearing deletes only after the business row no longer points at it.',
    emptyValues: 'Empty string, null and omitted input are distinct: empty clears where the legacy API does so; omitted preserves existing values on update.',
    fixturePolicy: 'Fixture inputs may use inert placeholders, but golden artifacts store only redacted placeholders, booleans, counts and hashes.',
  };
}

function goldenContract() {
  return {
    generator: 'scripts/migration-p08/generate-node-golden.js',
    artifact: 'fixtures/migration/p08/node-static.golden.json',
    manifest: 'fixtures/migration/p08/manifest.json',
    scenarios: ['automation-static', 'chat-static', 'config-static', 'mcp-static', 'snapshot-static', 'secret-redaction'],
    safety: [
      'Use only a generator-owned OS temporary database and workspace.',
      'Reject unapproved output paths unless explicitly allowed by a caller test harness.',
      'Record input recipe hash, schema summary hash, row counts, physical database before/after hashes and sql.js handoff state.',
      'Close sql.js before artifacts are committed.',
      'Fail closed on real user profile paths, Electron userData paths, credential-shaped strings or raw fixture secret values.',
    ],
    comparison: 'Strict deep comparison of field presence, scalar types, null-vs-empty, ordering, redacted summaries and hashes; no loose subset matching.',
  };
}

function field(name, type, defaultValue, nullable, sensitivity = 'public') {
  return { name, type, default: defaultValue, nullable, sensitivity };
}

function secretKind(kind, ownerScope, legacySources, publicIndicators) {
  return { kind, ownerScope, legacySources, publicIndicators, rawReadBackAllowed: false };
}

function buildContract(rootDir = ROOT) {
  const contractPath = path.join(rootDir, OUTPUT_PATH);
  if (!fs.existsSync(contractPath) || !fs.statSync(contractPath).isFile()) {
    throw new Error(`frozen contract missing: ${OUTPUT_PATH}`);
  }
  const committed = JSON.parse(fs.readFileSync(contractPath, 'utf8'));
  if (committed.schemaVersion !== FORMAT_VERSION || committed.version !== CONTRACT_VERSION) {
    throw new Error('frozen contract version mismatch');
  }
  const current = deriveContract(rootDir);
  if (stableJson(committed) !== stableJson(current)) {
    throw new Error('frozen contract drift');
  }
  return committed;
}

function writeContract(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputPath = path.resolve(rootDir, options.outputPath || OUTPUT_PATH);
  const expected = path.resolve(rootDir, OUTPUT_PATH);
  if (outputPath !== expected && !options.allowExternalOutput) throw new Error('write contract output must remain in docs/migration/p08');
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  const contract = deriveContract(rootDir);
  fs.writeFileSync(outputPath, stableJson(contract), 'utf8');
  return outputPath;
}

function parseArgs(argv) {
  const options = { write: false };
  for (const arg of argv) {
    if (arg === '--write') options.write = true;
    else throw new Error(`unknown argument: ${arg}`);
  }
  return options;
}

if (require.main === module) {
  try {
    const options = parseArgs(process.argv.slice(2));
    if (options.write) {
      const outputPath = writeContract();
      const contract = JSON.parse(fs.readFileSync(outputPath, 'utf8'));
      process.stdout.write(`${JSON.stringify({ ok: true, output: path.relative(ROOT, outputPath), sha256: sha256(stableJson(contract)) })}\n`);
    } else {
      process.stdout.write(stableJson(buildContract(ROOT)));
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  CONTRACT_VERSION,
  FORMAT_VERSION,
  OUTPUT_PATH,
  SOURCE_MARKERS,
  buildContract,
  deriveContract,
  parseArgs,
  sha256,
  stableJson,
  writeContract,
};
