import type { TerminalRestSession, TerminalSocketClientEnvelope, TerminalSocketServerEnvelope } from '../../types';

export type TerminalSocketState = 'connecting' | 'open' | 'replaying' | 'retrying' | 'closed' | 'unavailable';
export type TerminalSocketFailureReason =
  | 'replay_gap'
  | 'cursor_too_old'
  | 'authorization_failed'
  | 'origin_forbidden'
  | 'protocol_error'
  | 'slow_consumer'
  | 'feature_disabled'
  | 'service_unavailable';

export interface TerminalSocketHandlers {
  onOutput?: (message: Extract<TerminalSocketServerEnvelope, { type: 'output' }>) => void;
  onExit?: (message: Extract<TerminalSocketServerEnvelope, { type: 'exit' }>) => void;
  onStatus?: (message: Extract<TerminalSocketServerEnvelope, { type: 'status' }>) => void;
  onClosed?: (message: Extract<TerminalSocketServerEnvelope, { type: 'closed' }>) => void;
  onState?: (state: TerminalSocketState, reason?: TerminalSocketFailureReason) => void;
}

export interface TerminalSocketOptions {
  baseUrl: string;
  projectId: number;
  sessionId: string;
  lastSeq?: number;
  handlers?: TerminalSocketHandlers;
  socketFactory?: (url: string) => WebSocket;
  retryLimit?: number;
  retryBaseMs?: number;
}

const MAXIMUM_FRAME_BYTES = 64 << 10;
const DEFAULT_RETRY_LIMIT = 5;
const DEFAULT_RETRY_BASE_MS = 250;
const MAXIMUM_RETRY_DELAY_MS = 5_000;
const TERMINAL_ID_PATTERN = /^term_[a-z0-9][a-z0-9_-]{5,}$/;

/**
 * Browser-facing wrapper around P14's private terminal WebSocket. Credentials
 * are never placed in this URL; Electron's privileged main process supplies
 * the session header only for the validated loopback terminal endpoint.
 */
export class TerminalSocket {
  readonly #baseUrl: string;
  readonly #projectId: number;
  readonly #sessionId: string;
  readonly #handlers: TerminalSocketHandlers;
  readonly #socketFactory: (url: string) => WebSocket;
  readonly #retryLimit: number;
  readonly #retryBaseMs: number;
  #lastSeq: number;
  #attempt = 0;
  #socket: WebSocket | null = null;
  #timer: ReturnType<typeof setTimeout> | null = null;
  #closed = false;
  #state: TerminalSocketState = 'closed';

  constructor(options: TerminalSocketOptions) {
    if (!validLoopbackBaseUrl(options?.baseUrl) || !positiveInteger(options?.projectId) ||
        !TERMINAL_ID_PATTERN.test(String(options?.sessionId || '')) || !nonNegativeSequence(options?.lastSeq ?? 0)) {
      throw new TypeError('terminal_socket_configuration_invalid');
    }
    const socketFactory = options.socketFactory ?? defaultSocketFactory;
    if (typeof socketFactory !== 'function') throw new TypeError('terminal_socket_unavailable');
    this.#baseUrl = options.baseUrl;
    this.#projectId = options.projectId;
    this.#sessionId = options.sessionId;
    this.#lastSeq = options.lastSeq ?? 0;
    this.#handlers = options.handlers ?? {};
    this.#socketFactory = socketFactory;
    this.#retryLimit = boundedPositive(options.retryLimit ?? DEFAULT_RETRY_LIMIT, DEFAULT_RETRY_LIMIT, 8);
    this.#retryBaseMs = boundedPositive(options.retryBaseMs ?? DEFAULT_RETRY_BASE_MS, DEFAULT_RETRY_BASE_MS, 1_000);
  }

