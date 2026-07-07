import {
  agentCliProviderLabel,
  codexReasoningEffortLabel,
  planCliSummaryLabel,
  readCodexReasoningEffort,
} from './shared';
import {
  aiConfigFormForProviderChange,
  aiConfigInputFromForm,
  aiThinkingDepthLabel,
  agentCliDefaultCommand,
  agentCliOptionDetails,
  codexReasoningOptionDetails,
  createDefaultChatConfigForm,
  createDefaultPlanGenerationSelection,
  isBuiltinPlanExecutionStrategy,
  isBuiltinPlanGenerationStrategy,
  normalizeAiThinkingDepthInput,
  planBackendDefaultCommand,
  planBackendDefaultModel,
  planBackendProviderOptionsForStrategy,
  planExecutionStrategyOptions,
  planGenerationInputFromComposerSelection,
  planGenerationStrategyOptions,
  scopeFileOpenModeOptions,
  thinkingDepthOptionsForProvider,
} from '../utils/workspaceForms';
import type { ChatConfigFormState } from '../utils/workspaceForms';

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };

function expectEqual(actual: unknown, expected: unknown) {
  if (actual !== expected) {
    throw new Error(`Expected ${JSON.stringify(actual)} to equal ${JSON.stringify(expected)}`);
  }
}

function expect(condition: unknown, message: string) {
  if (!condition) throw new Error(message);
}

function expectDeepEqual(actual: unknown, expected: unknown) {
  const actualText = JSON.stringify(actual);
  const expectedText = JSON.stringify(expected);
  if (actualText !== expectedText) {
    throw new Error(`Expected ${actualText} to deep equal ${expectedText}`);
  }
}

function expectSourceIncludes(source: string, token: string, message: string) {
  expect(source.includes(token), message);
}

describe('update download UI wiring', () => {
  it('keeps global update notice wired to download phases and installer opening', () => {
    const source = readFileSync('src/renderer/components/UpdateNotice.tsx', 'utf8');

    expectSourceIncludes(source, 'openInstaller', 'UpdateNotice should expose the installer open handler');
    expectSourceIncludes(source, '打开安装包', 'UpdateNotice should show the installer open action');
    expectSourceIncludes(source, '正在自动下载安装包', 'UpdateNotice should describe automatic download progress');
    expectSourceIncludes(source, "phase === 'downloaded'", 'UpdateNotice should gate downloaded installer actions');
    expectSourceIncludes(source, "phase === 'failed'", 'UpdateNotice should surface failed downloads');
    expectSourceIncludes(source, "phase === 'unavailable'", 'UpdateNotice should surface unavailable installers');
    expectSourceIncludes(source, 'dismissUpdate', 'UpdateNotice should keep the dismiss action available');
  });

  it('keeps about page installer card wired to local file status and actions', () => {
    const source = readFileSync('src/renderer/components/workspace/WorkspaceSettingsView.tsx', 'utf8');

    expectSourceIncludes(source, 'update-installer-card', 'About page should render the installer card');
    expectSourceIncludes(source, '资产名称', 'About page should show installer asset name');
    expectSourceIncludes(source, '下载状态', 'About page should show installer download status');
    expectSourceIncludes(source, '本地文件', 'About page should show local installer path');
    expectSourceIncludes(source, 'openInstaller', 'About page should use the installer open handler');
    expectSourceIncludes(source, '打开安装包', 'About page should show the installer open action');
    expectSourceIncludes(source, '打开 GitHub Releases', 'About page should keep the releases fallback action');
    expectSourceIncludes(source, "status.downloadPhase === 'downloaded'", 'About page should only open downloaded installers');
  });
});

describe('shared Codex reasoning helpers', () => {
  it('reads xhigh from shared display sources', () => {
    expectEqual(readCodexReasoningEffort({ codex_reasoning_effort: 'xhigh' }), 'xhigh');
    expectEqual(readCodexReasoningEffort({ reasoningEffort: ' XHIGH ' }), 'xhigh');
  });

  it('falls back to medium for empty and invalid Codex values', () => {
    expectEqual(readCodexReasoningEffort({ codex_reasoning_effort: '' }), 'medium');
    expectEqual(readCodexReasoningEffort({ codex_reasoning_effort: 'invalid' }), 'medium');
  });

  it('keeps Claude summaries free of Codex reasoning depth', () => {
    const source = { agent_cli_provider: 'claude', codex_reasoning_effort: 'xhigh' };

    expectEqual(readCodexReasoningEffort(source), null);
    expectEqual(planCliSummaryLabel(source), 'Claude CLI');
  });

  it('labels xhigh without degrading it to medium', () => {
    expectEqual(codexReasoningEffortLabel('xhigh'), '超高');
  });
});

