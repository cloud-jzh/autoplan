'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const baselineInventory = require('../migration-baseline/inventory-dependencies');

const DATABASE_PATH = 'src/database.js';
const BASELINE_PATH = 'docs/migration/p00/database-and-dependencies.json';
const INVENTORY_PATH = 'docs/migration/p04/schema-inventory.json';
const FORMAT_VERSION = 1;

const TABLE_POLICIES = {
  ai_configs: {
    history: ['project/thinking fields may be ensureColumn additions; project rows are globalized and exact duplicate identities are collapsed'],
    backfillPrerequisites: ['remap conversations to the smallest equivalent config id before duplicate deletion'],
    invariants: ['effective configs are global after migration', 'api_key is secret material'],
    relations: ['conversations.ai_config_id -> ai_configs.id is optional'],
  },
  attachments: {
    history: ['project_id is an ensureColumn addition for legacy polymorphic owner rows'],
    backfillPrerequisites: ['assign NULL project_id only after the default project exists'],
    invariants: ['owner_type/owner_id is polymorphic', 'stored_path is audited independently from row ownership'],
    relations: ['project_id -> projects.id', 'owner_type/owner_id -> requirements or feedback is audit/application constrained'],
  },
  chat_messages: {
    history: ['project_id and conversation_id are compatibility additions'],
    backfillPrerequisites: ['repair conversation ownership and create/reuse a per-project default conversation for unscoped messages'],
    invariants: ['message and conversation project ids agree', 'content/tool fields are sensitive'],
    relations: ['project_id -> projects.id', 'conversation_id -> conversations.id'],
  },
  claude_cli_configs: {
    history: ['nullable project_id retains global/legacy scope compatibility'],
    backfillPrerequisites: ['audit default uniqueness and historical project scope before strengthening constraints'],
    invariants: ['auth_token is secret', 'default uniqueness is application enforced in Node'],
    relations: ['project_id is nullable legacy/global scope metadata'],
  },
  conversations: {
    history: ['project_id, ai_config_id, pinned_at and codex_session_id are compatibility additions'],
    backfillPrerequisites: ['default project exists and AI duplicate ids are remapped'],
    invariants: ['conversation owns same-project messages', 'codex_session_id is not authorization'],
    relations: ['project_id -> projects.id', 'ai_config_id -> ai_configs.id'],
  },
  events: {
    history: ['project_id is added and legacy NULL values are assigned to the default project'],
    backfillPrerequisites: ['default project exists'],
    invariants: ['message/meta must not leak secrets into infrastructure output'],
    relations: ['project_id -> projects.id'],
  },
  executors: {
    history: ['fresh CREATE requires project/label/command/timestamps while legacy ensureColumn supplies 1 or empty compatibility defaults'],
    backfillPrerequisites: ['default project 1, JSON dependencies and required empty values are audited'],
    invariants: ['dependency labels are JSON application keys', 'command/options/actions/log fields are sensitive'],
    relations: ['project_id -> projects.id', 'depends_on_json is application constrained'],
  },
  feedback: {
    history: ['project/link/config/session/failure fields are additions; linked_plan_id predates normalized links'],
    backfillPrerequisites: ['project assignment precedes same-project linked plan backfill'],
    invariants: ['requirement/project ownership agrees', 'generation credentials/logs are sensitive'],
    relations: ['project_id -> projects.id', 'requirement_id -> requirements.id', 'linked_plan_id is legacy audited linkage'],
  },
  intake_plan_links: {
    history: ['table is created after initial DDL; valid same-project legacy links become phase 1 links'],
    backfillPrerequisites: ['project/intake/plan exist and uniqueness conflicts are reported'],
    invariants: ['phase index and plan tuple uniqueness are per project/type/intake', 'intake_id is polymorphic'],
    relations: ['project_id -> projects.id', 'plan_id -> plans.id', 'intake_type/intake_id is audit/application constrained'],
  },
  loop_state: {
    history: ['retained singleton compatibility table with id fixed to 1'],
    backfillPrerequisites: ['may seed only the first project and missing default project state'],
    invariants: ['cannot represent or overwrite authoritative multi-project state'],
    relations: ['legacy seed only; no durable project foreign key'],
  },
  plan_tasks: {
    history: ['scope/timing/session/acceptance fields are compatibility additions'],
    backfillPrerequisites: ['orphan plans and duplicate plan/task keys fail audit'],
    invariants: ['task_key is unique within a plan', 'status and sort_order drive deterministic aggregation'],
    relations: ['plan_id -> plans.id'],
  },
  plans: {
    history: ['project/sort/backend/session fields are additions; config ids are ensureColumn-only; invalid sort orders are allocated'],
    backfillPrerequisites: ['project assignment precedes per-project created_at/id ordering'],
    invariants: ['positive order is preserved', 'aggregates match tasks', 'path/command/token fields are sensitive'],
    relations: ['project_id -> projects.id', 'tasks and legacy/normalized intake links reference plans'],
  },
  project_states: {
    history: ['backend config ids, prompt and env_vars are additions; default state is seeded from loop_state'],
    backfillPrerequisites: ['default project exists; INSERT OR IGNORE preserves existing state'],
    invariants: ['one row per project', 'env_vars and Claude tokens are secret material'],
    relations: ['project_id -> projects.id is one-to-one'],
  },
  projects: {
    history: ['empty legacy databases receive one project derived from loop_state'],
    backfillPrerequisites: ['loop_state is readable when projects is empty'],
    invariants: ['workspace_path requires path audit', 'project is the ownership root'],
    relations: ['logical parent of project_id columns'],
  },
  requirements: {
    history: ['project/link/config/acceptance/failure fields are additions; linked_plan_id predates normalized links'],
    backfillPrerequisites: ['project assignment precedes valid same-project link backfill'],
    invariants: ['source_path requires path audit', 'body/credentials/errors/log references are sensitive'],
    relations: ['project_id -> projects.id', 'linked_plan_id is legacy audited linkage'],
  },
  scan_files: {
    history: ['legacy key omitted project_id; rename/create/copy/drop adds project ownership using the selected fallback id'],
    backfillPrerequisites: ['default project exists and legacy table is completely readable'],
    invariants: ['key is project_id/scan_type/file_path', 'path is audited', 'partial rebuild is never accepted'],
    relations: ['project_id -> projects.id'],
  },
  scripts: {
    history: ['source_type and schedule_cron are compatibility additions'],
    backfillPrerequisites: ['project and path ownership pass audit'],
    invariants: ['path/work_dir require path audit', 'body/last_log are sensitive process content'],
    relations: ['nullable project_id -> projects.id'],
  },
  settings: {
    history: ['defaults are inserted by key without overwriting existing values'],
    backfillPrerequisites: ['settings exists before legacy chat config migration'],
    invariants: ['values are strings', 'credential and local-path keys have sensitive policy'],
    relations: ['no foreign keys'],
  },
};

