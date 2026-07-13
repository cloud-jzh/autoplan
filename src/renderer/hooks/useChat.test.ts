export {};

type TestRegistrar = (name: string, fn: () => void) => void;

declare function require(id: string): unknown;
declare const process: { cwd(): string };

const { describe, it } = require('node:test') as { describe: TestRegistrar; it: TestRegistrar };
const { readFileSync } = require('node:fs') as { readFileSync: (path: string, encoding: string) => string };
const { join } = require('node:path') as { join: (...parts: string[]) => string };

function source(...parts: string[]) { return readFileSync(join(process.cwd(), ...parts), 'utf8'); }
function expect(value: unknown, message: string) { if (!value) throw new Error(message); }

describe('P13A useChat transport isolation', () => {
  const hook = source('src', 'renderer', 'hooks', 'useChat.ts');

  it('uses the injected AutoplanClient and never a component-level preload Chat call', () => {
    expect(hook.includes('const client = useAutoplanClient();'), 'Chat hook must use the injected client');
    expect(hook.includes('const chatHttp = useHttpChatOperations();'), 'Chat HTTP selection must remain feature-gated');
    expect(!/window\.autoplan\.(?:chat|conversation)/.test(hook), 'Chat hook bypasses AutoplanClient');
    expect(hook.includes('await client.chatSend({'), 'send must remain on the selected transport');
    expect(hook.includes('await client.chatStop({'), 'stop must remain on the selected transport');
    expect(hook.includes('await client.chatClear({'), 'clear must remain on the selected transport');
  });

  it('owns one conversation-scoped Chat SSE subscription and releases it on remount', () => {
    expect(hook.includes('chatHttp.connectChatEvents(projectId, activeConversationId'), 'Chat SSE must be scoped by project and conversation');
    expect(hook.includes('stream?.completeResync()'), 'resync must complete only after history reload');
    expect(hook.includes('stream?.();'), 'Chat SSE subscription must be released on cleanup');
    expect(hook.includes('if (!active) return;'), 'late Chat events must be ignored after cleanup');
    expect(hook.includes('doneConversationId !== null && doneConversationId !== cid'), 'terminal events must not mutate another conversation');
  });

  it('keeps optimistic state bounded by authoritative history after terminal events', () => {
    expect(hook.includes('client\n        .chatHistory({ projectId: historyProjectId, conversationId: cid })'), 'terminal event must reload authoritative history');
    expect(hook.includes("event.status === 'done' ? 'done' : event.status === 'aborted' ? 'aborted' : 'error'"), 'legacy terminal status mapping drifted');
    expect(hook.includes('resetTransientState();'), 'terminal state must reset before a later turn can start');
  });
});
