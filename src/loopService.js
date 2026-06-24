const crypto = require('node:crypto');
const { EventEmitter } = require('node:events');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { nowIso } = require('./database');
const { CodexActivityPrinter } = require('./codexActivity');

const ACTIVE_RUNTIME_PHASES = new Set(['running', 'scan', 'generate-plan', 'execute-task', 'validate']);
const ACTIVE_RUNTIME_PHASE_SQL = "('running','scan','generate-plan','execute-task','validate')";
const MAX_PARALLEL_TASKS = 2;
const PARALLEL_BLOCKING_TASK_RE = /(全量|回归|验证|验收|测试|记录|整理|发布|部署|test|validate|regression|release|deploy)/i;
const TASK_SCOPE_LABEL_RE = '(?:scope|scopes|files?|影响范围|并发键)';
const TASK_SCOPE_RE = new RegExp(`${TASK_SCOPE_LABEL_RE}\\s*[:=：]\\s*([^>\\]\\n]+)`, 'i');
const TASK_SCOPE_COMMENT_RE = new RegExp(`\\s*<!--\\s*${TASK_SCOPE_LABEL_RE}\\s*[:=：]\\s*([^>]*?)\\s*-->\\s*`, 'i');
const TASK_SCOPE_SPLIT_RE = /[,，、;；]+/;
const TASK_PATH_RE = /[\w./\\-]+\.(?:dart|js|jsx|ts|tsx|css|scss|html|md|json|ya?ml)/gi;
const TASK_LIFECYCLE_EVENT_RECORDED = Symbol('taskLifecycleEventRecorded');
const CODEX_SESSION_UUID_RE_SOURCE = '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}';
const CODEX_SESSION_ID_RES = Object.freeze([
  new RegExp(`\\bsession\\s+id:\\s*(${CODEX_SESSION_UUID_RE_SOURCE})\\b`, 'i'),
  new RegExp(`"(?:session_id|sessionId)"\\s*:\\s*"(${CODEX_SESSION_UUID_RE_SOURCE})"`, 'i'),
  new RegExp(`\\b(?:session_id|sessionId)\\s*[:=]\\s*(${CODEX_SESSION_UUID_RE_SOURCE})\\b`, 'i'),
]);
const CODEX_RESUME_FAILURE_RE = /(?:thread\/resume|resume failed|no rollout found|session\s+(?:not\s+found|missing)|conversation\s+not\s+found|unknown\s+session|invalid\s+session)/i;

const TASK_EVENT_TYPES = Object.freeze({
  STARTED: 'task.started',
  SUCCEEDED: 'task.succeeded',
  FAILED: 'task.failed',
  STOP_REQUESTED: 'task.stop.requested',
  STOPPED: 'task.stopped',
  INTERRUPTED: 'task.interrupted',
});

const LEGACY_TASK_EVENT_TYPES = Object.freeze({
  EXECUTED: 'task.executed',
  STOPPING: 'task.stopping',
});

const TASK_EVENT_STATUS = Object.freeze({
  PENDING: 'pending',
  RUNNING: 'running',
  COMPLETED: 'completed',
  FAILED: 'failed',
  STOPPING: 'stopping',
  STOPPED: 'stopped',
  INTERRUPTED: 'interrupted',
});

const TASK_EVENT_SEMANTICS = Object.freeze({
  [TASK_EVENT_TYPES.STARTED]: Object.freeze({ status: TASK_EVENT_STATUS.RUNNING, label: '开始了任务' }),
  [TASK_EVENT_TYPES.SUCCEEDED]: Object.freeze({ status: TASK_EVENT_STATUS.COMPLETED, label: '结束了任务' }),
  [TASK_EVENT_TYPES.FAILED]: Object.freeze({ status: TASK_EVENT_STATUS.FAILED, label: '任务失败' }),
  [TASK_EVENT_TYPES.STOP_REQUESTED]: Object.freeze({ status: TASK_EVENT_STATUS.STOPPING, label: '请求停止任务' }),
  [TASK_EVENT_TYPES.STOPPED]: Object.freeze({ status: TASK_EVENT_STATUS.STOPPED, label: '已停止任务' }),
  [TASK_EVENT_TYPES.INTERRUPTED]: Object.freeze({ status: TASK_EVENT_STATUS.INTERRUPTED, label: '已中断任务' }),
});

const TASK_EVENT_COMPATIBILITY = Object.freeze({
  [LEGACY_TASK_EVENT_TYPES.EXECUTED]: TASK_EVENT_TYPES.SUCCEEDED,
  [LEGACY_TASK_EVENT_TYPES.STOPPING]: TASK_EVENT_TYPES.STOPPED,
});

class LoopService extends EventEmitter {
  constructor(db) {
    super();
    this.db = db;
    this.runtimes = new Map();
    this.resetRuntimeState();
  }

  runtime(projectId) {
    const id = Number(projectId || 0);
    if (!id) return null;
    let runtime = this.runtimes.get(id);
    if (!runtime) {
      runtime = createProjectRuntime();
      this.runtimes.set(id, runtime);
    }
    return runtime;
  }

  existingRuntime(projectId) {
    return this.runtimes.get(Number(projectId || 0)) || null;
  }

  projects() {
    return this.db
      .all('SELECT * FROM projects ORDER BY updated_at DESC, id DESC')
      .map((project) => this.withProjectRuntimeSummary(project));
  }

  withProjectRuntimeSummary(project) {
    const state =
      this.db.get(
        'SELECT phase, interval_seconds, validation_command FROM project_states WHERE project_id = ?',
        [project.id],
      ) || {};
    const runtime = this.existingRuntime(project.id);
    const running = runtime?.running ? 1 : 0;
    let phase = state.phase || 'idle';
    if (!running && !runtime?.busy && ACTIVE_RUNTIME_PHASES.has(String(phase || ''))) {
      phase = 'stopped';
    }
    return {
      ...project,
      running,
      phase,
      interval_seconds: Number(state.interval_seconds || 5),
      validation_command: state.validation_command || '',
    };
  }

  project(projectId) {
    if (!projectId) return null;
    return this.db.get('SELECT * FROM projects WHERE id = ?', [projectId]);
  }

  defaultProjectId() {
    return this.projects()[0]?.id || null;
  }

  ensureProjectState(projectId) {
    if (!projectId) return;
    this.db.run(
      `INSERT OR IGNORE INTO project_states
       (project_id, running, phase, interval_seconds, validation_command, updated_at)
       VALUES (?, 0, 'idle', 5, 'flutter analyze', ?)`,
      [projectId, nowIso()],
    );
  }

  resetRuntimeState() {
    const now = nowIso();
    this.db.run(
      `UPDATE project_states
       SET running = 0,
           phase = CASE WHEN phase IN ${ACTIVE_RUNTIME_PHASE_SQL} THEN 'stopped' ELSE phase END,
           updated_at = ?
       WHERE running != 0 OR phase IN ${ACTIVE_RUNTIME_PHASE_SQL}`,
      [now],
    );
    this.db.run(
      `UPDATE loop_state
       SET running = 0,
           phase = CASE WHEN phase IN ${ACTIVE_RUNTIME_PHASE_SQL} THEN 'stopped' ELSE phase END,
           updated_at = ?
       WHERE id = 1 AND (running != 0 OR phase IN ${ACTIVE_RUNTIME_PHASE_SQL})`,
      [now],
    );
    this.db.run('UPDATE plan_tasks SET status = ?, updated_at = ? WHERE status = ?', ['pending', now, 'running']);
  }

  status(projectId = this.defaultProjectId()) {
    if (!projectId) return null;
    this.ensureProjectState(projectId);
    return this.normalizeRuntimeStatus(projectId, this.db.get('SELECT * FROM project_states WHERE project_id = ?', [projectId]));
  }

  normalizeRuntimeStatus(projectId, state) {
    if (!state) return null;
    const runtime = this.existingRuntime(projectId);
    const runtimeRunning = Boolean(runtime?.running);
    const normalized = {
      ...state,
      running: runtimeRunning ? 1 : 0,
    };

    if (!runtimeRunning && !runtime?.busy && ACTIVE_RUNTIME_PHASES.has(String(normalized.phase || ''))) {
      normalized.phase = 'stopped';
    }

    return normalized;
  }

  configure(projectId, { workspacePath, intervalSeconds, validationCommand }) {
    const current = this.status(projectId);
    const project = this.project(projectId);
    const runtime = this.existingRuntime(projectId);
    const nextWorkspace = workspacePath ?? project?.workspace_path;
    const workspaceOwner = this.activeProjectForWorkspace(nextWorkspace, projectId);
    if ((runtime?.running || runtime?.busy) && workspaceOwner) {
      throw new Error(`工作区正在被项目「${workspaceOwner.name}」使用，请先停止对应循环`);
    }
    if (!project || !current) throw new Error('项目不存在');

    this.db.run(
      `UPDATE projects
       SET workspace_path = ?, updated_at = ?
       WHERE id = ?`,
      [nextWorkspace, nowIso(), projectId],
    );
    const nextInterval = Number(intervalSeconds || current.interval_seconds || 5);
    this.db.run(
      `UPDATE project_states
       SET interval_seconds = ?, validation_command = ?, updated_at = ?
       WHERE project_id = ?`,
      [
        nextInterval,
        validationCommand ?? current.validation_command,
        nowIso(),
        projectId,
      ],
    );
    if (runtime?.running) this.scheduleProject(projectId, nextInterval);
    this.emitUpdate(projectId);
  }

  scheduleProject(projectId, intervalSeconds) {
    const runtime = this.runtime(projectId);
    if (!runtime) return;
    if (runtime.timer) clearInterval(runtime.timer);
    runtime.timer = setInterval(() => {
      if (!runtime.running) return;
      this.runOnce(projectId).catch((error) => this.recordError(projectId, error));
    }, Math.max(1, Number(intervalSeconds || 5)) * 1000);
  }

  activeProjectForWorkspace(workspace, projectId) {
    const key = workspaceKey(workspace);
    if (!key) return null;
    for (const [id, runtime] of this.runtimes.entries()) {
      if (Number(id) === Number(projectId)) continue;
      if (!runtime.running && !runtime.busy && !runtime.activeChild) continue;
      const project = this.project(id);
      if (workspaceKey(project?.workspace_path) === key) return project;
    }
    return null;
  }

