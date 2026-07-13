export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function source(...parts: string[]) { return readFileSync(join(process.cwd(), ...parts), 'utf8'); }
function expect(value: unknown, message: string) { if (!value) throw new Error(message); }

describe('P13A useChatQueue transport isolation', () => {
  const hook = source('src', 'renderer', 'hooks', 'useChatQueue.ts');

  it('uses a complete queue snapshot only for the selected conversation', () => {
    expect(hook.includes('const client = useAutoplanClient();'), 'queue hook must use injected client');
    expect(hook.includes('const chatHttp = useHttpChatOperations();'), 'queue HTTP gate is missing');
    expect(hook.includes('snapshot.conversationId !== cid'), 'queue events must be conversation-scoped');
    expect(hook.includes('setItems(Array.isArray(snapshot.items) ? snapshot.items : []);'), 'queue event must replace, not append, the snapshot');
    expect(!/window\.autoplan\.(?:chat|conversation)/.test(hook), 'queue hook bypasses AutoplanClient');
  });

  it('recovers queue state once after resync and closes subscriptions under StrictMode', () => {
    expect(hook.includes('chatHttp.connectChatEvents(projectId, cid'), 'queue SSE endpoint must be conversation-scoped');
    expect(hook.includes('client.chatQueueList({ projectId, conversationId: cid })'), 'resync must read authoritative queue');
    expect(hook.includes('.finally(() => stream?.completeResync());'), 'reconnect must wait for queue reload');
    expect(hook.includes('stream?.();'), 'queue stream cleanup is missing');
    expect(hook.includes('active = false;'), 'late queue events must be ignored after cleanup');
  });
});
