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

function expectAnyIncludes(sourceText: string, snippets: string[], message: string) {
  expect(snippets.some((snippet) => sourceText.includes(snippet)), message);
}

function expectCountAtLeast(sourceText: string, snippet: string, minimum: number, message: string) {
  const count = sourceText.split(snippet).length - 1;
  expect(count >= minimum, message);
}

describe('Workspace task page structure', () => {
  it('keeps the task page split into Plan and task columns', () => {
    const page = source('src', 'renderer', 'pages', 'WorkspacePage.tsx');

    expectIncludes(page, 'data-testid="workspace-task-main"', 'task main test anchor should exist');
    expectIncludes(page, 'className="task-status-grid"', 'task page should use the two-column grid container');
    expectIncludes(page, '<PlanList', 'task page should render the Plan column');
    expectIncludes(page, '<TaskList', 'task page should render the task column');
    expectIncludes(page, 'selectedPlanTaskFilter', 'task column should stay connected to Plan selection filtering');
  });

  it('renders Plan cards with progress, concurrency, metadata, and actions', () => {
    const planList = source('src', 'renderer', 'components', 'plans', 'PlanList.tsx');
    const wrappedPlanList = source('src', 'renderer', 'components', 'PlanLists.tsx');

    expectIncludes(planList, "className={`plan-card ${cardState}${selected ? ' selected' : ''}`}", 'Plan list should render visual cards with selected state');
    expectIncludes(planList, 'onSelectPlan?.(plan);', 'Plan card should support direct selection');
    expectIncludes(wrappedPlanList, 'onSelectPlan={selectPlan}', 'Plan card selection should drive workspace selection state');
    expectIncludes(planList, 'className="plan-progress"', 'Plan card should include progress block');
    expectIncludes(planList, 'className="concurrency-row"', 'Plan card should include concurrency summary');
    expectIncludes(planList, 'className="plan-meta"', 'Plan card should include CLI/hash/update metadata');
    expectIncludes(planList, "className={`plan-validation ${plan.validation_passed ? 'passed' : 'pending'}`}", 'Plan card should expose validation state');
    expectIncludes(planList, 'plan-parallel-link', 'Plan card should preserve parallel execution entry');
    expectIncludes(planList, 'plan-read-link', 'Plan card should preserve read-full-plan entry');
  });

  it('keeps task grouping, status filters, and scope semantic classes wired', () => {
    const planLists = source('src', 'renderer', 'components', 'PlanLists.tsx');
    const taskList = source('src', 'renderer', 'components', 'plans', 'TaskList.tsx');
    const planTasks = source('src', 'renderer', 'utils', 'planTasks.ts');

    expectIncludes(planLists, 'className="task-filter-tabs"', 'task status filters should remain available');
    expectIncludes(planLists, 'className="list compact task-groups"', 'task groups should use the compact group layout');
    expectIncludes(planLists, 'task-plan-group-toggle', 'task groups should have an expand/collapse trigger');
    expectIncludes(planLists, 'formatTaskPlanGroupProgress(group)', 'task groups should render progress only');
    expectIncludes(taskList, "className={`task-item${running ? ' running' : ''}`}", 'standalone task list should render task cards');
    expectIncludes(taskList, "className={`task-scope-chip scope-chip${semanticClass ? ` ${semanticClass}` : ''}`}", 'scope chips should receive semantic classes');
    expectIncludes(planTasks, 'scopeFileClassName', 'scope file semantic class helper should exist');
    expectIncludes(planTasks, "if (file.isUnknown) return 'unknown special';", 'unknown scope should have a distinct class');
    expectIncludes(planTasks, "if (file.isValidation) return 'validation';", 'validation scope should have a distinct class');
    expectIncludes(planTasks, 'if (left.hasRunningTask !== right.hasRunningTask)', 'running task groups should sort first');
  });
});

