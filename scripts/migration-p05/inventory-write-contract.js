'use strict';

const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.resolve(__dirname, '../..');
const OUTPUT_PATH = 'docs/migration/p05/write-contract.json';
const FORMAT_VERSION = 1;
const CONTRACT_VERSION = 'p05-project-config-write-contract-v1';

const SOURCE_MARKERS = Object.freeze({
  'src/main.js': [
    "ipcMain.handle('projects:create'",
    "ipcMain.handle('projects:update'",
    "ipcMain.handle('projects:delete'",
    "ipcMain.handle('loop:configure'",
    "return loop.snapshot(projectId)",
  ],
  'src/intakeService.js': [
    'createProject(input = {})',
    'INSERT INTO projects (name, workspace_path, description, created_at, updated_at)',
    'this.loop.ensureProjectState(id)',
    'return this.loop.snapshot(id)',
  ],
  'src/loopService.js': [
    'SELECT * FROM projects ORDER BY updated_at DESC, id DESC',
    'ensureProjectState(projectId)',
    'hasRuntimeConfigInput(input = {})',
    'configure(projectId, config = {})',
    'UPDATE project_states',
    'snapshot(projectId = null)',
  ],
  'src/loop/snapshots.js': [
    'function snapshot(service, helpers, projectId = null)',
    'function emptySnapshot(projects, mcp = null)',
    'ORDER BY plans.sort_order ASC, plans.created_at ASC, plans.id ASC',
    'ORDER BY created_at DESC, id DESC',
  ],
  'src/database.js': [
    'CREATE TABLE IF NOT EXISTS settings',
    'CREATE TABLE IF NOT EXISTS projects',
    'CREATE TABLE IF NOT EXISTS project_states',
    'setSetting(key, value)',
    'getSettings(prefix = \'\')',
  ],
});

