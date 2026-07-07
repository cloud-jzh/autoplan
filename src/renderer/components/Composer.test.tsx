export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function expect(condition: unknown, message: string) {
  if (!condition) throw new Error(message);
}

function source(...parts: string[]) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

function expectIncludes(sourceText: string, snippet: string, message: string) {
  expect(sourceText.includes(snippet), message);
}

function expectNotIncludes(sourceText: string, snippet: string, message: string) {
  expect(!sourceText.includes(snippet), message);
}

function sliceBetween(sourceText: string, startNeedle: string, endNeedle: string, message: string) {
  const start = sourceText.indexOf(startNeedle);
  expect(start >= 0, message);
  const end = sourceText.indexOf(endNeedle, start);
  expect(end >= 0, message);
  return sourceText.slice(start, end + endNeedle.length);
}

describe('Composer plan generation override contract', () => {
  it('defines submit payload fields for generation overrides only', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');
    const payload = sliceBetween(
      composer,
      'export interface ComposerSubmitPayload {',
      'function getClipboardImageFiles',
      '应能定位 ComposerSubmitPayload 接口',
    );

    expectIncludes(payload, 'planGenerationStrategy?: PlanGenerationInputFields[\'planGenerationStrategy\'];', '提交 payload 应允许覆盖生成策略');
    expectIncludes(payload, 'planGenerationProvider?: PlanGenerationInputFields[\'planGenerationProvider\'];', '提交 payload 应允许覆盖生成 Provider');
    expectIncludes(payload, 'planGenerationCommand?: PlanGenerationInputFields[\'planGenerationCommand\'];', '提交 payload 应允许覆盖外部生成命令');
    expectIncludes(payload, 'planGenerationModel?: PlanGenerationInputFields[\'planGenerationModel\'];', '提交 payload 应允许覆盖内置生成模型');
    expectIncludes(payload, 'planGenerationCodexReasoningEffort?: PlanGenerationInputFields[\'planGenerationCodexReasoningEffort\'];', '提交 payload 应允许覆盖生成 Codex 思考深度');
    expect(!payload.includes('planExecution'), 'Composer 提交 payload 不应包含任务执行覆盖字段');
  });

  it('submits only planGenerationInputFromComposerSelection output when CLI controls are present', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');
    const submitBlock = sliceBetween(
      composer,
      'const submit = async (event: FormEvent<HTMLFormElement>) => {',
      'const addFiles =',
      '应能定位 Composer submit 逻辑',
    );

    expectIncludes(submitBlock, '...planGenerationInputFromComposerSelection(selectedGeneration),', 'Composer 应只展开生成配置归一化结果');
    expectIncludes(submitBlock, ': createAsDraft ? { body: value, createAsDraft } : value;', '缺少配置上下文时应保持旧字符串/草稿 payload 兼容');
    expect(!submitBlock.includes('planExecution'), 'Composer submit 逻辑不应构造执行覆盖字段');
  });

  it('renders direct CLI provider controls without the old source selector', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');

    expectIncludes(composer, 'const cliProviderOptions = cliSelection?.options || [];', 'Composer 应直接使用上下文中的 CLI Provider 选项');
    expectIncludes(composer, 'className="composer-icon-select composer-cli-select"', 'Composer 应渲染 CLI Provider 选择控件');
    expectIncludes(composer, 'aria-label="选择计划生成 CLI 后端"', 'Composer 应暴露直接 CLI 后端选择器');
    expectIncludes(composer, 'onChange={(event) => changePlanGenerationProvider(event.target.value as PlanBackendProvider)}', 'CLI Provider 变化应走生成 Provider 回调');
    expectIncludes(composer, '{cliProviderOptions.map((option) => (', 'CLI Provider 选项应沿用上下文提供的 Codex/Claude/OpenCode/Oh My Pi 列表');
    expectNotIncludes(composer, 'aria-label="选择计划生成配置来源"', 'Composer 不应再渲染计划生成配置来源选择器');
    expectNotIncludes(composer, '项目默认', 'Composer 不应再包含“项目默认”文案');
    expectNotIncludes(composer, '自定义生成', 'Composer 不应再包含“自定义生成”文案');
    expectNotIncludes(composer, 'aria-label="选择计划生成策略"', 'Composer 底部不应再展示生成策略选择器');
    expectNotIncludes(composer, "aria-label={isBuiltinGeneration ? '计划生成模型' : '计划生成命令'}", 'Composer 底部不应再展示模型/命令输入');
  });

  it('shows Codex reasoning only for Codex CLI and keeps all reasoning options available', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');
    const forms = source('src', 'renderer', 'utils', 'workspaceForms.ts');

    expectIncludes(composer, 'const isCodexProvider = isCodexPlanBackendProvider(selectedProvider);', 'Composer 应按当前 CLI Provider 判断 Codex');
    expectIncludes(composer, '{isCodexProvider ? (', 'Codex 思考深度控件应只在 Codex Provider 下渲染');
    expectIncludes(composer, 'className="composer-icon-select composer-reasoning-select"', 'Codex 思考深度应有独立选择控件');
    expectIncludes(composer, 'aria-label="选择 Codex 思考深度"', 'Codex 思考深度选择器应具备可访问标签');
    expectIncludes(composer, '{cliSelection.reasoningOptions.map((option) => (', 'Codex 思考深度选项应沿用上下文列表');
    expectIncludes(forms, "{ value: 'low', label: '低 · 快速' }", 'Codex reasoning 应包含 low');
    expectIncludes(forms, "{ value: 'medium', label: '中 · 默认' }", 'Codex reasoning 应包含 medium');
    expectIncludes(forms, "{ value: 'high', label: '高 · 深入' }", 'Codex reasoning 应包含 high');
    expectIncludes(forms, "{ value: 'xhigh', label: '超高 · 最深入' }", 'Codex reasoning 应包含 xhigh');
  });

  it('keeps Composer context scoped to generation selection handlers', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');
    const context = sliceBetween(
      composer,
      'interface ComposerCliSelectionValue {',
      'const ComposerCliSelectionContext',
      '应能定位 Composer 配置上下文接口',
    );

    expectIncludes(context, 'selectedByType: Record<IntakeType, ComposerPlanGenerationSelection>;', 'Composer 上下文应保存每类 intake 的生成配置选择');
    expectIncludes(context, 'options: AgentCliOption[];', 'Composer 上下文应暴露 CLI Provider 选项');
    expectIncludes(context, 'reasoningOptions: AgentCliOption[];', 'Composer 上下文应暴露 Codex 思考深度选项');
    expectIncludes(context, 'onProviderChange: (type: IntakeType, provider: PlanBackendProvider) => void;', 'Composer 上下文应暴露生成 Provider 切换');
    expectIncludes(context, 'onReasoningChange: (type: IntakeType, effort: CodexReasoningEffort) => void;', 'Composer 上下文应暴露生成 Codex 思考深度切换');
    expectIncludes(context, 'onStrategyChange: (type: IntakeType, strategy: PlanGenerationStrategy) => void;', 'Composer 上下文只保留必要的生成策略兜底回调');
    expectNotIncludes(context, 'onUseProjectDefaultChange', 'Composer 上下文不应再暴露项目默认来源切换');
    expectNotIncludes(context, 'onCommandChange', 'Composer 上下文不应再暴露底部命令输入回调');
    expectNotIncludes(context, 'onModelChange', 'Composer 上下文不应再暴露底部模型输入回调');
    expectNotIncludes(context, 'planExecution', 'Composer 配置上下文不应包含执行方案');
  });

  it('normalizes Composer selections into explicit external CLI generation overrides', () => {
    const forms = source('src', 'renderer', 'utils', 'workspaceForms.ts');
    const selectionType = sliceBetween(
      forms,
      'export type ComposerPlanGenerationSelection = {',
      'export function createDefaultPlanGenerationSelection',
      '应能定位 Composer 计划生成选择类型',
    );
    const normalizer = sliceBetween(
      forms,
      'export function planGenerationInputFromComposerSelection',
      'export function loopFormFromProjectState',
      '应能定位 Composer 计划生成归一化函数',
    );

    expectNotIncludes(selectionType, 'useProjectDefault', 'Composer 选择状态不应再依赖 useProjectDefault 开关');
    expectNotIncludes(normalizer, 'if (selection.useProjectDefault) return {};', 'Composer 归一化不应因项目默认返回空覆盖');
    expectIncludes(normalizer, 'planGenerationStrategy: strategy,', 'Composer 归一化应提交计划生成策略覆盖');
    expectIncludes(normalizer, 'planGenerationProvider: provider,', 'Composer 归一化应提交计划生成 Provider 覆盖');
    expectIncludes(normalizer, "planGenerationCommand: selection.command.trim(),", 'Composer 归一化应提交外部 CLI 命令字段');
    expectIncludes(normalizer, "planGenerationModel: '',", 'Composer 归一化应保持外部 CLI 语义，不提交内置模型');
    expectIncludes(normalizer, 'PLAN_GENERATION_STRATEGIES.EXTERNAL_CLI_MARKDOWN', 'Composer 归一化应把非外部策略兜底为外部 CLI');
    expectIncludes(normalizer, 'isCodexPlanBackendProvider(provider)', 'Composer 归一化应按 Provider 判断 Codex reasoning');
    expectIncludes(normalizer, 'normalizeCodexReasoningEffort(selection.codexReasoningEffort)', 'Codex Provider 应保留 low/medium/high/xhigh reasoning');
    expectIncludes(normalizer, ': null,', '非 Codex Provider 应归一化为 null reasoning');
    expectNotIncludes(normalizer, 'planExecution', 'Composer 归一化输出不应包含执行覆盖字段');
  });

  it('keeps requirement and feedback Composer state independent in the workspace controller', () => {
    const controller = source('src', 'renderer', 'hooks', 'useWorkspaceController.ts');
    const initBlock = sliceBetween(
      controller,
      'setComposerPlanGeneration({',
      'state?.plan_generation_codex_reasoning_effort,',
      '应能定位 Composer 选择初始化逻辑',
    );
    const selectionContext = sliceBetween(
      controller,
      'const composerCliSelection = useMemo(',
      'setSearchQuery',
      '应能定位 Composer CLI selection context',
    );

    expectIncludes(initBlock, 'requirement: composerPlanGenerationSelectionFromProjectState(state),', '需求 composer 应按当前项目初始化独立选择对象');
    expectIncludes(initBlock, 'feedback: composerPlanGenerationSelectionFromProjectState(state),', '反馈 composer 应按当前项目初始化独立选择对象');
    expectIncludes(selectionContext, 'selectedByType: composerPlanGeneration,', 'Composer 上下文应按 intake 类型读取当前选择');
    expectIncludes(selectionContext, 'onProviderChange: (type: IntakeType, provider: PlanBackendProvider) => {', 'Composer 上下文应提供 CLI Provider 切换');
    expectIncludes(selectionContext, 'onReasoningChange: (type: IntakeType, effort: CodexReasoningEffort) => {', 'Composer 上下文应提供 Codex 思考深度切换');
    expectIncludes(selectionContext, 'PLAN_GENERATION_STRATEGIES.EXTERNAL_CLI_MARKDOWN', '切换 CLI 时应保持外部 CLI 计划生成语义');
    expectIncludes(selectionContext, 'command: currentProvider === nextProvider ? currentSelection.command : \'\',', '切换不同 CLI 时应清空旧命令并走默认命令兜底');
    expectNotIncludes(selectionContext, 'onUseProjectDefaultChange', 'Workspace controller 不应再暴露项目默认来源切换');
    expectNotIncludes(selectionContext, 'useProjectDefault', 'Workspace controller 的 Composer 状态不应再写入项目默认开关');
  });
});
