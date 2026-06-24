import type { AppEvent, Plan, PlanDraft, PlanTask } from '../types';
import { RecordCard } from './IntakePanel';
import { formatChinaDateTime, formatDuration, getRunningDurationMs } from '../utils/time';

type TimedPlanTask = PlanTask & {
  plan_id?: number | null;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number | null;
};

type EventMetaRecord = Record<string, unknown>;

type TaskEventTone = 'start' | 'success' | 'failed' | 'stopped' | 'stopping' | 'updated';

type TaskEventDisplay = {
  title: string;
  body: string;
  meta: string;
  badge?: string;
  tone?: TaskEventTone;
};

const TASK_EVENT_PRESENTATION: Record<TaskEventTone, { action: string; badge: string }> = {
  start: { action: '开始了', badge: '开始' },
  success: { action: '结束了', badge: '成功' },
  failed: { action: '执行失败', badge: '失败' },
  stopped: { action: '停止了', badge: '停止' },
  stopping: { action: '请求停止', badge: '停止中' },
  updated: { action: '更新了', badge: '任务' },
};

function readDurationMs(task: TimedPlanTask) {
  if (task.duration_ms === null || typeof task.duration_ms === 'undefined') return null;
  const duration = Number(task.duration_ms);
  return Number.isFinite(duration) && duration >= 0 ? duration : null;
}

function getTaskDurationMs(task: TimedPlanTask) {
  if (task.status === 'running') return getRunningDurationMs(readDurationMs(task), task.started_at);
  return readDurationMs(task);
}

function readPlanId(task: TimedPlanTask) {
  if (task.plan_id === null || typeof task.plan_id === 'undefined') return null;
  const planId = Number(task.plan_id);
  return Number.isFinite(planId) ? planId : null;
}

function formatTaskDuration(task: TimedPlanTask) {
  const duration = getTaskDurationMs(task);
  if (task.status === 'running') return `已运行 ${formatDuration(duration, '0秒')}`;
  if (duration === null || (duration === 0 && !task.started_at && !task.finished_at)) return '未开始';
  return `耗时 ${formatDuration(duration, '0秒')}`;
}

function tasksForPlan(tasks: PlanTask[], plan: Plan, planCount: number) {
  const timedTasks = tasks as TimedPlanTask[];
  const hasPlanIds = timedTasks.some((task) => readPlanId(task) !== null);
  if (!hasPlanIds) return planCount === 1 ? timedTasks : [];
  return timedTasks.filter((task) => readPlanId(task) === plan.id);
}

function formatPlanDurationSummary(tasks: TimedPlanTask[]) {
  const totalMs = tasks.reduce((sum, task) => sum + (getTaskDurationMs(task) ?? 0), 0);
  const completedMs = tasks.reduce(
    (sum, task) => sum + (task.status === 'completed' ? getTaskDurationMs(task) ?? 0 : 0),
    0,
  );

  return `总耗时 ${formatDuration(totalMs, '0秒')} · 已完成 ${formatDuration(completedMs, '0秒')}`;
}

function toEventMetaRecord(value: unknown): EventMetaRecord | null {
  if (!value) return null;
  if (typeof value === 'string') {
    try {
      return toEventMetaRecord(JSON.parse(value));
    } catch {
      return null;
    }
  }
  if (typeof value === 'object' && !Array.isArray(value)) return value as EventMetaRecord;
  return null;
}

function readEventMeta(event: AppEvent) {
  return toEventMetaRecord((event as AppEvent & { meta?: unknown }).meta);
}

