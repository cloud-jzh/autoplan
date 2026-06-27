import type { KeyboardEvent, MouseEvent, ReactNode } from 'react';
import { useId, useState } from 'react';
import type { Plan, PlanTask, WorkspacePlanReadState } from '../../types';
import { planCliSummaryLabel } from '../shared';
import { formatChinaDateTime } from '../../utils/time';
import {
  formatPlanDurationSummary,
  planTitle,
  tasksForPlan,
  type ParallelRunRequest,
} from '../../utils/planTasks';
import { PlanReaderModal } from './PlanReaderModal';

const PLAN_CARD_INTERACTIVE_SELECTOR = [
  'a[href]',
  'button',
  'input',
  'select',
  'textarea',
  'summary',
  '[role="button"]',
  '[role="link"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

function isPlanCardInteractiveEvent(event: MouseEvent<HTMLElement> | KeyboardEvent<HTMLElement>) {
  const target = event.target;
  if (!(target instanceof HTMLElement)) return false;
  const interactiveElement = target.closest<HTMLElement>(PLAN_CARD_INTERACTIVE_SELECTOR);
  return Boolean(
    interactiveElement &&
      interactiveElement !== event.currentTarget &&
      event.currentTarget.contains(interactiveElement),
  );
}

function parallelRunDisabledReason(plan: Plan, hasRunningTask: boolean) {
  if (hasRunningTask) return '该计划已有任务执行中';
  if (plan.validation_passed || plan.status === 'completed') return '计划已完成';
  if (!plan.concurrency_suggestion?.hasSafeParallelBatches) return '暂无安全可并发批次';
  return '';
}

function getPlanProgressPercent(plan: Plan) {
  const total = Math.max(0, Number(plan.total_tasks || 0));
  if (!total) return 0;
  return Math.max(0, Math.min(100, Math.round((Number(plan.completed_tasks || 0) / total) * 100)));
}

function planCardState(plan: Plan, hasRunningTask: boolean) {
  if (plan.is_draft || plan.status === 'draft') return 'draft';
  if (plan.validation_passed || plan.status === 'completed') return 'completed';
  if (hasRunningTask || plan.status === 'running') return 'running';
  return 'active';
}

function planCardChipClass(state: string) {
  if (state === 'completed') return 'chip-completed';
  if (state === 'running') return 'chip-running';
  if (state === 'draft') return 'chip-waiting';
  return 'chip-pending';
}

export function PlanList({
  emptyText = '暂无 plan。',
  latestReadingPlan,
  onCloseReader,
  onOpenReader,
  onRunParallel,
  onSelectPlan,
  onRefreshReader,
  plans,
  readerState,
  renderPlanControls,
  selectedPlanId,
  tasks = [],
  totalPlanCount = plans.length,
}: {
  emptyText?: string;
  latestReadingPlan?: Plan | null;
  onCloseReader: () => void;
  onOpenReader: (plan: Plan) => void;
  onRunParallel?: (request: ParallelRunRequest) => void;
  onSelectPlan?: (plan: Plan) => void;
  onRefreshReader: () => void;
  plans: Plan[];
  readerState: WorkspacePlanReadState;
  renderPlanControls?: (plan: Plan) => ReactNode;
  selectedPlanId?: number | null;
  tasks?: PlanTask[];
  totalPlanCount?: number;
}) {
  const readingPlan = readerState.plan;
  const planReading = readerState.loading;
  const readerDialogId = useId();
  const [confirmingPlan, setConfirmingPlan] = useState<Plan | null>(null);

  return (
    <>
      {plans.length ? (
        <div className="list compact plan-card-list">
          {plans.map((plan) => {
            const planTasks = tasksForPlan(tasks, plan, totalPlanCount);
            const durationSummary = formatPlanDurationSummary(planTasks);
            const title = planTitle(plan);
            const progressPercent = getPlanProgressPercent(plan);
            const cliSummary = planCliSummaryLabel(plan);
            const readingThisPlan = Boolean(
              readingPlan && readingPlan.id === plan.id && readingPlan.project_id === plan.project_id,
            );
            const disableRead = planReading && readingThisPlan;
            const suggestion = plan.concurrency_suggestion;
            const runningInPlan = planTasks.some((task) => task.status === 'running');
            const parallelDisabledReason = parallelRunDisabledReason(plan, runningInPlan);
            const canRunParallel = Boolean(onRunParallel && suggestion?.hasSafeParallelBatches && !parallelDisabledReason);
            const cardState = planCardState(plan, runningInPlan);
            const selected = selectedPlanId === plan.id;
            const progressTone = plan.validation_passed || plan.status === 'completed' ? ' success' : runningInPlan ? ' running' : '';
            return (
              <article
                className={`plan-card ${cardState}${selected ? ' selected' : ''}`}
                key={plan.id}
                aria-selected={onSelectPlan ? selected : undefined}
                data-plan-status={cardState}
                data-selected={selected ? 'true' : undefined}
                tabIndex={onSelectPlan ? 0 : undefined}
                onClick={(event) => {
                  if (isPlanCardInteractiveEvent(event)) return;
                  onSelectPlan?.(plan);
                }}
                onKeyDown={(event) => {
                  if (event.key !== 'Enter' && event.key !== ' ') return;
                  if (isPlanCardInteractiveEvent(event)) return;
                  event.preventDefault();
                  onSelectPlan?.(plan);
                }}
              >
                <div className="plan-top">
                  <span className={`chip ${planCardChipClass(cardState)}`}>{plan.status}</span>
                  <span className={`plan-title${cardState === 'draft' ? ' muted' : ''}`} title={title || plan.file_path}>
                    {title || '未命名计划'}
                  </span>
                </div>

                <div className="plan-path" title={plan.file_path}>{plan.file_path}</div>

                <div className="plan-progress">
                  <div className="progress-head">
                    <span className="ph-left">任务进度</span>
                    <span className="ph-right">{plan.completed_tasks} / {plan.total_tasks} · {progressPercent}%</span>
                  </div>
                  <div className={`progress${progressTone}`} aria-hidden="true">
                    <span style={{ width: `${progressPercent}%` }} />
                  </div>
                  <div className="plan-progress-subline">{durationSummary}</div>
                </div>

                <div className="concurrency-row">
                  <span className="conc-item parallel">可并发 <b>{suggestion?.parallelTaskCount || 0}</b></span>
                  <span className="conc-item batch">建议 <b>{suggestion?.batchCount || 0}</b> 批</span>
                  <span className="conc-item serial">串行 <b>{suggestion?.serialTaskCount || 0}</b></span>
                  {parallelDisabledReason ? <span className="conc-item blocked" title={parallelDisabledReason}>原因：{parallelDisabledReason}</span> : null}
                </div>

                <div className="plan-meta">
                  <span className="cli-tag">{cliSummary}</span>
                  <span className="meta-dot" />
                  <span className="mono">{plan.hash?.slice(0, 12) || 'no-hash'}</span>
                  <span className="meta-dot" />
                  <span>{formatChinaDateTime(plan.updated_at)}</span>
                </div>

                <div className="plan-actions">
                  <div className="plan-primary-actions">
                    <button
                      type="button"
                      className="btn btn-sm btn-primary plan-parallel-link"
                      disabled={!canRunParallel}
                      title={parallelDisabledReason || undefined}
                      onClick={() => setConfirmingPlan(plan)}
                    >
                      并发执行
                    </button>
                    <button
                      type="button"
                      className="btn btn-sm plan-read-link"
                      aria-haspopup="dialog"
                      aria-controls={readingThisPlan ? readerDialogId : undefined}
                      aria-expanded={readingThisPlan}
                      aria-label={`${disableRead ? '正在读取' : '阅读全文'}：${plan.file_path}`}
                      disabled={disableRead}
                      onClick={() => onOpenReader(plan)}
                    >
                      {disableRead ? '读取中…' : '阅读全文'}
                    </button>
                  </div>
                  {renderPlanControls ? <div className="plan-secondary-actions">{renderPlanControls(plan)}</div> : null}
                  <span className={`plan-validation ${plan.validation_passed ? 'passed' : 'pending'}`}>
                    验收 {plan.validation_passed ? 'passed' : 'pending'}
                  </span>
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        <div className="empty">{emptyText}</div>
      )}

      {confirmingPlan ? (
        <div className="modal-mask" onClick={() => setConfirmingPlan(null)}>
          <div className="modal parallel-confirm-modal" role="dialog" aria-modal="true" onClick={(event) => event.stopPropagation()}>
            <div className="modal-head">
              <h3>确认并发执行</h3>
              <button type="button" className="modal-close" onClick={() => setConfirmingPlan(null)} aria-label="关闭并发执行确认">
                ×
              </button>
            </div>
            <div className="parallel-confirm-body">
              <p>确认后将按以下安全批次启动；取消不会改变任务状态。</p>
              {confirmingPlan.concurrency_suggestion.batches.map((batch) => (
                <section className="parallel-batch-card" key={batch.batch}>
                  <div className="parallel-batch-head">
                    <strong>批次 {batch.batch}</strong>
                    <span>{batch.reason}</span>
                  </div>
                  <ul>
                    {batch.tasks.map((task) => (
                      <li key={task.id}>
                        <span>{task.task_key} · {task.title}</span>
                        <small className="mono">{task.scopes.join(', ')}</small>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
              {confirmingPlan.concurrency_suggestion.serialTasks.length ? (
                <details className="parallel-serial-reasons">
                  <summary>查看不建议并发原因</summary>
                  <ul>
                    {confirmingPlan.concurrency_suggestion.serialTasks.map((task) => (
                      <li key={task.id}>{task.task_key}：{task.reason}</li>
                    ))}
                  </ul>
                </details>
              ) : null}
            </div>
            <div className="modal-foot">
              <button type="button" className="btn" onClick={() => setConfirmingPlan(null)}>取消</button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  const plan = confirmingPlan;
                  setConfirmingPlan(null);
                  onRunParallel?.({
                    plan,
                    batches: plan.concurrency_suggestion.batches.map((batch) => ({
                      taskIds: batch.tasks.map((task) => task.id),
                    })),
                  });
                }}
              >
                确认并发执行
              </button>
            </div>
          </div>
        </div>
      ) : null}

      <PlanReaderModal
        dialogId={readerDialogId}
        latestPlan={latestReadingPlan}
        onClose={onCloseReader}
        onRefresh={onRefreshReader}
        readerState={readerState}
      />
    </>
  );
}