const COMPATIBILITY_MIGRATIONS = [
  ['ensure-columns', ["this.ensureColumn('requirements', 'project_id'", "this.ensureColumn('conversations', 'codex_session_id'"], 'all initial tables exist', 'append every missing compatibility column in source order without rewriting existing definitions', 'ALTER defaults apply to existing legacy rows where declared', 'fresh and upgraded physical defaults can differ'],
  ['intake-link-schema', ['ensureIntakePlanLinksTable()', 'CREATE TABLE IF NOT EXISTS intake_plan_links', 'CREATE UNIQUE INDEX IF NOT EXISTS idx_intake_plan_links_intake_phase'], 'plans and intake tables exist', 'create normalized polymorphic link table and three indexes', 'no rows until backfill', 'no foreign key prevents historical orphans'],
  ['default-project', ['ensureDefaultProject()', 'SELECT * FROM loop_state WHERE id = 1', 'INSERT INTO projects'], 'projects is empty and loop_state is readable', 'create the first project from legacy singleton state', 'adds exactly one project only when none exists', 'workspace_path is sensitive'],
  ['default-settings', ['ensureDefaultSettings()', 'const defaults = {', 'INSERT OR IGNORE INTO settings'], 'settings exists', 'insert frozen keys without overwriting values', 'may generate an MCP token and default chat/update/terminal values', 'raw setting values never enter reports'],
  ['scan-files-rebuild', ['migrateScanFilesTable(defaultProjectId)', 'ALTER TABLE scan_files RENAME TO scan_files_legacy', 'CREATE TABLE scan_files', 'INSERT OR REPLACE INTO scan_files', 'DROP TABLE scan_files_legacy'], 'default project exists and final composite key is absent', 'rename, recreate, coalesce/copy and drop legacy scan table', 'adds project ownership and may replace duplicate final keys', 'interruption can leave scan_files_legacy'],
  ['assign-legacy-project', ['assignLegacyRows(defaultProjectId)', "for (const table of ['requirements', 'feedback', 'attachments', 'plans', 'events', 'scan_files'])", 'UPDATE ${table} SET project_id = ? WHERE project_id IS NULL'], 'default project and final scan table exist', 'assign NULL project ownership across the frozen table set', 'updates only NULL project_id rows', 'non-NULL orphan/cross-project ids remain for audit'],
  ['intake-link-backfill', ['backfillIntakePlanLinks()', 'INSERT OR IGNORE INTO intake_plan_links', 'JOIN plans', 'CAST(${table}.linked_plan_id AS INTEGER) > 0'], 'project assignment and normalized schema completed', 'add phase 1 only for positive existing same-project plans', 'invalid linked_plan_id remains untouched', 'ignored uniqueness collisions require audit'],
  ['plan-sort-backfill', ['backfillPlanSortOrders()', 'ORDER BY COALESCE(project_id, 0) ASC, created_at ASC, id ASC', 'UPDATE plans SET sort_order = ? WHERE id = ?'], 'project assignment completed', 'preserve positive order and allocate increasing values per project', 'changes zero/non-finite values only', 'duplicate positive order remains possible'],
  ['default-project-state', ['ensureProjectState(defaultProjectId)', 'INSERT OR IGNORE INTO project_states'], 'default project and loop_state exist', 'seed a missing state from singleton values', 'existing state is preserved', 'loop_state can later diverge'],
  ['ai-global-dedupe', ['migrateAiConfigsToGlobal()', 'UPDATE ai_configs SET project_id = NULL', 'groupRows.sort((a, b) => Number(a.id) - Number(b.id))', 'UPDATE conversations', 'DELETE FROM ai_configs WHERE id IN'], 'AI compatibility columns and conversations exist', 'globalize, retain minimum identical id, remap then delete duplicates', 'project ids are nulled and exact duplicates deleted', 'destructive dedupe requires identity/reference audit'],
  ['legacy-chat-config', ['migrateChatToAiConfigs(defaultProjectId)', 'SELECT id FROM ai_configs WHERE project_id IS NULL LIMIT 1', "this.getSetting('chat.apiKey')", 'INSERT INTO ai_configs'], 'global dedupe completed and settings readable', 'create one global default only when none exists', 'legacy credential is read; schema v1 routes raw secret to secret_refs', 'credentials never enter output'],
  ['chat-conversation-ownership', ['migrateChatMessagesToConversation(defaultProjectId)', 'UPDATE conversations SET project_id', 'UPDATE chat_messages', 'SELECT id FROM conversations WHERE project_id = ? AND title = ?', 'UPDATE chat_messages SET conversation_id'], 'default project exists and config remap completed', 'align ownership and attach unscoped messages to default conversations', 'may create one default conversation per project', 'missing conversation ids remain blocking orphans'],
].map(([id, markers, precondition, behavior, dataEffect, failureRisk]) => ({
  id, markers, preconditions: [precondition], behavior, dataEffect, failureRisk, source: null,
}));

