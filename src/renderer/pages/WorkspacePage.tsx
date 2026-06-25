import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { WORKSPACE_SEARCH_SOURCE_TYPES } from '../types';
import type {
  AppSnapshot,
  IntakeType,
  PendingAttachment,
  Plan,
  Project,
  ProjectState,
  WorkspacePlanReadState,
  WorkspaceSearchGroup,
  WorkspaceSearchSourceType,
  WorkspaceSearchState,
  WorkspaceTab,
} from '../types';
import { useSnapshot } from '../hooks/useSnapshot';
import { IntakePanel } from '../components/IntakePanel';
import { EventList, PlanDraftList, PlanList, TaskList } from '../components/PlanLists';
import { SearchResults } from '../components/SearchResults';
import { CodexLog } from '../components/CodexLog';
import { Icon, type IconName } from '../components/icons';
import { getFilePath } from '../components/shared';
import { searchWorkspaceSnapshot } from '../utils/search';
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

const searchNoMatchText = '没有匹配结果。';

type WorkspaceFilterableItems = Pick<AppSnapshot, 'requirements' | 'feedback' | 'planDrafts' | 'plans' | 'tasks' | 'events'>;

function createEmptyPlanReadState(): WorkspacePlanReadState {
  return { plan: null, result: null, loading: false, error: null };
}

function getErrorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === 'string' && error.trim()) return error.trim();
  return fallback;
}

