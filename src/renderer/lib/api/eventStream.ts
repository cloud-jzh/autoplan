import {
  compareEventIDs,
  parseP10EventEnvelope,
  type EventStreamWatermark,
  type P10EventEnvelope,
  type RendererResyncReason,
  type ResumableEventSubscription,
} from './events';

const MAXIMUM_FRAME_BYTES = 1024 * 1024;
const INITIAL_RETRY_DELAY_MS = 250;
const MAXIMUM_RETRY_DELAY_MS = 10_000;

export interface ChatSSEEventEnvelope {
  schema_version: 1;
  event_id: string;
  project_id: number;
  project_revision: number;
  request_id: string;
  occurred_at: string;
  type: 'chat_chunk' | 'chat_queue' | 'chat_done';
  data: Record<string, unknown>;
}

export interface ResumableChatEventStreamOptions {
  projectId: number;
  initialWatermark?: EventStreamWatermark;
  open: (lastEventId: string | null, signal: AbortSignal) => Promise<Response>;
  onEvent: (event: ChatSSEEventEnvelope) => void;
  onResync: (reason: RendererResyncReason) => void;
  onWatermark: (watermark: EventStreamWatermark) => void;
  onState: (state: 'connecting' | 'open' | 'retrying' | 'unavailable' | 'closed', attempt: number) => void;
  isRetryable: (error: unknown) => boolean;
}

export interface ResumableEventStreamOptions {
  projectId: number;
  /** Operation streams intentionally do not require contiguous project revisions. */
  requireContiguousRevisions: boolean;
  initialWatermark?: EventStreamWatermark;
  open: (lastEventId: string | null, signal: AbortSignal) => Promise<Response>;
  onEvent: (event: P10EventEnvelope) => void;
  onResync: (reason: RendererResyncReason) => void;
  onWatermark: (watermark: EventStreamWatermark) => void;
  onState: (state: 'connecting' | 'open' | 'retrying' | 'unavailable' | 'closed', attempt: number) => void;
  isRetryable: (error: unknown) => boolean;
}

/**
 * Fetch-based SSE ownership with one AbortController and a bounded retry timer.
 * It deliberately pauses after resync_required so an old cursor cannot loop
 * before the owner has atomically committed an authoritative snapshot.
 */
export function createResumableEventStream(options: ResumableEventStreamOptions): ResumableEventSubscription {
  let watermark: EventStreamWatermark = { eventId: options.initialWatermark?.eventId ?? null, projectRevision: options.initialWatermark?.projectRevision ?? null };
  let stopped = false;
  let resyncBlocked = false;
  let attempt = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let activeController: AbortController | null = null;

  const notify = (state: Parameters<ResumableEventStreamOptions['onState']>[0]) => {
    try {
      options.onState(state, attempt + 1);
    } catch {
      // Observers cannot alter stream ownership.
    }
  };
  const stop = (() => {
    if (stopped) return;
    stopped = true;
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    activeController?.abort();
    activeController = null;
    notify('closed');
  }) as ResumableEventSubscription;
  stop.completeResync = () => {
    if (stopped || !resyncBlocked) return;
    resyncBlocked = false;
    watermark = { eventId: null, projectRevision: null };
    options.onWatermark(watermark);
    attempt = 0;
    scheduleConnect(0, false);
  };

  const requireResync = (reason: RendererResyncReason) => {
    if (stopped || resyncBlocked) return;
    resyncBlocked = true;
    activeController?.abort();
    activeController = null;
    try {
      options.onResync(reason);
    } catch {
      // The stream remains paused until its owner acknowledges the resync.
    }
  };
  const scheduleRetry = () => {
    if (stopped || resyncBlocked) return;
    attempt += 1;
    const delay = Math.min(MAXIMUM_RETRY_DELAY_MS, INITIAL_RETRY_DELAY_MS * (2 ** Math.min(attempt - 1, 5)));
    notify('retrying');
    scheduleConnect(delay, true);
  };
  const scheduleConnect = (delay: number, _isRetry: boolean) => {
    if (stopped || resyncBlocked) return;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => {
      timer = undefined;
      void connect();
    }, delay);
  };
  const connect = async () => {
    if (stopped || resyncBlocked || activeController) return;
    const controller = new AbortController();
    activeController = controller;
    notify('connecting');
    try {
      const response = await options.open(watermark.eventId, controller.signal);
      if (stopped || controller.signal.aborted || resyncBlocked) return;
      attempt = 0;
      notify('open');
      const outcome = await consumeSSE(response.body, controller.signal, (frame) => {
        if (stopped || resyncBlocked) return 'stop';
        let event: P10EventEnvelope;
        try {
          event = parseP10EventEnvelope(frame.data);
        } catch {
          requireResync('invalid_event');
          return 'stop';
        }
        if (frame.event !== null && frame.event !== event.type ||
            frame.id !== null && frame.id !== event.event_id || event.project_id !== options.projectId) {
          requireResync(event.project_id !== options.projectId ? 'project_mismatch' : 'invalid_event');
          return 'stop';
        }
        if (event.type === 'heartbeat') return 'continue';
        if (event.type === 'resync_required') {
          requireResync(event.payload.reason as RendererResyncReason);
          return 'stop';
        }
        const eventID = event.event_id;
        const revision = event.project_revision;
        if (eventID === null || revision === null) {
          requireResync('invalid_event');
          return 'stop';
        }
        if (watermark.eventId !== null && compareEventIDs(eventID, watermark.eventId) <= 0) return 'continue';
        if (watermark.projectRevision !== null &&
            (revision <= watermark.projectRevision ||
              (options.requireContiguousRevisions && revision !== watermark.projectRevision + 1))) {
          requireResync(revision <= watermark.projectRevision ? 'event_out_of_order' : 'revision_gap');
          return 'stop';
        }
        try {
          options.onEvent(event);
        } catch {
          requireResync('invalid_event');
          return 'stop';
        }
        watermark = { eventId: eventID, projectRevision: revision };
        options.onWatermark(watermark);
        return 'continue';
      });
      if (!stopped && !resyncBlocked && outcome === 'closed') scheduleRetry();
    } catch (error) {
      if (!stopped && !controller.signal.aborted && !resyncBlocked) {
        if (options.isRetryable(error)) scheduleRetry();
        else notify('unavailable');
      }
    } finally {
      if (activeController === controller) activeController = null;
    }
  };

  scheduleConnect(0, false);
  return stop;
}

