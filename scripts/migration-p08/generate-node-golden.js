'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { AppDatabase, nowIso } = require('../../src/database');
const { createIntakeService } = require('../../src/intakeService');
const { LoopService } = require('../../src/loopService');
const { createExecutorStore } = require('../../src/executors/executorStore');
const { createAiConfig, deleteAiConfig, listAiConfigs } = require('../../src/chat/aiConfigService');
const { createClaudeCliConfig, deleteClaudeCliConfig, listClaudeCliConfigs, setDefaultClaudeCliConfig } = require('../../src/chat/claudeCliConfigService');
const { createConversation, listConversations, updateConversation } = require('../../src/chat/chatController');
const { mcpServerConfig, saveMcpSettings } = require('../../src/mcpConfig');
const { buildContract, CONTRACT_VERSION, sha256, stableJson } = require('./inventory-static-contract');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_DIR = 'fixtures/migration/p08';
const GOLDEN_NAME = 'node-static.golden.json';
const MANIFEST_NAME = 'manifest.json';
const GENERATOR_VERSION = 'p08-node-static-golden-v1';
const NORMALIZATION_VERSION = 'p08-node-static-normalization-v1';
const TEMP_PREFIX = 'autoplan-p08-node-';
const FIXED_EPOCH_MS = Date.parse('2026-07-11T00:00:00.000Z');
const UTC = '<utc>';
const FIXTURE_ROOT = '<fixture-root>';
const REDACTED = '<redacted>';
const REDACTED_PATH = '<redacted-path>';
const REDACTED_COMMAND = '<redacted-command>';
const REDACTED_ENV = '<redacted-env>';
const REDACTED_CONTENT = '<redacted-content>';
const REDACTED_TOOL_DATA = '<redacted-tool-data>';
const REDACTED_SESSION = '<redacted-session-id>';
const MASKED_SECRET = '<masked-secret>';
const SECRET_FIXTURES = Object.freeze({
  aiKey: 'non-working-p08-ai-token-0000',
  claudeTokenA: 'non-working-p08-claude-token-1111',
  claudeTokenB: 'non-working-p08-claude-token-2222',
  mcpToken: 'non-working-p08-mcp-token-3333',
  planGenerationToken: 'non-working-p08-plan-gen-token-4444',
  planExecutionToken: 'non-working-p08-plan-exec-token-5555',
  envValue: 'non-working-p08-env-secret-6666',
});
const RELEVANT_TABLES = Object.freeze(['settings', 'project_states', 'scripts', 'executors', 'conversations', 'chat_messages', 'ai_configs', 'claude_cli_configs']);
const MCP_ENV_KEYS = Object.freeze(['AUTOPLAN_MCP_ENABLED', 'AUTOPLAN_MCP_TRANSPORT', 'AUTOPLAN_MCP_HOST', 'AUTOPLAN_MCP_PORT', 'AUTOPLAN_MCP_PATH', 'AUTOPLAN_MCP_AUTH_TOKEN']);

class BlockedError extends Error {
  constructor(reason) { super(`blocked: ${reason}`); this.name = 'BlockedError'; this.reason = reason; }
}

function installDeterministicRuntime() {
  const NativeDate = global.Date, originalRandomBytes = crypto.randomBytes;
  const originalEnv = Object.fromEntries(MCP_ENV_KEYS.map((key) => [key, process.env[key]]));
  let tick = 0, randomCounter = 0;
  class DeterministicDate extends NativeDate {
    constructor(...args) { args.length ? super(...args) : super(FIXED_EPOCH_MS + tick++ * 1000); }
    static now() { return FIXED_EPOCH_MS + tick++ * 1000; }
  }
  DeterministicDate.parse = NativeDate.parse; DeterministicDate.UTC = NativeDate.UTC;
  global.Date = DeterministicDate;
  crypto.randomBytes = (size) => { const value = Buffer.alloc(size); for (let i = 0; i < size; i += 1) value[i] = (randomCounter + i + 73) % 256; randomCounter += size; return value; };
  for (const key of MCP_ENV_KEYS) delete process.env[key];
  return () => {
    global.Date = NativeDate; crypto.randomBytes = originalRandomBytes;
    for (const key of MCP_ENV_KEYS) originalEnv[key] === undefined ? delete process.env[key] : process.env[key] = originalEnv[key];
  };
}

function assertApprovedOutput(rootDir, outputDir, options = {}) {
  if (path.resolve(outputDir) !== path.resolve(rootDir, OUTPUT_DIR) && !options.allowExternalOutput) throw new BlockedError('golden_output_not_approved');
}

