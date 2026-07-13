import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAutoplanClient, useTerminalConnectionOperations } from '../lib/api/provider';
import type {
  TerminalCreateInput,
  TerminalEvent,
  TerminalReplayResult,
  TerminalSession,
  TerminalSessionResult,
} from '../types';

export type TerminalBusyAction = 'create' | 'refresh' | 'write' | 'resize' | 'kill' | 'close' | 'rename' | 'clear' | 'replay';
export type TerminalDataHandler = (data: string, session: TerminalSession) => void;
type TerminalDataHandlers = Map<TerminalDataHandler, number>;

export interface UseTerminalSessionsOptions {
  projectId: number;
  initialSessions?: TerminalSession[];
  autoRefresh?: boolean;
}

export interface UseTerminalSessionsResult {
  sessions: TerminalSession[];
  activeSession: TerminalSession | null;
  activeSessionId: string | null;
  activeCount: number;
  loading: boolean;
  error: string;
  busyAction: TerminalBusyAction | null;
  setActiveSessionId: (sessionId: string | null) => void;
  clearError: () => void;
  refresh: () => Promise<void>;
  createSession: (input?: Partial<TerminalCreateInput>) => Promise<TerminalSession | null>;
  write: (sessionId: string, data: string) => Promise<boolean>;
  resize: (sessionId: string, cols: number, rows: number) => Promise<boolean>;
  kill: (sessionId: string) => Promise<TerminalSession | null>;
  close: (sessionId: string) => Promise<boolean>;
  rename: (sessionId: string, title: string) => Promise<TerminalSession | null>;
  clear: (sessionId: string) => Promise<boolean>;
  replay: (sessionId: string) => Promise<TerminalReplayResult | null>;
  removeLocal: (sessionId: string) => void;
  subscribeData: (sessionId: string, handler: TerminalDataHandler) => () => void;
}

const EMPTY_TERMINAL_SESSIONS: TerminalSession[] = [];

