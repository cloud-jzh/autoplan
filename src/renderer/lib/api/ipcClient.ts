import {
  AUTOPLAN_CLIENT_OPERATION_KEYS,
  type AutoplanClient,
} from './client';
import type { AutoplanClientEventKey, EventHandler, Unsubscribe } from './events';

type ForwardedTarget = Record<PropertyKey, unknown>;

const QUERY_OPERATION_KEYS = [
  'listPlans', 'getPlan', 'listPlanTasks', 'getPlanTask', 'listPlanEvents',
] as const;
const EVENT_KEYS: readonly AutoplanClientEventKey[] = [
  'onLoopUpdate', 'onLoopPatch', 'onTerminalData', 'onTerminalExit', 'onTerminalStatus', 'onTerminalClosed',
  'onChatChunk', 'onChatDone', 'onChatQueue', 'onAiConfigChanged', 'onClaudeCliConfigChanged',
];

function unavailable(): never {
  throw new Error('go_business_transport_unavailable');
}

function unavailableAsync(): Promise<never> {
  return Promise.reject(new Error('go_business_transport_unavailable'));
}

/**
 * P005 deliberately has no renderer-to-main business IPC adapter. This class
 * is supplied only to keep unimplemented Go capability branches fail-closed
 * while HttpAutoplanClient owns every admitted business request.
 */
export interface UnavailableAutoplanClient extends AutoplanClient {}

export class UnavailableAutoplanClient {
  #destroyed = false;

  constructor() {
    const target = this as unknown as ForwardedTarget;
    for (const key of [...AUTOPLAN_CLIENT_OPERATION_KEYS, ...QUERY_OPERATION_KEYS]) {
      Object.defineProperty(target, key, {
        configurable: false,
        enumerable: true,
        writable: false,
        value: key === 'mcpToolNames' ? Object.freeze([]) : unavailableAsync,
      });
    }
    for (const key of EVENT_KEYS) {
      Object.defineProperty(target, key, {
        configurable: false,
        enumerable: true,
        writable: false,
        value: (_handler: EventHandler<unknown>): Unsubscribe => {
          if (this.#destroyed) unavailable();
          return () => undefined;
        },
      });
    }
  }

  destroy(): void {
    this.#destroyed = true;
  }
}
