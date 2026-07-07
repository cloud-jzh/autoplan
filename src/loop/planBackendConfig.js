const {
  DEFAULT_AGENT_CLI_PROVIDER,
  normalizeAgentCliCommand,
  normalizeAgentCliProvider,
} = require('../agentCli');

const DEFAULT_PLAN_GENERATION_STRATEGY = 'external-cli-markdown';
const DEFAULT_PLAN_EXECUTION_STRATEGY = 'external-cli';
const PLAN_GENERATION_STRATEGIES = new Set([
  DEFAULT_PLAN_GENERATION_STRATEGY,
  'external-cli-structured',
  'builtin-llm-structured',
]);
const PLAN_EXECUTION_STRATEGIES = new Set([
  DEFAULT_PLAN_EXECUTION_STRATEGY,
  'builtin-llm',
]);
const BUILTIN_LLM_PROVIDERS = new Set(['openai', 'deepseek', 'anthropic']);
const CODEX_REASONING_EFFORTS = new Set(['low', 'medium', 'high', 'xhigh']);
const DEFAULT_CODEX_REASONING_EFFORT = 'medium';

const PLAN_GENERATION_STRATEGY_KEYS = Object.freeze(['planGenerationStrategy', 'plan_generation_strategy']);
const PLAN_GENERATION_PROVIDER_KEYS = Object.freeze(['planGenerationProvider', 'plan_generation_provider']);
const PLAN_GENERATION_COMMAND_KEYS = Object.freeze(['planGenerationCommand', 'plan_generation_command']);
const PLAN_GENERATION_MODEL_KEYS = Object.freeze(['planGenerationModel', 'plan_generation_model']);
const PLAN_GENERATION_CODEX_REASONING_EFFORT_KEYS = Object.freeze([
  'planGenerationCodexReasoningEffort',
  'plan_generation_codex_reasoning_effort',
]);
// Claude CLI 自定义连接配置（注入为 ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL 环境变量）。
// 与 builtin-llm 的 model 字段语义分离——后者走内置 LLM SDK，前者走 claude 子进程的环境变量覆盖路径，
// 两者互不干扰。
const PLAN_GENERATION_CLAUDE_BASE_URL_KEYS = Object.freeze([
  'planGenerationClaudeBaseUrl',
  'plan_generation_claude_base_url',
]);
const PLAN_GENERATION_CLAUDE_AUTH_TOKEN_KEYS = Object.freeze([
  'planGenerationClaudeAuthToken',
  'plan_generation_claude_auth_token',
]);
const PLAN_GENERATION_CLAUDE_MODEL_KEYS = Object.freeze([
  'planGenerationClaudeModel',
  'plan_generation_claude_model',
]);
// Claude CLI 多配置（需求 #93）：选中的 claude_cli_configs.id；0 表示未选（回退默认配置或内联字段）。
const PLAN_GENERATION_CLAUDE_CONFIG_ID_KEYS = Object.freeze([
  'planGenerationClaudeConfigId',
  'plan_generation_claude_config_id',
]);
const PLAN_EXECUTION_STRATEGY_KEYS = Object.freeze(['planExecutionStrategy', 'plan_execution_strategy']);
const PLAN_EXECUTION_PROVIDER_KEYS = Object.freeze(['planExecutionProvider', 'plan_execution_provider']);
const PLAN_EXECUTION_COMMAND_KEYS = Object.freeze(['planExecutionCommand', 'plan_execution_command']);
const PLAN_EXECUTION_MODEL_KEYS = Object.freeze(['planExecutionModel', 'plan_execution_model']);
const PLAN_EXECUTION_CODEX_REASONING_EFFORT_KEYS = Object.freeze([
  'planExecutionCodexReasoningEffort',
  'plan_execution_codex_reasoning_effort',
]);
const PLAN_EXECUTION_CLAUDE_BASE_URL_KEYS = Object.freeze([
  'planExecutionClaudeBaseUrl',
  'plan_execution_claude_base_url',
]);
const PLAN_EXECUTION_CLAUDE_AUTH_TOKEN_KEYS = Object.freeze([
  'planExecutionClaudeAuthToken',
  'plan_execution_claude_auth_token',
]);
const PLAN_EXECUTION_CLAUDE_MODEL_KEYS = Object.freeze([
  'planExecutionClaudeModel',
  'plan_execution_claude_model',
]);
const PLAN_EXECUTION_CLAUDE_CONFIG_ID_KEYS = Object.freeze([
  'planExecutionClaudeConfigId',
  'plan_execution_claude_config_id',
]);
const LEGACY_AGENT_CLI_PROVIDER_KEYS = Object.freeze([
  'agentCliProvider',
  'agent_cli_provider',
  'cliProvider',
  'cli_provider',
  'cliBackend',
  'cli_backend',
]);
const LEGACY_AGENT_CLI_COMMAND_KEYS = Object.freeze([
  'agentCliCommand',
  'agent_cli_command',
  'cliCommand',
  'cli_command',
  'cliPath',
  'cli_path',
]);
const LEGACY_CODEX_REASONING_EFFORT_KEYS = Object.freeze([
  'codexReasoningEffort',
  'codex_reasoning_effort',
  'codexThinkingDepth',
  'codex_thinking_depth',
  'reasoningEffort',
  'reasoning_effort',
  'thinkingDepth',
  'thinking_depth',
]);
const PLAN_BACKEND_CONFIG_INPUT_KEYS = Object.freeze([
  ...PLAN_GENERATION_STRATEGY_KEYS,
  ...PLAN_GENERATION_PROVIDER_KEYS,
  ...PLAN_GENERATION_COMMAND_KEYS,
  ...PLAN_GENERATION_MODEL_KEYS,
  ...PLAN_GENERATION_CODEX_REASONING_EFFORT_KEYS,
  ...PLAN_GENERATION_CLAUDE_BASE_URL_KEYS,
  ...PLAN_GENERATION_CLAUDE_AUTH_TOKEN_KEYS,
  ...PLAN_GENERATION_CLAUDE_MODEL_KEYS,
  ...PLAN_GENERATION_CLAUDE_CONFIG_ID_KEYS,
  ...PLAN_EXECUTION_STRATEGY_KEYS,
  ...PLAN_EXECUTION_PROVIDER_KEYS,
  ...PLAN_EXECUTION_COMMAND_KEYS,
  ...PLAN_EXECUTION_MODEL_KEYS,
  ...PLAN_EXECUTION_CODEX_REASONING_EFFORT_KEYS,
  ...PLAN_EXECUTION_CLAUDE_BASE_URL_KEYS,
  ...PLAN_EXECUTION_CLAUDE_AUTH_TOKEN_KEYS,
  ...PLAN_EXECUTION_CLAUDE_MODEL_KEYS,
  ...PLAN_EXECUTION_CLAUDE_CONFIG_ID_KEYS,
]);