export function useTerminalSessions({
  projectId,
  initialSessions = EMPTY_TERMINAL_SESSIONS,
  autoRefresh = true,
}: UseTerminalSessionsOptions): UseTerminalSessionsResult {
  const client = useAutoplanClient();
  const terminalConnectionOperations = useTerminalConnectionOperations();
  const [sessions, setSessions] = useState<TerminalSession[]>(() => normalizeSessions(initialSessions, projectId));
  const [activeSessionId, setActiveSessionIdState] = useState<string | null>(() => sessions[0]?.id ?? null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [busyAction, setBusyAction] = useState<TerminalBusyAction | null>(null);
  const projectIdRef = useRef(projectId);
  const previousProjectIdRef = useRef(projectId);
  const mountedRef = useRef(false);
  const dataHandlersRef = useRef(new Map<string, TerminalDataHandlers>());
  const terminalConnectionsRef = useRef(new Map<string, () => void>());
  const terminalSequencesRef = useRef(new Map<string, number>());
  const closedSessionKeysRef = useRef(new Set<string>());
  projectIdRef.current = projectId;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      terminalConnectionsRef.current.forEach((unsubscribe) => unsubscribe());
      terminalConnectionsRef.current.clear();
      terminalSequencesRef.current.clear();
      dataHandlersRef.current.clear();
    };
  }, []);

  const setActiveSessionId = useCallback((sessionId: string | null) => {
    setActiveSessionIdState(sessionId || null);
  }, []);

  const clearError = useCallback(() => setError(''), []);

  const selectExistingSession = useCallback((nextSessions: TerminalSession[]) => {
    setActiveSessionIdState((current) => {
      if (current && nextSessions.some((session) => session.id === current)) return current;
      const running = nextSessions.find(isTerminalActive);
      return running?.id ?? nextSessions[0]?.id ?? null;
    });
  }, []);

  const removeSession = useCallback((sessionId: string, sessionProjectId: number | string = projectIdRef.current) => {
    rememberClosedSession(sessionId, sessionProjectId, closedSessionKeysRef.current);
    terminalConnectionsRef.current.get(sessionId)?.();
    terminalConnectionsRef.current.delete(sessionId);
    terminalSequencesRef.current.delete(sessionId);
    removeSessionFromState(sessionId, setSessions, setActiveSessionIdState, dataHandlersRef);
  }, []);

  const upsertSession = useCallback((session: TerminalSession | null | undefined) => {
    if (!session || !belongsToProject(session, projectIdRef.current)) return;
    if (shouldRemoveSession(session, projectIdRef.current, closedSessionKeysRef.current)) {
      removeSession(session.id, session.projectId);
      return;
    }
    setSessions((current) => {
      const next = sortSessions([
        ...normalizeSessions(current, projectIdRef.current, closedSessionKeysRef.current)
          .filter((item) => item.id !== session.id),
        session,
      ]);
      selectExistingSession(next);
      return next;
    });
  }, [removeSession, selectExistingSession]);

  const applyTerminalEvent = useCallback((event: TerminalEvent) => {
    if (!eventBelongsToProject(event, projectIdRef.current)) return;
    const sessionId = terminalEventSessionId(event);
    if (terminalEventClosed(event, projectIdRef.current, closedSessionKeysRef.current)) {
      removeSession(sessionId, terminalEventProjectId(event) ?? projectIdRef.current);
      return;
    }
    upsertSession(event.session);
  }, [removeSession, upsertSession]);

  const deliverTerminalData = useCallback((event: TerminalEvent & { data: string }) => {
    if (!eventBelongsToProject(event, projectIdRef.current)) return;
    if (terminalEventClosed(event, projectIdRef.current, closedSessionKeysRef.current)) {
      removeSession(terminalEventSessionId(event), terminalEventProjectId(event) ?? projectIdRef.current);
      return;
    }
    if (!event.session) return;
    const sequence = event.seq;
    if (typeof sequence === 'number') {
      const previous = terminalSequencesRef.current.get(event.session.id) ?? 0;
      if (sequence <= previous) return;
      if (previous !== 0 && sequence !== previous + 1) {
        setError('终端输出需要重新连接');
        return;
      }
      terminalSequencesRef.current.set(event.session.id, sequence);
    }
    upsertSession(event.session);
    const data = String(event.data ?? '');
    if (!data) return;
    const handlers = dataHandlersRef.current.get(event.session.id);
    if (!handlers || handlers.size === 0) return;
    handlers.forEach((_owners, handler) => handler(data, event.session));
  }, [removeSession, upsertSession]);

  const openTerminalConnection = useCallback((sessionId: string) => {
    if (!mountedRef.current || !sessionId || terminalConnectionsRef.current.has(sessionId) ||
        !dataHandlersRef.current.get(sessionId)?.size || !terminalSequencesRef.current.has(sessionId)) return;
    const unsubscribe = terminalConnectionOperations?.connectTerminal?.(
      projectIdRef.current,
      sessionId,
      terminalSequencesRef.current.get(sessionId) ?? 0,
      {
        onData: deliverTerminalData,
        onExit: applyTerminalEvent,
        onStatus: applyTerminalEvent,
        onClosed: applyTerminalEvent,
        onUnavailable: (reason) => {
          if (mountedRef.current) setError(terminalConnectionError(reason));
        },
      },
    );
    if (typeof unsubscribe === 'function') terminalConnectionsRef.current.set(sessionId, unsubscribe);
  }, [applyTerminalEvent, deliverTerminalData, terminalConnectionOperations]);

  const refresh = useCallback(async () => {
    const requestProjectId = projectId;
    if (!isValidProjectId(requestProjectId)) {
      setSessions([]);
      setActiveSessionIdState(null);
      return;
    }

    setLoading(true);
    setBusyAction('refresh');
    try {
      const result = await client.listTerminals({ projectId: requestProjectId });
      if (!mountedRef.current || projectIdRef.current !== requestProjectId) return;
      if (!result.ok) {
        setError(result.message || '读取终端会话失败');
        setSessions([]);
        setActiveSessionIdState(null);
        return;
      }
      rememberClosedSessions(result.sessions, requestProjectId, closedSessionKeysRef.current);
      const nextSessions = normalizeSessions(result.sessions, requestProjectId, closedSessionKeysRef.current);
      setSessions(nextSessions);
      selectExistingSession(nextSessions);
    } catch (err) {
      if (mountedRef.current && projectIdRef.current === requestProjectId) setError(errorMessage(err, '读取终端会话失败'));
    } finally {
      if (mountedRef.current && projectIdRef.current === requestProjectId) {
        setLoading(false);
        setBusyAction((current) => (current === 'refresh' ? null : current));
      }
    }
  }, [client, projectId, selectExistingSession]);

  useEffect(() => {
    if (previousProjectIdRef.current !== projectId) {
      terminalConnectionsRef.current.forEach((unsubscribe) => unsubscribe());
      terminalConnectionsRef.current.clear();
      terminalSequencesRef.current.clear();
      dataHandlersRef.current.clear();
      closedSessionKeysRef.current.clear();
      previousProjectIdRef.current = projectId;
    }
    rememberClosedSessions(initialSessions, projectId, closedSessionKeysRef.current);
    const normalized = normalizeSessions(initialSessions, projectId, closedSessionKeysRef.current);
    setSessions((current) => {
      const next = mergeSessions(current, normalized, projectId, closedSessionKeysRef.current);
      if (sameSessionList(current, next)) {
        selectExistingSession(current);
        return current;
      }
      selectExistingSession(next);
      return next;
    });
  }, [initialSessions, projectId, selectExistingSession]);

  useEffect(() => {
    rememberClosedSessions(initialSessions, projectId, closedSessionKeysRef.current);
    const normalized = normalizeSessions(initialSessions, projectId, closedSessionKeysRef.current);
    setSessions(normalized);
    selectExistingSession(normalized);
    setError('');
    if (autoRefresh) void refresh();
  }, [autoRefresh, projectId, refresh, selectExistingSession]);

  useEffect(() => {
    let active = true;
    const unsubscribeData = client.onTerminalData((event) => {
      if (!active) return;
      deliverTerminalData(event);
    });
    const unsubscribeExit = client.onTerminalExit((event) => {
      if (active) applyTerminalEvent(event);
    });
    const unsubscribeStatus = client.onTerminalStatus((event) => {
      if (active) applyTerminalEvent(event);
    });
    const unsubscribeClosed = client.onTerminalClosed((event) => {
      if (active) applyTerminalEvent(event);
    });
    return () => {
      active = false;
      unsubscribeData();
      unsubscribeExit();
      unsubscribeStatus();
      unsubscribeClosed();
    };
  }, [applyTerminalEvent, client, deliverTerminalData]);

  const createSession = useCallback(async (input: Partial<TerminalCreateInput> = {}) => {
    if (!isValidProjectId(projectId)) {
      setError('项目不存在');
      return null;
    }
    setBusyAction('create');
    try {
      const payload: TerminalCreateInput = {
        projectId,
        ...input,
      };
      const result = await client.createTerminal(payload);
      if (!mountedRef.current || projectIdRef.current !== projectId) return null;
      const session = sessionFromResult(result, '创建终端失败', setError);
      if (!session) return null;
      upsertSession(session);
      setActiveSessionIdState(session.id);
      return session;
    } catch (err) {
      if (mountedRef.current && projectIdRef.current === projectId) setError(errorMessage(err, '创建终端失败'));
      return null;
    } finally {
      if (mountedRef.current && projectIdRef.current === projectId) setBusyAction((current) => (current === 'create' ? null : current));
    }
  }, [client, projectId, upsertSession]);

  const write = useCallback(async (sessionId: string, data: string) => {
    if (!sessionId || !data) return true;
    try {
      const result = await client.writeTerminal({ sessionId, data });
      if (!mountedRef.current) return false;
      return Boolean(sessionFromResult(result, '终端写入失败', setError, { silentOk: true }));
    } catch (err) {
      if (mountedRef.current) setError(errorMessage(err, '终端写入失败'));
      return false;
    }
  }, [client]);

  const resize = useCallback(async (sessionId: string, cols: number, rows: number) => {
    if (!sessionId || !Number.isInteger(cols) || !Number.isInteger(rows)) return false;
    try {
      const result = await client.resizeTerminal({ sessionId, cols, rows });
      if (!mountedRef.current) return false;
      const session = sessionFromResult(result, '调整终端尺寸失败', setError, { silentOk: true });
      if (session) upsertSession(session);
      return Boolean(session);
    } catch (err) {
      if (mountedRef.current) setError(errorMessage(err, '调整终端尺寸失败'));
      return false;
    }
  }, [client, upsertSession]);

  const kill = useCallback(async (sessionId: string) => runSessionAction(
    'kill',
    () => client.killTerminal({ sessionId }),
    '停止终端失败',
    setBusyAction,
    setError,
    upsertSession,
    () => mountedRef.current,
  ), [client, upsertSession]);

  const close = useCallback(async (sessionId: string) => {
    if (!sessionId) return false;
    setBusyAction('close');
    try {
      const result = await client.closeTerminal({ sessionId });
      if (!mountedRef.current) return false;
      if (!result.ok) {
        setError(result.message || '关闭终端失败');
        return false;
      }
      removeSession(result.session?.id || sessionId, result.session?.projectId ?? projectIdRef.current);
      return true;
    } catch (err) {
      if (mountedRef.current) setError(errorMessage(err, '关闭终端失败'));
      return false;
    } finally {
      if (mountedRef.current) setBusyAction((current) => (current === 'close' ? null : current));
    }
  }, [client, removeSession]);

  const rename = useCallback(async (sessionId: string, title: string) => runSessionAction(
    'rename',
    () => client.renameTerminal({ sessionId, title }),
    '重命名终端失败',
    setBusyAction,
    setError,
    upsertSession,
    () => mountedRef.current,
  ), [client, upsertSession]);

  const clear = useCallback(async (sessionId: string) => {
    if (!sessionId) return false;
    setBusyAction('clear');
    try {
      const result = await client.clearTerminal({ sessionId });
      if (!mountedRef.current) return false;
      const session = sessionFromResult(result, '清屏失败', setError, { silentOk: true });
      if (session) upsertSession(session);
      return Boolean(session);
    } catch (err) {
      if (mountedRef.current) setError(errorMessage(err, '清屏失败'));
      return false;
    } finally {
      if (mountedRef.current) setBusyAction((current) => (current === 'clear' ? null : current));
    }
  }, [client, upsertSession]);

  const replay = useCallback(async (sessionId: string) => {
    if (!sessionId) return null;
    setBusyAction('replay');
    try {
      const result = await client.replayTerminal({ sessionId });
      if (!mountedRef.current) return null;
      if (!result.ok) {
        setError(result.message || '读取终端输出失败');
        return null;
      }
      if (typeof result.lastSeq === 'number' && result.lastSeq >= 0) {
        terminalSequencesRef.current.set(sessionId, result.lastSeq);
        // The view writes this REST replay in the promise continuation. Delay
        // the first live attach to the next task so replay always precedes
        // live output, including a synchronous WebSocket test double.
        setTimeout(() => openTerminalConnection(sessionId), 0);
      }
      upsertSession(result.session);
      return result;
    } catch (err) {
      if (mountedRef.current) setError(errorMessage(err, '读取终端输出失败'));
      return null;
    } finally {
      if (mountedRef.current) setBusyAction((current) => (current === 'replay' ? null : current));
    }
  }, [client, openTerminalConnection, upsertSession]);

  const removeLocal = useCallback((sessionId: string) => {
    removeSession(sessionId);
  }, [removeSession]);

  const subscribeData = useCallback((sessionId: string, handler: TerminalDataHandler) => {
    if (!sessionId) return () => {};
    let handlers = dataHandlersRef.current.get(sessionId);
    if (!handlers) {
      handlers = new Map();
      dataHandlersRef.current.set(sessionId, handlers);
    }
    handlers.set(handler, (handlers.get(handler) || 0) + 1);
    openTerminalConnection(sessionId);
    let active = true;
    return () => {
      if (!active) return;
      active = false;
      const current = dataHandlersRef.current.get(sessionId);
      if (!current) return;
      const owners = current.get(handler) || 0;
      if (owners > 1) current.set(handler, owners - 1);
      else current.delete(handler);
      if (current.size === 0) {
        dataHandlersRef.current.delete(sessionId);
        terminalConnectionsRef.current.get(sessionId)?.();
        terminalConnectionsRef.current.delete(sessionId);
        // A remounted xterm starts from an empty screen. Force it through the
        // bounded REST replay before a fresh socket attach rather than letting
        // an old live cursor race ahead of that reconstruction.
        terminalSequencesRef.current.delete(sessionId);
      }
    };
  }, [openTerminalConnection]);

  const activeSession = useMemo(
    () => sessions.find((session) => session.id === activeSessionId) ?? sessions[0] ?? null,
    [activeSessionId, sessions],
  );
  const activeCount = useMemo(() => sessions.filter(isTerminalActive).length, [sessions]);

  return {
    sessions,
    activeSession,
    activeSessionId: activeSession?.id ?? null,
    activeCount,
    loading,
    error,
    busyAction,
    setActiveSessionId,
    clearError,
    refresh,
    createSession,
    write,
    resize,
    kill,
    close,
    rename,
    clear,
    replay,
    removeLocal,
    subscribeData,
  };
}

