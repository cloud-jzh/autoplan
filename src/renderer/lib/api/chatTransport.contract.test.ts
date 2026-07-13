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

function expect(value: unknown, message: string) {
  if (!value) throw new Error(message);
}

describe('P13A Chat HTTP transport contract', () => {
  const client = source('src', 'renderer', 'lib', 'api', 'client.ts');
  const http = source('src', 'renderer', 'lib', 'api', 'httpClient.ts');
  const events = source('src', 'renderer', 'lib', 'api', 'eventStream.ts');
  const fixture = source('fixtures', 'migration', 'p13', 'chat', 'security-cases.json');
  const main = source('src', 'main.js');
  const preload = source('src', 'preload.js');

  it('keeps Chat on an independent, fail-closed runtime gate', () => {
    expect(client.includes("'go_chat_api'"), 'renderer Chat gate is missing');
    expect(http.includes('go_chat_api: false'), 'Chat gate must default to disabled');
    expect(main.includes("go_chat_api: 'AUTOPLAN_SIDECAR_GO_CHAT_API'"), 'main handoff gate is missing');
    expect(preload.includes("'go_chat_api'"), 'preload must validate the complete runtime feature document');
  });

  it('does not replay an admitted Chat mutation or fall back after HTTP selection', () => {
    const start = http.indexOf('chatSend =');
    const chatMethods = http.slice(start, http.indexOf('listStaticAIConfigs', start));
    expect(chatMethods.includes('idempotency_key: this.#idempotencyKeyFor(payload)'), 'send body lacks its stable idempotency key');
    expect(!chatMethods.includes('retryTransportFailure: true'), 'Chat writes must not be replayed by the renderer');
    expect(chatMethods.includes("if (!this.isChatHTTPEnabled()) return this.#delegate.chatSend(payload)"), 'disabled gate must preserve legacy Chat');
  });

  it('uses the P13A REST and resumable SSE boundaries', () => {
    for (const route of ['/messages', ':stop', '/queue', '/events']) {
      expect(http.includes(route), `missing Chat route fragment ${route}`);
    }
    expect(http.includes('createResumableChatEventStream'), 'Chat must not reuse the P10 payload parser');
    expect(events.includes('createResumableChatEventStream'), 'Chat SSE reconnect owner is missing');
    expect(events.includes('resyncBlocked'), 'Chat SSE must pause until authoritative resync completes');
    expect(http.includes('chunkSequences'), 'turn chunk sequence dedupe is missing');
    expect(events.includes("requireResync('event_out_of_order')"), 'out-of-order Chat events must resync');
  });

  it('blocks direct legacy Chat IPC while P13A owns the request', () => {
    expect(main.includes('assertLegacyChatAdapterEnabled();'), 'legacy Chat IPC lacks an ownership guard');
    expect(main.includes("throw new GoDataClientError('service_unavailable')"), 'legacy Chat guard must fail closed');
  });

  it('keeps security and rollback cases in the authorized P13A fixture', () => {
    for (const id of [
      'ordered_chunks_then_single_done', 'reconnect_drops_duplicate_event', 'conversation_switch_ignores_late_event',
      'resync_reads_history_and_queue_without_resend', 'cancelled_turn_stays_with_origin_runtime',
      'secret_and_path_redaction', 'idempotency_timeout_never_replays_turn',
    ]) {
      expect(fixture.includes(`"id": "${id}"`), `missing P13A fixture ${id}`);
    }
  });
});
