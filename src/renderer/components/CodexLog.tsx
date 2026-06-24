import { useEffect, useRef } from 'react';
import type { ActivityLine, CodexSessionInfo } from '../types';
import { formatChinaTime } from '../utils/time';

const LOG_BOTTOM_THRESHOLD_PX = 24;

const ROLE_CONFIG: Record<string, { label: string; cls: string }> = {
  codex: { label: 'Codex', cls: 'act-codex' },
  exec: { label: '执行', cls: 'act-exec' },
  thinking: { label: '思考', cls: 'act-thinking' },
  error: { label: '错误', cls: 'act-error' },
  system: { label: '系统', cls: 'act-system' },
  user: { label: '用户', cls: 'act-user' },
  info: { label: '信息', cls: 'act-info' },
};

function isNearLogBottom(el: HTMLDivElement) {
  const maxScrollTop = Math.max(0, el.scrollHeight - el.clientHeight);
  return maxScrollTop - el.scrollTop <= LOG_BOTTOM_THRESHOLD_PX;
}

/**
 * 以活动时间线显示 codex 执行过程（参考 run_advanced_plan_with_codex.dart 的 Activity 输出）。
 * 优先用过滤后的 activity 行；若为空则回退到 logTail 原始尾部。
 */
export function CodexLog({
  log,
  activity,
  context,
}: {
  log: string;
  activity?: ActivityLine[];
  context?: CodexSessionInfo | null;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const isAtBottomRef = useRef(true);
  const lines = activity && activity.length > 0 ? activity : null;
  const contextLabel = codexContextLabel(context);

  const updateBottomState = () => {
    const el = scrollRef.current;
    isAtBottomRef.current = el ? isNearLogBottom(el) : true;
  };

  useEffect(() => {
    const el = scrollRef.current;
    if (el) {
      el.scrollTop = el.scrollHeight;
      updateBottomState();
    }
  }, [log, activity, contextLabel]);

  const contextLine = contextLabel ? (
    <div className="act-line act-info">
      <span className="act-time">上下文</span>
      <span className="act-tag">会话</span>
      <span className="act-text">{contextLabel}</span>
    </div>
  ) : null;

  if (!lines) {
    return (
      <div className="codex-log" ref={scrollRef} onScroll={updateBottomState}>
        {contextLine}
        <pre className="codex-raw">{log ? log.slice(-4000) : '等待 Codex 输出…'}</pre>
      </div>
    );
  }

  return (
    <div className="codex-log" ref={scrollRef} onScroll={updateBottomState}>
      {contextLine}
      {lines.map((line, index) => {
        const config = ROLE_CONFIG[line.role] || { label: line.role || '信息', cls: 'act-info' };
        return (
          <div className={`act-line ${config.cls}`} key={index}>
            <span className="act-time">{formatChinaTime(line.at)}</span>
            <span className="act-tag">{config.label}</span>
            <span className="act-text">{line.text}</span>
          </div>
        );
      })}
    </div>
  );
}

function codexContextLabel(context?: CodexSessionInfo | null) {
  if (!context) return '';
  const explicit = context.codexSessionLabel?.trim();
  if (explicit) return explicit;
  const requested = context.codexSessionRequestedShortId || shortCodexSessionId(context.codexSessionRequestedId);
  const current = context.codexSessionShortId || shortCodexSessionId(context.codexSessionId);
  if (context.codexSessionFallback || context.codexSessionState === 'fallback-new') {
    if (current && requested) return `回退新建会话 ${current}（原 ${requested}）`;
    if (current) return `回退新建会话 ${current}`;
    return requested ? `回退新建会话（原 ${requested}）` : '回退新建会话';
  }
  if (context.codexSessionMode === 'resume') return current ? `恢复会话 ${current}` : '恢复会话';
  if (context.codexSessionMode === 'new') return current ? `新建会话 ${current}` : '新建会话';
  return current ? `会话 ${current}` : '';
}

function shortCodexSessionId(sessionId?: string | null) {
  const text = sessionId?.trim();
  if (!text) return '';
  if (text.length <= 13) return text;
  return `${text.slice(0, 8)}…${text.slice(-4)}`;
}