export function isTerminalActive(session: TerminalSession) {
  const status = String(session.status || '').toLowerCase();
  return !session.endedAt && !['exited', 'killed', 'error'].includes(status);
}

function isValidProjectId(projectId: number) {
  return Number.isInteger(projectId) && projectId > 0;
}

function terminalConnectionError(reason: string): string {
  const messages: Record<string, string> = {
    replay_gap: '终端回放已过期，请刷新会话',
    cursor_too_old: '终端回放游标无效，请刷新会话',
    authorization_failed: '终端访问权限已失效',
    origin_forbidden: '终端来源校验被拒绝',
    protocol_error: '终端数据通道协议错误',
    slow_consumer: '终端输出过快，连接已关闭',
    feature_disabled: 'Terminal Go transport 未启用',
  };
  return messages[reason] ?? '终端数据通道暂不可用';
}

function belongsToProject(session: TerminalSession, projectId: number) {
  return Number(session.projectId) === Number(projectId);
}

function normalizeSessions(
  sessions: TerminalSession[] = [],
  projectId: number,
  closedSessionKeys: ReadonlySet<string> = new Set(),
) {
  return sortSessions(sessions.filter((session) => (
    belongsToProject(session, projectId)
    && !shouldRemoveSession(session, projectId, closedSessionKeys)
  )));
}