const PROJECT_STATE_FIELDS = Object.freeze([
  field('interval_seconds', ['intervalSeconds'], 'number', 5, 'fallback-on-falsy', true, 'public'),
  field('validation_command', ['validationCommand', 'validation_command'], 'string', '', 'explicit-null-clears', true, 'command-sensitive'),
  field('project_prompt', ['projectPrompt', 'project_prompt', 'prompt'], 'string', '', 'explicit-null-clears', true, 'user-content'),
  field('agent_cli_provider', ['agentCliProvider', 'agent_cli_provider', 'cliProvider', 'cli_provider', 'cliBackend', 'cli_backend'], 'string', 'codex', 'trim-lower-invalid-to-codex', true, 'public'),
  field('agent_cli_command', ['agentCliCommand', 'agent_cli_command', 'cliCommand', 'cli_command', 'cliPath', 'cli_path'], 'string', '', 'trim-empty-allowed', true, 'command-sensitive'),
  field('codex_reasoning_effort', ['codexReasoningEffort', 'codex_reasoning_effort', 'codexThinkingDepth', 'codex_thinking_depth', 'reasoningEffort', 'reasoning_effort', 'thinkingDepth', 'thinking_depth'], 'nullable-string', 'medium', 'low-medium-high-xhigh; invalid-to-medium; null outside codex', true, 'public'),
  field('plan_generation_strategy', ['planGenerationStrategy', 'plan_generation_strategy'], 'string', 'external-cli-markdown', 'normalized-enum', true, 'public'),
  field('plan_generation_provider', ['planGenerationProvider', 'plan_generation_provider'], 'nullable-string', null, 'normalized-provider', true, 'public'),
  field('plan_generation_command', ['planGenerationCommand', 'plan_generation_command'], 'string', '', 'trim-empty-allowed', true, 'command-sensitive'),
  field('plan_generation_model', ['planGenerationModel', 'plan_generation_model'], 'string', '', 'trim-empty-allowed', true, 'configuration-sensitive'),
  field('plan_generation_codex_reasoning_effort', ['planGenerationCodexReasoningEffort', 'plan_generation_codex_reasoning_effort'], 'nullable-string', null, 'normalized-enum', true, 'public'),
  field('plan_generation_claude_base_url', ['planGenerationClaudeBaseUrl', 'plan_generation_claude_base_url'], 'string', '', 'trim-empty-allowed', true, 'endpoint-sensitive'),
  field('plan_generation_claude_auth_token', ['planGenerationClaudeAuthToken', 'plan_generation_claude_auth_token'], 'string', '', 'empty-clears', true, 'secret'),
  field('plan_generation_claude_model', ['planGenerationClaudeModel', 'plan_generation_claude_model'], 'string', '', 'trim-empty-allowed', true, 'configuration-sensitive'),
  field('plan_generation_claude_config_id', ['planGenerationClaudeConfigId', 'plan_generation_claude_config_id'], 'integer', 0, 'non-positive-to-zero', true, 'reference-sensitive'),
  field('plan_execution_strategy', ['planExecutionStrategy', 'plan_execution_strategy'], 'string', 'external-cli', 'normalized-enum', true, 'public'),
  field('plan_execution_provider', ['planExecutionProvider', 'plan_execution_provider'], 'nullable-string', null, 'normalized-provider', true, 'public'),
  field('plan_execution_command', ['planExecutionCommand', 'plan_execution_command'], 'string', '', 'trim-empty-allowed', true, 'command-sensitive'),
  field('plan_execution_model', ['planExecutionModel', 'plan_execution_model'], 'string', '', 'trim-empty-allowed', true, 'configuration-sensitive'),
  field('plan_execution_codex_reasoning_effort', ['planExecutionCodexReasoningEffort', 'plan_execution_codex_reasoning_effort'], 'nullable-string', null, 'normalized-enum', true, 'public'),
  field('plan_execution_claude_base_url', ['planExecutionClaudeBaseUrl', 'plan_execution_claude_base_url'], 'string', '', 'trim-empty-allowed', true, 'endpoint-sensitive'),
  field('plan_execution_claude_auth_token', ['planExecutionClaudeAuthToken', 'plan_execution_claude_auth_token'], 'string', '', 'empty-clears', true, 'secret'),
  field('plan_execution_claude_model', ['planExecutionClaudeModel', 'plan_execution_claude_model'], 'string', '', 'trim-empty-allowed', true, 'configuration-sensitive'),
  field('plan_execution_claude_config_id', ['planExecutionClaudeConfigId', 'plan_execution_claude_config_id'], 'integer', 0, 'non-positive-to-zero', true, 'reference-sensitive'),
  field('env_vars', ['envVars'], 'json-array', '', 'only-array-writes; names-trimmed; empty-names-dropped', true, 'secret'),
]);

const MCP_SETTING_FIELDS = Object.freeze([
  setting('mcp.enabled', ['enabled', 'mcpEnabled', 'mcp_enabled'], 'boolean-string', 'true', true, 'public'),
  setting('mcp.transport', ['transport', 'mcpTransport', 'mcp_transport'], 'string', 'http', true, 'public'),
  setting('mcp.host', ['host', 'mcpHost', 'mcp_host'], 'string', '127.0.0.1', true, 'endpoint-sensitive'),
  setting('mcp.port', ['port', 'mcpPort', 'mcp_port'], 'integer-string', '43847', true, 'public'),
  setting('mcp.path', ['path', 'mcpPath', 'mcp_path'], 'string', '/mcp', true, 'public'),
  setting('mcp.authToken', ['mcpAuthToken', 'mcp_auth_token', 'authToken'], 'string', '<generated-secret>', true, 'secret'),
  setting('mcp.portExplicit', [], 'boolean-string', 'false', true, 'internal'),
]);