  get lastSeq(): number { return this.#lastSeq; }
  get state(): TerminalSocketState { return this.#state; }

  open(): void {
    if (this.#closed || this.#socket || this.#timer) return;
    this.#setState(this.#attempt === 0 ? 'connecting' : 'retrying');
    let socket: WebSocket;
    try {
      socket = this.#socketFactory(terminalSocketURL(this.#baseUrl, this.#projectId, this.#sessionId, this.#lastSeq));
    } catch {
      this.#scheduleRetry('service_unavailable');
      return;
    }
    this.#socket = socket;
    socket.onopen = () => {
      if (this.#socket !== socket || this.#closed) return;
      this.#setState('replaying');
      this.#setState('open');
    };
    socket.onmessage = (event) => this.#receive(socket, event.data);
    socket.onerror = () => undefined;
    socket.onclose = (event) => {
      if (this.#socket !== socket) return;
      this.#socket = null;
      if (this.#closed) {
        this.#setState('closed');
        return;
      }
      this.#scheduleRetry(closeReason(event.code));
    };
  }

  sendInput(data: string): boolean {
    return this.#send({ type: 'input', data });
  }

  resize(cols: number, rows: number): boolean {
    return this.#send({ type: 'resize', cols, rows });
  }

  ping(nonce: string): boolean {
    return this.#send({ type: 'ping', nonce });
  }

  close(): void {
    if (this.#closed) return;
    this.#closed = true;
    if (this.#timer !== null) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    const socket = this.#socket;
    this.#socket = null;
    if (socket && socket.readyState === 1) {
      try { socket.close(1000); } catch { /* browser close failures are terminal-local */ }
    }
    this.#setState('closed');
  }

  #send(message: TerminalSocketClientEnvelope): boolean {
    const socket = this.#socket;
    if (this.#closed || !socket || socket.readyState !== 1 || !validClientMessage(message)) return false;
    try {
      socket.send(JSON.stringify(message));
      return true;
    } catch {
      return false;
    }
  }

  #receive(socket: WebSocket, data: unknown): void {
    if (this.#socket !== socket || this.#closed || typeof data !== 'string' || data.length > MAXIMUM_FRAME_BYTES) {
      this.#protocolFailure(socket);
      return;
    }
    let value: unknown;
    try {
      value = JSON.parse(data);
    } catch {
      this.#protocolFailure(socket);
      return;
    }
    const message = parseServerMessage(value);
    if (!message) {
      this.#protocolFailure(socket);
      return;
    }
    if (message.type === 'output') {
      if (message.seq <= this.#lastSeq) return;
      if (this.#lastSeq !== 0 && message.seq !== this.#lastSeq + 1) {
        this.#protocolFailure(socket);
        return;
      }
      this.#lastSeq = message.seq;
      this.#handlers.onOutput?.(message);
      return;
    }
    if (message.type === 'exit') this.#handlers.onExit?.(message);
    else if (message.type === 'status') this.#handlers.onStatus?.(message);
    else if (message.type === 'closed') this.#handlers.onClosed?.(message);
  }

  #protocolFailure(socket: WebSocket): void {
    if (this.#socket !== socket) return;
    try { socket.close(1002); } catch { /* close is best-effort */ }
    this.#setState('unavailable', 'protocol_error');
  }

  #scheduleRetry(reason: TerminalSocketFailureReason): void {
    if (this.#closed) return;
    // Count the connection lifetime rather than each individual TCP upgrade:
    // an upgrade that immediately closes as a slow consumer must not reset the
    // budget and form an unbounded reconnect loop.
    if (!retryable(reason) || this.#attempt >= this.#retryLimit) {
      this.#setState('unavailable', reason);
      return;
    }
    this.#attempt += 1;
    this.#setState('retrying', reason);
    const delay = Math.min(MAXIMUM_RETRY_DELAY_MS, this.#retryBaseMs * 2 ** (this.#attempt - 1));
    this.#timer = setTimeout(() => {
      this.#timer = null;
      this.open();
    }, delay);
  }

  #setState(state: TerminalSocketState, reason?: TerminalSocketFailureReason): void {
    this.#state = state;
    this.#handlers.onState?.(state, reason);
  }
}

export function terminalSocketURL(baseUrl: string, projectId: number, sessionId: string, lastSeq = 0): string {
  if (!validLoopbackBaseUrl(baseUrl) || !positiveInteger(projectId) || !TERMINAL_ID_PATTERN.test(sessionId) || !nonNegativeSequence(lastSeq)) {
    throw new TypeError('terminal_socket_configuration_invalid');
  }
  const url = new URL(baseUrl);
  url.protocol = 'ws:';
  url.pathname = `/api/v1/terminals/${encodeURIComponent(sessionId)}/ws`;
  url.search = `?project_id=${projectId}&last_seq=${lastSeq}`;
  return url.toString();
}

function defaultSocketFactory(url: string): WebSocket {
  if (typeof WebSocket !== 'function') throw new TypeError('terminal_socket_unavailable');
  return new WebSocket(url);
}

function parseServerMessage(value: unknown): TerminalSocketServerEnvelope | null {
  if (!isRecord(value) || typeof value.type !== 'string') return null;
  switch (value.type) {
    case 'output':
      return exactKeys(value, ['type', 'seq', 'data']) && positiveSequence(value.seq) && validData(value.data)
        ? { type: 'output', seq: value.seq, data: value.data } : null;
    case 'exit':
      return exactKeys(value, ['type', 'exit_code', 'signal']) && nullableInteger(value.exit_code) && nullableShortString(value.signal)
        ? { type: 'exit', exit_code: value.exit_code, signal: value.signal } : null;
    case 'status':
      return exactKeys(value, ['type', 'session']) && validServerSession(value.session)
        ? { type: 'status', session: value.session as unknown as TerminalRestSession } : null;
    case 'closed':
      return exactKeys(value, ['type', 'closed', 'session']) && value.closed === true && validServerSession(value.session)
        ? { type: 'closed', closed: true, session: value.session as unknown as TerminalRestSession } : null;
    case 'pong':
      return exactKeys(value, ['type', 'nonce']) && typeof value.nonce === 'string' && value.nonce.length > 0 && value.nonce.length <= 128
        ? { type: 'pong', nonce: value.nonce } : null;
    default:
      return null;
  }
}

function validClientMessage(value: TerminalSocketClientEnvelope): boolean {
  if (value.type === 'input') return validData(value.data);
  if (value.type === 'resize') return Number.isInteger(value.cols) && value.cols >= 2 && value.cols <= 500 &&
    Number.isInteger(value.rows) && value.rows >= 1 && value.rows <= 200;
  return typeof value.nonce === 'string' && value.nonce.length > 0 && value.nonce.length <= 128;
}

function validServerSession(value: unknown): value is Record<string, unknown> {
  if (!isRecord(value) || !exactKeys(value, [
    'id', 'project_id', 'title', 'cwd', 'shell', 'status', 'created_at', 'ended_at', 'exit_code',
    'cols', 'rows', 'profile', 'closed', 'runtime',
  ])) return false;
  return TERMINAL_ID_PATTERN.test(String(value.id)) && positiveInteger(value.project_id) &&
    typeof value.title === 'string' && typeof value.cwd === 'string' && typeof value.shell === 'string' &&
    typeof value.status === 'string' && typeof value.created_at === 'string' &&
    (value.ended_at === null || typeof value.ended_at === 'string') && nullableInteger(value.exit_code) &&
    Number.isInteger(value.cols) && Number(value.cols) >= 2 && Number(value.cols) <= 500 &&
    Number.isInteger(value.rows) && Number(value.rows) >= 1 && Number(value.rows) <= 200 &&
    typeof value.closed === 'boolean' && value.runtime === 'go' && validServerProfile(value.profile);
}

function validServerProfile(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['id', 'name', 'kind', 'shell_path', 'args', 'env']) &&
    typeof value.id === 'string' && typeof value.name === 'string' &&
    (value.kind === 'default' || value.kind === 'custom') && typeof value.shell_path === 'string' &&
    Array.isArray(value.args) && value.args.every((item) => typeof item === 'string') &&
    isRecord(value.env) && Object.keys(value.env).length === 0;
}

function closeReason(code: number): TerminalSocketFailureReason {
  if (code === 1013) return 'slow_consumer';
  if (code === 1008) return 'authorization_failed';
  if (code === 1002 || code === 1007 || code === 1009) return 'protocol_error';
  if (code === 1012 || code === 1011) return 'service_unavailable';
  return 'service_unavailable';
}

function retryable(reason: TerminalSocketFailureReason): boolean {
  return reason === 'service_unavailable' || reason === 'slow_consumer';
}

function validLoopbackBaseUrl(value: unknown): value is string {
  try {
    const url = new URL(String(value));
    return url.protocol === 'http:' && url.hostname === '127.0.0.1' && Boolean(url.port) &&
      url.pathname === '/' && !url.username && !url.password && !url.search && !url.hash;
  } catch {
    return false;
  }
}

function exactKeys(value: Record<string, unknown>, expected: readonly string[]): boolean {
  const keys = Object.keys(value);
  return keys.length === expected.length && expected.every((key) => key in value);
}
function isRecord(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value); }
function positiveInteger(value: unknown): value is number { return typeof value === 'number' && Number.isSafeInteger(value) && value > 0; }
function nonNegativeSequence(value: unknown): value is number { return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= 9007199254740991; }
function positiveSequence(value: unknown): value is number { return nonNegativeSequence(value) && value > 0; }
function nullableInteger(value: unknown): value is number | null { return value === null || (typeof value === 'number' && Number.isSafeInteger(value) && value >= -2147483648 && value <= 2147483647); }
function nullableShortString(value: unknown): value is string | null { return value === null || (typeof value === 'string' && value.length <= 64); }
function validData(value: unknown): value is string { return typeof value === 'string' && value.length > 0 && value.length <= MAXIMUM_FRAME_BYTES; }
function boundedPositive(value: unknown, fallback: number, maximum: number): number { return Number.isInteger(value) && Number(value) > 0 && Number(value) <= maximum ? Number(value) : fallback; }
