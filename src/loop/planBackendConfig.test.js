const { describe, it } = require('node:test');
const assert = require('node:assert/strict');

const {
  effectivePlanExecutionConfig,
  effectivePlanGenerationConfig,
  planExecutionAgentCliOperationFields,
  planExecutionConfigFields,
  planGenerationAgentCliOperationFields,
  planGenerationConfigFields,
} = require('./planBackendConfig');

describe('planBackendConfig generation/execution normalization', () => {
  it('prefers new generation fields over legacy CLI fields and falls back to legacy when absent', () => {
    const prioritized = effectivePlanGenerationConfig(
      {
        agent_cli_provider: 'codex',
        agent_cli_command: 'codex-default',
        codex_reasoning_effort: 'low',
      },
      {
        plan_generation_strategy: 'external-cli-structured',
        plan_generation_provider: 'claude',
        plan_generation_command: 'claude-plan',
        agent_cli_provider: 'opencode',
        codex_reasoning_effort: 'xhigh',
      },
    );

    assert.equal(prioritized.strategy, 'external-cli-structured');
    assert.equal(prioritized.provider, 'claude');
    assert.equal(prioritized.command, 'claude-plan');
    assert.equal(prioritized.codexReasoningEffort, null);

    const legacyFallback = effectivePlanGenerationConfig(
      {
        agent_cli_provider: 'opencode',
        agent_cli_command: 'opencode-plan',
      },
      {},
    );

    assert.equal(legacyFallback.strategy, 'external-cli-markdown');
    assert.equal(legacyFallback.provider, 'opencode');
    assert.equal(legacyFallback.command, 'opencode-plan');
    assert.equal(legacyFallback.codexReasoningEffort, null);
  });

  it('keeps Codex reasoning effort separate for generation and execution', () => {
    const defaults = {
      plan_generation_strategy: 'external-cli-markdown',
      plan_generation_provider: 'codex',
      plan_generation_command: 'codex-plan',
      plan_generation_codex_reasoning_effort: 'high',
      plan_execution_strategy: 'external-cli',
      plan_execution_provider: 'codex',
      plan_execution_command: 'codex-exec',
      plan_execution_codex_reasoning_effort: 'xhigh',
      codex_reasoning_effort: 'low',
    };

    const generation = effectivePlanGenerationConfig(defaults);
    const execution = effectivePlanExecutionConfig(defaults);

    assert.equal(generation.codexReasoningEffort, 'high');
    assert.equal(execution.codexReasoningEffort, 'xhigh');
    assert.deepEqual(planGenerationAgentCliOperationFields(generation), {
      agentCliProvider: 'codex',
      agentCliCommand: 'codex-plan',
      codexReasoningEffort: 'high',
    });
    assert.deepEqual(planExecutionAgentCliOperationFields(execution), {
      agentCliProvider: 'codex',
      agentCliCommand: 'codex-exec',
      codexReasoningEffort: 'xhigh',
    });
  });

  it('normalizes reasoning to null for non-Codex generation and execution providers', () => {
    const generation = planGenerationConfigFields({
      strategy: 'external-cli-structured',
      provider: 'claude',
      command: 'claude-plan',
      codexReasoningEffort: 'xhigh',
    });
    const execution = planExecutionConfigFields({
      strategy: 'external-cli',
      provider: 'opencode',
      command: 'opencode-run',
      codexReasoningEffort: 'high',
    });

    assert.equal(generation.codexReasoningEffort, null);
    assert.equal(generation.planGenerationCodexReasoningEffort, null);
    assert.equal(execution.codexReasoningEffort, null);
    assert.equal(execution.planExecutionCodexReasoningEffort, null);
  });

  it('does not read generation provider when resolving execution config', () => {
    const execution = effectivePlanExecutionConfig({
      plan_generation_strategy: 'external-cli-structured',
      plan_generation_provider: 'claude',
      plan_generation_command: 'claude-plan',
      plan_execution_strategy: 'external-cli',
    });

    assert.equal(execution.strategy, 'external-cli');
    assert.equal(execution.provider, 'codex');
    assert.equal(execution.command, '');
    assert.equal(execution.codexReasoningEffort, 'medium');
  });

  // 回归：plan 行的 legacy agent_cli_command（生成阶段回填，如 codex）不得在 execution provider
  // 已显式为 claude 时污染 execution command——否则会拼成 provider=claude + command=codex，
  // spawn 时用 claude 参数（--print）调用 codex 可执行文件而报「unexpected argument '--print'」。
  // 见 docs/progress/logs/20260705-100613_execute-P001.log。
  it('does not fall back to legacy agent_cli_command when execution provider is explicit', () => {
    const execution = effectivePlanExecutionConfig(
      {},
      {
        plan_execution_strategy: 'external-cli',
        plan_execution_provider: 'claude',
        plan_execution_command: '',
        agent_cli_provider: 'codex',
        agent_cli_command: 'codex',
      },
    );

    assert.equal(execution.strategy, 'external-cli');
    assert.equal(execution.provider, 'claude');
    assert.equal(execution.command, '');
    assert.equal(execution.codexReasoningEffort, null);
  });

  it('does not fall back to legacy agent_cli_command when generation provider is explicit', () => {
    const generation = effectivePlanGenerationConfig(
      {},
      {
        plan_generation_strategy: 'external-cli-structured',
        plan_generation_provider: 'claude',
        plan_generation_command: '',
        agent_cli_provider: 'codex',
        agent_cli_command: 'codex',
      },
    );

    assert.equal(generation.strategy, 'external-cli-structured');
    assert.equal(generation.provider, 'claude');
    assert.equal(generation.command, '');
    assert.equal(generation.codexReasoningEffort, null);
  });

  it('still falls back to legacy agent_cli_* together when execution provider is absent', () => {
    const execution = effectivePlanExecutionConfig(
      {},
      {
        plan_execution_strategy: 'external-cli',
        agent_cli_provider: 'opencode',
        agent_cli_command: 'opencode-run',
      },
    );

    assert.equal(execution.provider, 'opencode');
    assert.equal(execution.command, 'opencode-run');
  });

  it('supports builtin generation config while keeping external CLI operation mapping guarded', () => {
    const builtin = effectivePlanGenerationConfig(
      {},
      {
        plan_generation_strategy: 'builtin-llm-structured',
        plan_generation_provider: 'openai',
        plan_generation_model: 'gpt-4o',
        plan_generation_codex_reasoning_effort: 'xhigh',
      },
    );

    assert.equal(builtin.strategy, 'builtin-llm-structured');
    assert.equal(builtin.provider, 'openai');
    assert.equal(builtin.model, 'gpt-4o');
    assert.equal(builtin.command, '');
    assert.equal(builtin.codexReasoningEffort, null);
    assert.throws(
      () => planGenerationAgentCliOperationFields(builtin),
      /does not use an external CLI/,
    );
  });
});