  start(projectId) {
    const state = this.status(projectId);
    const project = this.project(projectId);
    if (!project || !state) throw new Error('项目不存在');
    if (!project.workspace_path) throw new Error('请先设置项目工作区路径');
    const runtime = this.runtime(projectId);
    if (!runtime) return;
    if (runtime.running) return;
    const workspaceOwner = this.activeProjectForWorkspace(project.workspace_path, projectId);
    if (workspaceOwner) {
      throw new Error(`工作区正在被项目「${workspaceOwner.name}」使用，请先停止对应循环`);
    }

    runtime.running = true;
    this.db.run(
      'UPDATE project_states SET running = 1, phase = ?, updated_at = ? WHERE project_id = ?',
      ['running', nowIso(), projectId],
    );
    this.emitUpdate(projectId);
    this.runOnce(projectId).catch((error) => this.recordError(projectId, error));
    this.scheduleProject(projectId, state.interval_seconds);
  }

  stop(projectId = null) {
    if (!projectId) {
      for (const id of Array.from(this.runtimes.keys())) this.stop(id);
      return;
    }
    const runtime = this.runtime(projectId);
    if (runtime?.timer) clearInterval(runtime.timer);
    if (runtime) {
      runtime.timer = null;
      runtime.running = false;
    }
    if (runtime?.activeOperations?.size) {
      for (const [operationKey, operation] of Array.from(runtime.activeOperations.entries())) {
        const child = runtime.activeChildren.get(operationKey);
        const activeTaskId = operation?.taskId || null;
        const finishedAt = nowIso();
        killChildProcess(child);
        const activeTask = activeTaskId ? this.taskForProject(projectId, activeTaskId) : null;
        const stoppedTask = activeTaskId
          ? this.finishTaskRun(activeTaskId, TASK_EVENT_STATUS.PENDING, finishedAt, { onlyIfRunning: true })
          : null;
        const eventTask = stoppedTask || activeTask || (activeTaskId ? { id: activeTaskId, plan_id: operation?.planId } : null);
        if (eventTask) {
          this.addTaskLifecycleEvent(projectId, TASK_EVENT_TYPES.STOPPED, eventTask, {
            status: TASK_EVENT_STATUS.STOPPED,
            finishedAt,
            log: operation?.logFile,
            exitCode: typeof operation?.exitCode === 'number' ? operation.exitCode : undefined,
          });
        } else {
          this.addEvent(projectId, 'operation.stopping', operation?.label || '');
        }
      }
    }
    this.db.run(
      'UPDATE project_states SET running = 0, phase = ?, updated_at = ? WHERE project_id = ?',
      ['stopped', nowIso(), projectId],
    );
    this.addEvent(projectId, 'loop.stopped', '循环已停止');
    this.emitUpdate(projectId);
  }

