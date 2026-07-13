import type {
  AiConfigChangedEvent,
  AppSnapshot,
  ChatDoneEvent,
  ChatQueueSnapshot,
  ClaudeCliConfigChangedEvent,
  TerminalClosedEvent,
  TerminalEvent,
  WorkspaceSnapshotPatch,
} from '../../types';

/** A subscription owns exactly one listener and returns its release function. */
export type Unsubscribe = () => void;
export type EventHandler<TEvent> = (event: TEvent) => void;
export type Subscribe<TEvent> = (handler: EventHandler<TEvent>) => Unsubscribe;

export type ProjectEventConnectionState =
  | 'connecting'
  | 'open'
  | 'retrying'
  | 'unavailable'
  | 'closed';

export interface ProjectEventConnectionUpdate {
  projectId: number;
  state: ProjectEventConnectionState;
  attempt: number;
  operationId?: string;
  /** Present only when a submitted runtime Operation has a pinned owner. */
  runtimeOwner?: 'go' | 'node';
}

export type P10EventClass = 'business' | 'operation' | 'control';
export type P10ResyncReason =
  | 'last_event_id_invalid'
  | 'last_event_id_future'
  | 'history_expired'
  | 'revision_gap'
  | 'project_mismatch'
  | 'slow_consumer';
export type RendererResyncReason = P10ResyncReason | 'invalid_event' | 'event_out_of_order';

/** P14 terminal output is a private WebSocket stream, never a P10 SSE event. */
export type TerminalSocketConnectionState = 'connecting' | 'open' | 'replaying' | 'retrying' | 'closed' | 'unavailable';
export type TerminalSocketFailureReason =
  | 'replay_gap' | 'cursor_too_old' | 'authorization_failed' | 'origin_forbidden'
  | 'protocol_error' | 'slow_consumer' | 'feature_disabled' | 'service_unavailable';
export interface TerminalSocketConnectionUpdate {
  projectId: number;
  sessionId: string;
  runtime: 'go';
  state: TerminalSocketConnectionState;
  lastSeq: number;
  reason?: TerminalSocketFailureReason;
}

export interface P10EventEnvelope {
  schema_version: 1;
  event_class: P10EventClass;
  event_id: string | null;
  project_id: number;
  project_revision: number | null;
  type: string;
  operation_id: string | null;
  request_id: string | null;
  occurred_at: string;
  payload: Record<string, unknown>;
}

export interface EventStreamWatermark {
  eventId: string | null;
  projectRevision: number | null;
}

export type ProjectEventDelivery =
  | {
      kind: 'event';
      projectId: number;
      source: 'project' | 'operation';
      event: P10EventEnvelope;
    }
  | {
      kind: 'resync';
      projectId: number;
      source: 'project' | 'operation';
      reason: RendererResyncReason;
    };

export type ProjectEventHandler = EventHandler<ProjectEventDelivery>;

/** A resync blocks reconnects until the authoritative snapshot has committed. */
export interface ResumableEventSubscription extends Unsubscribe {
  completeResync: () => void;
}

export type ConnectProjectEvents = (
  projectId: number,
  onState?: EventHandler<ProjectEventConnectionUpdate>,
  onEvent?: ProjectEventHandler,
) => ResumableEventSubscription;

export type ConnectOperationEvents = (
  projectId: number,
  operationId: string,
  onState?: EventHandler<ProjectEventConnectionUpdate>,
  onEvent?: ProjectEventHandler,
) => ResumableEventSubscription;

const EVENT_ID_PATTERN = /^[1-9][0-9]{0,18}$/;
const EVENT_REQUEST_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
const EVENT_IDENTIFIER_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const EVENT_UTC_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/;
const BUSINESS_EVENT_PATTERN = /^(?:project\.(?:snapshot|patch)|business\.[a-z][a-z0-9]*(?:[._:-][a-z0-9]+)*)$/;
const OPERATION_EVENT_TYPES = new Set([
  'operation.queued', 'operation.running', 'operation.succeeded',
  'operation.failed', 'operation.cancelled', 'operation.interrupted',
]);
const RESYNC_REASONS = new Set<P10ResyncReason>([
  'last_event_id_invalid', 'last_event_id_future', 'history_expired',
  'revision_gap', 'project_mismatch', 'slow_consumer',
]);
const FORBIDDEN_PAYLOAD_KEY = /workspace[_-]?path|user[_-]?data|(?:^|_)(?:env|token|password|secret|credential)(?:$|_)|env[_-]?vars|api[_-]?key|access[_-]?token|refresh[_-]?token|private[_-]?key|authorization|cookie|session|command|stdout|stderr|(?:^|_)path(?:$|_)|cwd|workdir/i;
const MAXIMUM_PAYLOAD_DEPTH = 16;
const MAXIMUM_PAYLOAD_ITEMS = 128;
const MAXIMUM_PAYLOAD_STRING_LENGTH = 2048;