const FOREIGN_KEY_DECISIONS = [
  ['project_states.project_id -> projects.id', 'establish-v1', 'CASCADE', 'owned one-to-one state', 'no orphan state'],
  ['requirements.project_id; feedback.project_id; plans.project_id -> projects.id', 'establish-v1', 'RESTRICT', 'intake/plan deletion coordinates external data', 'NULL assignment and orphan audit pass'],
  ['feedback.requirement_id -> requirements.id', 'establish-v1', 'RESTRICT', 'optional non-polymorphic parent', 'orphan/cross-project feedback fails audit'],
  ['attachments.project_id -> projects.id', 'establish-v1', 'RESTRICT', 'file deletion is external to SQLite', 'owner/project/path audit passes'],
  ['attachments.owner_type/owner_id', 'defer-audit-application', 'NONE', 'polymorphic owner has no single lossless foreign key', 'type/existence/project are audited'],
  ['scan_files.project_id; events.project_id -> projects.id', 'establish-v1', 'CASCADE', 'derived/project-owned rows', 'rebuild/assignment and audit pass'],
  ['plan_tasks.plan_id -> plans.id', 'establish-v1', 'CASCADE', 'task is owned by one plan', 'orphan and duplicate-key audit passes'],
  ['scripts.project_id; executors.project_id -> projects.id', 'establish-v1', 'RESTRICT', 'external commands/processes prevent silent cascade', 'project/path/process audit passes'],
  ['chat_messages.project_id; conversations.project_id -> projects.id', 'establish-v1', 'CASCADE', 'chat rows are project-owned', 'ownership/cross-project audit passes'],
  ['chat_messages.conversation_id -> conversations.id', 'establish-v1', 'CASCADE', 'message belongs to one conversation', 'unscoped migration and orphan audit pass'],
  ['conversations.ai_config_id -> ai_configs.id', 'establish-v1', 'SET NULL', 'config is optional after dedupe', 'remap completes and missing ids fail audit'],
  ['intake_plan_links.project_id -> projects.id', 'establish-v1', 'RESTRICT', 'cross-project link must fail closed', 'aggregate audit passes'],
  ['intake_plan_links.plan_id -> plans.id', 'establish-v1', 'CASCADE', 'normalized link follows plan lifecycle', 'orphan/cross-project audit passes'],
  ['intake_plan_links.intake_type/intake_id', 'defer-audit-application', 'NONE', 'polymorphic intake has no single lossless foreign key', 'type/existence/project/phase are audited'],
  ['requirements.linked_plan_id; feedback.linked_plan_id', 'defer-legacy-audit', 'NONE', 'invalid denormalized history is preserved while normalized links become canonical', 'invalid/cross-project values are reported, never cleared'],
  ['ai_configs.project_id; claude_cli_configs.project_id', 'defer-global-scope', 'NONE', 'NULL denotes current global scope', 'historical scope/default conflicts are audited'],
  ['loop_state', 'retain-without-foreign-key', 'NONE', 'legacy seed is not authoritative project state', 'schema v1 services ignore it as multi-project state'],
].map(([relation, decision, onDelete, rationale, precondition]) => ({ relation, decision, onDelete, rationale, precondition }));

