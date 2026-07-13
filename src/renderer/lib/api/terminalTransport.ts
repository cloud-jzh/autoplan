import type {
  AutoplanApi,
  TerminalCloseResult,
  TerminalCreateInput,
  TerminalErrorResult,
  TerminalEvent,
  TerminalListResult,
  TerminalReplayResult,
  TerminalRestCreateInput,
  TerminalRestReplay,
  TerminalRestSession,
  TerminalSession,
  TerminalSessionIdInput,
  TerminalSessionResult,
} from '../../types';
import type { TerminalConnectionHandlers } from './client';
import { TerminalSocket } from './terminalSocket';

type LegacyTerminalOperations = Pick<AutoplanApi,
  'createTerminal' | 'listTerminals' | 'writeTerminal' | 'resizeTerminal' |
  'killTerminal' | 'closeTerminal' | 'renameTerminal' | 'replayTerminal' | 'clearTerminal'>;

export interface GoTerminalControlPlane {
  create(projectId: number, input: TerminalRestCreateInput): Promise<TerminalRestSession>;
  list(projectId: number): Promise<TerminalRestSession[]>;
  write(projectId: number, sessionId: string, data: string): Promise<TerminalRestSession>;
  resize(projectId: number, sessionId: string, cols: number, rows: number): Promise<TerminalRestSession>;
  kill(projectId: number, sessionId: string): Promise<TerminalRestSession>;
  close(projectId: number, sessionId: string): Promise<TerminalRestSession>;
  rename(projectId: number, sessionId: string, title: string): Promise<TerminalRestSession>;
  clear(projectId: number, sessionId: string): Promise<TerminalRestSession>;
  replay(projectId: number, sessionId: string, lastSeq: number): Promise<TerminalRestReplay>;
}

export interface TerminalTransportOptions {
  enabled: boolean;
  legacy: LegacyTerminalOperations;
  control?: GoTerminalControlPlane;
  baseUrl?: string;
  socketFactory?: (url: string) => WebSocket;
}

interface SessionOwner {
  projectId: number;
  runtime: 'go' | 'node';
  session: TerminalSession;
}

/** A terminal-only owner selector. No non-terminal operation can use it. */
export class TerminalTransport {
  readonly #enabled: boolean;
  readonly #legacy: LegacyTerminalOperations;
  readonly #control: GoTerminalControlPlane | null;
  readonly #baseUrl: string | null;
  readonly #socketFactory?: (url: string) => WebSocket;
  readonly #sessions = new Map<string, SessionOwner>();

  constructor(options: TerminalTransportOptions) {
    this.#enabled = options.enabled === true;
    this.#legacy = options.legacy;
    // The flag owns admission for *new* sessions.  A session already created
    // by Go must remain on Go after a rollback, so keep its constrained
    // control plane available for owner-routed operations and discovery.
    this.#control = options.control ?? null;
    this.#baseUrl = typeof options.baseUrl === 'string' ? options.baseUrl : null;
    this.#socketFactory = options.socketFactory;
  }