function assertPrerequisites(rootDir) {
  const required = ['docs/migration/p06/intake-contract.json', 'fixtures/migration/p06/manifest.json', 'docs/migration/p07/README.md', 'docs/migration/p07/runbook.md', 'fixtures/migration/p07/expected-errors.json', 'backend/migrations/0001_schema_v1.sql'];
  const artifacts = required.map((relativePath) => {
    const absolutePath = path.join(rootDir, relativePath);
    if (!fs.existsSync(absolutePath) || !fs.statSync(absolutePath).isFile()) throw new BlockedError(`p08_prerequisite_missing:${relativePath}`);
    return { path: relativePath, sha256: sha256(fs.readFileSync(absolutePath)) };
  });
  const p06Manifest = JSON.parse(fs.readFileSync(path.join(rootDir, 'fixtures/migration/p06/manifest.json'), 'utf8'));
  if (p06Manifest.source?.syntheticOnly !== true || p06Manifest.writerHandoff?.sameCopyConcurrentWritersAllowed !== false) throw new BlockedError('p06_fixture_or_writer_handoff_not_approved');
  return artifacts;
}

function assertContractFrozen(rootDir) {
  const relativePath = 'docs/migration/p08/static-contract.json';
  const contract = buildContract(rootDir);
  if (contract.version !== CONTRACT_VERSION || contract.status !== 'frozen-node-static-contract') throw new BlockedError('static_contract_not_frozen');
  return { path: relativePath, sha256: sha256(fs.readFileSync(path.join(rootDir, relativePath))) };
}