const SENSITIVE_DATA = [
  {
    id: 'secret-credentials', classification: 'secret',
    locations: ['ai_configs.api_key', 'claude_cli_configs.auth_token', 'settings:chat.apiKey', 'settings:mcp.authToken', 'requirements.plan_generation_claude_auth_token', 'feedback.plan_generation_claude_auth_token', 'plans.plan_generation_claude_auth_token', 'plans.plan_execution_claude_auth_token', 'project_states.plan_generation_claude_auth_token', 'project_states.plan_execution_claude_auth_token'],
    migrationPolicy: 'move raw values to secret_refs; business rows retain only redacted reference/presence', apiPolicy: 'never return raw values', logPolicy: 'never log values/SQL parameters/headers/environments', eventPolicy: 'never emit raw values', fixturePolicy: 'non-working placeholders only',
  },
  {
    id: 'session-identifiers', classification: 'sensitive session/process identifier',
    locations: ['feedback.agent_cli_session_id', 'plans.agent_cli_session_id', 'plan_tasks.agent_cli_session_id', 'plan_tasks.codex_session_id', 'conversations.codex_session_id', 'executors.plugin_state_json'],
    migrationPolicy: 'retain scoped compatibility identifiers; never use as authorization', apiPolicy: 'existing scoped contracts only', logPolicy: 'omit or shorten', eventPolicy: 'existing scoped non-reusable fields only', fixturePolicy: 'deterministic fake ids only',
  },
  {
    id: 'environment-and-commands', classification: 'secret-capable process input',
    locations: ['project_states.env_vars', 'project_states.validation_command', 'project_states.agent_cli_command', 'project_states.plan_generation_command', 'project_states.plan_execution_command', 'requirements.agent_cli_command', 'requirements.plan_generation_command', 'feedback.agent_cli_command', 'feedback.plan_generation_command', 'plans.agent_cli_command', 'plans.plan_generation_command', 'plans.plan_execution_command', 'executors.command', 'executors.args_json', 'executors.options_json', 'executors.actions_json', 'scripts.body'],
    migrationPolicy: 'preserve unknown compatibility content; isolate structurally identifiable secrets without parse-and-drop', apiPolicy: 'authorized execution paths only', logPolicy: 'redact arguments/environment/embedded credentials', eventPolicy: 'never emit environment or command bodies', fixturePolicy: 'synthetic inert commands and fake variables',
  },
  {
    id: 'paths', classification: 'local filesystem path',
    locations: ['projects.workspace_path', 'requirements.source_path', 'attachments.stored_path', 'plans.file_path', 'scripts.path', 'scripts.work_dir', 'scan_files.file_path', 'requirements.last_generate_log_file', 'feedback.last_generate_log_file', 'settings:update.localInstallerPath'],
    migrationPolicy: 'preserve values but require read-only path audit; evidence stores field names/placeholders only', apiPolicy: 'authorized project/local session after allowed-root checks', logPolicy: 'redacted id or authorized-root-relative only', eventPolicy: 'never emit unauthorized absolute paths', fixturePolicy: 'reserved fake or generator-owned temporary roots only',
  },
  {
    id: 'content-and-logs', classification: 'user content and process output',
    locations: ['requirements.body', 'feedback.body', 'chat_messages.content', 'chat_messages.tool_calls', 'chat_messages.tool_result', 'events.message', 'events.meta', 'scripts.last_log', 'executors.last_log', 'requirements.last_generate_error', 'feedback.last_generate_error', 'project_states.last_error', 'plan_tasks.raw_line'],
    migrationPolicy: 'preserve rows without copying content into reports/fixtures', apiPolicy: 'project/session scoped and bounded', logPolicy: 'do not duplicate; redact credential fragments', eventPolicy: 'contract-required bounded content only', fixturePolicy: 'synthetic text only',
  },
  {
    id: 'network-endpoints', classification: 'sensitive endpoint metadata',
    locations: ['ai_configs.base_url', 'claude_cli_configs.base_url', 'requirements.plan_generation_claude_base_url', 'feedback.plan_generation_claude_base_url', 'plans.plan_generation_claude_base_url', 'plans.plan_execution_claude_base_url', 'project_states.plan_generation_claude_base_url', 'project_states.plan_execution_claude_base_url', 'settings:mcp.host', 'settings:mcp.port', 'settings:mcp.path', 'settings:update.installerAssetDownloadUrl'],
    migrationPolicy: 'preserve validated endpoints separately from credentials', apiPolicy: 'authorized configuration contracts only', logPolicy: 'omit query credentials/auth metadata', eventPolicy: 'never emit auth headers or token-bearing URLs', fixturePolicy: 'reserved invalid endpoints only',
  },
];