  create = async (input: TerminalCreateInput): Promise<TerminalSessionResult> => {
    // A legacy-admission rejection is terminal and is intentionally not
    // retried through Go. Retry would cross-adopt a runtime during rollback.
    if (!this.#enabled) return this.#legacy.createTerminal(input);
    const projectId = validProject(input?.projectId);
    if (!projectId || !this.#control) return terminalFailure('terminal_feature_disabled');
    try {
      const session = this.#remember(goSession(await this.#control.create(projectId, terminalCreateInput(input))));
      return { ok: true, session };
    } catch (error) {
      return terminalFailure(errorCode(error));
    }
  };

  list = async (input: { projectId: number }): Promise<TerminalListResult> => {
    const projectId = validProject(input?.projectId);
    if (!projectId) return terminalFailure('terminal_invalid_payload');
    const legacy = await this.#legacy.listTerminals({ projectId }).catch(() => ({ ok: true as const, sessions: [] }));
    const node = legacy.ok ? legacy.sessions.map((session) => this.#remember(nodeSession(session))) : [];
    // Rollback changes only future admission.  Continue discovering Go-owned
    // sessions so a renderer restart cannot reinterpret them as Node handles.
    if (!this.#control) return legacy.ok ? { ok: true, sessions: node } : legacy;
    try {
      const go = (await this.#control.list(projectId)).map((session) => this.#remember(goSession(session)));
      return { ok: true, sessions: mergeSessions(node, go) };
    } catch (error) {
      // A Go list outage never reassigns sessions, but it must not hide the
      // still-authoritative Node list during a terminal-only rollback.
      if (legacy.ok) return { ok: true, sessions: node };
      return terminalFailure(errorCode(error));
    }
  };

  write = async (input: { sessionId: string; data: string }): Promise<TerminalSessionResult> =>
    this.#mutate(input?.sessionId, (owner) => {
      if (owner.runtime === 'node') return this.#legacy.writeTerminal(input);
      if (!this.#control || typeof input?.data !== 'string' || input.data.length === 0) return Promise.resolve(terminalFailure('terminal_invalid_payload'));
      return this.#goResult(() => this.#control!.write(owner.projectId, owner.session.id, input.data));
    });

  resize = async (input: { sessionId: string; cols: number; rows: number }): Promise<TerminalSessionResult> =>
    this.#mutate(input?.sessionId, (owner) => {
      if (owner.runtime === 'node') return this.#legacy.resizeTerminal(input);
      if (!this.#control || !validSize(input.cols, input.rows)) return Promise.resolve(terminalFailure('terminal_invalid_payload'));
      return this.#goResult(() => this.#control!.resize(owner.projectId, owner.session.id, input.cols, input.rows));
    });

  kill = async (input: TerminalSessionIdInput): Promise<TerminalSessionResult> =>
    this.#mutate(input?.sessionId, (owner) => owner.runtime === 'node'
      ? this.#legacy.killTerminal(input)
      : this.#goResult(() => this.#control!.kill(owner.projectId, owner.session.id)));

  close = async (input: TerminalSessionIdInput): Promise<TerminalCloseResult> => {
    const owner = this.#owner(input?.sessionId);
    if (!owner) return terminalFailure('terminal_session_not_found');
    if (owner.runtime === 'node') return this.#legacy.closeTerminal(input);
    if (!this.#control) return terminalFailure('terminal_feature_disabled');
    try {
      const session = this.#remember(goSession(await this.#control.close(owner.projectId, owner.session.id)));
      this.#sessions.delete(session.id);
      return { ok: true, session, closed: true };
    } catch (error) {
      return terminalFailure(errorCode(error));
    }
  };

  rename = async (input: { sessionId: string; title: string }): Promise<TerminalSessionResult> =>
    this.#mutate(input?.sessionId, (owner) => owner.runtime === 'node'
      ? this.#legacy.renameTerminal(input)
      : this.#goResult(() => this.#control!.rename(owner.projectId, owner.session.id, input.title)));

  clear = async (input: TerminalSessionIdInput): Promise<TerminalSessionResult> =>
    this.#mutate(input?.sessionId, (owner) => owner.runtime === 'node'
      ? this.#legacy.clearTerminal(input)
      : this.#goResult(() => this.#control!.clear(owner.projectId, owner.session.id)));

  replay = async (input: TerminalSessionIdInput): Promise<TerminalReplayResult> => {
    const owner = this.#owner(input?.sessionId);
    if (!owner) return terminalFailure('terminal_session_not_found');
    if (owner.runtime === 'node') return this.#legacy.replayTerminal(input);
    if (!this.#control) return terminalFailure('terminal_feature_disabled');
    try {
      const replay = await this.#control.replay(owner.projectId, owner.session.id, 0);
      const session = this.#remember(goSession(replay.session));
      const chunks = replay.entries.map((entry) => entry.data);
      return { ok: true, session, chunks, data: chunks.join(''), lastSeq: replay.last_seq };
    } catch (error) {
      return terminalFailure(errorCode(error));
    }
  };

  connect = (projectId: number, sessionId: string, lastSeq: number, handlers: TerminalConnectionHandlers): (() => void) => {
    const owner = this.#owner(sessionId);
    if (!owner || owner.runtime !== 'go' || owner.projectId !== projectId || !this.#baseUrl) return () => undefined;
    const socket = new TerminalSocket({
      baseUrl: this.#baseUrl,
      projectId,
      sessionId,
      lastSeq,
      handlers: {
        onOutput: (message) => handlers.onData?.({
          sessionId, projectId, session: owner.session, seq: message.seq, data: message.data,
        }),
        onExit: (message) => handlers.onExit?.({
          sessionId, projectId, session: owner.session, exitCode: message.exit_code, signal: message.signal,
        }),
        onStatus: (message) => {
          const session = this.#remember(goSession(message.session));
          handlers.onStatus?.({ sessionId: session.id, projectId, session });
        },
        onClosed: (message) => {
          const session = this.#remember(goSession(message.session));
          this.#sessions.delete(session.id);
          handlers.onClosed?.({ sessionId: session.id, projectId, session: { ...session, closed: true }, closed: true });
        },
        onState: (state, reason) => {
          if (state === 'unavailable' && reason) handlers.onUnavailable?.(reason);
        },
      },
      ...(this.#socketFactory ? { socketFactory: this.#socketFactory } : {}),
    });
    socket.open();
    return () => socket.close();
  };

  #owner(sessionId: unknown): SessionOwner | null {
    const id = String(sessionId || '').trim();
    return id ? this.#sessions.get(id) ?? null : null;
  }

  async #mutate(
    sessionId: unknown,
    action: (owner: SessionOwner) => Promise<TerminalSessionResult>,
  ): Promise<TerminalSessionResult> {
    const owner = this.#owner(sessionId);
    if (!owner) return terminalFailure('terminal_session_not_found');
    return action(owner);
  }

  async #goResult(action: () => Promise<TerminalRestSession>): Promise<TerminalSessionResult> {
    if (!this.#control) return terminalFailure('terminal_feature_disabled');
    try {
      return { ok: true, session: this.#remember(goSession(await action())) };
    } catch (error) {
      return terminalFailure(errorCode(error));
    }
  }

  #remember(session: TerminalSession): TerminalSession {
    const projectId = validProject(session.projectId);
    if (!projectId) throw new TypeError('terminal_invalid_response');
    const copied = { ...session, profile: { ...session.profile, args: [...session.profile.args], env: { ...session.profile.env } } };
    const runtime = copied.runtime === 'go' ? 'go' : 'node';
    const existing = this.#sessions.get(copied.id);
    // Do not let a same-ID projection adopt a handle from the other runtime.
    // A collision is left with its original owner until it is closed.
    if (existing && existing.runtime !== runtime) return existing.session;
    this.#sessions.set(copied.id, { projectId, runtime, session: copied });
    return copied;
  }

}

function terminalCreateInput(input: TerminalCreateInput): TerminalRestCreateInput {
  const result: TerminalRestCreateInput = {};
  if (typeof input.cwd === 'string') result.cwd = input.cwd;
  if (typeof input.profileId === 'string') result.profile_id = input.profileId;
  if (input.profile && typeof input.profile !== 'string') {
    const id = typeof input.profile.id === 'string' ? input.profile.id : input.profile.profileId;
    if (typeof id === 'string') result.profile = { id };
  }
  if (typeof input.title === 'string') result.title = input.title;
  if (Number.isInteger(input.cols)) result.cols = input.cols;
  if (Number.isInteger(input.rows)) result.rows = input.rows;
  if (input.env && typeof input.env === 'object') result.env = input.env;
  return result;
}

export function goSession(value: TerminalRestSession): TerminalSession {
  return {
    id: value.id,
    projectId: value.project_id,
    title: value.title,
    cwd: value.cwd,
    shell: value.shell,
    status: value.status,
    createdAt: value.created_at,
    endedAt: value.ended_at,
    exitCode: value.exit_code,
    cols: value.cols,
    rows: value.rows,
    profile: {
      id: value.profile.id,
      name: value.profile.name,
      kind: value.profile.kind,
      shellPath: value.profile.shell_path,
      args: [...value.profile.args],
      env: {},
    },
    closed: value.closed,
    runtime: 'go',
  };
}

export function nodeSession(session: TerminalSession): TerminalSession {
  return { ...session, profile: { ...session.profile, args: [...session.profile.args], env: { ...session.profile.env } }, runtime: 'node' };
}

function mergeSessions(node: TerminalSession[], go: TerminalSession[]): TerminalSession[] {
  const byID = new Map<string, TerminalSession>();
  for (const session of node) byID.set(session.id, session);
  // A collision is never a licence to adopt a legacy handle. Preserve the
  // Node session and omit the conflicting Go projection until an operator
  // resolves the impossible ID collision.
  for (const session of go) if (!byID.has(session.id)) byID.set(session.id, session);
  return [...byID.values()].sort((left, right) => String(left.createdAt).localeCompare(String(right.createdAt)) || left.id.localeCompare(right.id));
}

function terminalFailure(code: string): TerminalErrorResult {
  return { ok: false, code, message: terminalMessage(code) };
}
function terminalMessage(code: string): string {
  const messages: Record<string, string> = {
    terminal_feature_disabled: 'Terminal Go transport is disabled',
    terminal_session_not_found: 'Terminal session was not found',
    terminal_forbidden: 'Terminal access is forbidden',
    terminal_replay_gap: 'Terminal replay is unavailable',
    terminal_cursor_too_old: 'Terminal replay cursor is invalid',
    terminal_slow_consumer: 'Terminal consumer is too slow',
    terminal_rate_limited: 'Terminal rate limit reached',
  };
  return messages[code] ?? 'Terminal transport is unavailable';
}
function errorCode(error: unknown): string {
  const code = error && typeof error === 'object' && typeof (error as { code?: unknown }).code === 'string'
    ? (error as { code: string }).code : 'terminal_pty_unavailable';
  return /^terminal_[a-z0-9_]+$/.test(code) ? code : 'terminal_pty_unavailable';
}
function validProject(value: unknown): number | null { return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null; }
function validSize(cols: unknown, rows: unknown): cols is number { return Number.isInteger(cols) && Number(cols) >= 2 && Number(cols) <= 500 && Number.isInteger(rows) && Number(rows) >= 1 && Number(rows) <= 200; }