function readMetaText(meta: EventMetaRecord | null, keys: string[]) {
  if (!meta) return '';
  for (const key of keys) {
    const value = meta[key];
    if (typeof value === 'string') {
      const text = value.trim();
      if (text) return text;
    }
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return '';
}

function readMetaNumber(meta: EventMetaRecord | null, keys: string[]) {
  const text = readMetaText(meta, keys);
  if (!text) return null;
  const value = Number(text);
  return Number.isFinite(value) ? value : null;
}

function includesAny(text: string, keywords: string[]) {
  return keywords.some((keyword) => text.includes(keyword));
}

function classifyTaskEvent(event: AppEvent, meta: EventMetaRecord | null): TaskEventTone {
  const eventText = `${event.type || ''} ${readMetaText(meta, ['status', 'taskStatus', 'task_status'])}`.toLowerCase();
  if (includesAny(eventText, ['stop_requested', 'stop-requested', 'stop.requested', 'request_stop', 'stopping'])) {
    return 'stopping';
  }
  if (includesAny(eventText, ['fail', 'error', 'errored'])) return 'failed';
  if (includesAny(eventText, ['stop', 'interrupt', 'cancel'])) return 'stopped';
  if (includesAny(eventText, ['start', 'begin', 'running'])) return 'start';
  if (includesAny(eventText, ['complete', 'finish', 'success', 'succeed', 'executed', 'done'])) return 'success';
  return 'updated';
}

function isTaskEventType(type: string) {
  return /^task[.:_-]/i.test(type);
}

function sameEventText(left: string, right: string) {
  return left.replace(/\s+/g, ' ').trim() === right.replace(/\s+/g, ' ').trim();
}

function formatTaskEvent(event: AppEvent, meta: EventMetaRecord): TaskEventDisplay | null {
  const taskKey = readMetaText(meta, ['taskKey', 'task_key']);
  const taskId = readMetaText(meta, ['taskId', 'task_id']);
  const taskTitle = readMetaText(meta, ['taskTitle', 'task_title', 'title']) || '未命名任务';
  const hasTaskIdentity = Boolean(taskKey || taskId || readMetaText(meta, ['taskTitle', 'task_title', 'title']));
  if (!hasTaskIdentity) return null;

  const tone = classifyTaskEvent(event, meta);
  const presentation = TASK_EVENT_PRESENTATION[tone];
  const taskLabel = taskKey ? `${taskKey} 任务` : taskId ? `任务 #${taskId}` : '任务';
  const separator = taskLabel === '任务' ? '' : ' ';
  const title = `${presentation.action}${separator}${taskLabel}：${taskTitle}`;
  const originalMessage = event.message?.trim() || '';
  const planId = readMetaText(meta, ['planId', 'plan_id']);
  const status = readMetaText(meta, ['status', 'taskStatus', 'task_status']);
  const durationMs = readMetaNumber(meta, ['durationMs', 'duration_ms']);
  const metaParts = [
    formatChinaDateTime(event.created_at),
    planId ? `Plan #${planId}` : '',
    status ? `状态 ${status}` : '',
    durationMs !== null ? `耗时 ${formatDuration(durationMs, '0秒')}` : '',
  ].filter(Boolean);

  return {
    title,
    body: originalMessage && !sameEventText(originalMessage, title) ? originalMessage : '',
    meta: metaParts.join(' · '),
    badge: presentation.badge,
    tone,
  };
}

function formatEvent(event: AppEvent): TaskEventDisplay {
  const meta = readEventMeta(event);
  const taskDisplay = meta && (isTaskEventType(event.type) || readMetaText(meta, ['taskKey', 'task_key', 'taskId', 'task_id']))
    ? formatTaskEvent(event, meta)
    : null;
  if (taskDisplay) return taskDisplay;

  return {
    title: event.type || '事件',
    body: event.message || '',
    meta: formatChinaDateTime(event.created_at),
  };
}

export function PlanDraftList({
  drafts,
  draftTextById,
  onAccept,
  onChange,
  onSave,
}: {
  drafts: PlanDraft[];
  draftTextById: Record<number, string>;
  onAccept: (draft: PlanDraft) => void;
  onChange: (id: number, markdown: string) => void;
  onSave: (draft: PlanDraft) => void;
}) {
  if (!drafts.length) {
    return <div className="empty">暂无计划草稿。发送需求或反馈后会自动生成。</div>;
  }

  return (
    <div className="list compact">
      {drafts.map((draft) => {
        const accepted = draft.status === 'accepted';
        return (
          <article className="item plan-draft" key={draft.id}>
            <div className="item-title">
              <span>
                草稿 #{draft.id} · {draft.source_type === 'feedback' ? '反馈' : '需求'} #{draft.source_id}
              </span>
              <span className={`chip ${accepted ? 'chip-accepted' : 'chip-waiting'}`}>{draft.status}</span>
            </div>
            <textarea
              readOnly={accepted}
              value={draftTextById[draft.id] ?? draft.markdown ?? ''}
              onChange={(event) => onChange(draft.id, event.target.value)}
            />
            <div className="button-row">
              {accepted ? (
                <span className="draft-note">已加入任务系统：Plan #{draft.linked_plan_id || ''}</span>
              ) : (
                <>
                  <button type="button" onClick={() => onSave(draft)}>
                    保存调整
                  </button>
                  <button className="btn-primary" type="button" onClick={() => onAccept(draft)}>
                    确认加入任务系统
                  </button>
                </>
              )}
            </div>
          </article>
        );
      })}
    </div>
  );
}

export function PlanList({ plans, tasks = [] }: { plans: Plan[]; tasks?: PlanTask[] }) {
  if (!plans.length) return <div className="empty">暂无 plan。</div>;

  return (
    <div className="list compact">
      {plans.map((plan) => {
        const durationSummary = formatPlanDurationSummary(tasksForPlan(tasks, plan, plans.length));
        return (
          <RecordCard
            key={plan.id}
            title={plan.file_path}
            status={plan.status}
            body={`${plan.completed_tasks}/${plan.total_tasks} tasks · ${durationSummary} · validation ${
              plan.validation_passed ? 'passed' : 'pending'
            }`}
            meta={`${plan.hash?.slice(0, 12) || ''} · ${formatChinaDateTime(plan.updated_at)}`}
          />
        );
      })}
    </div>
  );
}

export function TaskList({
  tasks,
  onRun,
  onStop,
}: {
  tasks: PlanTask[];
  onRun?: (task: PlanTask) => void;
  onStop?: (task: PlanTask) => void;
}) {
  if (!tasks.length) return <div className="empty">暂无任务。</div>;

  return (
    <div className="list compact">
      {tasks.map((task) => {
        const running = task.status === 'running';
        const completed = task.status === 'completed';
        const durationLabel = formatTaskDuration(task as TimedPlanTask);
        return (
          <RecordCard
            actions={
              <div className="item-actions">
                <button type="button" className="btn-link" disabled={completed || running} onClick={() => onRun?.(task)}>
                  执行
                </button>
                <button type="button" className="btn-link danger-link" disabled={!running} onClick={() => onStop?.(task)}>
                  停止
                </button>
              </div>
            }
            key={task.id}
            title={task.title}
            status={task.status}
            body={task.file_path}
            meta={`${task.task_key} · ${durationLabel} · ${formatChinaDateTime(task.updated_at)}`}
          />
        );
      })}
    </div>
  );
}

export function EventList({ events }: { events: AppEvent[] }) {
  if (!events.length) return <div className="empty">暂无事件。</div>;

  return (
    <div className="list compact event-list">
      {events.map((event) => {
        const display = formatEvent(event);
        return (
          <article className={`item event-item ${display.tone ? `event-item-${display.tone}` : ''}`} key={event.id}>
            <div className="item-title event-title">
              <span>{display.title}</span>
              {display.badge ? <span className={`event-badge event-badge-${display.tone}`}>{display.badge}</span> : null}
            </div>
            {display.body ? <div className="item-body plain-text">{display.body}</div> : null}
            <div className="meta event-meta">{display.meta}</div>
          </article>
        );
      })}
    </div>
  );
}
