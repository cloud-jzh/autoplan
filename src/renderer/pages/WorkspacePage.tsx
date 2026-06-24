import { FormEvent, useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import type {
  AppSnapshot,
  IntakeType,
  PendingAttachment,
  Project,
  ProjectState,
  WorkspaceTab,
} from '../types';
import { useSnapshot } from '../hooks/useSnapshot';
import { IntakePanel } from '../components/IntakePanel';
import { EventList, PlanList, TaskList } from '../components/PlanLists';
import { CodexLog } from '../components/CodexLog';
import { Icon, type IconName } from '../components/icons';
import { getFilePath } from '../components/shared';
import { formatChinaTime } from '../utils/time';

const emptyPendingAttachments: Record<IntakeType, PendingAttachment[]> = {
  requirement: [],
  feedback: [],
};

const tabs: Array<{ id: WorkspaceTab; label: string; icon: IconName }> = [
  { id: 'overview', label: '概览', icon: 'overview' },
  { id: 'requirement', label: '需求', icon: 'requirement' },
  { id: 'feedback', label: '反馈', icon: 'feedback' },
  { id: 'tasks', label: '任务与计划', icon: 'tasks' },
  { id: 'events', label: '事件流', icon: 'events' },
];

export function WorkspacePage() {
  const params = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const projectId = Number(params.projectId);
  const { snapshot, setSnapshot, error, setError } = useSnapshot(Number.isFinite(projectId) ? projectId : null);
  const [activeTab, setActiveTab] = useState<WorkspaceTab>('overview');
  const [pendingAttachments, setPendingAttachments] =
    useState<Record<IntakeType, PendingAttachment[]>>(emptyPendingAttachments);
  const [loopForm, setLoopForm] = useState({ workspacePath: '', intervalSeconds: '5', validationCommand: '' });
  const [searchQuery, setSearchQuery] = useState('');

  const project = snapshot?.activeProject || null;
  const state = snapshot?.state || null;
  const projects = snapshot?.projects || [];
  const normalizedSearchQuery = useMemo(() => normalizeSearchQuery(searchQuery), [searchQuery]);
  const searchableItemCount = useMemo(() => (snapshot ? countSearchableItems(snapshot) : 0), [snapshot]);
  const searchHitCount = useMemo(
    () => (snapshot && normalizedSearchQuery ? countWorkspaceSearchMatches(snapshot, normalizedSearchQuery) : 0),
    [snapshot, normalizedSearchQuery],
  );

  const showError = useCallback((e: unknown) => {
    const msg = e instanceof Error ? e.message : String(e);
    setError(msg);
    window.alert(msg);
  }, [setError]);

  useEffect(() => {
    if (!state) return;
    setLoopForm({
      workspacePath: state.workspace_path || '',
      intervalSeconds: String(state.interval_seconds || 5),
      validationCommand: state.validation_command || '',
    });
  }, [state?.workspace_path, state?.interval_seconds, state?.validation_command, state]);

  useEffect(() => {
    setSearchQuery('');
  }, [projectId]);

  const addPendingFiles = useCallback((type: IntakeType, files: FileList | File[] | null) => {
    const selected = Array.from(files || [])
      .map((file) => ({ path: getFilePath(file), name: file.name, size: file.size, type: file.type }))
      .filter((file) => file.path);
    if (!selected.length) return;
    setPendingAttachments((current) => {
      const nextItems = [...current[type]];
      for (const file of selected) {
        if (!nextItems.some((item) => item.path === file.path && item.size === file.size)) nextItems.push(file);
      }
      return { ...current, [type]: nextItems };
    });
  }, []);

  const removePendingAttachment = useCallback((type: IntakeType, index: number) => {
    setPendingAttachments((current) => ({
      ...current,
      [type]: current[type].filter((_, itemIndex) => itemIndex !== index),
    }));
  }, []);

  const createRequirement = useCallback(
    async (body: string) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.createRequirement({
          projectId,
          body,
          attachments: pendingAttachments.requirement,
        });
        setSnapshot(next);
        setPendingAttachments((current) => ({ ...current, requirement: [] }));
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [pendingAttachments.requirement, projectId, setSnapshot, setError, showError],
  );

  const createFeedback = useCallback(
    async (body: string) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.createFeedback({
          projectId,
          body,
          attachments: pendingAttachments.feedback,
        });
        setSnapshot(next);
        setPendingAttachments((current) => ({ ...current, feedback: [] }));
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [pendingAttachments.feedback, projectId, setSnapshot, setError, showError],
  );

  const updateRequirement = useCallback(
    async (id: number, input: { title?: string; body?: string; status?: string }) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.updateRequirement({ projectId, id, ...input });
        setSnapshot(next);
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const deleteRequirement = useCallback(
    async (id: number) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.deleteRequirement({ projectId, id });
        setSnapshot(next);
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const updateFeedback = useCallback(
    async (id: number, input: { title?: string; body?: string; status?: string }) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.updateFeedback({ projectId, id, ...input });
        setSnapshot(next);
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const deleteFeedback = useCallback(
    async (id: number) => {
      if (!projectId) return false;
      try {
        const next = await window.autoplan.deleteFeedback({ projectId, id });
        setSnapshot(next);
        setError(null);
        return true;
      } catch (e) {
        showError(e);
        return false;
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const submitLoopConfig = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    try {
      const next = await window.autoplan.configureLoop({
        projectId,
        workspacePath: loopForm.workspacePath,
        intervalSeconds: Number(loopForm.intervalSeconds || 5),
        validationCommand: loopForm.validationCommand,
      });
      setSnapshot(next);
      setError(null);
    } catch (e) {
      showError(e);
    }
  };

  const runLoopAction = async (action: () => Promise<AppSnapshot>) => {
    try {
      const next = await action();
      setSnapshot(next);
      setError(null);
    } catch (e) {
      showError(e);
    }
  };

  const interruptIntake = useCallback(
    async (type: IntakeType, id: number) => {
      try {
        const next = await window.autoplan.interruptIntake({ projectId, type, id });
        setSnapshot(next);
        setError(null);
      } catch (e) {
        showError(e);
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const resumeIntake = useCallback(
    async (type: IntakeType, id: number) => {
      try {
        const next = await window.autoplan.resumeIntake({ projectId, type, id });
        setSnapshot(next);
        setError(null);
      } catch (e) {
        showError(e);
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const appendIntakeTask = useCallback(
    async (type: IntakeType, id: number, title: string) => {
      try {
        const next = await window.autoplan.appendIntakeTask({ projectId, type, id, title });
        setSnapshot(next);
        setError(null);
      } catch (e) {
        showError(e);
      }
    },
    [projectId, setSnapshot, setError, showError],
  );

  const switchProject = (nextId: number) => {
    if (nextId && nextId !== projectId) navigate(`/projects/${nextId}`);
  };

  if (!snapshot) {
    return (
      <div className="workspace-shell">
        <WorkspaceSidebar
          activeTab={activeTab}
          onTab={setActiveTab}
          onBack={() => navigate('/projects')}
          projectId={projectId}
          projects={projects}
          currentProject={project}
          state={null}
          onSwitchProject={switchProject}
        />
        <div className="workspace-main">
          <div className="empty">加载中...</div>
        </div>
      </div>
    );
  }

  return (
    <div className="workspace-shell">
      <WorkspaceSidebar
        activeTab={activeTab}
        onTab={setActiveTab}
        onBack={() => navigate('/projects')}
        projectId={projectId}
        projects={projects}
        currentProject={project}
        state={state}
        onSwitchProject={switchProject}
      />
      <div className="workspace-main">
        <header className="topbar">
          <div className="topbar-title">
            <h1>{tabTitle(activeTab)}</h1>
            <p>{tabSubtitle(activeTab, project)}</p>
          </div>
          <WorkspaceSearchBox
            hitCount={searchHitCount}
            onQueryChange={setSearchQuery}
            query={searchQuery}
            totalCount={searchableItemCount}
          />
          <div className="topbar-actions">
            <span className="pill">
              <span className={`led ${state?.running ? 'running' : 'stopped'}`} />
              {state ? `${state.running ? 'running' : 'stopped'} · ${state.phase || 'idle'}` : 'idle'}
            </span>
            <button
              type="button"
              className={`btn btn-sm ${state?.running ? 'btn-danger' : 'btn-primary'}`}
              onClick={() =>
                runLoopAction(() =>
                  state?.running
                    ? window.autoplan.stopLoop({ projectId, manual: true })
                    : window.autoplan.startLoop({ projectId, manual: true }),
                )
              }
            >
              <Icon name={state?.running ? 'stop' : 'run'} size={14} aria-hidden />
              {state?.running ? '停止' : '启动'}
            </button>
          </div>
        </header>

        <section className={`view ${activeTab === 'overview' ? 'active' : ''}`}>
          {error ? <div className="error-banner">{error}</div> : null}
          <OverviewView snapshot={snapshot} state={state} onGoTasks={() => setActiveTab('tasks')} />
        </section>

        <section className={`view ${activeTab === 'requirement' ? 'active' : ''}`}>
          {activeTab === 'requirement' ? (
            <IntakePanel
              emptyText="暂无需求。也可以把需求文件放到工作区 docs/issues。"
              heading="需求记录"
              items={snapshot.requirements}
              pendingAttachments={pendingAttachments.requirement}
              placeholder="输入需求，Enter 发送，Shift+Enter 换行"
              submitLabel="发送需求"
              subtitle="循环开启后自动扫描并生成计划"
              type="requirement"
              attachments={snapshot.attachments}
              onAddFiles={addPendingFiles}
              onDelete={deleteRequirement}
              onRemoveAttachment={removePendingAttachment}
              onSubmit={createRequirement}
              onUpdate={updateRequirement}
              onInterrupt={interruptIntake}
              onResume={resumeIntake}
              onAppendTask={appendIntakeTask}
            />
          ) : null}
        </section>

        <section className={`view ${activeTab === 'feedback' ? 'active' : ''}`}>
          {activeTab === 'feedback' ? (
            <IntakePanel
              emptyText="暂无反馈。"
              heading="反馈记录"
              items={snapshot.feedback}
              pendingAttachments={pendingAttachments.feedback}
              placeholder="输入反馈，Enter 发送，Shift+Enter 换行"
              submitLabel="发送反馈"
              subtitle="循环开启后自动扫描并生成计划"
              type="feedback"
              attachments={snapshot.attachments}
              onAddFiles={addPendingFiles}
              onDelete={deleteFeedback}
              onRemoveAttachment={removePendingAttachment}
              onSubmit={createFeedback}
              onUpdate={updateFeedback}
              onInterrupt={interruptIntake}
              onResume={resumeIntake}
              onAppendTask={appendIntakeTask}
            />
          ) : null}
        </section>

        <section className={`view ${activeTab === 'tasks' ? 'active' : ''}`}>
          {activeTab === 'tasks' ? (
            <div className="task-layout">
              <aside className="task-sidebar">
                <form className="editor card" onSubmit={submitLoopConfig}>
                  <div className="card-head">
                    <h2>循环控制</h2>
                  </div>
                  <label className="field">
                    工作区路径
                    <input
                      value={loopForm.workspacePath}
                      onChange={(event) => setLoopForm((current) => ({ ...current, workspacePath: event.target.value }))}
                      placeholder="D:\project\GitHub\my-app"
                    />
                  </label>
                  <label className="field">
                    间隔秒数
                    <input
                      min="1"
                      type="number"
                      value={loopForm.intervalSeconds}
                      onChange={(event) => setLoopForm((current) => ({ ...current, intervalSeconds: event.target.value }))}
                    />
                  </label>
                  <label className="field">
                    验收命令（留空则不校验）
                    <input
                      value={loopForm.validationCommand}
                      onChange={(event) => setLoopForm((current) => ({ ...current, validationCommand: event.target.value }))}
                      placeholder="flutter analyze"
                    />
                  </label>
                  <div className="button-row">
                    <button type="submit">保存配置</button>
                    <button
                      type="button"
                      onClick={() =>
                        runLoopAction(() =>
                          state?.running
                            ? window.autoplan.stopLoop({ projectId, manual: true })
                            : window.autoplan.startLoop({ projectId, manual: true }),
                        )
                      }
                    >
                      {state?.running ? '停止' : '启动'}
                    </button>
                  </div>
                </form>
                <section className="side-section card">
                  <div className="card-head">
                    <h2>事件</h2>
                  </div>
                  <EventList events={snapshot.events} />
                </section>
              </aside>

              <div className="task-main">
                <div className="task-status-grid">
                  <section className="card">
                    <div className="card-head">
                      <h2>Plan</h2>
                    </div>
                    <PlanList plans={snapshot.plans} tasks={snapshot.tasks} />
                  </section>
                  <section className="card">
                    <div className="card-head">
                      <h2>任务</h2>
                    </div>
                    <TaskList
                      tasks={snapshot.tasks}
                      onRun={(task) => runLoopAction(() => window.autoplan.runTask({ projectId, taskId: task.id }))}
                      onStop={(task) => runLoopAction(() => window.autoplan.stopTask({ projectId, taskId: task.id }))}
                    />
                  </section>
                </div>
              </div>
            </div>
          ) : null}
        </section>

        <section className={`view ${activeTab === 'events' ? 'active' : ''}`}>
          {activeTab === 'events' ? (
            <section className="card">
              <div className="card-head">
                <h2>事件流</h2>
                <span className="hint">最近 80 条 · 实时更新</span>
              </div>
              <EventList events={snapshot.events} />
            </section>
          ) : null}
        </section>
      </div>
    </div>
  );
}

function WorkspaceSidebar({
  activeTab,
  onTab,
  onBack,
  projectId,
  projects,
  currentProject,
  state,
  onSwitchProject,
}: {
  activeTab: WorkspaceTab;
  onTab: (tab: WorkspaceTab) => void;
  onBack: () => void;
  projectId: number;
  projects: Project[];
  currentProject: Project | null;
  state: ProjectState | null;
  onSwitchProject: (id: number) => void;
}) {
  return (
    <aside className="sidebar">
      <div className="brand">
        <div className="brand-mark">A</div>
        <div>
          <div className="brand-name">AutoPlan</div>
          <div className="brand-sub">需求 · 计划 · 执行 · 验收</div>
        </div>
      </div>

      <button type="button" className="back-link" onClick={onBack}>
        <Icon name="back" size={16} aria-hidden />
        返回项目列表
      </button>

      <div className="project-switcher">
        <div className="project-label">当前项目</div>
        <select
          className="project-select"
          value={projectId}
          onChange={(event) => onSwitchProject(Number(event.target.value))}
        >
          {projects.map((project) => (
            <option key={project.id} value={project.id}>
              {project.name}
            </option>
          ))}
        </select>
        {currentProject ? <div className="project-path mono">{currentProject.workspace_path || '未设置工作区'}</div> : null}
      </div>

      <div className="nav-group-label">工作区</div>
      {tabs.map((tab) => (
        <button
          className={`nav-item ${activeTab === tab.id ? 'active' : ''}`}
          key={tab.id}
          type="button"
          onClick={() => onTab(tab.id)}
        >
          <span className="nav-ico">
            <Icon name={tab.icon} size={18} aria-hidden="true" />
          </span>
          <span>{tab.label}</span>
        </button>
      ))}

      <div className="sidebar-footer">
        <div className="loop-mini">
          <span className={`led ${state?.running ? 'running' : 'stopped'}`} />
          <span>
            循环 <b>{state?.running ? '运行中' : '已停止'}</b>
          </span>
        </div>
        <div className="loop-config mono">
          间隔 {state?.interval_seconds || 5}s
          {state?.validation_command ? ` · ${state.validation_command}` : ' · 无验收命令'}
        </div>
      </div>
    </aside>
  );
}

function WorkspaceSearchBox({
  hitCount,
  onQueryChange,
  query,
  totalCount,
}: {
  hitCount: number;
  onQueryChange: (query: string) => void;
  query: string;
  totalCount: number;
}) {
  const hasQuery = Boolean(normalizeSearchQuery(query));
  const resultLabel = hasQuery ? `命中 ${hitCount} 条` : `可搜索 ${totalCount} 条`;

  return (
    <div className="workspace-search" role="search" aria-label="工作区搜索">
      <div className="workspace-search-field">
        <Icon name="search" size={16} className="workspace-search-icon" aria-hidden="true" />
        <input
          aria-label="搜索当前工作区"
          className="workspace-search-input search-input"
          onChange={(event) => onQueryChange(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Escape' && query) {
              event.preventDefault();
              onQueryChange('');
            }
          }}
          placeholder="搜索需求、反馈、任务、Plan 或事件"
          value={query}
        />
        {query ? (
          <button
            type="button"
            className="workspace-search-clear"
            onClick={() => onQueryChange('')}
            aria-label="清空工作区搜索关键字"
          >
            <Icon name="close" size={15} aria-hidden="true" />
          </button>
        ) : null}
      </div>
      <span className={`workspace-search-count${hasQuery && hitCount === 0 ? ' is-empty' : ''}`} aria-live="polite">
        {resultLabel}
      </span>
    </div>
  );
}

function normalizeSearchQuery(value: string) {
  return value.trim().toLowerCase().replace(/\s+/g, ' ');
}

function countSearchableItems(snapshot: AppSnapshot) {
  return (
    snapshot.requirements.length +
    snapshot.feedback.length +
    snapshot.planDrafts.length +
    snapshot.plans.length +
    snapshot.tasks.length +
    snapshot.events.length
  );
}

function countWorkspaceSearchMatches(snapshot: AppSnapshot, normalizedQuery: string) {
  const groups: unknown[][] = [
    snapshot.requirements,
    snapshot.feedback,
    snapshot.planDrafts,
    snapshot.plans,
    snapshot.tasks,
    snapshot.events,
  ];

  return groups.reduce(
    (count, group) => count + group.filter((item) => normalizeSearchQuery(toSearchText(item)).includes(normalizedQuery)).length,
    0,
  );
}

function toSearchText(value: unknown, depth = 0): string {
  if (value === null || value === undefined || depth > 6) return '';
  if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return String(value);
  if (Array.isArray(value)) return value.map((item) => toSearchText(item, depth + 1)).join(' ');
  if (typeof value === 'object') {
    return Object.values(value as Record<string, unknown>)
      .map((item) => toSearchText(item, depth + 1))
      .join(' ');
  }
  return '';
}

function OverviewView({
  snapshot,
  state,
  onGoTasks,
}: {
  snapshot: AppSnapshot;
  state: ProjectState | null;
  onGoTasks: () => void;
}) {
  const reqCount = snapshot.requirements.length;
  const planCount = snapshot.plans.length;
  const runningPlan = snapshot.plans.find((plan) => !['completed'].includes(plan.status));
  const doneTasks = snapshot.tasks.filter((task) => task.status === 'completed').length;
  const totalTasks = snapshot.tasks.length;

  const phases = ['scan', 'generate-plan', 'execute-task', 'validate', 'completed'];
  const currentPhase = state?.phase || 'idle';
  const activeIndex = phases.indexOf(currentPhase);
  const operation = snapshot.activeOperation || snapshot.lastOperation;
  const operationActive = Boolean(snapshot.activeOperation);
  const operationTime = operation?.startedAt ? `开始于 ${formatChinaTime(operation.startedAt)}` : '';
  const operationExit =
    operation && !operationActive && typeof operation.exitCode === 'number'
      ? `退出码 ${operation.exitCode}${operation.exitCode === 0 ? '（成功）' : '（失败）'}`
      : '';
  const operationHint = operation
    ? [operationTime, operation.codexSessionLabel, operationExit].filter(Boolean).join(' · ')
    : '等待下一次执行';
  const operationTitle = operation ? `${operationActive ? '执行日志' : '最近执行'} · ${operation.label}` : '执行日志';

  return (
    <>
      <div className="stat-grid">
        <StatCard icon="requirement" value={String(reqCount)} label="需求" accent="brand" />
        <StatCard
          icon="plan"
          value={String(planCount)}
          label="计划"
          sub={runningPlan ? `${runningPlan.completed_tasks}/${runningPlan.total_tasks} 任务` : '无进行中'}
          accent="info"
        />
        <StatCard icon="tasks" value={`${doneTasks}/${totalTasks}`} label="任务进度" accent="success" />
        <StatCard icon="refresh" value={`${state?.interval_seconds || 5}s`} label="循环间隔" accent="warning" />
      </div>

      <div className="overview-grid">
        <div className="overview-main-column">
          <section className="card live-log-card">
            <div className="card-head log-card-head">
              <div className="log-title-line">
                <h2>
                  <span className={`live-dot${operationActive ? '' : ' idle'}`} /> {operationTitle}
                </h2>
                <span className="hint">{operationHint}</span>
                <span className={`log-phase-chip ${state?.running ? 'running' : 'stopped'}`}>
                  {state?.running ? '循环运行中' : '循环已停止'} · {currentPhase}
                </span>
              </div>
              <div className="log-summary">
                <span>
                  计划草稿 <b>{snapshot.planDrafts.length}</b>
                </span>
                <span>
                  待确认草稿 <b>{snapshot.planDrafts.filter((draft) => draft.status !== 'accepted').length}</b>
                </span>
                <span>
                  反馈 <b>{snapshot.feedback.length}</b>
                </span>
              </div>
            </div>
            <CodexLog log={operation?.logTail || ''} activity={operation?.activity || []} context={operation || null} />
          </section>
        </div>

        <div className="overview-side-column">
          <section className="card">
            <div className="card-head">
              <h2>循环阶段流水线</h2>
            </div>
            <div className="card-body">
              <div className="pipeline">
                {phases.map((phase, index) => {
                  const done = activeIndex > index;
                  const active = activeIndex === index;
                  return (
                    <div className={`pipe-step ${done ? 'done' : ''} ${active ? 'active' : ''}`} key={phase}>
                      <div className="pipe-node">
                        {done ? (
                          <Icon name="complete" size={18} className="pipe-status-icon" aria-hidden="true" />
                        ) : active ? (
                          <Icon name="run" size={18} className="pipe-status-icon" aria-hidden="true" />
                        ) : (
                          index + 1
                        )}
                      </div>
                      <div className="pipe-label">{phaseLabel(phase)}</div>
                    </div>
                  );
                })}
              </div>
            </div>
          </section>

          <section className="card">
            <div className="card-head">
              <h2>近期事件</h2>
              <span className="spacer">
                <button type="button" className="btn-link" onClick={onGoTasks}>
                  查看任务
                  <Icon name="enter" size={14} aria-hidden />
                </button>
              </span>
            </div>
            <div className="card-body">
              <EventList events={snapshot.events.slice(0, 8)} />
            </div>
          </section>
        </div>
      </div>
    </>
  );
}

function StatCard({
  icon,
  value,
  label,
  sub,
  accent,
}: {
  icon: IconName;
  value: string;
  label: string;
  sub?: string;
  accent: 'brand' | 'info' | 'success' | 'warning';
}) {
  return (
    <div className={`stat stat-${accent}`}>
      <div className="stat-ico">
        <Icon name={icon} size={20} aria-hidden="true" />
      </div>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
      {sub ? <div className="stat-delta">{sub}</div> : null}
    </div>
  );
}

function tabTitle(tab: WorkspaceTab) {
  return { overview: '概览', requirement: '需求模块', feedback: '反馈模块', tasks: '任务与计划', events: '事件流' }[tab];
}

function tabSubtitle(tab: WorkspaceTab, project: Project | null) {
  const base = {
    overview: '循环状态、阶段流水线与各模块一览',
    requirement: '收集需求，发送后自动生成计划草稿',
    feedback: '收集反馈，关联需求并生成计划草稿',
    tasks: '循环控制、计划草稿、Plan 与任务进度',
    events: '循环运行日志与任务执行记录',
  }[tab];
  return project ? `${base} · ${project.name}` : base;
}

function phaseLabel(phase: string) {
  return (
    {
      idle: '空闲',
      scan: '扫描',
      'generate-plan': '生成计划',
      'execute-task': '执行任务',
      validate: '验收',
      completed: '完成',
      waiting: '等待',
      error: '异常',
    }[phase] || phase
  );
}