function effectivePlanGenerationConfig(defaults = {}, intake = {}) {
  const strategy = normalizePlanGenerationStrategy(
    firstConfigValue([intake, defaults], PLAN_GENERATION_STRATEGY_KEYS),
  );
  // legacy agent_cli_* 字段是「生成阶段实际命令」的回填快照（见 runCodex 返回的 agentCliCommand，
  // 默认即 provider 同名命令）。新流程里 generation/execution 已各自独立存配置，若 command 再逐字段
  // 回退到 legacy，会让 provider 取新字段、command 却落到 legacy（反映的是同一阶段的运行命令），
  // 在 generation 与 execution 使用不同 CLI 时（如 codex 生成、claude 执行）产生 provider↔command
  // 跨后端错配。故 legacy 回退改为「同一对象内整体绑定」：在同一 source 上，仅当 primary provider
  // 缺失而 legacy provider 存在时，provider 与 command 才同时从该 source 的 legacy 字段取；
  // primary provider 已显式时，command 只取同 source 的 primary 字段，避免跨后端错配。
  const { provider: providerValue, command: commandValue } = resolvePlanBackendProviderCommand(
    [intake, defaults],
    PLAN_GENERATION_PROVIDER_KEYS,
    PLAN_GENERATION_COMMAND_KEYS,
  );
  const provider = normalizePlanBackendProvider(providerValue, strategy);
  const command = normalizeAgentCliCommand(commandValue);
  const model = normalizeOptionalString(firstConfigValue([intake, defaults], PLAN_GENERATION_MODEL_KEYS)) || '';
  const codexReasoningEffort = provider === DEFAULT_AGENT_CLI_PROVIDER
    ? normalizeCodexReasoningEffort(firstConfigValue(
      [intake, defaults],
      PLAN_GENERATION_CODEX_REASONING_EFFORT_KEYS,
      LEGACY_CODEX_REASONING_EFFORT_KEYS,
    ))
    : null;
  // Claude 自定义连接配置（仅作为 spawn 环境变量注入用，归一化为字符串，不做 provider 校验）。
  const claudeBaseUrl = normalizeOptionalString(
    firstConfigValue([intake, defaults], PLAN_GENERATION_CLAUDE_BASE_URL_KEYS),
  ) || '';
  const claudeAuthToken = normalizeOptionalString(
    firstConfigValue([intake, defaults], PLAN_GENERATION_CLAUDE_AUTH_TOKEN_KEYS),
  ) || '';
  const claudeModel = normalizeOptionalString(
    firstConfigValue([intake, defaults], PLAN_GENERATION_CLAUDE_MODEL_KEYS),
  ) || '';
  const claudeConfigId = normalizeClaudeConfigId(
    firstConfigValue([intake, defaults], PLAN_GENERATION_CLAUDE_CONFIG_ID_KEYS),
  );
  return planGenerationConfigFields({
    strategy,
    provider,
    command,
    model,
    codexReasoningEffort,
    claudeBaseUrl,
    claudeAuthToken,
    claudeModel,
    claudeConfigId,
  });
}