function mergeSessions(
  current: TerminalSession[],
  incoming: TerminalSession[],
  projectId: number,
  closedSessionKeys: ReadonlySet<string>,
) {
  const byId = new Map<string, TerminalSession>();
  normalizeSessions(current, projectId, closedSessionKeys).forEach((session) => byId.set(session.id, session));
  normalizeSessions(incoming, projectId, closedSessionKeys).forEach((session) => byId.set(session.id, session));
  return sortSessions(Array.from(byId.values()));
}

function sortSessions(sessions: TerminalSession[]) {
  return [...sessions].sort((left, right) => String(left.createdAt || '').localeCompare(String(right.createdAt || '')));
}

function sameSessionList(left: TerminalSession[], right: TerminalSession[]) {
  if (left.length !== right.length) return false;
  return left.every((session, index) => sessionSignature(session) === sessionSignature(right[index]));
}

function sessionSignature(session: TerminalSession) {
  return [
    session.id,
    session.projectId,
    session.title,
    session.cwd,
    session.shell,
    session.status,
    session.createdAt,
    session.endedAt ?? '',
    session.exitCode ?? '',
    session.cols ?? '',
    session.rows ?? '',
    session.profile?.id ?? '',
    session.profile?.name ?? '',
    session.profile?.shellPath ?? '',
    session.closed ? 'closed' : '',
  ].join('\0');
}