describe('shared OpenCode display helpers', () => {
  it('labels the opencode provider without Codex reasoning depth', () => {
    expectEqual(agentCliProviderLabel('opencode'), 'OpenCode');
    expectEqual(agentCliProviderLabel('OPENCODE'), 'OpenCode');
    expectEqual(agentCliDefaultCommand('opencode'), 'opencode');
  });

  it('keeps OpenCode plan summaries free of Codex reasoning depth', () => {
    expectEqual(planCliSummaryLabel({ agentCliProvider: 'opencode' }), 'OpenCode CLI');
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'opencode', codex_reasoning_effort: 'xhigh' }),
      'OpenCode CLI',
    );
  });
});

describe('shared Oh My Pi display helpers', () => {
  it('labels the oh-my-pi provider and resolves the omp command', () => {
    expectEqual(agentCliProviderLabel('oh-my-pi'), 'Oh My Pi');
    expectEqual(agentCliProviderLabel('OH-MY-PI'), 'Oh My Pi');
    expectEqual(agentCliDefaultCommand('oh-my-pi'), 'omp');
  });

  it('keeps Oh My Pi plan summaries free of Codex reasoning depth', () => {
    expectEqual(planCliSummaryLabel({ agentCliProvider: 'oh-my-pi' }), 'Oh My Pi CLI');
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'oh-my-pi', codex_reasoning_effort: 'xhigh' }),
      'Oh My Pi CLI',
    );
  });
});

describe('shared planCliSummaryLabel with intake-shaped fields', () => {
  it('labels Codex with low reasoning effort from intake fields', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'codex', codex_reasoning_effort: 'low' }),
      'Codex CLI · 思考深度 low',
    );
  });

  it('labels Codex with high reasoning effort from intake fields', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'codex', codex_reasoning_effort: 'high' }),
      'Codex CLI · 思考深度 high',
    );
  });

  it('labels Codex with xhigh reasoning effort from intake fields', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'codex', codex_reasoning_effort: 'xhigh' }),
      'Codex CLI · 思考深度 超高',
    );
  });

  it('labels Claude from intake fields without reasoning depth suffix', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'claude' }),
      'Claude CLI',
    );
  });

  it('labels OpenCode from intake fields', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'opencode' }),
      'OpenCode CLI',
    );
  });

  it('labels Oh My Pi from intake fields', () => {
    expectEqual(
      planCliSummaryLabel({ agent_cli_provider: 'oh-my-pi' }),
      'Oh My Pi CLI',
    );
  });

  it('defaults to Codex medium when provider is missing from intake', () => {
    expectEqual(
      planCliSummaryLabel({}),
      'Codex CLI · 思考深度 medium',
    );
  });
});

describe('settings choice metadata', () => {
  it('keeps CLI provider choices ready for segmented controls', () => {
    expectEqual(agentCliOptionDetails.length, 4);
    expectEqual(agentCliOptionDetails[0].value, 'codex');
    expectEqual(agentCliOptionDetails[1].value, 'claude');
    expectEqual(agentCliOptionDetails[2].value, 'opencode');
    expectEqual(agentCliOptionDetails[3].value, 'oh-my-pi');
    expect(agentCliOptionDetails.every((option) => option.description), 'CLI options should include descriptions');
  });

  it('keeps Codex effort card choices aligned with labels', () => {
    const effortValues = codexReasoningOptionDetails.map((option) => option.value).join(',');

    expectEqual(effortValues, 'low,medium,high,xhigh');
    expectEqual(codexReasoningOptionDetails.find((option) => option.value === 'xhigh')?.label, '超高');
    expect(codexReasoningOptionDetails.every((option) => option.description), 'Codex effort options should include descriptions');
  });

  it('keeps scope open modes complete for the segmented control', () => {
    const scopeModes = scopeFileOpenModeOptions.map((option) => option.value).join(',');

    expectEqual(scopeModes, 'system,folder,vscode,command');
    expect(scopeFileOpenModeOptions.some((option) => option.description.includes('{file}')), 'command mode should document the {file} placeholder');
  });
});

describe('AI config thinking depth metadata', () => {
  it('keeps OpenAI xhigh option available with the display label', () => {
    const openaiOptions = thinkingDepthOptionsForProvider('openai');

    expectDeepEqual(
      openaiOptions.map((option) => option.value),
      ['', 'low', 'medium', 'high', 'xhigh'],
    );
    expectEqual(openaiOptions.find((option) => option.value === 'xhigh')?.label, '超高');
    expectEqual(normalizeAiThinkingDepthInput(' XHIGH ', 'openai'), 'xhigh');
    expectEqual(aiThinkingDepthLabel('xhigh', 'openai'), '思考 · 超高');
  });

  it('does not submit xhigh for providers that do not support it', () => {
    const openaiForm: ChatConfigFormState = {
      provider: 'openai',
      baseUrl: 'https://api.openai.com',
      apiKey: 'sk-render-xhigh',
      model: 'gpt-4o',
      temperature: '0.3',
      thinkingDepth: 'xhigh',
      thinkingBudgetTokens: '5000',
    };

    expectDeepEqual(
      thinkingDepthOptionsForProvider('deepseek').map((option) => option.value),
      ['', 'low', 'medium', 'high'],
    );
    expectDeepEqual(thinkingDepthOptionsForProvider('anthropic'), []);
    expectEqual(normalizeAiThinkingDepthInput('xhigh', 'deepseek'), '');
    expectEqual(normalizeAiThinkingDepthInput('xhigh', 'anthropic'), '');

    const switchedToDeepSeek = aiConfigFormForProviderChange(openaiForm, 'deepseek');
    expectEqual(switchedToDeepSeek.thinkingDepth, '');

    const deepseekPayload = aiConfigInputFromForm('DeepSeek', { ...openaiForm, provider: 'deepseek' });
    expectEqual(deepseekPayload.thinkingDepth, null);
    expectEqual(deepseekPayload.thinkingBudgetTokens, null);

    const anthropicPayload = aiConfigInputFromForm('Anthropic', { ...openaiForm, provider: 'anthropic' });
    expectEqual(anthropicPayload.thinkingDepth, null);
    expectEqual(anthropicPayload.thinkingBudgetTokens, 5000);
  });
});

