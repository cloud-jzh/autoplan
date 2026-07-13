import { getHttpChatOperations, type AutoplanClient, type HttpChatOperations, type HttpStaticOperations } from './client';
import {
  AUTOPLAN_HTTP_RUNTIME_CONFIG_KEY,
  HttpClientError,
  HttpAutoplanClient,
  type HttpAutoplanRuntimeConfig,
} from './httpClient';
import { UnavailableAutoplanClient } from './ipcClient';

export const AUTOPLAN_TRANSPORT_ENV = 'VITE_AUTOPLAN_TRANSPORT' as const;
export const DEFAULT_AUTOPLAN_TRANSPORT = 'http' as const;
export const HTTP_AUTOPLAN_TRANSPORT = 'http' as const;

export type AutoplanTransport = typeof HTTP_AUTOPLAN_TRANSPORT;

export interface AutoplanTransportConfig {
  requestedTransport?: string | null;
  transport: AutoplanTransport;
  fellBackToIpc: boolean;
  production: boolean;
}

/**
 * P005 admits only the Go HTTP/SSE/WebSocket application-service transport.
 * Environment values never revive a renderer-to-main business IPC fallback.
 */
export function resolveAutoplanTransport(
  requestedTransport?: string | null,
  production = false,
  runtimeAvailable = false,
): AutoplanTransportConfig {
  void runtimeAvailable;
  return {
    requestedTransport,
    transport: HTTP_AUTOPLAN_TRANSPORT,
    fellBackToIpc: false,
    production,
  };
}

function readTransportConfig(runtimeAvailable = false): AutoplanTransportConfig {
  return resolveAutoplanTransport(
    import.meta.env[AUTOPLAN_TRANSPORT_ENV],
    Boolean(import.meta.env.PROD),
    runtimeAvailable,
  );
}

export interface AutoplanClientFactoryOptions {
  config: AutoplanTransportConfig;
  unavailableClient: AutoplanClient;
  httpRuntime?: HttpAutoplanRuntimeConfig;
}

export function createAutoplanClient(options: AutoplanClientFactoryOptions): AutoplanClient {
  if (!options.httpRuntime) {
    throw new HttpClientError('go_business_transport_unavailable');
  }
  // HttpAutoplanClient owns P06 persistence and each independently gated
  // runtime family, including P14 Terminal REST plus its private WebSocket.
  // A selected HTTP mutation never changes transport mid-intent after it has
  // started.
  return new HttpAutoplanClient({ ...options.httpRuntime, delegate: options.unavailableClient });
}

function consumeHttpRuntimeConfig(): HttpAutoplanRuntimeConfig | undefined {
  const runtime = globalThis as typeof globalThis & {
    [AUTOPLAN_HTTP_RUNTIME_CONFIG_KEY]?: HttpAutoplanRuntimeConfig;
  };
  const config = runtime[AUTOPLAN_HTTP_RUNTIME_CONFIG_KEY];
  if (config !== undefined) Reflect.deleteProperty(runtime, AUTOPLAN_HTTP_RUNTIME_CONFIG_KEY);
  return config;
}

// Module lifetime is application lifetime. React StrictMode never recreates it.
const unavailableClient: AutoplanClient = new UnavailableAutoplanClient();
const httpRuntime = consumeHttpRuntimeConfig();
export const autoplanTransportConfig = Object.freeze(readTransportConfig(httpRuntime !== undefined));
const autoplanClient: AutoplanClient = createAutoplanClient({
  config: autoplanTransportConfig,
  unavailableClient,
  ...(httpRuntime ? { httpRuntime } : {}),
});

export function getAutoplanClient(): AutoplanClient {
  return autoplanClient;
}

/**
 * Static routes are projections of the same Go application service. A failed
 * HTTP request remains failed; no business IPC fallback exists.
 */
export function getStaticHttpOperations(): HttpStaticOperations | null {
  return autoplanClient as unknown as HttpStaticOperations;
}

/** P13A stays off unless the renderer handoff explicitly opens its own flag. */
export function getHttpChatOperationsForClient(client: AutoplanClient = autoplanClient): HttpChatOperations | null {
  return getHttpChatOperations(client);
}