function removeSessionFromState(
  sessionId: string,
  setSessions: (action: (current: TerminalSession[]) => TerminalSession[]) => void,
  setActiveSessionIdState: (action: (active: string | null) => string | null) => void,
  dataHandlersRef: { current: Map<string, TerminalDataHandlers> },
) {
  if (!sessionId) return;
  setSessions((current) => {
    const next = current.filter((session) => session.id !== sessionId);
    setActiveSessionIdState((active) => (
      active && next.some((session) => session.id === active) ? active : next[0]?.id ?? null
    ));
    dataHandlersRef.current.delete(sessionId);
    return next;
  });
}

function shouldRemoveSession(
  session: TerminalSession,
  projectId: number,
  closedSessionKeys: ReadonlySet<string>,
) {
  return Boolean(session.closed) || closedSessionKeys.has(closedSessionKey(projectId, session.id));
}

function rememberClosedSessions(
  sessions: TerminalSession[] = [],
  projectId: number,
  closedSessionKeys: Set<string>,
) {
  sessions.forEach((session) => {
    if (belongsToProject(session, projectId) && session.closed) {
      rememberClosedSession(session.id, session.projectId, closedSessionKeys);
    }
  });
}

function rememberClosedSession(
  sessionId: string | null | undefined,
  projectId: number | string,
  closedSessionKeys: Set<string>,
) {
  const id = String(sessionId || '').trim();
  if (!id) return;
  closedSessionKeys.add(closedSessionKey(projectId, id));
}