const POLICY_TEMPLATE = {
  description: 'Frozen evidence of effective Node/sql.js schema and compatibility semantics; contains no database row values.',
  tables: Object.entries(TABLE_POLICIES).map(([name, policy]) => ({ name, ...policy })),
  defaultData: [
    { target: 'loop_state:1', behavior: 'INSERT OR IGNORE idle singleton with SQLite current time', sensitive: false },
    { target: 'projects:first and project_states:default', behavior: 'seed only when missing from loop_state without emitting values', sensitive: true },
    { target: 'settings', keys: ['chat.apiKey', 'chat.baseUrl', 'chat.model', 'chat.provider', 'chat.temperature', 'mcp.authToken', 'mcp.enabled', 'mcp.host', 'mcp.path', 'mcp.port', 'mcp.transport', 'terminal.confirmBeforeKill', 'terminal.defaultProfile', 'terminal.fontSize', 'terminal.initialCwd', 'terminal.retainOnExit', 'terminal.scrollbackLimit', 'update.autoCheck', 'update.dismissedVersion', 'update.downloadAssetKey', 'update.downloadBytesReceived', 'update.downloadCompletedAt', 'update.downloadError', 'update.downloadPhase', 'update.downloadProgress', 'update.downloadReason', 'update.downloadStartedAt', 'update.downloadTotalBytes', 'update.downloadVersion', 'update.installerAssetArch', 'update.installerAssetAvailable', 'update.installerAssetDownloadUrl', 'update.installerAssetKind', 'update.installerAssetName', 'update.installerAssetPlatform', 'update.installerAssetReason', 'update.installerAssetSize', 'update.installerAssetStatus', 'update.intervalMinutes', 'update.lastCheckedAt', 'update.localInstallerPath'], behavior: 'INSERT OR IGNORE; generated MCP token migrates to secret_refs without inventorying its value', sensitive: true },
    { target: 'ai_configs:default', behavior: 'create one global config only when none exists, sourcing legacy chat settings', sensitive: true },
    { target: 'conversations:default', behavior: 'create/reuse per project only for unscoped legacy messages', sensitive: false },
  ],
  compatibilityMigrations: COMPATIBILITY_MIGRATIONS,
  foreignKeyDecisions: FOREIGN_KEY_DECISIONS,
  sensitiveData: SENSITIVE_DATA,
  risks: [
    { id: 'no-version-ledger', severity: 'blocking', statement: 'Node best-effort compatibility statements have no schema_migrations/user_version.' },
    { id: 'historical-orphans', severity: 'blocking', statement: 'No current SQLite foreign keys; unexplained orphan/cross-project data is rejected, never repaired silently.' },
    { id: 'polymorphic-relations', severity: 'retained', statement: 'Attachment owner and intake id remain audit/application constrained.' },
    { id: 'scan-rebuild-interruption', severity: 'blocking', statement: 'Rename/create/copy/drop can leave an ambiguous legacy table.' },
    { id: 'ai-dedupe-data-loss', severity: 'blocking', statement: 'Global dedupe requires identity/reference proof before deletion.' },
    { id: 'physical-default-provenance', severity: 'retained', statement: 'Fresh and upgraded executor/scan physical defaults differ.' },
    { id: 'external-files', severity: 'blocking', statement: 'Paths refer outside SQLite; audit is read-only and never repairs/deletes files.' },
    { id: 'sensitive-legacy-columns', severity: 'blocking', statement: 'Credentials/env/commands/logs/content never enter reports, APIs, events or copied fixtures.' },
    { id: 'loop-state-divergence', severity: 'retained', statement: 'Legacy singleton is not authoritative multi-project state.' },
  ],
};

