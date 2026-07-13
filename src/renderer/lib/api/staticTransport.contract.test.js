const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { join } = require('node:path');
const { describe, it } = require('node:test');

const root = process.cwd();

function source(...parts) {
  return readFileSync(join(root, ...parts), 'utf8');
}

function staticInterface(sourceText) {
  const match = sourceText.match(/export interface HttpStaticOperations \{([\s\S]*?)\n\}/);
  assert.ok(match, 'HttpStaticOperations is missing');
  return match[1];
}

describe('static HTTP transport contract', () => {
  it('exposes persistence metadata without adding runtime operations', () => {
    const contract = staticInterface(source('src', 'renderer', 'lib', 'api', 'client.ts'));
    for (const operation of [
      'listStaticScripts', 'getStaticScript', 'createStaticScript', 'updateStaticScript', 'deleteStaticScript',
      'listStaticExecutors', 'getStaticExecutor', 'createStaticExecutor', 'updateStaticExecutor', 'deleteStaticExecutor',
      'listStaticConversations', 'listStaticMessages', 'listStaticAIConfigs', 'listStaticClaudeConfigs', 'getStaticMCPConfig',
    ]) {
      assert.match(contract, new RegExp(`\\b${operation}\\b`));
    }
    assert.doesNotMatch(contract, /\b(?:run|stop|send|stream|queue|start)\w*/i);
  });

  it('keeps IPC as the default and makes static HTTP access explicit', () => {
    const transport = source('src', 'renderer', 'lib', 'api', 'transport.ts');
    assert.match(transport, /DEFAULT_AUTOPLAN_TRANSPORT = 'ipc'/);
    assert.match(transport, /function getStaticHttpOperations\(\): HttpStaticOperations \| null/);
    assert.match(transport, /transport !== HTTP_AUTOPLAN_TRANSPORT\) return null/);
  });
});