function effectivePlanExecutionConfig(defaults = {}, plan = {}) {
  const strategy = normalizePlanExecutionStrategy(
    firstConfigValue([plan, defaults], PLAN_EXECUTION_STRATEGY_KEYS),
  );
  // 与 effectivePlanGenerationConfig 同理：legacy agent_cli_* 字段在新流程里被生成阶段回填为生成命令
  // （如 codex），若 execution 在 provider 已明确（如 claude）时仍逐字段回退到 legacy agent_cli_command，
  // 会得到「provider=claude + command=codex」的错配——spawn 时按 claude 拼装 --print 等参数却调用 codex
  // 可执行文件，codex 不认 --print 即报「unexpected argument '--print'」（见 P001 执行日志）。故 legacy
  // 回退改为同一对象内 provider+command 绑定：primary provider 已显式时，command 不再跨到 legacy。
  const { provider: providerValue, command: commandValue } = resolvePlanBackendProviderCommand(
    [plan, defaults],
    PLAN_EXECUTION_PROVIDER_KEYS,
    PLAN_EXECUTION_COMMAND_KEYS,
  );
  const provider = normalizePlanBackendProvider(providerValue, strategy);
  const command = normalizeAgentCliCommand(commandValue);
  const model = normalizeOptionalString(firstConfigValue([plan, defaults], PLAN_EXECUTION_MODEL_KEYS)) || '';
  const codexReasoningEffort = provider === DEFAULT_AGENT_CLI_PROVIDER
    ? normalizeCodexReasoningEffort(firstConfigValue(
      [plan, defaults],
      PLAN_EXECUTION_CODEX_REASONING_EFFORT_KEYS,
      LEGACY_CODEX_REASONING_EFFORT_KEYS,
    ))
    : null;
  const claudeBaseUrl = normalizeOptionalString(
    firstConfigValue([plan, defaults], PLAN_EXECUTION_CLAUDE_BASE_URL_KEYS),
  ) || '';
  const claudeAuthToken = normalizeOptionalString(
    firstConfigValue([plan, defaults], PLAN_EXECUTION_CLAUDE_AUTH_TOKEN_KEYS),
  ) || '';
  const claudeModel = normalizeOptionalString(
    firstConfigValue([plan, defaults], PLAN_EXECUTION_CLAUDE_MODEL_KEYS),
  ) || '';
  const claudeConfigId = normalizeClaudeConfigId(
    firstConfigValue([plan, defaults], PLAN_EXECUTION_CLAUDE_CONFIG_ID_KEYS),
  );
  return planExecutionConfigFields({
    strategy,
    provider,
    command,
    model,
    codexReasoningEffort,
    claudeBaseUrl,
    claudeAuthToken,
    claudeModel,
    claudeConfigId,
  });
}

// 在 sources 链上成对解析 (provider, command)，保持「同一对象内 legacy 整体回退」语义：
// 对每个 source 依次判定——若该 source 的 primary provider 字段存在，则 provider/command 都从该
// source 的 primary 字段取（command 缺失即空，由下游 defaultAgentCliCommand(provider) 兜底）；
// 若该 source 的 primary provider 缺失但 legacy provider 存在，则 provider/command 都从该 source
// 的 legacy 字段取；两者皆无则继续下一个 source。这样可避免「provider 取 A source 的 primary、
// command 却取 A source 的 legacy」这类跨后端错配。
function resolvePlanBackendProviderCommand(sources, providerKeys, commandKeys) {
  for (const source of sources) {
    const primaryProvider = readFirstOwnValue(source, providerKeys);
    if (primaryProvider !== undefined && primaryProvider !== null && primaryProvider !== '') {
      return {
        provider: primaryProvider,
        command: readFirstOwnValue(source, commandKeys),
      };
    }
    const legacyProvider = readFirstOwnValue(source, LEGACY_AGENT_CLI_PROVIDER_KEYS);
    if (legacyProvider !== undefined && legacyProvider !== null && legacyProvider !== '') {
      return {
        provider: legacyProvider,
        command: readFirstOwnValue(source, LEGACY_AGENT_CLI_COMMAND_KEYS),
      };
    }
  }
  return { provider: undefined, command: undefined };
}