function createScriptRow(db, projectId, fixtureRoot) {
  const ts = nowIso();
  const id = db.insert(
    `INSERT INTO scripts (project_id, name, path, runtime, body, description, trigger_mode, hook_stage, schedule_cron, enabled, work_dir, timeout_seconds, fail_aborts, context_inject, sort_order, source_type, created_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [projectId, 'P08 fixture script', path.join(fixtureRoot, 'scripts', 'fixture-script.js'), 'node', "console.log('p08 fixture script');", 'Synthetic static contract script', 'schedule', null, '*/5 * * * *', 1, path.join(fixtureRoot, 'workspace-alpha'), 42, 1, 'env', 10, 'inline', ts, ts],
  );
  db.run('UPDATE scripts SET last_status = ?, last_exit_code = ?, last_duration_ms = ?, last_log = ?, last_run_at = ?, updated_at = ? WHERE id = ?', ['ok', 0, 123, 'synthetic script output that must not be public', nowIso(), nowIso(), id]);
  return id;
}

function insertChatMessage(db, projectId, conversationId, role, content, toolCalls = null, toolResult = null, status = 'done') {
  const createdAt = nowIso();
  db.run(
    `INSERT INTO chat_messages (project_id, conversation_id, role, content, tool_calls, tool_result, status, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
    [projectId, conversationId, role, content, toolCalls ? JSON.stringify(toolCalls) : null, toolResult ? JSON.stringify(toolResult) : null, status, createdAt],
  );
  db.run('UPDATE conversations SET updated_at = ? WHERE id = ? AND project_id = ?', [createdAt, conversationId, projectId]);
}

async function executeScenarios(temporaryRoot) {
  const databasePath = path.join(temporaryRoot, 'sanitized-p08-node.sqlite');
  const workspaceAlpha = path.join(temporaryRoot, 'workspace-alpha');
  const attachmentsRoot = path.join(temporaryRoot, 'attachments');
  fs.mkdirSync(workspaceAlpha, { recursive: true }); fs.mkdirSync(attachmentsRoot, { recursive: true });
  const db = new AppDatabase(databasePath);
  let closed = false;
  try {
    await db.init();
    const databaseBeforeSha256 = sha256(fs.readFileSync(databasePath));
    const loop = new LoopService(db);
    const intake = createIntakeService({ db, loop, attachmentsRoot });
    const projectId = Number(intake.createProject({ name: 'P08 Alpha', workspacePath: workspaceAlpha, description: 'Synthetic static compatibility project' }).activeProjectId);
    db.run(
      `UPDATE project_states SET env_vars = ?, plan_generation_claude_auth_token = ?, plan_execution_claude_auth_token = ?, updated_at = ? WHERE project_id = ?`,
      [JSON.stringify([{ name: 'P08_SECRET_ENV', value: SECRET_FIXTURES.envValue }]), SECRET_FIXTURES.planGenerationToken, SECRET_FIXTURES.planExecutionToken, nowIso(), projectId],
    );
    saveMcpSettings(db, { mcpEnabled: false, mcpTransport: 'http', mcpHost: '127.0.0.1', mcpPort: 43910, mcpPath: 'p08-mcp', mcpAuthToken: SECRET_FIXTURES.mcpToken });
    const scriptId = createScriptRow(db, projectId, temporaryRoot);
    const executorStore = createExecutorStore(db);
    const shellExecutor = executorStore.create(projectId, {
      label: 'P08 shell executor', type: 'shell', command: 'node',
      args: ['scripts/fixture.js', { value: '--mode static', quoting: 'weak' }],
      options: { cwd: workspaceAlpha, env: { P08_SECRET_ENV: SECRET_FIXTURES.envValue } },
      group: { kind: 'build', isDefault: true }, presentation: { reveal: 'silent', panel: 'dedicated', clear: true },
      problemMatcher: ['$tsc'], dependsOn: [], dependsOrder: 'parallel', enabled: true, sortOrder: 20,
    });
    executorStore.updateRunState(projectId, shellExecutor.id, { lastStatus: 'ok', lastExitCode: 0, lastDurationMs: 234, lastLog: 'synthetic executor output that must not be public', lastRunAt: nowIso() });
    const pluginExecutor = executorStore.create(projectId, {
      label: 'P08 plugin executor', type: 'plugin',
      actions: { start: { type: 'command', command: 'npm', args: ['run', 'dev'] }, reload: { type: 'input', input: 'r' }, stop: { type: 'command', command: 'npm', args: ['run', 'stop'] } },
      options: { cwd: workspaceAlpha, env: { P08_SECRET_ENV: SECRET_FIXTURES.envValue } }, dependsOn: ['P08 shell executor'], dependsOrder: 'sequence', enabled: false, sortOrder: 30,
    });
    const aiConfig = createAiConfig(db, { name: 'P08 AI config', provider: 'anthropic', baseUrl: 'https://invalid.example.test/anthropic', apiKey: SECRET_FIXTURES.aiKey, model: 'claude-sonnet-fixture', temperature: '0.2', thinkingDepth: 'high', thinkingBudgetTokens: 4096 });
    const aiConfigsBeforeDelete = listAiConfigs(db);
    const conversationA = createConversation(db, { projectId, title: 'P08 conversation A', aiConfigId: aiConfig.id });
    const conversationB = createConversation(db, { projectId, title: 'P08 conversation B', aiConfigId: null });
    updateConversation(db, conversationB.id, { projectId, pinned: true });
    db.run('UPDATE conversations SET codex_session_id = ?, updated_at = ? WHERE id = ? AND project_id = ?', ['codex-session-p08-fixture-secret', nowIso(), conversationA.id, projectId]);
    insertChatMessage(db, projectId, conversationA.id, 'user', 'Synthetic user prompt with private body');
    insertChatMessage(db, projectId, conversationA.id, 'assistant', 'Synthetic assistant response', [{ id: 'tool-call-p08', name: 'list_requirements', arguments: '{"limit":1}' }]);
    insertChatMessage(db, projectId, conversationA.id, 'tool', '{"ok":true,"content":"private tool result"}', null, { tool_call_id: 'tool-call-p08', name: 'list_requirements', result: { private: true } });
    insertChatMessage(db, projectId, conversationA.id, 'user', 'Queued synthetic prompt', null, null, 'queued');
    const conversationsBeforeAiDelete = listConversations(db, projectId);
    const chatHistory = db.all('SELECT * FROM chat_messages WHERE conversation_id = ? AND project_id = ? ORDER BY created_at ASC, id ASC', [conversationA.id, projectId]);
    deleteAiConfig(db, aiConfig.id);
    const conversationsAfterAiDelete = listConversations(db, projectId);
    createClaudeCliConfig(db, { name: 'P08 Claude A', baseUrl: 'https://invalid.example.test/claude-a', authToken: SECRET_FIXTURES.claudeTokenA, model: 'claude-a-fixture' });
    const claudeB = createClaudeCliConfig(db, { name: 'P08 Claude B', baseUrl: 'https://invalid.example.test/claude-b', authToken: SECRET_FIXTURES.claudeTokenB, model: 'claude-b-fixture' });
    setDefaultClaudeCliConfig(db, claudeB.id);
    const claudeBeforeDelete = listClaudeCliConfigs(db);
    deleteClaudeCliConfig(db, claudeB.id);
    const snapshot = loop.snapshot(projectId);
    const schema = schemaSummary(db);
    const raw = {
      schemaVersion: 1, version: GENERATOR_VERSION, schema, schemaSha256: sha256(stableJson(schema)), rowCounts: tableCounts(db), handoff: { sqlJsClosed: true, databaseOwnerReleased: true },
      scenarios: [
        { id: 'automation-static', scriptIds: [scriptId], executorIds: [shellExecutor.id, pluginExecutor.id], scripts: snapshot.scripts.map(projectScriptRow), executors: snapshot.executors.map(projectExecutorRow) },
        { id: 'chat-static', conversationsBeforeAiDelete: conversationsBeforeAiDelete.map(projectConversation), conversationsAfterAiDelete: conversationsAfterAiDelete.map(projectConversation), chatHistory: chatHistory.map(projectChatMessage) },
        { id: 'config-static', aiConfigsBeforeDelete: aiConfigsBeforeDelete.map(projectAiConfig), aiConfigsAfterDelete: listAiConfigs(db).map(projectAiConfig), claudeBeforeDelete: claudeBeforeDelete.map(projectClaudeConfig), claudeAfterDelete: listClaudeCliConfigs(db).map(projectClaudeConfig) },
        { id: 'mcp-static', settings: projectMcpSettings(db.getSettings('mcp.')), serverConfig: projectMcpServerConfig(mcpServerConfig(db, {})), snapshot: projectMcpSnapshot(snapshot.mcp) },
        { id: 'snapshot-static', snapshot: projectSnapshot(snapshot) },
        { id: 'secret-redaction', observations: secretObservations(snapshot, aiConfigsBeforeDelete, claudeBeforeDelete, chatHistory) },
      ],
    };
    const databaseAfterSha256 = sha256(fs.readFileSync(databasePath));
    db.close(); closed = true;
    return { databasePath, databaseBeforeSha256, databaseAfterSha256, fixtureRecipe: fixtureRecipe(temporaryRoot), raw };
  } finally { if (!closed) db.close(); }
}

function fixtureRecipe(temporaryRoot) {
  return { project: { name: 'P08 Alpha', workspacePath: path.join(temporaryRoot, 'workspace-alpha') }, automation: { script: 'one static script row', executors: ['shell', 'plugin'] }, chat: { conversations: 2, messages: 4 }, config: { aiConfigs: 1, claudeCliConfigs: 2, mcpSettings: true }, secrets: Object.keys(SECRET_FIXTURES).sort() };
}

function schemaSummary(db) {
  return Object.fromEntries(RELEVANT_TABLES.map((table) => [table, db.all(`PRAGMA table_info(${table})`).map((column) => ({ cid: Number(column.cid), name: column.name, type: column.type, notnull: Number(column.notnull), dflt_value: column.dflt_value ?? null, pk: Number(column.pk) }))]));
}

function tableCounts(db) {
  return Object.fromEntries(RELEVANT_TABLES.map((table) => [table, Number(db.get(`SELECT COUNT(*) AS count FROM ${table}`)?.count || 0)]));
}

function projectScriptRow(row = {}) {
  return { fields: Object.keys(row).sort(), id: row.id, project_id: row.project_id, name: row.name, runtime: row.runtime, source_type: row.source_type, trigger_mode: row.trigger_mode, hook_stage: row.hook_stage ?? null, schedule_cron: row.schedule_cron ?? null, enabled: Number(row.enabled), timeout_seconds: Number(row.timeout_seconds), fail_aborts: Number(row.fail_aborts), context_inject: row.context_inject, sort_order: Number(row.sort_order), last_status: row.last_status ?? null, last_exit_code: nullableNumber(row.last_exit_code), last_duration_ms: nullableNumber(row.last_duration_ms), has_path: Boolean(row.path), has_body: Boolean(row.body), has_work_dir: Boolean(row.work_dir), has_last_log: Boolean(row.last_log), created_at: UTC, updated_at: UTC, last_run_at: row.last_run_at ? UTC : null };
}

function projectExecutorRow(row = {}) {
  return {
    fields: Object.keys(row).sort(), id: row.id, projectId: row.projectId, project_id: row.project_id, label: row.label, type: row.type, command: row.command ? REDACTED_COMMAND : '', argsShape: Array.isArray(row.args) ? row.args.map(argShape) : [],
    actions: row.actions ? Object.keys(row.actions).sort() : [], options: { cwd: row.options?.cwd ? REDACTED_PATH : '', envKeyCount: row.options?.env && typeof row.options.env === 'object' ? Object.keys(row.options.env).length : 0, env: row.options?.env && Object.keys(row.options.env || {}).length ? REDACTED_ENV : {} },
    group: row.group, group_kind: row.group_kind ?? null, group_is_default: Number(row.group_is_default || 0), presentation: row.presentation || {}, problemMatcherType: Array.isArray(row.problemMatcher) ? 'array' : typeof row.problemMatcher,
    dependsOn: row.dependsOn || [], dependsOrder: row.dependsOrder, depends_order: row.depends_order, enabled: Boolean(row.enabled), sortOrder: Number(row.sortOrder), sort_order: Number(row.sort_order),
    lastStatus: row.lastStatus ?? null, last_status: row.last_status ?? null, lastExitCode: nullableNumber(row.lastExitCode), last_exit_code: nullableNumber(row.last_exit_code), lastDurationMs: nullableNumber(row.lastDurationMs), last_duration_ms: nullableNumber(row.last_duration_ms),
    hasLastLog: Boolean(row.lastLog), has_last_log: Boolean(row.last_log), lastRunAt: row.lastRunAt ? UTC : null, last_run_at: row.last_run_at ? UTC : null, running: Boolean(row.running), runStatus: row.runStatus, hasActiveOperation: Boolean(row.activeOperation), createdAt: row.createdAt ? UTC : null, created_at: row.created_at ? UTC : null, updatedAt: row.updatedAt ? UTC : null, updated_at: row.updated_at ? UTC : null,
  };
}

function argShape(arg) { return arg && typeof arg === 'object' && !Array.isArray(arg) ? { type: 'object', keys: Object.keys(arg).sort(), value: REDACTED_COMMAND } : { type: typeof arg, value: REDACTED_COMMAND }; }

function projectConversation(row = {}) {
  return { fields: Object.keys(row).sort(), id: row.id, project_id: row.project_id, projectId: row.projectId, title: row.title, ai_config_id: row.ai_config_id ?? null, aiConfigId: row.aiConfigId ?? null, pinned: Boolean(row.pinned), pinned_at: row.pinned_at ? UTC : null, pinnedAt: row.pinnedAt ? UTC : null, created_at: UTC, createdAt: UTC, updated_at: UTC, updatedAt: UTC, codex_session_id_public: false };
}

function projectChatMessage(row = {}) {
  return { fields: Object.keys(row).sort(), id: row.id, project_id: row.project_id, conversation_id: row.conversation_id ?? null, role: row.role, status: row.status ?? null, content: row.content ? REDACTED_CONTENT : '', tool_calls: row.tool_calls ? REDACTED_TOOL_DATA : null, tool_result: row.tool_result ? REDACTED_TOOL_DATA : null, created_at: UTC };
}

function projectAiConfig(config = {}) {
  return { fields: Object.keys(config).sort(), id: config.id, projectId: config.projectId ?? null, name: config.name, provider: config.provider, baseUrl: config.baseUrl, hasApiKey: Boolean(config.hasApiKey), maskedKey: config.maskedKey ? MASKED_SECRET : '', model: config.model, temperature: config.temperature, thinkingDepth: config.thinkingDepth ?? null, thinkingBudgetTokens: config.thinkingBudgetTokens ?? null, createdAt: config.createdAt ? UTC : null, updatedAt: config.updatedAt ? UTC : null, rawApiKeyPublic: false };
}

function projectClaudeConfig(config = {}) {
  return { fields: Object.keys(config).sort(), id: config.id, projectId: config.projectId ?? null, name: config.name, baseUrl: config.baseUrl, hasAuthToken: Boolean(config.hasAuthToken), maskedKey: config.maskedKey ? MASKED_SECRET : '', model: config.model, isDefault: Boolean(config.isDefault), createdAt: config.createdAt ? UTC : null, updatedAt: config.updatedAt ? UTC : null, rawAuthTokenPublic: false };
}

function projectMcpSettings(settings = {}) {
  return { keys: Object.keys(settings).sort(), enabled: settings['mcp.enabled'] ?? '', transport: settings['mcp.transport'] ?? '', host: settings['mcp.host'] ?? '', port: settings['mcp.port'] ?? '', path: settings['mcp.path'] ?? '', portExplicit: settings['mcp.portExplicit'] ?? '', hasAuthToken: Boolean(settings['mcp.authToken']), secretValuePublic: false };
}

function projectMcpServerConfig(config = {}) {
  return { fields: Object.keys(config).filter((key) => key !== 'authToken').sort(), secretFields: Object.prototype.hasOwnProperty.call(config, 'authToken') ? ['authToken'] : [], enabled: Boolean(config.enabled), transport: config.transport, host: config.host, port: Number(config.port), path: config.path, hasAuthToken: Boolean(config.authToken), secretValuePublic: false, autoPortFallback: Boolean(config.autoPortFallback) };
}

function projectMcpSnapshot(mcp = {}) {
  return {
    fields: Object.keys(mcp).sort(), enabled: Boolean(mcp.enabled), running: Boolean(mcp.running), status: mcp.status, transport: mcp.transport, host: mcp.host, port: mcp.port, path: mcp.path, url: mcp.url, hasAuthToken: Boolean(mcp.hasAuthToken), authTokenMasked: mcp.authTokenMasked ? MASKED_SECRET : '', authHeader: mcp.authHeader ? 'Authorization: Bearer <redacted>' : '', localOnly: Boolean(mcp.localOnly),
    toolCount: Array.isArray(mcp.tools) ? mcp.tools.length : 0, tools: Array.isArray(mcp.tools) ? [...mcp.tools].sort() : [], toolDocCount: Array.isArray(mcp.toolDocs) ? mcp.toolDocs.length : 0, connectionExample: mcp.connectionExample, note: mcp.note, lastEvent: mcp.lastEvent ? { type: mcp.lastEvent.type, createdAt: UTC } : null, lastError: mcp.lastError || null, startedAt: mcp.startedAt ? UTC : null, rawAuthTokenPublic: false,
  };
}

function projectSnapshot(snapshot = {}) {
  const state = snapshot.state || {};
  return {
    topLevelFields: Object.keys(snapshot).sort(), activeProjectId: snapshot.activeProjectId ?? null, activeProjectFields: snapshot.activeProject ? Object.keys(snapshot.activeProject).sort() : [], projectsCount: Array.isArray(snapshot.projects) ? snapshot.projects.length : 0,
    collectionCounts: { requirements: lengthOf(snapshot.requirements), feedback: lengthOf(snapshot.feedback), attachments: lengthOf(snapshot.attachments), plans: lengthOf(snapshot.plans), tasks: lengthOf(snapshot.tasks), events: lengthOf(snapshot.events), scripts: lengthOf(snapshot.scripts), executors: lengthOf(snapshot.executors), terminals: lengthOf(snapshot.terminals), activeOperations: lengthOf(snapshot.activeOperations) },
    stateFields: state ? Object.keys(state).sort() : [], stateSecrets: { env_vars: state.env_vars ? REDACTED_ENV : '', plan_generation_has_claude_auth_token: Boolean(state.plan_generation_has_claude_auth_token), plan_generation_claude_auth_token: state.plan_generation_claude_auth_token ? MASKED_SECRET : '', plan_execution_has_claude_auth_token: Boolean(state.plan_execution_has_claude_auth_token), plan_execution_claude_auth_token: state.plan_execution_claude_auth_token ? MASKED_SECRET : '' },
    mcp: projectMcpSnapshot(snapshot.mcp), scriptOrder: (snapshot.scripts || []).map((row) => row.id), executorOrder: (snapshot.executors || []).map((row) => row.id), activeOperation: snapshot.activeOperation ? REDACTED : null, lastOperation: snapshot.lastOperation ? REDACTED : null,
  };
}

function secretObservations(snapshot, aiConfigs, claudeConfigs, chatHistory) {
  return { aiConfigRawKeyPublic: aiConfigs.some((config) => Object.prototype.hasOwnProperty.call(config, 'apiKey') || Object.prototype.hasOwnProperty.call(config, 'api_key')), aiConfigMaskedOnly: aiConfigs.some((config) => config.hasApiKey && Boolean(config.maskedKey)), claudeRawTokenPublic: claudeConfigs.some((config) => Object.prototype.hasOwnProperty.call(config, 'authToken') || Object.prototype.hasOwnProperty.call(config, 'auth_token')), claudeMaskedOnly: claudeConfigs.some((config) => config.hasAuthToken && Boolean(config.maskedKey)), mcpRawTokenPublic: Object.prototype.hasOwnProperty.call(snapshot.mcp || {}, 'authToken'), mcpMaskedOnly: Boolean(snapshot.mcp?.hasAuthToken && snapshot.mcp?.authTokenMasked), stateEnvVarsRedacted: Boolean(snapshot.state?.env_vars), chatContentRedactedInGolden: chatHistory.every((row) => Boolean(row.content)), toolDataPresentButRedacted: chatHistory.some((row) => row.tool_calls || row.tool_result), conversationCodexSessionIdGolden: REDACTED_SESSION };
}

function lengthOf(value) { return Array.isArray(value) ? value.length : 0; }
function nullableNumber(value) { if (value === undefined || value === null || value === '') return null; const number = Number(value); return Number.isFinite(number) ? number : null; }
function createNormalizationContext(value, fixtureRoot) { return { fixtureRoot: path.resolve(fixtureRoot).replace(/\\/g, '/'), value }; }

function normalizeGolden(value, options = {}) {
  const context = options.context || createNormalizationContext(value, options.fixtureRoot);
  const normalized = normalizeValue(value, context, []);
  assertSanitized(normalized, options);
  return { value: normalized, metadata: { version: NORMALIZATION_VERSION, placeholders: { fixtureRoot: FIXTURE_ROOT, utc: UTC, redacted: REDACTED, path: REDACTED_PATH, command: REDACTED_COMMAND, env: REDACTED_ENV, content: REDACTED_CONTENT, toolData: REDACTED_TOOL_DATA, session: REDACTED_SESSION, maskedSecret: MASKED_SECRET } } };
}

function normalizeValue(value, context, trail) {
  if (value === null || value === undefined || typeof value === 'boolean' || typeof value === 'number') return value;
  if (typeof value === 'string') return normalizeString(value, context, trail);
  if (Array.isArray(value)) return value.map((item, index) => normalizeValue(item, context, [...trail, index]));
  if (typeof value !== 'object') throw new Error(`unsupported golden value at ${pointer(trail)}`);
  return Object.fromEntries(Object.entries(value).map(([key, child]) => [key, isSensitiveKey(key) && child ? sensitivePlaceholder(key) : normalizeValue(child, context, [...trail, key])]));
}

function normalizeString(value, context, trail) {
  const key = String(trail.at(-1) || '');
  if (isTimeKey(key) && value && value !== UTC) return UTC;
  const normalized = value.replace(/\\/g, '/');
  if (context.fixtureRoot && normalized.toLowerCase() === context.fixtureRoot.toLowerCase()) return FIXTURE_ROOT;
  if (context.fixtureRoot && normalized.toLowerCase().startsWith(`${context.fixtureRoot.toLowerCase()}/`)) return `${FIXTURE_ROOT}${normalized.slice(context.fixtureRoot.length)}`;
  return value;
}

function isTimeKey(key) { return /(?:^|_)(?:created|updated|started|last_run|run|pinned)_at$/i.test(key) || /(?:created|updated|started|lastRun|pinned)At$/.test(key); }
function isSensitiveKey(key) {
  const normalized = String(key).toLowerCase();
  return normalized === 'apikey' || normalized === 'api_key' || normalized === 'authtoken' || normalized === 'auth_token' || normalized === 'token' || normalized === 'env_vars' || normalized === 'env' || normalized === 'content' || normalized === 'tool_calls' || normalized === 'tool_result' || normalized === 'codex_session_id' || normalized === 'command' || normalized === 'args' || normalized === 'path' || normalized === 'work_dir' || normalized === 'cwd' || normalized === 'last_log' || normalized === 'lastlog';
}

function sensitivePlaceholder(key) {
  const normalized = String(key).toLowerCase();
  if (normalized.includes('token') || normalized.includes('key')) return REDACTED;
  if (normalized === 'env' || normalized === 'env_vars') return REDACTED_ENV;
  if (normalized === 'content') return REDACTED_CONTENT;
  if (normalized.includes('tool_')) return REDACTED_TOOL_DATA;
  if (normalized.includes('session')) return REDACTED_SESSION;
  if (normalized === 'command' || normalized === 'args') return REDACTED_COMMAND;
  if (normalized === 'path' || normalized === 'work_dir' || normalized === 'cwd') return REDACTED_PATH;
  return normalized.includes('log') ? REDACTED : REDACTED;
}

function assertSanitized(value, options = {}) {
  const encoded = JSON.stringify(value);
  const normalized = encoded.replace(/\\/g, '/').toLowerCase();
  const forbiddenPaths = [options.fixtureRoot, os.homedir(), process.env.APPDATA, process.env.LOCALAPPDATA].filter(Boolean).map((item) => path.resolve(item).replace(/\\/g, '/').toLowerCase());
  if (forbiddenPaths.some((item) => normalized.includes(item))) throw new BlockedError('golden_contains_real_absolute_path');
  if (Object.values(SECRET_FIXTURES).some((rawSecret) => encoded.includes(rawSecret))) throw new BlockedError('golden_contains_raw_fixture_secret');
  if (/\b(?:sk-|ghp_|github_pat_)[A-Za-z0-9_-]{8,}\b/.test(encoded)) throw new BlockedError('golden_contains_credential_shape');
  return true;
}

function pointer(trail) { return trail.length ? `/${trail.map((part) => String(part).replace(/~/g, '~0').replace(/\//g, '~1')).join('/')}` : '/'; }

function safeRemoveTemporaryRoot(temporaryRoot) {
  const tempRoot = fs.realpathSync(os.tmpdir());
  const resolved = path.resolve(temporaryRoot);
  const relative = path.relative(tempRoot, resolved);
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative) || !path.basename(resolved).startsWith(TEMP_PREFIX)) throw new BlockedError('temporary_cleanup_boundary_failed');
  fs.rmSync(resolved, { recursive: true, force: true });
}

