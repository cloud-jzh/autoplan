import { useMemo, useState } from 'react';
import type { Plan, PlanTask } from '../../types';
import { Icon } from '../icons';
import { formatChinaDateTime } from '../../utils/time';
import {
  planTitle,
  scopeFileLabel,
  scopeFileStatus,
  acceptanceSelectionKey,
  type AcceptanceGroup,
  type AcceptedRecord,
} from '../../utils/planTasks';

type AcceptanceTarget = 'plan' | 'task';

interface AcceptanceViewProps {
  projectId: number;
  groups: AcceptanceGroup[];
  recentAccepted: AcceptedRecord[];
  onAccept: (targetType: AcceptanceTarget, id: number) => void;
  onUnaccept: (targetType: AcceptanceTarget, id: number) => void;
  onAcceptItems: (targets: { targetType: AcceptanceTarget; id: number }[]) => void;
  onUnacceptItems: (targets: { targetType: AcceptanceTarget; id: number }[]) => void;
}

function basename(filePath: string) {
  if (!filePath) return '';
  const parts = filePath.split(/[\\/]/);
  return parts[parts.length - 1] || filePath;
}

function planFileLabel(plan: Plan) {
  return basename(String(plan.file_path || '').trim()) || '未命名计划文件';
}

function planCompletedPct(plan: Plan) {
  const total = Number(plan.total_tasks) || 0;
  if (total <= 0) return 0;
  const completed = Math.min(Number(plan.completed_tasks) || 0, total);
  return Math.round((completed / total) * 100);
}

function planProgressText(plan: Plan) {
  const total = Number(plan.total_tasks) || 0;
  const completed = Number(plan.completed_tasks) || 0;
  if (total <= 0) return '';
  return `${completed} / ${total} · ${planCompletedPct(plan)}%`;
}

function primaryScopeFile(task: PlanTask) {
  const files = task.scope_files || [];
  return files.find((file) => !file.isUnknown && !file.isValidation) || files[0] || null;
}

function primaryScopeLabel(task: PlanTask) {
  const file = primaryScopeFile(task);
  if (file) return scopeFileLabel(file);
  return String(task.scope || '').trim();
}

function recordId(record: AcceptedRecord) {
  return record.targetType === 'plan' ? record.plan.id : record.task.id;
}

/** 将选择集合解析为验收目标列表（plan:123 → { targetType:'plan', id:123 }） */
function selectionTargets(selection: Set<string>): { targetType: AcceptanceTarget; id: number }[] {
  const results: { targetType: AcceptanceTarget; id: number }[] = [];
  for (const key of selection) {
    const idx = key.indexOf(':');
    if (idx <= 0) continue;
    const targetType = key.slice(0, idx);
    const id = Number(key.slice(idx + 1));
    if ((targetType === 'plan' || targetType === 'task') && Number.isFinite(id)) {
      results.push({ targetType, id });
    }
  }
  return results;
}

/**
 * 验收视图（P007）：把「已完成且未验收」的计划/任务按计划分组渲染为两级 checklist，
 * 支持多选 + 批量验收 / 批量取消验收（单次原子 IPC）；逐项 checkbox 即验收、取消即回退。
 */