function closedSessionKey(projectId: number | string, sessionId: string) {
  return `${projectKey(projectId)}:${sessionId}`;
}

function projectKey(projectId: number | string) {
  const number = Number(projectId);
  return Number.isFinite(number) ? String(number) : String(projectId || '');
}

function terminalEventSessionId(event: TerminalEvent) {
  return String(event.sessionId || event.session?.id || '').trim();
}

function terminalEventProjectId(event: TerminalEvent) {
  return event.projectId ?? event.session?.projectId;
}

function eventBelongsToProject(event: TerminalEvent, projectId: number) {
  if (event.session) return belongsToProject(event.session, projectId);
  return Number(event.projectId) === Number(projectId);
}

function terminalEventClosed(
  event: TerminalEvent,
  projectId: number,
  closedSessionKeys: ReadonlySet<string>,
) {
  if (event.closed || event.session?.closed) return true;
  const sessionId = terminalEventSessionId(event);
  if (!sessionId) return false;
  return closedSessionKeys.has(closedSessionKey(projectId, sessionId))
    || closedSessionKeys.has(closedSessionKey(terminalEventProjectId(event) ?? projectId, sessionId));
}

function sessionFromResult(
  result: TerminalSessionResult,
  fallback: string,
  setError: (message: string) => void,
  options: { silentOk?: boolean } = {},
) {
  if (!result.ok) {
    setError(result.message || fallback);
    return null;
  }
  if (!options.silentOk) setError('');
  return result.session;
}

async function runSessionAction(
  action: TerminalBusyAction,
  call: () => Promise<TerminalSessionResult>,
  fallback: string,
  setBusyAction: (action: TerminalBusyAction | null | ((current: TerminalBusyAction | null) => TerminalBusyAction | null)) => void,
  setError: (message: string) => void,
  upsertSession: (session: TerminalSession | null | undefined) => void,
  isActive: () => boolean,
) {
  setBusyAction(action);
  try {
    const result = await call();
    if (!isActive()) return null;
    const session = sessionFromResult(result, fallback, setError);
    if (!session) return null;
    upsertSession(session);
    return session;
  } catch (err) {
    if (isActive()) setError(errorMessage(err, fallback));
    return null;
  } finally {
    if (isActive()) setBusyAction((current) => (current === action ? null : current));
  }
}

function errorMessage(error: unknown, fallback: string) {
  if (error instanceof Error && error.message) return error.message;
  const text = String(error || '').trim();
  return text || fallback;
}