  async runOnce(projectId = this.defaultProjectId()) {
    if (!projectId) return;
    const runtime = this.runtime(projectId);
    if (!runtime || runtime.busy) return;
    runtime.busy = true;
    try {
      const project = this.project(projectId);
      const workspace = project?.workspace_path;
      if (!workspace) return;
      this.setPhase(projectId, 'scan');
      this.ensureWorkspaceDirs(workspace);

      // 扫描未完成、未中断、尚未生成计划的需求和反馈
      const pendingRequirements = this.db
        .all(
          `SELECT * FROM requirements
           WHERE project_id = ? AND linked_plan_id IS NULL
             AND status NOT IN ('completed', 'closed')
           ORDER BY created_at ASC`,
          [projectId],
        )
        .map((row) => ({ ...row, __type: 'requirement' }));
      const pendingFeedback = this.db
        .all(
          `SELECT * FROM feedback
           WHERE project_id = ? AND linked_plan_id IS NULL
             AND status NOT IN ('completed', 'closed')
           ORDER BY created_at ASC`,
          [projectId],
        )
        .map((row) => ({ ...row, __type: 'feedback' }));
      const pendingIntakes = [...pendingRequirements, ...pendingFeedback];
      this.addEvent(projectId, 'scan.done', `待处理需求/反馈=${pendingIntakes.length}`);

      // 一次只生成一个计划（保持串行，避免 codex 并发），剩余等下一轮 timer
      let generatedPlanId = null;
      if (pendingIntakes.length > 0) {
        generatedPlanId = await this.generatePlanForIntake(projectId, workspace, pendingIntakes[0]);
      }

      // 同步 docs/plan 目录下的 plan 文件（兼容文件式需求）
      const planScan = this.scanDirectory(path.join(workspace, 'docs', 'plan'), workspace, ['.md']);
      this.saveScan(projectId, 'plan', planScan);

      // 执行队列里可运行的 plan
      const nextPlan = generatedPlanId
        ? this.db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [generatedPlanId, projectId])
        : this.nextRunnablePlan(projectId);
      if (!nextPlan) {
        if (runtime.running || this.db.get('SELECT phase FROM project_states WHERE project_id = ?', [projectId])?.phase !== 'stopped') {
          this.setPhase(projectId, pendingIntakes.length > 0 && runtime.running ? 'waiting' : 'idle');
        }
        return;
      }

      await this.processPlan(workspace, nextPlan);
      if (runtime.running) {
        this.setPhase(projectId, 'waiting');
      } else if (this.db.get('SELECT phase FROM project_states WHERE project_id = ?', [projectId])?.phase !== 'stopped') {
        this.setPhase(projectId, 'idle');
      }
    } finally {
      runtime.busy = false;
      this.emitUpdate(projectId);
    }
  }

  async runTask(projectId, taskId) {
    const runtime = this.runtime(projectId);
    if (!runtime) return;
    if (runtime.busy) throw new Error('该项目已有任务正在执行，请稍后再试');
    const project = this.project(projectId);
    const task = this.taskForProject(projectId, taskId);
    const plan = task ? this.db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [task.plan_id, projectId]) : null;
    const workspace = project?.workspace_path;
    if (!project || !task || !plan) throw new Error('任务不存在');
    if (!workspace) throw new Error('请先设置项目工作区路径');
    const workspaceOwner = this.activeProjectForWorkspace(workspace, projectId);
    if (workspaceOwner) {
      throw new Error(`工作区正在被项目「${workspaceOwner.name}」使用，请先停止对应循环`);
    }

    runtime.busy = true;
    try {
      const result = await this.executeTask(workspace, plan, task);
      if (result.exitCode === 0) {
        this.completeTask(workspace, plan, task, result);
      }
      if (runtime.running) {
        this.setPhase(projectId, 'waiting');
      } else if (this.db.get('SELECT phase FROM project_states WHERE project_id = ?', [projectId])?.phase !== 'stopped') {
        this.setPhase(projectId, 'idle');
      }
    } finally {
      runtime.busy = false;
      this.emitUpdate(projectId);
    }
  }

  stopTask(projectId, taskId) {
    const task = this.taskForProject(projectId, taskId);
    if (!task) throw new Error('任务不存在');
    const runtime = this.runtime(projectId);

    const activeEntry = findRuntimeOperation(runtime, (operation) => Number(operation?.taskId) === Number(taskId));
    if (activeEntry) {
      killChildProcess(activeEntry.child);
      const finishedAt = nowIso();
      const stoppedTask = runtime?.running
        ? task
        : this.finishTaskRun(taskId, TASK_EVENT_STATUS.PENDING, finishedAt, { onlyIfRunning: true }) || task;
      if (!runtime.running) {
        this.addTaskLifecycleEvent(projectId, TASK_EVENT_TYPES.STOPPED, stoppedTask, {
          status: TASK_EVENT_STATUS.STOPPED,
          finishedAt,
          log: activeEntry.operation?.logFile,
          exitCode: typeof activeEntry.operation?.exitCode === 'number' ? activeEntry.operation.exitCode : undefined,
        });
      }
    } else {
      const finishedAt = nowIso();
      const stoppedTask = this.finishTaskRun(taskId, TASK_EVENT_STATUS.PENDING, finishedAt, { onlyIfRunning: true }) || task;
      this.addTaskLifecycleEvent(projectId, TASK_EVENT_TYPES.STOP_REQUESTED, stoppedTask, {
        status: TASK_EVENT_STATUS.STOPPING,
        finishedAt,
      });
    }

    if (runtime?.running) this.stop(projectId);
    else this.setPhase(projectId, 'stopped');
  }

  taskForProject(projectId, taskId) {
    if (!taskId) return null;
    return this.db.get(
      `SELECT plan_tasks.*
       FROM plan_tasks JOIN plans ON plans.id = plan_tasks.plan_id
       WHERE plan_tasks.id = ? AND plans.project_id = ?`,
      [taskId, projectId],
    );
  }

  startTaskRun(taskId, startedAt = nowIso()) {
    this.db.run(
      `UPDATE plan_tasks
       SET status = ?,
           started_at = CASE WHEN status = ? AND started_at IS NOT NULL THEN started_at ELSE ? END,
           finished_at = NULL,
           updated_at = ?
       WHERE id = ?`,
      [TASK_EVENT_STATUS.RUNNING, TASK_EVENT_STATUS.RUNNING, startedAt, startedAt, taskId],
    );
    return this.db.get('SELECT * FROM plan_tasks WHERE id = ?', [taskId]);
  }

  updateTaskCodexSession(taskId, sessionId, updatedAt = nowIso()) {
    const normalizedSessionId = normalizeCodexSessionId(sessionId);
    if (!normalizedSessionId) return this.db.get('SELECT * FROM plan_tasks WHERE id = ?', [taskId]);
    this.db.run(
      `UPDATE plan_tasks
       SET codex_session_id = ?, updated_at = ?
       WHERE id = ?`,
      [normalizedSessionId, updatedAt, taskId],
    );
    return this.db.get('SELECT * FROM plan_tasks WHERE id = ?', [taskId]);
  }

  finishTaskRun(taskId, status, finishedAt = nowIso(), options = {}) {
    const task = this.db.get('SELECT * FROM plan_tasks WHERE id = ?', [taskId]);
    if (!task) return null;
    const isRunning = task.status === TASK_EVENT_STATUS.RUNNING;
    if (options.onlyIfRunning && !isRunning) return withTaskDurationMeta(task);

    const runDurationMs = isRunning ? taskRunDurationMs(task.started_at, finishedAt) : undefined;
    const durationMs = normalizeDurationMs(task.duration_ms) + (runDurationMs || 0);
    this.db.run(
      `UPDATE plan_tasks
       SET status = ?, duration_ms = ?, finished_at = ?, updated_at = ?
       WHERE id = ?`,
      [status, durationMs, finishedAt, finishedAt, taskId],
    );
    return withTaskDurationMeta(
      {
        ...task,
        status,
        duration_ms: durationMs,
        finished_at: finishedAt,
        updated_at: finishedAt,
      },
      runDurationMs,
    );
  }

  addTaskLifecycleEvent(projectId, type, task, metaOverrides = {}) {
    const meta = taskEventMeta(task, metaOverrides);
    this.addEvent(projectId, type, taskEventMessage(type, task, meta), meta);
  }

  recordTaskFailure(projectId, plan, task, finishedAt = nowIso(), metaOverrides = {}) {
    const currentTask = this.db.get('SELECT * FROM plan_tasks WHERE id = ?', [task.id]) || task;
    if (currentTask.status !== TASK_EVENT_STATUS.RUNNING) return null;
    const failedTask = this.finishTaskRun(task.id, TASK_EVENT_STATUS.PENDING, finishedAt, { onlyIfRunning: true }) || currentTask;
    this.addTaskLifecycleEvent(projectId, TASK_EVENT_TYPES.FAILED, failedTask, {
      planId: plan?.id,
      status: TASK_EVENT_STATUS.FAILED,
      finishedAt,
      ...metaOverrides,
    });
    return failedTask;
  }

  /** 中断某个 plan：停止正在执行的 codex 进程，未完成任务标记为 blocked，plan 标记为 interrupted */
  interruptPlan(projectId, planId) {
    const plan = this.db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [planId, projectId]);
    if (!plan) return;
    // 若当前正在执行该 plan 的任务，kill 进程
    const runtime = this.runtime(projectId);
    const activePlanOperations = runtime
      ? findRuntimeOperations(runtime, (operation) => Number(operation?.planId) === Number(planId))
      : [];
    for (const activeEntry of activePlanOperations) {
      const finishedAt = nowIso();
      killChildProcess(activeEntry.child);
      const activeTaskId = activeEntry.operation?.taskId;
      const activeTask = activeTaskId ? this.taskForProject(projectId, activeTaskId) : null;
      const interruptedTask = activeTaskId
        ? this.finishTaskRun(activeTaskId, 'blocked', finishedAt, { onlyIfRunning: true })
        : null;
      const eventTask = interruptedTask || activeTask || (activeTaskId ? { id: activeTaskId, plan_id: planId } : null);
      if (eventTask) {
        this.addTaskLifecycleEvent(projectId, TASK_EVENT_TYPES.INTERRUPTED, eventTask, {
          planId,
          taskId: activeTaskId || undefined,
          status: TASK_EVENT_STATUS.INTERRUPTED,
          finishedAt,
          log: activeEntry.operation?.logFile,
          exitCode: typeof activeEntry.operation?.exitCode === 'number' ? activeEntry.operation.exitCode : undefined,
        });
      }
    }
    // 其余未完成任务 → blocked
    this.db.run(
      `UPDATE plan_tasks SET status = ?, updated_at = ?
       WHERE plan_id = ? AND status IN ('pending', 'running')`,
      ['blocked', nowIso(), planId],
    );
    this.db.run('UPDATE plans SET status = ?, updated_at = ? WHERE id = ?', ['interrupted', nowIso(), planId]);
    this.addEvent(projectId, 'plan.interrupted', `plan #${planId} 已中断，未完成任务已挂起`);
    this.emitUpdate(projectId);
  }

  /** 恢复被中断的 plan：blocked → pending，plan → pending，循环运行时自动继续执行 */
  resumePlan(projectId, planId) {
    this.db.run(
      `UPDATE plan_tasks SET status = ?, updated_at = ?
       WHERE plan_id = ? AND status = ?`,
      ['pending', nowIso(), planId, 'blocked'],
    );
    this.db.run('UPDATE plans SET status = ?, updated_at = ? WHERE id = ?', ['pending', nowIso(), planId]);
    this.addEvent(projectId, 'plan.resumed', `plan #${planId} 已恢复`);
    this.emitUpdate(projectId);
  }

  /** 向现有 plan 追加一个任务：写回 plan 文件 + syncPlanTasks 重新解析 */
  appendTask(projectId, planId, title) {
    const project = this.project(projectId);
    const workspace = project?.workspace_path;
    const plan = this.db.get('SELECT * FROM plans WHERE id = ? AND project_id = ?', [planId, projectId]);
    if (!plan) throw new Error('计划不存在');
    if (!workspace) throw new Error('请先设置项目工作区路径');

    const planFile = path.join(workspace, plan.file_path);
    if (!fs.existsSync(planFile)) throw new Error('plan 文件不存在，无法追加任务');

    const cleanTitle = String(title || '').trim();
    if (!cleanTitle) throw new Error('任务标题不能为空');

    // 计算下一个 task_key
    const existing = this.db.all('SELECT task_key FROM plan_tasks WHERE plan_id = ? ORDER BY sort_order', [planId]);
    const maxNum = existing.reduce((max, row) => {
      const m = String(row.task_key || '').match(/P0*(\d+)/i);
      return m ? Math.max(max, Number(m[1])) : max;
    }, 0);
    const taskKey = `P${String(maxNum + 1).padStart(3, '0')}`;

    // 追加到 plan 文件的"## 任务计划"段末尾
    let content = fs.readFileSync(planFile, 'utf8');
    const line = ensureTaskScopeComment(`- [ ] ${taskKey}: ${cleanTitle}`);
    const taskSectionIdx = content.search(/##\s*任务计划/);
    if (taskSectionIdx === -1) {
      content = `${content.trim()}\n\n## 任务计划\n${line}\n`;
    } else {
      content = `${content.trimEnd()}\n${line}\n`;
    }
    fs.writeFileSync(planFile, content, 'utf8');

    // 若 plan 被中断过，恢复为 pending 让新任务可执行
    if (plan.status === 'interrupted') {
      this.db.run('UPDATE plans SET status = ?, updated_at = ? WHERE id = ?', ['pending', nowIso(), planId]);
    }
    // 重新解析任务入库
    this.syncPlanTasks(planId, planFile);
    const task = this.db.get('SELECT * FROM plan_tasks WHERE plan_id = ? AND task_key = ?', [planId, taskKey]);
    this.addEvent(
      projectId,
      'task.appended',
      `追加 ${taskKey}: ${cleanTitle}`,
      taskEventMeta(task, {
        planId,
        taskKey,
        taskTitle: cleanTitle,
        status: TASK_EVENT_STATUS.PENDING,
      }),
    );
    this.emitUpdate(projectId);

    // 循环在运行则立即拾取
    if (this.status(projectId)?.running) {
      this.runOnce(projectId).catch((error) => this.recordError(projectId, error));
    }
    return taskKey;
  }

  ensureWorkspaceDirs(workspace) {
    for (const dir of ['docs/issues', 'docs/plan', 'docs/progress', 'docs/progress/logs']) {
      fs.mkdirSync(path.join(workspace, dir), { recursive: true });
    }
  }

  scanDirectory(root, workspace, extensions) {
    if (!fs.existsSync(root)) return { root, aggregateHash: hashText(''), files: [] };
    const files = [];
    const visit = (dir) => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) {
          visit(full);
        } else if (entry.isFile() && extensions.includes(path.extname(entry.name).toLowerCase())) {
          const stat = fs.statSync(full);
          files.push({
            path: normalizeRelative(workspace, full),
            hash: hashFile(full),
            size: stat.size,
            modifiedAt: stat.mtime.toISOString(),
          });
        }
      }
    };
    visit(root);
    files.sort((a, b) => a.path.localeCompare(b.path));
    return {
      root,
      aggregateHash: hashText(files.map((file) => `${file.path}|${file.hash}|${file.size}`).join('\n')),
      files,
    };
  }

  saveScan(projectId, type, scan) {
    const scannedAt = nowIso();
    this.db.run('DELETE FROM scan_files WHERE project_id = ? AND scan_type = ?', [projectId, type]);
    for (const file of scan.files) {
      this.db.run(
        `INSERT OR REPLACE INTO scan_files
         (project_id, scan_type, file_path, hash, size, modified_at, scanned_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
        [projectId, type, file.path, file.hash, file.size, file.modifiedAt, scannedAt],
      );
    }
  }

  hasPlanForIssueHash(projectId, issueHash) {
    return Boolean(
      this.db.get('SELECT id FROM plans WHERE project_id = ? AND issue_hash = ? LIMIT 1', [projectId, issueHash]),
    );
  }

  nextRunnablePlan(projectId) {
    return this.db.get(
      `SELECT * FROM plans
       WHERE project_id = ? AND status NOT IN ('completed', 'interrupted')
       ORDER BY created_at ASC
       LIMIT 1`,
      [projectId],
    );
  }

  async generatePlan(projectId, workspace, issueScan) {
    this.setPhase(projectId, 'generate-plan');
    const planFile = path.join(
      workspace,
      'docs',
      'plan',
      `plan_${timestampForPath()}_${issueScan.aggregateHash.slice(0, 8)}.md`,
    );
    const issueBundle = issueScan.files
      .map((file) => {
        const full = path.join(workspace, file.path);
        return ['---', `path: ${file.path}`, `hash: ${file.hash}`, 'content:', readSnippet(full, 20000)].join('\n');
      })
      .join('\n');

    const prompt = [
      '你是需求整理与开发计划生成者。',
      '请根据 docs/issues 收集到的反馈和需求，生成一个开发计划和验收标准。',
      '',
      `输出文件：${planFile}`,
      '',
      '格式要求：',
      '- 每个任务必须严格使用固定格式：- [ ] P001: 任务标题 <!-- scope: lib/foo.dart,test/foo_test.dart -->',
      '- scope 必填，表示该任务预计修改的文件或模块；多个 scope 用英文逗号分隔；无法判断时写 <!-- scope: unknown -->，unknown 任务不会并发执行',
      '- 每个任务要有验收要点',
      '- 必须包含总体验收标准和进度区',
      '- 只写 plan 文件，不要改业务代码',
      '',
      '需求快照：',
      issueBundle,
    ].join('\n');

    const result = await this.runCodex(workspace, prompt, 'generate-plan', { projectId });
    if (result.exitCode !== 0 || !fs.existsSync(planFile)) {
      this.addEvent(projectId, 'plan.generate.failed', result.logFile);
      return;
    }

    const id = this.db.insert(
      `INSERT INTO plans
       (project_id, issue_hash, file_path, hash, status, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
      [
        projectId,
        issueScan.aggregateHash,
        normalizeRelative(workspace, planFile),
        hashFile(planFile),
        'pending',
        nowIso(),
        nowIso(),
      ],
    );
    this.syncPlanTasks(id, planFile);
    this.db.run('UPDATE project_states SET last_issue_hash = ?, updated_at = ? WHERE project_id = ?', [
      issueScan.aggregateHash,
      nowIso(),
      projectId,
    ]);
    this.addEvent(projectId, 'plan.generated', normalizeRelative(workspace, planFile));
  }

  /** 为单条需求/反馈生成计划（调 codex），并回写 linked_plan_id。失败返回 null，下轮重试。 */
  async generatePlanForIntake(projectId, workspace, intake) {
    const table = intake.__type === 'feedback' ? 'feedback' : 'requirements';
    const sourceName = intake.__type === 'feedback' ? '反馈' : '需求';
    this.setPhase(projectId, 'generate-plan');
    const safeId = String(intake.id).replace(/[^0-9a-zA-Z_-]/g, '');
    const planFile = path.join(
      workspace,
      'docs',
      'plan',
      `plan_${intake.__type}_${safeId}_${timestampForPath()}.md`,
    );
    const prompt = [
      '你是需求整理与开发计划生成者。',
      `请根据以下${sourceName}，生成一个开发计划和验收标准。`,
      '',
      `输出文件：${planFile}`,
      '',
      '格式要求：',
      '- 每个任务必须严格使用固定格式：- [ ] P001: 任务标题 <!-- scope: lib/foo.dart,test/foo_test.dart -->',
      '- scope 必填，表示该任务预计修改的文件或模块；多个 scope 用英文逗号分隔；无法判断时写 <!-- scope: unknown -->，unknown 任务不会并发执行',
      '- 每个任务要有验收要点',
      '- 必须包含总体验收标准和进度区',
      '- 只写 plan 文件，不要改业务代码',
      '',
      `${sourceName} #${intake.id} 内容：`,
      String(intake.body || '').trim() || '（正文为空）',
    ].join('\n');

    const result = await this.runCodex(workspace, prompt, `gen-${intake.__type}-${intake.id}`, { projectId });
    if (result.exitCode !== 0 || !fs.existsSync(planFile)) {
      this.addEvent(projectId, 'plan.generate.failed', `${sourceName} #${intake.id} 计划生成失败：${result.logFile}`);
      return null;
    }

    const issueHash = `${intake.__type}-${intake.id}-${hashText(String(intake.body || '')).slice(0, 16)}`;
    const id = this.db.insert(
      `INSERT INTO plans
       (project_id, issue_hash, file_path, hash, status, total_tasks, completed_tasks, validation_passed, created_at, updated_at)
       VALUES (?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
      [projectId, issueHash, normalizeRelative(workspace, planFile), hashFile(planFile), 'pending', nowIso(), nowIso()],
    );
    this.syncPlanTasks(id, planFile);
    // 回写关联
    this.db.run(`UPDATE ${table} SET linked_plan_id = ?, updated_at = ? WHERE id = ?`, [id, nowIso(), intake.id]);
    this.addEvent(projectId, 'plan.generated', `${sourceName} #${intake.id} 已生成计划：${normalizeRelative(workspace, planFile)}`);
    return id;
  }

  async processPlan(workspace, plan) {
    const planFile = path.join(workspace, plan.file_path);
    this.syncPlanTasks(plan.id, planFile);
    const pendingTasks = this.db.all(
      `SELECT * FROM plan_tasks WHERE plan_id = ? AND status = 'pending'
       ORDER BY sort_order ASC`,
      [plan.id],
    );
    if (pendingTasks.length) {
      const batch = this.parallelTaskBatch(pendingTasks);
      if (batch.length > 1) {
        await this.executeTaskBatch(workspace, plan, batch);
      } else {
        const task = batch[0] || pendingTasks[0];
        const result = await this.executeTask(workspace, plan, task);
        if (result.exitCode === 0) {
          this.completeTask(workspace, plan, task, result);
        }
      }
      return;
    }
    await this.validatePlan(workspace, plan);
  }

  parallelTaskBatch(tasks) {
    const selected = [];
    const usedScopes = new Set();
    for (const task of tasks) {
      if (selected.length >= MAX_PARALLEL_TASKS) break;
      const scopes = taskParallelScopes(task);
      if (!scopes.length) {
        if (!selected.length) return [task];
        continue;
      }
      if (scopes.some((scope) => usedScopes.has(scope))) continue;
      selected.push(task);
      for (const scope of scopes) usedScopes.add(scope);
    }
    return selected.length > 1 ? selected : [tasks[0]];
  }

  async executeTaskBatch(workspace, plan, tasks) {
    this.setPhase(plan.project_id, 'execute-task');
    this.addEvent(
      plan.project_id,
      'tasks.parallel.started',
      `并发执行 ${tasks.map((task) => task.task_key).join(', ')}`,
      { taskIds: tasks.map((task) => task.id) },
    );
    const results = await Promise.all(
      tasks.map(async (task) => {
        let result;
        try {
          result = await this.executeTask(workspace, plan, task, { parallel: true });
        } catch (error) {
          const finishedAt = nowIso();
          if (!taskLifecycleEventRecorded(error)) {
            this.recordTaskFailure(plan.project_id, plan, task, finishedAt, {
              error: error?.message || String(error),
            });
          }
          return { task, result: { exitCode: -1, finishedAt } };
        }
        if (result.exitCode === 0) {
          this.completeTask(workspace, plan, task, result);
        }
        return { task, result };
      }),
    );
    return results;
  }

  async executeTask(workspace, plan, task, options = {}) {
    this.setPhase(plan.project_id, 'execute-task');
    const startedAt = nowIso();
    const startedTask = this.startTaskRun(task.id, startedAt) || task;
    const existingSessionId = operationCodexSessionId(startedTask);
    const startedSessionContext = codexSessionContextFields({
      codexSessionId: existingSessionId,
      codexSessionMode: existingSessionId ? 'resume' : 'new',
    });
    this.addTaskLifecycleEvent(plan.project_id, TASK_EVENT_TYPES.STARTED, startedTask, {
      planId: plan.id,
      status: TASK_EVENT_STATUS.RUNNING,
      startedAt,
      ...startedSessionContext,
    });
    const planFile = path.join(workspace, plan.file_path);
    const completionRules = [
      '- plan 文件是只读上下文：不要修改 plan 文件，不要勾选 checkbox，不要更新 plan 进度区',
      '- AutoPlan 会在任务成功后统一写回数据库、checkbox 和进度区',
      '- 只修改当前任务 scope 直接相关的业务文件',
      '- 如需测试，只运行与当前任务直接相关的最小测试集，不要运行全量测试或与本任务无关的测试',
      '- 只有当前任务本身明确要求全量验证时，才允许运行全量测试',
      '- 不输出完整 diff、源码全文或长文件列表',
      '- 中文文件读写使用 UTF-8',
    ];
    if (options.parallel) {
      completionRules.unshift('- 当前为并发执行模式，不要读写其它任务的 scope');
    }
    const prompt = [
      '你是开发执行者。',
      `请只执行指定任务 ${task.task_key}，不要提前执行其它任务，也不要顺手处理其它 checkbox。`,
      '',
      `plan 文件（只读）：${planFile}`,
      `指定任务：${task.raw_line}`,
      `任务 scope：${task.scope || taskScopeText(task) || 'unknown'}`,
      '',
      '完成后必须：',
      ...completionRules,
    ].join('\n');
    let result;
    try {
      result = await this.runCodexWithPlanGuard(workspace, prompt, `execute-${task.task_key}`, {
        projectId: plan.project_id,
        planId: plan.id,
        taskId: task.id,
        parallel: Boolean(options.parallel),
        ...(existingSessionId ? { codexSessionId: existingSessionId } : {}),
      }, planFile);
    } catch (error) {
      const finishedAt = nowIso();
      const failedTask = this.recordTaskFailure(plan.project_id, plan, task, finishedAt, {
        error: error?.message || String(error),
        ...startedSessionContext,
      });
      if (failedTask) markTaskLifecycleEventRecorded(error);
      throw error;
    }
    const finishedAt = nowIso();
    result.finishedAt = finishedAt;
    const capturedSessionId = operationCodexSessionId(result);
    if (capturedSessionId) this.updateTaskCodexSession(task.id, capturedSessionId, finishedAt);
    const succeeded = result.exitCode === 0;
    if (!succeeded) {
      this.recordTaskFailure(plan.project_id, plan, task, finishedAt, {
        exitCode: result.exitCode,
        log: result.logFile,
        ...codexSessionContextFields(result),
      });
    }
    return result;
  }

  completeTask(workspace, plan, task, result) {
    const planFile = path.join(workspace, plan.file_path);
    const finishedAt = result?.finishedAt || nowIso();
    this.markTaskCompletedInPlan(workspace, planFile, task, result);
    const completedTask = this.finishTaskRun(task.id, TASK_EVENT_STATUS.COMPLETED, finishedAt) || task;
    this.addTaskLifecycleEvent(plan.project_id, TASK_EVENT_TYPES.SUCCEEDED, completedTask, {
      planId: plan.id,
      status: TASK_EVENT_STATUS.COMPLETED,
      finishedAt,
      exitCode: result?.exitCode,
      log: result?.logFile,
      ...codexSessionContextFields(result),
    });
    this.refreshPlanProgress(plan.id, planFile);
    this.emitUpdate(plan.project_id);
  }

  refreshPlanProgress(planId, planFile) {
    const totals = this.db.get(
      `SELECT COUNT(*) AS total,
              SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END) AS completed
       FROM plan_tasks
       WHERE plan_id = ?`,
      [planId],
    ) || { total: 0, completed: 0 };
    const total = Number(totals.total || 0);
    const completed = Number(totals.completed || 0);
    const status = total > 0 && completed === total ? 'ready_for_validation' : 'running';
    const hash = fs.existsSync(planFile) ? hashFile(planFile) : '';
    this.db.run(
      'UPDATE plans SET hash = ?, status = ?, total_tasks = ?, completed_tasks = ?, updated_at = ? WHERE id = ?',
      [hash, status, total, completed, nowIso(), planId],
    );
  }

  markTaskCompletedInPlan(workspace, planFile, task, result) {
    if (!fs.existsSync(planFile)) return;
    const relativeLog = result?.logFile ? normalizeRelative(workspace, result.logFile) : '';
    const key = escapeRegExp(String(task.task_key || ''));
    const checkboxRe = new RegExp(`(^\\s*[-*]\\s+\\[)([ xX])(\\]\\s+${key}(?:\\b|[:：\\s-]))`, 'm');
    let content = fs.readFileSync(planFile, 'utf8');
    if (checkboxRe.test(content)) {
      content = content.replace(checkboxRe, '$1x$3');
    }

    const note = `- ${task.task_key} AutoPlan 完成：${nowIso()}${relativeLog ? `；日志：${relativeLog}` : ''}`;
    const noteRe = new RegExp(`^\\s*-\\s+${key}\\s+AutoPlan 完成：.*$`, 'm');
    if (noteRe.test(content)) {
      content = content.replace(noteRe, note);
    } else if (/##\s*进度区/.test(content)) {
      content = `${content.trimEnd()}\n${note}\n`;
    } else {
      content = `${content.trimEnd()}\n\n## 进度区\n${note}\n`;
    }
    fs.writeFileSync(planFile, content, 'utf8');
  }

  async validatePlan(workspace, plan) {
    this.setPhase(plan.project_id, 'validate');
    const planFile = path.join(workspace, plan.file_path);
    const command = String(this.status(plan.project_id)?.validation_command || '').trim();
    if (!command) {
      // 验收命令为空：跳过校验，直接标记完成。
      this.db.run(
        'UPDATE plans SET status = ?, validation_passed = 1, updated_at = ? WHERE id = ?',
        ['completed', nowIso(), plan.id],
      );
      this.addEvent(plan.project_id, 'plan.completed', '任务全部完成（验收命令为空，已跳过校验）');
      return;
    }
    let validation = await this.runShell(workspace, command, `validate-${plan.id}`, { projectId: plan.project_id });
    for (let attempt = 1; validation.exitCode !== 0 && attempt <= 2; attempt += 1) {
      this.db.run('UPDATE plans SET status = ?, updated_at = ? WHERE id = ?', [
        'validation_failed',
        nowIso(),
        plan.id,
      ]);
      const prompt = [
        'plan 已完成，但宿主项目验收失败。请只修复验证错误。',
        `plan 文件（只读）：${planFile}`,
        '不要修改 plan 文件、checkbox 或进度区。',
        `失败命令：${command}`,
        '失败输出摘要：',
        tailText(validation.output, 12000),
      ].join('\n');
      await this.runCodexWithPlanGuard(workspace, prompt, `repair-${plan.id}-${attempt}`, {
        projectId: plan.project_id,
        planId: plan.id,
      }, planFile);
      validation = await this.runShell(workspace, command, `validate-${plan.id}-repair-${attempt}`, {
        projectId: plan.project_id,
      });
    }

    if (validation.exitCode === 0) {
      this.db.run(
        'UPDATE plans SET status = ?, validation_passed = 1, updated_at = ? WHERE id = ?',
        ['completed', nowIso(), plan.id],
      );
      this.addEvent(plan.project_id, 'plan.completed', plan.file_path);
    } else {
      this.addEvent(plan.project_id, 'validation.failed', validation.logFile);
    }
  }

  syncPlanTasks(planId, planFile) {
    if (!fs.existsSync(planFile)) return;
    normalizePlanTaskScopes(planFile);
    const text = fs.readFileSync(planFile, 'utf8');
    const regex = /^\s*[-*]\s+\[([ xX])\]\s+(.+)$/gm;
    const tasks = [];
    let match;
    let index = 0;
    while ((match = regex.exec(text))) {
      index += 1;
      const rawTitle = match[2].trim();
      const titleWithoutScope = stripTaskScopeComment(rawTitle);
      const idMatch = titleWithoutScope.match(/^([A-Za-z]+[-_]?\d+|P\d+)[:：\s-]+(.+)$/);
      tasks.push({
        key: idMatch?.[1] || `P${String(index).padStart(3, '0')}`,
        title: idMatch?.[2]?.trim() || titleWithoutScope || rawTitle,
        rawLine: ensureTaskScopeComment(match[0]),
        scope: taskScopeText({ raw_line: match[0], title: rawTitle }),
        status: match[1].toLowerCase() === 'x' ? 'completed' : 'pending',
        sortOrder: index,
      });
    }

    const existingTasks = this.db.all('SELECT * FROM plan_tasks WHERE plan_id = ? ORDER BY sort_order ASC, id ASC', [planId]);
    const existingByKey = new Map();
    for (const existing of existingTasks) {
      const matches = existingByKey.get(existing.task_key) || [];
      matches.push(existing);
      existingByKey.set(existing.task_key, matches);
    }

    const syncedStatuses = [];
    for (const task of tasks) {
      const existing = existingByKey.get(task.key)?.shift();
      const status = existing ? syncedTaskStatus(task.status, existing.status) : task.status;
      syncedStatuses.push(status);
      if (existing) {
        this.db.run(
          `UPDATE plan_tasks
           SET title = ?, raw_line = ?, scope = ?, status = ?, sort_order = ?, updated_at = ?
           WHERE id = ?`,
          [
            task.title,
            task.rawLine,
            task.scope,
            status,
            task.sortOrder,
            nowIso(),
            existing.id,
          ],
        );
      } else {
        this.db.run(
          `INSERT INTO plan_tasks (plan_id, task_key, title, raw_line, scope, status, sort_order, updated_at)
           VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
          [planId, task.key, task.title, task.rawLine, task.scope, status, task.sortOrder, nowIso()],
        );
      }
    }

    for (const matches of existingByKey.values()) {
      for (const stale of matches) {
        this.db.run('DELETE FROM plan_tasks WHERE id = ?', [stale.id]);
      }
    }

    const completed = syncedStatuses.filter((status) => status === 'completed').length;
    const status = tasks.length > 0 && completed === tasks.length ? 'ready_for_validation' : 'running';
    this.db.run(
      'UPDATE plans SET hash = ?, status = ?, total_tasks = ?, completed_tasks = ?, updated_at = ? WHERE id = ?',
      [hashFile(planFile), status, tasks.length, completed, nowIso(), planId],
    );
  }

  /** 把当前 activeOperation 转存为 lastOperation（保留日志），然后清空 active */
  archiveOperation(projectId, operationKey) {
    const runtime = this.runtime(projectId);
    if (!runtime) return;
    const op = operationKey ? runtime.activeOperations.get(operationKey) : runtime.activeOperation;
    if (op) {
      if (op.activity && typeof op.activity.flush === 'function') {
        op.activity.flush();
      }
      runtime.lastOperation = {
        label: op.label || '',
        projectId: op.projectId || null,
        planId: op.planId || null,
        taskId: op.taskId || null,
        startedAt: op.startedAt || null,
        finishedAt: nowIso(),
        exitCode: typeof op.exitCode === 'number' ? op.exitCode : null,
        logTail: (op.logBuffer || '').slice(-8000),
        activity: op.activity ? op.activity.getLines() : [],
        ...codexSessionContextFields(op),
      };
    }
    if (operationKey) {
      runtime.activeChildren.delete(operationKey);
      runtime.activeOperations.delete(operationKey);
    } else {
      runtime.activeChildren.clear();
      runtime.activeOperations.clear();
    }
    refreshRuntimeActive(runtime);
  }

  async runCodexWithPlanGuard(workspace, prompt, label, operation, planFile) {
    const before = planFile && fs.existsSync(planFile) ? fs.readFileSync(planFile, 'utf8') : null;
    const result = await this.runCodex(workspace, prompt, label, operation);
    if (before !== null) {
      const changed = !fs.existsSync(planFile) || fs.readFileSync(planFile, 'utf8') !== before;
      if (changed) {
        fs.writeFileSync(planFile, before, 'utf8');
        this.addEvent(operation.projectId, 'plan.guard.restored', `Codex 修改了 plan，已恢复：${normalizeRelative(workspace, planFile)}`, {
          planId: operation.planId || null,
          taskId: operation.taskId || null,
        });
      }
    }
    return result;
  }

  async runCodex(workspace, prompt, label, operation = {}) {
    const projectIdForEmit = operation.projectId;
    const runtime = this.runtime(projectIdForEmit);
    if (!runtime) throw new Error('projectId is required for codex operations');
    const logDir = path.join(workspace, 'docs', 'progress', 'logs');
    fs.mkdirSync(logDir, { recursive: true });
    const prefix = `${timestampForPath()}_${safePart(label)}`;
    const logFile = path.join(logDir, `${prefix}.log`);
    const lastFile = path.join(logDir, `${prefix}.last.txt`);
    const hasSessionOption = hasCodexSessionOption(operation);
    const requestedSessionId = operationCodexSessionId(operation);
    const activeOperation = {
      ...operation,
      label,
      logFile,
      lastFile,
      codexSessionId: requestedSessionId || null,
      codexSessionRequestedId: requestedSessionId || null,
      codexSessionMode: requestedSessionId ? 'resume' : 'new',
      codexSessionState: requestedSessionId ? 'resume' : 'new',
      logBuffer: '',
      activity: new CodexActivityPrinter(200),
      startedAt: nowIso(),
    };
    let operationKey = null;
    let capturedSessionId = '';
    let sessionScanBuffer = '';
    const stream = fs.createWriteStream(logFile, { encoding: 'utf8' });
    const appendInternalLog = (message) => {
      const text = `\n[AutoPlan] ${message}\n`;
      stream.write(text);
      activeOperation.logBuffer = `${activeOperation.logBuffer || ''}${text}`;
      if (activeOperation.logBuffer.length > 24000) {
        activeOperation.logBuffer = activeOperation.logBuffer.slice(-16000);
      }
    };
    const resultFor = (exitCode, mode) => {
      const sessionId = capturedSessionId || (mode === 'resume' ? requestedSessionId : '');
      const codexSessionFields = codexSessionContextFields({
        codexSessionId: sessionId,
        codexSessionRequestedId: requestedSessionId,
        codexSessionMode: mode,
        codexSessionFallback: mode === 'new' && Boolean(requestedSessionId),
      });
      return {
        exitCode,
        logFile,
        lastFile,
        sessionId: sessionId || null,
        codexSessionId: sessionId || null,
        codexSessionMode: mode,
        resumed: mode === 'resume',
        ...codexSessionFields,
      };
    };
    const runAttempt = async (args, mode) => {
      const child = spawn('codex', args, { shell: process.platform === 'win32', cwd: workspace });
      activeOperation.codexSessionMode = mode;
      activeOperation.codexSessionState = activeOperation.codexSessionFallback ? 'fallback-new' : mode;
      activeOperation.codexSessionId = mode === 'resume' ? requestedSessionId || null : capturedSessionId || null;
      if (operationKey) {
        runtime.activeChildren.set(operationKey, child);
        runtime.activeChild = child;
        runtime.activeOperation = activeOperation;
      } else {
        operationKey = registerRuntimeOperation(runtime, child, activeOperation);
      }
      child.stdin.setDefaultEncoding('utf8');
      child.stdin.end(prompt);

      let attemptOutput = '';
      // 累积原始日志 + 提取可读活动行
      const onChunk = (chunk) => {
        const text = chunk.toString('utf8');
        attemptOutput += text;
        if (!runtime.activeOperations.has(operationKey)) return;
        activeOperation.logBuffer = (activeOperation.logBuffer || '') + text;
        if (activeOperation.logBuffer.length > 24000) {
          activeOperation.logBuffer = activeOperation.logBuffer.slice(-16000);
        }
        sessionScanBuffer = `${sessionScanBuffer}${text}`.slice(-4000);
        const parsedSessionId = extractCodexSessionId(sessionScanBuffer);
        if (parsedSessionId) {
          capturedSessionId = parsedSessionId;
          activeOperation.codexSessionId = parsedSessionId;
        }
        // 喂给活动打印机，提取人类可读的进度行
        if (activeOperation.activity) activeOperation.activity.offer(text);
      };
      child.stdout.on('data', onChunk);
      child.stderr.on('data', onChunk);
      child.stdout.pipe(stream, { end: false });
      child.stderr.pipe(stream, { end: false });

      const exitCode = await waitForChild(child, 45 * 60 * 1000);
      if (runtime.activeOperations.has(operationKey)) activeOperation.exitCode = exitCode;
      return { exitCode, output: attemptOutput };
    };
    const tailTimer = setInterval(() => {
      if (projectIdForEmit) this.emitUpdate(projectIdForEmit);
    }, 600);
    try {
      if (requestedSessionId) {
        this.addEvent(projectIdForEmit, 'codex.session.resume.started', `尝试恢复 Codex 会话 ${shortCodexSessionId(requestedSessionId)}`, {
          ...codexSessionContextFields({
            codexSessionId: requestedSessionId,
            codexSessionRequestedId: requestedSessionId,
            codexSessionMode: 'resume',
          }),
          sessionId: requestedSessionId,
          label,
          planId: operation.planId || null,
          taskId: operation.taskId || null,
        });
        const resume = await runAttempt(codexResumeSessionArgs(requestedSessionId, lastFile), 'resume');
        const resumeFailed = resume.exitCode !== 0 && !extractCodexSessionId(resume.output) && isCodexResumeFailure(resume.output);
        if (!resumeFailed) return resultFor(resume.exitCode, 'resume');

        this.addEvent(projectIdForEmit, 'codex.session.resume.failed', `恢复 Codex 会话失败，已回退新建：${shortCodexSessionId(requestedSessionId)}`, {
          ...codexSessionContextFields({
            codexSessionRequestedId: requestedSessionId,
            codexSessionMode: 'new',
            codexSessionState: 'fallback-new',
            codexSessionFallback: true,
          }),
          sessionId: requestedSessionId,
          label,
          planId: operation.planId || null,
          taskId: operation.taskId || null,
          exitCode: resume.exitCode,
          log: logFile,
        });
        activeOperation.codexSessionFallback = true;
        activeOperation.codexSessionRequestedId = requestedSessionId;
        activeOperation.codexSessionId = null;
        activeOperation.codexSessionMode = 'new';
        activeOperation.codexSessionState = 'fallback-new';
        appendInternalLog(`Codex resume failed for session ${requestedSessionId}; falling back to a new session.`);
      } else if (hasSessionOption) {
        this.addEvent(projectIdForEmit, 'codex.session.resume.skipped', 'Codex session id 为空，已新建会话', {
          ...codexSessionContextFields({ codexSessionMode: 'new' }),
          label,
          planId: operation.planId || null,
          taskId: operation.taskId || null,
          reason: 'empty_session_id',
        });
        appendInternalLog('Codex session id was empty; starting a new session.');
      }

      const fresh = await runAttempt(codexNewSessionArgs(workspace, lastFile), 'new');
      return resultFor(fresh.exitCode, 'new');
    } finally {
      clearInterval(tailTimer);
      stream.end();
      if (operationKey && runtime.activeOperations.has(operationKey)) {
        this.archiveOperation(projectIdForEmit, operationKey);
      }
    }
  }

  async runShell(workspace, command, label, operation = {}) {
    const projectIdForEmit = operation.projectId;
    const runtime = this.runtime(projectIdForEmit);
    if (!runtime) throw new Error('projectId is required for shell operations');
    const logDir = path.join(workspace, 'docs', 'progress', 'logs');
    fs.mkdirSync(logDir, { recursive: true });
    const logFile = path.join(logDir, `${timestampForPath()}_${safePart(label)}.log`);
    const shellCommand = process.platform === 'win32' ? `chcp 65001>nul && ${command}` : command;
    const child = spawn(shellCommand, {
      shell: true,
      cwd: workspace,
      env: { ...process.env },
    });
    const activeOperation = {
      ...operation,
      label,
      logBuffer: '',
      activity: new CodexActivityPrinter(200),
      startedAt: nowIso(),
    };
    const operationKey = registerRuntimeOperation(runtime, child, activeOperation);
    let output = '';
    const onChunk = (chunk) => {
      const text = chunk.toString('utf8');
      output += text;
      if (!runtime.activeOperations.has(operationKey)) return;
      activeOperation.logBuffer = (activeOperation.logBuffer || '') + text;
      if (activeOperation.logBuffer.length > 24000) {
        activeOperation.logBuffer = activeOperation.logBuffer.slice(-16000);
      }
      if (activeOperation.activity) activeOperation.activity.offer(text);
    };
    child.stdout.on('data', onChunk);
    child.stderr.on('data', onChunk);
    const tailTimer = setInterval(() => {
      if (projectIdForEmit) this.emitUpdate(projectIdForEmit);
    }, 600);
    try {
      const exitCode = await waitForChild(child, 10 * 60 * 1000);
      fs.writeFileSync(logFile, output, 'utf8');
      if (runtime.activeOperations.has(operationKey)) activeOperation.exitCode = exitCode;
      return { exitCode, output, logFile };
    } finally {
      clearInterval(tailTimer);
      if (runtime.activeOperations.has(operationKey)) {
        this.archiveOperation(projectIdForEmit, operationKey);
      }
    }
  }

  setPhase(projectId, phase) {
    this.db.run('UPDATE project_states SET phase = ?, updated_at = ? WHERE project_id = ?', [
      phase,
      nowIso(),
      projectId,
    ]);
    this.emitUpdate(projectId);
  }

  recordError(projectId, error) {
    const message = error?.stack || error?.message || String(error);
    this.db.run(
      'UPDATE project_states SET phase = ?, last_error = ?, updated_at = ? WHERE project_id = ?',
      ['error', message, nowIso(), projectId],
    );
    this.addEvent(projectId, 'loop.error', message);
    this.emitUpdate(projectId);
  }

  addEvent(projectId, type, message, meta = null) {
    this.db.run('INSERT INTO events (project_id, type, message, meta, created_at) VALUES (?, ?, ?, ?, ?)', [
      projectId,
      type,
      message,
      meta ? JSON.stringify(meta) : null,
      nowIso(),
    ]);
    this.emitUpdate(projectId);
  }

  emitUpdate(projectId) {
    this.emit('update', this.snapshot(projectId));
  }

  snapshot(projectId = null) {
    const projects = this.projects();
    if (!projectId) return emptySnapshot(projects);

    const activeProject = this.project(projectId);
    if (!activeProject) return emptySnapshot(projects);

    const state = {
      ...(this.status(projectId) || {}),
      workspace_path: activeProject.workspace_path || '',
    };
    const runtime = this.existingRuntime(projectId);
    const taskCodexContexts = runtimeCodexContextByTask(runtime, projectId);

    return {
      activeProjectId: projectId,
      activeProject,
      projects,
      state,
      requirements: this.db.all(
        `SELECT requirements.*, plans.status AS plan_status,
                plans.completed_tasks AS plan_completed, plans.total_tasks AS plan_total
         FROM requirements
         LEFT JOIN plans ON plans.id = requirements.linked_plan_id
         WHERE requirements.project_id = ?
         ORDER BY requirements.updated_at DESC`,
        [projectId],
      ),
      feedback: this.db.all(
        `SELECT feedback.*, plans.status AS plan_status,
                plans.completed_tasks AS plan_completed, plans.total_tasks AS plan_total
         FROM feedback
         LEFT JOIN plans ON plans.id = feedback.linked_plan_id
         WHERE feedback.project_id = ?
         ORDER BY feedback.updated_at DESC`,
        [projectId],
      ),
      attachments: this.db.all(
        'SELECT * FROM attachments WHERE project_id = ? ORDER BY created_at DESC, id DESC',
        [projectId],
      ),
      planDrafts: this.db.all('SELECT * FROM plan_drafts WHERE project_id = ? ORDER BY updated_at DESC, id DESC', [
        projectId,
      ]),
      plans: this.db.all('SELECT * FROM plans WHERE project_id = ? ORDER BY created_at DESC', [projectId]),
      tasks: this.db
        .all(
          `SELECT plan_tasks.*, plans.file_path
           FROM plan_tasks JOIN plans ON plans.id = plan_tasks.plan_id
           WHERE plans.project_id = ?
           ORDER BY plans.created_at DESC, plan_tasks.sort_order ASC`,
          [projectId],
        )
        .map((task) => taskSnapshotRow(task, taskCodexContexts.get(Number(task.id)))),
      events: this.db
        .all('SELECT * FROM events WHERE project_id = ? ORDER BY id DESC LIMIT 80', [projectId])
        .map((event) => eventSnapshotRow(event)),
      scans: this.db.all(
        'SELECT * FROM scan_files WHERE project_id = ? ORDER BY scanned_at DESC, file_path ASC',
        [projectId],
      ),
      activeOperation:
        runtime?.activeOperation && Number(runtime.activeOperation.projectId) === Number(projectId)
          ? operationSnapshotRow(runtime.activeOperation)
          : null,
      activeOperations: runtime?.activeOperations
        ? Array.from(runtime.activeOperations.values())
            .filter((operation) => Number(operation.projectId) === Number(projectId))
            .map((operation) => operationSnapshotRow(operation))
        : [],
      lastOperation:
        runtime?.lastOperation && Number(runtime.lastOperation.projectId) === Number(projectId)
          ? runtime.lastOperation
          : null,
    };
  }
}

function taskEventMeta(task, overrides = {}) {
  const meta = {
    ...compactEventMeta({
      taskId: task?.id,
      taskKey: task?.task_key,
      taskTitle: task?.title,
      planId: task?.plan_id,
      status: task?.status,
      startedAt: task?.started_at,
      finishedAt: task?.finished_at,
      durationMs: task?.duration_ms,
      runDurationMs: task?.run_duration_ms,
    }),
    ...compactEventMeta(overrides),
  };
  Object.assign(
    meta,
    codexSessionContextFields({
      codexSessionId: meta.codexSessionId ?? meta.codex_session_id ?? meta.sessionId ?? task?.codex_session_id,
      codexSessionRequestedId: meta.codexSessionRequestedId,
      codexSessionMode: meta.codexSessionMode,
      codexSessionState: meta.codexSessionState,
      codexSessionFallback: meta.codexSessionFallback,
    }),
  );
  meta.taskId = normalizeOptionalNumber(meta.taskId);
  meta.planId = normalizeOptionalNumber(meta.planId);
  meta.taskKey = normalizeOptionalString(meta.taskKey);
  meta.taskTitle = normalizeOptionalString(meta.taskTitle);
  meta.status = normalizeOptionalString(meta.status);
  meta.startedAt = normalizeOptionalString(meta.startedAt);
  meta.finishedAt = normalizeOptionalString(meta.finishedAt);
  meta.durationMs = normalizeOptionalNumber(meta.durationMs);
  meta.runDurationMs = normalizeOptionalNumber(meta.runDurationMs);
  const compacted = compactEventMeta(meta);
  return Object.keys(compacted).length ? compacted : null;
}

function taskEventMessage(type, task, meta = null) {
  const taskLabel = task?.task_key ? `${task.task_key} 任务` : task?.id ? `任务 #${task.id}` : '任务';
  const separator = taskLabel === '任务' ? '' : ' ';
  const taskTitle = normalizeOptionalString(task?.title) || '未命名任务';
  const action =
    {
      [TASK_EVENT_TYPES.STARTED]: '开始了',
      [TASK_EVENT_TYPES.SUCCEEDED]: '结束了',
      [TASK_EVENT_TYPES.FAILED]: '执行失败',
      [TASK_EVENT_TYPES.STOP_REQUESTED]: '请求停止',
      [TASK_EVENT_TYPES.STOPPED]: '停止了',
      [TASK_EVENT_TYPES.INTERRUPTED]: '中断了',
    }[type] || '更新了';
  const codexContext = codexSessionReadableLabel(meta);
  return `${action}${separator}${taskLabel}：${taskTitle}${codexContext ? `（${codexContext}）` : ''}`;
}

function markTaskLifecycleEventRecorded(error) {
  if (!error || (typeof error !== 'object' && typeof error !== 'function')) return;
  try {
    Object.defineProperty(error, TASK_LIFECYCLE_EVENT_RECORDED, { value: true });
  } catch {
    error[TASK_LIFECYCLE_EVENT_RECORDED] = true;
  }
}

function taskLifecycleEventRecorded(error) {
  return Boolean(error && (typeof error === 'object' || typeof error === 'function') && error[TASK_LIFECYCLE_EVENT_RECORDED]);
}

function syncedTaskStatus(parsedStatus, existingStatus) {
  const next = normalizeOptionalString(parsedStatus) || TASK_EVENT_STATUS.PENDING;
  const current = normalizeOptionalString(existingStatus);
  if (!current) return next;
  if (next === TASK_EVENT_STATUS.COMPLETED || current === TASK_EVENT_STATUS.COMPLETED) return TASK_EVENT_STATUS.COMPLETED;
  if (current === TASK_EVENT_STATUS.RUNNING || current === 'blocked') return current;
  return next;
}

function compactEventMeta(meta) {
  const result = {};
  for (const [key, value] of Object.entries(meta || {})) {
    if (value !== undefined && value !== null && value !== '') result[key] = value;
  }
  return result;
}

function normalizeOptionalNumber(value) {
  if (value === undefined || value === null || value === '') return undefined;
  const number = Number(value);
  return Number.isFinite(number) ? number : undefined;
}

function normalizeOptionalString(value) {
  if (value === undefined || value === null) return undefined;
  const text = String(value).trim();
  return text || undefined;
}

function withTaskDurationMeta(task, runDurationMs) {
  if (!task) return null;
  return {
    ...task,
    duration_ms: normalizeDurationMs(task.duration_ms),
    ...(runDurationMs !== undefined ? { run_duration_ms: normalizeDurationMs(runDurationMs) } : {}),
  };
}

function operationSnapshotRow(operation) {
  if (!operation) return null;
  const activity = Array.isArray(operation.activity)
    ? operation.activity
    : operation.activity && typeof operation.activity.getLines === 'function'
      ? operation.activity.getLines()
      : [];
  return {
    label: operation.label || '',
    projectId: operation.projectId || null,
    planId: operation.planId || null,
    taskId: operation.taskId || null,
    startedAt: operation.startedAt || null,
    ...(operation.finishedAt ? { finishedAt: operation.finishedAt } : {}),
    ...(typeof operation.exitCode === 'number' ? { exitCode: operation.exitCode } : {}),
    logTail: (operation.logBuffer || operation.logTail || '').slice(-8000),
    activity,
    ...codexSessionContextFields(operation),
  };
}

function runtimeCodexContextByTask(runtime, projectId) {
  const contexts = new Map();
  if (runtime?.lastOperation && Number(runtime.lastOperation.projectId) === Number(projectId) && runtime.lastOperation.taskId) {
    contexts.set(Number(runtime.lastOperation.taskId), codexSessionContextFields(runtime.lastOperation));
  }
  for (const operation of runtime?.activeOperations?.values?.() || []) {
    if (Number(operation.projectId) !== Number(projectId) || !operation.taskId) continue;
    contexts.set(Number(operation.taskId), codexSessionContextFields(operation));
  }
  return contexts;
}

function codexSessionContextFields(source = {}) {
  const sessionId = operationCodexSessionId(source);
  const requestedSessionId = normalizeCodexSessionId(source.codexSessionRequestedId ?? source.requestedSessionId);
  const mode = normalizeCodexSessionMode(source.codexSessionMode);
  const fallback = Boolean(source.codexSessionFallback);
  const state = normalizeOptionalString(source.codexSessionState) || (fallback ? 'fallback-new' : mode);
  const label = codexSessionReadableLabel({
    codexSessionId: sessionId,
    codexSessionRequestedId: requestedSessionId,
    codexSessionMode: mode,
    codexSessionState: state,
    codexSessionFallback: fallback,
  });
  return compactEventMeta({
    codexSessionId: sessionId || undefined,
    codexSessionShortId: sessionId ? shortCodexSessionId(sessionId) : undefined,
    codexSessionMode: mode || undefined,
    codexSessionState: state || undefined,
    codexSessionLabel: label || undefined,
    codexSessionRequestedId: requestedSessionId || undefined,
    codexSessionRequestedShortId: requestedSessionId ? shortCodexSessionId(requestedSessionId) : undefined,
    codexSessionFallback: fallback || undefined,
  });
}

function normalizeCodexSessionMode(mode) {
  const normalized = normalizeOptionalString(mode);
  if (normalized === 'new' || normalized === 'resume') return normalized;
  return undefined;
}

function codexSessionReadableLabel(source = {}) {
  const explicit = normalizeOptionalString(source.codexSessionLabel);
  if (explicit) return explicit;
  const sessionId = operationCodexSessionId(source);
  const requestedSessionId = normalizeCodexSessionId(source.codexSessionRequestedId ?? source.requestedSessionId);
  const sessionShortId = sessionId ? shortCodexSessionId(sessionId) : '';
  const requestedShortId = requestedSessionId ? shortCodexSessionId(requestedSessionId) : '';
  const mode = normalizeCodexSessionMode(source.codexSessionMode);
  const state = normalizeOptionalString(source.codexSessionState);
  if (state === 'fallback-new' || source.codexSessionFallback) {
    if (sessionShortId && requestedShortId) return `回退新建会话 ${sessionShortId}（原 ${requestedShortId}）`;
    if (sessionShortId) return `回退新建会话 ${sessionShortId}`;
    return requestedShortId ? `回退新建会话（原 ${requestedShortId}）` : '回退新建会话';
  }
  if (mode === 'resume') return sessionShortId ? `恢复会话 ${sessionShortId}` : '恢复会话';
  if (mode === 'new') return sessionShortId ? `新建会话 ${sessionShortId}` : '新建会话';
  return sessionShortId ? `会话 ${sessionShortId}` : '';
}

function taskSnapshotRow(task, codexContext = null) {
  if (!task) return task;
  const startedAt = normalizeOptionalString(task.started_at) || null;
  const finishedAt = normalizeOptionalString(task.finished_at) || null;
  const isRunning = task.status === TASK_EVENT_STATUS.RUNNING;
  const runDurationMs = isRunning ? taskRunDurationMs(startedAt, nowIso()) : undefined;
  const sessionContext = codexSessionContextFields({
    codexSessionId: codexContext?.codexSessionId ?? task.codex_session_id,
    codexSessionRequestedId: codexContext?.codexSessionRequestedId,
    codexSessionMode: codexContext?.codexSessionMode,
    codexSessionState: codexContext?.codexSessionState,
    codexSessionFallback: codexContext?.codexSessionFallback,
  });
  return {
    ...task,
    started_at: startedAt,
    finished_at: finishedAt,
    duration_ms: normalizeDurationMs(task.duration_ms),
    ...(runDurationMs !== undefined ? { run_duration_ms: normalizeDurationMs(runDurationMs) } : {}),
    ...sessionContext,
  };
}

function normalizeDurationMs(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return 0;
  return Math.round(number);
}

function taskRunDurationMs(startedAt, finishedAt) {
  const started = Date.parse(startedAt || '');
  const finished = Date.parse(finishedAt || '');
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished <= started) return 0;
  return Math.round(finished - started);
}

function createProjectRuntime() {
  return {
    timer: null,
    running: false,
    busy: false,
    activeChild: null,
    activeOperation: null,
    activeChildren: new Map(),
    activeOperations: new Map(),
    lastOperation: null,
  };
}

function emptySnapshot(projects) {
  return {
    activeProjectId: null,
    activeProject: null,
    projects,
    state: null,
    requirements: [],
    feedback: [],
    attachments: [],
    planDrafts: [],
    plans: [],
    tasks: [],
    events: [],
    scans: [],
    activeOperation: null,
    activeOperations: [],
    lastOperation: null,
  };
}

function eventSnapshotRow(event) {
  if (!event) return event;
  return {
    ...event,
    meta: parseEventMeta(event.meta),
  };
}

function parseEventMeta(meta) {
  if (!meta) return null;
  if (typeof meta !== 'string') return meta;
  try {
    const parsed = JSON.parse(meta);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed;
    if (typeof parsed === 'string') return parsed;
  } catch {
    return meta;
  }
  return meta;
}

function hasCodexSessionOption(operation = {}) {
  return ['codexSessionId', 'sessionId', 'codex_session_id'].some((key) => Object.prototype.hasOwnProperty.call(operation, key));
}

function operationCodexSessionId(operation = {}) {
  return normalizeCodexSessionId(operation.codexSessionId ?? operation.sessionId ?? operation.codex_session_id);
}

function normalizeCodexSessionId(value) {
  if (value === null || value === undefined) return '';
  return String(value).trim();
}

function extractCodexSessionId(text) {
  if (!text) return '';
  const source = String(text);
  for (const re of CODEX_SESSION_ID_RES) {
    const match = source.match(re);
    if (match?.[1]) return match[1];
  }
  return '';
}

function isCodexResumeFailure(output) {
  return CODEX_RESUME_FAILURE_RE.test(String(output || ''));
}

function shortCodexSessionId(sessionId) {
  const normalized = normalizeCodexSessionId(sessionId);
  if (normalized.length <= 13) return normalized || 'unknown';
  return `${normalized.slice(0, 8)}…${normalized.slice(-4)}`;
}

function codexNewSessionArgs(workspace, lastFile) {
  return [
    'exec',
    '--cd',
    workspace,
    '--color',
    'never',
    '-o',
    lastFile,
    '--sandbox',
    'danger-full-access',
    '-',
  ];
}

function codexResumeSessionArgs(sessionId, lastFile) {
  return [
    'exec',
    'resume',
    '-o',
    lastFile,
    sessionId,
    '-',
  ];
}

function registerRuntimeOperation(runtime, child, operation) {
  if (!runtime.activeChildren) runtime.activeChildren = new Map();
  if (!runtime.activeOperations) runtime.activeOperations = new Map();
  const operationKey = `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  runtime.activeChildren.set(operationKey, child);
  runtime.activeOperations.set(operationKey, operation);
  runtime.activeChild = child;
  runtime.activeOperation = operation;
  return operationKey;
}

function refreshRuntimeActive(runtime) {
  const entries = Array.from(runtime.activeOperations?.entries?.() || []);
  const latest = entries.at(-1);
  if (!latest) {
    runtime.activeChild = null;
    runtime.activeOperation = null;
    return;
  }
  runtime.activeChild = runtime.activeChildren.get(latest[0]) || null;
  runtime.activeOperation = latest[1] || null;
}

function findRuntimeOperation(runtime, predicate) {
  return findRuntimeOperations(runtime, predicate)[0] || null;
}

function findRuntimeOperations(runtime, predicate) {
  if (!runtime?.activeOperations) return [];
  const matches = [];
  for (const [operationKey, operation] of runtime.activeOperations.entries()) {
    if (predicate(operation)) {
      matches.push({
        operationKey,
        operation,
        child: runtime.activeChildren?.get(operationKey) || null,
      });
    }
  }
  return matches;
}

function waitForChild(child, timeoutMs) {
  return new Promise((resolve) => {
    let settled = false;
    let killTimer = null;
    const finish = (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (killTimer) clearTimeout(killTimer);
      resolve(code ?? 0);
    };
    const timer = setTimeout(() => {
      killChildProcess(child);
      killTimer = setTimeout(() => finish(-1), 5000);
    }, timeoutMs);
    child.on('exit', (code) => {
      finish(code);
    });
    child.on('error', () => finish(-1));
  });
}

function killChildProcess(child) {
  if (!child || child.killed) return;
  if (process.platform === 'win32' && child.pid) {
    const killer = spawn('taskkill', ['/pid', String(child.pid), '/T', '/F'], { windowsHide: true });
    killer.on('error', () => child.kill());
    return;
  }
  child.kill('SIGTERM');
}

function hashFile(filePath) {
  return crypto.createHash('sha256').update(fs.readFileSync(filePath)).digest('hex');
}

function hashText(text) {
  return crypto.createHash('sha256').update(text).digest('hex');
}

function taskParallelScopes(task) {
  const raw = `${task.task_key || ''} ${task.title || ''} ${task.raw_line || ''}`;
  if (PARALLEL_BLOCKING_TASK_RE.test(raw)) return [];
  return taskDeclaredScopes(task, { keepUnknown: false });
}

function taskScopeText(task) {
  const explicit = taskDeclaredScopes(task, { keepUnknown: true, includePathFallback: false });
  if (explicit.length) return explicit.join(', ');
  const inferred = taskDeclaredScopes(task, { keepUnknown: false });
  return inferred.join(', ') || 'unknown';
}

function taskDeclaredScopes(task, options = {}) {
  const { keepUnknown = false, includePathFallback = true } = options;
  const raw = `${task.task_key || ''} ${task.title || ''} ${task.raw_line || ''}`;
  const scopes = new Set();

  addScopeParts(scopes, String(task.scope || '').split(TASK_SCOPE_SPLIT_RE), { keepUnknown });
  addScopeParts(scopes, explicitTaskScopeParts(raw), { keepUnknown });

  if (includePathFallback) {
    for (const match of raw.matchAll(TASK_PATH_RE)) {
      const scope = normalizeTaskScope(match[0], { keepUnknown });
      if (scope && !scope.startsWith('docs/plan/') && !scope.startsWith('docs/progress/')) {
        scopes.add(scope);
      }
    }
  }

  return Array.from(scopes);
}

function explicitTaskScopeParts(raw) {
  const explicit = String(raw || '').match(TASK_SCOPE_RE);
  return explicit?.[1] ? explicit[1].split(TASK_SCOPE_SPLIT_RE) : [];
}

function addScopeParts(scopes, parts, options = {}) {
  for (const part of parts) {
    const scope = normalizeTaskScope(part, options);
    if (scope) scopes.add(scope);
  }
}

function normalizeTaskScope(value, options = {}) {
  const scope = String(value || '')
    .trim()
    .replace(/^["'`[{(]+|["'`\]})]+$/g, '')
    .replace(/\s*--$/, '')
    .replaceAll('\\', '/')
    .toLowerCase();
  if (!scope || scope === '-') return '';
  if (scope === 'unknown') return options.keepUnknown ? 'unknown' : '';
  return scope;
}

function ensureTaskScopeComment(line, fallbackScope = 'unknown') {
  const text = String(line || '').trimEnd();
  return TASK_SCOPE_RE.test(text) ? text : `${text} <!-- scope: ${fallbackScope} -->`;
}

function stripTaskScopeComment(value) {
  return String(value || '').replace(TASK_SCOPE_COMMENT_RE, ' ').replace(/\s+/g, ' ').trim();
}

function normalizePlanTaskScopes(planFile) {
  const content = fs.readFileSync(planFile, 'utf8');
  let changed = false;
  const next = content.replace(/^(\s*[-*]\s+\[[ xX]\]\s+.+)$/gm, (line) => {
    if (TASK_SCOPE_RE.test(line)) return line;
    changed = true;
    return ensureTaskScopeComment(line);
  });
  if (changed) fs.writeFileSync(planFile, next, 'utf8');
}

function normalizeRelative(root, fullPath) {
  return path.relative(root, fullPath).replaceAll(path.sep, '/');
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function workspaceKey(workspace) {
  const value = String(workspace || '').trim();
  if (!value) return '';
  const resolved = path.resolve(value);
  return process.platform === 'win32' ? resolved.toLowerCase() : resolved;
}

function timestampForPath() {
  const now = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(
    now.getHours(),
  )}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
}

function readSnippet(filePath, maxChars) {
  const text = fs.readFileSync(filePath, 'utf8');
  return text.length > maxChars ? `${text.slice(0, maxChars)}\n...[truncated]` : text;
}

function tailText(text, maxChars) {
  return text.length > maxChars ? text.slice(text.length - maxChars) : text;
}

function safePart(value) {
  return String(value).replace(/[^A-Za-z0-9_.-]+/g, '_');
}

module.exports = {
  LoopService,
  LEGACY_TASK_EVENT_TYPES,
  TASK_EVENT_COMPATIBILITY,
  TASK_EVENT_SEMANTICS,
  TASK_EVENT_STATUS,
  TASK_EVENT_TYPES,
};