function normalizeSql(value) {
  return String(value || '').replace(/\s+/g, ' ').trim();
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function methodAt(source, offset) {
  let method = 'module';
  for (const match of source.slice(0, offset).matchAll(/^  ([A-Za-z][A-Za-z0-9_]*)\([^)]*\) \{/gm)) method = match[1];
  return method;
}

function sourceAnchor(source, offset, kind, name) {
  return `${DATABASE_PATH}#${methodAt(source, offset)}:${kind}:${name}`;
}

function balancedEnd(source, openIndex) {
  let depth = 0;
  let quote = null;
  for (let index = openIndex; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (char === quote && source[index - 1] !== '\\') quote = null;
      continue;
    }
    if (char === "'" || char === '"' || char === '`') quote = char;
    else if (char === '(') depth += 1;
    else if (char === ')') {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function splitSqlList(source) {
  const result = [];
  let start = 0;
  let depth = 0;
  let quote = null;
  for (let index = 0; index < source.length; index += 1) {
    const char = source[index];
    if (quote) {
      if (char === quote && source[index - 1] !== '\\') quote = null;
      continue;
    }
    if (char === "'" || char === '"') quote = char;
    else if (char === '(') depth += 1;
    else if (char === ')') depth -= 1;
    else if (char === ',' && depth === 0) {
      result.push({ text: source.slice(start, index), offset: start });
      start = index + 1;
    }
  }
  if (source.slice(start).trim()) result.push({ text: source.slice(start), offset: start });
  return result;
}

function parseDefault(definition) {
  const match = /\bDEFAULT\s+((?:'[^']*(?:''[^']*)*')|(?:"[^"]*")|(?:\([^)]*\))|[^\s,]+)/i.exec(definition);
  return match ? match[1] : null;
}

function parseColumn(name, definition, position, declarations) {
  const type = /^([A-Z][A-Z0-9_]*)\b/i.exec(definition)?.[1]?.toUpperCase() || '';
  return {
    position,
    name,
    type,
    nullable: !/\bNOT\s+NULL\b/i.test(definition) && !/\bPRIMARY\s+KEY\b/i.test(definition),
    defaultSql: parseDefault(definition),
    primaryKeyPosition: /\bPRIMARY\s+KEY\b/i.test(definition) ? 1 : null,
    declarations,
  };
}

function parseNameList(value) {
  return splitSqlList(value).map(({ text }) => normalizeSql(text)
    .replace(/^(?:["`\[])(.*?)(?:["`\]])$/, '$1')
    .replace(/\s+(?:ASC|DESC)$/i, '')
    .toLowerCase());
}

function extractCreateEvidence(source) {
  const tables = new Map();
  const pattern = /CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([a-z_][a-z0-9_]*)\s*\(/gi;
  for (const match of source.matchAll(pattern)) {
    const name = match[1].toLowerCase();
    const open = source.indexOf('(', match.index);
    const close = balancedEnd(source, open);
    if (close < 0) throw new Error(`CREATE TABLE 未闭合：${name}`);
    const entry = tables.get(name) || {
      name,
      createLines: [],
      columns: new Map(),
      primaryKey: [],
      uniqueKeys: [],
    };
    entry.createLines.push(sourceAnchor(source, match.index, 'create-table', name));
    for (const part of splitSqlList(source.slice(open + 1, close))) {
      const definition = normalizeSql(part.text);
      const primary = /^PRIMARY\s+KEY\s*\((.*)\)$/i.exec(definition);
      const unique = /^UNIQUE\s*\((.*)\)$/i.exec(definition);
      if (primary) {
        entry.primaryKey = parseNameList(primary[1]);
        continue;
      }
      if (unique) {
        entry.uniqueKeys.push(parseNameList(unique[1]));
        continue;
      }
      if (/^(?:CHECK|FOREIGN|CONSTRAINT)\b/i.test(definition)) continue;
      const column = /^([a-z_][a-z0-9_]*)\s+(.+)$/i.exec(definition);
      if (!column) continue;
      const columnName = column[1].toLowerCase();
      const columnDefinition = normalizeSql(column[2]);
      const declaration = {
        kind: 'create-table',
        source: sourceAnchor(source, open + 1 + part.offset, 'column', `${name}.${columnName}`),
        definition: columnDefinition,
      };
      if (!entry.columns.has(columnName)) {
        entry.columns.set(columnName, { definition: columnDefinition, declarations: [declaration] });
      } else {
        entry.columns.get(columnName).declarations.push(declaration);
      }
      if (/\bPRIMARY\s+KEY\b/i.test(columnDefinition) && !entry.primaryKey.length) entry.primaryKey = [columnName];
      if (/\bUNIQUE\b/i.test(columnDefinition)) entry.uniqueKeys.push([columnName]);
    }
    tables.set(name, entry);
  }
  return tables;
}

function extractEnsureEvidence(source) {
  const result = new Map();
  const pattern = /this\.ensureColumn\(\s*'([^']+)'\s*,\s*'([^']+)'\s*,\s*(?:'([^']*)'|"([^"]*)")\s*\)/g;
  for (const match of source.matchAll(pattern)) {
    const table = match[1].toLowerCase();
    const entries = result.get(table) || [];
    entries.push({
      column: match[2].toLowerCase(),
      definition: normalizeSql(match[3] ?? match[4]),
      source: sourceAnchor(source, match.index, 'ensure-column', `${table}.${match[2].toLowerCase()}`),
    });
    result.set(table, entries);
  }
  return result;
}

function extractIndexes(source) {
  const indexes = [];
  const pattern = /CREATE\s+(UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+([a-z_][a-z0-9_]*)\s+ON\s+([a-z_][a-z0-9_]*)\s*\(([^;]+?)\)/gi;
  for (const match of source.matchAll(pattern)) {
    indexes.push({
      name: match[2].toLowerCase(),
      table: match[3].toLowerCase(),
      unique: Boolean(match[1]),
      columns: splitSqlList(match[4]).map(({ text }) => {
        const normalized = normalizeSql(text);
        return {
          name: normalized.replace(/\s+(?:ASC|DESC)$/i, '').toLowerCase(),
          order: /\s+DESC$/i.test(normalized) ? 'DESC' : 'ASC',
        };
      }),
      source: sourceAnchor(source, match.index, 'index', match[2].toLowerCase()),
    });
  }
  return indexes.sort((left, right) => left.name.localeCompare(right.name));
}

function collectDatabaseEvidence(source) {
  const creates = extractCreateEvidence(source);
  const ensures = extractEnsureEvidence(source);
  for (const [table, entries] of ensures) {
    if (!creates.has(table)) {
      creates.set(table, { name: table, createLines: [], columns: new Map(), primaryKey: [], uniqueKeys: [] });
    }
    const target = creates.get(table);
    for (const item of entries) {
      const declaration = { kind: 'ensure-column', source: item.source, definition: item.definition };
      if (target.columns.has(item.column)) target.columns.get(item.column).declarations.push(declaration);
      else target.columns.set(item.column, { definition: item.definition, declarations: [declaration] });
    }
  }

  const indexes = extractIndexes(source);
  const tables = [...creates.values()].sort((left, right) => left.name.localeCompare(right.name)).map((table) => {
    const primaryKey = table.primaryKey;
    const columns = [...table.columns.entries()].map(([name, value], index) => {
      const column = parseColumn(name, value.definition, index + 1, value.declarations);
      const compositePosition = primaryKey.indexOf(name);
      if (compositePosition >= 0) {
        column.primaryKeyPosition = compositePosition + 1;
        column.nullable = false;
      }
      return column;
    });
    const inlineUnique = table.uniqueKeys;
    const indexUnique = indexes.filter((index) => index.table === table.name && index.unique)
      .map((index) => index.columns.map((column) => column.name));
    return {
      name: table.name,
      sourceLocations: table.createLines,
      columns,
      primaryKey,
      uniqueKeys: [...inlineUnique, ...indexUnique]
        .sort((left, right) => left.join(',').localeCompare(right.join(','))),
      indexes: indexes.filter((index) => index.table === table.name).map((index) => index.name),
    };
  });
  return { tables, indexes };
}

function findMarker(source, marker) {
  const index = source.indexOf(marker);
  return index < 0 ? null : sourceAnchor(source, index, 'compatibility', normalizeSql(marker).slice(0, 64));
}

function hydrateInventory(inventory, databaseSource, baselineSource) {
  const evidence = collectDatabaseEvidence(databaseSource);
  const compatibilityStart = databaseSource.indexOf('\n  migrate() {');
  const compatibilityEnd = databaseSource.indexOf('\n  persist() {', compatibilityStart);
  if (compatibilityStart < 0 || compatibilityEnd < 0) throw new Error('AppDatabase compatibility method block not found');
  const decisions = new Map((inventory.tables || []).map((table) => [table.name, {
    history: table.history,
    backfillPrerequisites: table.backfillPrerequisites,
    invariants: table.invariants,
    relations: table.relations,
  }]));
  const tables = evidence.tables.map((table) => ({ ...table, ...(decisions.get(table.name) || {}) }));
  const compatibilityMigrations = (inventory.compatibilityMigrations || []).map((migration) => ({
    ...migration,
    source: findMarker(databaseSource, migration.markers?.[0] || ''),
  }));
  return {
    formatVersion: FORMAT_VERSION,
    schema: 'autoplan-schema-v1',
    description: inventory.description,
    generatedBy: 'scripts/migration-p04/inventory-schema.js',
    inputs: [
      { path: DATABASE_PATH, hashScope: 'effective tables, columns, constraints and indexes', sha256: sha256(stableStringify(evidence)) },
      { path: DATABASE_PATH, hashScope: 'AppDatabase migrate through migrateScanFilesTable compatibility method block', sha256: sha256(databaseSource.slice(compatibilityStart, compatibilityEnd)) },
      { path: BASELINE_PATH, hashScope: 'complete P00 database-and-dependencies baseline bytes', sha256: sha256(baselineSource) },
    ],
    ordering: {
      tables: 'name ascending',
      columns: 'effective SQLite ordinal (CREATE order, then ensureColumn order)',
      indexes: 'name ascending',
    },
    tables,
    indexes: evidence.indexes,
    defaultData: inventory.defaultData,
    compatibilityMigrations,
    foreignKeyDecisions: inventory.foreignKeyDecisions,
    sensitiveData: inventory.sensitiveData,
    risks: inventory.risks,
  };
}

function stableStringify(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function scanForSecrets(value) {
  const text = JSON.stringify(value);
  const checks = [
    ['machine-local path', /[A-Za-z]:[\\/](?:Users|Documents and Settings|AppData)[\\/]/i],
    ['OpenAI-style credential', /\bsk-[A-Za-z0-9_-]{12,}\b/],
    ['GitHub credential', /\b(?:ghp|github_pat)_[A-Za-z0-9_]{12,}\b/i],
    ['private key', /BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY/],
  ];
  return checks.filter(([, pattern]) => pattern.test(text)).map(([label]) => `inventory contains ${label}`);
}

function compareJson(label, actual, expected, errors) {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) errors.push(`${label} drift`);
}

function validatePolicyShape(inventory, errors) {
  if (inventory.formatVersion !== FORMAT_VERSION) errors.push(`formatVersion must be ${FORMAT_VERSION}`);
  if (inventory.schema !== 'autoplan-schema-v1') errors.push('schema must be autoplan-schema-v1');
  const tableNames = (inventory.tables || []).map((table) => table.name);
  const sortedNames = [...tableNames].sort((a, b) => a.localeCompare(b));
  compareJson('table ordering', tableNames, sortedNames, errors);
  for (const table of inventory.tables || []) {
    for (const field of ['history', 'backfillPrerequisites', 'invariants', 'relations']) {
      if (!Array.isArray(table[field]) || !table[field].length) errors.push(`${table.name}.${field} must be non-empty`);
    }
    const positions = table.columns.map((column) => column.position);
    compareJson(`${table.name} column positions`, positions, positions.map((_, index) => index + 1), errors);
    for (const column of table.columns) {
      if (!column.type || typeof column.nullable !== 'boolean' || !column.declarations?.length) {
        errors.push(`${table.name}.${column.name} lacks type/nullability/provenance`);
      }
    }
  }
  for (const item of inventory.compatibilityMigrations || []) {
    for (const field of ['id', 'markers', 'preconditions', 'behavior', 'dataEffect', 'failureRisk']) {
      if (!item[field] || (Array.isArray(item[field]) && !item[field].length)) errors.push(`compatibility migration lacks ${field}`);
    }
  }
  for (const item of inventory.sensitiveData || []) {
    for (const field of ['id', 'classification', 'locations', 'migrationPolicy', 'apiPolicy', 'logPolicy', 'eventPolicy', 'fixturePolicy']) {
      if (!item[field] || (Array.isArray(item[field]) && !item[field].length)) errors.push(`${item.id || 'sensitive item'} lacks ${field}`);
    }
  }
  for (const decision of inventory.foreignKeyDecisions || []) {
    for (const field of ['relation', 'decision', 'onDelete', 'rationale', 'precondition']) {
      if (!decision[field]) errors.push(`${decision.relation || 'foreign key decision'} lacks ${field}`);
    }
  }
}

function validateInventory(rootDir, inventory, sources = {}) {
  const errors = [];
  const databaseSource = sources.databaseSource ?? fs.readFileSync(path.join(rootDir, DATABASE_PATH), 'utf8');
  const baselineSource = sources.baselineSource ?? fs.readFileSync(path.join(rootDir, BASELINE_PATH), 'utf8');
  let baseline;
  try {
    baseline = JSON.parse(baselineSource);
  } catch (error) {
    return [`P00 baseline is not valid JSON: ${error.message}`];
  }
  errors.push(...baselineInventory.validateInventory(rootDir, baseline).map((error) => `P00: ${error}`));
  validatePolicyShape(inventory, errors);
  errors.push(...scanForSecrets(inventory));

  const hydrated = hydrateInventory(inventory, databaseSource, baselineSource);
  compareJson('source-derived tables', inventory.tables, hydrated.tables, errors);
  compareJson('source-derived indexes', inventory.indexes, hydrated.indexes, errors);
  compareJson('input hashes', inventory.inputs, hydrated.inputs, errors);
  compareJson('compatibility source locations', inventory.compatibilityMigrations, hydrated.compatibilityMigrations, errors);

  const baselineTables = Object.keys(baseline.schema || {}).sort((a, b) => a.localeCompare(b));
  compareJson('P00 table cross-check', inventory.tables.map((table) => table.name), baselineTables, errors);
  for (const table of inventory.tables || []) {
    const columns = table.columns.map((column) => column.name).sort((a, b) => a.localeCompare(b));
    compareJson(`P00 columns ${table.name}`, columns, baseline.schema?.[table.name] || [], errors);
  }

  for (const migration of inventory.compatibilityMigrations || []) {
    let cursor = -1;
    for (const marker of migration.markers || []) {
      const next = databaseSource.indexOf(marker, cursor + 1);
      if (next < 0) {
        errors.push(`${migration.id} marker missing or out of order: ${marker}`);
        break;
      }
      cursor = next;
    }
  }
  return errors;
}

function loadInventory(rootDir) {
  return JSON.parse(fs.readFileSync(path.join(rootDir, INVENTORY_PATH), 'utf8'));
}

function run(rootDir = path.resolve(__dirname, '../..')) {
  const raw = fs.readFileSync(path.join(rootDir, INVENTORY_PATH), 'utf8');
  const inventory = JSON.parse(raw);
  const errors = validateInventory(rootDir, inventory);
  if (raw !== stableStringify(inventory)) errors.push('schema inventory JSON is not canonical UTF-8 JSON with a final newline');
  if (errors.length) {
    const error = new Error(`P04 schema inventory drift (${errors.length})\n- ${errors.join('\n- ')}`);
    error.errors = errors;
    throw error;
  }
  return {
    ok: true,
    schema: inventory.schema,
    tables: inventory.tables.length,
    columns: inventory.tables.reduce((sum, table) => sum + table.columns.length, 0),
    indexes: inventory.indexes.length,
    compatibilityMigrations: inventory.compatibilityMigrations.length,
  };
}

if (require.main === module) {
  try {
    const rootArg = process.argv.slice(2).find((argument) => !argument.startsWith('--'));
    const rootDir = rootArg ? path.resolve(rootArg) : path.resolve(__dirname, '../..');
    if (process.argv.includes('--print')) {
      const databaseSource = fs.readFileSync(path.join(rootDir, DATABASE_PATH), 'utf8');
      const baselineSource = fs.readFileSync(path.join(rootDir, BASELINE_PATH), 'utf8');
      process.stdout.write(stableStringify(hydrateInventory(POLICY_TEMPLATE, databaseSource, baselineSource)));
    } else {
      process.stdout.write(`${JSON.stringify(run(rootDir))}\n`);
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = {
  BASELINE_PATH,
  DATABASE_PATH,
  FORMAT_VERSION,
  INVENTORY_PATH,
  collectDatabaseEvidence,
  extractIndexes,
  hydrateInventory,
  loadInventory,
  scanForSecrets,
  stableStringify,
  validateInventory,
  run,
};