/**
 * P13A has a deliberately narrow SSE envelope. It shares retry, cursor and
 * resync ownership with P10 but never coerces a Chat event into P10 payload.
 */
export function createResumableChatEventStream(options: ResumableChatEventStreamOptions): ResumableEventSubscription {
  let watermark: EventStreamWatermark = { eventId: options.initialWatermark?.eventId ?? null, projectRevision: options.initialWatermark?.projectRevision ?? null };
  let stopped = false;
  let resyncBlocked = false;
  let attempt = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let activeController: AbortController | null = null;
  const notify = (state: Parameters<ResumableChatEventStreamOptions['onState']>[0]) => {
    try { options.onState(state, attempt + 1); } catch { /* observers do not own the connection */ }
  };
  const stop = (() => {
    if (stopped) return;
    stopped = true;
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    activeController?.abort();
    activeController = null;
    notify('closed');
  }) as ResumableEventSubscription;
  stop.completeResync = () => {
    if (stopped || !resyncBlocked) return;
    resyncBlocked = false;
    watermark = { eventId: null, projectRevision: null };
    options.onWatermark(watermark);
    attempt = 0;
    scheduleConnect(0);
  };
  const requireResync = (reason: RendererResyncReason) => {
    if (stopped || resyncBlocked) return;
    resyncBlocked = true;
    activeController?.abort();
    activeController = null;
    try { options.onResync(reason); } catch { /* acknowledgement remains required */ }
  };
  const scheduleRetry = () => {
    if (stopped || resyncBlocked) return;
    attempt += 1;
    notify('retrying');
    scheduleConnect(Math.min(MAXIMUM_RETRY_DELAY_MS, INITIAL_RETRY_DELAY_MS * (2 ** Math.min(attempt - 1, 5))));
  };
  const scheduleConnect = (delay: number) => {
    if (stopped || resyncBlocked) return;
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(() => { timer = undefined; void connect(); }, delay);
  };
  const connect = async () => {
    if (stopped || resyncBlocked || activeController) return;
    const controller = new AbortController();
    activeController = controller;
    notify('connecting');
    try {
      const response = await options.open(watermark.eventId, controller.signal);
      if (stopped || controller.signal.aborted || resyncBlocked) return;
      attempt = 0;
      notify('open');
      const outcome = await consumeSSE(response.body, controller.signal, (frame) => {
        if (stopped || resyncBlocked) return 'stop';
        const parsed = parseChatFrame(frame);
        if (parsed.kind === 'invalid') { requireResync('invalid_event'); return 'stop'; }
        if (parsed.kind === 'control') {
          if (parsed.type === 'heartbeat') return 'continue';
          requireResync(parsed.reason);
          return 'stop';
        }
        const event = parsed.event;
        if (frame.event !== event.type || frame.id !== event.event_id || event.project_id !== options.projectId) {
          requireResync(event.project_id !== options.projectId ? 'project_mismatch' : 'invalid_event');
          return 'stop';
        }
        if (watermark.eventId !== null && compareEventIDs(event.event_id, watermark.eventId) <= 0) return 'continue';
        if (watermark.projectRevision !== null && event.project_revision <= watermark.projectRevision) {
          requireResync('event_out_of_order');
          return 'stop';
        }
        try { options.onEvent(event); } catch { requireResync('invalid_event'); return 'stop'; }
        watermark = { eventId: event.event_id, projectRevision: event.project_revision };
        options.onWatermark(watermark);
        return 'continue';
      });
      if (!stopped && !resyncBlocked && outcome === 'closed') scheduleRetry();
    } catch (error) {
      if (!stopped && !controller.signal.aborted && !resyncBlocked) {
        if (options.isRetryable(error)) scheduleRetry(); else notify('unavailable');
      }
    } finally {
      if (activeController === controller) activeController = null;
    }
  };
  scheduleConnect(0);
  return stop;
}