/** Parses the frozen P10 envelope before a renderer cache can observe it. */
export function parseP10EventEnvelope(value: unknown): P10EventEnvelope {
  if (!isRecord(value)) throw new TypeError('invalid_event');
  const required = [
    'schema_version', 'event_class', 'event_id', 'project_id', 'project_revision',
    'type', 'operation_id', 'request_id', 'occurred_at', 'payload',
  ];
  if (required.some((key) => !(key in value)) || Object.keys(value).some((key) => !required.includes(key))) {
    throw new TypeError('invalid_event');
  }
  const envelope: P10EventEnvelope = {
    schema_version: value.schema_version as 1,
    event_class: value.event_class as P10EventClass,
    event_id: value.event_id as string | null,
    project_id: value.project_id as number,
    project_revision: value.project_revision as number | null,
    type: value.type as string,
    operation_id: value.operation_id as string | null,
    request_id: value.request_id as string | null,
    occurred_at: value.occurred_at as string,
    payload: value.payload as Record<string, unknown>,
  };
  if (envelope.schema_version !== 1 ||
      !['business', 'operation', 'control'].includes(envelope.event_class) ||
      !positiveSafeInteger(envelope.project_id) || typeof envelope.type !== 'string' ||
      envelope.type.length === 0 || envelope.type.length > 128 ||
      typeof envelope.occurred_at !== 'string' || !EVENT_UTC_PATTERN.test(envelope.occurred_at) ||
      Number.isNaN(Date.parse(envelope.occurred_at)) || !isSafePayload(envelope.payload, 0)) {
    throw new TypeError('invalid_event');
  }
  if (envelope.event_class === 'control') {
    if (envelope.event_id !== null || envelope.project_revision !== null) throw new TypeError('invalid_event');
    if (envelope.type === 'heartbeat') {
      if (envelope.operation_id !== null || envelope.request_id !== null || Object.keys(envelope.payload).length !== 0) {
        throw new TypeError('invalid_event');
      }
    } else if (envelope.type !== 'resync_required' || !isResyncPayload(envelope.payload)) {
      throw new TypeError('invalid_event');
    }
    return envelope;
  }
  if (!validPersistentFields(envelope)) throw new TypeError('invalid_event');
  if (envelope.event_class === 'business' && !BUSINESS_EVENT_PATTERN.test(envelope.type)) {
    throw new TypeError('invalid_event');
  }
  if (envelope.event_class === 'operation' &&
      (!OPERATION_EVENT_TYPES.has(envelope.type) || !validIdentifier(envelope.operation_id))) {
    throw new TypeError('invalid_event');
  }
  return envelope;
}

export function compareEventIDs(left: string, right: string): number {
  if (!EVENT_ID_PATTERN.test(left) || !EVENT_ID_PATTERN.test(right)) throw new TypeError('invalid_event_id');
  return left.length === right.length ? left.localeCompare(right) : left.length - right.length;
}

export function isTerminalOperationEvent(event: P10EventEnvelope): boolean {
  return event.event_class === 'operation' && [
    'operation.succeeded', 'operation.failed', 'operation.cancelled', 'operation.interrupted',
  ].includes(event.type);
}

function validPersistentFields(envelope: P10EventEnvelope) {
  return typeof envelope.event_id === 'string' && EVENT_ID_PATTERN.test(envelope.event_id) &&
    positiveSafeInteger(envelope.project_revision) && validIdentifier(envelope.request_id) &&
    (envelope.operation_id === null || validIdentifier(envelope.operation_id));
}

function validIdentifier(value: unknown): value is string {
  return typeof value === 'string' && EVENT_IDENTIFIER_PATTERN.test(value);
}

function positiveSafeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0;
}

