export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function source(...parts: string[]) {
  return readFileSync(join(process.cwd(), ...parts), 'utf8');
}

function expect(condition: unknown, message: string) {
  if (!condition) throw new Error(message);
}

function expectIncludes(sourceText: string, snippet: string, message: string) {
  expect(sourceText.includes(snippet), message);
}

function expectNotIncludes(sourceText: string, snippet: string, message: string) {
  expect(!sourceText.includes(snippet), message);
}

describe('useTerminalSessions regression', () => {
  it('keeps the hook API focused on terminal session lifecycle actions', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, "export type TerminalBusyAction = 'create' | 'refresh' | 'write' | 'resize' | 'kill' | 'close' | 'rename' | 'clear' | 'replay';", 'hook 应暴露终端动作 busy 枚举');
    expectIncludes(hook, 'export interface UseTerminalSessionsResult', 'hook 应声明返回结构');
    expectIncludes(hook, 'activeSession: TerminalSession | null;', '返回结构应包含 activeSession');
    expectIncludes(hook, 'activeCount: number;', '返回结构应包含运行会话计数');
    expectIncludes(hook, 'createSession: (input?: Partial<TerminalCreateInput>) => Promise<TerminalSession | null>;', '返回结构应包含 createSession');
    expectIncludes(hook, 'write: (sessionId: string, data: string) => Promise<boolean>;', '返回结构应包含 write');
    expectIncludes(hook, 'resize: (sessionId: string, cols: number, rows: number) => Promise<boolean>;', '返回结构应包含 resize');
    expectIncludes(hook, 'kill: (sessionId: string) => Promise<TerminalSession | null>;', '返回结构应包含 kill');
    expectIncludes(hook, 'close: (sessionId: string) => Promise<boolean>;', '返回结构应包含 close');
    expectIncludes(hook, 'rename: (sessionId: string, title: string) => Promise<TerminalSession | null>;', '返回结构应包含 rename');
    expectIncludes(hook, 'clear: (sessionId: string) => Promise<boolean>;', '返回结构应包含 clear');
    expectIncludes(hook, 'replay: (sessionId: string) => Promise<TerminalReplayResult | null>;', '返回结构应包含 replay');
    expectIncludes(hook, 'subscribeData: (sessionId: string, handler: TerminalDataHandler) => () => void;', '返回结构应包含数据订阅');
  });

  it('filters sessions by project, keeps selection stable, and counts only active sessions', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, 'function belongsToProject(session: TerminalSession, projectId: number)', 'hook 应集中判断 session 是否属于当前项目');
    expectIncludes(hook, 'return Number(session.projectId) === Number(projectId);', '项目归属判断应兼容字符串/数字 projectId');
    expectIncludes(hook, 'const closedSessionKeysRef = useRef(new Set<string>());', 'hook 应记录已关闭会话 tombstone');
    expectIncludes(hook, 'function normalizeSessions(', 'hook 应归一化初始和刷新会话');
    expectIncludes(hook, 'closedSessionKeys: ReadonlySet<string> = new Set(),', '归一化应接收 closed tombstone');
    expectIncludes(hook, 'belongsToProject(session, projectId)', '归一化应过滤其它项目终端');
    expectIncludes(hook, '&& !shouldRemoveSession(session, projectId, closedSessionKeys)', '归一化应过滤已关闭终端');
    expectIncludes(hook, 'String(left.createdAt || \'\').localeCompare(String(right.createdAt || \'\'))', '终端列表排序应稳定按创建时间升序');
    expectIncludes(hook, 'if (current && nextSessions.some((session) => session.id === current)) return current;', '刷新后应保留仍存在的选中会话');
    expectIncludes(hook, 'const running = nextSessions.find(isTerminalActive);', '当前会话丢失时应优先选中运行中的终端');
    expectIncludes(hook, 'const activeCount = useMemo(() => sessions.filter(isTerminalActive).length, [sessions]);', 'activeCount 应按 isTerminalActive 计算');
    expectIncludes(hook, "return !session.endedAt && !['exited', 'killed', 'error'].includes(status);", '活动判定应排除已退出/已杀死/错误会话');
  });

  it('subscribes to terminal event streams and cleans up every listener on unmount', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, 'const client = useAutoplanClient();', 'hook 应读取注入的业务客户端');
    expectIncludes(hook, 'const unsubscribeData = client.onTerminalData((event) => {', 'hook 应订阅终端 data 事件');
    expectIncludes(hook, 'const unsubscribeExit = client.onTerminalExit((event) => {', 'hook 应订阅终端 exit 事件');
    expectIncludes(hook, 'const unsubscribeStatus = client.onTerminalStatus((event) => {', 'hook 应订阅终端 status 事件');
    expectIncludes(hook, 'const unsubscribeClosed = client.onTerminalClosed((event) => {', 'hook 应订阅终端 closed 事件');
    expectIncludes(hook, 'active = false;', '卸载后迟到事件应失效');
    expectIncludes(hook, 'if (!eventBelongsToProject(event, projectIdRef.current)) return;', 'data 事件应过滤其它项目');
    expectIncludes(hook, 'if (terminalEventClosed(event, projectIdRef.current, closedSessionKeysRef.current)) {', 'data 事件遇到关闭态应执行删除语义');
    expectIncludes(hook, 'const data = String(event.data ?? \'\');', 'data 事件应归一化输出文本');
    expectIncludes(hook, 'if (!data) return;', '空 data 不应触发订阅回调');
    expectIncludes(hook, 'handlers.forEach((_owners, handler) => handler(data, event.session));', 'data 事件应分发给当前 session 的订阅者');
    expectIncludes(hook, 'handlers.set(handler, (handlers.get(handler) || 0) + 1);', '重复数据订阅应按 owner 引用计数去重');
    expectIncludes(hook, 'if (!active) return;', '数据订阅释放应幂等');
    expectIncludes(hook, 'unsubscribeData();', '卸载时应清理 data 监听');
    expectIncludes(hook, 'unsubscribeExit();', '卸载时应清理 exit 监听');
    expectIncludes(hook, 'unsubscribeStatus();', '卸载时应清理 status 监听');
    expectIncludes(hook, 'unsubscribeClosed();', '卸载时应清理 closed 监听');
  });

  it('treats close results and closed events as deletes instead of upserts', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, 'const removeSession = useCallback((sessionId: string, sessionProjectId: number | string = projectIdRef.current) => {', 'hook 应集中执行关闭删除语义');
    expectIncludes(hook, 'rememberClosedSession(sessionId, sessionProjectId, closedSessionKeysRef.current);', '删除前应记录 closed tombstone');
    expectIncludes(hook, 'removeSessionFromState(sessionId, setSessions, setActiveSessionIdState, dataHandlersRef);', '删除应同步移除会话、活动会话和数据订阅');
    expectIncludes(hook, 'removeSession(result.session?.id || sessionId, result.session?.projectId ?? projectIdRef.current);', 'closeTerminal 成功后应按返回 session 删除');
    expectIncludes(hook, 'if (shouldRemoveSession(session, projectIdRef.current, closedSessionKeysRef.current)) {', 'upsert 前应识别关闭态 session');
    expectIncludes(hook, 'removeSession(session.id, session.projectId);', '关闭态 session 应删除而不是 upsert');
    expectIncludes(hook, 'function terminalEventClosed(', 'hook 应集中判断关闭事件');
    expectIncludes(hook, 'if (event.closed || event.session?.closed) return true;', 'closed 事件或 closed session 均应视为删除');
    expectIncludes(hook, 'closedSessionKeys.has(closedSessionKey(projectId, sessionId))', '旧 exit/status 事件命中 tombstone 时不应重新插入');
    expectIncludes(hook, 'active && next.some((session) => session.id === active) ? active : next[0]?.id ?? null', '删除当前或最后一个会话后应重新选择或清空 activeSessionId');
  });

  it('routes actions through preload terminal APIs without executor or script side effects', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, 'client.listTerminals({ projectId: requestProjectId })', 'refresh 应调用 listTerminals');
    expectIncludes(hook, 'client.createTerminal(payload)', 'createSession 应调用 createTerminal');
    expectIncludes(hook, 'client.writeTerminal({ sessionId, data })', 'write 应调用 writeTerminal');
    expectIncludes(hook, 'client.resizeTerminal({ sessionId, cols, rows })', 'resize 应调用 resizeTerminal');
    expectIncludes(hook, 'client.killTerminal({ sessionId })', 'kill 应调用 killTerminal');
    expectIncludes(hook, 'client.closeTerminal({ sessionId })', 'close 应调用 closeTerminal');
    expectIncludes(hook, 'client.renameTerminal({ sessionId, title })', 'rename 应调用 renameTerminal');
    expectIncludes(hook, 'client.clearTerminal({ sessionId })', 'clear 应调用 clearTerminal');
    expectIncludes(hook, 'client.replayTerminal({ sessionId })', 'replay 应调用 replayTerminal');
    expectNotIncludes(hook, 'runExecutor', '终端 hook 不应直接运行执行器');
    expectNotIncludes(hook, 'runScript', '终端 hook 不应直接运行脚本');
    expectNotIncludes(hook, 'lastStatus', '终端 hook 不应修改执行器最近状态');
  });

  it('opens only the injected private terminal connection and deduplicates Go output by sequence', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, "import { useAutoplanClient, useTerminalConnectionOperations } from '../lib/api/provider';", 'hook 应通过 provider 获取 terminal connection');
    expectIncludes(hook, 'const terminalConnectionsRef = useRef(new Map<string, () => void>());', 'hook 应保存受限 terminal connection');
    expectIncludes(hook, 'const terminalSequencesRef = useRef(new Map<string, number>());', 'hook 应保存每会话最后已应用 seq');
    expectIncludes(hook, 'if (sequence <= previous) return;', '重复或乱序 terminal output 不应重复应用');
    expectIncludes(hook, 'if (previous !== 0 && sequence !== previous + 1)', 'seq 缺口应进入可恢复错误状态');
    expectIncludes(hook, 'terminalConnectionOperations?.connectTerminal?.(', 'hook 应经注入 client 打开私有 terminal socket');
    expectIncludes(hook, 'terminalConnectionsRef.current.get(sessionId)?.();', '最后一个订阅释放时应关闭对应 socket');
    expectNotIncludes(hook, 'new WebSocket(', 'hook 不应直接创建 WebSocket');
    expectNotIncludes(hook, 'window.autoplan', 'hook 不应直接访问 preload 对象');
  });

  it('orders REST replay before live attachment and releases socket state on teardown', () => {
    const hook = source('src', 'renderer', 'hooks', 'useTerminalSessions.ts');

    expectIncludes(hook, 'setTimeout(() => openTerminalConnection(sessionId), 0);', 'live socket attach must follow replay delivery');
    expectIncludes(hook, 'terminalConnectionsRef.current.forEach((unsubscribe) => unsubscribe());', 'unmount and project changes must close every private socket');
    expectIncludes(hook, 'terminalSequencesRef.current.clear();', 'project changes must not carry a cursor across projects');
    expectIncludes(hook, 'terminalSequencesRef.current.delete(sessionId);', 'xterm remount must replay rather than race an old cursor');
    expectIncludes(hook, 'if (!mountedRef.current || !sessionId || terminalConnectionsRef.current.has(sessionId)', 'strict-mode cleanup must prevent duplicate attachment');
  });
});