type ParsedChatFrame =
  | { kind: 'event'; event: ChatSSEEventEnvelope }
  | { kind: 'control'; type: 'heartbeat' | 'resync_required'; reason: RendererResyncReason }
  | { kind: 'invalid' };

function parseChatFrame(frame: SSEFrame): ParsedChatFrame {
  try {
    const control = parseP10EventEnvelope(frame.data);
    if (control.event_class !== 'control' || (control.type !== 'heartbeat' && control.type !== 'resync_required') ||
        frame.id !== null || frame.event !== control.type) return { kind: 'invalid' };
    return control.type === 'heartbeat'
      ? { kind: 'control', type: 'heartbeat', reason: 'invalid_event' }
      : { kind: 'control', type: 'resync_required', reason: control.payload.reason as RendererResyncReason };
  } catch {
    // Persistent P13A events have a different, frozen envelope.
  }
  const value = frame.data;
  if (!isRecord(value)) return { kind: 'invalid' };
  const required = ['schema_version', 'event_id', 'project_id', 'project_revision', 'request_id', 'occurred_at', 'type', 'data'];
  if (Object.keys(value).length !== required.length || required.some((key) => !(key in value))) return { kind: 'invalid' };
  if (value.schema_version !== 1 || typeof value.event_id !== 'string' || !/^[1-9][0-9]{0,18}$/.test(value.event_id) ||
      !positiveSafeInteger(value.project_id) || !positiveSafeInteger(value.project_revision) || typeof value.request_id !== 'string' ||
      !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/.test(value.request_id) || typeof value.occurred_at !== 'string' ||
      !validUTC(value.occurred_at) || (value.type !== 'chat_chunk' && value.type !== 'chat_queue' && value.type !== 'chat_done') ||
      !isSafeChatData(value.data)) return { kind: 'invalid' };
  return { kind: 'event', event: value as unknown as ChatSSEEventEnvelope };
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0;
}

function validUTC(value: string): boolean {
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value) && !Number.isNaN(Date.parse(value));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isSafeChatData(value: unknown, depth = 0): value is Record<string, unknown> {
  if (!isRecord(value) || depth > 16 || Object.keys(value).length > 128) return false;
  return Object.entries(value).every(([key, child]) =>
    !/workspace[_-]?path|(?:^|_)(?:env|token|password|secret|credential|command|stdout|stderr)(?:$|_)/i.test(key) &&
    (child === null || typeof child === 'boolean' || typeof child === 'number' ||
      typeof child === 'string' && child.length <= 2048 ||
      Array.isArray(child) && child.length <= 128 && child.every((item) => item === null || typeof item !== 'object' || isSafeChatData(item, depth + 1)) ||
      isRecord(child) && isSafeChatData(child, depth + 1)),
  );
}

type ConsumeResult = 'closed' | 'stopped';
type FrameResult = 'continue' | 'stop';
interface SSEFrame { id: string | null; event: string | null; data: unknown; }

async function consumeSSE(
  stream: ReadableStream<Uint8Array> | null,
  signal: AbortSignal,
  onFrame: (frame: SSEFrame) => FrameResult,
): Promise<ConsumeResult> {
  if (!stream) throw new TypeError('event_stream_unavailable');
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffered = '';
  let frameBytes = 0;
  let id: string | null = null;
  let event: string | null = null;
  let data: string[] = [];
  const dispatch = (): FrameResult => {
    if (!data.length) {
      id = null;
      event = null;
      return 'continue';
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(data.join('\n')) as unknown;
    } catch {
      throw new TypeError('invalid_event_stream');
    }
    const outcome = onFrame({ id, event, data: parsed });
    id = null;
    event = null;
    data = [];
    frameBytes = 0;
    return outcome;
  };
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      if (done) break;
      frameBytes += value.byteLength;
      if (frameBytes > MAXIMUM_FRAME_BYTES) throw new TypeError('invalid_event_stream');
      buffered += decoder.decode(value, { stream: true });
      let newline = buffered.indexOf('\n');
      while (newline >= 0) {
        const line = buffered.slice(0, newline).replace(/\r$/, '');
        buffered = buffered.slice(newline + 1);
        if (line === '') {
          if (dispatch() === 'stop') return 'stopped';
        } else if (!line.startsWith(':')) {
          const separator = line.indexOf(':');
          const field = separator < 0 ? line : line.slice(0, separator);
          const raw = separator < 0 ? '' : line.slice(separator + 1).replace(/^ /, '');
          if (field === 'id') id = raw;
          else if (field === 'event') event = raw;
          else if (field === 'data') data.push(raw);
          else if (field !== 'retry') throw new TypeError('invalid_event_stream');
        }
        newline = buffered.indexOf('\n');
      }
    }
    if (!signal.aborted && buffered.length) throw new TypeError('invalid_event_stream');
    return 'closed';
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}