function writeArtifacts(outputDir, artifacts) {
  fs.mkdirSync(outputDir, { recursive: true });
  const staging = fs.mkdtempSync(path.join(os.tmpdir(), 'autoplan-p08-output-'));
  try {
    for (const [name, content] of Object.entries(artifacts)) fs.writeFileSync(path.join(staging, name), content, 'utf8');
    for (const name of Object.keys(artifacts)) fs.copyFileSync(path.join(staging, name), path.join(outputDir, name));
  } finally { fs.rmSync(staging, { recursive: true, force: true }); }
}

async function buildGoldenBundle(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const prerequisites = assertPrerequisites(rootDir);
  const staticContract = assertContractFrozen(rootDir);
  const temporaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), TEMP_PREFIX));
  const restoreRuntime = installDeterministicRuntime();
  try {
    const execution = await executeScenarios(temporaryRoot);
    const normalized = normalizeGolden(execution.raw, { fixtureRoot: temporaryRoot });
    const golden = { ...normalized.value, normalization: normalized.metadata };
    const goldenContent = stableJson(golden);
    const generatorPath = path.join(rootDir, 'scripts/migration-p08/generate-node-golden.js');
    const inventoryPath = path.join(rootDir, 'scripts/migration-p08/inventory-static-contract.js');
    const fixtureRecipe = normalizeGolden(execution.fixtureRecipe, { fixtureRoot: temporaryRoot }).value;
    const manifest = {
      schemaVersion: 1, version: GENERATOR_VERSION, fixture_set: 'autoplan-p08-static-contract',
      generator: 'scripts/migration-p08/generate-node-golden.js', generatorSha256: sha256(fs.readFileSync(generatorPath)),
      inventory: { path: 'scripts/migration-p08/inventory-static-contract.js', sha256: sha256(fs.readFileSync(inventoryPath)) },
      source: { syntheticOnly: true, databaseCopy: 'generator-owned OS temporary database; never Electron userData or production autoplan.sqlite', execution: 'Node scenarios are serial; no external process, Chat stream, queue pump or MCP listener is started', fixtureRecipeSha256: sha256(stableJson(fixtureRecipe)), databaseBeforeSha256: execution.databaseBeforeSha256, databaseAfterSha256: execution.databaseAfterSha256, schemaSha256: golden.schemaSha256, rowCounts: golden.rowCounts },
      prerequisites: { staticContract, upstream: prerequisites }, normalization: normalized.metadata,
      scenarios: golden.scenarios.map((scenario) => scenario.id),
      writerHandoff: { sequence: ['P06/P07 prerequisites and frozen P08 contract are checked', 'Node opens a fresh generator-owned temporary database', 'Node executes static persistence scenarios serially', 'Node captures schema, row counts and database hashes', 'Node closes sql.js and releases database ownership', 'artifacts are staged and committed', 'Go may later open only a separately reset copy'], sameCopyConcurrentWritersAllowed: false, nodeClosedBeforeArtifactCommit: true },
      forbiddenPublicClasses: ['raw API key', 'raw Claude token', 'raw MCP token', 'env vars', 'secret ref or provider locator', 'command arguments', 'message content', 'tool data', 'codex session id', 'real userData/workspace path'],
      artifacts: [{ name: GOLDEN_NAME, sha256: sha256(goldenContent) }],
    };
    return { artifacts: { [GOLDEN_NAME]: goldenContent, [MANIFEST_NAME]: stableJson(manifest) }, golden, manifest };
  } finally { restoreRuntime(); safeRemoveTemporaryRoot(temporaryRoot); }
}

async function generateNodeGolden(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputDir = path.resolve(options.outputDir || path.join(rootDir, OUTPUT_DIR));
  assertApprovedOutput(rootDir, outputDir, options);
  const bundle = await buildGoldenBundle({ rootDir });
  writeArtifacts(outputDir, bundle.artifacts);
  return bundle;
}

function parseArgs(argv) { if (argv.length) throw new Error(`unknown argument: ${argv[0]}`); return {}; }

if (require.main === module) {
  generateNodeGolden(parseArgs(process.argv.slice(2)))
    .then((bundle) => process.stdout.write(`${JSON.stringify({ ok: true, artifacts: Object.keys(bundle.artifacts) })}\n`))
    .catch((error) => { process.stderr.write(`${error instanceof BlockedError ? error.message : 'golden generation failed safely'}\n`); process.exitCode = 1; });
}

module.exports = { BlockedError, GENERATOR_VERSION, GOLDEN_NAME, NORMALIZATION_VERSION, buildGoldenBundle, executeScenarios, generateNodeGolden, normalizeGolden, parseArgs, safeRemoveTemporaryRoot };