describe('Workspace intake Plan preview binding', () => {
  it('wires requirement and feedback panels to the shared Plan reader flow', () => {
    const page = source('src', 'renderer', 'pages', 'WorkspacePage.tsx');
    const controller = source('src', 'renderer', 'hooks', 'useWorkspaceController.ts');

    expectIncludes(page, 'const intakePlanPreviewProps = {', 'intake panels should share preview props');
    expectIncludes(page, 'plans: snapshot.plans', 'intake preview props should carry current Plan snapshots');
    expectAnyIncludes(
      page,
      ['onOpenPlan: openIntakePlanReader', 'onPreviewPlan: openIntakePlanReader'],
      'intake preview props should use the controller Plan reader callback',
    );
    expectCountAtLeast(page, '{...intakePlanPreviewProps}', 2, 'requirement and feedback panels should both receive preview props');
    expectIncludes(page, '<PlanReaderModal', 'workspace should mount the reusable Plan reader modal once');
    expectIncludes(page, 'readerState={planReadState}', 'intake preview should reuse the workspace Plan reader state');

    expectIncludes(controller, 'const openIntakePlanReader = useCallback', 'controller should expose an intake Plan reader opener');
    expectIncludes(controller, 'findLinkedPlanInSnapshot(snapshot?.plans || [], planId, projectId)', 'intake preview should locate plans from the current snapshot first');
    expectIncludes(controller, 'showUnavailableLinkedPlanReader', 'controller should provide unavailable Plan fallback state');
    expectIncludes(controller, '绑定 Plan ID 无效，暂无法预览。', 'invalid linked Plan IDs should produce a readable error');
    expectIncludes(controller, '绑定 Plan #${planId} 当前不可用', 'missing linked Plan snapshots should produce a readable error');
  });

  it('renders bound intake Plan metadata, progress, preview affordance, and fallbacks', () => {
    const page = source('src', 'renderer', 'pages', 'WorkspacePage.tsx');
    const intakePanel = source('src', 'renderer', 'components', 'IntakePanel.tsx');
    const styles = source('src', 'renderer', 'styles', 'components.css');
    const pageUsesOpenPlan = page.includes('onOpenPlan: openIntakePlanReader');
    const pageUsesPreviewPlan = page.includes('onPreviewPlan: openIntakePlanReader');
    const intakeAcceptsOpenPlan = intakePanel.includes('onOpenPlan');
    const intakeAcceptsPreviewPlan = intakePanel.includes('onPreviewPlan');

    expectIncludes(intakePanel, 'function PlanBindingCard', 'intake panel should render a dedicated bound Plan card');
    expectIncludes(intakePanel, 'if (linkedPlanId === null) return null;', 'unbound intake items should not show Plan preview UI');
    expectIncludes(intakePanel, "readStringField(item, ['plan_title', 'linked_plan_title'])", 'bound cards should read Plan title snapshots');
    expectIncludes(intakePanel, "readStringField(item, ['plan_file_path', 'linked_plan_file_path', 'plan_path', 'linked_plan_path'])", 'bound cards should read Plan path snapshots');
    expectIncludes(intakePanel, 'Plan ID <b>#{linkedPlanId}</b>', 'bound cards should display Plan ID');
    expectIncludes(intakePanel, '任务进度 <b>{progressLabel}</b>', 'bound cards should display task progress text');
    expectIncludes(intakePanel, 'className="intake-plan-progress"', 'bound cards should display task progress bars');
    expectIncludes(intakePanel, 'disabled={!canPreview}', 'unavailable Plan previews should be disabled');
    expectIncludes(intakePanel, '绑定 Plan 快照缺失，暂不能预览全文。', 'missing Plan snapshots should show fallback copy');
    expect(
      (pageUsesOpenPlan && intakeAcceptsOpenPlan) || (pageUsesPreviewPlan && intakeAcceptsPreviewPlan),
      'WorkspacePage and IntakePanel should use the same intake preview callback prop name',
    );

    expectIncludes(styles, '.intake-plan-card', 'bound Plan card styles should be scoped to intake cards');
    expectIncludes(styles, '.intake-plan-name', 'long Plan titles should have dedicated text styling');
    expectIncludes(styles, '.intake-plan-path', 'long Plan paths should have dedicated truncation styling');
    expectIncludes(styles, '.intake-plan-side', 'preview actions should be able to wrap without squeezing intake actions');
  });
});