const RELATION_POLICIES = Object.freeze([
  relation('project_states', 'project_id', 'cascade', 'one-to-one owned state', 'Node explicitly deletes; P04 FK CASCADE'),
  relation('requirements', 'project_id', 'managed-cascade', 'delete explicitly after attachment/link checks and before the project; FK RESTRICT prevents accidental implicit loss', 'Node explicitly deletes; P04 FK RESTRICT requires child-first application coordination'),
  relation('feedback', 'project_id', 'managed-cascade', 'delete explicitly after attachment/link checks and before requirements/project', 'Node explicitly deletes; P04 FK RESTRICT requires child-first application coordination'),
  relation('attachments', 'project_id', 'restrict', 'external file cleanup cannot be represented by a database cascade', 'Node deletes only rows; P04 FK RESTRICT'),
  relation('intake_plan_links', 'project_id/plan_id', 'cascade-via-plan; restrict-unexplained-orphan', 'valid links follow the explicitly deleted plan; invalid or cross-project history blocks deletion', 'Node omits this table; P04 project FK RESTRICT and plan FK CASCADE'),
  relation('plans', 'project_id', 'managed-cascade', 'delete explicitly after link audit; plan files remain subject to the file policy', 'Node deletes tasks then plans; P04 FK RESTRICT requires child-first application coordination'),
  relation('plan_tasks', 'plan_id', 'cascade-via-plan', 'task is owned by its plan', 'Node explicit child-first delete; P04 FK CASCADE'),
  relation('events', 'project_id', 'cascade', 'derived project history', 'Node explicit delete; P04 FK CASCADE'),
  relation('scan_files', 'project_id', 'cascade', 'derived scan cache', 'Node explicit delete; P04 FK CASCADE'),
  relation('scripts', 'project_id', 'restrict', 'script body/path/process configuration is external-effect capable', 'Node omits this table; P04 FK RESTRICT'),
  relation('executors', 'project_id', 'restrict', 'executor process configuration is external-effect capable', 'Node omits this table; P04 FK RESTRICT'),
  relation('conversations', 'project_id', 'cascade', 'project-owned conversation', 'Node omits this table; P04 FK CASCADE'),
  relation('chat_messages', 'project_id/conversation_id', 'cascade', 'message is owned by project conversation', 'Node omits this table; P04 FK CASCADE'),
  relation('operations', 'project_id', 'restrict', 'active or historical operation must remain referentially valid', 'P04 schema extension; deletion requires no referencing operation'),
  relation('terminal/runtime', 'project_id', 'dispose-before-commit', 'in-memory/external process state must be stopped before database deletion', 'Node disposes terminal and stops loop before row deletion'),
  relation('settings', 'global key', 'retain', 'settings are global rather than project-owned', 'never delete settings during project deletion'),
  relation('ai_configs/claude_cli_configs', 'nullable project_id', 'retain-global-restrict-scoped', 'global configuration survives; historical scoped rows must be audited', 'P04 defers global scope semantics'),
]);

function field(column, inputAliases, type, defaultValue, emptyRule, versionParticipant, sensitivity) {
  return {
    column,
    inputAliases,
    type,
    default: defaultValue,
    emptyRule,
    versionParticipant,
    businessIdempotencyKey: `project:<project_id>:project_state:${column}:<normalized-value>`,
    returnPolicy: returnPolicy(sensitivity),
    sensitivity,
  };
}

function setting(key, inputAliases, type, defaultValue, versionParticipant, sensitivity) {
  return {
    key,
    inputAliases,
    type,
    default: defaultValue,
    storage: 'String(value)',
    emptyRule: key === 'mcp.authToken'
      ? 'explicit empty clears; omitted retains'
      : 'explicit empty is normalized to the field default; omitted retains',
    versionParticipant,
    businessIdempotencyKey: `session:settings:${key}:<normalized-value>`,
    returnPolicy: returnPolicy(sensitivity),
    sensitivity,
  };
}

function relation(table, key, decision, rationale, compatibility) {
  return { table, key, decision, rationale, compatibility };
}