export function WorkspacePage() {
  const params = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const projectId = Number(params.projectId);
  const { snapshot, setSnapshot, error, setError } = useSnapshot(Number.isFinite(projectId) ? projectId : null);
  const [activeTab, setActiveTab] = useState<WorkspaceTab>('overview');
  const [pendingAttachments, setPendingAttachments] =
    useState<Record<IntakeType, PendingAttachment[]>>(emptyPendingAttachments);
  const [loopForm, setLoopForm] = useState({ workspacePath: '', intervalSeconds: '5', validationCommand: '' });
  const [draftTextById, setDraftTextById] = useState<Record<number, string>>({});
  const [searchQuery, setSearchQuery] = useState('');
  const [planReadState, setPlanReadState] = useState<WorkspacePlanReadState>(() => createEmptyPlanReadState());
  const planReadRequestRef = useRef(0);

  const project = snapshot?.activeProject || null;
  const state = snapshot?.state || null;
  const projects = snapshot?.projects || [];
  const workspaceSearch = useMemo(() => searchWorkspaceSnapshot(snapshot, searchQuery), [snapshot, searchQuery]);
  const searchableItemCount = useMemo(() => (snapshot ? countSearchableItems(snapshot) : 0), [snapshot]);
  const searchHitCount = workspaceSearch.total;
  const isSearching = !workspaceSearch.query.isEmpty;
  const filteredItems = useMemo(
    () => createFilteredWorkspaceItems(snapshot, workspaceSearch),
    [snapshot, workspaceSearch],
  );
  const filteredEmptyText = isSearching ? searchNoMatchText : undefined;
  const latestReadingPlan = useMemo(() => {
    if (!planReadState.plan) return null;
    return (
      snapshot?.plans.find(
        (plan) => plan.id === planReadState.plan?.id && plan.project_id === planReadState.plan?.project_id,
      ) || planReadState.plan
    );
  }, [planReadState.plan, snapshot?.plans]);

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

  useEffect(() => {
    planReadRequestRef.current += 1;
    setPlanReadState(createEmptyPlanReadState());
    return () => {
      planReadRequestRef.current += 1;
    };
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

  const changePlanDraft = useCallback((id: number, markdown: string) => {
    setDraftTextById((current) => ({ ...current, [id]: markdown }));
  }, []);

  const savePlanDraft = useCallback(
    async (draft: AppSnapshot['planDrafts'][number]) => {
      try {
        const next = await window.autoplan.updatePlanDraft({
          id: draft.id,
          markdown: draftTextById[draft.id] ?? draft.markdown ?? '',
        });
        setSnapshot(next);
        setDraftTextById((current) => {
          if (!(draft.id in current)) return current;
          const nextDraftText = { ...current };
          delete nextDraftText[draft.id];
          return nextDraftText;
        });
        setError(null);
      } catch (e) {
        showError(e);
      }
    },
    [draftTextById, setSnapshot, setError, showError],
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

  const readPlanForReader = useCallback(async (plan: Plan) => {
    const requestId = planReadRequestRef.current + 1;
    planReadRequestRef.current = requestId;
    setPlanReadState({ plan, result: null, loading: true, error: null });

    try {
      const result = await window.autoplan.readPlan({ projectId: plan.project_id, planId: plan.id });
      if (planReadRequestRef.current !== requestId) return;

      setPlanReadState({
        plan: {
          ...plan,
          file_path: result.file_path || plan.file_path,
          hash: result.hash || plan.hash,
          updated_at: result.updated_at || plan.updated_at,
        },
        result,
        loading: false,
        error: result.ok ? null : result.error || '读取 Plan 全文失败',
      });
    } catch (e) {
      if (planReadRequestRef.current !== requestId) return;
      setPlanReadState({
        plan,
        result: null,
        loading: false,
        error: getErrorMessage(e, '读取 Plan 全文失败'),
      });
    }
  }, []);

  const openPlanReader = useCallback(
    (plan: Plan) => {
      const currentPlan = planReadState.plan;
      if (planReadState.loading && currentPlan && currentPlan.id === plan.id && currentPlan.project_id === plan.project_id) {
        return;
      }
      void readPlanForReader(plan);
    },
    [planReadState.loading, planReadState.plan?.id, planReadState.plan?.project_id, readPlanForReader],
  );

  const closePlanReader = useCallback(() => {
    planReadRequestRef.current += 1;
    setPlanReadState(createEmptyPlanReadState());
  }, []);

  const refreshPlanReader = useCallback(() => {
    if (planReadState.loading) return;
    const plan = latestReadingPlan || planReadState.plan;
    if (!plan) return;
    void readPlanForReader(plan);
  }, [latestReadingPlan, planReadState.loading, planReadState.plan, readPlanForReader]);

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

        <SearchResults
          onClear={() => setSearchQuery('')}
          onSelectGroup={setActiveTab}
          onSelectResult={(result) => setActiveTab(result.targetTab)}
          searchState={workspaceSearch}
        />

        <section className={`view ${activeTab === 'overview' ? 'active' : ''}`}>
          {error ? <div className="error-banner">{error}</div> : null}
          <OverviewView snapshot={snapshot} state={state} onGoTasks={() => setActiveTab('tasks')} />
        </section>

        <section className={`view ${activeTab === 'requirement' ? 'active' : ''}`}>
          {activeTab === 'requirement' ? (
            <IntakePanel
              emptyText={filteredEmptyText || '暂无需求。也可以把需求文件放到工作区 docs/issues。'}
              heading="需求记录"
              items={filteredItems.requirements}
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
              emptyText={filteredEmptyText || '暂无反馈。'}
              heading="反馈记录"
              items={filteredItems.feedback}
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
                  <EventList emptyText={filteredEmptyText} events={filteredItems.events} />
                </section>
              </aside>

              <div className="task-main">
                <div className="task-status-grid">
                  <section className="card">
                    <div className="card-head">
                      <h2>计划草稿</h2>
                    </div>
                    <PlanDraftList
                      drafts={filteredItems.planDrafts}
                      draftTextById={draftTextById}
                      emptyText={filteredEmptyText}
                      onChange={changePlanDraft}
                      onSave={savePlanDraft}
                    />
                  </section>
                  <section className="card">
                    <div className="card-head">
                      <h2>Plan</h2>
                    </div>
                    <PlanList
                      emptyText={filteredEmptyText}
                      latestReadingPlan={latestReadingPlan}
                      onCloseReader={closePlanReader}
                      onOpenReader={openPlanReader}
                      onRefreshReader={refreshPlanReader}
                      plans={filteredItems.plans}
                      readerState={planReadState}
                      tasks={snapshot.tasks}
                      totalPlanCount={snapshot.plans.length}
                    />
                  </section>
                  <section className="card">
                    <div className="card-head">
                      <h2>任务</h2>
                    </div>
                    <TaskList
                      emptyText={filteredEmptyText}
                      tasks={filteredItems.tasks}
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
              <EventList emptyText={filteredEmptyText} events={filteredItems.events} />
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

function createFilteredWorkspaceItems(
  snapshot: AppSnapshot | null | undefined,
  searchState: WorkspaceSearchState,
): WorkspaceFilterableItems {
  if (!snapshot) {
    return { requirements: [], feedback: [], planDrafts: [], plans: [], tasks: [], events: [] };
  }
  if (searchState.query.isEmpty) {
    return {
      requirements: snapshot.requirements,
      feedback: snapshot.feedback,
      planDrafts: snapshot.planDrafts,
      plans: snapshot.plans,
      tasks: snapshot.tasks,
      events: snapshot.events,
    };
  }

  return {
    requirements: filterItemsBySearchGroup(
      snapshot.requirements,
      searchState.groups,
      WORKSPACE_SEARCH_SOURCE_TYPES.REQUIREMENT,
    ),
    feedback: filterItemsBySearchGroup(snapshot.feedback, searchState.groups, WORKSPACE_SEARCH_SOURCE_TYPES.FEEDBACK),
    planDrafts: filterItemsBySearchGroup(
      snapshot.planDrafts,
      searchState.groups,
      WORKSPACE_SEARCH_SOURCE_TYPES.PLAN_DRAFT,
    ),
    plans: filterItemsBySearchGroup(snapshot.plans, searchState.groups, WORKSPACE_SEARCH_SOURCE_TYPES.PLAN),
    tasks: filterItemsBySearchGroup(snapshot.tasks, searchState.groups, WORKSPACE_SEARCH_SOURCE_TYPES.TASK),
    events: filterItemsBySearchGroup(snapshot.events, searchState.groups, WORKSPACE_SEARCH_SOURCE_TYPES.EVENT),
  };
}

function filterItemsBySearchGroup<T extends { id: number }>(
  items: T[],
  groups: WorkspaceSearchGroup[],
  source: WorkspaceSearchSourceType,
) {
  const group = groups.find((item) => item.source === source);
  if (!group?.results.length) return [];

  const recordIds = new Set(group.results.map((result) => result.recordId));
  return items.filter((item) => recordIds.has(item.id));
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