function planGenerationAgentCliOperationFields(config = {}) {
  const normalized = planGenerationConfigFields({
    strategy: normalizePlanGenerationStrategy(config.strategy ?? config.planGenerationStrategy),
    provider: normalizePlanBackendProvider(config.provider ?? config.planGenerationProvider),
    command: normalizeAgentCliCommand(config.command ?? config.planGenerationCommand),
    model: normalizeOptionalString(config.model ?? config.planGenerationModel) || '',
    codexReasoningEffort: normalizeOptionalCodexReasoningEffort(
      config.codexReasoningEffort ?? config.planGenerationCodexReasoningEffort,
    ),
    claudeBaseUrl: normalizeOptionalString(config.claudeBaseUrl ?? config.planGenerationClaudeBaseUrl) || '',
    claudeAuthToken: normalizeOptionalString(config.claudeAuthToken ?? config.planGenerationClaudeAuthToken) || '',
    claudeModel: normalizeOptionalString(config.claudeModel ?? config.planGenerationClaudeModel) || '',
    claudeConfigId: normalizeClaudeConfigId(config.claudeConfigId ?? config.planGenerationClaudeConfigId),
  });
  if (!isExternalCliPlanGenerationStrategy(normalized.strategy)) {
    throw new Error(`plan generation strategy ${normalized.strategy} does not use an external CLI`);
  }
  return agentCliOperationFieldsForPlanBackend(normalized);
}

function planExecutionAgentCliOperationFields(config = {}) {
  const normalized = planExecutionConfigFields({
    strategy: normalizePlanExecutionStrategy(config.strategy ?? config.planExecutionStrategy),
    provider: normalizePlanBackendProvider(config.provider ?? config.planExecutionProvider),
    command: normalizeAgentCliCommand(config.command ?? config.planExecutionCommand),
    model: normalizeOptionalString(config.model ?? config.planExecutionModel) || '',
    codexReasoningEffort: normalizeOptionalCodexReasoningEffort(
      config.codexReasoningEffort ?? config.planExecutionCodexReasoningEffort,
    ),
    claudeBaseUrl: normalizeOptionalString(config.claudeBaseUrl ?? config.planExecutionClaudeBaseUrl) || '',
    claudeAuthToken: normalizeOptionalString(config.claudeAuthToken ?? config.planExecutionClaudeAuthToken) || '',
    claudeModel: normalizeOptionalString(config.claudeModel ?? config.planExecutionClaudeModel) || '',
    claudeConfigId: normalizeClaudeConfigId(config.claudeConfigId ?? config.planExecutionClaudeConfigId),
  });
  if (!isExternalCliPlanExecutionStrategy(normalized.strategy)) {
    throw new Error(`plan execution strategy ${normalized.strategy} does not use an external CLI`);
  }
  return agentCliOperationFieldsForPlanBackend(normalized);
}

function agentCliOperationFieldsForPlanBackend(config = {}) {
  const provider = normalizeAgentCliProvider(config.provider);
  const command = normalizeAgentCliCommand(config.command);
  const codexReasoningEffort = provider === DEFAULT_AGENT_CLI_PROVIDER
    ? normalizeCodexReasoningEffort(config.codexReasoningEffort)
    : null;
  return compactDefinedFields({
    agentCliProvider: provider,
    agentCliCommand: command,
    codexReasoningEffort: provider === DEFAULT_AGENT_CLI_PROVIDER ? codexReasoningEffort : undefined,
    // Claude 自定义连接字段透传，供 loopService 持久化到对应列、runCodex 取用拼装 spawn 环境变量。
    claudeBaseUrl: normalizeOptionalString(config.claudeBaseUrl),
    claudeAuthToken: normalizeOptionalString(config.claudeAuthToken),
    claudeModel: normalizeOptionalString(config.claudeModel),
    // claude_config_id：0 表示未选，省略以与空字符串字段保持一致；runCodex 解析时缺失即回退默认/内联。
    claudeConfigId: Number(config.claudeConfigId) > 0 ? Number(config.claudeConfigId) : undefined,
  });
}