function returnPolicy(sensitivity) {
  if (sensitivity === 'secret') return 'never-return-raw; presence/mask only';
  if (['command-sensitive', 'endpoint-sensitive', 'configuration-sensitive', 'reference-sensitive'].includes(sensitivity)) return 'authorized-project-snapshot-only; redact from fixtures/logs/errors';
  if (sensitivity === 'user-content') return 'authorized-project-snapshot-only';
  if (sensitivity === 'internal') return 'not-in-public-dto';
  return 'return-in-compatible-snapshot';
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function stableJson(value) {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function locateMarker(source, marker, sourcePath) {
  const index = source.indexOf(marker);
  if (index < 0) throw new Error(`write contract source marker missing: ${sourcePath}: ${marker}`);
  return 1 + source.slice(0, index).split('\n').length - 1;
}

function sourceEvidence(rootDir = ROOT) {
  return Object.entries(SOURCE_MARKERS).map(([sourcePath, markers]) => {
    const bytes = fs.readFileSync(path.join(rootDir, sourcePath));
    const source = bytes.toString('utf8');
    return {
      path: sourcePath,
      sha256: sha256(bytes),
      anchors: markers.map((marker) => ({ marker, line: locateMarker(source, marker, sourcePath) })),
    };
  });
}

function prerequisiteEvidence(rootDir = ROOT) {
  const artifacts = ['docs/migration/p04/schema-inventory.json', 'fixtures/migration/p04/manifest.json'];
  return artifacts.map((artifact) => {
    const bytes = fs.readFileSync(path.join(rootDir, artifact));
    return { path: artifact, sha256: sha256(bytes) };
  });
}

function buildContract(rootDir = ROOT) {
  const evidence = sourceEvidence(rootDir);
  const prerequisites = prerequisiteEvidence(rootDir);
  return {
    schemaVersion: FORMAT_VERSION,
    version: CONTRACT_VERSION,
    status: 'frozen-with-explicit-p04-delete-blockers',
    purpose: 'Project/Config Node write behavior and post-mutation AppSnapshot compatibility contract for the P05 Go migration',
    evidence,
    prerequisites: {
      policy: 'P04 schema/checksum/audit/owner gates must pass before any writer opens a fixture copy',
      artifacts: prerequisites,
      ownerRule: 'Node/sql.js and Go must never hold the same database copy concurrently; Node must close before handoff',
    },
    mutationAtomicity: {
      target: 'one top-level transaction per create/update/configure/reset/delete; commit before snapshot reread',
      observedNodeLimitation: 'legacy AppDatabase persists each statement; create/update/configure/delete are multi-statement and not atomic',
      migrationRule: 'preserve values/errors/snapshot semantics while strengthening atomicity; never publish a snapshot or event before commit',
    },
    mutations: [
      {
        id: 'projects:create',
        projectIdInput: 'server-generated integer',
        inputs: {
          name: 'String(input.name || "").trim(); empty falls back to first non-empty workspacePath line (trimmed, max 80), then 未命名项目',
          workspace_path: 'workspacePath only; falsy becomes empty string',
          description: 'falsy becomes empty string',
          autoRun: 'strict boolean true starts runtime after persistence',
          runtimeConfig: 'any frozen project-state input alias invokes configure after the default state row is created',
        },
        writes: ['projects INSERT', 'project_states INSERT OR IGNORE', 'optional projects UPDATE', 'optional project_states UPDATE'],
        timestamps: 'created_at and updated_at are UTC ISO strings; configure obtains independent UTC values',
        businessIdempotency: 'none in Node: repeating the same payload creates another project; Go requires Idempotency-Key replay protection and must not deduplicate by name alone',
        success: 'commit, reread LoopService.snapshot(new id), return the complete AppSnapshot directly',
      },
      {
        id: 'projects:update',
        projectIdAliases: ['projectId', 'id'],
        inputs: {
          name: 'String(input.name || current.name).trim(); empty/falsy preserves current name',
          workspace_path: 'workspacePath ?? current.workspace_path; explicit empty string clears',
          description: 'description ?? current.description; explicit empty string clears; null preserves',
          runtimeConfig: 'if present, configure runs before the final projects UPDATE',
        },
        writes: ['optional configure writes', 'projects name/workspace_path/description/updated_at UPDATE'],
        businessIdempotency: 'same values still update updated_at in Node; transport retry must use Idempotency-Key to avoid a second logical mutation',
        success: 'commit, reread complete snapshot for project id',
      },
      {
        id: 'loop:configure',
        projectIdAliases: ['projectId', 'id'],
        inputs: {
          workspace_path: 'workspacePath ?? current workspace_path',
          interval_seconds: 'Number(intervalSeconds || current.interval_seconds || 5); zero/falsy preserves current',
          mcp: 'only explicitly present aliases are normalized and written as strings; explicit port also sets mcp.portExplicit=true',
          projectState: 'see configFields; omitted fields preserve current values',
        },
        writes: ['optional settings INSERT OR REPLACE', 'projects workspace_path/updated_at UPDATE', 'project_states selected fields/updated_at UPDATE', 'optional unfinished plan execution config synchronization'],
        businessIdempotency: 'same payload is not a no-op in Node because updated_at changes; Go replay uses Idempotency-Key, while a fresh request with the same normalized target may return a reread without incrementing version',
        success: 'commit, reread complete snapshot for project id; MCP restart scheduling is outside the database transaction and not part of the snapshot contract',
      },
      {
        id: 'projects:delete',
        projectIdAliases: ['projectId', 'id'],
        preconditions: ['project exists', 'runtime state is not running', 'all RESTRICT relations have zero rows', 'database owner/schema gates remain valid'],
        runningError: { code: 'PROJECT_RUNNING', legacyMessage: '请先停止该项目循环再删除' },
        relationError: { code: 'PROJECT_RELATION_CONFLICT', rule: 'include relation field names/count categories only; never row data, paths, commands, env or secrets' },
        missingBehavior: { code: 'PROJECT_NOT_FOUND', legacyMessage: '项目不存在', repeatedDelete: 'same Idempotency-Key replays original success; otherwise PROJECT_NOT_FOUND' },
        writes: ['dispose runtime/terminal', 'transactional CASCADE relations', 'project_states/projects delete'],
        success: 'commit, reread LoopService.snapshot(null); activeProjectId/activeProject/state are null and projects remain sorted',
        compatibilityBlocker: 'legacy Node omits several P04 relations and can leave orphans; Go must follow relationPolicies rather than copying the omission',
      },
    ],
    configFields: {
      versionContract: {
        initial: 1,
        participants: ['settings.version', 'project_states.version'],
        compareAndSwap: 'expected version is mandatory for Go mutations; exactly one increment on a fresh successful logical mutation',
        replay: 'Idempotency-Key replay returns the original committed result/version without increment',
        sameTargetFreshRequest: 'may be treated as business-idempotent and return the current reread without increment',
        errors: { missing: 'VERSION_REQUIRED', stale: 'VERSION_CONFLICT', future: 'VERSION_CONFLICT' },
        note: 'version columns are P04 schema-v1 additions and are absent from the legacy Node snapshot unless the row source contains them',
      },
      settingsStorageContract: {
        readOne: 'getSetting(key, fallback=null) returns the stored string or the caller fallback without coercing the fallback',
        readMany: 'getSettings(prefix) returns an object built from key/value rows ordered by key ASC',
        write: 'setSetting uses INSERT OR REPLACE and String(value); arbitrary setting keys are not exposed by P05 APIs',
        allowlist: MCP_SETTING_FIELDS.map((item) => item.key),
      },
      projectState: PROJECT_STATE_FIELDS,
      settings: MCP_SETTING_FIELDS,
    },
    relationPolicies: RELATION_POLICIES,
    snapshot: {
      assembler: 'LoopService.snapshot -> loop/snapshots.snapshot',
      commitRule: 'always query repository again after commit; never assemble success from request values or stale in-memory rows',
      missingProject: 'same complete empty snapshot shape as snapshot(null), with the current sorted project list',
      topLevelFields: ['activeProjectId', 'activeProject', 'projects', 'mcp', 'state', 'requirements', 'feedback', 'attachments', 'plans', 'tasks', 'events', 'scans', 'scanSummary', 'scripts', 'executors', 'terminals', 'activeOperation', 'activeOperations', 'lastOperation'],
      naming: 'top-level historical camelCase is retained; database/domain rows remain snake_case',
      nullability: 'null and missing are distinct; empty collections remain present; unknown fields fail golden comparison',
      projectOrder: ['updated_at DESC', 'id DESC'],
      collectionOrder: {
        requirements: ['updated_at DESC'], feedback: ['updated_at DESC'], attachments: ['created_at DESC', 'id DESC'],
        plans: ['sort_order ASC', 'created_at ASC', 'id ASC'], tasks: ['plan order', 'sort_order ASC', 'id ASC'],
        events: ['id DESC', 'LIMIT 80'], scripts: ['sort_order ASC', 'id ASC'], executors: ['sort_order ASC', 'id ASC'],
      },
      scalarRules: ['SQLite integer booleans stay numeric where Node returns rows', 'runtime running is normalized to 0/1', 'nullable backend fields remain explicit null', 'UTC values use RFC3339/ISO-8601 Z form'],
    },
    errors: [
      { code: 'PROJECT_NOT_FOUND', legacyMessage: '项目不存在', mutations: ['update', 'configure', 'delete'] },
      { code: 'PROJECT_RUNNING', legacyMessage: '请先停止该项目循环再删除', mutations: ['delete'] },
      { code: 'WORKSPACE_IN_USE', legacyMessagePattern: '工作区正在被项目「…」使用，请先停止对应循环', mutations: ['configure', 'update'] },
      { code: 'PROJECT_RELATION_CONFLICT', legacyMessage: null, mutations: ['delete'], note: 'stable Go error introduced to fail closed against P04 RESTRICT relations' },
      { code: 'VERSION_REQUIRED', legacyMessage: null, mutations: ['configure', 'update'] },
      { code: 'VERSION_CONFLICT', legacyMessage: null, mutations: ['configure', 'update'] },
      { code: 'IDEMPOTENCY_KEY_REUSED', legacyMessage: null, mutations: ['create', 'update', 'configure', 'delete'] },
    ],
    sensitiveData: {
      neverReturnRaw: ['Claude auth tokens', 'MCP auth token', 'API tokens', 'env_vars values', 'secret_refs', 'command environment'],
      fixturesAndLogs: 'store field names and deterministic redaction placeholders only; no raw command, token, env value, userData path, database row, or unauthorized absolute path',
      compatibleMasks: ['*_has_claude_auth_token boolean', 'legacy ····suffix masks may be read from Node but golden normalization replaces the suffix', 'mcp.hasAuthToken boolean'],
    },
    golden: {
      generator: 'scripts/migration-p05/generate-node-golden.js',
      scenarios: ['create', 'duplicate-create', 'delete-duplicate-create', 'update', 'configure', 'duplicate-configure', 'missing-update', 'missing-delete', 'running-delete', 'delete', 'duplicate-delete'],
      normalization: 'only generator-owned temporary root, UTC timestamps, auto IDs and sensitive values are replaced; references share one mapping',
      comparison: 'deep strict comparison of every key/type/null/missing/value/order; no ignored or unknown fields',
      handoff: 'close sql.js and release the owner lock before Go opens an independently reset copy',
    },
  };
}

function writeContract(options = {}) {
  const rootDir = path.resolve(options.rootDir || ROOT);
  const outputPath = path.resolve(rootDir, options.outputPath || OUTPUT_PATH);
  const expected = path.resolve(rootDir, OUTPUT_PATH);
  if (outputPath !== expected && !options.allowExternalOutput) throw new Error('write contract output must remain in docs/migration/p05');
  fs.mkdirSync(path.dirname(outputPath), { recursive: true });
  fs.writeFileSync(outputPath, stableJson(buildContract(rootDir)), 'utf8');
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
    const contract = buildContract(ROOT);
    if (options.write) {
      const outputPath = writeContract();
      process.stdout.write(`${JSON.stringify({ ok: true, output: path.relative(ROOT, outputPath), sha256: sha256(stableJson(contract)) })}\n`);
    } else {
      process.stdout.write(stableJson(contract));
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
  RELATION_POLICIES,
  SOURCE_MARKERS,
  buildContract,
  parseArgs,
  sha256,
  stableJson,
  writeContract,
};