describe('planBackendConfig Claude custom connection fields', () => {
  it('reads Claude baseUrl/authToken/model from intake and falls back to defaults', () => {
    const generation = effectivePlanGenerationConfig(
      {
        plan_generation_claude_base_url: 'https://default.example.com',
        plan_generation_claude_auth_token: 'sk-default',
        plan_generation_claude_model: 'claude-default',
      },
      {
        plan_generation_strategy: 'external-cli-markdown',
        plan_generation_provider: 'claude',
        plan_generation_claude_base_url: 'https://intake.example.com',
        plan_generation_claude_model: 'claude-intake',
      },
    );

    // intake 优先于 defaults；intake 没有的字段（authToken）回退到 defaults。
    assert.equal(generation.claudeBaseUrl, 'https://intake.example.com');
    assert.equal(generation.claudeAuthToken, 'sk-default');
    assert.equal(generation.claudeModel, 'claude-intake');
    // 蛇形键同步输出。
    assert.equal(generation.planGenerationClaudeBaseUrl, 'https://intake.example.com');
    assert.equal(generation.planGenerationClaudeAuthToken, 'sk-default');
    assert.equal(generation.planGenerationClaudeModel, 'claude-intake');
  });

  it('execution config reads Claude fields independently from generation', () => {
    const execution = effectivePlanExecutionConfig(
      {
        plan_execution_claude_base_url: 'https://exec.example.com',
        plan_execution_claude_auth_token: 'sk-exec',
        plan_execution_claude_model: 'claude-exec',
      },
      {
        plan_execution_strategy: 'external-cli',
        plan_execution_provider: 'claude',
      },
    );

    assert.equal(execution.claudeBaseUrl, 'https://exec.example.com');
    assert.equal(execution.claudeAuthToken, 'sk-exec');
    assert.equal(execution.claudeModel, 'claude-exec');
  });

  it('returns empty Claude fields when none configured', () => {
    const generation = effectivePlanGenerationConfig({}, {
      plan_generation_strategy: 'external-cli-markdown',
      plan_generation_provider: 'claude',
    });

    assert.equal(generation.claudeBaseUrl, '');
    assert.equal(generation.claudeAuthToken, '');
    assert.equal(generation.claudeModel, '');
  });

  it('transparently passes Claude fields through agentCliOperationFieldsForPlanBackend', () => {
    const generation = effectivePlanGenerationConfig({}, {
      plan_generation_strategy: 'external-cli-markdown',
      plan_generation_provider: 'claude',
      plan_generation_claude_base_url: 'https://plan.example.com',
      plan_generation_claude_auth_token: 'sk-plan',
      plan_generation_claude_model: 'claude-plan',
    });
    const operationFields = planGenerationAgentCliOperationFields(generation);

    // compactDefinedFields 会保留非空 Claude 字段，让 task execution 链路能透传到 spawn env。
    assert.equal(operationFields.agentCliProvider, 'claude');
    assert.equal(operationFields.claudeBaseUrl, 'https://plan.example.com');
    assert.equal(operationFields.claudeAuthToken, 'sk-plan');
    assert.equal(operationFields.claudeModel, 'claude-plan');
  });

  it('strips empty Claude fields from agentCliOperationFields (compactDefinedFields)', () => {
    const generation = effectivePlanGenerationConfig({}, {
      plan_generation_strategy: 'external-cli-markdown',
      plan_generation_provider: 'claude',
    });
    const operationFields = planGenerationAgentCliOperationFields(generation);

    // 空 Claude 字段不应出现在 operation fields 中（避免污染 operation 对象）。
    assert.equal(operationFields.claudeBaseUrl, undefined);
    assert.equal(operationFields.claudeAuthToken, undefined);
    assert.equal(operationFields.claudeModel, undefined);
  });
});