export function AcceptanceView({
  projectId,
  groups,
  recentAccepted,
  onAccept,
  onUnaccept,
  onAcceptItems,
  onUnacceptItems,
}: AcceptanceViewProps) {
  const [acceptedCollapsed, setAcceptedCollapsed] = useState(false);
  const [selection, setSelection] = useState<Set<string>>(new Set());
  const pendingTaskCount = useMemo(
    () => groups.reduce((sum, group) => sum + group.tasks.length, 0),
    [groups],
  );
  const latestAcceptedAt = recentAccepted.length ? recentAccepted[0].acceptedAt : '';
  const hasPending = groups.length > 0;

  function handleToggleSelection(key: string) {
    setSelection((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function acceptAllPending() {
    const targets: { targetType: AcceptanceTarget; id: number }[] = [];
    groups.forEach((group) => {
      targets.push({ targetType: 'plan', id: group.plan.id });
      group.tasks.forEach((task) => targets.push({ targetType: 'task', id: task.id }));
    });
    if (targets.length > 0) onAcceptItems(targets);
  }

  function acceptPlanGroup(group: AcceptanceGroup) {
    const targets: { targetType: AcceptanceTarget; id: number }[] = [
      { targetType: 'plan', id: group.plan.id },
      ...group.tasks.map((task) => ({ targetType: 'task' as AcceptanceTarget, id: task.id })),
    ];
    onAcceptItems(targets);
  }

  const allPendingKeys = useMemo(() => {
    const keys: string[] = [];
    groups.forEach((g) => {
      keys.push(acceptanceSelectionKey('plan', g.plan.id));
      g.tasks.forEach((t) => keys.push(acceptanceSelectionKey('task', t.id)));
    });
    return keys;
  }, [groups]);

  const allAcceptedKeys = useMemo(
    () => recentAccepted.map((r) => acceptanceSelectionKey(r.targetType, recordId(r))),
    [recentAccepted],
  );

  const allSelectableKeys = useMemo(
    () => [...allPendingKeys, ...allAcceptedKeys],
    [allPendingKeys, allAcceptedKeys],
  );

  const isSelecting = selection.size > 0;

  return (
    <div className="acceptance-view" data-project-id={projectId}>
      <div className="accept-bar">
        {isSelecting ? (
          <BatchBar
            selection={selection}
            allKeys={allSelectableKeys}
            onAcceptSelected={() => {
              const targets = selectionTargets(selection);
              if (targets.length > 0) onAcceptItems(targets);
              setSelection(new Set());
            }}
            onUnacceptSelected={() => {
              const targets = selectionTargets(selection);
              if (targets.length > 0) onUnacceptItems(targets);
              setSelection(new Set());
            }}
            onSelectAll={() => setSelection(new Set(allSelectableKeys))}
            onClearSelection={() => setSelection(new Set())}
          />
        ) : (
          <>
            <span className="accept-ico" aria-hidden="true">
              <Icon name="acceptance" size={20} />
            </span>
            <div className="accept-text">
              <p>
                已完成但未验收的<b>计划与任务</b>，勾选复选框选择后批量验收；逐项勾选即完成验收；取消勾选可回退。
              </p>
              <div className="accept-meta">
                <span>
                  待验收 <b>{groups.length}</b> 个计划
                </span>
                <span className="meta-dot" aria-hidden="true" />
                <span>
                  <b>{pendingTaskCount}</b> 个任务
                </span>
                {latestAcceptedAt ? (
                  <>
                    <span className="meta-dot" aria-hidden="true" />
                    <span>
                      最近验收 <b className="mono">{formatChinaDateTime(latestAcceptedAt)}</b>
                    </span>
                  </>
                ) : null}
              </div>
            </div>
            <div className="accept-actions">
              <button
                type="button"
                className="btn btn-sm btn-primary"
                onClick={acceptAllPending}
                disabled={!hasPending}
                title={hasPending ? '批量验收当页全部待验收项' : '暂无待验收项'}
              >
                <Icon name="check-double" size={14} aria-hidden="true" />
                全部验收
              </button>
            </div>
          </>
        )}
      </div>

      {hasPending ? (
        <>
          <div className="accept-section-head">
            <h2>
              待验收
              <span className="count">
                {groups.length} 个计划 · {pendingTaskCount} 个任务
              </span>
            </h2>
            <span className="hint">勾选复选框即验收，取消即回退</span>
          </div>
          <div className="accept-groups">
            {groups.map((group) => (
              <PendingPlanCard
                key={group.plan.id}
                group={group}
                onAccept={onAccept}
                onAcceptGroup={acceptPlanGroup}
                selection={selection}
                onToggleSelection={handleToggleSelection}
              />
            ))}
          </div>
        </>
      ) : (
        <AcceptanceEmpty />
      )}

      {recentAccepted.length ? (
        <AcceptedSection
          records={recentAccepted}
          collapsed={acceptedCollapsed}
          onToggle={() => setAcceptedCollapsed((current) => !current)}
          onUnaccept={onUnaccept}
          selection={selection}
          onToggleSelection={handleToggleSelection}
        />
      ) : null}
    </div>
  );
}

/** 逐项立即验收 checkbox（单目标，不参与多选） */
function AcceptanceCheck({
  checked,
  title,
  onClick,
}: {
  checked: boolean;
  title?: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      className={`check${checked ? ' checked' : ''}`}
      title={title}
      onClick={onClick}
    >
      <Icon name="check" size={13} aria-hidden="true" />
    </button>
  );
}

/** 多选 checkbox（仅入选择集合，不立即验收） */
function SelectionCheckbox({
  checked,
  onClick,
}: {
  checked: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      className={`sel-check${checked ? ' checked' : ''}`}
      title={checked ? '取消选择' : '选择此项'}
      onClick={onClick}
    >
      <Icon name="check" size={12} aria-hidden="true" />
    </button>
  );
}

/** 批量操作条（选中 ≥1 项时在 .accept-bar 内渲染，替代默认头部内容） */
function BatchBar({
  selection,
  allKeys,
  onAcceptSelected,
  onUnacceptSelected,
  onSelectAll,
  onClearSelection,
}: {
  selection: Set<string>;
  allKeys: string[];
  onAcceptSelected: () => void;
  onUnacceptSelected: () => void;
  onSelectAll: () => void;
  onClearSelection: () => void;
}) {
  return (
    <div className="accept-batch-bar">
      <span className="batch-count">
        已选 <b>{selection.size}</b> 项
      </span>
      <button
        type="button"
        className="btn btn-sm btn-primary"
        onClick={onAcceptSelected}
        title="批量验收已选中的待验收项（单次原子操作）"
      >
        <Icon name="check-double" size={14} aria-hidden="true" />
        批量验收选中
      </button>
      <button
        type="button"
        className="btn btn-sm"
        onClick={onUnacceptSelected}
        title="批量取消验收已选中的已验收项（单次原子操作）"
      >
        <Icon name="undo" size={14} aria-hidden="true" />
        批量取消验收选中
      </button>
      <button
        type="button"
        className="btn btn-sm"
        onClick={onSelectAll}
        title="全选当页所有可选项"
      >
        全选当页
      </button>
      <button
        type="button"
        className="btn btn-sm"
        onClick={onClearSelection}
        title="取消所有选择"
      >
        取消选择
      </button>
    </div>
  );
}

function PendingPlanCard({
  group,
  onAccept,
  onAcceptGroup,
  selection,
  onToggleSelection,
}: {
  group: AcceptanceGroup;
  onAccept: (targetType: AcceptanceTarget, id: number) => void;
  onAcceptGroup: (group: AcceptanceGroup) => void;
  selection: Set<string>;
  onToggleSelection: (key: string) => void;
}) {
  const { plan, tasks } = group;
  const pct = planCompletedPct(plan);
  const progressText = planProgressText(plan);
  const planKey = acceptanceSelectionKey('plan', plan.id);
  return (
    <div className="accept-plan">
      <div className="accept-plan-head">
        <SelectionCheckbox
          checked={selection.has(planKey)}
          onClick={() => onToggleSelection(planKey)}
        />
        <AcceptanceCheck
          checked={false}
          title="验收此计划"
          onClick={() => onAccept('plan', plan.id)}
        />
        <div className="plan-main">
          <div className="plan-path mono" title={plan.file_path || undefined}>
            {planFileLabel(plan)}
          </div>
          <div className="plan-title">{planTitle(plan) || '未命名计划'}</div>
        </div>
        <div className="accept-plan-progress">
          {progressText ? (
            <>
              <div className={`progress${pct >= 100 ? ' success' : ''}`}>
                <span style={{ width: `${pct}%` }} />
              </div>
              <span className="progress-text mono">{progressText}</span>
            </>
          ) : null}
        </div>
        <span className="chip chip-completed">completed</span>
        <div className="accept-plan-actions">
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => onAcceptGroup(group)}
            disabled={tasks.length === 0}
            title={
              tasks.length
                ? `批量验收本计划 ${tasks.length} 个待验收任务`
                : '本计划暂无待验收任务'
            }
          >
            <Icon name="check-double" size={14} aria-hidden="true" />
            全部验收本计划
          </button>
        </div>
      </div>
      {tasks.length ? (
        <div className="accept-tasks">
          {tasks.map((task) => (
            <PendingTaskRow
              key={task.id}
              task={task}
              onAccept={onAccept}
              selection={selection}
              onToggleSelection={onToggleSelection}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function PendingTaskRow({
  task,
  onAccept,
  selection,
  onToggleSelection,
}: {
  task: PlanTask;
  onAccept: (targetType: AcceptanceTarget, id: number) => void;
  selection: Set<string>;
  onToggleSelection: (key: string) => void;
}) {
  const scopeFile = primaryScopeFile(task);
  const scopeText = primaryScopeLabel(task);
  const scopeTitle = scopeFile ? scopeFileStatus(scopeFile) : scopeText;
  const taskKey = acceptanceSelectionKey('task', task.id);
  return (
    <div className="accept-task" id={`workspace-acceptance-task-${task.id}`}>
      <SelectionCheckbox
        checked={selection.has(taskKey)}
        onClick={() => onToggleSelection(taskKey)}
      />
      <AcceptanceCheck checked={false} title="验收此任务" onClick={() => onAccept('task', task.id)} />
      <span className="task-key mono">{task.task_key || `#${task.id}`}</span>
      <span className="task-title" title={task.title}>
        {task.title}
      </span>
      {scopeText ? (
        <span className="task-scope" title={scopeTitle}>
          <Icon name="file" size={12} aria-hidden="true" />
          <span className="mono">{scopeText}</span>
        </span>
      ) : null}
    </div>
  );
}

function recordMetaLine(record: AcceptedRecord) {
  if (record.targetType === 'plan') return planFileLabel(record.plan);
  const key = record.task.task_key || `#${record.task.id}`;
  const scope = primaryScopeLabel(record.task);
  return scope ? `${key} · ${scope}` : key;
}

function AcceptedSection({
  records,
  collapsed,
  onToggle,
  onUnaccept,
  selection,
  onToggleSelection,
}: {
  records: AcceptedRecord[];
  collapsed: boolean;
  onToggle: () => void;
  onUnaccept: (targetType: AcceptanceTarget, id: number) => void;
  selection: Set<string>;
  onToggleSelection: (key: string) => void;
}) {
  return (
    <div className={`accepted-card${collapsed ? ' collapsed' : ''}`}>
      <button type="button" className="accepted-head" aria-expanded={!collapsed} onClick={onToggle}>
        <span className="acc-caret" aria-hidden="true">
          <Icon name="chevron-down" size={14} />
        </span>
        <span className="acc-title">
          已验收（最近）
          <span className="count">{records.length}</span>
        </span>
        <span className="acc-hint">点击折叠 / 展开</span>
      </button>
      {collapsed ? null : (
        <div className="accepted-body">
          {records.map((record) => (
            <AcceptedRow
              key={`${record.targetType}-${recordId(record)}`}
              record={record}
              onUnaccept={onUnaccept}
              selection={selection}
              onToggleSelection={onToggleSelection}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function AcceptedRow({
  record,
  onUnaccept,
  selection,
  onToggleSelection,
}: {
  record: AcceptedRecord;
  onUnaccept: (targetType: AcceptanceTarget, id: number) => void;
  selection: Set<string>;
  onToggleSelection: (key: string) => void;
}) {
  const isPlan = record.targetType === 'plan';
  const id = recordId(record);
  const title = record.targetType === 'plan' ? planTitle(record.plan) || '未命名计划' : record.task.title;
  const metaLine = recordMetaLine(record);
  const selKey = acceptanceSelectionKey(record.targetType, id);
  return (
    <div className="accepted-item" id={`workspace-acceptance-${record.targetType}-${id}`}>
      <SelectionCheckbox
        checked={selection.has(selKey)}
        onClick={() => onToggleSelection(selKey)}
      />
      <span className="check checked ai-check" aria-hidden="true">
        <Icon name="check" size={13} />
      </span>
      <div className="ai-main">
        <div className="ai-title" title={title}>
          {title}
        </div>
        <div className="ai-meta">
          <span className="ai-type">{isPlan ? '计划' : '任务'}</span>
          {metaLine ? <span className="mono">{metaLine}</span> : null}
          <span className="meta-dot" aria-hidden="true" />
          <span>
            验收于 <span className="mono">{formatChinaDateTime(record.acceptedAt)}</span>
          </span>
        </div>
      </div>
      <div className="ai-actions">
        <button
          type="button"
          className="btn btn-sm accept-undo-btn"
          onClick={() => onUnaccept(record.targetType, id)}
          title="取消验收，回到待验收"
        >
          <Icon name="undo" size={14} aria-hidden="true" />
          取消验收
        </button>
      </div>
    </div>
  );
}

function AcceptanceEmpty() {
  return (
    <div className="accept-empty">
      <div className="empty-ico" aria-hidden="true">
        <Icon name="acceptance" size={28} />
      </div>
      <div className="empty-title">暂无待验收项</div>
      <p>
        当前项目所有已完成的计划与任务均已通过验收。新任务在循环自动验收通过并进入{' '}
        <code>completed</code> 后会出现在这里，供你逐项确认。
      </p>
    </div>
  );
}