function isResyncPayload(payload: Record<string, unknown>) {
  return Object.keys(payload).length === 1 && typeof payload.reason === 'string' &&
    RESYNC_REASONS.has(payload.reason as P10ResyncReason);
}

function isSafePayload(value: unknown, depth: number): value is Record<string, unknown> {
  if (!isRecord(value) || depth > MAXIMUM_PAYLOAD_DEPTH || Object.keys(value).length > MAXIMUM_PAYLOAD_ITEMS) return false;
  return Object.entries(value).every(([key, child]) => !FORBIDDEN_PAYLOAD_KEY.test(key) && isSafePayloadValue(child, depth + 1));
}

function isSafePayloadValue(value: unknown, depth: number): boolean {
  if (value === null || typeof value === 'boolean' || typeof value === 'number') return true;
  if (typeof value === 'string') return value.length <= MAXIMUM_PAYLOAD_STRING_LENGTH;
  if (Array.isArray(value)) return depth <= MAXIMUM_PAYLOAD_DEPTH && value.length <= MAXIMUM_PAYLOAD_ITEMS &&
    value.every((child) => isSafePayloadValue(child, depth + 1));
  return depth <= MAXIMUM_PAYLOAD_DEPTH && isSafePayload(value, depth);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** The existing chat transport envelope. Its data DTO is intentionally not remodelled. */
export interface ChatChunkTransportEvent {
  type: string;
  data: Record<string, unknown>;
}

/**
 * Canonical payload map for events owned by the business API transport.
 * Full snapshots and patches remain distinct so consumers can preserve the
 * existing full-snapshot-wins ordering rule.
 */
export interface AutoplanEventMap {
  snapshot: AppSnapshot;
  patch: WorkspaceSnapshotPatch;
  // Legacy IPC callers remain valid. P006's Go WebSocket adapter invokes the
  // session-scoped terminal connection callback directly; it never maps raw
  // output into these project SSE-compatible event channels.
  terminalData: TerminalEvent & { data: string };
  terminalExit: TerminalEvent;
  terminalStatus: TerminalEvent;
  terminalClosed: TerminalClosedEvent;
  chatChunk: ChatChunkTransportEvent;
  chatDone: ChatDoneEvent;
  chatQueue: ChatQueueSnapshot;
  aiConfigChanged: AiConfigChangedEvent;
  claudeCliConfigChanged: ClaudeCliConfigChangedEvent;
}

/** Event subscription surface exposed by every AutoplanClient transport. */
export interface AutoplanClientEvents {
  onLoopUpdate: Subscribe<AutoplanEventMap['snapshot']>;
  onLoopPatch: Subscribe<AutoplanEventMap['patch']>;
  onTerminalData: Subscribe<AutoplanEventMap['terminalData']>;
  onTerminalExit: Subscribe<AutoplanEventMap['terminalExit']>;
  onTerminalStatus: Subscribe<AutoplanEventMap['terminalStatus']>;
  onTerminalClosed: Subscribe<AutoplanEventMap['terminalClosed']>;
  onChatChunk: Subscribe<AutoplanEventMap['chatChunk']>;
  onChatDone: Subscribe<AutoplanEventMap['chatDone']>;
  onChatQueue: Subscribe<AutoplanEventMap['chatQueue']>;
  onAiConfigChanged: Subscribe<AutoplanEventMap['aiConfigChanged']>;
  onClaudeCliConfigChanged: Subscribe<AutoplanEventMap['claudeCliConfigChanged']>;
}

export const AUTOPLAN_CLIENT_EVENT_KEYS = [
  'onLoopUpdate',
  'onLoopPatch',
  'onTerminalData',
  'onTerminalExit',
  'onTerminalStatus',
  'onTerminalClosed',
  'onChatChunk',
  'onChatDone',
  'onChatQueue',
  'onAiConfigChanged',
  'onClaudeCliConfigChanged',
] as const satisfies readonly (keyof AutoplanClientEvents)[];

export type AutoplanClientEventKey = (typeof AUTOPLAN_CLIENT_EVENT_KEYS)[number];

// Kept on the events boundary so older transport contract fixtures can load
// HttpAutoplanClient without a second runtime module dependency.
export { createResumableEventStream, createResumableChatEventStream } from './eventStream';
export type { ChatSSEEventEnvelope, ResumableChatEventStreamOptions, ResumableEventStreamOptions } from './eventStream';