describe('AI config default model metadata', () => {
  it('uses gpt-5.5 for OpenAI default forms and empty submissions', () => {
    const form = createDefaultChatConfigForm();

    expectEqual(form.provider, 'openai');
    expectEqual(form.model, 'gpt-5.5');

    const payload = aiConfigInputFromForm('OpenAI', { ...form, model: '' });
    expectEqual(payload.model, 'gpt-5.5');

    const switchedFromLegacyOpenAiDefault = aiConfigFormForProviderChange(
      { ...form, model: 'gpt-4o' },
      'openai',
    );
    expectEqual(switchedFromLegacyOpenAiDefault.model, 'gpt-5.5');
  });
});

describe('plan backend settings metadata', () => {
  it('keeps generation and execution strategy choices separate', () => {
    expectDeepEqual(
      planGenerationStrategyOptions.map((option) => option.value),
      ['external-cli-markdown', 'external-cli-structured', 'builtin-llm-structured'],
    );
    expectDeepEqual(
      planExecutionStrategyOptions.map((option) => option.value),
      ['external-cli', 'builtin-llm'],
    );
    expectEqual(isBuiltinPlanGenerationStrategy('builtin-llm-structured'), true);
    expectEqual(isBuiltinPlanGenerationStrategy('external-cli-structured'), false);
    expectEqual(isBuiltinPlanExecutionStrategy('builtin-llm'), true);
    expectEqual(isBuiltinPlanExecutionStrategy('external-cli'), false);
  });

  it('maps backend providers to the right command or model defaults', () => {
    expectDeepEqual(
      planBackendProviderOptionsForStrategy('external-cli-structured').map((option) => option.value),
      ['codex', 'claude', 'opencode', 'oh-my-pi'],
    );
    expectDeepEqual(
      planBackendProviderOptionsForStrategy('builtin-llm-structured').map((option) => option.value),
      ['openai', 'deepseek', 'anthropic'],
    );
    expectEqual(planBackendDefaultCommand('oh-my-pi'), 'omp');
    expectEqual(planBackendDefaultCommand('claude'), 'claude');
    expectEqual(planBackendDefaultModel('openai'), 'gpt-5.5');
    expectEqual(planBackendDefaultModel('deepseek'), 'deepseek-chat');
    expectEqual(planBackendDefaultModel('anthropic'), 'claude-sonnet-4-6');
  });

  it('normalizes Composer overrides to generation fields only', () => {
    const projectDefault = planGenerationInputFromComposerSelection(createDefaultPlanGenerationSelection());
    const external = planGenerationInputFromComposerSelection(createDefaultPlanGenerationSelection({
      strategy: 'external-cli-structured',
      provider: 'codex',
      command: ' codex plan ',
      codexReasoningEffort: 'xhigh',
    }));
    const builtin = planGenerationInputFromComposerSelection(createDefaultPlanGenerationSelection({
      strategy: 'builtin-llm-structured',
      provider: 'deepseek',
      model: '',
      command: 'should-not-cross',
      codexReasoningEffort: 'high',
    }));

    expectDeepEqual(projectDefault, {});
    expectDeepEqual(external, {
      planGenerationStrategy: 'external-cli-structured',
      planGenerationProvider: 'codex',
      planGenerationCommand: 'codex plan',
      planGenerationModel: '',
      planGenerationCodexReasoningEffort: 'xhigh',
    });
    expectDeepEqual(builtin, {
      planGenerationStrategy: 'builtin-llm-structured',
      planGenerationProvider: 'deepseek',
      planGenerationCommand: '',
      planGenerationModel: 'deepseek-chat',
      planGenerationCodexReasoningEffort: null,
    });
    expect(
      Object.keys({ ...external, ...builtin }).every((key) => !key.startsWith('planExecution')),
      'Composer 归一化输出不应包含执行覆盖字段',
    );
  });
});
