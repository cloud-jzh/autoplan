'use strict';

const assert = require('node:assert/strict');
const { describe, it } = require('node:test');
const { GoDataClient } = require('../data/goDataClient');

describe('P09 chat compatibility through GoDataClient', () => {
  it('uses Go commands for queued/interrupted chat control and receives sorted snapshot history', async () => {
    const commands = [];
    const client = clientFor(commands, { activeProjectId: 5, chat_messages: [
      { id: 2, project_id: 5, conversation_id: 8, created_at: '2026-07-11T04:45:56Z', state: 'queued' },
      { id: 1, project_id: 5, conversation_id: 8, created_at: '2026-07-11T04:45:55Z', state: 'interrupted' },
    ] });
    await client.sendChat(5, 8, 'sanitized chat', metadata('send'));
    await client.pumpChat(5, 8, metadata('pump'));
    await client.generateChatTitle(5, 8, metadata('title'));
    await client.stopChat(5, 8, metadata('stop'));
    const result = await client.clearChat(5, 8, metadata('clear'));
    assert.deepEqual(commands.map((command) => command.command), ['chat.send', 'chat.pump', 'chat.generate_title', 'chat.stop', 'chat.clear']);
    assert.deepEqual(commands[0].chat, { content: 'sanitized chat' });
    assert.equal(result.snapshot.chat_messages[0].id, 2);
    assert.equal(typeof client.db, 'undefined');
    assert.equal(typeof client.persist, 'undefined');
  });
});

function clientFor(commands, snapshot) {
  return new GoDataClient({
    baseUrl: 'http://127.0.0.1:43123',
    fetch: async (_url, init) => {
      const command = JSON.parse(init.body);
      commands.push(command);
      return { ok: true, status: 202, headers: { get: () => null }, async json() { return { data: { operation: { operation_id: `chat-${commands.length}`, type: command.command, status: 'accepted', request_id: init.headers['x-request-id'], accepted_at: '2026-07-11T04:45:56Z' }, snapshot } }; } };
    },
  });
}
function metadata(name) { return { requestId: `p09-chat-${name}`, idempotencyKey: `p09-chat-intent-${name}` }; }
