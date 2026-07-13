import type { AutoplanApi, UpdateStatus } from '../../types';
import type { EventHandler, Unsubscribe } from '../api/events';
import {
  DESKTOP_BRIDGE_OPERATION_KEYS,
  type DesktopBridge,
} from './bridge';

type ForwardedTarget = Record<PropertyKey, unknown>;

function defineForwardedOperation(
  target: ForwardedTarget,
  api: AutoplanApi,
  key: (typeof DESKTOP_BRIDGE_OPERATION_KEYS)[number],
) {
  const operation = api[key];
  if (typeof operation !== 'function') {
    throw new TypeError(`window.autoplan.${key} must be a function`);
  }
  Object.defineProperty(target, key, {
    configurable: false,
    enumerable: true,
    writable: false,
    value: (...args: unknown[]) => Reflect.apply(operation, api, args),
  });
}

interface SharedUpdateSubscription {
  owners: number;
  unsubscribeIpc: Unsubscribe;
}

/** IPC implementation containing Electron-native capabilities only. */
export interface IpcDesktopBridge extends DesktopBridge {}

export class IpcDesktopBridge {
  readonly #api: AutoplanApi;
  readonly #subscriptions = new Map<EventHandler<UpdateStatus>, SharedUpdateSubscription>();
  readonly #activeOwners = new Set<Unsubscribe>();
  #destroyed = false;

  constructor(api: AutoplanApi = window.autoplan) {
    this.#api = api;
    for (const key of DESKTOP_BRIDGE_OPERATION_KEYS) {
      defineForwardedOperation(this as unknown as ForwardedTarget, api, key);
    }
  }

  onUpdateStatus = (handler: EventHandler<UpdateStatus>): Unsubscribe => {
    if (this.#destroyed) throw new Error('IpcDesktopBridge has been destroyed');

    let shared = this.#subscriptions.get(handler);
    if (!shared) {
      const unsubscribeIpc = this.#api.onUpdateStatus(handler);
      if (typeof unsubscribeIpc !== 'function') {
        throw new TypeError('window.autoplan.onUpdateStatus must return an unsubscribe function');
      }
      shared = { owners: 0, unsubscribeIpc };
      this.#subscriptions.set(handler, shared);
    }
    shared.owners += 1;

    let active = true;
    const unsubscribe = () => {
      if (!active) return;
      active = false;
      this.#activeOwners.delete(unsubscribe);
      if (!shared) return;
      shared.owners -= 1;
      if (shared.owners > 0) return;
      this.#subscriptions.delete(handler);
      shared.unsubscribeIpc();
    };
    this.#activeOwners.add(unsubscribe);
    return unsubscribe;
  };

  /** Releases every update listener owned by this bridge. Safe to repeat. */
  destroy(): void {
    if (this.#destroyed) return;
    this.#destroyed = true;

    let firstError: unknown;
    for (const unsubscribe of [...this.#activeOwners]) {
      try {
        unsubscribe();
      } catch (error) {
        firstError ??= error;
      }
    }
    if (firstError) throw firstError;
  }
}

let defaultDesktopBridge: DesktopBridge | null = null;

/** The sole renderer entrypoint for preload-backed native capabilities. */
export function getDefaultDesktopBridge(): DesktopBridge {
  defaultDesktopBridge ??= new IpcDesktopBridge();
  return defaultDesktopBridge;
}