function planGenerationConfigFields(config = {}) {
  const strategy = normalizePlanGenerationStrategy(config.strategy);
  const provider = normalizePlanBackendProvider(config.provider, strategy);
  const codexReasoningEffort = provider === DEFAULT_AGENT_CLI_PROVIDER
    ? normalizeCodexReasoningEffort(config.codexReasoningEffort)
    : null;
  const command = normalizeAgentCliCommand(config.command);
  const model = normalizeOptionalString(config.model) || '';
  const claudeBaseUrl = normalizeOptionalString(config.claudeBaseUrl) || '';
  const claudeAuthToken = normalizeOptionalString(config.claudeAuthToken) || '';
  const claudeModel = normalizeOptionalString(config.claudeModel) || '';
  const claudeConfigId = normalizeClaudeConfigId(config.claudeConfigId);
  return {
    strategy,
    provider,
    command,
    model,
    codexReasoningEffort,
    claudeBaseUrl,
    claudeAuthToken,
    claudeModel,
    claudeConfigId,
    planGenerationStrategy: strategy,
    planGenerationProvider: provider,
    planGenerationCommand: command,
    planGenerationModel: model,
    planGenerationCodexReasoningEffort: codexReasoningEffort,
    planGenerationClaudeBaseUrl: claudeBaseUrl,
    planGenerationClaudeAuthToken: claudeAuthToken,
    planGenerationClaudeModel: claudeModel,
    planGenerationClaudeConfigId: claudeConfigId,
  };
}

function planExecutionConfigFields(config = {}) {
  const strategy = normalizePlanExecutionStrategy(config.strategy);
  const provider = normalizePlanBackendProvider(config.provider, strategy);
  const codexReasoningEffort = provider === DEFAULT_AGENT_CLI_PROVIDER
    ? normalizeCodexReasoningEffort(config.codexReasoningEffort)
    : null;
  const command = normalizeAgentCliCommand(config.command);
  const model = normalizeOptionalString(config.model) || '';
  const claudeBaseUrl = normalizeOptionalString(config.claudeBaseUrl) || '';
  const claudeAuthToken = normalizeOptionalString(config.claudeAuthToken) || '';
  const claudeModel = normalizeOptionalString(config.claudeModel) || '';
  const claudeConfigId = normalizeClaudeConfigId(config.claudeConfigId);
  return {
    strategy,
    provider,
    command,
    model,
    codexReasoningEffort,
    claudeBaseUrl,
    claudeAuthToken,
    claudeModel,
    claudeConfigId,
    planExecutionStrategy: strategy,
    planExecutionProvider: provider,
    planExecutionCommand: command,
    planExecutionModel: model,
    planExecutionCodexReasoningEffort: codexReasoningEffort,
    planExecutionClaudeBaseUrl: claudeBaseUrl,
    planExecutionClaudeAuthToken: claudeAuthToken,
    planExecutionClaudeModel: claudeModel,
    planExecutionClaudeConfigId: claudeConfigId,
  };
}

function normalizePlanGenerationStrategy(value) {
  const strategy = normalizeOptionalLowerString(value);
  return PLAN_GENERATION_STRATEGIES.has(strategy) ? strategy : DEFAULT_PLAN_GENERATION_STRATEGY;
}

function normalizePlanExecutionStrategy(value) {
  const strategy = normalizeOptionalLowerString(value);
  return PLAN_EXECUTION_STRATEGIES.has(strategy) ? strategy : DEFAULT_PLAN_EXECUTION_STRATEGY;
}

function normalizePlanBackendProvider(value, strategy = null) {
  const normalized = normalizeOptionalLowerString(value);
  if (!normalized) return DEFAULT_AGENT_CLI_PROVIDER;
  if (isBuiltinPlanBackendStrategy(strategy)) {
    return BUILTIN_LLM_PROVIDERS.has(normalized) ? normalized : DEFAULT_AGENT_CLI_PROVIDER;
  }
  return normalizeAgentCliProvider(normalized);
}

function normalizeCodexReasoningEffort(value) {
  const effort = normalizeOptionalLowerString(value);
  return CODEX_REASONING_EFFORTS.has(effort) ? effort : DEFAULT_CODEX_REASONING_EFFORT;
}