describe('Workspace settings page structure', () => {
  it('defines four settings panes with navigation metadata', () => {
    const settingsView = source('src', 'renderer', 'components', 'workspace', 'WorkspaceSettingsView.tsx');

    expectIncludes(settingsView, "type SettingsPane = 'loop' | 'cli' | 'scope' | 'mcp';", 'settings panes should match the four required groups');
    expectIncludes(settingsView, 'className="settings-nav"', 'settings view should render the left navigation');
    expectIncludes(settingsView, 'settings-nav-item', 'settings navigation should render selectable items');
    expectIncludes(settingsView, 'className="settings-content"', 'settings view should render independently scrolling content');
    expectIncludes(settingsView, 'className="settings-pane active"', 'settings view should render active pane content');
  });

  it('keeps CLI, scope, and MCP interactions represented in source', () => {
    const composer = source('src', 'renderer', 'components', 'Composer.tsx');
    const settingsView = source('src', 'renderer', 'components', 'workspace', 'WorkspaceSettingsView.tsx');

    expectIncludes(settingsView, 'agentCliOptionDetails.map', 'CLI provider should use segmented option data');
    expectIncludes(settingsView, 'codexReasoningOptionDetails.map', 'Codex reasoning should render option cards');
    expectIncludes(settingsView, 'isCodexAgentCliProvider(loopForm.agentCliProvider)', 'non-Codex providers should hide Codex-only effort controls');
    expectIncludes(settingsView, 'agentCliDefaultCommand(loopForm.agentCliProvider)', 'CLI command placeholder should follow the selected provider');
    expectIncludes(composer, "selectedProvider !== 'claude'", 'composer should treat Claude as non-Codex');
    expectIncludes(composer, 'agentCliProvider: selectedProvider as AgentCliProvider', 'composer submit payload should carry the selected CLI provider');
    expectIncludes(composer, '...(isCodexProvider ? { codexReasoningEffort:', 'composer should only submit Codex reasoning for Codex provider');
    expectIncludes(settingsView, 'scopeFileOpenModeOptions.map', 'scope mode should use segmented option data');
    expectIncludes(settingsView, "scopeFileOpenSettings.mode === 'vscode' || scopeFileOpenSettings.mode === 'command'", 'editor command should only expand for command-based modes');
    expectIncludes(settingsView, '<InfoRow label="服务状态">', 'MCP pane should expose service status as readonly info');
    expectIncludes(settingsView, 'value={mcpAuthToken}', 'MCP pane should expose editable auth token');
    expectIncludes(settingsView, '<InfoRow label="请求头">', 'MCP pane should show the standard auth header');
    expectIncludes(settingsView, '<InfoRow label="工具清单">', 'MCP pane should expose tool list as readonly info');
    expectIncludes(settingsView, 'AUTOPLAN_MCP_ENABLED=0', 'MCP pane should keep the disable reminder');
  });
});

describe('OpenCode CLI backend integration', () => {
  it('exposes OpenCode CLI across shared labels, settings form, and option data', () => {
    const shared = source('src', 'renderer', 'components', 'shared.tsx');
    const forms = source('src', 'renderer', 'utils', 'workspaceForms.ts');
    const settingsView = source('src', 'renderer', 'components', 'workspace', 'WorkspaceSettingsView.tsx');

    expectIncludes(shared, "if (value === 'opencode') return 'OpenCode';", 'shared label helper should resolve opencode to OpenCode');
    expectIncludes(forms, "{ value: 'opencode', label: 'OpenCode CLI' },", 'CLI option list should expose OpenCode CLI for the composer and settings');
    expectIncludes(forms, "if (normalized === 'opencode') return 'opencode';", 'default command resolver should return opencode for the OpenCode backend');
    expectIncludes(settingsView, "if (provider === 'opencode') return 'OpenCode';", 'settings view should display the OpenCode backend name');
    expectIncludes(settingsView, "loopForm.agentCliProvider === 'opencode'", 'settings view should branch the command hint on the OpenCode backend');
  });
});