function normalizeOptionalCodexReasoningEffort(value) {
  if (value === undefined || value === null || value === '') return null;
  return normalizeCodexReasoningEffort(value);
}

function normalizeOptionalString(value) {
  if (value === undefined || value === null) return undefined;
  const text = String(value).trim();
  return text || undefined;
}

/**
 * 归一化 claude_cli_configs.id：正整数原样返回（取 floor），其余（undefined/null/''/0/负数/NaN）归 0。
 * 0 为「未选配置」哨兵，触发 resolveDefault → 内联字段回退。
 */
function normalizeClaudeConfigId(value) {
  if (value === undefined || value === null || value === '') return 0;
  const num = Number(value);
  if (!Number.isFinite(num) || num <= 0) return 0;
  return Math.floor(num);
}

function normalizeOptionalLowerString(value) {
  const text = normalizeOptionalString(value);
  return text ? text.toLowerCase() : undefined;
}

function firstConfigValue(sources, primaryKeys, fallbackKeys = []) {
  for (const source of sources) {
    const value = readFirstOwnValue(source, primaryKeys);
    if (value !== undefined && value !== null && value !== '') return value;
    const fallbackValue = readFirstOwnValue(source, fallbackKeys);
    if (fallbackValue !== undefined && fallbackValue !== null && fallbackValue !== '') return fallbackValue;
  }
  return undefined;
}

function readFirstOwnValue(source, keys = []) {
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(source || {}, key)) return source[key];
  }
  return undefined;
}

function compactDefinedFields(fields) {
  const result = {};
  for (const [key, value] of Object.entries(fields || {})) {
    if (value !== undefined && value !== null && value !== '') result[key] = value;
  }
  return result;
}

function isExternalCliPlanGenerationStrategy(strategy) {
  const normalized = normalizePlanGenerationStrategy(strategy);
  return normalized === 'external-cli-markdown' || normalized === 'external-cli-structured';
}

function isExternalCliPlanExecutionStrategy(strategy) {
  return normalizePlanExecutionStrategy(strategy) === 'external-cli';
}

function isBuiltinPlanBackendStrategy(strategy) {
  const normalized = normalizeOptionalLowerString(strategy);
  return normalized === 'builtin-llm-structured' || normalized === 'builtin-llm';
}

module.exports = {
  DEFAULT_PLAN_EXECUTION_STRATEGY,
  DEFAULT_PLAN_GENERATION_STRATEGY,
  BUILTIN_LLM_PROVIDERS,
  PLAN_BACKEND_CONFIG_INPUT_KEYS,
  PLAN_EXECUTION_CLAUDE_AUTH_TOKEN_KEYS,
  PLAN_EXECUTION_CLAUDE_BASE_URL_KEYS,
  PLAN_EXECUTION_CLAUDE_CONFIG_ID_KEYS,
  PLAN_EXECUTION_CLAUDE_MODEL_KEYS,
  PLAN_EXECUTION_CODEX_REASONING_EFFORT_KEYS,
  PLAN_EXECUTION_COMMAND_KEYS,
  PLAN_EXECUTION_MODEL_KEYS,
  PLAN_EXECUTION_PROVIDER_KEYS,
  PLAN_EXECUTION_STRATEGIES,
  PLAN_EXECUTION_STRATEGY_KEYS,
  PLAN_GENERATION_CLAUDE_AUTH_TOKEN_KEYS,
  PLAN_GENERATION_CLAUDE_BASE_URL_KEYS,
  PLAN_GENERATION_CLAUDE_CONFIG_ID_KEYS,
  PLAN_GENERATION_CLAUDE_MODEL_KEYS,
  PLAN_GENERATION_CODEX_REASONING_EFFORT_KEYS,
  PLAN_GENERATION_COMMAND_KEYS,
  PLAN_GENERATION_MODEL_KEYS,
  PLAN_GENERATION_PROVIDER_KEYS,
  PLAN_GENERATION_STRATEGIES,
  PLAN_GENERATION_STRATEGY_KEYS,
  agentCliOperationFieldsForPlanBackend,
  effectivePlanExecutionConfig,
  effectivePlanGenerationConfig,
  isExternalCliPlanExecutionStrategy,
  isExternalCliPlanGenerationStrategy,
  isBuiltinPlanBackendStrategy,
  normalizePlanBackendProvider,
  normalizePlanExecutionStrategy,
  normalizePlanGenerationStrategy,
  planExecutionAgentCliOperationFields,
  planExecutionConfigFields,
  planGenerationAgentCliOperationFields,
  planGenerationConfigFields,
};
